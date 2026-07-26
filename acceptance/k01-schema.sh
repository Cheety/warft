#!/usr/bin/env bash
# k01-schema.sh — the state schema, checked by a machine (AP-2.2).
#
# Six rows rest on this script:
#   AB-K01-1  three objects, three lifetimes — fields complete per the K-01 table
#   AB-K01-2  UUID v7 from the producer — no central counter in the schema (probe)
#   AB-K01-3  cell identifier everywhere — cell is NOT NULL on every table
#   AB-K01-4  project reference everywhere — project is NOT NULL on every table
#   AB-K01-5  no secrets — the schema checker finds no secret column (probe)
#   AB-V05-2  additive migration — a removing migration is rejected (probe)
#
# The probes mutate a scratch copy of the contract and expect schema-additive.py to reject it —
# and control probes expect the unchanged contract to be accepted and an addition to pass, so a
# checker that rejects everything cannot pass. AB-K01-3 and AB-K01-4 are scripts, not probes, but
# each still gets one bite probe: a rule nothing has ever violated is indistinguishable from no
# rule.
#
# The exemption lists for cell and project live in schema-additive.py, closed and with the
# requirement that forces each entry; this script prints them rather than restating them, and the
# database leg below checks the loaded schema against the same list.
#
# The stage 2 boundary holds here: these are properties of the contract, not of an implementation.
# No queue logic, no scheduler — structure and rules.
#
# Exit:  0 = no FAIL
#        1 = at least one FAIL

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SQL="$ROOT/contract/schema.sql"
TOOL="$ROOT/acceptance/schema-additive.py"

SCRATCH="$(mktemp -d)"
PG=""
cleanup() {
  rm -rf "$SCRATCH"
  [ -n "$PG" ] && docker rm -f "$PG" >/dev/null 2>&1
}
trap cleanup EXIT

PASS=0; FAIL=0; SKIP=0

pass() { printf '  \033[32mPASS\033[0m  %-46s %s\n' "$1" "${2:-}"; PASS=$((PASS+1)); }
fail() { printf '  \033[31mFAIL\033[0m  %-46s %s\n' "$1" "${2:-}"; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m  %-46s %s\n' "$1" "${2:-}"; SKIP=$((SKIP+1)); }
note() { printf '        %s\n' "$1"; }
banner() { printf '\n\033[1m%s\033[0m\n' "$1"; }

# --------------------------------------------------------------------------
# The instrument itself: parse once, and the unchanged contract must pass —
# the control every probe below leans on.
# --------------------------------------------------------------------------
banner "K-01 — the checker and the parse"

DUMP="$SCRATCH/dump.tsv"
if "$TOOL" dump "$SQL" > "$DUMP"; then
  pass "K01-0a the schema parses" \
       "$(grep -c $'^table\t' "$DUMP") tables, $(grep -c $'^column\t' "$DUMP") columns"
else
  fail "K01-0a the schema parses" "schema-additive.py rejects it"
fi

if "$TOOL" check "$SQL" > "$SCRATCH/check.out" 2>&1; then
  pass "K01-0b the checker accepts the contract" "$(head -1 "$SCRATCH/check.out")"
else
  fail "K01-0b the checker accepts the contract" "$(head -1 "$SCRATCH/check.out")"
fi

# --------------------------------------------------------------------------
# AB-K01-1 — three objects, three lifetimes, fields complete per the table
# --------------------------------------------------------------------------
banner "K-01 — three objects, three lifetimes (AB-K01-1)"

# fields LABEL TABLE DETAIL FIELD... — every field must be a column of the table.
fields() {
  local label="$1" tbl="$2" detail="$3"; shift 3
  local missing=()
  for f in "$@"; do
    grep -q $'^column\t'"$tbl"$'\t'"$f"$'\t' "$DUMP" || missing+=("$f")
  done
  if [ ${#missing[@]} -eq 0 ]; then
    pass "$label" "$detail"
  else
    fail "$label" "missing: ${missing[*]}"
  fi
}

fields "K01-1a envelope carries its twelve fields" envelope \
  "text -> text_body; attachments[] -> attachments, content hashes" \
  id cell channel channel_message_id sender_external principal authority text_body \
  attachments thread received_at idempotency

if grep -q $'^column\tenvelope\tprincipal\tuuid\tnullable\t' "$DUMP"; then
  pass "K01-1b principal may be empty" "first contact produces an invitation, not an attribution"
else
  fail "K01-1b principal may be empty" "envelope.principal is not a nullable uuid"
fi

fields "K01-1c spec carries its ten fields" spec \
  "acceptance[] and assumptions[] are objects of their own (SP-Q01)" \
  id version cell project goal bounds budget risk_class
for child in acceptance assumption; do
  fields "K01-1c   $child keyed by spec@version" "$child" "spec_id + spec_version" \
    spec_id spec_version
done

ORDER_MISSING=()
for f in id cell project spec_id spec_version parent class platform image_hash \
         pipeline_version authority_ref budget_share locality_group state attempt idempotency; do
  grep -q $'^column\torder\t'"$f"$'\t' "$DUMP" || ORDER_MISSING+=("$f")
done
if [ ${#ORDER_MISSING[@]} -eq 0 ]; then
  pass "K01-1d order carries its fifteen fields" \
       "spec@version -> spec_id+spec_version; authority -> authority_ref, a reference (SP-K01-5)"
else
  fail "K01-1d order carries its fifteen fields" "missing: ${ORDER_MISSING[*]}"
fi

if grep -q $'^column\tenvelope\tpurge_after\t' "$DUMP" \
   && grep -q $'^pk\tspec\tid,version$' "$DUMP" \
   && grep -q $'^column\torder\tcreated_at\t' "$DUMP" \
   && grep -q $'^column\torder\tupdated_at\t' "$DUMP"; then
  pass "K01-1e three lifetimes stand in the fields" \
       "purge_after (minutes) / pk (id,version), never overwritten (months) / created_at,updated_at"
else
  fail "K01-1e three lifetimes stand in the fields"
fi

MISSING=()
for t in envelope spec order principal identity_link project attempt lease outbox receipt \
         judgment budget_pot skill_version container_image pipeline_version node \
         locality_group audit halt; do
  grep -q $'^table\t'"$t"'$' "$DUMP" || MISSING+=("$t")
done
if [ ${#MISSING[@]} -eq 0 ]; then
  pass "K01-1f every object of SP-K01-8 has a table" "16 derived objects, and the three above"
else
  fail "K01-1f every object of SP-K01-8 has a table" "missing: ${MISSING[*]}"
fi

# --------------------------------------------------------------------------
# Probe helpers. A probe that does not change the file proves nothing, so
# that is a FAIL (the e10-schema.sh pattern).
# --------------------------------------------------------------------------

# mutate SED_EXPR OUT — sed over the contract into the scratch copy.
mutate() {
  sed "$1" "$SQL" > "$SCRATCH/$2"
  ! cmp -s "$SQL" "$SCRATCH/$2"
}

# append SQL_LINE OUT — the contract plus one statement.
append() {
  { cat "$SQL"; echo "$1"; } > "$SCRATCH/$2"
}

# rejected_check LABEL FILE — check mode must exit non-zero.
rejected_check() {
  if "$TOOL" check "$2" > "$SCRATCH/lint.out" 2>&1; then
    fail "$1" "the checker accepted it"
  else
    pass "$1" "$(head -1 "$SCRATCH/lint.out")"
  fi
}

# rejected_compare LABEL OLD NEW — compare mode must exit non-zero.
rejected_compare() {
  if "$TOOL" "$2" "$3" > "$SCRATCH/lint.out" 2>&1; then
    fail "$1" "the tool accepted it"
  else
    pass "$1" "$(head -1 "$SCRATCH/lint.out")"
  fi
}

# --------------------------------------------------------------------------
# AB-K01-2 — UUID v7 from the producer: no central counter (probe)
# --------------------------------------------------------------------------
banner "K-01 — no central counter (AB-K01-2, probe)"

append "CREATE TABLE probe_ticket (id bigserial PRIMARY KEY, cell text NOT NULL REFERENCES cell(id), project uuid NOT NULL REFERENCES project(id));" serial.sql
rejected_check "K01-2a a serial column is rejected" "$SCRATCH/serial.sql"

append "CREATE TABLE probe_ticket (id bigint GENERATED ALWAYS AS IDENTITY, cell text NOT NULL, project uuid NOT NULL);" identity.sql
rejected_check "K01-2b an identity column is rejected" "$SCRATCH/identity.sql"

append "CREATE SEQUENCE probe_seq;" sequence.sql
rejected_check "K01-2c a sequence is rejected" "$SCRATCH/sequence.sql"

append "CREATE TABLE probe_ticket (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), cell text NOT NULL, project uuid NOT NULL);" dbuuid.sql
rejected_check "K01-2d a database-assigned id is rejected" "$SCRATCH/dbuuid.sql"

DEFAULTED_IDS=$(awk -F'\t' '$1=="column" && $3=="id" && $6!="-"' "$DUMP")
if [ -z "$DEFAULTED_IDS" ]; then
  pass "K01-2e identifiers carry no database default" "the producer assigns them, sortable UUID v7"
else
  fail "K01-2e identifiers carry no database default" "$(echo "$DEFAULTED_IDS" | head -1)"
fi

# --------------------------------------------------------------------------
# AB-K01-3 / AB-K01-4 — cell and project NOT NULL on every table
# --------------------------------------------------------------------------

# notnull_everywhere LABEL COLUMN — every table carries COLUMN NOT NULL or stands on the
# closed exemption list in schema-additive.py.
notnull_everywhere() {
  local label="$1" col="$2"
  local exempt carried=0 bad=()
  exempt="$("$TOOL" exemptions | awk -F'\t' -v c="$col" '$1==c {print $2}')"
  while IFS=$'\t' read -r _ tbl; do
    if echo "$exempt" | grep -qx "$tbl"; then
      continue
    fi
    if grep -q $'^column\t'"$tbl"$'\t'"$col"$'\t[a-z]*\tnotnull\t' "$DUMP"; then
      carried=$((carried+1))
    else
      bad+=("$tbl")
    fi
  done < <(grep $'^table\t' "$DUMP")
  if [ ${#bad[@]} -eq 0 ]; then
    pass "$label" "$carried tables carry it; exempt: $(echo $exempt | tr '\n' ' ')"
  else
    fail "$label" "without it: ${bad[*]}"
  fi
  "$TOOL" exemptions | awk -F'\t' -v c="$col" '$1==c {printf "        exempt %-17s %s\n", $2, $3}'
}

banner "K-01 — cell identifier everywhere (AB-K01-3)"
notnull_everywhere "K01-3a cell is NOT NULL on every table" cell

if mutate '/^CREATE TABLE judgment (/,/^);/{/^  cell          text NOT NULL REFERENCES cell(id),$/d}' nocell.sql; then
  rejected_check "K01-3b a table without cell is rejected" "$SCRATCH/nocell.sql"
else
  fail "K01-3b a table without cell is rejected" "the mutation did not take"
fi

banner "K-01 — project reference everywhere (AB-K01-4)"
notnull_everywhere "K01-4a project is NOT NULL on every table" project

if mutate '/^CREATE TABLE judgment (/,/^);/{/^  project       uuid NOT NULL REFERENCES project(id),$/d}' noproject.sql; then
  rejected_check "K01-4b a table without project is rejected" "$SCRATCH/noproject.sql"
else
  fail "K01-4b a table without project is rejected" "the mutation did not take"
fi

if mutate '/^CREATE TABLE spec (/,/^);/{s/^  project     uuid NOT NULL REFERENCES project(id),$/  project     uuid REFERENCES project(id),/}' nullproject.sql; then
  rejected_check "K01-4c a nullable project is rejected" "$SCRATCH/nullproject.sql"
else
  fail "K01-4c a nullable project is rejected" "the mutation did not take"
fi

# --------------------------------------------------------------------------
# AB-K01-5 — no secrets: the schema checker finds no secret column (probe)
# --------------------------------------------------------------------------
banner "K-01 — no secret column (AB-K01-5, probe)"

append "CREATE TABLE probe_vault (id uuid PRIMARY KEY, cell text NOT NULL, project uuid NOT NULL, api_key text NOT NULL);" apikey.sql
rejected_check "K01-5a an api_key column is rejected" "$SCRATCH/apikey.sql"

append "CREATE TABLE probe_vault (id uuid PRIMARY KEY, cell text NOT NULL, project uuid NOT NULL, github_token text);" token.sql
rejected_check "K01-5b a token column is rejected" "$SCRATCH/token.sql"

append "CREATE TABLE probe_vault (id uuid PRIMARY KEY, cell text NOT NULL, project uuid NOT NULL, signing_secret text);" secret.sql
rejected_check "K01-5c a secret column is rejected" "$SCRATCH/secret.sql"

REFS=()
for r in authority_ref content_hash payload_ref; do
  grep -q $'^column\t[a-z_]*\t'"$r"$'\t' "$DUMP" || REFS+=("$r")
done
if [ ${#REFS[@]} -eq 0 ]; then
  pass "K01-5d the contract carries references only" "authority_ref, content_hash, payload_ref"
else
  fail "K01-5d the contract carries references only" "missing: ${REFS[*]}"
fi

if grep -q $'^column\tbudget_pot\ttokens_cap\t' "$DUMP"; then
  pass "K01-5e budget tokens stay" "a quantity (V-04), not a credential — words, not substrings"
else
  fail "K01-5e budget tokens stay" "budget_pot.tokens_cap is gone"
fi

# --------------------------------------------------------------------------
# AB-V05-2 — additive migration: a removing migration is rejected (probe)
# --------------------------------------------------------------------------
banner "V-05 — additive only (AB-V05-2, probe)"

if mutate '/^CREATE TABLE envelope (/,/^);/{/^  thread             text,$/d}' removedcol.sql; then
  rejected_compare "V05-2a a removed column is rejected" "$SQL" "$SCRATCH/removedcol.sql"
else
  fail "V05-2a a removed column is rejected" "the mutation did not take"
fi

if mutate '/^CREATE TABLE halt (/,/^);/d' removedtbl.sql; then
  rejected_compare "V05-2b a removed table is rejected" "$SQL" "$SCRATCH/removedtbl.sql"
else
  fail "V05-2b a removed table is rejected" "the mutation did not take"
fi

if mutate '/^CREATE TABLE "order" (/,/^);/{s/^  attempt        int NOT NULL DEFAULT 1,$/  attempt        text NOT NULL DEFAULT 1,/}' retyped.sql; then
  rejected_compare "V05-2c a retyped column is rejected" "$SQL" "$SCRATCH/retyped.sql"
else
  fail "V05-2c a retyped column is rejected" "the mutation did not take"
fi

if mutate '/^CREATE TABLE spec (/,/^);/{s/^  project     uuid NOT NULL REFERENCES project(id),$/  project     uuid REFERENCES project(id),/}' loosened.sql; then
  rejected_compare "V05-2d a repurposed nullability is rejected" "$SQL" "$SCRATCH/loosened.sql"
else
  fail "V05-2d a repurposed nullability is rejected" "the mutation did not take"
fi

if mutate "s/'injection',//" removedval.sql; then
  rejected_compare "V05-2e a removed enum value is rejected" "$SQL" "$SCRATCH/removedval.sql"
else
  fail "V05-2e a removed enum value is rejected" "the mutation did not take"
fi

# Control probes: a tool that rejects everything would pass 2a-2e. It must accept the unchanged
# contract and a plain addition — SP-V05-2's release one, "write the new field".
if "$TOOL" "$SQL" "$SQL" > /dev/null 2>&1; then
  pass "V05-2f the unchanged schema is additive"
else
  fail "V05-2f the unchanged schema is additive" "the tool rejects identity"
fi

if mutate '/^CREATE TABLE envelope (/,/^);/{s/^  thread             text,$/  thread             text,\n  probe_note         text,/}' addedcol.sql \
   && "$TOOL" "$SQL" "$SCRATCH/addedcol.sql" > /dev/null 2>&1; then
  pass "V05-2g an added column is accepted"
else
  fail "V05-2g an added column is accepted" "the tool rejects an addition"
fi

append "CREATE TABLE probe_note (id uuid PRIMARY KEY, cell text NOT NULL REFERENCES cell(id), project uuid NOT NULL REFERENCES project(id));" addedtbl.sql
if "$TOOL" "$SQL" "$SCRATCH/addedtbl.sql" > /dev/null 2>&1; then
  pass "V05-2h an added table is accepted"
else
  fail "V05-2h an added table is accepted" "the tool rejects an addition"
fi

# --------------------------------------------------------------------------
# The contract in a running database. The parser above reads the schema; a
# Postgres 16 loads it, and the catalogs must agree with the checker.
# --------------------------------------------------------------------------
banner "K-01 — the contract in a database (Postgres 16)"

if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
  PG="k01-schema-$$"
  docker run --rm -d --name "$PG" -e POSTGRES_PASSWORD=acceptance postgres:16 >/dev/null

  psql_c() { docker exec -i "$PG" psql -U postgres -At -v ON_ERROR_STOP=1 -c "$1" 2>/dev/null; }

  # The entrypoint starts a temporary server during initdb and restarts it; one answered query is
  # not readiness. Ready means two answered queries a second apart.
  READY=""
  for _ in $(seq 1 90); do
    if psql_c "SELECT 1" >/dev/null; then
      sleep 1
      psql_c "SELECT 1" >/dev/null && { READY=1; break; }
    fi
    sleep 1
  done

  if [ -z "$READY" ]; then
    fail "K01-db1 the schema loads" "postgres:16 did not become ready"
  elif docker exec -i "$PG" psql -U postgres -q -v ON_ERROR_STOP=1 -f - < "$SQL" > "$SCRATCH/load.out" 2>&1; then
    pass "K01-db1 the schema loads" "postgres:16, ON_ERROR_STOP"

    DB_BAD=""; DB_ERR=""
    for col in cell project; do
      EXEMPT_CSV="$("$TOOL" exemptions | awk -F'\t' -v c="$col" '$1==c {printf "%s'\''%s'\''", sep, $2; sep=","}')"
      MISSING_DB=$(psql_c "SELECT t.tablename FROM pg_tables t
          WHERE t.schemaname='public' AND t.tablename NOT IN ($EXEMPT_CSV)
            AND NOT EXISTS (SELECT 1 FROM information_schema.columns c
                             WHERE c.table_schema='public' AND c.table_name=t.tablename
                               AND c.column_name='$col' AND c.is_nullable='NO')") || DB_ERR=1
      [ -n "$MISSING_DB" ] && DB_BAD="$DB_BAD $col:$(echo "$MISSING_DB" | tr '\n' ',')"
    done
    if [ -n "$DB_ERR" ]; then
      fail "K01-db2 the catalogs agree with the checker" "the catalog query itself failed"
    elif [ -z "$DB_BAD" ]; then
      pass "K01-db2 the catalogs agree with the checker" "information_schema confirms cell and project"
    else
      fail "K01-db2 the catalogs agree with the checker" "$DB_BAD"
    fi

    SEQ_COUNT=$(psql_c "SELECT count(*) FROM pg_class WHERE relkind='S'") || SEQ_COUNT="query failed"
    if [ "$SEQ_COUNT" = "0" ]; then
      pass "K01-db3 no sequence exists once loaded" "pg_class relkind='S': 0"
    else
      fail "K01-db3 no sequence exists once loaded" "$SEQ_COUNT"
    fi
  else
    fail "K01-db1 the schema loads" "$(head -1 "$SCRATCH/load.out")"
  fi
else
  skip "K01-db the contract in a database" "no docker on this machine; the CI leg brings one"
fi

# --------------------------------------------------------------------------
banner "Result"
printf '  %d PASS · %d FAIL · %d SKIP\n\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
