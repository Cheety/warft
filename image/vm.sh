#!/usr/bin/env bash
# vm.sh — run a script inside the built image and hand back what it said (AP-1.1).
#
#   image/vm.sh [--role ROLE] [--file PATH]... [--timeout SECONDS] SCRIPT [ARG]...
#
# E-11's first step reads "A-06 as a script against a bare mkosi VM". This is the "against": the
# checks stay outside the image and are carried in at boot, so the artifact under test never has to
# contain its own test. AP-1.2 runs a06-acceptance.sh through the same door.
#
# How a script gets in. systemd reads credentials from SMBIOS OEM strings — in a virtual machine
# exactly as a node reads its boot values from instance data, which is what E-01 rules for the five
# values from A-04. mkosi hands them to qemu, the manager places them in /run/credentials/@system,
# and `systemd.run=` on the command line appended at runtime executes the one this script wrote.
# Both are mechanisms the image already lives by; neither is a door opened for testing.
#
# SCRIPT lands as the credential `workpod.script`, every --file next to it under its own basename,
# and --role as `workpod.role` — which is the credential the image's own role generator reads, not
# a test fixture.
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
#        124 on timeout · 125 when the guest never reported one · 2 on a usage error

set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
OUTPUT="${OUTPUT:-$HERE/.build/pass1}"
TIMEOUT=600
ROLE=""
FILES=()

usage() { sed -n '3p' "$0" | sed 's/^# *//' >&2; exit 2; }

while [ $# -gt 0 ]; do
  case "$1" in
    --role)    ROLE="${2:?}"; shift 2 ;;
    --file)    FILES+=("${2:?}"); shift 2 ;;
    --timeout) TIMEOUT="${2:?}"; shift 2 ;;
    --output)  OUTPUT="${2:?}"; shift 2 ;;
    --)        shift; break ;;
    -*)        echo "vm.sh: unknown option $1" >&2; usage ;;
    *)         break ;;
  esac
done

[ $# -ge 1 ] || usage
SCRIPT="$1"; shift
[ -r "$SCRIPT" ] || { echo "vm.sh: cannot read $SCRIPT" >&2; exit 2; }

command -v mkosi >/dev/null 2>&1 || { echo "vm.sh: mkosi is not installed" >&2; exit 2; }
[ -d "$OUTPUT" ] || { echo "vm.sh: no image in $OUTPUT — run image/build.sh first" >&2; exit 2; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
CREDS="$WORK/credentials"
mkdir -p "$CREDS"

# mkosi takes a directory of credentials and names each one after its file. Every file here is
# deliberately left non-executable: mkosi runs an executable credential file on the *host* and
# passes its output instead of its contents, which for a check script would run it in the wrong
# machine entirely.
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
{
  echo '# written by image/vm.sh'
  echo 'export CREDENTIALS_DIRECTORY=/run/credentials/@system'
  printf 'bash "$CREDENTIALS_DIRECTORY/workpod.script"'
  for a in "$@"; do printf ' %q' "$a"; done
  echo
  echo 'echo "WORKPOD-EXIT: $?"'
} > "$CREDS/workpod.check"
chmod 0644 "$CREDS/workpod.check"

CONSOLE="$WORK/console.log"
echo "== vm (role ${ROLE:-none}): $(basename "$SCRIPT") $*" >&2

# --console=native puts the guest console straight on stdio; the other modes need
# systemd-pty-forward and a terminal, which a build agent has no business requiring.
# --firmware=uefi rather than the default, which prefers secure boot: systemd-stub only accepts a
# command line appended at runtime when secure boot is off, and that command line is how the check
# is started at all.
timeout --foreground "$TIMEOUT" \
  mkosi --directory "$HERE" --output-directory "$OUTPUT" \
        --ephemeral --console=native --firmware=uefi \
        --credential "$CREDS" \
        --kernel-command-line-extra \
          "systemd.run=\"/bin/bash /run/credentials/@system/workpod.check\" systemd.run_success_action=poweroff systemd.run_failure_action=poweroff" \
        vm > "$CONSOLE" 2>&1
rc=$?

# A serial console leaves carriage returns behind; stripping them makes the log grep and read like
# a log rather than like a terminal recording.
tr -d '\r' < "$CONSOLE" >&2

if [ "$rc" = 124 ]; then
  echo "vm.sh: the guest did not finish within ${TIMEOUT}s" >&2
  exit 124
fi

STATUS="$(tr -d '\r' < "$CONSOLE" | sed -n 's/^WORKPOD-EXIT: \([0-9]\{1,3\}\)$/\1/p' | tail -1)"
if [ -z "$STATUS" ]; then
  echo "vm.sh: the guest never reported an exit code — the boot did not reach the check (mkosi exited $rc)" >&2
  exit 125
fi
exit "$STATUS"
