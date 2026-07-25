# image/ — the build environment (AP-0.2)

Answers one question: **does the image build reproducibly?** No roles, no units, no kernel
requirements — those are AP-1.1.

```
mkosi.conf                  distribution, output, content, build
mkosi.conf.d/10-snapshot.conf   the pinned koji repository id (SP-E01-2)
build.sh                    builds twice, second pass offline, compares — this is AB-A03-2
```

```
./image/build.sh        # or: make image
```

## What makes a rebuild identical

Two mechanisms, and neither is optional:

| Mechanism | Where | Without it |
|---|---|---|
| `Snapshot=` pins the package set to one koji repository id | `mkosi.conf.d/10-snapshot.conf` | a rebuild resolves whatever is current; "does the same thing run on every node" is unanswerable |
| `SOURCE_DATE_EPOCH` clamps modification times to the commit being built | `build.sh` | the artifact depends on the day it was built |

The second pass runs with `--cache-only=always`, so the package manager cannot reach the network
(SP-A03-1). That is not a detail: if pass 2 could still fetch, an identical result would only mean
nothing changed upstream in the last minute.

## Bumping the snapshot

```
mkosi --directory image latest-snapshot     # prints the newest id
```

Write it into `10-snapshot.conf`, rebuild twice, compare, and commit the new id **with the
comparison in the message**. A bump changes the package set, so it changes the artifact — that is
expected. What must not change is that two builds of the same id are identical.

## What has been verified, and what has not

This configuration **has never been executed.** It was written on a host with no mkosi, no dnf, no
rpm and no root. Saying so is cheaper than letting the first run discover it.

Verified:

- Every option name and its config section against mkosi's own reference (`mkosi.1.md`), not from
  memory. `Snapshot=`, `CacheOnly=`, `ManifestFormat=`, `SourceDateEpoch=` and the CLI spellings
  `--output-directory`, `--package-cache-dir`, `--cache-only`, `--directory` all exist as used.
- The snapshot id `6679022` exists under `https://kojipkgs.fedoraproject.org/repos/f43-build/`.
- `build.sh` parses (`bash -n`) and its missing-mkosi path behaves.
- `.github/workflows/image.yml` parses as YAML.

Not verified, because it cannot be here:

- That the build succeeds at all.
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
