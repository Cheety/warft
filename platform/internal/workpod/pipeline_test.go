package workpod

import (
	"strings"
	"testing"

	"github.com/Cheety/warft/platform/internal/runner"
)

// SP-T05-1 holds for a pod that never ran too. A run that died in `prepare` has six phases it could
// not reach, and each of them is recorded as not reached — because a hole in the log and a step that
// was skipped on purpose would otherwise read the same.
func TestAPodThatDiedEarlyStillCarriesTheSpine(t *testing.T) {
	var l runner.PhaseLog
	l.Add(runner.PhasePrepare, runner.Failed, 0, 0, "the base is not a btrfs subvolume")
	l.Add(runner.PhaseReap, runner.Ran, 0, 0, "patch out, pod gone")

	full := completeSpine(l.Records, runner.Report{FinalState: "failed", Cause: "tool.failure"})
	if err := runner.VerifySpine(full); err != nil {
		t.Fatalf("a pod that died in prepare left the spine: %v", err)
	}
	if len(full) != 7 {
		t.Errorf("the log carries %d phases, the spine has 7", len(full))
	}
	for _, r := range full {
		if r.Phase == runner.PhasePrepare || r.Phase == runner.PhaseReap {
			continue
		}
		if r.Outcome != runner.Skipped || !strings.Contains(r.Detail, "tool.failure") {
			t.Errorf("%s reads %q/%q, and a phase nobody reached says why", r.Phase, r.Outcome, r.Detail)
		}
	}
}

// A complete log is left exactly as it is: `check` and `repair` interleave once per rework round,
// and rebuilding it by phase would lose which round each record belonged to.
func TestACompleteSpineIsNotReordered(t *testing.T) {
	var l runner.PhaseLog
	l.Add(runner.PhasePrepare, runner.Ran, 0, 0, "x")
	l.Add(runner.PhasePlan, runner.Skipped, 0, 0, "x")
	l.Add(runner.PhaseEdit, runner.Ran, 0, 0, "x")
	l.Add(runner.PhaseCheck, runner.Failed, 0, 0, "x")
	l.Add(runner.PhaseRepair, runner.Ran, 1, 0, "x")
	l.Add(runner.PhaseCheck, runner.Ran, 1, 0, "x")
	l.Add(runner.PhaseDeliver, runner.Ran, 0, 0, "x")
	l.Add(runner.PhaseReap, runner.Ran, 0, 0, "x")

	full := completeSpine(l.Records, runner.Report{FinalState: "delivered"})
	if len(full) != len(l.Records) {
		t.Fatalf("a complete log grew from %d to %d records", len(l.Records), len(full))
	}
	for i := range full {
		if full[i] != l.Records[i] {
			t.Errorf("record %d moved: %v became %v", i, l.Records[i], full[i])
		}
	}
}

// The deliver phase reads what changed out of the patch, because the patch is the measurement. A
// second walk of the two trees could disagree with the thing the reply actually carries.
func TestChangedPathsAreReadOutOfThePatch(t *testing.T) {
	pod := "/data/work/pods/order-1"
	patch := strings.Join([]string{
		"--- /data/work/bases/repo-a/src/main.go\t2026-07-28 10:00:00.000000000 +0000",
		"+++ " + pod + "/src/main.go\t2026-07-28 10:00:01.000000000 +0000",
		"@@ -1 +1 @@",
		"-one",
		"+two",
		"--- /data/work/bases/repo-a/docs/readme.md\t2026-07-28 10:00:00.000000000 +0000",
		"+++ " + pod + "/docs/readme.md\t2026-07-28 10:00:01.000000000 +0000",
		"@@ -0,0 +1 @@",
		"+new",
		"Binary files /data/work/bases/repo-a/logo.png and " + pod + "/logo.png differ",
		"",
	}, "\n")

	got := changedPaths([]byte(patch), pod)
	want := []string{"docs/readme.md", "logo.png", "src/main.go"}
	if len(got) != len(want) {
		t.Fatalf("the patch touched %v, expected %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("path %d is %q, expected %q", i, got[i], want[i])
		}
	}
}

// Place three, applied to what the patch says was touched.
func TestPlaceThreeIsCheckedAgainstTheDiff(t *testing.T) {
	changed := []string{"docs/readme.md", "src/main.go", "src/deep/inner.go"}

	if out := outsidePlaces(changed, nil); out != nil {
		t.Errorf("with no prefixes the whole working copy is allowed, and %v was refused", out)
	}
	if out := outsidePlaces(changed, []string{"src"}); len(out) != 1 || out[0] != "docs/readme.md" {
		t.Errorf("only docs/readme.md lies outside `src`, and the answer was %v", out)
	}
	if out := outsidePlaces(changed, []string{"src/", "docs/"}); out != nil {
		t.Errorf("a trailing slash changed what a prefix means: %v", out)
	}
	// A prefix is a path, not a string: `src` must not admit `srcbackup`.
	if out := outsidePlaces([]string{"srcbackup/x"}, []string{"src"}); len(out) != 1 {
		t.Errorf("`src` admitted srcbackup/x, and a prefix is a path and not a string: %v", out)
	}
	// The prefix may name a single file.
	if out := outsidePlaces([]string{"go.mod"}, []string{"go.mod"}); out != nil {
		t.Errorf("a place naming one file refused that file: %v", out)
	}
}
