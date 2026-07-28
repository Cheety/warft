package scheduling

import (
	"testing"
	"time"
)

func TestThresholdsAreSPRC2WithOP6(t *testing.T) {
	if len(Thresholds()) != 6 {
		t.Fatalf("SP-RC-2 has six signals, the program carries %d", len(Thresholds()))
	}
	// The one exception OP-6 rules deliberately: `memory full avg10 > 5 %` acts on a single
	// sample, because the panel wrote "do not wait" beside it.
	stalled, err := ThresholdOf(MemoryStalled)
	if err != nil {
		t.Fatal(err)
	}
	if stalled.EnterSamples != 1 {
		t.Fatalf("memory.full enters on %d samples; OP-6 rules one", stalled.EnterSamples)
	}
	for _, th := range Thresholds() {
		if th.Signal == Misclassified {
			continue // a reclassification, not a state: OP-6 gives it neither edge
		}
		if th.Signal != MemoryStalled && th.EnterSamples != 2 {
			t.Errorf("%s enters on %d samples; OP-6 rules two", th.Signal, th.EnterSamples)
		}
		// Half the enter threshold is OP-6's release edge for the four pressure signals. The
		// fault rate is the one row where the open point writes its own two numbers — 100 per
		// second in, 10 per second out — because a tenth is where thrashing is over rather than
		// merely halved.
		want := th.Enter / 2
		if th.Signal == Thrashing {
			want = 10
		}
		if th.Release != want {
			t.Errorf("%s releases at %v; OP-6 rules %v", th.Signal, th.Release, want)
		}
	}
}

func sample(sec int, memSome, memFull, ioFull, cpu float64) Sample {
	return Sample{At: at(sec), MemorySomeAvg10: memSome, MemoryFullAvg10: memFull,
		IOFullAvg10: ioFull, CPUSomeAvg60: cpu, CPUTokensFree: true}
}

func active(sigs []Signal, want Signal) bool {
	for _, s := range sigs {
		if s == want {
			return true
		}
	}
	return false
}

func TestOneSpikeIsNotAnEvent(t *testing.T) {
	w := NewWatcher()
	if active(w.Observe(sample(0, 12, 0, 0, 0)), MemoryTight) {
		t.Fatal("one sample above 10 % acted; OP-6 rules two, because one sample is a spike")
	}
	if !active(w.Observe(sample(2, 12, 0, 0, 0)), MemoryTight) {
		t.Fatal("two consecutive samples above 10 % did not act")
	}
}

func TestStalledMemoryActsOnTheFirstSample(t *testing.T) {
	w := NewWatcher()
	sigs := w.Observe(sample(0, 0, 6, 0, 0))
	if !active(sigs, MemoryStalled) {
		t.Fatal("memory.full above 5 % waited for a second sample; SP-RC-2 says do not wait")
	}
	if Demanded(sigs) != RungFreeze {
		t.Fatalf("a stalled machine demands %q; SP-RC-2 says freeze immediately", Demanded(sigs))
	}
}

func TestHysteresisHoldsTheTwoEdgesApart(t *testing.T) {
	w := NewWatcher()
	w.Observe(sample(0, 12, 0, 0, 0))
	w.Observe(sample(2, 12, 0, 0, 0))
	if !active(w.Active(), MemoryTight) {
		t.Fatal("setup: the signal should be acting")
	}
	// In the band between the release edge (5 %) and the enter edge (10 %) it neither clears nor
	// re-triggers — that band is the hysteresis.
	if !active(w.Observe(sample(4, 7, 0, 0, 0)), MemoryTight) {
		t.Fatal("the signal cleared at 7 %, inside its own hysteresis band")
	}
	// Under the release edge, but not yet for the hold time.
	if !active(w.Observe(sample(6, 1, 0, 0, 0)), MemoryTight) {
		t.Fatal("the signal cleared on the first sample under the release edge; OP-6 rules 30 s")
	}
	if !active(w.Observe(sample(30, 1, 0, 0, 0)), MemoryTight) {
		t.Fatal("the signal cleared after 24 s under the release edge; OP-6 rules 30 s")
	}
	if active(w.Observe(sample(37, 1, 0, 0, 0)), MemoryTight) {
		t.Fatal("the signal never cleared after 31 s under the release edge")
	}
}

func TestCPUPressureOnlyCountsWithFreeTokens(t *testing.T) {
	w := NewWatcher()
	busy := func(sec int) Sample {
		s := sample(sec, 0, 0, 0, 80)
		s.CPUTokensFree = false
		return s
	}
	w.Observe(busy(0))
	if active(w.Observe(busy(2)), CPUBesideThePoint) {
		t.Fatal("CPU pressure with every token out acted; that is the machine doing its work")
	}
	w.Observe(sample(4, 0, 0, 0, 80))
	if !active(w.Observe(sample(6, 0, 0, 0, 80)), CPUBesideThePoint) {
		t.Fatal("CPU pressure with free tokens did not act; something is computing beside the point")
	}
}

func TestCountersAreReadAsChangeAndRate(t *testing.T) {
	w := NewWatcher()
	// The first sample of a counter has nothing to compare against.
	s := sample(0, 0, 0, 0, 0)
	s.PgMajFault, s.MemoryEventsHigh = 1000, 5
	if len(w.Observe(s)) != 0 {
		t.Fatal("a counter acted on its first reading, with nothing to compare it against")
	}
	// 300 faults in 2 s is 150/s, above the 100/s edge; two such samples act.
	s2 := sample(2, 0, 0, 0, 0)
	s2.PgMajFault, s2.MemoryEventsHigh = 1300, 6
	w.Observe(s2)
	s3 := sample(4, 0, 0, 0, 0)
	s3.PgMajFault, s3.MemoryEventsHigh = 1600, 7
	sigs := w.Observe(s3)
	if !active(sigs, Thrashing) {
		t.Fatal("150 faults per second on two samples did not act")
	}
	if !active(sigs, Misclassified) {
		t.Fatal("a memory.events high counter rising on two samples did not act")
	}
	if Demanded(sigs) != RungEscalate {
		t.Fatalf("thrashing demands %q; SP-RC-2 says the hardest rung, immediately", Demanded(sigs))
	}
}

func TestMisclassifiedDemandsNoRung(t *testing.T) {
	th, err := ThresholdOf(Misclassified)
	if err != nil {
		t.Fatal(err)
	}
	if th.Rung != "" {
		t.Fatalf("raising a pod's class climbed to %q; OP-6 calls it a reclassification, not a state", th.Rung)
	}
	if Demanded([]Signal{Misclassified}) != "" {
		t.Fatal("a reclassification moved the ladder")
	}
}

// AB-RC-3: five rungs run in order, without an abort.
func TestLadderRunsInOrderAndNeverAborts(t *testing.T) {
	l := Ladder()
	want := []Rung{RungThrottle, RungBlock, RungFreeze, RungCheckpoint, RungEscalate}
	if len(l) != 5 {
		t.Fatalf("the ladder has %d rungs; SP-RC-3 has five", len(l))
	}
	for i, r := range want {
		if l[i] != r {
			t.Fatalf("rung %d is %q; SP-RC-3 puts %q there", i+1, l[i], r)
		}
		if r == "abort" || r == "kill" {
			t.Fatalf("the ladder carries %q", r)
		}
	}

	// A signal demanding the hardest rung still runs the four below it, in order.
	var e Escalation
	entered := e.Climb(RungEscalate)
	if len(entered) != 5 {
		t.Fatalf("climbing to the top entered %d rungs; the ladder is an order, not a menu", len(entered))
	}
	for i, r := range want {
		if entered[i] != r {
			t.Fatalf("rung %d entered was %q, expected %q", i+1, entered[i], r)
		}
	}
	if e.Rung() != RungEscalate {
		t.Fatalf("the escalation stands on %q", e.Rung())
	}
	// And it comes down one rung at a time.
	if left := e.Descend(); left != RungEscalate || e.Rung() != RungCheckpoint {
		t.Fatalf("descending left %q and stands on %q", left, e.Rung())
	}
}

func TestAdmissionStopsAtTheBlockRung(t *testing.T) {
	var e Escalation
	if !e.Admits() {
		t.Fatal("a node on the ground refused admission")
	}
	e.Climb(RungThrottle)
	if !e.Admits() {
		t.Fatal("throttling stopped admission; that is the next rung")
	}
	e.Climb(RungBlock)
	if e.Admits() {
		t.Fatal("the block rung admitted a pod")
	}
}

// AB-RC-1: admission decides from pressure, not from utilization. There is no field in a sample
// that reports how busy the cores are, and a machine at full CPU with no pressure admits.
func TestFullCPUWithoutPressureStillAdmits(t *testing.T) {
	w := NewWatcher()
	var e Escalation
	for sec := 0; sec < 20; sec += 2 {
		// Every core busy is not a signal at all; what is read is `cpu.pressure`, and it is 0.
		e.Step(w.Observe(sample(sec, 0, 0, 0, 0)))
	}
	if !e.Admits() {
		t.Fatal("a fully utilized machine under no pressure was blocked; 100 % CPU is healthy")
	}
	// The same machine at 12 % memory pressure — a number no utilization metric would show —
	// stops admitting.
	e.Step(w.Observe(sample(20, 12, 0, 0, 0)))
	e.Step(w.Observe(sample(22, 12, 0, 0, 0)))
	if e.Admits() {
		t.Fatal("memory pressure above 10 % did not stop admission (SP-RC-2)")
	}
}

func TestSampleIntervalIsTwoSeconds(t *testing.T) {
	if SampleInterval != 2*time.Second {
		t.Fatalf("the PSI reader samples every %s; SP-RC-1 says two seconds", SampleInterval)
	}
}
