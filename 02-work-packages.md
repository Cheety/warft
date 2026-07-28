# Work packages — from an empty directory to a running cell

**Base rule (E-11):** build in this order, and every step ends with **a measurement, not an opinion**.
No step starts before the previous one has delivered its number.

**Second rule (A-06):** acceptance is written before the build. `03-acceptance-matrix.md` is complete
on day one, and completely red. Progress means rows turn green — not that there are more files.

**Third rule (V-05):** every ruling that deviates from this document is filed as a decision in Git
before it becomes code. Decisions do not belong in the database.

| Stage | Result | Measures | Rough duration¹ |
|---|---|---|---|
| 0 | Workbench | nothing (no platform behavior) | 1 week |
| 1 | The base holds, or E-01 falls | the five constants (E-05) | 2–3 weeks |
| 2 | The contract | nothing — that is the point | 2–3 weeks |
| 3 | Smallest system, without a model | T-04, T-05, R-A, K-02, K-03 | 6–8 weeks |
| 4 | Handles and knowledge | success rate per handle | 6–8 weeks |
| 5 | Intent, then captain | share without a clarification | 8–10 weeks |
| 6 | Second node | V-02, and only then V-03 | 3–4 weeks |
| 7 | Test bench | `escape_rate` | 3–4 weeks |
| 8 | Growth | by measurement, not by plan | open |

¹ Guide values for two to three people. They are planning, not a promise — the same distinction as
with the constants in E-05.

---

## Stage 0 — Workbench

*Produces no platform behavior and therefore measures nothing. It stands before step 1 from E-11,
because otherwise the measurement from step 1 has nowhere to go.*

### AP-0.1 Repository layout and decision store
- **Content:** one repository with `image/` (mkosi), `platform/` (Go), `skills/` (the `SKILL.md`
  catalog), `decisions/` (decisions as Markdown, versioned), `acceptance/` (check scripts),
  `contract/` (Protobuf, SQL).
- **Content:** E-01 through E-11 are carried over into `decisions/` as decisions — verbatim, including
  the "overturned by" line. That is the holding G-01 calls "decisions".
- **Done when:** `decisions/` contains eleven files, each with ruling, rationale and overturn
  condition.

### AP-0.2 Build environment
- **Content:** mkosi with a pinned Fedora package snapshot (SP-E01-2). Signing key generated, the
  private part **not** on a build machine. A CI leg that builds every image **twice and compares the
  artifacts**.
- **Done when:** two build runs of the same revision produce bit-identical artifacts. (That is
  AB-A03-2, the first green row of the matrix.)

### AP-0.3 Acceptance matrix as a registry
- **Content:** every check from `03-acceptance-matrix.md` as an entry in a test registry, each with
  identifier, kind (`script` · `measurement` · `drill` · `inspection`), state `red`.
- **Done when:** `make acceptance` runs through and reports: 0 green, all red. From here on, progress
  is measurable.

---

## Stage 1 — Base and measurement *(E-11, step 1)*

> "A-06 as a script against a bare mkosi VM; the base holds, or E-01 falls."

### AP-1.1 Bare image
- **Content:** mkosi image, Fedora kernel, the kernel requirements from SP-A02-2: cgroup v2 unified
  and PSI, overlayfs, btrfs/XFS with reflink, user namespaces, seccomp, Landlock, zram with zstd,
  `CONFIG_CHECKPOINT_RESTORE`, KVM, io_uring. Userland from SP-A02-3, **without toolchains**.
- **Done when:** the image boots in a VM, `/usr` read-only under dm-verity.

### AP-1.2 A-06 as a script
- **Content:** `acceptance/a06-acceptance.sh` with the thirteen checks from A-06 (section A of the
  matrix). Checks that do not yet need a platform (cgroup/PSI, reflink, namespaces, freezer/zram,
  CRIU, inventory against SBOM, verity and fallback, no inbound ports) run in full now; the rest
  report `SKIP` naming the work package in which they turn green.
- **Done when:** eight of the thirteen checks green, five as `SKIP` with a reference. No `FAIL`.

### AP-1.3 Calibration run
- **Content:** 500 pods created, 20 active (A-06, last row). Measured are the five constants from
  E-05: host and runtime per role, page cache baseline, pages per frozen pod, zram factor, active pod
  (cores and MB). Additionally: hysteresis for the PSI thresholds (OP-6).
- **Done when:** the five measured numbers stand in `decisions/E-05.md` **next to** the given ones,
  and the R-D occupancy table computes with the measured ones.
- **Gate:** if a kernel requirement is missing and cannot be reconfigured, **the base changes, not the
  order** (E-01, overturn condition: then NixOS).

**Measurement of this stage:** the five constants. → AB-E05-1

---

## Stage 2 — The contract *(E-11, step 2)*

> "K-01, K-02, E-10 — schema, migrations, worker interface — the contract everything else is built
> against. Measures nothing, that is the point."

### AP-2.1 Protobuf schema (E-10)
- **Content:** `contract/platform.proto` — one schema for the control API, worker pull, gates, events
  and the harness socket. Rule: additive fields only, field numbers are never reassigned (SP-E10-3),
  enforced by a schema linter in CI.
- **Done when:** the linter rejects a removed or repurposed field number (test case in the
  repository).

### AP-2.2 State schema (K-01)
- **Content:** `contract/schema.sql` — tables for every object from SP-K01-1 and SP-K01-8. `cell` and
  `project` are `NOT NULL` on every table. UUID v7 from the producer. No secret columns, references
  only. A migration tool that permits additive migrations only (SP-V05-2).
- **Done when:** a migration that removes a column is rejected by the tool.

### AP-2.3 State machine as a database contract (K-02)
- **Content:** transition table with a writer column; a database trigger that rejects a transition by
  the wrong writer. `attempt` as the unit of retry. `cause` is mandatory in every terminal state. No
  backward transition. Set lease and heartbeat parameters (OP-4).
- **Done when:** the test case "worker writes `queued → leased`" fails at the database level, not in
  the application. → AB-K02-1

### AP-2.4 Authority (K-04, E-10)
- **Content:** Biscuit issuer, attenuation library, verification at three places (control API, Git
  proxy, egress proxy), revocation ID per block, revocation list distribution with static stability.
- **Done when:** the test case "a block tries to widen `targets`" is cryptographically impossible, not
  merely rejected. → AB-K04-2

### AP-2.5 Identity format (B-01, part 1)
- **Content:** naming scheme for node certificates with **role and cell in the name**. mTLS
  scaffolding. Enrollment itself comes in stage 6 — the format has to stand before that, because
  stage 3 already builds against it.
- **Done when:** the control plane can check "work node from cell B" as a statement instead of
  accepting it as a claim.

**Measurement of this stage:** none. That is the point.

---

## Stage 3 — Smallest system, without a captain *(E-11, step 3)*

> "`role = all`, one adapter (CLI), one pipeline, one runner — **without a captain**, jobs by hand."
> A system whose mechanics first run together with a model has two unknown sources of error and no way
> to separate them.

### AP-3.1 Boot `role = all`
- **Content:** four systemd slices (`control`, `captain`, `knowledge`, `work`), `memory.min` on the
  system slice, `memory.oom.group=1` on pod slices. Postgres on `/data/db`, work volume separate.
- **Done when:** a node starts along A-04 (verity → disk → role → selftest → register) and registers
  only after passing the selftest.

### AP-3.2 CLI adapter (T-01)
- **Content:** one adapter with `receive()`, `identity()`, `respond()`, `capabilities()`. Level
  `confidential` via device certificate. Envelope with an idempotency key; attachments
  content-addressed, read-only, never executable (OP-5).
- **Done when:** the same message twice produces one job, not two. → AB-T01-7

### AP-3.3 Runner and workpod (T-04)
- **Content:** containerd/runc; CoW snapshot in O(1); resource contract from R-A (`cpu.weight`,
  `memory.high`, `pids.max`, `io.latency`); **no network**, Unix socket only; inject concurrency
  variables (SP-RC-5); freeze after 45 s of quiet; reaper with lifetime and idle limit **on the
  worker**.
- **Done when:** a pod without network starts in ~200 ms on an image hit; after a simulated worker
  restart no orphaned subvolumes are left. → AB-T04-5

### AP-3.4 Pipeline (T-05)
- **Content:** fixed spine `prepare · plan · edit · check · repair · deliver · reap`, versioned as
  `pipeline@version`; the seven movable places as job fields; `n` rework rounds (OP-2), then a reply
  with diff, logs and an assessment instead of silent failure.
- **Done when:** a deliberately unsolvable job ends after `n` rounds in a named state with a cause,
  not in a loop. → AB-T05-3

### AP-3.5 Outbox and the two gates (K-03, B-02)
- **Content:** `outbox` keyed by `order + target + content_hash`; Git proxy (checks policy, signs
  itself); egress proxy on the work node with an allowlist **per job**, without DNS in the pod;
  a register for non-idempotent targets (record → execute → acknowledge, and on a missing
  acknowledgement **ask**).
- **Done when:** the same job executed twice produces **one** push. → AB-K03-2

### AP-3.6 Budgets and admission (V-04, E-08)
- **Content:** `pod_minutes`, `tokens`, `money`; reservation at admission, release at the terminal
  state; three caps (envelope, project, principal-day, OP-1); halt as a field **and** as a file on the
  control node, read at every admission decision.
- **Done when:** with the halt file set and the API switched off no further job is admitted, running
  ones run to completion. → AB-E08-3

### AP-3.7 Scheduler and pressure (R-B, R-C)
- **Content:** tokens per phase (`net`, `io`, `cpu·ram`); four priorities with aging; preemption =
  freezing at the phase boundary; exclusive operation above ~60 % RAM; PSI reader every two seconds
  with the six signals and the escalation ladder; record peak RSS per repository and phase, after
  three runs mechanical admission.
- **Done when:** under generated memory pressure the scheduler freezes the lowest priority, aborts
  nothing, and the control plane stays operable. → AB-RC-3

### AP-3.8 Observation (B-03)
- **Content:** one trace per job with the phases as spans, cost, attempt, evidence class,
  model/prompt/pipeline version. Pod logs to the node immediately, tagged with the job ID. Audit trail
  separate and immutable. **Exactly four alerts.**
- **Done when:** a completed job is reconstructible from the channel message to the patch in **one**
  query. → AB-K01-7

**Measurement of this stage:** jobs per hour on one node; share of orphaned subvolumes after a restart
(must be zero); double execution without double effect passed.
→ AB-T04-5, AB-A06-11, `acceptance/stage3-measurement.sh` (`decisions/stage-3-measurement.md`)

---

## Stage 4 — Handles and knowledge *(E-11, step 4)*

> "The ten handles first — they need no model and are checkable before any agent question."
> They are the only layer that can be proven completely before anything is guessed.

### AP-4.1 Fact store (E-09, G-01)
- **Content:** SCIP indexer for the first language; storage as Parquet, partitioned by build target,
  keyed by `file content + indexer version`; DuckDB **embedded in the harness**; exactly one writer;
  distribution to pods as a read-only CoW snapshot.
- **Done when:** `fact.query` in the pod is a file read without network, and the cell serves symbols,
  callers, types and ownership from derivation. → AB-E09-3

### AP-4.2 The thirteen handles (F-03, F-07)
- **Content:** `fact.query`, `context.assemble`, `test.select`, `build`, `check.types`, `check.tests`,
  `codemod.apply`, `artifact.compare`, `flake.detect`, `snapshot.create`, `snapshot.reap`,
  `effect.enqueue`, `gap.report` — each as a `SKILL.md` with the five parts from F-01,
  `metadata.order: handle`, `metadata.min_authority`, its own abort condition and **three cases in the
  corpus**.
- **Done when:** every handle has a measured `success_rate` and a named boundary. A handle without
  evidence does not enter the catalog. → AB-F01-1

### AP-4.3 Context assembly (G-07)
- **Content:** `anchor · hull · skeleton · body · manifest` with a depth table and a budget; activity
  state as a filter; drift marking; **gap report as a hard gate**; never truncate silently.
- **Done when:** a job without an index or without tests at the anchor does **not** start, but
  produces a preparation job. → AB-G07-6

### AP-4.4 Catalog and resolution (F-04, part 1)
- **Content:** content-addressed capability versions; resolution **deterministic outside the pod**;
  only resolved capabilities are enclosed — the pod never sees the catalog.
- **Done when:** a pod context contains no description of an unresolved capability. → AB-F07-3

### AP-4.5 Derived relations
- **Content:** Soufflé rules for the transitive call graph, reachability and points-to on candidates,
  executed as a `maintenance` job, written back as further partitions.
- **Done when:** the run stays inside the maintenance window — the number from E-09, overturn
  condition.

**Measurement of this stage:** `success_rate` per handle; response time of `fact.query`; share of jobs
whose hull blows the context budget.

---

## Stage 5 — Intent, then captain *(E-11, step 5)*

> "The intent contract **before** the model that uses it."

### AP-5.1 Intent contract (Q-01)
- **Content:** `spec` as its own versioned object; `acceptance[]` and `assumptions[]` as objects;
  reversibility matrix (`reversible` · `costly` · `irreversible`); at most one clarification per job;
  clarification deadline (OP-3); **no job without an acceptance criterion**; re-evaluation of open jobs
  on a version change.
- **Done when:** a prompt with no derivable criterion produces a preparation job instead of a job.
  → AB-Q01-6

### AP-5.2 Procedures (F-03, order `procedure`)
- **Content:** `spec.derive`, `test.repair`, `bug.fix`, `interface.change`, `function.extract`,
  `dependency.update`, `test.write`, `review.produce`, `image.build`, `adapter.build` — each with
  exactly one model step, evidence at the test, boundary named, calls downward only.
- **Done when:** the catalog checker rejects an upward call and a cycle. → AB-F02-2

### AP-5.3 Model roles (E-06)
- **Content:** `classifier` (self-operated), `worker`, `reviewer` at a **different provider**;
  resolution through a capability statement, never through a name in code; the egress proxy inserts the
  key; provider pinned per project; euro cap reserved at the most expensive permissible provider
  (OP-7).
- **Done when:** switching provider is a table row, not an intervention in prompts or code.
  → AB-E06-2

### AP-5.4 Evidence and acceptance (Q-02)
- **Content:** the seven classes as an acceptance machine; the checking pod sees only diff, criterion,
  facts; every check blocks; `unproven` as its own exit with diff, gap report and assessment.
- **Done when:** a job whose criterion cannot be evidenced ends in `unproven` and lands in the human
  stack — not in `delivered`, not in `failed`. → AB-Q02-6

### AP-5.5 Captain (T-02)
- **Content:** router **without a model** on the host (deduplicate, resolve identity, attach
  authority, determine project, wake); captain per project in the pod, stateless between turns,
  without a runtime socket, without project mounts; subagents by write authority; events back into the
  channels with a verbosity profile.
- **Done when:** a project with 200 subjobs produces a handful of replies in one thread, not 200.
  → AB-T02-9

### AP-5.6 Coverage check (F-04, complete)
- **Content:** `criteria → resolve → coverage → gap`; five failure causes distinguished; a gap becomes
  an ordinary job (F-05), never an improvisation mid-run; `coverage_rate` is tracked.
- **Done when:** an uncovered job fails **before** the pod starts, with a named cause. → AB-F04-2

### AP-5.7 Further intakes (T-01)
- **Content:** one `linked` and one `public` adapter; a linking invitation on first contact;
  confirmation **on a different channel**; a limit in pod minutes per principal and channel; gVisor for
  pods at level `public`.
- **Done when:** a text from an open channel claiming a higher authority changes nothing about the
  authority of the envelope. → AB-T01-9

**Measurement of this stage:** `no_clarification_rate`, `coverage_rate`, share of `unproven`. If
`no_clarification_rate` does not rise, the platform does not scale — and then Q-01 is the building
site, not the model.

---

## Stage 6 — Second node *(E-11, step 6)*

> "`role = work`: intake, lease, expiry, rolling update. V-02, **and only then** V-03."

### AP-6.1 Enrollment (B-01)
- **Content:** `enrollment token` (single use, minutes) → certificate with role and cell in the name →
  hourly overlapping renewal → expiry. No standing secret in the image; keys on encrypted `/var`,
  TPM-bound where possible.
- **Done when:** a node with an expired certificate receives no more jobs — without intervention.

### AP-6.2 Ferry across nodes (V-02)
- **Content:** pull, lease, heartbeat, expiry, return of the job; locality groups and sticky
  assignment repository → node; work stealing only inside the group (OP-8).
- **Done when:** a hard-powered-off worker returns its jobs after expiry, and those run to completion
  elsewhere — without a leader election. → AB-V02-1

### AP-6.3 Network boundaries sharpened (B-02)
- **Content:** nftables from the image, default deny; no inbound ports, not even SSH; an egress proxy
  per work node; rejected targets into the display.
- **Done when:** a port scan from outside finds nothing — even after a role change. → AB-B02-6

### AP-6.4 Rolling update (A-02, A-03)
- **Content:** A/B via `systemd-sysupdate`, boot counter, fallback after three failed starts, three
  channels; a node reports "healthy" only after a **job carried to completion**; no drain command.
- **Done when:** two versions run at the same time, no job is lost. → AB-A02-5

### AP-6.5 Backup and drill (V-05)
- **Content:** synchronous replica of the control data, point-in-time restore, append-only log for
  judgements, daily snapshot for facts, **no backup at all** for snapshots and caches; audit trail
  separate.
- **Done when:** a cell has been rebuilt from backup once, checked against the corpus, and the time
  measured. The drill is an ordinary job. → AB-V05-3

**Measurement of this stage:** time from expiry to resumption; job loss during an update (must be
zero); restore time.

---

## Stage 7 — Test bench *(E-11, step 7)*

> "The regression corpus **before** the first model change."

### AP-7.1 Corpus
- **Content:** recorded jobs with a known outcome; frozen facts; recorded tool responses. Every case
  that slipped through in operation is taken in.
- **Done when:** the corpus contains at least three cases per capability in the catalog (SP-F05-2).

### AP-7.2 Replay, comparison, canary
- **Content:** a repeatable run **without live calls**; the four metrics; a shadow run in operation
  whose result is discarded; prompt and model version pinnable per project; rolling back cheaper than
  repairing.
- **Done when:** two runs of the same revision produce the same metrics. → AB-Q04-2

### AP-7.3 Failure classification (Q-03)
- **Content:** every terminal state gets one of the seven classes; `escape_rate` can be broken down by
  class.
- **Done when:** the question "which kind of failure slips through" is a query, not an investigation.

### AP-7.4 Retention and deletion (E-07)
- **Content:** periods per data kind; deletion as a job with a 30-day deadline; deletion receipt in the
  audit trail; periods as a cell property (OP-9).
- **Done when:** "remove everything belonging to this project" runs as a job and is evidenced.
  → AB-V04-6

### AP-7.5 Operations role and the bad day (E-08, B-04)
- **Content:** two trained people (OP-10); `halt.set/renew/clear` with a 60-minute expiry; a two-person
  rule for everything that widens rights or money; the seven incidents from B-04 with trigger and first
  remedy; every automation with an off position and a log.
- **Done when:** a halt that was set expires by itself after 60 minutes without renewal, and the drills
  from B-04 have been run once. → AB-E08-4

**Measurement of this stage:** `escape_rate` as a baseline. **Only then** the first model change.

---

## Stage 8 — Growth *(by measurement, not by plan)*

- **AP-8.1 Second cell** — opened at **80 %** of the limit from E-04 (500 active projects or p99
  `admitted → occupied` = 2 s). First migration of a project: freeze, copy, re-derive facts, switch
  over. → AB-V03-5
- **AP-8.2 Campaigns for large jobs** — `corpus.reduce`, `module.extract`, `dependencies.untangle`,
  `framework.migrate`, `coverage.build` as catalog entries (G-02 through G-06), not as a platform
  layer.
- **AP-8.3 Further runners** — `windows`, `macos`, `remote` against the same runner contract. No
  rebuild, because `platform` has been in the envelope since stage 2.
- **AP-8.4 Catalog maintenance as routine operation** — every gap becomes a job (F-05);
  `coverage_rate` rises, or the platform only repeats itself.

---

## What goes wrong first — and in which work package it is prevented

| Most likely way this platform tips over | Prevented in |
|---|---|
| Ten thousand orphaned subvolumes after a week (T-04) | AP-3.3, reaper on the worker |
| One retry starts three pods (T-01) | AP-3.2, idempotency key |
| Two writers on `state`, double execution under load (K-02) | AP-2.3, trigger in the database |
| Double push, double email (K-03) | AP-3.5, outbox with a domain key |
| An injected text raises the authority (T-01) | AP-2.4, attenuation as cryptography |
| The scheduler spreads evenly and becomes slower than one machine (V-02) | AP-6.2, locality groups |
| Non-blocking warnings train people to look away (Q-02) | AP-5.4, every check blocks or goes |
| Catalog maintenance on Tuesday changes a thousand running projects (F-04) | AP-4.4, pinning via content hash |
| A model change silently serves old judgements (G-03) | AP-4.1, version in the cache key |
| A forgotten halt is the second incident of the day (E-08) | AP-7.5, expiry after 60 minutes |

---

## The first week, concretely

1. Create `decisions/` and carry E-01 through E-11 over verbatim — including "overturned by". (AP-0.1)
2. Transfer `03-acceptance-matrix.md` into the test registry. Everything red. (AP-0.3)
3. mkosi scaffolding with a pinned Fedora snapshot, CI builds twice and compares. (AP-0.2)
4. Run `acceptance/a06-acceptance.sh` against the first VM. The checks for cgroup v2/PSI, reflink,
   namespaces, freezer/zram and CRIU are the decision on E-01 — they come first. (AP-1.2)
5. Only once those five are green is a line of Go written.
