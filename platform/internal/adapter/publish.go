package adapter

import (
	"fmt"
	"io"

	workpodv1 "github.com/Cheety/warft/platform/api/workpodv1"
	"github.com/Cheety/warft/platform/internal/outbox"
)

// SeenDir is where an adapter remembers which events it has already put into a channel. On /var,
// for the same reason the outbox is (SP-K03-6, SP-A05-1): a seen-set that died with the process
// would remember nothing at the one moment it is needed — the restart.
const SeenDir = "/var/lib/workpod/events-seen"

// Publisher is SP-K03-5: "replies into channels are events (T-02); the adapter deduplicates via the
// event ID. A restart of the control plane produces no second message."
//
// It is deliberately not an outbox entry. K-03's outbox is for effects a *gate* executes, and
// SP-K03-3 rules there are two gates and nothing else — a third one for channels would be the thing
// that sentence forbids. A reply is an event, and the deduplication is the adapter's, keyed by the
// id the event was given by whoever produced it.
//
// The key is the event id and not the content, because two genuinely different events may say the
// same words: "check failed" twice in a row on two attempts is two messages, and suppressing the
// second because it reads like the first would hide the thing the channel is there to show.
type Publisher struct {
	Respond Responder
	Seen    *outbox.Ledger
	Out     io.Writer
}

// Responder is the one method of the adapter contract that publishing needs: respond(). Narrower
// than Contract on purpose — an adapter's receive() shapes a *platform's* native event, and which
// platform that is has nothing to do with whether a reply has already gone out.
type Responder interface {
	Respond(*workpodv1.Event) (string, error)
}

// NewPublisher opens a publisher over a seen-set directory.
func NewPublisher(r Responder, dir string, out io.Writer) *Publisher {
	if dir == "" {
		dir = SeenDir
	}
	return &Publisher{Respond: r, Seen: outbox.OpenLedger(dir), Out: out}
}

// Publish renders an event into the language of the channel and delivers it — at most once per
// event id, ever.
//
// The bool is "did this call deliver?" — false means the id was already in the seen-set and the
// channel saw nothing. A control plane that restarts and republishes its unacknowledged events
// finds every one of them here and the channel gets no second message (AB-K03-5).
//
// The id is recorded *before* the message is rendered and written, not after. The failure that
// matters is the double message, and a crash between writing and recording would cause exactly one;
// a crash between recording and writing loses a message instead, which is the direction a channel
// can be asked about and a duplicate cannot be taken back.
func (p *Publisher) Publish(e *workpodv1.Event) (bool, error) {
	if e.GetId() == "" {
		return false, fmt.Errorf("an event without an id cannot be deduplicated, and every reply into a channel is deduplicated (SP-K03-5)")
	}
	seen, err := p.Seen.Seen(e.GetId())
	if err != nil {
		return false, err
	}
	if seen {
		return false, nil
	}
	text, err := p.Respond.Respond(e)
	if err != nil {
		return false, err
	}
	if _, err := fmt.Fprintln(p.Out, text); err != nil {
		return false, err
	}
	return true, nil
}
