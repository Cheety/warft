package workpod

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// SweepInterval is how often the reaper looks. Often enough that an orphan is measured in minutes
// rather than in days, rarely enough that it costs nothing: the sweep is two directory listings and
// one `runc list`.
const SweepInterval = time.Minute

// Reaper is SP-T04-5, and SP-T04-5 is the reason this platform does not fill its disks: *every pod
// has a lifetime and an idle limit, and the reaper runs on the worker, not in the control plane*
// (V-02).
//
// It reaps what no supervisor is watching. A pod with a live supervisor is somebody's — the loop in
// Workpod.watch enforces that pod's lifetime and idle limit and delivers its patch. A pod whose
// supervisor is gone has neither: nobody will collect its result, nobody holds its lease, and its
// subvolume is the "ten thousand orphaned subvolumes after a week" T-04 names as the most likely way
// a platform like this tips over.
//
// After a worker restart that is *every* pod on the node, which is what AB-T04-5 measures.
type Reaper struct {
	Pod *Workpod
	Log func(format string, a ...any)
}

// Sweep reaps every pod whose supervisor is gone and returns their ids. It is the first thing a
// worker does and then the thing it does every minute.
//
// Two lists are compared: the working copies on the disk, and the containers runc knows about. Both
// directions matter — a subvolume without a container is a pod that was half created, a container
// without a subvolume is one that was half reaped, and neither may stay.
func (r *Reaper) Sweep() ([]string, error) {
	pods, err := r.Pod.Store.Pods()
	if err != nil {
		return nil, err
	}
	known, err := containers()
	if err != nil {
		// runc not answering is not "no containers". Reaping the disk on that assumption would
		// delete the working copy out from under a running pod.
		return nil, fmt.Errorf("asking runc what is running: %w", err)
	}

	seen := map[string]bool{}
	var orphans []string
	for _, id := range pods {
		seen[id] = true
		if r.supervised(id) {
			continue
		}
		orphans = append(orphans, id)
	}
	for id := range known {
		if seen[id] || r.supervised(id) {
			continue
		}
		orphans = append(orphans, id)
	}

	var reaped []string
	for _, id := range orphans {
		if err := r.Pod.reap(id, nil); err != nil {
			r.logf("reaping the orphan %s: %v", id, err)
			continue
		}
		reaped = append(reaped, id)
		r.logf("reaped the orphan %s — its supervisor is gone (SP-T04-5)", id)
	}
	return reaped, nil
}

// Run is the reaper as the worker holds it: once at start, then every minute until the worker stops.
func (r *Reaper) Run(ctx context.Context) {
	tick := time.NewTicker(SweepInterval)
	defer tick.Stop()
	for {
		if reaped, err := r.Sweep(); err != nil {
			r.logf("sweep: %v", err)
		} else if len(reaped) > 0 {
			r.logf("%d orphaned pod(s) reaped", len(reaped))
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// supervised reports whether some living process wrote its pid beside this pod and is still there.
//
// The pid alone would not be enough — pids are reused, and a reaper that spared a pod because an
// unrelated process inherited its number would spare it forever. The command line is checked too,
// which is cheap and makes the mistake need two coincidences instead of one.
func (r *Reaper) supervised(podID string) bool {
	b, err := os.ReadFile(filepath.Join(r.Pod.Store.RunDir(podID), "supervisor.pid"))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return false
	}
	if pid == os.Getpid() {
		return true
	}
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	return strings.Contains(strings.ReplaceAll(string(cmdline), "\x00", " "), "workpod")
}

func (r *Reaper) logf(format string, a ...any) {
	if r.Log != nil {
		r.Log(format, a...)
	}
}
