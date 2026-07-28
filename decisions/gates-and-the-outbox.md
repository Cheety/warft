# Decision: the outbox, the two gates, and where a credential is allowed to lie

**Status:** ruled · **Date:** 2026-07-28 · **Affects:** SP-K03-1 … SP-K03-6, SP-B01-4, SP-B02-1,
SP-B02-2, SP-B02-3, SP-B02-4, SP-K04-5, AB-K03-1 … AB-K03-5, AB-A06-11, AB-B01-4, AB-B02-2,
AB-B02-4, AP-3.5. Narrowed by AP-6.2 and AP-5.5.

K-03 states the chain — pod → outbox → gate → receipt — and B-02 states the boundaries. Building
them at stage 3 needs three things the panels do not fix, and none of them may be guessed in code
(V-05): what the outbox is *made of*, where the per-job allowlist comes from before a Biscuit
exists, and how SP-B01-4 and SP-B02-2 are both true at once.

## Ruling

### 1. The outbox is a directory of files on `/var`, one file per domain key

`/var/lib/workpod/outbox/<sha256 of order + target + content_hash>.json`. The domain key of
SP-K03-2 is the *name of the file*, and recording an entry is `open(O_CREAT|O_EXCL)`: the second
attempt at the same push does not need a lock, a transaction or a leader, because the filesystem
already refuses to create a name twice. That is the whole of V-02's "a doubly executed job is
harmless" made true at the one place where it is not — the kernel arbitrates, not an agreement
between two workers.

An entry stands in exactly one of five states:

| State | Means |
|---|---|
| `recorded` | the pod stated an intent; nothing has been executed |
| `executing` | the register's first half: written down **before** the gate was called |
| `acknowledged` | the gate answered; the receipt is in the entry |
| `denied` | the gate refused by policy or allowlist; a terminal state with a cause |
| `asking` | the acknowledgement is missing and a human is being asked (SP-K03-4) |

`recorded → executing → acknowledged` is SP-K03-4's "record → execute → acknowledge", applied to
every entry rather than only to non-idempotent ones, because writing the intent down first costs one
`rename(2)` and buys the same evidence for both.

**The one place where retrying is forbidden is a state transition, not a convention.** An entry
found in `executing` with `requires_register` set may not be executed again: the store answers
`ErrAskDoNotRetry`, moves the entry to `asking`, and there is no code path that takes it back to
`executing`. An entry *without* `requires_register` found in `executing` may be executed again —
that is not a retry in the harmful sense, because the domain key makes the second call the same
push, and the gate's ledger recognizes it as one.

`/var` is SP-K03-6 and SP-A05-1: the outbox survives the pod that wrote into it and the restart of
the worker that was draining it. It is deliberately not on `/run` (dies with the boot) and not on
`/data/work` (a reinstall wipes it).

### 2. The gate keeps its own ledger, and it is the same mechanism

`/var/lib/workpod-gate/<gate>/<domain key>.json` on the gate's side. The outbox deduplicates the
*intent*; the ledger deduplicates the *execution*. Both are needed and they are not the same claim:
a worker that crashed between calling the gate and writing the receipt would otherwise call again,
and the outbox — on another machine — cannot know that the push already happened. Two entries in two
places, one domain key, one push.

### 3. Until AP-6.2 mints a Biscuit, the allowlist is derived from the authority level

SP-B02-4 wants an allowlist per job — target, method, size limit — **derived from the authority**
(K-04). AP-2.4 built the attenuation library and its verifier in Python; SP-K04-5 mints the token
*through the lease*, which is AP-6.2's. So at stage 3 there is no token in Go to derive from, and the
authority that exists is the level the channel decided, carried on the order as
`authority_ref = "envelope:<id>"` ([`jobs-by-hand.md`](jobs-by-hand.md)).

The ruling: the egress gate derives the allowlist from **the authority level of the job**, through
the table in `platform/internal/egress/b02-allowlist.tsv` — one row per level, naming the permitted
targets, the permitted methods and the size limit. The table is the source; the program reads it and
does not carry a second copy.

Three properties are ruled with it, and they are what the row is checked against:

- **Default deny.** A job with no allowlist entry reaches nothing. Not "reaches the defaults" — an
  unknown level is a refusal, so a level added to the enum and forgotten in the table fails closed.
- **Per job, not per node.** The allowlist is looked up by `order_id` at every single forward. Two
  jobs on the same node reach different sets of targets, which is the whole of SP-B02-4; a node-wide
  rule would pass a test with one job on the node and be wrong with two.
- **The pod resolves nothing.** The pod sends a *name*; the gate matches the name against the
  allowlist and resolves it (SP-B02-3). A pod that sends an address instead of a name is refused,
  because an address is what a resolver produces and the pod has none.

When AP-6.2 grants the first lease, the allowlist is derived from the token's `targets` block
instead and the table describes nothing. The lookup keyed by `order_id` does not change; only what
fills it does.

### 4. SP-B01-4 and SP-B02-2 are both true: a gate is a process, not a machine

SP-B02-2 puts the egress proxy **on the work node**. SP-B01-4 says credentials lie exclusively in the
gates and **never on work nodes**. Read as statements about machines the two contradict each other,
and one of them would have to be broken.

They are not statements about machines. SP-B01-4's operative word is *exclusively*: a credential is
in a gate or it is nowhere. The egress gate is a gate that happens to run on the work node, in its
own unit, under its own user, with its credentials loaded by systemd into a directory the worker
cannot read. What SP-B01-4 forbids is a credential reachable from the work *layer* — the pod, the
worker, the working copy, the image — and that stays forbidden and is what AB-B01-4 probes: the pod
has no network at all (T-04), the worker's environment carries nothing, and nothing under
`/data/work`, `/var/lib/workpod` or a pod's rootfs holds a key.

Concretely:

| | Git gate | Egress gate |
|---|---|---|
| Role | `control` | `work` — SP-B02-2, one per work node |
| Unit | `workpod-git-gate.service` | `workpod-egress-gate.service` |
| Listens on | a Unix socket, `/run/workpod/git-gate.sock` | a Unix socket, `/run/workpod/egress-gate.sock` |
| Credentials | `${CREDENTIALS_DIRECTORY}`, `0700`, its own user | the same, its own user |

Neither gate opens a port. SP-B02-6 says no open port means no open port, and a Unix socket is how a
gate is reachable without one; it is also what makes AB-B02-2's "no central throughput bottleneck"
checkable, since a socket cannot be reached from another machine even by accident.

### 5. A reply into a channel is an event, and never an outbox entry

SP-K03-5 rules replies into channels as events (T-02), deduplicated by the adapter via the event ID.
So the outbox refuses a `channel:` target by name: an effect that went through the outbox would be
executed by a gate, and there is no third gate (SP-K03-3). The adapter's outbound side keeps the
seen event IDs in the same store shape as the outbox, on `/var`, so a control plane that restarts and
republishes produces no second message (AB-K03-5). Publishing itself is still AP-5.5's; what stands
here is the adapter half SP-K03-5 names.

## Rationale

1. **The domain key has to be enforced by something that is not a component of this system.**
   Every alternative — a lock, an election, a unique index in Postgres — makes the no-double-push
   claim depend on a part of the platform being up. `O_EXCL` on a local filesystem holds while the
   control plane is down, while the network is gone, and across a worker restart, which is exactly
   the set of situations in which a double push actually happens.
2. **Recording before executing is worth a `rename` even where it is not required.** SP-K03-4 asks
   for the register on non-idempotent targets. Doing it for all of them means the evidence for
   "what did this job try to do" does not depend on the target's class, and the probe for AB-K03-1
   ("the pod produces intent, the gate executes") can read one file rather than reason about two
   paths.
3. **Deriving the allowlist from a table rather than from code is what makes the row checkable.**
   AB-B02-4 is an `S` check: target, method and size limit derived from the authority. A table has
   rows a script can hold against the levels in `contract/schema.sql`; a `switch` in Go has
   branches that a script can only re-implement.
4. **Failing closed on an unknown level is the cheap half of B-02 and the one that is usually
   missing.** SP-B02-7 has the same shape for nftables — default deny, from the image. The gate
   agreeing with the firewall about the direction of the default is worth more than either of them
   being clever.
5. **The contradiction in §4 is real and had to be ruled rather than papered over.** Choosing the
   machine reading of SP-B01-4 would put the egress proxy centrally and break SP-B02-2 and its
   acceptance row; choosing it the other way round would put a Git key on every work node. The
   process reading breaks neither, and it is the one the panel's own boundary table implies — it
   lists `gates` as a boundary of its own, beside `node`, not as a kind of node.

## Consequences

- Three modules join `platform/`: `internal/outbox` (the store, the key, the register),
  `internal/gitgate` and `internal/egress`. All three are rows in
  [`module-dependencies.md`](module-dependencies.md) in the same commit, as that file requires.
- `workpod git-gate` and `workpod egress-gate` stop refusing. `components` reports them serving
  since AP-3.5, and the two `refuse()` calls naming this work package are gone — which is what
  AB-E02-1 re-reads on the next run.
- The pod's `EnqueueEffect` stops refusing and records. A pod that enqueues an effect and a pod that
  runs twice are now different things in the store, and that difference is AB-A06-11.
- The image gains two units and the `work` role gains one of them. `workpod-control.target`'s
  comment claiming both gates is corrected: SP-B02-2 puts one of them on the work node.
- `acceptance/k03-outbox.sh` is the run the nine rows turn green through, and it is a trigger path
  of the platform workflow for the reason every acceptance script is one.

## Overturned by

AP-6.2, in part: the first minted lease replaces the level-derived allowlist with the token's own
`targets` block, and §3's table stops describing anything. AP-5.5 overturns §5's second half — when
the captain publishes events, the adapter's seen-set becomes one end of a path that has two.
Wholly: a second artifact. If E-02 falls and the gates become their own programs, §4's argument that
a gate is a process rather than a machine is no longer needed, because the process is the program.
