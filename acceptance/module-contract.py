#!/usr/bin/env python3
"""module-contract.py — platform/ against the dependency contract (SP-G01-5, AB-G01-5).

SP-G01-5 wants the module contract *machine-checkable*: "module A may depend on B and C, not on D".
`decisions/module-dependencies.md` is that contract, and this script is the machine that checks it.

The decision file is the source. This script reads the ranks table out of it and holds the import
graph of `platform/` against it, so there is no second place where the same rule is written and no
day on which the two disagree. Drift is an error in both directions, the way it is between
`03-acceptance-matrix.md` and `acceptance/registry.tsv`:

  * an import the table does not permit          → the program broke the contract
  * a package the table does not name            → the contract stopped covering the program
  * a row naming a package that does not exist   → the contract describes something that is gone

Imports are read, not compiled: Go import blocks are parsed as text, so this needs no toolchain, no
module download and no network. That is what lets the decisions leg check the contract in seconds
while the image leg is still building the binary.

  ./module-contract.py            report and verdict; exit 1 on any violation
  ./module-contract.py --quiet    only violations
"""

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DECISION = ROOT / "decisions" / "module-dependencies.md"
PLATFORM = ROOT / "platform"
MODULE_PATH = "github.com/Cheety/warft/platform/"

# The one phrase in the "may depend on" column that is not a list of modules. cmd/workpod is the
# entry point: it is allowed to reach every module, because assembling them is what it is for.
ALL = "every module above"


def parse_contract(text):
    """The ranks table of the decision, as {module: set(permitted imports)}.

    Recognized by its header rather than by position, so adding prose above it cannot silently
    change what is read.
    """
    rows, in_table = {}, False
    for line in text.splitlines():
        line = line.strip()
        if line.startswith("|") and "May depend on" in line:
            in_table = True
            continue
        if in_table:
            if not line.startswith("|"):
                break  # the table ended; the rest of the decision is prose
            cells = [c.strip() for c in line.strip("|").split("|")]
            if len(cells) < 3 or set(cells[0]) <= set("- :"):
                continue  # the |---|---| separator row
            module = strip_code(cells[1])
            if not module:
                continue
            allowed = cells[2]
            if ALL in allowed:
                rows[module] = ALL
            else:
                rows[module] = {m for m in re.findall(r"`([^`]+)`", allowed)}
    return rows


def strip_code(cell):
    m = re.search(r"`([^`]+)`", cell)
    return m.group(1) if m else ""


def packages():
    """Every directory under platform/ that holds Go source, named as it is imported."""
    found = set()
    for f in PLATFORM.rglob("*.go"):
        if "/vendor/" in f.as_posix():
            continue
        found.add(f.parent.relative_to(PLATFORM).as_posix())
    return found


def imports(pkg):
    """The platform's own packages that `pkg` imports.

    Go allows `import "x"` and a parenthesized block; entries may carry an alias or `_`. Only
    quoted strings inside an import statement are read, so a path mentioned in a comment or in a
    string literal elsewhere in the file is not mistaken for a dependency.
    """
    edges = set()
    for f in sorted((PLATFORM / pkg).glob("*.go")):
        in_block = False
        for line in f.read_text(encoding="utf-8").splitlines():
            s = line.strip()
            if in_block:
                if s.startswith(")"):
                    in_block = False
                    continue
            elif s.startswith("import ("):
                in_block = True
                continue
            elif not s.startswith("import "):
                continue
            m = re.search(r'"([^"]+)"', s)
            if m and m.group(1).startswith(MODULE_PATH):
                edges.add(m.group(1)[len(MODULE_PATH):])
    return edges


def main():
    quiet = "--quiet" in sys.argv
    if not DECISION.exists():
        print(f"  no contract at {DECISION.relative_to(ROOT)} — SP-G01-5 has nothing to check")
        return 1

    contract = parse_contract(DECISION.read_text(encoding="utf-8"))
    if not contract:
        print("  the contract in decisions/module-dependencies.md has no ranks table")
        return 1

    present = packages()
    violations = []

    for pkg in sorted(present - set(contract)):
        violations.append(f"{pkg} is not named in the contract — a new module is a row in the table")
    for pkg in sorted(set(contract) - present):
        violations.append(f"the contract names {pkg}, which does not exist in platform/")

    edges = {}
    for pkg in sorted(present & set(contract)):
        allowed = contract[pkg]
        edges[pkg] = sorted(imports(pkg))
        if allowed == ALL:
            continue
        for dep in edges[pkg]:
            if dep not in allowed:
                violations.append(f"{pkg} imports {dep}, which the contract does not permit")

    if not quiet:
        print(f"  {len(present)} packages, {sum(len(v) for v in edges.values())} internal imports")
        for pkg in sorted(edges):
            allowed = "everything" if contract[pkg] == ALL else " ".join(sorted(contract[pkg])) or "nothing"
            print(f"    {pkg:26} -> {' '.join(edges[pkg]) or '(none)':46} may: {allowed}")

    # Prefixed so a caller can pick the verdict lines out of the report without parsing it —
    # acceptance/g01-decisions.sh reports the count and the first of them.
    for v in violations:
        print(f"    violation: {v}")
    return 1 if violations else 0


if __name__ == "__main__":
    sys.exit(main())
