// The six signals of SP-RC-2, read from one cgroup.
//
// `Pressure` above reports the three numbers a capacity request carries. This is the fuller reading
// the scheduler decides from: four pressure levels the kernel already averaged, and the two counters
// SP-RC-2 reacts to the *change* of.

package cgroup

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Reading is one sample of a slice, as SP-RC-1 asks for it: pressure, never utilization. Nothing
// here reports how busy the cores are, because a busy machine is a healthy one and a stalled one is
// not.
type Reading struct {
	MemorySomeAvg10 float64
	MemoryFullAvg10 float64
	IOFullAvg10     float64
	CPUSomeAvg60    float64

	// The two cumulative counters. They mean nothing on their own — what SP-RC-2 reads is
	// `memory.events high` rising and `pgmajfault` rising fast — so the difference between two
	// readings is the signal, and forming it is the scheduler's.
	MemoryEventsHigh uint64
	PgMajFault       uint64
}

// Signals reads all six from a cgroup path.
//
// A file that is missing reports zero rather than an error. On the unified hierarchy a controller
// that was never enabled has no file, and a slice with nothing in it genuinely has no pressure —
// refusing to sample a quiet node would make the reader fail exactly when there is nothing wrong.
func Signals(path string) Reading {
	return Reading{
		MemorySomeAvg10:  readAvg(filepath.Join(path, "memory.pressure"), "some", "avg10"),
		MemoryFullAvg10:  readAvg(filepath.Join(path, "memory.pressure"), "full", "avg10"),
		IOFullAvg10:      readAvg(filepath.Join(path, "io.pressure"), "full", "avg10"),
		CPUSomeAvg60:     readAvg(filepath.Join(path, "cpu.pressure"), "some", "avg60"),
		MemoryEventsHigh: readCounter(filepath.Join(path, "memory.events"), "high"),
		PgMajFault:       readCounter(filepath.Join(path, "memory.stat"), "pgmajfault"),
	}
}

// SignalsOfUnit is the same by unit name, asking systemd where the slice lives rather than
// assembling the path (the pattern AP-1.3's run 27 established).
func SignalsOfUnit(unit string) (Reading, error) {
	p, err := UnitPath(unit)
	if err != nil {
		return Reading{}, err
	}
	return Signals(p), nil
}

// readCounter reads one `key value` line out of a flat cgroup file.
func readCounter(file, key string) uint64 {
	b, err := os.ReadFile(file)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == key {
			n, err := strconv.ParseUint(f[1], 10, 64)
			if err != nil {
				return 0
			}
			return n
		}
	}
	return 0
}

// Freeze stops every process in a cgroup with the kernel's own freezer (`cgroup.freeze`).
//
// This is SP-RB-4's preemption, exactly: the processes stop, they keep their memory, their open
// files and their place in the phase, and Thaw puts them back. Nothing is signalled, so nothing can
// mishandle a signal — a frozen pod loses its slot and not its state, which is the difference
// between a preemption and a kill.
//
// It is a cgroup write rather than a runc call because the pod's cgroup is what the scheduler
// knows: the supervisor that owns the container is on the worker (SP-T04-5), and freezing has to
// work from the side that decided it.
func Freeze(path string) error { return Write(path, "cgroup.freeze", "1") }

// Thaw is the way back. A preemption that could never end would be a kill with a gentler name.
func Thaw(path string) error { return Write(path, "cgroup.freeze", "0") }

// Frozen reads `cgroup.events` back: the kernel reports `frozen 1` once every process has actually
// stopped, which is later than the write returns. A caller that needs the fact rather than the
// request polls this.
func Frozen(path string) bool {
	b, err := os.ReadFile(filepath.Join(path, "cgroup.events"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "frozen 1" {
			return true
		}
	}
	return false
}

// Procs is how many processes live in a cgroup. AB-RB-4 rests on it: after a freeze the count is
// unchanged, because freezing is not killing.
func Procs(path string) (int, error) {
	b, err := os.ReadFile(filepath.Join(path, "cgroup.procs"))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n, nil
}

// SetCPUWeight is SP-RC-3's first rung: throttle by lowering `cpu.weight`, never by capping with
// `cpu.max` (SP-RA-3 — fairness through weights, not through hard ceilings).
func SetCPUWeight(path string, weight int) error {
	if weight < 1 {
		weight = 1
	}
	if weight > 10000 {
		weight = 10000
	}
	return Write(path, "cpu.weight", strconv.Itoa(weight))
}

// MemoryCurrent is what a cgroup has in memory right now, in bytes — the peak of it over a phase is
// what SP-RC-6 records per repository and phase.
func MemoryCurrent(path string) (uint64, error) {
	s, err := Read(path, "memory.current")
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(s, 10, 64)
}

// MemoryPeak is the kernel's own high-water mark, where it exists (memory.peak, Linux 6.5+). A
// kernel without it reports 0 and false, and the caller falls back to sampling memory.current —
// which is what the peak column of a profile is built from either way.
func MemoryPeak(path string) (uint64, bool) {
	s, err := Read(path, "memory.peak")
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
