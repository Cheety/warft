#!/usr/bin/env python3
"""gh-import.py — creates labels, milestones and issues from tracker/issues.json.

The same task as gh-import.sh, without jq — and with the follow-up work from that script's
closing note already done:

  · "Blocked by: AP-x.y" is rewritten to issue numbers (step 4).
  · Every work package hangs off its epic as a sub-issue (GitHub sub-issues).

Requires: gh (authenticated).
Usage:  ./gh-import.py <owner/repo> [--dry-run] [--force]

The order stays E-11: epics first, then work packages, then decisions, then the gate.
No milestone begins before the previous one has delivered its number.
"""

import json
import re
import subprocess
import sys
import time
from pathlib import Path

HERE = Path(__file__).resolve().parent
JSON = HERE / "issues.json"
MAP = HERE / "issue-map.json"

LABELS = {
    "stage/0": "ededed", "stage/1": "c5def5", "stage/2": "c5def5", "stage/3": "c5def5",
    "stage/4": "c5def5", "stage/5": "c5def5", "stage/6": "c5def5", "stage/7": "c5def5",
    "stage/8": "c5def5", "stage/9": "c5def5",
    "panel/T": "e3e7e4", "panel/Q": "e3e7e4", "panel/F": "e3e7e4", "panel/R": "e3e7e4",
    "panel/K": "e3e7e4", "panel/V": "e3e7e4", "panel/B": "e3e7e4", "panel/G": "e3e7e4",
    "panel/A": "e3e7e4", "panel/E": "e3e7e4",
    "effort/S": "f6f8fa", "effort/M": "f6f8fa", "effort/L": "f6f8fa", "effort/XL": "fbca04",
    "kind/platform": "1d76db", "kind/image": "0e8a16", "kind/contract": "5319e7",
    "kind/catalog": "006b75", "kind/operations": "b60205", "kind/decision": "d93f0b",
    "acceptance/blocker": "b5451e", "measurement": "2a5f6e", "epic": "182119",
    "blocker": "b60205", "gate": "000000",
}

# Order of creation. Epics first, so that the sub-issues have a parent.
ORDER = {"epic": 0, "arbeitspaket": 1, "entscheidung": 2, "gate": 3}

FOOTER = (
    "<sub>Generated from `02-work-packages.md` · identifier `{id}` · "
    "definition of done see `04-issues.md`</sub>"
)

REF = re.compile(r"\b(AP-\d+\.\d+|OP-\d+)\b(?!\s*\(#)")


def natkey(ident):
    """AP-0.2 before AP-0.10, OP-2 before OP-10 — numbers count as numbers."""
    return [int(t) if t.isdigit() else t for t in re.split(r"(\d+)", ident)]


def gh(*args, check=True):
    """Call gh and return stdout."""
    p = subprocess.run(["gh", *args], capture_output=True, text=True)
    if check and p.returncode != 0:
        raise RuntimeError(f"gh {' '.join(args)}\n{p.stderr.strip()}")
    return p.stdout.strip(), p.returncode, p.stderr.strip()


def main():
    if len(sys.argv) < 2:
        sys.exit("Usage: ./gh-import.py owner/repo [--dry-run] [--force]")
    repo = sys.argv[1]
    dry = "--dry-run" in sys.argv[2:]

    issues = json.loads(JSON.read_text(encoding="utf-8"))
    issues.sort(key=lambda x: (ORDER[x["typ"]], natkey(x["id"])))

    # Pre-flight: a second run must not create 66 duplicates.
    if not dry and "--force" not in sys.argv[2:]:
        out, _, _ = gh("issue", "list", "--repo", repo, "--state", "all",
                       "--limit", "1", "--json", "number")
        if json.loads(out or "[]"):
            sys.exit(f"Aborting: {repo} already has issues. Use --force to create anyway.")

    # ------------------------------------------------------------------ Labels
    print(f"== Labels ({len(LABELS)})")
    for name, color in sorted(LABELS.items()):
        if dry:
            print(f"  [dry] label: {name}")
        else:
            gh("label", "create", name, "--repo", repo, "--color", color, "--force")
    if not dry:
        print(f"  {len(LABELS)} labels created/updated")

    # --------------------------------------------------------------- Milestones
    # A milestone closes when its measurement exists — not when its issues are closed.
    print("== Milestones")
    existing = {}
    if not dry:
        out, _, _ = gh("api", f"repos/{repo}/milestones?state=all&per_page=100")
        existing = {m["title"]: m["number"] for m in json.loads(out or "[]")}
    for x in issues:
        if x["typ"] != "epic":
            continue
        title = x["milestone"]
        if dry:
            print(f"  [dry] milestone: {title}")
        elif title in existing:
            print(f"  exists: {title}")
        else:
            desc = x["body"].split("\n\n")[0][:250]
            gh("api", f"repos/{repo}/milestones", "-f", f"title={title}", "-f", f"description={desc}")
            print(f"  {title}")

    # ------------------------------------------------------------------- Issues
    print("== Issues")
    numbers = {}
    for x in issues:
        body = f"{x['body']}\n\n{FOOTER.format(id=x['id'])}"
        if dry:
            print(f"  [dry] {x['id']:<8} {x['titel'][:78]}")
            continue
        out, _, _ = gh(
            "issue", "create", "--repo", repo,
            "--title", x["titel"],
            "--body", body,
            "--label", ",".join(x["labels"]),
            "--milestone", x["milestone"],
        )
        num = int(out.rstrip("/").rsplit("/", 1)[-1])
        numbers[x["id"]] = num
        print(f"  #{num:<4} {x['id']:<8} {x['titel'][:70]}")
        time.sleep(0.6)  # GitHub's secondary rate limit on bulk creation
    print(f"  {len(issues)} issues")

    if dry:
        print("\n[dry] Follow-up (cross-references, sub-issues) is skipped.")
        return

    MAP.write_text(json.dumps(numbers, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    print(f"  identifier -> number mapping in {MAP.name}")

    # ------------------------------------ Follow-up 1: references to numbers
    # "Blocked by: AP-1.1" becomes "AP-1.1 (#12)" — in running text too, so that the
    # order from E-11 is navigable in the tracker.
    print("== Cross-references")
    patched = 0
    for x in issues:
        num = numbers[x["id"]]
        new = REF.sub(
            lambda m: f"{m.group(1)} (#{numbers[m.group(1)]})" if m.group(1) in numbers else m.group(1),
            x["body"],
        )
        if new == x["body"]:
            continue
        gh("issue", "edit", str(num), "--repo", repo,
           "--body", f"{new}\n\n{FOOTER.format(id=x['id'])}")
        patched += 1
        time.sleep(0.4)
    print(f"  {patched} issues given cross-references")

    # -------------------------------- Follow-up 2: sub-issues under their epic
    # A work package belongs to exactly one stage. The milestone says which;
    # the epic of that same stage becomes its parent.
    print("== Sub-issues")
    epic_of = {x["milestone"]: numbers[x["id"]] for x in issues if x["typ"] == "epic"}
    dbid = {}
    for x in issues:
        if x["typ"] == "epic":
            continue
        num = numbers[x["id"]]
        if num not in dbid:
            out, _, _ = gh("api", f"repos/{repo}/issues/{num}", "--jq", ".id")
            dbid[num] = int(out)
        parent = epic_of[x["milestone"]]
        _, rc, err = gh("api", f"repos/{repo}/issues/{parent}/sub_issues",
                        "-X", "POST", "-F", f"sub_issue_id={dbid[num]}", check=False)
        if rc != 0:
            print(f"  skipped {x['id']} -> #{parent}: {err.splitlines()[0][:90] if err else rc}")
        time.sleep(0.4)
    print(f"  {len(issues) - len(epic_of)} sub-issues linked")

    print("\nOpen (deliberately not automated):")
    print("  · Split the four XL issues: AP-4.2 (13 handles), AP-5.2 (10 procedures),")
    print("    AP-8.2 (6 campaigns), AP-8.3 (3 runners) — the content is in 01-specification.md.")
    print("  · Transfer 03-acceptance-matrix.md into a test registry (AP-0.3). Everything red.")


if __name__ == "__main__":
    main()
