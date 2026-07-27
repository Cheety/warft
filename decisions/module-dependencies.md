# Decision: the module dependency contract of the platform binary

**Status:** ruled · **Date:** 2026-07-27 · **Affects:** SP-G01-5, SP-E02-1, SP-V01-1, SP-A04-2,
AB-G01-5, AP-3.1. Supersedes the deferral in [`module-contract.md`](module-contract.md).

SP-G01-5 asks for a module contract that is **machine-checkable and survives losing the database**:
"module A may depend on B and C, not on D". Until AP-3.1 there were no modules — SP-E02-1 puts the
control plane, the scheduler, the worker, the adapters, both gates and the harness into one Go
binary, and one binary is where such a contract is worth having, because nothing in the compiler
stops the worker from reaching into the control plane.

This is that contract. The table below is the source: `acceptance/module-contract.py` reads it out of
this file and holds `platform/` against it on every push, so the decision is the thing that is
checked rather than a description of something else that is.

## Ruling

**Every package of `platform/` stands in one of four ranks, and an import may only go to a lower
rank.** No edge exists that this table does not permit, and no package exists that it does not name.

| Rank | Module | May depend on | May not depend on |
|---|---|---|---|
| contract | `api/workpodv1` | — | anything |
| base | `internal/boot` | — | anything |
| base | `internal/cgroup` | — | anything |
| step | `internal/disk` | `internal/boot` | another step, a role, `cmd` |
| step | `internal/selftest` | `internal/boot`, `internal/cgroup` | another step, a role, `cmd` |
| step | `internal/statedb` | `internal/boot` | another step, a role, `cmd` |
| role | `internal/controlplane` | `api/workpodv1`, `internal/boot`, `internal/cgroup`, `internal/statedb` | another role, a step, `cmd` |
| role | `internal/worker` | `api/workpodv1`, `internal/boot`, `internal/cgroup` | another role, a step, `cmd` |
| entry | `cmd/workpod` | every module above | — |

Three of those "may not" lines carry the weight and are worth saying in prose:

- **The judge does not depend on the mechanism it judges.** `internal/selftest` may not import
  `internal/disk`. SP-A04-2 splits the two on purpose — the disk step mounts what it finds, the
  selftest decides whether the result is a node — and boot 4 of `acceptance/a04-boot.sh` probes
  exactly that split. An import would make the two agree by construction, which is the failure a
  selftest exists to prevent (SP-A04-3).
- **A role reaches another role over the wire or not at all.** `internal/worker` may not import
  `internal/controlplane` and the reverse holds too. V-02 is pull with a lease and V-01 lets the
  layers be nodes of their own; an in-process call between them would be an edge that survives only
  as long as they share a machine — and the day they do not, it is a rewrite rather than a
  deployment.
- **The contract does not reach back.** `api/workpodv1` is generated from `contract/platform.proto`
  (E-10) and imports nothing of this program. A schema that depends on its implementation is not one.

**A new module is a change to this table, in this file, in the same commit.** The check fails on a
package the table does not name — in both directions, the way `acceptance/registry.py` fails on
drift between matrix and registry. Silence is not the same as permission.

## Rationale

1. **The ranks are the ones the platform already has, not ones invented for the check.** Facts about
   the machine (the five boot values, the cgroup tree) sit below the steps of A-04, which sit below
   the roles of V-01, which sit below the entry point. That is the order the start sequence runs in
   and the order the panels are written in; the graph as built at AP-3.1 fits it without a single
   edge moved.
2. **The forbidden edges are the ones a single binary makes easy.** The whole rationale for a
   contract here is SP-E02-1: six components, one artifact, no linker to stop them. The three
   prohibitions above are the couplings that would be cheap to write and expensive to undo, and each
   one has a requirement behind it rather than a preference.
3. **The decision is the source, so it cannot drift from what is checked.** A separate
   configuration file would be a second place to state the same thing, and the two would disagree on
   the day someone edits one of them. Reading the table out of this file keeps V-05's "decisions live
   in Git, never in the database" true of the contract too — it is readable without the platform, and
   it survives losing the database because it is the file.
4. **Checking imports statically needs no toolchain.** `module-contract.py` reads Go import blocks;
   it does not build, download modules or need a compiler, so the decisions leg checks the contract
   in seconds and stays independent of the image leg that builds the binary.

## Consequences

- `AB-G01-5` turns green through a run of `acceptance/g01-decisions.sh`, which is what
  [`module-contract.md`](module-contract.md) deferred it for. Both halves are now evidenced by a run:
  the decision store and the module contract.
- The decisions leg has to re-run when the program changes: `platform/**` and
  `acceptance/module-contract.py` belong in its trigger paths, for the same reason the acceptance
  scripts are trigger paths of the image leg — a check that does not re-run rests on a run of the
  old program. A change that breaks the contract then fails the leg that owns the contract, not the
  one that owns the image.
- The scheduler, the adapters, both gates and the harness are entry points that refuse until their
  work packages build them (AP-3.2, AP-3.3, AP-3.5, AP-3.7). Each arrives as a module and a row in
  this table, added in the commit that builds it.

## Overturned by

A second artifact. This contract exists because SP-E02-1 puts everything in one binary; if that
ruling falls — E-02's own overturn condition — the boundaries become processes and are enforced by
the network instead of by this table. Short of that: any change to the ranks is a change to this
file first, and to `platform/` second.
