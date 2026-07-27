// Package allocation is R-A in one place: the four resource classes of SP-RA-1, the cgroup v2 knobs
// they become, and the concurrency SP-RC-5 injects because a container cannot see its own size.
//
// The numbers are read from ra1-classes.tsv rather than written here. Four of its columns are
// SP-RA-1's own table, four are ruled in decisions/resource-contract.md, and acceptance/t04-runner.sh
// holds the file against both — so a limit cannot move in the code without moving in the panel or in
// the ruling.
//
// Nothing in this package touches a cgroup. It computes what a pod is owed; writing it is the
// runner's (internal/workpod), and reading pressure back is internal/cgroup's. That split is why
// this is a base module: the scheduler of AP-3.7 will size an admission decision from the same table
// without going anywhere near a container.
package allocation

import (
	"bufio"
	_ "embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

//go:embed ra1-classes.tsv
var classSource string

// Class is one of SP-RA-1's four. The names are the values of the `resource_class` enum in
// contract/schema.sql and of `ResourceClass` in contract/platform.proto — one word in three places
// because they are the same word.
type Class string

const (
	Tiny   Class = "tiny"
	Small  Class = "small"
	Medium Class = "medium"
	Large  Class = "large"
)

// bytesPerMiB is used to state Node's heap ceiling, which takes MiB rather than bytes.
const bytesPerMiB = 1024 * 1024

// nodeHeapNumerator and nodeHeapDenominator are the ¾ of decisions/resource-contract.md §5: what is
// left of the RAM limit for a JavaScript heap once everything in the pod that is not that heap has
// its share.
const (
	nodeHeapNumerator   = 3
	nodeHeapDenominator = 4
)

// Allocation is one class, resolved: what SP-RA-1 promises and what a cgroup has to say to keep the
// promise.
type Allocation struct {
	Class Class

	// SP-RA-1's own columns. Milli-cores because the panel's requests are tenths of a core and
	// integers compare without surprises.
	CPURequestedMilli int64
	CPULimitMilli     int64
	RAMRequestedBytes int64
	RAMLimitBytes     int64

	// The knobs, ruled in decisions/resource-contract.md §3.
	CPUWeight   int64 // cpu.weight — the request, as a share (SP-RA-3)
	MemoryMin   int64 // memory.min — the request, guaranteed
	MemoryHigh  int64 // memory.high — the limit, throttled not shot (SP-RA-2)
	PidsMax     int64 // pids.max — the fork wall (SP-RA-4)
	IOLatencyMs int64 // io.latency target (SP-RA-4)
}

// Cores is the CPU limit in whole cores — what the pod is told it may use (SP-RC-5). A limit below
// one core would still leave one worker, because zero workers is not a concurrency.
func (a Allocation) Cores() int {
	c := int(a.CPULimitMilli / 1000)
	if c < 1 {
		return 1
	}
	return c
}

// Environment is SP-RC-5: the concurrency variables, from the allocation rather than from
// `os.cpus()`. A container reports the host's cores, so a `tiny` pod on a 96-core machine would
// otherwise start 96 compilers inside a 128 MB guarantee.
//
// Returned sorted by name so a pod's environment is a function of its allocation and nothing else —
// two pods of the same class get byte-identical environments, which is what makes a run comparable
// to the one before it.
func (a Allocation) Environment() []string {
	n := a.Cores()
	heapMiB := a.RAMLimitBytes / bytesPerMiB * nodeHeapNumerator / nodeHeapDenominator
	env := []string{
		fmt.Sprintf("MAKEFLAGS=-j%d", n),
		fmt.Sprintf("CARGO_BUILD_JOBS=%d", n),
		fmt.Sprintf("UV_THREADPOOL_SIZE=%d", n),
		fmt.Sprintf("TURBO_CONCURRENCY=%d", n),
		fmt.Sprintf("NODE_OPTIONS=--max-old-space-size=%d --v8-pool-size=%d", heapMiB, n),
	}
	sort.Strings(env)
	return env
}

// IOLatency is the io.latency line for a device, in the format the kernel takes: a target in
// microseconds against one major:minor. The device is the disk the working copy sits on — a target
// against a disk the pod never writes to would protect nothing.
func (a Allocation) IOLatency(major, minor uint32) string {
	return fmt.Sprintf("%d:%d target=%d", major, minor, a.IOLatencyMs*1000)
}

// ruled holds the parsed table. Parsed once: the file is embedded at build time, so a second parse
// could not produce a different answer.
var ruled = mustParse(classSource)

// For returns the allocation of a class. An unknown class is an error rather than a default,
// because every path that produces one — the state contract's enum, the wire's enum, a job stated by
// hand — can only produce the four, and a fifth means something upstream is broken.
func For(c Class) (Allocation, error) {
	a, ok := ruled[c]
	if !ok {
		return Allocation{}, fmt.Errorf("%q is not one of R-A's four classes (%s)", c, strings.Join(Names(), ", "))
	}
	return a, nil
}

// Names is the four class names in the order SP-RA-1 lists them — smallest first.
func Names() []string {
	out := make([]string, 0, len(ruled))
	for _, c := range order {
		if _, ok := ruled[c]; ok {
			out = append(out, string(c))
		}
	}
	return out
}

// order is SP-RA-1's own order. The table is a map, and the panel's order is information the map
// cannot hold.
var order = []Class{Tiny, Small, Medium, Large}

func mustParse(src string) map[Class]Allocation {
	m, err := parse(src)
	if err != nil {
		// Embedded at build time: a malformed table is a broken build, not a runtime condition a
		// caller could do anything about.
		panic("ra1-classes.tsv: " + err.Error())
	}
	return m
}

func parse(src string) (map[Class]Allocation, error) {
	out := map[Class]Allocation{}
	sc := bufio.NewScanner(strings.NewReader(src))
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 8 {
			return nil, fmt.Errorf("%q has %d fields, expected 8", line, len(f))
		}
		var n [7]int64
		for i := range n {
			v, err := strconv.ParseInt(f[i+1], 10, 64)
			if err != nil || v <= 0 {
				return nil, fmt.Errorf("class %s, field %d: %q is not a positive number", f[0], i+2, f[i+1])
			}
			n[i] = v
		}
		c := Class(f[0])
		if _, dup := out[c]; dup {
			return nil, fmt.Errorf("class %s stands twice", c)
		}
		out[c] = Allocation{
			Class:             c,
			CPURequestedMilli: n[0],
			CPULimitMilli:     n[1],
			RAMRequestedBytes: n[2],
			RAMLimitBytes:     n[3],
			MemoryMin:         n[2],
			MemoryHigh:        n[3],
			CPUWeight:         n[4],
			PidsMax:           n[5],
			IOLatencyMs:       n[6],
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	for _, c := range order {
		if _, ok := out[c]; !ok {
			return nil, fmt.Errorf("SP-RA-1 has four classes and %s is missing", c)
		}
	}
	if len(out) != len(order) {
		return nil, fmt.Errorf("%d classes in the table, SP-RA-1 has %d", len(out), len(order))
	}
	return out, nil
}
