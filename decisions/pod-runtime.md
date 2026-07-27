# Decision: how a workpod is actually run

**Status:** ruled · **Date:** 2026-07-27 · **Affects:** SP-T04-1, SP-T04-2, SP-T04-3, SP-T04-4,
SP-T04-5, SP-T03-1, SP-E02-3, SP-E02-4, AB-T04-2, AB-T04-3, AB-T04-5, AB-T03-1, AB-E02-4, AP-3.3.
Narrowed by AP-3.4 (the pipeline), AP-3.5 (the gates) and AP-5.2 (`image.build`).

T-04 says what is inside a pod and what is not, and in which states it may be. It does not say which
program creates the namespaces, where the pod's files sit, how long a pod may live, or what "quiet"
means when the panel writes "frozen after 45 s of quiet". Those are five gaps, and each of them would
otherwise become a constant somewhere in the runner that nobody can find again.

## Ruling

### 1. The runner drives `runc` directly; containerd's daemon is not in the path yet

SP-E02-3 rules "containerd with runc". The runner built in AP-3.3 speaks to **runc** — it writes an
OCI bundle and runs `runc create` · `runc start` · `runc pause` · `runc checkpoint` · `runc delete`.
The containerd daemon is not started and its client is not linked in.

The split is along what each of the two does. runc is the half that makes a pod a pod: namespaces,
the mount table, the seccomp filter, the cgroup. containerd is the half that *distributes* images —
a content store, a registry client, a snapshotter — and at stage 3 there is nothing to distribute:
no image is built (SP-T03-2's build agent is the `image.build` procedure of AP-5.2), no registry is
reachable (a work node has no egress but the gates of AP-3.5), and the image index of §5 below is a
directory on the work disk. Starting a daemon whose only job is to hand a local path to runc would be
a component with no work in it, and AB-E02-1 would have to count it.

**Overturned by** the first container image that arrives over the network. At that point the index
becomes containerd's content store, the runner drives containerd's client, and runc stays where it is
— underneath. gVisor for pods at level `public` (SP-E02-3's second sentence) arrives with AP-5.7 and
is a second value of the same setting, not a second runtime path.

### 2. `create` → write the contract → `start`

The resource contract of R-A is written **between** `runc create` and `runc start`, not after the
container is running. `runc create` makes the container and its cgroup and leaves the init process
stopped before it executes anything; that is the only moment at which `memory.high`, `cpu.weight`,
`pids.max`, `io.latency` and `memory.oom.group` can be in force for a pod's *first* instruction. A
contract written afterwards is a contract with a hole in it exactly where a runaway pod starts.

This is also what makes SP-T04-3's lifecycle real rather than descriptive: `created` is a container
that exists and has not run, which is a state runc has and a `docker run` does not.

### 3. The pod's paths are a contract, not a convention

| In the pod | What | Mode |
|---|---|---|
| `/work` | the working copy — a btrfs snapshot of the base, in O(1) (SP-T04-1) | read-write |
| `/harness/workpod` | the agent harness: the host's own binary (SP-E02-4) | read-only |
| `/run/workpod/job.json` | the job | read-only |
| `/run/workpod/harness.sock` | the only way out (SP-T04-2) | read-write |
| `/run/workpod/out/` | where the report is left | read-write |

The **patch is not in that table**, and its absence is a ruling of its own: it is computed on the
host, by `diff -ruN` between the base subvolume and the pod's snapshot, and written to
`/var/lib/workpod/patches/<pod id>.diff`. Both trees are outside the pod, so what a pod changed is
measured rather than claimed — a pod cannot hand out a patch that does not match what it did. It
lands on `/var` because it is the job's result and has to outlive the pod; the outbox of AP-3.5 is
where it goes from there.

Everything else under `/` comes from the image's read-only layers. `/harness` is deliberately a path
no image layer owns: SP-E02-4 says a harness update is an image update and not a rebuild of all
container images, and that is only true if the harness is mounted *beside* the image rather than
baked into it. The image's digest does not cover `/harness`, which is the mechanism AB-E02-4 measures.

The pod's own files live on the work disk and in `/run`:

```
/data/work/images/<digest>.json         an image manifest, content-addressed (SP-T03-4's shape)
/data/work/images/<digest>/             its skeleton — the mount points, shared read-only
/data/work/index/<requirement hash>     the image index — a hit is this file existing
/data/work/bases/<key>/                 a working-copy base subvolume
/data/work/pods/<pod id>/               the working copy: a snapshot of the base
/run/workpod/pods/<pod id>/             bundle, socket, lifecycle log, output
/var/lib/workpod/buildjobs/<hash>.json  a build job produced by a miss (§5)
```

### 4. Quiet, lifetime, and the idle limit

SP-T04-3 gives the freeze threshold — 45 s of quiet — and no definition of quiet. SP-T04-5 asks for a
lifetime and an idle limit and gives neither number.

**Quiet** is: the pod's cgroup consumed less than **1 % of one core** over the window, and no call
arrived on the harness socket in it. Two conditions rather than one, because either alone is wrong —
a pod spinning on a poll loop is not quiet even in silence, and a pod blocked on a model response
consumes no CPU but is not idle if it is talking to the host. The threshold is not zero because an
idle process is not a stopped one: a runtime that wakes a monitor thread every few milliseconds spends
a few tens of milliseconds per minute doing nothing, and a threshold of zero would mean no pod is ever
quiet.

**The lifetime** is the job's `pod_minutes` budget where it has one, and **60 minutes** where it does
not. A pod past its lifetime is reaped with the cause `budget.exhausted` — one of the state
contract's own `cause_code` values, because SP-K02-3 wants a cause and not a new word for one. The
budget pots themselves are AP-3.6's.

**The idle limit** is **15 minutes**. It is not the freeze threshold and must not be confused with it:
freezing is reversible and cheap — a frozen pod costs the compressed pages of E-05's `frozen_pod`
constant — while reaping ends the job. 45 s of quiet says "nothing is happening right now"; fifteen
minutes of it says "nothing is going to happen".

**A worker adopts nothing.** The reaper reaps every pod whose **supervisor process is gone** —
running, frozen or checkpointed — and after a worker restart that is every pod on the node. A pod
whose supervisor died has no one to deliver its patch to and no one holding its lease; leaving it
running would spend a node's memory on a result nobody will collect, and leaving its subvolume behind
is the failure mode T-04 calls "ten thousand orphaned subvolumes after a week". The job is not lost:
it returns to the queue when its lease expires (OP-4), which is V-02's mechanism and needs no
cooperation from the dead worker.

The supervisor is named by a pid file beside the pod, and the check is the pid *and* the command line
behind it. The pid alone would not do: pids are reused, and a reaper that spared a pod because an
unrelated process inherited its number would spare it forever — which is the orphan this whole
section exists to prevent, arriving by a longer road.

### 5. A miss produces a build job on the node; submitting it is not this work package's

SP-T03-1: a hit starts the pod, a miss is a build job. The runner refuses to start on a miss and
writes a build job to `/var/lib/workpod/buildjobs/<requirement hash>.json` — a complete
`HandWrittenJob` (`decisions/jobs-by-hand.md`) whose idempotency key is the requirement hash, so two
pods missing the same image produce one build job and not two.

It is written and not submitted. Submitting it means an envelope through intake, and a work node is
not a device: SP-T01-5 makes an unattributed sender produce no job, and building the attribution path
for a node would be inventing T-01's next panel here. The queue on `/var` survives a worker restart,
which is what it has to do until the `image.build` procedure of AP-5.2 drains it.

**The start budget.** SP-T03-1 says a hit starts in "~200 ms". The measured quantity is `create` →
`active`: from the first syscall of the runner's `Run` to the container's init process being started.
The ceiling is a **median of 250 ms over twenty starts**, with the machine recorded beside the number
the way `acceptance/e05-constants.tsv` records E-05's. A median rather than a maximum because the
first start after a boot pays for page cache the second does not, and twenty because that is enough
for a median to mean something and cheap enough to run on every image build.

## Rationale

1. **Every number here has a mechanism behind it, not a preference.** The lifetime is a budget, the
   idle limit is three orders of magnitude above the freeze threshold on purpose, the quiet threshold
   is set by what an idle runtime actually costs, and the start budget is the panel's own number with
   a statistic named.
2. **Reaping on start is the cheap half of the hardest failure.** T-04 calls the orphaned subvolume
   the most likely way a platform like this tips over, and the expensive half — a reaper that runs
   continuously against lifetimes and idle limits — only ever sees pods whose worker is alive. The
   sweep at start is the one that covers the case where it is not, and it is three lines: list what is
   on the disk, list what runc knows, delete the rest.
3. **runc rather than containerd is a smaller claim than it looks.** SP-E02-3 names both, and this
   ruling drops neither: runc is doing all the work the panel wants from it, and containerd is
   deferred to the day there is something for it to do. The overturn condition is a date the platform
   will reach, not a hypothetical.

## Overturned by

Named per section: §1 by the first image over the network, §4's numbers by AP-3.6's budget pots and
AP-3.7's measured admission, §5 by AP-5.2's `image.build` procedure. §2 and §3 fall only if the
runtime changes — a different OCI runtime keeps them, gVisor keeps them, a runner that is not a
container (`macos`, `remote`, AP-8.3) satisfies the same `Runner` contract and has paths of its own.
