package runner

import (
	"strings"
	"testing"
)

// SP-T03-1 forms the requirement hash from what an image has to carry. Two jobs that ask for the
// same thing must land on the same key, or the index splits and every job is a miss.
func TestRequirementHashIsStable(t *testing.T) {
	a := Requirements{Language: "go", LanguageVersion: "1.24", SystemPackages: []string{"git", "make"}, TestRunner: "go test"}
	b := Requirements{Language: "go", LanguageVersion: "1.24", SystemPackages: []string{"make", "git"}, TestRunner: "go test"}
	if a.Hash() != b.Hash() {
		t.Errorf("the same packages in another order gave another key:\n%s\n%s", a.Hash(), b.Hash())
	}
	if !strings.HasPrefix(a.Hash(), "sha256:") {
		t.Errorf("a requirement hash names its algorithm: %s", a.Hash())
	}
}

// And the other direction: a requirement that changes the image must change the key, or a job gets
// an image that cannot run it.
func TestRequirementHashSeparates(t *testing.T) {
	base := Requirements{Language: "go", LanguageVersion: "1.24"}
	for name, other := range map[string]Requirements{
		"another version":  {Language: "go", LanguageVersion: "1.23"},
		"another language": {Language: "rust", LanguageVersion: "1.24"},
		"a package more":   {Language: "go", LanguageVersion: "1.24", SystemPackages: []string{"git"}},
		"a test runner":    {Language: "go", LanguageVersion: "1.24", TestRunner: "gotestsum"},
		"a browser":        {Language: "go", LanguageVersion: "1.24", BrowserEngine: "chromium"},
	} {
		if base.Hash() == other.Hash() {
			t.Errorf("%s did not change the requirement hash", name)
		}
	}
}

func TestEmptyRequirements(t *testing.T) {
	if !(Requirements{}).Empty() {
		t.Error("nothing asked for is not empty")
	}
	if (Requirements{SystemPackages: []string{"git"}}).Empty() {
		t.Error("a package asked for reads as empty")
	}
}

func TestJobValidate(t *testing.T) {
	ok := Job{
		OrderID: "018f4242-0000-7000-8000-000000000001", Attempt: 1,
		Cell: "eu-c1", Project: "018f4242-0000-7000-8000-00000000000b",
		Class: "small", Command: []string{"true"},
		Requirements: Requirements{Language: "go"},
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("a complete job was refused: %v", err)
	}

	for name, break_ := range map[string]func(*Job){
		"no order":   func(j *Job) { j.OrderID = "" },
		"no cell":    func(j *Job) { j.Cell = "" },
		"no project": func(j *Job) { j.Project = "" },
		"no class":   func(j *Job) { j.Class = "" },
		"no command": func(j *Job) { j.Command = nil },
		"no image and no requirements": func(j *Job) {
			j.Requirements, j.ImageDigest = Requirements{}, ""
		},
	} {
		j := ok
		break_(&j)
		if err := j.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// A job that names its image needs no requirements: order.image_hash is already the answer the
	// requirement hash would have looked up.
	j := ok
	j.Requirements, j.ImageDigest = Requirements{}, "sha256:abc"
	if err := j.Validate(); err != nil {
		t.Errorf("a job naming its image was refused: %v", err)
	}
}

// SP-T04-3's chain, forwards and backwards.
func TestLifecycle(t *testing.T) {
	chain := []State{Created, Active, Frozen, Checkpointed, Reaped}
	for i := 0; i+1 < len(chain); i++ {
		if !CanTransition(chain[i], chain[i+1]) {
			t.Errorf("the panel's own chain is broken at %s → %s", chain[i], chain[i+1])
		}
	}
	for _, s := range []State{Created, Active, Frozen, Checkpointed} {
		if !CanTransition(s, Reaped) {
			t.Errorf("%s cannot be reaped, but a lifetime can end in any state (SP-T04-5)", s)
		}
	}
	// Preemption ends (SP-RB-3), and a dump that cannot be restored is a deletion.
	if !CanTransition(Frozen, Active) {
		t.Error("a frozen pod can never run again — that is a kill with a gentler name")
	}
	if !CanTransition(Checkpointed, Active) {
		t.Error("a checkpoint that cannot be restored is a deletion")
	}
	for _, bad := range [][2]State{
		{Created, Frozen}, {Active, Checkpointed}, {Reaped, Active}, {Reaped, Reaped}, {Active, Created},
	} {
		if CanTransition(bad[0], bad[1]) {
			t.Errorf("%s → %s is not a transition SP-T04-3 has", bad[0], bad[1])
		}
	}
}

// The three unbuilt pools refuse by the name of the work package that builds them, the way the
// binary's unbuilt entry points do (AB-E02-1).
func TestNotBuiltNamesItsWorkPackage(t *testing.T) {
	for _, p := range []Platform{Windows, MacOS, Remote} {
		err := NotBuilt(p)
		if err == nil {
			t.Fatalf("%s: no refusal", p)
		}
		if !strings.Contains(err.Error(), "AP-8.3") {
			t.Errorf("%s: %q does not name the work package that builds it", p, err)
		}
	}
}
