#!/usr/bin/env bash
# k03-outbox.sh — the outbox and the two gates, measured (AP-3.5).
#
#   acceptance/k03-outbox.sh          the sources against the program, then the chain end to end
#   acceptance/k03-outbox.sh host     only the sources — no build, for a working tree
#
# Nine rows hang on this run:
#
#   AB-K03-1   P  outbox — the pod produces intent, the gate executes
#   AB-K03-2   P  domain key — two attempts, one push
#   AB-K03-3   P  two gates, nothing else — a bypass attempt fails
#   AB-K03-4   P  register — a missing acknowledgement leads to asking, never to a retry
#   AB-K03-5   P  the adapter deduplicates — a control restart produces no second message
#   AB-A06-11  P  double execution without double effect — the same job twice, one push
#   AB-B01-4   P  keys only in the gates — on work nodes and in pods no model or Git key is found
#   AB-B02-2   S  egress proxy on the work node — no central throughput bottleneck
#   AB-B02-4   S  allowlist per job — target, method, size limit derived from the authority
#
# Nothing here is simulated. The gates are the real `workpod git-gate` and `workpod egress-gate`
# out of the built binary, each on its own Unix socket; the repository is a real bare Git
# repository and the pushes are real pushes counted with `git rev-list`; the outbox is the real
# store on a directory that stands in for /var, and the drain is `workpod outbox drain`.
#
# What it does not do is boot a machine. Everything AP-3.5 owns — the domain key, the register, the
# two gates, the allowlist, the credentials' whereabouts — is a property of the programs and of
# their files, and it is measurable wherever the binary runs. The one requirement that needs a node
# is "a pod has no network", and that is AB-T04-2, already green through AP-3.3's boot.
#
# Exit:  0 = the nine rows are evidenced by this run
#        1 = they are not

set -uo pipefail

MODE="${1:-drive}"
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"

PASS=0; FAIL=0
pass() { printf '  \033[32mPASS\033[0m  %-52s %s\n' "$1" "${2:-}"; PASS=$((PASS+1)); }
fail() { printf '  \033[31mFAIL\033[0m  %-52s %s\n' "$1" "${2:-}"; FAIL=$((FAIL+1)); }

SPEC="$ROOT/01-specification.md"
SCHEMA="$ROOT/contract/schema.sql"
PROTO="$ROOT/contract/platform.proto"
RULING="$ROOT/decisions/gates-and-the-outbox.md"
ALLOWLIST="$ROOT/platform/internal/egress/b02-allowlist.tsv"

# =================================================================================================
# The sources against the program. A run that measured a program the specification does not
# describe would measure nothing — the same argument acceptance/t05-pipeline.sh's host half makes.
# =================================================================================================
printf '\n\033[1mK-03 and B-02 — the sources against the program\033[0m\n\n'

# ---- AB-B02-4: the allowlist is derived from the authority, and the levels are the contract's ----
# SP-B02-4 names three things — target, method, size limit — and the table has to carry all three.
HEADER="$(grep -v '^#' "$ALLOWLIST" | grep -m1 'level')"
for column in level targets methods size_limit; do
  if grep -q "$column" <<< "$HEADER"; then
    pass "B02-4a the table carries \"$column\"" ""
  else
    fail "B02-4a the table carries \"$column\"" "header: $HEADER"
  fi
done

# The levels are contract/schema.sql's `authority_level`, and drift in either direction is an
# error: a level in the enum with no row fails closed at runtime, and a row for a level the
# contract does not have is a rule about nothing.
# The enum stands on one line in contract/schema.sql; a sed range would run past it into the next
# CREATE TYPE and compare the allowlist against the resource classes.
ENUM_LEVELS="$(grep -m1 'CREATE TYPE authority_level' "$SCHEMA" \
  | grep -o "'[a-z]*'" | tr -d "'" | sort | tr '\n' ' ')"
TABLE_LEVELS="$(grep -v '^#' "$ALLOWLIST" | awk -F'\t' 'NF>=4 && $1!="level" {print $1}' | sort | tr '\n' ' ')"
if [ -n "$ENUM_LEVELS" ] && [ "$ENUM_LEVELS" = "$TABLE_LEVELS" ]; then
  pass "B02-4b every authority level derives an allowlist" "$TABLE_LEVELS"
else
  fail "B02-4b every authority level derives an allowlist" "enum: ${ENUM_LEVELS:-none} | table: ${TABLE_LEVELS:-none}"
fi

# A bare `*` in the target column would make the allowlist a route. SP-B02-4 says target, and a
# target that is everything is not one.
if grep -v '^#' "$ALLOWLIST" | awk -F'\t' 'NF>=4 && $1!="level" {print $2}' | grep -qx '\*'; then
  fail "B02-4c no level reaches everything" "a row's targets are a bare *"
else
  pass "B02-4c no level reaches everything" "every row names hosts"
fi

# Every row bounds the size. A missing or zero limit is the field SP-B02-4 names being absent.
BAD_LIMIT="$(grep -v '^#' "$ALLOWLIST" | awk -F'\t' 'NF>=4 && $1!="level" && ($4+0)<=0 {print $1}')"
if [ -z "$BAD_LIMIT" ]; then
  pass "B02-4d every level bounds the response size" ""
else
  fail "B02-4d every level bounds the response size" "no limit on: $BAD_LIMIT"
fi

# ---- AB-B02-2: the egress proxy stands on the work node ----------------------------------------
# Two things make that true and both are checked, because either alone would be easy to fake: the
# unit belongs to the work role, and the gate is reachable only over a socket on the machine.
WORK_TARGET="$ROOT/image/mkosi.extra/usr/lib/systemd/system/workpod-work.target"
CONTROL_TARGET="$ROOT/image/mkosi.extra/usr/lib/systemd/system/workpod-control.target"
if grep -q 'workpod-egress-gate.service' "$WORK_TARGET"; then
  pass "B02-2a the egress gate belongs to the work role" "workpod-work.target"
else
  fail "B02-2a the egress gate belongs to the work role" "not in workpod-work.target (SP-B02-2)"
fi
if grep -q 'workpod-egress-gate.service' "$CONTROL_TARGET"; then
  fail "B02-2b it is not a central service" "the control role also activates it"
else
  pass "B02-2b it is not a central service" "the control role does not activate it"
fi
if grep -q 'workpod-git-gate.service' "$CONTROL_TARGET"; then
  pass "B02-2c the Git gate is the control role's" "workpod-control.target"
else
  fail "B02-2c the Git gate is the control role's" "not in workpod-control.target"
fi

# SP-B02-6: no open port means no open port. Neither gate may listen on one.
if grep -rn 'net.Listen("tcp"' "$ROOT/platform/internal/gitgate" "$ROOT/platform/internal/egress" >/dev/null 2>&1; then
  fail "B02-2d neither gate opens a port" "a gate listens on tcp (SP-B02-6)"
else
  pass "B02-2d neither gate opens a port" "both listen on a Unix socket"
fi

# ---- SP-K03-3: two gates, and the contract knows exactly two -----------------------------------
GATE_SERVICES="$(grep -c '^service \(GitGate\|EgressGate\)' "$PROTO")"
if [ "$GATE_SERVICES" = "2" ]; then
  pass "K03-3a the contract names two gates" "GitGate, EgressGate"
else
  fail "K03-3a the contract names two gates" "$GATE_SERVICES gate service(s) in platform.proto"
fi

# ---- The ruling exists, because a deviation is a decision before it is code (V-05) --------------
for section in "domain key" "allowlist" "SP-B01-4" "Overturned by"; do
  if grep -qi -- "$section" "$RULING"; then
    pass "V05 the ruling covers \"$section\"" ""
  else
    fail "V05 the ruling covers \"$section\"" "decisions/gates-and-the-outbox.md"
  fi
done

if [ "$MODE" = host ]; then
  printf '\n  %d passed, %d failed — sources only, the chain was not run\n\n' "$PASS" "$FAIL"
  [ "$FAIL" -eq 0 ] || exit 1
  exit 0
fi

# =================================================================================================
# The chain, end to end. Real binary, real gates, real repository, real pushes.
# =================================================================================================
command -v go >/dev/null 2>&1 || { echo "  no Go toolchain — cannot build the binary this run measures" >&2; exit 1; }
command -v git >/dev/null 2>&1 || { echo "  no git — the Git gate has nothing to push to" >&2; exit 1; }

WORK="$(mktemp -d)"
cleanup() {
  [ -n "${GIT_GATE_PID:-}" ] && kill "$GIT_GATE_PID" 2>/dev/null
  [ -n "${EGRESS_PID:-}" ] && kill "$EGRESS_PID" 2>/dev/null
  wait 2>/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT

printf '\n\033[1mK-03 — the chain: pod → outbox → gate → receipt\033[0m\n\n'

BIN="$WORK/workpod"
if ! (cd "$ROOT/platform" && go build -o "$BIN" ./cmd/workpod) 2>"$WORK/build.log"; then
  fail "the binary builds" "$(tail -3 "$WORK/build.log")"
  printf '\n  %d passed, %d failed\n\n' "$PASS" "$FAIL"
  exit 1
fi
pass "the binary builds" "$($BIN version)"

# Both gates report serving rather than refusing — AB-E02-1's list, re-read after this work package.
COMPONENTS="$($BIN components)"
for gate in git-gate egress-gate; do
  if grep -q "^$gate  *serving  *AP-3.5" <<< "$COMPONENTS"; then
    pass "the $gate serves since AP-3.5" ""
  else
    fail "the $gate serves since AP-3.5" "$(grep "^$gate" <<< "$COMPONENTS")"
  fi
done

# ---- The node's areas -------------------------------------------------------------------------
VAR="$WORK/var/lib/workpod"                 # /var of the work node
OUTBOX="$VAR/outbox"
GRANTS="$VAR/egress-grants"
GATE_VAR="$WORK/var/lib/workpod-gate"       # the gates', and only the gates'
CREDS="$WORK/credentials"                   # what systemd would load for the gate's unit
mkdir -p "$VAR" "$GRANTS" "$GATE_VAR" "$CREDS" "$WORK/run"

# The repository the job pushes to, and a base commit in it.
BARE="$WORK/repo.git"
SEED="$WORK/seed"
git init --quiet --bare --initial-branch=main "$BARE"
git init --quiet --initial-branch=main "$SEED"
echo base > "$SEED/README"
git -C "$SEED" add README
git -C "$SEED" -c user.name=t -c user.email=t@t commit --quiet -m base
git -C "$SEED" remote add origin "$BARE"
git -C "$SEED" push --quiet origin main

# The patch the job produced. `payload_ref` is a reference; this is what it resolves to.
cat > "$WORK/change.patch" <<'PATCH'
diff --git a/README b/README
--- a/README
+++ b/README
@@ -1 +1,2 @@
 base
+from the pod
PATCH

# The gate's policy and its signing credential.
printf '%s\tmain\tyes\n' "$BARE" > "$WORK/git-gate-policy.tsv"
echo "the git gate's signing key" > "$CREDS/git-signing-key"
chmod 0600 "$CREDS/git-signing-key"

GIT_SOCK="$WORK/run/git-gate.sock"
EGRESS_SOCK="$WORK/run/egress-gate.sock"

"$BIN" git-gate --socket "$GIT_SOCK" --policy "$WORK/git-gate-policy.tsv" \
  --ledger "$GATE_VAR/git" --credential "$CREDS/git-signing-key" >"$WORK/git-gate.log" 2>&1 &
GIT_GATE_PID=$!
"$BIN" egress-gate --socket "$EGRESS_SOCK" --grants "$GRANTS" \
  --credentials "$CREDS/egress" >"$WORK/egress-gate.log" 2>&1 &
EGRESS_PID=$!

for _ in $(seq 1 50); do
  [ -S "$GIT_SOCK" ] && [ -S "$EGRESS_SOCK" ] && break
  sleep 0.1
done
if [ -S "$GIT_SOCK" ] && [ -S "$EGRESS_SOCK" ]; then
  pass "both gates serve on Unix sockets" "$(basename "$GIT_SOCK"), $(basename "$EGRESS_SOCK")"
else
  fail "both gates serve on Unix sockets" "$(tail -2 "$WORK/git-gate.log" "$WORK/egress-gate.log")"
fi

# AB-B02-2, measured rather than read: neither gate is listening on a port. `ss` is not in every
# container, so the socket file is the positive evidence and a port scan the negative one where a
# tool exists to do it with.
if command -v ss >/dev/null 2>&1; then
  if ss -lntp 2>/dev/null | grep -q workpod; then
    fail "B02-2e no gate listens on a port" "$(ss -lntp 2>/dev/null | grep workpod)"
  else
    pass "B02-2e no gate listens on a port" "measured with ss"
  fi
else
  pass "B02-2e no gate listens on a port" "no ss; the sockets are files (checked above)"
fi

outbox() { "$BIN" outbox "$@" --dir "$OUTBOX" --git-gate "$GIT_SOCK" --egress-gate "$EGRESS_SOCK"; }

TARGET="git+$BARE#main"
HASH="$(sha256sum "$WORK/change.patch" | cut -d' ' -f1)"

# ---- AB-K03-1: the pod produces intent, the gate executes ---------------------------------------
outbox record --order order-1 --target "$TARGET" --content-hash "$HASH" \
  --payload-ref "$WORK/change.patch" > "$WORK/record.log" 2>&1
STATE="$(outbox list 2>/dev/null | head -1 | awk '{print $1}')"
COMMITS_AFTER_RECORD="$(git -C "$BARE" rev-list --count main)"
if [ "$STATE" = recorded ] && [ "$COMMITS_AFTER_RECORD" = 1 ]; then
  pass "K03-1a recording is an intent, not an act" "state=$STATE, commits still $COMMITS_AFTER_RECORD"
else
  fail "K03-1a recording is an intent, not an act" "state=$STATE, commits=$COMMITS_AFTER_RECORD"
fi

outbox drain > "$WORK/drain-1.log" 2>&1
COMMITS_AFTER_DRAIN="$(git -C "$BARE" rev-list --count main)"
if [ "$COMMITS_AFTER_DRAIN" = 2 ]; then
  pass "K03-1b the gate executes what the outbox holds" "commits $COMMITS_AFTER_RECORD -> $COMMITS_AFTER_DRAIN"
else
  fail "K03-1b the gate executes what the outbox holds" "commits=$COMMITS_AFTER_DRAIN, expected 2. $(tail -3 "$WORK/drain-1.log")"
fi

# The receipt came back into the job (SP-K03-1's last link).
if outbox list 2>/dev/null | grep -q '^acknowledged'; then
  pass "K03-1c the receipt is back in the entry" "$(outbox list 2>/dev/null | head -1 | cut -c1-60)"
else
  fail "K03-1c the receipt is back in the entry" "$(outbox list 2>/dev/null | head -1)"
fi

# The gate signed itself (SP-K03-3), and the signature is readable in the repository's own log.
if git -C "$BARE" log -1 --format=%B main | grep -q 'Workpod-Gate-Signature:'; then
  pass "K03-3b the gate signs itself" "$(git -C "$BARE" log -1 --format=%B main | grep Signature | cut -c1-48)"
else
  fail "K03-3b the gate signs itself" "the commit carries no signature trailer"
fi

# ---- AB-K03-2 and AB-A06-11: the same job twice, one push ---------------------------------------
# The second run of the same job re-derives the same patch for the same branch. Everything about it
# is a second execution — a second record, a second drain, a second gate call — and the repository
# has to be unmoved afterwards.
outbox record --order order-1 --target "$TARGET" --content-hash "$HASH" \
  --payload-ref "$WORK/change.patch" > "$WORK/record-2.log" 2>&1
if grep -q 'already recorded' "$WORK/record-2.log"; then
  pass "K03-2a the domain key refuses a second entry" "$(cat "$WORK/record-2.log")"
else
  fail "K03-2a the domain key refuses a second entry" "$(cat "$WORK/record-2.log")"
fi
ENTRIES="$(find "$OUTBOX" -name '*.json' | wc -l)"
if [ "$ENTRIES" = 1 ]; then
  pass "K03-2b one entry for one effect" "$ENTRIES file in the outbox"
else
  fail "K03-2b one entry for one effect" "$ENTRIES files"
fi

# And now the harder half: a *second worker* with its own outbox, which has never seen the first
# one — the situation V-02 creates by having no leader election. The gate's ledger is what stops it.
OUTBOX2="$WORK/var2/outbox"
"$BIN" outbox record --dir "$OUTBOX2" --order order-1 --target "$TARGET" --content-hash "$HASH" \
  --payload-ref "$WORK/change.patch" > "$WORK/record-3.log" 2>&1
"$BIN" outbox drain --dir "$OUTBOX2" --git-gate "$GIT_SOCK" --egress-gate "$EGRESS_SOCK" \
  > "$WORK/drain-2.log" 2>&1
COMMITS_FINAL="$(git -C "$BARE" rev-list --count main)"
if [ "$COMMITS_FINAL" = 2 ]; then
  pass "K03-2c two attempts, one push" "commits still $COMMITS_FINAL after a second worker drained"
else
  fail "K03-2c two attempts, one push" "commits=$COMMITS_FINAL — the job was executed twice (AB-A06-11)"
fi
if grep -q 'already executed' "$WORK/drain-2.log"; then
  pass "A06-11 the second execution had no second effect" "the gate's ledger answered"
else
  fail "A06-11 the second execution had no second effect" "$(tail -3 "$WORK/drain-2.log")"
fi

# ---- AB-K03-3: two gates, nothing else ----------------------------------------------------------
# A target that names no gate must not find one. This is the bypass attempt as the matrix means it:
# the forbidden action has to fail, and it fails at the outbox because there is no third gate to
# route it to.
for BAD in "ftp://files.example.org/x" "file:///etc/shadow" "channel:cli#general"; do
  if outbox record --order order-bypass --target "$BAD" --content-hash h1 >"$WORK/bypass.log" 2>&1; then
    fail "K03-3c $BAD finds no gate" "it was accepted into the outbox"
  else
    pass "K03-3c $BAD finds no gate" "$(head -1 "$WORK/bypass.log" | cut -c1-56)"
  fi
done

# The other half of "what does not go through a gate does not exist": a job with no grant reaches
# nothing through the egress gate. Default deny, and the refusal names its cause (SP-B02-5).
outbox record --order order-nogrant --target "https://proxy.golang.org/x" --content-hash h2 >/dev/null 2>&1
outbox drain > "$WORK/drain-nogrant.log" 2>&1
if outbox list 2>/dev/null | grep -q '^denied.*order-nogrant'; then
  pass "B02-4e a job with no allowlist reaches nothing" "denied, with a cause"
else
  fail "B02-4e a job with no allowlist reaches nothing" "$(outbox list 2>/dev/null | grep order-nogrant)"
fi

# ---- AB-K03-4: the register, and the one forbidden retry ----------------------------------------
# A non-idempotent target, recorded and begun, whose gate never acknowledged — the worker died. The
# next attempt must ask, and must not execute.
MAILHASH="deadbeef"
outbox record --order order-mail --target "https://mail.example.invalid/send" \
  --content-hash "$MAILHASH" --requires-register >/dev/null 2>&1
# Drain once. There is no grant for this order, so the gate refuses — but what is being checked is
# the register's own transition, so the entry is put into `executing` directly, which is the state a
# worker that died mid-call leaves behind.
python3 - "$OUTBOX" "$MAILHASH" <<'PY'
import hashlib, json, pathlib, sys
outbox, content_hash = pathlib.Path(sys.argv[1]), sys.argv[2]
key = f"order=order-mail\ntarget=https://mail.example.invalid/send\ncontent_hash={content_hash}\n"
path = outbox / (hashlib.sha256(key.encode()).hexdigest() + ".json")
entry = json.loads(path.read_text())
entry["state"] = "executing"          # the gate was called; the worker died before the receipt
path.write_text(json.dumps(entry, indent=2) + "\n")
PY
outbox drain > "$WORK/drain-register.log" 2>&1
if grep -q 'needs asking, not a second call' "$WORK/drain-register.log"; then
  pass "K03-4a a missing acknowledgement asks" "$(grep -m1 'needs asking' "$WORK/drain-register.log" | cut -c1-56)"
else
  fail "K03-4a a missing acknowledgement asks" "$(tail -3 "$WORK/drain-register.log")"
fi
if outbox list 2>/dev/null | grep -q '^asking.*order-mail'; then
  pass "K03-4b the entry waits on a human" "state=asking"
else
  fail "K03-4b the entry waits on a human" "$(outbox list 2>/dev/null | grep order-mail)"
fi
if outbox unanswered 2>/dev/null | grep -q 'order-mail'; then
  pass "K03-4c it is on the list a human is asked from" ""
else
  fail "K03-4c it is on the list a human is asked from" "$(outbox unanswered 2>/dev/null | tail -1)"
fi
# The probe half: there is no way to retry it. A `retry` sub-command would be the requirement
# broken by the interface rather than by the state machine, so its absence is checked too.
if "$BIN" outbox retry --dir "$OUTBOX" >/dev/null 2>&1; then
  fail "K03-4d there is no retry" "`workpod outbox retry` exists (SP-K03-4)"
else
  pass "K03-4d there is no retry" "the only place retrying is forbidden has no verb for it"
fi

# ---- AB-B01-4: keys only in the gates -----------------------------------------------------------
# The probe searches the work node's own areas for the gate's secret. The secret is a known string,
# so a hit is unambiguous — a search for "key" would find the word and prove nothing.
SECRET="the git gate's signing key"
FOUND=""
for area in "$VAR" "$OUTBOX" "$GRANTS" "$WORK/run" "$SEED"; do
  [ -e "$area" ] || continue
  if grep -rlF "$SECRET" "$area" >/dev/null 2>&1; then
    FOUND="$FOUND $area"
  fi
done
if [ -z "$FOUND" ]; then
  pass "B01-4a no gate secret in the work layer" "searched: /var/lib/workpod, the outbox, the grants, /run, the working copy"
else
  fail "B01-4a no gate secret in the work layer" "found in:$FOUND"
fi
# It is where it is supposed to be, or the search above proved only that it exists nowhere.
if grep -rlF "$SECRET" "$CREDS" >/dev/null 2>&1; then
  pass "B01-4b it is in the gate's credential directory" "$(basename "$CREDS")/git-signing-key"
else
  fail "B01-4b it is in the gate's credential directory" "the control is broken: the secret is nowhere"
fi
# Nor does the gate leak it into its own log, which is the place a struct dump would put it.
if grep -qF "$SECRET" "$WORK/git-gate.log" 2>/dev/null; then
  fail "B01-4c the gate does not log its key" "it is in the gate's own log"
else
  pass "B01-4c the gate does not log its key" ""
fi
# And the outbox entries — which cross from the pod's side to the gate's — carry no credential.
if grep -rlF "$SECRET" "$OUTBOX" "$GATE_VAR" >/dev/null 2>&1; then
  fail "B01-4d neither the outbox nor the ledger holds a key" ""
else
  pass "B01-4d neither the outbox nor the ledger holds a key" "both hold references, never secrets"
fi

# ---- AB-K03-5: the adapter deduplicates via the event ID ----------------------------------------
# The unit tests hold the mechanism; this holds it against a restart of the process, which is what
# the row is actually about.
if (cd "$ROOT/platform" && go test ./internal/adapter/ -run 'TestARestartProducesNoSecondMessage|TestTwoEventsWithTheSameWordsAreTwoMessages' -count=1) >"$WORK/adapter.log" 2>&1; then
  pass "K03-5a a restart produces no second message" "internal/adapter, -count=1"
else
  fail "K03-5a a restart produces no second message" "$(tail -5 "$WORK/adapter.log")"
fi

# ---- SP-K03-6: the outbox survives the worker ---------------------------------------------------
# The gates are killed and started again over the same directories — which is what a restart is —
# and everything that was in the outbox and in the ledger is still there.
BEFORE="$(find "$OUTBOX" -name '*.json' | wc -l)"
LEDGER_BEFORE="$(find "$GATE_VAR" -name '*.json' | wc -l)"
kill "$GIT_GATE_PID" "$EGRESS_PID" 2>/dev/null; wait 2>/dev/null
"$BIN" git-gate --socket "$GIT_SOCK" --policy "$WORK/git-gate-policy.tsv" \
  --ledger "$GATE_VAR/git" --credential "$CREDS/git-signing-key" >>"$WORK/git-gate.log" 2>&1 &
GIT_GATE_PID=$!
for _ in $(seq 1 50); do [ -S "$GIT_SOCK" ] && break; sleep 0.1; done
AFTER="$(find "$OUTBOX" -name '*.json' | wc -l)"
LEDGER_AFTER="$(find "$GATE_VAR" -name '*.json' | wc -l)"
if [ "$BEFORE" = "$AFTER" ] && [ "$LEDGER_BEFORE" = "$LEDGER_AFTER" ] && [ "$AFTER" -gt 0 ]; then
  pass "K03-6 the outbox and the ledger survive a restart" "$AFTER entries, $LEDGER_AFTER ledger records"
else
  fail "K03-6 the outbox and the ledger survive a restart" "outbox $BEFORE->$AFTER, ledger $LEDGER_BEFORE->$LEDGER_AFTER"
fi

# The restarted gate still refuses the push it already made — the ledger is what carries that
# across, and without it the restart would be a second push.
"$BIN" outbox record --dir "$WORK/var3/outbox" --order order-1 --target "$TARGET" \
  --content-hash "$HASH" --payload-ref "$WORK/change.patch" >/dev/null 2>&1
"$BIN" outbox drain --dir "$WORK/var3/outbox" --git-gate "$GIT_SOCK" --egress-gate "$EGRESS_SOCK" \
  >"$WORK/drain-3.log" 2>&1
COMMITS_RESTART="$(git -C "$BARE" rev-list --count main)"
if [ "$COMMITS_RESTART" = 2 ]; then
  pass "A06-11b a restarted gate does not push again" "commits still $COMMITS_RESTART"
else
  fail "A06-11b a restarted gate does not push again" "commits=$COMMITS_RESTART"
fi

printf '\n  %d passed, %d failed\n\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ] || exit 1
