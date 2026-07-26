#!/usr/bin/env bash
# a06-acceptance.sh — the acceptance of the system image (A-06, SP-A06-1).
#
#   acceptance/a06-acceptance.sh          boot the build and hold the list against it
#   acceptance/a06-acceptance.sh probe    the list itself; runs inside the machine
#
# E-11, step 1: "A-06 as a script against a bare mkosi VM; the base holds, or E-01 falls."
# The list was written before the image (AP-0.2) and is run against it here (AP-1.2).
#
# Rule: the image is done when this list is green — not when it boots.
#
# Thirteen rows, and at this point in the build order eight of them can be evidenced and five cannot.
# The five name the work package that turns them green instead of being quietly left out; that is
# section D's rule, applied one work package at a time. Two further rows travel with the list because
# their panels put them here — AB-K04-7 (time is infrastructure, K-04) and AB-B01-2 (an image is
# public, B-01).
#
# Three rows are decided on both sides of the machine, and the host composes them:
#
#   AB-A06-6  the image carries no toolchain — checked in the machine — *and* the SBOM lists none.
#             "Inventory against SBOM" is a comparison, so both halves have to be looked at.
#   AB-A06-7  verity carries the root — checked in the machine — *and* a deliberately damaged copy
#             of the artifact does not start. The damage is done on the host, to a copy, and the
#             boot that fails is the evidence.
#
# What this run does not evidence is the other half of AB-A06-7's sentence, "B takes over": there is
# one system slot in the image today. A/B and its boot counter (SP-A03-4) arrive with the disk layout
# in AP-3.1 and the channels in AP-6.4. The damaged image not starting is the half that A-03 calls
# "verity or nothing at all", and it is the half AB-A03-3 asks for in as many words.
#
# Exit:  0 = no FAIL (the five open rows are permitted and named)
#        1 = at least one FAIL

set -uo pipefail

MODE="${1:-drive}"
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"

# What a toolchain, an interpreter, a package manager or an editor is called (SP-A02-3, SP-A01-1).
# Checked twice over: as a file in the image, and as a package in the bill of materials. A name that
# is missing here is a hole in both.
FORBIDDEN_BINARIES=(gcc cc c++ g++ clang ld as ar make cmake meson ninja
                    python python2 python3 perl ruby node npm go gofmt rustc cargo javac java
                    dnf dnf-3 dnf5 microdnf yum rpm rpm2cpio apt pip pip3
                    vi vim nano emacs ed)
# One entry per clause of SP-A02-3 and A-01, and nothing beyond them: a package that the
# specification does not forbid does not belong on a list that fails a build. Libraries are left
# out on purpose — `rpm-libs` without `rpm` manages no packages, and failing on it would be this
# check inventing a requirement.
FORBIDDEN_PACKAGES=(gcc gcc-c++ cpp clang llvm binutils glibc-devel kernel-devel
                    make cmake meson ninja-build
                    python3 python3-libs python-unversioned-command nodejs npm golang rust cargo
                    java-openjdk
                    dnf dnf5 microdnf yum rpm rpm-build
                    perl perl-libs ruby vim vim-enhanced vim-minimal nano emacs
                    man-db man-pages)

# =================================================================================================
# Host side: build the machine the list needs, run it, damage a copy, and compose the table.
# =================================================================================================
if [ "$MODE" = drive ]; then
  OUTPUT="${OUTPUT:-$ROOT/image/.build/pass1}"
  IMAGE="$OUTPUT/workpod.raw"
  MANIFEST="$OUTPUT/workpod.manifest"
  # The root partition of a disk image built by systemd-repart, by its GPT type rather than by its
  # number: 10-root.conf says `Type=root`, and this is what that becomes on the disk.
  ROOT_PARTITION_TYPE=4F68BCE3-E8CD-4DB1-96E7-FBCAF984B709

  [ -f "$IMAGE" ] || { echo "a06: no image at $IMAGE — run image/build.sh first" >&2; exit 2; }

  LOG="$(mktemp -d)"
  trap 'rm -rf "$LOG"' EXIT

  # ---------------------------------------------------------------------------------------------
  # 1 — the list, inside the machine.
  #
  # 8 GB of scratch disk for AB-A06-2: a 1 GB source file, a reflink copy of it that must cost
  # nothing, and a real copy of it that must cost a gigabyte — the third is what makes the second a
  # measurement rather than a threshold. 900 seconds because a runner without /dev/kvm emulates,
  # and two gigabytes of writes plus a CRIU dump and restore are the slowest things in the list.
  # ---------------------------------------------------------------------------------------------
  "$ROOT/image/vm.sh" --role work --disk 8G --timeout 900 \
      "$HERE/a06-acceptance.sh" probe 2>&1 | tee "$LOG/probe"
  probe_rc=$?

  # The verdicts, off the console. Anything may stand in front of the marker — a printk fragment on
  # the same line, or the journal's prefix when the guest's console write fell back to stdout — so
  # the matcher tolerates a prefix. Run 22 is why (image/README.md).
  sed -n 's/.*WORKPOD-A06: \(AB-[A-Z0-9-]*\) \([A-Z]*\) \(.*\)$/\1 \2 \3/p' "$LOG/probe" > "$LOG/rows"

  guest_state() { awk -v id="$1" '$1 == id { print $2; exit }' "$LOG/rows"; }
  guest_text()  { awk -v id="$1" '$1 == id { $1=""; $2=""; sub(/^ +/, ""); print; exit }' "$LOG/rows"; }

  # ---------------------------------------------------------------------------------------------
  # 2 — the SBOM half of AB-A06-6. The machine says what it carries; the bill of materials says
  #     what was put in. An inventory is the comparison of the two.
  # ---------------------------------------------------------------------------------------------
  sbom_state=FAIL sbom_text=""
  if [ ! -r "$MANIFEST" ]; then
    sbom_text="no SBOM at $MANIFEST — without a bill of materials A-01's first rationale falls"
  else
    packages="$(grep -o '"name"[[:space:]]*:[[:space:]]*"[^"]*"' "$MANIFEST" \
                | sed 's/.*"\(.*\)"$/\1/' | sort -u)"
    n_packages="$(printf '%s\n' "$packages" | grep -c .)"
    found=()
    for p in "${FORBIDDEN_PACKAGES[@]}"; do
      printf '%s\n' "$packages" | grep -qx "$p" && found+=("$p")
    done
    # Langpacks other than the minimal one are the locale half of SP-A02-3's sentence. glibc needs
    # one of the two, so the minimal one being there is not a finding; any other is.
    while read -r p; do
      case "$p" in glibc-langpack-*|glibc-all-langpacks) found+=("$p") ;; esac
    done <<< "$packages"
    if [ "$n_packages" -lt 50 ]; then
      sbom_text="the SBOM lists $n_packages packages — that is not a bill of materials"
    elif [ "${#found[@]}" -gt 0 ]; then
      sbom_text="the SBOM lists ${found[*]}"
    else
      sbom_state=PASS
      sbom_text="$n_packages packages, none of them a toolchain, an interpreter or a package manager"
    fi
  fi

  # ---------------------------------------------------------------------------------------------
  # 3 — the drill of AB-A06-7: damage the image deliberately and watch it not start.
  #
  # The damage goes into the root partition, at the erofs superblock 1024 bytes in — the one block
  # that is certainly read, so the failure is a fact about the boot rather than about which blocks
  # happened to be touched. dm-verity sits underneath the filesystem and hashes every block as it is
  # read, so the corruption is caught before erofs ever sees it.
  #
  # It is done to a copy. The artifact must come out of this run unchanged: AB-A03-7's seal is over
  # exactly these bytes.
  #
  # A machine that cannot mount its root has nowhere to go — no getty, no SSH (SP-A04-4) — so it
  # waits in the initrd until the timeout kills it. That is why the drill's verdict is not the exit
  # code: it is that the check never ran *and* the console says dm-verity refused the block.
  # ---------------------------------------------------------------------------------------------
  drill_state=FAIL drill_text=""
  if ! command -v sfdisk >/dev/null 2>&1; then
    drill_text="sfdisk is not installed — the root partition cannot be located"
  else
    start_sector="$(sfdisk --dump "$IMAGE" 2>/dev/null \
                    | awk -v t="type=$ROOT_PARTITION_TYPE" '
                        index(toupper($0), toupper(t)) {
                          for (i = 1; i <= NF; i++)
                            if ($i == "start=") { print $(i+1) + 0; exit }
                            else if ($i ~ /^start=/) { s = $i; sub(/^start=/, "", s); print s + 0; exit }
                        }')"
    if [ -z "${start_sector:-}" ] || [ "$start_sector" = 0 ]; then
      drill_text="no partition of type $ROOT_PARTITION_TYPE in $IMAGE"
    else
      cp --sparse=always "$IMAGE" "$LOG/damaged.raw"
      # 512 bytes of noise over the erofs superblock, which begins 1024 bytes into the partition.
      dd if=/dev/urandom of="$LOG/damaged.raw" bs=512 count=1 \
         seek=$(( start_sector + 2 )) conv=notrunc status=none
      echo "== drill: 512 bytes overwritten at sector $(( start_sector + 2 )) of a copy" >&2
      # 120 seconds: the healthy image reaches the check in seven, and a machine that cannot mount
      # its root never finishes at all, so the rest of the budget is spent waiting for the timeout.
      "$ROOT/image/vm.sh" --role work --image "$LOG/damaged.raw" --timeout 120 \
          "$HERE/a06-acceptance.sh" probe > "$LOG/damaged" 2>&1
      damaged_rc=$?
      # The console of the damaged boot, always. It is the evidence when the drill passes and the
      # only diagnosis when it does not; the first run of this drill swallowed it and left a verdict
      # nobody could check.
      echo "== drill: the last 40 lines the damaged machine said (vm.sh exited $damaged_rc)" >&2
      tail -40 "$LOG/damaged" | sed 's/^/   | /' >&2

      # `device-mapper: verity` on its own is not evidence: the module logs that string when it
      # loads, on every healthy boot too. What has to be found is the refusal.
      corruption="$(grep -aoiE '(device-mapper: )?verity[^|]*corrupt[a-z]*' "$LOG/damaged" | head -1)"
      if grep -q 'WORKPOD-EXIT:' "$LOG/damaged"; then
        drill_text="the damaged image booted and ran the check — verity did not stop it"
      elif [ -n "$corruption" ]; then
        drill_state=PASS
        drill_text="did not start: $corruption"
      else
        drill_text="the boot did not finish (vm.sh $damaged_rc) but nothing named a corrupted block"
      fi
    fi
  fi

  # ---------------------------------------------------------------------------------------------
  # The table. Two-sided rows are the conjunction of their halves: a row is green when everything it
  # claims was evidenced, not when most of it was.
  # ---------------------------------------------------------------------------------------------
  PASS=0; FAIL=0; SKIP=0
  row() {
    local state="$2" c=31
    case "$state" in PASS) c=32; PASS=$((PASS+1)) ;; SKIP) c=33; SKIP=$((SKIP+1)) ;; *) FAIL=$((FAIL+1)) ;; esac
    printf '  \033[%sm%-4s\033[0m  %-10s %-38s %s\n' "$c" "$state" "$1" "$3" "${4:-}"
  }
  both() { [ "$1" = PASS ] && [ "$2" = PASS ] && echo PASS || echo FAIL; }
  # A row the machine never reported is red, and says so. Silence is the state a boot that died
  # before the check leaves behind, and it must not read as a blank column.
  from_guest() {
    local state; state="$(guest_state "$1")"
    if [ -z "$state" ]; then
      row "$1" FAIL "$2" "the machine did not report this row — read the boot log above"
    else
      row "$1" "$state" "$2" "$(guest_text "$1")"
    fi
  }

  printf '\n\033[1mA-06 — acceptance of the system image\033[0m\n\n'

  from_guest AB-A06-1 "cgroup v2 unified, PSI readable"
  from_guest AB-A06-2 "reflink snapshot in O(1)"
  from_guest AB-A06-3 "user namespaces, seccomp, Landlock"
  from_guest AB-A06-4 "freezer and zram with zstd"
  from_guest AB-A06-5 "CRIU: dump and restore"
  row AB-A06-6 "$(both "$(guest_state AB-A06-6)" "$sbom_state")" \
      "no toolchain, no package manager" "$(guest_text AB-A06-6) · $sbom_text"
  row AB-A06-7 "$(both "$(guest_state AB-A06-7)" "$drill_state")" \
      "verity and fallback" "$(guest_text AB-A06-7) · $drill_text"
  from_guest AB-A06-8 "no inbound ports"
  from_guest AB-A06-9  "role = all"
  from_guest AB-A06-10 "role = work, foreign cell"
  from_guest AB-A06-11 "double execution without effect"
  from_guest AB-A06-12 "rolling update"
  from_guest AB-A06-13 "calibration run"

  # AP-1.2's "done when" is about section A alone — eight of thirteen green, five open, no red — so
  # the two rows other panels put on this list are counted beside it and not into it.
  section_a_pass=$PASS section_a_fail=$FAIL section_a_skip=$SKIP

  printf '\n\033[1mThe rows other panels put on this list\033[0m\n\n'
  from_guest AB-K04-7 "clock (K-04)"
  from_guest AB-B01-2 "no standing secret (B-01)"

  printf '\n  section A:  %d green, %d red, %d open   of thirteen\n' \
         "$section_a_pass" "$section_a_fail" "$section_a_skip"
  printf '  and beside it:  %d green, %d red\n' \
         "$(( PASS - section_a_pass ))" "$(( FAIL - section_a_fail ))"

  if [ "$FAIL" -gt 0 ]; then
    echo
    echo "  The image is not done. An image without an acceptance record is a proposal."
    exit 1
  fi
  if [ "$probe_rc" -ne 0 ]; then
    echo
    echo "  Every row is green or open, but the machine exited $probe_rc — read the log above."
    exit 1
  fi
  cat <<'EOF'

  The same run evidences three rows that section B states in other words:
    AB-A05-2  reflink                           = AB-A06-2
    AB-A01-1  no package manager, no toolchain  = AB-A06-6
    AB-A03-3  verity                            = AB-A06-7

  The five open rows need the platform binary, not another image. They carry their work package.
  What no run of this list can evidence yet is the second half of AB-A06-7's sentence, "B takes
  over": there is one system slot in the image. A/B and its boot counter (SP-A03-4) arrive with the
  disk layout in AP-3.1 and the channels in AP-6.4.
EOF
  exit 0
fi

# =================================================================================================
# Guest side: the list, in the machine.
# =================================================================================================
PASS=0; FAIL=0; SKIP=0
NOTES=()

note() { NOTES+=("$1"); }

verdict() {  # $1 = row, $2 = PASS|FAIL|SKIP, $3 = one line of evidence
  local c=31
  case "$2" in PASS) c=32; PASS=$((PASS+1)) ;; SKIP) c=33; SKIP=$((SKIP+1)) ;; *) FAIL=$((FAIL+1)) ;; esac
  printf '  \033[%sm%-4s\033[0m  %-10s %s\n' "$c" "$2" "$1" "$3"
  for n in ${NOTES+"${NOTES[@]}"}; do printf '              · %s\n' "$n"; done
  NOTES=()
  # The verdict goes to the console, the reasoning to the journal. The journal reaches the serial
  # line prefixed and rate limited, and the host anchors on these lines (image/README.md, run 22).
  local marker="WORKPOD-A06: $1 $2 $3"
  echo "$marker" > /dev/console 2>/dev/null || echo "$marker"
}

WORK=/run/a06
mkdir -p "$WORK"

printf '\n\033[1mA-06 — in the machine\033[0m\n\n'

# The list needs a normally booted node: zram is a swap unit, the clock is a service, and a port
# that is only opened under multi-user.target would not be seen. systemd-run-generator points
# default.target at the check, so multi-user.target has to be asked for — and the login prompts are
# masked first, at runtime only, because a getty would take the console this check reports over
# (a02-roles.sh does the same, for the same reason).
#
# What is waited on is multi-user.target, not `systemctl is-system-running`. The latter cannot
# become `running` here by construction: it reports `starting` until the initial transaction is
# done, and this check is a unit inside that transaction, so it would be waiting for itself. The
# first run of this script spent its whole ninety-second budget that way and then ran anyway.
systemctl mask --runtime getty.target serial-getty@ttyS0.service >/dev/null 2>&1
systemctl start --no-block multi-user.target
for _ in $(seq 1 90); do
  [ "$(systemctl is-active multi-user.target 2>/dev/null)" = active ] && break
  sleep 1
done
BOOT="$(systemctl is-active multi-user.target 2>&1)"
printf '  boot: multi-user.target %s · %s\n' "$BOOT" "$(uname -r)"
if [ "$BOOT" != active ]; then
  # A wait that runs out says nothing on its own. What is still queued says which unit to look at.
  echo "  it did not settle in 90 s; the jobs still queued:"
  systemctl list-jobs 2>&1 | sed 's/^/    /'
fi
echo

# -------------------------------------------------------------------------------------------------
# 1  cgroup v2 unified, PSI readable                       → otherwise R-A and R-C fall
#    R-A schedules by pressure and R-C reads it per pod, so the file has to exist on a cgroup and
#    not only globally. There is no pods slice until AP-3.1, so the check makes one of the shape the
#    platform will use and reads the pressure of that.
# -------------------------------------------------------------------------------------------------
check_cgroup_psi() {
  local ok=1 f
  if [ "$(stat -fc %T /sys/fs/cgroup 2>/dev/null)" = cgroup2fs ] && [ ! -d /sys/fs/cgroup/unified ]; then
    note "/sys/fs/cgroup is cgroup2fs, and there is no v1 hierarchy beside it"
  else
    note "not a unified cgroup v2 hierarchy"; ok=0
  fi
  [ -r /proc/pressure/cpu ] || { note "/proc/pressure/cpu is not readable — CONFIG_PSI is off"; ok=0; }

  systemd-run --quiet --unit=a06-psi --slice=workpod-pods.slice /usr/bin/sleep 30 2>/dev/null
  local slice_cg pressure
  slice_cg="$(systemctl show -p ControlGroup --value workpod-pods.slice 2>/dev/null)"
  if [ -n "$slice_cg" ] && [ -r "/sys/fs/cgroup$slice_cg/cpu.pressure" ]; then
    pressure="$(awk 'NR==1 {print $1, $2, $3}' "/sys/fs/cgroup$slice_cg/cpu.pressure")"
    note "$slice_cg: $pressure"
    for f in cpu memory io; do
      [ -r "/sys/fs/cgroup$slice_cg/$f.pressure" ] || { note "$f.pressure is missing on the slice"; ok=0; }
    done
  else
    note "no cpu.pressure on a slice — pressure per pod is what R-C reads"; ok=0
  fi
  systemctl stop a06-psi.service >/dev/null 2>&1
  return $(( ! ok ))
}
if check_cgroup_psi; then
  verdict AB-A06-1 PASS "cgroup v2 unified, pressure readable globally and per slice"
else
  verdict AB-A06-1 FAIL "cgroup v2 or PSI is missing — R-A and R-C fall with it"
fi

# -------------------------------------------------------------------------------------------------
# 2  reflink snapshot in O(1)                              → otherwise T-04 and G-03 fall
#    1 GB copied in milliseconds, the disk does not grow. The third measurement — a real copy of the
#    same file — is what makes the second one mean something: it shows the instrument can see a
#    gigabyte arrive, so not seeing one after the reflink is a fact about the filesystem.
#    Also AB-A05-2.
# -------------------------------------------------------------------------------------------------
check_reflink() {
  local disk=/dev/disk/by-id/virtio-workpod-scratch
  [ -b "$disk" ] || disk=/dev/vdb
  if [ ! -b "$disk" ]; then
    note "no scratch disk — image/vm.sh --disk gives the machine one"
    return 1
  fi
  local mnt="$WORK/work"
  mkdir -p "$mnt"
  mkfs.btrfs -q -f -L a06work "$disk" 2>&1 | sed 's/^/              /' || { note "mkfs.btrfs failed"; return 1; }
  mount "$disk" "$mnt" || { note "mounting btrfs failed — is the module in the image?"; return 1; }

  local ok=1 used0 used1 used2 t0 reflink_ms copy_ms
  used() { sync; btrfs filesystem usage -b "$mnt" | awk '$1 == "Used:" { print $2; exit }'; }

  dd if=/dev/zero of="$mnt/src" bs=1M count=1024 status=none
  used0="$(used)"

  t0=$(date +%s%N)
  if cp --reflink=always "$mnt/src" "$mnt/snapshot"; then
    reflink_ms=$(( ($(date +%s%N) - t0) / 1000000 ))
    used1="$(used)"
  else
    note "$(stat -fc %T "$mnt") cannot reflink — btrfs or XFS is required (SP-A05-2)"
    umount "$mnt"; return 1
  fi

  # --sparse=never, because the source is a gigabyte of zeroes and cp punches holes for those by
  # default. A sparse copy would cost no disk either, and the control measurement — that the
  # instrument can see a gigabyte arrive — would quietly measure nothing.
  t0=$(date +%s%N)
  cp --reflink=never --sparse=never "$mnt/src" "$mnt/full-copy"
  copy_ms=$(( ($(date +%s%N) - t0) / 1000000 ))
  used2="$(used)"

  local snapshot_growth=$(( (used1 - used0) / 1048576 )) copy_growth=$(( (used2 - used1) / 1048576 ))
  note "reflink: ${reflink_ms} ms, +${snapshot_growth} MB — a real copy of the same file: ${copy_ms} ms, +${copy_growth} MB"
  note "$(btrfs filesystem du -s --raw "$mnt/snapshot" | tail -1)"

  # 64 MB of slack for the metadata the copy does write; a gigabyte of data would not fit in it.
  [ "$snapshot_growth" -lt 64 ] || { note "the snapshot cost ${snapshot_growth} MB — that is a copy"; ok=0; }
  [ "$copy_growth" -gt 900 ]    || { note "a real copy cost only ${copy_growth} MB — the measurement cannot see a gigabyte"; ok=0; }
  [ "$reflink_ms" -lt 1000 ] || { note "${reflink_ms} ms is not O(1)"; ok=0; }
  # An order of magnitude faster than the copy it replaces, or fast enough that the comparison is
  # measuring process startup rather than the filesystem. Without the second clause an emulated
  # runner could fail this by being uniformly slow, which is not a fact about reflink.
  [ "$reflink_ms" -lt 200 ] || [ $(( reflink_ms * 10 )) -lt "$copy_ms" ] \
    || { note "the snapshot is not an order of magnitude faster than the copy it replaces"; ok=0; }

  umount "$mnt"
  return $(( ! ok ))
}
if check_reflink; then
  verdict AB-A06-2 PASS "btrfs: a 1 GB snapshot is metadata only"
else
  verdict AB-A06-2 FAIL "no O(1) snapshot — T-04 and G-03 rest on this"
fi

# -------------------------------------------------------------------------------------------------
# 3  user namespaces, seccomp, Landlock                    → otherwise T-04 falls
#    Two of the three walls are probed by having a forbidden action fail. The third is read off the
#    kernel's own list of active security modules: Landlock is applied by the sandboxed process to
#    itself through a syscall, and there is no process to sandbox until the runtime exists (AP-3.1)
#    and no compiler in the image to write one — by SP-A02-3, deliberately.
# -------------------------------------------------------------------------------------------------
check_confinement() {
  local ok=1 out
  local secret="$WORK/secret"
  echo "a file only real root may read" > "$secret"
  chmod 600 "$secret"

  # Root inside the namespace, powerless outside it. That is the whole point of the wall: a pod may
  # believe it is root and still not reach the host.
  local inside_uid
  inside_uid="$(setpriv --reuid=65534 --regid=65534 --clear-groups unshare -Ur id -u 2>&1)"
  if [ "$inside_uid" = 0 ]; then
    note "unprivileged user namespace: uid 0 inside"
  else
    note "no unprivileged user namespace (id said '$inside_uid')"; ok=0
  fi
  if out="$(setpriv --reuid=65534 --regid=65534 --clear-groups unshare -Ur cat "$secret" 2>&1)"; then
    note "…and it read a root-only file: $out"; ok=0
  else
    note "…and cannot read a root-only file outside it: ${out##*: }"
  fi

  # seccomp, through the mechanism the platform will use for it: a systemd unit property. The same
  # mount succeeds without the filter, so what failed is the filter and not the mount.
  mkdir -p "$WORK/mnt"
  if mount -t tmpfs none "$WORK/mnt" 2>/dev/null; then
    umount "$WORK/mnt"
    if systemd-run --quiet --wait --pipe -p SystemCallFilter='~@mount' -p SystemCallErrorNumber=EPERM \
         /usr/bin/mount -t tmpfs none "$WORK/mnt" >/dev/null 2>&1; then
      note "a seccomp filter did not stop a filtered syscall"; ok=0
      umount "$WORK/mnt" 2>/dev/null
    else
      note "seccomp: mount(2) succeeds unfiltered and fails under SystemCallFilter=~@mount"
    fi
  else
    note "mount(2) fails even unfiltered — the seccomp probe would prove nothing"; ok=0
  fi
  local mode
  mode="$(systemd-run --quiet --wait --pipe -p SystemCallFilter=@system-service \
            /usr/bin/grep '^Seccomp:' /proc/self/status 2>/dev/null | awk '{print $2}')"
  [ "$mode" = 2 ] && note "a filtered unit runs in seccomp mode 2 (filter)" \
                  || { note "a filtered unit reports seccomp mode '${mode:-none}'"; ok=0; }

  if grep -qw landlock /sys/kernel/security/lsm 2>/dev/null; then
    note "landlock is in the kernel's active LSM list: $(cat /sys/kernel/security/lsm)"
  else
    note "landlock is not active — CONFIG_LSM has to name it, or lsm= on the command line does"; ok=0
  fi
  return $(( ! ok ))
}
if check_confinement; then
  verdict AB-A06-3 PASS "userns confines, seccomp filters, Landlock is active"
else
  verdict AB-A06-3 FAIL "one of the three walls around a pod is missing"
fi

# -------------------------------------------------------------------------------------------------
# 4  freezer and zram with zstd                            → otherwise R-C and R-D fall
#    Rung 3 of R-C's ladder: freeze the pod, let its pages compress. The mechanism is checked here;
#    the factor is a number and AP-1.3 measures it (E-05), which is why AB-A06-4 stays with AP-1.3
#    in the registry even though this run passes.
# -------------------------------------------------------------------------------------------------
check_freezer_zram() {
  local ok=1
  if [ -e /sys/block/zram0/comp_algorithm ] && grep -q '\[zstd\]' /sys/block/zram0/comp_algorithm; then
    note "zram0: $(cat /sys/block/zram0/comp_algorithm), $(( $(cat /sys/block/zram0/disksize) / 1048576 )) MB"
  else
    note "zram0 is not there with zstd selected — the kernel's own default is lzo-rle"; ok=0
  fi
  if grep -q '^/dev/zram0' /proc/swaps; then
    note "in use as swap: $(awk '/^\/dev\/zram0/ {print $3 " KB, " $4 " KB used"}' /proc/swaps)"
  else
    note "zram0 is not swap — frozen pages have nowhere to go (SP-A05-1)"; ok=0
  fi

  systemd-run --quiet --unit=a06-freeze --slice=workpod-pods.slice /usr/bin/sleep 300 2>/dev/null
  local cg i; cg="$(systemctl show -p ControlGroup --value a06-freeze.service 2>/dev/null)"
  if [ -n "$cg" ] && [ -e "/sys/fs/cgroup$cg/cgroup.freeze" ]; then
    # The kernel reports `frozen 1` in cgroup.events once every process in the cgroup has actually
    # stopped, which is a moment after the write to cgroup.freeze returns. Polling is the difference
    # between checking that the freezer worked and checking that it was asked to.
    systemctl freeze a06-freeze.service >/dev/null 2>&1
    for i in $(seq 1 10); do grep -q '^frozen 1' "/sys/fs/cgroup$cg/cgroup.events" && break; sleep 1; done
    if [ "$(cat "/sys/fs/cgroup$cg/cgroup.freeze")" = 1 ] \
       && grep -q '^frozen 1' "/sys/fs/cgroup$cg/cgroup.events" \
       && grep -q '^populated 1' "/sys/fs/cgroup$cg/cgroup.events"; then
      note "cgroup freezer: the unit's processes are frozen and still alive"
    else
      note "cgroup.freeze did not take: $(tr '\n' ' ' < "/sys/fs/cgroup$cg/cgroup.events")"; ok=0
    fi
    systemctl thaw a06-freeze.service >/dev/null 2>&1
    for i in $(seq 1 10); do grep -q '^frozen 0' "/sys/fs/cgroup$cg/cgroup.events" && break; sleep 1; done
    [ "$(cat "/sys/fs/cgroup$cg/cgroup.freeze")" = 0 ] || { note "thawing did not take"; ok=0; }
  else
    note "no cgroup.freeze on the unit's cgroup"; ok=0
  fi
  systemctl stop a06-freeze.service >/dev/null 2>&1

  local orig comp
  orig="$(cat /sys/block/zram0/orig_data_size 2>/dev/null || echo 0)"
  comp="$(cat /sys/block/zram0/compr_data_size 2>/dev/null || echo 0)"
  if [ "${comp:-0}" -gt 0 ]; then
    note "factor so far: $(awk -v o="$orig" -v c="$comp" 'BEGIN {printf "%.2f", o/c}') — AP-1.3 measures it under load"
  else
    note "nothing swapped out yet; the factor is measured in AP-1.3 and recorded in decisions/E-05.md"
  fi
  return $(( ! ok ))
}
if check_freezer_zram; then
  verdict AB-A06-4 PASS "freezer holds, zram0 swaps with zstd; the factor is AP-1.3"
else
  verdict AB-A06-4 FAIL "rung 3 of R-C's ladder does not hold"
fi

# -------------------------------------------------------------------------------------------------
# 5  CRIU: dump and restore                                → otherwise rung 4 of the ladder is missing
#    This is the row E-01 hangs on. `criu check` is diagnostics; the verdict is a process that was
#    dumped, killed with it, restored under the same pid, and went on counting from where it stopped.
#    If this fails with a fixed kernel, the base changes — not the order (E-01, E-11).
# -------------------------------------------------------------------------------------------------
check_criu() {
  command -v criu >/dev/null 2>&1 || { note "criu is not in the image"; return 1; }
  note "criu $(criu --version | head -1)"
  criu check >/dev/null 2>&1 && note "criu check: green" || note "criu check: $(criu check 2>&1 | tail -1)"

  local dir="$WORK/criu" counter="$WORK/criu/counter"
  rm -rf "$dir"; mkdir -p "$dir/images"

  # setsid so the process has no controlling terminal: a job with one needs --shell-job, and that is
  # a property of how the check started it rather than of checkpointing. It reports its own pid
  # because setsid forks, so the shell's $! is the wrong one.
  setsid bash -c "echo \$\$ > $dir/pid; n=0; while :; do n=\$((n+1)); printf %s \"\$n\" > $counter; sleep 0.2; done" \
    < /dev/null > "$dir/log" 2>&1 &
  # Out of the job table: the sample is meant to be killed — first by criu, then by this check — and
  # a shell that still owns it prints "Killed" into the middle of the results.
  disown %% 2>/dev/null
  sleep 2
  local pid; pid="$(cat "$dir/pid" 2>/dev/null)"
  [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null || { note "the sample process did not start"; return 1; }

  local at_dump; at_dump="$(cat "$counter" 2>/dev/null)"; [ -n "$at_dump" ] || at_dump=0
  if ! criu dump --tree "$pid" --images-dir "$dir/images" --log-file dump.log -v4; then
    note "dump failed: $(tail -3 "$dir/images/dump.log" 2>/dev/null | tr '\n' ' ')"
    kill -9 "$pid" 2>/dev/null
    return 1
  fi
  kill -0 "$pid" 2>/dev/null && { note "the process survived its own dump"; kill -9 "$pid"; return 1; }
  note "dumped at $at_dump; pid $pid is gone"

  if ! criu restore --images-dir "$dir/images" --restore-detached --log-file restore.log -v4; then
    note "restore failed: $(tail -3 "$dir/images/restore.log" 2>/dev/null | tr '\n' ' ')"
    return 1
  fi
  sleep 2
  local alive=0 after
  kill -0 "$pid" 2>/dev/null && alive=1
  after="$(cat "$counter" 2>/dev/null)"; [ -n "$after" ] || after=0
  kill -9 "$pid" 2>/dev/null
  if [ "$alive" = 1 ] && [ "$after" -gt "$at_dump" ]; then
    note "restored under pid $pid and counting again: $at_dump → $after"
    return 0
  fi
  note "the restored process did not go on (alive=$alive, $at_dump → $after)"
  return 1
}
if check_criu; then
  verdict AB-A06-5 PASS "a sample process was checkpointed and revived"
else
  verdict AB-A06-5 FAIL "no dump and restore — E-01's overturn condition applies"
fi

# -------------------------------------------------------------------------------------------------
# 6  no toolchain, no package manager                      → otherwise A-01 falls
#    The half of the inventory that is in the machine. The other half — the bill of materials — is
#    checked on the host, where the SBOM lies next to the artifact. Also AB-A01-1.
# -------------------------------------------------------------------------------------------------
check_no_toolchain() {
  local ok=1 found=() extra=() b d f
  for b in "${FORBIDDEN_BINARIES[@]}"; do
    command -v "$b" >/dev/null 2>&1 && found+=("$b")
  done
  # Not only on PATH: a compiler in /usr/libexec is a compiler.
  for d in /usr/bin /usr/sbin /usr/libexec /usr/local/bin /usr/local/sbin; do
    [ -d "$d" ] || continue
    for b in "${FORBIDDEN_BINARIES[@]}"; do
      [ -e "$d/$b" ] && case " ${found[*]-} " in *" $b "*) ;; *) extra+=("$d/$b") ;; esac
    done
  done
  if [ "${#found[@]}" -eq 0 ] && [ "${#extra[@]}" -eq 0 ]; then
    note "none of the ${#FORBIDDEN_BINARIES[@]} candidate names is in the image"
  else
    note "found: ${found[*]-} ${extra[*]-}"; ok=0
  fi

  # Globbing rather than find: findutils is not in the image and should not have to be (SP-A02-3),
  # which is the same reason e01-kernel.sh walks the module tree with globstar.
  local man=()
  shopt -s globstar nullglob
  for f in /usr/share/man/**/*; do [ -f "$f" ] && man+=("$f"); done
  shopt -u globstar nullglob
  [ "${#man[@]}" -eq 0 ] && note "no man pages" \
                         || { note "${#man[@]} man pages in the image, e.g. ${man[0]}"; ok=0; }

  local locales; locales="$(locale -a 2>/dev/null | grep -vxE 'C|C\.(UTF-8|utf8)|POSIX' | tr '\n' ' ')"
  [ -z "$locales" ] && note "locales: $(locale -a 2>/dev/null | tr '\n' ' ')" \
                    || { note "locales beyond C.UTF-8: $locales"; ok=0; }
  return $(( ! ok ))
}
if check_no_toolchain; then
  verdict AB-A06-6 PASS "no compiler, interpreter, package manager or editor in the image"
else
  verdict AB-A06-6 FAIL "the image carries something A-01 says it must not"
fi

# -------------------------------------------------------------------------------------------------
# 7  verity                                                → otherwise A-03 falls
#    The half that is a property of the running machine. The other half is the drill on the host:
#    a deliberately damaged copy of this artifact must not start. Also AB-A03-3.
# -------------------------------------------------------------------------------------------------
check_verity() {
  local ok=1 verity=0 d
  for d in /sys/devices/virtual/block/dm-*; do
    [ -r "$d/dm/uuid" ] || continue
    case "$(cat "$d/dm/uuid")" in CRYPT-VERITY-*) verity=1 ;; esac
  done
  [ "$verity" = 1 ] && note "dm-verity carries the root: every block is hashed as it is read" \
                    || { note "read-only without verity is a mount option, not protection"; ok=0; }

  local roothash; roothash="$(sed -n 's/.*roothash=\([0-9a-f]*\).*/\1/p' /proc/cmdline)"
  [ -n "$roothash" ] && note "roothash on the command line: ${roothash:0:16}…" \
                     || { note "no roothash= on the kernel command line"; ok=0; }

  findmnt -no OPTIONS / | grep -qw ro && note "/ is mounted read-only" \
                                      || { note "/ is writable: $(findmnt -no OPTIONS /)"; ok=0; }

  local err
  if err="$(touch /usr/lib/systemd/system/workpod-a06-probe 2>&1)"; then
    rm -f /usr/lib/systemd/system/workpod-a06-probe
    note "a write to /usr succeeded"; ok=0
  else
    note "a write to /usr fails in the kernel: ${err##*: }"
  fi
  return $(( ! ok ))
}
if check_verity; then
  verdict AB-A06-7 PASS "root under dm-verity, / read-only, /usr unwritable"
else
  verdict AB-A06-7 FAIL "the content of this image is not protected"
fi

# -------------------------------------------------------------------------------------------------
# 8  no inbound ports                                      → otherwise B-02 falls
#    From inside. AB-A06-8 asks for the scan from outside and after a role change as well, which is
#    why the row stays with AP-6.3 in the registry: this is the necessary half, not the whole.
# -------------------------------------------------------------------------------------------------
check_no_ports() {
  local listening
  listening="$(ss -Hltn 2>/dev/null | awk '{print $4}' | grep -vE '^(127\.|\[::1\])' || true)"
  note "udp: $(ss -Hlun 2>/dev/null | awk '{print $4}' | grep -vE '^(127\.|\[::1\])' | tr '\n' ' ')"
  if [ -z "$listening" ]; then
    note "no tcp socket listens on anything but loopback — not even SSH (SP-A04-4)"
    return 0
  fi
  note "listening: $(echo "$listening" | tr '\n' ' ')"
  return 1
}
if check_no_ports; then
  verdict AB-A06-8 PASS "nothing listens; the scan from outside is AP-6.3"
else
  verdict AB-A06-8 FAIL "the machine has an inbound surface"
fi

# -------------------------------------------------------------------------------------------------
# 9–13  the rows that need the platform binary, not another image.
# -------------------------------------------------------------------------------------------------
verdict AB-A06-9  SKIP "one job from envelope to patch — turns green in AP-3.8"
verdict AB-A06-10 SKIP "intake, lease, heartbeat, expiry, return — turns green in AP-6.2"
verdict AB-A06-11 SKIP "the same job twice, one push — turns green in AP-3.5"
verdict AB-A06-12 SKIP "two versions at once, no job lost — turns green in AP-6.4"
verdict AB-A06-13 SKIP "500 pods created, 20 active — turns green in AP-1.3"

# -------------------------------------------------------------------------------------------------
# K-04: time is infrastructure, not an operations manual (SP-K04-7). What that requires of an image
# is that the discipline is in it and active by itself — leases and token expiry are functions of
# the clock, so a node nobody has configured must still keep one. Whether it has reached a server
# depends on there being a network; this machine has none, and the state is reported rather than
# demanded.
# -------------------------------------------------------------------------------------------------
check_clock() {
  local ok=1
  command -v chronyd >/dev/null 2>&1 && note "chronyd is in the image" \
                                     || { note "no time synchronization in the image"; ok=0; }
  local active; active="$(systemctl is-active chronyd.service 2>&1)"
  [ "$active" = active ] && note "chronyd.service is active without anyone having enabled it" \
                         || { note "chronyd.service is $active"; ok=0; }
  systemctl is-enabled chronyd.service >/dev/null 2>&1 \
    || { note "chronyd.service is not enabled in the image"; ok=0; }
  [ "$(timedatectl show -p NTP --value 2>/dev/null)" = yes ] \
    && note "timedatectl: NTP enabled" || { note "timedatectl reports NTP off"; ok=0; }
  note "synchronized: $(timedatectl show -p NTPSynchronized --value 2>/dev/null) · $(chronyc -n tracking 2>/dev/null | head -1)"
  return $(( ! ok ))
}
if check_clock; then
  verdict AB-K04-7 PASS "the clock is image content and starts by itself"
else
  verdict AB-K04-7 FAIL "a node with a wrong clock grants and refuses rights arbitrarily"
fi

# -------------------------------------------------------------------------------------------------
# B-01: an image is to be treated as public (SP-B01-2). Node identity comes into being at first
# start and lies encrypted on /var, so nothing that identifies or authenticates a node may be baked
# into the artifact — not a key, not a host key, not even a machine id.
# -------------------------------------------------------------------------------------------------
check_no_secret() {
  local ok=1 keys=() f
  shopt -s globstar nullglob
  for f in /etc/**/*.key /etc/**/*.pem /etc/ssh/*_key /root/.ssh/* /etc/pki/**/*.key \
           /usr/lib/**/*.key /usr/share/**/*.pem; do
    [ -f "$f" ] && keys+=("$f")
  done
  shopt -u globstar nullglob
  # A certificate is public and belongs in the boot path; a private key never does. The filename
  # scan above is coarse on purpose, so the content decides.
  local private=()
  for f in ${keys+"${keys[@]}"}; do
    grep -lq -- '-----BEGIN .*PRIVATE KEY-----' "$f" 2>/dev/null && private+=("$f")
  done
  if grep -rlq -- '-----BEGIN .*PRIVATE KEY-----' /etc /root 2>/dev/null; then
    private+=("$(grep -rl -- '-----BEGIN .*PRIVATE KEY-----' /etc /root 2>/dev/null | tr '\n' ' ')")
  fi
  if [ "${#private[@]}" -eq 0 ]; then
    note "no private key material under /etc, /root or /usr (${#keys[@]} candidate files by name)"
  else
    note "private keys in the image: ${private[*]}"; ok=0
  fi

  # /etc/machine-id cannot be read for this: on a read-only root systemd keeps the id it made at
  # this start in /run and bind-mounts it over the image's file, so reading the path shows the
  # runtime identity and never the artifact's. What the bind mount itself says is the answer —
  # systemd only makes one when the image's file is empty, which is SP-B01-2's "identity comes into
  # being at first start", stated by the mechanism rather than by a file's content.
  local mid_fs; mid_fs="$(findmnt -no FSTYPE /etc/machine-id 2>/dev/null)"
  if [ "$mid_fs" = tmpfs ]; then
    note "the machine id was made at this start and lives in /run: $(cat /etc/machine-id)"
  elif [ ! -s /etc/machine-id ] || [ "$(cat /etc/machine-id)" = uninitialized ]; then
    note "the image's machine id is empty — identity begins at first start"
  else
    note "the image carries a machine id: $(cat /etc/machine-id)"; ok=0
  fi
  [ -e /root/.ssh/authorized_keys ] && { note "authorized_keys in the image"; ok=0; } \
                                    || note "no authorized_keys"
  return $(( ! ok ))
}
if check_no_secret; then
  verdict AB-B01-2 PASS "the artifact carries no standing secret and no identity"
else
  verdict AB-B01-2 FAIL "an image is public, and this one is not safe to be"
fi

printf '\n  %d green, %d red, %d open\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
