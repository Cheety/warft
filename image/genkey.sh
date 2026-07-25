#!/usr/bin/env bash
# genkey.sh — generates the image signing key pair (AP-0.2, SP-A03-3, decisions/signing-key.md).
#
# Run once, by hand, on the machine that keeps the key. Never in CI, and this script refuses there:
# the whole content of the ruling is that no build machine holds the private half, and a rule that
# depends on everyone remembering it is not a rule.
#
#   image/genkey.sh                        key to ~/.config/warft/signing.key, certificate to
#                                          image/signing.crt
#   WARFT_SIGNING_KEY=/path image/genkey.sh   key elsewhere
#
# openssl asks for a passphrase twice. Give one. With a single person there is no offline signing
# host and no hardware token, so the key sits on a machine its owner uses daily — encryption at rest
# is the difference between losing the machine and losing the root of trust.
#
# The same pair carries the dm-verity roothash in AP-1.2 (SP-A03-3), where signing.crt goes into the
# boot path. There is no second key for that.

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
CERT="$HERE/signing.crt"
KEY="${WARFT_SIGNING_KEY:-$HOME/.config/warft/signing.key}"
DAYS="${DAYS:-3650}"

if [ -n "${CI:-}${GITHUB_ACTIONS:-}" ]; then
  echo "genkey.sh does not run in CI." >&2
  echo "The private key must not be on a build machine (SP-A03-3, decisions/signing-key.md)." >&2
  exit 1
fi

command -v openssl >/dev/null 2>&1 || { echo "openssl is required" >&2; exit 1; }

# A key inside the working copy is one `git add -A` away from being published, and the default path
# is outside it for that reason. Someone overriding WARFT_SIGNING_KEY should not be able to undo it
# by accident.
TOP="$(git -C "$HERE" rev-parse --show-toplevel 2>/dev/null || true)"
KEYDIR="$(cd "$(dirname "$KEY")" 2>/dev/null && pwd || dirname "$KEY")"
case "${TOP:+$KEYDIR/}" in
  "$TOP"/*) echo "refusing to write the private key into the working copy: $KEY" >&2; exit 1 ;;
esac

if [ -e "$KEY" ]; then
  echo "a key already exists at $KEY" >&2
  echo "Replacing it invalidates every seal made with it. Move it away deliberately first." >&2
  exit 1
fi

mkdir -p "$(dirname "$KEY")"
chmod 700 "$(dirname "$KEY")" 2>/dev/null || true

echo "== generating an RSA-4096 pair, certificate valid $DAYS days"
echo "   private -> $KEY   (passphrase-encrypted, stays here)"
echo "   public  -> $CERT  (committed, and in the boot path from AP-1.2)"
echo

# umask 077 so the key is never briefly world-readable between being written and being chmod'ed.
# It catches the certificate in the same net, which is wrong in the other direction: that half is
# public, belongs in Git, and goes into the boot path in AP-1.2. Hence the explicit modes after.
( umask 077
  openssl req -x509 -newkey rsa:4096 -sha256 -days "$DAYS" \
    -keyout "$KEY" -out "$CERT" \
    -subj "/CN=Workpod image signing" \
    -addext "basicConstraints=critical,CA:FALSE" \
    -addext "keyUsage=critical,digitalSignature" \
    -addext "extendedKeyUsage=codeSigning" )

chmod 600 "$KEY"
chmod 644 "$CERT"

echo
echo "== certificate"
openssl x509 -in "$CERT" -noout -subject -enddate -fingerprint -sha256 | sed 's/^/  /'
echo
echo "Commit $CERT. Do not commit $KEY — it is outside the working copy so that you cannot."
echo "Next: seal a build with image/seal.sh."
