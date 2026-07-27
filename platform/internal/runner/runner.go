// Package runner is SP-T04-4's abstraction, and only that: *given a working directory and a job,
// deliver a patch and a report*. It is operating-system neutral — nothing in this file knows about
// namespaces, cgroups, btrfs or runc, because a `macos` runner has none of them and satisfies the
// same contract (AP-8.3).
//
// The workpod is one implementation of it (internal/workpod) and the panel is explicit that the
// abstraction is not over the workpod. That is why the contract is a base module: the pod-side
// harness (internal/harness) speaks it from inside the pod, the work role speaks it from outside,
// and neither may import the other.
//
// The same types are the wire between host and pod. There is no second encoding: the host writes a
// Job to /run/workpod/job.json, the harness reads it, the harness writes a Report to
// /run/workpod/out/report.json, the host reads it. decisions/pod-runtime.md §3 fixes those paths.
package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Platform is one of SP-T04-4's pools. The envelope carries it and the scheduler knows several;
// only `alpine`, the normal case, is built (AP-3.3). The other three are AP-8.3's.
type Platform string

const (
	Alpine  Platform = "alpine"
	Windows Platform = "windows"
	MacOS   Platform = "macos"
	Remote  Platform = "remote"
)

// Platforms is the four pools, in the order SP-T04-4 names them.
var Platforms = []Platform{Alpine, Windows, MacOS, Remote}

// Runner is the contract. One sentence of SP-T04-4, as a type.
type Runner interface {
	// Platform names the pool this runner serves.
	Platform() Platform

	// Run takes a working directory and a job and delivers a patch and a report. The working
	// directory is the runner's to prepare — a workpod snapshots it, a remote runner ships it —
	// and `base` is what it is prepared from.
	Run(ctx context.Context, base string, job Job) (Report, error)
}

// NotBuilt is the refusal a pool that has no implementation answers with. It names the work package
// rather than returning a generic error, for the reason the binary's unbuilt entry points do
// (AB-E02-1): what is missing is a work package, never a pretense.
func NotBuilt(p Platform) error {
	return fmt.Errorf("no runner for the %s pool — the further runners are AP-8.3's; only %s is built (SP-T04-4)", p, Alpine)
}

// Requirements is what SP-T03-1 forms the requirement hash from: language and version, system
// packages, test runner, browser engine. Nothing else belongs in it — the hash is the identity of an
// image, and a field that does not change the image would split the index for no reason.
type Requirements struct {
	Language        string   `json:"language"`
	LanguageVersion string   `json:"language_version"`
	SystemPackages  []string `json:"system_packages"`
	TestRunner      string   `json:"test_runner,omitempty"`
	BrowserEngine   string   `json:"browser_engine,omitempty"`
}

// Empty reports whether nothing was asked for. A job with empty requirements and no image digest
// names no image at all, which is a job nobody can start (SP-T03-1).
func (r Requirements) Empty() bool {
	return r.Language == "" && r.LanguageVersion == "" && len(r.SystemPackages) == 0 &&
		r.TestRunner == "" && r.BrowserEngine == ""
}

// Hash is the requirement hash of SP-T03-1: the key the image index is looked up by.
//
// The encoding is canonical rather than JSON, because a hash of JSON is a hash of a serializer's
// habits — key order, escaping and whitespace would all change the key without changing the
// requirements. Package names are sorted for the same reason: two jobs asking for the same packages
// in a different order want the same image.
func (r Requirements) Hash() string {
	pkgs := append([]string(nil), r.SystemPackages...)
	sort.Strings(pkgs)
	var b strings.Builder
	for _, kv := range [][2]string{
		{"language", r.Language},
		{"language_version", r.LanguageVersion},
		{"system_packages", strings.Join(pkgs, ",")},
		{"test_runner", r.TestRunner},
		{"browser_engine", r.BrowserEngine},
	} {
		fmt.Fprintf(&b, "%s=%s\n", kv[0], kv[1])
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Job is what a runner is handed. It is the order of contract/schema.sql narrowed to what running
// needs: the identity to report under, the class to allocate by, the image to resolve, the base to
// snapshot, and the budget that bounds it.
type Job struct {
	OrderID string `json:"order_id"`
	Attempt uint32 `json:"attempt"`
	Cell    string `json:"cell"`
	Project string `json:"project"`

	Platform Platform `json:"platform"`
	// Class is one of R-A's four, named as contract/schema.sql's `resource_class` enum names it.
	// A string rather than an allocation.Class because this contract is neutral: a runner on a
	// platform without cgroups still has a class, and internal/allocation is what turns it into
	// knobs.
	Class string `json:"class"`

	Requirements Requirements `json:"requirements"`
	// ImageDigest, when set, is `order.image_hash`: a job that already names its image skips the
	// requirement hash entirely. A job with neither is a job whose image nobody decided.
	ImageDigest string `json:"image_digest,omitempty"`

	// Command is what runs in the working directory. Until AP-3.4 there is no pipeline and no
	// agent, so the `edit` phase is a command stated by hand — the same shape
	// decisions/jobs-by-hand.md rules for the job itself.
	Command []string `json:"command"`

	// PodMinutes is the budget half of the lifetime (decisions/pod-runtime.md §4). Zero means the
	// job carries no budget and the default ceiling applies; the pots themselves are AP-3.6's.
	PodMinutes uint64 `json:"pod_minutes,omitempty"`
}

// Validate refuses a job that cannot be run before anything is created. A refusal after the snapshot
// stands is a subvolume nobody asked for.
func (j Job) Validate() error {
	switch {
	case j.OrderID == "":
		return fmt.Errorf("a job without an order id cannot be reported on (SP-K01-1)")
	case j.Cell == "":
		return fmt.Errorf("cell is NOT NULL on every table (SP-K01-1)")
	case j.Project == "":
		return fmt.Errorf("project is NOT NULL on every table (SP-K01-1)")
	case j.Class == "":
		return fmt.Errorf("a job without a class cannot be allocated (SP-RA-1)")
	case len(j.Command) == 0:
		return fmt.Errorf("a job without a command has nothing to run; the pipeline that would supply one is AP-3.4's")
	case j.ImageDigest == "" && j.Requirements.Empty():
		return fmt.Errorf("a job names an image or the requirements to resolve one (SP-T03-1)")
	}
	return nil
}

// State is SP-T04-3's lifecycle. The panel's chain is created → active → frozen → checkpointed →
// reaped, and these are its five words.
type State string

const (
	Created      State = "created"      // the snapshot stands, nothing has run
	Active       State = "active"       // the init process was started
	Frozen       State = "frozen"       // after 45 s of quiet
	Checkpointed State = "checkpointed" // dumped to disk
	Reaped       State = "reaped"       // patch out, pod gone
)

// transitions is the state machine, and it is deliberately wider than the panel's arrow in two
// places.
//
// `frozen → active` exists because SP-RB-3 makes freezing the platform's preemption — "preemption =
// freezing at the phase boundary" — and a preemption that could never end would be a kill with a
// gentler name. `checkpointed → active` is the restore half of R-C's fourth rung; a dump nobody can
// restore is a deletion.
//
// Everything may reach `reaped`, because a lifetime can end in any state and SP-T04-5 puts no
// condition on the reaper.
var transitions = map[State][]State{
	Created:      {Active, Reaped},
	Active:       {Frozen, Reaped},
	Frozen:       {Active, Checkpointed, Reaped},
	Checkpointed: {Active, Reaped},
	Reaped:       nil,
}

// CanTransition reports whether a pod may go from one state to another.
func CanTransition(from, to State) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Transition is one step of a pod's life, with the moment it happened and why. A terminal state
// without a cause is what SP-K02-3 forbids on an order; the same discipline holds here, one level
// down, because a pod that was reaped without a reason is a pod nobody can account for.
type Transition struct {
	State  State     `json:"state"`
	At     time.Time `json:"at"`
	Reason string    `json:"reason"`
}

// Report is what comes back out. The fields are `Report` of contract/platform.proto narrowed to what
// a runner can know: everything after running is written by the worker (SP-K02-1), and the runner is
// what the worker writes it from.
type Report struct {
	OrderID string `json:"order_id"`
	Attempt uint32 `json:"attempt"`

	// FinalState and Cause are the order's, not the pod's: `delivered`, `unproven`, `failed`.
	// Cause is mandatory in every terminal state (SP-K02-3).
	FinalState string `json:"final_state"`
	Cause      string `json:"cause,omitempty"`
	Evidence   string `json:"evidence,omitempty"`

	PatchHash string `json:"patch_hash,omitempty"`
	// PatchPath is where the patch was left on the node. It is not on the wire — contract's Report
	// carries the hash, because a path on one node means nothing on another — but the worker that
	// has to hand the patch to the outbox (AP-3.5) needs to be able to find it.
	PatchPath string `json:"patch_path,omitempty"`
	Text      string `json:"report_text"`

	ExitCode int `json:"exit_code"`
	// StartMillis is AB-T03-1's measurement: from the first act of the runner to the pod being
	// active. SP-T03-1's "~200 ms" is about this number and no other — not about how long the job
	// took, and not about how long the image took to arrive, which on a hit is nothing.
	StartMillis int64        `json:"start_millis"`
	PodSeconds  uint64       `json:"pod_seconds"`
	Lifecycle   []Transition `json:"lifecycle,omitempty"`

	// ImageDigest is the image the pod actually ran, which is not always the one the job named:
	// a job carrying only requirements has one resolved for it (SP-T03-1).
	ImageDigest string `json:"image_digest,omitempty"`
}

// PodPaths is decisions/pod-runtime.md §3 as constants, so the two sides of the bind mount cannot
// drift: the host writes to these paths from outside, the harness reads them from inside.
const (
	PodWorkDir    = "/work"
	PodHarness    = "/harness/workpod"
	PodJobFile    = "/run/workpod/job.json"
	PodSocket     = "/run/workpod/harness.sock"
	PodOutDir     = "/run/workpod/out"
	PodReportFile = PodOutDir + "/report.json"
)
