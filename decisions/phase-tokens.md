# Decision: which token a phase holds, and how many there are of each

**Status:** ruled · **Date:** 2026-07-28 · **Affects:** SP-RB-1, SP-RC-5, SP-RC-2, AB-RB-1, AB-RB-5,
AP-3.7. **Panels:** R-B, R-C, T-05.

SP-RB-1 names three token classes and the kind of work each is for: `net` (planning, reworking —
many slots), `io` (preparing, clearing up — few), `cpu·ram` (building, checking — the bottleneck).
T-05 names seven phases (SP-T05-1). The panel does not join the two tables, and a scheduler cannot
run without that join: every phase a pod enters must ask for exactly one token, or "a pod holds only
the token of its current phase" is not a rule but a wish.

This is that join, and the sizes of the three pools with it.

## Ruling

### The mapping

Each of T-05's seven phases holds exactly one token, for its whole duration:

| Phase | Token | Why this one |
|---|---|---|
| `prepare` | `io` | the snapshot, the image, the working copy — SP-RB-1's "preparing" |
| `plan` | `net` | SP-RB-1's "planning", verbatim |
| `edit` | `net` | the model writes the patch; the pod is waiting on a response, not computing |
| `check` | `cpu·ram` | SP-RB-1's "building, checking", verbatim — the bottleneck |
| `repair` | `net` | SP-RB-1's "reworking", verbatim |
| `deliver` | `io` | patch out, effect enqueued — bytes moving, nothing computing |
| `reap` | `io` | SP-RB-1's "clearing up", verbatim |

The machine-readable half is `platform/internal/scheduling/phase-tokens.tsv`, and
`acceptance/rb-scheduler.sh` holds the two against each other: a row here that is not there is
drift, whichever side it is on.

Five of the seven rows are the panel's own words. The two the panel does not name are `edit` and
`deliver`, and both follow from the sentence that closes SP-RB-1 — *whoever waits for a model
response returns its CPU token beforehand*. A phase that spends its time waiting on a model is a
`net` phase whatever else it does, and a phase that spends its time moving bytes is an `io` one.

### Returning the CPU token while waiting

A pod that enters `awaiting_reply` returns the token of its current phase before it waits, and asks
for one again when the reply arrives. This is SP-RB-1's closing sentence, and it is stated for every
class rather than only for `cpu·ram`: a pod waiting on a model holds nothing, because a token it
holds is a token no other pod can have while nothing happens with it.

The reacquisition is not guaranteed to be immediate, and that is deliberate — a job that comes back
from a model queues like any other. What it does not lose is its state (SP-RB-4).

### The pool sizes

The three pools are derived from the node's allocation and never from `os.cpus()` (SP-RC-5, the same
reason the pod's own concurrency is injected):

| Pool | Size | Why |
|---|---|---|
| `cpu·ram` | `cores`, at least 1 | one computing pod per core is the bottleneck SP-RB-1 calls it; more would be the "20 running, 2 computing" the work package opens with, inverted |
| `io` | `max(1, cores / 4)` | "few". One queue depth per four cores keeps the disk from being the thing every pod waits on, and SP-RC-2 cuts it to exactly 1 under `io full avg10 > 20 %` |
| `net` | `8 × cores` | "many slots". A waiting pod costs a frozen pod's memory (E-05: 0.37 MB measured), not a core; the number is the point where the control plane's own bookkeeping, not the work, would become the cost |

`cores` is the whole node's allocation for the work layer, injected by the caller. The ratios are
what is ruled here; the absolute numbers follow the machine.

### Exclusive operation

SP-RB-5 and SP-RB-6 are above this table, not beside it: a job whose predicted peak RSS exceeds 60 %
of the available RAM takes **all** `cpu·ram` tokens for its `check` phase, and no second such job is
started while it holds them. The tokens are not divided; they are held. That is what "never
time-sliced" means with a token pool underneath it.

## Rationale

1. **The join has to exist somewhere, and a table is better than a switch statement.** Without it,
   every phase transition would ask an unwritten rule which token to take, and the answer would live
   in whichever function was written last. As a table it is one thing, readable without the platform,
   and checkable against the program.
2. **Five rows are quotations, and the two that are not follow from the panel's own closing
   sentence.** SP-RB-1 does not classify `edit` and `deliver`, but it does say what makes a phase a
   `net` phase: waiting for a model. That sentence decides `edit`. `deliver` is not computing and not
   waiting on a model — it is moving a patch and an effect, which is what `io` is for.
3. **`edit` on `net` is the whole point of the work package.** The issue opens with "20 jobs can be
   running while only two are computing". If `edit` held a CPU token the scheduler would run as many
   jobs as it has cores while nearly all of them sat waiting on an API — exactly the coarse counter
   R-B replaces.
4. **The sizes are ratios, because the machine is not decided here.** E-02 puts a cell on one node
   today and a fleet later; a ruling in absolute slots would be re-ruled on the next machine. What is
   invariant is that CPU is the bottleneck, disk is narrow, and waiting is cheap.
5. **`io = cores / 4` has a floor of 1 and a ceiling SP-RC-2 already wrote.** The panel's reaction to
   disk pressure is "I/O tokens to 1, serialize installations", so the value under pressure is
   ruled — this rules only the value without it.

## Overturned by

A measurement in which a phase's token class is the reason a node is idle: `net` slots exhausted
while cores are free, or `cpu·ram` tokens held by pods that are demonstrably waiting on a model. The
first would mean `8 × cores` is too small, the second that a phase is on the wrong row. Also: a
change to T-05's spine (SP-T05-1) — a new phase is a new row here, in the same commit, or the
program refuses to schedule it.
