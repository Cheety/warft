package outbox

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func entry(order, target, hash string) Entry {
	return Entry{Order: order, Target: target, ContentHash: hash, PayloadRef: "/tmp/patch"}
}

const branchTarget = "git+/srv/git/repo.git#feature/x"

// SP-K03-2: the key is a domain key. The same patch onto the same branch is the same push, no
// matter how many attempts produced it — so the attempt number may not be in the key, and two
// different attempts of one job must collide.
func TestTheDomainKeyIsOrderTargetAndContentHash(t *testing.T) {
	a := Key{Order: "o1", Target: branchTarget, ContentHash: "h1"}
	b := Key{Order: "o1", Target: branchTarget, ContentHash: "h1"}
	if a.ID() != b.ID() {
		t.Fatalf("two statements of the same effect produced two keys: %s != %s", a.ID(), b.ID())
	}
	for _, other := range []Key{
		{Order: "o2", Target: branchTarget, ContentHash: "h1"},
		{Order: "o1", Target: "git+/srv/git/repo.git#main", ContentHash: "h1"},
		{Order: "o1", Target: branchTarget, ContentHash: "h2"},
	} {
		if other.ID() == a.ID() {
			t.Fatalf("%s collided with %s", other, a)
		}
	}
}

// A field moving from one part of the key to another must change the key. Without the field names
// in the encoding, order "a" + target "b" and order "ab" + target "" would hash the same.
func TestTheEncodingIsUnambiguous(t *testing.T) {
	a := Key{Order: "git+x", Target: "git+y#b", ContentHash: "h"}
	b := Key{Order: "git+xgit+y#b", Target: "", ContentHash: "h"}
	if a.ID() == b.ID() {
		t.Fatal("two different keys hashed the same — the encoding is ambiguous")
	}
}

// AB-K03-2, at the store: two attempts, one entry. The second Record answers with the first one's
// entry and says it was not fresh, rather than writing a second file.
func TestRecordingTheSameEffectTwiceProducesOneEntry(t *testing.T) {
	s := New(t.TempDir())
	first, fresh, err := s.Record(entry("o1", branchTarget, "h1"))
	if err != nil || !fresh {
		t.Fatalf("the first record was not fresh: %v %v", fresh, err)
	}
	second, fresh, err := s.Record(entry("o1", branchTarget, "h1"))
	if err != nil {
		t.Fatal(err)
	}
	if fresh {
		t.Fatal("the second record of the same domain key claimed to be fresh — that is the double push")
	}
	if second.Recorded != first.Recorded {
		t.Fatalf("the second record replaced the first: %s != %s", second.Recorded, first.Recorded)
	}
	all, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("%d entries in the outbox, expected 1", len(all))
	}
}

// SP-K03-1: the pod produces an intent, the gate executes. Recording executes nothing, so a fresh
// entry is `recorded` and never anything further along.
func TestRecordingExecutesNothing(t *testing.T) {
	s := New(t.TempDir())
	e, _, err := s.Record(entry("o1", branchTarget, "h1"))
	if err != nil {
		t.Fatal(err)
	}
	if e.State != Recorded {
		t.Fatalf("a fresh entry is %s, expected %s", e.State, Recorded)
	}
	if e.Receipt != nil {
		t.Fatal("recording produced a receipt — the gate executes, not the outbox (SP-K03-1)")
	}
}

// SP-K03-4: record first, then execute, then acknowledge. Begin has to have written `executing`
// before the caller runs, or a crash mid-call would leave no evidence that anything was attempted.
func TestTheRegisterRecordsBeforeItExecutes(t *testing.T) {
	s := New(t.TempDir())
	e := entry("o1", branchTarget, "h1")
	e.RequiresRegister = true
	if _, _, err := s.Record(e); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(e.Key()); err != nil {
		t.Fatal(err)
	}
	// Read it back off the disk rather than trusting the returned value: what matters is what a
	// second process would find after this one died.
	onDisk, err := New(s.Dir).Get(e.Key())
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.State != Executing {
		t.Fatalf("the disk says %s before the gate was called, expected %s", onDisk.State, Executing)
	}
}

// SP-K03-4's second sentence, and the one requirement in the system that forbids a retry: a
// non-idempotent target whose acknowledgement never came may not be executed again.
func TestAMissingAcknowledgementAsksAndNeverRetries(t *testing.T) {
	s := New(t.TempDir())
	e := entry("o1", "https://mail.example.org/send", "h1")
	e.RequiresRegister = true
	if _, _, err := s.Record(e); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(e.Key()); err != nil {
		t.Fatal(err)
	}
	// The worker died here. Another one comes along and tries again.
	if _, err := s.Begin(e.Key()); !errors.Is(err, ErrAskDoNotRetry) {
		t.Fatalf("a second Begin on a non-idempotent target answered %v, expected ErrAskDoNotRetry", err)
	}
	after, err := s.Get(e.Key())
	if err != nil {
		t.Fatal(err)
	}
	if after.State != Asking {
		t.Fatalf("the entry is %s, expected %s — asking is what replaces retrying (SP-K03-4)", after.State, Asking)
	}
	if after.Cause == "" {
		t.Fatal("an entry waiting on a human carries no cause")
	}
	// And there is no way back: even a third attempt gets the refusal, not an execution.
	if _, err := s.Begin(e.Key()); !errors.Is(err, ErrAskDoNotRetry) {
		t.Fatalf("Asking was not terminal: %v", err)
	}
}

// The other half of the same rule: an *idempotent* target may be executed again after a crash,
// because the domain key makes the second call the same call. Forbidding it here would strand every
// push whose worker died.
func TestAnIdempotentTargetMayBeExecutedAgain(t *testing.T) {
	s := New(t.TempDir())
	e := entry("o1", branchTarget, "h1")
	if _, _, err := s.Record(e); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(e.Key()); err != nil {
		t.Fatal(err)
	}
	again, err := s.Begin(e.Key())
	if err != nil {
		t.Fatalf("an idempotent target refused a second attempt: %v", err)
	}
	if again.Attempt != 2 {
		t.Fatalf("attempt %d, expected 2", again.Attempt)
	}
}

// SP-K03-6: the outbox lies on /var and survives the pod and the restart of the worker. A second
// Store over the same directory is what a restarted worker is.
func TestTheOutboxSurvivesTheProcessThatWroteIt(t *testing.T) {
	dir := t.TempDir()
	e := entry("o1", branchTarget, "h1")
	if _, _, err := New(dir).Record(e); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir).Acknowledge(e.Key(), Receipt{Executed: true, ExternalID: "abc123"}); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(dir).Get(e.Key())
	if err != nil {
		t.Fatal(err)
	}
	if restarted.State != Acknowledged || restarted.Receipt.ExternalID != "abc123" {
		t.Fatalf("after a restart the entry reads %s/%v", restarted.State, restarted.Receipt)
	}
}

// SP-K03-3: two gates and nothing else. An unknown scheme is refused rather than defaulted to the
// egress gate, because defaulting would make every typo an outbound request.
func TestOnlyTwoGatesExist(t *testing.T) {
	if g, err := GateFor("git+/srv/repo.git#main"); err != nil || g != Git {
		t.Fatalf("git target routed to %v (%v)", g, err)
	}
	if g, err := GateFor("https://proxy.golang.org/x"); err != nil || g != Egress {
		t.Fatalf("https target routed to %v (%v)", g, err)
	}
	for _, target := range []string{"ftp://x/y", "smtp:mail", "file:///etc/passwd", "x"} {
		if _, err := GateFor(target); err == nil {
			t.Fatalf("%q found a gate; there are two and nothing else (SP-K03-3)", target)
		}
	}
}

// SP-K03-5: replies into channels are events, not effects — so the outbox refuses one by name
// rather than inventing a third gate to execute it.
func TestAChannelReplyIsNotAnOutboxEntry(t *testing.T) {
	s := New(t.TempDir())
	if _, _, err := s.Record(entry("o1", "channel:cli#general", "h1")); !errors.Is(err, ErrChannelTarget) {
		t.Fatalf("a channel target was accepted into the outbox: %v", err)
	}
}

// A key missing one of its three parts would collide with every other key missing the same one.
func TestAnIncompleteKeyIsRefused(t *testing.T) {
	s := New(t.TempDir())
	for _, e := range []Entry{
		{Target: branchTarget, ContentHash: "h"},
		{Order: "o", ContentHash: "h"},
		{Order: "o", Target: branchTarget},
	} {
		if _, _, err := s.Record(e); err == nil {
			t.Fatalf("%+v was accepted as a domain key", e)
		}
	}
}

// Pending is what a drain acts on. Asking is not in it: that state's whole meaning is that the
// machine stops and a human starts.
func TestPendingExcludesWhatAHumanOwns(t *testing.T) {
	s := New(t.TempDir())
	pending := entry("o1", branchTarget, "h1")
	asking := entry("o2", "https://mail.example.org/send", "h2")
	asking.RequiresRegister = true
	for _, e := range []Entry{pending, asking} {
		if _, _, err := s.Record(e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Ask(asking.Key(), "the mail gateway never answered"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Order != "o1" {
		t.Fatalf("pending is %+v, expected only o1", got)
	}
	unanswered, err := s.Unanswered()
	if err != nil {
		t.Fatal(err)
	}
	if len(unanswered) != 1 || unanswered[0].Order != "o2" {
		t.Fatalf("unanswered is %+v, expected only o2", unanswered)
	}
}

// AB-K03-2 at the gate's side: the ledger executes at most once per domain key, ever, and the
// second caller gets the first one's receipt.
func TestTheLedgerExecutesOncePerDomainKey(t *testing.T) {
	l := OpenLedger(filepath.Join(t.TempDir(), "ledger"))
	e := entry("o1", branchTarget, "h1")
	calls := 0
	exec := func() (Receipt, error) {
		calls++
		return Receipt{Executed: true, ExternalID: "commit-1"}, nil
	}
	if _, executed, err := l.Once(e, exec); err != nil || !executed {
		t.Fatalf("the first call did not execute: %v %v", executed, err)
	}
	r, executed, err := l.Once(e, exec)
	if err != nil {
		t.Fatal(err)
	}
	if executed {
		t.Fatal("the second call executed — that is the double push (SP-K03-2)")
	}
	if r.ExternalID != "commit-1" {
		t.Fatalf("the second call got receipt %q, expected the first one's", r.ExternalID)
	}
	if calls != 1 {
		t.Fatalf("the effect ran %d times, expected 1", calls)
	}
}

// AB-K03-5: the adapter deduplicates via the event ID, and a restart produces no second message.
func TestTheSeenSetRemembersAcrossARestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "seen")
	seen, err := OpenLedger(dir).Seen("ev-1")
	if err != nil || seen {
		t.Fatalf("the first sighting reported seen=%v (%v)", seen, err)
	}
	// A new Ledger over the same directory is what a restarted process is.
	seen, err = OpenLedger(dir).Seen("ev-1")
	if err != nil || !seen {
		t.Fatalf("after a restart the event was not remembered: seen=%v (%v)", seen, err)
	}
	if seen, err := OpenLedger(dir).Seen("ev-2"); err != nil || seen {
		t.Fatalf("a different event id was taken for the same one: %v %v", seen, err)
	}
}

// SP-K03-4 through the drain: an entry a dead worker left in `executing` on a non-idempotent target
// is not silently skipped. The drain moves it to Asking and counts it, so the question reaches
// whoever is reading the drain's output rather than sitting in a state nobody looks at.
//
// The gates are not reachable in this test and must not need to be: nothing here may be executed.
func TestTheDrainAsksAboutAStrandedRegisterEntry(t *testing.T) {
	s := New(t.TempDir())
	e := entry("o1", "https://mail.example.invalid/send", "h1")
	e.RequiresRegister = true
	if _, _, err := s.Record(e); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(e.Key()); err != nil {
		t.Fatal(err)
	}
	// The worker died here, leaving the entry in Executing.
	d, err := Drain(context.Background(), s, Sockets{Git: "/nonexistent", Egress: "/nonexistent"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Asked != 1 {
		t.Fatalf("the drain asked about %d entries, expected 1 (%+v)", d.Asked, d)
	}
	if d.Executed != 0 || d.Deduplicated != 0 {
		t.Fatalf("the drain executed something it was told not to retry: %+v", d)
	}
	after, err := s.Get(e.Key())
	if err != nil {
		t.Fatal(err)
	}
	if after.State != Asking {
		t.Fatalf("the entry is %s after the drain, expected %s", after.State, Asking)
	}
}
