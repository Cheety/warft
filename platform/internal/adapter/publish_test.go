package adapter

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	workpodv1 "github.com/Cheety/warft/platform/api/workpodv1"
)

func event(id string, kind workpodv1.Event_Kind) *workpodv1.Event {
	return &workpodv1.Event{Id: id, OrderId: "o1", Kind: kind, Detail: "detail for " + id}
}

// AB-K03-5, SP-K03-5: the adapter deduplicates via the event ID. A restart of the control plane
// produces no second message — and a restart is what a second Publisher over the same directory is.
func TestARestartProducesNoSecondMessage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "seen")
	var out bytes.Buffer

	p := NewPublisher(&CLI{}, dir, &out)
	delivered, err := p.Publish(event("ev-1", workpodv1.Event_DONE))
	if err != nil || !delivered {
		t.Fatalf("the first publish delivered=%v (%v)", delivered, err)
	}
	first := out.String()
	if first == "" {
		t.Fatal("nothing reached the channel")
	}

	// The control plane restarts and republishes everything it did not see acknowledged.
	restarted := NewPublisher(&CLI{}, dir, &out)
	delivered, err = restarted.Publish(event("ev-1", workpodv1.Event_DONE))
	if err != nil {
		t.Fatal(err)
	}
	if delivered {
		t.Fatal("the republished event reached the channel a second time (SP-K03-5)")
	}
	if out.String() != first {
		t.Fatalf("the channel got more than one message:\n%s", out.String())
	}
}

// The key is the event id and not the content: two genuinely different events may say the same
// words, and suppressing the second would hide what the channel exists to show.
func TestTwoEventsWithTheSameWordsAreTwoMessages(t *testing.T) {
	var out bytes.Buffer
	p := NewPublisher(&CLI{}, filepath.Join(t.TempDir(), "seen"), &out)
	// Same kind, same order, same rendering — two ids, and therefore two messages. The CLI's
	// respond() does not render `detail` at all, which is exactly why this is the case worth
	// checking: deduplicating on what reached the channel would collapse these two into one.
	for _, id := range []string{"ev-1", "ev-2"} {
		e := event(id, workpodv1.Event_CHECK_FAILED)
		e.Detail = "the same words both times"
		delivered, err := p.Publish(e)
		if err != nil || !delivered {
			t.Fatalf("%s: delivered=%v (%v)", id, delivered, err)
		}
	}
	if lines := strings.Count(strings.TrimSpace(out.String()), "\n") + 1; lines != 2 {
		t.Fatalf("two different events produced %d message(s):\n%s", lines, out.String())
	}
}

// An event with no id cannot be deduplicated, and every reply into a channel is deduplicated. So it
// is refused rather than delivered at whatever risk.
func TestAnEventWithoutAnIdIsRefused(t *testing.T) {
	var out bytes.Buffer
	p := NewPublisher(&CLI{}, filepath.Join(t.TempDir(), "seen"), &out)
	if _, err := p.Publish(event("", workpodv1.Event_DONE)); err == nil {
		t.Fatal("an event with no id was published")
	}
	if out.Len() != 0 {
		t.Fatalf("it reached the channel anyway: %s", out.String())
	}
}
