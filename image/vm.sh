#!/usr/bin/env bash
# vm.sh — run a script inside the built image and hand back what it said (AP-1.1).
#
#   image/vm.sh [--role ROLE] [--file PATH]... [--disk SIZE] [--image PATH] [--timeout SECONDS]
#               [--memory MB] [--cpus N] SCRIPT [ARG]...
#
# E-11's first step reads "A-06 as a script against a bare mkosi VM". This is the "against": the
# checks stay outside the image and are carried in at boot, so the artifact under test never has to
# contain its own test. AP-1.2 runs a06-acceptance.sh through the same door.
#
# How a script gets in. systemd reads credentials from SMBIOS OEM strings — in a virtual machine
# exactly as a node reads its boot values from instance data, which is what E-01 rules for the five
# values from A-04. qemu carries them, the manager places them in /run/credentials/@system, and
# `systemd.run=` on the command line appended at runtime executes the one this script wrote. Both
# are mechanisms the image already lives by; neither is a door opened for testing.
#
# SCRIPT lands as the credential `workpod.script`, every --file next to it under its own basename,
# and --role as `workpod.role` — which is the credential the image's own role generator reads, not
# a test fixture.
#
# Two options exist for AP-1.2, and both are about the disk rather than the check:
#
#   --disk SIZE   attaches a second, empty virtio disk, reachable in the machine as
#                 /dev/disk/by-id/virtio-workpod-scratch. The image has no data partition until
#                 AP-3.1 builds A-05's layout, and AB-A06-2 has to measure a reflink snapshot on a
#                 real filesystem — a tmpfs cannot reflink and would turn the measurement into a
#                 skip. The disk is a throwaway file in this script's temporary directory.
#   --image PATH  boots PATH instead of the build's `workpod.raw`. AB-A06-7's drill damages a copy
#                 of the artifact on purpose and needs the damaged copy to boot, or rather to fail
#                 to.
#
# AP-1.3 added two more, and both are about the machine rather than the check: the calibration run
# creates five hundred pods and lets twenty of them work (A-06's last row), and the constants it
# measures are memory and cores. 2048 MB and two cores are enough to hold a check and not enough to
# hold a fleet, so the size of the machine has to be sayable — and it is printed with every number
# the run reports, because a measured constant without the machine it was measured on is a number
# without a unit.
#
#   --memory MB   RAM of the guest, default 2048
#   --cpus N      vCPUs of the guest, default 2
#
# Two things about the guest are worth knowing before reading its output:
#
#   * systemd-run-generator points default.target at its own target, so the boot stops short of
#     multi-user.target. A check that wants a normally booted node has to ask for one — see
#     acceptance/a02-roles.sh, which does.
#   * an exit status cannot travel over a console, so the wrapper prints it as a trailer line and
#     this script reads it back. Everything the guest printed goes to stderr either way, so a
#     failed run is diagnosable and not merely known.
#
# The image is booted ephemerally, on a throwaway copy. Booting the artifact itself would let the
# firmware write to its ESP, and the seal from AB-A03-7 is over exactly those bytes.
#
# Exit:  the guest script's exit code, or
#        124 on timeout · 125 when the guest never reported one · 2 on a usage error, which
#        includes a payload too large for the SMBIOS table to carry (see the check further down)

set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
OUTPUT="${OUTPUT:-$HERE/.build/pass1}"
TIMEOUT=600
ROLE=""
FILES=()
SCRATCH=""
IMAGE=""
MEMORY=2048
CPUS=2

usage() { sed -n '3p' "$0" | sed 's/^# *//' >&2; exit 2; }

while [ $# -gt 0 ]; do
  case "$1" in
    --role)    ROLE="${2:?}"; shift 2 ;;
    --file)    FILES+=("${2:?}"); shift 2 ;;
    --disk)    SCRATCH="${2:?}"; shift 2 ;;
    --image)   IMAGE="${2:?}"; shift 2 ;;
    --timeout) TIMEOUT="${2:?}"; shift 2 ;;
    --memory)  MEMORY="${2:?}"; shift 2 ;;
    --cpus)    CPUS="${2:?}"; shift 2 ;;
    --output)  OUTPUT="${2:?}"; shift 2 ;;
    --)        shift; break ;;
    -*)        echo "vm.sh: unknown option $1" >&2; usage ;;
    *)         break ;;
  esac
done

[ $# -ge 1 ] || usage
SCRIPT="$1"; shift
[ -r "$SCRIPT" ] || { echo "vm.sh: cannot read $SCRIPT" >&2; exit 2; }

command -v qemu-system-x86_64 >/dev/null 2>&1 || { echo "vm.sh: qemu-system-x86_64 is not installed" >&2; exit 2; }
if [ -z "$IMAGE" ]; then
  [ -d "$OUTPUT" ] || { echo "vm.sh: no image in $OUTPUT — run image/build.sh first" >&2; exit 2; }
  IMAGE="$OUTPUT/workpod.raw"
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
CREDS="$WORK/credentials"
mkdir -p "$CREDS"

# Each file here becomes one credential, named after the file, and lands in the machine under
# /run/credentials/@system.
install -m 0644 "$SCRIPT" "$CREDS/workpod.script"
for f in ${FILES+"${FILES[@]}"}; do
  [ -r "$f" ] || { echo "vm.sh: cannot read $f" >&2; exit 2; }
  install -m 0644 "$f" "$CREDS/$(basename "$f")"
done
if [ -n "$ROLE" ]; then
  printf '%s' "$ROLE" > "$CREDS/workpod.role"
  chmod 0644 "$CREDS/workpod.role"
fi

# The wrapper is what systemd.run= starts. It exists so the guest's exit code has somewhere to go,
# and it carries the arguments so the command line stays free of nested quoting.
#
# The trailer goes to /dev/console rather than to stdout. A service's stdout is the journal, and the
# journal reaches the serial line only by being forwarded — which rewrites every line with a
# timestamp and a syslog identifier, and is rate limited. Run 21 is what that costs: the check ran,
# printed its verdict, and the run still failed 125 because the line the host was looking for had
# arrived as `[    6.956905] bash[378]: WORKPOD-EXIT: 1`. Diagnostics can afford the journal; the
# verdict cannot. The fallback keeps a machine whose console cannot be opened diagnosable instead of
# silent.
{
  echo '# written by image/vm.sh'
  echo 'export CREDENTIALS_DIRECTORY=/run/credentials/@system'
  printf 'bash "$CREDENTIALS_DIRECTORY/workpod.script"'
  for a in "$@"; do printf ' %q' "$a"; done
  echo
  echo 'rc=$?'
  echo 'echo "WORKPOD-EXIT: $rc" > /dev/console 2>/dev/null || echo "WORKPOD-EXIT: $rc"'
} > "$CREDS/workpod.check"
chmod 0644 "$CREDS/workpod.check"

# The credentials, in the encoding systemd reads them in: one SMBIOS type 11 OEM string per
# credential, `io.systemd.credential.binary:<name>=<base64>`. This is what mkosi builds too, and
# writing it out here is the point — every step from this file to the machine is visible.
SMB="$WORK/smbios"
mkdir -p "$SMB"
SMBIOS=()
for c in "$CREDS"/*; do
  name="$(basename "$c")"
  { printf 'io.systemd.credential.binary:%s=' "$name"; base64 -w0 < "$c"; } > "$SMB/$name"
  SMBIOS+=(-smbios "type=11,path=$SMB/$name")
done

# The command line appended at runtime. systemd-stub reads this OEM string and adds it to the one
# baked into the unified kernel image — which is the only way to start something in a machine whose
# command line is otherwise sealed inside a signed artifact. Commas are doubled because qemu reads
# them as its own separators; the quoting around systemd.run= is what systemd-run-generator(8)
# documents, and it is what keeps the command from being split at its space.
EXTRA='systemd.run="/bin/bash /run/credentials/@system/workpod.check"'
EXTRA="$EXTRA systemd.run_success_action=poweroff systemd.run_failure_action=poweroff"
EXTRA="${EXTRA//,/,,}"
SMBIOS+=(-smbios "type=11,value=io.systemd.stub.kernel-cmdline-extra=$EXTRA")

# And the limit that carries all of this: an SMBIOS structure is addressed with a 16-bit length, so
# the OEM string table cannot exceed 64 KB — and when it does, nothing is dropped selectively and
# nothing complains. qemu builds the table, the firmware refuses it whole, and the machine boots
# without credentials, without a role and without the appended command line: straight past
# default.target to a login prompt, where it sits until the timeout kills it.
#
# Run 26 is what that costs. `calibration.sh` carried both its halves in one file, 64,948 bytes in
# base64, and spent fifteen minutes at a getty. The check was correct, the payload could not arrive,
# and nothing in the log said so — the kernel command line in the boot header was simply missing its
# tail. So the size is checked here, before a machine is started, and the message names the file to
# split. A check that cannot be delivered is not a red row, it is a silent one.
payload=0
for f in "$SMB"/*; do payload=$(( payload + $(wc -c < "$f") )); done
payload=$(( payload + ${#EXTRA} + 64 ))     # the appended command line, plus the structure itself
if [ "$payload" -gt 63488 ]; then           # 62 KB, leaving room for the tables qemu adds itself
  echo "vm.sh: the credentials come to $payload bytes of SMBIOS OEM strings, and the table holds 64 KB." >&2
  echo "        The largest is $(ls -S "$SMB" | head -1) at $(wc -c < "$SMB/$(ls -S "$SMB" | head -1)") bytes." >&2
  echo "        Nothing would arrive in the machine and it would boot to a login prompt (run 26)." >&2
  echo "        Split the check: the half that drives stays on the host, the half that probes travels." >&2
  exit 2
fi
echo "   credentials $payload bytes of 63488" >&2

# The firmware. A UEFI machine needs the code half read-only and a writable copy of the variables,
# and the paths differ by distribution, so they are searched rather than assumed.
OVMF_CODE=""
for f in /usr/share/edk2/ovmf/OVMF_CODE.fd /usr/share/OVMF/OVMF_CODE.fd \
         /usr/share/edk2-ovmf/x64/OVMF_CODE.fd /usr/share/qemu/edk2-x86_64-code.fd \
         $(ls -1 /usr/share/edk2/ovmf/OVMF_CODE*.fd /usr/share/OVMF/OVMF_CODE*.fd 2>/dev/null); do
  case "$f" in *secboot*) continue ;; esac   # secure boot refuses the command line appended above
  [ -f "$f" ] && { OVMF_CODE="$f"; break; }
done
[ -n "$OVMF_CODE" ] || { echo "vm.sh: no OVMF firmware found — install edk2-ovmf" >&2; exit 2; }
OVMF_VARS="${OVMF_CODE%CODE.fd}VARS.fd"
[ -f "$OVMF_VARS" ] || OVMF_VARS="$(dirname "$OVMF_CODE")/OVMF_VARS.fd"
[ -f "$OVMF_VARS" ] || { echo "vm.sh: no OVMF variables template next to $OVMF_CODE" >&2; exit 2; }
install -m 0644 "$OVMF_VARS" "$WORK/vars.fd"

[ -f "$IMAGE" ] || { echo "vm.sh: no image at $IMAGE — run image/build.sh first" >&2; exit 2; }

# The disks. The root is always read through a snapshot (see below); the scratch disk is written
# straight through, because it is a file in $WORK that the trap deletes either way. `serial=` is
# what makes it findable in the machine by name instead of by device order — udev builds
# /dev/disk/by-id/virtio-workpod-scratch from it.
DISKS=(-drive "if=none,id=root,format=raw,snapshot=on,file=$IMAGE"
       -device virtio-blk-pci,drive=root)
if [ -n "$SCRATCH" ]; then
  truncate -s "$SCRATCH" "$WORK/scratch.raw" \
    || { echo "vm.sh: cannot create a ${SCRATCH} scratch disk" >&2; exit 2; }
  DISKS+=(-drive "if=none,id=scratch,format=raw,file=$WORK/scratch.raw"
          -device virtio-blk-pci,drive=scratch,serial=workpod-scratch)
fi

CONSOLE="$WORK/console.log"
echo "== vm (role ${ROLE:-none}): $(basename "$SCRIPT") $*" >&2
echo "   image $IMAGE${SCRATCH:+ · scratch $SCRATCH}" >&2
echo "   ${CPUS} vcpu · ${MEMORY} MB" >&2
echo "   firmware $OVMF_CODE" >&2
[ -c /dev/kvm ] && echo "   /dev/kvm present" >&2 || echo "   no /dev/kvm — emulated, slower" >&2

# qemu is driven directly rather than through `mkosi vm`. mkosi's own path registers the machine
# with systemd-machined and puts qemu in a scope, and in a build container with no systemd running
# there is nothing to register with: run 19 and run 20 sat for ten minutes each and produced not one
# line on either console. What a check needs is a disk, a firmware, a console and the two SMBIOS
# strings above — all of which are written out here, where they can be read.
#
# snapshot=on keeps every write in a temporary file. The artifact must come out of this unchanged:
# the seal from AB-A03-7 is over exactly these bytes.
#
# The console is the serial port the image's own command line names, so the check's output and the
# kernel's log arrive on the same line, in order. -no-reboot turns a boot loop into an exit.
timeout --foreground "$TIMEOUT" \
  qemu-system-x86_64 \
    -machine q35,accel=kvm:tcg -cpu max -smp "$CPUS" -m "$MEMORY" \
    -display none -nodefaults -no-reboot \
    -drive "if=pflash,unit=0,format=raw,readonly=on,file=$OVMF_CODE" \
    -drive "if=pflash,unit=1,format=raw,file=$WORK/vars.fd" \
    "${DISKS[@]}" \
    -object rng-random,filename=/dev/urandom,id=rng0 -device virtio-rng-pci,rng=rng0 \
    -serial stdio \
    "${SMBIOS[@]}" > "$CONSOLE" 2>&1
rc=$?

# A serial console leaves carriage returns behind; stripping them makes the log grep and read like
# a log rather than like a terminal recording.
tr -d '\r' < "$CONSOLE" >&2

if [ "$rc" = 124 ]; then
  echo "vm.sh: the guest did not finish within ${TIMEOUT}s" >&2
  exit 124
fi

# Anything may precede the trailer on its line: a kernel message that printk left without a newline,
# or the journal's own prefix when the console could not be opened and the fallback above ran. The
# number ends the line either way, and it is the last such line the guest wrote.
STATUS="$(tr -d '\r' < "$CONSOLE" | sed -n 's/.*WORKPOD-EXIT: \([0-9]\{1,3\}\)$/\1/p' | tail -1)"
if [ -z "$STATUS" ]; then
  echo "vm.sh: the guest never reported an exit code — the boot did not reach the check (qemu exited $rc)" >&2
  exit 125
fi
exit "$STATUS"
