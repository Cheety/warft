// `workpod scheduler` with no subcommand: the reader running on a node, wired to the machine.

package scheduler

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Cheety/warft/platform/internal/boot"
	"github.com/Cheety/warft/platform/internal/cgroup"
	"github.com/Cheety/warft/platform/internal/scheduling"
	"github.com/Cheety/warft/platform/internal/statedb"
)

// Serve runs the scheduler of one cell until it is stopped.
//
// It is the control layer's, not the work layer's: the decisions are written to the state contract
// and the pressure is read from the node the pods are on. On a `role = all` node those are the same
// machine, which is stage 3's shape (V-01: logically constant, physically variable).
func Serve(v boot.Values, cores int) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if cores <= 0 {
		// The scheduler is the one that *makes* the allocation, so for it the machine's core count
		// is an input rather than something injected. SP-RC-5 is about the pod, which must never
		// read the host's cores — and does not: its concurrency is written into its environment by
		// the runner.
		cores = runtime.NumCPU()
	}

	slicePath, err := cgroup.UnitPath(PodsSlice)
	if err != nil {
		return fmt.Errorf("the pods slice %s: %w — SP-RC-1 reads the pressure there", PodsSlice, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool, err := statedb.Open(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	s := New(v.Cell, scheduling.NewPool(scheduling.SizesFor(cores)), SliceReader(slicePath))
	s.Logf = func(format string, a ...any) { fmt.Printf(format+"\n", a...) }
	wire(s, pool, slicePath, v.Cell)

	sizes := s.Tokens.Sizes()
	fmt.Printf("scheduler: cell %s, %d cores — %d net, %d io, %d cpu·ram tokens (SP-RB-1)\n",
		v.Cell, cores, sizes.Net, sizes.IO, sizes.CPURAM)
	fmt.Printf("scheduler: reading %s every %s, six signals, five rungs (SP-RC-1, SP-RC-3)\n",
		slicePath, scheduling.SampleInterval)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		cancel()
	}()

	err = s.Run(ctx, func(t Turn) {
		for _, act := range t.Acts {
			fmt.Printf("scheduler: %s\n", act)
		}
		for _, e := range t.Errors {
			fmt.Printf("scheduler: %s\n", e)
		}
	})
	if err == context.Canceled {
		return nil
	}
	return err
}

// wire gives the scheduler its four ways of reaching the machine. The division of who performs
// which rung is decisions/escalation-ladder.md, and this function is that table in code.
func wire(s *Scheduler, pool *pgxpool.Pool, slicePath, cell string) {
	s.Throttle = func(weight int) error { return cgroup.SetCPUWeight(slicePath, weight) }

	// SP-RC-3 freezes the lowest priority first. The order it reads is the queue's, backwards: the
	// job a fresh queue would have served last is the one that loses its slot.
	s.FreezeOne = func(ctx context.Context) (string, error) {
		running, err := statedb.Running(ctx, pool, cell)
		if err != nil || len(running) == 0 {
			return "", err
		}
		victim := running[len(running)-1]
		if err := statedb.Freeze(ctx, pool, victim.OrderID,
			"the pressure ladder reached `freeze`; the lowest priority loses its slot, not its state (SP-RC-3, SP-RB-4)"); err != nil {
			return "", err
		}
		return victim.OrderID, nil
	}

	// The dump itself belongs to the supervisor that owns the container (SP-T04-3,
	// decisions/escalation-ladder.md). What the scheduler owes it is the demand, in the trail.
	s.Checkpoint = func(ctx context.Context, order string) error {
		return statedb.RecordEscalation(ctx, pool, cell, string(scheduling.RungCheckpoint),
			[]string{"order:" + order})
	}

	s.Escalate = func(ctx context.Context, signals []scheduling.Signal) error {
		names := make([]string, 0, len(signals))
		for _, sig := range signals {
			names = append(names, string(sig))
		}
		return statedb.RecordEscalation(ctx, pool, cell, string(scheduling.RungEscalate), names)
	}
}
