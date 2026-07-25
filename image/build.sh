#!/usr/bin/env bash
# build.sh — builds the image twice and compares the artifacts (AP-0.2, AB-A03-2).
#
# This is the acceptance of the build environment, not a convenience wrapper. It is written the way
# A-06 writes acceptance: the run decides, not the explanation.
#
#   pass 1   network allowed; resolves the pinned package set and fills the package cache
#   pass 2   --cache-only=always, so the package manager cannot reach the network at all
#            (SP-A03-1: "build (mkosi, without network)")
#   compare  every artifact of pass 1 against pass 2, byte for byte
#
# The second pass being offline is not a detail: if pass 2 could still reach the network, an
# identical result would only mean nothing changed upstream in the last minute.
#
# Exit:  0 = identical, AB-A03-2 is evidenced by this run
#        1 = differ, or a build failed
#
# SOURCE_DATE_EPOCH comes from the commit being built, so the timestamp is a property of the
# revision rather than of the day. Pass it in to override.

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
WORK="${WORK:-$HERE/.build}"
CACHE="$WORK/pkgcache"
BUILDCACHE="$WORK/buildcache"
A="$WORK/pass1"
B="$WORK/pass2"

if ! command -v mkosi >/dev/null 2>&1; then
  echo "mkosi is not installed. It is the one build dependency of this repository (E-01)." >&2
  echo "  git clone --branch v$(cat "$HERE/tool-version") https://github.com/systemd/mkosi" >&2
  echo "  and put its bin/ on PATH" >&2
  exit 1
fi

# The build tool is an input to the artifact. Pinning the packages while leaving mkosi floating
# would be half a pin: a newer mkosi can lay out the same packages differently and the comparison
# would then say nothing. Fedora 43 ships 25.3, which lacks settings this configuration uses.
#
# The pin does NOT live in `mkosi.version`, which reads like its natural home. That filename is
# magic to mkosi: it sets ImageVersion=. With the tool pin in it, the tool version silently became
# the image version and the artifacts came out named `workpod_26.raw`. The image version is a
# number the platform assigns (SP-A03-6, AP-6.4), not the version of the program that built it.
WANT="$(cat "$HERE/tool-version")"
HAVE="$(mkosi --version 2>/dev/null | grep -oE '[0-9]+(\.[0-9]+)*' | head -1)"
if [ "$HAVE" != "$WANT" ]; then
  echo "mkosi $WANT is pinned in image/tool-version, but mkosi $HAVE is on PATH." >&2
  echo "Two builds made with different tools prove nothing about reproducibility (SP-A03-2)." >&2
  exit 1
fi

# The image's revision is the last commit that touched image/, not the repository's HEAD. Taken from
# HEAD, a commit to a document moved SOURCE_DATE_EPOCH and with it every artifact hash — which would
# unseal the image (AB-A03-7) on commits that cannot have changed it.
REVISION="$(git -C "$HERE" log -1 --pretty=%H -- . 2>/dev/null || true)"
# An uncommitted change is a different image than any commit describes, and no seal can honestly
# match it. Saying so here beats a verification failure nobody can explain.
if [ -n "$REVISION" ] && [ -n "$(git -C "$HERE" status --porcelain -- . 2>/dev/null)" ]; then
  REVISION="$REVISION-dirty"
fi

if [ -z "${SOURCE_DATE_EPOCH:-}" ]; then
  SOURCE_DATE_EPOCH="$(git -C "$HERE" log -1 --pretty=%ct -- . 2>/dev/null || true)"
  [ -n "$SOURCE_DATE_EPOCH" ] || { echo "no commit to take SOURCE_DATE_EPOCH from; set it" >&2; exit 1; }
fi
export SOURCE_DATE_EPOCH

rm -rf "$A" "$B"
mkdir -p "$A" "$B" "$CACHE" "$BUILDCACHE"

PIN="$(grep -h '^LocalMirror=' "$HERE"/mkosi.conf.d/*.conf | cut -d= -f2-)"

echo "== pin"
echo "  LocalMirror=$PIN"
echo "  SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH"
echo "  revision=${REVISION:-unknown}"

# Two distinct caches, and mkosi wants both configured before it will accept CacheOnly=always
# (it checks config.cache_dir, not the package cache):
#   --package-cache-dir  the downloaded RPMs. Shared on purpose — it is what lets pass 2 be offline.
#   --cache-directory    the build cache. Incremental=no in mkosi.conf, so nothing of the built
#                        image is cached here; sharing it cannot let pass 2 reuse pass 1's result
#                        and trivially "prove" reproducibility.
echo "== pass 1 (network allowed, fills the package cache)"
mkosi --directory "$HERE" --output-directory "$A" \
      --package-cache-dir "$CACHE" --cache-directory "$BUILDCACHE" build

echo "== pass 2 (--cache-only=always, no network)"
mkosi --directory "$HERE" --output-directory "$B" \
      --package-cache-dir "$CACHE" --cache-directory "$BUILDCACHE" \
      --cache-only=always build

echo "== compare"
# Hash every artifact of both passes, relative to its own output directory, and diff the lists.
# Reported per file so a mismatch is diagnosable rather than merely known.
( cd "$A" && find . -type f -exec sha256sum {} + | sort -k2 ) > "$WORK/pass1.sha256"
( cd "$B" && find . -type f -exec sha256sum {} + | sort -k2 ) > "$WORK/pass2.sha256"

n=$(wc -l < "$WORK/pass1.sha256")
if [ "$n" -eq 0 ]; then
  echo "  pass 1 produced no artifacts — nothing was compared" >&2
  exit 1
fi

if diff -u "$WORK/pass1.sha256" "$WORK/pass2.sha256" > "$WORK/diff.txt"; then
  echo "  $n artifacts, bit-identical across both passes"

  # The seal record: what image/seal.sh signs and image/verify.sh checks (AB-A03-7). It is written
  # only after the comparison succeeded, because sealing an image that is not reproducible would
  # seal one of two possible results. Above the hashes stand the inputs that produced them, so the
  # signature binds artifacts, SBOM and build inputs in one file a person can read.
  #
  # The comment lines are part of what is signed. Keep them byte-stable: verify.sh compares a freshly
  # written record against the sealed one, so a reworded header reads as a changed image.
  { echo "# workpod image seal — SP-A03-7, AB-A03-7"
    echo "# revision: ${REVISION:-unknown}"
    echo "# source-date-epoch: $SOURCE_DATE_EPOCH"
    echo "# pin: $PIN"
    echo "# mkosi: $WANT"
    cat "$WORK/pass1.sha256"
  } > "$WORK/image.seal"

  echo
  echo "AB-A03-2 green through this run. Record it:"
  echo "  acceptance/registry.py  (set AB-A03-2 to green with this run as its evidence)"
  echo
  echo "Seal record written to $WORK/image.seal — AB-A03-7 continues from there:"
  echo "  image/verify.sh   is this build the sealed one? (needs no key)"
  echo "  image/seal.sh     sign this record (needs the key; not on a build machine)"
  exit 0
fi

echo "  artifacts differ:" >&2
sed -n '3,40p' "$WORK/diff.txt" >&2
echo >&2

# "They differ" is not actionable for a 198M blob. How *much* differs says what kind of problem it
# is: a handful of bytes is metadata that was not seeded, megabytes is layout or ordering.
echo "  where:" >&2
# cmp exits 1 when files differ, which is the normal case here. Under `set -e` with `pipefail` a
# command substitution wrapping it takes the script down with it, so each one ends in `|| true`.
while read -r f; do
  if [ ! -f "$B/$f" ]; then
    printf '    %-28s only in pass 1\n' "$f" >&2
    continue
  fi
  cmp -s "$A/$f" "$B/$f" && continue
  size=$(stat -c%s "$A/$f")
  first=$(cmp "$A/$f" "$B/$f" 2>/dev/null | sed 's/.*differ: //' || true)
  ndiff=$(cmp -l "$A/$f" "$B/$f" 2>/dev/null | wc -l || true)
  printf '    %-28s %s of %s bytes differ, first at %s\n' "$f" "$ndiff" "$size" "$first" >&2
done < <(cd "$A" && find . -type f | sort)
echo >&2

# A byte offset does not identify a cause; a path does. When a differing artifact is an erofs
# filesystem, unpack both and name the files inside that differ. Skipped without complaint when
# fsck.erofs is absent or the artifact is not erofs — this is diagnosis, not a second check.
if command -v fsck.erofs >/dev/null 2>&1; then
  while read -r f; do
    [ -f "$B/$f" ] || continue
    cmp -s "$A/$f" "$B/$f" && continue
    xa="$WORK/unpacked1"; xb="$WORK/unpacked2"
    rm -rf "$xa" "$xb"; mkdir -p "$xa" "$xb"
    fsck.erofs "--extract=$xa" "$A/$f" >/dev/null 2>&1 || continue
    fsck.erofs "--extract=$xb" "$B/$f" >/dev/null 2>&1 || continue
    echo "  inside $f:" >&2
    { diff -qr "$xa" "$xb" 2>&1 | sed "s|$WORK/unpacked1|pass1|;s|$WORK/unpacked2|pass2|;s/^/    /" \
      | head -30; } >&2 || true
    echo >&2
  done < <(cd "$A" && find . -type f | sort)
fi
echo "AB-A03-2 stays red. A build that is not reproducible cannot answer whether the same thing" >&2
echo "runs on every node (SP-A03-2, SP-E01-2)." >&2
exit 1
