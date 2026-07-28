# Decision: the aging rule — how a waiting job rises, and how far

**Status:** ruled · **Date:** 2026-07-28 · **Affects:** SP-RB-2, SP-RB-3, SP-RB-6, AB-RB-2, AB-RB-3,
AP-3.7. **Panels:** R-B.

SP-RB-2 gives four priorities, each with a *waits at most*, and exactly one of them may preempt.
SP-RB-3 adds one sentence: *the longer a batch job waits, the further it rises*. "Further" is not a
number, and a queue cannot be ordered by an adverb. This is the rule that makes SP-RB-3 checkable,
and it is written so that AB-RB-3 — "a batch job does not starve behind interactive work" — is a
consequence of the ordering rather than a hope about the load.

## Ruling

Every job carries the bound of its priority, from SP-RB-2 unchanged:

| Priority | Bound | May preempt |
|---|---|---|
| `interactive` | 2 s | yes |
| `batch` | 5 min | no |
| `maintenance` | 1 h | no |
| `background` | none | no |

**A job is *overdue* when it has waited longer than its own bound.** `background` has no bound and is
therefore never overdue — SP-RB-2 writes "unbounded" in that row, and this is what unbounded means.

The queue is ordered by three keys, in this order:

1. **Overdue before not overdue.** Any job past its own bound goes before every job still inside its
   bound, whatever the two priorities are.
2. **Among overdue jobs: the largest `wait / bound` first.** A batch job that has waited 10 minutes
   (ratio 2.0) goes before an interactive job that has waited 3 seconds (ratio 1.5).
3. **Among jobs inside their bound: priority first** — `interactive`, `batch`, `maintenance`,
   `background` — **and within one priority, arrival order.** Arrival order is the tiebreak
   everywhere, including between two overdue jobs of the same ratio.

**Aging moves a job in the queue and never grants it the right to preempt.** SP-RB-2 gives "may
preempt: yes" to exactly one row, and an aged batch job is a batch job that is next, not an
interactive one. A running pod is frozen only for an `interactive` job, or by the pressure ladder
(SP-RC-3) — which is not a priority decision at all.

**SP-RB-6's "short ones first" is inside key 3, not above it.** Between two large runs neither of
which is overdue, the shorter predicted runtime goes first; aging still overrides it, which is the
"with aging as protection" of that requirement.

## Rationale

1. **The bound is already in the specification, so the rule needs no new number.** Every constant in
   this ruling is quoted from SP-RB-2's table. What was missing was a comparison, not a value — and a
   rule that introduces no constants cannot drift from the panel.
2. **`wait / bound` is the only comparison that treats the four bounds as what they are: promises of
   different sizes.** An interactive job 1 second past 2 seconds and a maintenance job 1 second past
   an hour are both overdue by a second, and they are not equally wronged. The ratio says so, and it
   says so without ranking the priorities a second time.
3. **Starvation is impossible for any bounded priority, by construction.** A batch job's ratio grows
   without limit while it waits; every interactive job that arrives starts at 0 and needs 2 seconds
   to reach 1.0. After 5 minutes the batch job is ahead of every interactive job that arrived in the
   last 2 seconds, and after 10 minutes ahead of every one that arrived in the last 4. There is no
   arrival rate of interactive work that holds a batch job back for ever, which is exactly what
   AB-RB-3 asks and what a plain priority queue cannot promise.
4. **Interactive work still wins when the machine is keeping up.** Below saturation nothing is
   overdue, key 1 is empty, and the order is the plain priority order of SP-RB-2 — an interactive job
   arriving into free slots waits for the queue and not for the aging (AB-RB-2's "≤ 2 s when slots
   are free").
5. **`background` never rising is not a bug in this rule.** SP-RB-2 says it waits unbounded, and
   R-B's list of what it is for — cleaning up, indexing, warming — is work whose whole value is that
   it yields. A background job that aged past a batch job would be a fifth priority nobody ruled.
6. **Separating "goes first" from "may preempt" keeps the expensive action rare.** Preemption costs a
   freeze and a phase boundary; reordering a queue costs nothing. Tying the two together would let
   ordinary batch load freeze running pods, and SP-RB-4's "the pod loses its slot, not its state" is
   cheap only as long as it is uncommon.

## Overturned by

A measured queue in which a bounded job's wait grows without bound under a load that is itself
bounded — that would mean key 1 is not reached, and the ordering, not the load, is the cause. Also: a
change to SP-RB-2's bounds or to its "may preempt" column, since every key above is read out of that
table.
