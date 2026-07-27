// Package harness is the pod's half of the runner contract: the same binary the worker runs,
// mounted read-only into every pod and started as its init process (SP-E02-4, SP-T04-1).
//
// It is deliberately small, and the boundary is AP-3.3's own: *no agent, no model*. What it does is
// read the job it was given, run it in the working copy, say what happened, and then wait to be
// frozen or reaped. Choosing what to run — plan, edit, check, repair — is the pipeline of T-05
// (AP-3.4), and the model that would drive it is stage 5's.
//
// It talks to exactly one thing: the Unix socket at /run/workpod/harness.sock. There is no network
// in the pod, no key and no token (SP-T04-2), so anything the pod cannot do with its own files goes
// through that socket or does not happen.
package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	workpodv1 "github.com/Cheety/warft/platform/api/workpodv1"
	"github.com/Cheety/warft/platform/internal/runner"
)

// outputTail is how much of a job's output travels in the report. SP-T04-2 forbids logs in the pod
// and B-03 sends them to the node as they happen (AP-3.8); until that path exists the tail is what
// makes a failure readable, and a bound is what keeps a runaway job from writing its report into
// the machine's memory.
const outputTail = 64 << 10

// zombieInterval is how often PID 1 collects what was reparented onto it. A pod's process table is
// bounded by pids.max (SP-RA-4), and an uncollected zombie holds a slot in it.
const zombieInterval = 5 * time.Second

// consoleFile is where the pod's own standard output and error go. It lies in the output directory,
// which is a bind mount from the host, so what the pod says is on the host the moment it is said
// (SP-T04-2: no logs in the pod).
const consoleFile = runner.PodOutDir + "/console.log"

// Run is `workpod harness`, inside the pod.
func Run() error {
	if err := redirectStdio(); err != nil {
		return err
	}
	job, err := readJob(runner.PodJobFile)
	if err != nil {
		return err
	}

	rep := runner.Report{OrderID: job.OrderID, Attempt: job.Attempt}
	var notes []string
	notes = append(notes, "harness socket: "+socketState())

	start := time.Now()
	output, code, runErr := runJob(job)
	rep.ExitCode = code

	switch {
	case runErr != nil && code < 0:
		rep.FinalState, rep.Cause = "failed", "tool.failure"
		notes = append(notes, "the command could not be started: "+runErr.Error())
	case code != 0:
		rep.FinalState, rep.Cause = "failed", "tool.failure"
		notes = append(notes, fmt.Sprintf("%v exited %d after %s", job.Command, code, time.Since(start).Round(time.Millisecond)))
	default:
		// Q-02: confidence is not an acceptance criterion, evidence is. The command succeeded and
		// nothing measured anything, so the honest terminal state is `unproven` — the capability
		// that would produce an evidence class is the check phase of T-05 and the handles of F-03,
		// and neither is built (AP-3.4, AP-4.2).
		rep.FinalState, rep.Cause = "unproven", "skill.missing"
		notes = append(notes, fmt.Sprintf("%v exited 0 after %s; no check ran, so there is no evidence class (Q-02)",
			job.Command, time.Since(start).Round(time.Millisecond)))
	}
	if len(output) > 0 {
		notes = append(notes, "--- output ---", string(output))
	}
	rep.Text = strings.Join(notes, "\n")

	if err := writeReport(rep); err != nil {
		return err
	}

	// The pod does not end here. SP-T04-3 gives it three more states — quiet, then frozen, then
	// checkpointed — and a harness that exited would take that away by turning every pod into a
	// container that stopped. The supervisor outside decides when the pod is over; from in here the
	// job is done and there is nothing left to do but wait quietly enough to be noticed.
	idle()
	return nil
}

// redirectStdio reopens the pod's three standard descriptors from inside the pod.
//
// It inherits them from `runc create`, which means they are open on *host* paths: descriptors whose
// mount is not in the pod's own mount table. CRIU dumps a process by walking its open files and
// resolving each one against the mount table it can see, so an inherited host descriptor makes the
// dump fail with "Can't lookup mount for fd=1" — and SP-T04-3's `checkpointed` would be unreachable
// for every pod on the platform. Reopening them here, through the bind mount the pod does know,
// costs two system calls and makes the fourth state of the lifecycle possible.
//
// It changes nothing about where the output goes: the file is the same file, on the host, in the
// directory the runner reads after the pod is over.
func redirectStdio() error {
	null, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer null.Close()
	console, err := os.OpenFile(consoleFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("the pod has no console at %s: %w", consoleFile, err)
	}
	defer console.Close()
	for _, d := range [][2]int{
		{int(null.Fd()), int(os.Stdin.Fd())},
		{int(console.Fd()), int(os.Stdout.Fd())},
		{int(console.Fd()), int(os.Stderr.Fd())},
	} {
		if err := syscall.Dup3(d[0], d[1], 0); err != nil {
			return fmt.Errorf("reopening descriptor %d: %w", d[1], err)
		}
	}
	return nil
}

func readJob(path string) (runner.Job, error) {
	var job runner.Job
	b, err := os.ReadFile(path)
	if err != nil {
		return job, fmt.Errorf("no job at %s — the harness runs in a pod, not on a node: %w", path, err)
	}
	if err := json.Unmarshal(b, &job); err != nil {
		return job, fmt.Errorf("%s: %w", path, err)
	}
	return job, job.Validate()
}

// runJob runs the job's command in the working copy. The environment is the pod's own, which is the
// one the runner built from the allocation (SP-RC-5) — inherited here on purpose, because inside the
// pod there is nothing else it could have come from.
func runJob(job runner.Job) ([]byte, int, error) {
	cmd := exec.Command(job.Command[0], job.Command[1:]...)
	cmd.Dir = runner.PodWorkDir
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

func writeReport(rep runner.Report) error {
	body, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	// Written beside the socket, in the one directory the pod may write into that the host reads.
	// The patch is not written here: the supervisor computes it from the base and the working copy,
	// both of which are outside the pod, so what the pod changed is measured rather than claimed.
	return os.WriteFile(runner.PodReportFile, append(body, '\n'), 0o644)
}

// socketState says whether the pod's only way out is there and speaking the contract.
//
// It calls a method that is not built yet on purpose. `Unimplemented` from the server is a complete
// answer: the socket exists, a gRPC server is behind it, and the method it named is one AP-4.1
// builds. A dial error or a transport error is the opposite, and the difference between the two is
// exactly what SP-T04-2 is about.
func socketState() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpc.NewClient("unix://"+runner.PodSocket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return "unreachable (" + err.Error() + ")"
	}
	defer conn.Close()

	_, err = workpodv1.NewHarnessClient(conn).QueryFacts(ctx, &workpodv1.FactQuery{})
	if err == nil {
		return "reachable"
	}
	if strings.Contains(err.Error(), "AP-4.1") {
		return "reachable"
	}
	return "unreachable (" + err.Error() + ")"
}

// idle waits to be frozen or reaped, and collects the processes a job left behind while it waits.
// It must be quiet in the sense of SP-T04-3 — under 1 % of one core — or the pod would never reach
// `frozen`, which is why this is a sleep and not a poll of anything.
func idle() {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	tick := time.NewTicker(zombieInterval)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			collectZombies()
		}
	}
}

// collectZombies is PID 1's other duty. The harness is the pod's init process, so anything the job
// orphaned is reparented onto it, and a process nobody waits for stays in the table.
func collectZombies() {
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
		if pid <= 0 || err != nil {
			return
		}
	}
}
