// Package outbox is K-03's chain, from the side the pod touches: *the pod produces an intent to
// act → outbox → gate → receipt back into the job* (SP-K03-1). The pod never acts itself, and this
// package is what it acts through instead.
//
// V-02 does without a leader election by arguing that a doubly executed job is harmless. At the
// place where a patch becomes a push that is not true, and the counterpart the design needs there
// is the domain key of SP-K03-2: `order + target + content_hash`. The same patch onto the same
// branch is the same push, no matter how many attempts produced it — enforced here by making the
// key the *name of a file* and creating it with O_EXCL, so the kernel arbitrates between two
// workers rather than an agreement between them. decisions/gates-and-the-outbox.md §1 rules that.
//
// The store lies on /var (SP-K03-6, SP-A05-1): it survives the pod that wrote into it and the
// restart of the worker that was draining it. Nothing here knows what a gate does — the gates are
// roles reached over a Unix socket (internal/gitgate, internal/egress), and this package holds only
// the intents, the states they move through, and the receipts that come back.
package outbox

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// State is where an entry stands. The five of decisions/gates-and-the-outbox.md §1, and an entry is
// in exactly one of them.
type State string

const (
	// Recorded — the pod stated an intent; nothing has been executed.
	Recorded State = "recorded"
	// Executing — the register's first half, written down *before* the gate was called (SP-K03-4).
	Executing State = "executing"
	// Acknowledged — the gate answered and the receipt is in the entry.
	Acknowledged State = "acknowledged"
	// Denied — the gate refused by policy or allowlist. A terminal state, never without a cause.
	Denied State = "denied"
	// Asking — the acknowledgement is missing and a human is being asked. SP-K03-4's "if the
	// acknowledgement is missing, ask; do not retry". There is no transition out of this state
	// back into Executing, which is the only way "do not retry" can be a property rather than a
	// habit.
	Asking State = "asking"
)

// ErrAskDoNotRetry is the refusal SP-K03-4 asks for, and the only one of its kind in the system: a
// non-idempotent target whose acknowledgement never arrived may not be executed a second time. The
// entry is moved to Asking and the caller is told to ask.
var ErrAskDoNotRetry = errors.New("the acknowledgement is missing — ask, do not retry (SP-K03-4)")

// ErrNotFound is a key that was never recorded.
var ErrNotFound = errors.New("no such outbox entry")

// ErrChannelTarget refuses a reply into a channel. SP-K03-5 rules those events, deduplicated by the
// adapter via the event ID; SP-K03-3 rules there are two gates and nothing else, so an outbox entry
// aimed at a channel names an executor that does not exist.
var ErrChannelTarget = errors.New("a reply into a channel is an event, not an effect — the adapter deduplicates it by event id (SP-K03-5)")

// Key is SP-K03-2's domain key. Not a random id, not the attempt number, not a timestamp: two
// attempts of the same job that produced the same content for the same target are the same effect,
// and the key is what says so.
type Key struct {
	Order       string
	Target      string
	ContentHash string
}

// String is the key in the form a human reads in a log. ID is what names the file.
func (k Key) String() string { return k.Order + " " + k.Target + " " + k.ContentHash }

// ID is the canonical hash of the three fields. Canonical rather than JSON for the reason
// runner.Requirements.Hash is: a hash of JSON is a hash of a serializer's habits. The field names
// are in the encoding so that a value moving from one field to another changes the key.
func (k Key) ID() string {
	var b strings.Builder
	fmt.Fprintf(&b, "order=%s\ntarget=%s\ncontent_hash=%s\n", k.Order, k.Target, k.ContentHash)
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// Valid reports what is wrong with a key, or nil. All three parts are required: a key missing one of
// them would collide with every other key missing the same one, which is the opposite of what a
// domain key is for.
func (k Key) Valid() error {
	switch {
	case k.Order == "":
		return errors.New("an outbox entry belongs to an order (SP-K03-2)")
	case k.Target == "":
		return errors.New("an outbox entry names a target (SP-K03-2)")
	case k.ContentHash == "":
		return errors.New("an outbox entry names the hash of its content — that is what makes two attempts one push (SP-K03-2)")
	case strings.HasPrefix(k.Target, ChannelScheme):
		return ErrChannelTarget
	}
	if _, err := GateFor(k.Target); err != nil {
		return err
	}
	return nil
}

// The three target schemes the system knows. Two of them name a gate; the third names the mistake
// of routing a channel reply through one (SP-K03-3, SP-K03-5).
const (
	GitScheme     = "git+"
	EgressScheme  = "https://"
	ChannelScheme = "channel:"
)

// Gate is which of SP-K03-3's two gates executes a target. There are two and there is no third:
// "what does not go through a gate does not exist", which is enforceable because pods have no
// network (T-04).
type Gate string

const (
	Git    Gate = "git-gate"
	Egress Gate = "egress-gate"
)

// GateFor routes a target to its gate by scheme. An unknown scheme is refused rather than defaulted
// to the egress gate: defaulting would make every typo an outbound request.
func GateFor(target string) (Gate, error) {
	switch {
	case strings.HasPrefix(target, GitScheme):
		return Git, nil
	case strings.HasPrefix(target, EgressScheme):
		return Egress, nil
	case strings.HasPrefix(target, ChannelScheme):
		return "", ErrChannelTarget
	}
	return "", fmt.Errorf("no gate executes %q — there are two gates and nothing else: %s… for the Git proxy, %s… for the egress proxy (SP-K03-3)", target, GitScheme, EgressScheme)
}

// Receipt is what comes back into the job (SP-K03-1). It is contract/platform.proto's Receipt in
// this package's own words — internal/outbox is a step module and does not speak the wire; the
// roles that call it translate.
type Receipt struct {
	Executed   bool      `json:"executed"`
	ExternalID string    `json:"external_id,omitempty"`
	At         time.Time `json:"at"`
}

// Entry is one intent to act, and everything that happened to it. It is written whole on every
// transition — the file is the record, and a reader that finds one has the entire history of that
// effect without a second lookup.
type Entry struct {
	Order       string `json:"order_id"`
	Target      string `json:"target"`
	ContentHash string `json:"content_hash"`
	// PayloadRef is a reference, not content (contract/platform.proto's own word for the field).
	// The outbox holds intents; a patch of any size lives in the working copy the pod left behind.
	PayloadRef string `json:"payload_ref,omitempty"`
	// RequiresRegister marks SP-K03-4's non-idempotent targets: email, payment, foreign ticket
	// systems. It decides one thing and only one — whether a missing acknowledgement may be
	// executed again.
	RequiresRegister bool `json:"requires_register"`

	State     State     `json:"state"`
	Attempt   uint32    `json:"attempt,omitempty"`
	Recorded  time.Time `json:"recorded_at"`
	Changed   time.Time `json:"changed_at"`
	Cause     string    `json:"cause,omitempty"`
	Receipt   *Receipt  `json:"receipt,omitempty"`
	Executing time.Time `json:"executing_since,omitempty"`
}

// Key is the entry's domain key.
func (e Entry) Key() Key { return Key{Order: e.Order, Target: e.Target, ContentHash: e.ContentHash} }

// Terminal reports whether nothing more will happen to this entry without a human.
func (e Entry) Terminal() bool {
	return e.State == Acknowledged || e.State == Denied || e.State == Asking
}
