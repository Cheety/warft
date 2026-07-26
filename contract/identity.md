# The node identity (B-01, E-10)

Normative. This document is contract, alongside `platform.proto`, `schema.sql` and `authority.md`:
it fixes the name a node certificate carries and the verification both ends of every gRPC
connection perform on it. Everything here is enforced by `acceptance/b01-identity.sh`; a claim
below that is not exercised by a probe is a gap, not a guarantee.

The certificate is the `bytes certificate` field of `EnrollResponse` in `platform.proto`
(SP-B01-1). This file says what that certificate names, and how a verifier turns the name into
"work node from cell B" as a statement.

## Why the name, not a table

SP-B01-1 puts role and cell **in the name** of the node certificate. The alternative — a
certificate that merely identifies a key, with role and cell in a table beside it — would make
"work node from cell B" a claim the control plane accepts. In the name it is a statement: whoever
verifies the certificate against the cell's CA has verified role and cell in the same stroke,
because the CA signed them into the certificate when it was issued. There is no second lookup that
could drift, and no table row through which identity could be edited after the fact.

## The name

Every node certificate carries **exactly one** URI Subject Alternative Name:

```
workpod://<cell>/<role>/<node_id>
```

| Part | Meaning | Form |
|---|---|---|
| `cell` | the cell from SP-A04-1 instance data | `[a-z0-9-]+`, e.g. `eu-c1` |
| `role` | one of the four from SP-A02-1 | `all` · `control` · `knowledge` · `work`, verbatim |
| `node_id` | this node, unique within its cell; the `node_id` of `CapacityRequest` and `Heartbeat` | `[a-z0-9-]+` |

`all` is a role like the other three, not a wildcard: the verifier compares it verbatim. A policy
that admits `control` and means "control, or the single node that carries all four slices" says
both names itself.

A certificate with zero `workpod://` names, with several, or with one that does not match the
grammar above carries **no identity** — ambiguity is refusal, not choice.

## The certificate profile

Both ends of every connection present the same shape of certificate. There is no separate client
and server profile, because every connection is between two nodes and either end may be the one
that dialed.

| Field | Value |
|---|---|
| subject | `CN=<node_id>` — display only; verification never reads the subject |
| subjectAltName | exactly one `URI:workpod://<cell>/<role>/<node_id>` |
| key | ECDSA P-256 |
| keyUsage | `critical, digitalSignature` |
| extendedKeyUsage | `serverAuth, clientAuth` |
| basicConstraints | `critical, CA:FALSE` |
| validity | short; renewal is hourly and overlapping (SP-B01-5), its mechanics are AP-6.1 |

P-256 rather than Ed25519, although the Biscuit issuer signs Ed25519 (`authority.md`): SP-B01-3
wants the node key in the TPM where the hardware allows, and P-256 is the curve TPM 2.0
implementations are required to carry — a key algorithm the TPM cannot hold would quietly turn
that SHOULD into a MAY. The connection is TLS 1.3.

## The trust anchor: a node CA per cell, in the system image

Node certificates of a cell are signed by that cell's **node CA**. Its certificate — public — is
carried in the system image, next to the Biscuit issuer's key (`authority.md`):

```
/usr/lib/workpod/trust_anchor/<cell>-ca.pem   # the cell's node CA certificate, PEM
/usr/lib/workpod/trust_anchor/<cell>.pub      # the cell issuer's Ed25519 key (authority.md)
```

Both ends verify against this anchor and need nothing secret. As with the authority, this document
fixes the mechanism and bakes no production key: there is no production cell yet, no CA keypair is
committed anywhere in this repository, and every CA the probe uses is ephemeral, minted per run
(the discipline of `decisions/signing-key.md`).

## mTLS on every gRPC connection, both sides (SP-E10-1)

The interfaces of SP-E10-1 — control API, worker pull, gates, events — are gRPC over TLS 1.3, and
the TLS is mutual. Scaffolding means: what each end presents and what each end verifies is fixed
here and probed; the listeners themselves are stage 3 work.

The **listening** end requires a client certificate — there is no anonymous connection on any
interface — and runs the verification path below on it. The **dialing** end runs the same path on
the listener's certificate, and additionally holds the certified role against the interface it
dialed: a node that dials its control plane (the `control` value of SP-A04-1) accepts `control` —
or `all`, where a single node carries all four slices — in its own cell, and hangs up on anything
else. Verification binds to the name in the certificate, never to DNS or to the address dialed:
the address is instance data and may move, the identity may not.

The harness socket carries the same Protobuf contract, but it runs inside one node, between worker
and pod, over a Unix socket without network (T-04). Which identity the pod end presents there is
fixed with the runner in AP-3.3, not here.

## The verification path

One path, run by both ends of every connection, in order. Each refusal has a name, so a probe can
tell a refusal for the right reason from a broken fixture:

1. **Chain.** The peer's certificate verifies against the cell's node CA from the system image,
   and the TLS handshake proves possession of the key. Anything else is refused: `untrusted`.
   What a certificate *claims* buys nothing here — an attacker who writes
   `workpod://eu-c1/work/n-1` into a self-signed certificate fails this step, because the
   statement is the CA's signature over the name, not the text of the name.
2. **Name.** Exactly one `workpod://` URI SAN, well-formed per the grammar above. Zero, several,
   or a malformed one — including a role that is not one of the four: `no_identity`.
3. **The statement replaces the claim.** From here on, the connection *is* "`<role>` node from
   cell `<cell>`", and every claim the peer makes beside the channel is held against the certified
   name: the `role` and `cell` fields of `EnrollRequest`, the `node_id` and `cell` of
   `CapacityRequest`, the `node_id` of `Heartbeat`. A mismatch is refused — `cell_mismatch`,
   `role_mismatch`, `node_mismatch` — and the certified name wins. The control plane never looks
   identity up in a table: K-01's tables record what a node did, the certificate is what it is.

## Who, not what (SP-E10-2)

The certificate says *who*, the token says *what is allowed* (`authority.md`). The name admits a
node to a connection; it grants no effect. A work node's certificate lets it pull — every effect
it then attempts stands or falls with the Biscuit authority its lease carries, verified at the
gates. No right is ever read from a certificate, and no identity from a token; the two halves meet
only where a policy names both, as in "a `work` node may call `RequestCapacity`" — role from the
certificate, everything the lease permits from the token. That separation is why a compromised
node has compute time, not rights beyond its leases.

## What is not decided here

Enrollment — how a node presents its single-use token and obtains this certificate — is AP-6.1
(AB-B01-1), as are renewal and rotation (SP-B01-5, AB-B01-5) and TPM binding with attestation on
joining (SP-B01-3). The real listeners boot in stage 3 (AP-3.1). This document and its probe
deliver the format and the verification path — the name as a statement — and build no server.
