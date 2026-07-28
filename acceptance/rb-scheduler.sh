#!/usr/bin/env bash
# rb-scheduler.sh — tokens per phase, four priorities with aging, the PSI ladder, and the queue
# with SKIP LOCKED (AP-3.7).
#
# Thirteen rows rest on this script:
#   AB-RB-1  S  token per phase — a waiting pod holds no CPU token
#   AB-RB-2  M  four priorities — `interactive` waits ≤ 2 s when slots are free
#   AB-RB-3  M  aging — a batch job does not starve behind interactive work
#   AB-RB-4  P  preempt = freeze — a preempted pod loses the slot, not the state
#   AB-RB-5  S  exclusive operation — a job above ~60 % RAM holds all CPU tokens
#   AB-RC-1  P  pressure instead of utilization — admission decides from PSI
#   AB-RC-3  S  escalation ladder — five rungs run in order, without an abort
#   AB-RC-6  M  prediction — after three runs admission decides mechanically
#   AB-RD-3  P  planning values — admission does not read the five constants
#   AB-E05-2 P  the same, from E-05's side
#   AB-V01-4 M  reservation beats priority — under sustained load admission stays fast
#   AB-E02-2 P  Postgres alone — no second broker, queue with SKIP LOCKED
#   AB-E02-5 M  the control layer inside 4 cores and 16 GB
#
# The rulings are checked first and against the files the binary embeds: decisions/phase-tokens.md
# against phase-tokens.tsv, decisions/aging.md's keys against the order the program produces, SP-RB-2
# and SP-RC-2 against the tables `workpod scheduler` prints, and OP-6's four numbers against the
# thresholds the watcher carries. A run that measured a program the ruling does not describe would
# measure nothing.
#
# Then the program runs. Pressure is replayed rather than caused, which is the only way to observe a
# 30-second release hold without waiting 30 seconds — and the same code path reads the real cgroup
# files under `--slice`, so the replay is a shortcut and never a stand-in. The queue half needs a
# real Postgres 16 with contract/schema.sql, because "SKIP LOCKED hands a row to one taker" is a
# claim about a database and not about a program.
#
# `rb-scheduler.sh host` runs everything that needs no database — which is most of it.
#
# Exit:  0 = no FAIL
#        1 = at least one FAIL

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TOKEN_RULING="$ROOT/decisions/phase-tokens.md"
AGING_RULING="$ROOT/decisions/aging.md"
LADDER_RULING="$ROOT/decisions/escalation-ladder.md"
OP6="$ROOT/decisions/OP-6.md"
TOKENS_TSV="$ROOT/platform/internal/scheduling/phase-tokens.tsv"
PIPELINE="$ROOT/platform/internal/runner/pipeline.go"
SPEC="$ROOT/01-specification.md"
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

# jq is not a dependency of this repository; python3 is already one (registry.py, module-contract.py).
json() { python3 -c "$1" ; }

# =================================================================================================
# decisions/phase-tokens.md — the ruling and the file the binary carries
# =================================================================================================
banner "SP-RB-1 — which token a phase holds (decisions/phase-tokens.md)"

# The ruling's table is `| `phase` | `token` | why |`; the file is the same pair as TSV. Both are
# read into one shape and compared as sets, so a row in one and not the other is drift whichever
# side it is on.
RULED_TOKENS="$(awk -F'|' '
  NF >= 4 && $2 ~ /`[a-z]+`/ && $3 ~ /`(net|io|cpu·ram)`/ {
    gsub(/[` ]/, "", $2); gsub(/[` ]/, "", $3); print $2 "\t" $3
  }' "$TOKEN_RULING" | sort)"
FILE_TOKENS="$(awk -F'\t' '!/^#/ && $1 != "phase" && NF == 2 {print $1 "\t" $2}' "$TOKENS_TSV" | sort)"

if [ -n "$RULED_TOKENS" ] && [ "$RULED_TOKENS" = "$FILE_TOKENS" ]; then
  pass "RB-1a the tokens are the ruled ones" "$(wc -l <<< "$FILE_TOKENS") phases, three classes"
else
  fail "RB-1a the tokens are the ruled ones" \
    "ruling: $(tr '\n\t' ' /' <<< "$RULED_TOKENS") · file: $(tr '\n\t' ' /' <<< "$FILE_TOKENS")"
fi

# The table covers T-05's spine exactly. A spine that grew an eighth phase and left the ruling alone
# would be a phase the scheduler has no token for.
SPINE="$(grep -oE 'Phase[A-Z][a-z]+ +Phase = "[a-z]+"' "$PIPELINE" | grep -oE '"[a-z]+"' | tr -d '"' | sort)"
TOKEN_PHASES="$(cut -f1 <<< "$FILE_TOKENS" | sort)"
if [ -n "$SPINE" ] && [ "$SPINE" = "$TOKEN_PHASES" ]; then
  pass "RB-1b the table covers the spine, exactly" "$(tr '\n' ' ' <<< "$SPINE")"
else
  fail "RB-1b the table covers the spine, exactly" \
    "spine: $(tr '\n' ' ' <<< "$SPINE") · table: $(tr '\n' ' ' <<< "$TOKEN_PHASES")"
fi

# SP-RB-1's closing sentence is the reason `edit` is on `net` and not on the bottleneck. It is the
# one row the work package opens with, so it is checked by name rather than only as part of the set.
EDIT_TOKEN="$(awk -F'\t' '$1=="edit" {print $2}' <<< "$FILE_TOKENS")"
CHECK_TOKEN="$(awk -F'\t' '$1=="check" {print $2}' <<< "$FILE_TOKENS")"
if [ "$EDIT_TOKEN" = "net" ] && [ "$CHECK_TOKEN" = "cpu·ram" ]; then
  pass "RB-1c waiting on a model is not computing" "edit=net · check=cpu·ram (SP-RB-1)"
else
  fail "RB-1c waiting on a model is not computing" "edit=$EDIT_TOKEN · check=$CHECK_TOKEN"
fi

# =================================================================================================
# The rulings that have no file: aging and the ladder
# =================================================================================================
banner "SP-RB-3, SP-RC-3 — the two rulings without a table of their own"

for f in "$AGING_RULING" "$LADDER_RULING"; do
  name="$(basename "$f")"
  if [ -f "$f" ] && grep -q '^## Ruling' "$f" && grep -q '^## Overturned by' "$f"; then
    pass "G01-x $name is a ruling" "ruling, rationale and an overturn condition (V-05)"
  else
    fail "G01-x $name is a ruling" "a decision without an overturn condition is an opinion"
  fi
done

# =================================================================================================
# The artifact
# =================================================================================================
banner "AP-3.7 — the binary"

BIN="$STAGED"
if [ ! -x "$BIN" ]; then
  if command -v go >/dev/null 2>&1; then
    BIN="$SCRATCH/workpod"
    ( cd "$ROOT/platform" && CGO_ENABLED=0 go build -trimpath -buildvcs=false -o "$BIN" ./cmd/workpod ) \
      || { fail "the binary builds" "go build failed"; result; exit 1; }
  else
    skip "AP-3.7 every check that runs the program" "neither a staged build nor go on this machine"
    result
    exit $?
  fi
fi
pass "the binary stands" "$(sha256sum "$BIN" | cut -d' ' -f1)"

# The entry point is no longer a refusal: SP-E02-1's seventh component serves.
COMPONENT="$("$BIN" components | awk '$1=="scheduler" {print $2}')"
if [ "$COMPONENT" = "serving" ]; then
  pass "E02-1a the scheduler serves" "the last of SP-E02-1's seven components"
else
  fail "E02-1a the scheduler serves" "components says '$COMPONENT'"
fi

# =================================================================================================
# AB-RB-1 — a waiting pod holds no CPU token
# =================================================================================================
banner "AB-RB-1 — the token is the phase's, and waiting returns it"

# The ruling as the binary carries it, against the file above. Two sources, one table.
"$BIN" scheduler tokens --cores 8 > "$SCRATCH/tokens.json"
BIN_TOKENS="$(json "
import json
d = json.load(open('$SCRATCH/tokens.json'))
print('\n'.join(sorted(r['phase'] + '\t' + r['token'] for r in d['phases'])))")"
if [ "$BIN_TOKENS" = "$FILE_TOKENS" ]; then
  pass "RB-1d the program carries the ruled table" "read back out of the binary, not out of the file"
else
  fail "RB-1d the program carries the ruled table" "$(tr '\n\t' ' /' <<< "$BIN_TOKENS")"
fi

SIZES="$(json "
import json
d = json.load(open('$SCRATCH/tokens.json'))['sizes']
print('%d %d %d' % (d['Net'], d['IO'], d['CPURAM']))")"
if [ "$SIZES" = "64 2 8" ]; then
  pass "RB-1e the pools follow the ruled ratios" "8 cores: 64 net · 2 io · 8 cpu·ram"
else
  fail "RB-1e the pools follow the ruled ratios" "8 cores gave '$SIZES', ruled '64 2 8'"
fi

# A run, not a table: one pod computes, begins to wait for a model response, and a second pod takes
# the token it returned. On a one-token pool that is only possible if the first really let go.
cat > "$SCRATCH/trace.tsv" <<'TRACE'
pod-a	check
pod-a	wait
pod-b	check
pod-a	plan
TRACE
"$BIN" scheduler tokens --cores 1 --trace "$SCRATCH/trace.tsv" > "$SCRATCH/trace.json"
TRACE_VERDICT="$(json "
import json
steps = [json.loads(o) for o in open('$SCRATCH/trace.json').read().replace('}\n{', '}\x00{').split('\x00')]
by = {(s['pod'], s['step']): s for s in steps}
bad = []
if by[('pod-a','check')]['holds'] != 'cpu·ram': bad.append('the computing pod holds no cpu·ram token')
if by[('pod-a','wait')]['holds'] != '':        bad.append('the waiting pod still holds a token')
if by[('pod-a','wait')]['held']['cpu·ram'] != 0: bad.append('the token was not returned to the pool')
if not by[('pod-b','check')]['granted']:       bad.append('the returned token was not handed on')
if by[('pod-a','plan')]['holds'] != 'net':     bad.append('the pod that came back took the wrong pool')
print('; '.join(bad))")"
if [ -z "$TRACE_VERDICT" ]; then
  pass "RB-1f a waiting pod holds no CPU token" "one token, handed on while the first pod waits (SP-RB-1)"
else
  fail "RB-1f a waiting pod holds no CPU token" "$TRACE_VERDICT"
fi

# =================================================================================================
# AB-RB-5 — exclusive operation above ~60 % RAM
# =================================================================================================
banner "AB-RB-5 — a large run holds every cpu·ram token, and there is only ever one"

cat > "$SCRATCH/exclusive.tsv" <<'TRACE'
pod-large	exclusive
pod-b	check
pod-large-2	exclusive
pod-c	plan
pod-large	leave
pod-large-2	exclusive
TRACE
"$BIN" scheduler tokens --cores 8 --trace "$SCRATCH/exclusive.tsv" > "$SCRATCH/exclusive.json"
EXCL_VERDICT="$(json "
import json
steps = [json.loads(o) for o in open('$SCRATCH/exclusive.json').read().replace('}\n{', '}\x00{').split('\x00')]
bad = []
if not steps[0]['granted'] or steps[0]['free_cpu_ram'] != 0:
    bad.append('the large run did not take every cpu·ram token')
if steps[1]['granted']: bad.append('a second pod computed beside the exclusive run')
if steps[2]['granted']: bad.append('two large runs at once (SP-RB-6)')
if not steps[3]['granted']: bad.append('planning was refused beside an exclusive run')
if not steps[5]['granted']: bad.append('the waiting large run never got the node')
print('; '.join(bad))")"
if [ -z "$EXCL_VERDICT" ]; then
  pass "RB-5a one large run holds all CPU tokens" "and the second waits rather than sharing (SP-RB-5, SP-RB-6)"
else
  fail "RB-5a one large run holds all CPU tokens" "$EXCL_VERDICT"
fi

# The threshold itself is the specification's ~60 %, and it is applied to a *measured* peak.
EXCL_SHARE="$("$BIN" scheduler predict --runs 3 --peak 7000000000 --free 10000000000 | \
  json "import json,sys; print(json.load(sys.stdin)['verdict']['Exclusive'])")"
NOT_EXCL="$("$BIN" scheduler predict --runs 3 --peak 5000000000 --free 10000000000 | \
  json "import json,sys; print(json.load(sys.stdin)['verdict']['Exclusive'])")"
if [ "$EXCL_SHARE" = "True" ] && [ "$NOT_EXCL" = "False" ]; then
  pass "RB-5b the threshold is ~60 % of what is free" "70 % runs alone, 50 % does not"
else
  fail "RB-5b the threshold is ~60 % of what is free" "70 %: $EXCL_SHARE · 50 %: $NOT_EXCL"
fi

# =================================================================================================
# AB-RB-2 and AB-RB-3 — the four priorities, and aging
# =================================================================================================
banner "AB-RB-2, AB-RB-3 — SP-RB-2's table, and what waiting does to it"

# SP-RB-2's four rows, out of §12.2 of the specification, against the table the program prints.
SPEC_PRIO="$(awk -F'|' '
  NF >= 5 && $2 ~ /`(interactive|batch|maintenance|background)`/ {
    gsub(/[` ]/, "", $2); gsub(/^ +| +$/, "", $3); gsub(/ /, "", $4)
    print $2 "\t" $3 "\t" $4
  }' "$SPEC" | sort)"
BIN_PRIO="$("$BIN" scheduler priorities | json "
import json,sys
rows = json.load(sys.stdin)
spec = {'2s': '2 s', '5m0s': '5 min', '1h0m0s': '1 h', 'unbounded': 'unbounded'}
print('\n'.join(sorted(
    r['priority'] + '\t' + spec[r['waits_at_most']] + '\t' + ('yes' if r['may_preempt'] else 'no')
    for r in rows)))")"
if [ "$SPEC_PRIO" = "$BIN_PRIO" ]; then
  pass "RB-2a the four priorities are SP-RB-2's" "$(tr '\n\t' ' /' <<< "$BIN_PRIO")"
else
  fail "RB-2a the four priorities are SP-RB-2's" \
    "specification: $(tr '\n\t' ' /' <<< "$SPEC_PRIO") · program: $(tr '\n\t' ' /' <<< "$BIN_PRIO")"
fi

# Below saturation nothing is overdue and the priority column decides: an interactive job that
# arrives into a queue of older, unhurried work is served first, inside its 2 seconds.
cat > "$SCRATCH/queue-fresh.json" <<'Q'
{"now": 1750000000, "jobs": [
  {"order_id": "batch-old",   "priority": "batch",       "waited_seconds": 100},
  {"order_id": "maintenance", "priority": "maintenance", "waited_seconds": 900},
  {"order_id": "background",  "priority": "background",  "waited_seconds": 9000},
  {"order_id": "interactive", "priority": "interactive", "waited_seconds": 0}]}
Q
FRESH="$("$BIN" scheduler order --queue "$SCRATCH/queue-fresh.json" | \
  json "import json,sys; print(' '.join(j['order_id'] for j in json.load(sys.stdin)))")"
if [ "$FRESH" = "interactive batch-old maintenance background" ]; then
  pass "RB-2b interactive is first while slots are free" "$FRESH"
else
  fail "RB-2b interactive is first while slots are free" "$FRESH"
fi

# AB-RB-3, measured: at which wait does a batch job overtake a stream of fresh interactive work?
# The bound is 300 s, so the answer must be just past it — earlier would break SP-RB-2's promise to
# interactive work, much later would be the starvation the row is about.
OVERTOOK=""
for waited in 60 200 299 301 400; do
  cat > "$SCRATCH/queue-aged.json" <<Q
{"now": 1750000000, "jobs": [
  {"order_id": "interactive-1", "priority": "interactive", "waited_seconds": 0},
  {"order_id": "interactive-2", "priority": "interactive", "waited_seconds": 1},
  {"order_id": "batch",         "priority": "batch",       "waited_seconds": $waited}]}
Q
  HEAD="$("$BIN" scheduler order --queue "$SCRATCH/queue-aged.json" | \
    json "import json,sys; print(json.load(sys.stdin)[0]['order_id'])")"
  if [ "$HEAD" = "batch" ] && [ -z "$OVERTOOK" ]; then OVERTOOK="$waited"; fi
done
measure "RB-3a a batch job overtakes after" "${OVERTOOK:-never} s of waiting (its bound is 300 s)"
if [ "$OVERTOOK" = "301" ]; then
  pass "RB-3a aging, not starvation" "inside its bound it waits; past it, it goes first (SP-RB-3)"
else
  fail "RB-3a aging, not starvation" "overtook at ${OVERTOOK:-never}, expected just past 300 s"
fi

# And the ratio, not the priority column, decides between two overdue jobs: 10 s over a 2 s bound is
# a factor of 5, which beats a batch job 400 s into a 300 s one.
cat > "$SCRATCH/queue-both.json" <<'Q'
{"now": 1750000000, "jobs": [
  {"order_id": "batch",   "priority": "batch",       "waited_seconds": 400},
  {"order_id": "stuck-i", "priority": "interactive", "waited_seconds": 10}]}
Q
BOTH="$("$BIN" scheduler order --queue "$SCRATCH/queue-both.json" | \
  json "import json,sys; print(' '.join(j['order_id'] for j in json.load(sys.stdin)))")"
if [ "$BOTH" = "stuck-i batch" ]; then
  pass "RB-3b the ratio decides between two overdue jobs" "5.0 before 1.33 (decisions/aging.md)"
else
  fail "RB-3b the ratio decides between two overdue jobs" "$BOTH"
fi

# SP-RB-2 gives "may preempt" to one row, and aging does not hand it to a second.
PREEMPTERS="$("$BIN" scheduler priorities | \
  json "import json,sys; print(sum(1 for r in json.load(sys.stdin) if r['may_preempt']))")"
if [ "$PREEMPTERS" = "1" ]; then
  pass "RB-2c one priority may preempt" "interactive, and aging never grants it to another"
else
  fail "RB-2c one priority may preempt" "$PREEMPTERS priorities may preempt"
fi

# =================================================================================================
# AB-RC-1 and AB-RC-3 — pressure, and the ladder over it
# =================================================================================================
banner "SP-RC-2, OP-6 — the six signals and their four numbers"

"$BIN" scheduler pressure --thresholds > "$SCRATCH/thresholds.json"
SIGNALS="$(json "
import json
d = json.load(open('$SCRATCH/thresholds.json'))
print(len(d['thresholds']))")"
if [ "$SIGNALS" = "6" ]; then
  pass "RC-2a six signals" "SP-RC-2's table, whole"
else
  fail "RC-2a six signals" "the program carries $SIGNALS"
fi

# OP-6's ruling, row by row: enter on two samples at the panel's threshold, release at half of it
# after 30 s — and `memory full` on one sample, because SP-RC-2 wrote "do not wait" beside it.
OP6_VERDICT="$(json "
import json
d = {t['signal']: t for t in json.load(open('$SCRATCH/thresholds.json'))['thresholds']}
want = {
  'memory.some.avg10':  (10.0, 2, 5.0,  '30s', 'block'),
  'memory.full.avg10':  (5.0,  1, 2.5,  '30s', 'freeze'),
  'io.full.avg10':      (20.0, 2, 10.0, '30s', 'throttle'),
  'cpu.some.avg60':     (60.0, 2, 30.0, '1m0s','throttle'),
  'pgmajfault':         (100.0,2, 10.0, '30s', 'escalate'),
}
bad = []
for sig, (enter, samples, release, hold, rung) in want.items():
    t = d.get(sig)
    if not t: bad.append(sig + ' is missing'); continue
    got = (t['enter'], t['enter_samples'], t['release'], t['release_hold'], t['rung'])
    if got != (enter, samples, release, hold, rung):
        bad.append('%s: %s != %s' % (sig, got, (enter, samples, release, hold, rung)))
if d.get('memory.events.high', {}).get('rung') != '':
    bad.append('a reclassification moved the ladder')
if json.load(open('$SCRATCH/thresholds.json'))['sample_interval'] != '2s':
    bad.append('the reader does not sample every two seconds (SP-RC-1)')
print('; '.join(bad))")"
if [ -z "$OP6_VERDICT" ]; then
  pass "RC-2b OP-6's four numbers per signal" "enter, samples, release, hold — and the one exception"
else
  fail "RC-2b OP-6's four numbers per signal" "$OP6_VERDICT"
fi

# The ruling itself says the same thing in prose. A number that moved in the program and not in the
# open point is drift.
if grep -q '2 consecutive samples' "$OP6" && grep -q '30 s' "$OP6" && grep -q 'on one sample' "$OP6"; then
  pass "RC-2c the ruling says what the program does" "decisions/OP-6.md"
else
  fail "RC-2c the ruling says what the program does" "OP-6 does not carry the numbers the program has"
fi

banner "AB-RC-1 — admission decides from pressure, never from utilization"

# A machine with every core busy and no pressure. `cpu.pressure` is what is read, and it is 0 —
# there is no field in a sample that says how busy the cores are, which is the requirement.
{
  echo "# t_seconds mem_some mem_full io_full cpu_some events_high pgmajfault"
  for t in 0 2 4 6 8 10; do echo "$t 0 0 0 0 0 0"; done
} > "$SCRATCH/psi-busy.tsv"
BUSY_ADMITS="$("$BIN" scheduler pressure --samples "$SCRATCH/psi-busy.tsv" | \
  json "
import json,sys
turns = [json.loads(o) for o in sys.stdin.read().replace('}\n{', '}\x00{').split('\x00')]
print(all(t['admits'] for t in turns))")"
if [ "$BUSY_ADMITS" = "True" ]; then
  pass "RC-1a a busy machine under no pressure admits" "100 % CPU is healthy (R-C's own words)"
else
  fail "RC-1a a busy machine under no pressure admits" "admission stopped without a signal"
fi

# The same machine with 12 % memory pressure — a number no utilization metric reports — stops
# admitting, on the second sample and not the first (OP-6).
{
  echo "# t_seconds mem_some mem_full io_full cpu_some events_high pgmajfault"
  echo "0 12 0 0 0 0 0"
  echo "2 12 0 0 0 0 0"
  echo "4 12 0 0 0 0 0"
} > "$SCRATCH/psi-tight.tsv"
TIGHT="$("$BIN" scheduler pressure --samples "$SCRATCH/psi-tight.tsv" | \
  json "
import json,sys
turns = [json.loads(o) for o in sys.stdin.read().replace('}\n{', '}\x00{').split('\x00')]
print('%s %s %s' % (turns[0]['admits'], turns[1]['admits'], turns[2]['admits']))")"
if [ "$TIGHT" = "True False False" ]; then
  pass "RC-1b memory pressure stops admission" "one sample is a spike, two are an event (OP-6)"
else
  fail "RC-1b memory pressure stops admission" "admits across three samples: $TIGHT"
fi

# The probe, from the other side: nothing in the scheduler reads a utilization metric. There is no
# /proc/stat, no load average and no core count in the decision path.
UTIL="$(grep -rnE 'loadavg|/proc/stat|cpu\.max|utiliz' \
  "$ROOT/platform/internal/scheduling" "$ROOT/platform/internal/scheduler" 2>/dev/null \
  | grep -v '_test.go' | grep -vE ':[0-9]+:[[:space:]]*//' || true)"
if [ -z "$UTIL" ]; then
  pass "RC-1c the decision path reads no utilization" "pressure files only (SP-RC-1)"
else
  fail "RC-1c the decision path reads no utilization" "$(head -2 <<< "$UTIL" | tr '\n' ' ')"
fi

banner "AB-RC-3 — five rungs, in order, and none of them an abort"

# pgmajfault rising fast demands the hardest rung immediately. "Immediately" moves the target, not
# the order: the four rungs below it still run, and they run first.
{
  echo "# t_seconds mem_some mem_full io_full cpu_some events_high pgmajfault"
  echo "0 0 0 0 0 0 0"
  echo "2 0 0 0 0 0 400"
  echo "4 0 0 0 0 0 800"
} > "$SCRATCH/psi-thrash.tsv"
"$BIN" scheduler pressure --samples "$SCRATCH/psi-thrash.tsv" > "$SCRATCH/thrash.json"
LADDER_RUN="$(json "
import json
turns = [json.loads(o) for o in open('$SCRATCH/thrash.json').read().replace('}\n{', '}\x00{').split('\x00')]
entered = [r for t in turns for r in t.get('entered') or []]
print(' '.join(entered))")"
if [ "$LADDER_RUN" = "throttle block freeze checkpoint escalate" ]; then
  pass "RC-3a the five rungs run in order" "$LADDER_RUN"
else
  fail "RC-3a the five rungs run in order" "entered: ${LADDER_RUN:-nothing}"
fi

ABORTS="$(json "
import json
turns = [json.loads(o) for o in open('$SCRATCH/thrash.json').read().replace('}\n{', '}\x00{').split('\x00')]
words = ' '.join(a for t in turns for a in (t.get('acts') or [])) + ' ' + \
        ' '.join(r for t in turns for r in (t.get('entered') or []))
print(' '.join(w for w in ('abort', 'kill', 'cancel') if w in words))")"
LADDER_WORDS="$(json "
import json
d = json.load(open('$SCRATCH/thresholds.json'))
print(' '.join(d['ladder']))")"
if [ -z "$ABORTS" ] && [ "$LADDER_WORDS" = "throttle block freeze checkpoint escalate" ]; then
  pass "RC-3b nothing was aborted" "the ladder has five values and none of them is a kill (SP-RB-4)"
else
  fail "RC-3b nothing was aborted" "words: ${ABORTS:-none} · ladder: $LADDER_WORDS"
fi

# It comes down the way it went up: one rung at a time, and only after the release hold.
{
  echo "# t_seconds mem_some mem_full io_full cpu_some events_high pgmajfault"
  echo "0 12 0 0 0 0 0"
  echo "2 12 0 0 0 0 0"
  echo "4 1 0 0 0 0 0"
  echo "20 1 0 0 0 0 0"
  echo "34 1 0 0 0 0 0"
  echo "36 1 0 0 0 0 0"
} > "$SCRATCH/psi-release.tsv"
RELEASE="$("$BIN" scheduler pressure --samples "$SCRATCH/psi-release.tsv" | \
  json "
import json,sys
turns = [json.loads(o) for o in sys.stdin.read().replace('}\n{', '}\x00{').split('\x00')]
print(' '.join(str(t['admits']) for t in turns))")"
if [ "$RELEASE" = "True False False False True True" ]; then
  pass "RC-3c the hold before release is OP-6's 30 s" "it does not release on the first quiet sample"
else
  fail "RC-3c the hold before release is OP-6's 30 s" "admits: $RELEASE"
fi

# =================================================================================================
# AB-RC-6, AB-RD-3, AB-E05-2 — prediction, and what admission may not read
# =================================================================================================
banner "AB-RC-6 — after three runs, admission decides mechanically"

MECH=""
for runs in 0 1 2 3 4; do
  M="$("$BIN" scheduler predict --runs "$runs" --peak 2000000000 --free 10000000000 | \
    json "import json,sys; print(json.load(sys.stdin)['verdict']['Mechanical'])")"
  MECH="$MECH $runs:$M"
done
if [ "$MECH" = " 0:False 1:False 2:False 3:True 4:True" ]; then
  pass "RC-6a three runs, and not two" "$MECH"
else
  fail "RC-6a three runs, and not two" "$MECH"
fi

# Above 90 % of what is free the job does not start, and it reports back with options rather than
# being truncated into silence (SP-RC-6, SP-V04-2).
REFUSAL="$("$BIN" scheduler predict --runs 3 --peak 9500000000 --free 10000000000)"
REFUSED="$(json "
import json
d = json.loads('''$REFUSAL''')
v = d['verdict']
print('%s %d' % (v['Admit'], len(v['Options'] or [])))")"
if [ "${REFUSED%% *}" = "False" ] && [ "${REFUSED##* }" -ge 2 ]; then
  pass "RC-6b above 90 % it does not start, and says what would help" "$REFUSED (admit, options)"
else
  fail "RC-6b above 90 % it does not start, and says what would help" "$REFUSED"
fi

banner "AB-RD-3, AB-E05-2 — the five constants are planning values"

# The probe: no module of the decision path reads acceptance/e05-constants.tsv, embeds it, or names
# one of its five keys. SP-RD-3 and the boundary of AP-3.7 are the same sentence from two sides.
E05_READ="$(grep -rniE 'e05-constants|active_pod|frozen_pod|zram_factor|page_cache_base|host_runtime' \
  "$ROOT/platform/internal/scheduling" "$ROOT/platform/internal/scheduler" \
  "$ROOT/platform/internal/statedb" 2>/dev/null \
  | grep -v '_test.go' | grep -vE ':[0-9]+:[[:space:]]*//' || true)"
if [ -z "$E05_READ" ]; then
  pass "RD-3a admission does not read the five constants" "no module of the decision path names one"
else
  fail "RD-3a admission does not read the five constants" "$(head -2 <<< "$E05_READ" | tr '\n' ' ')"
fi

# And from the running side: the only numbers that move the verdict are the measured peak and what
# the node has free. E-05 plans an active pod at 960 MB given / 122.5 MB measured; neither number
# changes either of these two answers, because neither is an input.
SMALL="$("$BIN" scheduler predict --runs 3 --peak 100000000 --free 8000000000 | \
  json "import json,sys; print(json.load(sys.stdin)['verdict']['Admit'])")"
HUGE="$("$BIN" scheduler predict --runs 3 --peak 8000000000 --free 8000000000 | \
  json "import json,sys; print(json.load(sys.stdin)['verdict']['Admit'])")"
if [ "$SMALL" = "True" ] && [ "$HUGE" = "False" ]; then
  pass "E05-2a the verdict moves with the measurement" "100 MB admitted, 8 GB refused, on the same node"
else
  fail "E05-2a the verdict moves with the measurement" "small: $SMALL · huge: $HUGE"
fi

# =================================================================================================
# AB-E02-2 — Postgres alone
# =================================================================================================
banner "AB-E02-2 — no second broker"

BROKERS="$(grep -inE 'amqp|rabbit|kafka|nats\.go|redis|nsq|sqs|pulsar' "$ROOT/platform/go.mod" || true)"
if [ -z "$BROKERS" ]; then
  pass "E02-2a there is no second broker" "the queue is a table; go.mod names no message system"
else
  fail "E02-2a there is no second broker" "$BROKERS"
fi

# The query the scheduler issues, read out of the program: one table, SKIP LOCKED, and the aging
# keys in front of the priority column.
ORDER_SQL="$("$BIN" scheduler order-sql)"
if grep -q 'o.priority::text' <<< "$ORDER_SQL" && grep -q 'updated_at' <<< "$ORDER_SQL"; then
  pass "E02-2b the ordering is generated from the ruling" "one ORDER BY, built from SP-RB-2's bounds"
else
  fail "E02-2b the ordering is generated from the ruling" "$ORDER_SQL"
fi
if grep -q 'SKIP LOCKED' "$SCHEMA"; then
  pass "E02-2c the state contract names SKIP LOCKED" "SP-RB-7, in the schema the plane loads"
else
  fail "E02-2c the state contract names SKIP LOCKED" "contract/schema.sql does not mention it"
fi

if [ "$MODE" = "host" ]; then
  result
  exit $?
fi

# =================================================================================================
# The database half
# =================================================================================================
banner "AP-3.7 — the queue against a real state database (Postgres 16)"

if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
  skip "AP-3.7 the database legs" "no docker on this machine; the CI leg brings one"
  result
  exit $?
fi

SOCK="$SCRATCH/sock"
mkdir -p "$SOCK"
chmod 0777 "$SOCK"
PG="rb-scheduler-$$"
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
  fail "RB-0a the database is up" "postgres:16 did not become ready"
  result
  exit 1
fi
pass "RB-0a the database is up" "socket only, listen_addresses empty"

psql_c() { docker exec -i "$PG" psql -U postgres -h /sock -d workpod -qAt -v ON_ERROR_STOP=1 -c "$1" 2>&1; }

export WORKPOD_DB_DSN="host=$SOCK user=postgres dbname=workpod"
export WORKPOD_DB_MAINTENANCE_DSN="host=$SOCK user=postgres dbname=postgres"
export WORKPOD_SCHEMA="$SCHEMA"
export WORKPOD_HALT_FILE="$SCRATCH/halt"
CREDS="$SCRATCH/credentials"
mkdir -p "$CREDS"
printf 'all'             > "$CREDS/workpod.role"
printf 'probe-c1'        > "$CREDS/workpod.cell"
printf '127.0.0.1:8447'  > "$CREDS/workpod.control"
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
  fail "RB-0b the plane loads the contract" "$(tail -3 "$SCRATCH/plane.log" | tr '\n' ' ')"
  result
  exit 1
fi
pass "RB-0b the plane loads the contract" "contract/schema.sql, including phase_profile (SP-RC-6)"

PRIN='018f4242-0000-7000-8000-0000000000c1'
PROJ='018f4242-0000-7000-8000-0000000000d1'
FIXTURES="$(psql_c "
INSERT INTO cell (id, tenant, retention) VALUES ('probe-c1', 'probe', '{}');
INSERT INTO principal (id, cell, daily_money_cap_micros) VALUES ('$PRIN', 'probe-c1', 230400000);
INSERT INTO project (id, cell, principal) VALUES ('$PROJ', 'probe-c1', '$PRIN');
INSERT INTO locality_group (id, cell) VALUES ('monorepo-a', 'probe-c1');")"
if [ -z "$FIXTURES" ]; then
  pass "RB-0c the fixtures stand" "one cell, one principal, one project, one locality group"
else
  fail "RB-0c the fixtures stand" "$FIXTURES"
  result
  exit 1
fi

# order PRIORITY KEY [WAITED_SECONDS] — one queued job, stated in SQL and aged by hand.
#
# `updated_at` is what the queue measures a wait from, and the transition trigger stamps it. Aging a
# job by hand is a write to that column *after* the transition, which is the only way to state "this
# job has waited six minutes" without waiting six minutes.
order() {
  local priority="$1" key="$2" waited="${3:-0}"
  local id
  id="$(psql_c "
    WITH e AS (
      INSERT INTO envelope (id, cell, project, channel, channel_message_id, sender_external,
                            principal, authority, text_body, received_at, idempotency, purge_after)
      VALUES (gen_random_uuid(), 'probe-c1', '$PROJ', 'cli', '$key', 'cli:sender',
              '$PRIN', 'confidential', 'a job', now(), '$key', now() + interval '30 days')
      RETURNING id
    ), s AS (
      INSERT INTO spec (id, version, cell, project, envelope_id, goal, bounds, budget, risk_class)
      SELECT gen_random_uuid(), 1, 'probe-c1', '$PROJ', e.id, 'a goal', '{}',
             '{\"pod_minutes\": 5, \"tokens\": 40000, \"money_micros\": 200000}', 'reversible' FROM e
      RETURNING id
    ), a AS (
      INSERT INTO acceptance (id, cell, project, spec_id, spec_version, statement,
                              required_evidence, machine_checkable)
      SELECT gen_random_uuid(), 'probe-c1', '$PROJ', s.id, 1, 'it passes', 'tests.new', true FROM s
    )
    INSERT INTO \"order\" (id, cell, project, spec_id, spec_version, class, platform, image_hash,
                           pipeline_version, authority_ref, budget_share, locality_group,
                           idempotency, priority)
    SELECT gen_random_uuid(), 'probe-c1', '$PROJ', s.id, 1, 'small', 'alpine', 'sha256:probe',
           'v1', 'envelope:probe', '{}', 'monorepo-a', '$key', '$priority' FROM s
    RETURNING id;")"
  "$BIN" control admit --order "$id" >/dev/null 2>&1
  "$BIN" scheduler enqueue --order "$id" >/dev/null 2>&1
  if [ "$waited" -gt 0 ]; then
    psql_c "UPDATE \"order\" SET updated_at = now() - interval '$waited seconds' WHERE id = '$id'" >/dev/null
  fi
  echo "$id"
}

# =================================================================================================
# AB-E02-2 — SKIP LOCKED hands a row to exactly one taker
# =================================================================================================
banner "AB-E02-2 — the queue, with SKIP LOCKED and no second broker"

for i in 1 2 3 4 5 6; do order batch "skip-$i" >/dev/null; done
QUEUED="$(psql_c "SELECT count(*) FROM \"order\" WHERE state = 'queued'")"
if [ "$QUEUED" = "6" ]; then
  pass "E02-2d the queue is a state of the order table" "6 jobs queued, no second system holds them"
else
  fail "E02-2d the queue is a state of the order table" "$QUEUED queued"
fi

# Two claims at once. Neither waits for the other and neither gets the other's rows — that is what
# SKIP LOCKED is for, and it is a claim about the database rather than about the program.
( "$BIN" scheduler queue --cell probe-c1 --claim 3 --hold 3s > "$SCRATCH/claim-a.json" 2>&1 ) &
CLAIM_A=$!
sleep 1
START="$(date +%s%N)"
"$BIN" scheduler queue --cell probe-c1 --claim 3 > "$SCRATCH/claim-b.json" 2>&1
ELAPSED_MS=$(( ($(date +%s%N) - START) / 1000000 ))
wait "$CLAIM_A"

DISJOINT="$(json "
import json
a = {q['OrderID'] for q in json.load(open('$SCRATCH/claim-a.json'))}
b = {q['OrderID'] for q in json.load(open('$SCRATCH/claim-b.json'))}
print('%d %d %d' % (len(a), len(b), len(a & b)))" 2>/dev/null || echo "0 0 0")"
if [ "$DISJOINT" = "3 3 0" ]; then
  pass "E02-2e two claims, six jobs, no overlap" "each row went to exactly one taker"
else
  fail "E02-2e two claims, six jobs, no overlap" "sizes and intersection: $DISJOINT"
fi
measure "E02-2f the second claim waited" "${ELAPSED_MS} ms while the first held three rows for 3 s"
if [ "$ELAPSED_MS" -lt 2000 ]; then
  pass "E02-2f the second claim did not wait" "SKIP LOCKED skips; it does not queue behind a lock"
else
  fail "E02-2f the second claim did not wait" "${ELAPSED_MS} ms"
fi

# =================================================================================================
# AB-RB-3 in the database — the SQL order is the ruling's order
# =================================================================================================
banner "AB-RB-3 — one ordering, in Go and in SQL"

I1="$(order interactive age-i 1)"
B1="$(order batch age-b 400)"
M1="$(order maintenance age-m 100)"
# The queue still holds the six jobs of the SKIP LOCKED check, and it should: what is compared is
# where these three stand relative to each other, in the database and in the program. Filtering is
# the comparison, not a way around it — a run against an empty queue would prove less.
SQL_ORDER="$("$BIN" scheduler queue --cell probe-c1 --limit 100 | json "
import json,sys
three = {'$I1', '$B1', '$M1'}
print(' '.join(q['OrderID'] for q in json.load(sys.stdin) if q['OrderID'] in three))")"
cat > "$SCRATCH/queue-same.json" <<Q
{"now": 1750000000, "jobs": [
  {"order_id": "$I1", "priority": "interactive", "waited_seconds": 1},
  {"order_id": "$B1", "priority": "batch",       "waited_seconds": 400},
  {"order_id": "$M1", "priority": "maintenance", "waited_seconds": 100}]}
Q
GO_ORDER="$("$BIN" scheduler order --queue "$SCRATCH/queue-same.json" | json "
import json,sys
print(' '.join(j['order_id'] for j in json.load(sys.stdin)))")"
if [ "$SQL_ORDER" = "$GO_ORDER" ] && [ "${SQL_ORDER%% *}" = "$B1" ]; then
  pass "RB-3c the database orders the way the ruling does" "the overdue batch job first, in both"
else
  fail "RB-3c the database orders the way the ruling does" "sql: $SQL_ORDER · go: $GO_ORDER"
fi

# =================================================================================================
# AB-RB-4 — preempting means freezing, not aborting
# =================================================================================================
banner "AB-RB-4 — the pod loses the slot, not the state"

# The job is walked to `running` the way the worker walks it, and then preempted. What must survive
# is everything about it: the attempt, what it spent, and the fact that it can go on.
psql_c "
  SET LOCAL workpod.writer = 'control';
  UPDATE \"order\" SET state = 'leased' WHERE id = '$B1';" >/dev/null
psql_c "
  SET LOCAL workpod.writer = 'worker';
  UPDATE \"order\" SET state = 'running' WHERE id = '$B1';" >/dev/null
psql_c "INSERT INTO attempt (order_id, attempt, cell, project) VALUES ('$B1', 1, 'probe-c1', '$PROJ')" >/dev/null
"$BIN" control spend --order "$B1" --pod-minutes 3 --tokens 20000 --money-micros 100000 >/dev/null 2>&1

BEFORE="$(psql_c "SELECT state || '/' || attempt || '/' || spent_pod_minutes || '/' || spent_tokens
                    FROM \"order\" WHERE id = '$B1'")"
"$BIN" scheduler freeze --order "$B1" --reason "the interactive job needs the slot (SP-RB-2)" >/dev/null 2>&1
FROZEN="$(psql_c "SELECT state || '/' || attempt || '/' || spent_pod_minutes || '/' || spent_tokens
                    FROM \"order\" WHERE id = '$B1'")"
"$BIN" scheduler thaw --order "$B1" --reason "the slot is free again" >/dev/null 2>&1
AFTER="$(psql_c "SELECT state || '/' || attempt || '/' || spent_pod_minutes || '/' || spent_tokens
                   FROM \"order\" WHERE id = '$B1'")"

if [ "$BEFORE" = "running/1/3/20000" ] && [ "$FROZEN" = "frozen/1/3/20000" ] && [ "$AFTER" = "running/1/3/20000" ]; then
  pass "RB-4a a freeze keeps everything but the slot" "running -> frozen -> running, attempt and spend unchanged"
else
  fail "RB-4a a freeze keeps everything but the slot" "$BEFORE -> $FROZEN -> $AFTER"
fi

# The reservation is still held: freezing is not a terminal state, so nothing was released.
HELD="$(psql_c "SELECT count(*) FROM budget_reservation WHERE order_id = '$B1' AND released_at IS NULL")"
if [ "$HELD" = "4" ]; then
  pass "RB-4b the frozen job still holds its pots" "a freeze releases nothing (SP-V04-3)"
else
  fail "RB-4b the frozen job still holds its pots" "$HELD of 4 reservations still held"
fi

# The probe: the freeze wrote a trail entry, and it wrote no terminal state. A preemption that
# cancelled would show as a cause, and K-02 would have demanded one.
CAUSE="$(psql_c "SELECT coalesce(cause::text, 'none') FROM \"order\" WHERE id = '$B1'")"
TRAIL="$(psql_c "SELECT count(*) FROM audit WHERE subject = 'order:$B1' AND action LIKE 'scheduler.%'")"
if [ "$CAUSE" = "none" ] && [ "$TRAIL" -ge 2 ]; then
  pass "RB-4c nothing was aborted" "no cause, and both moves are in the trail (SP-K02-3, B-03)"
else
  fail "RB-4c nothing was aborted" "cause: $CAUSE · trail entries: $TRAIL"
fi

# The pod side of the same requirement, where a machine allows it: the kernel's freezer stops every
# process in a cgroup and keeps them. The count before and after is the check — freezing is not
# killing.
if sudo -n true 2>/dev/null && [ "$(stat -fc %T /sys/fs/cgroup 2>/dev/null)" = "cgroup2fs" ]; then
  CG="/sys/fs/cgroup/rb-scheduler-$$"
  if sudo mkdir -p "$CG" 2>/dev/null; then
    sleep 300 &
    VICTIM=$!
    echo "$VICTIM" | sudo tee "$CG/cgroup.procs" >/dev/null 2>&1
    PROCS_BEFORE="$(sudo cat "$CG/cgroup.procs" | grep -c . || echo 0)"
    echo 1 | sudo tee "$CG/cgroup.freeze" >/dev/null
    sleep 1
    FROZEN_FLAG="$(grep -c 'frozen 1' "$CG/cgroup.events" 2>/dev/null || echo 0)"
    PROCS_FROZEN="$(sudo cat "$CG/cgroup.procs" | grep -c . || echo 0)"
    echo 0 | sudo tee "$CG/cgroup.freeze" >/dev/null
    ALIVE=0; kill -0 "$VICTIM" 2>/dev/null && ALIVE=1
    kill "$VICTIM" 2>/dev/null
    sudo rmdir "$CG" 2>/dev/null
    if [ "$PROCS_BEFORE" = "1" ] && [ "$PROCS_FROZEN" = "1" ] && [ "$FROZEN_FLAG" = "1" ] && [ "$ALIVE" = "1" ]; then
      pass "RB-4d the kernel's freezer keeps the processes" "cgroup.freeze=1, one process before and after"
    else
      fail "RB-4d the kernel's freezer keeps the processes" \
        "before $PROCS_BEFORE · frozen $PROCS_FROZEN · flag $FROZEN_FLAG · alive $ALIVE"
    fi
  else
    skip "RB-4d the kernel's freezer" "no writable cgroup root on this machine"
  fi
else
  skip "RB-4d the kernel's freezer" "needs cgroup v2 and passwordless sudo; the CI leg has both"
fi

# =================================================================================================
# AB-RC-6 in the database — three runs, and then a mechanical answer
# =================================================================================================
banner "AB-RC-6 — the profile accumulates, and the third run changes the answer"

MECH_DB=""
for run in 1 2 3; do
  M="$("$BIN" scheduler record --cell probe-c1 --project "$PROJ" --repository monorepo-a \
        --phase check --peak $((2000000000 + run * 100000000)) --runtime-ms $((60000 * run)) | \
      json "import json,sys; print(json.load(sys.stdin)['mechanical'])")"
  MECH_DB="$MECH_DB $run:$M"
done
PROFILE="$("$BIN" scheduler predict --cell probe-c1 --project "$PROJ" --repository monorepo-a \
  --phase check --free 10000000000)"
PROF_VERDICT="$(json "
import json
d = json.loads('''$PROFILE''')
print('%d %d %s' % (d['profile']['Runs'], d['profile']['PeakRSS'], d['verdict']['Mechanical']))")"
if [ "$MECH_DB" = " 1:False 2:False 3:True" ] && [ "$PROF_VERDICT" = "3 2300000000 True" ]; then
  pass "RC-6c three recorded runs make admission mechanical" "$PROF_VERDICT (runs, peak, mechanical)"
else
  fail "RC-6c three recorded runs make admission mechanical" "$MECH_DB · $PROF_VERDICT"
fi

# The peak is the largest of the three and not their mean: admission asks whether a job fits.
if [ "${PROF_VERDICT#* }" = "2300000000 True" ]; then
  pass "RC-6d the profile keeps the largest peak" "2.3 GB of 2.1, 2.2, 2.3 — not their mean"
else
  fail "RC-6d the profile keeps the largest peak" "$PROF_VERDICT"
fi

# =================================================================================================
# AB-V01-4 — under sustained load admission stays fast
# =================================================================================================
banner "AB-V01-4 — reservation beats priority: no death spiral"

for i in $(seq 1 60); do order batch "load-$i" $(( (i % 7) * 90 )) >/dev/null; done
DEPTH="$(psql_c "SELECT count(*) FROM \"order\" WHERE state = 'queued'")"

WORST=0
TOTAL=0
ROUNDS=10
for _ in $(seq 1 $ROUNDS); do
  T0="$(date +%s%N)"
  "$BIN" scheduler queue --cell probe-c1 --limit 20 >/dev/null 2>&1
  MS=$(( ($(date +%s%N) - T0) / 1000000 ))
  TOTAL=$((TOTAL + MS))
  [ "$MS" -gt "$WORST" ] && WORST=$MS
done
AVG=$((TOTAL / ROUNDS))
measure "V01-4a a queue round over $DEPTH queued jobs" "${AVG} ms average, ${WORST} ms worst of $ROUNDS"
# The death spiral is a scheduler that gets slower as the queue grows until admission lags behind
# arrival. A round that stays inside the interactive promise of SP-RB-2 cannot produce one.
if [ "$WORST" -lt 2000 ]; then
  pass "V01-4a admission stays fast under load" "worst round ${WORST} ms, inside interactive's 2 s"
else
  fail "V01-4a admission stays fast under load" "worst round ${WORST} ms"
fi

# =================================================================================================
# AB-E02-5 — the control layer inside 4 cores and 16 GB
# =================================================================================================
banner "AB-E02-5 — what the control layer costs under the load of a cell"

PLANE_RSS_KB="$(ps -o rss= -p "$PLANE" 2>/dev/null | tr -d ' ')"
PG_RSS_KB="$(docker exec "$PG" sh -c "ps -eo rss= 2>/dev/null | awk '{s+=\$1} END {print s}'" 2>/dev/null | tr -d ' ')"
: "${PLANE_RSS_KB:=0}"
: "${PG_RSS_KB:=0}"
TOTAL_MB=$(( (PLANE_RSS_KB + PG_RSS_KB) / 1024 ))
measure "E02-5a the control layer under load" "${TOTAL_MB} MB resident — plane ${PLANE_RSS_KB} kB, database ${PG_RSS_KB} kB"
if [ "$TOTAL_MB" -gt 0 ] && [ "$TOTAL_MB" -lt 16384 ]; then
  pass "E02-5a the control layer fits in 16 GB" "${TOTAL_MB} MB with $DEPTH jobs queued (SP-E02-5)"
else
  fail "E02-5a the control layer fits in 16 GB" "${TOTAL_MB} MB"
fi
CORES="$(nproc)"
measure "E02-5b the machine it was measured on" "$CORES cores; SP-E02-5's ceiling is 4 for the control layer"

result
exit $?
