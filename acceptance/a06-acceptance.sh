#!/usr/bin/env bash
# a06-acceptance.sh — acceptance of the system image (A-06).
#
# E-11, step 1: "A-06 as a script against a bare mkosi VM; the base holds, or E-01 falls."
# This script is written BEFORE the image. It is both the build plan and the acceptance.
#
# Rule: the image is done when this list is green — not when it boots.
#
# Exit:  0 = no FAIL (SKIPs permitted and named)
#        1 = at least one FAIL
#
# Usage: ./a06-acceptance.sh          all checks
#        ./a06-acceptance.sh base     only those that need no platform (stage 1)

set -uo pipefail

PASS=0; FAIL=0; SKIP=0
MODE="${1:-all}"
POD_SLICE="${POD_SLICE:-/sys/fs/cgroup/workpod.slice/pods.slice}"
WORKDIR="${WORKDIR:-/data/work}"

pass() { printf '  \033[32mPASS\033[0m  %-42s %s\n' "$1" "${2:-}"; PASS=$((PASS+1)); }
fail() { printf '  \033[31mFAIL\033[0m  %-42s %s\n' "$1" "${2:-}"; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m  %-42s %s\n' "$1" "${2:-}"; SKIP=$((SKIP+1)); }
head() { printf '\n\033[1m%s\033[0m\n' "$1"; }

need_platform() {  # checks that can only turn green once the platform is built
  [ "$MODE" = "all" ] || return 1
  command -v workpod >/dev/null 2>&1
}

head "A-06 — acceptance of the system image"

# --------------------------------------------------------------------------
# 1  cgroup v2 unified, PSI readable              → otherwise R-A and R-C fall
# --------------------------------------------------------------------------
if [ "$(stat -fc %T /sys/fs/cgroup 2>/dev/null)" = "cgroup2fs" ] \
   && [ ! -d /sys/fs/cgroup/unified ]; then
  if [ -r /proc/pressure/cpu ] && [ -r "${POD_SLICE}/cpu.pressure" ] 2>/dev/null; then
    pass "AB-A06-1 cgroup v2 + PSI" "$(awk 'NR==1{print $1,$2}' "${POD_SLICE}/cpu.pressure" 2>/dev/null)"
  elif [ -r /proc/pressure/cpu ]; then
    skip "AB-A06-1 cgroup v2 + PSI" "readable globally, pods slice still missing (AP-3.1)"
  else
    fail "AB-A06-1 cgroup v2 + PSI" "CONFIG_PSI missing — E-01 falls"
  fi
else
  fail "AB-A06-1 cgroup v2 + PSI" "no unified cgroup v2"
fi

# --------------------------------------------------------------------------
# 2  reflink snapshot in O(1)                     → otherwise T-04 and G-03 fall
#    1 GB copied in milliseconds, the disk does not grow.
# --------------------------------------------------------------------------
if [ -d "$WORKDIR" ]; then
  FS=$(stat -fc %T "$WORKDIR")
  T="$WORKDIR/.a06"; mkdir -p "$T"
  dd if=/dev/zero of="$T/src" bs=1M count=1024 status=none 2>/dev/null
  BEFORE=$(df -k --output=used "$WORKDIR" | tail -1)
  START=$(date +%s%N)
  if cp --reflink=always "$T/src" "$T/dst" 2>/dev/null; then
    MS=$(( ($(date +%s%N) - START) / 1000000 ))
    AFTER=$(df -k --output=used "$WORKDIR" | tail -1)
    GROWTH=$(( AFTER - BEFORE ))
    if [ "$MS" -lt 200 ] && [ "$GROWTH" -lt 51200 ]; then
      pass "AB-A06-2 reflink O(1)" "${FS}, ${MS} ms, +${GROWTH} KB"
    else
      fail "AB-A06-2 reflink O(1)" "${MS} ms, +${GROWTH} KB — not O(1)"
    fi
  else
    fail "AB-A06-2 reflink O(1)" "${FS} cannot do reflink — btrfs or XFS required (A-05)"
  fi
  rm -rf "$T"
else
  skip "AB-A06-2 reflink O(1)" "$WORKDIR missing (AP-3.1)"
fi

# --------------------------------------------------------------------------
# 3  user namespaces, seccomp, Landlock           → otherwise T-04 falls
#    test pod without rights, escape attempt fails.
# --------------------------------------------------------------------------
OK=1
[ "$(sysctl -n user.max_user_namespaces 2>/dev/null || echo 0)" -gt 0 ] || OK=0
grep -q 'Seccomp' /proc/self/status || OK=0
[ -e /sys/kernel/security/lsm ] && grep -q landlock /sys/kernel/security/lsm || OK=0
if [ "$OK" = 1 ]; then
  pass "AB-A06-3 userns/seccomp/landlock" "present"
else
  fail "AB-A06-3 userns/seccomp/landlock" "one of the three options is missing from the kernel"
fi

# --------------------------------------------------------------------------
# 4  freezer and zram with zstd                   → otherwise R-C and R-D fall
#    The zram factor is MEASURED, not assumed (E-05).
# --------------------------------------------------------------------------
if [ -e /sys/block/zram0/comp_algorithm ] && grep -q '\[zstd\]' /sys/block/zram0/comp_algorithm; then
  ORIG=$(cat /sys/block/zram0/orig_data_size 2>/dev/null || echo 0)
  COMP=$(cat /sys/block/zram0/compr_data_size 2>/dev/null || echo 0)
  if [ "$COMP" -gt 0 ]; then
    FACTOR=$(awk -v o="$ORIG" -v c="$COMP" 'BEGIN{printf "%.2f", o/c}')
    pass "AB-A06-4 freezer + zram(zstd)" "measured factor ${FACTOR} → record in decisions/E-05.md"
  else
    skip "AB-A06-4 freezer + zram(zstd)" "zstd active, no load yet — measure in the calibration run"
  fi
else
  fail "AB-A06-4 freezer + zram(zstd)" "zram with zstd not active"
fi
[ -e "${POD_SLICE}/cgroup.freeze" ] 2>/dev/null \
  && pass "AB-A06-4b cgroup freezer" "cgroup.freeze present" \
  || skip "AB-A06-4b cgroup freezer" "pods slice still missing (AP-3.1)"

# --------------------------------------------------------------------------
# 5  CRIU: dump and restore                       → otherwise rung 4 of the ladder is missing (R-C)
#    This is the check E-01 hangs on: if it is missing with a fixed kernel, NixOS is the answer.
# --------------------------------------------------------------------------
if command -v criu >/dev/null 2>&1; then
  if criu check --extra >/dev/null 2>&1; then
    pass "AB-A06-5 CRIU dump/restore" "criu check --extra green"
  else
    fail "AB-A06-5 CRIU dump/restore" "kernel options missing — E-01, the overturn condition applies"
  fi
else
  fail "AB-A06-5 CRIU dump/restore" "criu not in the image"
fi

# --------------------------------------------------------------------------
# 6  no toolchain, no package manager             → otherwise A-01 falls
#    Inventory against the SBOM (A-03).
# --------------------------------------------------------------------------
FORBIDDEN=(gcc cc clang make python3 node npm go rustc dnf rpm apt yum pip pip3)
FOUND=()
for b in "${FORBIDDEN[@]}"; do command -v "$b" >/dev/null 2>&1 && FOUND+=("$b"); done
if [ ${#FOUND[@]} -eq 0 ]; then
  pass "AB-A06-6 no toolchain" "none of the ${#FORBIDDEN[@]} candidates present"
else
  fail "AB-A06-6 no toolchain" "found: ${FOUND[*]}"
fi
if [ -r /usr/share/workpod/sbom.json ]; then
  pass "AB-A06-6b SBOM present" "$(stat -c %s /usr/share/workpod/sbom.json) bytes"
else
  fail "AB-A06-6b SBOM present" "without a bill of materials the first rationale from A-01 falls"
fi

# --------------------------------------------------------------------------
# 7  verity and fallback                          → otherwise A-03 falls
#    A damaged image does not start, B takes over. The destructive part is a drill.
# --------------------------------------------------------------------------
if [ -d /sys/kernel/config/dm-verity ] || dmsetup status 2>/dev/null | grep -q verity; then
  pass "AB-A06-7 dm-verity active" "root filesystem under a checksum tree"
else
  fail "AB-A06-7 dm-verity active" "read-only without verity is a mount option, not protection"
fi
if mount | grep -q ' / .*\bro\b'; then
  pass "AB-A06-7b / read-only" ""
else
  fail "AB-A06-7b / read-only" ""
fi
skip "AB-A06-7c fallback after 3 failed starts" "drill: damage the image deliberately (D, AP-1.2)"

# --------------------------------------------------------------------------
# 8  no inbound ports                             → otherwise B-02 falls
# --------------------------------------------------------------------------
LISTEN=$(ss -Hltn 2>/dev/null | awk '{print $4}' | grep -v '^127\.' | grep -v '^\[::1\]' | wc -l)
if [ "$LISTEN" -eq 0 ]; then
  pass "AB-A06-8 no inbound ports" "not even SSH"
else
  fail "AB-A06-8 no inbound ports" "$LISTEN listening sockets — repeat the scan from outside"
fi

# --------------------------------------------------------------------------
# 9–12  checks that need the platform
# --------------------------------------------------------------------------
if need_platform; then
  workpod acceptance role-all         >/dev/null 2>&1 && pass "AB-A06-9  role=all"          "envelope to patch" || fail "AB-A06-9  role=all" ""
  workpod acceptance role-work-cell   >/dev/null 2>&1 && pass "AB-A06-10 role=work"         "lease, heartbeat, expiry" || fail "AB-A06-10 role=work" ""
  workpod acceptance double-execution >/dev/null 2>&1 && pass "AB-A06-11 double execution"  "twice, one push" || fail "AB-A06-11 double execution" ""
  workpod acceptance rolling-update   >/dev/null 2>&1 && pass "AB-A06-12 rolling update"    "no job lost" || fail "AB-A06-12 rolling update" ""
else
  skip "AB-A06-9  role=all"          "turns green in AP-3.8"
  skip "AB-A06-10 role=work"         "turns green in AP-6.2"
  skip "AB-A06-11 double execution"  "turns green in AP-3.5"
  skip "AB-A06-12 rolling update"    "turns green in AP-6.4"
fi

# --------------------------------------------------------------------------
# 13  calibration run                             → replaces the given values from E-05
#     500 pods created, 20 active; the numbers from R-D against the measurement.
# --------------------------------------------------------------------------
if need_platform; then
  if workpod acceptance calibration --allocated 500 --active 20 >/tmp/a06-calibration.txt 2>&1; then
    pass "AB-A06-13 calibration run" "numbers in /tmp/a06-calibration.txt → decisions/E-05.md"
  else
    fail "AB-A06-13 calibration run" "see /tmp/a06-calibration.txt"
  fi
else
  skip "AB-A06-13 calibration run" "turns green in AP-1.3 (measures the five constants)"
fi

# --------------------------------------------------------------------------
# Addition from K-04: time is infrastructure, not an operations manual.
# --------------------------------------------------------------------------
if timedatectl show -p NTPSynchronized --value 2>/dev/null | grep -q yes; then
  pass "AB-K04-7 clock synchronized" "leases and token expiry are functions of time"
else
  fail "AB-K04-7 clock synchronized" "a node with a wrong clock acts plausibly wrong"
fi

head "Result"
printf '  %d green, %d red, %d open\n\n' "$PASS" "$FAIL" "$SKIP"
if [ "$FAIL" -gt 0 ]; then
  echo "  The image is not done. An image without an acceptance record is a proposal."
  exit 1
fi
if [ "$SKIP" -gt 0 ]; then
  echo "  No failure. The open rows carry their work package in the text."
fi
exit 0
