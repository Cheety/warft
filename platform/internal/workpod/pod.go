package workpod

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Cheety/warft/platform/internal/allocation"
	"github.com/Cheety/warft/platform/internal/cgroup"
	"github.com/Cheety/warft/platform/internal/runner"
)

// The four numbers of decisions/pod-runtime.md §4. They are constants and not configuration: a node
// that could be told to keep pods alive longer than the platform's own ruling would be a node whose
// operator can defeat the reaper by editing a file.
const (
	// FreezeAfter is SP-T04-3's own number.
	FreezeAfter = 45 * time.Second
	// IdleLimit is three orders of magnitude above it, because freezing is reversible and reaping
	// is not.
	IdleLimit = 15 * time.Minute
	// DefaultLifetime applies to a job that carries no pod_minutes budget; the pots are AP-3.6's.
	DefaultLifetime = 60 * time.Minute
	// quietCPUMicrosPerSecond is 1 % of one core. Not zero: an idle process is not a stopped one,
	// and a runtime that wakes a monitor thread every few milliseconds would never be quiet under a
	// threshold of zero.
	quietCPUMicrosPerSecond = 10_000
)

// ReapAfter says where a pod's life ends.
type ReapAfter string

const (
	// AfterQuiet is SP-T04-3's chain in full: the pod is left running after it has delivered, and
	// 45 s of quiet takes it through frozen and checkpointed to reaped.
	AfterQuiet ReapAfter = "quiet"
	// AfterReport ends the pod the moment its report is in. Which of the two a pod gets is the
	// pipeline's to decide once there is one (AP-3.4): a pod between two phases waits, a pod that
	// has finished its last phase does not.
	AfterReport ReapAfter = "report"
)

// Workpod is the runner of the `alpine` pool: a container on this node. T-04 is explicit that the
// abstraction is over Runner and not over this type, which is why everything below is allowed to
// know about runc, btrfs and cgroups and nothing above it is.
type Workpod struct {
	Store Store
	// Reap decides the end of the lifecycle; AfterQuiet is the panel's.
	Reap ReapAfter
	// Log is where the lifecycle is narrated. A pod's transitions are operational facts — B-03
	// makes them spans of a trace in AP-3.8 — so they are said out loud rather than kept.
	Log func(format string, a ...any)

	mu      sync.Mutex
	sockets map[string]*harnessSocket
}

// New is a runner over the node's own layout.
func New(s Store) *Workpod {
	return &Workpod{Store: s, Reap: AfterQuiet, Log: func(string, ...any) {}, sockets: map[string]*harnessSocket{}}
}

// Platform is SP-T04-4's pool. One runner, one pool: `windows`, `macos` and `remote` are AP-8.3's
// and satisfy this same interface.
func (w *Workpod) Platform() runner.Platform { return runner.Alpine }

// life carries a pod's lifecycle as it happens.
type life struct {
	states []runner.Transition
	log    func(format string, a ...any)
}

func (l *life) at(s runner.State, reason string) error {
	if len(l.states) > 0 {
		from := l.states[len(l.states)-1].State
		if !runner.CanTransition(from, s) {
			return fmt.Errorf("a pod does not go from %s to %s (SP-T04-3)", from, s)
		}
	} else if s != runner.Created {
		return fmt.Errorf("a pod's life begins at %s, not at %s (SP-T04-3)", runner.Created, s)
	}
	l.states = append(l.states, runner.Transition{State: s, At: time.Now().UTC(), Reason: reason})
	l.log("pod %s: %s", s, reason)
	return nil
}

// Run is the runner contract: given a working directory and a job, deliver a patch and a report.
//
// The order of the steps is the whole of T-04, and each of them is where one requirement lands:
// resolve (SP-T03-1) · snapshot (SP-T04-1) · create (SP-T04-3) · contract (SP-RA-1…4) · start ·
// watch (SP-T04-3, SP-T04-5) · reap.
// The return values are named because the two deferred blocks below fill them in: a pod's lifetime
// and its lifecycle are known only after it is over, including on the paths that end early.
func (w *Workpod) Run(ctx context.Context, base string, job runner.Job) (rep runner.Report, err error) {
	rep = runner.Report{OrderID: job.OrderID, Attempt: job.Attempt}
	if err := job.Validate(); err != nil {
		return rep, err
	}
	if job.Platform != "" && job.Platform != runner.Alpine {
		return rep, runner.NotBuilt(job.Platform)
	}
	alloc, err := allocation.For(allocation.Class(job.Class))
	if err != nil {
		return rep, err
	}

	// SP-T03-1 first, before anything is created: a miss must not leave a subvolume behind for an
	// image that does not exist.
	manifest, err := w.Store.Resolve(job)
	if err != nil {
		return rep, err
	}
	rep.ImageDigest = manifest.Digest()

	podID := PodID(job)
	l := &life{log: w.logf}
	started := time.Now()

	defer func() {
		rep.PodSeconds = uint64(time.Since(started).Seconds())
		rep.Lifecycle = l.states
	}()

	// Everything from here on has something to clean up, so every path out of it goes through the
	// reaper. That is not tidiness: a pod that fails between the snapshot and the first instruction
	// is exactly the pod whose subvolume nobody would think to delete.
	defer func() {
		if err := w.reap(podID, l); err != nil {
			w.logf("reaping %s: %v", podID, err)
		}
	}()

	if err := w.prepare(podID, base, job, manifest, alloc); err != nil {
		return rep, err
	}
	if err := l.at(runner.Created, "the snapshot stands and the container exists"); err != nil {
		return rep, err
	}

	cgPath, err := w.contract(podID, alloc)
	if err != nil {
		return rep, err
	}
	if err := runcCmd("start", podID); err != nil {
		return rep, fmt.Errorf("starting %s: %w", podID, err)
	}
	if err := l.at(runner.Active, "the harness is running under R-A's contract"); err != nil {
		return rep, err
	}
	// The clock started before the image was resolved, so this is the whole of what SP-T03-1 calls
	// starting: resolve, snapshot, bundle, create, contract, start.
	rep.StartMillis = time.Since(started).Milliseconds()
	w.logf("pod %s: started in %d ms (SP-T03-1)", podID, rep.StartMillis)

	delivered, endCause, err := w.watch(ctx, podID, cgPath, job, l)
	if err != nil {
		return rep, err
	}
	rep = w.collect(rep, podID, base, delivered, endCause)
	return rep, nil
}

// PodID is a pod's name on the node and its container id in runc. Order and attempt, because a
// retry is a second pod and not the same one (SP-K01-1's attempt).
func PodID(job runner.Job) string {
	return fmt.Sprintf("%s-%d", job.OrderID, job.Attempt)
}

// prepare is everything up to a container that exists and has not run.
func (w *Workpod) prepare(podID, base string, job runner.Job, m Manifest, a allocation.Allocation) error {
	runDir := w.Store.RunDir(podID)
	podRun := filepath.Join(runDir, "pod")
	if err := os.MkdirAll(filepath.Join(podRun, "out"), 0o755); err != nil {
		return err
	}
	// The supervisor's own pid, so the reaper can tell a pod that is being watched from one whose
	// watcher died (SP-T04-5, decisions/pod-runtime.md §4).
	if err := os.WriteFile(filepath.Join(runDir, "supervisor.pid"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		return err
	}

	body, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(podRun, "job.json"), append(body, '\n'), 0o444); err != nil {
		return err
	}

	// The socket before the container: the pod's only way out has to be there when the pod's first
	// instruction runs, not shortly after (SP-T04-2).
	sock, err := w.serveHarness(podID, job, filepath.Join(podRun, "harness.sock"))
	if err != nil {
		return err
	}
	w.mu.Lock()
	w.sockets[podID] = sock
	w.mu.Unlock()

	if _, err := w.Store.Snapshot(base, podID); err != nil {
		return err
	}
	bundle, err := w.Store.writeBundle(podID, m, job, a)
	if err != nil {
		return err
	}
	return runcCreate(podID, bundle, filepath.Join(runDir, "init.pid"), filepath.Join(podRun, "out", "console.log"))
}

// contract writes R-A into the pod's cgroup and hands back the path, which the quiet detector then
// reads CPU time from.
func (w *Workpod) contract(podID string, a allocation.Allocation) (string, error) {
	pid, err := initPID(w.Store.RunDir(podID))
	if err != nil {
		return "", err
	}
	cgPath, err := cgroup.OfPID(pid)
	if err != nil {
		return "", err
	}
	if err := applyContract(cgPath, a, w.Store.Work); err != nil {
		return "", err
	}
	// Read back out of the cgroup and said out loud, including the two knobs that must be absent:
	// R-A is a claim about a running pod, and the honest way to state it is with the values the
	// kernel holds rather than the ones the program meant to write (SP-RA-1, AB-RA-1).
	held, err := readContract(cgPath)
	if err != nil {
		return "", err
	}
	keys := make([]string, 0, len(held))
	for k := range held {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, k+"="+held[k])
	}
	w.logf("pod %s: cgroup %s", podID, cgPath)
	w.logf("pod %s: contract %s %s", podID, a.Class, strings.Join(parts, " "))
	return cgPath, nil
}

// watch is SP-T04-3 and SP-T04-5 in one loop: it waits for the report, freezes on quiet, and ends
// the pod on its lifetime or its idle limit.
//
// It returns whether the pod delivered and, where the pod's life was ended from outside, the cause
// to report it under. A pod that hit its lifetime without delivering has not failed silently:
// SP-K02-3 wants a cause in every terminal state, and this is where the two that come from the
// reaper's side are named.
func (w *Workpod) watch(ctx context.Context, podID, cgPath string, job runner.Job, l *life) (bool, string, error) {
	reportFile := filepath.Join(w.Store.RunDir(podID), "pod", "out", "report.json")
	lifetime := DefaultLifetime
	if job.PodMinutes > 0 {
		lifetime = time.Duration(job.PodMinutes) * time.Minute
	}
	deadline := time.Now().Add(lifetime)

	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	var (
		delivered  bool
		lastCPU    uint64
		busySince  = time.Now()
		haveSample bool
	)
	for {
		select {
		case <-ctx.Done():
			return delivered, "", ctx.Err()
		case <-tick.C:
		}

		if !delivered {
			if _, err := os.Stat(reportFile); err == nil {
				delivered = true
				w.logf("pod %s: the report is in", podID)
				if w.Reap == AfterReport {
					return true, "", nil
				}
			}
		}

		if time.Now().After(deadline) {
			// Reaping happens in Run's defer; saying why here is what keeps a terminal state from
			// being without a cause (SP-K02-3).
			w.logf("pod %s: past its lifetime of %s", podID, lifetime)
			return delivered, "budget.exhausted", nil
		}

		cpu, err := cgroup.CPUMicros(cgPath)
		if err != nil {
			// The cgroup is gone: the container ended on its own.
			if !containerExists(podID) {
				w.logf("pod %s: the container ended", podID)
				return delivered, "", nil
			}
			return delivered, "", err
		}
		busy := !haveSample || cpu-lastCPU > quietCPUMicrosPerSecond
		lastCPU, haveSample = cpu, true
		w.mu.Lock()
		called := w.sockets[podID].lastCallAfter(busySince)
		w.mu.Unlock()
		if busy || called {
			busySince = time.Now()
			continue
		}

		quiet := time.Since(busySince)
		if quiet >= IdleLimit {
			// The idle limit is a budget of time like the lifetime is, spent on nothing rather
			// than on work (decisions/pod-runtime.md §4).
			w.logf("pod %s: idle for %s", podID, IdleLimit)
			return delivered, "budget.exhausted", nil
		}
		if quiet >= FreezeAfter && currentState(podID) == "running" {
			// A failed freeze or checkpoint ends the pod; it does not lose what the pod produced.
			// The patch and the report are what the job is for, and the last two states of
			// SP-T04-3's chain are how the pod stops — a dump that could not be taken must not
			// take a delivered result with it.
			if err := w.freezeAndCheckpoint(podID, l, quiet); err != nil {
				w.logf("pod %s: %v", podID, err)
			}
			return delivered, "", nil
		}
	}
}

// freezeAndCheckpoint is the second half of SP-T04-3's chain: frozen, then dumped to disk. Reaping
// follows in Run's defer, which is where every path out of a pod's life goes.
func (w *Workpod) freezeAndCheckpoint(podID string, l *life, quiet time.Duration) error {
	if err := runcCmd("pause", podID); err != nil {
		return fmt.Errorf("freezing %s: %w", podID, err)
	}
	if err := l.at(runner.Frozen, fmt.Sprintf("quiet for %s", quiet.Round(time.Second))); err != nil {
		return err
	}

	dir := filepath.Join(w.Store.RunDir(podID), "checkpoint")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// `--manage-cgroups-mode=ignore` tells CRIU to dump no cgroup properties. That is not a
	// concession: the pod's contract is written by the runner between `runc create` and `runc
	// start` on every pod it makes (decisions/pod-runtime.md §2), so a dump that carried R-A's
	// knobs would carry a copy of something that is rewritten from the class anyway — and on a
	// node, where the cgroup is a systemd scope, dumping it is what fails.
	if err := runcCmd("checkpoint", "--manage-cgroups-mode", "ignore",
		"--image-path", dir, "--work-path", dir, podID); err != nil {
		return fmt.Errorf("checkpointing %s: %w\n%s", podID, err, tail(filepath.Join(dir, "dump.log")))
	}
	return l.at(runner.Checkpointed, "dumped to disk (CRIU)")
}

// collect takes the two halves of what a pod delivers: the report it wrote, and the patch the
// supervisor computes from the base and the working copy.
//
// The patch is made **outside** the pod, from two trees the pod cannot reach — the base subvolume
// and its own snapshot, both on the host. What the pod changed is therefore measured rather than
// claimed, and a pod cannot hand out a patch that does not match what it did. It lands on /var,
// because it is the job's result and has to outlive the pod that produced it (SP-K03-1's outbox on
// /var is where it goes once AP-3.5 builds one).
func (w *Workpod) collect(rep runner.Report, podID, base string, delivered bool, endCause string) runner.Report {
	out := filepath.Join(w.Store.RunDir(podID), "pod", "out")
	if b, err := os.ReadFile(filepath.Join(out, "report.json")); err == nil {
		var fromPod runner.Report
		if err := json.Unmarshal(b, &fromPod); err == nil {
			rep.FinalState, rep.Cause, rep.Evidence = fromPod.FinalState, fromPod.Cause, fromPod.Evidence
			rep.Text, rep.ExitCode = fromPod.Text, fromPod.ExitCode
		}
	}
	if path, hash, err := w.writePatch(podID, base); err != nil {
		w.logf("pod %s: the patch could not be made: %v", podID, err)
	} else {
		rep.PatchPath, rep.PatchHash = path, hash
	}
	if !delivered && rep.FinalState == "" {
		// SP-K02-3: no terminal state without a cause. A pod that ended without a report ended for
		// a reason, and the cause has to be one of the state contract's words, not a new one.
		rep.FinalState, rep.Cause = "failed", "tool.failure"
		if endCause != "" {
			rep.Cause = endCause
		}
		rep.Text = "the pod ended without leaving a report at " + runner.PodReportFile
	}
	return rep
}

// writePatch is `diff -ruN base workingCopy`, on the host. diff exits 0 when the trees are equal and
// 1 when they differ; anything above that is a failure of the tool and not a property of the trees.
func (w *Workpod) writePatch(podID, base string) (string, string, error) {
	dir := filepath.Join(w.Store.Var, "patches")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	from := base
	if from == "" {
		// A job with no base changed nothing that can be expressed as a difference from something.
		// An empty directory is that something.
		empty, err := os.MkdirTemp("", "workpod-empty-")
		if err != nil {
			return "", "", err
		}
		defer os.RemoveAll(empty)
		from = empty
	}
	cmd := exec.Command("diff", "--recursive", "--unified", "--new-file", "--no-dereference", from, w.Store.PodDir(podID))
	body, err := cmd.Output()
	if code := cmd.ProcessState.ExitCode(); code > 1 {
		return "", "", fmt.Errorf("diff exited %d: %w", code, err)
	}
	path := filepath.Join(dir, podID+".diff")
	// Removed first: the patch is written read-only, and a second attempt on the same pod would
	// otherwise fail against its own earlier output rather than replace it.
	_ = os.Remove(path)
	if err := os.WriteFile(path, body, 0o444); err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(body)
	return path, "sha256:" + hex.EncodeToString(sum[:]), nil
}

// reap is SP-T04-3's last state and SP-T04-5's whole point: the container gone, the subvolume
// deleted, nothing left on the disk. It is idempotent, because the reaper calls it on pods whose
// supervisor never got this far.
func (w *Workpod) reap(podID string, l *life) error {
	w.mu.Lock()
	sock := w.sockets[podID]
	delete(w.sockets, podID)
	w.mu.Unlock()
	if sock != nil {
		sock.close()
	}

	var errs []string
	if containerExists(podID) {
		if err := runcCmd("delete", "--force", podID); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if err := subvolumeDelete(w.Store.PodDir(podID)); err != nil && !os.IsNotExist(err) {
		errs = append(errs, err.Error())
	}
	if err := os.RemoveAll(w.Store.RunDir(podID)); err != nil {
		errs = append(errs, err.Error())
	}
	if l != nil {
		// Best effort: a pod that could not reach `reaped` in its own state machine is one whose
		// life ended in a state the machine does not leave, and that is worth saying rather than
		// worth failing on.
		_ = l.at(runner.Reaped, "patch out, pod gone")
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func (w *Workpod) logf(format string, a ...any) {
	if w.Log != nil {
		w.Log(format, a...)
	}
}

// -------------------------------------------------------------------------------------------------
// runc, driven as a program (decisions/pod-runtime.md §1)
// -------------------------------------------------------------------------------------------------

// runcArgs prefixes runc's global flags. On a node `--systemd-cgroup` is not optional: it makes a
// pod's cgroup a transient scope under workpod-work.slice with delegation, rather than a directory
// created behind systemd's back in a tree systemd owns and prunes. Where systemd is not the init —
// which on a node never happens (SP-A02-4) — runc manages the cgroup itself; see cgroupsPath.
func runcArgs(args []string) []string {
	if systemdManaged() {
		return append([]string{"--systemd-cgroup"}, args...)
	}
	return args
}

func runcCmd(args ...string) error {
	out, err := exec.Command("runc", runcArgs(args)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("runc %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// runcCreate is the one runc call that may not be given pipes.
//
// `runc create` returns as soon as the container exists, but it leaves `runc init` running with the
// container's standard streams — which, with no terminal and no console socket, are the ones it
// inherited. Handing it an os/exec pipe therefore hangs the caller forever: CombinedOutput waits for
// end-of-file on a descriptor the stopped init process is holding open until `runc start`. The first
// run of this code hung there for ten minutes with a container in `created`.
//
// Real files instead, and that fixes a second thing at the same time. The pod's stdout and stderr are
// what it inherits here, so pointing them at a file on the host is SP-T04-2's "no logs in the pod":
// everything the pod says lands outside it as it is said. B-03 gives that stream a job id and a trace
// (AP-3.8); here it is a file beside the pod, gone when the pod is.
//
// The file has to lie in the output directory rather than anywhere else on the host, and that is not
// tidiness either: the output directory is bind-mounted into the pod, so the pod's own descriptor 1
// resolves to a mount its namespace knows about. A console on a path the pod cannot see is one CRIU
// refuses to dump — "Can't lookup mount for fd=1" — and SP-T04-3's `checkpointed` would be
// unreachable for every pod.
func runcCreate(podID, bundle, pidFile, consoleLog string) error {
	console, err := os.OpenFile(consoleLog, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer console.Close()
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer devNull.Close()

	cmd := exec.Command("runc", runcArgs([]string{"create", "--bundle", bundle, "--pid-file", pidFile, podID})...)
	// *os.File rather than an io.Writer: os/exec passes a file's descriptor to the child directly
	// and creates no pipe and no copying goroutine to wait on.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devNull, console, console
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("runc create %s: %w\n%s", podID, err, tail(consoleLog))
	}
	return nil
}

// tail is the last of a pod's console, for a message about why something failed.
func tail(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(b) > 4096 {
		b = b[len(b)-4096:]
	}
	return strings.TrimSpace(string(b))
}

type runcState struct {
	ID     string `json:"id"`
	Pid    int    `json:"pid"`
	Status string `json:"status"`
	Bundle string `json:"bundle"`
}

func containerState(id string) (runcState, error) {
	var s runcState
	out, err := exec.Command("runc", runcArgs([]string{"state", id})...).Output()
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(out, &s)
}

func containerExists(id string) bool {
	_, err := containerState(id)
	return err == nil
}

func currentState(id string) string {
	s, err := containerState(id)
	if err != nil {
		return ""
	}
	return s.Status
}

// containers is every container runc knows about on this node — the reaper's second list, against
// the working copies on the disk.
func containers() (map[string]runcState, error) {
	out, err := exec.Command("runc", runcArgs([]string{"list", "--format", "json"})...).Output()
	if err != nil {
		return nil, err
	}
	// An empty list is `null` rather than `[]`, which unmarshals into a nil slice; that is the
	// no-containers case and not an error.
	var list []runcState
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, err
	}
	m := map[string]runcState{}
	for _, s := range list {
		m[s.ID] = s
	}
	return m, nil
}

func initPID(runDir string) (int, error) {
	b, err := os.ReadFile(filepath.Join(runDir, "init.pid"))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}
