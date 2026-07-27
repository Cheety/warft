#!/usr/bin/env bash
# g01-decisions.sh — the decision store, checked by a machine (AP-0.1, AB-G01-5).
#
# G-01 splits knowledge into facts, judgements and decisions and holds decisions to a different
# standard than the other two: few, versioned, serial, in Git rather than in the database (SP-G01-5,
# SP-K01-9, SP-V05-1). Those are properties a script can check, and until one does they are a claim
# about the repository rather than a fact about it — which is Q-02 applied to the store that holds
# the rulings.
#
# The module dependency contract from SP-G01-5 ("module A may depend on B and C, not on D") was the
# half of AB-G01-5 that had nothing to check while platform/ was empty. AP-3.1 filled it, so the
# deferral in decisions/module-contract.md expired and decisions/module-dependencies.md took its
# place: the ranks table in that decision is the contract, and acceptance/module-contract.py holds
# the import graph of platform/ against it here. Both halves of the row are now a run.
#
# Exit:  0 = no FAIL
#        1 = at least one FAIL

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DECISIONS="$ROOT/decisions"
PACKAGES="$ROOT/02-work-packages.md"
SCHEMA="$ROOT/contract/schema.sql"

PASS=0; FAIL=0

pass() { printf '  \033[32mPASS\033[0m  %-46s %s\n' "$1" "${2:-}"; PASS=$((PASS+1)); }
fail() { printf '  \033[31mFAIL\033[0m  %-46s %s\n' "$1" "${2:-}"; FAIL=$((FAIL+1)); }
banner() { printf '\n\033[1m%s\033[0m\n' "$1"; }

# The text of one `## Section`, up to the next heading of the same level, with whitespace collapsed.
# Used to tell a heading that exists from a heading that says something.
section() {
  awk -v want="## $2" '
    $0 == want { inside = 1; next }
    /^## / { inside = 0 }
    inside { print }
  ' "$1" | tr -d '[:space:]'
}

banner "G-01 — the decision store (AB-G01-5)"

# --------------------------------------------------------------------------
# 1  The eleven rulings, each with all three parts
#    E-11 is the build order itself, so a missing ruling is not a missing file
#    but a missing reason for the order everything else follows.
# --------------------------------------------------------------------------
MISSING=()
for n in 01 02 03 04 05 06 07 08 09 10 11; do
  [ -f "$DECISIONS/E-$n.md" ] || MISSING+=("E-$n")
done
if [ ${#MISSING[@]} -eq 0 ]; then
  pass "G01-1 eleven rulings present" "E-01 … E-11"
else
  fail "G01-1 eleven rulings present" "missing: ${MISSING[*]}"
fi

INCOMPLETE=()
for f in "$DECISIONS"/E-*.md; do
  [ -f "$f" ] || continue
  for part in Ruling Rationale "Overturned by"; do
    # A heading with nothing under it is the shape of a decision without its content — which is
    # exactly what "carried over verbatim" is meant to prevent.
    [ "$(section "$f" "$part" | wc -c)" -gt 20 ] || INCOMPLETE+=("$(basename "$f"):$part")
  done
done
if [ ${#INCOMPLETE[@]} -eq 0 ]; then
  pass "G01-2 ruling · rationale · overturned by" "all three, with content, in every ruling"
else
  fail "G01-2 ruling · rationale · overturned by" "${INCOMPLETE[*]}"
fi

# --------------------------------------------------------------------------
# 2  The ten open points, each with a due work package that exists
#    §19 leaves these open deliberately. An open point without a due date is a
#    gap that nobody will notice; one naming a work package that does not exist
#    is worse, because it looks like it has a date.
# --------------------------------------------------------------------------
MISSING=(); UNDATED=(); UNKNOWN=()
for n in 1 2 3 4 5 6 7 8 9 10; do
  f="$DECISIONS/OP-$n.md"
  if [ ! -f "$f" ]; then MISSING+=("OP-$n"); continue; fi
  due="$(grep -o 'Due before:\*\* *AP-[0-9]\+\.[0-9]\+' "$f" | grep -o 'AP-[0-9]\+\.[0-9]\+' | head -1)"
  if [ -z "$due" ]; then
    UNDATED+=("OP-$n")
  elif ! grep -q "^### $due " "$PACKAGES"; then
    UNKNOWN+=("OP-$n->$due")
  fi
done
if [ ${#MISSING[@]} -eq 0 ] && [ ${#UNDATED[@]} -eq 0 ] && [ ${#UNKNOWN[@]} -eq 0 ]; then
  pass "G01-3 ten open points, each with a due date" "every due work package exists"
else
  fail "G01-3 ten open points, each with a due date" \
       "missing: ${MISSING[*]:-none}; undated: ${UNDATED[*]:-none}; unknown: ${UNKNOWN[*]:-none}"
fi

# --------------------------------------------------------------------------
# 3  Rulings taken in this repository, rather than carried over from the panels
#    They are decisions like any other and are held to the same shape — a
#    ruling and the condition that voids it. Without the second they are
#    opinions with a date.
# --------------------------------------------------------------------------
BARE=()
for f in "$DECISIONS"/*.md; do
  b="$(basename "$f")"
  case "$b" in E-*|OP-*) continue ;; esac
  [ "$(section "$f" "Ruling" | wc -c)" -gt 20 ]        || BARE+=("$b:Ruling")
  [ "$(section "$f" "Overturned by" | wc -c)" -gt 20 ] || BARE+=("$b:Overturned by")
done
if [ ${#BARE[@]} -eq 0 ]; then
  pass "G01-4 local rulings carry an overturn condition" \
       "$(ls "$DECISIONS"/*.md | grep -vc '/\(E-\|OP-\)') files"
else
  fail "G01-4 local rulings carry an overturn condition" "${BARE[*]}"
fi

# --------------------------------------------------------------------------
# 4  The store is readable on its own
#    SP-V05-1 keeps decisions in Git so they survive losing the database. That
#    is only worth anything if they can be read without the platform: plain
#    text, and cross-references that resolve to files rather than to a service.
# --------------------------------------------------------------------------
BINARY=()
while read -r f; do
  file "$f" 2>/dev/null | grep -q 'text' || BINARY+=("$(basename "$f")")
done < <(find "$DECISIONS" -type f)
if [ ${#BINARY[@]} -eq 0 ]; then
  pass "G01-5 readable without the platform" "$(find "$DECISIONS" -type f | wc -l) files, all text"
else
  fail "G01-5 readable without the platform" "not text: ${BINARY[*]}"
fi

BROKEN=()
for f in "$DECISIONS"/*.md; do
  while read -r link; do
    [ -n "$link" ] || continue
    case "$link" in http*|"#"*) continue ;; esac
    target="${link%%#*}"
    [ -e "$DECISIONS/$target" ] || [ -e "$ROOT/$target" ] || \
      BROKEN+=("$(basename "$f")->$target")
  done < <(grep -o '](\([^)]*\))' "$f" | sed 's/^](//;s/)$//')
done
if [ ${#BROKEN[@]} -eq 0 ]; then
  pass "G01-6 cross-references resolve" "every link is a file, not a service"
else
  fail "G01-6 cross-references resolve" "${BROKEN[*]}"
fi

# --------------------------------------------------------------------------
# 5  The database holds a reference, never the decision
#    SP-K01-9 and V-05: the platform may point at a decision, it may not store
#    one. A column that could hold the text is the whole failure — it makes the
#    database authoritative again and "survives losing the database" false.
# --------------------------------------------------------------------------
if [ ! -f "$SCHEMA" ]; then
  fail "G01-7 the database holds only a reference" "no contract/schema.sql"
else
  COLUMNS="$(awk '/^CREATE TABLE decision_ref/ { inside = 1; next }
                  inside && /^\);/ { exit }
                  inside { print $1 }' "$SCHEMA" | tr -d ' ')"
  if [ -z "$COLUMNS" ]; then
    fail "G01-7 the database holds only a reference" "no decision_ref table in the schema"
  else
    EXTRA=()
    for c in $COLUMNS; do
      case "$c" in id|cell|repo|path|commit|"") ;; *) EXTRA+=("$c") ;; esac
    done
    if [ ${#EXTRA[@]} -eq 0 ]; then
      pass "G01-7 the database holds only a reference" "decision_ref: $(echo $COLUMNS | tr '\n' ' ')"
    else
      fail "G01-7 the database holds only a reference" \
           "decision_ref may hold content: ${EXTRA[*]}"
    fi
  fi
fi

# --------------------------------------------------------------------------
# 6  The module contract, against the modules
#    SP-G01-5's second half. The contract is the ranks table in
#    decisions/module-dependencies.md and the program is platform/; this reads
#    both and compares. It is deliberately the same shape as the registry
#    against the matrix — a package the contract does not name is drift, not a
#    package that happens to be unconstrained.
# --------------------------------------------------------------------------
CONTRACT_OUT="$("$ROOT/acceptance/module-contract.py" 2>&1)"; CONTRACT_RC=$?
if [ "$CONTRACT_RC" -eq 0 ]; then
  pass "G01-8 the module contract holds" "$(printf '%s\n' "$CONTRACT_OUT" | head -1 | sed 's/^ *//')"
else
  pattern='violation: '
  fail "G01-8 the module contract holds" \
       "$(printf '%s\n' "$CONTRACT_OUT" | grep -c "$pattern") against decisions/module-dependencies.md"
  printf '%s\n' "$CONTRACT_OUT" | grep "$pattern" | sed 's/^ *violation: /        /'
fi
printf '%s\n' "$CONTRACT_OUT" | grep -v 'violation: ' | sed 's/^/  /'

# --------------------------------------------------------------------------
banner "Result"
printf '  %d green, %d red\n\n' "$PASS" "$FAIL"
if [ "$FAIL" -gt 0 ]; then
  echo "  The decision store does not hold. A ruling nobody can check is an opinion with a date."
  exit 1
fi
echo "  AB-G01-5 is evidenced by this run, both halves: the decision store above, and the module"
echo "  contract of decisions/module-dependencies.md against the imports of platform/."
exit 0
