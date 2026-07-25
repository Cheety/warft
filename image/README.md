# image/ — the build environment (AP-0.2)

Answers one question: **does the image build reproducibly?** No roles, no units, no kernel
requirements — those are AP-1.1.

```
mkosi.conf                        distribution, output, content, build
mkosi.conf.d/10-package-pin.conf  the pinned Fedora release tree (SP-E01-2)
mkosi.version                     the pinned mkosi version
build.sh                          builds twice, second pass offline, compares — this is AB-A03-2
```

```
./image/build.sh        # or: make image
```

## What makes a rebuild identical

Three mechanisms, and none is optional:

| Mechanism | Where | Without it |
|---|---|---|
| `LocalMirror=` pins the package set to one frozen Fedora release tree | `mkosi.conf.d/10-package-pin.conf` | a rebuild resolves whatever is current; "does the same thing run on every node" is unanswerable |
| `SOURCE_DATE_EPOCH` clamps modification times to the commit being built | `build.sh` | the artifact depends on the day it was built |
| the mkosi version is pinned and checked before either pass | `mkosi.version`, enforced in `build.sh` | the build tool floats; a newer mkosi can lay out the same packages differently, and the comparison then says nothing |

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

The same holds for `mkosi.version`: bumping the tool is a bump like any other, and `build.sh`
refuses to run when the version on PATH is not the pinned one.

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

## Not delivered: the signature

`AB-A03-7` ("SBOM and signature — present and verified per image") is only half met. The SBOM is
covered: `ManifestFormat=json` writes a manifest of everything installed, and CI collects it.

The signature is not, and not by oversight. SP-A03-3 rules that the public key lies in the boot path
and **the private one not on the machine**, and AP-0.2's task list repeats it: *generate the signing
key; do not place the private part on a build machine*. Where that key lives instead is a decision
nobody can make from inside the repository — an offline host, a hardware token, or a signing service
the CI leg calls without ever holding the key. mkosi's `genkey` verb generates the pair, but where
the private half goes decides whether the signature means anything.

Until that is ruled and filed in `decisions/`, `AB-A03-7` stays red. Guessing it in code is exactly
what the specification forbids (V-05).
