// The CLI adapter — the first thing that satisfies the contract in adapter.go, and the one channel
// T-01 puts at level `confidential`: "CLI with a device certificate" (SP-T01-4).
//
// The authority comes from the channel, never from the text (SP-T01-9). Here that means: a device
// certificate is present, so the level is `confidential`; no certificate, so there is no channel
// and no envelope. Nothing a caller writes in --text can move that line, because the level is not
// read from anything the caller wrote.

package adapter

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	workpodv1 "github.com/Cheety/warft/platform/api/workpodv1"
	"github.com/Cheety/warft/platform/internal/attachment"
	"github.com/Cheety/warft/platform/internal/ids"
)

// Channel is the name this adapter files its envelopes under. It is half of the state contract's
// UNIQUE (channel, idempotency): keys are unique within a channel, never across all of them.
const Channel = "cli"

// DefaultDeviceCert is where the device certificate lies on a node. It is not a boot value: the
// five of SP-A04-1 are the node's, and this one is the operator's.
const DefaultDeviceCert = "/etc/workpod/device.crt"

// Invocation is the CLI's platform event: one run of `workpod adapter submit`. Every other adapter
// has a different one, which is why Contract is generic over it.
type Invocation struct {
	Cell        string
	Project     string
	MessageID   string
	Text        string
	Thread      string
	Attachments []attachment.Candidate
	Platform    string
	ByHand      *workpodv1.HandWrittenJob
	ReceivedAt  time.Time
}

// CLI satisfies Contract[Invocation].
type CLI struct {
	external string
	store    *attachment.Store
}

var _ Contract[Invocation] = (*CLI)(nil)

// NewCLI reads the device certificate and takes its identity from it. A missing or unreadable
// certificate is a refusal, not a downgrade to a lower level: SP-T01-4 grants `confidential` to
// "CLI with a device certificate", and a CLI without one is not that channel.
//
// The certificate is read for its identity, not verified against the cell's trust anchor — mTLS
// and enrollment are AP-6.1's (B-01). Until a connection can state its role and cell instead of
// claiming them, this channel reaches the control plane over loopback only, the same bound the
// plane's own listener holds itself to.
func NewCLI(certPath, storeRoot string) (*CLI, error) {
	if certPath == "" {
		certPath = DefaultDeviceCert
	}
	raw, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("no device certificate at %s: the CLI is level confidential *because* of it (SP-T01-4): %w", certPath, err)
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%s is not a PEM certificate", certPath)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", certPath, err)
	}
	// The public key, not the subject: a subject is what a certificate says about itself, and the
	// same device re-certified keeps its key. SHA-256 over the SubjectPublicKeyInfo is the
	// fingerprint every other tool computes the same way.
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return &CLI{
		external: Channel + ":" + hex.EncodeToString(sum[:]),
		store:    attachment.NewStore(storeRoot, attachment.Ruled()),
	}, nil
}

// Identity delivers only the external identifier — "cli:<key fingerprint>". Which principal stands
// behind it is `identity_link`'s answer and intake's to look up; an adapter never attributes
// (SP-T01-5).
func (c *CLI) Identity() (string, error) {
	if c.external == "" {
		return "", fmt.Errorf("no device certificate was read")
	}
	return c.external, nil
}

// Capabilities is what this channel declares. A terminal carries a thread reference and files, has
// no buttons, and imposes no character limit of its own.
func (c *CLI) Capabilities() Capabilities {
	return Capabilities{
		Threads:        true,
		Attachments:    true,
		Buttons:        false,
		CharacterLimit: 0,
	}
}

// Receive shapes one invocation into an envelope: the channel's fields, the attachments checked
// and filed at intake (SP-K01-6, OP-5), and the authority the channel — not the text — decided.
func (c *CLI) Receive(in Invocation) (*workpodv1.Envelope, error) {
	if in.Cell == "" {
		return nil, fmt.Errorf("an envelope carries its cell (SP-K01-3); --cell is missing")
	}
	if in.Project == "" {
		return nil, fmt.Errorf("an envelope carries its project (SP-K01-4); --project is missing")
	}
	// SP-T01-7, and the reason this work package exists: chat platforms redeliver, and without a
	// key a retry starts three pods. On a channel whose message identity is stable across
	// redeliveries the message id *is* the key; an adapter for a channel that redelivers under new
	// ids derives one instead — that derivation is `receive()`'s work, per adapter.
	if in.MessageID == "" {
		return nil, fmt.Errorf("an envelope needs an idempotency key (SP-T01-7); --message-id is missing")
	}
	if in.Text == "" && len(in.Attachments) == 0 {
		return nil, fmt.Errorf("an envelope with neither text nor attachments carries nothing")
	}

	caps := c.Capabilities()
	if in.Thread != "" && !caps.Threads {
		return nil, fmt.Errorf("this channel declares no threads")
	}
	if len(in.Attachments) > 0 && !caps.Attachments {
		return nil, fmt.Errorf("this channel declares no attachments")
	}
	if caps.CharacterLimit > 0 && len(in.Text) > caps.CharacterLimit {
		return nil, fmt.Errorf("%d characters exceed this channel's limit of %d", len(in.Text), caps.CharacterLimit)
	}

	sender, err := c.Identity()
	if err != nil {
		return nil, err
	}

	accepted, err := c.store.AcceptAll(in.Attachments)
	if err != nil {
		return nil, err
	}
	refs := make([]*workpodv1.Attachment, 0, len(accepted))
	for _, a := range accepted {
		refs = append(refs, &workpodv1.Attachment{
			ContentHash: a.ContentHash,
			MediaType:   a.MediaType,
			SizeBytes:   uint64(a.SizeBytes),
		})
	}

	at := in.ReceivedAt
	if at.IsZero() {
		at = time.Now()
	}
	platform := in.Platform
	if platform == "" {
		platform = "alpine" // the runner pool the state contract already defaults to (SP-T04-4)
	}

	return &workpodv1.Envelope{
		Id:               ids.New(),
		Cell:             in.Cell,
		Project:          in.Project,
		Channel:          Channel,
		ChannelMessageId: in.MessageID,
		SenderExternal:   sender,
		Authority:        workpodv1.AuthorityLevel_CONFIDENTIAL,
		Text:             in.Text,
		Attachments:      refs,
		Thread:           in.Thread,
		ReceivedAt:       timestamppb.New(at),
		Idempotency:      in.MessageID,
		Platform:         platform,
		ByHand:           in.ByHand,
	}, nil
}

// Respond renders an event into the language of the channel. A terminal's language is one line;
// `verbosity` is this adapter's profile (SP-T02-9), and it decides how much of the detail comes
// with it rather than whether the event is reported at all.
func (c *CLI) Respond(ev *workpodv1.Event) (string, error) {
	if ev == nil {
		return "", fmt.Errorf("no event to render")
	}
	var b strings.Builder
	switch ev.GetKind() {
	case workpodv1.Event_ACCEPTED:
		b.WriteString("accepted")
	case workpodv1.Event_STARTED:
		b.WriteString("started")
	case workpodv1.Event_CHECK_FAILED:
		b.WriteString("check failed")
	case workpodv1.Event_CLARIFICATION:
		b.WriteString("clarification needed")
	case workpodv1.Event_DONE:
		b.WriteString("done")
	default:
		return "", fmt.Errorf("event %q has no kind — an event without one says nothing", ev.GetId())
	}
	if ev.GetOrderId() != "" {
		b.WriteString("  order " + ev.GetOrderId())
	}
	if ev.GetVerbosity() > 0 && ev.GetDetail() != "" {
		b.WriteString("\n  " + ev.GetDetail())
	}
	return b.String(), nil
}
