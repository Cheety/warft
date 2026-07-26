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
// it: the runner (AP-3.3) arms every pod slice it creates through this same call.
func ArmOOMGroup(unit string) error {
	p, err := UnitPath(unit)
	if err != nil {
		return err
	}
	f := filepath.Join(p, "memory.oom.group")
	if err := os.WriteFile(f, []byte("1\n"), 0o644); err != nil {
		return fmt.Errorf("arming %s: %w", f, err)
	}
	b, err := os.ReadFile(f)
	if err != nil || strings.TrimSpace(string(b)) != "1" {
		return fmt.Errorf("%s did not hold the value 1 after writing it", f)
	}
	return nil
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
