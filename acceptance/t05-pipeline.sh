#!/usr/bin/env bash
# t05-pipeline.sh — the pipeline machine, measured on a node (AP-3.4).
#
#   acceptance/t05-pipeline.sh          the four sources, then one boot of the built image
#   acceptance/t05-pipeline.sh host     only the sources — no machine, for a working tree
#   acceptance/t05-pipeline.sh probe    in the machine: the spine, the places, the end of the loop
#
# Three rows hang on this run:
#
#   AB-T05-1  S  fixed spine — every job passes through the same seven steps
#   AB-T05-2  P  movable joints — only the seven named places differ per job
#   AB-T05-3  S  end of the loop — after `n` rounds a reply with diff, logs, assessment
#
# The host side checks four sources against the program, and it checks them the way
# acceptance/t04-runner.sh checks R-A's two tables: a name or a number that moved in one place and
# not in the other is drift, and drift fails the run before the machine is booted. A boot that
# measured a program the specification does not describe would measure nothing.
#
#   SP-T05-1's seven phases        against the spine in platform/internal/runner/pipeline.go
#   SP-T05-2's seven places        against runner.PlaceNames
#   decisions/OP-2.md's ceilings   against platform/internal/runner/op2-rounds.tsv
#   contract/schema.sql's enum     against runner.EvidenceClasses
#
# Nothing in the machine is simulated. The pods are runc containers on btrfs snapshots; the phase
# logs are read out of the reports the pods wrote; the deliberately unsolvable job spends the whole
# ruled number of rounds against a check that cannot pass, and the diff, the log and the assessment
# it replies with are read off the disk it left them on.
#
# Exit:  0 = the three rows are evidenced by this run
#        1 = they are not

set -uo pipefail

MODE="${1:-drive}"
HERE="$(cd "$(dirname "$0")" && pwd)"

# =================================================================================================
# Host side — the four sources against the program.
# =================================================================================================
if [ "$MODE" = drive ] || [ "$MODE" = host ]; then
  ROOT="$(cd "$HERE/.." && pwd)"
  SPEC="$ROOT/01-specification.md"
  RULING="$ROOT/decisions/OP-2.md"
  ROUNDS="$ROOT/platform/internal/runner/op2-rounds.tsv"
  PIPELINE_GO="$ROOT/platform/internal/runner/pipeline.go"
  CATALOG="$ROOT/platform/internal/runner/t05-pipelines.json"
  SCHEMA="$ROOT/contract/schema.sql"
  VM="$ROOT/image/vm.sh"

  PASS=0; FAIL=0
  pass() { printf '  \033[32mPASS\033[0m  %-46s %s\n' "$1" "${2:-}"; PASS=$((PASS+1)); }
  fail() { printf '  \033[31mFAIL\033[0m  %-46s %s\n' "$1" "${2:-}"; FAIL=$((FAIL+1)); }

  printf '\n\033[1mT-05 — the pipeline, against the four sources it is written from\033[0m\n\n'

  # ---- SP-T05-1: the spine ---------------------------------------------------------------------
  # The panel's own sentence: "A fixed spine, the same for all jobs: `prepare` → `plan` → …". Every
  # backticked word of that requirement, in the order it stands there.
  SPEC_SPINE="$(awk '/^\*\*SP-T05-1/,/^$/' "$SPEC" | grep -o '`[a-z]*`' | tr -d '`' | tr '\n' ' ')"
  # The program's: the one array the definition may not be changed through.
  CODE_SPINE="$(sed -n 's/^var spine = \[7\]Phase{\(.*\)}$/\1/p' "$PIPELINE_GO" \
    | tr ',' '\n' | sed 's/^ *Phase//; s/^ *//; s/ *$//' | tr 'A-Z' 'a-z' | tr '\n' ' ')"
  if [ -n "$SPEC_SPINE" ] && [ "$SPEC_SPINE" = "$CODE_SPINE" ]; then
    pass "T05-1a the spine is SP-T05-1's, in order" "$SPEC_SPINE"
  else
    fail "T05-1a the spine is SP-T05-1's, in order" "panel: ${SPEC_SPINE:-none} | code: ${CODE_SPINE:-none}"
  fi

  # ---- SP-T05-2: the seven places ---------------------------------------------------------------
  # The requirement names them in prose, separated by semicolons. Seven clauses, and each of them has
  # to have a place in the program that answers to it.
  CLAUSES="$(awk '/^\*\*SP-T05-2/,/^$/' "$SPEC" | tr '\n' ' ' | sed 's/.*only at these places: //' \
    | tr ';' '\n' | sed 's/^ *//; s/ *$//; s/\.$//' | grep -c .)"
  CODE_PLACES="$(awk '/^var PlaceNames = \[7\]string\{/,/^\}/' "$PIPELINE_GO" \
    | grep -o '"[a-z_]*"' | tr -d '"' | tr '\n' ' ')"
  CODE_COUNT="$(echo "$CODE_PLACES" | wc -w)"
  if [ "$CLAUSES" = "7" ] && [ "$CODE_COUNT" = "7" ]; then
    pass "T05-2a seven places in the panel, seven in the code" "$CODE_PLACES"
  else
    fail "T05-2a seven places in the panel, seven in the code" "panel: $CLAUSES clauses | code: $CODE_COUNT places"
  fi
  # The join between the prose and the identifiers: one word out of each clause that the matching
  # place name has to carry. Without it the count above would be satisfied by seven of anything.
  for word in image plan paths check round acceptance snapshot; do
    if grep -q "$word" <<< "$CODE_PLACES"; then
      pass "T05-2b place \"$word\" has a name in the code" "$(tr ' ' '\n' <<< "$CODE_PLACES" | grep -m1 "$word")"
    else
      fail "T05-2b place \"$word\" has a name in the code" "no place name carries \"$word\""
    fi
  done

  # ---- decisions/OP-2.md against op2-rounds.tsv --------------------------------------------------
  # The ruling's table: | `tiny` | 1 | 0.1 CPU · 128 MB requested |
  ruled_ceiling() { awk -F'|' -v c="$1" '$2 ~ ("`" c "`") && $3 ~ /^ *[0-9]+ *$/ { gsub(/ /,"",$3); print $3; exit }' "$RULING"; }
  file_ceiling()  { awk -F'\t' -v c="$1" '$1==c {print $2}' "$ROUNDS"; }
  for class in tiny small medium large; do
    R="$(ruled_ceiling "$class")"; F="$(file_ceiling "$class")"
    if [ -n "$R" ] && [ "$R" = "$F" ]; then
      pass "T05-3a $class's ceiling is the ruled one" "$F rework round(s)"
    else
      fail "T05-3a $class's ceiling is the ruled one" "ruling: ${R:-none} | file: ${F:-none}"
    fi
  done
  # The ruling is a ruling and not still a placeholder — OP-2 was due before this work package.
  if grep -q '^\*\*Status:\*\* ruled' "$RULING"; then
    pass "T05-3b OP-2 is ruled, not open" "$(sed -n 's/^\*\*Status:\*\* ruled · \(.*\)$/\1/p' "$RULING" | head -1)"
  else
    fail "T05-3b OP-2 is ruled, not open" "decisions/OP-2.md is still a placeholder; AP-3.4 may not start (V-05)"
  fi
  # §19's number is the definition's default, and the definition is the human's file.
  CATALOG_ROUNDS="$(sed -n 's/.*"rework_rounds": \([0-9]*\).*/\1/p' "$CATALOG" | head -1)"
  if [ "$CATALOG_ROUNDS" = "3" ]; then
    pass "T05-3c default@1 carries §19's three" "rework_rounds = $CATALOG_ROUNDS"
  else
    fail "T05-3c default@1 carries §19's three" "the catalog says ${CATALOG_ROUNDS:-none}"
  fi

  # ---- contract/schema.sql's evidence classes ---------------------------------------------------
  # Place six names an evidence class, and a delivery claims it (Q-02). The list in the contract
  # module is a copy of the state contract's enum, so the two are held together here.
  SCHEMA_EVIDENCE="$(awk "/CREATE TYPE evidence_class/,/;/" "$SCHEMA" | grep -o "'[a-z.]*'" | tr -d "'" | sort | tr '\n' ' ')"
  CODE_EVIDENCE="$(awk '/^var EvidenceClasses = \[\]string\{/,/^\}/' "$PIPELINE_GO" \
    | grep -o '"[a-z.]*"' | tr -d '"' | sort | tr '\n' ' ')"
  if [ -n "$SCHEMA_EVIDENCE" ] && [ "$SCHEMA_EVIDENCE" = "$CODE_EVIDENCE" ]; then
    pass "T05-2c the acceptance criterion is an evidence class" "$(echo "$SCHEMA_EVIDENCE" | wc -w) classes, both sides"
  else
    fail "T05-2c the acceptance criterion is an evidence class" "schema: ${SCHEMA_EVIDENCE:-none} | code: ${CODE_EVIDENCE:-none}"
  fi

  if [ "$FAIL" -ne 0 ]; then
    printf '\n  %d met, %d not — the sources disagree; a boot would measure a program the panel does not describe\n\n' "$PASS" "$FAIL"
    exit 1
  fi
  printf '\n  %d met, %d not\n' "$PASS" "$FAIL"
  [ "$MODE" = host ] && exit 0

  # ---- the boot ---------------------------------------------------------------------------------
  DISKS="$(mktemp -d)"
  trap 'rm -rf "$DISKS"' EXIT
  printf 'eu-c1'          > "$DISKS/workpod.cell"
  printf '127.0.0.1:8443' > "$DISKS/workpod.control"
  printf 'probe-t05'      > "$DISKS/workpod.locality_group"

  TPM=()
  command -v swtpm >/dev/null 2>&1 && TPM=(--tpm)

  printf '\n\033[1m== boot: the pipeline on a node\033[0m\n'
  if "$VM" --timeout 1800 --role all --memory 4096 --cpus 4 "${TPM[@]}" \
       --file "$DISKS/workpod.cell" --file "$DISKS/workpod.control" --file "$DISKS/workpod.locality_group" \
       --persist-disk "$DISKS/data.raw:workpod-data:6G" \
       --persist-disk "$DISKS/work.raw:workpod-work:6G" \
       "$HERE/t05-pipeline.sh" probe 2>&1 | tee "$DISKS/probe.log"; then
    echo
    echo "AB-T05-1, AB-T05-2 and AB-T05-3 green through this run: every job passed through the same"
    echo "seven steps, nothing but the seven places differed between two of them, and the job that"
    echo "cannot be solved ended after the ruled number of rounds with a diff, logs and an assessment."
    exit 0
  fi
  echo
  echo "the three rows of AP-3.4 stay red."
  exit 1
fi

# =================================================================================================
# Guest side.
# =================================================================================================
PASS=0; FAIL=0
pass() { printf '  \033[32mPASS\033[0m  %-46s %s\n' "$1" "${2:-}"; PASS=$((PASS+1)); }
fail() { printf '  \033[31mFAIL\033[0m  %-46s %s\n' "$1" "${2:-}"; FAIL=$((FAIL+1)); }
banner() { printf '\n\033[1m%s\033[0m\n\n' "$1"; }

WORK=/run/t05
REGISTERED=/run/workpod/registered

start_boot() {
  systemctl mask --runtime getty.target serial-getty@ttyS0.service >/dev/null 2>&1
  systemctl start --no-block multi-user.target
}
wait_file() { local i; for i in $(seq 1 "$2"); do [ -e "$1" ] && return 0; sleep 1; done; return 1; }

# report FIELD LOGFILE — one top-level scalar out of the report `workpod pod run` printed. No jq in
# the image (SP-A02-3), and the report is written one field per line.
report() { sed -n "s/^  \"$1\": \"\{0,1\}\([^\",]*\)\"\{0,1\},\{0,1\}$/\1/p" "$2" | tail -1; }

# spine LOGFILE — the phases of the report, in the order they first occur. That is exactly the
# question SP-T05-1 asks: `check` and `repair` repeat once per rework round, and what has to be the
# same for all jobs is the sequence of distinct steps underneath the repetition.
spine() { sed -n 's/^ *"phase": "\([a-z]*\)".*$/\1/p' "$1" | awk '!seen[$0]++' | tr '\n' ' '; }

# phasecount LOGFILE PHASE — how often one phase happened.
phasecount() { sed -n 's/^ *"phase": "\([a-z]*\)".*$/\1/p' "$1" | grep -c "^$2$"; }

SPINE="prepare plan edit check repair deliver reap "

# job NAME CLASS PLACES COMMAND… — a job stated by hand, with a places object written verbatim.
json_string() { printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g' | sed ':a;N;$!ba;s/\n/\\n/g'; }
job() {
  local name="$1" class="$2" places="$3"; shift 3
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
  "command": [$args]$places
}
EOF
  JOBSEQ=$((JOBSEQ+1))
}
JOBSEQ=1

banner "AP-3.4 — the pipeline on a node"
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
SKEL="$WORK/skeleton"
mkdir -p "$SKEL"/{usr,proc,sys,dev,tmp,run,work,harness,etc,var}
ln -sf usr/bin "$SKEL/bin"; ln -sf usr/sbin "$SKEL/sbin"
ln -sf usr/lib "$SKEL/lib"; ln -sf usr/lib64 "$SKEL/lib64"
printf '{"language":"sh","language_version":"5","system_packages":["coreutils"]}' > "$WORK/req.json"
if IMPORT="$(workpod pod image import --skeleton "$SKEL" --requirements "$WORK/req.json" --layer /usr:/usr 2>&1)"; then
  pass "an image stands in the index" "$(awk -F'\t' '$1=="image"{print $2}' <<< "$IMPORT")"
else
  fail "an image stands in the index" "$IMPORT"
  printf '\n  %d met, %d not\n' "$PASS" "$FAIL"
  exit 1
fi

mkdir -p "$WORK/repo/src" "$WORK/repo/docs"
printf 'one\n' > "$WORK/repo/src/file.txt"
printf 'docs\n' > "$WORK/repo/docs/readme.md"
workpod pod base repo-a --from "$WORK/repo" >/dev/null 2>&1 \
  && pass "a working-copy base stands" "/data/work/bases/repo-a" \
  || fail "a working-copy base stands" "$(workpod pod base repo-a --from "$WORK/repo" 2>&1)"

# =================================================================================================
# AB-T05-1 — the fixed spine
# =================================================================================================
banner "T-05 — a fixed spine, the same for all jobs (AB-T05-1, script)"

# The definition the artifact carries, read back out of it. `pipeline@version` and its content hash
# are what an order records, and the spine is printed underneath them because it is the half of the
# definition that never differs (SP-T05-4).
PIPE="$(workpod pod pipeline 2>&1)"
CATALOG_SPINE="$(awk -F'\t' '$1=="spine"{printf "%s ", $2}' <<< "$PIPE")"
if [ "$CATALOG_SPINE" = "$SPINE" ]; then
  pass "T05-1a the artifact carries the spine" "$CATALOG_SPINE"
else
  fail "T05-1a the artifact carries the spine" "${CATALOG_SPINE:-none}"
fi
DEFHASH="$(awk -F'\t' '$1=="pipeline" && $2=="default@1"{print $3}' <<< "$PIPE")"
if [ -n "$DEFHASH" ]; then
  pass "T05-1b default@1 has a content hash" "$DEFHASH"
else
  fail "T05-1b default@1 has a content hash" "$PIPE"
fi

# Three jobs that could not be more different: one delivers, one cannot be solved, one demands a plan
# nothing can write. All three pass through the same seven steps.
job deliver medium ',
  "places": {"checks": [{"name": "written", "command": ["/bin/sh", "-c", "test -f src/file.txt"], "blocks": true}],
             "acceptance": "tests.existing"}' /bin/sh -c 'echo two >> src/file.txt'
job unsolvable medium ',
  "places": {"checks": [{"name": "impossible", "command": ["/bin/sh", "-c", "exit 1"], "blocks": true}]}' \
  /bin/sh -c 'echo round >> src/file.txt'
job planned medium ',
  "places": {"plan_required": true}' /bin/sh -c 'echo never >> src/file.txt'

for name in deliver unsolvable planned; do
  workpod pod run --job "$WORK/$name.json" --base /data/work/bases/repo-a --reap report \
    >"$WORK/$name.log" 2>&1
  GOT="$(spine "$WORK/$name.log")"
  if [ "$GOT" = "$SPINE" ]; then
    pass "T05-1c \"$name\" passed through the seven steps" "$(report final_state "$WORK/$name.log")/$(report cause "$WORK/$name.log")"
  else
    fail "T05-1c \"$name\" passed through the seven steps" "${GOT:-no phases in the report}"
    grep -m1 -A3 '"phases"' "$WORK/$name.log" | sed 's/^/        /'
  fi
done

# The three states they end in are three different ones, which is what makes the sentence above
# worth checking: the spine is the same although nothing else is.
D_STATE="$(report final_state "$WORK/deliver.log")"
U_STATE="$(report final_state "$WORK/unsolvable.log")"
P_CAUSE="$(report cause "$WORK/planned.log")"
if [ "$D_STATE" = "delivered" ] && [ "$(report evidence "$WORK/deliver.log")" = "tests.existing" ]; then
  pass "T05-1d a passing blocking check delivers" "evidence tests.existing (Q-02)"
else
  fail "T05-1d a passing blocking check delivers" "$D_STATE/$(report evidence "$WORK/deliver.log")"
fi
if [ "$P_CAUSE" = "skill.missing" ]; then
  pass "T05-1e a demanded plan is refused, not invented" "unproven/skill.missing — the planner is AP-5.5's"
else
  fail "T05-1e a demanded plan is refused, not invented" "$(report final_state "$WORK/planned.log")/$P_CAUSE"
fi

# =================================================================================================
# AB-T05-2 — only the seven named places differ per job
# =================================================================================================
banner "T-05 — movable joints, and only the seven (AB-T05-2, probe)"

# Two jobs that differ at three places and nowhere else: the same definition, the same content hash,
# the same spine. That is what "movable at clearly named places" means when it holds.
job moved-a small ',
  "places": {"checks": [{"name": "written", "command": ["/bin/sh", "-c", "test -f src/file.txt"], "blocks": true}],
             "rework_rounds": 1, "acceptance": "types.lint"}' /bin/sh -c 'echo a >> src/file.txt'
workpod pod run --job "$WORK/moved-a.json" --base /data/work/bases/repo-a --reap report >"$WORK/moved-a.log" 2>&1
MOVED="$(workpod pod pipeline --job "$WORK/moved-a.json" 2>&1 | awk -F'\t' '$1=="moved"{print $2}')"
if [ "$(spine "$WORK/moved-a.log")" = "$SPINE" ] \
   && [ "$(report pipeline_hash "$WORK/moved-a.log")" = "$(report pipeline_hash "$WORK/deliver.log")" ]; then
  pass "T05-2a two jobs, one definition, one spine" "moved: $MOVED"
else
  fail "T05-2a two jobs, one definition, one spine" "$(spine "$WORK/moved-a.log") | $(report pipeline_hash "$WORK/moved-a.log")"
fi
# Every place the job moved is one of the seven, and the seven are all the program knows.
PLACES="$(workpod pod pipeline --job "$WORK/moved-a.json" 2>&1 | awk -F'\t' '$1=="place"{printf "%s ", $2}')"
if [ "$PLACES" = "image plan_required paths checks rework_rounds acceptance keep_snapshot_on_failure " ]; then
  pass "T05-2b the job has seven places and no eighth" "$PLACES"
else
  fail "T05-2b the job has seven places and no eighth" "${PLACES:-none}"
fi

# The forbidden actions. Each of these is a job that tries to differ somewhere that is not one of the
# seven, and each must fail — SP-T05-2 is a closed list, and a decoder that shrugged at an unknown
# field would open it by accident.
refuse() {
  local what="$1" file="$2"
  if OUT="$(workpod pod run --job "$file" --base /data/work/bases/repo-a --reap report 2>&1)"; then
    fail "T05-2c $what" "the runner accepted it"
    return
  fi
  pass "T05-2c $what" "$(tail -1 <<< "$OUT" | cut -c1-96)"
}

cat > "$WORK/own-spine.json" <<'EOF'
{
  "order_id": "018f4242-0000-7000-8000-0000000000e1", "attempt": 1, "cell": "eu-c1",
  "project": "018f4242-0000-7000-8000-00000000000b", "platform": "alpine", "class": "medium",
  "requirements": {"language": "sh", "language_version": "5"}, "command": ["/usr/bin/true"],
  "spine": ["prepare", "edit", "deliver"]
}
EOF
refuse "a job may not carry a spine of its own" "$WORK/own-spine.json"

cat > "$WORK/own-place.json" <<'EOF'
{
  "order_id": "018f4242-0000-7000-8000-0000000000e2", "attempt": 1, "cell": "eu-c1",
  "project": "018f4242-0000-7000-8000-00000000000b", "platform": "alpine", "class": "medium",
  "requirements": {"language": "sh", "language_version": "5"}, "command": ["/usr/bin/true"],
  "places": {"network": true}
}
EOF
refuse "a job may not invent an eighth place" "$WORK/own-place.json"

cat > "$WORK/own-pipeline.json" <<'EOF'
{
  "order_id": "018f4242-0000-7000-8000-0000000000e3", "attempt": 1, "cell": "eu-c1",
  "project": "018f4242-0000-7000-8000-00000000000b", "platform": "alpine", "class": "medium",
  "requirements": {"language": "sh", "language_version": "5"}, "command": ["/usr/bin/true"],
  "pipeline_version": "invented@1"
}
EOF
refuse "a job may not pin a pipeline nobody filed" "$WORK/own-pipeline.json"

cat > "$WORK/too-many-rounds.json" <<'EOF'
{
  "order_id": "018f4242-0000-7000-8000-0000000000e4", "attempt": 1, "cell": "eu-c1",
  "project": "018f4242-0000-7000-8000-00000000000b", "platform": "alpine", "class": "tiny",
  "requirements": {"language": "sh", "language_version": "5"}, "command": ["/usr/bin/true"],
  "places": {"rework_rounds": 9}
}
EOF
refuse "a job may not raise its own ceiling" "$WORK/too-many-rounds.json"

# Place three, enforced against the diff rather than against the pod's word for it: a job that may
# only touch src/ and writes into docs/ does not deliver.
job outside medium ',
  "places": {"paths": ["src"],
             "checks": [{"name": "written", "command": ["/bin/sh", "-c", "test -f src/file.txt"], "blocks": true}]}' \
  /bin/sh -c 'echo strayed >> docs/readme.md'
workpod pod run --job "$WORK/outside.json" --base /data/work/bases/repo-a --reap report >"$WORK/outside.log" 2>&1
if [ "$(report final_state "$WORK/outside.log")" = "failed" ] && [ "$(report cause "$WORK/outside.log")" = "goal.wrong" ]; then
  pass "T05-2d a patch outside place three does not deliver" "failed/goal.wrong, measured from the diff"
else
  fail "T05-2d a patch outside place three does not deliver" "$(report final_state "$WORK/outside.log")/$(report cause "$WORK/outside.log")"
fi

# =================================================================================================
# AB-T05-3 — the end of the loop
# =================================================================================================
banner "T-05 — the loop ends, and it ends with a reply (AB-T05-3, script)"

ROUNDS="$(report rounds "$WORK/unsolvable.log")"
ALLOWED="$(report rounds_allowed "$WORK/unsolvable.log")"
U_CAUSE="$(report cause "$WORK/unsolvable.log")"
if [ "$U_STATE" = "unproven" ] && [ "$U_CAUSE" = "unsolvable" ]; then
  pass "T05-3a the unsolvable job ends in a named state" "unproven/unsolvable, not a loop"
else
  fail "T05-3a the unsolvable job ends in a named state" "${U_STATE:-none}/${U_CAUSE:-none}"
fi
if [ "$ROUNDS" = "3" ] && [ "$ALLOWED" = "3" ]; then
  pass "T05-3b it spent the ruled number of rounds" "$ROUNDS of $ALLOWED (decisions/OP-2.md, medium)"
else
  fail "T05-3b it spent the ruled number of rounds" "${ROUNDS:-none} of ${ALLOWED:-none}, the ruling says 3"
fi
# Four checks and three repairs: a round is one repair and the check that judges it, and the first
# check judges the edit rather than a round.
CHECKS="$(phasecount "$WORK/unsolvable.log" check)"
REPAIRS="$(phasecount "$WORK/unsolvable.log" repair)"
if [ "$CHECKS" = "4" ] && [ "$REPAIRS" = "3" ]; then
  pass "T05-3c a round is a repair and the check after it" "$CHECKS checks, $REPAIRS repairs"
else
  fail "T05-3c a round is a repair and the check after it" "$CHECKS checks, $REPAIRS repairs"
fi

# The reply itself: a diff, logs and an assessment. All three on the disk, all three named by the
# report, and the pod they belong to is gone.
PATCHFILE="$(report patch_path "$WORK/unsolvable.log")"
LOGFILE="$(report log_path "$WORK/unsolvable.log")"
if [ -s "$PATCHFILE" ] && grep -q '^+round$' "$PATCHFILE"; then
  pass "T05-3d the reply carries a diff" "$PATCHFILE, $(wc -l < "$PATCHFILE") lines"
else
  fail "T05-3d the reply carries a diff" "${PATCHFILE:-none}"
fi
if [ -s "$LOGFILE" ]; then
  pass "T05-3e the reply carries the logs" "$LOGFILE, $(wc -c < "$LOGFILE") bytes"
else
  fail "T05-3e the reply carries the logs" "${LOGFILE:-none} — the console is on /run and dies with the pod"
fi
ASSESSMENT="$(sed -n 's/^  "assessment": "\(.*\)",\{0,1\}$/\1/p' "$WORK/unsolvable.log")"
if grep -q 'impossible' <<< "$ASSESSMENT" && grep -q 'OP-2' <<< "$ASSESSMENT"; then
  pass "T05-3f the reply carries an assessment" "$(sed 's/\\n/ /g' <<< "$ASSESSMENT" | cut -c1-92)…"
else
  fail "T05-3f the reply carries an assessment" "${ASSESSMENT:-none}"
fi

# Place seven: the working copy of a pod that did not deliver survives it, as a read-only snapshot
# outside the directory the reaper sweeps.
job kept medium ',
  "places": {"keep_snapshot_on_failure": true,
             "checks": [{"name": "impossible", "command": ["/bin/sh", "-c", "exit 1"], "blocks": true}],
             "rework_rounds": 0}' /bin/sh -c 'echo kept >> src/file.txt'
workpod pod run --job "$WORK/kept.json" --base /data/work/bases/repo-a --reap report >"$WORK/kept.log" 2>&1
KEPT="$(report kept_snapshot "$WORK/kept.log")"
if [ -n "$KEPT" ] && [ -f "$KEPT/src/file.txt" ] && grep -q '^kept$' "$KEPT/src/file.txt"; then
  pass "T05-3g place seven keeps the working copy" "$KEPT"
else
  fail "T05-3g place seven keeps the working copy" "${KEPT:-nothing was kept}"
fi
# And it is not in the pods directory, so the next sweep does not take it away again.
if workpod pod reap >/dev/null 2>&1 && [ -f "$KEPT/src/file.txt" ]; then
  pass "T05-3h the reaper leaves a kept snapshot alone" "a sweep ran and $KEPT still stands"
else
  fail "T05-3h the reaper leaves a kept snapshot alone" "$KEPT is gone after a sweep (SP-T04-5)"
fi

printf '\n  %d met, %d not\n\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
