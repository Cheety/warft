package runner

import (
	"encoding/json"
	"strings"
	"testing"
)

// job is a job that runs: enough of SP-K01-1's identity to be reported on, a class to allocate by,
// requirements to resolve an image from and an edit command.
func job(class string) Job {
	return Job{
		OrderID:      "018f4242-0000-7000-8000-000000000001",
		Attempt:      1,
		Cell:         "eu-c1",
		Project:      "018f4242-0000-7000-8000-00000000000b",
		Class:        class,
		Requirements: Requirements{Language: "sh", LanguageVersion: "5"},
		Command:      []string{"/usr/bin/true"},
	}
}

// SP-T05-1: a fixed spine, the same for all jobs. The order is the panel's own sentence, and it is
// checked here because everything else in T-05 is stated relative to it.
func TestSpineIsThePanelsSeven(t *testing.T) {
	want := []Phase{PhasePrepare, PhasePlan, PhaseEdit, PhaseCheck, PhaseRepair, PhaseDeliver, PhaseReap}
	got := Spine()
	if len(got) != len(want) {
		t.Fatalf("the spine has %d steps, not %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("step %d is %s, the panel says %s", i+1, got[i], want[i])
		}
	}
}

// SP-T05-4: no runtime object may change the definition. A caller that writes into what Spine hands
// back must not be writing into the spine itself.
func TestSpineCannotBeChangedByItsCaller(t *testing.T) {
	s := Spine()
	s[0] = "deploy"
	if Spine()[0] != PhasePrepare {
		t.Error("changing a copy of the spine changed the spine (SP-T05-4)")
	}
}

// AB-T05-1, as an invariant of the program rather than of a script: every job passes through all
// seven, in order, and nothing else appears.
func TestVerifySpine(t *testing.T) {
	full := func() []PhaseRecord {
		var l PhaseLog
		for _, p := range Spine() {
			l.Add(p, Ran, 0, 0, "ran")
		}
		return l.Records
	}

	if err := VerifySpine(full()); err != nil {
		t.Fatalf("the seven steps in order were refused: %v", err)
	}

	// The loop repeats two of them, which is what SP-T05-3 asks for and must stay lawful.
	var loop PhaseLog
	loop.Add(PhasePrepare, Ran, 0, 0, "x")
	loop.Add(PhasePlan, Skipped, 0, 0, "x")
	loop.Add(PhaseEdit, Ran, 0, 0, "x")
	loop.Add(PhaseCheck, Failed, 0, 0, "x")
	loop.Add(PhaseRepair, Ran, 1, 0, "x")
	loop.Add(PhaseCheck, Failed, 1, 0, "x")
	loop.Add(PhaseRepair, Ran, 2, 0, "x")
	loop.Add(PhaseCheck, Failed, 2, 0, "x")
	loop.Add(PhaseDeliver, Ran, 0, 0, "x")
	loop.Add(PhaseReap, Ran, 0, 0, "x")
	if err := VerifySpine(loop.Records); err != nil {
		t.Errorf("a job with two rework rounds left the spine: %v", err)
	}

	missing := full()[1:]
	if err := VerifySpine(missing); err == nil {
		t.Error("a job that skipped a step was accepted (SP-T05-1)")
	}

	swapped := full()
	swapped[2], swapped[3] = swapped[3], swapped[2]
	if err := VerifySpine(swapped); err == nil {
		t.Error("check before edit was accepted; the spine is fixed (SP-T05-1)")
	}

	extra := append(full(), PhaseRecord{Phase: "deploy", Outcome: Ran})
	if err := VerifySpine(extra); err == nil {
		t.Error("an eighth phase was accepted (SP-T05-1)")
	}
}

// SP-T05-4: the definition is versioned, and the version names the spine too. A change to either
// half has to change the hash, or `pipeline@version` names two different things on two days.
func TestContentHashCoversSpineAndPlaces(t *testing.T) {
	p, err := PipelineByRef(DefaultPipeline)
	if err != nil {
		t.Fatal(err)
	}
	base := p.ContentHash()
	if !strings.HasPrefix(base, "sha256:") {
		t.Errorf("a content hash names its algorithm: %s", base)
	}

	for name, change := range map[string]func(Pipeline) Pipeline{
		"another version":     func(q Pipeline) Pipeline { q.Version = "2"; return q },
		"another description": func(q Pipeline) Pipeline { q.Description = "something else"; return q },
		"another reap":        func(q Pipeline) Pipeline { q.ReapAfter = ReapAfterReport; return q },
		"a plan demanded":     func(q Pipeline) Pipeline { q.Places.PlanRequired = true; return q },
		"a path":              func(q Pipeline) Pipeline { q.Places.Paths = []string{"src"}; return q },
		"a check": func(q Pipeline) Pipeline {
			q.Places.Checks = []Check{{Name: "unit", Command: []string{"true"}, Blocks: true}}
			return q
		},
		"another round count": func(q Pipeline) Pipeline { q.Places.ReworkRounds = 2; return q },
		"another acceptance":  func(q Pipeline) Pipeline { q.Places.Acceptance = "tests.new"; return q },
		"a kept snapshot":     func(q Pipeline) Pipeline { q.Places.KeepSnapshotOnFailure = true; return q },
	} {
		if change(p).ContentHash() == base {
			t.Errorf("%s did not change the content hash", name)
		}
	}

	// The order of the checks is part of the definition: it decides which failure a reply names
	// first, so two orders are two pipelines.
	one := p
	one.Places.Checks = []Check{{Name: "a", Command: []string{"true"}}, {Name: "b", Command: []string{"true"}}}
	other := p
	other.Places.Checks = []Check{{Name: "b", Command: []string{"true"}}, {Name: "a", Command: []string{"true"}}}
	if one.ContentHash() == other.ContentHash() {
		t.Error("two check orders share one content hash")
	}
}

// The catalog is embedded, so a broken entry is a broken binary. Every definition in it must stand
// on its own before any job pins it.
func TestCatalogIsValidAndCarriesTheDefault(t *testing.T) {
	ps := Pipelines()
	if len(ps) == 0 {
		t.Fatal("the artifact carries no pipeline; a job that pins nothing would have nothing to run under")
	}
	for _, p := range ps {
		if err := p.Validate(); err != nil {
			t.Errorf("%s: %v", p.Ref(), err)
		}
	}
	if _, err := PipelineByRef(DefaultPipeline); err != nil {
		t.Errorf("the default pipeline is not in the catalog: %v", err)
	}
	if _, err := PipelineByRef("default@99"); err == nil {
		t.Error("a version that does not exist was resolved anyway (SP-T05-4)")
	}
}

// decisions/OP-2.md: §19's three is the default, the class carries the ceiling, and the smaller of
// the two wins.
func TestReworkRoundsAgainstTheRuledCeilings(t *testing.T) {
	ceilings := RoundCeilings()
	for _, class := range []string{"tiny", "small", "medium", "large"} {
		if _, ok := ceilings[class]; !ok {
			t.Fatalf("no ceiling is ruled for %s (decisions/OP-2.md)", class)
		}
	}
	if _, err := RoundCeiling("enormous"); err == nil {
		t.Error("a class nobody ruled a ceiling for got one anyway; an unbounded loop is what OP-2 prevents")
	}

	def, err := PipelineByRef(DefaultPipeline)
	if err != nil {
		t.Fatal(err)
	}
	for _, class := range []string{"tiny", "small", "medium", "large"} {
		eff, err := job(class).Effective()
		if err != nil {
			t.Fatalf("%s: %v", class, err)
		}
		want := min(def.Places.ReworkRounds, ceilings[class])
		if eff.Places.ReworkRounds != want {
			t.Errorf("%s runs %d rounds, the ruling allows %d", class, eff.Places.ReworkRounds, want)
		}
	}
}

// A job may move place five downwards freely and may not move it up: over the ceiling it is
// refused, not clamped (decisions/OP-2.md).
func TestAJobMayLowerTheRoundsAndNotRaiseThem(t *testing.T) {
	fewer := 1
	j := job("medium")
	j.Places = &PlaceMoves{ReworkRounds: &fewer}
	eff, err := j.Effective()
	if err != nil {
		t.Fatalf("a job asking for fewer rounds was refused: %v", err)
	}
	if eff.Places.ReworkRounds != 1 {
		t.Errorf("the job asked for 1 round and got %d", eff.Places.ReworkRounds)
	}

	none := 0
	j.Places = &PlaceMoves{ReworkRounds: &none}
	if eff, err := j.Effective(); err != nil || eff.Places.ReworkRounds != 0 {
		t.Errorf("zero rounds is a lawful job — check once, never repair: %v %d", err, eff.Places.ReworkRounds)
	}

	more := 9
	j.Places = &PlaceMoves{ReworkRounds: &more}
	_, err = j.Effective()
	if err == nil {
		t.Fatal("a job asking for nine rounds on a medium pod was accepted (decisions/OP-2.md)")
	}
	if !strings.Contains(err.Error(), "OP-2") {
		t.Errorf("the refusal does not name the ruling it comes from: %v", err)
	}

	// A tiny pod's ceiling is one, so the same number is lawful on medium and refused here.
	tiny := job("tiny")
	two := 2
	tiny.Places = &PlaceMoves{ReworkRounds: &two}
	if _, err := tiny.Effective(); err == nil {
		t.Error("two rounds on a tiny pod was accepted; the ceiling is per class")
	}
}

// SP-T05-2 is a closed list. A job that moves one of the seven is a job; a job that moves anything
// else is not one, and the decoder is where that is decided (AB-T05-2).
func TestAJobMayNotMoveAnEighthThing(t *testing.T) {
	good := `{"order_id":"o","attempt":1,"cell":"eu-c1","project":"p","class":"medium",
	          "requirements":{"language":"sh","language_version":"5"},"command":["/usr/bin/true"],
	          "places":{"plan_required":true,"rework_rounds":1}}`
	if _, err := DecodeJob([]byte(good)); err != nil {
		t.Fatalf("a job that moves two of the seven places was refused: %v", err)
	}

	for name, body := range map[string]string{
		"a spine of its own":    `{"order_id":"o","spine":["prepare","deliver"]}`,
		"phases of its own":     `{"order_id":"o","phases":[{"phase":"edit"}]}`,
		"an eighth place":       `{"order_id":"o","places":{"network":true}}`,
		"a renamed place":       `{"order_id":"o","places":{"rounds":2}}`,
		"a pipeline of its own": `{"order_id":"o","pipeline":{"name":"mine","version":"1"}}`,
	} {
		if _, err := DecodeJob([]byte(body)); err == nil {
			t.Errorf("%s was accepted; SP-T05-2 names seven places and there is no eighth", name)
		}
	}
}

// Moved is what a reply and the probe read: which of the seven this job moves, and nothing else can
// appear in that answer.
func TestMovedNamesOnlyThePlaces(t *testing.T) {
	j := job("medium")
	plan := true
	acc := "tests.new"
	j.Places = &PlaceMoves{PlanRequired: &plan, Acceptance: &acc}
	moved, err := j.Moved()
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, p := range PlaceNames {
		known[p] = true
	}
	for _, m := range moved {
		if !known[m] {
			t.Errorf("%q is not one of SP-T05-2's seven places", m)
		}
	}
	// The image counts as moved because this job carries requirements of its own; plan_required and
	// acceptance because it states them.
	want := map[string]bool{"image": true, "plan_required": true, "acceptance": true}
	if len(moved) != len(want) {
		t.Errorf("moved %v, expected %v", moved, want)
	}
	for _, m := range moved {
		if !want[m] {
			t.Errorf("%q was reported as moved and is not", m)
		}
	}
}

// A place is only movable to something the platform has a word for. Q-02: an acceptance criterion
// that is not an evidence class would let a job deliver against a claim nobody can check.
func TestPlacesAreRefusedWhenTheyNameNothing(t *testing.T) {
	bad := "it looked right"
	j := job("medium")
	j.Places = &PlaceMoves{Acceptance: &bad}
	if _, err := j.Effective(); err == nil {
		t.Error("an acceptance criterion outside evidence_class was accepted (Q-02)")
	}

	escape := []string{"../etc"}
	j.Places = &PlaceMoves{Paths: &escape}
	if _, err := j.Effective(); err == nil {
		t.Error("a path leaving the working copy was accepted (SP-T05-2, place three)")
	}

	absolute := []string{"/etc"}
	j.Places = &PlaceMoves{Paths: &absolute}
	if _, err := j.Effective(); err == nil {
		t.Error("an absolute path was accepted; place three is relative to the working copy")
	}

	nameless := []Check{{Command: []string{"true"}}}
	j.Places = &PlaceMoves{Checks: &nameless}
	if _, err := j.Effective(); err == nil {
		t.Error("a check without a name was accepted; a reply could not report it")
	}
}

// A job pinning a definition that does not exist is refused before anything is created — the same
// moment every other unrunnable job is refused (SP-T05-4).
func TestValidateRefusesAnUnknownPipeline(t *testing.T) {
	j := job("medium")
	j.PipelineVersion = "invented@7"
	err := j.Validate()
	if err == nil {
		t.Fatal("a job pinned a pipeline the artifact does not carry and was accepted")
	}
	if !strings.Contains(err.Error(), "invented@7") {
		t.Errorf("the refusal does not name what was pinned: %v", err)
	}

	j.PipelineVersion = DefaultPipeline
	if err := j.Validate(); err != nil {
		t.Errorf("a job pinning the default pipeline was refused: %v", err)
	}
}

// The places a job did not move stay exactly as the human filed them. This is the other half of
// SP-T05-4: a job changes what it runs, never what the pipeline is.
func TestAJobDoesNotChangeTheDefinition(t *testing.T) {
	before, err := PipelineByRef(DefaultPipeline)
	if err != nil {
		t.Fatal(err)
	}
	j := job("medium")
	rounds := 0
	checks := []Check{{Name: "unit", Command: []string{"false"}, Blocks: true}}
	j.Places = &PlaceMoves{ReworkRounds: &rounds, Checks: &checks}
	if _, err := j.Effective(); err != nil {
		t.Fatal(err)
	}
	after, err := PipelineByRef(DefaultPipeline)
	if err != nil {
		t.Fatal(err)
	}
	if before.ContentHash() != after.ContentHash() {
		t.Error("running a job changed the pipeline definition (SP-T05-4)")
	}
}

// The job travels between host and pod as JSON, so what the host writes the pod has to read back
// unchanged — including a place moved to false, which a non-pointer field could not carry.
func TestPlacesSurviveTheWire(t *testing.T) {
	keep := false
	plan := false
	j := job("medium")
	j.Places = &PlaceMoves{KeepSnapshotOnFailure: &keep, PlanRequired: &plan}
	b, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeJob(b)
	if err != nil {
		t.Fatal(err)
	}
	if back.Places == nil || back.Places.KeepSnapshotOnFailure == nil || *back.Places.KeepSnapshotOnFailure {
		t.Error("a place moved to false did not survive the wire")
	}
}
