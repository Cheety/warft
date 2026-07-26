#!/usr/bin/env bash
# calibration-probe.sh — the guest half of the calibration run (AP-1.3).
#
#   acceptance/calibration-probe.sh probe    [total] [active] [scale] [window]
#   acceptance/calibration-probe.sh baseline
#
# Runs inside the machine, carried in by image/vm.sh as a systemd credential. acceptance/
# calibration.sh is the half that stays on the host: it boots this, reads the markers it writes to
# the console and composes the table. Read that file first — the reasoning behind every number here
# is in its header.
#
# Two halves rather than one file, because of a limit that is easy to hit and hard to see: the
# credential travels as an SMBIOS OEM string, and that table is capped at 64 KB. A single file
# carrying both halves came to 64,948 bytes in base64, the table was dropped whole, and the machine
# booted to a login prompt with no credentials, no role and no check — for fifteen minutes, until the
# timeout. vm.sh now refuses a payload that cannot arrive; this file is the other half of that fix.
#
# What it reports, in order:
#
#   1  host and runtime at rest, and the page cache baseline          — two of the five constants
#   2  the fleet: 500 pods in three slices, 20 of them active
#   3  the freezer, on the fleet — rung 3 of R-C's ladder at scale
#   4  the zram factor, on the pages of frozen pods                   — the third constant
#   5  the active mix over one window                                 — the fourth, recorded only
#   6  a deliberate pressure event, and the decay after it            — OP-6
#
# Marker protocol: `WORKPOD-CAL: <key> <value>` to /dev/console. The console and not the journal,
# because the journal prefixes and rate limits what it forwards (run 21, run 22).
#
# Exit:  0 = the run completed; the host decides the rows

set -uo pipefail

MODE="${1:-probe}"

# The fleet from A-06's last row, and the window, both given by the host so that they stand in one
# place. The class mix underneath them is not a parameter: it is E-05's 20/30/35/15 and R-A's table.
POD_TOTAL="${2:-500}"
POD_ACTIVE="${3:-20}"
ACTIVE_SCALE="${4:-8}"
WINDOW_S="${5:-60}"

CLASSES=(tiny small medium large)
CLASS_SHARE=(0.20 0.30 0.35 0.15)      # E-05's mix
CLASS_RAM_MB=(128 384 1024 3072)       # requested RAM, SP-RA-1
CLASS_LIMIT_MB=(512 1536 3072 8192)    # tolerated RAM — memory.high, because a pod is throttled
                                       # and not shot (SP-RA-2)
CLASS_WEIGHT=(10 30 100 200)           # cpu.weight, proportional to the requested cores (SP-RA-3)

# How many pods of each class. The mix decides it rather than a second list of numbers that could
# disagree with the first: 20/30/35/15 of twenty is 4, 6, 7 and the rest.
CLASS_COUNT=()
assigned=0
for c in 0 1 2; do
  n="$(awk -v s="${CLASS_SHARE[$c]}" -v a="$POD_ACTIVE" 'BEGIN { printf "%d", s * a + 0.5 }')"
  CLASS_COUNT+=("$n")
  assigned=$(( assigned + n ))
done
CLASS_COUNT+=("$(( POD_ACTIVE - assigned ))")

# What half the frozen pods hold while they are frozen. The other half holds nothing but itself, so
# the mechanism's own cost is measured rather than derived, and this half gives the compression factor
# pages with content in them. zram compresses page by page, so a seed repeated across pages does not
# flatter the factor.
FROZEN_STATE_KB=1024

SAMPLE_MS=500        # PSI sampling interval. SP-RC-1 reads pressure every two seconds; measuring
                     # the dynamics of that signal needs to sample faster than it is read.

WORK=/run/cal
STATE="$WORK/state"
mkdir -p "$WORK" "$WORK/rounds"

mark() {  # the verdict channel: the console, because the journal prefixes and rate limits it
          # (run 21, run 22) and the host anchors on these lines.
  local m="WORKPOD-CAL: $1 $2"
  echo "$m" > /dev/console 2>/dev/null || echo "$m"
}
say() { printf '  %s\n' "$*"; }

kb() { awk -v k="$1:" '$1 == k { print $2; exit }' /proc/meminfo; }
mb() { awk -v v="$1" 'BEGIN { printf "%.0f", v / 1024 }'; }
now_ms() { date +%s%3N; }

PODS_CG=/sys/fs/cgroup/workpod-pods.slice
BARE_CG="$PODS_CG/workpod-pods-bare.slice"
STATE_CG="$PODS_CG/workpod-pods-state.slice"
ACTIVE_CG="$PODS_CG/workpod-pods-active.slice"

CREDENTIALS="${CREDENTIALS_DIRECTORY:-/run/credentials/@system}"
ROLE="$(tr -d '[:space:]' < "$CREDENTIALS/workpod.role" 2>/dev/null)"
[ -n "$ROLE" ] || ROLE=none

printf '\n\033[1mAP-1.3 — in the machine (role %s, mode %s)\033[0m\n\n' "$ROLE" "$MODE"

# A normally booted node, for the reasons a06-acceptance.sh needs one: zram is a swap unit and the
# slices are systemd's. The login prompts are masked at runtime only — a getty would take the console
# this run reports over, and the machine has no other channel back (SP-A04-4).
systemctl mask --runtime getty.target serial-getty@ttyS0.service >/dev/null 2>&1
systemctl start --no-block multi-user.target
for _ in $(seq 1 90); do
  [ "$(systemctl is-active multi-user.target 2>/dev/null)" = active ] && break
  sleep 1
done
BOOT="$(systemctl is-active multi-user.target 2>&1)"
say "boot: multi-user.target $BOOT · $(uname -r)"
[ "$BOOT" = active ] || systemctl list-jobs 2>&1 | sed 's/^/    /'

# The machine, so every number below has one. A constant measured on a machine nobody named is a
# number without a unit.
mark machine_cores "$(nproc)"
mark machine_mem_total_mb "$(mb "$(kb MemTotal)")"
mark role "$ROLE"

# -------------------------------------------------------------------------------------------------
# 1  Host and runtime, and the page cache baseline — both at rest, before a pod exists.
#
#    "Host and runtime" is what the node holds when nobody has asked it for anything:
#    MemTotal - MemAvailable. The breakdown stands beside it because a single number cannot be
#    checked, and because the parts are what will grow — the control plane in AP-3.1 and containerd's
#    per-pod runtime with it.
#
#    The page cache baseline is file-backed cache only, so Shmem is subtracted: tmpfs pages are a
#    pod's state, not a shared base layer, and the constant is about "the number of simultaneously
#    used base layers" — of which there are none until container images arrive in AP-3.3.
# -------------------------------------------------------------------------------------------------
slice_mb() {  # $1 = cgroup path under /sys/fs/cgroup
  local f="/sys/fs/cgroup$1/memory.current"
  if [ -r "$f" ]; then mb "$(( $(cat "$f") / 1024 ))"; else printf '?'; fi
}

at_rest() {
  local total avail cached shmem slab tables stack held cache
  total="$(kb MemTotal)"; avail="$(kb MemAvailable)"
  cached="$(kb Cached)";  shmem="$(kb Shmem)"
  slab="$(kb Slab)"; tables="$(kb PageTables)"; stack="$(kb KernelStack)"
  held="$(mb "$(( total - avail ))")"; cache="$(mb "$(( cached - shmem ))")"
  # Twice, once under the role's own name: the constant is per role, and the host reads the two
  # boots out of one concatenated log.
  mark host_runtime_mb "$held"
  mark "host_runtime_${ROLE}_mb" "$held"
  mark page_cache_mb "$cache"
  mark "page_cache_${ROLE}_mb" "$cache"
  mark host_runtime_detail \
    "slab $(mb "$slab") MB · page tables $(mb "$tables") MB · kernel stacks $(mb "$stack") MB · system.slice $(slice_mb /system.slice) MB"
  say "at rest as $ROLE: $held MB held, $cache MB page cache, $(mb "$shmem") MB shmem"
}

# zram, as the image configured it. AB-A06-4 checked the algorithm in AP-1.2; here it is the device
# the factor is measured on, so its size and algorithm travel with the number.
zram_algo="$(sed -n 's/.*\[\(.*\)\].*/\1/p' /sys/block/zram0/comp_algorithm 2>/dev/null)"
mark zram_algorithm "${zram_algo:-none}"
mark zram_size_mb "$(( $(cat /sys/block/zram0/disksize 2>/dev/null || echo 0) / 1048576 ))"

sleep 5   # the boot's own transients: udev settling, chronyd starting, the journal flushing
at_rest

if [ "$MODE" = baseline ]; then
  say "baseline only — the fleet runs in the work boot"
  exit 0
fi

# -------------------------------------------------------------------------------------------------
# 2  The fleet: 500 pods, 20 of them active.
#
#    A pod here is a transient unit in a slice — the mechanism SP-A02-4 gives R-A and R-C, and the
#    same slice a06-acceptance.sh reads pressure from. Three slices under it, because the fleet has
#    three parts that are measured apart from each other:
#
#      workpod-pods-active.slice  the twenty that work, each with its class's cpu.weight and its
#                                 class's tolerated limit as memory.high
#      workpod-pods-bare.slice    240 frozen pods holding nothing but themselves, so the marginal
#                                 cost of the mechanism has no size chosen by this script in it
#      workpod-pods-state.slice   240 frozen pods holding a stated megabyte of the image's own
#                                 configuration text, so the factor has pages with content to work on
# -------------------------------------------------------------------------------------------------
# The state lies on a tmpfs of its own rather than on /run, whose default limit is half of RAM and
# would cap the mix silently. tmpfs pages are charged to the cgroup of the task that writes them,
# which is what makes a pod's state its own.
mkdir -p "$STATE"
mount -t tmpfs -o size=4G,mode=0700 cal-state "$STATE" \
  || say "the state tmpfs did not mount — pods write into /run instead"

# The seed: the image's own configuration text. Not random bytes — a page of noise compresses at 1.0
# and would make the factor a fact about /dev/urandom. zram compresses each page on its own, so the
# seed repeating across pages does not flatter the result either.
{ for _ in $(seq 1 80); do
    cat /usr/lib/systemd/system/*.service /usr/lib/systemd/system/*.target /usr/lib/os-release 2>/dev/null
  done; } | head -c 1048576 > "$WORK/seed"
say "seed: $(( $(stat -c %s "$WORK/seed") / 1024 )) KB of the image's own unit files"

# The pod. One file, two shapes: a frozen pod fills its state once and waits to be frozen; an active
# pod waits for the window to open and then runs rounds of R-B's phases until it is told to stop.
cat > "$WORK/pod.sh" <<'POD'
#!/bin/bash
# A pod, as far as stage 1 has one: a shell in a cgroup of its own.
#   $1 name · $2 state KB · $3 active|idle · $4 MB the active load touches per round
set -u
name="$1"; state_kb="$2"; kind="$3"; set_mb="${4:-0}"
seed=/run/cal/seed
store="/run/cal/state/$name"

fill() {  # $1 = file, $2 = KB. The seed is a megabyte, so anything larger is the seed again.
  local left="$2" chunk
  : > "$1"
  while [ "$left" -gt 0 ]; do
    chunk=$(( left > 1024 ? 1024 : left ))
    head -c $(( chunk * 1024 )) "$seed" >> "$1"
    left=$(( left - chunk ))
  done
}

[ "$state_kb" -gt 0 ] && fill "$store" "$state_kb"

if [ "$kind" != active ]; then
  # Frozen pods do nothing at all. The freezer is what happens to them and their pages are what is
  # measured, so anything they did here would be measured too.
  while :; do sleep 3600; done
fi

# The window is opened by the run, not by the pod: the fleet is created first and the mix is measured
# on top of it, so an active pod that started working while the other 480 were still being created
# would be measured against a machine that does not exist any more.
while [ ! -e /run/cal/go ]; do sleep 0.2; done

# R-B's phases, in the order a job passes through them (SP-RB-1). The `net` phase is a pod waiting
# for a model response: it holds no cpu token, so it is a sleep and not a spin.
round=0
while [ ! -e /run/cal/stop ]; do
  round=$(( round + 1 ))
  fill "$store" $(( set_mb * 1024 ))     # io, then cpu·ram: its working set, written and hot
  sha256sum "$store" > /dev/null          # cpu·ram: every page of it read back
  echo "$round" > "/run/cal/rounds/$name"
  sleep 1                                 # net: waiting, holding nothing
done
POD
chmod +x "$WORK/pod.sh"
rm -f "$WORK/go" "$WORK/stop" "$WORK/stop-psi" "$WORK/stop-pressure"

pods_kb() {  # anon + shmem of a slice — the pages that go to zram when the slice is reclaimed
  local f="$1/memory.stat"
  if [ -r "$f" ]; then
    awk '$1 == "anon" || $1 == "shmem" { s += $2 } END { printf "%d", s / 1024 }' "$f"
  else
    printf '0'
  fi
}
count_pods() { ls -d "$1"/cal-pod-*.service 2>/dev/null | wc -l; }

t_fleet=$(now_ms)

# The twenty active pods, in the mix's shape.
mix_shape=""; idx=1
declare -A POD_CLASS
for c in 0 1 2 3; do
  n="${CLASS_COUNT[$c]}"
  set_mb=$(( CLASS_RAM_MB[c] / ACTIVE_SCALE ))
  mix_shape="$mix_shape${mix_shape:+ · }${CLASSES[$c]}×$n at ${set_mb} MB"
  for _ in $(seq 1 "$n"); do
    systemd-run --quiet --unit="cal-pod-$idx" --slice=workpod-pods-active.slice \
      --property=MemoryAccounting=yes --property=CPUAccounting=yes --property=TasksMax=64 \
      --property="CPUWeight=${CLASS_WEIGHT[$c]}" --property="MemoryHigh=${CLASS_LIMIT_MB[$c]}M" \
      "$WORK/pod.sh" "pod-$idx" 0 active "$set_mb" 2>/dev/null \
      || say "active pod $idx did not start"
    POD_CLASS[$idx]="${CLASSES[$c]}"
    idx=$(( idx + 1 ))
  done
done
mark mix_shape "$mix_shape"
mark active_scale "$ACTIVE_SCALE"
mark window_s "$WINDOW_S"

# Then the frozen fleet, in its two halves. Each half's marginal cost is measured over its own pods,
# so neither number is the other one minus a constant.
create_frozen() {  # $1 = slice, $2 = first index, $3 = last index, $4 = state KB
  local i
  for i in $(seq "$2" "$3"); do
    systemd-run --quiet --unit="cal-pod-$i" --slice="$1" \
      --property=MemoryAccounting=yes --property=CPUAccounting=yes --property=TasksMax=64 \
      "$WORK/pod.sh" "pod-$i" "$4" idle 0 2>/dev/null || { say "pod $i did not start"; return 1; }
  done
}

bare_from=$(( POD_ACTIVE + 1 ))
bare_to=$(( POD_ACTIVE + (POD_TOTAL - POD_ACTIVE) / 2 ))
bare_before="$(pods_kb "$BARE_CG")"
create_frozen workpod-pods-bare.slice "$bare_from" "$bare_to" 0
sleep 3
bare_after="$(pods_kb "$BARE_CG")"
bare_n=$(( bare_to - bare_from + 1 ))

state_before="$(pods_kb "$STATE_CG")"
create_frozen workpod-pods-state.slice $(( bare_to + 1 )) "$POD_TOTAL" "$FROZEN_STATE_KB"
sleep 3
state_after="$(pods_kb "$STATE_CG")"
state_n=$(( POD_TOTAL - bare_to ))

fleet_s=$(( ($(now_ms) - t_fleet) / 1000 ))
n_bare="$(count_pods "$BARE_CG")"; n_state="$(count_pods "$STATE_CG")"
n_active="$(count_pods "$ACTIVE_CG")"
tasks="$(cat "$PODS_CG/pids.current" 2>/dev/null || echo 0)"

mark pods_created "$(( n_bare + n_state + n_active ))"
mark pods_active "$n_active"
mark pods_frozen "$(( n_bare + n_state ))"
mark fleet_create_s "$fleet_s"
mark fleet_detail "$n_active active · $n_bare bare · $n_state holding state · $tasks tasks in the slice"

# The two marginal costs. A pod is one cgroup and one shell; the second half carries a megabyte of
# text on top of that. Both are reported, because R-D's frozen-pod value is the first plus whatever
# a pod actually holds — and what it will hold is the harness, which is AP-3.1's to build.
frozen_bare_kb="$(awk -v a="$bare_after" -v b="$bare_before" -v n="$bare_n" \
                    'BEGIN { printf "%.0f", n > 0 ? (a - b) / n : 0 }')"
frozen_state_kb="$(awk -v a="$state_after" -v b="$state_before" -v n="$state_n" \
                     'BEGIN { printf "%.0f", n > 0 ? (a - b) / n : 0 }')"
mark frozen_pod_mb "$(awk -v v="$frozen_bare_kb" 'BEGIN { printf "%.2f", v / 1024 }')"
mark frozen_pod_state_mb "$(awk -v v="$frozen_state_kb" 'BEGIN { printf "%.2f", v / 1024 }')"
mark frozen_pod_detail \
  "$frozen_bare_kb KB per bare pod over $bare_n of them · $frozen_state_kb KB when it holds ${FROZEN_STATE_KB} KB of text, over $state_n"
say "frozen pod: $frozen_bare_kb KB bare, $frozen_state_kb KB with ${FROZEN_STATE_KB} KB of state"

# -------------------------------------------------------------------------------------------------
# 3  The freezer, on the fleet — rung 3 of R-C's ladder, at fleet scale.
#
#    One write per slice and not one per pod: the freezer is hierarchical, and that is the property
#    which makes "freeze the fleet" a single decision instead of 480 of them. The kernel reports
#    `frozen 1` a moment after the write returns, so it is polled rather than assumed.
# -------------------------------------------------------------------------------------------------
freeze_slice() {  # $1 = unit, $2 = its cgroup
  # `systemctl freeze` first, because that is how the platform will ask for it — and the kernel
  # interface underneath as the fallback, because not every systemd version will freeze a slice on
  # request. What is checked either way is `cgroup.events`, which is the kernel answering.
  if ! systemctl freeze "$1" >/dev/null 2>&1; then
    say "systemctl freeze $1 was refused — writing cgroup.freeze directly"
    echo 1 > "$2/cgroup.freeze" 2>/dev/null
  fi
  local i
  for i in $(seq 1 30); do
    grep -q '^frozen 1' "$2/cgroup.events" 2>/dev/null && return 0
    sleep 1
  done
  return 1
}
froze=1
freeze_slice workpod-pods-bare.slice "$BARE_CG"   || froze=0
freeze_slice workpod-pods-state.slice "$STATE_CG" || froze=0
alive=$(( $(cat "$BARE_CG/pids.current" 2>/dev/null || echo 0)
        + $(cat "$STATE_CG/pids.current" 2>/dev/null || echo 0) ))
[ "$alive" -gt 0 ] || froze=0
mark frozen_held "$froze"
mark frozen_detail "$alive tasks frozen and still alive across $(( n_bare + n_state )) pods"
say "freezer: held=$froze, $alive tasks frozen and alive"

# -------------------------------------------------------------------------------------------------
# 4  The zram factor, on the pages of frozen pods.
#
#    R-D divides the frozen pod's pages by this number and nothing else — frozen pods lie
#    compressed, active pods lie hot. So the factor is measured where it is used: the two frozen
#    slices are reclaimed and what zram received is read off the device.
#
#    `memory.reclaim` is the ask, in as many words: reclaim this many bytes from this cgroup. The
#    anonymous and tmpfs pages of a frozen process are reclaimable — freezing stops its tasks, it
#    does not pin their memory — and with zram as the only swap they can go nowhere else.
#
#    Two numbers come out and both are reported. The ratio zram itself achieved (original over
#    compressed) is a property of the pages. The factor R-D wants is pages per byte of memory the
#    machine actually holds, which is the original size over what the allocator holds — same-filled
#    pages cost nothing at all, and a planning value that ignored that would plan for memory nobody
#    uses.
# -------------------------------------------------------------------------------------------------
zram_field() {  # $1 = field of mm_stat, 1-based; falls back to the per-value files
  local v
  v="$(awk -v i="$1" '{ print $i; exit }' /sys/block/zram0/mm_stat 2>/dev/null)"
  if [ -z "$v" ]; then
    case "$1" in
      1) v="$(cat /sys/block/zram0/orig_data_size 2>/dev/null)" ;;
      2) v="$(cat /sys/block/zram0/compr_data_size 2>/dev/null)" ;;
      3) v="$(cat /sys/block/zram0/mem_used_total 2>/dev/null)" ;;
    esac
  fi
  printf '%s' "${v:-0}"
}
orig0="$(zram_field 1)"; compr0="$(zram_field 2)"; used0="$(zram_field 3)"; same0="$(zram_field 6)"

reclaim() {  # $1 = cgroup, $2 = KB to ask for
  if [ -w "$1/memory.reclaim" ] && echo $(( $2 * 1024 )) > "$1/memory.reclaim" 2>/dev/null; then
    return 0
  fi
  # Without memory.reclaim: squeeze with memory.high, let the kernel do the same work, then let go
  # again. Throttled, not shot — SP-RA-2's rule holds for this script as well.
  local high; high="$(cat "$1/memory.high" 2>/dev/null)"
  echo $(( 8 * 1024 * 1024 )) > "$1/memory.high" 2>/dev/null || return 1
  sleep 5
  echo "${high:-max}" > "$1/memory.high" 2>/dev/null
}
reclaim "$BARE_CG"  "$(pods_kb "$BARE_CG")"
reclaim "$STATE_CG" "$(pods_kb "$STATE_CG")"
sleep 5

orig1="$(zram_field 1)"; compr1="$(zram_field 2)"; used1="$(zram_field 3)"; same1="$(zram_field 6)"
read -r zram_factor zram_ratio zram_orig_mb zram_used_mb zram_same <<EOF
$(awk -v o0="$orig0" -v o1="$orig1" -v c0="$compr0" -v c1="$compr1" \
     -v u0="$used0" -v u1="$used1" -v s0="$same0" -v s1="$same1" 'BEGIN {
  do_ = o1 - o0; dc = c1 - c0; du = u1 - u0; ds = s1 - s0
  printf "%.2f %.2f %.1f %.1f %d",
         (du > 0 ? do_ / du : 0), (dc > 0 ? do_ / dc : 0), do_ / 1048576, du / 1048576, ds
}')
EOF
mark zram_factor "$zram_factor"
mark zram_orig_mb "$zram_orig_mb"
mark zram_detail \
  "$zram_orig_mb MB of frozen pages into $zram_used_mb MB of memory · pure ratio $zram_ratio · $zram_same same-filled pages"
say "zram: factor $zram_factor (ratio $zram_ratio), $zram_orig_mb MB → $zram_used_mb MB"

# -------------------------------------------------------------------------------------------------
# 5  The active mix: twenty pods, the class mix, one window.
#
#    Measured per pod: cpu time over the window, and the peak its cgroup reached. What cannot be
#    measured here is what an active pod costs, because there is no job — E-05 sends that number to
#    R-C's three runs per repository and this run does not pretend otherwise. What the window does
#    establish is R-A's weights at work: the classes get cpu in the ratio of their requests, on a
#    machine with less cpu than the mix asks for.
# -------------------------------------------------------------------------------------------------
cpu_usec() { awk '$1 == "usage_usec" { u = $2 } END { printf "%d", u + 0 }' "$1/cpu.stat" 2>/dev/null; }

# The PSI sampler, beside the load and beside the pressure event after it. One line per sample, so
# the dynamics can be read afterwards instead of being asserted during.
psi_sample() {
  local interval; interval="$(awk -v ms="$SAMPLE_MS" 'BEGIN { printf "%.3f", ms / 1000 }')"
  while [ ! -e "$WORK/stop-psi" ]; do
    printf '%s %s %s %s\n' "$(now_ms)" \
      "$(awk '/^some/ { for (i = 2; i <= NF; i++) if ($i ~ /^avg10=/) { sub(/avg10=/, "", $i); print $i } }' "$PODS_CG/memory.pressure" 2>/dev/null)" \
      "$(awk '/^full/ { for (i = 2; i <= NF; i++) if ($i ~ /^avg10=/) { sub(/avg10=/, "", $i); print $i } }' "$PODS_CG/memory.pressure" 2>/dev/null)" \
      "$(awk '$1 == "pgmajfault" { print $2 }' /proc/vmstat)"
    sleep "$interval"
  done
}
psi_sample > "$WORK/psi.log" &
PSI_PID=$!

# The noise floor first: what the signal reads while nothing is happening. A hysteresis band that
# does not clear this would flap on an idle machine, which is the failure OP-6 is about.
#
# Twenty seconds of it, and only the last six are read. The reclaim above stalled the fleet on
# purpose, and `avg10` needs two of its own windows to forget that — a noise floor measured in the
# tail of a real event would be the event, not the floor.
sleep 20
mark psi_noise_pct "$(tail -12 "$WORK/psi.log" \
  | awk 'NF >= 4 { if ($2 + 0 > m) m = $2 + 0 } END { printf "%.2f", m }')"

declare -A CPU_BEFORE
for i in $(seq 1 "$POD_ACTIVE"); do
  CPU_BEFORE[$i]="$(cpu_usec "$ACTIVE_CG/cal-pod-$i.service")"
done

t_window=$(now_ms)
: > "$WORK/go"          # the window opens; the twenty pods start their rounds
sleep "$WINDOW_S"
window_ms=$(( $(now_ms) - t_window ))

declare -A CORES_SUM RAM_SUM CLASS_N
for cl in "${CLASSES[@]}"; do CORES_SUM[$cl]=0; RAM_SUM[$cl]=0; CLASS_N[$cl]=0; done
for i in $(seq 1 "$POD_ACTIVE"); do
  cg="$ACTIVE_CG/cal-pod-$i.service"
  after="$(cpu_usec "$cg")"
  peak="$(cat "$cg/memory.peak" 2>/dev/null || cat "$cg/memory.current" 2>/dev/null || echo 0)"
  cl="${POD_CLASS[$i]}"
  CORES_SUM[$cl]="$(awk -v s="${CORES_SUM[$cl]}" -v a="$after" -v b="${CPU_BEFORE[$i]}" -v w="$window_ms" \
                      'BEGIN { printf "%.4f", s + (a - b) / 1000 / w }')"
  RAM_SUM[$cl]="$(awk -v s="${RAM_SUM[$cl]}" -v v="$peak" 'BEGIN { printf "%.1f", s + v / 1048576 }')"
  CLASS_N[$cl]=$(( ${CLASS_N[$cl]} + 1 ))
done

# The mix, weighted the way E-05 weights it: the share of each class times what that class measured.
# 0.2·tiny + 0.3·small + 0.35·medium + 0.15·large, over the per-pod means.
active_cores=0; active_ram=0; active_detail=""
for c in 0 1 2 3; do
  cl="${CLASSES[$c]}"
  [ "${CLASS_N[$cl]}" -gt 0 ] || continue
  per_cores="$(awk -v s="${CORES_SUM[$cl]}" -v n="${CLASS_N[$cl]}" 'BEGIN { printf "%.3f", s / n }')"
  per_ram="$(awk -v s="${RAM_SUM[$cl]}" -v n="${CLASS_N[$cl]}" 'BEGIN { printf "%.1f", s / n }')"
  active_cores="$(awk -v a="$active_cores" -v w="${CLASS_SHARE[$c]}" -v v="$per_cores" \
                    'BEGIN { printf "%.3f", a + w * v }')"
  active_ram="$(awk -v a="$active_ram" -v w="${CLASS_SHARE[$c]}" -v v="$per_ram" \
                  'BEGIN { printf "%.1f", a + w * v }')"
  active_detail="$active_detail${active_detail:+ · }$cl $per_cores cores, $per_ram MB"
  mark "active_${cl}_cores" "$per_cores"
  mark "active_${cl}_ram_mb" "$per_ram"
done
mark active_pod_cores "$active_cores"
mark active_pod_ram_mb "$active_ram"
mark active_detail "$active_detail"
mark active_rounds "$(cat "$WORK/rounds"/* 2>/dev/null | awk '{ s += $1 } END { print s + 0 }') rounds in $(( window_ms / 1000 )) s"
say "active mix: $active_cores cores and $active_ram MB per pod, weighted over the mix"
say "  $active_detail"

# The mix is over. Its working sets go with it: the pages the pressure event needs to be reclaimed
# are its own, and 2 GB of finished state lying in tmpfs would decide the event before it starts.
: > "$WORK/stop"
sleep 5
for i in $(seq 1 "$POD_ACTIVE"); do rm -f "$STATE/pod-$i"; done

# -------------------------------------------------------------------------------------------------
# 6  The pressure signal: what OP-6 is about.
#
#    §19 leaves the hysteresis and hold times of the PSI thresholds open and proposes deriving them
#    from this run. A threshold needs three numbers before it can be acted on: what the signal reads
#    when nothing is wrong, how fast it crosses when something is, and how long it takes to come back
#    once the cause is gone. The third decides the hold time, because `avg10` is an exponential
#    average over ten seconds and cannot fall faster than its own window — acting on its way down is
#    what flaps.
#
#    The cause here is one pod under a low memory.high: it allocates, the kernel reclaims, the
#    reclaim stalls it, and the stall is what PSI counts. It is a stated cause and not part of the
#    mix, and it is stopped the moment the threshold is crossed.
# -------------------------------------------------------------------------------------------------
cat > "$WORK/pressure.sh" <<'PRESSURE'
#!/bin/bash
# Deliberate memory pressure: hold more tmpfs than memory.high allows, so every further page has to
# be reclaimed before it can be written. Bounded at half a gigabyte — pressure, not an OOM.
set -u
while [ ! -e /run/cal/stop-pressure ]; do
  for i in $(seq 1 16); do
    [ -e /run/cal/stop-pressure ] && break
    : > "/run/cal/state/pressure-$i"
    for _ in $(seq 1 32); do cat /run/cal/seed >> "/run/cal/state/pressure-$i"; done
  done
  rm -f /run/cal/state/pressure-*
done
PRESSURE
chmod +x "$WORK/pressure.sh"

psi_now() {
  awk '/^some/ { for (i = 2; i <= NF; i++) if ($i ~ /^avg10=/) { sub(/avg10=/, "", $i); print $i + 0 } }' \
      "$PODS_CG/memory.pressure" 2>/dev/null
}
over() { awk -v p="${1:-0}" -v t="$2" 'BEGIN { print (p + 0 > t) ? 1 : 0 }'; }
faults() { awk '$1 == "pgmajfault" { print $2 }' /proc/vmstat; }

t_press=$(now_ms)
faults0="$(faults)"
systemd-run --quiet --unit=cal-pressure --slice=workpod-pods-active.slice \
  --property=MemoryAccounting=yes --property=MemoryHigh=128M --property=TasksMax=16 \
  "$WORK/pressure.sh" 2>/dev/null || say "the pressure pod did not start"

cross_ms=0
for _ in $(seq 1 120); do
  [ "$(over "$(psi_now)" 10)" = 1 ] && { cross_ms=$(( $(now_ms) - t_press )); break; }
  sleep 0.5
done
at_cross="$(psi_now)"
faults1="$(faults)"
: > "$WORK/stop-pressure"
systemctl stop cal-pressure.service >/dev/null 2>&1
rm -f "$STATE"/pressure-*

# The decay, from the moment the cause is gone. Timed here rather than read out of the log, so the
# three crossings are measured against one clock.
t_release=$(now_ms)
d10=0; d5=0; d1=0
for _ in $(seq 1 240); do
  p="$(psi_now)"; el=$(( $(now_ms) - t_release ))
  [ "$d10" = 0 ] && [ "$(over "$p" 10)" = 0 ] && d10=$el
  [ "$d5"  = 0 ] && [ "$(over "$p" 5)"  = 0 ] && d5=$el
  [ "$d1"  = 0 ] && [ "$(over "$p" 1)"  = 0 ] && d1=$el
  [ "$d1" != 0 ] && break
  sleep 0.5
done

: > "$WORK/stop-psi"
wait "$PSI_PID" 2>/dev/null

mark psi_cross_s "$(awk -v v="$cross_ms" 'BEGIN { printf "%.1f", v / 1000 }')"
mark psi_at_cross_pct "${at_cross:-0}"
mark psi_peak_pct "$(awk 'NF >= 4 { if ($2 + 0 > m) m = $2 + 0 } END { printf "%.1f", m }' "$WORK/psi.log")"
mark psi_full_peak_pct "$(awk 'NF >= 4 { if ($3 + 0 > m) m = $3 + 0 } END { printf "%.1f", m }' "$WORK/psi.log")"
mark psi_decay_10_s "$(awk -v v="$d10" 'BEGIN { printf "%.1f", v / 1000 }')"
mark psi_decay_5_s "$(awk -v v="$d5" 'BEGIN { printf "%.1f", v / 1000 }')"
mark psi_decay_1_s "$(awk -v v="$d1" 'BEGIN { printf "%.1f", v / 1000 }')"
mark pgmajfault_rate "$(awk -v a="$faults1" -v b="$faults0" -v ms="$(( $(now_ms) - t_press ))" \
                          'BEGIN { printf "%.0f", (a - b) * 1000 / (ms > 0 ? ms : 1) }')"
mark psi_samples "$(wc -l < "$WORK/psi.log") samples at ${SAMPLE_MS} ms"
say "pressure: crossed 10 % after ${cross_ms} ms, back under 1 % ${d1} ms after release"

# -------------------------------------------------------------------------------------------------
# Down again. The fleet is thawed rather than stopped: a frozen cgroup cannot process a signal, and
# the poweroff that follows this script is what takes the units down. Thawing first is what keeps
# that from waiting for 480 stop jobs it cannot deliver.
# -------------------------------------------------------------------------------------------------
for cg in "$BARE_CG" "$STATE_CG"; do
  systemctl thaw "$(basename "$cg")" >/dev/null 2>&1 || echo 0 > "$cg/cgroup.freeze" 2>/dev/null
done

printf '\n  the run is complete; the table is composed on the host\n'
exit 0
