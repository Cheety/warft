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
| base | `internal/ids` | — | anything |
| base | `internal/attachment` | — | anything |
| base | `internal/allocation` | — | anything |
| base | `internal/runner` | — | anything |
| base | `internal/budget` | — | anything |
| step | `internal/disk` | `internal/boot` | another step, a role, `cmd` |
| step | `internal/selftest` | `internal/boot`, `internal/cgroup` | another step, a role, `cmd` |
| step | `internal/statedb` | `internal/boot`, `internal/budget`, `internal/ids` | another step, a role, `cmd` |
| step | `internal/outbox` | `api/workpodv1` | another step, a role, `cmd` |
| step | `internal/workpod` | `api/workpodv1`, `internal/allocation`, `internal/cgroup`, `internal/outbox`, `internal/runner` | another step, a role, `cmd` |
| role | `internal/adapter` | `api/workpodv1`, `internal/boot`, `internal/attachment`, `internal/ids`, `internal/outbox` | another role, a step, `cmd` |
| role | `internal/egress` | `api/workpodv1`, `internal/outbox` | another role, a step, `cmd` |
| role | `internal/gitgate` | `api/workpodv1`, `internal/outbox` | another role, a step, `cmd` |
| role | `internal/controlplane` | `api/workpodv1`, `internal/boot`, `internal/budget`, `internal/cgroup`, `internal/statedb`, `internal/attachment` | another role, a step, `cmd` |
| role | `internal/harness` | `api/workpodv1`, `internal/runner` | another role, a step, `cmd` |
| role | `internal/worker` | `api/workpodv1`, `internal/boot`, `internal/cgroup`, `internal/workpod` | another role, a step, `cmd` |
| entry | `cmd/workpod` | every module above | — |

Five of those "may not" lines carry the weight and are worth saying in prose:

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
- **The runner contract is a base module and its implementation is not.** SP-T04-4 says the
  abstraction is over `Runner`, not over `Workpod`, and the table is that sentence: `internal/runner`
  holds the contract and imports nothing, `internal/workpod` is one implementation of it and imports
  runc, cgroups and btrfs, and `internal/harness` — which runs *inside* a pod — sees the contract and
  never the implementation. The day `windows`, `macos` or `remote` arrives (AP-8.3) it is a second
  step beside `internal/workpod`, not a change to anything above it.
- **The outbox is below both gates, and neither gate owns it.** `internal/outbox` holds the domain
  key, the states an effect moves through and the register of SP-K03-4; `internal/gitgate` and
  `internal/egress` are two implementations of "a gate executes one" and import it, while the pod's
  host side (`internal/workpod`) imports it to record an intent and never to execute one. Putting
  the key in either gate would make the other gate's idempotency a favour from the first, and
  putting it in the worker would put it above the two things that need it most.
- **The budget is below the transaction that spends it.** `internal/budget` holds OP-1's caps, the
  shape of a refusal and the halt as it is read from a file, and it imports nothing — not even the
  database driver. The reservation itself is a transaction over `budget_pot` and belongs to
  `internal/statedb`, and the answer the sender gets belongs to `internal/controlplane`; both import
  the caps, neither owns them. The halt is the reason this matters: SP-E08-3's second path has to be
  readable when the state database is the part that is broken, so the module that reads it may not
  depend on the module that talks to it.
- **What two roles must agree on is a module, not a favour from one of them.** `internal/adapter`
  and `internal/controlplane` both enforce OP-5, and `internal/statedb` speaks only the state
  contract's words while `api/workpodv1` speaks the wire's. Neither role may import the other, so
  the shared halves sit below both: `internal/attachment` holds the ruling and the store, and the
  translation between wire and schema lives in the role that already knows both (`intake.go`). A
  base module is how two roles share a rule without one of them owning it.

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
- The scheduler and both gates are entry points that refuse until their work packages build them
  (AP-3.5, AP-3.7). Each arrives as a module and a row in this table, added in the commit that builds
  it — as `internal/adapter`, `internal/attachment` and `internal/ids` did in AP-3.2's, and as
  `internal/runner`, `internal/allocation`, `internal/workpod` and `internal/harness` did in
  AP-3.3's, which is the commit the harness stopped refusing in.

## Overturned by

A second artifact. This contract exists because SP-E02-1 puts everything in one binary; if that
ruling falls — E-02's own overturn condition — the boundaries become processes and are enforced by
the network instead of by this table. Short of that: any change to the ranks is a change to this
file first, and to `platform/` second.
