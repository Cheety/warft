#!/usr/bin/env bash
# e01-kernel.sh — the kernel of the built image against image/kernel-requirements.conf (AB-E01-1).
#
#   acceptance/e01-kernel.sh          boot the image and check it there
#   acceptance/e01-kernel.sh probe    the check itself; runs inside the machine
#
# AB-E01-1 reads "mkosi on Fedora — image built, the kernel configuration is a file in the
# repository". E-01 takes the Fedora kernel, so that file is not a `.config` somebody here wrote
# but the list of options the image is held to; decisions/kernel-configuration.md rules why those
# are the same thing for this purpose and where they are not.
#
# The check reads the kernel's own configuration at /usr/lib/modules/<kver>/config, which Fedora
# ships in kernel-core, and the module tree next to it. Both come from the running system rather
# than from the package list that was supposed to produce it — a package list checking itself would
# check nothing.
#
# Exit:  0 = every requirement met, AB-E01-1 is evidenced by this run
#        1 = at least one is not

set -uo pipefail

MODE="${1:-drive}"
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"

if [ "$MODE" = drive ]; then
  exec "$ROOT/image/vm.sh" --role work --file "$ROOT/image/kernel-requirements.conf" \
       "$HERE/e01-kernel.sh" probe
fi

REQUIREMENTS="${CREDENTIALS_DIRECTORY:-/run/credentials/@system}/kernel-requirements.conf"
[ -r "$REQUIREMENTS" ] || REQUIREMENTS="$ROOT/image/kernel-requirements.conf"

PASS=0; FAIL=0

pass() { printf '  \033[32mPASS\033[0m  %-30s %s\n' "$1" "${2:-}"; PASS=$((PASS+1)); }
fail() { printf '  \033[31mFAIL\033[0m  %-30s %s\n' "$1" "${2:-}"; FAIL=$((FAIL+1)); }

KVER="$(uname -r)"
MODULES="/usr/lib/modules/$KVER"
[ -d "$MODULES" ] || MODULES="/lib/modules/$KVER"
CONFIG="$MODULES/config"

printf '\n\033[1mAB-E01-1 — the kernel of this image against %s\033[0m\n' "$(basename "$REQUIREMENTS")"
printf '  kernel %s\n\n' "$KVER"

[ -r "$REQUIREMENTS" ] || { echo "  no requirements file at $REQUIREMENTS" >&2; exit 1; }
if [ ! -r "$CONFIG" ]; then
  echo "  the kernel ships no configuration at $CONFIG." >&2
  echo "  Without it the image cannot be held to SP-A02-2 at all, which is AB-E01-1 failing." >&2
  exit 1
fi

# Every module in the image, once, by name with `-` folded to `_` the way modinfo reports it. Built
# with bash's own globbing rather than find, because findutils is not in the image and should not
# have to be (SP-A02-3).
shopt -s globstar nullglob
declare -A MODULE_PRESENT=()
MODULE_PATHS=()
for m in "$MODULES"/kernel/**/*.ko*; do
  MODULE_PATHS+=("${m#"$MODULES"/}")
  name="${m##*/}"; name="${name%%.ko*}"
  MODULE_PRESENT["${name//-/_}"]=1
done
shopt -u globstar nullglob

# `y` means built in; `m` means built in or a module, and then the module has to be in the image.
# The distinction is the point: an option Fedora sets does not put a driver on the disk.
symbol_state() {  # $1 = symbol without CONFIG_ → prints y, m, or nothing
  sed -n "s/^CONFIG_$1=\\(.*\\)/\\1/p" "$CONFIG" | head -1
}

requirement_met() {  # $1 = symbol, $2 = y|m, $3 = module or -
  local state; state="$(symbol_state "$1")"
  case "$state" in
    y) return 0 ;;
    m) [ "$2" = m ] || return 1
       [ "$3" = - ] && return 0
       [ -n "${MODULE_PRESENT[${3//-/_}]:-}" ] ;;
    *) return 1 ;;
  esac
}

declare -A GROUP_MET=() GROUP_TRIED=()

while read -r keyword a b c d; do
  case "$keyword" in
    ''|'#'*) continue ;;

    require)
      state="$(symbol_state "$a")"
      if requirement_met "$a" "$b" "${c:--}"; then
        pass "CONFIG_$a" "$state${c:+ · $c}"
      else
        fail "CONFIG_$a" "wanted $b${c:+ (module $c)}, found ${state:-nothing}"
      fi
      ;;

    require-one)
      GROUP_TRIED["$a"]=1
      if requirement_met "$b" "$c" "${d:--}"; then GROUP_MET["$a"]=1; fi
      ;;

    unset)
      state="$(symbol_state "$a")"
      if [ -z "$state" ]; then
        pass "CONFIG_$a" "not set, as required"
      else
        fail "CONFIG_$a" "set to $state"
      fi
      ;;

    exclude)
      # Name what is there, not only how many. A count says the mechanism failed; a name says which
      # module to trace, and with mkosi the answer is usually that something outside this directory
      # depends on it and dragged it back in.
      found=()
      for p in "${MODULE_PATHS[@]}"; do
        case "$p" in "$a"/*) name="${p##*/}"; found+=("${name%%.ko*}") ;; esac
      done
      if [ "${#found[@]}" -eq 0 ]; then
        pass "out: $a" "no module"
      else
        fail "out: $a" "${found[*]}"
      fi
      ;;

    absent)
      if [ -z "${MODULE_PRESENT[${a//-/_}]:-}" ]; then
        pass "absent: $a" "not in the image"
      else
        fail "absent: $a" "still in the image"
      fi
      ;;

    *) fail "$keyword" "unknown keyword in $(basename "$REQUIREMENTS")" ;;
  esac
done < "$REQUIREMENTS"

for g in "${!GROUP_TRIED[@]}"; do
  if [ -n "${GROUP_MET[$g]:-}" ]; then
    pass "one of: $g" "satisfied"
  else
    fail "one of: $g" "no alternative in $(basename "$REQUIREMENTS") is satisfied"
  fi
done

printf '\n  %d met, %d not\n\n' "$PASS" "$FAIL"
if [ "$FAIL" -gt 0 ]; then
  echo "  A requirement the Fedora kernel does not meet is not a bug to be worked around: it is"
  echo "  E-01's overturn condition (decisions/E-01.md, decisions/kernel-configuration.md)."
  exit 1
fi
echo "  AB-E01-1 green through this run: the kernel configuration is a file, and the image keeps it."
exit 0
