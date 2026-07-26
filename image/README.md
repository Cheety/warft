# image/ — the system image

AP-0.2 answered one question here: **does the image build reproducibly?** AP-1.1 added the content —
the kernel requirements from SP-A02-2, the userland from SP-A02-3, and a bootable disk whose root is
read-only under dm-verity. AP-1.2 holds the result against A-06's list of thirteen.

```
mkosi.conf                        distribution, output, content, build
mkosi.conf.d/10-package-pin.conf  the pinned Fedora release tree (SP-E01-2)
mkosi.repart/                     the partitions: ESP, erofs root, verity hash
mkosi.extra/                      what is copied in: the role generator, four targets, zram
kernel-requirements.conf          the kernel configuration as a file (SP-A02-2, AB-E01-1)
tool-version                      the pinned mkosi version
build.sh                          builds twice, second pass offline, compares — this is AB-A03-2
vm.sh                             runs a script inside the built image; the door A-06 comes through
genkey.sh                         generates the signing pair, once, off the build machines
seal.sh                           signs a build's seal record — the only step that needs the key
verify.sh                         checks a build against the seal — needs no key. This is AB-A03-7
signing.crt                       the public certificate
seal/image.seal[.sig]             what was sealed, and the signature over it
```

```
./image/build.sh              # or: make image
./image/verify.sh             # or: make verify
acceptance/e01-kernel.sh      # or: make image-acceptance — boots the build and checks it
acceptance/a02-roles.sh
acceptance/a06-acceptance.sh
```

## What makes a rebuild identical

Three mechanisms, and none is optional:

| Mechanism | Where | Without it |
|---|---|---|
| `LocalMirror=` pins the package set to one frozen Fedora release tree | `mkosi.conf.d/10-package-pin.conf` | a rebuild resolves whatever is current; "does the same thing run on every node" is unanswerable |
| `SOURCE_DATE_EPOCH` clamps modification times to the commit being built | `build.sh` | the artifact depends on the day it was built |
| the mkosi version is pinned and checked before either pass | `tool-version`, enforced in `build.sh` | the build tool floats; a newer mkosi can lay out the same packages differently, and the comparison then says nothing |

The third one was not in the first draft. Fedora 43 ships mkosi 25.3, which has no `Snapshot=` at
all, so a CI run failed with `Unknown setting Snapshot`. Pinning the packages while leaving the tool
to the distribution was half a pin; the tool is an input to the artifact like any other. CI
therefore takes mkosi from its own repository at the pinned tag and installs the distribution
package only for its dependencies.

`Snapshot=` reads like the setting for the package pin, and it is — for rawhide only. mkosi accepts
it on Fedora solely with `Release=rawhide` (`mkosi/distribution/fedora.py`), which the next run
said in as many words. Rawhide is the development branch, so taking it would trade the reason for
choosing Fedora against a setting name. A released tree is already frozen — the repodata for 43 was
last written on 2025-10-23 — so `LocalMirror=` pointed at it is the same guarantee without rawhide.
It also yields exactly one repository, which is what "build without network" needs.

What that leaves out is the `updates` repository, the only moving part of a released Fedora. That is
the mechanism, not a gap: a pinned image does not silently gain packages. Security fixes arrive the
way A-02 and A-03 already deliver everything else — bump the pin, rebuild, compare, roll a new image
through the three channels.

The second pass runs with `--cache-only=always`, so the package manager cannot reach the network
(SP-A03-1). That is not a detail: if pass 2 could still fetch, an identical result would only mean
nothing changed upstream in the last minute.

## Bumping the pin

Point the URL in `10-package-pin.conf` at the new release tree, rebuild twice, compare, and commit
it **with the comparison in the message**. A bump changes the package set, so it changes the
artifact — that is expected. What must not change is that two builds of the same pin are identical.

The same holds for `tool-version`: bumping the tool is a bump like any other, and `build.sh`
refuses to run when the version on PATH is not the pinned one.

That pin is deliberately **not** in a file called `mkosi.version`, which is where it started. mkosi
treats that filename as magic and reads it as `ImageVersion=`, so the pinned tool version became the
image version and the first green run produced `workpod_26.raw`, `workpod_26.manifest` and
`workpod_26.root-x86-64.raw`. Nothing broke — the two passes still matched — but the image version
is a number the platform assigns per SP-A03-6, not the version of the program that built it. The
artifacts are now named `workpod.*` and stay that way until AP-6.4 gives them a real version.

## The content (AP-1.1)

### The kernel is checked, not written

E-01 takes the Fedora kernel, so `kernel-requirements.conf` is not a `.config` that gets compiled: it
is the list of options the image is held to, and `acceptance/e01-kernel.sh` reads the kernel's own
configuration out of the booted image and compares. That reading of `AB-E01-1` is ruled in
[`decisions/kernel-configuration.md`](../decisions/kernel-configuration.md), together with the
second half — SP-A02-2's "Out: unused filesystems, sound, legacy drivers", which with a distribution
kernel is a matter of which modules reach the image rather than which options were set.

The mechanism for "Out" is `KernelModules=` in `mkosi.conf` plus the package list, which takes
`kernel-core` and `kernel-modules-core` and leaves `kernel-modules` and `kernel-modules-extra`
out. Requirement and mechanism live in two files on purpose: when the mechanism forgets one, the
module is in the image and the check says so.

Excluding a directory does not exclude everything in it. mkosi filters the module tree and then
resolves dependencies over what survived, and that second pass puts a filtered-out module straight
back when a survivor depends on it. Run 21 found `parport` in an image that excludes
`kernel/drivers/parport`: `lp`, `ppdev`, `i2c-parport` and `pps_parport` sit in three other
directories and each names it. The requirements file therefore has a second keyword — `absent
<module>`, which says about one driver what `exclude` says about a place — and the four are named
there as well as excluded in `mkosi.conf`. With the last dependent gone, `parport` is dropped
instead of dragged back.

### Verity, and what the ESP is for

The root is erofs with a verity hash partition next to it (`mkosi.repart/10-root.conf`,
`20-root-verity.conf`). mkosi puts the resulting roothash on the kernel command line of the unified
kernel image, so the boot path carries the hash of the content it is about to mount, and every block
is checked as it is read. The ESP is the only writable partition and cannot be used to change what
runs: a modified root stops matching the hash.

The third file of the usual triple — a verity **signature** partition — is absent, and after AP-1.2
that is an open question rather than a plan. It needs the private key at build time, and by
[`decisions/signing-key.md`](../decisions/signing-key.md) that key is on no build machine; the
offline shape that works for the seal does not transfer, because a kernel only enforces a signed
roothash against a certificate in its own keyring, and E-01 takes Fedora's kernel as it comes. So
SP-A03-3's second sentence — "the public key lies in the boot path" — is not evidenced by any run
yet. No matrix row asks for it: `AB-A03-3` and `AB-A06-7` both ask that a damaged image not start,
and AP-1.2 evidences that with the drill below. Placing the signature is work that begins with a
ruling in `decisions/`, not with a partition.

### Four roles, one artifact

`SP-A02-1` gives a node one boot variable. It arrives as a systemd credential — the form E-01 rules
for all five values from A-04 — and `workpod-role-generator` turns it into a single symlink under
`/run/systemd/generator`, wanting `workpod-<role>.target` and nothing else. That is the whole of
SP-A04-2's `role` step, and it is what SP-A03-5 means by "the role may not change the content, only
the activated units": the generator writes to `/run`, and `/usr` is read-only under verity, so a
role that tried to write to the image would fail against the kernel rather than against a rule.

`acceptance/a02-roles.sh` boots the same artifact as `control` and as `work` and holds the two
against each other — same roothash, different target active, `/usr` unwritable in both.

### Booting a check into the image

`vm.sh` runs a script inside the built image and hands back its exit code. The script travels as a
systemd credential over SMBIOS, the same door a node's boot values come through, and
`systemd.run=` on the runtime-appended command line starts it. Nothing is added to the image to
make this work, which is the point: the artifact under test does not carry its own test. AP-1.2
brings `a06-acceptance.sh` in the same way.

Two consequences show up in the guest: the boot stops short of `multi-user.target`, because
`systemd-run-generator` points `default.target` at its own target, and the image is booted
ephemerally so the firmware's writes to the ESP never touch the sealed artifact.

Until AP-3.1 builds the disk layout from A-05, the image has no data partition and boots with
`systemd.volatile=state` — `/var` on tmpfs, the root still read-only. A node keeps nothing across a
reboot yet.

## The list (AP-1.2)

`acceptance/a06-acceptance.sh` is section A of the acceptance matrix, thirteen rows, and it was
written before the image was. AP-1.2 is where it is run against one. Eight rows can be evidenced by
an image on its own; five need the platform binary and report as open, each naming the work package
that closes it. Two more rows travel with the list because their own panels put them there —
`AB-K04-7` (time is infrastructure) and `AB-B01-2` (an image is public).

Most of it runs in the machine, through the same door as the other two checks. Three things about
it are worth reading before the script:

**Two rows are decided on both sides.** "Inventory against SBOM" (`AB-A06-6`) is a comparison, so
both halves are looked at: the machine says what it carries, the bill of materials next to the
artifact says what was put in, and the row is green only when neither names a compiler, an
interpreter, a package manager or an editor. `AB-A06-7` is the same shape — verity carrying the root
is a property of the running machine, and "a damaged image does not start" is a property of an
artifact that has been damaged on purpose.

**The drill damages a copy.** 512 bytes of noise go over the erofs superblock, 1024 bytes into the
root partition — the one block that is certainly read, so the failure is a fact about the boot and
not about which blocks happened to be touched. dm-verity sits under the filesystem and hashes every
block as it is read, so the corruption is caught before erofs sees it. The machine that results has
nowhere to go — no getty, no SSH — so it waits until the timeout kills it, and the verdict is
therefore not an exit code: it is that the check never ran *and* the console names the block
dm-verity refused. The artifact itself is untouched; `AB-A03-7`'s seal is over exactly those bytes.

**Two measurements needed something the image does not have yet.** `AB-A06-2` measures a reflink
snapshot, and there is no `/data/work` until AP-3.1 builds A-05's layout — a tmpfs cannot reflink,
so the row would have been a skip. `vm.sh --disk` gives the machine an empty second disk instead;
the check makes a btrfs on it, writes a gigabyte, and measures three things rather than two: the
snapshot's time, the disk it cost, **and** the time and disk a real copy of the same file costs. The
third is what makes the second a measurement instead of a threshold — an instrument that can see a
gigabyte arrive and does not see one after the snapshot is saying something. `AB-A06-4` needed zram
to exist at all: `zram-generator` and a configuration in `mkosi.extra` now put it in the image, with
zstd said explicitly, because the kernel's own default is lzo-rle and the compression factor is one
of the five constants AP-1.3 measures.

What this list does not evidence is the second half of `AB-A06-7`'s sentence, "B takes over": there
is one system slot in the image. A/B and its boot counter (SP-A03-4) arrive with the disk layout in
AP-3.1 and the channels in AP-6.4. The half that is evidenced is the half A-03 calls "verity or
nothing at all", and it is what `AB-A03-3` asks for in as many words.

Landlock is the one wall of `AB-A06-3` that is read off the kernel rather than probed by a failing
action. User namespaces and seccomp are probed: a process that is root inside its namespace cannot
read a root-owned file outside it, and a syscall a `SystemCallFilter=` blocks fails where the same
syscall succeeds unfiltered. Landlock is applied by a sandboxed process to itself through a syscall
— there is no pod to sandbox until the runtime exists, and no compiler in the image to write one,
which SP-A02-3 intends. What the run does establish is that the kernel has it active in its LSM
list, which a configuration symbol alone does not say.

## The calibration (AP-1.3)

`acceptance/calibration.sh` is A-06's last row and the measurement stage 1 ends with: 500 pods
created, 20 of them active, and the five constants of E-05 measured instead of assumed. It boots the
same artifact through the same door as the other three checks, and it is the first one that needs a
different machine — `vm.sh --memory` and `--cpus` exist for it. 2048 MB and two cores hold a check;
they do not hold a fleet.

**A pod at this stage is a cgroup with a shell in it,** created through `systemd-run` in
`workpod-pods.slice` — the mechanism SP-A02-4 gives R-A and R-C, and the same slice the acceptance
list reads pressure from. It carries no harness, no container image and no runtime, because none of
those exist before AP-3.1. That is not a gap in the run, it is the reason two of the numbers it
produces are floors and say so.

**Three slices, because the fleet has three parts that are measured apart from each other.** The
twenty active pods get their class's `cpu.weight` and their class's tolerated limit as `memory.high`,
in the mix E-05 states — 4 tiny, 6 small, 7 medium, 3 large, which is 20/30/35/15 over twenty. Of the
480 frozen pods, half hold nothing but themselves, so the marginal cost of the mechanism has no size
chosen by the script in it; the other half hold a stated megabyte of the image's own unit files, so
the compression factor has pages with content to work on. zram compresses page by page, which is why
a seed repeated across pages does not flatter that factor, and why the seed is configuration text
rather than `/dev/urandom` — noise compresses at 1.0 and would make the factor a fact about the
random device.

**The factor is measured where R-D uses it.** Frozen pods lie compressed and active pods lie hot, so
the occupancy table divides the frozen pod's pages by the factor and nothing else. The two frozen
slices are frozen with one write each — the freezer is hierarchical, which is what makes "freeze the
fleet" one decision instead of 480 — and then reclaimed with `memory.reclaim`, and what zram received
is read off the device. Freezing stops a pod's tasks; it does not pin their pages.

**Four of the five constants are adopted, and the fifth is the panel's own exception.** E-05's table
names where each is measured: four say "A-06", and the active pod says "three runs per repository
(R-C)". There is no job at stage 1, so the run measures what the mix cost on the machine it had,
records it, and leaves the table's 960 MB and 0.8 cores in place until AP-3.7. The twenty active pods
together request 19.2 GB — four times what a runner can give a guest — so the load runs the mix at
1/8 of each class request and prints the fraction with every number it produces.

**The pressure event at the end is OP-6's.** One pod under a low `memory.high` until `memory some
avg10` crosses 10 %, then released, and the decay timed to three thresholds. `avg10` is an
exponential average over ten seconds and cannot fall faster than its own window, so the decay is what
a hold time has to clear — which is the value §19 left open and proposed deriving from exactly this
run.

**The numbers land in two files and the run holds them against each other.** `decisions/E-05.md` is
the ruling; `acceptance/e05-constants.tsv` is the same numbers in the shape R-D reads them in. A
number in the table that is not in the ruling is drift, and a fresh measurement more than a factor of
two from the recorded one stops the run instead of being averaged into it. A factor of two rather
than a percentage: past that it is a different machine or a broken measurement, and both need a
person.

The host side of the script can be replayed without a machine — `CAL_REPLAY=<dir>` composes the table
from a saved console log — because everything after the two boots is arithmetic over marker lines,
and arithmetic that has never run over real output is a guess.

## What the runs have established

This configuration was written on a host with no mkosi, no dnf, no rpm and no root, so nothing here
was proven by reading it. Every correction below came from a red CI run, which is the argument for
having the run at all.

| Run | Failure | What it established |
|---|---|---|
| 1 | exit 126, `Permission denied` | `core.fileMode=false` on a Windows working copy, so `chmod +x` never reached the index. `acceptance/registry.py` had the same defect and would have broken `make acceptance` on any fresh clone. |
| 2 | `Unknown setting Snapshot` | Fedora ships mkosi 25.3; the setting arrived in v26. The tool needed pinning too. |
| 3 | GPG key not found | `--setopt=install_weak_deps=False` had removed mkosi's own toolchain. SP-A02-3 governs the image, not the container building it. |
| 4 | `Snapshot= is only supported for rawhide` | The package pin had to be a frozen release tree via `LocalMirror=`, not a koji snapshot. |

Checked without a run, before the first push of AP-0.2:

- Every option name and its config section against mkosi's own reference, not from memory:
  `LocalMirror=`, `CacheOnly=`, `ManifestFormat=`, `SourceDateEpoch=`, and the CLI spellings
  `--output-directory`, `--package-cache-dir`, `--cache-only`, `--directory`.
- The pinned tree serves `repodata/repomd.xml` and has not moved since 2025-10-23.
- `build.sh` parses (`bash -n`); the missing-mkosi and version-mismatch paths behave.
- `.github/workflows/image.yml` parses as YAML.

And before the first push of AP-1.1, against the pinned tree and mkosi v26's own source rather than
from memory:

- Every symbol in `kernel-requirements.conf` against Fedora's `kernel-x86_64-fedora.config`. All are
  set; `CONFIG_SECCOMP_FILTER` is the one that is selected rather than written, and appears only in
  the built configuration the image ships.
- Which subpackage each required module comes from: all of them are in `kernel-modules-core`, and
  `kernel-core` carries no modules at all — which is why the package list names both.
- That every package named exists in the pinned release tree, not only in `updates`.
- `KernelModules=` semantics: a list of nothing but exclusions would empty the module tree, because
  mkosi keeps what a positive pattern matched. Hence the leading `*`.
- `Credentials=` takes a directory and names each credential after its file — and runs the file
  instead of reading it when it is executable, which is why `vm.sh` writes them mode 0644.
- That `systemd.run="…"` is quoted the way `systemd-run-generator(8)` documents, that the generator
  redirects `default.target`, and that generators are given `$CREDENTIALS_DIRECTORY`.
- The check scripts parse (`bash -n`), and `e01-kernel.sh` was run against a synthetic module tree
  in both directions: a missing alternative and a module that should not be there both fail it.

Then the runs that boot it. AP-1.1's "done when" is a boot, so every one of these is a correction
that only a booted machine could have produced:

| Run | Failure | What it established |
|---|---|---|
| 16 | `2 of 536870912 bytes differ` | Adding a kernel and a verity pair reopened reproducibility — and a comparison that counts bytes cannot say what to fix. |
| 17 | the same two bytes, now dumped | They were the creation and write times of the ESP's `EFI` volume label entry. dosfstools 4.2 ignores `SOURCE_DATE_EPOCH`; `--invariant` is its own answer. |
| 18 | `--ephemeral: expected one argument` | The boot never started. |
| 19, 20 | ten minutes, no output at all | `mkosi vm` registers the machine with systemd-machined, and a build container runs no systemd. Nothing was listening to the console either. |
| 21 | exit 125 after a clean check | The image boots, and both defects below were only visible because it does. |
| 22 | `the run as 'control' reported no roothash` | `AB-E01-1` green, 48 of 48. Both role boots passed 8 of 8 with the identical roothash — and the host read neither, because the probe's marker went through the journal and arrived prefixed. Run 21's trailer defect, one level up. |

Run 21 is where the image first ran a check of its own. It established three things:

- **The image boots.** The machine came up under dm-verity, took the check in as a credential, ran
  it, printed 48 results and powered itself off — in seven seconds.
- **The build survived its content.** Pass 1 and pass 2 stayed bit-identical with a kernel, a
  bootloader and a verity hash partition in the image, which run 16 had shown was not free.
- **47 of 48 requirements were met**, and the one that was not is a real finding rather than a
  broken check: `parport`, dragged back past its own exclusion by four dependents in other
  directories.

Two defects, both fixed since:

| Defect | Why it mattered |
|---|---|
| The exit trailer went through the journal, which prefixes and rate limits it. The host looked for the line at the start of a line and found `[    6.956905] bash[378]: WORKPOD-EXIT: 1`. | A check that runs correctly and reports correctly still failed the run. The verdict now goes to `/dev/console` directly; only diagnostics use the journal. |
| The failing exclude said "1 modules still in the image" and not which one. | The mechanism was three directories away from where it looked. Naming it is the same correction run 17 made for the byte comparison. |

The module fix was checked before it was pushed, the same way everything else here is checked
without a build machine: mkosi v26's own `filter_kernel_modules` and its dependency walk, replayed
over `kernel-modules-core` and `kernel-core` as the pinned tree ships them. Unfixed it reproduces
run 21 exactly — 2177 modules survive the filter, 2178 reach the image, and the extra one is
`parport`. With the four dependents excluded it is 2173 and 2173, and nothing is dragged back past
the filter at all.

Run 22 sharpened the journal lesson into a rule: **a marker the host parses goes to `/dev/console`;
the journal is for people.** vm.sh's exit trailer learned this from run 21; the roothash marker in
`a02-roles.sh` had the same defect and run 22 found it the same way — a probe that passed inside
the machine and a host that could not see it. Both markers now write to the console with a journal
fallback, and both host-side matchers tolerate a prefix, so the fallback path is still read.

[Run 23](https://github.com/Cheety/warft/actions/runs/30181410890) closed AP-1.1: `AB-E01-1` at
52 met, 0 not — the four `absent` rows among them, `parport` gone — and `AB-A02-1` with both roles
at 8 met, 0 not, the identical roothash on both boots, and the host now able to read it. The image
boots in a VM with / read-only under dm-verity, which is the work package's "done when", said by a
run rather than by this file.

Then the list itself (AP-1.2). [Run 24](https://github.com/Cheety/warft/actions/runs/30200264202)
reported eight green, five open, no red on its first attempt — and two of those verdicts were worth
less than they looked:

| Run | What passed but should not have | What it established |
|---|---|---|
| 24 | the drill's evidence was `device-mapper: verity: sha256 using "sha256-lib"` | The module logs that string when it loads, on every healthy boot. The pattern was `verity`, so any hung boot would have matched it. A check has to anchor on the refusal, not on the mechanism being present — and the damaged machine's console went to a file that was then thrown away, so the verdict could not be checked at all. |
| 24 | ninety seconds of nothing before the first check | The wait was on `systemctl is-system-running`, which cannot report `running` here by construction: it stays `starting` until the initial transaction is done, and the check is a unit inside that transaction. It was waiting for itself. |

[Run 25](https://github.com/Cheety/warft/actions/runs/30200758783) is the one the eleven rows are
green through. The same eight and five, and now with the numbers behind them:

- **reflink is O(1) and it is measured against something.** 4 ms and +0 MB for a 1 GB snapshot,
  against 1103 ms and +1026 MB for a real copy of the same file on the same filesystem. `btrfs
  filesystem du` reports the snapshot as 1073741824 bytes total, **0 exclusive**.
- **CRIU works on this kernel**, which is the row all of E-11's stage 1 hangs on: `criu check`
  green, a sample process dumped at count 10 and gone, restored under the same pid, counting again
  at 20. E-01's overturn condition was not reached.
- **zram selected zstd**, not the kernel's lzo-rle default: `lzo-rle lzo lz4 lz4hc [zstd] deflate
  842`, 487 MB, in use as swap. The compression factor stays open — it is AP-1.3's to measure.
- **The damaged image did not start.** `device-mapper: verity: 253:2: data block 0 is corrupted`,
  then emergency mode, then `Cannot open access to console, the root account is locked` — a node
  with no way in is A-04 working, not A-04 failing. The check never ran, which is the other half of
  the verdict.
- **The boot settles in 7.8 s** now that the wait is on a target rather than on itself.

## The seal: SBOM and signature (AB-A03-7)

The SBOM is `ManifestFormat=json` — a manifest of everything installed, written per image. The
signature is over a **seal record**, not over the image, and that choice is what lets CI check it
without ever holding a key:

```
# workpod image seal — SP-A03-7, AB-A03-7
# revision: 9f04…            the last commit that touched image/
# source-date-epoch: 1753…
# pin: https://dl.fedoraproject.org/pub/fedora/linux/releases/43/…
# mkosi: 26
4180…  ./workpod.manifest    the SBOM
9f3a…  ./workpod.raw
41ad…  ./workpod.root-x86-64.raw
```

400 bytes, readable, and it binds the artifacts, the SBOM and the inputs that produced them into one
signable file.

The revision is the last commit touching a **build input** — `mkosi.conf`, `mkosi.conf.d/`,
`mkosi.repart/`, `tool-version`, `build.sh` — and that allowlist is not fussiness. The seal lives in
`image/seal/` and the certificate in `image/signing.crt`, so a revision meaning "the last commit
touching `image/`" would be changed by the act of committing the seal: the seal would invalidate
itself the moment it was recorded, a fixed point that can never be reached. The same argument, less
sharply, covers this README and the three scripts — none of them can change the artifact, so none of
them may unseal it. A new build input that is not added to the list shows up as `verify.sh` exiting
1 rather than as nothing at all. Because the build is reproducible (`AB-A03-2`), those hashes are a function of those
inputs — so a signature made once covers every later rebuild of the same revision, including the
rebuild CI does to check it. The two rows hold each other up: when the build stops being
reproducible, the seal stops verifying.

Where the private key lives is ruled in [`decisions/signing-key.md`](../decisions/signing-key.md):
one X.509 pair, generated by the owner, passphrase-encrypted, outside the working copy, never a CI
secret. `genkey.sh` refuses to run under `CI`, `verify.sh` takes no key, and no workflow reads a
signing secret — the constraint can be read off the repository instead of being remembered.

### Sealing a build

```
image/genkey.sh                             once, ever; commit image/signing.crt
gh run download <id> -n hashes -D /tmp/seal   the record CI published
image/seal.sh /tmp/seal/image.seal            sign it; commit image/seal/
```

The next CI run rebuilds and verifies against it — **that** run evidences `AB-A03-7`, not the seal
step. Sealing is deliberate work with the same shape as bumping the pin: build, compare, sign,
commit with the run named in the message.

### The three outcomes of `verify.sh`

| Exit | Means | CI |
|---|---|---|
| 0 | sealed and verified | `AB-A03-7` evidenced by this run |
| 2 | unsealed — these inputs have no signature yet | reported, does not fail the build |
| 1 | the signature is broken, the SBOM is not a bill of materials, or the same inputs produced different artifacts | fails |

Unsealed is not a failure on purpose. During AP-1.1 the image changes on nearly every commit, and
demanding a re-seal per commit would make the seal an obstacle to route around, which is how such
mechanisms die. That an unsealed image reaches no channel is SP-A03-1's gate, and it is built in
AP-6.4 where the channels are.

Exit 1 on "same inputs, different artifacts" is worth its own line: `build.sh` compares two passes
inside one run, while `verify.sh` compares against a run from another day on another machine. That
is the stronger claim of the two, and it is free.
