# The authority token (K-04, E-10)

Normative. This document is contract, alongside `platform.proto` and `schema.sql`: it fixes the
format of the authority the platform carries and the rules a gate applies to it. Everything here is
enforced by `acceptance/k04-authority.py`, run by `acceptance/k04-authority.sh`; a claim below that
is not exercised by a probe is a gap, not a guarantee.

The authority is the `bytes authority` field already present in `Order` and `Lease` in
`platform.proto` (SP-K04-1, SP-K04-5). This file says what those bytes are.

## Why a token, not a field

The certificate says *who*, the token says *what is allowed* (SP-E10-2). A compromised node has
compute time, not rights beyond its leases. As an attached field, "no model raises the authority"
would be an agreement between components (SP-K04-8); as a **Biscuit token** it is a property of
cryptography (SP-K04-2). JWT is excluded — it has no offline attenuation. Macaroons are excluded —
their verification needs a shared standing secret in every gate, which B-01 forbids.

## The token — a Biscuit

A Biscuit is a chain of blocks. Each block is signed with a key the previous block commits to (a key
ratchet), so the chain can only be extended, never edited, without breaking the signature. The
**authority block** is signed by the cell issuer; every later block is an **attenuation**.

The authority block carries the content of SP-K04-1 as Datalog facts:

| Fact | SP-K04-1 field | Meaning |
|---|---|---|
| `issuer($id)`, `cell($c)` | `issuer`, `cell` | who vouches, and where it holds |
| `principal($p)`, `project($proj)` | `principal`, `project` | for whom, in what radius |
| `level($l)` | `level` | `public` · `linked` · `confidential` (T-01) |
| `right($resource, $operation)` | `targets` | one fact per granted effect — repositories, egress targets, environments, the project itself |
| `budget($kind, $amount)` | `budget` | pod minutes, tokens, money (V-04) |
| `issued($t)` | — | the watermark revocation reads (see below) |

and one check that fixes the expiry — minutes to hours, never days (SP-K04-1, SP-K04-5):

```
check if time($t), $t < <expires>
```

Time is infrastructure (SP-K04-7): the token carries only the deadline, the gate supplies the current
time from a clock the system image and the selftest keep correct (A-06). A node with a wrong clock is
kept out of the fleet by AB-K04-7, not by this token.

`targets` become one `right($resource, $operation)` fact each, rather than a single opaque field, so
that a gate's authorization reads exactly the effect it guards. Resources are namespaced by kind:
`repo:<name>`, `host:<name>`, `project:<name>`.

## Attenuation only, never widening (SP-K04-2)

Every hop may append a block that **adds** conditions; none may remove one. A captain that produces
subjobs attenuates, and that is the only way new authorities come into being. Widening is impossible
by two independent mechanisms, both proved by AB-K04-2:

1. **Signature.** A rewritten authority block, a self-built token, or tampered bytes fail
   verification against the trust anchor. Whatever such a token *claims* — a wider `level`, a repo it
   was never granted, a larger `budget` — it is refused because the signature, not the claim, is
   checked.
2. **Block scoping.** The gates' authorization policy trusts facts from the **authority block and the
   authorizer only**. A `right(...)` fact injected by a later attenuation block is validly signed and
   present in the token, yet the policy never reads it. So a hop cannot grant itself a right it was
   not handed, even without forging anything. Checks, by contrast, accumulate across every block and
   none can be removed — a block that re-permits an operation an earlier block restricted changes
   nothing.

There is no `widen` in the library, and there is no path — code or model — on which a model output
changes `level`, `targets` or `budget` (SP-K04-8). Authority is granted by code.

## Verification at the effect, at three gates (SP-K04-3)

Verification happens at the effect, not at hand-off. The **control API**, the **Git proxy** and the
**egress proxy** each verify **fully**. A gate that trusts the origin is not a gate: each checks the
signature against the trust anchor, checks revocation, and runs its own policy, trusting neither the
token's claimed `issuer` nor any prior gate's verdict.

There is **one verifier**, called three times; the gates differ only in the request they present:

| Gate | Operation | Resource |
|---|---|---|
| control API | `control.admit` | `project:<project>` |
| Git proxy | `git.read` · `git.write` | `repo:<name>` |
| egress proxy | `egress` | `host:<name>` |

The verifier, in order: (1) parse and check the signature against the trust anchor; (2) check
revocation and static stability against the project's list; (3) run the gate's Datalog policy, which
allows the effect only if the authority block grants exactly that `right($resource, $operation)` and
the token's `project` matches the request's. Each gate needs the public key and nothing secret
(SP-K04-4).

## The trust anchor in the system image (SP-K04-4, A-04)

The public verification key is the `trust_anchor` of SP-A04-1 — carried **in the system image**, not
passed at boot. Its documented location is:

```
/usr/lib/workpod/trust_anchor/<cell>.pub    # the cell issuer's Ed25519 public key, raw bytes
```

A gate loads the key for its cell from this path and verifies against it. Because the key is public,
committing its location and format leaks nothing, and the gates hold no secret.

This document fixes the **mechanism**; it bakes **no production key**. There is no production cell
yet, so there is no issuer keypair to place in the image, and none is committed anywhere in this
repository — every keypair the probes use is ephemeral, minted per run (the discipline of
`decisions/signing-key.md`: a private key is never on a build machine). When a cell is provisioned,
its issuer's public half is written to the path above; that is enrollment work (AP-6.x), and it does
not change this contract.

## Revocation per project, with static stability (SP-K04-6)

Every block has a **revocation id** (a Biscuit primitive). Revoking the authority block's id revokes
the token and everything attenuated from it; revoking an attenuation block's id revokes that subtree.

Revocation is **per project**, distributed on the same line as the egress allowlist and with the same
static stability (SP-V02-3, V-02). The distributed artifact is a **revocation list per project**
carrying:

- `project` — the radius it governs. A gate loads the list for the token's project; a list for
  another project has no bearing, and a token checked against the wrong project's list is refused
  fail-closed.
- `sequence` — a monotonic counter from the control plane.
- `issued_through` — the watermark: authorities issued at or before it are covered by this list.
- `revoked` — the revocation ids that are no longer valid.

The gate never calls the control plane to verify (SP-K04-4); it verifies against the last list it was
handed. Static stability, when the control plane is down (SP-V02-3):

- **The last known list holds.** A revoked block stays refused with no fresh fetch; a token covered by
  the last list (issued at or before its watermark) still verifies. Static stability is not "refuse
  everything".
- **New authorities are refused.** An authority issued past the watermark cannot be checked against a
  current-enough list, and a gate that cannot know whether an authority was revoked must not admit it.
  Once the control plane is reachable again, a fresher list advances the watermark and the same
  authority verifies.

Serving the list is later work (AP-6.x); the format and the verifier's use of it are contract, fixed
here.

## What is not decided here

Granting flows, enrollment, the real gate processes and the serving of the revocation list are stage
3 and later (AP-3.x, AP-6.x). Short validity renewed through the lease is AB-K04-5 (AP-6.1); the clock
in the selftest is AB-K04-7 (AP-1.2). This document and its probes deliver the cryptographic contract
and prove its properties; they build no server.
