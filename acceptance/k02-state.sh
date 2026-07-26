#!/usr/bin/env bash
# k02-state.sh — the state machine as a database contract, probed in a database (AP-2.3).
#
# Four rows rest on this script:
#   AB-K02-1  one field, one writer — a worker's write on queued -> leased fails in the database (probe)
#   AB-K02-2  attempt as the unit — a retry produces a new attempt, not a new job
#   AB-K02-3  no terminal state without a cause — failed without cause is rejected (probe)
#   AB-K02-5  no backward transitions — a transition out of a terminal state is rejected (probe)
#
# Everything here runs against a Postgres 16 loaded with contract/schema.sql, because the claim
# under test is about the database and not about any application: the probes speak raw SQL, there
# is no application in the loop, and each probe names the guard it expects — the trigger, a check
# constraint, a unique key — so a refusal for the wrong reason cannot pass as the right one.
# Controls prove the lawful action passes, so a database that refuses everything cannot pass as
# one that enforces the rule.
#
# OP-4's leg: the ruling in decisions/OP-4.md and the lease_parameter rows the schema seeds are
# held against each other — a number in one that is not in the other is drift.
#
# The stage 2 boundary holds: granting a lease is AP-6.2's work. Here only the contract about who
# may write, exercised with fixture rows a producer could have written.
#
# Exit:  0 = no FAIL
#        1 = at least one FAIL

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SQL="$ROOT/contract/schema.sql"
OP4="$ROOT/decisions/OP-4.md"

SCRATCH="$(mktemp -d)"
PG=""
cleanup() {
  rm -rf "$SCRATCH"
  [ -n "$PG" ] && docker rm -f "$PG" >/dev/null 2>&1
}
trap cleanup EXIT

PASS=0; FAIL=0; SKIP=0

pass() { printf '  \033[32mPASS\033[0m  %-52s %s\n' "$1" "${2:-}"; PASS=$((PASS+1)); }
fail() { printf '  \033[31mFAIL\033[0m  %-52s %s\n' "$1" "${2:-}"; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m  %-52s %s\n' "$1" "${2:-}"; SKIP=$((SKIP+1)); }
banner() { printf '\n\033[1m%s\033[0m\n' "$1"; }

if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  banner "K-02 — the state machine in a database"
  skip "K02 all checks" "no docker on this machine; the CI leg brings one"
  banner "Result"
  printf '  %d PASS · %d FAIL · %d SKIP\n\n' "$PASS" "$FAIL" "$SKIP"
  exit 0
fi

# --------------------------------------------------------------------------
# A Postgres 16 with the contract loaded.
# --------------------------------------------------------------------------
banner "K-02 — the contract in a database (Postgres 16)"

PG="k02-state-$$"
docker run --rm -d --name "$PG" -e POSTGRES_PASSWORD=acceptance postgres:16 >/dev/null

query() { docker exec -i "$PG" psql -U postgres -qAt -v ON_ERROR_STOP=1 -c "$1" 2>&1; }

# The entrypoint starts a temporary server during initdb and restarts it; one answered query is
# not readiness. Ready means two answered queries a second apart.
READY=""
for _ in $(seq 1 90); do
  if query "SELECT 1" >/dev/null; then
    sleep 1
    query "SELECT 1" >/dev/null && { READY=1; break; }
  fi
  sleep 1
done

if [ -z "$READY" ]; then
  fail "K02-0a the schema loads" "postgres:16 did not become ready"
elif docker exec -i "$PG" psql -U postgres -q -v ON_ERROR_STOP=1 -f - < "$SQL" > "$SCRATCH/load.out" 2>&1; then
  pass "K02-0a the schema loads" "postgres:16, ON_ERROR_STOP"
else
  fail "K02-0a the schema loads" "$(head -1 "$SCRATCH/load.out")"
fi
if [ "$FAIL" -gt 0 ]; then
  banner "Result"
  printf '  %d PASS · %d FAIL · %d SKIP\n\n' "$PASS" "$FAIL" "$SKIP"
  exit 1
fi

# run_sql [WRITER] — one transaction over stdin; WRITER '' claims nothing, exactly like a client
# that never set workpod.writer.
run_sql() {
  local writer="${1:-}"
  { echo "BEGIN;"
    [ -n "$writer" ] && echo "SET LOCAL workpod.writer = '$writer';"
    cat
    echo "COMMIT;"
  } | docker exec -i "$PG" psql -U postgres -qAt -v ON_ERROR_STOP=1 -f - 2>&1
}

# refused LABEL WRITER GUARD — the SQL on stdin must fail, and fail on the named guard. A refusal
# for another reason is a broken fixture wearing the colour of enforcement.
refused() {
  local label="$1" writer="$2" guard="$3" out
  if out=$(run_sql "$writer"); then
    fail "$label" "the database accepted it"
  elif printf '%s\n' "$out" | grep -q "$guard"; then
    pass "$label" "$(printf '%s\n' "$out" | grep -m1 "$guard" | sed 's/^.*ERROR: *//')"
  else
    fail "$label" "refused, but not by the guard: $(printf '%s\n' "$out" | head -1)"
  fi
}

# accepted LABEL WRITER DETAIL — the SQL on stdin must pass.
accepted() {
  local label="$1" writer="$2" detail="${3:-}" out
  if out=$(run_sql "$writer"); then
    pass "$label" "$detail"
  else
    fail "$label" "$(printf '%s\n' "$out" | head -1)"
  fi
}

# Fixtures a producer could have written: one cell, one project, one spec, five orders. States
# start at 'new' and are walked through the contract, never assumed. Inserts are not transitions,
# so the trigger does not guard them — which is the K-02 rule, not a gap: two writers on one field
# is an update problem.
P='018f4242-0000-7000-8000-00000000000b'
run_sql '' > "$SCRATCH/fixtures.out" <<SQL
INSERT INTO cell (id, tenant, retention) VALUES ('probe-c1', 'probe', '{}');
INSERT INTO principal (id, cell, daily_money_cap_micros)
  VALUES ('018f4242-0000-7000-8000-00000000000a', 'probe-c1', 0);
INSERT INTO project (id, cell, principal)
  VALUES ('$P', 'probe-c1', '018f4242-0000-7000-8000-00000000000a');
INSERT INTO envelope (id, cell, project, channel, channel_message_id, sender_external, authority,
                      text_body, received_at, idempotency, purge_after)
  VALUES ('018f4242-0000-7000-8000-00000000000c', 'probe-c1', '$P', 'cli', 'm-1', 'probe',
          'confidential', 'probe', now(), 'idem-env', now() + interval '30 days');
INSERT INTO spec (id, version, cell, project, envelope_id, goal, bounds, budget, risk_class)
  VALUES ('018f4242-0000-7000-8000-00000000000d', 1, 'probe-c1', '$P',
          '018f4242-0000-7000-8000-00000000000c', 'probe', '{}', '{}', 'reversible');
INSERT INTO "order" (id, cell, project, spec_id, spec_version, class, platform, image_hash,
                     pipeline_version, authority_ref, budget_share, locality_group, idempotency)
SELECT id, 'probe-c1', '$P', '018f4242-0000-7000-8000-00000000000d', 1, 'small', 'alpine',
       'sha256:probe', 'v1', 'ref:probe', '{}', 'lg-probe', idem
FROM (VALUES ('018f4242-0000-7000-8000-000000000001'::uuid, 'idem-A'),
             ('018f4242-0000-7000-8000-000000000002', 'idem-B'),
             ('018f4242-0000-7000-8000-000000000003', 'idem-C'),
             ('018f4242-0000-7000-8000-000000000004', 'idem-D'),
             ('018f4242-0000-7000-8000-000000000005', 'idem-E')) AS f(id, idem);
SQL
if [ $? -eq 0 ]; then
  pass "K02-0b the fixtures stand" "one project, five orders, all in state 'new'"
else
  fail "K02-0b the fixtures stand" "$(head -1 "$SCRATCH/fixtures.out")"
fi

A='018f4242-0000-7000-8000-000000000001'
B='018f4242-0000-7000-8000-000000000002'
C='018f4242-0000-7000-8000-000000000003'
D='018f4242-0000-7000-8000-000000000004'
E='018f4242-0000-7000-8000-000000000005'

# to_state ORDER STATE... — walk an order along lawful control/worker transitions.
walk() {
  local id="$1"; shift
  for step in "$@"; do
    run_sql "${step%%:*}" <<SQL
UPDATE "order" SET state = '${step#*:}' WHERE id = '$id';
SQL
  done
}

state_of() { query "SELECT state FROM \"order\" WHERE id = '$1'"; }

# --------------------------------------------------------------------------
# AB-K02-1 — one field, one writer: the wrong writer fails in the database
# --------------------------------------------------------------------------
banner "K-02 — one field, one writer (AB-K02-1, probe)"

accepted "K02-1a the lawful walk passes" control "control writes new -> admitted -> queued" <<SQL
UPDATE "order" SET state = 'admitted' WHERE id = '$A';
UPDATE "order" SET state = 'queued'   WHERE id = '$A';
SQL

# The done-when of AP-2.3: this UPDATE is raw SQL, there is no application in the loop, and the
# refusal comes from the trigger.
refused "K02-1b the worker on queued -> leased is refused" worker "written exclusively by control" <<SQL
UPDATE "order" SET state = 'leased' WHERE id = '$A';
SQL

if [ "$(state_of "$A")" = "queued" ]; then
  pass "K02-1c the refused write left no mark" "order A still stands at 'queued'"
else
  fail "K02-1c the refused write left no mark" "order A is '$(state_of "$A")'"
fi

refused "K02-1d a write claiming no writer is refused" "" "not by <not set>" <<SQL
UPDATE "order" SET state = 'leased' WHERE id = '$A';
SQL

refused "K02-1e a transition outside the table is refused" control "does not exist" <<SQL
UPDATE "order" SET state = 'running' WHERE id = '$A';
SQL

accepted "K02-1f the control plane leases" control "queued -> leased, the same field, the ruled writer" <<SQL
UPDATE "order" SET state = 'leased' WHERE id = '$A';
INSERT INTO attempt (order_id, attempt, cell, project) VALUES ('$A', 1, 'probe-c1', '$P');
SQL

# The transition table carries SP-K02-1's rows verbatim: six named transitions, and cancelled
# from every non-terminal state, written by control.
MISSING=()
while IFS='|' read -r f t w; do
  [ -n "$f" ] || continue
  [ "$(query "SELECT count(*) FROM state_transition
              WHERE from_state='$f' AND to_state='$t' AND writer='$w'")" = "1" ] || MISSING+=("$f->$t:$w")
done <<'ROWS'
queued|leased|control
leased|queued|control
running|frozen|worker
running|awaiting_reply|worker
running|delivered|worker
running|unproven|worker
new|cancelled|control
admitted|cancelled|control
queued|cancelled|control
leased|cancelled|control
running|cancelled|control
frozen|cancelled|control
awaiting_reply|cancelled|control
ROWS
if [ ${#MISSING[@]} -eq 0 ]; then
  pass "K02-1g the table carries SP-K02-1's rows" "six named transitions; any -> cancelled by control"
else
  fail "K02-1g the table carries SP-K02-1's rows" "missing: ${MISSING[*]}"
fi

# --------------------------------------------------------------------------
# AB-K02-2 — attempt as the unit: a retry is a new attempt, not a new job
# --------------------------------------------------------------------------
banner "K-02 — the attempt, not the job (AB-K02-2)"

walk "$B" control:admitted control:queued > /dev/null
accepted "K02-2a the first attempt is leased" control "order B: queued -> leased, attempt 1" <<SQL
UPDATE "order" SET state = 'leased' WHERE id = '$B';
INSERT INTO attempt (order_id, attempt, cell, project) VALUES ('$B', 1, 'probe-c1', '$P');
SQL

# The deadline expired and no heartbeat came (OP-4): the control plane takes the job back. Same
# order id, same idempotency key, new attempt counter — the retry in SP-K02-2's words.
accepted "K02-2b the deadline path retries" control "leased -> queued, attempt 1 ends, attempt 2 begins" <<SQL
UPDATE "order" SET state = 'queued', attempt = 2 WHERE id = '$B';
UPDATE attempt SET ended_at = now(), cause = 'tool.failure' WHERE order_id = '$B' AND attempt = 1;
INSERT INTO attempt (order_id, attempt, cell, project) VALUES ('$B', 2, 'probe-c1', '$P');
SQL

JOBS=$(query "SELECT count(*) FROM \"order\" WHERE project = '$P' AND idempotency = 'idem-B'")
TRIES=$(query "SELECT count(*) FROM attempt WHERE order_id = '$B'")
if [ "$JOBS" = "1" ] && [ "$TRIES" = "2" ]; then
  pass "K02-2c one job, two attempts" "the retry produced a new attempt, never a new order row"
else
  fail "K02-2c one job, two attempts" "orders: $JOBS, attempts: $TRIES"
fi

refused "K02-2d the retry cannot arrive as a new job" control "order_project_idempotency_attempt_key" <<SQL
INSERT INTO "order" (id, cell, project, spec_id, spec_version, class, platform, image_hash,
                     pipeline_version, authority_ref, budget_share, locality_group,
                     attempt, idempotency)
VALUES ('018f4242-0000-7000-8000-000000000006', 'probe-c1', '$P',
        '018f4242-0000-7000-8000-00000000000d', 1, 'small', 'alpine', 'sha256:probe', 'v1',
        'ref:probe', '{}', 'lg-probe', 2, 'idem-B');
SQL

FIRST=$(query "SELECT cause FROM attempt WHERE order_id = '$B' AND attempt = 1")
LIVE=$(query "SELECT state || '/' || coalesce(cause::text, '-') FROM \"order\" WHERE id = '$B'")
if [ "$FIRST" = "tool.failure" ] && [ "$LIVE" = "queued/-" ]; then
  pass "K02-2e each attempt keeps its own result" "attempt 1 ended with tool.failure; the order runs on, cause empty"
else
  fail "K02-2e each attempt keeps its own result" "attempt 1: '$FIRST', order: '$LIVE'"
fi

# --------------------------------------------------------------------------
# AB-K02-3 — no terminal state without a cause
# --------------------------------------------------------------------------
banner "K-02 — no terminal state without a cause (AB-K02-3, probe)"

walk "$C" control:admitted control:queued control:leased worker:running > /dev/null
walk "$D" control:admitted control:queued control:leased worker:running > /dev/null

refused "K02-3a failed without a cause is refused" worker "end_state_needs_cause" <<SQL
UPDATE "order" SET state = 'failed' WHERE id = '$C';
SQL

refused "K02-3b unproven without a cause is refused" worker "end_state_needs_cause" <<SQL
UPDATE "order" SET state = 'unproven' WHERE id = '$D';
SQL

refused "K02-3c cancelled without a cause is refused" control "end_state_needs_cause" <<SQL
UPDATE "order" SET state = 'cancelled' WHERE id = '$E';
SQL

accepted "K02-3d failed with a cause passes" worker "order C: running -> failed, cause tool.failure" <<SQL
UPDATE "order" SET state = 'failed', cause = 'tool.failure' WHERE id = '$C';
SQL

accepted "K02-3e cancelled with a cause passes" control "order E: new -> cancelled, cause budget.exhausted" <<SQL
UPDATE "order" SET state = 'cancelled', cause = 'budget.exhausted' WHERE id = '$E';
SQL

# The cause set is SP-K02-3's, from Q-03 and F-04 — exactly, in both directions: a missing key
# breaks the evaluation by kind of failure, a stray key is an invented architecture.
SPEC_SET="assumption.replicated budget.exhausted context.missing fact.invented goal.wrong \
injection knowledge.missing regression.silent skill.missing spec.wrong tool.failure unsolvable"
DB_SET=$(query "SELECT string_agg(enumlabel, ' ' ORDER BY enumlabel)
                FROM pg_enum e JOIN pg_type t ON e.enumtypid = t.oid
                WHERE t.typname = 'cause_code'")
if [ "$DB_SET" = "$(echo $SPEC_SET)" ]; then
  pass "K02-3f the cause set is Q-03 plus F-04" "twelve keys, none missing, none invented"
else
  fail "K02-3f the cause set is Q-03 plus F-04" "database: $DB_SET"
fi

# --------------------------------------------------------------------------
# AB-K02-5 — no way back out of a terminal state
# --------------------------------------------------------------------------
banner "K-02 — no backward transitions (AB-K02-5, probe)"

refused "K02-5a failed -> queued is refused" control "cannot be left" <<SQL
UPDATE "order" SET state = 'queued', cause = NULL WHERE id = '$C';
SQL

refused "K02-5b failed -> running is refused" worker "cannot be left" <<SQL
UPDATE "order" SET state = 'running' WHERE id = '$C';
SQL

refused "K02-5c cancelled -> queued is refused" control "cannot be left" <<SQL
UPDATE "order" SET state = 'queued', cause = NULL WHERE id = '$E';
SQL

accepted "K02-5d delivered stands as its own exit" worker "order D: running -> delivered, evidence tests.new" <<SQL
UPDATE "order" SET state = 'delivered', evidence = 'tests.new' WHERE id = '$D';
SQL

refused "K02-5e delivered -> running is refused" worker "cannot be left" <<SQL
UPDATE "order" SET state = 'running' WHERE id = '$D';
SQL

EXITS=$(query "SELECT count(*) FROM state_transition
               WHERE from_state IN ('delivered','unproven','failed','cancelled')")
if [ "$EXITS" = "0" ]; then
  pass "K02-5f the table offers no exit from a terminal state" "a fresh attempt is a new job (SP-K02-5)"
else
  fail "K02-5f the table offers no exit from a terminal state" "$EXITS transitions leave one"
fi

# --------------------------------------------------------------------------
# OP-4 — the ruling and the seed are the same numbers
# --------------------------------------------------------------------------
banner "OP-4 — lease and heartbeat parameters (decisions/OP-4.md)"

# The ruling's table names each parameter in backticks; the first number of the value column is
# the ruled value. The seed in contract/schema.sql uses the same names, so this is a join, not a
# heuristic.
ruled() {
  awk -F'|' -v name="$1" '$2 ~ ("`" name "`") { match($3, /[0-9]+/)
    print substr($3, RSTART, RLENGTH); exit }' "$OP4"
}

DRIFT=()
for p in lease_duration_seconds heartbeat_interval_seconds failures_to_release; do
  WANT=$(ruled "$p")
  HAVE=$(query "SELECT value FROM lease_parameter WHERE name = '$p'")
  [ -n "$WANT" ] && [ "$WANT" = "$HAVE" ] || DRIFT+=("$p: ruled '${WANT:-nothing}', seeded '$HAVE'")
done
ROWS=$(query "SELECT count(*) FROM lease_parameter")
if [ ${#DRIFT[@]} -eq 0 ] && [ "$ROWS" = "3" ]; then
  pass "OP4-a the seed carries the ruling" "lease 60 s, heartbeat 15 s, 3 failures = release; 3 rows, no extras"
else
  fail "OP4-a the seed carries the ruling" "${DRIFT[*]:-} rows: $ROWS"
fi

# --------------------------------------------------------------------------
banner "Result"
printf '  %d PASS · %d FAIL · %d SKIP\n\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
