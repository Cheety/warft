#!/usr/bin/env bash
# a02-roles.sh — four roles, one artifact (AB-A02-1, SP-A02-1, SP-A03-5).
#
#   acceptance/a02-roles.sh          boot the image twice, once per role, and compare
#   acceptance/a02-roles.sh probe    the check itself; runs inside the machine
#
# AB-A02-1 is a probe in the sense of the matrix: the forbidden action has to fail. Two things are
# forbidden here and both are checked, because either one alone would be easy to fake.
#
#   the role may not change the content   — /usr is read-only under dm-verity, so a write to it
#                                           fails against the kernel and not against a convention
#   the roles may not be different images — two boots with different roles have to report the same
#                                           verity roothash, which is a hash of the content itself
#
# What the role may do is activate units, and that is what the generator does: one symlink in
# /run/systemd/generator, naming the target of the role it was given and no other.
#
# The boot stops short of multi-user.target here — systemd-run-generator points default.target at
# its own target so that the check can run at all (see image/vm.sh). The probe therefore asks for
# multi-user.target itself and then watches what comes up. What the role decided is visible before
# that, in the generator's output, and is checked there.
#
# Exit:  0 = AB-A02-1 is evidenced by this run
#        1 = it is not

set -uo pipefail

MODE="${1:-drive}"
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
ROLES=(all control knowledge work)

# ---------------------------------------------------------------------------------------------
# Host side: boot the same artifact under two roles and hold the two runs against each other.
# `control` and `work` are the pair that matters — the two ends of SP-A02-1's scaling sentence.
# ---------------------------------------------------------------------------------------------
if [ "$MODE" = drive ]; then
  LOG="$(mktemp -d)"
  trap 'rm -rf "$LOG"' EXIT
  failed=0
  hashes=""

  for role in control work; do
    if ! "$ROOT/image/vm.sh" --role "$role" "$HERE/a02-roles.sh" probe 2>&1 | tee "$LOG/$role"; then
      failed=1
    fi
    h="$(sed -n 's/^WORKPOD-ROOTHASH: //p' "$LOG/$role" | tail -1)"
    if [ -z "$h" ]; then
      echo "  the run as '$role' reported no roothash" >&2
      failed=1
    fi
    hashes="$hashes$role $h"$'\n'
  done

  printf '\n\033[1mAB-A02-1 — one artifact under two roles\033[0m\n'
  printf '%s' "$hashes" | sed 's/^/  /'

  distinct="$(printf '%s' "$hashes" | awk 'NF==2 {print $2}' | sort -u | wc -l)"
  if [ "$distinct" = 1 ]; then
    printf '  \033[32mPASS\033[0m  the content is the same under both roles\n'
  else
    printf '  \033[31mFAIL\033[0m  the roles boot different content — that is two images, not one\n'
    failed=1
  fi

  if [ "$failed" -ne 0 ]; then
    echo
    echo "  AB-A02-1 stays red."
    exit 1
  fi
  echo
  echo "  AB-A02-1 green through this run: the role activates units and changes nothing."
  exit 0
fi

# ---------------------------------------------------------------------------------------------
# Guest side.
# ---------------------------------------------------------------------------------------------
PASS=0; FAIL=0
pass() { printf '  \033[32mPASS\033[0m  %-34s %s\n' "$1" "${2:-}"; PASS=$((PASS+1)); }
fail() { printf '  \033[31mFAIL\033[0m  %-34s %s\n' "$1" "${2:-}"; FAIL=$((FAIL+1)); }

CREDENTIALS="${CREDENTIALS_DIRECTORY:-/run/credentials/@system}"
ROLE="$(tr -d '[:space:]' < "$CREDENTIALS/workpod.role" 2>/dev/null)"
GENERATED=/run/systemd/generator/multi-user.target.wants

printf '\n\033[1mAB-A02-1 — role %s\033[0m\n\n' "${ROLE:-<none>}"
echo "WORKPOD-ROLE: $ROLE"

[ -n "$ROLE" ] || { echo "  no role credential reached the machine" >&2; exit 1; }

# 1 — what the role decided, before anything was started. The symlink is the generator's whole
#     output, and it lies under /run: the image is not where a role writes.
if [ -L "$GENERATED/workpod-$ROLE.target" ]; then
  pass "role wants its own target" "$(readlink "$GENERATED/workpod-$ROLE.target")"
else
  fail "role wants its own target" "no symlink in $GENERATED"
fi

other_wanted=0
for r in "${ROLES[@]}"; do
  [ "$r" = "$ROLE" ] && continue
  [ -e "$GENERATED/workpod-$r.target" ] && { other_wanted=$((other_wanted+1)); }
done
if [ "$other_wanted" -eq 0 ]; then
  pass "no other role is wanted" "one boot value, one role"
else
  fail "no other role is wanted" "$other_wanted of the other three are also wanted"
fi

# 2 — and what actually comes up. multi-user.target has to be asked for because the check is
#     started in its place; everything below it is then the boot's own doing.
#
#     The login prompts are masked first, at runtime only, in /run. multi-user.target wants them,
#     and a getty would open the same console this check reports over and take it — the machine has
#     no other channel back (SP-A04-4: no SSH). Masking is undone by the poweroff that follows.
systemctl mask --runtime getty.target serial-getty@hvc0.service >/dev/null 2>&1
systemctl start --no-block multi-user.target
for _ in $(seq 1 60); do
  [ "$(systemctl is-active "workpod-$ROLE.target" 2>/dev/null)" = active ] && break
  sleep 1
done

if [ "$(systemctl is-active "workpod-$ROLE.target" 2>/dev/null)" = active ]; then
  pass "workpod-$ROLE.target is active" "the role activated units"
else
  fail "workpod-$ROLE.target is active" "$(systemctl is-active "workpod-$ROLE.target" 2>&1)"
fi

still=()
for r in "${ROLES[@]}"; do
  [ "$r" = "$ROLE" ] && continue
  [ "$(systemctl is-active "workpod-$r.target" 2>/dev/null)" = active ] && still+=("$r")
done
if [ "${#still[@]}" -eq 0 ]; then
  pass "the other three stay inactive" ""
else
  fail "the other three stay inactive" "${still[*]}"
fi

# 3 — the forbidden action. Not "the role does not write to /usr" but "it cannot": the root is
#     read-only under dm-verity, so this fails in the kernel.
if err="$(touch /usr/lib/systemd/system/workpod-a02-probe 2>&1)"; then
  rm -f /usr/lib/systemd/system/workpod-a02-probe
  fail "writing to /usr fails" "it succeeded — the content is not protected"
else
  pass "writing to /usr fails" "${err##*: }"
fi

# 4 — and that the protection is verity rather than a mount option, which would be a promise
#     instead of a check. The roothash printed here is what the host side compares between roles.
ROOTHASH="$(sed -n 's/.*roothash=\([0-9a-f]*\).*/\1/p' /proc/cmdline)"
if [ -n "$ROOTHASH" ]; then
  pass "root under a roothash" "${ROOTHASH:0:16}…"
else
  fail "root under a roothash" "no roothash= on the kernel command line"
fi

verity=0
for d in /sys/devices/virtual/block/dm-*; do
  [ -r "$d/dm/uuid" ] || continue
  case "$(cat "$d/dm/uuid")" in CRYPT-VERITY-*) verity=1 ;; esac
done
if [ "$verity" = 1 ]; then
  pass "dm-verity carries the root" "checked block by block as it is read"
else
  fail "dm-verity carries the root" "read-only without verity is a mount option, not protection"
fi

if findmnt -no OPTIONS / | grep -qw ro; then
  pass "/ is mounted read-only" ""
else
  fail "/ is mounted read-only" "$(findmnt -no OPTIONS /)"
fi

echo "WORKPOD-ROOTHASH: $ROOTHASH"
printf '\n  %d met, %d not\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
