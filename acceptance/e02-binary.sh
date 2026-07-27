#!/usr/bin/env bash
# e02-binary.sh — one Go binary (AB-E02-1, SP-E02-1, E-02).
#
#   acceptance/e02-binary.sh          build (or take the staged build) and hold it to the row
#
# The row reads: control plane, scheduler, worker, adapter, both gates, harness from one
# artifact. Three things are checked, and the third is the one that keeps the row honest:
#
#   one file          a single ELF, statically linked — no runtime on the host (SP-A02-3)
#   seven entries     every component of the row answers from this one artifact
#   honest refusals   a component a later work package builds refuses with that package's name
#                     and exit 69, instead of pretending (Q-02). AB-E02-1 wants the surface in
#                     one artifact — not the stages 3.2…3.7 delivered early, and not faked.
#
# The same artifact goes into the image: build.sh stages it for mkosi, and a04-boot.sh compares
# the hash the booted node reports against the file this script checked.
#
# Exit:  0 = AB-E02-1 is evidenced by this run
#        1 = it is not

set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
STAGED="$ROOT/image/.build/platform-tree/usr/bin/workpod"

PASS=0; FAIL=0
pass() { printf '  \033[32mPASS\033[0m  %-34s %s\n' "$1" "${2:-}"; PASS=$((PASS+1)); }
fail() { printf '  \033[31mFAIL\033[0m  %-34s %s\n' "$1" "${2:-}"; FAIL=$((FAIL+1)); }

printf '\n\033[1mAB-E02-1 — one Go binary\033[0m\n\n'

# The staged build if image/build.sh made one, otherwise a fresh build — the check is about the
# artifact's shape, and both paths produce the artifact the same way.
BIN="$STAGED"
if [ ! -x "$BIN" ]; then
  command -v go >/dev/null 2>&1 || { echo "  neither a staged build at $BIN nor go on PATH" >&2; exit 1; }
  BIN="$(mktemp -d)/workpod"
  ( cd "$ROOT/platform" && CGO_ENABLED=0 go build -trimpath -buildvcs=false -o "$BIN" ./cmd/workpod ) \
    || { echo "  the build failed" >&2; exit 1; }
fi
echo "  artifact $BIN"
echo "  sha256   $(sha256sum "$BIN" | cut -d' ' -f1)"
echo

# 1 — statically linked. ldd says "not a dynamic executable" and exits non-zero on a static
#     binary (which is why its output is captured rather than piped under pipefail); a dynamic
#     one would list its libraries and expose the runtime SP-A02-3 forbids.
LDD="$(ldd "$BIN" 2>&1 || true)"
if echo "$LDD" | grep -qi "not a dynamic executable"; then
  pass "statically linked" "no runtime on the host (SP-E02-1)"
else
  fail "statically linked" "$(echo "$LDD" | head -1)"
fi

# 2 — the seven components, from the artifact's own mouth.
COMPONENTS="$("$BIN" components 2>&1)"
echo "$COMPONENTS" | sed 's/^/        /'
for c in control-plane scheduler worker adapter git-gate egress-gate harness; do
  if echo "$COMPONENTS" | grep -q "^$c "; then
    pass "$c is an entry point" ""
  else
    fail "$c is an entry point" "not in the artifact's component list"
  fi
done

# 3 — what is not built refuses by the name of the package that builds it. The exit code is 69
#     (unavailable), distinguishable from a built component that failed.
#
#     The list is the artifact's own. `components` says which of the seven still refuse and which
#     work package owns each; reading it here instead of restating it means this check needs no
#     edit when a component is built, and cannot quietly go on demanding a refusal from something
#     that now serves. A component that serves is proven by its own work package's rows — the
#     control plane and the worker by a boot (a04-boot.sh), the adapter by t01-intake.sh.
REFUSING=0; SERVING=0
while read -r c state ap; do
  [ -n "$c" ] || continue
  case "$state" in
    serving)
      SERVING=$((SERVING+1))
      ;;
    refusing-until)
      REFUSING=$((REFUSING+1))
      out="$("$BIN" "$c" 2>&1)"; rc=$?
      if [ "$rc" -eq 69 ] && echo "$out" | grep -q "$ap"; then
        pass "$c refuses honestly" "exit 69, names $ap"
      else
        fail "$c refuses honestly" "exit $rc — $(echo "$out" | head -1)"
      fi
      ;;
    *)
      fail "$c states what it is" "neither serving nor refusing-until: $state"
      ;;
  esac
done <<< "$COMPONENTS"
printf '        %d serving · %d refusing until their work package\n' "$SERVING" "$REFUSING"

# 4 — the serving ones answer. `version` proves the artifact runs at all; control and worker are
#     proven by a boot (a04-boot.sh), not by a flag — serving is a machine's business.
if "$BIN" version >/dev/null 2>&1; then
  pass "the artifact answers" "$("$BIN" version)"
else
  fail "the artifact answers" "version exited $?"
fi

printf '\n  %d met, %d not\n' "$PASS" "$FAIL"
if [ "$FAIL" -ne 0 ]; then
  echo "  AB-E02-1 stays red."
  exit 1
fi
echo "  AB-E02-1 green through this run: one artifact, seven entry points, no pretense."
