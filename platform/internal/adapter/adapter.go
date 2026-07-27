// Package adapter is T-01: "there is no list of supported devices, but an adapter contract with
// four methods" (SP-T01-1). The contract is here; the CLI is the first thing that satisfies it.
//
// The four methods are the whole of the interface between a platform and this system. Everything
// past intake speaks envelopes, and from the captain onward nobody knows where a job came from
// (SP-T01-2) — so a second adapter is attached by writing four methods, never by changing the core
// (AB-T01-1, AP-5.7).
package adapter

import (
	workpodv1 "github.com/Cheety/warft/platform/api/workpodv1"
)

// Capabilities is what a channel declares about itself (SP-T01-1). It is asked before an envelope
// is shaped, not after: a channel that cannot carry attachments refuses them at intake rather than
// dropping them silently somewhere downstream.
type Capabilities struct {
	Threads        bool
	Attachments    bool
	Buttons        bool
	CharacterLimit int // 0 = unbounded
}

// Contract is SP-T01-1's four methods.
//
// Native is the platform's own event — a CLI invocation here, a Discord message or a webhook body
// later. The core never names it, which is the point: `receive()` is the only place where a
// platform's shape is known, and past it there are only envelopes.
type Contract[Native any] interface {
	// Receive shapes a platform event into a uniform envelope: channel, message ID, sender, text,
	// attachments, thread reference, time — and the idempotency key without which a redelivery
	// starts a second job (SP-T01-7).
	Receive(Native) (*workpodv1.Envelope, error)

	// Identity delivers only the external identifier, for example "discord:184…". Only that: an
	// adapter never resolves a principal, because attribution is never automatic (SP-T01-5).
	Identity() (string, error)

	// Respond renders a platform event into the language of the channel.
	Respond(*workpodv1.Event) (string, error)

	// Capabilities declares threads, attachments, buttons, character limit.
	Capabilities() Capabilities
}
