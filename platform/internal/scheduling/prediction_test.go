package scheduling

import (
	"testing"
	"time"
)

func profileOf(runs int, peak int64, runtime time.Duration) Profile {
	var p Profile
	for i := 0; i < runs; i++ {
		if err := p.Add(Observation{Repository: "monorepo-a", Phase: "check",
			PeakRSS: peak, Runtime: runtime}); err != nil {
			panic(err)
		}
	}
	return p
}

// AB-RC-6: after three runs admission decides mechanically.
func TestAdmissionBecomesMechanicalAfterThreeRuns(t *testing.T) {
	free := int64(10) << 30
	for runs := 0; runs < 3; runs++ {
		v := Decide(profileOf(runs, 2<<30, time.Minute), free)
		if v.Mechanical {
			t.Fatalf("%d run(s) decided mechanically; SP-RC-6 asks for three", runs)
		}
		if !v.Admit {
			t.Fatalf("%d run(s): the job was refused without a prediction to refuse it on", runs)
		}
	}
	v := Decide(profileOf(3, 2<<30, time.Minute), free)
	if !v.Mechanical || !v.Admit {
		t.Fatalf("three runs did not decide mechanically: %+v", v)
	}
	if v.Exclusive {
		t.Fatal("2 GB of 10 GB free took the whole node")
	}
}

func TestProfileKeepsTheLargestPeak(t *testing.T) {
	var p Profile
	for _, peak := range []int64{1 << 30, 3 << 30, 2 << 30} {
		if err := p.Add(Observation{Repository: "r", Phase: "check", PeakRSS: peak,
			Runtime: time.Duration(peak) * time.Nanosecond}); err != nil {
			t.Fatal(err)
		}
	}
	if p.PeakRSS != 3<<30 {
		t.Fatalf("the profile carries %d bytes; admission asks whether it fits, not what it usually needs", p.PeakRSS)
	}
	if p.Runs != 3 {
		t.Fatalf("%d runs", p.Runs)
	}
	if err := p.Add(Observation{Repository: "other", Phase: "check"}); err == nil {
		t.Fatal("two repositories were folded into one profile")
	}
}

func TestAboveNinetyPercentTheJobDoesNotStartButReportsOptions(t *testing.T) {
	free := int64(10) << 30
	v := Decide(profileOf(3, 95*(free/100), time.Hour), free)
	if v.Admit {
		t.Fatal("a job predicted at 95 % of what is free was started; SP-RC-6 says it does not start")
	}
	if len(v.Options) == 0 {
		t.Fatal("a refusal without options is the silent truncation SP-V04-2 forbids")
	}
	if v.Share < 0.9 {
		t.Fatalf("share %.2f", v.Share)
	}
}

func TestBetweenSixtyAndNinetyItRunsAlone(t *testing.T) {
	free := int64(10) << 30
	v := Decide(profileOf(4, 7*(free/10), time.Hour), free)
	if !v.Admit || !v.Exclusive {
		t.Fatalf("70 %% of what is free must run, and must run alone: %+v", v)
	}
}

// AB-RD-3 and AB-E05-2 at the level where they are decided: the numbers Decide reads are the
// measured peak and what the node has free — the five constants of E-05 are not among them.
func TestDecideReadsOnlyMeasuredNumbers(t *testing.T) {
	// E-05's active pod is 960 MB given / 122.5 MB measured. Neither number changes any decision
	// here: what decides is the profile, and a profile of a repository that peaks at 8 GB is
	// refused on a node with 8 GB free whatever E-05 would have planned with.
	free := int64(8) << 30
	if Decide(profileOf(3, 8<<30, time.Hour), free).Admit {
		t.Fatal("a job whose measured peak is the whole node was admitted")
	}
	if !Decide(profileOf(3, 100<<20, time.Hour), free).Admit {
		t.Fatal("a job whose measured peak is 100 MB was refused on a node with 8 GB free")
	}
}
