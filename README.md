# Workpod platform — build order

Derived from *Architecture overview, revision 2 (July 2026)*, 57 panels. This package is that
architecture document turned into something you can start on Monday morning — and something that, at
the end, decides whether the platform was hit.

It is the same transformation Q-01 demands of every prompt this platform will ever accept: a draft
becomes a checkable criterion.

## The four documents

| File | What it is | When you read it |
|---|---|---|
| `01-specification.md` | The normative specification. Every requirement carries an identifier `SP-<panel>-<n>` and comes from exactly one panel. Nothing invented on top; what the draft leaves open stands in §19 as an open point with a due date. | While building, as a reference |
| `02-work-packages.md` | Nine stages, 45 work packages, each with a "done when" that measures. The order is the one from E-11 and is not swapped. | For planning and for cutting the work |
| `03-acceptance-matrix.md` | **The actual instrument.** Every panel has at least one check that turns red if it was not built. The platform is hit when this list is green. | On day one — and every day after |
| `contract/`, `acceptance/`, `skills/` | Runnable artifacts: the one interface schema, the database schema with its trigger rules, the capability template, the A-06 acceptance script. | From AP-0.2 on |
| `tracker/` | The 66 issues as data (`issues.json`, `issues.csv`) and the two import scripts. `issue-map.json` records which identifier carries which issue number. | Once, while setting up |

## Directories

```
01–04*.md    specification, work packages, acceptance matrix, issues
contract/    platform.proto (E-10) · schema.sql (K-01, K-02)
acceptance/  registry.py · registry.tsv — the 212 checks and their state (AP-0.3)
             a06-acceptance.sh — thirteen checks, written before the image
skills/      SKILL.template.md — template for a catalog entry (F-01, F-07)
tracker/     issues.json · issues.csv · gh-import.{sh,py} · issue-map.json
decisions/   empty until AP-0.1: eleven rulings E-01…E-11, ten open points OP-1…OP-10
image/       empty until AP-1.1 (mkosi)
platform/    empty until AP-3.1 (Go)
```

`decisions/`, `image/` and `platform/` are deliberately empty. Filling them is work with an
acceptance, not tidying up — AP-0.1, AP-1.1, AP-3.1.

## The tracker

```
tracker/gh-import.py <owner/repo> --dry-run    # shows what would be created
tracker/gh-import.py <owner/repo>              # creates it
```

35 labels, 10 milestones (= stages), 66 issues: 10 epics, 45 work packages, 10 decisions, 1 gate.
The Python version needs no `jq` and does two things `gh-import.sh` leaves as manual follow-up:
"Blocked by: AP-x.y" is rewritten to issue numbers, and every work package hangs as a sub-issue off
the epic of its stage. A second run aborts instead of creating duplicates.

Open and deliberately not automated: splitting the four XL issues — AP-4.2 (13 handles), AP-5.2
(10 procedures), AP-8.2 (6 campaigns), AP-8.3 (3 runners). The cut is in `01-specification.md`; it is
a decision, not a loop.

## The order you start in

1. Transfer `03-acceptance-matrix.md` into a test registry. **Everything red.**
2. Run `acceptance/a06-acceptance.sh` against the first bare mkosi VM.
3. The five kernel checks in it (cgroup v2/PSI, reflink, namespaces, freezer/zram, CRIU) are the
   decision on E-01. If they are red and cannot be reconfigured, **the base changes, not the order**.
4. Only then the first line of Go.

> Whoever writes this list first and builds afterwards has turned an architecture document into a
> build order. — A-06

## What "hit 100 %" means

Three conditions, all three checkable (section D of the matrix):

1. Every row of the acceptance matrix is green — through a run, not through an explanation.
2. The six numbers (`escape_rate`, `false_reject_rate`, `cost_per_acceptance`,
   `no_clarification_rate`, `coverage_rate`, `success_rate` per capability) have a recorded baseline
   and a direction.
3. Every open point from §19 is decided and lies as a file in `decisions/`, with ruling, rationale
   and overturn condition.

## The three rules that stand above everything

- **Language:** everything in this repository is English — names and prose alike. Names are taken
  over unchanged; identifiers (`SP-*`, `AB-*`, `AP-*`, `OP-*`, `E-*`, panel letters) are never
  translated. See `CLAUDE.md` for the bound glossary.
- **Order (E-11):** every step ends with a measurement instead of an opinion. No step starts before
  the previous one has delivered its number.
- **Acceptance (Q-02):** confidence is not an acceptance criterion, evidence is. That holds for every
  job the platform runs — and for the platform itself.
