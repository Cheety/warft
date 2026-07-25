#!/usr/bin/env python3
"""registry.py — the acceptance matrix as a test registry (AP-0.3).

`03-acceptance-matrix.md` is the source of which checks exist. This registry holds their *state*.
The two are kept in step: a check that stands in the matrix but not in the registry is drift, and
drift is an error rather than a silent omission.

  ./registry.py            verify against the matrix, print the report, exit 1 if anything is red
  ./registry.py sync       take new checks over from the matrix, keep the state of known ones

No check is implemented here — only registered. Rows turn green through a run, in the work package
that owns them (A-06, Q-02).

States
  red      not evidenced. The starting state of every check.
  green    evidenced through a run. Set by the work package that owns the check.
  open     deliberately open, and only with a justification pointing at a file in decisions/.
           Section D of the matrix: a row that cannot turn green is not left out, but justified.
"""

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
MATRIX = ROOT / "03-acceptance-matrix.md"
REGISTRY = Path(__file__).resolve().parent / "registry.tsv"

COLUMNS = ["id", "kind", "state", "work_package", "justification"]
STATES = {"red", "green", "open"}
KINDS = {"S", "M", "D", "P"}


def parse_matrix():
    """Every table row that begins with an AB- identifier. Section A has six columns, section B
    five; in both, kind is second to last and the work package last."""
    checks = {}
    for line in MATRIX.read_text(encoding="utf-8").splitlines():
        if not line.startswith("| AB-"):
            continue
        f = [c.strip() for c in line.strip().strip("|").split("|")]
        ident, kind, ap = f[0], f[-2], f[-1]
        if not re.fullmatch(r"AB-[A-Z0-9]+-\d+", ident):
            continue  # the AB-A06-* row points at section A; it is not a check of its own
        if kind not in KINDS:
            sys.exit(f"{ident}: unknown kind {kind!r} — expected one of {sorted(KINDS)}")
        if ident in checks:
            sys.exit(f"{ident}: listed twice in the matrix")
        checks[ident] = {"id": ident, "kind": kind, "work_package": ap}
    return checks


def read_registry():
    if not REGISTRY.exists():
        return {}
    rows = {}
    for n, line in enumerate(REGISTRY.read_text(encoding="utf-8").splitlines(), 1):
        if not line.strip() or line.startswith("#"):
            continue
        f = line.split("\t")
        if n == 1 and f[0] == "id":
            continue
        f += [""] * (len(COLUMNS) - len(f))
        row = dict(zip(COLUMNS, f))
        if row["state"] not in STATES:
            sys.exit(f"{row['id']}: unknown state {row['state']!r}")
        if row["state"] == "open" and not row["justification"]:
            sys.exit(f"{row['id']}: state 'open' needs a justification in decisions/")
        rows[row["id"]] = row
    return rows


def natkey(ident):
    return [int(t) if t.isdigit() else t for t in re.split(r"(\d+)", ident)]


def write_registry(rows):
    out = ["\t".join(COLUMNS)]
    out += ["\t".join(rows[i][c] for c in COLUMNS) for i in sorted(rows, key=natkey)]
    REGISTRY.write_text("\n".join(out) + "\n", encoding="utf-8")


def sync():
    matrix, have = parse_matrix(), read_registry()
    added = sorted(set(matrix) - set(have), key=natkey)
    gone = sorted(set(have) - set(matrix), key=natkey)
    rows = {}
    for ident, m in matrix.items():
        old = have.get(ident, {})
        rows[ident] = {**m,
                       "state": old.get("state", "red"),
                       "justification": old.get("justification", "")}
    write_registry(rows)
    for i in added:
        print(f"  + {i}")
    for i in gone:
        print(f"  - {i}  (no longer in the matrix; state dropped)")
    print(f"{len(rows)} checks registered · {len(added)} added · {len(gone)} removed")


def report():
    matrix, rows = parse_matrix(), read_registry()
    drift = sorted(set(matrix) ^ set(rows), key=natkey)
    if drift:
        print("Registry and matrix disagree. Run `make acceptance-sync`.")
        for i in drift:
            print(f"  {'only in matrix' if i in matrix else 'only in registry'}: {i}")
        return 2

    green = [r for r in rows.values() if r["state"] == "green"]
    red = [r for r in rows.values() if r["state"] == "red"]
    open_ = [r for r in rows.values() if r["state"] == "open"]

    print(f"Acceptance registry — {len(rows)} checks\n")
    print(f"  green  {len(green):4}")
    print(f"  red    {len(red):4}")
    print(f"  open   {len(open_):4}   deliberately open, justified in decisions/")
    kinds = {k: sum(1 for r in rows.values() if r["kind"] == k) for k in sorted(KINDS)}
    print("\n  by kind  " + "   ".join(f"{k} {n}" for k, n in kinds.items())
          + "     S script · M measurement · D drill · P probe")
    for r in sorted(open_, key=lambda r: natkey(r["id"])):
        print(f"\n  open: {r['id']} ({r['work_package']}) -> {r['justification']}")

    if red:
        print(f"\nNot accepted. {len(red)} of {len(rows)} checks are red.")
        return 1
    print("\nEvery check is green or justified as open.")
    return 0


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "sync":
        sync()
    else:
        sys.exit(report())
