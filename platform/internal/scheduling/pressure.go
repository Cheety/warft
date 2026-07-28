// R-C: the six signals of SP-RC-2 with OP-6's hysteresis, and the five rungs of SP-RC-3.
//
// What is read is pressure, not utilization (SP-RC-1). Nothing in this file knows how many cores
// are busy, and that is the point: 100 % CPU is a healthy machine, and 40 % CPU pressure is a
// machine leaving work undone.

package scheduling

import (
	"fmt"
	"sort"
	"time"
)

// SampleInterval is SP-RC-1's "every two seconds". Every hold time in OP-6 is counted in samples of
// this length, so a reader that sampled at another rate would be measuring against numbers derived
// for this one.
const SampleInterval = 2 * time.Second

// Sample is one reading of the pods slice: SP-RC-2's six signals as the kernel reports them.
//
// The two counters are cumulative — `memory.events high` and `pgmajfault` only ever rise — and the
// watcher turns them into a change and a rate, because "rising" and "rising fast" are what the
// panel reacts to.
type Sample struct {
	At time.Time

	MemorySomeAvg10 float64 // memory.pressure, some, avg10, in percent
	MemoryFullAvg10 float64 // memory.pressure, full, avg10
	IOFullAvg10     float64 // io.pressure, full, avg10
	CPUSomeAvg60    float64 // cpu.pressure, some, avg60

	MemoryEventsHigh uint64 // memory.events, the `high` counter
	PgMajFault       uint64 // memory.stat, pgmajfault

	// CPUTokensFree is the "with free tokens" of SP-RC-2's fourth row. Pressure on the CPU while
	// the scheduler is holding tokens back is the scheduler working; pressure while tokens are
	// free is something computing beside the point.
	CPUTokensFree bool
}

// Signal is one of SP-RC-2's six rows, named by the file and field it is read from.
type Signal string

const (
	MemoryTight       Signal = "memory.some.avg10"  // getting tight — admit no new pods
	MemoryStalled     Signal = "memory.full.avg10"  // everything is stalled — freeze immediately
	IOBottleneck      Signal = "io.full.avg10"      // the disk is the bottleneck — I/O tokens to 1
	CPUBesideThePoint Signal = "cpu.some.avg60"     // something is computing beside the point
	Misclassified     Signal = "memory.events.high" // the pod is classified wrongly — raise the class
	Thrashing         Signal = "pgmajfault"         // swap thrashing is beginning — the hardest rung
)

// Rung is one step of SP-RC-3's escalation ladder. There are five, and none of them is an abort:
// the ladder ends at the captain, not at a kill. AB-RC-3 is that sentence made checkable — a type
// with five values cannot grow a sixth at runtime.
type Rung string

const (
	RungThrottle   Rung = "throttle"   // lower cpu.weight
	RungBlock      Rung = "block"      // no admission
	RungFreeze     Rung = "freeze"     // lowest priority first
	RungCheckpoint Rung = "checkpoint" // CRIU dump
	RungEscalate   Rung = "escalate"   // to the captain
)

// ladder is SP-RC-3 in its order. The order is the requirement: a rung is never skipped, so a
// signal demanding the top still runs the four below it on the way up.
var ladder = []Rung{RungThrottle, RungBlock, RungFreeze, RungCheckpoint, RungEscalate}

// Ladder is the five rungs, in order.
func Ladder() []Rung { return append([]Rung(nil), ladder...) }

// rungIndex is 1-based so that 0 can mean "on the ground, nothing escalated".
func rungIndex(r Rung) int {
	for i, l := range ladder {
		if l == r {
			return i + 1
		}
	}
	return 0
}

// Threshold is one row of SP-RC-2 with OP-6's four numbers around it: where it acts, how many
// samples it takes to act, where it releases, and how long it must stay there.
type Threshold struct {
	Signal Signal
	// Enter and Release are in the unit of the signal — percent for the four pressure rows, faults
	// per second for pgmajfault. Misclassified has neither: it is a counter that either rose or
	// did not.
	Enter        float64
	EnterSamples int
	Release      float64
	ReleaseHold  time.Duration

	// Rung is the rung this signal demands while it is active. Misclassified demands none: OP-6
	// calls it a reclassification and not a state, and raising a pod's class is not a step of the
	// ladder.
	Rung     Rung
	Reaction string
}

// thresholds is SP-RC-2's table with OP-6's ruling applied. Both are quoted rather than derived:
// the six enter values are the panel's, the four numbers around each are the open point's.
var thresholds = []Threshold{
	{MemoryTight, 10, 2, 5, 30 * time.Second, RungBlock,
		"getting tight — admit no new pods"},
	// The one signal that acts on a single sample. SP-RC-2 writes "freeze immediately, do not
	// wait" next to it, and OP-6 keeps that exception deliberately: a hold time here would be a
	// hold time on the reaction the panel wrote "do not wait" beside.
	{MemoryStalled, 5, 1, 2.5, 30 * time.Second, RungFreeze,
		"everything is stalled — freeze immediately, do not wait"},
	{IOBottleneck, 20, 2, 10, 30 * time.Second, RungThrottle,
		"the disk is the bottleneck — I/O tokens to 1, serialize installations"},
	// 60 s of release hold rather than 30, because avg60 is a six-times-longer window and OP-6
	// scales the argument with it.
	{CPUBesideThePoint, 60, 2, 30, 60 * time.Second, RungThrottle,
		"something is computing beside the point — look for zram, the proxy or an outlier"},
	{Misclassified, 0, 2, 0, 0, "",
		"the pod is classified wrongly — raise the class, do not give it more time"},
	{Thrashing, 100, 2, 10, 30 * time.Second, RungEscalate,
		"swap thrashing is beginning — the hardest rung, immediately"},
}

// Thresholds is SP-RC-2 and OP-6 as the program carries them.
func Thresholds() []Threshold { return append([]Threshold(nil), thresholds...) }

// ThresholdOf is one row.
func ThresholdOf(s Signal) (Threshold, error) {
	for _, t := range thresholds {
		if t.Signal == s {
			return t, nil
		}
	}
	return Threshold{}, fmt.Errorf("SP-RC-2 has six signals and %q is not one of them", s)
}

// Watcher turns a stream of samples into the set of signals that are actually acting.
//
// It is the whole of OP-6: a threshold crossed on one sample is a spike and not an event (except
// the one the panel says not to wait for), and a threshold released the moment the number dips is
// the flapping the open point exists to prevent. The watcher holds the two edges apart.
type Watcher struct {
	states map[Signal]*signalState
	prev   *Sample
}

type signalState struct {
	above  int       // consecutive samples at or above the enter edge
	active bool      // the signal is acting
	below  time.Time // when it first fell under the release edge; zero while it is above
}

// NewWatcher is a watcher with every signal at rest.
func NewWatcher() *Watcher {
	w := &Watcher{states: map[Signal]*signalState{}}
	for _, t := range thresholds {
		w.states[t.Signal] = &signalState{}
	}
	return w
}

// Observe takes one sample and returns the signals acting after it, in the order SP-RC-2 lists
// them. It is the only way the watcher's state changes.
func (w *Watcher) Observe(s Sample) []Signal {
	for _, t := range thresholds {
		value, comparable := w.value(t, s)
		st := w.states[t.Signal]
		if !comparable {
			// The first sample of a counter signal has nothing to compare against. That is not
			// "no pressure" — it is no reading, and a reading nobody has is not an event.
			continue
		}
		w.step(t, st, value, s.At)
	}
	w.prev = &s
	return w.Active()
}

// value is the number a threshold is compared against. Four signals are levels the kernel already
// averaged; two are counters, and what SP-RC-2 reacts to there is their change.
func (w *Watcher) value(t Threshold, s Sample) (float64, bool) {
	switch t.Signal {
	case MemoryTight:
		return s.MemorySomeAvg10, true
	case MemoryStalled:
		return s.MemoryFullAvg10, true
	case IOBottleneck:
		return s.IOFullAvg10, true
	case CPUBesideThePoint:
		// "with free tokens" is part of the condition, not commentary on it: pressure on the CPU
		// while every token is out is the machine doing the work it was given.
		if !s.CPUTokensFree {
			return 0, true
		}
		return s.CPUSomeAvg60, true
	case Misclassified:
		if w.prev == nil {
			return 0, false
		}
		// "rising" — any increase counts, and the enter edge is 0, so two consecutive rises act.
		if s.MemoryEventsHigh > w.prev.MemoryEventsHigh {
			return 1, true
		}
		return 0, true
	case Thrashing:
		if w.prev == nil {
			return 0, false
		}
		elapsed := s.At.Sub(w.prev.At).Seconds()
		if elapsed <= 0 {
			return 0, false
		}
		if s.PgMajFault < w.prev.PgMajFault {
			return 0, true // the counter was reset with the cgroup; that is not a fall in faults
		}
		return float64(s.PgMajFault-w.prev.PgMajFault) / elapsed, true
	}
	return 0, false
}

// step is OP-6's state machine for one signal: enter on N consecutive samples above the edge,
// release only after the value has stayed under half the edge for the hold time.
func (w *Watcher) step(t Threshold, st *signalState, value float64, at time.Time) {
	if value > t.Enter {
		st.above++
		st.below = time.Time{}
		if st.above >= t.EnterSamples {
			st.active = true
		}
		return
	}
	st.above = 0
	if !st.active {
		return
	}
	if t.ReleaseHold == 0 {
		// Misclassified is a reclassification and not a state: one attempt at the higher class,
		// and it is over. It clears on the first sample that does not rise.
		st.active = false
		return
	}
	if value >= t.Release {
		// Between the release edge and the enter edge: the signal neither acts again nor clears.
		// That band is the hysteresis.
		st.below = time.Time{}
		return
	}
	if st.below.IsZero() {
		st.below = at
		return
	}
	if at.Sub(st.below) >= t.ReleaseHold {
		st.active = false
		st.below = time.Time{}
	}
}

// Active is the signals acting now, in SP-RC-2's order.
func (w *Watcher) Active() []Signal {
	var out []Signal
	for _, t := range thresholds {
		if w.states[t.Signal].active {
			out = append(out, t.Signal)
		}
	}
	return out
}

// Reaction is what SP-RC-2 writes beside a signal.
func Reaction(s Signal) string {
	t, err := ThresholdOf(s)
	if err != nil {
		return ""
	}
	return t.Reaction
}

// Demanded is the highest rung the acting signals demand, or "" when they demand none.
func Demanded(signals []Signal) Rung {
	highest := 0
	for _, s := range signals {
		t, err := ThresholdOf(s)
		if err != nil {
			continue
		}
		if i := rungIndex(t.Rung); i > highest {
			highest = i
		}
	}
	if highest == 0 {
		return ""
	}
	return ladder[highest-1]
}

// Escalation is where on the ladder the node currently stands, and the only thing that moves it.
//
// It climbs one rung at a time and reports every rung it entered, so a signal that demands the
// hardest rung immediately still runs the four below it in order — SP-RC-3's ladder is an order,
// not a menu, and AB-RC-3 checks exactly that. It descends one rung at a time as well: a machine
// that fell off the ladder the moment pressure eased would unfreeze into the same pressure.
type Escalation struct {
	at int // 0 = on the ground
}

// Rung is where the escalation stands, or "" on the ground.
func (e *Escalation) Rung() Rung {
	if e.at == 0 {
		return ""
	}
	return ladder[e.at-1]
}

// Climb runs up to the demanded rung and returns the rungs newly entered, in order. Climbing to a
// rung at or below where it already stands enters nothing.
func (e *Escalation) Climb(target Rung) []Rung {
	want := rungIndex(target)
	var entered []Rung
	for e.at < want {
		e.at++
		entered = append(entered, ladder[e.at-1])
	}
	return entered
}

// Descend steps one rung down and returns the rung left behind, or "" when it was already on the
// ground.
func (e *Escalation) Descend() Rung {
	if e.at == 0 {
		return ""
	}
	left := ladder[e.at-1]
	e.at--
	return left
}

// Step is one turn of the ladder against one set of acting signals: climb to what they demand, or
// step down one rung when they demand less than where it stands. It returns the rungs newly
// entered — descending enters none, and the caller reads Rung() for where the node now stands.
func (e *Escalation) Step(signals []Signal) []Rung {
	target := rungIndex(Demanded(signals))
	switch {
	case target > e.at:
		return e.Climb(ladder[target-1])
	case target < e.at:
		e.Descend()
	}
	return nil
}

// Admits reports whether the ladder currently permits admitting new pods. From `block` upwards it
// does not — which is SP-RC-2's reaction to a tight machine and SP-RC-3's second rung, and it is
// the whole of AB-RC-1: the answer comes from pressure, never from how busy the cores look.
func (e *Escalation) Admits() bool {
	return e.at < rungIndex(RungBlock)
}

// SignalNames is the six, sorted, for a report that wants them stable.
func SignalNames() []string {
	out := make([]string, 0, len(thresholds))
	for _, t := range thresholds {
		out = append(out, string(t.Signal))
	}
	sort.Strings(out)
	return out
}
