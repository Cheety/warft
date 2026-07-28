// Package scheduler is the role: the PSI reader that runs every two seconds, the ladder it climbs,
// and the queue rounds between them (R-B, R-C).
//
// The rules are not here. Which token a phase holds, how a waiting job rises, what the six signals
// mean and what the five rungs are all live in `internal/scheduling`, which imports nothing and can
// therefore be read without a node. What lives here is everything that touches something: the
// cgroup files of SP-RC-1, the state contract's queue, and the four rungs that act on the machine.
//
// The division between deciding and performing is decisions/escalation-ladder.md, and it is why the
// four actions below are fields rather than calls: on a node they are cgroup writes and database
// transactions, and in a check they are whatever the check needs to observe. A scheduler that could
// only be exercised by generating real memory pressure would be a scheduler nobody tests until the
// day it matters.
package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/Cheety/warft/platform/internal/cgroup"
	"github.com/Cheety/warft/platform/internal/scheduling"
)

// PodsSlice is SP-RC-1's "pods slice": the work layer's slice on every node, and the one the
// pressure is read from. It is the unit `internal/worker` registers under, named again here because
// a role may not import another role — the two agree on a unit name the image creates, not on a
// Go constant one of them owns.
const PodsSlice = "workpod-work.slice"

// throttledWeight is the `cpu.weight` the pods slice drops to on the first rung. systemd's default
// is 100; a fifth of it leaves the pods running and puts the control plane, the proxies and access
// in front of them under contention — which is SP-RC-4's reservation expressed as a weight rather
// than as a reservation (SP-RA-3: weights, never ceilings).
const throttledWeight = 20

// Scheduler is one node's scheduler: the token pools, the pressure watcher, the ladder, and the
// four ways it reaches the machine.
type Scheduler struct {
	Cell   string
	Tokens *scheduling.Pool

	// Read is one sample of the pods slice. On a node this is cgroup.Signals of the slice; in a
	// check it is a replayed reading, which is the only way to observe a 30-second release hold
	// without waiting 30 seconds.
	Read func() scheduling.Sample

	// The four rungs that act outside this process. Each is optional: a scheduler without a
	// Freeze cannot freeze, and says so in the turn rather than pretending it did.
	Throttle   func(weight int) error
	FreezeOne  func(ctx context.Context) (string, error)
	Checkpoint func(ctx context.Context, pod string) error
	Escalate   func(ctx context.Context, signals []scheduling.Signal) error

	Logf func(format string, a ...any)

	watcher *scheduling.Watcher
	esc     scheduling.Escalation
	frozen  []string
}

// New is a scheduler with its watcher at rest and its ladder on the ground.
func New(cell string, tokens *scheduling.Pool, read func() scheduling.Sample) *Scheduler {
	return &Scheduler{
		Cell:    cell,
		Tokens:  tokens,
		Read:    read,
		watcher: scheduling.NewWatcher(),
	}
}

// Turn is what one tick of the reader did: what it read, what acted, which rungs it entered, and
// whether the node still admits.
type Turn struct {
	At      time.Time           `json:"at"`
	Sample  scheduling.Sample   `json:"sample"`
	Signals []scheduling.Signal `json:"signals"`
	Entered []scheduling.Rung   `json:"entered,omitempty"`
	Rung    scheduling.Rung     `json:"rung,omitempty"`
	Admits  bool                `json:"admits"`
	Acts    []string            `json:"acts,omitempty"`
	Frozen  []string            `json:"frozen,omitempty"`
	Errors  []string            `json:"errors,omitempty"`
}

// Tick is one turn of SP-RC-1's two-second reader: read the six signals, let the hysteresis of OP-6
// decide which of them are acting, move the ladder by at most one rung per rung, and perform what
// the new rungs demand.
//
// Every rung entered is performed in the order it was entered. That is AB-RC-3: a signal demanding
// the hardest rung immediately still runs the four below it, and none of the five aborts anything.
func (s *Scheduler) Tick(ctx context.Context) Turn {
	sample := s.Read()
	// SP-RC-2's fourth row is conditional on free tokens, and only the pool knows: pressure on the
	// CPU while every token is out is the machine doing the work it was given.
	sample.CPUTokensFree = s.Tokens.Free(scheduling.ClassCPURAM) > 0

	signals := s.watcher.Observe(sample)
	entered := s.esc.Step(signals)

	t := Turn{At: sample.At, Sample: sample, Signals: signals, Entered: entered,
		Rung: s.esc.Rung(), Admits: s.esc.Admits()}

	// The io reaction is SP-RC-2's own and is not a rung: the pool is cut to 1 while the disk is
	// the bottleneck, and restored when it is not.
	if contains(signals, scheduling.IOBottleneck) {
		s.Tokens.SetIO(1)
		t.Acts = append(t.Acts, "io tokens to 1 — the disk is the bottleneck (SP-RC-2)")
	}

	for _, rung := range entered {
		s.perform(ctx, rung, signals, &t)
	}
	// Blocking is a state and not an act: the pool has to stay blocked while the ladder stands at
	// or above the rung, and has to lift when it comes down.
	s.Tokens.Block(!s.esc.Admits())
	t.Admits = s.esc.Admits()
	t.Frozen = append([]string(nil), s.frozen...)

	if s.Logf != nil && (len(entered) > 0 || len(signals) > 0) {
		s.Logf("scheduler: %d signal(s) acting, ladder on %q, admits=%v", len(signals), s.esc.Rung(), t.Admits)
	}
	return t
}

// perform is one rung, done. It never returns an error to the caller: a rung that could not be
// performed is recorded in the turn, because the ladder has to keep climbing — a node that stopped
// escalating because one rung failed would be stuck on the rung below the one it needs.
func (s *Scheduler) perform(ctx context.Context, rung scheduling.Rung, signals []scheduling.Signal, t *Turn) {
	switch rung {
	case scheduling.RungThrottle:
		if s.Throttle == nil {
			t.Errors = append(t.Errors, "throttle: this scheduler has no slice to lower cpu.weight on")
			return
		}
		if err := s.Throttle(throttledWeight); err != nil {
			t.Errors = append(t.Errors, "throttle: "+err.Error())
			return
		}
		t.Acts = append(t.Acts, fmt.Sprintf("throttle — cpu.weight of the pods slice to %d (SP-RA-3)", throttledWeight))

	case scheduling.RungBlock:
		// The pool is set from the ladder's own state after every tick, so this rung is entered
		// rather than executed. Recording it is what makes the order observable.
		t.Acts = append(t.Acts, "block — no admission while the ladder stands here (SP-RC-3)")

	case scheduling.RungFreeze:
		if s.FreezeOne == nil {
			t.Errors = append(t.Errors, "freeze: this scheduler has no pod to freeze")
			return
		}
		pod, err := s.FreezeOne(ctx)
		if err != nil {
			t.Errors = append(t.Errors, "freeze: "+err.Error())
			return
		}
		if pod == "" {
			t.Acts = append(t.Acts, "freeze — nothing was running to freeze")
			return
		}
		s.frozen = append(s.frozen, pod)
		t.Acts = append(t.Acts, fmt.Sprintf("freeze — %s lost its slot, not its state (SP-RB-4)", pod))

	case scheduling.RungCheckpoint:
		if s.Checkpoint == nil || len(s.frozen) == 0 {
			t.Acts = append(t.Acts, "checkpoint — nothing frozen is due for a dump")
			return
		}
		for _, pod := range s.frozen {
			if err := s.Checkpoint(ctx, pod); err != nil {
				t.Errors = append(t.Errors, "checkpoint "+pod+": "+err.Error())
				continue
			}
			t.Acts = append(t.Acts, fmt.Sprintf("checkpoint — %s is due for a CRIU dump (SP-T04-3)", pod))
		}

	case scheduling.RungEscalate:
		if s.Escalate == nil {
			t.Errors = append(t.Errors, "escalate: this scheduler has nowhere to escalate to")
			return
		}
		if err := s.Escalate(ctx, signals); err != nil {
			t.Errors = append(t.Errors, "escalate: "+err.Error())
			return
		}
		t.Acts = append(t.Acts, "escalate — the duty officer is told which signals demanded it (E-08)")
	}
}

// Thawed drops a pod from the list of what this scheduler froze. The pod that comes back is the
// worker's to restart; what the scheduler owns is the record of who it took the slot from.
func (s *Scheduler) Thawed(pod string) {
	out := s.frozen[:0]
	for _, p := range s.frozen {
		if p != pod {
			out = append(out, p)
		}
	}
	s.frozen = out
}

// Rung is where the ladder stands, or "" on the ground.
func (s *Scheduler) Rung() scheduling.Rung { return s.esc.Rung() }

// Admits is whether new pods may be admitted. It is the pressure's answer and never the
// utilization's (SP-RC-1, AB-RC-1).
func (s *Scheduler) Admits() bool { return s.esc.Admits() }

// Run is the reader of SP-RC-1: every two seconds, until the context ends.
func (s *Scheduler) Run(ctx context.Context, tick func(Turn)) error {
	t := time.NewTicker(scheduling.SampleInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			turn := s.Tick(ctx)
			if tick != nil {
				tick(turn)
			}
		}
	}
}

// SliceReader is the reader a node uses: the six signals of the pods slice, sampled through the
// cgroup files SP-RC-1 names.
func SliceReader(path string) func() scheduling.Sample {
	return func() scheduling.Sample {
		r := cgroup.Signals(path)
		return scheduling.Sample{
			At:               time.Now(),
			MemorySomeAvg10:  r.MemorySomeAvg10,
			MemoryFullAvg10:  r.MemoryFullAvg10,
			IOFullAvg10:      r.IOFullAvg10,
			CPUSomeAvg60:     r.CPUSomeAvg60,
			MemoryEventsHigh: r.MemoryEventsHigh,
			PgMajFault:       r.PgMajFault,
		}
	}
}

func contains(sigs []scheduling.Signal, want scheduling.Signal) bool {
	for _, s := range sigs {
		if s == want {
			return true
		}
	}
	return false
}
