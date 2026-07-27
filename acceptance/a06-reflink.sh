#!/usr/bin/env bash
# a06-reflink.sh — AB-A06-2 (and AB-A05-2): a 1 GB snapshot that costs no disk and no time.
#
# This is one row of acceptance/a06-acceptance.sh and it lives in its own file for one reason: it
# has to run somewhere else. The measurement writes two gigabytes — a source file and a real copy
# of it — and where those pages are charged decides whether the machine survives them.
#
# a06-acceptance.sh arrives through `systemd.run=`, so it and everything it forks run under
# system.slice, and AP-3.1 put SP-RC-4's reservation there: MemoryMin=4G. systemd mounts cgroup2
# with `memory_recursiveprot`, so that protection covers the whole subtree, page cache included —
# and on the 2 GB machine this list runs on, a reservation larger than the machine makes every one
# of those two gigabytes unreclaimable. Run 30222311883 is what that costs: the kernel OOM-killed
# chronyd and then systemd itself, thirteen seconds in, holding 1.8 GB of file pages it was
# forbidden to take, and the list never reported another row.
#
# The pages are the work layer's, not the plane's: a snapshot of a workspace is what T-04 and G-03
# take, and workpod-work.slice is deliberately without memory.min so that the kernel has somewhere
# to reclaim from (SP-RC-4). So the measurement is run there, as a transient unit — which is also
# the only place it says anything true, because a reflink in the work layer is the operation the
# platform will actually perform.
#
# It is one process launch and every timing is taken inside it. That matters: wrapping the two `cp`
# calls individually in systemd-run would put unit startup inside the stopwatch, and the stopwatch
# is what distinguishes a reflink from a copy.
#
# Output: one line per observation on stdout, which the caller turns into notes.
# Exit:   0 = the snapshot is O(1) in time and in disk · 1 = it is not

set -uo pipefail

MNT="${1:-/run/a06/reflink}"
OK=1

say() { printf '%s\n' "$1"; }

disk=/dev/disk/by-id/virtio-workpod-scratch
[ -b "$disk" ] || disk=/dev/vdb
if [ ! -b "$disk" ]; then
  say "no scratch disk — image/vm.sh --disk gives the machine one"
  exit 1
fi

mkdir -p "$MNT"
if ! mkfs.btrfs -q -f -L a06work "$disk" 2>&1; then
  say "mkfs.btrfs failed"
  exit 1
fi
mount "$disk" "$MNT" || { say "mounting btrfs failed — is the module in the image?"; exit 1; }

used() { sync; btrfs filesystem usage -b "$MNT" | awk '$1 == "Used:" { print $2; exit }'; }

dd if=/dev/zero of="$MNT/src" bs=1M count=1024 status=none
used0="$(used)"

t0=$(date +%s%N)
if cp --reflink=always "$MNT/src" "$MNT/snapshot"; then
  reflink_ms=$(( ($(date +%s%N) - t0) / 1000000 ))
  used1="$(used)"
else
  say "$(stat -fc %T "$MNT") cannot reflink — btrfs or XFS is required (SP-A05-2)"
  umount "$MNT"; exit 1
fi

# --sparse=never, because the source is a gigabyte of zeroes and cp punches holes for those by
# default. A sparse copy would cost no disk either, and the control measurement — that the
# instrument can see a gigabyte arrive — would quietly measure nothing.
t0=$(date +%s%N)
cp --reflink=never --sparse=never "$MNT/src" "$MNT/full-copy"
copy_ms=$(( ($(date +%s%N) - t0) / 1000000 ))
used2="$(used)"

snapshot_growth=$(( (used1 - used0) / 1048576 ))
copy_growth=$(( (used2 - used1) / 1048576 ))
say "reflink: ${reflink_ms} ms, +${snapshot_growth} MB — a real copy of the same file: ${copy_ms} ms, +${copy_growth} MB"
say "$(btrfs filesystem du -s --raw "$MNT/snapshot" | tail -1)"
say "charged to $(cat /proc/self/cgroup | sed 's/^0:://')"

# 64 MB of slack for the metadata the copy does write; a gigabyte of data would not fit in it.
[ "$snapshot_growth" -lt 64 ] || { say "the snapshot cost ${snapshot_growth} MB — that is a copy"; OK=0; }
[ "$copy_growth" -gt 900 ]    || { say "a real copy cost only ${copy_growth} MB — the measurement cannot see a gigabyte"; OK=0; }
[ "$reflink_ms" -lt 1000 ]    || { say "${reflink_ms} ms is not O(1)"; OK=0; }
# An order of magnitude faster than the copy it replaces, or fast enough that the comparison is
# measuring process startup rather than the filesystem. Without the second clause an emulated
# runner could fail this by being uniformly slow, which is not a fact about reflink.
[ "$reflink_ms" -lt 200 ] || [ $(( reflink_ms * 10 )) -lt "$copy_ms" ] \
  || { say "the snapshot is not an order of magnitude faster than the copy it replaces"; OK=0; }

umount "$MNT"
exit $(( ! OK ))
