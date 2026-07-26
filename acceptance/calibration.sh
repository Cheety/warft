#!/usr/bin/env bash
# calibration.sh — the calibration run (AP-1.3, A-06's last row: AB-A06-13, AB-A06-4, AB-E05-1).
#
#   acceptance/calibration.sh            boot the build, run the fleet, measure the five constants
#   CAL_REPLAY=<dir> acceptance/calibration.sh    compose the table from a saved console log
#
# This is the half that stays on the host: it boots the artifact twice, reads what the machine wrote
# to its console and composes the table. acceptance/calibration-probe.sh is the half that goes into
# the machine — two files rather than one, because a credential travels as an SMBIOS OEM string and
# that table is capped at 64 KB (see image/vm.sh, and run 26 in image/README.md).
#
# E-05 rules that the five constants are planning values of the occupancy table and that the runtime
# never reads them. Its overturn condition is this run: "the calibration run from A-06. It changes
# numbers, not rules — and enters them into the table instead of discussing them."
#
# So this run measures, and what it measures lands in decisions/E-05.md next to the given values and
# in acceptance/e05-constants.tsv, which is the machine-readable half of that ruling and what R-D
# computes with. The five, with the panel's own "measured in" column beside them:
#
#   host and runtime per role   12 GB control · 6 GB work    measured in A-06          ← this run
#   page cache baseline         8 GB                         measured in A-06          ← this run
#   pages per frozen pod        24 MB                        measured in A-06          ← this run
#   zram factor                 1.6                          measured in A-06          ← this run
#   active pod                  0.8 cores · 960 MB           three runs per repo (R-C) ← AP-3.7
#
# The fifth is the panel's own exception and it is kept: an active pod's cost is what a real job does
# to a machine, and at stage 1 there is no job — no harness (T-01), no container image (T-03), no
# runtime (AP-3.1). This run measures the mix's shape and cost on the machine it has, reports it, and
# does not put it into the table. AP-3.7 does that, from R-C's three runs per repository.
#
# The other four are measured on a node that is a bare image, which is what A-06 is. Two of them are
# floors and say so: host and runtime carries no control plane yet, and the page cache carries no
# base layers. A floor with the condition of its measurement attached is a number; a floor presented
# as the constant would be the explanation Q-02 rejects, wearing the colour of a result.
#
# What a pod is here: a cgroup in `workpod-pods.slice` with a shell in it, created through
# `systemd-run` — the mechanism SP-A02-4 gives the platform for R-A and R-C, and the same slice
# a06-acceptance.sh reads pressure from. It carries no harness, so the frozen-pod number is the cost
# of the mechanism and not of a pod at work.
#
# Exit:  0 = AB-A06-13, AB-A06-4 and AB-E05-1 are evidenced by this run
#        1 = at least one of them is not
#        2 = there is nothing to run against (no image, no probe)

set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
PROBE="$HERE/calibration-probe.sh"

# The fleet from A-06's last row, and what the machine is told to build. Not parameters: the row
# reads "500 pods created, 20 active", and a check whose shape can be turned down is a check that
# will be. The class mix underneath them — E-05's 20/30/35/15 over R-A's four classes — belongs to
# the probe, because it is the machine that has to build it.
POD_TOTAL=500
POD_ACTIVE=20

# The twenty active pods together request 19.2 GB of RAM — four times what a build runner can give a
# machine. The load therefore runs the mix at a stated fraction of each class request, and the
# fraction travels with every number it produces. This is the one place where the run cannot do what
# the panel describes, and it is the reason the active-pod constant is recorded and not adopted.
ACTIVE_SCALE=8
WINDOW_S=60          # the active mix runs this long; per-pod cpu is its usage over this window

OUTPUT="${OUTPUT:-$ROOT/image/.build/pass1}"
IMAGE="$OUTPUT/workpod.raw"
CONSTANTS="$HERE/e05-constants.tsv"
RULING="$ROOT/decisions/E-05.md"

# 6 GB and 4 cores: 500 pods and a working set of a few gigabytes do not fit in the 2 GB a check
# gets, and a GitHub runner has 16 GB and 4 cores in total. The machine is as large as the
# measurement needs and as small as the host can spare, and it is printed with the numbers.
VM_MEMORY="${CAL_MEMORY:-6144}"
VM_CPUS="${CAL_CPUS:-4}"

LOG="$(mktemp -d)"
trap 'rm -rf "$LOG"' EXIT

# A door for checking the arithmetic below without a build machine: CAL_REPLAY names a directory
# with a `probe` and a `baseline` console log from an earlier run, and the host side composes from
# those instead of booting. Everything after the two boots is arithmetic over marker lines, and
# arithmetic that has never run over real output is a guess — replaying is how the rest of this
# repository is checked when it cannot be booted (image/README.md).
if [ -n "${CAL_REPLAY:-}" ]; then
  [ -r "$CAL_REPLAY/probe" ] || { echo "calibration: no $CAL_REPLAY/probe" >&2; exit 2; }
  cp "$CAL_REPLAY/probe" "$LOG/probe"
  cp "$CAL_REPLAY/baseline" "$LOG/baseline" 2>/dev/null || : > "$LOG/baseline"
  probe_rc=0; baseline_rc=0
  echo "== replaying $CAL_REPLAY — no machine was booted for this table" >&2
else
  [ -f "$IMAGE" ] || { echo "calibration: no image at $IMAGE — run image/build.sh first" >&2; exit 2; }
  [ -r "$PROBE" ] || { echo "calibration: no probe at $PROBE" >&2; exit 2; }

  # 1 — the run itself, as a work node. 900 seconds: the fleet takes about a minute to create, the
  #     mix runs for WINDOW_S, and the pressure event with its decay is the same order again.
  "$ROOT/image/vm.sh" --role work --memory "$VM_MEMORY" --cpus "$VM_CPUS" --timeout 900 \
      "$PROBE" probe "$POD_TOTAL" "$POD_ACTIVE" "$ACTIVE_SCALE" "$WINDOW_S" 2>&1 | tee "$LOG/probe"
  probe_rc=$?

  # 2 — host and runtime again, as a control node. E-05 gives that constant per role (12 GB
  #     control · 6 GB work), so it is measured per role. Nothing else needs a second boot, which
  #     is why this one measures at rest and is over in a fraction of the time.
  "$ROOT/image/vm.sh" --role control --memory "$VM_MEMORY" --cpus "$VM_CPUS" --timeout 240 \
      "$PROBE" baseline 2>&1 | tee "$LOG/baseline"
  baseline_rc=$?
fi

# The markers, off the console. Anything may stand in front of one — a printk fragment on the same
# line, or the journal's prefix when the guest's console write fell back to stdout (run 22).
cat "$LOG/probe" "$LOG/baseline" \
  | sed -n 's/.*WORKPOD-CAL: \([a-z0-9_]*\) \(.*\)$/\1 \2/p' > "$LOG/marks"

m() {  # $1 = key, $2 = what to print when the machine did not report it
  local v
  v="$(awk -v k="$1" '$1 == k { $1=""; sub(/^ +/, ""); print; exit }' "$LOG/marks")"
  printf '%s' "${v:-${2-}}"
}

# -----------------------------------------------------------------------------------------------
# R-D, ported from the occupancy table's own computation so the comparison is against the table
# and not against a description of it. Left of its sliders: one node. Right of them: the cell.
# Frozen pods lie compressed and active pods lie hot, which is why the zram factor divides the
# frozen pod's pages and nothing else.
# -----------------------------------------------------------------------------------------------
rd() {  # ram_gb cores nodes fleet rush_pct | host_gb cache_gb frost_mb zram pod_mb cores_per_pod
  awk -v ram="$1" -v cores="$2" -v nodes="$3" -v fleet="$4" -v rush="$5" \
      -v host="$6" -v cache="$7" -v frost="$8" -v zram="$9" -v podmb="${10}" -v perpod="${11}" \
      'BEGIN {
    frostmb  = frost / zram
    pernode  = fleet / nodes
    avail    = ram - host - cache; if (avail < 0) avail = 0; avail *= 1024
    baseload = pernode * frostmb
    rest     = avail - baseload
    sur      = podmb - frostmb; if (sur < 1) sur = 1
    ramslots = rest > 0 ? int(rest / sur) : 0
    cpuslots = perpod > 0 ? int(cores / perpod) : 0
    ceilnode = int(pernode); if (ceilnode < pernode) ceilnode++
    slots    = ramslots < cpuslots ? ramslots : cpuslots
    if (ceilnode < slots) slots = ceilnode
    total    = slots * nodes; if (total > fleet) total = fleet
    want     = int(fleet * rush / 100 + 0.5)
    active   = want < total ? want : total
    # One word: the caller splits this line into the fields of one printf.
    if (rest <= 0)                 eng = "fleet"
    else if (ramslots < cpuslots)  eng = "memory"
    else if (cpuslots < ramslots)  eng = "cores"
    else                           eng = "balanced"
    printf "%d %d %d %d %s\n", total, active, want - active, fleet - want, eng
  }'
}

measured_of() {  # $1 = the key in e05-constants.tsv -> what this run measured for it
  case "$1" in
    host_runtime_control) m host_runtime_control_mb ;;
    host_runtime_work)    m host_runtime_work_mb ;;
    page_cache_base)      m page_cache_work_mb ;;
    frozen_pod)           m frozen_pod_mb ;;
    zram_factor)          m zram_factor ;;
    active_pod_cores)     m active_pod_cores ;;
    active_pod_ram)       m active_pod_ram_mb ;;
  esac
}

PASS=0; FAIL=0
row() {
  local state="$2" c=31
  case "$state" in PASS) c=32; PASS=$((PASS+1)) ;; *) FAIL=$((FAIL+1)) ;; esac
  printf '  \033[%sm%-4s\033[0m  %-10s %-28s %s\n' "$c" "$state" "$1" "$3" "${4:-}"
}

printf '\n\033[1mAP-1.3 — the calibration run\033[0m\n\n'
printf '  machine   %s cores · %s MB · zram %s MB with %s\n' \
       "$(m machine_cores ?)" "$(m machine_mem_total_mb ?)" \
       "$(m zram_size_mb ?)" "$(m zram_algorithm ?)"
printf '  fleet     %s pods created, %s active, %s frozen, in %s s — %s\n' \
       "$(m pods_created 0)" "$(m pods_active 0)" "$(m pods_frozen 0)" \
       "$(m fleet_create_s ?)" "$(m fleet_detail ?)"
printf '  mix       %s, each at 1/%s of its class request, over a %s s window\n' \
       "$(m mix_shape ?)" "$(m active_scale ?)" "$(m window_s ?)"

printf '\n\033[1m  the five constants\033[0m\n\n'
printf '  %-21s %11s %11s  %s\n' "constant" "given" "measured" "what the measurement covers"
printf '  %-21s %11s %11s  %s\n' "host+runtime control" "12288 MB" \
       "$(m host_runtime_control_mb ?) MB" "no control plane in the image yet — a floor (AP-3.1)"
printf '  %-21s %11s %11s  %s\n' "host+runtime work" "6144 MB" \
       "$(m host_runtime_work_mb ?) MB" "$(m host_runtime_detail ?)"
printf '  %-21s %11s %11s  %s\n' "page cache baseline" "8192 MB" \
       "$(m page_cache_work_mb ?) MB" "no base layers to share yet — a floor (AP-3.3)"
printf '  %-21s %11s %11s  %s\n' "frozen pod" "24 MB" \
       "$(m frozen_pod_mb ?) MB" "$(m frozen_pod_detail ?)"
printf '  %-21s %11s %11s  %s\n' "zram factor" "1.60" \
       "$(m zram_factor ?)" "$(m zram_detail ?)"
printf '  %-21s %11s %11s  %s\n' "active pod" "0.80 cores" \
       "$(m active_pod_cores ?) cores" "recorded, not adopted — R-C measures this (AP-3.7)"
printf '  %-21s %11s %11s  %s\n' "" "960 MB" \
       "$(m active_pod_ram_mb ?) MB" "$(m active_detail ?)"

printf '\n\033[1m  the pressure signal (OP-6)\033[0m\n\n'
printf '  at rest              memory some avg10 peaks at %s %%\n' "$(m psi_noise_pct ?)"
printf '  rise                 crossed 10 %% after %s s · peak %s %% some, %s %% full\n' \
       "$(m psi_cross_s ?)" "$(m psi_peak_pct ?)" "$(m psi_full_peak_pct ?)"
printf '  decay once released  under 10 %% in %s s · under 5 %% in %s s · under 1 %% in %s s\n' \
       "$(m psi_decay_10_s ?)" "$(m psi_decay_5_s ?)" "$(m psi_decay_1_s ?)"
printf '  major faults         %s per second while it was rising · %s\n' \
       "$(m pgmajfault_rate ?)" "$(m psi_samples ?)"

# -----------------------------------------------------------------------------------------------
# R-D against the measurement — the substance of AB-A06-13. Two questions, four rows:
#
#   what the table plans   at its own sliders, with the given constants and with the measured
#                          four. The active pod stays at 960 MB and 0.8 cores in both, because
#                          that is the constant this run does not adopt — so what moves between
#                          the two rows is exactly what was measured here.
#   what the table would   on the machine that just ran the fleet. The measured row uses the
#   have said about this   measured active pod too, because the pods that ran are the ones it
#   run                    measured; the given row is what the table said in advance.
#
# The second question is the check: a table computing with the measured numbers has to be
# consistent with a machine that ran twenty active pods. The given numbers are not, and that is
# the finding rather than an error — 12 GB of host and 8 GB of page cache do not fit in 6 GB, so
# the table planned zero slots for a machine that carried twenty.
# -----------------------------------------------------------------------------------------------
GIVEN=(12 8 24 1.6)            # host_gb cache_gb frost_mb zram — E-05's given values
GIVEN_POD=(960 0.8)            # pod_mb cores_per_pod
# A measurement that failed leaves a zero behind, and a zero in a divisor turns a table into an
# awk error. The floors below keep the row printable; the verdicts further down are what fail.
positive() { awk -v v="$1" -v f="$2" 'BEGIN { print (v + 0 > 0) ? v + 0 : f }'; }
meas_host_gb="$(awk -v v="$(m host_runtime_work_mb 0)" 'BEGIN { printf "%.3f", v / 1024 }')"
meas_cache_gb="$(awk -v v="$(m page_cache_work_mb 0)" 'BEGIN { printf "%.3f", v / 1024 }')"
MEASURED=("$meas_host_gb" "$meas_cache_gb" "$(m frozen_pod_mb 0)" "$(positive "$(m zram_factor 0)" 1)")
MEASURED_POD=("$(positive "$(m active_pod_ram_mb 0)" 960)" "$(positive "$(m active_pod_cores 0)" 0.8)")

printf '\n\033[1m  R-D against the measurement\033[0m\n\n'
printf '  %-32s %7s %7s %7s %7s  %s\n' "" "slots" "active" "queued" "frozen" "bottleneck"
printf '  %-32s %7s %7s %7s %7s  %s\n' "table defaults · given" \
       $(rd 256 96 1 2000 15 "${GIVEN[@]}" "${GIVEN_POD[@]}")
printf '  %-32s %7s %7s %7s %7s  %s\n' "table defaults · measured" \
       $(rd 256 96 1 2000 15 "${MEASURED[@]}" "${GIVEN_POD[@]}")
# The table's defaults are cpu-bound, so the four measured constants change nothing there — which
# is worth seeing rather than hiding. Where they do change something is the end of the sliders the
# panel itself calls the interesting one: a small node carrying a large fleet, where the base load
# of being frozen is what runs out first. 64 GB and 6000 pods are the extremes of R-D's own two
# sliders, not a case invented for this table.
printf '  %-32s %7s %7s %7s %7s  %s\n' "64 GB · 6000 pods · given" \
       $(rd 64 96 1 6000 15 "${GIVEN[@]}" "${GIVEN_POD[@]}")
printf '  %-32s %7s %7s %7s %7s  %s\n' "64 GB · 6000 pods · measured" \
       $(rd 64 96 1 6000 15 "${MEASURED[@]}" "${GIVEN_POD[@]}")

run_ram_gb="$(awk -v v="$(m machine_mem_total_mb 0)" 'BEGIN { printf "%.2f", v / 1024 }')"
run_cores="$(m machine_cores 1)"
run_fleet="$(m pods_created 1)"
run_rush="$(awk -v a="$(m pods_active 0)" -v f="$run_fleet" 'BEGIN { printf "%.2f", 100 * a / f }')"
given_here="$(rd "$run_ram_gb" "$run_cores" 1 "$run_fleet" "$run_rush" "${GIVEN[@]}" "${GIVEN_POD[@]}")"
meas_here="$(rd "$run_ram_gb" "$run_cores" 1 "$run_fleet" "$run_rush" "${MEASURED[@]}" "${MEASURED_POD[@]}")"
printf '  %-32s %7s %7s %7s %7s  %s\n' "this machine · given" $given_here
printf '  %-32s %7s %7s %7s %7s  %s\n' "this machine · measured" $meas_here
printf '  %-32s %7s\n' "this machine · what ran" "$(m pods_active 0)"
printf '\n  "measured" is the four constants this run adopts. The active pod stays at 960 MB and\n'
printf '  0.8 cores in every row but the last pair, where the pods that ran are the ones measured.\n'

slots_given="$(printf '%s' "$given_here" | awk '{print $1}')"
slots_meas="$(printf '%s' "$meas_here" | awk '{print $1}')"

# -----------------------------------------------------------------------------------------------
# The three rows.
# -----------------------------------------------------------------------------------------------
printf '\n\033[1m  the rows\033[0m\n\n'

created="$(m pods_created 0)"; active="$(m pods_active 0)"; frozen="$(m pods_frozen 0)"
if [ "$created" = "$POD_TOTAL" ] && [ "$active" = "$POD_ACTIVE" ] \
   && [ "$frozen" = "$(( POD_TOTAL - POD_ACTIVE ))" ] \
   && [ "${slots_meas:-0}" -ge "$POD_ACTIVE" ]; then
  row AB-A06-13 PASS "calibration run" \
      "$created created, $active active; R-D here: $slots_given slots given, $slots_meas measured"
else
  row AB-A06-13 FAIL "calibration run" \
      "$created of $POD_TOTAL created, $active active, $frozen frozen; R-D measured $slots_meas slots"
fi

# AB-A06-4 had its mechanism checked in AP-1.2 and asks here for the number: "pod frozen, factor
# measured instead of assumed". Both halves are held to, because a factor measured on pages that
# were never frozen would be a fact about zram and not about rung 3 of R-C's ladder.
froze="$(m frozen_held 0)"; factor="$(m zram_factor 0)"; algo="$(m zram_algorithm none)"
if [ "$froze" = 1 ] && [ "$(awk -v f="$factor" 'BEGIN { print (f + 0 > 1) ? 1 : 0 }')" = 1 ] \
   && [ "$algo" = zstd ]; then
  row AB-A06-4 PASS "freezer and zram with zstd" \
      "$frozen pods frozen and alive; factor $factor over $(m zram_orig_mb ?) MB of their pages"
else
  row AB-A06-4 FAIL "freezer and zram with zstd" \
      "frozen=$froze · factor=$factor · algorithm=$algo"
fi

# AB-E05-1: the given numbers are replaced by measured ones. A measurement nobody wrote down
# replaces nothing, so this row is about the ruling as much as about the run — and the run holds
# the ruling against itself, the way registry.py holds the registry against the matrix.
e05_notes=()
if [ ! -r "$CONSTANTS" ]; then
  e05_notes+=("no $CONSTANTS — the ruling has no machine-readable half")
else
  awk -F'\t' '!/^#/ && NF >= 5 && $1 != "key" { print $1, $2, $3, $4, $5 }' "$CONSTANTS" > "$LOG/constants"
  unrecorded=(); drifted=(); unruled=()
  while read -r key given recorded unit adopted; do
    [ -n "$key" ] || continue
    measured="$(measured_of "$key")"
    if [ -z "$measured" ]; then
      e05_notes+=("$key was not measured by this run")
      continue
    fi
    case "$recorded" in
      ""|"—"|"-") unrecorded+=("$key = $measured $unit") ; continue ;;
    esac
    # A factor of two, not a percentage. A constant that moves further than that is a different
    # machine or a broken measurement, and both need a person rather than a threshold. Inside the
    # band the delta is printed and nothing fails — measurements differ, that is what they do.
    if [ "$(awk -v a="$measured" -v b="$recorded" \
              'BEGIN { print (a+0 > 0 && b+0 > 0 && a/b < 2 && b/a < 2) ? 1 : 0 }')" != 1 ]; then
      drifted+=("$key: $recorded $unit recorded ($adopted), $measured measured")
    fi
    # Coarse on purpose: the number has to appear in the ruling as it stands in the table. It
    # catches the two ways these files drift — a number changed in one and not the other.
    grep -Fq -- "$recorded" "$RULING" || unruled+=("$key=$recorded")
  done < "$LOG/constants"
  [ "${#unrecorded[@]}" -eq 0 ] || e05_notes+=("not in the ruling yet: ${unrecorded[*]}")
  [ "${#drifted[@]}" -eq 0 ]    || e05_notes+=("${drifted[*]}")
  [ "${#unruled[@]}" -eq 0 ]    || e05_notes+=("in $(basename "$CONSTANTS") but not in decisions/E-05.md: ${unruled[*]}")
fi
if [ "${#e05_notes[@]}" -eq 0 ]; then
  row AB-E05-1 PASS "five constants" \
      "measured, recorded in decisions/E-05.md, and R-D computes with the four A-06 measures"
else
  row AB-E05-1 FAIL "five constants" "${e05_notes[0]}"
  for n in "${e05_notes[@]:1}"; do printf '              · %s\n' "$n"; done
fi

printf '\n  %d green, %d red\n' "$PASS" "$FAIL"

if [ "$FAIL" -gt 0 ]; then
  cat <<'EOF'

The calibration run did not evidence its rows. If the numbers above are this run's and the ruling
is what is behind, take them over into decisions/E-05.md and acceptance/e05-constants.tsv in one
commit — the next run then holds the ruling against a fresh measurement, which is the only way a
recorded constant stays a measured one.
EOF
  exit 1
fi
if [ "$probe_rc" -ne 0 ] || [ "$baseline_rc" -ne 0 ]; then
  printf '\n  Every row is green, but a machine exited non-zero (probe %s, baseline %s).\n' \
         "$probe_rc" "$baseline_rc"
  exit 1
fi
cat <<'EOF'

E-05's overturn condition is met by this run: the numbers changed, the rule did not. The five stay
planning values, and admission decides on PSI and measured peak RSS (SP-RD-3).
EOF
exit 0
