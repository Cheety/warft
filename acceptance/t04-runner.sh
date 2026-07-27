#!/usr/bin/env bash
# t04-runner.sh — the runner and the workpod, measured on a node (AP-3.3).
#
#   acceptance/t04-runner.sh          the tables, then one boot of the built image
#   acceptance/t04-runner.sh probe    in the machine: pods, contract, lifecycle, reaper
#
# Ten rows hang on this run:
#
#   AB-T03-1  M  image resolution — a hit starts in ~200 ms, a miss produces a build job
#   AB-T04-2  P  a pod without network and without keys
#   AB-T04-3  S  created → active → frozen after 45 s → checkpointed → reaped
#   AB-T04-5  S  after a worker restart, zero orphaned subvolumes
#   AB-RA-1   S  the allocation sets request and limit per SP-RA-1's table
#   AB-RA-2   P  a pod above memory.high is throttled, not killed
#   AB-RA-4   P  a pod with a fork loop does not paralyze the machine
#   AB-RC-5   P  a pod with one core does not start four workers
#   AB-B02-3  P  a DNS query in the pod fails
#   AB-E02-4  S  the harness is read-only in the pod, and updating it is not an image rebuild
#
# Nothing here is simulated. The pods are runc containers on btrfs snapshots with real cgroups; the
# numbers are read out of /sys/fs/cgroup rather than out of the program that wrote them; the freeze
# waits the whole 45 s SP-T04-3 asks for and the checkpoint is a CRIU dump. The reaper's row is
# evidenced by killing three supervisors and restarting the worker.
#
# The host side checks the two tables first, and it checks them against their *sources*: the four
# given columns of platform/internal/allocation/ra1-classes.tsv against SP-RA-1's own table in
# 01-specification.md, and the four ruled columns against decisions/resource-contract.md. A number
# that moved in one and not the other is drift, and drift is an error here the way it is between the
# matrix and the registry.
#
# Exit:  0 = the ten rows are evidenced by this run
#        1 = they are not

set -uo pipefail

MODE="${1:-drive}"
HERE="$(cd "$(dirname "$0")" && pwd)"

# =================================================================================================
# Host side.
# =================================================================================================
if [ "$MODE" = drive ]; then
  ROOT="$(cd "$HERE/.." && pwd)"
  SPEC="$ROOT/01-specification.md"
  RULING="$ROOT/decisions/resource-contract.md"
  CLASSES="$ROOT/platform/internal/allocation/ra1-classes.tsv"
  VM="$ROOT/image/vm.sh"
  DISKS="$(mktemp -d)"
  trap 'rm -rf "$DISKS"' EXIT

  PASS=0; FAIL=0
  pass() { printf '  \033[32mPASS\033[0m  %-44s %s\n' "$1" "${2:-}"; PASS=$((PASS+1)); }
  fail() { printf '  \033[31mFAIL\033[0m  %-44s %s\n' "$1" "${2:-}"; FAIL=$((FAIL+1)); }

  printf '\n\033[1mR-A — the four classes, against their two sources\033[0m\n\n'

  # SP-RA-1's table, out of the specification: | `tiny` | 0.1 | 1.0 | 128 MB | 512 MB | … |
  # CPU as milli-cores, memory as binary bytes — the reading decisions/resource-contract.md §1 rules.
  spec_row() {
    awk -F'|' -v c="$1" '
      function trim(x) { gsub(/^ +| +$/, "", x); return x }
      $2 ~ ("`" c "`") && $3 ~ /^ *[0-9.]+ *$/ {
        cpu_req = trim($3) * 1000; cpu_lim = trim($4) * 1000
        split(trim($5), a, " "); split(trim($6), b, " ")
        ram_req = a[1] * (a[2] == "GB" ? 1073741824 : 1048576)
        ram_lim = b[1] * (b[2] == "GB" ? 1073741824 : 1048576)
        printf "%d %d %d %d", cpu_req, cpu_lim, ram_req, ram_lim
        exit
      }' "$SPEC"
  }
  # The ruling's table: | `tiny` | 10 | 134217728 | 536870912 | 128 | 100 |
  ruled_row() {
    awk -F'|' -v c="$1" '
      $2 ~ ("`" c "`") && $3 ~ /^ *[0-9]+ *$/ {
        gsub(/ /, "", $3); gsub(/ /, "", $4); gsub(/ /, "", $5); gsub(/ /, "", $6); gsub(/ /, "", $7)
        printf "%s %s %s %s %s", $3, $4, $5, $6, $7
        exit
      }' "$RULING"
  }
  file_row() { awk -F'\t' -v c="$1" '$1==c {printf "%s %s %s %s %s %s %s", $2, $3, $4, $5, $6, $7, $8}' "$CLASSES"; }

  for class in tiny small medium large; do
    F="$(file_row "$class")"
    read -r f_cpureq f_cpulim f_ramreq f_ramlim f_weight f_pids f_io <<< "$F"
    read -r s_cpureq s_cpulim s_ramreq s_ramlim <<< "$(spec_row "$class")"
    read -r r_weight r_min r_high r_pids r_io <<< "$(ruled_row "$class")"

    if [ -n "$s_cpureq" ] && [ "$f_cpureq" = "$s_cpureq" ] && [ "$f_cpulim" = "$s_cpulim" ] \
       && [ "$f_ramreq" = "$s_ramreq" ] && [ "$f_ramlim" = "$s_ramlim" ]; then
      pass "RA1-a $class is SP-RA-1's row" "$((f_cpureq))m/$((f_cpulim))m cpu · $((f_ramreq/1048576))/$((f_ramlim/1048576)) MiB"
    else
      fail "RA1-a $class is SP-RA-1's row" "panel: ${s_cpureq:-none} ${s_cpulim:-} ${s_ramreq:-} ${s_ramlim:-} | file: $f_cpureq $f_cpulim $f_ramreq $f_ramlim"
    fi

    # The ruled half: memory.min is the request and memory.high is the limit, so the ruling's
    # columns three and four must be the file's RAM columns — the mapping of §2, checked.
    if [ -n "$r_weight" ] && [ "$f_weight" = "$r_weight" ] && [ "$f_pids" = "$r_pids" ] && [ "$f_io" = "$r_io" ] \
       && [ "$f_ramreq" = "$r_min" ] && [ "$f_ramlim" = "$r_high" ]; then
      pass "RA1-b $class is the ruled row" "cpu.weight=$f_weight pids.max=$f_pids io.latency=${f_io}ms"
    else
      fail "RA1-b $class is the ruled row" "ruled: ${r_weight:-none} ${r_min:-} ${r_high:-} ${r_pids:-} ${r_io:-} | file: $f_weight $f_ramreq $f_ramlim $f_pids $f_io"
    fi
  done

  # SP-RA-2 and SP-RA-3 are absences, and the ruling has to keep saying so.
  if grep -q 'memory.max` is \*\*not set' "$RULING" || grep -q '`memory.max` and `cpu.max`' "$RULING"; then
    pass "RA1-c the two forbidden knobs are ruled out" "no memory.max, no cpu.max (SP-RA-2, SP-RA-3)"
  else
    fail "RA1-c the two forbidden knobs are ruled out" "the ruling no longer says which knobs stay unset"
  fi

  if [ "$FAIL" -ne 0 ]; then
    printf '\n  the tables disagree; the boot would measure a program the ruling does not describe\n\n'
    exit 1
  fi

  # ---- the boot -------------------------------------------------------------------------------
  printf 'eu-c1'          > "$DISKS/workpod.cell"
  printf '127.0.0.1:8443' > "$DISKS/workpod.control"
  printf 'probe-t04'      > "$DISKS/workpod.locality_group"

  TPM=()
  command -v swtpm >/dev/null 2>&1 && TPM=(--tpm)

  printf '\n\033[1m== boot: the runner on a node\033[0m\n'
  if "$VM" --timeout 1800 --role all --memory 6144 --cpus 4 "${TPM[@]}" \
       --file "$DISKS/workpod.cell" --file "$DISKS/workpod.control" --file "$DISKS/workpod.locality_group" \
       --file "$CLASSES" \
       --persist-disk "$DISKS/data.raw:workpod-data:6G" \
       --persist-disk "$DISKS/work.raw:workpod-work:6G" \
       "$HERE/t04-runner.sh" probe 2>&1 | tee "$DISKS/probe.log"; then
    echo
    echo "AB-T03-1, AB-T04-2, AB-T04-3, AB-T04-5, AB-RA-1, AB-RA-2, AB-RA-4, AB-RC-5, AB-B02-3 and"
    echo "AB-E02-4 green through this run: pods start under R-A's contract without a network, live"
    echo "the lifecycle T-04 describes, and none of them survives the worker that started it."
    exit 0
  fi
  echo
  echo "the ten rows of AP-3.3 stay red."
  exit 1
fi

# =================================================================================================
# Guest side.
# =================================================================================================
PASS=0; FAIL=0
pass() { printf '  \033[32mPASS\033[0m  %-44s %s\n' "$1" "${2:-}"; PASS=$((PASS+1)); }
fail() { printf '  \033[31mFAIL\033[0m  %-44s %s\n' "$1" "${2:-}"; FAIL=$((FAIL+1)); }
banner() { printf '\n\033[1m%s\033[0m\n\n' "$1"; }

CREDS="${CREDENTIALS_DIRECTORY:-/run/credentials/@system}"
WORK=/run/t04
REGISTERED=/run/workpod/registered

start_boot() {
  systemctl mask --runtime getty.target serial-getty@ttyS0.service >/dev/null 2>&1
  systemctl start --no-block multi-user.target
}
wait_file() { local i; for i in $(seq 1 "$2"); do [ -e "$1" ] && return 0; sleep 1; done; return 1; }

# report FIELD LOGFILE — one scalar out of the report `workpod pod run` printed. No jq in the image
# (SP-A02-3), and the report is written one field per line, so this is a read rather than a parse.
report() { sed -n "s/^  \"$1\": \"\{0,1\}\([^\",]*\)\"\{0,1\},\{0,1\}$/\1/p" "$2" | tail -1; }

# What a pod says about itself comes back inside the report's one JSON string, with its newlines
# escaped. So the pod prints tokens rather than output to be parsed: every question below is answered
# by one word that either stands in the report or does not.
podcg() { sed -n 's/.*: cgroup \(.*\)$/\1/p' "$1" | tail -1; }

# podout LOGFILE — what the pod itself printed, as lines. The report carries it as one JSON string
# with its newlines escaped, and it repeats the command before the output; taking only what follows
# the output marker is what keeps a question from being answered by the text of the question.
podout() {
  sed -n 's/^  "report_text": "\(.*\)",\{0,1\}$/\1/p' "$1" \
    | sed 's/\\n/\n/g' \
    | awk 'f { print } /^--- output ---$/ { f = 1 }'
}

# job NAME CLASS COMMAND… — a job stated by hand (decisions/jobs-by-hand.md).
#
# The arguments are escaped for JSON here rather than written escaped above, because a probe command
# is a shell script and a shell script has newlines in it — and a literal newline inside a JSON
# string is not a JSON string. The first run of this file wrote four invalid job files that way and
# every check that read the pod's own words failed at once.
json_string() { printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g' | sed ':a;N;$!ba;s/\n/\\n/g'; }
job() {
  local name="$1" class="$2"; shift 2
  local args=""
  for a in "$@"; do args="$args${args:+,}\"$(json_string "$a")\""; done
  cat > "$WORK/$name.json" <<EOF
{
  "order_id": "018f4242-0000-7000-8000-0000000000$(printf '%02d' "$JOBSEQ")",
  "attempt": 1,
  "cell": "eu-c1",
  "project": "018f4242-0000-7000-8000-00000000000b",
  "platform": "alpine",
  "class": "$class",
  "requirements": {"language": "sh", "language_version": "5", "system_packages": ["coreutils"]},
  "command": [$args]
}
EOF
  JOBSEQ=$((JOBSEQ+1))
}
JOBSEQ=1

banner "AP-3.3 — the runner on a node"
start_boot
if wait_file "$REGISTERED" 420; then
  pass "the node stands" "$(tr '\n' ' ' < "$REGISTERED")"
else
  fail "the node stands" "no $REGISTERED after 420 s"
  journalctl -u workpod-disk -u workpod-selftest -u workpod-worker --no-pager 2>&1 | tail -30 | sed 's/^/        /'
  printf '\n  %d met, %d not\n' "$PASS" "$FAIL"
  exit 1
fi

mkdir -p "$WORK"

# The image: a skeleton of mount points and the node's own /usr as the one shared read-only layer.
# Building the tree is the `image.build` procedure of AP-5.2; importing one is the index's write
# side, which is what a runner needs to resolve anything at all (decisions/pod-runtime.md §1).
SKEL="$WORK/skeleton"
mkdir -p "$SKEL"/{usr,proc,sys,dev,tmp,run,work,harness,etc,var}
ln -sf usr/bin "$SKEL/bin"; ln -sf usr/sbin "$SKEL/sbin"
ln -sf usr/lib "$SKEL/lib"; ln -sf usr/lib64 "$SKEL/lib64"
printf '{"language":"sh","language_version":"5","system_packages":["coreutils"]}' > "$WORK/req.json"
if IMPORT="$(workpod pod image import --skeleton "$SKEL" --requirements "$WORK/req.json" --layer /usr:/usr 2>&1)"; then
  IMAGE="$(awk -F'\t' '$1=="image"{print $2}' <<< "$IMPORT")"
  pass "an image stands in the index" "$IMAGE"
else
  fail "an image stands in the index" "$IMPORT"
  printf '\n  %d met, %d not\n' "$PASS" "$FAIL"
  exit 1
fi

mkdir -p "$WORK/repo" && printf 'one\ntwo\n' > "$WORK/repo/file.txt"
workpod pod base repo-a --from "$WORK/repo" >/dev/null 2>&1 \
  && pass "a working-copy base stands" "/data/work/bases/repo-a" \
  || fail "a working-copy base stands" "$(workpod pod base repo-a --from "$WORK/repo" 2>&1)"

# =================================================================================================
# AB-T03-1 — image resolution: a hit starts in ~200 ms, a miss produces a build job
# =================================================================================================
banner "T-03 — image resolution (AB-T03-1, measurement)"

cat > "$WORK/miss.json" <<'EOF'
{
  "order_id": "018f4242-0000-7000-8000-0000000000f1", "attempt": 1, "cell": "eu-c1",
  "project": "018f4242-0000-7000-8000-00000000000b", "platform": "alpine", "class": "medium",
  "requirements": {"language": "rust", "language_version": "1.83", "system_packages": ["clang"]},
  "command": ["/usr/bin/true"]
}
EOF
MISS="$(workpod pod resolve --job "$WORK/miss.json" 2>&1)"
MISSHASH="$(awk -F'\t' '$1=="miss"{print $2}' <<< "$MISS")"
BUILDJOB="$(awk -F'\t' '$1=="build-job"{print $2}' <<< "$MISS")"
if [ -n "$MISSHASH" ] && [ -f "$BUILDJOB" ] && grep -q "image-build:$MISSHASH" "$BUILDJOB"; then
  pass "T03-1a a miss produces a build job" "$(basename "$BUILDJOB"), keyed by the requirement hash"
else
  fail "T03-1a a miss produces a build job" "$MISS"
fi
# And the pod does not start on a miss: refusing is the point, not a fallback image.
job miss-run medium /usr/bin/true
sed -i 's/"language": "sh"/"language": "rust"/' "$WORK/miss-run.json"
if workpod pod run --job "$WORK/miss-run.json" >"$WORK/miss-run.log" 2>&1; then
  fail "T03-1b a miss starts nothing" "the runner started a pod for an image it does not have"
else
  pass "T03-1b a miss starts nothing" "the runner refuses and names the build job"
fi

# Twenty starts on a hit, and the median of them. decisions/pod-runtime.md §5: a median rather than a
# maximum, because the first start after a boot pays for page cache the second does not.
job hit tiny /usr/bin/true
: > "$WORK/starts"
STARTFAIL=0
for i in $(seq 1 20); do
  if workpod pod run --job "$WORK/hit.json" --base /data/work/bases/repo-a --reap report >"$WORK/hit-$i.log" 2>&1; then
    report start_millis "$WORK/hit-$i.log" >> "$WORK/starts"
  else
    STARTFAIL=$((STARTFAIL+1))
    tail -3 "$WORK/hit-$i.log" | sed 's/^/        /'
  fi
done
MEDIAN="$(sort -n "$WORK/starts" | sed -n '10p')"
SLOWEST="$(sort -n "$WORK/starts" | tail -1)"
COUNT="$(wc -l < "$WORK/starts")"
MACHINE="$(nproc) cores, $(awk '/MemTotal/{printf "%.1f GB", $2/1048576}' /proc/meminfo)"
echo "T04-CONSTANT pod_start_median_ms=${MEDIAN:-none} slowest=${SLOWEST:-none} runs=$COUNT machine=$MACHINE"
if [ "$COUNT" -eq 20 ] && [ -n "$MEDIAN" ] && [ "$MEDIAN" -le 250 ]; then
  pass "T03-1c a hit starts in ~200 ms" "median ${MEDIAN} ms over 20 starts, slowest ${SLOWEST} ms ($MACHINE)"
else
  fail "T03-1c a hit starts in ~200 ms" "median ${MEDIAN:-none} ms over $COUNT starts ($STARTFAIL failed)"
fi

# =================================================================================================
# AB-RA-1 — the allocation sets request and limit per the table
# =================================================================================================
banner "R-A — the contract in the pod's own cgroup (AB-RA-1, script)"

CLASSFILE="$CREDS/ra1-classes.tsv"
[ -f "$CLASSFILE" ] || fail "RA1-0 the class table arrived" "no $CLASSFILE — image/vm.sh --file carries it"
want() { awk -F'\t' -v c="$1" -v n="$2" '$1==c {print $n}' "$CLASSFILE"; }

for class in tiny small medium large; do
  job "class-$class" "$class" /usr/bin/true
  workpod pod run --job "$WORK/class-$class.json" --base /data/work/bases/repo-a --reap report \
    >"$WORK/class-$class.log" 2>&1
  LINE="$(grep -m1 ': contract ' "$WORK/class-$class.log")"
  get() { sed -n "s/.* $1=\([^ ]*\).*/\1/p" <<< "$LINE"; }
  W="$(get cpu.weight)"; MIN="$(get memory.min)"; HIGH="$(get memory.high)"
  PIDS="$(get pids.max)"; OOMG="$(get memory.oom.group)"
  IOTARGET="$(sed -n 's/.*io.latency=[0-9]*:[0-9]* target=\([0-9]*\).*/\1/p' <<< "$LINE")"
  MAXMEM="$(sed -n 's/.*memory.max=\([^ ]*\).*/\1/p' <<< "$LINE")"
  if [ "$W" = "$(want "$class" 6)" ] && [ "$MIN" = "$(want "$class" 4)" ] \
     && [ "$HIGH" = "$(want "$class" 5)" ] && [ "$PIDS" = "$(want "$class" 7)" ] \
     && [ "$IOTARGET" = "$(( $(want "$class" 8) * 1000 ))" ] && [ "$OOMG" = "1" ]; then
    pass "RA1-d $class, read back out of the cgroup" "weight $W · min $((MIN/1048576)) MiB · high $((HIGH/1048576)) MiB · pids $PIDS"
  else
    fail "RA1-d $class, read back out of the cgroup" "${LINE:-no contract line in the log}"
  fi
  # SP-RA-2: throttled, not shot. The absence is the requirement.
  if [ "$MAXMEM" = "max" ]; then
    pass "RA1-e $class has no memory.max" "the pod is throttled, never shot (SP-RA-2)"
  else
    fail "RA1-e $class has no memory.max" "memory.max=$MAXMEM"
  fi
done

# =================================================================================================
# AB-T04-2 · AB-B02-3 · AB-RC-5 · AB-E02-4 — what is not in the pod, and what is
# =================================================================================================
banner "T-04 — no network, no keys, one core, one harness (AB-T04-2, AB-B02-3, AB-RC-5, AB-E02-4)"

# A key and a token on the node, in the worker's own environment. If either reaches a pod, it
# reached it because the runner passed it on — which is the failure SP-T04-2 is about.
export ANTHROPIC_API_KEY="sk-node-secret-must-not-travel"
export GITHUB_TOKEN="ghp-node-secret-must-not-travel"

PROBE='
echo IPLINKS=$(ip -o link | grep -c .)
echo NETDEVS=$(ls /sys/class/net | paste -sd, -)
getent hosts example.com >/dev/null 2>&1 && echo DNS-ANSWERED || echo DNS-REFUSED
echo SECRETS=$(env | grep -c not-travel)
echo HARNESSSUM=$(sha256sum /harness/workpod | cut -c1-64)
touch /harness/written 2>/dev/null && echo HARNESS-WRITABLE || echo HARNESS-READONLY
echo changed >> /work/file.txt && echo WORK-WRITABLE || echo WORK-READONLY
test -S /run/workpod/harness.sock && echo SOCKET-THERE || echo SOCKET-MISSING
echo CORES=$(nproc)
echo CONC=$MAKEFLAGS,$CARGO_BUILD_JOBS,$UV_THREADPOOL_SIZE,$TURBO_CONCURRENCY
echo NODE=$NODE_OPTIONS'
job inside tiny /usr/bin/bash -c "$PROBE"
workpod pod run --job "$WORK/inside.json" --base /data/work/bases/repo-a --reap report \
  >"$WORK/inside.log" 2>&1
podout "$WORK/inside.log" > "$WORK/inside.out"
IN="$WORK/inside.out"

# ---- no network (AB-T04-2, AB-B02-3) ----------------------------------------------------------
IPLINKS="$(sed -n 's/^IPLINKS=//p' "$IN")"
NETDEVS="$(sed -n 's/^NETDEVS=//p' "$IN")"
if [ "$IPLINKS" = "1" ] && [ "$NETDEVS" = "lo" ]; then
  pass "T04-2a the pod has no interface but lo" "a network namespace with nothing put into it"
else
  fail "T04-2a the pod has no interface but lo" "ip counts ${IPLINKS:-none} link(s) · sysfs: ${NETDEVS:-none}"
fi
if grep -q 'DNS-REFUSED' "$IN" && ! grep -q 'DNS-ANSWERED' "$IN"; then
  pass "B02-3a a DNS query in the pod fails" "no resolver, no route, no namespace to ask in"
else
  fail "B02-3a a DNS query in the pod fails" "$(grep -m1 'DNS-' "$IN")"
fi

# ---- no keys (AB-T04-2) ------------------------------------------------------------------------
if grep -qx 'SECRETS=0' "$IN"; then
  pass "T04-2b no LLM key and no Git token" "the environment is built from the allocation, never inherited"
else
  fail "T04-2b no LLM key and no Git token" "$(sed -n 's/^SECRETS=/secrets in the pod: /p' "$IN")"
fi
if grep -qx 'SOCKET-THERE' "$IN" && grep -q 'harness socket: reachable' "$WORK/inside.log"; then
  pass "T04-2c the only way out is the socket" "the Harness service answers on /run/workpod/harness.sock"
else
  fail "T04-2c the only way out is the socket" "$(grep -m1 -E 'SOCKET-|harness socket' "$IN" "$WORK/inside.log")"
fi

# ---- concurrency from the allocation (AB-RC-5) --------------------------------------------------
CORES="$(sed -n 's/^CORES=//p' "$IN")"
CONC="$(sed -n 's/^CONC=//p' "$IN")"
if [ -n "$CORES" ] && [ "$CORES" -gt 1 ] && [ "$CONC" = "-j1,1,1,1" ]; then
  pass "RC5-a one core, one worker" "nproc says $CORES, the allocation says one — make/cargo/uv/turbo all 1"
else
  fail "RC5-a one core, one worker" "nproc=${CORES:-none}, concurrency=${CONC:-none}"
fi
if grep -qx 'NODE=--max-old-space-size=384 --v8-pool-size=1' "$IN"; then
  pass "RC5-b Node is told both numbers" "heap 384 MiB, pool 1 — neither derived from the host"
else
  fail "RC5-b Node is told both numbers" "$(sed -n 's/^NODE=//p' "$IN")"
fi

# ---- the harness, read-only, the same binary (AB-E02-4) ------------------------------------------
HOSTSUM="$(sha256sum /usr/bin/workpod | cut -d' ' -f1)"
PODSUM="$(sed -n 's/^HARNESSSUM=//p' "$IN")"
if [ "$PODSUM" = "$HOSTSUM" ]; then
  pass "E02-4a the pod runs the node's own binary" "${HOSTSUM:0:16}…"
else
  fail "E02-4a the pod runs the node's own binary" "pod ${PODSUM:-none} vs node ${HOSTSUM:0:16}"
fi
if grep -qx 'HARNESS-READONLY' "$IN"; then
  pass "E02-4b the harness is read-only in the pod" "a pod cannot change the thing that runs it"
else
  fail "E02-4b the harness is read-only in the pod" "$(grep -m1 '^HARNESS-' "$IN")"
fi

# The second half of SP-E02-4: a harness update is an image update, not a container rebuild. A
# different harness binary is mounted into a pod of the *same* image, and the image's digest does
# not move — because the manifest never covered the harness in the first place.
NEXT=/var/lib/workpod/workpod-next
cp /usr/bin/workpod "$NEXT"
printf '\n# a harness update\n' >> "$NEXT"
chmod +x "$NEXT"
NEXTSUM="$(sha256sum "$NEXT" | cut -d' ' -f1)"
job updated tiny /usr/bin/bash -c 'sha256sum /harness/workpod'
"$NEXT" pod run --job "$WORK/updated.json" --base /data/work/bases/repo-a --reap report \
  >"$WORK/updated.log" 2>&1
UPDSUM="$(podout "$WORK/updated.log" | grep -m1 -oE '^[0-9a-f]{64}')"
UPDIMAGE="$(report image_digest "$WORK/updated.log")"
OLDIMAGE="$(report image_digest "$WORK/inside.log")"
if [ "$UPDSUM" = "$NEXTSUM" ] && [ "$UPDSUM" != "$HOSTSUM" ] && [ "$UPDIMAGE" = "$OLDIMAGE" ] && [ -n "$UPDIMAGE" ]; then
  pass "E02-4c a harness update is not an image rebuild" "the pod runs the new binary, the image is still $UPDIMAGE"
else
  fail "E02-4c a harness update is not an image rebuild" "harness ${UPDSUM:-none} vs ${NEXTSUM:0:16} · image ${UPDIMAGE:-none} vs ${OLDIMAGE:-none}"
fi

# =================================================================================================
# AB-RA-2 — a pod above memory.high is throttled, not killed
# =================================================================================================
banner "R-A — throttled, not shot (AB-RA-2, probe)"

# A `tiny` pod is allowed 512 MiB before it is throttled; this one holds 700. There is no
# memory.max, so nothing may kill it: the kernel reclaims against it, swaps it to zram, slows it
# down, and lets it finish. The two numbers that decide the row are read out of the pod's own
# memory.events while it is still running — after the reap the cgroup is gone, and a claim about a
# pod has to be read from the pod.
job hog tiny /usr/bin/bash -c 'head -c 900M /dev/zero | tail -c 700M | wc -c; echo HOG-FINISHED'
timeout 400 workpod pod run --job "$WORK/hog.json" --base /data/work/bases/repo-a --reap report \
  >"$WORK/hog.log" 2>&1 &
HOGPID=$!
HOGCG=""
for _ in $(seq 1 120); do
  HOGCG="$(podcg "$WORK/hog.log")"
  [ -n "$HOGCG" ] && break
  sleep 1
done
HIGHEV=0; OOMKILL=0
while kill -0 "$HOGPID" 2>/dev/null; do
  if [ -f "$HOGCG/memory.events" ]; then
    h="$(awk '$1=="high"{print $2}' "$HOGCG/memory.events")"
    o="$(awk '$1=="oom_kill"{print $2}' "$HOGCG/memory.events")"
    [ -n "$h" ] && HIGHEV="$h"
    [ -n "$o" ] && OOMKILL="$o"
  fi
  sleep 1
done
wait "$HOGPID"

if grep -q 'HOG-FINISHED' "$WORK/hog.log" && [ "$(report exit_code "$WORK/hog.log")" = "0" ]; then
  pass "RA2-a the pod above memory.high finished" "700 MiB held against a 512 MiB limit"
else
  fail "RA2-a the pod above memory.high finished" "$(tail -3 "$WORK/hog.log" | tr '\n' ' ')"
fi
if [ "${HIGHEV:-0}" -gt 0 ] && [ "${OOMKILL:-1}" -eq 0 ]; then
  pass "RA2-b throttled, not shot" "memory.events high=$HIGHEV, oom_kill=$OOMKILL (SP-RA-2)"
else
  fail "RA2-b throttled, not shot" "high=$HIGHEV oom_kill=$OOMKILL — cgroup ${HOGCG:-not found}"
fi

# =================================================================================================
# AB-RA-4 — a fork loop does not paralyze the machine
# =================================================================================================
banner "R-A — the fork wall (AB-RA-4, probe)"

# `tiny` may hold 128 processes. This one asks for four hundred, and the wall is read out of the
# pod's own pids.events while it is being hit — a count the pod reported itself would need a fork to
# produce, which is the one thing a pod at its pids.max cannot do.
job forkbomb tiny /usr/bin/bash -c 'for i in $(seq 1 400); do sleep 20 & done 2>/dev/null; exit 0'
timeout 300 workpod pod run --job "$WORK/forkbomb.json" --base /data/work/bases/repo-a --reap report \
  >"$WORK/forkbomb.log" 2>&1 &
FORKPID=$!
FORKCG=""
for _ in $(seq 1 120); do
  FORKCG="$(podcg "$WORK/forkbomb.log")"
  [ -n "$FORKCG" ] && break
  sleep 1
done
PIDSMAXHIT=0; PIDSPEAK=0; PINGS=0; PINGFAIL=0
while kill -0 "$FORKPID" 2>/dev/null; do
  if [ -f "$FORKCG/pids.events" ]; then
    m="$(awk '$1=="max"{print $2}' "$FORKCG/pids.events")"
    [ -n "$m" ] && PIDSMAXHIT="$m"
    c="$(cat "$FORKCG/pids.current" 2>/dev/null)"
    [ -n "$c" ] && [ "$c" -gt "$PIDSPEAK" ] && PIDSPEAK="$c"
  fi
  # The machine has to stay answerable while a pod is forking at it (AB-RA-4). The path is the
  # pull path a node registers over, not a health endpoint invented for the probe.
  if workpod ping --deadline 5s >/dev/null 2>&1; then PINGS=$((PINGS+1)); else PINGFAIL=$((PINGFAIL+1)); fi
  sleep 1
done
wait "$FORKPID"

if [ "${PIDSMAXHIT:-0}" -gt 0 ] && [ "${PIDSPEAK:-0}" -le 128 ] && [ "${PIDSPEAK:-0}" -gt 1 ]; then
  pass "RA4-a pids.max held" "the fork wall was hit $PIDSMAXHIT times, peak $PIDSPEAK of 128 (400 asked for)"
else
  fail "RA4-a pids.max held" "pids.events max=$PIDSMAXHIT, peak=$PIDSPEAK — cgroup ${FORKCG:-not found}"
fi
if [ "$PINGFAIL" -eq 0 ] && [ "$PINGS" -gt 0 ]; then
  pass "RA4-b the machine stayed answerable" "$PINGS pings during the fork loop, none missed"
else
  fail "RA4-b the machine stayed answerable" "$PINGS answered, $PINGFAIL missed"
fi
if grep -q 'io.latency=[0-9]*:[0-9]* target=100000' "$WORK/forkbomb.log"; then
  pass "RA4-c io.latency is set on the pod" "SP-RA-4's second knob, against the work disk"
else
  fail "RA4-c io.latency is set on the pod" "$(grep -m1 -o 'io.latency=[^ ]* [^ ]*' "$WORK/forkbomb.log")"
fi

# =================================================================================================
# AB-T04-3 — the lifecycle
# =================================================================================================
banner "T-04 — created → active → frozen → checkpointed → reaped (AB-T04-3, script)"

# No --reap here: the pod delivers, goes quiet, and the supervisor takes it the whole way. The 45 s
# are SP-T04-3's own number and they are waited out rather than shortened.
job life tiny /usr/bin/bash -c 'echo delivered > /work/out.txt'
timeout 400 workpod pod run --job "$WORK/life.json" --base /data/work/bases/repo-a \
  >"$WORK/life.log" 2>&1
STATES="$(grep -o '"state": "[a-z]*"' "$WORK/life.log" | sed 's/.*"\([a-z]*\)"$/\1/' | tr '\n' ' ')"
if [ "$STATES" = "created active frozen checkpointed reaped " ]; then
  pass "T04-3a the five states, in order" "$STATES"
else
  fail "T04-3a the five states, in order" "${STATES:-none} · $(tail -3 "$WORK/life.log" | tr '\n' ' ')"
fi
FROZEN_AT="$(grep -m1 -A2 '"state": "frozen"' "$WORK/life.log" | sed -n 's/.*"reason": "\(.*\)"$/\1/p')"
case "$FROZEN_AT" in
  "quiet for 4"[5-9]s|"quiet for 5"?s) pass "T04-3b frozen after 45 s of quiet" "$FROZEN_AT" ;;
  *) fail "T04-3b frozen after 45 s of quiet" "${FROZEN_AT:-no reason recorded}" ;;
esac
PATCHHASH="$(report patch_hash "$WORK/life.log")"
if [ -n "$PATCHHASH" ]; then
  pass "T04-3c the pod delivered a patch and a report" "$PATCHHASH"
else
  fail "T04-3c the pod delivered a patch and a report" "no patch hash in the report"
fi

# =================================================================================================
# AB-T04-5 — after a worker restart, zero orphaned subvolumes
# =================================================================================================
banner "T-04 — the reaper (AB-T04-5, script)"

# Three pods whose supervisors are killed outright: the working copies stay on the disk and the
# containers stay running, which is exactly what a worker that died leaves behind.
#
# The worker is stopped *first*, and not for tidiness. Its reaper sweeps every minute, so with the
# worker up the three orphans might be collected between the kill and the count — the reaper doing
# its job and the probe reading it as a failure to make one. Stopping the worker is also the more
# faithful simulation: what AB-T04-5 asks about is a node whose worker was not there.
systemctl stop workpod-worker.service
ORPHANS=()
for n in 1 2 3; do
  job "orphan-$n" tiny /usr/bin/bash -c 'sleep 600'
  workpod pod run --job "$WORK/orphan-$n.json" --base /data/work/bases/repo-a >"$WORK/orphan-$n.log" 2>&1 &
  ORPHANS+=($!)
done
for _ in $(seq 1 90); do
  [ "$(ls /data/work/pods 2>/dev/null | wc -l)" -ge 3 ] && break
  sleep 1
done
for p in "${ORPHANS[@]}"; do kill -9 "$p" 2>/dev/null; done
sleep 2
BEFORE="$(ls /data/work/pods 2>/dev/null | wc -l)"
UNSUPERVISED="$(workpod pod list 2>/dev/null | grep -c orphan)"
if [ "$BEFORE" -ge 3 ] && [ "$UNSUPERVISED" -ge 3 ]; then
  pass "T04-5a three supervisors killed" "$BEFORE working copies on the disk, $UNSUPERVISED of them unsupervised"
else
  fail "T04-5a three supervisors killed" "$BEFORE working copies, $UNSUPERVISED unsupervised — the orphans were never made"
fi

# The worker comes back. The reaper runs on the worker (SP-T04-5, V-02), and its first sweep is the
# one that matters: every pod it finds belonged to a worker that is no longer running.
systemctl start workpod-worker.service
for _ in $(seq 1 180); do
  [ "$(systemctl is-active workpod-worker.service)" = active ] && break
  sleep 1
done
for _ in $(seq 1 60); do
  [ "$(ls /data/work/pods 2>/dev/null | wc -l)" -eq 0 ] && break
  sleep 1
done
AFTER="$(ls /data/work/pods 2>/dev/null | wc -l)"
LEFTOVER="$(runc --systemd-cgroup list 2>/dev/null | tail -n +2 | wc -l)"
if [ "$AFTER" -eq 0 ] && [ "$LEFTOVER" -eq 0 ]; then
  pass "T04-5b zero orphaned subvolumes" "the restarted worker reaped all $BEFORE of them"
else
  fail "T04-5b zero orphaned subvolumes" "$AFTER subvolumes, $LEFTOVER containers still there"
  ls -la /data/work/pods 2>&1 | sed 's/^/        /'
fi
# Named orphans, not a count. Every worker start prints how many it swept — including "0" at boot —
# so the line that evidences this row is the one that names a pod it actually reaped.
REAPED="$(journalctl -u workpod-worker.service --no-pager 2>/dev/null | grep -c 'reaped the orphan')"
if [ "$REAPED" -ge 3 ]; then
  pass "T04-5c the worker says what it reaped" "$REAPED orphans named in the journal, by the worker that swept them"
else
  fail "T04-5c the worker says what it reaped" "$REAPED named · $(journalctl -u workpod-worker.service --no-pager 2>/dev/null | tail -3 | tr '\n' ' ')"
fi

# One last look at the whole disk: nothing of any pod in this run is left anywhere.
STRAY="$(ls /run/workpod/pods 2>/dev/null | wc -l)"
if [ "$STRAY" -eq 0 ]; then
  pass "T04-5d nothing left in /run either" "bundles, sockets and consoles gone with their pods"
else
  fail "T04-5d nothing left in /run either" "$STRAY pod directories under /run/workpod/pods"
fi

printf '\n  %d met, %d not\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
