package scheduling

import (
	"fmt"
	"testing"
	"time"
)

func TestBoundsAreSPRB2sTable(t *testing.T) {
	want := map[Priority]time.Duration{
		Interactive: 2 * time.Second,
		Batch:       5 * time.Minute,
		Maintenance: time.Hour,
		Background:  0,
	}
	for p, d := range want {
		b, err := BoundOf(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if b.Wait != d {
			t.Errorf("%s waits at most %s; SP-RB-2 says %s", p, b.Wait, d)
		}
	}
	// Exactly one row may preempt.
	n := 0
	for _, b := range Bounds() {
		if b.MayPreempt {
			n++
		}
	}
	if n != 1 || !MayPreempt(Interactive) {
		t.Fatalf("%d priorities may preempt; SP-RB-2 gives that column to interactive alone", n)
	}
}

// AB-RB-2: interactive is first while nothing is overdue.
func TestInteractiveGoesFirstBelowSaturation(t *testing.T) {
	now := at(100)
	q := []Waiting{
		{OrderID: "batch-old", Priority: Batch, Since: at(0)},
		{OrderID: "maint", Priority: Maintenance, Since: at(0)},
		{OrderID: "interactive", Priority: Interactive, Since: at(100)},
		{OrderID: "bg", Priority: Background, Since: at(0)},
	}
	// batch-old has waited 100 s of its 300 s bound: nothing is overdue.
	got := Order(q, now)
	if got[0].OrderID != "interactive" {
		t.Fatalf("the queue starts with %s; below saturation the priority column decides", got[0].OrderID)
	}
	if got[len(got)-1].OrderID != "bg" {
		t.Fatalf("background is not last: %s is", got[len(got)-1].OrderID)
	}
}

// AB-RB-3: a batch job does not starve behind interactive work.
func TestAgedBatchOvertakesFreshInteractive(t *testing.T) {
	now := at(1000)
	q := []Waiting{
		{OrderID: "interactive-1", Priority: Interactive, Since: at(999)},
		{OrderID: "interactive-2", Priority: Interactive, Since: at(1000)},
		{OrderID: "batch", Priority: Batch, Since: at(600)}, // 400 s of a 300 s bound
	}
	got := Order(q, now)
	if got[0].OrderID != "batch" {
		t.Fatalf("the overdue batch job is behind %s; SP-RB-3 says it rises", got[0].OrderID)
	}

	// And it keeps rising: an interactive job that is itself overdue by a larger factor goes
	// first again, which is the ratio doing its work rather than the priority column.
	q = append(q, Waiting{OrderID: "interactive-stuck", Priority: Interactive, Since: at(990)})
	got = Order(q, now)
	if got[0].OrderID != "interactive-stuck" {
		t.Fatalf("first is %s; 10 s over a 2 s bound is a ratio of 5, above the batch job's 1.33",
			got[0].OrderID)
	}
}

// The starvation argument of decisions/aging.md, run rather than asserted: a batch job behind an
// endless stream of interactive arrivals still leaves the queue, and it leaves it just after its
// own bound.
//
// The node serves one job a second and one interactive job arrives a second — a queue that exactly
// keeps up, which is where a plain priority order starves the batch job for ever.
func TestBatchDoesNotStarveUnderAConstantInteractiveStream(t *testing.T) {
	queue := []Waiting{{OrderID: "batch", Priority: Batch, Since: at(0)}}
	served := -1
	for second := 1; second <= 600 && served < 0; second++ {
		now := at(second)
		queue = append(queue, Waiting{
			OrderID:  fmt.Sprintf("interactive-%d", second),
			Priority: Interactive,
			Since:    now,
		})
		head := Order(queue, now)[0]
		if head.OrderID == "batch" {
			served = second
		}
		queue = without(queue, head.OrderID)
	}
	if served < 0 {
		t.Fatal("the batch job never reached the head of the queue in 600 s of interactive arrivals")
	}
	if served <= 300 {
		t.Fatalf("the batch job went first after %d s, inside its own 300 s bound", served)
	}
	if served > 330 {
		t.Fatalf("the batch job waited %d s; its bound is 300 s and key 1 puts it first the moment it is past it", served)
	}
}

func without(q []Waiting, id string) []Waiting {
	out := q[:0]
	for _, w := range q {
		if w.OrderID != id {
			out = append(out, w)
		}
	}
	return out
}

func TestShortLargeRunFirstInsideTheBound(t *testing.T) {
	now := at(10)
	q := []Waiting{
		{OrderID: "long", Priority: Batch, Since: at(0), Large: true, PredictedRuntime: time.Hour},
		{OrderID: "short", Priority: Batch, Since: at(1), Large: true, PredictedRuntime: time.Minute},
		{OrderID: "unmeasured", Priority: Batch, Since: at(2), Large: true},
	}
	got := Order(q, now)
	if got[0].OrderID != "short" {
		t.Fatalf("SP-RB-6 puts short ones first; got %s", got[0].OrderID)
	}
	if got[2].OrderID != "unmeasured" {
		t.Fatalf("a large run nobody measured went before a measured one: %s", got[2].OrderID)
	}
}

// AB-RB-4, at the level where it is decided: preemption is a freeze at a phase boundary, and there
// is no field in the answer that could carry a kill.
func TestOnlyInteractivePreemptsAndOnlyByFreezing(t *testing.T) {
	now := at(100)
	running := []Waiting{
		{OrderID: "maint", Priority: Maintenance, Since: at(0)},
		{OrderID: "batch", Priority: Batch, Since: at(0)},
	}
	p, ok := Preempt(Waiting{OrderID: "i", Priority: Interactive, Since: now}, running, now)
	if !ok {
		t.Fatal("an interactive job preempted nothing while maintenance work was running")
	}
	if p.Victim != "maint" {
		t.Fatalf("the victim is %s; the lowest priority loses the slot first (SP-RC-3)", p.Victim)
	}
	if !p.AtPhaseBoundary {
		t.Fatal("a preemption that is not at a phase boundary is an abort with a gentler name")
	}

	if _, ok := Preempt(Waiting{OrderID: "b", Priority: Batch, Since: now}, running, now); ok {
		t.Fatal("a batch job preempted; SP-RB-2 gives that right to interactive alone")
	}
	// Aging moves a job in the queue and never grants it the right to preempt
	// (decisions/aging.md).
	aged := Waiting{OrderID: "b", Priority: Batch, Since: at(0)}
	if !aged.Overdue(at(400)) {
		t.Fatal("setup: the batch job should be overdue")
	}
	if _, ok := Preempt(aged, running, at(400)); ok {
		t.Fatal("an aged batch job preempted a running pod")
	}
	// And nothing is preempted for an interactive job when only interactive work is running.
	if _, ok := Preempt(Waiting{OrderID: "i2", Priority: Interactive, Since: now},
		[]Waiting{{OrderID: "i1", Priority: Interactive, Since: at(99)}}, now); ok {
		t.Fatal("one 2-second promise was traded for another")
	}
}
