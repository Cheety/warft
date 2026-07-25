# Workpod platform — build specification

**Revision 1 of the specification, derived from architecture overview revision 2 (July 2026).**
This specification is the transformation of the architecture document into a build order. It invents
nothing on top; where the architecture document leaves a quantity open, it stands in §19 as an open
point with a decision deadline — not as a silent assumption in code.

---

## §0 Scope, language, conventions

### 0.1 Language rule

Everything in this repository is written in **English**: names — fields, states, events, levels,
classes, steps, roles, capabilities — and the prose that explains them. Names are taken over into code
and configuration unchanged.

> **Deviation from the architecture document, deliberate and recorded.** The document's own rule was
> "names in English, explained in German", with the occupancy table (R-D) kept entirely in German. That
> rule has been replaced by English throughout; see `CLAUDE.md` for the bound glossary. The
> substantive part of the old exception survives in SP-RD-1: the occupancy table is an instrument, not
> a contract. Requirement identifiers (`SP-*`, `AB-*`, `AP-*`, `OP-*`, `E-*`, panel letters) are never
> translated.

### 0.2 Normative force

| Word | Meaning |
|---|---|
| MUST | a violation is an acceptance defect. The corresponding row of the acceptance matrix turns red. |
| SHOULD | a deviation is permitted, but must stand as a decision in Git (V-05: decisions belong in Git). |
| MAY | freedom of the builder. |

### 0.3 Requirement identifiers

Every requirement carries the identifier `SP-<panel>-<n>`. Every requirement has exactly one row in
`03-acceptance-matrix.md` with a check `AB-<panel>-<n>`. **A requirement without a check is not a
requirement** — that is Q-02, applied to this document itself.

### 0.4 The three states that carry everything

| State | Memory | Meaning |
|---|---|---|
| `allocated` | ~40 MB | lies on disk only. CoW snapshot, costs the actual difference. |
| `frozen` | ~24 MB | processes alive, the cgroup freezer holds, pages compressed in zram. |
| `active` | 0.3–2 GB | builds, tests, calls tools. Only here does real cost arise. |

The scheduler (R-B) and the occupancy table (R-D) make exactly this distinction. Every capacity
statement in the system MUST be traceable back to these three states.

### 0.5 What revision 1 builds and what it does not

**Built:** T-01 through T-05, Q-01 through Q-04, F-01 through F-07, R-A through R-D, K-01 through
K-04, V-01 through V-05, B-01 through B-04, A-01 through A-06, E-01 through E-11, G-01 and G-07.

**As capability content, not as platform core (§15):** G-02 (reduction cascade), G-03 (caches, except
the two layers T-04 needs), G-04 (waves), G-05 (activity states), G-06 (toolbox). They are campaigns
and procedures in the catalog (F-03), not a platform layer of their own. Whoever builds them into the
core has blurred the cut from F-01.

**Not built:** a second cell (V-03 stage 4) before the measured limit from E-04; Raft/consensus
(V-02: "consensus only when you can prove you need it"); Windows and macOS runners (T-04) — but the
`platform` contract MUST stand in the envelope from the start, so that they are not a rebuild later.

---

## §1 Object model (K-01)

### 1.1 The three core objects

**SP-K01-1 (MUST)** — There are exactly three objects with three lifetimes and one provenance chain:

| Object | Fields | Lives |
|---|---|---|
| `envelope` | `id`, `cell`, `channel`, `channel_message_id`, `sender_external`, `principal?`, `authority`, `text`, `attachments[]`, `thread`, `received_at`, `idempotency` | minutes |
| `spec` | `id`, `cell`, `version`, `project`, `goal`, `acceptance[]`, `assumptions[]`, `bounds` (repositories, paths, environments), `budget`, `risk_class` | months |
| `order` | `id`, `cell`, `project`, `spec@version`, `parent`, `class` (R-A), `platform`, `image_hash`, `pipeline@version`, `authority`, `budget_share`, `locality_group`, `state`, `attempt`, `idempotency` | minutes to hours |

**SP-K01-2 (MUST)** — Identifiers are assigned by the producer, not by the database: sortable UUID v7.
A central counter is forbidden; it makes horizontal scaling impossible later.

**SP-K01-3 (MUST)** — Every object carries its cell (`cell`). Without a cell identifier, migration
(V-03) cannot be carried out and the tenant boundary is not checkable. This field is maintained from
the first commit, even while there is only one cell — it is the only reason the jump to stage 4 is
small.

**SP-K01-4 (MUST)** — Every object carries its project (`project`). A precondition for deletion, cost
attribution and incident radius. An object without a project reference cannot be attributed or removed
later.

**SP-K01-5 (MUST)** — No object contains secrets, only references to them. No key in the event log, in
the backup, in the support ticket.

**SP-K01-6 (MUST)** — Attachments are data like text: type and size check at intake, stored
content-addressed, mounted read-only into exactly the pod that needs them, never executable.

**SP-K01-7 (MUST)** — The provenance chain is mechanically resolvable: `order → spec@version →
envelope → channel_message_id`. Backward resolution MUST be a query, not a reading of logs.

### 1.2 Derived objects (implicit in the document, named here)

**SP-K01-8 (MUST)** — In addition, the following MUST be maintained as objects of their own with `cell`
and `project`: `principal`, `identity_link` (external → principal), `project`, `attempt`, `lease`,
`outbox_entry`, `receipt`, `judgment`, `budget_pot`, `skill@version`, `image@hash`, `pipeline@version`,
`node`, `locality_group`, `audit_entry`, `halt`.

**SP-K01-9 (MUST)** — `decision` (G-01) is **not** a database object; it lies in Git (V-05). The
database holds only a reference (repository, path, commit).

---

## §2 State machine (K-02)

### 2.1 States

`new` → `admitted` → `queued` → `leased` → `running` → { `frozen` | `awaiting_reply` } → `running` →
{ `delivered` | `unproven` | `failed` } · `cancelled` from any state.

| State | Meaning |
|---|---|
| `new` | envelope accepted |
| `admitted` | budget reserved (V-04) |
| `queued` | in the queue (R-B) |
| `leased` | lease granted |
| `running` | the pod is working |
| `frozen` | preempted, state present |
| `awaiting_reply` | clarification outstanding |
| `delivered` | evidence produced (Q-02) |
| `unproven` | result without evidence |
| `failed` | with a cause key |
| `cancelled` | budget, halt, revocation |

### 2.2 Write authority

**SP-K02-1 (MUST)** — One field, one writer:

| Transition | Written by | Trigger |
|---|---|---|
| `queued → leased` | control plane | the worker requests capacity (V-02) |
| `leased → queued` | control plane | deadline expired, no heartbeat |
| `running → frozen` | worker | preemption at a phase boundary (R-B) |
| `running → awaiting_reply` | worker | clarification per Q-01; the lease pauses, the budget does not run |
| `running → delivered / unproven` | worker | report with an evidence class (Q-02) |
| any `→ cancelled` | control plane | budget exhausted, authority revoked, halt (B-04) |

The database MUST enforce this (a trigger or a state column per writer), not the application. Two
writers on the same field are the beginning of every double execution — and only under load.

**SP-K02-2 (MUST)** — The **attempt** (`attempt`) is the unit of retry, not the job: same `order.id`,
same `idempotency`, new `attempt`, its own result.

**SP-K02-3 (MUST)** — No terminal state without a cause key. `failed` without `cause` is a defect in
the log, not a result, and makes the evaluation by kind of failure (Q-03) impossible. The cause key
MUST come from the set in Q-03 and F-04 (`fact.invented`, `context.missing`, `spec.wrong`,
`tool.failure`, `injection`, `regression.silent`, `assumption.replicated`, `skill.missing`,
`knowledge.missing`, `goal.wrong`, `budget.exhausted`, `unsolvable`).

**SP-K02-4 (MUST)** — Clarifications have a deadline. Without an answer after `n` hours (§19, open
point OP-3): the job goes back to the captain with the proposal to continue under an assumption; the
assumption is logged (Q-01), not suppressed.

**SP-K02-5 (MUST)** — States are not reachable backwards. There is no way back out of a terminal
state; a fresh attempt is a new job with a reference to the old one.

**SP-K02-6 (MUST)** — `unproven` is an exit of its own, neither `failed` nor `delivered` (Q-02). The
job delivers a diff, a gap report and an assessment, and moves into the human stack.

**SP-K02-7 (MUST)** — Poisoned job: after two crashes, quarantine instead of a third attempt (B-04).

---

## §3 Authority (K-04, E-10)

**SP-K04-1 (MUST)** — The authority is a **Biscuit token**, not a field. Content:

| In the token | Meaning |
|---|---|
| `issuer`, `cell` | who vouches, and where it holds |
| `principal`, `project` | for whom, in what radius |
| `level` | `confidential` · `linked` · `public` (T-01) |
| `targets` | repositories, branches, egress targets, environments |
| `budget` | pod minutes, tokens, money (V-04) |
| `expires` | minutes to hours, never days |

**SP-K04-2 (MUST)** — Attenuation only, never widening. Every hop may add conditions, none may remove
them. A captain that produces subjobs attenuates; that is the only way new authorities come into
being. This property comes from cryptography (Biscuit blocks), not from discipline — which is why JWT
is excluded (no offline attenuation) and Macaroons are excluded (verification would need a shared
standing secret, which B-01 forbids).

**SP-K04-3 (MUST)** — Verification happens at the effect, not at hand-off: the Git proxy, the egress
proxy and the control API each verify fully. A gate that trusts the origin is not a gate.

**SP-K04-4 (MUST)** — The public verification key lies in the system image (`trust_anchor`, A-04). The
gates need no secret.

**SP-K04-5 (MUST)** — Short validity, renewed through the lease. A worker that drops out of the fleet
loses its rights by itself.

**SP-K04-6 (MUST)** — Revocation per project via a revocation ID per block, distributed on the same
line as the egress allowlist, with the same static stability (V-02): the last known revocation list
stays valid, new authorities are refused.

**SP-K04-7 (MUST)** — Time is infrastructure: time synchronization belongs in the system image and in
the selftest (A-06), not in an operations manual. A node with a wrong clock grants or refuses rights
arbitrarily.

**SP-K04-8 (MUST)** — Authority is granted by code, never by a model (T-02). There is no path on which
a model output widens `level`, `targets` or `budget`.

### 3.1 Interfaces (E-10)

**SP-E10-1 (MUST)** — All interfaces — control API, worker pull, gates, events, harness socket — are
**Protobuf over gRPC**, mTLS with the node certificates from B-01. Exactly one interface schema in the
system, from the adapter through to the gate.

**SP-E10-2 (MUST)** — The certificate says *who*, the token says *what is allowed*. A compromised node
has compute time, not rights beyond its leases.

**SP-E10-3 (MUST)** — Additive fields only. Field numbers die, they are never reassigned. That is the
migration rule from V-05, enforced by the schema.

**SP-E10-4 (MUST)** — The wire is Protobuf, the state stays relational: in Postgres there are columns,
not serialized blobs.

**SP-E10-5 (MUST)** — Exports (V-05) and the audit trail (B-03) render from the same Protobuf
definitions. There is no second, human-maintained format that could drift.

---

## §4 Outward effect (K-03)

**SP-K03-1 (MUST)** — Chain: the `pod` produces an intent to act → `outbox` → `gate` → `receipt` back
into the job. The pod never acts itself.

**SP-K03-2 (MUST)** — The key of the outbox is a domain key: `order + target + content_hash`. The same
patch onto the same branch is the same push, no matter how many attempts produced it.

**SP-K03-3 (MUST)** — Two gates, nothing else: the Git proxy and the egress proxy. What does not go
through a gate does not exist — enforceable, because pods have no network (T-04).

**SP-K03-4 (MUST)** — Non-idempotent targets (email, payment, foreign ticket systems) get a register:
record first, then execute, then acknowledge. **If the acknowledgement is missing, ask; do not retry.**
That is the only place in the system where retrying is forbidden.

**SP-K03-5 (MUST)** — Replies into channels are events (T-02); the adapter deduplicates via the event
ID. A restart of the control plane produces no second message.

**SP-K03-6 (MUST)** — The outbox lies on `/var` of the work node (A-05) and survives the pod and the
restart of the worker.

---

## §5 Intake and adapters (T-01)

**SP-T01-1 (MUST)** — There is no list of supported devices, but an adapter contract with four methods:

| Method | Provides |
|---|---|
| `receive()` | shapes a platform event into a uniform envelope: channel, message ID, sender, text, attachments, thread reference, time |
| `identity()` | delivers only the external identifier, for example `discord:184…` |
| `respond()` | renders platform events into the language of the channel |
| `capabilities()` | declares threads, attachments, buttons, character limit |

**SP-T01-2 (MUST)** — From the captain onward, nobody knows any more where a job came from.

**SP-T01-3 (MUST)** — An adapter is itself only a small workload; a new intake is a job
(`adapter.build`, F-03), not a release.

**SP-T01-4 (MUST)** — The authority comes from the channel, not from the text:

| Level | Examples | What may come of it |
|---|---|---|
| `confidential` | CLI with a device certificate, MCP, own app | everything, including push and deploy |
| `linked` | Discord DM, X DM, Slack DM of a linked account | ordinary jobs; protected branches only with confirmation |
| `public` | open Discord channel, X mention, shared link | read and propose, hard budget, never write rights |

**SP-T01-5 (MUST)** — First contact produces no job, but an invitation to link, confirmed over a
confidential channel. Never attribute automatically.

**SP-T01-6 (MUST)** — Confirmation happens on a different channel than the request. A deploy from the
Discord DM is confirmed in the app.

**SP-T01-7 (MUST)** — Every envelope needs an idempotency key. Chat platforms redeliver; without a key
a retry starts three pods.

**SP-T01-8 (MUST)** — Limits are in **pod minutes**, not in requests — per principal and per channel.

**SP-T01-9 (MUST)** — Text from open channels is data, never instruction. The authority is attached to
the envelope at intake and travels unchanged into the pod.

**SP-T01-10 (MUST)** — Pods at level `public` run under gVisor (`runsc`), bound to the authority level,
not to a setting (E-02).

---

## §6 Captain (T-02)

**SP-T02-1 (MUST)** — The captain dispatches and executes nothing. Five steps: `decompose` → `locate`
→ `size` → `prioritize` → `dispatch`.

**SP-T02-2 (MUST)** — It runs in a pod itself, **never with a containerd or Docker socket**. It gets
the same API as everyone else, only with a higher authority level.

**SP-T02-3 (MUST)** — Rights matrix:

| The captain may | The captain may not |
|---|---|
| produce jobs, up to the authority of its envelope | grant authority beyond that of the envelope |
| status, freeze, cancel within its own project | see pods of foreign projects or principals |
| read results: patches, logs, reports | mount arbitrary host paths |
| ask clarifications through the adapters | change the pipeline definition |

The last row carries the most: the pipeline is configuration that belongs to the human, not a runtime
object of the captain.

**SP-T02-4 (MUST)** — Subagents by write authority, not per agent: think only → a process in the
captain pod; read → a process with a read-only mount; write or execute code → its own workpod.

**SP-T02-5 (MUST)** — One captain per project, not one globally.

**SP-T02-6 (MUST)** — Stateless between turns: the transcript in the database, the pod is woken, acts,
disappears.

**SP-T02-7 (MUST)** — A small router **without a model** stays on the host: deduplicate, resolve
identity, attach authority, determine the project, wake the captain.

**SP-T02-8 (MUST)** — No project mounts in the captain pod. It reads results through the API.

**SP-T02-9 (MUST)** — What flows back are events, not messages: `accepted`, `started`, `check.failed`,
`clarification`, `done`. Every adapter renders them with its own verbosity profile; a project with 200
subjobs does not produce 200 replies in one thread. One thread corresponds to one project.

---

## §7 Intent contract (Q-01)

**SP-Q01-1 (MUST)** — Between the envelope and the job stands the `spec`. Five steps: `prompt` →
`interpret` (goal, bounds, assumptions) → `acceptance` (how you recognize it) → `clarify` (only when it
changes something) → `spec` (versioned, citable).

**SP-Q01-2 (MUST)** — Jobs cite the specification version they were produced against. If the intent
changes, the version changes — and all open jobs of the old version are **re-evaluated**, not silently
carried on.

**SP-Q01-3 (MUST)** — Clarify by reversibility, not by confidence:

| Effect | Interpretation unambiguous | Interpretation ambiguous |
|---|---|---|
| `reversible` (branch, sandbox, report) | execute | execute, log the assumption |
| `costly` (merge, migration, rebuild) | execute, raise the evidence class | clarify exactly once |
| `irreversible` (deploy, deletion, message to the outside, money) | confirmation on a different channel (T-01) | not without a human, no automation |

**SP-Q01-4 (MUST)** — At most one clarification per job, and only about the ambiguity that actually
changes the execution.

**SP-Q01-5 (MUST)** — Assumptions are objects, not prose: standing individually, revocable
individually; a revocation invalidates exactly the jobs that rest on it.

**SP-Q01-6 (MUST)** — **No acceptance criterion, no job.** If it is missing and cannot be derived, that
is a finding and produces a preparation job. Hard gate.

**SP-Q01-7 (MUST)** — The specification is the only object of the platform that belongs to the human
(consequence for retention: E-07, project + 12 months; export: V-05).

---

## §8 Burden of proof, kinds of failure, test bench (Q-02, Q-03, Q-04)

### 8.1 Evidence classes (Q-02)

**SP-Q02-1 (MUST)** — Acceptance is against evidence classes, cheapest first; the class demanded
follows from the risk, not from the size of the diff:

| Class | Proves | Cost | Demanded for |
|---|---|---|---|
| `artifact.identical` | behavior unchanged (bit-identical, G-06) | almost nothing | mechanical transformation |
| `types.lint` | inner consistency | seconds | always, without exception |
| `tests.existing` | no regression in the covered part | minutes | always, where tests exist |
| `tests.new` | the criterion from Q-01 is met | minutes | every change of behavior |
| `mutation.diff` | the tests really check something | expensive | payment, rights, data paths |
| `review.independent` | a second opinion without contagion | medium | everything irreversible |
| `human` | appropriateness, not correctness | most expensive | decisions (G-01) |

**SP-Q02-2 (MUST)** — The checker never sees the path, only the result: diff, criterion, facts — not
the author's transcript, not the author's rationale, and where possible a different model (E-06: a
different provider).

**SP-Q02-3 (MUST)** — Self-report does not count. Every claim by an agent is either mechanically
evidenced (for example through `fact.query`) or marked as an assumption.

**SP-Q02-4 (MUST)** — Every check blocks, or it is removed. Non-blocking warnings are forbidden.

**SP-Q02-5 (MUST)** — Repetition is not a check: no majority vote over correlated runs.

### 8.2 Kinds of failure (Q-03)

**SP-Q03-1 (MUST)** — Every terminal state `failed` and `unproven` is assigned to one of the seven
classes; evaluation by class is a query:

| Kind of failure | Caught by | Not caught by |
|---|---|---|
| `fact.invented` | facts from derivation instead of model knowledge (G-01), the compiler | more tests, a bigger model |
| `context.missing` | gap report and hard gate (G-07), fact query | a bigger context window |
| `spec.wrong` | intent contract with an acceptance criterion (Q-01) | any technical check, without exception |
| `tool.failure` | retry with isolation, flake detection, attempt counter (K-02) | changing model |
| `injection` | the authority chain as a token (K-04), gates verifying at the effect | prompt hardening alone |
| `regression.silent` | regression corpus with an escape rate (Q-04) | spot checks in operation |
| `assumption.replicated` | judgements with a citation, contradictions marked instead of resolved (G-01) | a single review, a spot check |

**SP-Q03-2 (MUST)** — `spec.wrong` is prevented exclusively at the front (Q-01). No technical mechanism
in the system may be passed off as a net for it.

### 8.3 Test bench (Q-04)

**SP-Q04-1 (MUST)** — System prompt, model version, context assembly and pipeline version are
**releases** and pass through the same gate: `corpus` → `replay` → `compare` → `canary`.

**SP-Q04-2 (MUST)** — Repeatable means: no live calls. The corpus runs against frozen facts, recorded
tool responses and a fixed model version.

**SP-Q04-3 (MUST)** — Four metrics, one judgement:

| Metric | Measures | Responds to |
|---|---|---|
| `escape_rate` | defective, but accepted | evidence classes (Q-02) |
| `false_reject_rate` | correct, but rejected | gates that are too strict |
| `cost_per_acceptance` | tokens and pod minutes until evidence | model routing, context width |
| `no_clarification_rate` | how much intent really suffices | Q-01 |

**SP-Q04-4 (MUST)** — Model, prompt and pipeline version stand in the **job log**, not only in the
cache key.

**SP-Q04-5 (MUST)** — The corpus grows out of operation: every case that slipped through is taken in.

**SP-Q04-6 (MUST)** — The canary compares on the same jobs; the result is discarded, only the metrics
count.

**SP-Q04-7 (MUST)** — Prompt and model version are pinnable per project; a running project does not
change generation mid-run. Rolling back MUST be cheaper than repairing.

---

## §9 Capabilities (F-01 through F-07)

### 9.1 What a capability is (F-01)

**SP-F01-1 (MUST)** — A capability is a versioned, content-addressed object of five parts, none of
which may be missing:

| Part | Content | If it is missing, then |
|---|---|---|
| `description` | what I am for, and when not | the coverage check (F-04) finds nothing to resolve against |
| `precondition` | what must be true before I start | the pod fails after twenty minutes on something that stood in a table beforehand |
| `procedure` | which steps, and where exactly a model | every run invents the path anew, no two runs are comparable |
| `evidence` | which evidence class I deliver | "done" means again whatever the agent takes it to mean |
| `boundary` | what I cannot do, and what I do then | the agent improvises beyond its reach, and does so convincingly |

**SP-F01-2 (MUST)** — Every capability names its minimum authority level (`min_authority`). An envelope
at level `public` cannot even resolve a writing capability — enforcement, not an admonition in the
prompt.

**SP-F01-3 (MUST)** — The cut stays sharp: capabilities are the **how**. Not the may (K-04), not the
knows (G-01), not the costs (V-04), not the pipeline (T-05).

**SP-F01-4 (MUST)** — Every capability has an abort condition of its own, tighter than the global
rework round from T-05. A handle that fails twice is broken, not unlucky.

### 9.2 Three orders (F-02)

**SP-F02-1 (MUST)** — The cut is by provability, not by cost:

| Order | Model share | Evidence | Ends with |
|---|---|---|---|
| `handle` | none | mechanical, by construction | an artifact |
| `procedure` | exactly one decision: which handles, in which order | types, tests, artifact comparison | a checkable claim |
| `campaign` | plan and judgement over aggregates | cited facts plus the executed mechanical share | a plan and waves (G-04) |

**SP-F02-2 (MUST)** — Call downward only: `campaign` → `procedure` → `handle` → nothing. The call graph
is a directed tree of bounded depth; the checker MUST reject cycles and upward calls.

**SP-F02-3 (MUST)** — A campaign changes nothing itself; it produces subjobs.

**SP-F02-4 (MUST)** — The model sits in exactly one place per procedure: it decides the **whether**,
never the **how**.

### 9.3 Starting stock (F-03)

**SP-F03-1 (MUST)** — On day one the catalog contains:

*Handles (no model, evidence by construction):*
`fact.query`, `context.assemble`, `test.select`, `build`, `check.types`, `check.tests`,
`codemod.apply`, `artifact.compare`, `flake.detect`, `snapshot.create`, `snapshot.reap`,
`effect.enqueue`, `gap.report`.

*Procedures (one model step, evidence at the test):*
`spec.derive`, `test.repair`, `bug.fix`, `interface.change`, `function.extract`, `dependency.update`,
`test.write`, `review.produce`, `image.build`, `adapter.build`.

*Campaigns (plan and waves, no change of their own):*
`project.plan`, `corpus.reduce`, `module.extract`, `dependencies.untangle`, `framework.migrate`,
`coverage.build`.

**SP-F03-2 (MUST)** — `gap.report` is a capability, not a failure: stopping is itself a usable result.

**SP-F03-3 (SHOULD)** — The catalog is the only artifact that deliberately grows — but only by checked
pieces, each with a boundary.

### 9.4 Coverage check (F-04)

**SP-F04-1 (MUST)** — Four steps before every job start: `criteria` (from the specification) →
`resolve` (which capabilities, which versions) → `coverage` (complete or not) → `gap` (a build job or a
clarification).

**SP-F04-2 (MUST)** — A job starts only on complete coverage. Hard gate.

**SP-F04-3 (MUST)** — Failure causes are distinguished, because only one of them is a missing
capability: `skill.missing`, `knowledge.missing`, `goal.wrong`, `budget.exhausted`, `unsolvable`. For
`goal.wrong` it holds that more capability makes it worse.

**SP-F04-4 (MUST)** — `coverage_rate` (share of jobs without a new capability) and `success_rate` per
**capability** (not per model) are tracked.

**SP-F04-5 (MUST)** — Capabilities are pinned per job via a content hash, never "latest".

**SP-F04-6 (MUST)** — The coverage check stands **behind** Q-01, not in front of it: first the
criterion, then the question whether we can do it.

### 9.5 The gap is a job (F-05)

**SP-F05-1 (MUST)** — If a procedure finds no fitting handle, it does **not** build it mid-run. It
reports a capability gap, and that becomes an ordinary job: the same pipeline, the same checks, the
same test bench.

**SP-F05-2 (MUST)** — Intake condition for a new capability: an evidence class, a named boundary, at
least **three cases in the regression corpus** (Q-04), and a name that also says what it does not do.

**SP-F05-3 (MUST)** — An aborted capability delivers a gap report and a proposal for splitting — never
silence and never half a result without a note.

**SP-F05-4 (MUST)** — Campaigns may report gaps too.

### 9.6 Format (F-07)

**SP-F07-1 (MUST)** — A capability is stored as a `SKILL.md` per the open Agent Skills standard: YAML
front matter plus a Markdown body, alongside `scripts/`, `references/`, `assets/`.

**SP-F07-2 (MUST)** — Mapping of the five parts:

| Part from F-01 | In the format |
|---|---|
| `description` | `description` in the front matter, including the convention "do not use for …" |
| `procedure` | the Markdown body; deterministic steps as `scripts/`, whose code never enters the context, only whose output does |
| `precondition` | `metadata.precondition`, plus `allowed-tools` for the tool side |
| `evidence` | `metadata.evidence` |
| `boundary` | `metadata.boundary` with the exit `gap.report` |
| order, authority | `metadata.order` (`handle` · `procedure` · `campaign`), `metadata.min_authority` |

**SP-F07-3 (MUST)** — **The pod never sees the catalog.** The coverage check resolves beforehand and
encloses only the resolved capabilities. An agent does not choose what it reads.

**SP-F07-4 (MUST)** — A capability MUST be runnable unchanged in a foreign standard runtime (only
`name`, `description`, body); unknown fields are ignored there.

**SP-F07-5 (MUST)** — Evaluations belong to the format, not beside it: three cases per capability in
the corpus before it may enter the catalog (identical to SP-F05-2).

---

## §10 Knowledge layer and context assembly (G-01, G-07, E-09)

### 10.1 Three layers (G-01)

**SP-G01-1 (MUST)** — `facts`, `judgments`, `decisions` are never mixed.

**SP-G01-2 (MUST)** — Facts are **derived**, never produced by a model: compiler front ends, build
graph queries, `git blame`. A hallucinated call graph is worse than none.

**SP-G01-3 (MUST)** — Every judgement cites the facts it rests on. If a content hash changes, all
judgements over it expire **automatically**.

**SP-G01-4 (MUST)** — Judgements go into an append-only log; new ones replace old ones explicitly
instead of overwriting them. Contradictions are marked, not resolved.

**SP-G01-5 (MUST)** — Decisions are few, versioned, the only serial part, and become a
machine-checkable contract (module A may depend on B and C, not on D). They lie in Git (V-05).

**SP-G01-6 (MUST)** — Agents do not read code in order to understand — they read facts. Code is read
only in narrowly cut windows, when something concrete is being changed.

### 10.2 Fact store (E-09)

**SP-E09-1 (MUST)** — The producer format is **SCIP**, per language the existing indexer. A missing
indexer is a job (F-05), not a project.

**SP-E09-2 (MUST)** — Storage columnar as **Parquet**, partitioned by build target, keyed by `file
content + indexer version` (G-03), as a read-only CoW snapshot in the pod.

**SP-E09-3 (MUST)** — The query is embedded in the harness (DuckDB), read-only in the pod — no service,
no network. `fact.query` is a file read, not a call.

**SP-E09-4 (MUST)** — Derived relations (transitive call graph, reachability, points-to on candidates)
are computed by **Soufflé** in `maintenance` jobs and written back as further partitions.

**SP-E09-5 (MUST)** — Exactly one writer: the indexer of the knowledge layer. Pods read — the same rule
as with the cache daemon (G-03).

**SP-E09-6 (MUST)** — Facts never leave the cell (E-07). With files that is a mount, not a firewall
rule.

### 10.3 Context assembly (G-07)

**SP-G07-1 (MUST)** — A deterministic step **before** the agent, with a budget and an abort condition,
in five stages: `anchor` (intention → symbols, by query) → `hull` (typed traversal) → `skeleton`
(signatures, broad) → `body` (only for survivors) → `manifest` (what is missing and why).

**SP-G07-2 (MUST)** — Traversal depths:

| Kind of edge | Depth | Rationale |
|---|---|---|
| called functions | 2–3 | behavior of the anchor |
| callers | 1–2, all for an interface | radius of change |
| types, interfaces | unbounded | small and load-bearing |
| tests at the anchor | always | without them no self-checking |
| build and configuration | always | decides what counts as silenced |
| library internals | 0 | signatures suffice |

**SP-G07-3 (MUST)** — The map is never trimmed. The context is filtered, never the knowledge layer.

**SP-G07-4 (MUST)** — Activity state as a filter: `active` with a body, `dormant` signature only and
marked, `silenced` only with a matching target configuration, `fossil` not at all — but as a **count in
the manifest**, so the agent can ask.

**SP-G07-5 (MUST)** — Drift as a freshness measure: commits on the code since the last change to the
documentation. A README with a large backlog comes in marked or not at all. Instructions that
contradict the actual state are flagged before acting.

**SP-G07-6 (MUST)** — A gap report instead of guessing: if an index is missing, tests are missing,
ownership is missing, the pod does not start, but produces a preparation job or asks through the
adapter. Hard gate.

**SP-G07-7 (MUST)** — Never truncate silently. If the hull blows the budget, the pod reports that the
job is cut too broadly and proposes a split.

**SP-G07-8 (MUST)** — Thin context changes the pipeline: no tests at the anchor → acceptance on human
review, or tests as a job of their own first; no callers found → gap report; no documentation with high
complexity → the job is marked risky, a narrower change area, a plan mandatory.

**SP-G07-9 (MUST)** — Nothing is dragged along between jobs. Every pod delivers **judgement candidates
with a citation** to their facts at the end; the next pod gets them as a cheap query. This write-back
path is the only thing that makes the system cheaper across thousands of jobs.

---

## §11 Container image, workpod, pipeline (T-03, T-04, T-05)

### 11.1 Container image resolution (T-03)

**SP-T03-1 (MUST)** — From the requirements of the job (language and version, system packages, test
runner, browser engine) a `requirement hash` is formed and looked up in the image index. A hit: the pod
starts in ~200 ms. A miss: a build job.

**SP-T03-2 (MUST)** — The build agent writes an image specification, builds it, runs a smoke test and
publishes it in the index **only then**.

**SP-T03-3 (MUST)** — No special path: the image build is an ordinary job in an ordinary workpod, with
the same pipeline, the same limits, the same isolation.

**SP-T03-4 (MUST)** — The image index is content-addressed and lies globally (V-03), not per cell.

### 11.2 The workpod (T-04)

**SP-T04-1 (MUST)** — What is inside: a root filesystem from shared read-only layers; a working copy as
a **CoW snapshot in O(1)**; one agent with a job, context and a repair budget; a resource contract of
`cpu.weight`, `memory.high`, `pids.max`.

**SP-T04-2 (MUST)** — What is not inside: **no network** (the only way out is a Unix socket to the
egress proxy), **no LLM key** (the proxy inserts it), **no Git token** (the push goes against the Git
proxy, which checks the policy and signs itself), **no logs** (everything goes to the host
immediately).

**SP-T04-3 (MUST)** — Lifecycle: `created` (the snapshot stands) → `active` → `frozen` (after 45 s of
quiet) → `checkpointed` (dump to disk) → `reaped` (patch out, pod gone).

**SP-T04-4 (MUST)** — The abstraction is over `Runner`, not over `Workpod`. The contract is
operating-system neutral: *given a working directory and a job, deliver a patch and a report.* The
envelope carries `platform`, the scheduler knows several pools: `alpine` (the normal case), `windows`,
`macos`, `remote`.

**SP-T04-5 (MUST)** — The **reaper** is mandatory: every pod has a lifetime and an idle limit. It runs
on the worker, not in the control plane (V-02).

### 11.3 Pipeline (T-05)

**SP-T05-1 (MUST)** — A fixed spine, the same for all jobs: `prepare` → `plan` → `edit` → `check` →
`repair` → `deliver` → `reap`.

**SP-T05-2 (MUST)** — Movable per job, and only at these places: which image/which toolchain; whether a
plan is demanded; which paths the agent may touch; which checks run and which block; how many rework
rounds; the acceptance criterion; whether the snapshot is kept on failure.

**SP-T05-3 (MUST)** — The loop `check → repair` has an end: at most `n` rounds (§19, OP-2). After that
the pod does not fail silently, but reports back with a diff, logs and an assessment of its own.

**SP-T05-4 (MUST)** — The pipeline definition is versioned (`pipeline@version`) and belongs to the
human; no runtime object may change it (T-02).

---

## §12 Resources and scheduler (R-A through R-D)

### 12.1 Resource contract (R-A)

**SP-RA-1 (MUST)** — Four classes, the request guaranteed, the limit tolerated:

| Class | CPU requested | CPU limit | RAM requested | RAM limit | Typical for |
|---|---|---|---|---|---|
| `tiny` | 0.1 | 1.0 | 128 MB | 512 MB | reading, searching, planning |
| `small` | 0.3 | 2.0 | 384 MB | 1.5 GB | scripts, small patches |
| `medium` | 1.0 | 4.0 | 1 GB | 3 GB | building and testing |
| `large` | 2.0 | 8.0 | 3 GB | 8 GB | monorepo, E2E, image build |

**SP-RA-2 (MUST)** — `memory.high` instead of `memory.max`: a pod is throttled, not shot.

**SP-RA-3 (MUST)** — `cpu.weight` instead of `cpu.max`: fairness through weights, not through hard
ceilings.

**SP-RA-4 (MUST)** — `pids.max` and `io.latency` are set.

### 12.2 Scheduler (R-B)

**SP-RB-1 (MUST)** — Tokens **per phase**, not one counter: `net` (planning, reworking — many slots),
`io` (preparing, clearing up — few), `cpu·ram` (building, checking — the bottleneck). A pod holds only
the token of its current phase; whoever waits for a model response returns its CPU token beforehand.

**SP-RB-2 (MUST)** — Four priorities:

| Priority | Waits at most | May preempt | For what |
|---|---|---|---|
| `interactive` | 2 s | yes | someone is sitting in front of it |
| `batch` | 5 min | no | the hundred subjobs of a project |
| `maintenance` | 1 h | no | image build, updating dependencies |
| `background` | unbounded | no | cleaning up, indexing, warming |

**SP-RB-3 (MUST)** — Aging in the queue: the longer a batch job waits, the further it rises.

**SP-RB-4 (MUST)** — Preempting means **freezing, not aborting**. The pod loses its slot, not its
state.

**SP-RB-5 (MUST)** — Large runs get exclusive operation: above ~60 % of the available RAM one job holds
all CPU tokens; running pods are frozen at the next phase boundary.

**SP-RB-6 (MUST)** — Two large runs are never time-sliced. Order: short ones first, with aging as
protection.

**SP-RB-7 (MUST)** — The queue lies in Postgres with `SKIP LOCKED`. No second broker (E-02).

### 12.3 Bottlenecks (R-C)

**SP-RC-1 (MUST)** — What is read is **pressure**, not utilization: `cpu.pressure`, `memory.pressure`,
`io.pressure` from the pods slice, every two seconds.

**SP-RC-2 (MUST)** — Thresholds and reactions:

| Signal | Meaning | Reaction |
|---|---|---|
| `memory some avg10 > 10 %` | getting tight | admit no new pods |
| `memory full avg10 > 5 %` | everything is stalled | freeze immediately, do not wait |
| `io full avg10 > 20 %` | the disk is the bottleneck | I/O tokens to 1, serialize installations |
| `cpu some avg60 > 60 %` with free tokens | something is computing beside the point | look for zram, the proxy or an outlier |
| `memory.events high` rising | the pod is classified wrongly | raise the class, do not give it more time |
| `pgmajfault` rising fast | swap thrashing is beginning | the hardest rung, immediately |

**SP-RC-3 (MUST)** — Escalation ladder: `throttle` (lower cpu.weight) → `block` (no admission) →
`freeze` (lowest priority first) → `checkpoint` (CRIU dump) → `escalate` (to the captain).

**SP-RC-4 (MUST)** — `memory.min` on the system slice reserves the control plane, the proxies and
access hard (default 4 GB, E-05). `memory.oom.group=1` as a net beneath it.

**SP-RC-5 (MUST)** — Concurrency is injected from the allocation, because `os.cpus()` in a container
reports the host's cores: `MAKEFLAGS`, `CARGO_BUILD_JOBS`, `UV_THREADPOOL_SIZE`, `NODE_OPTIONS`,
`TURBO_CONCURRENCY`.

**SP-RC-6 (MUST)** — Predict instead of clean up: record peak RSS and runtime per repository and phase.
After three runs admission decides mechanically; above 90 % a job does not start at all, but reports
back with options.

### 12.4 Occupancy table (R-D)

**SP-RD-1 (SHOULD)** — The occupancy table is an instrument, not a contract. *(The original clause
"stays German" is void; see the language ruling in §0.1.)*

**SP-RD-2 (MUST)** — In operation it shows real values from `cpu.pressure` and `memory.pressure` (R-C)
in the same places — the same display, a different source. It does not estimate.

**SP-RD-3 (MUST)** — The five constants from E-05 are **planning values**. Admission and preemption do
not read them.

---

## §13 Distribution (V-01 through V-05, E-03, E-04, E-07)

### 13.1 Four layers (V-01)

**SP-V01-1 (MUST)** — `control` (router, scheduler, state, leases), `captain` (reserved capacity per
project), `knowledge` (fact store, scales for reading), `work` (workpods, snapshots, caches). Logically
constant, physically variable.

**SP-V01-2 (MUST)** — The control layer holds only what is expensive to reconstruct: jobs, projects,
authorities, identity links, pipeline definitions, decision references, leases. Everything else may
die.

**SP-V01-3 (MUST)** — The captain does not belong on the control island; it is a privileged client.

**SP-V01-4 (MUST)** — Reservation beats priority. Under sustained load priority degrades and produces
the death spiral (overload → scheduler slow → admission lags → more admitted).

**SP-V01-5 (SHOULD)** — Isolation levels 0–3 (cgroup slices with `memory.min` → the control plane in
its own VM → its own small machine → the control plane replicated, workers as a fleet). Level 0
suffices on day one; level 2 from the second work node on.

### 13.2 Ferry and failure (V-02)

**SP-V02-1 (MUST)** — Not push, but pull: the worker requests capacity, receives jobs with a deadline
and extends it by heartbeat. If the deadline expires, the job goes back into the queue. No leader, no
election.

**SP-V02-2 (MUST)** — Workers have **no open port**. All connections go outbound.

**SP-V02-3 (MUST)** — Static stability when the control plane fails:

| What | Behavior |
|---|---|
| running pods | run to completion, do not need the control plane |
| valid leases | keep working until the deadline ends |
| new jobs | adapters buffer, nothing is lost |
| results | buffered locally, delivered afterwards |
| egress policy | the last known allowlist stays valid |
| new authorities | are refused |

**SP-V02-4 (MUST)** — Locality: sticky assignment repository → node, queues per `locality_group`, work
stealing only within. A scheduler that spreads evenly is slower on this load than a single machine.

**SP-V02-5 (MUST)** — No Raft, no consensus in revision 1. One control machine with replicated Postgres
and static stability is level 3.

### 13.3 Cells (V-03, E-03, E-04)

**SP-V03-1 (MUST)** — Four layers: `global` (identity, project→cell, image index, cost accounts —
**identifiers only, never content**) → `cell` (control plane, fact store, queue, gates) → `locality
group` → `node`.

**SP-V03-2 (MUST)** — The project is the routing key and lies **wholly** in one cell. Across cell
boundaries only enrollment, assignment and accounting flow.

**SP-V03-3 (MUST)** — The tenant cuts the cell, the repository family cuts the locality group inside it
(E-03). A tenant never crosses a cell boundary; if it grows too large, it gets several cells, cut by
repository family.

**SP-V03-4 (MUST)** — Regulatory requirements get a cell of their own, not a setting of their own.

**SP-V03-5 (MUST)** — The cell boundary (E-04) = the smaller of **500 active projects** and the node
count at which p99 of `admitted → occupied` reaches **2 seconds** with free slots. Starting value 32
work nodes. A new cell is opened at **80 %**.

**SP-V03-6 (MUST)** — Changing cell is migration, not failover: freeze the project, copy the state,
re-derive the facts in the target cell, switch over.

### 13.4 Tenants and budget pots (V-04)

**SP-V04-1 (MUST)** — Three pots: `pod_minutes` (reserved at **admission**, not at billing), `tokens`
(in advance per job from the analysis budget), `money` (a daily cap per principal).

**SP-V04-2 (MUST)** — Exhausted means: `pod_minutes` → no new jobs, running ones to completion;
`tokens` → a reply with options instead of a silent truncation; `money` → a hard limit, only a human
raises it (E-08: two people).

**SP-V04-3 (MUST)** — Reserve in advance, do not count afterwards. An unspent reservation is released
at the terminal state.

**SP-V04-4 (MUST)** — Fairness through weighted shares of the **bottleneck**, not through a queue per
tenant. The scarcest resource changes (R-C).

**SP-V04-5 (MUST)** — Three caps, three purposes: per envelope against abuse, per project against
outliers, per principal and day against the bill.

**SP-V04-6 (MUST)** — Deletion is a job, not a ticket: "remove everything belonging to this project" is
executable and evidenceable, because every object carries its project (K-01).

### 13.5 State and backup (V-05)

**SP-V05-1 (MUST)** — Backup plan by origin:

| Kind of data | Origin | Backup |
|---|---|---|
| `control state` | authoritative | synchronous replica in the cell, point-in-time restore |
| `specs` | human | like control state, additionally exportable |
| `decisions` | human, serial | **in Git, not in the database** |
| `judgments` | expensively produced | append-only log, replicated |
| `facts` | derivable | a daily snapshot suffices |
| `snapshots, caches, images` | reproducible | do not back up at all |
| `audit` | evidence | separate storage, immutable, its own period |

**SP-V05-2 (MUST)** — Migrations run additively: write the new field, read both, remove the old one —
three releases. Under a rolling update two versions run at the same time.

**SP-V05-3 (MUST)** — Restore is practiced, not documented: rebuild a cell from backup, run it against
the regression corpus, measure the time. The drill is an ordinary job.

### 13.6 Regions and retention (E-07)

**SP-E07-1 (MUST)** — One region per tenant, default the EU. Content never leaves its cell.

**SP-E07-2 (MUST)** — Default periods:

| Kind of data | Period |
|---|---|
| `envelope` (raw text, attachments) | 30 days |
| `model_io` | 30 days, never for provider training |
| `traces, logs` | 90 days |
| `facts, judgments` | bound to the content hash, expire with it |
| `specs, decisions` | project + 12 months |
| `audit` | 12 months, extendable per tenant |

**SP-E07-3 (MUST)** — Deletion is a job with a deadline of 30 days. What stays in the audit trail is
the deletion receipt, not the content.

**SP-E07-4 (MUST)** — Statutory retention obligations are set by the tenant; the setting is a **cell
property**, so that it cannot be circumvented.

---

## §14 Operations (B-01 through B-04, E-08)

### 14.1 Identity of the machines (B-01)

**SP-B01-1 (MUST)** — Chain: `enrollment token` (single use, valid for minutes) → `node certificate`
(role and cell **in the name**) → `renewal` (hourly, overlapping) → `expiry` (expires by itself).

**SP-B01-2 (MUST)** — No shared standing secret in the system image. An image is to be treated as
public. Node identity comes into being at first start and lies encrypted on `/var` (A-05).

**SP-B01-3 (SHOULD)** — Where the hardware allows: keys in the TPM, attestation on joining.

**SP-B01-4 (MUST)** — Credentials for models and Git lie exclusively in the gates, never on work nodes
and never in pods.

**SP-B01-5 (MUST)** — Rotation is routine: node certificates hourly, gate secrets weekly, issuers
yearly, each with an overlap.

### 14.2 Network boundaries and gates (B-02)

**SP-B02-1 (MUST)** — Boundaries:

| Boundary | May | May not |
|---|---|---|
| `pod` | one Unix socket to the egress proxy | network, DNS, ports, neighboring pods |
| `node` | outbound to the control plane and the gates | inbound ports, not even SSH |
| `gates` | onto the internet, with keys and an allowlist | accept jobs, hold state |
| `cell` | internally free, outward named | data into other cells |

**SP-B02-2 (MUST)** — The egress proxy stands on the **work node**, not centrally.

**SP-B02-3 (MUST)** — No name resolution in the pod. The proxy resolves; the pod knows only names from
its allowlist.

**SP-B02-4 (MUST)** — An allowlist **per job**, not per node: target, method, size limit — derived from
the authority (K-04).

**SP-B02-5 (MUST)** — Rejected targets belong in the display, not only in the log: the best early
warning signal for injection this system has.

**SP-B02-6 (MUST)** — No open port means no open port. Remote access runs over the same outbound
channel or not at all.

**SP-B02-7 (MUST)** — The rules come from the system image (nftables, default deny), not from the
runtime.

### 14.3 Observation (B-03)

**SP-B03-1 (MUST)** — The unit of observation is the **job**: one trace from the envelope to the
result, with the phases from R-B as spans, with cost, evidence class, attempt as well as model, prompt
and pipeline version.

**SP-B03-2 (MUST)** — Four SLOs: `time_to_first_progress` (interactive jobs), `no_clarification_rate`,
`escape_rate`, `cost_per_acceptance` (per project and tenant).

**SP-B03-3 (MUST)** — **Exactly four alerts** may wake a human: the control plane unreachable; the queue
growing monotonically over twenty minutes; escapes or rejections jumping; the budget of a cell
exhausted prematurely. Everything else is a display. A fifth alert devalues the four.

**SP-B03-4 (MUST)** — The logs of the pods are evidence: to the node immediately, tagged there with the
job ID and the attempt, kept linked to the job.

**SP-B03-5 (MUST)** — The audit trail lies separate and immutable, with its own retention period per
tenant: who received which authority when, which gate let what through, which human accepted what.

**SP-B03-6 (MUST)** — Cost visibility per project.

### 14.4 The bad day (B-04)

**SP-B04-1 (MUST)** — Decided beforehand, not in the middle of it:

| Incident | First remedy | Who triggers |
|---|---|---|
| injection suspected (the gate rejects targets in clusters) | freeze the project, block the project's authorities | a threshold |
| node compromised | take it out of the allocation, checkpoint the pods out, restart the node | automatically |
| model provider disrupted | a second provider, raise the acceptance class, pause the batch | operations |
| cost outlier | close the pots, running jobs to completion, no new ones | a threshold |
| the queue fills up | load shedding at the intake, `public` first | automatically |
| poisoned job | after two crashes, quarantine | automatically |
| the situation is unclear | a global halt: no more admission, running ones to completion, state stays | a named human |

**SP-B04-2 (MUST)** — The global halt is a property of **admission**, not a restart. In an incident no
processes are shot.

**SP-B04-3 (MUST)** — Rejection happens at the intake, not in the middle.

**SP-B04-4 (MUST)** — Every automation has an off position and a log.

**SP-B04-5 (MUST)** — Practice happens in operation: let a cell fail, restart the control plane, replay
a backup.

### 14.5 The duty officer (E-08)

**SP-E08-1 (MUST)** — A role on call, at least two trained people, one of them active. It may **halt
alone and never widen alone**.

**SP-E08-2 (MUST)** — Actions:

| Action | Who | Rule |
|---|---|---|
| `halt.set` | one person | a rationale is mandatory, reported over all adapters |
| `halt.renew` | one person | again every 60 minutes, otherwise it expires |
| `halt.clear` | one person | logged, with a situation report |
| `cap.raise`, `authority.extend` | **two people** | everything that widens rights or money |
| `project.freeze` | a threshold or one person | (B-04) |

**SP-E08-3 (MUST)** — The halt has two paths: a field in admission **and** a file on the control node
that is read at every admission decision — so that it also takes effect when the API no longer
answers.

**SP-E08-4 (MUST)** — The expiry after 60 minutes is mandatory, not a convenience: a state that needs
attention in order to persist disappears by itself.

---

## §15 Large jobs (G-02 through G-06) — capability content

**SP-G-1 (MUST)** — G-02 through G-06 are built as campaigns and procedures in the catalog
(`corpus.reduce`, `module.extract`, `dependencies.untangle`, `framework.migrate`, `coverage.build`), not
as a platform layer. For that the platform provides: wave execution with a barrier, an analysis budget
in the job, an aggregate view for the captain.

**SP-G02-1 (MUST)** — The reduction cascade writes **equivalence classes, not deletions** (`chunk hash`
→ `file hash` → `AST normal form` → `reachability` → `semantics`).

**SP-G02-2 (MUST)** — Reachability counts only in the intersection with dynamic evidence: statically
unreachable **and** never observed in coverage, the linker map and profile data.

**SP-G02-3 (MUST)** — Shrinking and restructuring run in separate waves.

**SP-G03-1 (MUST)** — Five caches, five keys: `facts` (file content + indexer version), `judgments`
(AST normal form + model and prompt version + cited facts), `build` (inputs + toolchain), `test result`
(hash of the affected build hull), `prompt prefix` (the shared system part + the context prefix). The
key contains the version of everything that produced the entry.

**SP-G03-2 (MUST)** — Shared caches only through a **daemon**, never as a writable directory. Pods
read, only the daemon writes, entries addressed via the hash of their inputs.

**SP-G03-3 (SHOULD)** — The package store hard-linked instead of copied (a bind mount from the host).

**SP-G04-1 (MUST)** — Waves in reverse topological order, leaves first; within a wave changes are
disjoint by construction; at the end of every wave a barrier: build, test, land.

**SP-G04-2 (MUST)** — The model decides the **whether**, a deterministic codemod does the **how**,
including all references.

**SP-G04-3 (MUST)** — The captain gets an analysis budget in the job (pod minutes, tokens, wall clock),
never sees the corpus, only aggregates, and works progressively.

**SP-G04-4 (MUST)** — The honest result of a campaign is a fact-backed migration plan plus the
mechanically verifiable share executed.

**SP-G05-1 (MUST)** — Activity state is a **field on every fact** (`active`, `dormant`, `silenced`,
`fossil`), not the result of a one-off phase; it expires with the fact.

**SP-G05-2 (MUST)** — `dormant` is never deleted (error paths, recovery). Analysis runs over the
**union of all configurations**.

**SP-G06-1 (MUST)** — Reproducible builds plus an artifact diff are the sharpest cheap oracle and are
the basis of the evidence class `artifact.identical`.

**SP-G06-2 (SHOULD)** — Cost visibility per module (`build seconds`, `binary size`, `attack surface`,
`analysis cost`) — the most effective lever in the whole document, and the only one that works without
technology.

---

## §16 System image and delivery (A-01 through A-06, E-01)

**SP-E01-1 (MUST)** — Ruled: **mkosi on Fedora packages with the Fedora kernel**. The root filesystem
under dm-verity, A/B via `systemd-sysupdate`, the data partition via `systemd-repart`, the five boot
values from A-04 as credentials.

**SP-E01-2 (MUST)** — Reproducible through a package snapshot held fixed (pinning). Without pinning
there is no bit-identical rebuild.

**SP-A02-1 (MUST)** — One image, four roles: `role = all` (a single node, four slices), `control`,
`knowledge`, `work`. The same software, one boot variable. Scaling means: add nodes with `role = work`.

**SP-A02-2 (MUST)** — In the kernel: cgroup v2 unified and PSI; overlayfs; btrfs or XFS with reflink;
user namespaces, seccomp, Landlock; zram with zstd; `CONFIG_CHECKPOINT_RESTORE`; KVM; io_uring. Out:
unused filesystems, sound, legacy drivers.

**SP-A02-3 (MUST)** — In the userland: systemd, containerd with runc (optionally runsc), btrfs-progs,
nftables, the platform binary, the state database. **No toolchains** — no compiler, no Python, no Node
on the host. No editors, no man pages, no locales except `C.UTF-8`.

**SP-A02-4 (MUST)** — systemd is not replaced: the slice hierarchy, socket activation and cgroup
management carry R-A and R-C in full.

**SP-A02-5 (MUST)** — A rolling update without a drain command: the worker stops pulling, runs its open
leases to completion, restarts, enrolls again.

**SP-A03-1 (MUST)** — Build: `source` (package list, kernel configuration, units) → `build` (mkosi,
**without network**) → `seal` (dm-verity, signature, SBOM) → `accept` (A-06, otherwise no channel) →
`formats` (raw, qcow2, cloud image).

**SP-A03-2 (MUST)** — Built twice, a bit-identical artifact.

**SP-A03-3 (MUST)** — Verity or nothing at all. The public key lies in the boot path, the private one
not on the machine.

**SP-A03-4 (MUST)** — A/B with a boot counter, fallback after three failed starts. A node reports
"healthy" only once it has **carried a job to completion** — not when systemd is finished.

**SP-A03-5 (MUST)** — The role may not change the content, only the activated units.

**SP-A03-6 (MUST)** — The version is a number, not a timestamp. Three channels: `canary`, `stable`,
`held`. A node knows which one it is in.

**SP-A03-7 (MUST)** — SBOM and signature per image.

**SP-A03-8 (MUST)** — Building is done by the platform itself (a job as in T-03). The only real special
case: the first node.

**SP-A04-1 (MUST)** — The first start receives exactly five values from outside:

| Value | Source | Example | Required |
|---|---|---|---|
| `role` | instance data | `work` | yes |
| `cell` | instance data | `eu-c1` | yes |
| `control` | instance data or DNS | `c1.intern:8443` | yes |
| `enrollment_token` | instance data, single use | short-lived | yes, except the control plane |
| `locality_group` | instance data | `monorepo-a` | no |
| `trust_anchor` | **in the system image** | issuer key | — |

**SP-A04-2 (MUST)** — Start sequence: `verity` → `disk` (find, decrypt, otherwise create) → `enroll` →
`role` (activate units) → `selftest` (a subset of A-06) → `register` (pulling begins).

**SP-A04-3 (MUST)** — A failed selftest means: **do not enroll**.

**SP-A04-4 (MUST)** — Nothing is reconfigured afterwards. A change is a new image and a new start.

**SP-A04-5 (MUST)** — The clock before enrollment.

**SP-A05-1 (MUST)** — Disk layout:

| Area | Content | Survives an update | Survives a reinstall |
|---|---|---|---|
| system A/B | the image, read-only, verity | is swapped | no |
| `/var` (encrypted) | node certificate, worker state, outbox | yes | no |
| `/data/work` | snapshots, package store, build and layer cache | yes | no, reproducible |
| `/data/db` (only `control`) | state database, audit | yes | **yes — the only one** |
| zram | compressed pages of frozen pods | no | no |

**SP-A05-2 (MUST)** — Reflink is a foundation, not a preference: btrfs or XFS with reflink, checked in
A-06.

**SP-A05-3 (MUST)** — The work disk separate from the data disk.

**SP-A05-4 (MUST)** — Encrypted with a TPM binding, not with a passphrase.

**SP-A05-5 (MUST)** — The disk is the first consumable that gets an alert.

**SP-A06-1 (MUST)** — The image is done when the acceptance list is green — not when it boots. The list
stands in full in `03-acceptance-matrix.md`, section A, and ships as a script
(`acceptance/a06-acceptance.sh`).

---

## §17 Runtime, language, models (E-02, E-06)

**SP-E02-1 (MUST)** — One **statically linked Go binary** for the control plane, the scheduler, the
worker, the adapters, both gates and the agent harness. No runtime on the host.

**SP-E02-2 (MUST)** — Postgres as the **only** state database, the queue inside it (`SKIP LOCKED`).

**SP-E02-3 (MUST)** — containerd with runc; gVisor (`runsc`) exclusively for pods at level `public`.

**SP-E02-4 (MUST)** — The agent harness is the same binary, mounted read-only into every pod. A harness
update is an image update, not a rebuild of all container images.

**SP-E02-5 (MUST)** — The control layer fits in 4 cores and 16 GB. This number is measured (A-06,
calibration run); the economics from V-01 hang on it.

**SP-E06-1 (MUST)** — Three model roles:

| Role | Task | Demands | Share |
|---|---|---|---|
| `classifier` | routing, deduplicating, pre-filtering, summarizing | small, cheap, **self-operable** | ~90 % |
| `worker` | planning, changing, reworking | large, tool-capable, long context | ~9 % |
| `reviewer` | a second opinion on diff and criterion | large, **a different provider** | ~1 %, always for the irreversible |

**SP-E06-2 (MUST)** — The job names **capabilities and a pinned version, not a provider**. Provider and
key are inserted by the egress proxy. Switching provider is a table row.

**SP-E06-3 (MUST)** — The provider is pinned per project (otherwise the prompt prefix cache expires).

**SP-E06-4 (MUST)** — The checker never runs at the provider of the worker.

**SP-E06-5 (MUST)** — Budgets run in tokens; the euro cap is reserved at the most expensive permissible
provider, so that a fallback per B-04 never blows a cap.

**SP-E06-6 (MUST)** — No provider name in the design or in code; the table is configuration.

---

## §18 Metrics — the numbers the promise hangs on

**SP-M-1 (MUST)** — Six numbers are tracked and kept breakable down by failure class (Q-03):
`escape_rate`, `false_reject_rate`, `cost_per_acceptance`, `no_clarification_rate` (Q-04),
`coverage_rate`, `success_rate` per capability (F-04).

**SP-M-2 (MUST)** — Without an escape rate, "very high quality" is a claim. `escape_rate` is the only
number that checks the quality promise, and every change to the system — including the ones in this
document — is measured against it.

**SP-M-3 (MUST)** — Every step from `02-work-packages.md` ends with **a measurement instead of an
opinion** (E-11). No step starts before the previous one has delivered its number.

---

## §19 Open points — decisions the architecture document deliberately leaves open

These values MUST be ruled before the named work package and filed as a decision in Git (V-05). They
are **not** guessed in code.

| ID | Open | Due before | Proposed ruling |
|---|---|---|---|
| OP-1 | starting values of the pots per authority level (pod minutes per envelope / project / principal-day) | AP-3.6 | `public` very small, `linked` medium, `confidential` a tenant cap |
| OP-2 | `n` rework rounds per pipeline (T-05) | AP-3.4 | 3, overridable per job class |
| OP-3 | the deadline for clarifications (K-02) before continuing under an assumption | AP-5.2 | 24 h for `batch`, 1 h for `interactive` |
| OP-4 | lease duration and heartbeat interval (V-02) | AP-2.3 | lease 60 s, heartbeat 15 s, three failures = release |
| OP-5 | size limits for attachments and permitted MIME types (K-01) | AP-3.2 | conservative, widening only as a decision |
| OP-6 | hysteresis and hold times of the PSI thresholds (R-C) | AP-3.7 | from the A-06 calibration run |
| OP-7 | the model and provider table (E-06) | AP-5.3 | two independent providers, the classifier self-operated |
| OP-8 | the cut of the first locality groups (E-03) | AP-6.2 | one per repository family, a monorepo = one |
| OP-9 | retention periods per tenant beyond the default (E-07) | AP-7.4 | a cell property, never a runtime setting |
| OP-10 | two named people for the duty officer role (E-08) | AP-7.5 | before the first delivery to outsiders |

---

## §20 Non-negotiable — the five sentences that carry everything else

1. **The authority comes from the channel, not from the text** (T-01/K-04). No model along the way can raise it.
2. **No acceptance criterion, no job; no evidence, no `delivered`** (Q-01/Q-02). `unproven` is an exit of its own.
3. **The pod has no network and never acts itself** (T-04/K-03). Everything outward goes through one of two gates, at most once.
4. **Facts are derived, never produced** (G-01). A hallucinated call graph is worse than none.
5. **Every object carries its cell and its project** (K-01). Without that there is neither deletion nor migration nor incident radius.
