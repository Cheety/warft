# Decision: English throughout, prose included

**Status:** ruled · **Date:** 2026-07-25 · **Supersedes:** the language rule in the head of the
architecture document (revision 2) and its restatement in SP-RD-1.

## Ruling

Everything in this repository is written in English: names and prose alike. This replaces the
document's original rule — "names in English, explained in German" — and removes its one exception,
under which the R-D occupancy table stayed entirely German.

Requirement identifiers are not affected and are never translated: `SP-*`, `AB-*`, `AP-*`, `OP-*`,
`E-*`, and the panel letters `T Q F R K V B G A E`.

The bound glossary lives in `CLAUDE.md`. It is binding because a translation that drifts term by term
produces two vocabularies for one system, which is the failure this rule exists to prevent.

## Rationale

1. **The names were already English.** The specification carried them and only glossed them in
   German — `` `handle` (Griff) ``, `` `procedure` (Verfahren) ``, `` `campaign` (Feldzug) ``. The
   glosses were the only German part of the vocabulary, so removing them costs no terminology and
   settles no open question.
2. **The seam was in the wrong place.** German prose around English identifiers means every sentence
   about a field crosses a language boundary. Reviews, commit messages and issue text sat on one side,
   the thing under review on the other.
3. **SP-RD-1 was the tell.** A normative requirement that fixes the *language* of an instrument is a
   requirement about presentation, not about the platform. Its substance — the occupancy table is an
   instrument, not a contract — survives unchanged; only the language clause is void.
4. It was decided by the repository owner, explicitly, with the scope of the change stated in advance.

## Consequences

- `01-specification.md` §0.1 records the deviation in the normative text, visibly, rather than
  silently presenting the new rule as the original one.
- SP-RD-1 keeps its substance and carries a note that the German-only clause is void.
- Runtime output moved with the prose, not only comments: the three `RAISE EXCEPTION` messages of the
  K-02 trigger, the PASS/FAIL/SKIP lines of `acceptance/a06-acceptance.sh`, that script's arguments
  (`alle`/`basis` → `all`/`base`) and its subcommands (`workpod abnahme` → `workpod acceptance`). The
  last of these is a contract against a binary that does not exist yet — cheaper to rename now than in
  stage 3.
- The acceptance matrix kind `Ü` (Übung) became `D` (drill); `P` (Prüffall) stayed `P` (probe). The
  registry built in AP-0.3 uses the new letters.
- Tracker labels and milestones were **renamed**, not recreated, so every existing assignment and the
  sub-issue hierarchy stayed intact.

## Overturned by

A second party to this repository who does not read English, or a regulatory requirement to hold the
specification in German. In that case the reversal is a translation of the prose only — the
identifiers and the glossary hold either way, and this file records what to translate back.
