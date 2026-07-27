# Decision: R-A's four classes as cgroup v2 knobs

**Status:** ruled · **Date:** 2026-07-27 · **Affects:** SP-RA-1, SP-RA-2, SP-RA-3, SP-RA-4,
SP-RC-5, AB-RA-1, AB-RA-2, AB-RA-4, AB-RC-5, AP-3.3. Due to be re-measured by AP-3.7 (SP-RC-6).

R-A gives four classes and two columns per resource — the request guaranteed, the limit tolerated.
It also names four knobs: `cpu.weight` instead of `cpu.max` (SP-RA-3), `memory.high` instead of
`memory.max` (SP-RA-2), and `pids.max` and `io.latency` set (SP-RA-4). What it does not give is the
arithmetic between the two: which knob carries the request, which carries the limit, and what number
`pids.max` and `io.latency` take — the panel names those two without a table behind them.

That is a gap, and a gap is decided here before it is code (V-05).

## Ruling

### 1. MB and GB in SP-RA-1 are binary

`128 MB` is 134217728 bytes, `1.5 GB` is 1610612736. The panel writes memory the way a kernel
reports it, and every number in the table is a round power of two when read that way and a ragged
one when read decimally.

### 2. The request is `memory.min` and `cpu.weight`; the limit is `memory.high` and the injected concurrency

| SP-RA-1 says | the knob | why |
|---|---|---|
| CPU requested | `cpu.weight` | SP-RA-3 forbids `cpu.max`, so the request can only be a share. Weight is proportional, so the ratio of two weights is the ratio of two requests — which is what "guaranteed" means when nothing is capped. |
| CPU limit | the concurrency variables of SP-RC-5 | The limit is *tolerated*, not enforced (SP-RA-1), and SP-RA-3 removes the knob that would enforce it. What is left is to tell the pod how wide it may be, which is exactly what SP-RC-5 asks for. |
| RAM requested | `memory.min` | The one memory knob that is a guarantee: pages up to `memory.min` are not reclaimed. SP-RC-4 already uses it for the control layer's 4 GB; a pod's request is the same mechanism, one level down. |
| RAM limit | `memory.high` | SP-RA-2, verbatim: throttled, not shot. |

`memory.max` is **not set on a pod**, and neither is `cpu.max`. Their absence is the ruling, not an
omission — a pod that exceeds its limit is reclaimed against and slowed down, and the only thing
that kills it is the machine running out, where `memory.oom.group=1` (SP-RC-4) makes it die whole.

`cpu.weight` = CPU requested × 100, so that the class with a request of one core carries cgroup v2's
default weight of 100 and the four classes stand in the ratio 1 : 3 : 10 : 20.

### 3. The four classes, as the runner writes them

| Class | `cpu.weight` | `memory.min` | `memory.high` | `pids.max` | `io.latency` |
|---|---|---|---|---|---|
| `tiny` | 10 | 134217728 | 536870912 | 128 | 100 |
| `small` | 30 | 402653184 | 1610612736 | 256 | 100 |
| `medium` | 100 | 1073741824 | 3221225472 | 1024 | 100 |
| `large` | 200 | 3221225472 | 8589934592 | 4096 | 100 |

`io.latency` is in milliseconds and is written as `<major>:<minor> target=<microseconds>` against the
device `/data/work` sits on — the disk a pod actually writes to (SP-A05-3).

The machine-readable half of this table is
[`platform/internal/allocation/ra1-classes.tsv`](../platform/internal/allocation/ra1-classes.tsv);
`acceptance/t04-runner.sh` holds the file against **both** sources — the four given columns against
SP-RA-1's own table in `01-specification.md`, and the four ruled columns against this file. A number
that moves in one and not the other is drift, and the run fails.

### 4. `pids.max` scales with the class; `io.latency` does not

`pids.max` is the fork wall of AB-RA-4. The numbers rise with the class because what a class is *for*
decides how many processes are legitimate: reading and planning is one process and a few helpers, a
monorepo build is a compiler per core and a test runner per package. 128 · 256 · 1024 · 4096 is one
doubling series from "a shell and its children" to "a large build", and the largest of them is an
eighth of a stock `kernel.pid_max`, so a full `large` pod cannot exhaust the machine's process table
even four at a time.

`io.latency` is **the same number for every class**, and that is deliberate. R-A's table has no IO
column: the class decides CPU and RAM and says nothing about disk. What SP-RA-4 asks for is that the
knob be *set* — a cgroup without `io.latency` is invisible to the io controller's protection
mechanism, and invisible is the one thing a pod may not be when the control layer needs to be
protected from it. All pods being peers at one target means no pod is prioritized over another,
which is the correct default while the scheduler has no io token yet. Differentiating the target per
phase is R-B's (`io` tokens, SP-RB-1) and lands with AP-3.7.

### 5. Concurrency comes from the CPU limit (SP-RC-5)

| Variable | Value | Because |
|---|---|---|
| `MAKEFLAGS` | `-j<cores>` | make's job count |
| `CARGO_BUILD_JOBS` | `<cores>` | cargo's |
| `UV_THREADPOOL_SIZE` | `<cores>` | libuv's blocking pool |
| `TURBO_CONCURRENCY` | `<cores>` | turbo's task fan-out |
| `NODE_OPTIONS` | `--max-old-space-size=<¾ of the RAM limit in MiB> --v8-pool-size=<cores>` | both halves of the same mistake |

`<cores>` is the CPU **limit** in whole cores — 1 · 2 · 4 · 8 — because the limit is what the pod may
use, and a pod told to be narrower than it is allowed to be is a pod that wastes its own allocation.

`NODE_OPTIONS` carries two settings because Node derives two things from the host it cannot see:
`--v8-pool-size` is the concurrency half SP-RC-5 lists it for, and `--max-old-space-size` is the same
error in memory — Node sizes its old-space heap from the machine's RAM, so a pod on a 512 GB host
plans a heap that `memory.high` will throttle it to death for. Three quarters of the limit leaves
room for everything in the pod that is not the JavaScript heap.

## Rationale

1. **Each mapping is forced, not chosen.** SP-RA-2 and SP-RA-3 remove `memory.max` and `cpu.max`;
   what remains for a guarantee is `memory.min` and `cpu.weight`, and what remains for a tolerated
   limit is `memory.high` and a number told to the pod. There was one degree of freedom — the factor
   between a CPU request and a weight — and the default weight of 100 fixes it.
2. **The two invented numbers are invented in one place.** `pids.max` and `io.latency` have no source
   in the specification. They are ruled here, in a table, with a machine-readable half a program
   reads and a check that fails when the two disagree — so they cannot become constants somewhere in
   the runner that nobody can find again.
3. **The check reads the specification, not a copy of it.** AB-RA-1 says "allocation sets request and
   limit per the table", and the table it means is SP-RA-1's. The acceptance parses that table out of
   `01-specification.md` directly, so a class whose numbers drift from the panel fails even if this
   ruling and the code agree with each other.

## Overturned by

A measured allocation. SP-RC-6 records peak RSS and runtime per repository and phase, and after three
runs admission decides mechanically — at that point the four classes stop being the only answer to
"how big is this job" and these numbers become the fallback for a job with no history. The mapping of
§2 survives that; the numbers of §3 and §4 are due to be re-measured by AP-3.7.
