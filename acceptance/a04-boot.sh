#!/usr/bin/env bash
# a04-boot.sh — the node starts along A-04, and the four rows that hang on that boot
# (AB-A04-1, AB-A04-3, AB-A05-1, AB-RC-4, AB-V01-1 · AP-3.1).
#
#   acceptance/a04-boot.sh                     four boots of the built image, host side
#   acceptance/a04-boot.sh probe-main          in the machine: sequence, slices, pressure, markers
#   acceptance/a04-boot.sh probe-reinstall     in the machine: only /data/db survived
#   acceptance/a04-boot.sh probe-nocell        in the machine: a missing boot value refuses, named
#   acceptance/a04-boot.sh probe-selftest-fail in the machine: failed selftest → no registration
#
# What each boot evidences:
#
#   boot 1  role=all on two named disks. The sequence verity → disk → role → selftest → register
#           runs to a registered node (AB-A04-1: the five values of SP-A04-1 and nothing further;
#           `all` carries the control plane and needs no enrollment token). The four layers stand
#           as four slices (AB-V01-1), the plane in system.slice under memory.min. Then the
#           pressure drama: a hog in an armed pod slice eats the machine, the OOM killer takes
#           the pod whole (memory.oom.group=1), and the plane answers over the pull path the
#           entire time (AB-RC-4). Markers are left on all three volumes for boot 2.
#   boot 2  the same disks after `workpod disk reinstall`. The sequence runs again; the markers
#           on /var and /data/work are gone, the one on /data/db is not (AB-A05-1).
#   boot 3  the cell credential withheld. The sequence refuses at its head, names the missing
#           value and SP-A04-1, and nothing registers (AB-A04-1 as a probe: the five are needed,
#           not just enough).
#   boot 4  a layout that violates SP-A05-3 (work partition on the data disk). The disk step
#           mounts what it finds — judging is the selftest's — and the selftest fails the
#           separation check: no registration, no control plane (AB-A04-3: a failed selftest
#           means do not enroll).
#
# Exit:  0 = the five rows are evidenced by this run
#        1 = they are not

set -uo pipefail

MODE="${1:-drive}"
HERE="$(cd "$(dirname "$0")" && pwd)"

# =================================================================================================
# Host side.
# =================================================================================================
if [ "$MODE" = drive ]; then
  ROOT="$(cd "$HERE/.." && pwd)"
  VM="$ROOT/image/vm.sh"
  STAGED="$ROOT/image/.build/platform-tree/usr/bin/workpod"
  DISKS="$(mktemp -d)"
  trap 'rm -rf "$DISKS"' EXIT

  # The boot values, as credential files vm.sh carries in — the same door instance data uses
  # (SP-A04-1, E-01). No enrollment token: `all` carries the control plane and SP-A04-1 exempts it.
  printf 'eu-c1'          > "$DISKS/workpod.cell"
  printf '127.0.0.1:8443' > "$DISKS/workpod.control"
  printf 'probe-a'        > "$DISKS/workpod.locality_group"
  CREDS=(--file "$DISKS/workpod.cell" --file "$DISKS/workpod.control" --file "$DISKS/workpod.locality_group")

  TPM=()
  if command -v swtpm >/dev/null 2>&1; then
    TPM=(--tpm)
    echo "== swtpm present: the machine gets a TPM, /var gets its binding (SP-A05-4)"
  else
    echo "== no swtpm: the machine has no TPM, the disk step will say /var stays plain"
  fi

  failed=0
  boot() {  # $1 = name, then vm.sh args
    local name="$1"; shift
    printf '\n\033[1m== boot: %s\033[0m\n' "$name"
    if ! "$VM" --timeout 1200 "$@" 2>&1 | tee "$DISKS/$name.log"; then
      echo "  boot '$name' failed" >&2
      failed=1
    fi
  }

  boot main --role all --memory 6144 --cpus 2 "${TPM[@]}" "${CREDS[@]}" \
    --persist-disk "$DISKS/data.raw:workpod-data:4G" \
    --persist-disk "$DISKS/work.raw:workpod-work:2G" \
    "$HERE/a04-boot.sh" probe-main

  # One artifact, in the image and outside it: the hash the booted node reported against the file
  # build.sh staged for mkosi (AB-E02-1's other half, and AB-V01-1's "the same software").
  guest_hash="$(sed -n 's/.*WORKPOD-BINARY: \([0-9a-f]*\)$/\1/p' "$DISKS/main.log" | tail -1)"
  host_hash="$(sha256sum "$STAGED" 2>/dev/null | cut -d' ' -f1)"
  if [ -n "$guest_hash" ] && [ "$guest_hash" = "$host_hash" ]; then
    printf '  \033[32mPASS\033[0m  the booted node runs the staged artifact (%s…)\n' "${guest_hash:0:16}"
  else
    printf '  \033[31mFAIL\033[0m  binary hash: guest %s vs staged %s\n' "${guest_hash:-none}" "${host_hash:-none}"
    failed=1
  fi

  boot reinstall --role all --memory 4096 --cpus 2 "${TPM[@]}" "${CREDS[@]}" \
    --persist-disk "$DISKS/data.raw:workpod-data:4G" \
    --persist-disk "$DISKS/work.raw:workpod-work:2G" \
    "$HERE/a04-boot.sh" probe-reinstall

  boot nocell --role all "${TPM[@]}" \
    --file "$DISKS/workpod.control" --file "$DISKS/workpod.locality_group" \
    "$HERE/a04-boot.sh" probe-nocell

  boot selftest-fail --role all --memory 4096 --cpus 2 "${TPM[@]}" "${CREDS[@]}" \
    --persist-disk "$DISKS/data2.raw:workpod-data:4G" \
    "$HERE/a04-boot.sh" probe-selftest-fail

  echo
  if [ "$failed" -ne 0 ]; then
    echo "AB-A04-1, AB-A04-3, AB-A05-1, AB-RC-4, AB-V01-1 stay red."
    exit 1
  fi
  echo "AB-A04-1, AB-A04-3, AB-A05-1, AB-RC-4 and AB-V01-1 green through this run:"
  echo "the sequence registers only past its selftest, the layout survives as A-05 says,"
  echo "and the plane answered while the work layer was eaten."
  exit 0
fi

# =================================================================================================
# Guest side. Shared helpers.
# =================================================================================================
PASS=0; FAIL=0
pass() { printf '  \033[32mPASS\033[0m  %-38s %s\n' "$1" "${2:-}"; PASS=$((PASS+1)); }
fail() { printf '  \033[31mFAIL\033[0m  %-38s %s\n' "$1" "${2:-}"; FAIL=$((FAIL+1)); }

finish() {  # $1 = probe name
  printf '\n  %d met, %d not\n' "$PASS" "$FAIL"
  [ "$FAIL" -eq 0 ]
}

# The boot stops short of multi-user.target under systemd.run (see image/vm.sh); the probe asks
# for it, with the login prompts masked so no getty takes the console — the a02 pattern.
start_boot() {
  systemctl mask --runtime getty.target serial-getty@ttyS0.service >/dev/null 2>&1
  systemctl start --no-block multi-user.target
}

wait_unit_state() {  # $1 = unit, $2 = wanted (active|failed), $3 = seconds
  local i
  for i in $(seq 1 "$3"); do
    case "$2" in
      active) [ "$(systemctl is-active "$1" 2>/dev/null)" = active ] && return 0 ;;
      failed) [ "$(systemctl is-failed "$1" 2>/dev/null)" = failed ] && return 0 ;;
    esac
    sleep 1
  done
  return 1
}

wait_file() {  # $1 = path, $2 = seconds
  local i
  for i in $(seq 1 "$2"); do
    [ -e "$1" ] && return 0
    sleep 1
  done
  return 1
}

REGISTERED=/run/workpod/registered
SELFTEST=/run/workpod/selftest.passed

case "$MODE" in

# -------------------------------------------------------------------------------------------------
probe-main)
  printf '\n\033[1mA-04 boot, role=all — sequence, layers, pressure\033[0m\n\n'
  start_boot

  # ---- the sequence runs to a registered node (AB-A04-1, positive half) ------------------------
  if wait_file "$REGISTERED" 420; then
    pass "registered" "$(tr '\n' ' ' < "$REGISTERED")"
  else
    fail "registered" "no $REGISTERED after 420 s"
    systemctl --no-pager --failed 2>&1 | sed 's/^/        /'
    journalctl -u workpod-disk -u workpod-selftest -u workpod-worker --no-pager 2>&1 | tail -40 | sed 's/^/        /'
  fi
  [ -e "$SELFTEST" ] && pass "selftest before register" "$(head -1 "$SELFTEST")" \
                     || fail "selftest before register" "no marker at $SELFTEST"
  for u in workpod-disk workpod-selftest workpod-control workpod-worker workpod-db; do
    st="$(systemctl is-active "$u.service" 2>/dev/null)"
    [ "$st" = active ] && pass "$u.service" "active" || fail "$u.service" "$st"
  done

  # ---- four layers, four slices, one machine (AB-V01-1) ----------------------------------------
  for s in workpod-control.slice workpod-captain.slice workpod-knowledge.slice workpod-work.slice; do
    st="$(systemctl is-active "$s" 2>/dev/null)"
    [ "$st" = active ] && pass "$s" "a layer of V-01" || fail "$s" "$st"
  done
  dbslice="$(systemctl show -p Slice --value workpod-db.service 2>/dev/null)"
  [ "$dbslice" = workpod-control.slice ] && pass "state db in the control layer" "$dbslice" \
                                         || fail "state db in the control layer" "Slice=$dbslice"
  roles=0
  for r in all control knowledge work; do [ -f "/usr/lib/systemd/system/workpod-$r.target" ] && roles=$((roles+1)); done
  [ "$roles" = 4 ] && pass "four roles in this one artifact" "the same software, one boot variable (SP-A02-1)" \
                   || fail "four roles in this one artifact" "$roles of 4 role targets present"

  # ---- the reservation stands where SP-RC-4 puts it (AB-RC-4, structural half) -----------------
  sysmin="$(cat /sys/fs/cgroup/system.slice/memory.min 2>/dev/null)"
  [ "$sysmin" = 4294967296 ] && pass "memory.min on the system slice" "4 GB (SP-RC-4, E-05)" \
                             || fail "memory.min on the system slice" "memory.min=$sysmin"
  cg="$(systemctl show -p ControlGroup --value workpod-control.service 2>/dev/null)"
  case "$cg" in
    /system.slice/*) pass "the plane under the reservation" "$cg" ;;
    *)               fail "the plane under the reservation" "ControlGroup=$cg" ;;
  esac

  # ---- the pressure drama (AB-RC-4, behavioral half) --------------------------------------------
  # A sleeper and a hog share one pod slice. `workpod podslice arm` sets memory.oom.group=1 on it;
  # the hog then eats the machine in ~50 MB bites of incompressible pages (urandom, NULs stripped
  # so bash keeps it) — bite-sized so no single allocation trips the overcommit heuristic, and
  # incompressible so zram cannot quietly absorb the pressure. The kernel reclaims everywhere but
  # the protected slices, OOMs, and must take sleeper and hog together — while the plane answers
  # over the very path registering used.
  systemd-run --quiet --unit=podprobe-hold --slice=workpod-work-podprobe.slice /usr/bin/sleep 600 \
    && pass "pod slice up" "workpod-work-podprobe.slice, nested under the work layer" \
    || fail "pod slice up" "systemd-run failed"
  if out="$(workpod podslice arm workpod-work-podprobe.slice 2>&1)"; then
    pass "memory.oom.group=1 armed" "$out"
  else
    fail "memory.oom.group=1 armed" "$out"
  fi
  systemd-run --quiet --unit=podprobe-hog --slice=workpod-work-podprobe.slice \
    /bin/bash -c 'c=(); while :; do c+=("$(head -c 50M /dev/urandom | tr -d "\\0")"); done' \
    || fail "hog started" "systemd-run failed"

  pings=0; ping_fail=0; hog_result=""
  for _ in $(seq 1 240); do
    if workpod ping --deadline 5s >/dev/null 2>&1; then pings=$((pings+1)); else ping_fail=$((ping_fail+1)); fi
    hog_result="$(systemctl show -p Result --value podprobe-hog.service 2>/dev/null)"
    [ "$hog_result" = oom-kill ] && break
    st="$(systemctl is-active podprobe-hog.service 2>/dev/null)"
    [ "$st" = failed ] && break
    sleep 1
  done
  hog_result="$(systemctl show -p Result --value podprobe-hog.service 2>/dev/null)"
  [ "$hog_result" = oom-kill ] && pass "the hog died of OOM" "Result=$hog_result" \
                               || fail "the hog died of OOM" "Result=${hog_result:-unknown} — no pressure, no probe"
  hold_active="$(systemctl is-active podprobe-hold.service 2>/dev/null)"
  hold_result="$(systemctl show -p Result --value podprobe-hold.service 2>/dev/null)"
  if [ "$hold_active" != active ] && [ "$hold_result" != success ]; then
    pass "the pod died whole" "the innocent sleeper went with it (memory.oom.group=1): Result=$hold_result"
  else
    fail "the pod died whole" "sleeper $hold_active/$hold_result — one process was shot, the pod survived"
  fi
  if [ "$ping_fail" = 0 ] && [ "$pings" -gt 0 ]; then
    pass "the plane answered throughout" "$pings pings under pressure, none missed (SP-RC-4)"
  else
    fail "the plane answered throughout" "$pings answered, $ping_fail missed"
  fi
  systemctl stop podprobe-hold.service podprobe-hog.service >/dev/null 2>&1
  systemctl reset-failed podprobe-hold.service podprobe-hog.service >/dev/null 2>&1

  # ---- markers for boot 2 (AB-A05-1) -------------------------------------------------------------
  echo "ap31-marker-var"  > /var/ap31.marker  || fail "marker on /var" "cannot write"
  echo "ap31-marker-work" > /data/work/ap31.marker || fail "marker on /data/work" "cannot write"
  echo "ap31-marker-db"   > /data/db/ap31.marker   || fail "marker on /data/db" "cannot write"
  sync
  pass "markers written" "/var, /data/work, /data/db — boot 2 reads what a reinstall left"

  echo "WORKPOD-BINARY: $(sha256sum /usr/bin/workpod | cut -d' ' -f1)" > /dev/console 2>/dev/null \
    || echo "WORKPOD-BINARY: $(sha256sum /usr/bin/workpod | cut -d' ' -f1)"
  finish probe-main
  ;;

# -------------------------------------------------------------------------------------------------
probe-reinstall)
  printf '\n\033[1mreinstall — only /data/db survives (SP-A05-1)\033[0m\n\n'

  # The installer's act, before the sequence runs: wipe /var and /data/work, keep /data/db.
  if out="$(workpod disk reinstall 2>&1)"; then
    pass "workpod disk reinstall" "$(echo "$out" | tail -1)"
  else
    fail "workpod disk reinstall" "$out"
  fi

  start_boot
  if wait_file "$REGISTERED" 420; then
    pass "the reinstalled node registers" "the sequence holds on found disks too"
  else
    fail "the reinstalled node registers" "no $REGISTERED after 420 s"
    journalctl -u workpod-disk -u workpod-selftest -u workpod-worker --no-pager 2>&1 | tail -40 | sed 's/^/        /'
  fi

  [ ! -e /var/ap31.marker ]        && pass "/var did not survive" "fresh filesystem, old marker gone" \
                                   || fail "/var did not survive" "the marker is still there"
  [ ! -e /data/work/ap31.marker ]  && pass "/data/work did not survive" "reproducible, so expendable" \
                                   || fail "/data/work did not survive" "the marker is still there"
  if [ "$(cat /data/db/ap31.marker 2>/dev/null)" = "ap31-marker-db" ]; then
    pass "/data/db survived" "the only area that does (SP-A05-1)"
  else
    fail "/data/db survived" "the marker is gone or damaged"
  fi
  finish probe-reinstall
  ;;

# -------------------------------------------------------------------------------------------------
probe-nocell)
  printf '\n\033[1mfive values or no start — cell withheld (SP-A04-1)\033[0m\n\n'
  start_boot

  if wait_unit_state workpod-disk.service failed 120; then
    pass "the sequence refused at its head" "workpod-disk.service failed"
  else
    fail "the sequence refused at its head" "$(systemctl is-active workpod-disk.service 2>&1)"
  fi
  if journalctl -u workpod-disk.service --no-pager 2>/dev/null | grep -q "cell is missing"; then
    pass "the refusal names the value" "\"cell is missing\", citing SP-A04-1"
  else
    fail "the refusal names the value" "no named refusal in the journal"
    journalctl -u workpod-disk.service --no-pager 2>&1 | tail -10 | sed 's/^/        /'
  fi
  [ ! -e "$REGISTERED" ] && pass "nothing registered" "" || fail "nothing registered" "$REGISTERED exists"
  for u in workpod-selftest workpod-worker workpod-control; do
    st="$(systemctl is-active "$u.service" 2>/dev/null)"
    [ "$st" != active ] && pass "$u never ran" "$st" || fail "$u never ran" "active despite the refusal"
  done
  finish probe-nocell
  ;;

# -------------------------------------------------------------------------------------------------
probe-selftest-fail)
  printf '\n\033[1mfailed selftest, no enrollment — work on the data disk (SP-A05-3, SP-A04-3)\033[0m\n\n'

  # Craft the violation before the sequence sees the disk: all three partitions on the one data
  # disk. Everything used here is image content (sfdisk, mkfs.btrfs) — the machine damages its own
  # layout the way a wrong installer would.
  DEV="$(readlink -f /dev/disk/by-id/*workpod-data* | head -1)"
  if [ -b "$DEV" ]; then
    printf 'label: gpt\nname=workpod-var, size=1GiB\nname=workpod-work, size=1GiB\nname=workpod-db\n' | sfdisk --quiet "$DEV" \
      && udevadm settle \
      && mkfs.btrfs --quiet --label workpod-var  "$DEV"1 >/dev/null 2>&1 \
      && mkfs.btrfs --quiet --label workpod-work "$DEV"2 >/dev/null 2>&1 \
      && mkfs.btrfs --quiet --label workpod-db   "$DEV"3 >/dev/null 2>&1 \
      && pass "violation crafted" "var, work and db on one disk — what SP-A05-3 forbids" \
      || fail "violation crafted" "could not partition $DEV"
  else
    fail "violation crafted" "no data disk at /dev/disk/by-id/*workpod-data*"
  fi

  start_boot
  if wait_unit_state workpod-selftest.service failed 300; then
    pass "the selftest failed" "as it must on this layout"
  else
    fail "the selftest failed" "$(systemctl is-active workpod-selftest.service 2>&1)"
    journalctl -u workpod-disk -u workpod-selftest --no-pager 2>&1 | tail -30 | sed 's/^/        /'
  fi
  dst="$(systemctl is-active workpod-disk.service 2>/dev/null)"
  [ "$dst" = active ] && pass "the disk step itself held" "mechanics mount, the selftest judges" \
                      || fail "the disk step itself held" "workpod-disk.service is $dst"
  if journalctl -u workpod-selftest.service --no-pager 2>/dev/null | grep -q "work disk separate"; then
    pass "the failure is named" "the separation check of SP-A05-3"
  else
    fail "the failure is named" "no named check in the journal"
  fi
  [ ! -e "$REGISTERED" ] && pass "no registration" "a failed selftest means: do not enroll (SP-A04-3)" \
                         || fail "no registration" "$REGISTERED exists"
  for u in workpod-worker workpod-control; do
    st="$(systemctl is-active "$u.service" 2>/dev/null)"
    [ "$st" != active ] && pass "$u stayed down" "$st" || fail "$u stayed down" "active past a failed selftest"
  done
  finish probe-selftest-fail
  ;;

*)
  echo "a04-boot.sh: unknown mode '$MODE'" >&2
  exit 2
  ;;
esac
