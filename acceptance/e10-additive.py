#!/usr/bin/env python3
"""e10-additive.py — the schema linter SP-E10-3 asks for (AP-2.1).

Additive fields only. Field numbers die, they are never reassigned — that is the migration rule
from V-05, enforced here rather than remembered. The linter compares two revisions of a schema and
rejects every change that is not an addition:

  - a field number that disappears without being reserved (it must die, not vanish)
  - a field number whose name, type or cardinality changed (a number is never repurposed)
  - a reserved number that becomes a field again (the dead stay dead)
  - a reservation that is lifted (else a number could be laundered back over two revisions —
    checking every step against its predecessor is only transitive if graves are permanent)
  - a removed message, enum, enum value, service or rpc, or a changed rpc signature
  - a changed package name (it renames every type on the wire)

Enum value numbers follow the same rules as field numbers.

The parser covers the subset of proto3 the contract uses and treats everything outside it as an
error, never as something to skip — a linter that guesses at what it reads would approve what it
did not understand.

  ./e10-additive.py OLD.proto NEW.proto    compare; exit 0 additive, 1 violations, 2 parse error
  ./e10-additive.py dump FILE.proto        print the parsed model as TSV, for other checks to read
"""

import re
import sys


# ---------------------------------------------------------------------------
# Tokenizer
# ---------------------------------------------------------------------------

TOKEN = re.compile(
    r'"(?:[^"\\]|\\.)*"'      # string literal
    r"|[A-Za-z_][A-Za-z0-9_.]*"  # identifier, possibly dotted
    r"|\d+"                   # number
    r"|[{}();=,<>]"           # punctuation
)


def tokenize(text, path):
    text = re.sub(r"//[^\n]*", " ", text)
    text = re.sub(r"/\*.*?\*/", " ", text, flags=re.S)
    tokens, pos = [], 0
    for m in TOKEN.finditer(text):
        between = text[pos : m.start()]
        if between.strip():
            sys.exit(f"{path}: cannot tokenize {between.strip()!r}")
        tokens.append(m.group(0))
        pos = m.end()
    if text[pos:].strip():
        sys.exit(f"{path}: cannot tokenize {text[pos:].strip()!r}")
    return tokens


# ---------------------------------------------------------------------------
# Parser — recursive descent over the subset the contract uses
# ---------------------------------------------------------------------------

class Parser:
    def __init__(self, tokens, path):
        self.tokens = tokens
        self.path = path
        self.i = 0
        self.package = None
        self.messages = {}   # fq name -> {"fields": {num: (name, type, card)}, "reserved": set()}
        self.enums = {}      # fq name -> {"values": {num: name}, "reserved": set()}
        self.services = {}   # name -> {rpc name: (in type, in stream, out type, out stream)}

    def fail(self, why):
        sys.exit(f"{self.path}: {why} (near token {self.i}: {self.tokens[self.i:self.i+5]})")

    def peek(self):
        return self.tokens[self.i] if self.i < len(self.tokens) else None

    def take(self, expect=None):
        tok = self.peek()
        if tok is None:
            self.fail("unexpected end of file")
        if expect is not None and tok != expect:
            self.fail(f"expected {expect!r}, found {tok!r}")
        self.i += 1
        return tok

    def parse(self):
        while self.peek() is not None:
            tok = self.take()
            if tok == "syntax":
                self.take("=")
                if self.take() != '"proto3"':
                    self.fail("only proto3 is spoken here")
                self.take(";")
            elif tok == "package":
                if self.package is not None:
                    self.fail("a second package statement")
                self.package = self.take()
                self.take(";")
            elif tok == "import":
                self.take()
                self.take(";")
            elif tok == "option":
                while self.take() != ";":
                    pass
            elif tok == "message":
                self.message(self.take(), prefix="")
            elif tok == "enum":
                self.enum(self.take(), prefix="")
            elif tok == "service":
                self.service(self.take())
            else:
                self.fail(f"unknown top-level construct {tok!r}")
        return self

    def reserved(self):
        """reserved 4, 9 to 11;  or  reserved "old_name";  — names are noted only as parsed."""
        numbers = set()
        while True:
            tok = self.take()
            if tok.startswith('"'):
                pass                       # a reserved name; the rule here is about numbers
            elif tok.isdigit():
                lo = int(tok)
                if self.peek() == "to":
                    self.take("to")
                    hi = int(self.take())
                    numbers.update(range(lo, hi + 1))
                else:
                    numbers.add(lo)
            else:
                self.fail(f"unexpected {tok!r} in reserved")
            tok = self.take()
            if tok == ";":
                return numbers
            if tok != ",":
                self.fail(f"expected ',' or ';' in reserved, found {tok!r}")

    def field(self, first, card, fields, reserved, msg):
        ftype = first
        if ftype == "map":
            self.take("<")
            key = self.take()
            self.take(",")
            val = self.take()
            self.take(">")
            ftype = f"map<{key},{val}>"
        name = self.take()
        self.take("=")
        num = int(self.take())
        self.take(";")
        if num in fields:
            self.fail(f"{msg}: field number {num} used twice")
        if num in reserved:
            self.fail(f"{msg}: field number {num} is reserved and in use at once")
        fields[num] = (name, ftype, card)

    def message(self, name, prefix):
        fq = f"{prefix}{name}"
        if fq in self.messages:
            self.fail(f"message {fq} defined twice")
        fields, reserved = {}, set()
        self.messages[fq] = {"fields": fields, "reserved": reserved}
        self.take("{")
        while True:
            tok = self.take()
            if tok == "}":
                break
            elif tok == "message":
                self.message(self.take(), prefix=f"{fq}.")
            elif tok == "enum":
                self.enum(self.take(), prefix=f"{fq}.")
            elif tok == "reserved":
                reserved.update(self.reserved())
            elif tok == "option":
                while self.take() != ";":
                    pass
            elif tok == "oneof":
                self.take()                # the oneof's name; membership is the cardinality
                self.take("{")
                while self.peek() != "}":
                    self.field(self.take(), "oneof", fields, reserved, fq)
                self.take("}")
            elif tok in ("repeated", "optional"):
                self.field(self.take(), tok, fields, reserved, fq)
            else:
                self.field(tok, "singular", fields, reserved, fq)
        for num in fields:
            if num in reserved:
                self.fail(f"{fq}: field number {num} is reserved and in use at once")

    def enum(self, name, prefix):
        fq = f"{prefix}{name}"
        if fq in self.enums:
            self.fail(f"enum {fq} defined twice")
        values, reserved = {}, set()
        self.enums[fq] = {"values": values, "reserved": reserved}
        self.take("{")
        while True:
            tok = self.take()
            if tok == "}":
                break
            elif tok == "reserved":
                reserved.update(self.reserved())
            elif tok == "option":
                while self.take() != ";":
                    pass
            else:
                self.take("=")
                num = int(self.take())
                self.take(";")
                if num in values:
                    self.fail(f"{fq}: value number {num} used twice")
                values[num] = tok
        for num in values:
            if num in reserved:
                self.fail(f"{fq}: value number {num} is reserved and in use at once")

    def service(self, name):
        if name in self.services:
            self.fail(f"service {name} defined twice")
        rpcs = {}
        self.services[name] = rpcs
        self.take("{")
        while True:
            tok = self.take()
            if tok == "}":
                break
            if tok != "rpc":
                self.fail(f"expected rpc in service {name}, found {tok!r}")
            rpc = self.take()
            self.take("(")
            in_stream = self.peek() == "stream" and bool(self.take())
            in_type = self.take()
            self.take(")")
            self.take("returns")
            self.take("(")
            out_stream = self.peek() == "stream" and bool(self.take())
            out_type = self.take()
            self.take(")")
            if self.peek() == "{":         # an options body; the subset allows an empty one
                self.take("{")
                self.take("}")
            else:
                self.take(";")
            if rpc in rpcs:
                self.fail(f"service {name}: rpc {rpc} defined twice")
            rpcs[rpc] = (in_type, in_stream, out_type, out_stream)


def load(path):
    try:
        text = open(path, encoding="utf-8").read()
    except OSError as e:
        sys.exit(f"{path}: {e}")
    return Parser(tokenize(text, path), path).parse()


# ---------------------------------------------------------------------------
# Comparison — everything that is not an addition is named, nothing is fixed up
# ---------------------------------------------------------------------------

def compare(old, new):
    bad = []

    if old.package != new.package:
        bad.append(f"package renamed: {old.package} -> {new.package}")

    for fq, o in sorted(old.messages.items()):
        n = new.messages.get(fq)
        if n is None:
            bad.append(f"message {fq} removed")
            continue
        for num, (name, ftype, card) in sorted(o["fields"].items()):
            if num in n["fields"]:
                nname, ntype, ncard = n["fields"][num]
                if (name, ftype, card) != (nname, ntype, ncard):
                    bad.append(
                        f"{fq}: field {num} repurposed: "
                        f"{card} {ftype} {name} -> {ncard} {ntype} {nname}"
                    )
            elif num in n["reserved"]:
                pass                       # the field died and its number is a grave
            else:
                bad.append(f"{fq}: field number {num} ({name}) removed without being reserved")
        for num in sorted(o["reserved"]):
            if num in n["fields"]:
                bad.append(f"{fq}: reserved number {num} reassigned to field {n['fields'][num][0]}")
            elif num not in n["reserved"]:
                bad.append(f"{fq}: reservation of number {num} lifted")

    for fq, o in sorted(old.enums.items()):
        n = new.enums.get(fq)
        if n is None:
            bad.append(f"enum {fq} removed")
            continue
        for num, name in sorted(o["values"].items()):
            if num in n["values"]:
                if n["values"][num] != name:
                    bad.append(f"{fq}: value {num} repurposed: {name} -> {n['values'][num]}")
            elif num in n["reserved"]:
                pass
            else:
                bad.append(f"{fq}: value number {num} ({name}) removed without being reserved")
        for num in sorted(o["reserved"]):
            if num in n["values"]:
                bad.append(f"{fq}: reserved number {num} reassigned to value {n['values'][num]}")
            elif num not in n["reserved"]:
                bad.append(f"{fq}: reservation of number {num} lifted")

    for svc, orpcs in sorted(old.services.items()):
        nrpcs = new.services.get(svc)
        if nrpcs is None:
            bad.append(f"service {svc} removed")
            continue
        for rpc, sig in sorted(orpcs.items()):
            if rpc not in nrpcs:
                bad.append(f"service {svc}: rpc {rpc} removed")
            elif nrpcs[rpc] != sig:
                bad.append(f"service {svc}: rpc {rpc} signature changed")

    return bad


def dump(model):
    print(f"package\t{model.package}")
    for fq, m in sorted(model.messages.items()):
        print(f"message\t{fq}")
        for num, (name, ftype, card) in sorted(m["fields"].items()):
            print(f"field\t{fq}\t{num}\t{name}\t{ftype}\t{card}")
        for num in sorted(m["reserved"]):
            print(f"reserved\t{fq}\t{num}")
    for fq, e in sorted(model.enums.items()):
        for num, name in sorted(e["values"].items()):
            print(f"value\t{fq}\t{num}\t{name}")
        for num in sorted(e["reserved"]):
            print(f"reserved\t{fq}\t{num}")
    for svc, rpcs in sorted(model.services.items()):
        for rpc, (it, istream, ot, ostream) in sorted(rpcs.items()):
            print(
                f"rpc\t{svc}\t{rpc}\t{'stream ' if istream else ''}{it}"
                f"\t{'stream ' if ostream else ''}{ot}"
            )


def main(argv):
    if len(argv) == 3 and argv[1] == "dump":
        dump(load(argv[2]))
        return 0
    if len(argv) != 3:
        print(__doc__.strip(), file=sys.stderr)
        return 2
    violations = compare(load(argv[1]), load(argv[2]))
    for v in violations:
        print(f"NOT ADDITIVE  {v}")
    if violations:
        return 1
    print("additive")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
