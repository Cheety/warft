# Decision: the two-person rule is deferred while the project has one person

**Status:** ruled · **Date:** 2026-07-25 · **Affects:** E-08, AP-0.1 (CODEOWNERS task), OP-10.

## Ruling

While this project has a single person, that person rules all decisions alone, including the ones
E-08 reserves for two people (`cap.raise`, `authority.extend`, and any change to `decisions/`).

No `CODEOWNERS` file is created for `decisions/` for now. The task in AP-0.1 that asks for one is
answered by this file instead.

E-08 itself is **not** amended: it still reads "at least two trained people, one of them active",
and its overturn condition still reads "nothing". This is a deferral with a stated expiry, not a
weakening of the ruling.

## Rationale

1. **The rule protects against a class of failure that does not exist yet.** E-08's asymmetry —
   halting alone is allowed, widening alone is not — guards against one operator raising rights or
   spend under pressure with nobody watching. With one person and no outside users, there is no
   second party whose exposure the second signature would protect.
2. **A single-owner `CODEOWNERS` would be worse than none.** GitHub does not let you approve your own
   pull request. A `CODEOWNERS` entry on `decisions/` naming the only person in the project, combined
   with a required review, locks that person out of the directory entirely. The rule would then be
   enforced by making the work impossible, which is not enforcement but breakage.
3. **Writing it down is the part that matters.** The risk of deferring E-08 is not that one person
   decides — it is that nobody notices when the second person arrives and the rule silently stays
   off. A file with an overturn condition is what makes that noticeable.
4. It was decided by the repository owner, explicitly, when the conflict was raised.

## Consequences

- AP-0.1's fourth task is satisfied by this file, not by a `CODEOWNERS` file.
- OP-10 ("two named people for the duty officer role", due before AP-7.5) stays **open**. It is not
  answered by this decision — it is the point at which this decision expires.
- AB-E08-1 ("widening actions demand two, a halt demands one") cannot turn green while this holds.
  Per section D of the acceptance matrix, a row that cannot turn green is not left out but justified
  as a decision and marked deliberately open. This file is that justification.

## Overturned by

A second person joining the project, or the first delivery to anyone outside it — whichever comes
first. At that point `CODEOWNERS` is created, OP-10 is ruled with two names, and AB-E08-1 becomes
checkable. Whoever reaches AP-7.5 without having done this has found the overturn condition and
ignored it.
