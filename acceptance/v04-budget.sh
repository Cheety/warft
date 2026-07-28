#!/usr/bin/env bash
# v04-budget.sh — the pots, the caps, the share-out and the halt with two paths (AP-3.6).
#
# Five rows rest on this script:
#   AB-V04-1  S  three budgets — reservation at admission, release at the terminal state
#   AB-V04-2  P  exhaustion — running out of tokens produces a reply with options, not a truncation
#   AB-V04-4  M  fairness at the bottleneck — a heavy sender gets a lot, not everything
#   AB-T01-8  S  the limit is in pod minutes, per principal and channel, not per request
#   AB-E08-3  P  the halt takes effect through the file — with the API up, and with it switched off
#
# Nothing here is simulated. A Postgres 16 loads contract/schema.sql, the real `workpod control`
# serves against it, and the jobs are admitted by the real admission — the same code path intake
# runs. What the probes read afterwards is the state database, because "reserved at admission" is a
# claim about rows, not about a return value.
#
# OP-1 is checked first and without any of that: the ruling in decisions/OP-1.md and the file the
# binary embeds must carry the same twelve rows, and those rows must obey the three rules the ruling
# says generate them. A number that moved in one and not the other is drift.
#
# `v04-budget.sh host` runs only that half — it needs no machine, so the platform workflow runs it
# on every change.
#
# The seams are WORKPOD_DB_DSN, WORKPOD_SCHEMA and WORKPOD_HALT_FILE, and they are the probe's
# alone: on a control node the socket, the contract's place in the image and /var/lib/workpod/halt
# are constants of the program (SP-A04-4, decisions/halt-file.md).
#
# Exit:  0 = no FAIL
#        1 = at least one FAIL

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RULING="$ROOT/decisions/OP-1.md"
POTS="$ROOT/platform/internal/budget/op1-pots.tsv"
HALT_RULING="$ROOT/decisions/halt-file.md"
SCHEMA="$ROOT/contract/schema.sql"
STAGED="$ROOT/image/.build/platform-tree/usr/bin/workpod"
MODE="${1:-all}"

SCRATCH="$(mktemp -d)"
PG=""
PLANE=""
cleanup() {
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

# =================================================================================================
# OP-1 — the ruling and the file the binary carries
# =================================================================================================
banner "OP-1 — the ruling and its machine-readable half (decisions/OP-1.md)"

# The ruling's table is `| level | scope | pod_minutes | tokens | money |` with the level and the
# scope in backticks; the file is the same five columns as TSV. Both are read into one shape and
# compared as sets, so a row in one and not the other is drift whichever side it is on.
RULED_ROWS="$(awk -F'|' '
  NF >= 7 && $2 ~ /`(public|linked|confidential)`/ {
    for (i = 2; i <= 6; i++) { gsub(/[` ]/, "", $i) }
    print $2 "\t" $3 "\t" $4 "\t" $5 "\t" $6
  }' "$RULING" | sort)"
FILE_ROWS="$(awk -F'\t' '$1=="pot" {print $2 "\t" $3 "\t" $4 "\t" $5 "\t" $6}' "$POTS" | sort)"

if [ -n "$RULED_ROWS" ] && [ "$RULED_ROWS" = "$FILE_ROWS" ]; then
  pass "V04-1a the caps are the ruled ones" "$(wc -l <<< "$FILE_ROWS") rows, three levels × four scopes"
else
  fail "V04-1a the caps are the ruled ones" \
       "ruled: $(wc -l <<< "$RULED_ROWS") rows, embedded: $(wc -l <<< "$FILE_ROWS") rows; diff: $(diff <(echo "$RULED_ROWS") <(echo "$FILE_ROWS") | tr '\n' ' ')"
fi

# The three rules of the ruling, against the file. A table that can only be copied is a table
# nobody can extend, so the rules are checked and not only the values.
RULE_BREAKS="$(awk -F'\t' '
  $1=="pot" {
    minutes[$2 "/" $3] = $4; tokens[$2 "/" $3] = $5; money[$2 "/" $3] = $6
  }
  END {
    for (k in minutes) {
      split(k, part, "/")
      if (part[2] != "principal_channel_day") {
        if (tokens[k] != 8000 * minutes[k]) print k ": tokens " tokens[k] " != 8000 x " minutes[k]
        if (money[k]  != 5 * tokens[k])     print k ": money " money[k] " != 5 x " tokens[k]
      } else {
        day = part[1] "/principal_day"
        if (tokens[k] != tokens[day] || money[k] != money[day])
          print k ": the channel pot must carry the day pot ceiling in tokens and money"
        if (2 * minutes[k] != minutes[day])
          print k ": one channel is half the day in pod minutes, got " minutes[k] " of " minutes[day]
      }
    }
  }' "$POTS")"
if [ -z "$RULE_BREAKS" ]; then
  pass "V04-1b the three rules generate the table" "tokens = 8000·minutes · money = 5·tokens · the channel binds minutes"
else
  fail "V04-1b the three rules generate the table" "$(tr '\n' ' ' <<< "$RULE_BREAKS")"
fi

# §19's direction, as arithmetic: public very small, confidential a tenant cap.
LADDER="$(awk -F'\t' '$1=="pot" && $3=="principal_day" {print $2 "=" $4}' "$POTS" | sort | tr '\n' ' ')"
PUB="$(awk -F'\t' '$1=="pot" && $3=="principal_day" && $2=="public" {print $4}' "$POTS")"
CONF="$(awk -F'\t' '$1=="pot" && $3=="principal_day" && $2=="confidential" {print $4}' "$POTS")"
if [ "$PUB" -lt "$CONF" ] && [ "$((PUB * 10))" -lt "$CONF" ]; then
  pass "V04-1c public is very small, confidential is a tenant cap" "$LADDER"
else
  fail "V04-1c public is very small, confidential is a tenant cap" "$LADDER"
fi

# =================================================================================================
# The halt file — the ruling and the program agree on where it is and how long it lasts
# =================================================================================================
banner "E-08 — the second path (decisions/halt-file.md)"

HALT_GO="$ROOT/platform/internal/budget/halt.go"
RULED_PATH="$(grep -o '/var/lib/workpod/halt' "$HALT_RULING" | head -1)"
CODE_PATH="$(grep -o '"/var/lib/workpod/halt"' "$HALT_GO" | head -1 | tr -d '"')"
if [ -n "$RULED_PATH" ] && [ "$RULED_PATH" = "$CODE_PATH" ]; then
  pass "E08-3a the file is where the ruling puts it" "$CODE_PATH"
else
  fail "E08-3a the file is where the ruling puts it" "ruled '${RULED_PATH:-nothing}', read '${CODE_PATH:-nothing}'"
fi
if grep -q '60 \* time.Minute' "$HALT_GO" && grep -q '60 minutes' "$HALT_RULING"; then
  pass "E08-3b the expiry is the specification's 60 minutes" "SP-E08-4, mandatory rather than convenient"
else
  fail "E08-3b the expiry is the specification's 60 minutes" "the ruling and halt.go disagree"
fi

if [ "$MODE" = "host" ]; then
  result
  exit $?
fi

# =================================================================================================
# The artifact
# =================================================================================================
banner "AP-3.6 — the binary"

BIN="$STAGED"
if [ ! -x "$BIN" ]; then
  if command -v go >/dev/null 2>&1; then
    BIN="$SCRATCH/workpod"
    ( cd "$ROOT/platform" && CGO_ENABLED=0 go build -trimpath -buildvcs=false -o "$BIN" ./cmd/workpod ) \
      || { fail "the binary builds" "go build failed"; result; exit 1; }
  else
    skip "V04 all database checks" "neither a staged build nor go on this machine; the CI leg brings one"
    result
    exit 0
  fi
fi
pass "the binary stands" "$(sha256sum "$BIN" | cut -d' ' -f1)"

if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  skip "V04 the database legs" "no docker on this machine; the CI leg brings one"
  result
  exit 0
fi
if ! command -v openssl >/dev/null 2>&1; then
  skip "V04 the database legs" "no openssl for a device certificate"
  result
  exit 0
fi

# =================================================================================================
# A Postgres 16 with the contract, and the plane against it
# =================================================================================================
banner "AP-3.6 — admission against a real state database (Postgres 16)"

SOCK="$SCRATCH/sock"
mkdir -p "$SOCK"
chmod 0777 "$SOCK"
PG="v04-budget-$$"
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
  fail "V04-0a the database is up" "postgres:16 did not become ready"
  result
  exit 1
fi
pass "V04-0a the database is up" "socket only, listen_addresses empty"

psql_c() { docker exec -i "$PG" psql -U postgres -h /sock -d workpod -qAt -v ON_ERROR_STOP=1 -c "$1" 2>&1; }

CERTDIR="$SCRATCH/device"
mkdir -p "$CERTDIR"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
  -keyout "$CERTDIR/device.key" -out "$CERTDIR/device.crt" -days 1 \
  -subj "/CN=acceptance device" >/dev/null 2>&1
CERT="$CERTDIR/device.crt"
IDENT="$("$BIN" adapter identity --device-cert "$CERT" 2>&1)"

STORE="$SCRATCH/attachments"
HALT_FILE="$SCRATCH/halt"
export WORKPOD_DB_DSN="host=$SOCK user=postgres dbname=workpod"
export WORKPOD_DB_MAINTENANCE_DSN="host=$SOCK user=postgres dbname=postgres"
export WORKPOD_SCHEMA="$SCHEMA"
export WORKPOD_HALT_FILE="$HALT_FILE"
CREDS="$SCRATCH/credentials"
mkdir -p "$CREDS"
printf 'all'             > "$CREDS/workpod.role"
printf 'probe-c1'        > "$CREDS/workpod.cell"
printf '127.0.0.1:8446'  > "$CREDS/workpod.control"
export CREDENTIALS_DIRECTORY="$CREDS"

start_plane() {
  "$BIN" control > "$SCRATCH/plane.log" 2>&1 &
  PLANE=$!
  for _ in $(seq 1 60); do
    grep -q "state database ready" "$SCRATCH/plane.log" && return 0
    kill -0 "$PLANE" 2>/dev/null || return 1
    sleep 1
  done
  return 1
}
stop_plane() {
  [ -n "$PLANE" ] && kill "$PLANE" 2>/dev/null
  wait "$PLANE" 2>/dev/null
  PLANE=""
}

if ! start_plane; then
  fail "V04-0b the plane loads the contract" "$(tail -3 "$SCRATCH/plane.log" | tr '\n' ' ')"
  result
  exit 1
fi
pass "V04-0b the plane loads the contract" "\`workpod control\` created the database and loaded contract/schema.sql"

# ---- fixtures ------------------------------------------------------------------------------------
# One cell, three principals — the device that submits over the CLI, and the heavy and light senders
# the share-out is measured on — and one project each.
PRIN='018f4242-0000-7000-8000-0000000000a1'
HEAVY='018f4242-0000-7000-8000-0000000000a2'
LIGHT='018f4242-0000-7000-8000-0000000000a3'
P='018f4242-0000-7000-8000-0000000000b1'
P2='018f4242-0000-7000-8000-0000000000b4'
PH='018f4242-0000-7000-8000-0000000000b2'
PL='018f4242-0000-7000-8000-0000000000b3'
FIXTURES="$(psql_c "
INSERT INTO cell (id, tenant, retention) VALUES ('probe-c1', 'probe', '{}');
INSERT INTO principal (id, cell, daily_money_cap_micros) VALUES
  ('$PRIN', 'probe-c1', 230400000), ('$HEAVY', 'probe-c1', 38400000), ('$LIGHT', 'probe-c1', 38400000);
INSERT INTO project (id, cell, principal) VALUES
  ('$P', 'probe-c1', '$PRIN'), ('$P2', 'probe-c1', '$PRIN'),
  ('$PH', 'probe-c1', '$HEAVY'), ('$PL', 'probe-c1', '$LIGHT');
INSERT INTO identity_link (external_id, principal, cell, confirmed_via, confirmed_at)
  VALUES ('$IDENT', '$PRIN', 'probe-c1', 'app', now());
INSERT INTO locality_group (id, cell) VALUES ('lg-probe', 'probe-c1');")"
if [ -z "$FIXTURES" ]; then
  pass "V04-0c the fixtures stand" "one cell, three principals with a daily money cap, four projects"
else
  fail "V04-0c the fixtures stand" "$FIXTURES"
  result
  exit 1
fi

# order PROJECT PRINCIPAL CHANNEL AUTHORITY POD_MINUTES TOKENS MONEY KEY — one job, stated in SQL.
#
# The adapter can only speak as the CLI channel, and half of what this script has to show is about
# other channels and other principals (SP-T01-8, SP-V04-4). So these jobs are written the way a
# second adapter would write them, through the same tables, and admitted by the same admission.
order() {
  local project="$1" principal="$2" channel="$3" authority="$4"
  local minutes="$5" tokens="$6" money="$7" key="$8"
  psql_c "
    WITH e AS (
      INSERT INTO envelope (id, cell, project, channel, channel_message_id, sender_external,
                            principal, authority, text_body, received_at, idempotency, purge_after)
      VALUES (gen_random_uuid(), 'probe-c1', '$project', '$channel', '$key', '$channel:sender',
              '$principal', '$authority', 'a job', now(), '$key', now() + interval '30 days')
      RETURNING id
    ), s AS (
      INSERT INTO spec (id, version, cell, project, envelope_id, goal, bounds, budget, risk_class)
      SELECT gen_random_uuid(), 1, 'probe-c1', '$project', e.id, 'a goal', '{}',
             '{\"pod_minutes\": $minutes, \"tokens\": $tokens, \"money_micros\": $money}',
             'reversible' FROM e
      RETURNING id
    ), a AS (
      INSERT INTO acceptance (id, cell, project, spec_id, spec_version, statement,
                              required_evidence, machine_checkable)
      SELECT gen_random_uuid(), 'probe-c1', '$project', s.id, 1, 'it passes', 'tests.new', true FROM s
    )
    INSERT INTO \"order\" (id, cell, project, spec_id, spec_version, class, platform, image_hash,
                           pipeline_version, authority_ref, budget_share, locality_group, idempotency)
    SELECT gen_random_uuid(), 'probe-c1', '$project', s.id, 1, 'small', 'alpine', 'sha256:probe',
           'v1', 'envelope:probe', '{}', 'lg-probe', '$channel:$key' FROM s
    RETURNING id;"
}

admit() { "$BIN" control admit --order "$1" 2>&1; }

# =================================================================================================
# AB-V04-1 — reservation at admission, release at the terminal state
# =================================================================================================
banner "V-04 — three pots, reserved in advance (AB-V04-1)"

O1="$(order "$P" "$PRIN" cli confidential 20 160000 800000 v04-1)"
STATE0="$(psql_c "SELECT state FROM \"order\" WHERE id = '$O1'")"
[ "$STATE0" = "new" ] && pass "V04-1d a job starts unadmitted" "state new, nothing reserved yet" \
                      || fail "V04-1d a job starts unadmitted" "$STATE0"

A1="$(admit "$O1")"
STATE1="$(psql_c "SELECT state FROM \"order\" WHERE id = '$O1'")"
if grep -q '"admitted": true' <<< "$A1" && [ "$STATE1" = "admitted" ]; then
  pass "V04-1e admission is the moment of reservation" "new -> admitted, written by control (K-02)"
else
  fail "V04-1e admission is the moment of reservation" "$(tr '\n' ' ' <<< "$A1") · state $STATE1"
fi

POTS_HELD="$(psql_c "
  SELECT string_agg(scope || ':' || pod_minutes_reserved || '/' || tokens_reserved || '/' ||
                    money_reserved_micros, ' ' ORDER BY scope)
    FROM budget_pot WHERE pod_minutes_reserved > 0")"
RES_ROWS="$(psql_c "SELECT count(*) FROM budget_reservation WHERE order_id = '$O1'")"
if [ "$RES_ROWS" = "4" ] && grep -q 'envelope:20/160000/800000' <<< "$POTS_HELD" \
   && grep -q 'principal_channel_day:20/160000/800000' <<< "$POTS_HELD"; then
  pass "V04-1f all three pots move, in all four scopes" "$POTS_HELD"
else
  fail "V04-1f all three pots move, in all four scopes" "$RES_ROWS reservations · $POTS_HELD"
fi

# The caps the pots were created with are OP-1's, per authority level — not a number in the program.
CAPS_HELD="$(psql_c "
  SELECT string_agg(scope || ':' || pod_minutes_cap, ' ' ORDER BY scope)
    FROM budget_pot WHERE authority = 'confidential'")"
if [ "$CAPS_HELD" = "envelope:120 principal_channel_day:2880 principal_day:5760 project:2880" ]; then
  pass "V04-1g the pots carry OP-1's caps" "$CAPS_HELD"
else
  fail "V04-1g the pots carry OP-1's caps" "$CAPS_HELD"
fi

# The release. The job spends 8 of its 20 pod minutes and then walks to a terminal state; what was
# not spent comes back, and what was spent stays counted (SP-V04-3).
"$BIN" control spend --order "$O1" --pod-minutes 8 --tokens 60000 --money-micros 300000 >/dev/null 2>&1
WALK="$(psql_c "
  SET LOCAL workpod.writer = 'control';
  UPDATE \"order\" SET state='queued' WHERE id='$O1';
  UPDATE \"order\" SET state='leased' WHERE id='$O1';
  SET LOCAL workpod.writer = 'worker';
  UPDATE \"order\" SET state='running' WHERE id='$O1';
  UPDATE \"order\" SET state='delivered', evidence='tests.new' WHERE id='$O1';")"
AFTER="$(psql_c "
  SELECT string_agg(scope || ':' || pod_minutes_reserved || '/' || tokens_reserved || '/' ||
                    money_reserved_micros, ' ' ORDER BY scope) FROM budget_pot")"
RELEASED="$(psql_c "SELECT count(*) FROM budget_reservation WHERE order_id='$O1' AND released_at IS NOT NULL")"
if [ "$RELEASED" = "4" ] && grep -q 'envelope:8/60000/300000' <<< "$AFTER" \
   && ! grep -q '20/160000' <<< "$AFTER"; then
  pass "V04-1h the unspent reservation is released" "8 of 20 pod minutes stay counted; the other 12 are free again"
else
  fail "V04-1h the unspent reservation is released" "$RELEASED released · $AFTER · ${WALK:-walk ok}"
fi

# =================================================================================================
# AB-T01-8 — the limit is in pod minutes, per principal and channel
# =================================================================================================
banner "T-01 — a limit in pod minutes, per principal and channel (AB-T01-8)"

# `public` on one channel: 60 pod minutes for the channel, 120 for the day, 60 for each project.
# Twelve jobs of five minutes are spread over two projects of the same principal, so no project pot
# is what fills — the channel is. The thirteenth is refused although its project and its day both
# still have room, which is exactly what SP-T01-8 asks the channel limit for.
ADMITTED_ON_CHANNEL=0
for i in $(seq 1 6); do
  Ta="$(order "$P"  "$PRIN" discord public 5 40000 200000 "t018-a-$i")"
  grep -q '"admitted": true' <<< "$(admit "$Ta")" && ADMITTED_ON_CHANNEL=$((ADMITTED_ON_CHANNEL+1))
  Tb="$(order "$P2" "$PRIN" discord public 5 40000 200000 "t018-b-$i")"
  grep -q '"admitted": true' <<< "$(admit "$Tb")" && ADMITTED_ON_CHANNEL=$((ADMITTED_ON_CHANNEL+1))
done
T3="$(order "$P" "$PRIN" discord public 5 40000 200000 t018-3)"
R3="$(admit "$T3")"
CHANNEL_POT="$(psql_c "SELECT pod_minutes_reserved || '/' || pod_minutes_cap FROM budget_pot
                        WHERE scope='principal_channel_day' AND channel='discord'")"
DAY_POT="$(psql_c "SELECT pod_minutes_reserved || '/' || pod_minutes_cap FROM budget_pot
                    WHERE scope='principal_day' AND authority='public'")"
PROJECT_POTS="$(psql_c "SELECT string_agg(pod_minutes_reserved || '/' || pod_minutes_cap, ' ')
                          FROM budget_pot WHERE scope='project' AND authority='public'")"
if grep -q '"pot": "principal_channel_day"' <<< "$R3" && grep -q '"resource": "pod_minutes"' <<< "$R3"; then
  pass "T01-8a the channel pot is what refuses" "channel $CHANNEL_POT · day $DAY_POT · projects $PROJECT_POTS"
else
  fail "T01-8a the channel pot is what refuses" "$(tr '\n' ' ' <<< "$R3") · channel $CHANNEL_POT"
fi

# The same principal, the same day, another channel: admitted. The limit is per channel, and one
# channel running hot does not take the day with it.
T4="$(order "$P" "$PRIN" email public 5 40000 200000 t018-4)"
R4="$(admit "$T4")"
if grep -q '"admitted": true' <<< "$R4"; then
  pass "T01-8b another channel still has its own pot" "one channel is half the day (OP-1)"
else
  fail "T01-8b another channel still has its own pot" "$(tr '\n' ' ' <<< "$R4")"
fi

# And it is minutes, not requests. Twelve requests passed on the channel that then refused the
# thirteenth, and what was full at that moment was its sixty minutes — a limit in requests would
# have had to refuse a number, and the number it refused at is the pod minutes.
if [ "$ADMITTED_ON_CHANNEL" = "12" ] && [ "$CHANNEL_POT" = "60/60" ]; then
  pass "T01-8c the limit counts minutes, not requests" "12 requests admitted, 60 of 60 pod minutes spent"
else
  fail "T01-8c the limit counts minutes, not requests" "$ADMITTED_ON_CHANNEL of 12 admitted · channel $CHANNEL_POT"
fi

# =================================================================================================
# AB-V04-2 — exhaustion answers with options, never with a silent truncation
# =================================================================================================
banner "V-04 — exhaustion is a reply with options (AB-V04-2, probe)"

# Through the real adapter, over the real wire: the reply a sender gets is what this row is about.
# The job asks for more tokens than a `confidential` envelope pot holds (960000 by OP-1).
ASKED=2000000
SUB="$("$BIN" adapter submit \
  --control 127.0.0.1:8446 --device-cert "$CERT" --store "$STORE" \
  --cell probe-c1 --project "$P" --message-id v04-2 \
  --text "rewrite everything" --goal "rewrite everything" \
  --acceptance "it still passes" --evidence tests.new \
  --risk reversible --class small \
  --image-hash sha256:probe --pipeline-version v1 --locality-group lg-probe \
  --budget-pod-minutes 10 --budget-tokens "$ASKED" --budget-money-micros 100 2>&1)"

if grep -q "not admitted" <<< "$SUB" && grep -q "budget.exhausted" <<< "$SUB" && grep -q "tokens" <<< "$SUB"; then
  pass "V04-2a the sender is told which pot ran out" "$(grep -m1 'not admitted' <<< "$SUB" | cut -c1-90)"
else
  fail "V04-2a the sender is told which pot ran out" "$(tr '\n' ' ' <<< "$SUB" | cut -c1-160)"
fi

OPTIONS="$(grep -c '·' <<< "$SUB")"
if [ "$OPTIONS" -ge 2 ] && grep -q "split the goal" <<< "$SUB"; then
  pass "V04-2b the reply carries options" "$OPTIONS of them, one of which is splitting the goal"
else
  fail "V04-2b the reply carries options" "$OPTIONS options · $(tr '\n' ' ' <<< "$SUB" | cut -c1-160)"
fi

# Not a truncation: the job was not quietly given fewer tokens and run anyway. The spec still says
# what was asked for, and the order never left `new`.
KEPT="$(psql_c "SELECT (s.budget->>'tokens')::bigint FROM spec s
                 JOIN envelope e ON e.id = s.envelope_id WHERE e.idempotency = 'v04-2'")"
STATE2="$(psql_c "SELECT o.state FROM \"order\" o JOIN spec s ON s.id=o.spec_id
                   JOIN envelope e ON e.id=s.envelope_id WHERE e.idempotency='v04-2'")"
if [ "$KEPT" = "$ASKED" ] && [ "$STATE2" = "new" ]; then
  pass "V04-2c nothing was silently truncated" "the spec still asks for $KEPT tokens; the order stayed new"
else
  fail "V04-2c nothing was silently truncated" "spec says $KEPT of $ASKED, state $STATE2"
fi

# The refusal is a decision, and B-03 records decisions — with the cause the state contract knows.
AUDITED="$(psql_c "SELECT detail->>'cause' FROM audit WHERE action='admission.refused'
                    ORDER BY at DESC LIMIT 1")"
if [ "$AUDITED" = "budget.exhausted" ]; then
  pass "V04-2d the refusal is recorded with its cause" "audit: admission.refused, cause budget.exhausted"
else
  fail "V04-2d the refusal is recorded with its cause" "${AUDITED:-no audit row}"
fi

# =================================================================================================
# AB-V04-4 — a heavy sender gets a lot, not everything
# =================================================================================================
banner "V-04 — the bottleneck, shared out by weighted shares (AB-V04-4, measurement)"

# What the earlier probes left waiting was refused for a reason that has not gone away, and a
# share-out is about who is competing now. They are closed with the cause they were refused with.
psql_c "SET LOCAL workpod.writer='control';
        UPDATE \"order\" SET state='cancelled', cause='budget.exhausted' WHERE state='new';" >/dev/null

for i in $(seq 1 12); do order "$PH" "$HEAVY" discord linked 10 80000 400000 "heavy-$i" >/dev/null; done
for i in $(seq 1 2);  do order "$PL" "$LIGHT" discord linked 10 80000 400000 "light-$i" >/dev/null; done

BOTTLENECK=60
SHARE="$("$BIN" control admit --cell probe-c1 --bottleneck "$BOTTLENECK" 2>&1)"
HEAVY_GOT="$(psql_c "SELECT coalesce(sum((s.budget->>'pod_minutes')::bigint),0) FROM \"order\" o
                      JOIN spec s ON s.id=o.spec_id AND s.version=o.spec_version
                     WHERE o.project='$PH' AND o.state='admitted'")"
LIGHT_GOT="$(psql_c "SELECT coalesce(sum((s.budget->>'pod_minutes')::bigint),0) FROM \"order\" o
                      JOIN spec s ON s.id=o.spec_id AND s.version=o.spec_version
                     WHERE o.project='$PL' AND o.state='admitted'")"
measure "V04-4 the share-out" "bottleneck $BOTTLENECK pod minutes · heavy asked 120 and got $HEAVY_GOT · light asked 20 and got $LIGHT_GOT"

if [ "$HEAVY_GOT" -gt "$LIGHT_GOT" ] && [ "$HEAVY_GOT" -lt 120 ] && [ "$HEAVY_GOT" -le "$BOTTLENECK" ]; then
  pass "V04-4a the heavy sender gets a lot, not everything" "$HEAVY_GOT of the 120 pod minutes it asked for"
else
  fail "V04-4a the heavy sender gets a lot, not everything" "$HEAVY_GOT of 120, bottleneck $BOTTLENECK"
fi
if [ "$LIGHT_GOT" = "20" ]; then
  pass "V04-4b the light sender is not starved behind it" "20 of 20, in full, while the heavy one waits"
else
  fail "V04-4b the light sender is not starved behind it" "$LIGHT_GOT of 20"
fi
if [ "$((HEAVY_GOT + LIGHT_GOT))" -le "$BOTTLENECK" ]; then
  pass "V04-4c the bottleneck is not overbooked" "$((HEAVY_GOT + LIGHT_GOT)) of $BOTTLENECK pod minutes handed out"
else
  fail "V04-4c the bottleneck is not overbooked" "$((HEAVY_GOT + LIGHT_GOT)) of $BOTTLENECK"
fi

# =================================================================================================
# AB-E08-3 — the halt takes effect through the file
# =================================================================================================
banner "E-08 — the halt with two paths (AB-E08-3, probe)"

HALT_ROWS="$(psql_c "SELECT count(*) FROM halt")"
"$BIN" control halt --set --reason "the model provider answers nonsense" --by "the duty officer" >/dev/null

# With the API up: the file alone stops admission, and the database says nothing at all.
H1="$(order "$P" "$PRIN" cli confidential 5 40000 200000 e083-1)"
RH1="$(admit "$H1")"
STATE_H1="$(psql_c "SELECT state FROM \"order\" WHERE id='$H1'")"
if grep -q '"halt_source": "file"' <<< "$RH1" && [ "$STATE_H1" = "new" ] && [ "$HALT_ROWS" = "0" ]; then
  pass "E08-3c the file halts while the API is up" "no row in halt; the file is what refused"
else
  fail "E08-3c the file halts while the API is up" "$(tr '\n' ' ' <<< "$RH1") · state $STATE_H1 · $HALT_ROWS halt rows"
fi

# A job that is already running is not touched: it runs to completion (SP-V04-2).
RUN="$(order "$P" "$PRIN" cli confidential 5 40000 200000 e083-running)"
psql_c "SET LOCAL workpod.writer='control';
        UPDATE \"order\" SET state='admitted' WHERE id='$RUN';
        UPDATE \"order\" SET state='queued'   WHERE id='$RUN';
        UPDATE \"order\" SET state='leased'   WHERE id='$RUN';
        SET LOCAL workpod.writer='worker';
        UPDATE \"order\" SET state='running'  WHERE id='$RUN';" >/dev/null

# Now the API is switched off — the case the second path exists for.
stop_plane
if "$BIN" adapter submit --control 127.0.0.1:8446 --device-cert "$CERT" --store "$STORE" \
     --cell probe-c1 --project "$P" --message-id e083-api-off --text x --goal x \
     --acceptance x --evidence tests.new --risk reversible --class small \
     --image-hash sha256:probe --pipeline-version v1 --locality-group lg-probe \
     --budget-pod-minutes 5 --budget-tokens 40000 --budget-money-micros 200000 >/dev/null 2>&1; then
  fail "E08-3d the API is off" "the plane answered after it was stopped"
else
  pass "E08-3d the API is off" "no plane on 127.0.0.1:8446 — the field in admission is unreachable"
fi

H2="$(order "$P" "$PRIN" cli confidential 5 40000 200000 e083-2)"
RH2="$(admit "$H2")"
STATE_H2="$(psql_c "SELECT state FROM \"order\" WHERE id='$H2'")"
if grep -q '"admitted": false' <<< "$RH2" && grep -q 'halt' <<< "$RH2" && [ "$STATE_H2" = "new" ]; then
  pass "E08-3e with the API off, nothing is admitted" "the halt file decided, without the plane"
else
  fail "E08-3e with the API off, nothing is admitted" "$(tr '\n' ' ' <<< "$RH2") · state $STATE_H2"
fi

FINISH="$(psql_c "SET LOCAL workpod.writer='worker';
                  UPDATE \"order\" SET state='delivered', evidence='tests.new' WHERE id='$RUN';")"
STATE_RUN="$(psql_c "SELECT state FROM \"order\" WHERE id='$RUN'")"
if [ "$STATE_RUN" = "delivered" ]; then
  pass "E08-3f running jobs run to completion" "the halt stops admission, not work (SP-V04-2)"
else
  fail "E08-3f running jobs run to completion" "$STATE_RUN · ${FINISH:-ok}"
fi

# The expiry is mandatory: a halt nobody renewed for 60 minutes stops nothing (SP-E08-4).
printf 'reason: set an hour and a half ago\nset_by: the duty officer\nset_at: %s\n' \
  "$(date -u -d '90 minutes ago' +%Y-%m-%dT%H:%M:%SZ)" > "$HALT_FILE"
touch -d '90 minutes ago' "$HALT_FILE"
RH3="$(admit "$H2")"
if grep -q '"admitted": true' <<< "$RH3"; then
  pass "E08-3g an unrenewed halt expires by itself" "60 minutes, and the cell decides again (SP-E08-4)"
else
  fail "E08-3g an unrenewed halt expires by itself" "$(tr '\n' ' ' <<< "$RH3")"
fi

# And clearing it is deleting the file.
"$BIN" control halt --set --reason "again" --by "the duty officer" >/dev/null
H4="$(order "$P" "$PRIN" cli confidential 5 40000 200000 e083-4)"
grep -q '"admitted": false' <<< "$(admit "$H4")" || fail "E08-3h the halt can be set again" "it did not refuse"
"$BIN" control halt --clear >/dev/null
RH4="$(admit "$H4")"
if grep -q '"admitted": true' <<< "$RH4"; then
  pass "E08-3h set, cleared, and the cell admits again" "the file is the state; deleting it is halt.clear"
else
  fail "E08-3h set, cleared, and the cell admits again" "$(tr '\n' ' ' <<< "$RH4")"
fi

result
