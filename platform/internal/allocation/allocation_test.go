package allocation

import (
	"strconv"
	"strings"
	"testing"
)

// The panel's own table, written out. This is the one place in the program where SP-RA-1's numbers
// are repeated, and it is a test on purpose: the acceptance parses them out of 01-specification.md,
// and this fails first and cheaper when the embedded file is edited by hand.
func TestSPRA1Table(t *testing.T) {
	want := []struct {
		class            Class
		cpuReq, cpuLimit int64
		ramReq, ramLimit int64
	}{
		{Tiny, 100, 1000, 128 << 20, 512 << 20},
		{Small, 300, 2000, 384 << 20, 1536 << 20},
		{Medium, 1000, 4000, 1 << 30, 3 << 30},
		{Large, 2000, 8000, 3 << 30, 8 << 30},
	}
	for _, w := range want {
		a, err := For(w.class)
		if err != nil {
			t.Fatalf("%s: %v", w.class, err)
		}
		if a.CPURequestedMilli != w.cpuReq || a.CPULimitMilli != w.cpuLimit {
			t.Errorf("%s CPU: %d/%d milli, want %d/%d", w.class, a.CPURequestedMilli, a.CPULimitMilli, w.cpuReq, w.cpuLimit)
		}
		if a.RAMRequestedBytes != w.ramReq || a.RAMLimitBytes != w.ramLimit {
			t.Errorf("%s RAM: %d/%d bytes, want %d/%d", w.class, a.RAMRequestedBytes, a.RAMLimitBytes, w.ramReq, w.ramLimit)
		}
	}
}

// SP-RA-2 and SP-RA-3 are absences, and an absence needs a test more than a presence does: nothing
// in the program would fail if a memory.max crept in, so the check is that the request goes to the
// guaranteeing knob and the limit to the throttling one.
func TestRequestGuaranteedLimitTolerated(t *testing.T) {
	for _, c := range order {
		a, err := For(c)
		if err != nil {
			t.Fatal(err)
		}
		if a.MemoryMin != a.RAMRequestedBytes {
			t.Errorf("%s: memory.min is %d, the request is %d", c, a.MemoryMin, a.RAMRequestedBytes)
		}
		if a.MemoryHigh != a.RAMLimitBytes {
			t.Errorf("%s: memory.high is %d, the limit is %d", c, a.MemoryHigh, a.RAMLimitBytes)
		}
		if a.MemoryMin >= a.MemoryHigh {
			t.Errorf("%s: a guarantee of %d against a limit of %d is not a class", c, a.MemoryMin, a.MemoryHigh)
		}
	}
}

// cpu.weight is the request as a share: one core requested is cgroup v2's default weight of 100, and
// the four stand in the ratio of their requests (decisions/resource-contract.md §2).
func TestCPUWeightIsTheRequest(t *testing.T) {
	for _, c := range order {
		a, err := For(c)
		if err != nil {
			t.Fatal(err)
		}
		if want := a.CPURequestedMilli / 10; a.CPUWeight != want {
			t.Errorf("%s: cpu.weight %d, want %d for a request of %d milli-cores", c, a.CPUWeight, want, a.CPURequestedMilli)
		}
	}
}

// AB-RC-5, in the unit that decides it: a pod with one core must not be told to start four workers.
func TestConcurrencyComesFromTheLimit(t *testing.T) {
	cases := map[Class]struct {
		cores   int
		heapMiB string
	}{
		Tiny:   {1, "384"},
		Small:  {2, "1152"},
		Medium: {4, "2304"},
		Large:  {8, "6144"},
	}
	for c, want := range cases {
		a, err := For(c)
		if err != nil {
			t.Fatal(err)
		}
		if a.Cores() != want.cores {
			t.Errorf("%s: %d cores, want %d", c, a.Cores(), want.cores)
		}
		env := strings.Join(a.Environment(), "\n")
		for _, must := range []string{
			"MAKEFLAGS=-j" + strconv.Itoa(want.cores),
			"CARGO_BUILD_JOBS=" + strconv.Itoa(want.cores),
			"UV_THREADPOOL_SIZE=" + strconv.Itoa(want.cores),
			"TURBO_CONCURRENCY=" + strconv.Itoa(want.cores),
			"NODE_OPTIONS=--max-old-space-size=" + want.heapMiB + " --v8-pool-size=" + strconv.Itoa(want.cores),
		} {
			if !strings.Contains(env, must) {
				t.Errorf("%s: %q missing from\n%s", c, must, env)
			}
		}
	}
}

// SP-RC-5 names five variables. Injecting four of them is a pod that starts four workers on one
// core through whichever one was forgotten.
func TestAllFiveVariablesAreInjected(t *testing.T) {
	a, err := For(Tiny)
	if err != nil {
		t.Fatal(err)
	}
	env := a.Environment()
	if len(env) != 5 {
		t.Fatalf("%d variables, SP-RC-5 names 5: %v", len(env), env)
	}
	for _, name := range []string{"MAKEFLAGS", "CARGO_BUILD_JOBS", "UV_THREADPOOL_SIZE", "NODE_OPTIONS", "TURBO_CONCURRENCY"} {
		found := false
		for _, e := range env {
			if strings.HasPrefix(e, name+"=") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is not injected", name)
		}
	}
}

func TestIOLatencyIsMicroseconds(t *testing.T) {
	a, err := For(Medium)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := a.IOLatency(259, 3), "259:3 target=100000"; got != want {
		t.Errorf("io.latency %q, want %q", got, want)
	}
}

func TestUnknownClassIsRefused(t *testing.T) {
	if _, err := For(Class("huge")); err == nil {
		t.Fatal("a fifth class was accepted; SP-RA-1 has four")
	}
}

func TestMalformedTableIsRefused(t *testing.T) {
	for name, src := range map[string]string{
		"a class missing":   "tiny\t100\t1000\t1\t2\t10\t128\t100\n",
		"a field missing":   "tiny\t100\t1000\t1\t2\t10\t128\n",
		"a class twice":     "tiny\t100\t1000\t1\t2\t10\t128\t100\ntiny\t100\t1000\t1\t2\t10\t128\t100\n",
		"a number negative": "tiny\t100\t1000\t1\t2\t10\t-1\t100\n",
	} {
		if _, err := parse(src); err == nil {
			t.Errorf("%s: parsed without complaint", name)
		}
	}
}
