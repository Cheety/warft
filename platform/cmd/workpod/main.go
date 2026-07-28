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
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Cheety/warft/platform/internal/adapter"
	"github.com/Cheety/warft/platform/internal/boot"
	"github.com/Cheety/warft/platform/internal/cgroup"
	"github.com/Cheety/warft/platform/internal/controlplane"
	"github.com/Cheety/warft/platform/internal/disk"
	"github.com/Cheety/warft/platform/internal/egress"
	"github.com/Cheety/warft/platform/internal/gitgate"
	"github.com/Cheety/warft/platform/internal/harness"
	"github.com/Cheety/warft/platform/internal/outbox"
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
	{"git-gate", "serving", "AP-3.5"},
	{"egress-gate", "serving", "AP-3.5"},
	{"harness", "serving", "AP-3.3"},
}

func usage() {
	fmt.Fprintf(os.Stderr, `workpod — the platform in one artifact (E-02)

components   control · scheduler · worker · adapter · git-gate · egress-gate · harness
control      control admit · control halt · control spend (V-04, E-08)
adapter      adapter submit · adapter identity · adapter capabilities (T-01)
outbox       outbox record · outbox list · outbox drain · outbox unanswered · outbox ask (K-03)
gates        git-gate --socket … · egress-gate --socket … (K-03, B-02)
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

// serveGitGate is `workpod git-gate`. The credential is optional at start and required at the first
// push onto a repository whose policy says the push is signed — a gate that refused to start
// without a key would be a gate nobody can run in a cell that pushes to no signed repository, and
// a gate that pushed unsigned because the key was missing would be worse than either (SP-K03-3).
func serveGitGate(args []string) error {
	fs := flag.NewFlagSet("git-gate", flag.ContinueOnError)
	socket := fs.String("socket", outbox.GitSocket, "the Unix socket the gate serves on — never a port (SP-B02-6)")
	policyPath := fs.String("policy", gitgate.PolicyFile, "which repositories and branches a push may touch")
	ledger := fs.String("ledger", gitgate.LedgerDir, "the gate's own ledger — one push per domain key, ever")
	credential := fs.String("credential", "", "the signing key, from the unit's credential directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	policy, err := gitgate.LoadPolicy(*policyPath)
	if err != nil {
		return err
	}
	g := &gitgate.Gate{
		Policy: policy,
		Ledger: outbox.OpenLedger(*ledger),
		Logf:   func(format string, a ...any) { fmt.Printf(format+"\n", a...) },
	}
	if *credential != "" {
		if g.Signer, err = gitgate.SignerFromCredential(*credential); err != nil {
			return err
		}
	}
	fmt.Printf("git-gate: %d policy line(s), serving on %s\n", len(policy), *socket)
	return gitgate.Serve(context.Background(), *socket, g)
}

// serveEgressGate is `workpod egress-gate`. It runs on the work node (SP-B02-2), one per node, and
// the allowlist it enforces is the job's rather than the node's (SP-B02-4).
func serveEgressGate(args []string) error {
	fs := flag.NewFlagSet("egress-gate", flag.ContinueOnError)
	socket := fs.String("socket", outbox.EgressSocket, "the Unix socket the gate serves on — never a port (SP-B02-6)")
	grants := fs.String("grants", egress.GrantsDir, "one allowlist per job, written when the job is admitted")
	credentials := fs.String("credentials", "", "the key directory, one file per host, from the unit's credential directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	g := &egress.Gate{
		Grants: *grants,
		Logf:   func(format string, a ...any) { fmt.Printf(format+"\n", a...) },
	}
	if *credentials != "" {
		g.Keys = egress.KeysFromCredential(*credentials)
	}
	fmt.Printf("egress-gate: %d authority level(s) derive an allowlist, serving on %s\n", len(egress.Ruled.Levels), *socket)
	return egress.Serve(context.Background(), *socket, g)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {

	case "control":
		// `workpod control` serves; `workpod control admit|halt|spend` are the operator's, and they
		// run against the state database and the halt file without the plane — which is what makes
		// a halt with the API switched off checkable at all (SP-E08-3).
		if len(os.Args) > 2 {
			if err := controlplane.Command(os.Args[2:], os.Stdout); err != nil {
				fail(err)
			}
			return
		}
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

	// The two gates of SP-K03-3, and there is no third. Each one listens on a Unix socket and
	// never on a port (SP-B02-6); which node runs which is the role's business, and SP-B02-2 puts
	// the egress proxy on the work node rather than centrally.
	case "git-gate":
		if err := serveGitGate(os.Args[2:]); err != nil {
			fail(err)
		}
	case "egress-gate":
		if err := serveEgressGate(os.Args[2:]); err != nil {
			fail(err)
		}

	case "outbox":
		if err := outbox.Command(os.Args[2:], os.Stdout); err != nil {
			fail(err)
		}
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
