package observation

import (
	"strings"
	"testing"

	"github.com/Cheety/warft/platform/internal/scheduling"
)

// The catalog the binary carries is SP-B03-3's: four that wake, and the slots are exactly the four.
func TestCatalogHasExactlyFourWakingAlerts(t *testing.T) {
	waking := Waking()
	if len(waking) != WakingSlots {
		t.Fatalf("%d waking alerts, and SP-B03-3 has exactly %d", len(waking), WakingSlots)
	}
	seen := map[int]bool{}
	for _, a := range waking {
		if a.Slot < 1 || a.Slot > WakingSlots {
			t.Errorf("%s holds slot %d", a.Name, a.Slot)
		}
		if seen[a.Slot] {
			t.Errorf("slot %d is held twice", a.Slot)
		}
		seen[a.Slot] = true
	}
	if len(Displays()) == 0 {
		t.Error("everything else is a display, and there is nothing else")
	}
	// SP-A05-5: the disk is the first consumable that gets an alert, and it is not a fifth waking
	// one.
	disk, err := ByName("disk_filling")
	if err != nil {
		t.Fatalf("no disk alert: %v", err)
	}
	if disk.Wakes {
		t.Error("the disk alert wakes a human — that would be a fifth alert (SP-B03-3)")
	}
}

// A fifth waking alert does not parse. The state contract refuses the same thing as a constraint;
// this is the half that fails the build rather than the insert.
func TestAFifthWakingAlertIsRefused(t *testing.T) {
	fifth := alertSource + "\nsomething_else\ttrue\t5\tnowhere\tan alert nobody ruled\n"
	if _, err := parseAlerts(fifth); err == nil {
		t.Fatal("a fifth waking alert was accepted")
	}
	// And so is a second alert in an existing slot: four slots, four alerts.
	second := alertSource + "\nsomething_else\ttrue\t2\tnowhere\tan alert nobody ruled\n"
	if _, err := parseAlerts(second); err == nil || !strings.Contains(err.Error(), "slot 2") {
		t.Fatalf("a second alert in slot 2 was accepted: %v", err)
	}
}

// The thresholds the evaluation compares against are read out of the ruled conditions, so the two
// cannot drift.
func TestThresholdsComeOutOfTheRuledConditions(t *testing.T) {
	for _, c := range []struct {
		alert, unit string
		want        float64
	}{
		{"queue_growing", " samples", 20},
		{"escapes_or_rejections_jumping", "x the mean", 3},
		{"escapes_or_rejections_jumping", " hours before it", 6},
		{"escapes_or_rejections_jumping", " rejections in absolute", 10},
		{"cell_budget_exhausted_early", " % of a cap", 90},
		{"cell_budget_exhausted_early", " % of its day", 75},
		{"disk_filling", " % full", 85},
	} {
		got, err := Threshold(c.alert, c.unit)
		if err != nil {
			t.Errorf("%s %q: %v", c.alert, c.unit, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s %q: %v, ruled %v", c.alert, c.unit, got, c.want)
		}
	}
	if _, err := Threshold("queue_growing", " parsecs"); err == nil {
		t.Error("a threshold the ruling does not carry was answered anyway")
	}
}

// R-D plans with the five constants and says what runs out first.
func TestPlanNamesTheBottleneck(t *testing.T) {
	table, err := Plan(Sliders{RAMGigabytes: 256, Cores: 96, WorkNodes: 1, Fleet: 2000, RushPercent: 15})
	if err != nil {
		t.Fatal(err)
	}
	if table.Mode != "planning" {
		t.Errorf("mode %q", table.Mode)
	}
	if len(table.Places) != 6 {
		t.Fatalf("%d places, and R-D has six", len(table.Places))
	}
	for _, p := range table.Places {
		if p.Source != "planned" {
			t.Errorf("place %s came from %q while planning", p.Name, p.Source)
		}
		if p.Value == "" {
			t.Errorf("place %s is empty in a design calculation", p.Name)
		}
	}
}

// SP-RD-2: in operation the same places carry measured values, and the one place that would have to
// be estimated carries none.
func TestMeasureDoesNotEstimate(t *testing.T) {
	r := Reading{Active: 3, Queued: 7, Frozen: 1, WorkNodes: 2, HaveCells: true, HavePSI: true,
		SampleOf: "/sys/fs/cgroup/workpod.slice/workpod-work.slice",
		Sample:   scheduling.Sample{MemorySomeAvg10: 12.5}}
	table := Measure(r)
	by := map[string]Place{}
	for _, p := range table.Places {
		by[p.Name] = p
	}
	if by["slots"].Source != "not measured" || by["slots"].Value != "" {
		t.Errorf("slots came out as %q from %q — R-D does not estimate", by["slots"].Value, by["slots"].Source)
	}
	for _, name := range []string{"active", "queued", "frozen", "per_node", "bottleneck"} {
		if by[name].Source != "measured" {
			t.Errorf("place %s is %q in operation", name, by[name].Source)
		}
	}
	if by["active"].Value != "3" || by["queued"].Value != "7" || by["per_node"].Value != "1" {
		t.Errorf("counts came out as %+v", by)
	}
	// 12.5 % memory some is over SP-RC-2's 10 %: the bottleneck names the signal, not a guess.
	if by["bottleneck"].Value != string(scheduling.MemoryTight) {
		t.Errorf("bottleneck %q, expected %q", by["bottleneck"].Value, scheduling.MemoryTight)
	}
	if !strings.Contains(by["bottleneck"].Why, "workpod-work.slice") {
		t.Errorf("the bottleneck does not say which files it was read from: %q", by["bottleneck"].Why)
	}
}

// A table with no machine in front of it says so, rather than reporting a quiet node.
func TestMeasureWithoutPressureSaysSo(t *testing.T) {
	table := Measure(Reading{HaveCells: true})
	for _, p := range table.Places {
		if p.Name == "bottleneck" {
			if p.Source != "not measured" || p.Why == "" {
				t.Errorf("bottleneck without pressure: %+v", p)
			}
		}
	}
}

// The constants the table computes with are E-05's, and the measured ones where E-05 adopted them.
func TestConstantsAdoptWhatWasMeasured(t *testing.T) {
	zram, err := ConstantOf("zram_factor")
	if err != nil {
		t.Fatal(err)
	}
	if zram.Value() != zram.Measured {
		t.Errorf("zram factor computes with %v, and AP-1.3 measured %v", zram.Value(), zram.Measured)
	}
	pod, err := ConstantOf("active_pod_ram")
	if err != nil {
		t.Fatal(err)
	}
	if pod.Value() != pod.Given {
		t.Errorf("the active pod computes with %v; E-05 keeps the given %v until three runs (SP-RC-6)",
			pod.Value(), pod.Given)
	}
}
