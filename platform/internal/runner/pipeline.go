// T-05, the pipeline: a fixed spine and seven movable places (AP-3.4).
//
// It lives beside the runner contract rather than in a module of its own, because it is the same
// wire T-04 already runs on: the host writes a Job, the pod reads it, the pod writes a Report, the
// host reads it — and the pipeline is what both sides have to agree the job *is*. The harness runs
// four of the seven phases from inside the pod and the workpod runs three from outside; neither may
// import the other (decisions/module-dependencies.md), so what they share sits here, in the one base
// module they both already speak.
//
// Nothing in this file runs anything. The spine is data, the places are data, and the two machines
// that execute them are internal/harness (plan · edit · check · repair) and internal/workpod
// (prepare · deliver · reap).
package runner

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Phase is one step of SP-T05-1's spine.
type Phase string

const (
	PhasePrepare Phase = "prepare"
	PhasePlan    Phase = "plan"
	PhaseEdit    Phase = "edit"
	PhaseCheck   Phase = "check"
	PhaseRepair  Phase = "repair"
	PhaseDeliver Phase = "deliver"
	PhaseReap    Phase = "reap"
)

// spine is SP-T05-1 exactly: *a fixed spine, the same for all jobs*. It is an array and unexported
// on purpose — a package-level slice is a runtime object, and SP-T05-4 says no runtime object may
// change the definition. Everyone else gets a copy from Spine().
var spine = [7]Phase{PhasePrepare, PhasePlan, PhaseEdit, PhaseCheck, PhaseRepair, PhaseDeliver, PhaseReap}

// Spine is the seven steps every job passes through, in order.
func Spine() []Phase { return append([]Phase(nil), spine[:]...) }

// SpineIndex is where a phase stands in the spine, and -1 for anything that is not one of the seven.
func SpineIndex(p Phase) int {
	for i, s := range spine {
		if s == p {
			return i
		}
	}
	return -1
}

// PlaceNames is SP-T05-2's seven, in the order the panel names them. The list is exported because
// the probe of AB-T05-2 asks a question about the list itself: only these differ per job.
//
// Place one is served by the job's own `image_digest` / `requirements` — the fields SP-T03-1 already
// put there — rather than by a second copy inside the pipeline. Two fields naming one image would be
// a place that can contradict itself. The other six are the job's `places` object.
var PlaceNames = [7]string{
	"image",
	"plan_required",
	"paths",
	"checks",
	"rework_rounds",
	"acceptance",
	"keep_snapshot_on_failure",
}

// EvidenceClasses is `evidence_class` of contract/schema.sql, which is what place six — the
// acceptance criterion — may name. It is repeated here rather than imported because this module
// imports nothing and the pod has no database; acceptance/t05-pipeline.sh holds the two lists
// against each other, so the repetition cannot drift.
var EvidenceClasses = []string{
	"artifact.identical", "types.lint", "tests.existing", "tests.new",
	"mutation.diff", "review.independent", "human",
}

// Check is one entry of place four: which checks run, and which of them block. A check that does not
// block is measured and reported; a check that blocks decides whether the job delivers and whether
// another rework round is spent on it (SP-T05-3).
type Check struct {
	Name    string   `json:"name"`
	Command []string `json:"command"`
	Blocks  bool     `json:"blocks"`
}

// Places is what a pipeline definition carries: six of SP-T05-2's seven, with the values that hold
// for every job that does not move them.
type Places struct {
	// PlanRequired is place two: whether a plan is demanded before anything is edited.
	PlanRequired bool `json:"plan_required"`
	// Paths is place three: which paths the agent may touch, as prefixes relative to the working
	// copy. Empty means the whole working copy. Prefixes rather than globs because a glob dialect
	// invented here would be a language nobody else in the platform speaks.
	Paths []string `json:"paths"`
	// Checks is place four.
	Checks []Check `json:"checks"`
	// ReworkRounds is place five, bounded per class by decisions/OP-2.md.
	ReworkRounds int `json:"rework_rounds"`
	// Acceptance is place six: the evidence class a job delivers under when its blocking checks
	// pass. Q-02 — a delivery without an evidence class is the confidence this platform rejects.
	Acceptance string `json:"acceptance"`
	// KeepSnapshotOnFailure is place seven: whether the working copy survives a pod that did not
	// deliver, as a read-only snapshot beside the pods.
	KeepSnapshotOnFailure bool `json:"keep_snapshot_on_failure"`
}

// PlaceMoves is the same six places as a job states them: every field a pointer, so that *not
// stated* and *stated as false* are different things. A plain struct would make
// `keep_snapshot_on_failure: false` indistinguishable from silence, and place seven would then be
// unmovable in one direction.
type PlaceMoves struct {
	PlanRequired          *bool     `json:"plan_required,omitempty"`
	Paths                 *[]string `json:"paths,omitempty"`
	Checks                *[]Check  `json:"checks,omitempty"`
	ReworkRounds          *int      `json:"rework_rounds,omitempty"`
	Acceptance            *string   `json:"acceptance,omitempty"`
	KeepSnapshotOnFailure *bool     `json:"keep_snapshot_on_failure,omitempty"`
}

// ReapAfter says where a pod's life ends, and it belongs to the definition rather than to a job:
// `quiet` is SP-T04-3's chain in full, `report` ends the pod the moment its report is in.
const (
	ReapAfterQuiet  = "quiet"
	ReapAfterReport = "report"
)

// Pipeline is a definition — the fixed spine plus the defaults of the six places. It is versioned
// (`pipeline@version`) and it belongs to the human (SP-T05-4): it is a file in Git, read out of the
// artifact, and no runtime object can reach it. A job pins one and moves places; a job cannot write
// one.
type Pipeline struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	ReapAfter   string `json:"reap_after"`
	Places      Places `json:"places"`
}

// Ref is `pipeline@version`, the form `order.pipeline_version` carries.
func (p Pipeline) Ref() string { return p.Name + "@" + p.Version }

// ContentHash is what `pipeline_version.content_hash` holds: the identity of a definition, spine
// included.
//
// The spine is hashed with the places on purpose. `pipeline@version` names the whole of T-05, so a
// day on which the spine changed while a version did not would be a day on which two different
// pipelines answered to one name. The encoding is canonical rather than JSON, for the reason
// Requirements.Hash is: a hash of JSON is a hash of a serializer's habits.
func (p Pipeline) ContentHash() string {
	var b strings.Builder
	fmt.Fprintf(&b, "spine=%s\n", joinPhases(Spine()))
	for _, kv := range [][2]string{
		{"pipeline", p.Ref()},
		{"description", p.Description},
		{"reap_after", p.ReapAfter},
		{"plan_required", strconv.FormatBool(p.Places.PlanRequired)},
		{"paths", strings.Join(p.Places.Paths, ",")},
		{"checks", encodeChecks(p.Places.Checks)},
		{"rework_rounds", strconv.Itoa(p.Places.ReworkRounds)},
		{"acceptance", p.Places.Acceptance},
		{"keep_snapshot_on_failure", strconv.FormatBool(p.Places.KeepSnapshotOnFailure)},
	} {
		fmt.Fprintf(&b, "%s=%s\n", kv[0], kv[1])
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func joinPhases(ps []Phase) string {
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = string(p)
	}
	return strings.Join(parts, ",")
}

// encodeChecks keeps the order the definition states. A check list is a sequence, not a set: the
// order decides which failure a report names first, and sorting it would make two different
// pipelines share one hash.
func encodeChecks(cs []Check) string {
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = fmt.Sprintf("%s:%s:%s", c.Name, strconv.FormatBool(c.Blocks), strings.Join(c.Command, " "))
	}
	return strings.Join(parts, "|")
}

// Validate refuses a definition that cannot be run. It holds for the catalog in the artifact too:
// the file is read at the first use and a broken entry is a broken binary, not a broken job.
func (p Pipeline) Validate() error {
	switch {
	case p.Name == "":
		return fmt.Errorf("a pipeline without a name cannot be pinned by a job (SP-T05-4)")
	case p.Version == "":
		return fmt.Errorf("pipeline %q carries no version — `pipeline@version` is the whole of SP-T05-4", p.Name)
	case strings.ContainsAny(p.Name, "@ \t") || strings.ContainsAny(p.Version, "@ \t"):
		return fmt.Errorf("%q: a pipeline name and version carry no `@` and no whitespace; the reference is name@version", p.Ref())
	case p.ReapAfter != ReapAfterQuiet && p.ReapAfter != ReapAfterReport:
		return fmt.Errorf("%s: reap_after is %s or %s, not %q", p.Ref(), ReapAfterQuiet, ReapAfterReport, p.ReapAfter)
	}
	return p.Places.Validate()
}

// Validate refuses places that name something the platform has no word for.
func (pl Places) Validate() error {
	if pl.ReworkRounds < 0 {
		return fmt.Errorf("rework_rounds is a count, not %d (SP-T05-3, decisions/OP-2.md)", pl.ReworkRounds)
	}
	if !knownEvidence(pl.Acceptance) {
		return fmt.Errorf("acceptance %q is not an evidence class of contract/schema.sql — one of %s (Q-02)",
			pl.Acceptance, strings.Join(EvidenceClasses, ", "))
	}
	for _, p := range pl.Paths {
		if p == "" {
			return fmt.Errorf("an empty path is not a place — leave `paths` empty to mean the whole working copy")
		}
		if strings.HasPrefix(p, "/") {
			return fmt.Errorf("path %q is absolute; place three is relative to the working copy", p)
		}
		if clean := path.Clean(p); clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("path %q leaves the working copy, which is the one thing place three exists to prevent", p)
		}
	}
	names := map[string]bool{}
	for _, c := range pl.Checks {
		switch {
		case c.Name == "":
			return fmt.Errorf("a check without a name cannot be reported on (SP-T05-3)")
		case names[c.Name]:
			return fmt.Errorf("two checks are called %q; a report that named one of them would be ambiguous", c.Name)
		case len(c.Command) == 0:
			return fmt.Errorf("check %q has no command to run", c.Name)
		}
		names[c.Name] = true
	}
	return nil
}

func knownEvidence(s string) bool {
	for _, e := range EvidenceClasses {
		if e == s {
			return true
		}
	}
	return false
}

// -------------------------------------------------------------------------------------------------
// The catalog and the ceilings — both in the artifact, both the human's
// -------------------------------------------------------------------------------------------------

// DefaultPipeline is what a job that pins nothing runs under. A job may not run under *no* pipeline:
// SP-T05-1's spine is the same for all jobs, and a job without a definition would be a job whose
// places nobody decided.
const DefaultPipeline = "default@1"

//go:embed t05-pipelines.json
var pipelineCatalog []byte

//go:embed op2-rounds.tsv
var op2Rounds []byte

type catalogFile struct {
	Pipelines []Pipeline `json:"pipelines"`
}

// Pipelines is every definition the artifact carries, in the order the file states them.
//
// It panics on a broken catalog rather than returning an error, and that is deliberate: the file is
// embedded, so a malformed entry is not a condition a node can be in — it is a binary that should
// never have been built, and acceptance/t05-pipeline.sh reads this same list on every run.
func Pipelines() []Pipeline {
	var f catalogFile
	if err := json.Unmarshal(pipelineCatalog, &f); err != nil {
		panic("t05-pipelines.json is not a catalog: " + err.Error())
	}
	for _, p := range f.Pipelines {
		if err := p.Validate(); err != nil {
			panic("t05-pipelines.json: " + err.Error())
		}
	}
	return f.Pipelines
}

// PipelineByRef looks one definition up by `name@version`.
func PipelineByRef(ref string) (Pipeline, error) {
	for _, p := range Pipelines() {
		if p.Ref() == ref {
			return p, nil
		}
	}
	var known []string
	for _, p := range Pipelines() {
		known = append(known, p.Ref())
	}
	return Pipeline{}, fmt.Errorf("no pipeline %s in this artifact — it carries %s (SP-T05-4: the definition belongs to the human, not to a job)",
		ref, strings.Join(known, ", "))
}

// RoundCeilings is decisions/OP-2.md's table: the most rework rounds a class may ever spend.
func RoundCeilings() map[string]int {
	out := map[string]int{}
	sc := bufio.NewScanner(bytes.NewReader(op2Rounds))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		class, rounds, ok := strings.Cut(line, "\t")
		if !ok {
			panic("op2-rounds.tsv: " + line + " is not class<TAB>rework_rounds_max")
		}
		n, err := strconv.Atoi(strings.TrimSpace(rounds))
		if err != nil || n < 0 {
			panic("op2-rounds.tsv: " + line + " does not carry a ceiling")
		}
		out[strings.TrimSpace(class)] = n
	}
	return out
}

// RoundCeiling is the ceiling of one class. A class the table does not name has no ceiling that was
// ruled, and a loop nobody bounded is the thing OP-2 exists to prevent — so it is an error, not a
// default.
func RoundCeiling(class string) (int, error) {
	n, ok := RoundCeilings()[class]
	if !ok {
		classes := make([]string, 0, len(RoundCeilings()))
		for c := range RoundCeilings() {
			classes = append(classes, c)
		}
		sort.Strings(classes)
		return 0, fmt.Errorf("no rework-round ceiling is ruled for class %q — decisions/OP-2.md rules %s",
			class, strings.Join(classes, ", "))
	}
	return n, nil
}

// -------------------------------------------------------------------------------------------------
// What a job actually runs under
// -------------------------------------------------------------------------------------------------

// Definition is the pipeline a job pinned, exactly as the human filed it — before the job moved
// anything.
//
// It is what a report has to name, and it is deliberately not the effective pipeline below.
// `pipeline_version.content_hash` is the identity of a definition; hashing the effective one would
// give two jobs that moved different places two different `pipeline@version`, and then the version
// would name the job rather than the pipeline — which is the opposite of SP-T05-4.
func (j Job) Definition() (Pipeline, error) {
	ref := j.PipelineVersion
	if ref == "" {
		ref = DefaultPipeline
	}
	return PipelineByRef(ref)
}

// Effective is the pipeline a job runs under: the definition it pinned, moved at the places it moves
// and nowhere else.
//
// The returned Pipeline keeps the pinned definition's name and version, because that is what the
// order records and what the report has to name — a job's moves are a job's, and calling the result
// something else would lose the join to `pipeline_version.content_hash`.
func (j Job) Effective() (Pipeline, error) {
	def, err := j.Definition()
	if err != nil {
		return Pipeline{}, err
	}

	ceiling, err := RoundCeiling(j.Class)
	if err != nil {
		return Pipeline{}, err
	}

	eff := def
	if m := j.Places; m != nil {
		if m.PlanRequired != nil {
			eff.Places.PlanRequired = *m.PlanRequired
		}
		if m.Paths != nil {
			eff.Places.Paths = append([]string(nil), *m.Paths...)
		}
		if m.Checks != nil {
			eff.Places.Checks = append([]Check(nil), *m.Checks...)
		}
		if m.Acceptance != nil {
			eff.Places.Acceptance = *m.Acceptance
		}
		if m.KeepSnapshotOnFailure != nil {
			eff.Places.KeepSnapshotOnFailure = *m.KeepSnapshotOnFailure
		}
		if m.ReworkRounds != nil {
			// Refused rather than clamped: a job asking for more rounds than its class allows is a
			// job whose author believed something untrue, and a run that quietly gave it fewer
			// would confirm the belief (decisions/OP-2.md).
			if *m.ReworkRounds > ceiling {
				return Pipeline{}, fmt.Errorf("the job asks for %d rework rounds; class %s allows at most %d (decisions/OP-2.md)",
					*m.ReworkRounds, j.Class, ceiling)
			}
			eff.Places.ReworkRounds = *m.ReworkRounds
		}
	}
	// The definition's default is bounded rather than refused: the ceiling is about what a class may
	// spend, and `default@1` is written for every class at once.
	if eff.Places.ReworkRounds > ceiling {
		eff.Places.ReworkRounds = ceiling
	}
	if err := eff.Validate(); err != nil {
		return Pipeline{}, err
	}
	return eff, nil
}

// Moved names the places this job moves away from the definition it pinned, in PlaceNames order.
// It is what `workpod pod pipeline --job` prints and what AB-T05-2 reads: the answer is always a
// subset of the seven, because there is nowhere else for a job to differ.
func (j Job) Moved() ([]string, error) {
	def, err := j.Definition()
	if err != nil {
		return nil, err
	}
	eff, err := j.Effective()
	if err != nil {
		return nil, err
	}

	var moved []string
	if j.ImageDigest != "" || !j.Requirements.Empty() {
		moved = append(moved, "image")
	}
	for _, p := range [...]struct {
		name string
		diff bool
	}{
		{"plan_required", eff.Places.PlanRequired != def.Places.PlanRequired},
		{"paths", strings.Join(eff.Places.Paths, ",") != strings.Join(def.Places.Paths, ",")},
		{"checks", encodeChecks(eff.Places.Checks) != encodeChecks(def.Places.Checks)},
		{"rework_rounds", eff.Places.ReworkRounds != def.Places.ReworkRounds},
		{"acceptance", eff.Places.Acceptance != def.Places.Acceptance},
		{"keep_snapshot_on_failure", eff.Places.KeepSnapshotOnFailure != def.Places.KeepSnapshotOnFailure},
	} {
		if p.diff {
			moved = append(moved, p.name)
		}
	}
	return moved, nil
}

// DecodeJob reads a job stated by hand and refuses any field the contract does not carry.
//
// This is the mechanism behind AB-T05-2. SP-T05-2 is a closed list — *movable per job, and only at
// these places* — and a decoder that ignored what it did not recognize would make the list open by
// accident: a job could carry `spine`, `phases` or a second pipeline definition, and nothing would
// say no. Refusing the unknown field is how the sentence stays true.
func DecodeJob(b []byte) (Job, error) {
	var job Job
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&job); err != nil {
		return job, fmt.Errorf("this is not a job the platform runs: %w (SP-T05-2 names the seven places a job may move; there is no eighth)", err)
	}
	return job, nil
}

// -------------------------------------------------------------------------------------------------
// The phase log — what makes SP-T05-1 checkable rather than believed
// -------------------------------------------------------------------------------------------------

// Outcome is how a phase went. A phase that did nothing says so and says why; the alternative is a
// spine with holes in it, and a hole cannot be told from a step that was skipped on purpose.
type Outcome string

const (
	Ran     Outcome = "ran"     // the phase did its work
	Skipped Outcome = "skipped" // the phase had nothing to do, and the detail says why
	Refused Outcome = "refused" // the phase would need something no work package has built yet
	Failed  Outcome = "failed"  // the phase ran and did not succeed
)

// PhaseRecord is one step of the spine as it actually went. `check` and `repair` occur once per
// rework round and carry the round they belong to; the other five occur once and carry zero.
type PhaseRecord struct {
	Phase   Phase   `json:"phase"`
	Outcome Outcome `json:"outcome"`
	Round   int     `json:"round,omitempty"`
	Detail  string  `json:"detail"`
	Millis  int64   `json:"millis"`
}

// PhaseLog collects them in the order they happened. Both machines write one: the harness for the
// four phases inside the pod, the workpod for the three outside, and the report carries them joined.
type PhaseLog struct {
	Records []PhaseRecord `json:"records"`
}

// Add records a phase. The detail is never empty by construction at the call sites — a step that
// says only "skipped" is a step nobody can account for, which is the same discipline SP-K02-3
// applies to a terminal state.
func (l *PhaseLog) Add(p Phase, o Outcome, round int, d time.Duration, format string, a ...any) {
	l.Records = append(l.Records, PhaseRecord{
		Phase:   p,
		Outcome: o,
		Round:   round,
		Detail:  fmt.Sprintf(format, a...),
		Millis:  d.Milliseconds(),
	})
}

// VerifySpine holds a phase log against SP-T05-1: *a fixed spine, the same for all jobs*.
//
// Three things have to be true, and the third is the one that makes the requirement more than a
// wish: every one of the seven occurs, the first time each occurs is in the spine's order, and
// nothing occurs that is not one of the seven. A job that ended early still passes through all
// seven — the ones it could not do are recorded as skipped, with the reason.
func VerifySpine(rs []PhaseRecord) error {
	known := map[Phase]bool{}
	for _, p := range spine {
		known[p] = true
	}
	first := map[Phase]int{}
	for i, r := range rs {
		if !known[r.Phase] {
			return fmt.Errorf("phase %q is not one of the seven of SP-T05-1: %s", r.Phase, joinPhases(Spine()))
		}
		if _, seen := first[r.Phase]; !seen {
			first[r.Phase] = i
		}
	}
	last := -1
	for _, p := range spine {
		i, ok := first[p]
		if !ok {
			return fmt.Errorf("the phase %s never happened; every job passes through all seven steps of SP-T05-1", p)
		}
		if i < last {
			return fmt.Errorf("%s came before the phase it follows in the spine %s", p, joinPhases(Spine()))
		}
		last = i
	}
	return nil
}
