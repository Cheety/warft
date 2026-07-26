# OP-4 — Lease duration and heartbeat interval

**Status:** ruled · **Due before:** AP-2.3 — ruled in time, 2026-07-26, in AP-2.3 · **Panels:**
V-02, K-02 · **Source:** §19 of `01-specification.md`.

§19 left this value open and proposed a number. This ruling adopts that proposal unchanged: nothing
in stage 2 can measure a lease — the platform that grants one arrives with AP-6.2 — and V-05 does
not allow the value to wait in application code until then. So the proposal becomes the ruling, and
the measurement that could move it becomes the overturn condition.

## To be decided

Lease duration and heartbeat interval

## Proposed ruling

lease 60 s, heartbeat 15 s, three failures = release

## Ruling

This file adopts the §19 proposal as it stands:

| Parameter | Value |
|---|---|
| `lease_duration_seconds` | 60 — the deadline a job is handed out with; every heartbeat extends it back to the full window |
| `heartbeat_interval_seconds` | 15 — how often the worker extends its lease |
| `failures_to_release` | 3 — missed heartbeats in a row before the control plane writes `leased → queued` |

`contract/schema.sql` seeds these three rows into `lease_parameter`. That table is the
machine-readable half of this file: the control plane reads the values from it instead of carrying
its own, and `acceptance/k02-state.sh` holds the seed and this table against each other on every
run — a number here that is not there is drift.

The two arms end a lease differently on purpose. Three missed heartbeats are 45 s of silence and
fire first: the control plane notices a silent worker while the lease still stands, takes the job
back with `leased → queued` — K-02's own row for exactly this — and owes nobody an election. The
60 s deadline is the backstop for the case where the control plane itself was away (SP-V02-3): a
lease that could not be extended ends on its own, wherever its worker is.

## Rationale

1. **Nothing here can be measured yet, and the value cannot wait.** AP-6.2 grants the first real
   lease; stage 2 only writes the contract about who may write. A lease duration living in
   application code until then would be exactly the guess-in-code V-05 forbids. Adopting the
   architecture document's own proposal is the ruling with the least invented content, and the
   overturn condition below is where the measurement re-enters.
2. **60 s bounds the damage of a dead worker without punishing a live one.** In SP-V02-1's pull
   model the lease deadline is the only failure detector there is — no leader, no election, nothing
   watches a worker except its own heartbeats. Sixty seconds means a job whose worker died sits in
   `leased` for at most a minute before it returns to the queue, which even an `interactive` job
   survives. And under a control-plane restart — SP-V05-2's rolling update runs two versions at
   once — 60 s is four full heartbeat windows of grace before any lease ends for a reason that was
   never the worker's.
3. **15 s keeps the heartbeat cheap where E-02 put the queue.** The queue lives in Postgres, and
   E-02's own arithmetic holds that up to roughly 5,000 writes per second. At one heartbeat per
   15 s, a cell holding a thousand concurrent leases adds about 67 writes per second — noise
   against that ceiling, so heartbeats never become the reason the queue needs the broker E-02
   ruled out.
4. **Three failures, because a false release is the expensive direction — and the trigger has
   already paid for patience.** One missed heartbeat is a lost packet, a stalled connection or a
   stop-the-world pause; releasing on it would re-queue jobs whose pods are alive and well. Double
   execution is not the risk — SP-K02-1's trigger fences the stale worker, whose `running → …`
   report finds a state that is no longer `running` and fails in the database — but every false
   release still burns an attempt and a pod start for nothing. Waiting 45 s costs only latency on
   genuinely dead workers, and the ceiling on that latency is what point 2 already set.

## Overturned by

A measurement, from AP-6.2's lease grant or from operation, showing that these numbers thrash or
starve. Thrash: jobs re-queued by `failures_to_release` whose released worker heartbeats again
within one further interval — that asks for a higher count or a longer interval. Starvation: dead
workers' jobs waiting the full release window often enough to breach an `interactive` deadline —
that asks for a shorter one. Load: heartbeat writes appearing as a measurable share of the state
database, which moves E-02's arithmetic — that asks for a longer interval. The re-measurement
belongs to AP-6.2, the first work package that can produce any of these numbers.
