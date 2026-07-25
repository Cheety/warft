#!/usr/bin/env bash
# seal.sh — signs the seal record of a build (AP-0.2, SP-A03-1 `seal`, SP-A03-7, AB-A03-7).
#
#   image/seal.sh [record]        default: image/.build/image.seal, written by build.sh
#
# This is the one step that needs the private key, so it is the one step CI cannot do
# (decisions/signing-key.md). The normal path is therefore:
#
#   1. CI builds twice, compares, and publishes the seal record as the `hashes` artifact
#   2. gh run download <id> -n hashes -D /tmp/seal
#   3. image/seal.sh /tmp/seal/image.seal
#   4. commit image/seal/ — the next CI run rebuilds and verifies against it
#
# Signing a record rather than the image is what makes that work: the record is 400 bytes, it names
# the artifact hashes and the inputs that produced them, and a reproducible build (AB-A03-2) makes
# those hashes a function of those inputs. One signature therefore covers every later rebuild of the
# same revision — including the rebuild CI does to check it.
#
# Exit:  0 = sealed, and the signature verified against image/signing.crt on the spot
#        1 = anything else

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
RECORD="${1:-$HERE/.build/image.seal}"
CERT="$HERE/signing.crt"
KEY="${WARFT_SIGNING_KEY:-$HOME/.config/warft/signing.key}"
SEALED="$HERE/seal/image.seal"

command -v openssl >/dev/null 2>&1 || { echo "openssl is required" >&2; exit 1; }

if [ ! -f "$KEY" ]; then
  echo "no signing key at $KEY" >&2
  echo "  image/genkey.sh   generates it (once, not on a build machine)" >&2
  exit 1
fi
if [ ! -f "$CERT" ]; then
  echo "no certificate at $CERT — generate the pair with image/genkey.sh" >&2
  exit 1
fi
if [ ! -f "$RECORD" ]; then
  echo "no seal record at $RECORD" >&2
  echo "It is written by image/build.sh after two passes compared equal, and published by CI as" >&2
  echo "part of the 'hashes' artifact. An image that was never compared is not sealed." >&2
  exit 1
fi

head -1 "$RECORD" | grep -q '^# workpod image seal' \
  || { echo "$RECORD is not a seal record" >&2; exit 1; }

# A dirty tree describes an image no commit can reproduce, so the signature would name a revision
# that does not exist. Refusing here beats a verification failure nobody can explain later.
if grep -q '^# revision: .*-dirty$' "$RECORD"; then
  echo "the record was built from an uncommitted image/ tree:" >&2
  grep '^# revision:' "$RECORD" | sed 's/^/  /' >&2
  echo "Commit first, rebuild, then seal — a seal must name a revision that exists." >&2
  exit 1
fi

mkdir -p "$HERE/seal"
cp "$RECORD" "$SEALED"

# openssl asks for the passphrase on the terminal. WARFT_SIGNING_PASSIN takes any openssl -passin
# spelling (`file:`, `env:`, `fd:`) for the day this is driven from a password manager; it stays
# unset by default so that the normal path is a prompt and not a passphrase in a shell history.
if [ -n "${WARFT_SIGNING_PASSIN:-}" ]; then
  openssl dgst -sha256 -sign "$KEY" -passin "$WARFT_SIGNING_PASSIN" -out "$SEALED.sig" "$SEALED"
else
  openssl dgst -sha256 -sign "$KEY" -out "$SEALED.sig" "$SEALED"
fi

# Verifying immediately, with the committed certificate rather than the key, is not ceremony: it is
# the difference between "the file was signed" and "the file was signed by the pair in this
# repository". A mismatched pair fails here, not on the next CI run.
if ! openssl dgst -sha256 -verify <(openssl x509 -in "$CERT" -pubkey -noout) \
       -signature "$SEALED.sig" "$SEALED" >/dev/null 2>&1; then
  rm -f "$SEALED.sig"
  echo "the signature does not verify against $CERT — key and certificate are not a pair" >&2
  exit 1
fi

echo "== sealed"
grep '^#' "$SEALED" | sed 's/^/  /'
grep -v '^#' "$SEALED" | sed 's/^/  /'
echo
echo "  signature -> ${SEALED#"$HERE"/}.sig  ($(stat -c%s "$SEALED.sig") bytes)"
echo
echo "Commit image/seal/. The next run of image/verify.sh rebuilds and checks against it —"
echo "that run, not this one, is what evidences AB-A03-7."
