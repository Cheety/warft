#!/usr/bin/env python3
"""schema-additive.py — the migration tool SP-V05-2 asks for, and the schema checker behind
AB-K01-5 (AP-2.2).

Two instruments over one parser, because both hold `contract/schema.sql` to rules the database
cannot hold itself to.

check FILE.sql — the intrinsic K-01 rules; every revision must pass on its own:

  - no central counter (SP-K01-2): no sequence, no serial, no identity column — and no identifier
    assigned by the database, because an id with a generation default is the same serialization
    wearing a different name.
  - no secret column (SP-K01-5): column names are read as words, not substrings. A `token` is a
    credential and is rejected; `tokens_cap` is a budget quantity (V-04) and is not. References —
    `authority_ref`, `content_hash`, `payload_ref` — are the lawful form.
  - `cell` and `project` NOT NULL on every table (SP-K01-3, SP-K01-4). A table stands exempt only
    where another MUST of the same specification forces it — the containment cannot point both
    ways (a cell holds projects, projects reference principals), the principal-day pot spans
    projects by SP-V04-5, the halt is deliberately wider than any project by E-08. Each exemption
    below names the requirement that forces it. The list is closed: a new table carries both
    columns, or someone amends this file where the change is visible.
  - the object list (SP-K01-8): every object the specification names has a table.
  - no decision table (SP-K01-9): decisions lie in Git; only `decision_ref` points at them, and
    g01-decisions.sh holds that pointer to repository, path and commit.

OLD.sql NEW.sql — the additive rule. SP-V05-2 runs migrations additively: write the new field,
read both, remove the old one — three releases, because under a rolling update two versions run
at once. The comparison rejects every change to the state contract that is not an addition:

  - a removed table, a removed column, a removed enum type or enum value
  - a column whose type or nullability changed (a name is never repurposed)
  - a removed function, trigger or rule — they carry enforcement (K-02, G-01), so removing one
    removes a rule of the contract

Defaults, checks, references, indexes and function bodies may change: they bind one database,
not the wire between two running versions. AB-V05-2's probes in k01-schema.sh prove the
rejection bites.

dump FILE.sql — the parsed model as TSV, for other checks to read.
exemptions — the exemption lists as TSV, so the database leg checks against the same list.

The parser covers the subset of SQL the contract uses and treats everything outside it as an
error, never as something to skip — the e10-additive.py principle: a linter that guesses at what
it reads would approve what it did not understand.

Exit: 0 clean or additive · 1 violations · 2 cannot read
"""

import re
import sys

# Tables that cannot carry `cell` while the rest of the specification holds. Closed list.
EXEMPT_CELL = {
    "cell": "the identity root: its primary key IS the cell identifier every other table carries",
    "state_transition": "K-02's transition contract as data, identical in every cell and seeded "
                        "by the schema itself; its rows are rules, not objects",
    "lease_parameter": "OP-4's ruled constants for SP-V02-1's pull model, identical in every cell "
                       "and seeded by the schema itself; its rows are rules, not objects",
    "alert": "SP-B03-3's four waking alerts and the displays beside them (decisions/alerts.md), "
             "identical in every cell and seeded by the schema itself; its rows are rules, not "
             "objects",
}

# Tables that cannot carry a NOT NULL `project` while the rest of the specification holds.
EXEMPT_PROJECT = {
    "cell": "a cell contains projects (V-03: a project lies wholly in one cell); the containment "
            "cannot point both ways",
    "project": "the identity root: its primary key IS the project",
    "principal": "project.principal already points the other way; a principal stands above its "
                 "projects and outlives any one of them",
    "identity_link": "T-01: confirmed at first contact, before any project exists; it binds an "
                     "identity to a principal, not work to a project",
    "state_transition": "K-02's transition contract as data; its rows are rules, not objects",
    "lease_parameter": "OP-4's ruled constants (SP-V02-1); its rows are rules, not objects",
    "node": "a node serves every project in its cell; repository -> node is locality (V-02), "
            "not ownership",
    "locality_group": "groups repositories and nodes for placement (SP-V02-4), across the "
                      "projects that share them",
    "halt": "E-08: the halt stops the cell, deliberately wider than any project",
    "decision_ref": "SP-K01-9: decisions rule the platform and lie in Git; this row is a pointer, "
                    "held to repository, path and commit by g01-decisions.sh",
    "budget_pot": "SP-V04-5: the principal-day cap spans projects; pot_scope_project forces "
                  "project NOT NULL on the envelope and project scopes",
    "audit": "B-03: the trail also records platform actions — a halt set, an authority issued — "
             "that no project owns; entries about a job carry its project",
    "skill_version": "F-07: the catalog serves the cell; a project pins a version by content "
                     "hash, it does not own it",
    "container_image": "T-03: content-addressed and shared by every project whose pods run on it",
    "pipeline_version": "T-05: the spine is versioned per cell; projects pin a version",
    "alert": "SP-B03-3: what may wake a human is a property of the platform, not of anyone's "
             "work; its rows are rules, not objects",
    "queue_sample": "decisions/alerts.md, slot 2: the queue is a property of the cell, and its "
                    "depth is the number of jobs of every project waiting in it",
}

# SP-K01-1 and SP-K01-8: the objects, and the table that carries each.
K01_OBJECTS = {
    "envelope": "envelope", "spec": "spec", "order": "order",
    "principal": "principal", "identity_link": "identity_link", "project": "project",
    "attempt": "attempt", "lease": "lease", "outbox_entry": "outbox", "receipt": "receipt",
    "judgment": "judgment", "budget_pot": "budget_pot", "skill@version": "skill_version",
    "image@hash": "container_image", "pipeline@version": "pipeline_version", "node": "node",
    "locality_group": "locality_group", "audit_entry": "audit", "halt": "halt",
}

# SP-K01-5, read as words: singular `token` is a credential, plural `tokens` is the budget
# quantity V-04 counts. `cert_expires` stays: an expiry is a fact about a secret, not one.
SECRET_WORDS = {"secret", "secrets", "password", "passwords", "passwd", "credential",
                "credentials", "token", "apikey", "key", "keys", "biscuit", "bearer", "jwt"}

SERIAL_TYPES = {"smallserial", "serial", "bigserial", "serial2", "serial4", "serial8"}
IDENT = r'(?:"[^"]+"|[A-Za-z_][A-Za-z0-9_]*)'


def fail2(path, why):
    sys.exit(f"{path}: {why}")


def unquote(name):
    return name[1:-1] if name.startswith('"') else name


def split_statements(text, path):
    """Split on top-level semicolons; strip -- comments; protect '...' and $tag$...$tag$."""
    statements, buf = [], []
    i, n = 0, len(text)
    in_string, dollar = False, None
    while i < n:
        c = text[i]
        if dollar is not None:
            if text.startswith(dollar, i):
                buf.append(dollar)
                i += len(dollar)
                dollar = None
            else:
                buf.append(c)
                i += 1
        elif in_string:
            buf.append(c)
            if c == "'":
                if i + 1 < n and text[i + 1] == "'":
                    buf.append("'")
                    i += 2
                    continue
                in_string = False
            i += 1
        elif c == "'":
            in_string = True
            buf.append(c)
            i += 1
        elif c == "$":
            m = re.match(r"\$[A-Za-z_]*\$", text[i:])
            if m:
                dollar = m.group(0)
                buf.append(dollar)
                i += len(dollar)
            else:
                buf.append(c)
                i += 1
        elif text.startswith("--", i):
            j = text.find("\n", i)
            i = n if j < 0 else j
        elif c == ";":
            s = "".join(buf).strip()
            if s:
                statements.append(s)
            buf = []
            i += 1
        else:
            buf.append(c)
            i += 1
    if in_string or dollar is not None:
        fail2(path, "unclosed quote")
    if "".join(buf).strip():
        fail2(path, f"statement without a terminating ';': {''.join(buf).strip()[:60]!r}")
    return statements


def split_top_commas(body):
    parts, buf, depth = [], [], 0
    in_string = False
    i = 0
    while i < len(body):
        c = body[i]
        if in_string:
            buf.append(c)
            if c == "'":
                if i + 1 < len(body) and body[i + 1] == "'":
                    buf.append("'")
                    i += 2
                    continue
                in_string = False
        elif c == "'":
            in_string = True
            buf.append(c)
        elif c == "(":
            depth += 1
            buf.append(c)
        elif c == ")":
            depth -= 1
            buf.append(c)
        elif c == "," and depth == 0:
            parts.append("".join(buf).strip())
            buf = []
        else:
            buf.append(c)
        i += 1
    if "".join(buf).strip():
        parts.append("".join(buf).strip())
    return parts


CONSTRAINT_STARTS = {"CONSTRAINT", "PRIMARY", "FOREIGN", "UNIQUE", "CHECK", "EXCLUDE"}


class Model:
    def __init__(self, path):
        self.path = path
        self.tables = {}   # name -> {"columns": {name: {...}}, "pk": [names]}
        self.enums = {}    # name -> [values]
        self.objects = set()  # (kind, name) for index/function/trigger/rule/sequence
        self.seeds = set()    # tables the schema itself inserts into


def parse_table(model, name, body):
    columns, pk = {}, []
    for item in split_top_commas(body):
        first = re.match(IDENT, item)
        if not first:
            fail2(model.path, f"table {name}: cannot read {item[:40]!r}")
        head = first.group(0).upper()
        if head in CONSTRAINT_STARTS:
            m = re.search(r"\bPRIMARY\s+KEY\s*\(([^)]*)\)", item, re.I)
            if m:
                pk = [unquote(c.strip()) for c in m.group(1).split(",")]
            continue
        m = re.match(rf"({IDENT})\s+({IDENT}(?:\[\])?)\s*(.*)$", item, re.S)
        if not m:
            fail2(model.path, f"table {name}: cannot read column {item[:40]!r}")
        cname, ctype, attrs = unquote(m.group(1)), m.group(2).lower(), m.group(3)
        tail = re.match(IDENT, attrs.strip())
        if tail and tail.group(0).lower() in ("precision", "varying", "with", "without"):
            fail2(model.path, f"table {name}: multi-word type at column {cname} is outside "
                              "the subset the contract uses")
        if cname in columns:
            fail2(model.path, f"table {name}: column {cname} defined twice")
        columns[cname] = {
            "type": ctype,
            "notnull": bool(re.search(r"\bNOT\s+NULL\b", attrs, re.I)
                            or re.search(r"\bPRIMARY\s+KEY\b", attrs, re.I)),
            "identity": bool(re.search(r"\bGENERATED\b.*\bIDENTITY\b", attrs, re.I | re.S)),
            "default": bool(re.search(r"\bDEFAULT\b", attrs, re.I)),
            "generated_id": bool(re.search(r"nextval|gen_random_uuid|uuid_generate", attrs, re.I)),
        }
    for c in pk:
        if c not in columns:
            fail2(model.path, f"table {name}: primary key names unknown column {c}")
        columns[c]["notnull"] = True
    if name in model.tables:
        fail2(model.path, f"table {name} defined twice")
    model.tables[name] = {"columns": columns, "pk": pk}


def load(path):
    try:
        text = open(path, encoding="utf-8").read()
    except OSError as e:
        sys.exit(f"{path}: {e}")
    model = Model(path)
    for s in split_statements(text, path):
        if re.fullmatch(r"(BEGIN|COMMIT)", s, re.I):
            continue
        m = re.match(rf"CREATE\s+TYPE\s+({IDENT})\s+AS\s+ENUM\s*\((.*)\)$", s, re.I | re.S)
        if m:
            name = unquote(m.group(1))
            if name in model.enums:
                fail2(path, f"enum {name} defined twice")
            model.enums[name] = re.findall(r"'([^']*)'", m.group(2))
            continue
        m = re.match(rf"CREATE\s+TABLE\s+({IDENT})\s*\(", s, re.I)
        if m:
            body = s[s.index("(") + 1:s.rindex(")")]
            parse_table(model, unquote(m.group(1)), body)
            continue
        m = re.match(rf"CREATE\s+(?:UNIQUE\s+)?INDEX\s+({IDENT})\b", s, re.I)
        if m:
            model.objects.add(("index", unquote(m.group(1))))
            continue
        m = re.match(rf"CREATE\s+(?:OR\s+REPLACE\s+)?FUNCTION\s+({IDENT})\s*\(", s, re.I)
        if m:
            model.objects.add(("function", unquote(m.group(1))))
            continue
        m = re.match(rf"CREATE\s+TRIGGER\s+({IDENT})\b", s, re.I)
        if m:
            model.objects.add(("trigger", unquote(m.group(1))))
            continue
        m = re.match(rf"CREATE\s+RULE\s+({IDENT})\b", s, re.I)
        if m:
            model.objects.add(("rule", unquote(m.group(1))))
            continue
        m = re.match(rf"CREATE\s+SEQUENCE\s+({IDENT})\b", s, re.I)
        if m:
            model.objects.add(("sequence", unquote(m.group(1))))
            continue
        m = re.match(rf"INSERT\s+INTO\s+({IDENT})\b", s, re.I)
        if m:
            model.seeds.add(unquote(m.group(1)))
            continue
        fail2(path, f"unknown statement {s[:60]!r}")
    return model


# ---------------------------------------------------------------------------
# check — the intrinsic K-01 rules
# ---------------------------------------------------------------------------

def check(model):
    bad = []

    for kind, name in sorted(model.objects):
        if kind == "sequence":
            bad.append(f"counter: sequence {name} — a central counter (SP-K01-2)")
    for tname, t in sorted(model.tables.items()):
        for cname, c in sorted(t["columns"].items()):
            if c["type"] in SERIAL_TYPES:
                bad.append(f"counter: {tname}.{cname} is {c['type']} — a central counter "
                           "(SP-K01-2)")
            if c["identity"]:
                bad.append(f"counter: {tname}.{cname} is an identity column — a central counter "
                           "(SP-K01-2)")
            if c["generated_id"]:
                bad.append(f"counter: {tname}.{cname} is assigned by the database — identifiers "
                           "come from the producer (SP-K01-2)")
            if set(cname.lower().split("_")) & SECRET_WORDS:
                bad.append(f"secret: {tname}.{cname} — no secret as a column, references only "
                           "(SP-K01-5)")
        for col, exempt, req in (("cell", EXEMPT_CELL, "SP-K01-3"),
                                 ("project", EXEMPT_PROJECT, "SP-K01-4")):
            if tname in exempt:
                continue
            if col not in t["columns"]:
                bad.append(f"{col}: table {tname} does not carry {col} ({req})")
            elif not t["columns"][col]["notnull"]:
                bad.append(f"{col}: {tname}.{col} is nullable — NOT NULL on every table ({req})")

    for obj, tname in sorted(K01_OBJECTS.items()):
        if tname not in model.tables:
            bad.append(f"object: {obj} has no table ({tname} expected, SP-K01-8)")

    if "decision" in model.tables:
        bad.append("decision: a decision table — decisions lie in Git, the database holds a "
                   "reference (SP-K01-9)")
    if "decision_ref" not in model.tables:
        bad.append("decision: decision_ref is missing (SP-K01-9)")

    return bad


# ---------------------------------------------------------------------------
# compare — everything that is not an addition is named, nothing is fixed up
# ---------------------------------------------------------------------------

GUARDED_KINDS = {"function", "trigger", "rule"}


def compare(old, new):
    bad = []

    for tname, o in sorted(old.tables.items()):
        n = new.tables.get(tname)
        if n is None:
            bad.append(f"table {tname} removed")
            continue
        for cname, oc in sorted(o["columns"].items()):
            nc = n["columns"].get(cname)
            if nc is None:
                bad.append(f"{tname}: column {cname} removed")
            elif oc["type"] != nc["type"]:
                bad.append(f"{tname}: column {cname} retyped: {oc['type']} -> {nc['type']}")
            elif oc["notnull"] != nc["notnull"]:
                o_null, n_null = ("NOT NULL" if oc["notnull"] else "nullable",
                                  "NOT NULL" if nc["notnull"] else "nullable")
                bad.append(f"{tname}: column {cname} repurposed: {o_null} -> {n_null}")

    for ename, values in sorted(old.enums.items()):
        n = new.enums.get(ename)
        if n is None:
            bad.append(f"enum {ename} removed")
            continue
        for v in values:
            if v not in n:
                bad.append(f"enum {ename}: value {v} removed")

    for kind, name in sorted(old.objects):
        if kind in GUARDED_KINDS and (kind, name) not in new.objects:
            bad.append(f"{kind} {name} removed — it carries a rule of the contract")

    return bad


# ---------------------------------------------------------------------------
# dump / exemptions — for other checks to read
# ---------------------------------------------------------------------------

def dump(model):
    for tname, t in sorted(model.tables.items()):
        print(f"table\t{tname}")
        for cname, c in sorted(t["columns"].items()):
            print(f"column\t{tname}\t{cname}\t{c['type']}"
                  f"\t{'notnull' if c['notnull'] else 'nullable'}"
                  f"\t{'default' if c['default'] else '-'}")
        if t["pk"]:
            print(f"pk\t{tname}\t{','.join(t['pk'])}")
    for ename, values in sorted(model.enums.items()):
        for v in values:
            print(f"enum\t{ename}\t{v}")
    for kind, name in sorted(model.objects):
        print(f"object\t{kind}\t{name}")
    for tname in sorted(model.seeds):
        print(f"seed\t{tname}")


def exemptions():
    for tname, reason in sorted(EXEMPT_CELL.items()):
        print(f"cell\t{tname}\t{reason}")
    for tname, reason in sorted(EXEMPT_PROJECT.items()):
        print(f"project\t{tname}\t{reason}")


def main(argv):
    if len(argv) == 2 and argv[1] == "exemptions":
        exemptions()
        return 0
    if len(argv) == 3 and argv[1] == "dump":
        dump(load(argv[2]))
        return 0
    if len(argv) == 3 and argv[1] == "check":
        model = load(argv[2])
        violations = check(model)
        for v in violations:
            print(f"VIOLATION  {v}")
        if violations:
            return 1
        print(f"K-01 holds: {len(model.tables)} tables, "
              f"{sum(len(t['columns']) for t in model.tables.values())} columns, "
              f"{len(model.enums)} enums, 0 violations")
        return 0
    if len(argv) == 3:
        violations = compare(load(argv[1]), load(argv[2]))
        for v in violations:
            print(f"NOT ADDITIVE  {v}")
        if violations:
            return 1
        print("additive")
        return 0
    print(__doc__.strip(), file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv))
