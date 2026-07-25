# image/ — the build environment (AP-0.2)

Answers one question: **does the image build reproducibly?** No roles, no units, no kernel
requirements — those are AP-1.1.

```
mkosi.conf                        distribution, output, content, build
mkosi.conf.d/10-package-pin.conf  the pinned Fedora release tree (SP-E01-2)
tool-version                      the pinned mkosi version
build.sh                          builds twice, second pass offline, compares — this is AB-A03-2
genkey.sh                         generates the signing pair, once, off the build machines
seal.sh                           signs a build's seal record — the only step that needs the key
verify.sh                         checks a build against the seal — needs no key. This is AB-A03-7
signing.crt                       the public certificate; in the boot path from AP-1.2
seal/image.seal[.sig]             what was sealed, and the signature over it
```

```
./image/build.sh        # or: make image
./image/verify.sh       # or: make verify
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

Checked without a run, before the first push:

- Every option name and its config section against mkosi's own reference, not from memory:
  `LocalMirror=`, `CacheOnly=`, `ManifestFormat=`, `SourceDateEpoch=`, and the CLI spellings
  `--output-directory`, `--package-cache-dir`, `--cache-only`, `--directory`.
- The pinned tree serves `repodata/repomd.xml` and has not moved since 2025-10-23.
- `build.sh` parses (`bash -n`); the missing-mkosi and version-mismatch paths behave.
- `.github/workflows/image.yml` parses as YAML.

Still not established:

- That two passes are bit-identical. **That is the whole point of AP-0.2**, and it is a property of
  a run. `AB-A03-2` stays red until the CI leg is green once.
- That the Fedora 43 package set suffices. The list is deliberately minimal; AP-1.1 sets the real
  content from SP-A02-2 and SP-A02-3.

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
