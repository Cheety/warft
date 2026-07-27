# OP-2 — The number `n` of rework rounds per pipeline

**Status:** ruled · **Due before:** AP-3.4 — ruled in time, 2026-07-28, in AP-3.4 · **Panels:**
T-05 · **Source:** §19 of `01-specification.md`.

§19 left this number open and proposed a direction rather than a value a program could read. This
ruling turns it into one definition, one default and four ceilings, because SP-T05-3 puts the end of
the loop *in the pipeline* — the one place where a job is still a plan rather than a pod that has
already spent an hour — and a bound at that place needs a number to bound against.

## To be decided

The number `n` of rework rounds per pipeline

## Proposed ruling

3, overridable per job class

## Ruling

### What a round is

**A rework round is one `repair` followed by the `check` that judges it.** The first `check` is not a
round: it judges the `edit`, and a job whose edit was right the first time has spent no rounds at
all. With `n = 3` a pod therefore checks at most four times and repairs at most three, and
`report.rounds` counts the repairs.

The definition matters more than the number. "Three rounds" counted the other way is four repairs and
a fifth check, which on a `large` pod is another ten minutes of a machine — and the sentence this
work package exists for is that a pod running in circles is more expensive than one that gives up.

### The default, and the four ceilings

`n` is stated twice, and the smaller of the two wins:

| Where | What it says | Who owns it |
|---|---|---|
| the pipeline definition | the default, the same for every job that does not move it | the human (SP-T05-4) |
| the resource class | the ceiling, the most this class may ever spend | this ruling |

`default@1` carries §19's number: **`rework_rounds = 3`**. The ceilings are per class, which is what
"overridable per job class" means read literally — the class is the only thing that already says what
a round costs:

| Class | ceiling `n` | one round costs (SP-RA-1) |
|---|---|---|
| `tiny` | 1 | 0.1 CPU · 128 MB requested |
| `small` | 3 | 0.3 CPU · 384 MB |
| `medium` | 3 | 1.0 CPU · 1 GB |
| `large` | 2 | 2.0 CPU · 3 GB, up to 8 CPU · 8 GB |

**Effective `n` = min(what was stated, the class ceiling).** A job may move place five downwards
freely — zero rounds is a lawful job, and it means "check once, never repair". A job that names
*more* than its class allows is **refused before the pod is created**, with the ceiling named in the
refusal. It is not quietly clamped: a job asking for five rounds on a `tiny` pod is a job whose
author believed something that is not true, and a run that silently gave it one round would confirm
the belief instead of correcting it.

### How the loop ends

After the last round's check still fails a blocking check, the pod does **not** fail silently
(SP-T05-3). The order ends `unproven` with cause `unsolvable`, and the reply carries three things:

- **the diff** — the patch the runner computed from the base and the working copy, outside the pod,
- **the logs** — the pod's console on the node, and the output of every check of the last round,
- **an assessment** — the pod's own account: which checks still fail, what each round changed, and
  what it would need that it did not have.

`unproven` rather than `failed` is Q-02 read strictly: the job ran, the tools worked, and what is
missing is the evidence — not the run. `unsolvable` is the cause code `contract/schema.sql` already
carries for exactly this, and inventing a second word for it would put a state in the database the
state contract has no enum value for.

### Where the numbers live

`platform/internal/runner/op2-rounds.tsv` is the machine-readable half of this file, embedded in the
binary. `acceptance/t05-pipeline.sh` holds the two against each other on every run — a ceiling in one
that is not in the other is drift, and the run fails. That is the shape `decisions/OP-5.md`,
`decisions/resource-contract.md` and `decisions/E-05.md` already have: the ruling is the source, the
file is what a program can read, and a check joins them so neither can move alone.

It is embedded rather than read from the node, for the reason R-A's table is: a runner bounds a loop
without asking anything. A ceiling a node could edit is a ceiling an operator can raise, and then the
bound is a suggestion.

## Rationale

1. **Three is §19's number and nothing measured yet contradicts it.** The proposal was made by the
   architecture document with the whole platform in view; this ruling is not the place to improve on
   it, only the place to make it precise enough to check. What was missing was never the value — it
   was what a round is and what happens after the last one.
2. **The ceiling is per class because the class is the price tag.** R-A already sorts jobs by what
   they cost to run; a second axis for "how much repair is affordable" would be a number nobody could
   set, because whoever writes a job knows what they want, not what a node's minute costs. The class
   they already chose says both.
3. **`tiny` gets one round because a `tiny` pod that is repairing is on the wrong class.** SP-RA-1
   names `tiny` for reading, searching and planning. A pod with 0.1 CPU that has failed a blocking
   check twice is not going to build its way out on the third try; one round is enough to notice
   that, and the reply then carries the assessment that says so.
4. **`large` gets two rounds, fewer than `medium`, because the boundary of this work package is a
   cost sentence.** A `large` round is twenty times a `tiny` one in requested CPU and twenty-four in
   requested memory, and it is the class of monorepo builds and E2E suites — the rounds that take
   minutes each. A monorepo job two repairs did not fix is not usually one repair away; it is a job a
   human should read a diff of. Spending a third round to postpone that reply is exactly the twenty
   minutes in circles the issue names.
5. **Refusing an over-large `n` costs nothing at the only moment it is free.** The check runs in
   `Job.Validate`, before the image is resolved and before the snapshot stands — the same place every
   other refusal of an unrunnable job already lives, because a refusal after the subvolume exists is
   a subvolume nobody asked for.
6. **The end of the loop is ruled here rather than guessed in code, which is why OP-2 exists.** The
   issue's own boundary says so: the value is decided and filed, never guessed anywhere in the code
   (V-05).

## Consequences

- `AP-3.4` is unblocked. `platform/internal/runner` enforces exactly these numbers, and
  `acceptance/t05-pipeline.sh` evidences AB-T05-3 with a deliberately unsolvable job that ends after
  the ruled number of rounds in `unproven` / `unsolvable` with a diff, logs and an assessment.
- The ceiling travels in the artifact, so it holds wherever a runner runs — including the pools
  AP-8.3 adds. A `windows` runner that repaired five times would break this ruling without importing
  a line of `internal/workpod`, which is why the number sits in the contract module and not in the
  implementation.
- Any change to a ceiling is a new revision of this file *and* of `op2-rounds.tsv`, in one commit.
  The check fails on either alone.
- Place five is the only one of SP-T05-2's seven a job may not move freely. That asymmetry is
  deliberate and is the whole of this ruling: the other six change what a job does, this one changes
  what it may spend.

## Overturned by

A measurement rather than an argument: the distribution of rounds-to-pass over real jobs, per class,
counted over a period of operation. AP-3.8's observation is what can count it — every round is a
phase record with a class beside it, so the number exists without anyone collecting it by hand.

Two shapes of measurement move the lines in opposite directions, and each moves only one:

- jobs that pass on the *last* permitted round, often enough to be a pattern rather than luck, say a
  ceiling sits one below where the work actually finishes — that class's ceiling rises by one,
- rounds spent after the point at which the failing check never changed again say the opposite: a
  loop that is already decided is being paid for, and the ceiling falls, or the pipeline learns to
  end on an unchanged check rather than on a count.

Whichever arrives first arrives as a new ruling here, never as a flag on a job.
