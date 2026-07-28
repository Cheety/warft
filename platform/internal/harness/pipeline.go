// The pod's four phases of T-05's spine: plan · edit · check · repair (AP-3.4).
//
// The other three are the runner's, outside the pod — `prepare` snapshots the working copy,
// `deliver` computes the patch from two trees the pod cannot reach, `reap` deletes it. The split is
// not tidiness: what the pod changed has to be measured rather than claimed, and a phase that runs
// where the claim is made could not measure it.
//
// There is still no agent and no model here. The `edit` phase is the job's command and the `repair`
// phase is that command again — the seam decisions/jobs-by-hand.md opens for a platform whose
// captain is stage 5's. What AP-3.4 adds is not intelligence, it is a bound: the loop ends after the
// number of rounds decisions/OP-2.md ruled, and it ends with a reply rather than with silence.
package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/Cheety/warft/platform/internal/runner"
)

// checkResult is one check of one round.
type checkResult struct {
	check    runner.Check
	exitCode int
	output   []byte
	startErr error
}

func (r checkResult) passed() bool { return r.startErr == nil && r.exitCode == 0 }

// round is what one pass through the loop produced, kept so the assessment can say what changed
// between two of them rather than only what failed in the last.
type round struct {
	number      int
	results     []checkResult
	outputHash  string
	repairCode  int
	repairError error
}

// runPipeline is the pod's half of SP-T05-1, and every path through it records all four of its
// phases. A phase that had nothing to do says so and says why: a spine with holes in it cannot be
// told from one that was never followed, which is the difference AB-T05-1 measures.
//
// `console` is where the run is narrated as it happens. In a pod it is descriptor 1, which is a bind
// mount from the host — so SP-T05-3's "logs" are on the node while the loop is still running rather
// than at the end of it, and SP-T04-2's "no logs in the pod" holds without a second mechanism. B-03
// gives that stream a job id and a trace in AP-3.8; here it is a file beside the pod, copied onto
// /var by the deliver phase because the pod's own directory does not survive the reaper.
//
// `notes` is what the caller already knows about the pod it is running in — the state of the socket,
// which is a fact about the pod and not about the pipeline. Both are passed in rather than gathered
// here so that the loop can be run against a directory without a pod, which is what its tests do.
func runPipeline(job runner.Job, pipe runner.Pipeline, dir string, console io.Writer, notes []string) runner.Report {
	// The definition's hash, not the effective pipeline's: `pipeline_version.content_hash` is the
	// identity of what the human filed, and two jobs that moved different places ran under one
	// definition (SP-T05-4).
	def, err := job.Definition()
	if err != nil {
		// Unreachable in a pod — the host refuses such a job before it creates anything — but a
		// report that named no pipeline would be worse than one that says why.
		def = pipe
	}
	rep := runner.Report{
		OrderID:         job.OrderID,
		Attempt:         job.Attempt,
		PipelineVersion: def.Ref(),
		PipelineHash:    def.ContentHash(),
		RoundsAllowed:   pipe.Places.ReworkRounds,
	}
	var log runner.PhaseLog
	notes = append([]string{"pipeline: " + def.Ref() + " " + def.ContentHash()}, notes...)
	for _, n := range notes {
		fmt.Fprintln(console, n)
	}

	// ---- plan ------------------------------------------------------------------------------
	if pipe.Places.PlanRequired {
		// Refused rather than invented. A plan demanded and not written is exactly the case Q-02
		// exists for: the honest answer is a named state with a cause, not an edit that pretends
		// the plan was there.
		log.Add(runner.PhasePlan, runner.Refused, 0, 0,
			"a plan is demanded and nothing in this artifact writes one — the planning agent is T-02's, built in stage 5 (AP-5.5)")
		log.Add(runner.PhaseEdit, runner.Skipped, 0, 0, "nothing is edited before the plan that was demanded")
		log.Add(runner.PhaseCheck, runner.Skipped, 0, 0, "nothing was edited, so there is nothing to check")
		log.Add(runner.PhaseRepair, runner.Skipped, 0, 0, "nothing was checked, so there is nothing to repair")
		rep.FinalState, rep.Cause = "unproven", "skill.missing"
		rep.Assessment = "The pipeline " + pipe.Ref() + " demands a plan (place plan_required) and this artifact " +
			"has no capability that produces one. The job was not edited, so the working copy is the base and the " +
			"diff is empty. What is missing is the planning agent of T-02, which AP-5.5 builds; until then a job " +
			"that demands a plan ends here, with a cause, rather than running without one."
		rep.Phases, rep.Text = log.Records, strings.Join(notes, "\n")
		return rep
	}
	log.Add(runner.PhasePlan, runner.Skipped, 0, 0, "no plan is demanded (place plan_required)")

	// ---- edit ------------------------------------------------------------------------------
	start := time.Now()
	narrate(console, "edit", job.Command)
	editOut, editCode, editErr := runCommand(job.Command, dir)
	editTook := time.Since(start)
	said(console, editOut, editCode)
	rep.ExitCode = editCode
	switch {
	case editErr != nil && editCode < 0:
		log.Add(runner.PhaseEdit, runner.Failed, 0, editTook, "the command could not be started: %v", editErr)
	case editCode != 0:
		log.Add(runner.PhaseEdit, runner.Failed, 0, editTook, "%v exited %d", job.Command, editCode)
	default:
		log.Add(runner.PhaseEdit, runner.Ran, 0, editTook, "%v exited 0", job.Command)
	}
	notes = append(notes, fmt.Sprintf("edit: %v exited %d after %s", job.Command, editCode, editTook.Round(time.Millisecond)))
	if len(editOut) > 0 {
		notes = append(notes, "--- output ---", string(editOut))
	}

	// ---- check → repair, bounded --------------------------------------------------------------
	blocking := 0
	for _, c := range pipe.Places.Checks {
		if c.Blocks {
			blocking++
		}
	}

	var rounds []round
	spent := 0
	for {
		start = time.Now()
		results := runChecks(pipe.Places.Checks, dir, console, spent)
		took := time.Since(start)
		failed := failedBlocking(results)

		switch {
		case len(pipe.Places.Checks) == 0:
			log.Add(runner.PhaseCheck, runner.Skipped, spent, took,
				"the job names no check, so nothing measured anything (place checks)")
		case len(failed) > 0:
			log.Add(runner.PhaseCheck, runner.Failed, spent, took, "%s", describe(results))
		default:
			log.Add(runner.PhaseCheck, runner.Ran, spent, took, "%s", describe(results))
		}
		rounds = append(rounds, round{number: spent, results: results, outputHash: outputHash(results)})

		if blocking == 0 || len(failed) == 0 || spent >= pipe.Places.ReworkRounds {
			break
		}

		spent++
		start = time.Now()
		narrate(console, fmt.Sprintf("repair %d of %d", spent, pipe.Places.ReworkRounds), job.Command)
		repairOut, repairCode, repairErr := runCommand(job.Command, dir)
		took = time.Since(start)
		said(console, repairOut, repairCode)
		if repairErr != nil && repairCode < 0 {
			log.Add(runner.PhaseRepair, runner.Failed, spent, took, "the command could not be started: %v", repairErr)
		} else {
			log.Add(runner.PhaseRepair, runner.Ran, spent, took,
				"round %d of %d: %v exited %d", spent, pipe.Places.ReworkRounds, job.Command, repairCode)
		}
		rounds[len(rounds)-1].repairCode, rounds[len(rounds)-1].repairError = repairCode, repairErr
		if len(repairOut) > 0 {
			notes = append(notes, fmt.Sprintf("--- repair %d ---", spent), string(repairOut))
		}
	}
	if spent == 0 {
		// The phase still happened, in the sense SP-T05-1 means: the spine is fixed, and a step
		// that was not needed is recorded as not needed rather than left out.
		log.Add(runner.PhaseRepair, runner.Skipped, 0, 0, "%s", whyNoRepair(pipe, blocking, rounds))
	}
	rep.Rounds = spent

	last := rounds[len(rounds)-1]
	switch {
	case blocking == 0 && (editCode != 0 || (editErr != nil && editCode < 0)):
		// No check decides anything, so the edit's own exit code is the only signal there is —
		// which is what the runner reported before there was a pipeline at all.
		rep.FinalState, rep.Cause = "failed", "tool.failure"
	case blocking == 0:
		// Q-02, applied to the pipeline: a delivery rests on a blocking check that passed. Nothing
		// blocked here, so nothing was decided, and claiming the acceptance criterion would be the
		// confidence this platform rejects.
		rep.FinalState, rep.Cause = "unproven", "skill.missing"
	case len(failedBlocking(last.results)) == 0:
		rep.FinalState, rep.Evidence = "delivered", pipe.Places.Acceptance
	default:
		// SP-T05-3: after n rounds the pod does not fail silently. `unproven` because the run was
		// sound and the evidence is what is missing; `unsolvable` because that is the cause code
		// contract/schema.sql carries for exactly this.
		rep.FinalState, rep.Cause = "unproven", "unsolvable"
	}

	rep.Assessment = assess(job, pipe, rounds, blocking, editCode)
	if out := checkOutput(last.results); out != "" {
		notes = append(notes, "--- checks, last round ---", out)
	}
	rep.Phases, rep.Text = log.Records, strings.Join(notes, "\n")
	return rep
}

// whyNoRepair says why the repair phase had nothing to do. Three reasons, and they are different
// enough that one sentence for all three would hide the interesting one.
func whyNoRepair(pipe runner.Pipeline, blocking int, rounds []round) string {
	switch {
	case len(pipe.Places.Checks) == 0:
		return "nothing was checked, so there is nothing to repair"
	case blocking == 0:
		return "no check blocks, so no failure asks for a repair (place checks)"
	case pipe.Places.ReworkRounds == 0:
		return "the job allows no rework round (place rework_rounds is 0, decisions/OP-2.md)"
	case len(failedBlocking(rounds[len(rounds)-1].results)) == 0:
		return "every blocking check passed the first time"
	}
	return "no rework round was spent"
}

// runChecks runs place four in the working copy, in the order the pipeline states them. Every check
// runs, including the ones after a failure: a reply that named the first failure and stopped would
// cost a round to discover the second.
func runChecks(checks []runner.Check, dir string, console io.Writer, round int) []checkResult {
	out := make([]checkResult, 0, len(checks))
	for _, c := range checks {
		narrate(console, fmt.Sprintf("check %d/%s", round, c.Name), c.Command)
		body, code, err := runCommand(c.Command, dir)
		said(console, body, code)
		r := checkResult{check: c, exitCode: code, output: body}
		if err != nil && code < 0 {
			r.startErr = err
		}
		out = append(out, r)
	}
	return out
}

// narrate and said are the two halves of one line in the pod's console: what is about to run, and
// what it said. They are written as the run happens rather than collected at the end, because a job
// that never finishes is exactly the job whose log someone needs (SP-T04-2, B-03).
func narrate(console io.Writer, phase string, argv []string) {
	fmt.Fprintf(console, "\n=== %s: %s ===\n", phase, strings.Join(argv, " "))
}

func said(console io.Writer, output []byte, code int) {
	if len(output) > 0 {
		console.Write(output)
		if output[len(output)-1] != '\n' {
			fmt.Fprintln(console)
		}
	}
	fmt.Fprintf(console, "--- exit %d ---\n", code)
}

func failedBlocking(rs []checkResult) []checkResult {
	var out []checkResult
	for _, r := range rs {
		if r.check.Blocks && !r.passed() {
			out = append(out, r)
		}
	}
	return out
}

// outputHash is what the assessment compares between rounds. A loop whose checks produce byte-identical
// output twice has stopped making progress, and saying so is more useful than saying "three rounds
// were spent".
func outputHash(rs []checkResult) string {
	h := sha256.New()
	for _, r := range rs {
		fmt.Fprintf(h, "%s\x00%d\x00", r.check.Name, r.exitCode)
		h.Write(r.output)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func describe(rs []checkResult) string {
	if len(rs) == 0 {
		return "no check"
	}
	parts := make([]string, len(rs))
	for i, r := range rs {
		state := "passed"
		if !r.passed() {
			state = fmt.Sprintf("failed (exit %d)", r.exitCode)
			if r.startErr != nil {
				state = "could not start"
			}
		}
		if r.check.Blocks {
			state += ", blocking"
		}
		parts[i] = r.check.Name + " " + state
	}
	return strings.Join(parts, " · ")
}

func checkOutput(rs []checkResult) string {
	var b strings.Builder
	for _, r := range rs {
		if len(r.output) == 0 {
			continue
		}
		fmt.Fprintf(&b, "[%s exit %d]\n%s\n", r.check.Name, r.exitCode, r.output)
	}
	return strings.TrimSpace(b.String())
}

// assess is the third part of SP-T05-3's reply: the pod's own account of where it got to.
//
// It is written on every path, including a delivery, because "which checks decided this and what it
// took" is one sentence read from either side — and a reply that carried an assessment only on
// failure would make the assessment a synonym for bad news rather than a record.
func assess(job runner.Job, pipe runner.Pipeline, rounds []round, blocking, editCode int) string {
	var b strings.Builder
	last := rounds[len(rounds)-1]
	failed := failedBlocking(last.results)

	fmt.Fprintf(&b, "Pipeline %s, %d of at most %d rework rounds spent.\n",
		pipe.Ref(), rounds[len(rounds)-1].number, pipe.Places.ReworkRounds)

	switch {
	case len(pipe.Places.Checks) == 0:
		fmt.Fprintf(&b, "\nThe job names no check, so nothing measured anything and there is no evidence class to "+
			"deliver under (Q-02). The edit command %v exited %d; whether that was the right change is a question "+
			"nothing here asked.\n", job.Command, editCode)
	case blocking == 0:
		fmt.Fprintf(&b, "\nEvery check the job names is non-blocking, so none of them decides. A delivery rests on a "+
			"blocking check that passed (Q-02), and there was none to pass.\n")
	case len(failed) == 0:
		fmt.Fprintf(&b, "\nEvery blocking check passed, so the job delivers under the acceptance criterion it named: "+
			"%s.\n", pipe.Places.Acceptance)
	default:
		fmt.Fprintf(&b, "\nThe acceptance criterion %s was not reached. Still failing after the last round:\n",
			pipe.Places.Acceptance)
		for _, r := range failed {
			fmt.Fprintf(&b, "  %-16s exit %d   %s\n", r.check.Name, r.exitCode, strings.Join(r.check.Command, " "))
		}
	}

	if len(pipe.Places.Checks) > 0 {
		b.WriteString("\nPer round:\n")
		for _, r := range rounds {
			fmt.Fprintf(&b, "  round %d  %s\n", r.number, describe(r.results))
		}
	}

	if len(failed) > 0 {
		b.WriteString("\nWhat it would have needed:\n")
		if len(rounds) >= 2 && rounds[len(rounds)-1].outputHash == rounds[len(rounds)-2].outputHash {
			b.WriteString("  The checks produced byte-identical output in the last two rounds, so the loop had " +
				"stopped making progress before it ran out of rounds. Another round would have produced the same " +
				"reply, later.\n")
		}
		fmt.Fprintf(&b, "  No agent read the check output. Until the model of stage 5 exists, the repair phase runs "+
			"the edit command again (decisions/jobs-by-hand.md), so a repair that needs a decision is one this "+
			"platform cannot make yet. The diff and the logs beside this assessment are what a human reads instead.\n")
		if rounds[len(rounds)-1].number >= pipe.Places.ReworkRounds && pipe.Places.ReworkRounds > 0 {
			fmt.Fprintf(&b, "  The loop ended on its bound, not on a decision: class %s allows at most %d rework "+
				"rounds (decisions/OP-2.md).\n", job.Class, pipe.Places.ReworkRounds)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// runCommand runs one command in the working copy. The environment is the pod's own, which is the
// one the runner built from the allocation (SP-RC-5) — inherited on purpose, because inside the pod
// there is nothing else it could have come from.
func runCommand(argv []string, dir string) ([]byte, int, error) {
	if len(argv) == 0 {
		return nil, -1, errors.New("no command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if len(out) > outputTail {
		out = append([]byte("… truncated to the last 64 KiB …\n"), out[len(out)-outputTail:]...)
	}
	if err == nil {
		return out, 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out, exit.ExitCode(), nil
	}
	return out, -1, err
}
