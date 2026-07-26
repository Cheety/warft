#!/usr/bin/env bash
# k04-authority.sh — authority as a Biscuit token, probed with the real Biscuit library (AP-2.4).
#
# Four rows rest on the Python this orchestrates (acceptance/k04-authority.py):
#   AB-K04-1  S  the authority is signed and verifiable offline
#   AB-K04-2  P  a widening attempt is cryptographically impossible
#   AB-K04-3  P  all three gates verify fully, none trusts the origin
#   AB-K04-6  P  the revocation list takes effect per project, even with the control plane down
#
# The probes run against biscuit-python, the reference implementation of the Biscuit spec — a
# hand-rolled signature scheme would prove nothing about widening but our own arithmetic. The host
# carries no toolchain and no pinned interpreter (SP-A02-3), so the library is pinned and installed
# in a throwaway container, exactly as k02-state.sh reaches for a Postgres it does not install on the
# host. The container is the build environment, not the image.
#
# The stage 2 boundary holds: this proves the cryptographic contract (contract/authority.md) and the
# verification path each gate will call. It builds no gate and no server. Keys are ephemeral, minted
# per run inside the probe; none is committed.
#
# Exit:  0 = no FAIL
#        1 = at least one FAIL

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PROBE="$ROOT/acceptance/k04-authority.py"

# The one place the library version is pinned. A reproducible probe needs a fixed dependency, the
# same reason the image pins its package snapshot (SP-E01-2).
BISCUIT_VERSION="0.4.0"
PYTHON_IMAGE="python:3.12-slim"

PASS=0; FAIL=0; SKIP=0
CTR=""
cleanup() { [ -n "$CTR" ] && docker rm -f "$CTR" >/dev/null 2>&1; }
trap cleanup EXIT

banner() { printf '\n\033[1m%s\033[0m\n' "$1"; }
skip() { printf '  \033[33mSKIP\033[0m  %-54s%s\n' "$1" "${2:-}"; SKIP=$((SKIP+1)); }

if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  banner "K-04 — authority as a Biscuit token"
  skip "K04 all checks" "no docker on this machine; the CI leg brings one"
  banner "Result"
  printf '  %d PASS · %d FAIL · %d SKIP\n\n' "$PASS" "$FAIL" "$SKIP"
  exit 0
fi

banner "K-04 — the Biscuit library (biscuit-python $BISCUIT_VERSION)"

CTR="k04-authority-$$"
docker run -d --name "$CTR" "$PYTHON_IMAGE" sleep 600 >/dev/null

# --no-cache-dir keeps the throwaway container lean; the pin makes the run reproducible.
if ! docker exec "$CTR" pip install --quiet --no-cache-dir "biscuit-python==$BISCUIT_VERSION"; then
  printf '  \033[31mFAIL\033[0m  K04-0 the library installs   biscuit-python==%s did not install\n' \
    "$BISCUIT_VERSION"
  banner "Result"
  printf '  0 PASS · 1 FAIL · 0 SKIP\n\n'
  exit 1
fi

# The probe is piped in over stdin — no bind mount of the slow working copy, and nothing written into
# the container image. Its own PASS/FAIL lines and exit code are the verdict this script relays.
docker exec -i "$CTR" python - < "$PROBE"
exit $?
