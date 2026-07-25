#!/usr/bin/env bash
# gh-import.sh — creates labels, milestones and issues from tracker/issues.json.
#
# Requires: gh (authenticated) and jq.
# Usage:    ./gh-import.sh <owner/repo> [--dry-run]
#
# GitHub has no real dependencies between issues; "blocked by" therefore stands in the text and is
# rewritten once to issue numbers after the import (step 4).
#
# See gh-import.py for a jq-free version that already does step 4 and links sub-issues.

set -euo pipefail

REPO="${1:?Usage: ./gh-import.sh owner/repo [--dry-run]}"
DRY="${2:-}"
JSON="$(dirname "$0")/issues.json"

run() { if [ "$DRY" = "--dry-run" ]; then printf '  [dry] %s\n' "$*"; else "$@"; fi; }

# --------------------------------------------------------------------------
# 1  Labels
# --------------------------------------------------------------------------
echo "== Labels"
declare -A LABELS=(
  ["stage/0"]="ededed"  ["stage/1"]="c5def5"  ["stage/2"]="c5def5"  ["stage/3"]="c5def5"
  ["stage/4"]="c5def5"  ["stage/5"]="c5def5"  ["stage/6"]="c5def5"  ["stage/7"]="c5def5"
  ["stage/8"]="c5def5"  ["stage/9"]="c5def5"
  ["panel/T"]="e3e7e4"  ["panel/Q"]="e3e7e4"  ["panel/F"]="e3e7e4"  ["panel/R"]="e3e7e4"
  ["panel/K"]="e3e7e4"  ["panel/V"]="e3e7e4"  ["panel/B"]="e3e7e4"  ["panel/G"]="e3e7e4"
  ["panel/A"]="e3e7e4"  ["panel/E"]="e3e7e4"
  ["effort/S"]="f6f8fa" ["effort/M"]="f6f8fa" ["effort/L"]="f6f8fa" ["effort/XL"]="fbca04"
  ["kind/platform"]="1d76db"      ["kind/image"]="0e8a16"       ["kind/contract"]="5319e7"
  ["kind/catalog"]="006b75"       ["kind/operations"]="b60205"  ["kind/decision"]="d93f0b"
  ["acceptance/blocker"]="b5451e" ["measurement"]="2a5f6e"      ["epic"]="182119"
  ["blocker"]="b60205"            ["gate"]="000000"
)
for l in "${!LABELS[@]}"; do
  run gh label create "$l" --repo "$REPO" --color "${LABELS[$l]}" --force >/dev/null
done
echo "  ${#LABELS[@]} labels"

# --------------------------------------------------------------------------
# 2  Milestones (= stages). A milestone closes when its measurement exists.
# --------------------------------------------------------------------------
echo "== Milestones"
jq -r '[.[] | select(.type=="epic")] | .[] | [.milestone, .body] | @tsv' "$JSON" \
| while IFS=$'\t' read -r title body; do
    if [ "$DRY" = "--dry-run" ]; then
      printf '  [dry] milestone: %s\n' "$title"
    else
      gh api "repos/$REPO/milestones" -f title="$title" -f description="${body:0:250}" >/dev/null 2>&1 \
        || echo "  exists: $title"
    fi
  done

# --------------------------------------------------------------------------
# 3  Issues
# --------------------------------------------------------------------------
echo "== Issues"
COUNT=0
while read -r row; do
  ID=$(jq -r '.id'        <<<"$row")
  TITLE=$(jq -r '.title'  <<<"$row")
  MS=$(jq -r '.milestone' <<<"$row")
  BODY=$(jq -r '.body'    <<<"$row")
  LBL=$(jq -r '.labels | join(",")' <<<"$row")

  FULL="$BODY

<sub>Generated from \`02-work-packages.md\` · identifier \`$ID\` · definition of done see \`04-issues.md\`</sub>"

  if [ "$DRY" = "--dry-run" ]; then
    printf '  [dry] %-8s %s\n' "$ID" "$TITLE"
  else
    gh issue create --repo "$REPO" \
      --title "$TITLE" \
      --body  "$FULL" \
      --label "$LBL" \
      --milestone "$MS" >/dev/null
    printf '  %-8s %s\n' "$ID" "$TITLE"
  fi
  COUNT=$((COUNT+1))
done < <(jq -c '.[]' "$JSON")
echo "  $COUNT issues"

# --------------------------------------------------------------------------
# 4  Manual follow-up (once)
# --------------------------------------------------------------------------
cat <<'NOTE'

Follow-up:
  · Rewrite "Blocked by: AP-x.y" in the texts to issue numbers
    (gh issue list --json number,title answers the mapping).
  · Split the four XL issues, if the tracker knows sub-issues:
    AP-4.2 (13 handles), AP-5.2 (10 procedures), AP-8.2 (6 campaigns), AP-8.3 (3 runners).
  · Hang the ten OP issues off the work package they are due before — they block it.

The first and third of these are already done by gh-import.py.

The order stays E-11: no milestone begins before the previous one has delivered its number.
NOTE
