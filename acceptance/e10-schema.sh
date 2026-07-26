#!/usr/bin/env bash
# e10-schema.sh — the contract, checked by a machine (AP-2.1).
#
# Four rows rest on this script:
#   AB-E10-1  one interface schema — adapter through gate speak the same Protobuf
#   AB-E10-3  additive fields only — a removed or reassigned field number is rejected (probe)
#   AB-E10-5  one source for renderings — export and audit trail render from the same definitions
#   AB-T04-4  runner abstraction — `platform` in the envelope, the scheduler knows several pools
#
# AB-E10-3 is a probe: the forbidden action must fail. The probes mutate a scratch copy of the
# contract and expect e10-additive.py to reject it — and two control probes expect it to accept a
# lawful death and a plain addition, so that a linter which rejects everything cannot pass.
#
# The stage 2 boundary holds here: these are properties of the contract, not of an implementation.
# "The scheduler knows several pools" is, at this stage, the envelope carrying `platform` and the
# schema naming the pools — the scheduler itself is stage 3 work.
#
# Exit:  0 = no FAIL
#        1 = at least one FAIL

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PROTO="$ROOT/contract/platform.proto"
LINT="$ROOT/acceptance/e10-additive.py"

SCRATCH="$(mktemp -d)"
trap 'rm -rf "$SCRATCH"' EXIT

PASS=0; FAIL=0; SKIP=0

pass() { printf '  \033[32mPASS\033[0m  %-46s %s\n' "$1" "${2:-}"; PASS=$((PASS+1)); }
fail() { printf '  \033[31mFAIL\033[0m  %-46s %s\n' "$1" "${2:-}"; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m  %-46s %s\n' "$1" "${2:-}"; SKIP=$((SKIP+1)); }
banner() { printf '\n\033[1m%s\033[0m\n' "$1"; }

# --------------------------------------------------------------------------
# AB-E10-1 — one interface schema in the system
# --------------------------------------------------------------------------
banner "E-10 — one schema (AB-E10-1)"

COUNT=$(find "$ROOT/contract" -name '*.proto' | wc -l)
if [ "$COUNT" = 1 ]; then
  pass "E10-1a exactly one schema" "contract/platform.proto"
else
  fail "E10-1a exactly one schema" "$COUNT .proto files under contract/"
fi

DUMP="$SCRATCH/dump.tsv"
if "$LINT" dump "$PROTO" > "$DUMP"; then
  pass "E10-1b the schema parses" "$(grep -c $'^field\t' "$DUMP") fields"
else
  fail "E10-1b the schema parses" "e10-additive.py rejects it"
fi

PKG=$(awk -F'\t' '$1=="package"{print $2}' "$DUMP")
if [ "$PKG" = "workpod.v1" ]; then
  pass "E10-1c one package" "workpod.v1"
else
  fail "E10-1c one package" "found: ${PKG:-none}"
fi

MISSING=()
for svc in ControlPlane GitGate EgressGate Harness; do
  grep -q $'^rpc\t'"$svc"$'\t' "$DUMP" || MISSING+=("$svc")
done
if [ ${#MISSING[@]} -eq 0 ]; then
  pass "E10-1d adapter, gates and harness stand in it" "ControlPlane GitGate EgressGate Harness"
else
  fail "E10-1d adapter, gates and harness stand in it" "missing: ${MISSING[*]}"
fi

# The claim made concrete: the pod's harness enqueues the very message the Git gate receives, and
# the adapter submits the envelope the control plane admits — the same types, one schema.
if grep -q $'^rpc\tGitGate\tPush\tOutboxEntry\t' "$DUMP" \
   && grep -q $'^rpc\tHarness\tEnqueueEffect\tOutboxEntry\t' "$DUMP" \
   && grep -q $'^rpc\tControlPlane\tSubmitEnvelope\tEnvelope\t' "$DUMP"; then
  pass "E10-1e gate and harness speak the same messages" "OutboxEntry, Envelope"
else
  fail "E10-1e gate and harness speak the same messages"
fi

if command -v protoc >/dev/null 2>&1; then
  WKT=()
  [ -d /usr/include/google/protobuf ] && WKT=(-I /usr/include)
  if protoc -I "$ROOT/contract" "${WKT[@]}" --descriptor_set_out=/dev/null platform.proto 2> "$SCRATCH/protoc.err"; then
    pass "E10-1f protoc compiles it" "$(protoc --version)"
  else
    fail "E10-1f protoc compiles it" "$(head -1 "$SCRATCH/protoc.err")"
  fi
else
  skip "E10-1f protoc compiles it" "no protoc on this machine; the CI leg installs one"
fi

# --------------------------------------------------------------------------
# AB-E10-3 — the probes: the forbidden action must fail
# --------------------------------------------------------------------------
banner "E-10 — additive only (AB-E10-3, probe)"

# mutate SED_EXPR OUT — a probe that does not change the file proves nothing, so that is a FAIL.
mutate() {
  sed "$1" "$PROTO" > "$SCRATCH/$2"
  ! cmp -s "$PROTO" "$SCRATCH/$2"
}

# rejected LABEL OLD NEW DETAIL — the linter must exit non-zero.
rejected() {
  if "$LINT" "$2" "$3" > "$SCRATCH/lint.out" 2>&1; then
    fail "$1" "the linter accepted it"
  else
    pass "$1" "$(head -1 "$SCRATCH/lint.out")"
  fi
}

if mutate 's/  string thread = 11;//' removed.proto; then
  rejected "E10-3a a removed field number is rejected" "$PROTO" "$SCRATCH/removed.proto"
else
  fail "E10-3a a removed field number is rejected" "the mutation did not take"
fi

if mutate 's/  string platform = 8;/  string region = 8;/' renamed.proto; then
  rejected "E10-3b a renamed field is rejected" "$PROTO" "$SCRATCH/renamed.proto"
else
  fail "E10-3b a renamed field is rejected" "the mutation did not take"
fi

if mutate 's/  uint32 attempt = 15;/  string attempt = 15;/' retyped.proto; then
  rejected "E10-3c a retyped field is rejected" "$PROTO" "$SCRATCH/retyped.proto"
else
  fail "E10-3c a retyped field is rejected" "the mutation did not take"
fi

# Reassignment: in the older revision the number is a grave, in the newer it is a field again.
if mutate 's/  string thread = 11;/  reserved 11;/' grave.proto; then
  rejected "E10-3d a reassigned field number is rejected" "$SCRATCH/grave.proto" "$PROTO"
else
  fail "E10-3d a reassigned field number is rejected" "the mutation did not take"
fi

# Control probes: a linter that rejects everything would pass 3a-3d. It must accept a lawful
# death (the number stays, as a grave) and a plain addition.
if "$LINT" "$PROTO" "$SCRATCH/grave.proto" > /dev/null 2>&1; then
  pass "E10-3e a reserved death is accepted"
else
  fail "E10-3e a reserved death is accepted" "the linter rejects the lawful path"
fi

if mutate 's/  string thread = 11;/  string thread = 11;\n  string probe_note = 15;/' additive.proto \
   && "$LINT" "$PROTO" "$SCRATCH/additive.proto" > /dev/null 2>&1; then
  pass "E10-3f an added field is accepted"
else
  fail "E10-3f an added field is accepted" "the linter rejects an addition"
fi

# --------------------------------------------------------------------------
# AB-E10-5 — export and audit trail render from the same definitions
# --------------------------------------------------------------------------
banner "E-10 — one source for renderings (AB-E10-5)"

# Every message-typed member of a rendering must be a message defined in this same schema — a
# rendering that restated fields, or imported a second schema, would be the drift SP-E10-5 forbids.
rendering() {  # $1 = row label, $2 = message, $3... = the members it must carry
  local label="$1" msg="$2"; shift 2
  if ! grep -q $'^message\t'"$msg"'$' "$DUMP"; then
    fail "$label" "message $msg is not in the schema"
    return
  fi
  local missing=() foreign=()
  for want in "$@"; do
    grep -q $'^field\t'"$msg"$'\t[0-9]*\t[a-z_]*\t'"$want"$'\t' "$DUMP" || missing+=("$want")
  done
  while IFS=$'\t' read -r _ _ _ name ftype _; do
    case "$ftype" in
      string|bool|bytes|uint32|uint64|int32|int64|double|float) continue ;;
      google.protobuf.*) continue ;;
    esac
    grep -q $'^message\t'"$ftype"'$' "$DUMP" || grep -q $'^value\t'"$ftype"$'\t' "$DUMP" \
      || foreign+=("$name:$ftype")
  done < <(grep $'^field\t'"$msg"$'\t' "$DUMP")
  if [ ${#missing[@]} -eq 0 ] && [ ${#foreign[@]} -eq 0 ]; then
    pass "$label" "$*"
  else
    fail "$label" "missing: ${missing[*]:-none} · outside the schema: ${foreign[*]:-none}"
  fi
}

# SP-B03-5: who received which authority when, which gate let what through, which human accepted
# what — each carried as the message that produced it.
rendering "E10-5a the audit trail wraps the system's messages" \
  AuditRecord Lease Receipt HumanAcceptance

# SP-V05-1: specs and control state are exportable — as themselves.
rendering "E10-5b the export wraps the system's messages" \
  ExportChunk Spec Envelope Order OutboxEntry Receipt Report AuditRecord

# --------------------------------------------------------------------------
# AB-T04-4 — the abstraction is over Runner, not over Workpod
# --------------------------------------------------------------------------
banner "T-04 — runner abstraction (AB-T04-4)"

if grep -q $'^field\tEnvelope\t14\tplatform\tstring\t' "$DUMP"; then
  pass "T04-4a the envelope carries platform" "Envelope field 14"
else
  fail "T04-4a the envelope carries platform"
fi

if grep -q $'^field\tOrder\t[0-9]*\tplatform\tstring\t' "$DUMP"; then
  pass "T04-4b the order carries it onward" "Order field 8"
else
  fail "T04-4b the order carries it onward"
fi

MISSING=()
for pool in alpine windows macos remote; do
  grep -q "$pool" "$PROTO" || MISSING+=("$pool")
done
if [ ${#MISSING[@]} -eq 0 ]; then
  pass "T04-4c the schema names the pools" "alpine windows macos remote"
else
  fail "T04-4c the schema names the pools" "missing: ${MISSING[*]}"
fi

# --------------------------------------------------------------------------
banner "Result"
printf '  %d PASS · %d FAIL · %d SKIP\n\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
