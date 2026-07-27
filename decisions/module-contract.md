# Decision: the module dependency contract is deferred until there are modules

**Status:** expired at AP-3.1, on its own overturn condition · **Date:** 2026-07-26 ·
**Due before:** AP-3.1 · **Affects:** SP-G01-5, AB-G01-5, AP-0.1.

> AP-3.1 drew the first module boundaries, so this deferral ended as it said it would. The contract
> itself is [`module-dependencies.md`](module-dependencies.md), `acceptance/module-contract.py`
> checks it, and `AB-G01-5` is green through a run rather than open against this file. What stands
> below is why the row was open for eleven work packages, which is worth keeping: the reason was
> never that the contract was hard to write, it was that there was nothing to write it about.

`AB-G01-5` reads "decisions in Git — the module contract is machine-checkable and survives losing
the database". That is two claims, and at AP-0.1 they are in different states.

## Ruling

The half of `AB-G01-5` that concerns the decision store is evidenced by
[run 30176815764](https://github.com/Cheety/warft/actions/runs/30176815764) of
`acceptance/g01-decisions.sh`: the eleven rulings carry ruling, rationale and overturn condition;
the ten open points each name a due work package that exists; every decision is plain text with
cross-references that resolve to files; and `decision_ref` in `contract/schema.sql` holds nothing
but repository, path and commit, so the database points at decisions and cannot contain one.

The half that concerns the **module dependency contract** — SP-G01-5's "module A may depend on B and
C, not on D" — is **deferred until AP-3.1**, and `AB-G01-5` stands `open` until then with this file
as its justification.

Nothing about module dependencies is written down in the meantime. The specification does not name
the modules, and inventing a dependency graph in order to have something to check would be a guess
in code where the architecture is silent — the one thing this repository forbids outright.

## Rationale

1. **There are no modules yet.** `platform/` is empty until AP-3.1 (SP-E02-1: control plane,
   scheduler, worker, adapter, both gates and harness out of one Go binary). A contract that says
   "A may depend on B" needs an A and a B. The directories that exist today — `image/`, `contract/`,
   `acceptance/`, `decisions/`, `tracker/` — are the shape of the build order, not of the program.
2. **Marking it green would be the failure the instrument exists to prevent.** The registry rejects
   a green row without a run precisely so that "done" cannot be asserted into it (Q-02). A row that
   went green while half its evidence column had nothing behind it would be that same assertion,
   made by someone who read the row generously.
3. **Splitting the row was the alternative and is worse.** `03-acceptance-matrix.md` is the source of
   which checks exist; cutting `AB-G01-5` into two rows would edit the instrument to fit the state of
   the work, which is the wrong direction of travel. Section D already provides for a row that
   cannot turn green yet: justify it as a decision and mark it deliberately open.
4. **AP-3.1 is where the constraint first has teeth.** SP-E02-1 puts six components in one binary,
   which is exactly the situation a dependency contract is for — one artifact, and nothing in the
   compiler stopping the scheduler from reaching into the adapter. The contract is worth writing on
   the day the first module boundary is drawn, not eight stages earlier.

## Consequences

- `AB-G01-5` is `open` in `acceptance/registry.tsv` with this file as its evidence, the shape
  [the deferred two-person rule](two-person-rule.md) already uses for `AB-E08-1`.
- AP-0.1 is complete on its own terms: `02-work-packages.md` asks that `decisions/` hold eleven files
  with ruling, rationale and overturn condition, and `acceptance/g01-decisions.sh` measures that on
  every push rather than asserting it.
- AP-3.1 inherits an obligation it did not ask for. Whoever draws the first module boundary writes
  the dependency contract, extends `g01-decisions.sh` to check it, and turns `AB-G01-5` green.
- Until then the script names the gap in its own header, so the deferral is visible from the check
  rather than only from this file.

## Overturned by

AP-3.1 — the first work package with modules in it. At that point this deferral expires: the
dependency contract is written as a decision, the script learns to check it, and `AB-G01-5` turns
green through that run. Whoever reaches AP-3.2 with this file still standing has passed the overturn
condition without acting on it.
