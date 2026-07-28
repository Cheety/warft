# Workpod platform — working rules

Derived from *Architecture overview, revision 2 (July 2026)*, 57 panels. This repository is that
architecture document turned into a build order: a normative specification, a build order in stages,
and an acceptance matrix that decides whether the platform was hit.

## Where work lands

Commit on `main`. Do not create a branch and do not open a pull request unless the request says so
explicitly — the words "branch", "PR" or "pull request" in the ask are the only trigger. Without
them, a change is finished when it is committed on `main`.

## Commit Messages
Do not add a `Co-Authored-By` trailer, a `Claude-Session` line, or any Claude / Claude Code session link (e.g. `claude.ai` / `claude.com` URLs) to commit messages.

## Code comments
The code itself should express what it does, but comments are necessary for business logic, complex algorithms, workarounds for bugs, unidiomatic implementations. Write comments at the same time you write or refactor the code.

Follow these best practices to ensure your comments add value:
- Explain Intent and Context: Describe the high-level reasoning behind a decision, especially if the implementatio seems counter-intuitive.
- Document Workarounds: If you are writing a fix for an obscure bug, server error, or third-party limitation, explain why that specific code exists so it isn't "accidentally" refactored later.
- Avoid Redundancy: Do not explain simple steps like // Increment i.
- Don't Comment Bad Code: If a block is too complicated, refactor it into smaller, readable functions first.

Docstrings are explicitely desired and not considered "comments" in the sense of this policy.

## Rule 1 — English, everywhere

**Everything in this repository is written in English.** Names, prose, comments, commit messages,
issue titles and issue bodies, decision records, test output.

This supersedes the earlier split ("names in English, explained in German"), including its exception
for the R-D occupancy table. There is no German left in the repository, and nothing new is written in
German. If you find German text, translating it is a fix, not a preference.

**Identifiers are never translated.** They are the joins between the four documents:

| Form | Means | Example |
|---|---|---|
| `SP-<panel>-<n>` | a requirement in `01-specification.md` | `SP-K01-3` |
| `AB-<panel>-<n>` | a check in `03-acceptance-matrix.md` | `AB-E05-1` |
| `AP-<stage>.<n>` | a work package in `02-work-packages.md` | `AP-3.6` |
| `OP-<n>` | an open point from §19, decided in `decisions/` | `OP-4` |
| `E-<n>` | one of the eleven rulings, `decisions/E-01.md` … `E-11.md` | `E-11` |
| panel letters | `T` `Q` `F` `R` `K` `V` `B` `G` `A` `E` | `tafel/K` → `panel/K` |

## Glossary — bind these, do not re-invent them

The specification already carried the English names and only glossed them in German, so most terms
were chosen by the author, not by a translator. Use exactly these:

| English | was (German) | Note |
|---|---|---|
| `handle` | Griff | order 1 of three: no model, mechanical, ends in an artifact |
| `procedure` | Verfahren | order 2: fixed sequence, ends in a checkable claim |
| `campaign` | Feldzug | order 3: plan and judgement over aggregates; changes nothing itself |
| `cell` | Zelle | `NOT NULL` on every table |
| `project` | Vorhaben | `NOT NULL` on every table; a thread equals a project |
| `min_authority` | Vollmachtsstufe | authority level |
| envelope | Umschlag | what intake accepts, before it becomes a job |
| authority | Vollmacht | the Biscuit token; attenuation only, never widening |
| intent contract | Absichtsvertrag | Q-01, between envelope and job |
| capability | Fähigkeit | a catalog entry, `SKILL.md` |
| coverage check | Deckungsprüfung | F-04, five failure causes |
| acceptance | Abnahme | the matrix, the script, the gate |
| acceptance matrix | Abnahme-Matrix | `03-acceptance-matrix.md`, 213 checks |
| work package | Arbeitspaket | `AP-<stage>.<n>` |
| stage | Stufe | milestone in the tracker |
| effort | Aufwand | `S` ≤ 3 days · `M` ≤ 1 week · `L` 2–3 weeks · `XL` split candidate |
| panel | Tafel | a lettered section of the architecture document |
| ruling | Festlegung | the decision itself, in `decisions/` |
| rationale | Begründung | why it was ruled that way |
| overturned by | Umgestoßen durch | the condition that voids the ruling |
| open point | offener Punkt | `OP-<n>`, §19, each with a due work package |
| measurement | Messung | how a stage ends — not an opinion |
| ferry | Fähre | V-02, pull not push |
| test bench | Prüfstand | stage 7, replay without live calls |
| duty officer | Leitstandführer | E-08, on call, at least two named people |
| occupancy table | Belegungstafel | R-D; now English too |
| rework round | Nachbesserungsrunde | T-05, bounded |
| terminal state | Endzustand | never without a cause |
| system image | Abbild | the mkosi OS image, `image/`, panels A-01…A-06 |
| container image | Image | what a pod runs in, T-03, `image_hash` |
| build order | Bauauftrag | what this repository is |

German kept "Abbild" and "Image" apart; English collides on "image". Say **system image** or
**container image** whenever the sentence does not already make it obvious — A-03 is about the first,
T-03 about the second.

The acceptance matrix marks each check with a kind: `S` script · `M` measurement · `D` drill ·
`P` probe (a negative test: the forbidden action must fail). `D` was `Ü` (Übung) and `P` was
Prüffall; the letters are part of `03-acceptance-matrix.md` and of the registry built in AP-0.3.

`tracker/issues.json` field names are English too: `id`, `type` (`epic` · `work_package` ·
`decision` · `gate`), `title`, `milestone`, `labels`, `blocked_by`, `effort`, `panels`, `spec`,
`acceptance`, `body`. `issues.csv` and the per-issue sections of `04-issues.md` are the same data in
two other shapes — change `issues.json` and regenerate, never edit one of the three alone.

## Rule 2 — Order (E-11)

Build in the order of `02-work-packages.md`. Every step ends with **a measurement, not an opinion**.
No step starts before the previous one has delivered its number. Do not reorder stages; if the base
does not hold, **the base changes, not the order** (E-01, overturned by: then NixOS).

## Rule 3 — Acceptance (Q-02)

Confidence is not an acceptance criterion, evidence is. That holds for every job the platform runs
and for the platform itself. Progress means rows in the acceptance matrix turn green — **not that
there are more files**. A row turns green through a run, never through an explanation.

`make acceptance` is that instrument. `acceptance/registry.tsv` holds the state of all 212 checks;
`03-acceptance-matrix.md` stays the source of which checks exist, and the two are kept in step —
drift is an error, not a silent omission. A check has three states: `red` (not evidenced, the
starting state of every check), `green` (evidenced through a run, **and the evidence column names
that run**), and `open` (deliberately open, with the evidence column naming the file in
`decisions/` that justifies it). The registry rejects a green row without a run, so "done" cannot be
asserted into the instrument — which is Q-02 applied to the thing that measures Q-02.

## Layout

```
01-specification.md      normative; every requirement is SP-<panel>-<n>, from exactly one panel
02-work-packages.md      nine stages, 45 work packages, each with a "done when" that measures
03-acceptance-matrix.md  213 checks; the actual instrument. Green here means the platform was hit
04-issues.md             the 66 tracker issues in long form, with a boundary each
contract/                platform.proto (E-10) · schema.sql (K-01, K-02)
acceptance/              registry.py · registry.tsv — the 212 checks and their state (AP-0.3)
                         a06-acceptance.sh — thirteen checks, written before the image
                         calibration.sh · calibration-probe.sh — the fleet, and E-05's five
                         constants measured on it (AP-1.3)
                         t04-runner.sh — pods on a node: R-A's contract read back out of their own
                         cgroups, no network, the lifecycle, the reaper (AP-3.3)
                         t05-pipeline.sh — T-05: the fixed spine, the seven movable places, and a
                         job that cannot be solved ending after OP-2's rounds (AP-3.4)
                         k03-outbox.sh — K-03's chain: both gates on their sockets, a job run twice
                         that pushes once, and the one refusal to retry (AP-3.5)
                         v04-budget.sh — V-04: reservation at admission, release at the terminal
                         state, the channel limit, and the halt file with the API off (AP-3.6)
                         rb-scheduler.sh — R-B and R-C: the token a phase holds, the aging that
                         keeps a batch job from starving, the five rungs in order, and the queue
                         with SKIP LOCKED (AP-3.7)
                         b03-observation.sh — B-03: one trace per job, the four alerts with no
                         fifth, pod logs as evidence, the refused targets in a display, and the
                         provenance chain in one query (AP-3.8)
                         e05-constants.tsv — those constants, given and measured; what R-D computes
                         with, and the machine-readable half of decisions/E-05.md
Makefile                 make acceptance — the instrument; fails while anything is red
skills/                  SKILL.template.md — template for a catalog entry (F-01, F-07)
tracker/                 issues.json · issues.csv · gh-import.{sh,py} · issue-map.json
decisions/               rulings E-01…E-11, open points OP-1…OP-10, and the rulings taken here (AP-0.1)
image/                   mkosi configuration, the units a role activates, build · seal · verify · vm
platform/                the one Go binary: the A-04 start sequence, SP-E02-1's seven entry points,
                         the Runner contract (T-04), the pipeline (T-05) beside it, the workpod
                         that implements both, K-03's outbox with the two gates that drain it, and
                         V-04's pots with the admission that reserves them, R-B's tokens,
                         priorities and PSI ladder over R-C's six signals, and B-03's trace,
                         alert catalog and occupancy table
```

All three started empty and were filled by the work package that owns them — AP-0.1, AP-1.1,
AP-3.1 — each through an acceptance, not by tidying up. The module boundaries inside `platform/` are
themselves a decision (`decisions/module-dependencies.md`), checked against the imports.

## Tracker

GitHub: `Cheety/warft`. 35 labels, 10 milestones (= stages), 66 issues — 10 epics, 45 work packages,
10 decisions, 1 gate. `tracker/issue-map.json` maps identifier to issue number; use it instead of
searching by title.

```
tracker/gh-import.py <owner/repo> --dry-run   # show what would be created
tracker/gh-import.py <owner/repo>             # create; aborts if issues already exist
```

Labels are `stage/0`…`stage/9` · `panel/T`…`panel/E` · `effort/S|M|L|XL` ·
`kind/platform|image|contract|catalog|operations|decision` · `acceptance/blocker` · `measurement` ·
`epic` · `blocker` · `gate`.

A milestone closes when its measurement exists — **not when its issues are closed**.

## The working copy lies on a Windows filesystem

`core.fileMode` is `false` here, so `chmod +x` changes the file but not the Git index, and a script
committed that way arrives at 100644 on a fresh clone — where it fails with "Permission denied".
`make acceptance` and the image CI leg both hit this once. After adding any script:

```
git update-index --chmod=+x <path>
git ls-files -s <path>          # expect 100755
```

## When working here

- **Do not invent architecture.** What is not already in the specification is not decided here. A gap
  is an open point with a due date, not a guess in code.
- **A deviation from the specification is a decision in `decisions/` before it is code** (V-05), with
  ruling, rationale and overturn condition. Decisions live in Git, never in the database — so they
  survive losing the database and are readable without the platform.
- **`decisions/` is never changed alone** (E-08, two-person rule; see CODEOWNERS once AP-0.1 lands).
- **No `TODO` in delivered code that stands in for a requirement.** What is missing is an issue.
- Definition of done, the same for every issue: the named acceptance rows are green through a run;
  deviations are filed as decisions; no requirement replaced by a comment.
