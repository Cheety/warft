# Acceptance matrix — the reverse side of the architecture document

This list is the instrument on which "hit 100 %" is decided. It contains **every** one of the 57 panels
from the architecture overview, each with the check that turns red if the panel was not built.

**Rule (A-06):** done is not when it runs. Done is when this list is green.
**Rule (Q-02):** confidence is not an acceptance criterion, evidence is. A row does not turn green
through an explanation, but through a run.
**Rule (Q-02):** every check blocks, or it is removed. There are no warnings in this list.

**Kinds:** `S` script (automatic, in CI) · `M` measurement (a number, recorded) ·
`D` drill (a human performs it once, logged) · `P` probe (negative test: the forbidden action must
fail).

---

## Section A — Acceptance of the image (A-06, verbatim)

These thirteen rows come unchanged from the architecture document. They are both the build plan and the
acceptance of the image, and they ship as `acceptance/a06-acceptance.sh`.

| ID | Check | Evidence | Otherwise lost | Kind | AP |
|---|---|---|---|---|---|
| AB-A06-1 | cgroup v2 unified, PSI readable | values from `cpu.pressure` in the pods slice | R-A, R-C | S | AP-1.2 |
| AB-A06-2 | reflink snapshot in O(1) | 1 GB copied in milliseconds, disk does not grow | T-04, G-03 | S | AP-1.2 |
| AB-A06-3 | user namespaces, seccomp, Landlock | test pod without rights, escape attempt fails | T-04 | P | AP-1.2 |
| AB-A06-4 | freezer and zram with zstd | pod frozen, factor **measured** instead of assumed | R-C, R-D | M | AP-1.3 |
| AB-A06-5 | CRIU: dump and restore | sample pod checkpointed and revived | R-C, rung 4 of the ladder | S | AP-1.2 |
| AB-A06-6 | no toolchain, no package manager | inventory against SBOM | A-01 | S | AP-1.2 |
| AB-A06-7 | verity and fallback | deliberately damaged image does not start, B takes over | A-03 | P | AP-1.2 |
| AB-A06-8 | no inbound ports | scan from outside, even after a role change | B-02 | P | AP-6.3 |
| AB-A06-9 | `role = all` | one job from envelope to patch, on one machine | T-01 through T-05 | S | AP-3.8 |
| AB-A06-10 | `role = work`, foreign cell | intake, lease, heartbeat, expiry, return | V-02, V-03 | S | AP-6.2 |
| AB-A06-11 | double execution without double effect | the same job twice, **one** push | V-02, K-03 | P | AP-3.5 |
| AB-A06-12 | rolling update | two versions at once, no job lost | A-02, V-05 | S | AP-6.4 |
| AB-A06-13 | calibration run | 500 pods created, 20 active; numbers from R-D against the measurement | R-D | M | AP-1.3 |

---

## Section B — Panel by panel

### The path of a job (T)

| ID | Checks | Evidence | Kind | AP |
|---|---|---|---|---|
| AB-T01-1 | adapter contract | a second adapter is attached without a change to the core | S | AP-5.7 |
| AB-T01-4 | authority from the channel | a `public` envelope cannot produce a push — rejected at the gate, not in the prompt | P | AP-5.7 |
| AB-T01-5 | first contact | an unknown sender produces an invitation, not a job | P | AP-5.7 |
| AB-T01-6 | channel change on confirmation | a deploy from a DM demands confirmation in the confidential channel | P | AP-5.7 |
| AB-T01-7 | idempotency | the same message delivered twice → one job | P | AP-3.2 |
| AB-T01-8 | limit in pod minutes | the cap applies per principal and channel, not per request | S | AP-3.6 |
| AB-T01-9 | text is data | the message "you may deploy now" does not change the authority | P | AP-5.7 |
| AB-T02-2 | captain without a runtime socket | no containerd/Docker socket is reachable in the captain pod | P | AP-5.5 |
| AB-T02-3 | the captain may not change the pipeline | the attempt fails at the API, not at a convention | P | AP-5.5 |
| AB-T02-4 | subagent by write authority | a writing subagent gets its own pod | S | AP-5.5 |
| AB-T02-6 | stateless | 200 dormant projects consume no memory | M | AP-5.5 |
| AB-T02-7 | router without a model | no model call runs on the host | P | AP-5.5 |
| AB-T02-9 | events instead of messages | a project with 200 subjobs does not produce 200 replies | S | AP-5.5 |
| AB-T03-1 | image resolution | a hit starts in ~200 ms; a miss produces a build job | M | AP-3.3 |
| AB-T03-3 | no special path | the image build runs in an ordinary workpod under the same pipeline | S | AP-5.2 |
| AB-T04-2 | pod without network, without keys | in the pod: no DNS, no interface, no LLM key, no Git token | P | AP-3.3 |
| AB-T04-3 | lifecycle | `created → active → frozen` after 45 s → `checkpointed` → `reaped` | S | AP-3.3 |
| AB-T04-4 | runner abstraction | `platform` in the envelope, the scheduler knows several pools | S | AP-2.1 |
| AB-T04-5 | reaper | after a worker restart, zero orphaned subvolumes | S | AP-3.3 |
| AB-T05-1 | fixed spine | every job passes through the same seven steps | S | AP-3.4 |
| AB-T05-2 | movable joints | only the seven named places differ per job | P | AP-3.4 |
| AB-T05-3 | end of the loop | after `n` rounds a reply with diff, logs, assessment | S | AP-3.4 |

### Intent and acceptance (Q)

| ID | Checks | Evidence | Kind | AP |
|---|---|---|---|---|
| AB-Q01-1 | specification as an object | `spec` is versioned, citable, exportable | S | AP-5.1 |
| AB-Q01-2 | version change | a change of intent re-evaluates open jobs instead of letting them run on | P | AP-5.1 |
| AB-Q01-3 | reversibility before confidence | `irreversible` + ambiguous never runs without a human | P | AP-5.1 |
| AB-Q01-4 | at most one clarification | a second clarification in the same job is rejected | P | AP-5.1 |
| AB-Q01-5 | assumptions as objects | revoking an assumption invalidates exactly the dependent jobs | S | AP-5.1 |
| AB-Q01-6 | hard gate | no acceptance criterion → preparation job, not a job | P | AP-5.1 |
| AB-Q02-1 | evidence classes | every acceptance names its class; the class follows from the risk | S | AP-5.4 |
| AB-Q02-2 | checker without contagion | the checking pod receives diff, criterion, facts — not the author's transcript | P | AP-5.4 |
| AB-Q02-3 | self-report does not count | a claim without evidence is marked as an assumption | S | AP-5.4 |
| AB-Q02-4 | every check blocks | no non-blocking warning exists | P | AP-5.4 |
| AB-Q02-6 | `unproven` as its own exit | a result that cannot be evidenced ends neither in `delivered` nor `failed` | P | AP-5.4 |
| AB-Q03-1 | seven kinds of failure | every terminal state carries a cause key from the set | S | AP-7.3 |
| AB-Q03-2 | `spec.wrong` | the evaluation shows the class separately; no technical net is spent on it | M | AP-7.3 |
| AB-Q04-1 | prompt and model are releases | a change passes through `corpus → replay → compare → canary` | S | AP-7.2 |
| AB-Q04-2 | repeatability | two runs of the same revision, the same metrics, no live calls | S | AP-7.2 |
| AB-Q04-4 | versions in the log | model, prompt and pipeline version stand on the job, not only in the cache | S | AP-3.8 |
| AB-Q04-5 | the corpus grows out of operation | every escape is taken in as a case | D | AP-7.1 |
| AB-Q04-7 | rolling back | a project can be pinned to the previous model and prompt version | S | AP-7.2 |

### Capabilities (F)

| ID | Checks | Evidence | Kind | AP |
|---|---|---|---|---|
| AB-F01-1 | five parts | the catalog checker rejects a capability without `evidence` or without `boundary` | P | AP-4.2 |
| AB-F01-2 | minimum authority | `public` cannot resolve a writing capability | P | AP-4.4 |
| AB-F01-4 | abort condition per capability | a handle that fails twice counts as broken | S | AP-4.2 |
| AB-F02-2 | call downward only | an upward call and a cycle are rejected | P | AP-5.2 |
| AB-F02-3 | a campaign changes nothing itself | a `campaign` produces subjobs, not a diff | P | AP-8.2 |
| AB-F03-1 | starting stock | thirteen handles, ten procedures, six campaigns in the catalog | S | AP-4.2, AP-5.2 |
| AB-F04-2 | coverage as a hard gate | an uncovered job fails before the pod starts | P | AP-5.6 |
| AB-F04-3 | five failure causes | the cause stands on the terminal state, `goal.wrong` does not lead to more capability | S | AP-5.6 |
| AB-F04-4 | metrics | `coverage_rate` and `success_rate` are tracked per capability | M | AP-4.2 |
| AB-F04-5 | pinning | a catalog change changes no running project | P | AP-4.4 |
| AB-F05-1 | a gap is a job | a missing handle is built, never improvised mid-run | P | AP-5.6 |
| AB-F05-2 | intake condition | a new capability without three corpus cases is rejected | P | AP-7.1 |
| AB-F05-3 | a sign at the end of the bridge | an abort delivers a gap report and a split proposal | S | AP-4.2 |
| AB-F06-1 | contribution of the harness | comparability, evidence, knowledge of the unseen, once-only effect, boundary, `unproven` | S | AP-5.4 |
| AB-F07-1 | format | a capability is a `SKILL.md` per the open standard, extra parts under `metadata:` | S | AP-4.2 |
| AB-F07-3 | the pod never sees the catalog | no unresolved description stands in the pod context | P | AP-4.4 |
| AB-F07-4 | portable | the same file runs unchanged in a foreign standard runtime | D | AP-4.2 |

### Rule set (R)

| ID | Checks | Evidence | Kind | AP |
|---|---|---|---|---|
| AB-RA-1 | four classes | allocation sets request and limit per the table | S | AP-3.3 |
| AB-RA-2 | throttle instead of shoot | a pod above `memory.high` is throttled, not killed | P | AP-3.3 |
| AB-RA-4 | fork protection | a pod with a fork loop does not paralyze the machine (`pids.max`, `io.latency`) | P | AP-3.3 |
| AB-RB-1 | token per phase | a waiting pod holds no CPU token | S | AP-3.7 |
| AB-RB-2 | four priorities | `interactive` waits ≤ 2 s when slots are free | M | AP-3.7 |
| AB-RB-3 | aging | a batch job does not starve behind interactive work | M | AP-3.7 |
| AB-RB-4 | preempt = freeze | a preempted pod loses the slot, not the state | P | AP-3.7 |
| AB-RB-5 | exclusive operation | a job above ~60 % RAM holds all CPU tokens | S | AP-3.7 |
| AB-RC-1 | pressure instead of utilization | admission decides from PSI, not from utilization | P | AP-3.7 |
| AB-RC-3 | escalation ladder | five rungs run in order, without an abort | S | AP-3.7 |
| AB-RC-4 | the control plane survives pressure | `memory.min` keeps control plane, proxies, access operable | P | AP-3.1 |
| AB-RC-5 | concurrency from the allocation | a pod with one core does not start four workers | P | AP-3.3 |
| AB-RC-6 | prediction | after three runs admission decides mechanically | M | AP-3.7 |
| AB-RD-2 | the table measures | in operation, real PSI values stand in the same places | S | AP-3.8 |
| AB-RD-3 | planning values | admission does not read the five constants | P | AP-3.7 |

### Contracts (K)

| ID | Checks | Evidence | Kind | AP |
|---|---|---|---|---|
| AB-K01-1 | three objects, three lifetimes | fields complete per the table | S | AP-2.2 |
| AB-K01-2 | UUID v7 from the producer | no central counter in the schema | P | AP-2.2 |
| AB-K01-3 | cell identifier everywhere | `cell` is `NOT NULL` on every table | S | AP-2.2 |
| AB-K01-4 | project reference everywhere | `project` is `NOT NULL` on every table | S | AP-2.2 |
| AB-K01-5 | no secrets | the schema checker finds no secret column | P | AP-2.2 |
| AB-K01-6 | attachments | type and size check at intake, read-only, never executable | P | AP-3.2 |
| AB-K01-7 | provenance chain | patch → job → specification version → envelope → channel message in **one** query | S | AP-3.8 |
| AB-K02-1 | one field, one writer | a worker's write attempt on `queued → leased` fails in the database | P | AP-2.3 |
| AB-K02-2 | attempt as the unit | a retry produces a new `attempt`, not a new job | S | AP-2.3 |
| AB-K02-3 | no terminal state without a cause | `failed` without `cause` is rejected | P | AP-2.3 |
| AB-K02-4 | clarification deadline | without an answer the job goes back to the captain, the assumption logged | S | AP-5.1 |
| AB-K02-5 | no backward transitions | a transition out of a terminal state is rejected | P | AP-2.3 |
| AB-K02-7 | quarantine | a job that crashed twice goes into quarantine, not into a third attempt | S | AP-7.5 |
| AB-K03-1 | outbox | the pod produces intent, the gate executes | P | AP-3.5 |
| AB-K03-2 | domain key | two attempts, one push | P | AP-3.5 |
| AB-K03-3 | two gates, nothing else | a bypass attempt fails because the pod has no network | P | AP-3.5 |
| AB-K03-4 | register | a missing acknowledgement leads to asking, never to a retry | P | AP-3.5 |
| AB-K03-5 | the adapter deduplicates | a control restart produces no second message | P | AP-3.5 |
| AB-K04-1 | token instead of a field | authority is signed and verifiable offline | S | AP-2.4 |
| AB-K04-2 | attenuation only | a widening attempt is cryptographically impossible | P | AP-2.4 |
| AB-K04-3 | verification at the effect | all three gates verify fully, none trusts the origin | P | AP-2.4 |
| AB-K04-5 | short validity | a deregistered worker loses its rights by itself | S | AP-6.1 |
| AB-K04-6 | revocation | the revocation list takes effect per project, even with the control plane down | P | AP-2.4 |
| AB-K04-7 | clock | a node with a wrong clock does not enroll | P | AP-1.2 |

### Distribution (V)

| ID | Checks | Evidence | Kind | AP |
|---|---|---|---|---|
| AB-V01-1 | four layers | four slices on one machine, four roles in the cluster — the same software | S | AP-3.1 |
| AB-V01-2 | the control layer holds only what is expensive | losing the work data costs warm-up time, not the platform | D | AP-6.5 |
| AB-V01-3 | the captain is a client | no captain process on the control island | P | AP-5.5 |
| AB-V01-4 | reservation beats priority | under sustained load admission stays fast (no death spiral) | M | AP-3.7 |
| AB-V02-1 | pull with a lease | expiry returns the job, without a leader election | S | AP-6.2 |
| AB-V02-2 | no open port | the worker accepts no inbound connection | P | AP-6.3 |
| AB-V02-3 | static stability | control plane down: running pods run, adapters buffer, the allowlist holds, new authorities refused | D | AP-6.5 |
| AB-V02-4 | locality | a repository stays sticky to the node, work stealing only inside the group | M | AP-6.2 |
| AB-V02-5 | reaper on the worker | twenty minutes of control-plane outage fill no disks | D | AP-6.5 |
| AB-V03-1 | globally identifiers only | no content lies in the global layer | P | AP-8.1 |
| AB-V03-2 | a project wholly in one cell | no hot loop crosses the cell boundary | P | AP-8.1 |
| AB-V03-5 | cell boundary measured | 500 projects or p99 = 2 s; a new cell at 80 % | M | AP-8.1 |
| AB-V03-6 | migration | project frozen, copied, facts re-derived, switched over | D | AP-8.1 |
| AB-V04-1 | three budgets | reservation at admission, release at the terminal state | S | AP-3.6 |
| AB-V04-2 | exhaustion | running out of tokens produces a reply with options, not a silent truncation | P | AP-3.6 |
| AB-V04-4 | fairness at the bottleneck | a heavy sender gets a lot, not everything | M | AP-3.6 |
| AB-V04-6 | deletion as a job | "everything belonging to this project" runs and is evidenced | S | AP-7.4 |
| AB-V05-1 | backup by origin | snapshots and caches are not backed up; decisions lie in Git | P | AP-6.5 |
| AB-V05-2 | additive migration | a removing migration is rejected | P | AP-2.2 |
| AB-V05-3 | practiced restore | a cell built once from backup, checked against the corpus, time measured | D | AP-6.5 |

### Operations (B)

| ID | Checks | Evidence | Kind | AP |
|---|---|---|---|---|
| AB-B01-1 | enrollment chain | token → certificate → hourly renewal → expiry | S | AP-6.1 |
| AB-B01-2 | no secret in the image | the image contains no standing key | P | AP-1.2 |
| AB-B01-3 | role and cell in the name | the control plane checks "work node from cell B" as a statement | S | AP-2.5 |
| AB-B01-4 | keys only in the gates | on work nodes and in pods no model or Git key is found | P | AP-3.5 |
| AB-B01-5 | rotation as routine | certificates hourly, gate secrets weekly, overlapping | S | AP-6.1 |
| AB-B02-1 | four boundaries | pod, node, gates, cell behave per the table | P | AP-6.3 |
| AB-B02-2 | egress proxy on the work node | no central throughput bottleneck | S | AP-3.5 |
| AB-B02-3 | no name resolution in the pod | a DNS query in the pod fails | P | AP-3.3 |
| AB-B02-4 | allowlist per job | target, method, size limit derived from the authority | S | AP-3.5 |
| AB-B02-5 | rejected targets in the display | a cluster of them is visible, not merely logged | S | AP-3.8 |
| AB-B02-6 | no open port | a scan from outside finds nothing, even after a role change | P | AP-6.3 |
| AB-B02-7 | rules from the image | a service cannot change the firewall at runtime | P | AP-6.3 |
| AB-B03-1 | one trace per job | phases as spans, with cost, attempt, evidence class, versions | S | AP-3.8 |
| AB-B03-3 | four alerts | no fifth waking alert exists | P | AP-3.8 |
| AB-B03-4 | logs as evidence | pod logs lie on the job, not in the pod | S | AP-3.8 |
| AB-B03-5 | audit trail separate | immutable, its own period per tenant | P | AP-7.4 |
| AB-B04-1 | seven incidents | first remedy and trigger are implemented, not documented | S | AP-7.5 |
| AB-B04-2 | halt instead of restart | in an incident no process is shot | P | AP-7.5 |
| AB-B04-3 | rejection at the intake | overload is rejected at the intake, `public` first | S | AP-7.5 |
| AB-B04-4 | off position | every automation can be switched off and is logged | P | AP-7.5 |
| AB-B04-5 | practiced instead of described | cell outage, control restart, restore run once | D | AP-7.5 |

### Large jobs (G)

| ID | Checks | Evidence | Kind | AP |
|---|---|---|---|---|
| AB-G01-2 | facts derived | no fact comes from a model output | P | AP-4.1 |
| AB-G01-3 | judgements cite facts | the content hash changes → the judgement expires automatically | P | AP-4.1 |
| AB-G01-4 | append-only | new judgements replace old ones explicitly; contradictions stay marked | S | AP-4.1 |
| AB-G01-5 | decisions in Git | the module contract is machine-checkable and survives losing the database | S | AP-0.1 |
| AB-G02-1 | equivalence classes | reduction stages write classes, not deletions | P | AP-8.2 |
| AB-G02-2 | reachability lies | removal only in the intersection with dynamic evidence | P | AP-8.2 |
| AB-G02-3 | separate waves | shrinking and restructuring never run together | P | AP-8.2 |
| AB-G03-1 | five keys | every cache key contains the version of what produced it | S | AP-4.1 |
| AB-G03-2 | cache daemon | pods read, only the daemon writes | P | AP-4.1 |
| AB-G04-1 | waves with a barrier | reverse topological, disjoint, at the end build/test/land | S | AP-8.2 |
| AB-G04-2 | whether instead of how | reference changes run through deterministic codemods | M | AP-8.2 |
| AB-G04-3 | analysis budget | the captain sees aggregates, never the corpus | P | AP-8.2 |
| AB-G05-1 | state on the fact | `active · dormant · silenced · fossil` expires with the fact | S | AP-4.1 |
| AB-G05-2 | `dormant` stays | no removal of error paths and recovery | P | AP-8.2 |
| AB-G06-1 | bit-identical artifact | `artifact.identical` is decided mechanically | S | AP-4.2 |
| AB-G06-2 | cost visibility | build time, artifact size, attack surface, analysis cost attributed per module | M | AP-8.2 |
| AB-G07-1 | five stages | assembly is deterministic, with a budget and an abort | S | AP-4.3 |
| AB-G07-3 | the map is never trimmed | the context is filtered, never the knowledge layer | P | AP-4.3 |
| AB-G07-6 | gap report as a gate | missing index or missing tests → preparation job instead of a start | P | AP-4.3 |
| AB-G07-7 | never truncate silently | an over-broad job reports back with a split proposal | P | AP-4.3 |
| AB-G07-9 | write-back path | the pod delivers judgement candidates with a citation; the next one reads them as a query | S | AP-4.3 |

### Delivery (A)

| ID | Checks | Evidence | Kind | AP |
|---|---|---|---|---|
| AB-A01-1 | no package manager, no toolchain | inventory against SBOM (= AB-A06-6) | S | AP-1.2 |
| AB-A02-1 | four roles, one artifact | the role activates units, does not change the content | P | AP-1.1 |
| AB-A02-5 | update without drain | the worker runs leases to completion, restarts, enrolls | S | AP-6.4 |
| AB-A03-2 | built twice, bit-identical | artifact comparison of two runs | S | AP-0.2 |
| AB-A03-3 | verity | a damaged image does not start (= AB-A06-7) | P | AP-1.2 |
| AB-A03-4 | healthy only after a job | a node does not report health when systemd is finished | P | AP-6.4 |
| AB-A03-6 | channels | `canary · stable · held`; a node knows which one it is in | S | AP-6.4 |
| AB-A03-7 | SBOM and signature | present and verified per image | S | AP-0.2 |
| AB-A04-1 | five quantities | the first start needs nothing further | P | AP-3.1 |
| AB-A04-3 | selftest before enrollment | a failed selftest → no enrollment | P | AP-3.1 |
| AB-A04-4 | nothing is reconfigured afterwards | no configuration path at runtime, no SSH | P | AP-6.3 |
| AB-A05-1 | disk layout | only `/data/db` survives a reinstall | P | AP-3.1 |
| AB-A05-2 | reflink | O(1) snapshot (= AB-A06-2) | S | AP-1.2 |
| AB-A05-4 | TPM instead of a passphrase | the node starts without a human | P | AP-6.1 |
| AB-A05-5 | disk alert | the disk is the first consumable with an alert | S | AP-3.8 |
| AB-A06-* | acceptance list | section A of this matrix, thirteen rows | S/M/P | AP-1.2 ff. |

### Rulings (E)

| ID | Checks | Evidence | Kind | AP |
|---|---|---|---|---|
| AB-E01-1 | mkosi on Fedora | image built, the kernel configuration is a file in the repository | S | AP-1.1 |
| AB-E01-2 | pinning | package snapshot held fixed, a rebuild is bit-identical | S | AP-0.2 |
| AB-E02-1 | one Go binary | control plane, scheduler, worker, adapter, both gates, harness from one artifact | S | AP-3.1 |
| AB-E02-2 | Postgres alone | no second broker, queue with `SKIP LOCKED` | P | AP-3.7 |
| AB-E02-3 | gVisor at the authority | `public` runs under `runsc`, otherwise `runc` | P | AP-5.7 |
| AB-E02-4 | harness read-only in the pod | a harness update is an image update, not a container rebuild | S | AP-3.3 |
| AB-E02-5 | control layer 4 cores / 16 GB | measured under the load of a full cell | M | AP-3.7 |
| AB-E03-1 | cell cut | the tenant cuts the cell, the repository family cuts the locality group | P | AP-8.1 |
| AB-E04-1 | cell size | 500 projects or p99 2 s; opening at 80 % (= AB-V03-5) | M | AP-8.1 |
| AB-E05-1 | five constants | the calibration run replaces the given numbers with measured ones | M | AP-1.3 |
| AB-E05-2 | planning values | the runtime does not read them (= AB-RD-3) | P | AP-3.7 |
| AB-E06-2 | models via capability | switching provider is a table row | S | AP-5.3 |
| AB-E06-4 | checker at a different provider | `reviewer` never runs at the provider of the `worker` | P | AP-5.4 |
| AB-E06-5 | budget in tokens | the euro cap is reserved at the most expensive permissible provider | S | AP-5.3 |
| AB-E07-1 | globally identifiers only | no content leaves the region (= AB-V03-1) | P | AP-7.4 |
| AB-E07-2 | periods | six data kinds with a default period, enforced | S | AP-7.4 |
| AB-E07-3 | deletion receipt | the receipt stays in the audit trail, not the content | P | AP-7.4 |
| AB-E08-1 | two people | widening actions demand two, a halt demands one | P | AP-7.5 |
| AB-E08-3 | second path | the halt takes effect through the file, even when the API is up | P | AP-3.6 |
| AB-E08-4 | expiry after 60 minutes | a halt not renewed expires by itself | S | AP-7.5 |
| AB-E09-3 | fact query without network | `fact.query` is a file read in the pod | P | AP-4.1 |
| AB-E09-5 | one writer | pods cannot write to the fact store | P | AP-4.1 |
| AB-E10-1 | one interface schema | adapter through gate speak the same Protobuf | S | AP-2.1 |
| AB-E10-3 | additive fields only | a removed or reassigned field number is rejected | P | AP-2.1 |
| AB-E10-5 | one source for renderings | export and audit trail render from the same definitions | S | AP-2.1 |
| AB-E11-1 | order kept | every stage delivered its number before the next one began | D | all |

---

## Section C — The six numbers

These numbers are not passed, they are tracked. They have no target value in the draft — they have a
direction and a baseline, recorded at commissioning.

| Number | Source | Direction | Without it |
|---|---|---|---|
| `escape_rate` | Q-04 | falls | "very high quality" is a claim |
| `false_reject_rate` | Q-04 | falls, without `escape_rate` rising | the gates drive the cost |
| `cost_per_acceptance` | Q-04 | falls | model routing is a matter of belief |
| `no_clarification_rate` | Q-04 | rises | the platform does not scale |
| `coverage_rate` | F-04 | rises | the platform only repeats itself |
| `success_rate` per capability | F-04 | rises per entry | nobody knows **which** capability is weak |

---

## Section D — What "hit 100 %" means

The platform is hit when **all three** conditions hold:

1. **Every row in sections A and B is green** — through a run, not through an explanation. A row that
   cannot turn green is not left out, but justified as a decision in Git (V-05) and marked in the
   matrix as deliberately open.
2. **The six numbers from section C have a recorded baseline** and a direction against which every
   further change is measured — including the changes to this build order itself.
3. **Every open point from §19 of the specification is decided** and lies as a file in `decisions/`,
   with ruling, rationale and overturn condition.

Everything else is a proposal. That is the same rule Q-02 imposes on every job, applied to the system
that runs the jobs.
