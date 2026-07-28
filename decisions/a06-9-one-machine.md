# Decision: AB-A06-9 is open, because no machine in this repository's CI is one machine

**Status:** ruled · **Date:** 2026-07-29 · **Affects:** AB-A06-9, AP-3.8, SP-A02-3, SP-RA-4,
SP-T04-1, E-11 step 3. Overturned by a node runner.

`AB-A06-9` reads "one job from envelope to patch, on one machine". The row was red and reported
itself as skipped, and the reason given — "this machine has no btrfs" — was not the reason: the pod
branch of `acceptance/b03-observation.sh` ran a job file nothing ever wrote, so the row could not
have passed on any machine. That is fixed; the chain now states the job by hand, imports an image,
makes a base and runs the order it admitted. What is left is a property of the machines, and it is
worth writing down rather than working around.

The row needs **one** machine carrying both halves of the chain:

| the pod half needs | the rest of the chain needs |
|---|---|
| btrfs, for the working copy in O(1) (SP-T04-1) | git, for the bare repository and the gate's `git apply` |
| runc, for the container (decisions/pod-runtime.md) | openssl, for the device certificate T-01's confidential channel rests on |
| the privilege to write the pod's cgroup (SP-RA-1) | python3, for reading the program's JSON back |
| a kernel with `io.latency` — `BLK_CGROUP_IOLATENCY` (SP-RA-4) | docker, for the Postgres the state contract is loaded into |

Neither machine this repository builds on has all eight.

**The build runner** has git, openssl, python3 and docker, and can be given btrfs, runc and the
privilege — the leg in `.github/workflows/platform.yml` does exactly that. Its kernel is where it
stops: a GitHub runner's kernel is built without `BLK_CGROUP_IOLATENCY`, so a pod's cgroup carries
`io.max`, `io.weight`, `io.prio.class` and `io.stat` and no `io.latency`. R-A's fifth knob has
nowhere to be written, and a pod without its resource contract is not a pod (SP-RA-4).

**The image** has the kernel — Fedora builds that controller in, which is why `AB-RA-4` is green
through `acceptance/t04-runner.sh` in a booted machine — and has none of the other four. That is not
an oversight: SP-A02-3 keeps toolchains off a node, and `image/mkosi.conf` carries no git, no
python3 and no openssl.

## Ruling

### 1. `AB-A06-9` is `open`, not `red`, and this file is its justification

`red` means not evidenced. `open` means deliberately open with a named reason, and that is what this
is: the check is written, it runs, and it is waiting for a machine rather than for work. The registry
row names this file, which is the mechanism `acceptance/registry.py` provides for exactly this case.

### 2. Nothing is relaxed to make it green

Three shortcuts were available and all three are refused:

- **Skipping `io.latency` on kernels that lack it.** That would put R-A's contract into the code as
  four knobs and a wish, on nodes as well as on runners, to make one row green on a machine that is
  not a node. SP-RA-4 says five.
- **Putting git, python3 and openssl into the image.** SP-A02-3 is a requirement about what a node
  carries, and a row in the acceptance matrix is not a reason to change a node's attack surface.
- **Claiming the row from two machines.** The row's words are "on one machine". A chain assembled
  from a pod in one place and a push in another is the thing it exists to rule out.

### 3. The chain runs everywhere it can, and says where it stopped

`acceptance/b03-observation.sh` checks four conditions before it runs a pod — btrfs, runc, the
privilege, and a kernel with `io.latency` — and names the one that failed. A machine that has all
four and then fails to run the pod **fails** the row rather than skipping it. The eight rows that do
not need a pod are unaffected and stay green.

## Rationale

1. **The row was never reachable, and the message said something else.** A skip that names the wrong
   cause is worse than a red: it looks like a machine problem someone else will solve. The check now
   states its own preconditions, so the next person reads the real one.
2. **A number of green rows are worth less than one honest open.** Q-02's whole claim is that
   evidence and not confidence decides. An `open` with the eight missing tools listed is a thing that
   can be closed by acquiring a machine; a green obtained by dropping `io.latency` is a claim about
   R-A that nothing behind it supports.
3. **The gap is one machine, and it already exists as a category.** A self-hosted runner that is a
   node — the image's kernel, with the four host tools beside it — closes this row and nothing else
   needs to change: `pod_machine()` will simply return true.

## Consequences

- `acceptance/registry.tsv` carries `AB-A06-9` as `open`, naming this file.
- `image/kernel-requirements.conf` gains `require BLK_CGROUP_IOLATENCY y`. The image's kernel already
  has it and `AB-E01-1` checks the file against the kernel, so what changes is that the dependency is
  stated where it is checked instead of being discovered by a pod failing between `runc create` and
  `runc start` — which is how it was discovered here.
- The work-disk step in `.github/workflows/platform.yml` stays. It cannot produce a pod on today's
  runner, and it is what makes the row green on the day the machine has the kernel, with no change
  to any check.

## Overturned by

A machine that is a node and carries git, python3, openssl and a state database beside it — a
self-hosted runner built from the image, or an equivalent host with the image's kernel. On that
machine `acceptance/b03-observation.sh` runs the pod, `AB-A06-9` turns green through that run, and
this file stops describing anything. Separately, any of the four host tools arriving in the image
would void the second half of the table — and would need its own ruling against SP-A02-3 first.
