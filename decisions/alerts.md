# Decision: the four alerts that wake a human, and the ones that only show

**Status:** ruled · **Date:** 2026-07-28 · **Affects:** SP-B03-3, SP-A05-5, SP-B02-5, SP-B03-2,
AB-B03-3, AB-A05-5, AB-B02-5, AP-3.8.

SP-B03-3 names four alerts and closes with a sentence that is the whole panel: *a fifth alert
devalues the four*. It does not say what the four are measured against, and two other MUSTs put
something that is called an alert beside them — SP-A05-5 ("the disk is the first consumable that
gets an alert") and SP-B02-5, which wants rejected egress targets in the display rather than only in
the log. Read carelessly, the specification asks for six alerts and forbids a fifth.

This is the ruling that makes the three requirements one thing.

## Ruling

**1 — An alert either wakes a human or it does not, and only four may wake one.** The waking four
are SP-B03-3's, in its own order:

| Slot | Name | Wakes on | Source |
|---|---|---|---|
| 1 | `control_plane_unreachable` | three consecutive failed pings at the heartbeat interval (OP-4: 15 s) | the node, not the database |
| 2 | `queue_growing` | twenty consecutive one-minute samples, none below its predecessor, the last strictly above the first | `queue_sample` |
| 3 | `escapes_or_rejections_jumping` | the last hour ≥ 3× the mean of the six before it, **and** ≥ 10 in absolute terms | `egress_rejection`, and escapes when Q-03 has them |
| 4 | `cell_budget_exhausted_early` | a daily pot at ≥ 90 % of any of its three caps while less than 75 % of its day has passed | `budget_pot` |

**2 — Everything else is a display, and the disk is one of them.** `disk_filling` fires at 85 % of
the work disk. It is an alert in SP-A05-5's sense — the disk is the first consumable that gets one,
and no other consumable has one — and it is not a fifth waking alert. A full disk stops jobs; it
does not lose them, and the platform has an hour of warning between 85 % and full. The same rank
holds `egress_rejections_clustered`, which is SP-B02-5's display: a cluster of refused targets per
project and target, visible without reading a journal.

**3 — The count is enforced by the state contract, not by discipline.** `alert.waking_slot` is
`1..4`, unique, and `NOT NULL` exactly when `wakes` is true. A fifth waking alert is a constraint
violation in Postgres, which is what makes AB-B03-3 a probe (`P`) rather than an inspection: the
forbidden action is attempted and the database refuses it.

**4 — The catalog is one list in two places, and they are held against each other.**
`platform/internal/observation/alerts.tsv` is what the binary carries; the seed in
`contract/schema.sql` is what a cell holds; this table is the ruling both answer to.
`acceptance/b03-observation.sh` compares all three on every run — a row in one and not in another is
drift, and drift is an error rather than a silent omission.

## Rationale

1. **Four is a number about humans, not about systems.** A duty officer (E-08) who is woken by six
   things learns to ignore all six; the panel says so in as many words. So the scarce resource being
   rationed here is attention, and the ruling protects it by making "wakes" a property with exactly
   four slots rather than a habit of whoever adds the next check.
2. **The thresholds are here because they are new numbers.** V-05 says a deviation is a decision
   before it is code, and a threshold nobody ruled is a number in code that no requirement carries.
   Each of the four rests on something already ruled where one exists — the ping interval is OP-4's
   heartbeat, the daily pots are OP-1's — and the two that do not (3× over six hours with a floor of
   ten; 85 % of the disk) are stated here so they can be argued with, and moved, without reading Go.
3. **A floor under the jump matters more than the ratio.** Rejections are the best early warning
   signal for injection this system has (SP-B02-5), and they are also the noisiest: one job with a
   wrong allowlist produces a cluster. Three times a small number is a small number, so the absolute
   floor is what keeps slot 3 from becoming the alert that trains people to ignore alerts.
4. **The disk is a consumable and not an incident.** SP-A05-5 asks that the disk be the *first*
   consumable to get an alert, which is a statement about ranking consumables, not about waking
   anybody at four in the morning. Making it a display keeps both requirements literally true.

## Consequences

- `contract/schema.sql` grows `alert` with the four seeded waking rows and the display rows, plus
  `queue_sample`, which slot 2 is measured from. Both are additive (SP-V05-2).
- `workpod observe alerts` prints the catalog and, against a cell, the state of every alert with the
  source it was read from. An alert the state database cannot answer — slot 1 lives on the node —
  says so instead of reporting a green it did not measure (Q-02).
- A fifth waking alert is possible only by changing this file, the seed and the constraint together.
  That is the point.

## Overturned by

An operations experience that names a fifth thing worth waking a human for. Then the ruling is not
"add a fifth" but "which of the four is it replacing" — the constraint holds the number at four, so
the trade has to be made explicitly rather than by accretion.
