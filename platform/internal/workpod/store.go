// Package workpod is the one implementation of the runner contract that exists: a container on this
// node (SP-T04-1 through SP-T04-5). T-04 says the abstraction is over `Runner` and not over
// `Workpod`, so everything operating-system specific is here and nothing of it is in
// internal/runner.
//
// What this package does, in the order it does it:
//
//	resolve   the requirement hash against the image index; a miss is a build job (SP-T03-1)
//	snapshot  the working copy off its base, in O(1), on btrfs (SP-T04-1, SP-A05-2)
//	create    an OCI bundle and `runc create` — the container exists and has not run
//	contract  R-A's knobs into the pod's cgroup, before the first instruction (SP-RA-1…4)
//	start     `runc start` — active
//	watch     45 s of quiet freezes it, the lifetime and the idle limit reap it (SP-T04-3, T04-5)
//	reap      the patch out, the container gone, the subvolume deleted
//
// runc is driven as a program, not as a library, and containerd is not in the path at all. The
// ruling for that, with the condition that overturns it, is decisions/pod-runtime.md §1.
package workpod

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Store is where a node keeps images, bases, working copies and running pods. The three roots are
// SP-A05-1's areas: the work disk holds everything reproducible, /run holds what dies with the boot,
// /var holds what must survive a worker restart.
type Store struct {
	Work string // /data/work — images, index, bases, working copies
	Run  string // /run/workpod — bundles, sockets, lifecycle logs
	Var  string // /var/lib/workpod — build jobs
}

// Default is the layout of a node.
func Default() Store {
	return Store{Work: "/data/work", Run: "/run/workpod", Var: "/var/lib/workpod"}
}

// PodDir is the working copy of a pod: the CoW snapshot, on the work disk.
func (s Store) PodDir(podID string) string { return filepath.Join(s.Work, "pods", podID) }

// RunDir is a pod's volatile side: the OCI bundle, the harness socket, the lifecycle log, the
// output. On /run because none of it means anything after a reboot — and because a socket on a
// disk that survives a restart is a socket that outlives the process behind it.
func (s Store) RunDir(podID string) string { return filepath.Join(s.Run, "pods", podID) }

// BaseDir is a working-copy base — a checkout a pod's working copy is snapshotted from.
func (s Store) BaseDir(key string) string { return filepath.Join(s.Work, "bases", key) }

// KeptDir is where place seven of T-05 leaves the working copy of a pod that did not deliver. It is
// deliberately not under `pods`: the reaper sweeps that directory against the containers runc knows
// about (SP-T04-5), so a kept working copy left there would be an orphan by the next minute.
func (s Store) KeptDir(podID string) string { return filepath.Join(s.Work, "kept", podID) }

// Keep is place seven, applied: a read-only snapshot of a working copy that is about to be reaped.
//
// A snapshot rather than a copy, for the reason Snapshot itself is one — btrfs shares the extents,
// so keeping a failed working copy costs the metadata of one subvolume and not the size of the tree.
// Read-only, because what it is for is being read: a tree someone can still edit is not evidence of
// what the pod left behind.
func (s Store) Keep(podID string) (string, error) {
	dst := s.KeptDir(podID)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	if err := subvolumeSnapshotReadOnly(s.PodDir(podID), dst); err != nil {
		return "", err
	}
	return dst, nil
}

// EnsureBase makes a base subvolume out of a directory, so that a working copy can be snapshotted
// off it. Preparing bases from a repository is the `prepare` phase of T-05 (AP-3.4); this is the
// mechanism underneath it.
func (s Store) EnsureBase(key, from string) (string, error) {
	dst := s.BaseDir(key)
	if isSubvolume(dst) {
		return dst, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if err := subvolumeCreate(dst); err != nil {
		return "", err
	}
	if from != "" {
		if err := copyTree(from, dst); err != nil {
			return "", err
		}
	}
	return dst, nil
}

// Snapshot is SP-T04-1's "working copy as a CoW snapshot in O(1)". btrfs shares the extents, so the
// cost is the metadata of one subvolume and not the size of the tree — which is the property AB-A06-2
// measured on this filesystem before anything depended on it.
//
// An empty base is not a missing base: a job with nothing to check out gets an empty subvolume, and
// that is still a snapshot in the sense that reaping it is one delete.
func (s Store) Snapshot(base, podID string) (string, error) {
	dst := s.PodDir(podID)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if base == "" {
		return dst, subvolumeCreate(dst)
	}
	if !isSubvolume(base) {
		return "", fmt.Errorf("%s is not a btrfs subvolume — a working copy is a snapshot in O(1) or it is a copy (SP-T04-1, SP-A05-2)", base)
	}
	return dst, subvolumeSnapshot(base, dst)
}

// Pods is every working copy on the disk, whether or not anything is running in it. The reaper's
// first list: what is on the disk, against what runc knows.
func (s Store) Pods() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.Work, "pods"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// -------------------------------------------------------------------------------------------------
// btrfs. Driven as programs for the same reason runc is: btrfs-progs is in the image (SP-A02-3) and
// the ioctls behind it would be a second implementation of a tool that is already there.
// -------------------------------------------------------------------------------------------------

func subvolumeCreate(path string) error {
	return run("btrfs", "subvolume", "create", path)
}

func subvolumeSnapshot(src, dst string) error {
	return run("btrfs", "subvolume", "snapshot", src, dst)
}

func subvolumeSnapshotReadOnly(src, dst string) error {
	return run("btrfs", "subvolume", "snapshot", "-r", src, dst)
}

func subvolumeSetReadOnly(path string, ro bool) error {
	return run("btrfs", "property", "set", path, "ro", map[bool]string{true: "true", false: "false"}[ro])
}

// subvolumeDelete removes a working copy. Read-only subvolumes are made writable first: a snapshot
// sealed read-only (an image skeleton) cannot be deleted while it is, and the reaper may not leave
// one behind because of it.
func subvolumeDelete(path string) error {
	if !isSubvolume(path) {
		return os.RemoveAll(path)
	}
	_ = subvolumeSetReadOnly(path, false)
	// Nested subvolumes are deleted first: btrfs refuses to delete a subvolume that contains one,
	// and a working copy of a repository with a snapshot inside it is the ordinary case once
	// AP-4.2's `snapshot.create` handle exists.
	if out, err := exec.Command("btrfs", "subvolume", "list", "-o", path).Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			i := strings.Index(line, " path ")
			if i < 0 {
				continue
			}
			nested := strings.TrimSpace(line[i+len(" path "):])
			_ = run("btrfs", "subvolume", "delete", filepath.Join(mountRoot(path), nested))
		}
	}
	return run("btrfs", "subvolume", "delete", path)
}

// mountRoot is the mount point the given path lies under; `btrfs subvolume list -o` reports nested
// subvolumes relative to it, not to the path asked about.
func mountRoot(path string) string {
	out, err := exec.Command("findmnt", "--noheadings", "--output", "TARGET", "--target", path).Output()
	if err != nil {
		return "/"
	}
	return strings.TrimSpace(string(out))
}

func isSubvolume(path string) bool {
	return exec.Command("btrfs", "subvolume", "show", path).Run() == nil
}

// copyTree copies a directory into an existing one, sharing extents where the filesystem can
// (--reflink=auto). Used for skeletons and bases, never for a working copy: a working copy is a
// snapshot, and copying one would be the O(n) that SP-T04-1 rules out.
func copyTree(src, dst string) error {
	return run("cp", "--archive", "--reflink=auto", src+"/.", dst)
}

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
