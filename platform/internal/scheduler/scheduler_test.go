package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/Cheety/warft/platform/internal/runner"
	"github.com/Cheety/warft/platform/internal/scheduling"
)

// The join `internal/scheduling` cannot make itself: it is a base module and imports nothing, so
// the table of decisions/phase-tokens.md is held against T-05's spine here, in the one role that
// sees both. A spine that grew an eighth phase fails this test rather than scheduling a phase the
// platform has no token for.
func TestEveryPhaseOfTheSpineHasARuledToken(t *testing.T) {
	tokens := scheduling.RuledTokens()
	for _, p := range runner.Spine() {
		if _, err := tokens.Of(scheduling.Phase(p)); err != nil {
			t.Errorf("T-05's phase %q has no token: %v", p, err)
		}
	}
	if len(tokens.Rows()) != len(runner.Spine()) {
		t.Errorf("the ruling has %d rows and the spine has %d phases",
			len(tokens.Rows()), len(runner.Spine()))
	}
}

func sample(sec int, memSome, memFull float64) scheduling.Sample {
	return scheduling.Sample{At: time.Unix(1750000000+int64(sec), 0).UTC(),
		MemorySomeAvg10: memSome, MemoryFullAvg10: memFull}
}

// AB-RC-3, end to end through the role: the five rungs run in order and nothing is aborted.
func TestLadderClimbsInOrderAndFreezesRatherThanAborts(t *testing.T) {
	var frozen []string
	var acts []string
	s := New("cell-1", scheduling.NewPool(scheduling.SizesFor(4)), nil)
	s.Throttle = func(w int) error { acts = append(acts, "throttle"); return nil }
	s.FreezeOne = func(context.Context) (string, error) {
		frozen = append(frozen, "order-low")
		return "order-low", nil
	}
	s.Checkpoint = func(_ context.Context, pod string) error { acts = append(acts, "checkpoint:"+pod); return nil }
	s.Escalate = func(context.Context, []scheduling.Signal) error { acts = append(acts, "escalate"); return nil }

	// Thrashing demands the hardest rung immediately; the four below it still run, in order.
	first := scheduling.Sample{At: time.Unix(1750000000, 0).UTC(), PgMajFault: 0}
	second := scheduling.Sample{At: time.Unix(1750000002, 0).UTC(), PgMajFault: 400}
	third := scheduling.Sample{At: time.Unix(1750000004, 0).UTC(), PgMajFault: 800}

	for _, sm := range []scheduling.Sample{first, second, third} {
		next := sm
		s.Read = func() scheduling.Sample { return next }
		turn := s.Tick(context.Background())
		if len(turn.Entered) == 0 {
			continue
		}
		want := []scheduling.Rung{scheduling.RungThrottle, scheduling.RungBlock,
			scheduling.RungFreeze, scheduling.RungCheckpoint, scheduling.RungEscalate}
		if len(turn.Entered) != 5 {
			t.Fatalf("entered %v; the ladder is climbed one rung at a time, all five", turn.Entered)
		}
		for i, r := range want {
			if turn.Entered[i] != r {
				t.Fatalf("rung %d was %q, expected %q", i+1, turn.Entered[i], r)
			}
		}
	}
	if len(frozen) != 1 {
		t.Fatalf("%d pods frozen; the freeze rung freezes the lowest priority", len(frozen))
	}
	if s.Admits() {
		t.Fatal("the node still admits at the top of the ladder")
	}
	if !s.Tokens.Blocked() {
		t.Fatal("the token pool still grants tokens above the block rung")
	}
}

// AB-RC-1: a machine at full utilization and no pressure keeps admitting; the same machine under
// memory pressure stops. Nothing in a sample says how busy the cores are.
func TestAdmissionFollowsPressureAndNotUtilization(t *testing.T) {
	s := New("cell-1", scheduling.NewPool(scheduling.SizesFor(4)), nil)
	s.Throttle = func(int) error { return nil }
	s.FreezeOne = func(context.Context) (string, error) { return "", nil }

	for sec := 0; sec < 10; sec += 2 {
		next := sample(sec, 0, 0)
		s.Read = func() scheduling.Sample { return next }
		if !s.Tick(context.Background()).Admits {
			t.Fatal("a machine under no pressure stopped admitting")
		}
	}
	for _, sec := range []int{10, 12} {
		next := sample(sec, 12, 0)
		s.Read = func() scheduling.Sample { return next }
		s.Tick(context.Background())
	}
	if s.Admits() {
		t.Fatal("memory pressure above 10 % on two samples did not stop admission")
	}
}

// The pool's own state feeds SP-RC-2's fourth row: pressure on the CPU while every token is out is
// the machine working, and the signal must not act on it.
func TestCPUSignalAsksThePoolWhetherTokensAreFree(t *testing.T) {
	pool := scheduling.NewPool(scheduling.Sizes{Net: 8, IO: 1, CPURAM: 1})
	if g, _ := pool.Enter("pod-a", "check"); !g.Granted {
		t.Fatal("setup")
	}
	s := New("cell-1", pool, nil)
	s.Throttle = func(int) error { return nil }
	for sec := 0; sec < 10; sec += 2 {
		next := scheduling.Sample{At: time.Unix(1750000000+int64(sec), 0).UTC(), CPUSomeAvg60: 80}
		s.Read = func() scheduling.Sample { return next }
		turn := s.Tick(context.Background())
		if len(turn.Signals) != 0 {
			t.Fatalf("CPU pressure acted while the only cpu·ram token was out: %v", turn.Signals)
		}
	}
}
