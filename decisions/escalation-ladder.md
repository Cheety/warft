# Decision: what each rung of the escalation ladder actually does

**Status:** ruled · **Date:** 2026-07-28 · **Affects:** SP-RC-3, SP-RA-3, SP-T04-3, SP-B03-5,
AB-RC-3, AB-RB-4, AP-3.7. **Panels:** R-C, R-B, T-04, B-03.

SP-RC-3 names five rungs — `throttle` → `block` → `freeze` → `checkpoint` → `escalate` — and no
panel says who performs them or with which mechanism. AB-RC-3 asks for the five to *run in order,
without an abort*, and a rung nobody can perform cannot run. This ruling says, for each of the five,
what happens and where.

## Ruling

| Rung | What runs | Where |
|---|---|---|
| `throttle` | `cpu.weight` of the pods slice is lowered, and under `io full avg10 > 20 %` the I/O token pool is cut to 1 | the scheduler, on the node — a cgroup write and a pool size |
| `block` | the token pool grants nothing new and the queue round claims nothing | the scheduler, in process |
| `freeze` | the lowest-priority running pod's cgroup is frozen with `cgroup.freeze`, and its order goes `running -> frozen` | the scheduler, on the node and in the state contract |
| `checkpoint` | the frozen pod is dumped to disk by SP-T04-3's own chain, which the runner already owns; the scheduler marks the pod as due and records the demand | the worker's supervisor (`internal/workpod`), requested by the scheduler |
| `escalate` | an entry in the audit trail naming the cell, the rung and the signals that demanded it, and a line on the console | the scheduler; the four waking alerts of B-03 are AP-3.8's |

**The ladder is climbed one rung at a time, in this order, and a signal demanding the hardest rung
runs the four below it on the way up.** `pgmajfault` rising fast demands `escalate` immediately
(SP-RC-2), and "immediately" moves the target, not the order.

**No rung aborts anything.** There are five values and none of them is a kill. A pod at the top of
the ladder is frozen and dumped; it is not lost, and there is no path in the ladder that ends a job.
That is SP-RB-4 one level up: what preemption does to a pod, pressure does to the node.

**`freeze` uses `cgroup.freeze`, not a signal and not runc.** The kernel's freezer stops every
process in the pod's cgroup with its memory, its open files and its place in the phase intact, and
`cgroup.events` reports `frozen 1` when they have actually stopped. A signal would have to be
handled by the process to be safe, and a pod runs somebody else's build.

**`throttle` lowers `cpu.weight` and never sets `cpu.max`.** SP-RA-3 rules that for a pod's own
contract — fairness through weights, not through hard ceilings — and the reason holds a level up: a
ceiling leaves cores idle while work waits, and a weight does not.

## Rationale

1. **Four of the five rungs are mechanisms the platform already has, and naming them is what was
   missing.** `cpu.weight`, `cgroup.freeze` and the CRIU dump of SP-T04-3 are all built; the ladder
   is an order over them rather than new machinery.
2. **The rung that is not the scheduler's is the one that owns a container.** Dumping a pod needs the
   bundle, the runc state and the work path — all of which live in the supervisor that created the
   pod (`internal/workpod`, and a role may not reach into another role). The scheduler freezes,
   because freezing is a cgroup write and the cgroup is a fact about the node; it requests the dump,
   because the dump is a fact about a container.
3. **Escalation goes to the audit trail before it goes to a person.** B-03 rules exactly four waking
   alerts and AP-3.8 builds them; an alert invented here would be a fifth, and R-C's own argument is
   that a fifth alert devalues the four. What the ladder owes the duty officer today is a record that
   says which signals demanded the rung, and that record is what the alert will read.
4. **Freezing the *lowest priority first* is SP-RC-3's own wording**, and it uses the same ordering as
   the queue (decisions/aging.md) read backwards — so the pod that is frozen is the one a fresh queue
   would have served last.

## Overturned by

A rung that turns out to need a mechanism the node cannot perform without a kill — that would be a
sixth rung, and it would have to be ruled rather than improvised. Also: AP-3.8 building B-03's four
alerts, at which point `escalate` writes one of them instead of only the trail, and this table's last
row changes in that work package's commit.
