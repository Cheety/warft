package harness

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cheety/warft/platform/internal/runner"
)

// The four phases the pod runs are the middle of the spine. A test drives them against a directory
// instead of a working copy, which is the whole of the difference: the loop, the bound and the reply
// are the same code the pod runs, and nothing here needs a container to be true.
func podPhases(t *testing.T, rs []runner.PhaseRecord) {
	t.Helper()
	// The three the runner contributes, so that VerifySpine can be asked the question it exists for.
	full := append([]runner.PhaseRecord{{Phase: runner.PhasePrepare, Outcome: runner.Ran}}, rs...)
	full = append(full,
		runner.PhaseRecord{Phase: runner.PhaseDeliver, Outcome: runner.Ran},
		runner.PhaseRecord{Phase: runner.PhaseReap, Outcome: runner.Ran})
	if err := runner.VerifySpine(full); err != nil {
		t.Errorf("the pod's phases do not join into the spine: %v\n%s", err, format(rs))
	}
}

func format(rs []runner.PhaseRecord) string {
	var b strings.Builder
	for _, r := range rs {
		b.WriteString("  " + string(r.Phase) + " " + string(r.Outcome) + " round " + itoa(r.Round) + ": " + r.Detail + "\n")
	}
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

func count(rs []runner.PhaseRecord, p runner.Phase) int {
	n := 0
	for _, r := range rs {
		if r.Phase == p {
			n++
		}
	}
	return n
}

// aJob is a job stated by hand, with an edit command the test supplies.
func aJob(class string, command ...string) runner.Job {
	return runner.Job{
		OrderID:      "018f4242-0000-7000-8000-000000000001",
		Attempt:      1,
		Cell:         "eu-c1",
		Project:      "018f4242-0000-7000-8000-00000000000b",
		Class:        class,
		Requirements: runner.Requirements{Language: "sh", LanguageVersion: "5"},
		Command:      command,
	}
}

func withPlaces(t *testing.T, job runner.Job, m *runner.PlaceMoves) (runner.Job, runner.Pipeline) {
	t.Helper()
	job.Places = m
	pipe, err := job.Effective()
	if err != nil {
		t.Fatalf("the job's places were refused: %v", err)
	}
	return job, pipe
}

func blocking(name string, argv ...string) runner.Check {
	return runner.Check{Name: name, Command: argv, Blocks: true}
}

// AB-T05-3 and decisions/OP-2.md, without a machine: a job whose blocking check can never pass ends
// after the ruled number of rounds, in a named state, with a cause — not in a loop.
func TestAnUnsolvableJobEndsAfterTheRuledRounds(t *testing.T) {
	dir := t.TempDir()
	checks := []runner.Check{blocking("impossible", "/bin/sh", "-c", "exit 1")}
	job, pipe := withPlaces(t, aJob("medium", "/bin/sh", "-c", "echo one > note.txt"),
		&runner.PlaceMoves{Checks: &checks})

	if pipe.Places.ReworkRounds != 3 {
		t.Fatalf("a medium pod runs %d rounds; decisions/OP-2.md rules 3", pipe.Places.ReworkRounds)
	}

	rep := runPipeline(job, pipe, dir, io.Discard, nil)

	if rep.FinalState != "unproven" || rep.Cause != "unsolvable" {
		t.Errorf("the job ended %q/%q, not unproven/unsolvable (SP-T05-3)", rep.FinalState, rep.Cause)
	}
	if rep.Evidence != "" {
		t.Errorf("a job that reached no acceptance criterion claimed the evidence class %q (Q-02)", rep.Evidence)
	}
	if rep.Rounds != 3 || rep.RoundsAllowed != 3 {
		t.Errorf("%d of %d rounds were spent; the bound is 3", rep.Rounds, rep.RoundsAllowed)
	}
	// One check per round plus the one that judged the edit, one repair per round.
	if got := count(rep.Phases, runner.PhaseCheck); got != 4 {
		t.Errorf("the checks ran %d times; three rounds is four checks", got)
	}
	if got := count(rep.Phases, runner.PhaseRepair); got != 3 {
		t.Errorf("the repair ran %d times; the bound is three", got)
	}
	if rep.Assessment == "" {
		t.Error("the reply carries no assessment (SP-T05-3)")
	}
	for _, want := range []string{"impossible", "OP-2", "rework rounds"} {
		if !strings.Contains(rep.Assessment, want) {
			t.Errorf("the assessment does not mention %q:\n%s", want, rep.Assessment)
		}
	}
	podPhases(t, rep.Phases)
}

// The bound is the class's, not the job's wish: a `tiny` pod gets one round for the same job.
func TestTheBoundIsTheClasss(t *testing.T) {
	checks := []runner.Check{blocking("impossible", "/bin/sh", "-c", "exit 1")}
	for class, want := range map[string]int{"tiny": 1, "small": 3, "medium": 3, "large": 2} {
		job, pipe := withPlaces(t, aJob(class, "/usr/bin/true"), &runner.PlaceMoves{Checks: &checks})
		rep := runPipeline(job, pipe, t.TempDir(), io.Discard, nil)
		if rep.Rounds != want {
			t.Errorf("%s spent %d rounds, the ruling allows %d", class, rep.Rounds, want)
		}
	}
}

// A job may end the loop before the bound: zero rounds means check once, never repair.
func TestZeroRoundsChecksOnceAndNeverRepairs(t *testing.T) {
	checks := []runner.Check{blocking("impossible", "/bin/sh", "-c", "exit 1")}
	none := 0
	job, pipe := withPlaces(t, aJob("medium", "/usr/bin/true"),
		&runner.PlaceMoves{Checks: &checks, ReworkRounds: &none})

	rep := runPipeline(job, pipe, t.TempDir(), io.Discard, nil)
	if rep.Rounds != 0 {
		t.Errorf("%d rounds were spent on a job that allows none", rep.Rounds)
	}
	if got := count(rep.Phases, runner.PhaseCheck); got != 1 {
		t.Errorf("the check ran %d times; without a rework round it runs once", got)
	}
	if rep.FinalState != "unproven" || rep.Cause != "unsolvable" {
		t.Errorf("the job ended %q/%q", rep.FinalState, rep.Cause)
	}
	// The phase still stands in the spine, as skipped and with the reason.
	for _, r := range rep.Phases {
		if r.Phase == runner.PhaseRepair {
			if r.Outcome != runner.Skipped || !strings.Contains(r.Detail, "rework_rounds") {
				t.Errorf("the repair phase reads %q/%q, and the reason is not the bound", r.Outcome, r.Detail)
			}
		}
	}
	podPhases(t, rep.Phases)
}

// Q-02, in the shape T-05 gives it: a blocking check that passes is what a delivery rests on, and
// the evidence class is the one the job named at place six.
func TestAPassingBlockingCheckDelivers(t *testing.T) {
	dir := t.TempDir()
	checks := []runner.Check{blocking("exists", "/bin/sh", "-c", "test -f note.txt")}
	acceptance := "tests.new"
	job, pipe := withPlaces(t, aJob("medium", "/bin/sh", "-c", "echo one > note.txt"),
		&runner.PlaceMoves{Checks: &checks, Acceptance: &acceptance})

	rep := runPipeline(job, pipe, dir, io.Discard, nil)
	if rep.FinalState != "delivered" {
		t.Fatalf("the job ended %q/%q:\n%s", rep.FinalState, rep.Cause, rep.Assessment)
	}
	if rep.Evidence != "tests.new" {
		t.Errorf("it delivered under %q, and the job named tests.new", rep.Evidence)
	}
	if rep.Cause != "" {
		t.Errorf("a delivery carries the cause %q", rep.Cause)
	}
	if rep.Rounds != 0 {
		t.Errorf("%d rework rounds were spent on a job that was right the first time", rep.Rounds)
	}
	podPhases(t, rep.Phases)
}

// The loop is not only a bound, it is a loop: a check that starts failing and stops has to deliver
// on the round it stops.
func TestARepairThatWorksEndsTheLoop(t *testing.T) {
	dir := t.TempDir()
	// The edit appends a line each time it runs — the repair phase is the edit command again until
	// the agent of stage 5 exists — and the check passes once there are three of them.
	checks := []runner.Check{blocking("three-lines", "/bin/sh", "-c", "test $(wc -l < log.txt) -ge 3")}
	job, pipe := withPlaces(t, aJob("medium", "/bin/sh", "-c", "echo line >> log.txt"),
		&runner.PlaceMoves{Checks: &checks})

	rep := runPipeline(job, pipe, dir, io.Discard, nil)
	if rep.FinalState != "delivered" {
		t.Fatalf("the job ended %q/%q:\n%s", rep.FinalState, rep.Cause, rep.Assessment)
	}
	if rep.Rounds != 2 {
		t.Errorf("it took %d rounds; the third line arrives on the second repair", rep.Rounds)
	}
	body, err := os.ReadFile(filepath.Join(dir, "log.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(body), "line"); got != 3 {
		t.Errorf("the working copy has %d lines after the loop, not 3", got)
	}
	podPhases(t, rep.Phases)
}

// A job that names no check is the job AP-3.3 could already run, and it must keep ending the way it
// did: nothing measured anything, so there is no evidence class (Q-02).
func TestNoCheckMeansNoEvidence(t *testing.T) {
	job, pipe := withPlaces(t, aJob("medium", "/usr/bin/true"), nil)
	rep := runPipeline(job, pipe, t.TempDir(), io.Discard, nil)
	if rep.FinalState != "unproven" || rep.Cause != "skill.missing" {
		t.Errorf("a job with no check ended %q/%q, not unproven/skill.missing", rep.FinalState, rep.Cause)
	}
	if rep.Rounds != 0 {
		t.Errorf("%d rounds were spent with nothing to check", rep.Rounds)
	}
	podPhases(t, rep.Phases)
}

// And the same job with a failing command keeps ending as a tool failure: with no check to decide,
// the exit code is the only signal there is.
func TestNoCheckAndAFailedEditIsAToolFailure(t *testing.T) {
	job, pipe := withPlaces(t, aJob("medium", "/bin/sh", "-c", "exit 3"), nil)
	rep := runPipeline(job, pipe, t.TempDir(), io.Discard, nil)
	if rep.FinalState != "failed" || rep.Cause != "tool.failure" {
		t.Errorf("the job ended %q/%q, not failed/tool.failure", rep.FinalState, rep.Cause)
	}
	if rep.ExitCode != 3 {
		t.Errorf("the report carries exit code %d, the command exited 3", rep.ExitCode)
	}
	podPhases(t, rep.Phases)
}

// A check that reports without blocking does not decide anything, and a delivery that rested on it
// would be a claim nobody checked (Q-02).
func TestACheckThatDoesNotBlockDoesNotDeliver(t *testing.T) {
	checks := []runner.Check{{Name: "advisory", Command: []string{"/usr/bin/true"}, Blocks: false}}
	job, pipe := withPlaces(t, aJob("medium", "/usr/bin/true"), &runner.PlaceMoves{Checks: &checks})
	rep := runPipeline(job, pipe, t.TempDir(), io.Discard, nil)
	if rep.FinalState != "unproven" || rep.Cause != "skill.missing" {
		t.Errorf("a job whose only check does not block ended %q/%q", rep.FinalState, rep.Cause)
	}
	if !strings.Contains(rep.Assessment, "non-blocking") {
		t.Errorf("the assessment does not say why nothing was decided:\n%s", rep.Assessment)
	}
	podPhases(t, rep.Phases)
}

// Place two, demanded and unbuildable: the honest answer is a named state with a cause, and all four
// phases still stand in the log (SP-T05-1).
func TestADemandedPlanIsRefusedRatherThanInvented(t *testing.T) {
	dir := t.TempDir()
	plan := true
	job, pipe := withPlaces(t, aJob("medium", "/bin/sh", "-c", "echo edited > file.txt"),
		&runner.PlaceMoves{PlanRequired: &plan})

	rep := runPipeline(job, pipe, dir, io.Discard, nil)
	if rep.FinalState != "unproven" || rep.Cause != "skill.missing" {
		t.Errorf("the job ended %q/%q, not unproven/skill.missing", rep.FinalState, rep.Cause)
	}
	if _, err := os.Stat(filepath.Join(dir, "file.txt")); err == nil {
		t.Error("the edit ran although the plan it demanded was never written")
	}
	var planned runner.PhaseRecord
	for _, r := range rep.Phases {
		if r.Phase == runner.PhasePlan {
			planned = r
		}
	}
	if planned.Outcome != runner.Refused {
		t.Errorf("the plan phase reads %q, and a plan nobody can write is refused", planned.Outcome)
	}
	podPhases(t, rep.Phases)
}

// Every check runs, including the ones after a failure: a reply that stopped at the first would cost
// a round to discover the second.
func TestEveryCheckRunsInARound(t *testing.T) {
	dir := t.TempDir()
	checks := []runner.Check{
		blocking("first", "/bin/sh", "-c", "exit 1"),
		blocking("second", "/bin/sh", "-c", "echo second-ran >> ran.txt; exit 1"),
	}
	none := 0
	job, pipe := withPlaces(t, aJob("medium", "/usr/bin/true"),
		&runner.PlaceMoves{Checks: &checks, ReworkRounds: &none})

	rep := runPipeline(job, pipe, dir, io.Discard, nil)
	if _, err := os.Stat(filepath.Join(dir, "ran.txt")); err != nil {
		t.Error("the second check did not run after the first one failed")
	}
	for _, want := range []string{"first", "second"} {
		if !strings.Contains(rep.Assessment, want) {
			t.Errorf("the assessment does not name the failing check %q:\n%s", want, rep.Assessment)
		}
	}
}

// The report names the definition it ran under and its content hash — the two halves of
// `pipeline_version` in contract/schema.sql (SP-T05-4).
func TestTheReportNamesThePipelineItRanUnder(t *testing.T) {
	def, err := runner.PipelineByRef(runner.DefaultPipeline)
	if err != nil {
		t.Fatal(err)
	}
	job, pipe := withPlaces(t, aJob("medium", "/usr/bin/true"), nil)
	rep := runPipeline(job, pipe, t.TempDir(), io.Discard, nil)
	if rep.PipelineVersion != runner.DefaultPipeline {
		t.Errorf("the report names %q, the job ran under %s", rep.PipelineVersion, runner.DefaultPipeline)
	}
	if rep.PipelineHash != def.ContentHash() {
		t.Errorf("the report's hash %q is not the definition's %q", rep.PipelineHash, def.ContentHash())
	}
}

// And it names the *definition*, not the job's arrangement of it. Two jobs that move different
// places ran under one pipeline; a hash that differed between them would make `pipeline@version`
// name the job instead of the pipeline, which is the opposite of what SP-T05-4 asks for.
func TestTwoJobsThatMoveDifferentPlacesShareOnePipeline(t *testing.T) {
	checks := []runner.Check{blocking("exists", "/bin/sh", "-c", "test -f note.txt")}
	acceptance := "types.lint"
	one := 1

	a, pipeA := withPlaces(t, aJob("medium", "/bin/sh", "-c", "echo one > note.txt"),
		&runner.PlaceMoves{Checks: &checks})
	b, pipeB := withPlaces(t, aJob("small", "/bin/sh", "-c", "echo one > note.txt"),
		&runner.PlaceMoves{Checks: &checks, Acceptance: &acceptance, ReworkRounds: &one})

	repA := runPipeline(a, pipeA, t.TempDir(), io.Discard, nil)
	repB := runPipeline(b, pipeB, t.TempDir(), io.Discard, nil)

	if repA.PipelineHash != repB.PipelineHash {
		t.Errorf("two jobs under %s reported two content hashes:\n  %s\n  %s",
			runner.DefaultPipeline, repA.PipelineHash, repB.PipelineHash)
	}
	if repA.PipelineVersion != repB.PipelineVersion {
		t.Errorf("%q and %q are the same pipeline", repA.PipelineVersion, repB.PipelineVersion)
	}
	// What differs is what the job asked for, and it is in the report where a reply can read it.
	if repA.RoundsAllowed == repB.RoundsAllowed {
		t.Errorf("both jobs were allowed %d rounds; one of them moved place five", repA.RoundsAllowed)
	}
}

// The console is the logs half of SP-T05-3's reply, and it is written while the loop runs rather
// than after it: a job that never finishes is exactly the job whose log someone needs.
func TestTheRunIsNarratedToTheConsole(t *testing.T) {
	var console strings.Builder
	checks := []runner.Check{blocking("impossible", "/bin/sh", "-c", "echo the-check-spoke; exit 1")}
	none := 0
	job, pipe := withPlaces(t, aJob("medium", "/bin/sh", "-c", "echo the-edit-spoke"),
		&runner.PlaceMoves{Checks: &checks, ReworkRounds: &none})

	runPipeline(job, pipe, t.TempDir(), &console, nil)

	for _, want := range []string{"=== edit:", "the-edit-spoke", "=== check 0/impossible:", "the-check-spoke", "--- exit 1 ---"} {
		if !strings.Contains(console.String(), want) {
			t.Errorf("the console does not carry %q:\n%s", want, console.String())
		}
	}
}
