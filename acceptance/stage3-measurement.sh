#!/usr/bin/env bash
# stage3-measurement.sh — how stage 3 ends: three numbers, on one node (E-11, 02-work-packages.md).
#
#   acceptance/stage3-measurement.sh         the two rows, then one boot that measures the third
#   acceptance/stage3-measurement.sh host    only the two rows — no machine, for a working tree
#   acceptance/stage3-measurement.sh probe N in the machine: N jobs through the pipeline, timed
#   S3_REPLAY=<file> acceptance/stage3-measurement.sh    compose the table from a saved console log
#
# E-11's rule is that every step ends with a measurement and not with an opinion, and stage 3's is
# stated in 02-work-packages.md as three numbers:
#
#   jobs per hour on one node                    this run
#   orphaned subvolumes after a restart, zero    AB-T04-5, measured in acceptance/t04-runner.sh
#   double execution without double effect       AB-A06-11, measured in acceptance/k03-outbox.sh
#
# Two of the three are acceptance rows and are therefore already answered by a run; this script
# reads their state out of acceptance/registry.tsv rather than measuring them again, because a
# second measurement of a green row would be a second instrument for one question. The third is not
# a row and is not going to become one: the acceptance matrix decides whether the platform was hit,
# and a throughput figure decides nothing — it describes. decisions/stage-3-measurement.md is the
# ruling that says so, and acceptance/stage3-measurement.tsv is where the number lands.
#
# What is counted here is a *job*: one order through the whole of T-05's spine — prepare, plan,
# edit, check, repair, deliver, reap — in a pod on a node, with a blocking check that has to pass
# before the delivery may claim its evidence class. Not a pod start (that is AB-T03-1's ~200 ms)
# and not a phase. The jobs are stated by hand, which is what stage 3 is: no captain sizes them and
# no model writes the edit (E-11 step 3, decisions/jobs-by-hand.md). So the number is a **floor**
# with its conditions attached, and the conditions travel with it into the table.
#
# Exit:  0 = the three numbers stand
#        1 = one of them does not
#        2 = there is nothing to run against (no image, no probe)

set -uo pipefail

MODE="${1:-drive}"
HERE="$(cd "$(dirname "$0")" && pwd)"

# Twenty jobs, for the same reason acceptance/t04-runner.sh starts twenty pods: enough that one slow
# first run does not decide the mean, few enough that the boot stays inside a build runner's patience.
JOBS="${2:-20}"

# =================================================================================================
# Guest side — N jobs through the pipeline, timed, on one node.
# =================================================================================================
if [ "$MODE" = "probe" ]; then
  WORK=/run/s3
  REGISTERED=/run/workpod/registered
  mark() { printf 'WORKPOD-S3: %s %s\n' "$1" "$2"; }

  systemctl mask --runtime getty.target serial-getty@ttyS0.service >/dev/null 2>&1
  systemctl start --no-block multi-user.target
  ready=""
  for _ in $(seq 1 420); do [ -e "$REGISTERED" ] && { ready=1; break; }; sleep 1; done
  if [ -z "$ready" ]; then
    mark fatal "no $REGISTERED after 420 s"
    exit 1
  fi

  mark machine_cores "$(nproc)"
  mark machine_mem_total_mb "$(awk '/MemTotal/ {printf "%d", $2/1024}' /proc/meminfo)"

  mkdir -p "$WORK"
  SKEL="$WORK/skeleton"
  mkdir -p "$SKEL"/{usr,proc,sys,dev,tmp,run,work,harness,etc,var}
  ln -sf usr/bin "$SKEL/bin"; ln -sf usr/sbin "$SKEL/sbin"
  ln -sf usr/lib "$SKEL/lib"; ln -sf usr/lib64 "$SKEL/lib64"
  printf '{"language":"sh","language_version":"5","system_packages":["coreutils"]}' > "$WORK/req.json"
  if ! workpod pod image import --skeleton "$SKEL" --requirements "$WORK/req.json" \
         --layer /usr:/usr > "$WORK/import.log" 2>&1; then
    mark fatal "no image in the index: $(tail -1 "$WORK/import.log")"
    exit 1
  fi

  mkdir -p "$WORK/repo"
  printf 'def parse(s):\n    return s.strip()\n' > "$WORK/repo/parser.py"
  if ! workpod pod base s3 --from "$WORK/repo" > "$WORK/base.log" 2>&1; then
    mark fatal "no base: $(tail -1 "$WORK/base.log")"
    exit 1
  fi
  BASE="$(awk -F'\t' '$1 == "base" {print $2}' "$WORK/base.log")"

  # Every job is the same job, because what is being measured is the machine and not the mix. It
  # edits one file and a blocking check reads the edit back — a job that delivered without a check
  # passing would be a faster number for a weaker claim (Q-02).
  job() {
    cat > "$WORK/job-$1.json" <<EOF
{
  "order_id": "018f4242-0000-7000-8000-0000000$(printf '%05d' "$1")",
  "attempt": 1,
  "cell": "eu-c1",
  "project": "018f4242-0000-7000-8000-00000000000b",
  "platform": "alpine",
  "class": "small",
  "requirements": {"language": "sh", "language_version": "5", "system_packages": ["coreutils"]},
  "command": ["/bin/sh", "-c", "printf 'def parse(s):\\\\n    return s.strip().lower()\\\\n' > parser.py"],
  "places": {"checks": [{"name": "deterministic",
                         "command": ["/bin/sh", "-c", "grep -q lower parser.py"],
                         "blocks": true}],
             "acceptance": "tests.new"}
}
EOF
  }

  # One warm-up job outside the measurement, and it is named rather than hidden: the first pod on a
  # node pays for the image index's first read and for the page cache being cold, and that cost is
  # a property of the boot, not of a job. Every number below is from a node that has run one.
  job 0
  workpod pod run --job "$WORK/job-0.json" --base "$BASE" --reap report > "$WORK/job-0.log" 2>&1
  mark warmup_ms "$(sed -n 's/^  "start_millis": \([0-9]*\).*$/\1/p' "$WORK/job-0.log" | tail -1)"

  DELIVERED=0
  START="$(date +%s%N)"
  for i in $(seq 1 "$JOBS"); do
    job "$i"
    workpod pod run --job "$WORK/job-$i.json" --base "$BASE" --reap report > "$WORK/job-$i.log" 2>&1
    STATE="$(sed -n 's/^  "final_state": "\([a-z]*\)".*$/\1/p' "$WORK/job-$i.log" | tail -1)"
    [ "$STATE" = "delivered" ] && DELIVERED=$((DELIVERED+1))
  done
  END="$(date +%s%N)"

  WALL_MS=$(( (END - START) / 1000000 ))
  mark jobs_total "$JOBS"
  mark jobs_delivered "$DELIVERED"
  mark wall_ms "$WALL_MS"
  mark mean_ms "$(awk -v w="$WALL_MS" -v n="$JOBS" 'BEGIN { printf "%d", w / n }')"
  mark jobs_per_hour "$(awk -v w="$WALL_MS" -v n="$JOBS" 'BEGIN { printf "%.1f", n * 3600000 / w }')"

  # What is left on the disk when the jobs are done. Not AB-T04-5 — that row is about a *restart* —
  # but the same disk read from the other side, and a throughput number measured on a node that was
  # quietly filling up would be one worth distrusting.
  mark pods_left "$(workpod pod list 2>/dev/null | grep -c . )"
  mark work_used_mb "$(df -m --output=used /data/work 2>/dev/null | tail -1 | tr -d ' ')"
  exit 0
fi

# =================================================================================================
# Host side.
# =================================================================================================
ROOT="$(cd "$HERE/.." && pwd)"
REGISTRY="$HERE/registry.tsv"
TABLE="$HERE/stage3-measurement.tsv"
RULING="$ROOT/decisions/stage-3-measurement.md"
VM="$ROOT/image/vm.sh"
OUTPUT="${OUTPUT:-$ROOT/image/.build/pass1}"
IMAGE="$OUTPUT/workpod.raw"

PASS=0; FAIL=0
pass() { printf '  \033[32mPASS\033[0m  %-44s %s\n' "$1" "${2:-}"; PASS=$((PASS+1)); }
fail() { printf '  \033[31mFAIL\033[0m  %-44s %s\n' "$1" "${2:-}"; FAIL=$((FAIL+1)); }

printf '\n\033[1mStage 3 — the measurement the stage ends with\033[0m\n\n'

# ---- the ruling exists before the measurement does (V-05) ---------------------------------------
if [ -r "$RULING" ] && grep -q '^\*\*Status:\*\* ruled' "$RULING"; then
  pass "S3-0 the measurement is ruled, not invented here" "decisions/stage-3-measurement.md"
else
  fail "S3-0 the measurement is ruled, not invented here" "no ruled decisions/stage-3-measurement.md"
fi

# ---- the two numbers that are already rows -------------------------------------------------------
# Read out of the registry, which is the instrument. A row that is green there names the run that
# made it green, and that run is this number's evidence — copying the figure into a second file
# would be a second place for it to drift.
registry_state()    { awk -F'\t' -v id="$1" '$1 == id {print $3}' "$REGISTRY"; }
registry_evidence() { awk -F'\t' -v id="$1" '$1 == id {print $5}' "$REGISTRY"; }
for row in "AB-T04-5:zero orphaned subvolumes after a restart" \
           "AB-A06-11:double execution without double effect"; do
  id="${row%%:*}"; what="${row#*:}"
  state="$(registry_state "$id")"
  evidence="$(registry_evidence "$id")"
  if [ "$state" = "green" ] && [ -n "$evidence" ]; then
    pass "S3-1 $what" "$id green -> $evidence"
  else
    fail "S3-1 $what" "$id is ${state:-not in the registry}${evidence:+ -> $evidence}"
  fi
done

if [ "$MODE" = "host" ]; then
  printf '\n  %d met, %d not — the third number needs a machine\n\n' "$PASS" "$FAIL"
  [ "$FAIL" -eq 0 ]
  exit $?
fi

# ---- the number that needs a node ---------------------------------------------------------------
LOG="$(mktemp -d)"
trap 'rm -rf "$LOG"' EXIT

if [ -n "${S3_REPLAY:-}" ]; then
  [ -r "$S3_REPLAY" ] || { echo "stage3: no console log at $S3_REPLAY" >&2; exit 2; }
  cp "$S3_REPLAY" "$LOG/probe"
  echo "== replaying $S3_REPLAY — no machine was booted for this number" >&2
else
  [ -f "$IMAGE" ] || { echo "stage3: no image at $IMAGE — run image/build.sh first" >&2; exit 2; }
  DISKS="$LOG/disks"
  mkdir -p "$DISKS"
  printf 'eu-c1'          > "$DISKS/workpod.cell"
  printf '127.0.0.1:8443' > "$DISKS/workpod.control"
  printf 'probe-s3'       > "$DISKS/workpod.locality_group"
  TPM=()
  command -v swtpm >/dev/null 2>&1 && TPM=(--tpm)

  # The same machine the pipeline leg boots, for the same reason: the number is only comparable to
  # the next one if the machine it was measured on travels with it, and 4 GB with 4 cores is what a
  # build runner can spare beside its own work.
  "$VM" --timeout 2400 --role all --memory 4096 --cpus 4 "${TPM[@]}" \
    --file "$DISKS/workpod.cell" --file "$DISKS/workpod.control" --file "$DISKS/workpod.locality_group" \
    --persist-disk "$DISKS/data.raw:workpod-data:6G" \
    --persist-disk "$DISKS/work.raw:workpod-work:8G" \
    "$HERE/stage3-measurement.sh" probe "$JOBS" 2>&1 | tee "$LOG/probe"
fi

sed -n 's/.*WORKPOD-S3: \([a-z0-9_]*\) \(.*\)$/\1 \2/p' "$LOG/probe" > "$LOG/marks"
m() { awk -v k="$1" '$1 == k { $1=""; sub(/^ +/, ""); print; exit }' "$LOG/marks"; }

# A machine that never got as far as a job has nothing to say about jobs per hour, and a table of
# question marks underneath a failure is noise. The run stops here and says which step it died on.
FATAL="$(m fatal)"
if [ -n "$FATAL" ]; then
  fail "S3-2 jobs per hour on one node" "the machine did not get to a job: $FATAL"
  printf '\n  %d met, %d not\n\n' "$PASS" "$FAIL"
  exit 1
fi

TOTAL="$(m jobs_total)"; DELIVERED="$(m jobs_delivered)"; RATE="$(m jobs_per_hour)"
printf '\n\033[1m  jobs per hour, on one node\033[0m\n\n'
printf '  machine        %s cores · %s MB\n' "$(m machine_cores ?)" "$(m machine_mem_total_mb ?)"
printf '  jobs           %s of %s delivered, %s ms each on average\n' \
       "${DELIVERED:-0}" "${TOTAL:-0}" "$(m mean_ms ?)"
printf '  warm-up        the first pod on the node took %s ms to start, outside the measurement\n' \
       "$(m warmup_ms ?)"
printf '  left behind    %s pods on the node, %s MB used on /data/work\n' \
       "$(m pods_left ?)" "$(m work_used_mb ?)"
printf '\n  \033[1mjobs per hour  %s\033[0m  — stated by hand, one node, no captain, no model\n\n' \
       "${RATE:-?}"

# Every job has to have delivered. A rate computed over jobs that ended `unproven` would be the
# throughput of a machine doing something other than the work (Q-02).
if [ -n "$RATE" ] && [ "$DELIVERED" = "$TOTAL" ] && [ "${TOTAL:-0}" -gt 0 ]; then
  pass "S3-2 jobs per hour on one node" "$RATE jobs/h over $TOTAL delivered jobs"
else
  fail "S3-2 jobs per hour on one node" "${DELIVERED:-0} of ${TOTAL:-0} delivered · rate ${RATE:-none}"
fi

# ---- the table -----------------------------------------------------------------------------------
# The recorded number against this run's. `pending` is the state before any run has produced one,
# and it is reported rather than failed — the same shape image/verify.sh uses for an unsealed
# revision. Once a number is recorded, a fresh measurement more than a factor of two away from it
# stops the run: that is either the machine or the platform having changed, and both are worth
# stopping for.
RECORDED="$(awk -F'\t' '$1 == "jobs_per_hour" {print $2}' "$TABLE" 2>/dev/null)"
EVIDENCE="$(awk -F'\t' '$1 == "jobs_per_hour" {print $4}' "$TABLE" 2>/dev/null)"
if [ -z "$RECORDED" ]; then
  fail "S3-3 the number is recorded with the run that made it" "no jobs_per_hour row in $TABLE"
elif [ "$RECORDED" = "pending" ]; then
  printf '  \033[33mRECORD\033[0m  %-44s %s\n' \
    "S3-3 the number is recorded with the run that made it" \
    "$TABLE says pending — enter $RATE and this run's URL"
elif [ -z "$EVIDENCE" ]; then
  fail "S3-3 the number is recorded with the run that made it" "$RECORDED without a run — Q-02"
else
  DRIFT="$(awk -v a="$RECORDED" -v b="${RATE:-0}" 'BEGIN { print (b > a * 2 || b < a / 2) ? "far" : "near" }')"
  if [ "$DRIFT" = "near" ]; then
    pass "S3-3 the number is recorded with the run that made it" "$RECORDED jobs/h -> $EVIDENCE"
  else
    fail "S3-3 the number is recorded with the run that made it" \
      "recorded $RECORDED, measured $RATE — more than a factor of two"
  fi
fi

printf '\n  %d met, %d not\n\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
exit $?
