package budget

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The table is OP-1's, and these are the three rules the ruling states it obeys. They are checked
// here as well as in acceptance/v04-budget.sh: the script holds the file against the ruling, this
// holds the file against itself, and a change that satisfies neither cannot reach a build.

func TestRuledTableIsComplete(t *testing.T) {
	caps := Ruled()
	for _, level := range Levels() {
		for _, scope := range Scopes() {
			p, err := caps.For(level, scope)
			if err != nil {
				t.Fatalf("%s/%s: %v", level, scope, err)
			}
			if p.PodMinutes <= 0 || p.Tokens <= 0 || p.MoneyMicros <= 0 {
				t.Errorf("%s/%s: a pot with a cap of zero admits nothing: %+v", level, scope, p)
			}
		}
	}
	if _, err := caps.For("root", ScopeProject); err == nil {
		t.Error("an authority level nobody ruled must be an error, not a default cap")
	}
}

func TestOP1GeneratingRules(t *testing.T) {
	caps := Ruled()
	for _, level := range Levels() {
		for _, scope := range []Scope{ScopeEnvelope, ScopeProject, ScopePrincipalDay} {
			p, err := caps.For(level, scope)
			if err != nil {
				t.Fatal(err)
			}
			if want := 8000 * p.PodMinutes; p.Tokens != want {
				t.Errorf("%s/%s: tokens = 8000 · pod_minutes is OP-1's rule 1; got %d, want %d",
					level, scope, p.Tokens, want)
			}
			if want := 5 * p.Tokens; p.MoneyMicros != want {
				t.Errorf("%s/%s: money = 5 µ€ · tokens is OP-1's rule 2; got %d, want %d",
					level, scope, p.MoneyMicros, want)
			}
		}

		// Rule 3: the channel pot binds pod minutes only, so its token and money caps are the
		// principal-day ones and it can never be the pot that refuses a token or a euro.
		day, _ := caps.For(level, ScopePrincipalDay)
		channel, _ := caps.For(level, ScopePrincipalChannelDay)
		if channel.Tokens != day.Tokens || channel.MoneyMicros != day.MoneyMicros {
			t.Errorf("%s: the channel pot must carry the day's token and money caps (OP-1 rule 3): %+v vs %+v",
				level, channel, day)
		}
		if channel.PodMinutes*2 != day.PodMinutes {
			t.Errorf("%s: one channel is half the day in pod minutes (SP-T01-8): %d of %d",
				level, channel.PodMinutes, day.PodMinutes)
		}
	}
}

func TestPublicIsSmallerThanLinkedIsSmallerThanConfidential(t *testing.T) {
	caps := Ruled()
	for _, scope := range Scopes() {
		var last int64
		for _, level := range Levels() {
			p, err := caps.For(level, scope)
			if err != nil {
				t.Fatal(err)
			}
			if p.PodMinutes <= last {
				t.Errorf("%s at %s: %d pod minutes does not exceed the level below it (%d) — §19 asks for public very small, confidential a tenant cap",
					scope, level, p.PodMinutes, last)
			}
			last = p.PodMinutes
		}
	}
}

// SP-V04-2: running out of tokens produces a reply with options, not a silent truncation. Money is
// the opposite end of the same requirement — a hard limit with exactly one way out, and that way is
// a human.

func TestRefusalCarriesOptions(t *testing.T) {
	tokens := Exhausted(ScopeProject, "linked", "tokens", 500_000, 12_000, 3_840_000)
	if len(tokens.Options) < 2 {
		t.Fatalf("a token refusal is a reply with options (SP-V04-2), got %d", len(tokens.Options))
	}
	msg := tokens.Error()
	for _, want := range []string{"budget.exhausted", "12000", "500000", "split the goal"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal must state %q: %s", want, msg)
		}
	}

	money := Exhausted(ScopePrincipalDay, "linked", "money", 10, 0, 38_400_000)
	if len(money.Options) != 1 {
		t.Fatalf("money is a hard limit with one way out (SP-V04-2), got %d options", len(money.Options))
	}
	if !strings.Contains(money.Options[0], "two people") {
		t.Errorf("only a human raises the money cap, and that is two people (E-08): %q", money.Options[0])
	}
	if Cause != "budget.exhausted" {
		t.Errorf("the cause is a cause_code of the state contract, got %q", Cause)
	}
}

// SP-E08-3: the halt is a field in admission and a file on the control node. These check the file
// half — the one that has to work when the API does not.

func TestHaltFileAbsentIsNotHalted(t *testing.T) {
	h, err := ReadHaltFile(filepath.Join(t.TempDir(), "halt"))
	if err != nil {
		t.Fatalf("an absent halt file is the ordinary state of a cell: %v", err)
	}
	if h.Active(time.Now()) {
		t.Error("no file, no halt")
	}
}

func TestHaltFileStopsAdmission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "halt")
	write(t, path, "reason: the model provider answers nonsense\nset_by: duty officer\n")

	h, err := ReadHaltFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Active(time.Now()) {
		t.Fatal("a halt file that exists halts the cell (SP-E08-3)")
	}
	if h.Source != "file" || h.SetBy != "duty officer" {
		t.Errorf("the halt names its path and its author: %+v", h)
	}
	if !strings.Contains(h.Refusal().Error(), "run to completion") {
		t.Errorf("the refusal states what happens to running jobs (SP-V04-2): %s", h.Refusal().Error())
	}
}

func TestHaltExpiresAfterSixtyMinutes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "halt")
	set := time.Now().Add(-90 * time.Minute)
	write(t, path, "reason: an hour and a half ago\nset_at: "+set.Format(time.RFC3339)+"\n")
	if err := os.Chtimes(path, set, set); err != nil {
		t.Fatal(err)
	}

	h, err := ReadHaltFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if h.Active(time.Now()) {
		t.Error("SP-E08-4: a halt nobody renewed for 60 minutes has expired, and that is mandatory")
	}

	// Touching the file is the renewal on this path — decisions/halt-file.md, and the only renewal
	// available while the API is down.
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}
	h, err = ReadHaltFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Active(now) {
		t.Error("touching the halt file renews it (decisions/halt-file.md)")
	}
}

func TestUnreadableSetAtIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "halt")
	write(t, path, "reason: whatever\nset_at: yesterday afternoon\n")
	if _, err := ReadHaltFile(path); err == nil {
		t.Error("a halt whose age cannot be read cannot expire, so it is an error and not a guess")
	}
}

func TestBothPathsHalt(t *testing.T) {
	now := time.Now()
	api := Halt{InForce: true, Reason: "over the API", SetBy: "duty officer",
		SetAt: now, ExpiresAt: now.Add(HaltExpiry), Source: "api"}

	// The API path alone halts.
	h, err := ReadHalt(filepath.Join(t.TempDir(), "halt"), api, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Active(now) || h.Source != "api" {
		t.Errorf("the field in admission is the first path (SP-E08-3): %+v", h)
	}

	// The file path halts while the database says nothing and cannot be asked — the case the
	// second path exists for.
	dir := t.TempDir()
	path := filepath.Join(dir, "halt")
	write(t, path, "reason: the API is not answering\n")
	h, err = ReadHalt(path, Halt{}, os.ErrDeadlineExceeded, now)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Active(now) || h.Source != "file" {
		t.Errorf("the file takes effect when the API no longer answers (SP-E08-3): %+v", h)
	}
}

// SP-V04-4: weighted shares of the bottleneck. A heavy sender gets a lot, not everything.

func TestHeavySenderGetsALotNotEverything(t *testing.T) {
	grants := Share(100, []Claim{
		{Principal: "heavy", Weight: 1, Want: 1000},
		{Principal: "light", Weight: 1, Want: 10},
	})
	byName := index(grants)
	if byName["light"] != 10 {
		t.Errorf("a light claim is met in full while capacity remains, got %d", byName["light"])
	}
	if byName["heavy"] != 90 {
		t.Errorf("the heavy sender takes what is left over, not what it asked for: got %d", byName["heavy"])
	}
	if byName["heavy"]+byName["light"] > 100 {
		t.Error("the bottleneck is not overbooked")
	}
}

func TestWeightsAreShares(t *testing.T) {
	grants := index(Share(120, []Claim{
		{Principal: "a", Weight: 3, Want: 1000},
		{Principal: "b", Weight: 1, Want: 1000},
	}))
	if grants["a"] != 90 || grants["b"] != 30 {
		t.Errorf("three to one is 90/30 of 120, got %d/%d", grants["a"], grants["b"])
	}
}

func TestNobodyIsStarvedAndNothingIsLost(t *testing.T) {
	claims := []Claim{
		{Principal: "a", Weight: 1, Want: 5},
		{Principal: "b", Weight: 1, Want: 5},
		{Principal: "c", Weight: 1, Want: 5},
	}
	grants := Share(7, claims)
	var total int64
	for _, g := range grants {
		if g.Granted == 0 {
			t.Errorf("%s was starved: %+v", g.Principal, g)
		}
		total += g.Granted
	}
	if total != 7 {
		t.Errorf("all seven units are handed out, got %d", total)
	}

	// Determinism: a fairness rule whose answer depends on map order is not one.
	first := Share(7, claims)
	for i, g := range Share(7, claims) {
		if g != first[i] {
			t.Errorf("the same claims produce the same grants; %+v vs %+v", g, first[i])
		}
	}
}

func TestNoCapacityGrantsNothing(t *testing.T) {
	for _, g := range Share(0, []Claim{{Principal: "a", Weight: 1, Want: 5}}) {
		if g.Granted != 0 {
			t.Errorf("an exhausted bottleneck hands out nothing, got %+v", g)
		}
	}
}

func index(grants []Grant) map[string]int64 {
	out := map[string]int64{}
	for _, g := range grants {
		out[g.Principal] = g.Granted
	}
	return out
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
