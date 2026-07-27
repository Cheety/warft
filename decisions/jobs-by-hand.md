# Decision: jobs by hand — what an envelope carries until the captain exists

**Status:** ruled · **Date:** 2026-07-27 · **Affects:** SP-T01-1, SP-T01-7, SP-Q01-1, SP-Q01-6,
SP-K04-5, E-10, E-11 step 3, AB-T01-7, AP-3.2. Due to be narrowed by AP-5.1 and AP-5.5.

E-11's third step builds "one adapter (CLI), one pipeline, one runner — **without a captain**, jobs
by hand". AP-3.2's "done when" is that the same message delivered twice produces **one job**, and
`EnvelopeAck` already answers with `order_id` — so intake at stage 3 does produce a job, and the
question this file answers is where the job's contents come from when neither the intent contract
(Q-01, AP-5.1) nor the captain (T-02, AP-5.5) exists yet.

Two things are needed and neither may be guessed in code (V-05): a place on the wire for what a
human decides by hand, and a value for `order.authority_ref` before any Biscuit has been minted.

## Ruling

### 1. `HandWrittenJob`, an additive addition to `contract/platform.proto`

The envelope gains one field, and the schema one message:

```
message HandWrittenJob {   // E-11 step 3: what a captain would decide, decided by a human instead
  Spec spec = 1;           // goal, acceptance, assumptions, bounds, budget, reversibility (Q-01)
  ResourceClass class = 2; // the captain's `size` step (SP-T02-1)
  string image_hash = 3;   // T-03, until the requirement hash resolves it (AP-4.2)
  string pipeline_version = 4;
  string locality_group = 5;
  Priority priority = 6;
  Budget budget_share = 7;
}

message Envelope {
  …
  HandWrittenJob by_hand = 15;
}
```

Field 15 is new and `HandWrittenJob` is new, so this is additive in the sense SP-E10-3 rules and
`acceptance/e10-additive.py` checks: no field number is reassigned, nothing is removed, no type
changes. `SubmitEnvelope(Envelope) returns (EnvelopeAck)` is untouched — the contract already
expected intake to answer with an order id, and this is what lets it.

**An envelope without `by_hand` is still an envelope.** Intake stores it and creates no job, and the
acknowledgement says so with an empty `order_id`. That is SP-Q01-6 holding — no acceptance
criterion, no job — not a failure. **An envelope whose `by_hand.spec` carries no acceptance is
refused**, for the same requirement read the other way: a job was asked for and the one thing a job
may not lack is missing.

### 2. `order.authority_ref` names the envelope until a token is minted

At intake the authority is the level the channel decided (SP-T01-4) and it is attached to the
envelope, unchanged (SP-T01-9). No Biscuit exists yet: SP-K04-5 mints one with short validity and
renews it **through the lease**, which is AP-6.2's work. So intake writes

```
authority_ref = "envelope:<envelope id>"
```

— the grant's origin, resolvable in one query to the level, the principal, the project and the cell
that decided it. When AP-6.2 grants the first lease it mints the token from exactly those facts and
the reference becomes the token's.

## Rationale

1. **The alternative to a named field is an unnamed one.** Without `HandWrittenJob` the same six
   decisions still have to reach intake in stage 3, and they would reach it as defaults in Go — a
   resource class here, a pipeline version there. That is precisely the guess-in-code V-05 forbids,
   and it would be invisible, whereas a message called `HandWrittenJob` is a thing AP-5.1 can be
   told to delete.
2. **It is one schema, not a side channel.** E-10's whole claim is that adapter, gate and harness
   speak one contract. A stage-3 escape hatch outside the proto — a JSON blob, an environment
   variable, a second endpoint — would be a second schema for the same traffic, and the day the
   captain arrives there would be two things to remove instead of one.
3. **The empty case is the requirement, not a gap.** Making `by_hand` optional is what keeps
   SP-Q01-6 checkable at stage 3: an envelope with no acceptance criterion produces no job, and the
   probe can show it. A mandatory field would have forced every adapter to invent one.
4. **`envelope:<id>` is a reference, which is all the column promises.** `authority_ref` exists
   because SP-K01-5 forbids the secret in the row; it holds a reference, and this is a reference
   that resolves — through `envelope.authority`, `envelope.principal` and `envelope.project` — to
   exactly the facts SP-K04-1 puts in an authority block. Writing a fabricated token id instead
   would be a value that resolves to nothing, which is the failure the column was shaped to avoid.
5. **Nothing here widens an authority.** The level travels from the channel into the envelope into
   the order and is never read from the text (SP-T01-9). `by_hand` carries no level and no rights:
   a human who states a job by hand states its size and its budget, never its authority. Widening
   stays cryptographically impossible (AB-K04-2), because the token is still minted from the
   channel's decision and nothing else.

## Consequences

- `contract/platform.proto` changes, and `platform/api/workpodv1` is regenerated in the same commit.
  The CI leg that holds the proto additive against its predecessor is the check that this ruling was
  followed rather than merely written.
- Intake refuses a `by_hand` whose spec has no acceptance, and `acceptance/t01-intake.sh` probes it.
- AP-5.1 derives the spec and AP-5.5 decides class, priority and locality. When both stand,
  `by_hand` has no sender left: the field is then deprecated in place — never renumbered, never
  reused (SP-E10-3) — and this file is what says why it was there.

## Overturned by

AP-5.1 and AP-5.5 together. Q-01 deriving a spec from an envelope, and a captain sizing and placing
the job, remove every field of `HandWrittenJob`; the day the CLI stops filling it, the field is
deprecated and this ruling has expired. Separately, AP-6.2 overturns half of it on its own: the
first minted lease replaces `envelope:<id>` with the token's own reference, and the paragraph above
stops describing anything.
