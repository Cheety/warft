#!/usr/bin/env bash
# verify.sh — is the image in front of us the one that was sealed? (AB-A03-7, SP-A03-7)
#
#   image/verify.sh [record] [artifact-dir]   defaults: image/.build/image.seal, image/.build/pass1
#
# Needs no private key. That is the point: by decisions/signing-key.md the key is not on a build
# machine, so the check that runs on every build must get by with the committed certificate. It does,
# because a signature over the seal record plus a reproducible build (AB-A03-2) is enough to say that
# these artifacts are those artifacts.
#
# The three outcomes are deliberately not two:
#
#   0  sealed and verified — AB-A03-7 is evidenced by this run
#   2  unsealed: these build inputs have no signature yet. Normal during AP-1.1, where the image
#      changes on nearly every commit; demanding a re-seal per commit would make the seal an
#      obstacle to route around, which is how such mechanisms die. That an unsealed image reaches no
#      channel is SP-A03-1's gate and belongs to AP-6.4.
#   1  sealed and wrong: the signature is broken, the SBOM is missing, or — the interesting case —
#      the same inputs produced different artifacts. The last is a reproducibility failure that
#      build.sh cannot see, because it compares two passes within one run while this compares
#      against a run from another day on another machine.

set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
RECORD="${1:-$HERE/.build/image.seal}"
ARTIFACTS="${2:-$HERE/.build/pass1}"
CERT="$HERE/signing.crt"
SEALED="$HERE/seal/image.seal"
SIG="$SEALED.sig"

fail()     { echo "  FAIL      $*" >&2; exit 1; }
unsealed() { echo "  UNSEALED  $*" >&2; exit 2; }

echo "== seal (AB-A03-7)"

command -v openssl >/dev/null 2>&1 || fail "openssl is required"
[ -f "$RECORD" ]      || fail "no seal record at $RECORD — run image/build.sh first"
[ -d "$ARTIFACTS" ]   || fail "no artifacts at $ARTIFACTS — run image/build.sh first"

if [ ! -f "$CERT" ]; then
  echo "  no certificate at ${CERT#"$HERE"/}. The pair has not been generated yet:" >&2
  echo "    image/genkey.sh   once, on the machine that keeps the key" >&2
  unsealed "no signing key exists (decisions/signing-key.md)"
fi
if [ ! -f "$SEALED" ] || [ ! -f "$SIG" ]; then
  echo "  no image has been sealed yet. With a build in hand:" >&2
  echo "    image/seal.sh $RECORD" >&2
  unsealed "no seal in image/seal/"
fi

# 1 — the signature, before anything is read out of the sealed record. Everything below treats that
#     file as authoritative, which it only is once this passes.
if ! openssl dgst -sha256 -verify <(openssl x509 -in "$CERT" -pubkey -noout) \
       -signature "$SIG" "$SEALED" >/dev/null 2>&1; then
  fail "the signature over ${SEALED#"$HERE"/} does not verify against ${CERT#"$HERE"/}"
fi
echo "  signature verified against ${CERT#"$HERE"/}"

# 2 — the inputs. Different inputs mean a different image, and an unsealed one; they do not mean
#     something is broken. This is what keeps development off the failure path.
if ! diff <(grep '^#' "$SEALED") <(grep '^#' "$RECORD") > /dev/null; then
  echo "  the sealed image was built from other inputs:" >&2
  diff <(grep '^#' "$SEALED") <(grep '^#' "$RECORD") \
    | grep '^[<>]' | sed 's/^</    sealed:/;s/^>/    built: /' >&2
  echo "  Re-seal after downloading this run's image.seal:  image/seal.sh <path>" >&2
  unsealed "this revision is not sealed"
fi
echo "  inputs match:$(grep '^# revision:' "$SEALED" | cut -d: -f2-)"

# 3 — the artifacts. Recomputed from the files rather than read out of the record: a record checking
#     itself checks nothing. Same command as build.sh, so the two lists are comparable.
HASHES="$(cd "$ARTIFACTS" && find . -type f -exec sha256sum {} + | sort -k2)"
if ! diff <(grep -v '^#' "$SEALED") <(printf '%s\n' "$HASHES") > /dev/null; then
  echo "  same inputs, different artifacts:" >&2
  diff <(grep -v '^#' "$SEALED") <(printf '%s\n' "$HASHES") \
    | grep '^[<>]' | sed 's/^</    sealed:/;s/^>/    built: /' >&2
  fail "the build is not reproducible across runs (SP-A03-2 fails, not only the seal)"
fi
echo "  $(grep -vc '^#' "$SEALED") artifacts match the seal"

# 4 — the SBOM. Its hash is inside what was just verified, so "present and verified per image" is
#     already established for the file; what remains is that it is a bill of materials and not an
#     empty file that happened to be hashed.
SBOM="$(grep -v '^#' "$SEALED" | awk '{print $2}' | grep '\.manifest$' | head -1)"
[ -n "$SBOM" ] || fail "the sealed record names no SBOM — ManifestFormat= is not producing one"
[ -s "$ARTIFACTS/$SBOM" ] || fail "the SBOM $SBOM is missing or empty"
if command -v python3 >/dev/null 2>&1; then
  python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); sys.exit(0 if d.get("packages") else 1)' \
    "$ARTIFACTS/$SBOM" 2>/dev/null \
    || fail "the SBOM $SBOM does not parse as a manifest with a package list"
  echo "  SBOM ${SBOM#./}: $(python3 -c 'import json,sys; print(len(json.load(open(sys.argv[1]))["packages"]))' "$ARTIFACTS/$SBOM") packages, sealed"
else
  echo "  SBOM ${SBOM#./}: $(stat -c%s "$ARTIFACTS/$SBOM") bytes, sealed (not parsed: no python3)"
fi

echo
echo "AB-A03-7 green through this run: SBOM and signature present and verified for this image."
exit 0
