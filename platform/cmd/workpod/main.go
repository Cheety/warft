// workpod — the platform, as one statically linked Go binary (SP-E02-1, E-02).
//
// Control plane, scheduler, worker, adapter, both gates and the agent harness are entry points of
// this one artifact; there is no second binary and no runtime on the host (SP-A02-3). What a later
// work package has not built yet refuses with the package that builds it — AB-E02-1 wants the
// surface in one artifact, and Q-02 forbids a fake behind any part of it.
//
// Besides the seven components, the binary carries the node mechanics of the A-04 start sequence:
// `disk`, `selftest`, and the cgroup primitives R-C gives the platform (`podslice`).
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/Cheety/warft/platform/internal/adapter"
	"github.com/Cheety/warft/platform/internal/boot"
	"github.com/Cheety/warft/platform/internal/cgroup"
	"github.com/Cheety/warft/platform/internal/controlplane"
	"github.com/Cheety/warft/platform/internal/disk"
	"github.com/Cheety/warft/platform/internal/harness"
	"github.com/Cheety/warft/platform/internal/selftest"
	"github.com/Cheety/warft/platform/internal/statedb"
	"github.com/Cheety/warft/platform/internal/worker"
	"github.com/Cheety/warft/platform/internal/workpod"
)

// revision is stamped by the image build; "development" outside one.
var revision = "development"

// exUnavailable is what a not-yet-built component exits with — distinguishable from 1 (a built
// component that failed) by every unit and probe that looks.
const exUnavailable = 69

// components is AB-E02-1's list: the seven from the row, each either serving since a work
// package or refusing until one.
var components = [][3]string{
	{"control-plane", "serving", "AP-3.1"},
	{"scheduler", "refusing-until", "AP-3.7"},
	{"worker", "serving", "AP-3.1"},
	{"adapter", "serving", "AP-3.2"},
	{"git-gate", "refusing-until", "AP-3.5"},
	{"egress-gate", "refusing-until", "AP-3.5"},
	{"harness", "serving", "AP-3.3"},
}

func usage() {
	fmt.Fprintf(os.Stderr, `workpod — the platform in one artifact (E-02)

components   control · scheduler · worker · adapter · git-gate · egress-gate · harness
adapter      adapter submit · adapter identity · adapter capabilities (T-01)
pod          pod run · pod resolve · pod pipeline · pod image import · pod base · pod list · pod reap (T-04, T-05)
node         disk [reinstall] · selftest · podslice arm <slice> · db-init · ping
about        components · version
`)
	os.Exit(2)
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "workpod %s: %v\n", os.Args[1], err)
	os.Exit(1)
}

func refuse(name, what, ap string) {
	fmt.Fprintf(os.Stderr, "workpod %s: not built — %s is %s's work.\n", name, what, ap)
	fmt.Fprintf(os.Stderr, "The entry point exists because the platform is one artifact (AB-E02-1); what is behind it\nturns green through %s's own rows, not through a pretense here (Q-02).\n", ap)
	os.Exit(exUnavailable)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {

	case "control":
		v := boot.Read()
		if err := v.Validate(); err != nil {
			fail(err)
		}
		if err := controlplane.Serve(v.Control); err != nil {
			fail(err)
		}

	case "worker":
		if err := worker.Run(boot.Read()); err != nil {
			fail(err)
		}

	case "adapter":
		if err := adapter.Run(os.Args[2:], boot.Read(), os.Stdout); err != nil {
			fail(err)
		}

	case "scheduler":
		refuse("scheduler", "tokens per phase, four priorities, the PSI ladder (R-B, R-C)", "AP-3.7")
	case "git-gate":
		refuse("git-gate", "the gate that checks policy and signs itself (K-03, B-02)", "AP-3.5")
	case "egress-gate":
		refuse("egress-gate", "the gate that holds the allowlist per job and inserts keys (B-02)", "AP-3.5")
	case "harness":
		// The pod's own init process. It runs nowhere else: outside a pod there is no job at
		// /run/workpod/job.json and the entry point says so rather than inventing one.
		if err := harness.Run(); err != nil {
			fail(err)
		}

	case "pod":
		if err := workpod.Command(os.Args[2:], os.Stdout); err != nil {
			fail(err)
		}

	case "disk":
		if len(os.Args) > 2 && os.Args[2] == "reinstall" {
			if err := disk.Reinstall(); err != nil {
				fail(err)
			}
			return
		}
		if err := disk.Run(); err != nil {
			fail(err)
		}

	case "selftest":
		if err := selftest.Run(); err != nil {
			fail(err)
		}

	case "podslice":
		if len(os.Args) != 4 || os.Args[2] != "arm" {
			usage()
		}
		if err := cgroup.ArmOOMGroup(os.Args[3]); err != nil {
			fail(err)
		}
		fmt.Printf("%s: memory.oom.group=1 — the pod is one workload to the OOM killer (SP-RC-4)\n", os.Args[3])

	case "db-init":
		if err := statedb.Init(); err != nil {
			fail(err)
		}

	case "ping":
		addr, cell := "", ""
		deadline := 5 * time.Second
		args := os.Args[2:]
		for i := 0; i < len(args); i++ {
			switch args[i] {
			case "--control":
				i++
				addr = args[i]
			case "--cell":
				i++
				cell = args[i]
			case "--deadline":
				i++
				d, err := time.ParseDuration(args[i])
				if err != nil {
					fail(err)
				}
				deadline = d
			default:
				usage()
			}
		}
		if err := worker.Ping(addr, cell, deadline); err != nil {
			fail(err)
		}

	case "components":
		for _, c := range components {
			fmt.Printf("%-13s %-14s %s\n", c[0], c[1], c[2])
		}

	case "version":
		fmt.Printf("workpod %s\n", revision)

	default:
		usage()
	}
}
