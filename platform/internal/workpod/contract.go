package workpod

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/Cheety/warft/platform/internal/allocation"
	"github.com/Cheety/warft/platform/internal/cgroup"
)

// applyContract writes R-A into a pod's cgroup. It runs between `runc create` and `runc start`
// (decisions/pod-runtime.md §2), which is the only moment at which the contract is in force for the
// pod's *first* instruction rather than for its second.
//
// Every write is read back (internal/cgroup.Write). A contract that did not land is worse than no
// contract, because from the outside it looks like one.
//
// What is deliberately not written: `memory.max` and `cpu.max`. SP-RA-2 and SP-RA-3 rule them out,
// and their absence is the difference between a pod that is throttled and one that is shot.
func applyContract(cgPath string, a allocation.Allocation, workDev string) error {
	for _, kv := range [][2]string{
		// The request, guaranteed: a share of the CPU (SP-RA-3) and memory the kernel may not
		// reclaim (SP-RA-1's "requested").
		{"cpu.weight", strconv.FormatInt(a.CPUWeight, 10)},
		{"memory.min", strconv.FormatInt(a.MemoryMin, 10)},
		// The limit, tolerated: above this the pod is throttled and reclaimed against (SP-RA-2).
		{"memory.high", strconv.FormatInt(a.MemoryHigh, 10)},
		// The fork wall (SP-RA-4, AB-RA-4).
		{"pids.max", strconv.FormatInt(a.PidsMax, 10)},
	} {
		if err := cgroup.Write(cgPath, kv[0], kv[1]); err != nil {
			return err
		}
	}

	major, minor, err := deviceNumbers(workDev)
	if err != nil {
		return fmt.Errorf("io.latency needs the disk the working copy sits on: %w", err)
	}
	if err := cgroup.Write(cgPath, "io.latency", a.IOLatency(major, minor)); err != nil {
		return err
	}

	// SP-RC-4's net beneath the whole contract: the pod dies whole or not at all. A pod that lost
	// one process to the OOM killer and kept the rest is neither running nor terminal, and K-02 has
	// no state for that.
	return cgroup.ArmOOMGroupPath(cgPath)
}

// readContract is the same five knobs, read back out of a live pod. The acceptance uses it, and so
// does `workpod pod status`: R-A is a claim about a running pod, and the only honest way to answer
// it is from the pod's own cgroup.
func readContract(cgPath string) (map[string]string, error) {
	out := map[string]string{}
	for _, f := range []string{"cpu.weight", "memory.min", "memory.high", "memory.max", "cpu.max", "pids.max", "io.latency", "memory.oom.group"} {
		v, err := cgroup.Read(cgPath, f)
		if err != nil {
			// memory.max and cpu.max are read to show they were *not* set. On a kernel that does
			// not carry one of them, absence is the same answer as "max".
			if os.IsNotExist(err) {
				out[f] = "max"
				continue
			}
			return nil, err
		}
		out[f] = v
	}
	return out, nil
}

// deviceNumbers is the major:minor of the block device a path lives on.
//
// Not `stat` on the directory: btrfs reports an anonymous device there, which is the same trap the
// disk step hit when it compared mounts by major:minor (internal/disk). The mount's source path is
// the identity that means something, so it is resolved first and stat'ed as a device node.
func deviceNumbers(path string) (uint32, uint32, error) {
	out, err := exec.Command("findmnt", "--noheadings", "--output", "SOURCE", "--target", path).Output()
	if err != nil {
		return 0, 0, fmt.Errorf("findmnt --target %s: %w", path, err)
	}
	src := strings.TrimSpace(string(out))
	// A btrfs mount reports its source with the subvolume appended: /dev/vdb1[/subvol].
	if i := strings.Index(src, "["); i > 0 {
		src = src[:i]
	}
	var st syscall.Stat_t
	if err := syscall.Stat(src, &st); err != nil {
		return 0, 0, fmt.Errorf("stat %s: %w", src, err)
	}
	rdev := uint64(st.Rdev)
	// The kernel's own encoding, the one /proc/partitions and io.latency use.
	major := uint32((rdev >> 8) & 0xfff)
	minor := uint32(rdev&0xff | ((rdev >> 12) & 0xfff00))
	if major == 0 {
		return 0, 0, fmt.Errorf("%s is not a block device — io.latency has nothing to name", src)
	}
	return major, minor, nil
}
