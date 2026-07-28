// The four priorities of SP-RB-2 and the aging rule of decisions/aging.md.

package scheduling

import (
	"fmt"
	"sort"
	"time"
)

// Priority is one of SP-RB-2's four. The words are the state contract's enum values, so a priority
// crosses the database and the wire unchanged.
type Priority string

const (
	Interactive Priority = "interactive"
	Batch       Priority = "batch"
	Maintenance Priority = "maintenance"
	Background  Priority = "background"
)

// Bound is one row of SP-RB-2's table: what a priority waits at most, and whether it may preempt.
type Bound struct {
	Priority Priority
	// Wait is the "waits at most" column. Zero means unbounded — SP-RB-2 writes that for
	// `background`, and by decisions/aging.md unbounded means it never becomes overdue and
	// therefore never rises.
	Wait       time.Duration
	MayPreempt bool
	// Rank is the order of the column itself, and it decides between jobs that are all inside
	// their bound.
	Rank int
}

// Unbounded reports SP-RB-2's fourth row: a priority with no promise attached.
func (b Bound) Unbounded() bool { return b.Wait == 0 }

// bounds is SP-RB-2's table, verbatim. It is not read from a file because it is not a ruling of
// this repository — it is a table of the specification, and acceptance/rb-scheduler.sh holds these
// four rows against §12.2 of 01-specification.md.
var bounds = []Bound{
	{Interactive, 2 * time.Second, true, 0},
	{Batch, 5 * time.Minute, false, 1},
	{Maintenance, time.Hour, false, 2},
	{Background, 0, false, 3},
}

// Bounds is SP-RB-2's table in the order the column stands in.
func Bounds() []Bound { return append([]Bound(nil), bounds...) }

// BoundOf is one row. An unknown priority is an error: a job whose bound was guessed is a job with
// a promise nobody made.
func BoundOf(p Priority) (Bound, error) {
	for _, b := range bounds {
		if b.Priority == p {
			return b, nil
		}
	}
	return Bound{}, fmt.Errorf("SP-RB-2 knows four priorities and %q is not one of them", p)
}

// MayPreempt is SP-RB-2's third column. Aging never changes it (decisions/aging.md): an aged batch
// job is next, not interactive.
func MayPreempt(p Priority) bool {
	b, err := BoundOf(p)
	return err == nil && b.MayPreempt
}

// Waiting is one job in the queue as the ordering sees it.
type Waiting struct {
	OrderID  string
	Priority Priority
	// Since is when the job entered the queue. Waiting is measured from there and not from when
	// the envelope arrived: what SP-RB-2 promises is a queue time.
	Since time.Time

	// Large marks a job that will need exclusive operation (SP-RB-5). It is set from a prediction
	// of three runs, and it only ever decides between two jobs that are otherwise equal.
	Large bool
	// PredictedRuntime is SP-RB-6's "short ones first", and it is consulted only between two large
	// runs inside their bound. Zero means no prediction, which sorts last among large runs — a job
	// nobody has measured is not the one to start when the alternative is measured and short.
	PredictedRuntime time.Duration
}

// Waited is how long this job has been in the queue at `now`.
func (w Waiting) Waited(now time.Time) time.Duration {
	d := now.Sub(w.Since)
	if d < 0 {
		return 0
	}
	return d
}

// Overdue is decisions/aging.md's first key: a job past its own bound. `background` is never
// overdue, because SP-RB-2 gives it no bound to be past.
func (w Waiting) Overdue(now time.Time) bool {
	b, err := BoundOf(w.Priority)
	if err != nil || b.Unbounded() {
		return false
	}
	return w.Waited(now) > b.Wait
}

// Ratio is `wait / bound` — the second key, and the reason an interactive job one second past two
// seconds does not outrank a batch job five minutes past five minutes. An unbounded priority has no
// ratio and reports 0.
func (w Waiting) Ratio(now time.Time) float64 {
	b, err := BoundOf(w.Priority)
	if err != nil || b.Unbounded() {
		return 0
	}
	return float64(w.Waited(now)) / float64(b.Wait)
}

// Order sorts the queue by decisions/aging.md's three keys: overdue before not overdue, then the
// largest overdue ratio, then priority and arrival. It does not modify its argument.
//
// The same three keys are the ORDER BY of the SKIP LOCKED query in `internal/statedb`, and
// acceptance/rb-scheduler.sh runs one queue through both to show they agree. Two orderings of one
// queue would be one ordering and one bug.
func Order(ws []Waiting, now time.Time) []Waiting {
	out := append([]Waiting(nil), ws...)
	sort.SliceStable(out, func(i, j int) bool { return less(out[i], out[j], now) })
	return out
}

func less(a, b Waiting, now time.Time) bool {
	// Key 1: overdue first, whatever the two priorities are.
	ao, bo := a.Overdue(now), b.Overdue(now)
	if ao != bo {
		return ao
	}
	// Key 2: among overdue jobs, the most overdue relative to its own promise.
	if ao {
		ar, br := a.Ratio(now), b.Ratio(now)
		if ar != br {
			return ar > br
		}
	}
	// Key 3: inside the bound, the priority column decides.
	ab, aerr := BoundOf(a.Priority)
	bb, berr := BoundOf(b.Priority)
	switch {
	case aerr != nil && berr == nil:
		return false // an unknown priority sorts last rather than crashing a queue
	case aerr == nil && berr != nil:
		return true
	case aerr == nil && berr == nil && ab.Rank != bb.Rank:
		return ab.Rank < bb.Rank
	}
	// SP-RB-6, inside key 3: between two large runs, the shorter predicted runtime first. A large
	// run without a prediction sorts behind one with a prediction, because "short ones first" needs
	// a number and an unmeasured job has none.
	if a.Large && b.Large && a.PredictedRuntime != b.PredictedRuntime {
		switch {
		case a.PredictedRuntime == 0:
			return false
		case b.PredictedRuntime == 0:
			return true
		}
		return a.PredictedRuntime < b.PredictedRuntime
	}
	// Key 4: arrival, everywhere, so fairness between priorities never becomes unfairness inside
	// one.
	if !a.Since.Equal(b.Since) {
		return a.Since.Before(b.Since)
	}
	return a.OrderID < b.OrderID
}

// Preemption is what the scheduler does to make room for an interactive job: it names the pod to
// freeze, and it is a freeze in the type as well as in the prose — there is no field here that
// could carry a kill (SP-RB-4).
type Preemption struct {
	// Victim is the running job that loses its slot. It keeps its state: `running -> frozen` is a
	// transition of the state contract, and `frozen -> running` is the way back.
	Victim string
	// For is the job the slot is made for.
	For string
	// AtPhaseBoundary is SP-RB-5's "at the next phase boundary" and AP-3.7's "preemption = freezing
	// at the phase boundary": the victim is frozen when it finishes the phase it is in, never in the
	// middle of one. A freeze inside a phase would lose the phase's work, which is the abort this
	// requirement exists to forbid.
	AtPhaseBoundary bool
	Reason          string
}

// Preempt decides who is frozen for a waiting job, or that nobody is.
//
// Only a priority whose row says "may preempt" preempts, and by SP-RB-2 that is `interactive`
// alone. The victim is the lowest-priority running job — the one that would be last in a fresh
// queue — and never one of the waiting job's own priority: freezing an interactive job for another
// interactive job would trade one 2-second promise for another.
//
// `running` is given in the order the scheduler holds it; ordering it here would need the arrival
// times, and the caller has them.
func Preempt(want Waiting, running []Waiting, now time.Time) (Preemption, bool) {
	if !MayPreempt(want.Priority) {
		return Preemption{}, false
	}
	wantBound, err := BoundOf(want.Priority)
	if err != nil {
		return Preemption{}, false
	}

	// The victim is the last job of a fresh queue: reverse the ordering and take the first that is
	// worth freezing.
	ordered := Order(running, now)
	for i := len(ordered) - 1; i >= 0; i-- {
		v := ordered[i]
		b, err := BoundOf(v.Priority)
		if err != nil || b.Rank <= wantBound.Rank {
			continue
		}
		return Preemption{
			Victim: v.OrderID, For: want.OrderID, AtPhaseBoundary: true,
			Reason: fmt.Sprintf("%s is %s and waits at most %s; %s is %s and loses the slot, not its state (SP-RB-2, SP-RB-4)",
				want.OrderID, want.Priority, wantBound.Wait, v.OrderID, v.Priority),
		}, true
	}
	return Preemption{}, false
}
