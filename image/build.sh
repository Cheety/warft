#!/usr/bin/env bash
# build.sh — builds the image twice and compares the artifacts (AP-0.2, AB-A03-2).
#
# This is the acceptance of the build environment, not a convenience wrapper. It is written the way
# A-06 writes acceptance: the run decides, not the explanation.
#
#   pass 1   network allowed; resolves the pinned snapshot and fills the package cache
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
A="$WORK/pass1"
B="$WORK/pass2"

if ! command -v mkosi >/dev/null 2>&1; then
  echo "mkosi is not installed. It is the one build dependency of this repository (E-01)." >&2
  echo "  Fedora:  dnf install mkosi        Other:  pip install mkosi" >&2
  exit 1
fi

if [ -z "${SOURCE_DATE_EPOCH:-}" ]; then
  SOURCE_DATE_EPOCH="$(git -C "$HERE" log -1 --pretty=%ct 2>/dev/null || true)"
  [ -n "$SOURCE_DATE_EPOCH" ] || { echo "no commit to take SOURCE_DATE_EPOCH from; set it" >&2; exit 1; }
fi
export SOURCE_DATE_EPOCH

rm -rf "$A" "$B"
mkdir -p "$A" "$B" "$CACHE"

echo "== snapshot"
grep -h '^Snapshot=' "$HERE"/mkosi.conf.d/*.conf | sed 's/^/  /'
echo "  SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH"

echo "== pass 1 (network allowed, fills the package cache)"
mkosi --directory "$HERE" --output-directory "$A" --package-cache-dir "$CACHE" build

echo "== pass 2 (--cache-only=always, no network)"
mkosi --directory "$HERE" --output-directory "$B" --package-cache-dir "$CACHE" \
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
  echo
  echo "AB-A03-2 green through this run. Record it:"
  echo "  acceptance/registry.py  (set AB-A03-2 to green with this run as its evidence)"
  exit 0
fi

echo "  artifacts differ:" >&2
sed -n '3,40p' "$WORK/diff.txt" >&2
echo >&2
echo "AB-A03-2 stays red. A build that is not reproducible cannot answer whether the same thing" >&2
echo "runs on every node (SP-A03-2, SP-E01-2)." >&2
exit 1
