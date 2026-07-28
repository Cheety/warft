# Decision: the measurement stage 3 ends with — three numbers, and where each one comes from

**Status:** ruled · **Date:** 2026-07-29 · **Affects:** E-11 step 3, 02-work-packages.md stage 3,
AB-T04-5, AB-A06-11, AB-A06-13, AP-3.3, AP-3.5, AP-3.8. Overturned by stage 5.

E-11 rules that every step ends with a measurement and not with an opinion, and that no step starts
before the previous one has delivered its number. Stage 3's number is three of them, stated in
`02-work-packages.md`:

> **Measurement of this stage:** jobs per hour on one node; share of orphaned subvolumes after a
> restart (must be zero); double execution without double effect passed.

Two of the three are acceptance rows and were answered by runs: `AB-T04-5` (a restarted worker sweeps
its orphans, `acceptance/t04-runner.sh`) and `AB-A06-11` (two attempts, one push,
`acceptance/k03-outbox.sh`). The third had no instrument at all. The stage therefore could not be
closed, and the pointer under that sentence — `→ AB-A06-11, AB-A06-13` — pointed the first number at
the wrong row: `AB-A06-13` is the calibration run of stage 1, a fleet of 500 pods created and 20
active on a bare image, which is a statement about a machine and not about jobs.

Three things needed ruling before code: whether jobs per hour becomes an acceptance row, what a job
is when it is counted, and what the pointer in the build order should say.

## Ruling

### 1. Jobs per hour is a measurement and never becomes a row in the acceptance matrix

`03-acceptance-matrix.md` decides whether the platform was hit. Its rows are answerable: a check
passes or it does not, and `acceptance/registry.tsv` refuses a green without a run. A throughput
figure answers nothing — it describes. Made into a row it would need a threshold, the threshold would
be invented here rather than derived from a panel, and the instrument would then measure the
invention. So the matrix keeps its 213 checks, and the number lands in
`acceptance/stage3-measurement.tsv` beside the run that produced it.

### 2. A job is one order through the whole spine, in a pod, on a node

Counted is an order that passes through all seven steps of T-05 — `prepare · plan · edit · check ·
repair · deliver · reap` — in a runc container on a btrfs snapshot, with a blocking check that has
to pass before `deliver` may claim its evidence class. Not a pod start, which is `AB-T03-1`'s
~200 ms and a different question; not a phase; and not a job that ended `unproven`, because the
throughput of a machine failing quickly is not throughput.

The jobs are stated by hand (`decisions/jobs-by-hand.md`), on one node, with no captain sizing them
and no model writing the edit. **The number is therefore a floor and is recorded as one**, with those
conditions attached — the same shape `acceptance/e05-constants.tsv` uses for the two constants that
were measured on an image carrying no control plane.

### 3. The pointer in the build order is corrected

Stage 3's measurement line now reads `→ AB-T04-5, AB-A06-11, acceptance/stage3-measurement.sh`.
`AB-A06-13` stays what it is, stage 1's calibration run, and is no longer cited as one of stage 3's
numbers.

### 4. The two rows are read, not re-measured

`acceptance/stage3-measurement.sh` reads the state of `AB-T04-5` and `AB-A06-11` out of
`acceptance/registry.tsv` and fails if either is not green with a run named beside it. It does not
measure them a second time: two instruments for one question is how the two answers start to
differ, and the registry is the instrument this repository already has.

## Rationale

1. **The stage could not close, and the reason was a missing instrument, not a missing opinion.**
   Every other number in this repository is produced by a script that can be run again. Closing
   stage 3 on a figure someone remembered from a demo would be exactly the "confidence as an
   acceptance criterion" Q-02 exists to refuse — and it would be the first such number, which is how
   the second one becomes easy.
2. **A floor is a number; a floor presented as the constant is an explanation.** The sentence is
   `calibration.sh`'s and it applies unchanged: jobs per hour with no model in the loop is not jobs
   per hour of the platform, and the honest form is the figure with the conditions of its
   measurement attached, not a figure withheld until the conditions are perfect.
3. **A threshold here would be invented.** No panel states a throughput target. Making the number a
   row would force one, and `01-specification.md` is the only place a requirement may come from —
   "what is not already in the specification is not decided here."
4. **The wrong pointer was load-bearing.** `AB-A06-13` is green, so a reader checking whether stage 3
   had delivered its numbers would have found two green rows against a three-number sentence and
   concluded yes. A pointer that makes a missing measurement look present is worse than no pointer.

## Consequences

- `acceptance/stage3-measurement.sh` and `acceptance/stage3-measurement.tsv` exist, and the image CI
  leg runs the script on the artifact it just built.
- `02-work-packages.md`'s stage 3 measurement line names the three instruments.
- The table lands with `jobs_per_hour` at `pending`, which the script reports and does not fail on.
  The first run that produces a number replaces `pending` with it and names the run; from then on a
  fresh measurement more than a factor of two away stops the run.
- Stage 4 may start when the three numbers stand — which is E-11's rule, applied to the stage that
  was waiting on the third.

## Overturned by

Stage 5. A captain sizing jobs (AP-5.5) and a model writing the edit (Q-04) change what a job *is*,
and a floor measured without either stops describing anything the platform then does. When AP-5.5
lands, this number is measured again under the conditions of that stage, and the row recorded here
becomes the number it is compared against rather than the number in force.
