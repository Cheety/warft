# image/ — the system image

AP-0.2 answered one question here: **does the image build reproducibly?** AP-1.1 added the content —
the kernel requirements from SP-A02-2, the userland from SP-A02-3, and a bootable disk whose root is
read-only under dm-verity.

```
mkosi.conf                        distribution, output, content, build
mkosi.conf.d/10-package-pin.conf  the pinned Fedora release tree (SP-E01-2)
mkosi.repart/                     the partitions: ESP, erofs root, verity hash
mkosi.extra/                      what is copied into the image: the role generator, four targets
kernel-requirements.conf          the kernel configuration as a file (SP-A02-2, AB-E01-1)
tool-version                      the pinned mkosi version
build.sh                          builds twice, second pass offline, compares — this is AB-A03-2
vm.sh                             runs a script inside the built image; the door A-06 comes through
genkey.sh                         generates the signing pair, once, off the build machines
seal.sh                           signs a build's seal record — the only step that needs the key
verify.sh                         checks a build against the seal — needs no key. This is AB-A03-7
signing.crt                       the public certificate; in the boot path from AP-1.2
seal/image.seal[.sig]             what was sealed, and the signature over it
```

```
./image/build.sh              # or: make image
./image/verify.sh             # or: make verify
acceptance/e01-kernel.sh      # or: make image-acceptance — boots the build and checks it
acceptance/a02-roles.sh
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

The third file of the usual triple — a verity **signature** partition — is deliberately absent. It
needs the private key at build time, and by
[`decisions/signing-key.md`](../decisions/signing-key.md) that key is on no build machine. `AP-1.2`
signs the roothash and puts `signing.crt` into the boot path.

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

Still not established:

- That `AB-A02-1` is green. Everything it probes has now held in a run — same roothash under both
  roles, `/usr` unwritable, verity on the root — but the run that shows its script saying so is the
  next one.

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
