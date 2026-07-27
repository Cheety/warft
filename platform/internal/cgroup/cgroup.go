// Package cgroup reads and writes the cgroup v2 knobs R-C names: memory.min on the system slice
// (SP-RC-4), memory.oom.group on pod slices (SP-RC-4), and the pressure files admission decides
// from (SP-RC-1).
//
// A unit's cgroup is asked from systemd rather than assembled from its name — the calibration run
// (AP-1.3, run 27) established that pattern after assembling a path measured the wrong slice.
package cgroup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const mountPoint = "/sys/fs/cgroup"

// UnitPath asks systemd where a unit's cgroup lives.
func UnitPath(unit string) (string, error) {
	out, err := exec.Command("systemctl", "show", "--property=ControlGroup", "--value", unit).Output()
	if err != nil {
		return "", fmt.Errorf("systemctl show %s: %w", unit, err)
	}
	rel := strings.TrimSpace(string(out))
	if rel == "" {
		return "", fmt.Errorf("unit %s has no cgroup — is it active?", unit)
	}
	return filepath.Join(mountPoint, rel), nil
}

// MemoryMin reads a slice's memory.min in bytes.
func MemoryMin(unit string) (uint64, error) {
	p, err := UnitPath(unit)
	if err != nil {
		return 0, err
	}
	b, err := os.ReadFile(filepath.Join(p, "memory.min"))
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(b))
	if s == "max" {
		return ^uint64(0), nil
	}
	return strconv.ParseUint(s, 10, 64)
}

// ArmOOMGroup sets memory.oom.group=1 on a pod slice (SP-RC-4's "net beneath it") and reads it
// back. With the bit set, the kernel's OOM killer takes the pod as one indivisible workload —
// a pod that loses one process to the OOM killer but keeps the rest is neither running nor
// terminal, and K-02 has no state for that.
//
// systemd exposes no directive for this cgroup attribute, which is why the platform binary owns
// it: the runner (AP-3.3) arms every pod slice it creates through ArmOOMGroupPath below.
func ArmOOMGroup(unit string) error {
	p, err := UnitPath(unit)
	if err != nil {
		return err
	}
	return ArmOOMGroupPath(p)
}

// ArmOOMGroupPath is the same by cgroup path rather than by unit name. The runner knows the path —
// it read it out of the container's own /proc entry — and asking systemd to translate a unit name
// it already resolved would be a round trip for an answer it holds.
func ArmOOMGroupPath(path string) error {
	return Write(path, "memory.oom.group", "1")
}

// Write sets one cgroup attribute and reads it back. Reading back is the point: a cgroup file that
// takes a value the controller then ignores — because the controller is not enabled in the parent,
// or because the value is out of range — fails silently on the write and would leave a pod running
// under a contract nobody applied (SP-RA-1).
//
// The comparison is exact. Every attribute the runner writes reads back verbatim — a byte count as
// the same byte count, an io.latency line as the same line — so anything else means the value did
// not land, and a contract that did not land is worse than none because it looks like one.
func Write(path, file, value string) error {
	f := filepath.Join(path, file)
	if err := os.WriteFile(f, []byte(value+"\n"), 0o644); err != nil {
		return fmt.Errorf("writing %s to %s: %w", value, f, err)
	}
	got, err := Read(path, file)
	if err != nil {
		return err
	}
	if got != value {
		return fmt.Errorf("%s reads %q after writing %q — the controller did not take it", f, got, value)
	}
	return nil
}

// Read is one cgroup attribute, trimmed.
func Read(path, file string) (string, error) {
	b, err := os.ReadFile(filepath.Join(path, file))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// OfPID is the cgroup of a running process, as an absolute path under the cgroup mount. On the
// unified hierarchy /proc/<pid>/cgroup has exactly one line and it begins with `0::`.
//
// This is how the runner finds a pod's cgroup: the container's init process is created and stopped
// before it runs anything (`runc create`), and where that process sits is the one authoritative
// answer — assembling the path from a unit name is what the calibration run already found to be
// wrong (AP-1.3, run 27).
func OfPID(pid int) (string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rel, ok := strings.CutPrefix(line, "0::"); ok {
			return filepath.Join(mountPoint, strings.TrimSpace(rel)), nil
		}
	}
	return "", fmt.Errorf("process %d has no unified cgroup — is cgroup v2 mounted?", pid)
}

// CPUMicros is the CPU time a cgroup has consumed, in microseconds. The quiet detector of SP-T04-3
// reads it: a pod that has not spent CPU is a pod in which nothing is happening.
func CPUMicros(path string) (uint64, error) {
	b, err := os.ReadFile(filepath.Join(path, "cpu.stat"))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "usage_usec "); ok {
			return strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		}
	}
	return 0, fmt.Errorf("no usage_usec in %s/cpu.stat", path)
}

// MemoryEvents is the memory.events map of a cgroup: `high`, `max`, `oom`, `oom_kill`. AB-RA-2
// rests on two of them — a pod above memory.high has a rising `high` count and an `oom_kill` of
// zero, which is throttled rather than shot (SP-RA-2).
func MemoryEvents(path string) (map[string]uint64, error) {
	b, err := os.ReadFile(filepath.Join(path, "memory.events"))
	if err != nil {
		return nil, err
	}
	out := map[string]uint64{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		n, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		out[f[0]] = n
	}
	return out, nil
}

// PSI carries the three pressure numbers CapacityRequest reports (R-C reads pressure, not
// utilization: SP-RC-1).
type PSI struct {
	MemorySomeAvg10 float64
	CPUSomeAvg60    float64
	IOFullAvg10     float64
}

// Pressure reads the work slice's pressure files. A slice that is not active yet has no cgroup
// and reports zero pressure — which is true.
func Pressure(unit string) PSI {
	var psi PSI
	p, err := UnitPath(unit)
	if err != nil {
		return psi
	}
	psi.MemorySomeAvg10 = readAvg(filepath.Join(p, "memory.pressure"), "some", "avg10")
	psi.CPUSomeAvg60 = readAvg(filepath.Join(p, "cpu.pressure"), "some", "avg60")
	psi.IOFullAvg10 = readAvg(filepath.Join(p, "io.pressure"), "full", "avg10")
	return psi
}

func readAvg(file, line, field string) float64 {
	b, err := os.ReadFile(file)
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(l, line+" ") {
			continue
		}
		for _, kv := range strings.Fields(l)[1:] {
			if v, ok := strings.CutPrefix(kv, field+"="); ok {
				f, _ := strconv.ParseFloat(v, 64)
				return f
			}
		}
	}
	return 0
}
