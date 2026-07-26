#!/usr/bin/env python3
"""k04-authority.py — authority as a Biscuit token, and the proof it cannot be widened (AP-2.4).

Four rows rest on this file:

  AB-K04-1  S  the authority is signed and verifiable offline
  AB-K04-2  P  a widening attempt is cryptographically impossible
  AB-K04-3  P  all three gates verify fully, none trusts the origin
  AB-K04-6  P  the revocation list takes effect per project, even with the control plane down

The file is two things at once. The top half is the *attenuation library*: an issuer per cell, a
function that issues an authority, a function that attenuates one (adds conditions, never removes
them), one verifier, and the revocation list the gates consult. The bottom half — under
``__main__`` — is the probe driver: it exercises that library and, for every forbidden action,
names the refusal it expects and proves the refusal happens. Controls prove the lawful action still
passes, so a verifier that refuses everything cannot pass as one that enforces the rule.

Why Biscuit and not a field. As an attached field, "no model raises the authority" is an agreement
between components (SP-K04-8). As a signed token it is a property of cryptography (SP-K04-2): every
hop may add a Biscuit block that adds conditions, and none may remove one, because a token is a key
ratchet — a block is signed with a key the previous block commits to, so a rewritten or self-built
block that claims wider ``targets``, ``level`` or ``budget`` fails signature verification against the
trust anchor. JWT is excluded (no offline attenuation) and Macaroons are excluded (verification would
need a shared standing secret, which B-01 forbids).

The one mechanism that makes widening impossible *without even touching the signature* is Biscuit's
block scoping. The gates' authorization policy trusts facts from the authority block and the
authorizer only; a ``right(...)`` fact injected by a later attenuation block is present in the token
and validly signed, yet the policy never reads it. So a hop cannot grant itself a right it was not
handed, whether by forging a signature (caught by the trust anchor) or by appending a validly signed
block (ignored by scope). The probe demonstrates both.

The stage 2 boundary holds. This is the cryptographic contract and the verification path each gate
will call, not the gates themselves: the control API, the Git proxy and the egress proxy are stage 3
and later. Granting flows, enrollment and the serving of the revocation list are AP-3.x and AP-6.x.
Here stands the verifier, exercised three times with the three gates' distinct contexts, each
verifying fully against the trust anchor and none trusting the token's origin. The trust anchor is an
ephemeral keypair minted per run — there is no production cell yet, so there is no production key to
bake; what the system image carries is the mechanism (contract/authority.md), a public verification
key at a documented path, and the gates need no secret (SP-K04-4).

Requires biscuit-python (pinned in acceptance/k04-authority.sh, which runs this file in a container).

Exit:  0 = no FAIL
       1 = at least one FAIL
"""

import sys
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone

import biscuit_auth as bau

# ---------------------------------------------------------------------------
# T-01 authority levels, widest last. A level permits the operations of every
# level at or below it; this ordering is the only place that relation lives.
# ---------------------------------------------------------------------------
LEVELS = ("public", "linked", "confidential")


# ---------------------------------------------------------------------------
# The attenuation library
# ---------------------------------------------------------------------------
@dataclass
class Issuer:
    """An issuer per cell (SP-K04-1). Holds the signing keypair; the public half is the trust
    anchor the system image carries and every gate verifies against (SP-K04-4)."""

    cell: str
    keypair: bau.KeyPair

    @classmethod
    def new(cls, cell: str) -> "Issuer":
        """Mint an issuer with a fresh Ed25519 keypair. Probes call this — no key is ever committed;
        each run's trust anchor exists only for that run (decisions/signing-key.md discipline)."""
        return cls(cell=cell, keypair=bau.KeyPair())

    @property
    def trust_anchor(self) -> bau.PublicKey:
        """The public verification key. This — and nothing secret — is what a gate needs."""
        return self.keypair.public_key


@dataclass
class Grant:
    """What an authority hands out: a set of (resource, operation) rights, a level, a budget and an
    expiry. This is the readable shape of SP-K04-1's token content before it becomes Biscuit facts."""

    principal: str
    project: str
    level: str
    rights: list  # (resource, operation) pairs — repositories, egress targets, the project itself
    budget: dict  # e.g. {"pod_minutes": 30, "tokens": 100000}
    issued: datetime
    expires: datetime


def issue(issuer: Issuer, grant: Grant) -> bau.Biscuit:
    """Build a signed authority token from a grant (SP-K04-1).

    The authority block carries the grant as facts — ``right(resource, operation)`` for each right,
    plus ``level``, ``budget``, ``principal``, ``project``, ``issuer``, ``cell`` and ``issued`` — and
    one check that the token has not expired. Rights and level live in the authority block precisely
    so that the gates, which trust only the authority block and the authorizer, read them from here
    and nowhere else; that is what makes a later block unable to add a right (see module docstring).
    """
    builder = bau.BiscuitBuilder("")
    for resource, operation in grant.rights:
        builder.add_fact(bau.Fact("right({r}, {o})", {"r": resource, "o": operation}))
    builder.add_fact(bau.Fact("issuer({i})", {"i": f"issuer/{issuer.cell}"}))
    builder.add_fact(bau.Fact("cell({c})", {"c": issuer.cell}))
    builder.add_fact(bau.Fact("principal({p})", {"p": grant.principal}))
    builder.add_fact(bau.Fact("project({p})", {"p": grant.project}))
    builder.add_fact(bau.Fact("level({l})", {"l": grant.level}))
    builder.add_fact(bau.Fact("issued({t})", {"t": grant.issued}))
    for kind, amount in grant.budget.items():
        builder.add_fact(bau.Fact("budget({k}, {a})", {"k": kind, "a": amount}))
    # Short validity, never days (SP-K04-1 "expires", SP-K04-5). Time is infrastructure (SP-K04-7):
    # the gate supplies the current time, the token carries only the deadline.
    builder.add_check(bau.Check("check if time($t), $t < {e}", {"e": grant.expires}))
    return builder.build(issuer.keypair.private_key)


def attenuate(token: bau.Biscuit, code: str, params: dict | None = None) -> bau.Biscuit:
    """Attenuate a token: append a block that adds conditions (SP-K04-2).

    A block may only add checks and facts; it can never delete a check an earlier block placed, and a
    fact it adds is invisible to the gates' policy (block scoping). So this is the only direction the
    library offers — there is no ``widen``. A captain producing subjobs calls exactly this, and that
    is the only way new authorities come into being.
    """
    block = bau.BlockBuilder(code, params or {})
    return token.append(block)


@dataclass
class RevocationList:
    """A revocation list per project (SP-K04-6), distributed on the same line as the egress allowlist
    with the same static stability (SP-V02-3).

    ``revoked`` holds the hex revocation ids of blocks that are no longer valid. ``issued_through`` is
    the watermark the distribution carried: authorities issued at or before it are covered by this
    list. ``reachable`` says whether the control plane can be reached to fetch a fresher list right
    now — the gate never calls the control plane to *verify* (SP-K04-4), it verifies against the last
    list it was handed, so this flag is what "the control plane is down" means at a gate.
    """

    project: str
    sequence: int
    issued_through: datetime
    revoked: set = field(default_factory=set)
    reachable: bool = True


@dataclass
class GateRequest:
    """One effect a gate is about to allow. ``gate`` names it for a message; the rest are the request
    facts the verifier feeds the authorizer. The three gates differ only in these values — the
    verifier below is one function, called three times (SP-K04-3)."""

    gate: str
    operation: str
    resource: str
    project: str


# The three gates that verify at the effect (SP-K04-3). Each builds a request against the same
# verifier; none is a special case in the verification path.
def control_api(project: str) -> GateRequest:
    """The control API admits work and issues leases for a project."""
    return GateRequest("control-api", "control.admit", f"project:{project}", project)


def git_proxy(repo: str, operation: str, project: str) -> GateRequest:
    """The Git proxy checks a read or a push against a repository (K-03, B-02)."""
    return GateRequest("git-proxy", operation, f"repo:{repo}", project)


def egress_proxy(host: str, project: str) -> GateRequest:
    """The egress proxy checks a request against an allowed target (T-04, B-02)."""
    return GateRequest("egress-proxy", "egress", f"host:{host}", project)


class Refused(Exception):
    """A verification refusal. ``cause`` is a short machine word a probe can assert on, so a refusal
    for the wrong reason cannot pass as the right one."""

    def __init__(self, cause: str, detail: str = ""):
        super().__init__(f"{cause}: {detail}" if detail else cause)
        self.cause = cause
        self.detail = detail


def verify(
    token_bytes: bytes,
    trust_anchor: bau.PublicKey,
    request: GateRequest,
    revocations: RevocationList,
    now: datetime,
) -> int:
    """The one verifier, verifying fully (SP-K04-3). Raises ``Refused`` on any failure.

    Every gate runs all of this and trusts nothing before it:

      1. **Signature against the trust anchor.** The token is parsed and its whole block chain checked
         against the issuer's public key. A forged or tampered token — including one whose ``issuer``
         fact *claims* the trusted cell — dies here, because the gate checks the signature, not the
         claim (SP-K04-3 "a gate that trusts the origin is not a gate").
      2. **Revocation and static stability** against the project's last known list (SP-K04-6).
      3. **The gate's policy**, as Datalog the authorizer runs. Rights are read from the authority
         block and the authorizer's own request facts only; an injected right in a later block is not
         in scope, which is where widening dies without a signature ever being questioned.

    Returns the index of the matched allow policy on success.
    """
    # 1 — signature. from_bytes verifies the entire ratchet against the anchor.
    try:
        token = bau.Biscuit.from_bytes(token_bytes, trust_anchor)
    except Exception as exc:  # BiscuitValidationError and its kin — all mean "not this anchor's"
        raise Refused("signature", type(exc).__name__) from exc

    # 2 — revocation, per project, with static stability.
    _check_revocation(token, request, revocations, now)

    # 3 — the gate's policy. The allow rule reads right() from the authority block and request facts;
    # a project-match check binds the token to the request's project so one project's authority cannot
    # act in another. level_permits() is asserted so the level the authority carries is load-bearing.
    authorizer = bau.AuthorizerBuilder(
        """
        time({now});
        request_operation({op});
        request_resource({res});
        request_project({proj});

        // the effect is allowed only if the authority block grants exactly it, for this project
        allow if
          right($res, $op),
          request_resource($res),
          request_operation($op),
          project($proj),
          request_project($proj);
        """,
        {"now": now, "op": request.operation, "res": request.resource, "proj": request.project},
    )
    try:
        return authorizer.build(token).authorize()
    except bau.AuthorizationError as exc:
        raise Refused("policy", str(exc).splitlines()[0]) from exc


def _check_revocation(
    token: bau.Biscuit, request: GateRequest, revocations: RevocationList, now: datetime
) -> None:
    """Revocation per project with static stability (SP-K04-6, SP-V02-3).

    The list is per project: a gate loads the list for the token's project, and a list for another
    project has no bearing. The last known list holds when the control plane is down — a revoked
    block stays refused with no fresh fetch. And a *new* authority is refused while the plane is down:
    one issued past the list's watermark cannot be checked against a current-enough list, and a gate
    that cannot know whether an authority was revoked must not admit it.
    """
    if revocations.project != request.project:
        # The gate was handed the wrong project's list. Fail closed rather than verify against a list
        # that does not govern this token.
        raise Refused("revocation.wrong_project",
                      f"list for {revocations.project}, token for {request.project}")

    revoked = set(token.revocation_ids) & revocations.revoked
    if revoked:
        raise Refused("revoked", f"block revocation id on the list for {revocations.project}")

    if not revocations.reachable:
        issued = _issued_at(token)
        if issued is None or issued > revocations.issued_through:
            # Static stability: the control plane is down and this authority is newer than the last
            # list the gate holds. New authorities are refused (SP-V02-3).
            raise Refused("new_authority_while_stale",
                          "issued past the last known revocation list, control plane down")


def _issued_at(token: bau.Biscuit) -> datetime | None:
    """The authority's ``issued`` time, read back through the authorizer so it comes from the signed
    authority block and not from any attenuation block."""
    rule = bau.Rule("issued_at($t) <- issued($t)")
    facts = bau.AuthorizerBuilder("").build(token).query(rule)
    for fact in facts:
        value = fact.terms[0]
        if isinstance(value, datetime):
            return value
    return None


# ---------------------------------------------------------------------------
# The probe driver
# ---------------------------------------------------------------------------
class Probes:
    """Counts and prints PASS/FAIL, coloured like the other acceptance scripts."""

    def __init__(self):
        self.passed = 0
        self.failed = 0

    def banner(self, text):
        print(f"\n\033[1m{text}\033[0m")

    def ok(self, label, detail=""):
        print(f"  \033[32mPASS\033[0m  {label:<54}{detail}")
        self.passed += 1

    def bad(self, label, detail=""):
        print(f"  \033[31mFAIL\033[0m  {label:<54}{detail}")
        self.failed += 1

    def refused(self, label, expected_cause, thunk):
        """The forbidden action must fail, and fail for the named cause. A refusal for another reason
        is a broken fixture wearing the colour of enforcement (the k02-state.sh discipline)."""
        try:
            thunk()
        except Refused as exc:
            if exc.cause == expected_cause:
                self.ok(label, f"refused: {exc.cause}")
            else:
                self.bad(label, f"refused, but by '{exc.cause}', expected '{expected_cause}'")
            return
        self.bad(label, f"was allowed; expected refusal '{expected_cause}'")

    def allowed(self, label, thunk, detail=""):
        """The lawful action must pass — the control that keeps a refuse-everything verifier honest."""
        try:
            thunk()
            self.ok(label, detail)
        except Refused as exc:
            self.bad(label, f"refused '{exc.cause}': {exc.detail}")


def main() -> int:
    p = Probes()
    now = datetime(2026, 7, 26, 10, 0, 0, tzinfo=timezone.utc)
    expires = now + timedelta(hours=2)

    # An issuer per cell, and a confidential authority for one principal in one project, granting
    # rights across all three gate resource types so one token can be presented at each gate.
    issuer = Issuer.new("eu-c1")
    other_issuer = Issuer.new("eu-c1")  # a second cell key: the "origin" a forgery would claim
    grant = Grant(
        principal="p-1",
        project="proj-a",
        level="confidential",
        rights=[
            ("project:proj-a", "control.admit"),
            ("repo:alpha", "git.read"),
            ("repo:alpha", "git.write"),
            ("host:api.example.com", "egress"),
        ],
        budget={"pod_minutes": 30, "tokens": 100000},
        issued=now,
        expires=expires,
    )
    authority = issue(issuer, grant)
    anchor = issuer.trust_anchor
    fresh = lambda: RevocationList("proj-a", 1, now, set(), reachable=True)

    # -----------------------------------------------------------------------
    # AB-K04-1 — signed and verifiable offline
    # -----------------------------------------------------------------------
    p.banner("K-04 — the authority is signed and verifiable offline (AB-K04-1, script)")

    # Offline means: the public key and the token bytes, and nothing else — no network, no control
    # plane, no shared secret. The whole verification below is a pure function of those two inputs.
    p.allowed("K04-1a a fresh authority verifies at a gate",
              lambda: verify(authority.to_bytes(), anchor, git_proxy("alpha", "git.read", "proj-a"),
                             fresh(), now),
              "public key + bytes only, no I/O")

    reparsed = bau.Biscuit.from_bytes(authority.to_bytes(), anchor)
    if len(reparsed.revocation_ids) == authority.block_count():
        p.ok("K04-1b every block carries a revocation id",
             f"{authority.block_count()} block(s), {len(reparsed.revocation_ids)} id(s)")
    else:
        p.bad("K04-1b every block carries a revocation id",
              f"{authority.block_count()} blocks, {len(reparsed.revocation_ids)} ids")

    p.refused("K04-1c a token no anchor signed does not verify", "signature",
              lambda: verify(authority.to_bytes(), other_issuer.trust_anchor,
                             git_proxy("alpha", "git.read", "proj-a"), fresh(), now))

    # -----------------------------------------------------------------------
    # AB-K04-2 — a widening attempt is cryptographically impossible
    # -----------------------------------------------------------------------
    p.banner("K-04 — widening is cryptographically impossible (AB-K04-2, probe)")

    # A captain attenuates: narrow the confidential authority to read-only on one repo. This is the
    # only way a new authority comes into being, and it can only remove capability.
    subjob = attenuate(
        authority,
        'check if request_operation($o), ["git.read"].contains($o);',
    )
    p.allowed("K04-2a the attenuated subjob still reads",
              lambda: verify(subjob.to_bytes(), anchor, git_proxy("alpha", "git.read", "proj-a"),
                             fresh(), now),
              "read survived the attenuation")
    p.refused("K04-2b the attenuated subjob cannot write", "policy",
              lambda: verify(subjob.to_bytes(), anchor, git_proxy("alpha", "git.write", "proj-a"),
                             fresh(), now))

    # (1) Re-widening by a later block: append a block that re-permits write. Checks accumulate and
    # none can be removed, so the read-only check from before still refuses the write.
    rewidened = attenuate(
        subjob,
        'check if request_operation($o), ["git.read","git.write"].contains($o);',
    )
    p.refused("K04-2c a later block cannot re-widen the operation", "policy",
              lambda: verify(rewidened.to_bytes(), anchor, git_proxy("alpha", "git.write", "proj-a"),
                             fresh(), now))

    # (2) Injecting a right by a validly signed block: the block is a legitimate ratchet (its
    # signature verifies), yet the gate's policy never reads a right() from a non-authority block, so
    # the injected wider target has no effect. Widening dies in scope, before the signature is even
    # in question.
    injected = attenuate(
        authority,
        'right("repo:beta", "git.write");',  # a fact the captain was never granted
    )
    p.allowed("K04-2d the injected block is a valid ratchet",
              lambda: bau.Biscuit.from_bytes(injected.to_bytes(), anchor) and None,
              "its signature verifies against the anchor")
    p.refused("K04-2e the injected wider right is not honored", "policy",
              lambda: verify(injected.to_bytes(), anchor, git_proxy("beta", "git.write", "proj-a"),
                             fresh(), now))

    # (3) Forging a wider authority from scratch with an attacker key: signature verification against
    # the trust anchor refuses it, whatever it claims.
    forged = issue(
        Issuer.new("eu-c1"),  # the attacker's own key, not the anchor
        Grant("p-1", "proj-a", "confidential",
              [("repo:beta", "git.write"), ("host:evil.example.com", "egress")],
              {"pod_minutes": 9999}, now, expires),
    )
    p.refused("K04-2f a self-built wider authority fails the anchor", "signature",
              lambda: verify(forged.to_bytes(), anchor, git_proxy("beta", "git.write", "proj-a"),
                             fresh(), now))

    # (4) Tampering with the bytes of a valid token: the ratchet no longer holds.
    raw = bytearray(authority.to_bytes())
    raw[len(raw) // 2] ^= 0xFF
    p.refused("K04-2g tampered token bytes fail the anchor", "signature",
              lambda: verify(bytes(raw), anchor, git_proxy("alpha", "git.read", "proj-a"),
                             fresh(), now))

    # -----------------------------------------------------------------------
    # AB-K04-3 — all three gates verify fully, none trusts the origin
    # -----------------------------------------------------------------------
    p.banner("K-04 — three gates, each verifying fully (AB-K04-3, probe)")

    # The same token at each of the three effects. Each call is the whole verifier — signature,
    # revocation, policy — with only the request differing.
    p.allowed("K04-3a control API admits for the project",
              lambda: verify(authority.to_bytes(), anchor, control_api("proj-a"), fresh(), now),
              "control.admit on project:proj-a")
    p.allowed("K04-3b Git proxy passes a permitted push",
              lambda: verify(authority.to_bytes(), anchor, git_proxy("alpha", "git.write", "proj-a"),
                             fresh(), now),
              "git.write on repo:alpha")
    p.allowed("K04-3c egress proxy passes a permitted target",
              lambda: verify(authority.to_bytes(), anchor,
                             egress_proxy("api.example.com", "proj-a"), fresh(), now),
              "egress to host:api.example.com")

    # Each gate enforces its own effect independently: a target the authority does not carry is
    # refused at the gate that guards it, even though the other two gates accepted the same token.
    p.refused("K04-3d egress refuses a target not granted", "policy",
              lambda: verify(authority.to_bytes(), anchor,
                             egress_proxy("evil.example.com", "proj-a"), fresh(), now))
    p.refused("K04-3e Git proxy refuses a repo not granted", "policy",
              lambda: verify(authority.to_bytes(), anchor, git_proxy("beta", "git.read", "proj-a"),
                             fresh(), now))

    # None trusts the origin: a token that *claims* the trusted issuer in an issuer() fact but is
    # signed by another key is refused at every gate, because each checks the signature, not the
    # claim. The claim is right there in the token and buys nothing.
    liar = issue(
        other_issuer,  # signed by a different key...
        Grant("p-1", "proj-a", "confidential",
              [("repo:alpha", "git.write"), ("host:api.example.com", "egress"),
               ("project:proj-a", "control.admit")],
              {"pod_minutes": 30}, now, expires),
    )
    for tag, gate_name, request in [
        ("K04-3f", "control API", control_api("proj-a")),
        ("K04-3g", "Git proxy", git_proxy("alpha", "git.write", "proj-a")),
        ("K04-3h", "egress proxy", egress_proxy("api.example.com", "proj-a")),
    ]:
        p.refused(f"{tag} {gate_name}: unsigned origin claim", "signature",
                  lambda r=request: verify(liar.to_bytes(), anchor, r, fresh(), now))

    # -----------------------------------------------------------------------
    # AB-K04-6 — revocation per project, even with the control plane down
    # -----------------------------------------------------------------------
    p.banner("K-04 — revocation per project, static stability (AB-K04-6, probe)")

    # The authority block's revocation id — revoking it revokes the token and everything attenuated
    # from it.
    root_revocation_id = authority.revocation_ids[0]

    # A list for proj-a that revokes the token, and a list for proj-b that does not. Both while the
    # control plane is reachable, to isolate the "per project" property from the "control plane down"
    # one.
    list_a_revoked = RevocationList("proj-a", 2, now, {root_revocation_id}, reachable=True)
    list_b_clean = RevocationList("proj-b", 2, now, set(), reachable=True)

    p.refused("K04-6a a revoked block is refused for its project", "revoked",
              lambda: verify(authority.to_bytes(), anchor, git_proxy("alpha", "git.read", "proj-a"),
                             list_a_revoked, now))

    # Per project: the same revocation id on proj-a's list does not touch a proj-b authority. A token
    # issued for proj-b, whose id is not on proj-b's list, verifies — revocation is scoped, not global.
    authority_b = issue(
        issuer,
        Grant("p-2", "proj-b", "linked", [("repo:gamma", "git.read")], {"pod_minutes": 10},
              now, expires),
    )
    p.allowed("K04-6b another project's authority is untouched",
              lambda: verify(authority_b.to_bytes(), anchor, git_proxy("gamma", "git.read", "proj-b"),
                             list_b_clean, now),
              "proj-a's revocation does not reach proj-b")
    p.refused("K04-6c a list governs only its own project",
              "revocation.wrong_project",
              lambda: verify(authority_b.to_bytes(), anchor, git_proxy("gamma", "git.read", "proj-b"),
                             list_a_revoked, now))

    # Static stability, control plane down: the last known list still holds. The gate makes no fresh
    # fetch, and the revoked token stays refused.
    list_a_down = RevocationList("proj-a", 2, now, {root_revocation_id}, reachable=False)
    p.refused("K04-6d the last known list holds while down", "revoked",
              lambda: verify(authority.to_bytes(), anchor, git_proxy("alpha", "git.read", "proj-a"),
                             list_a_down, now))

    # And a token already covered by that last list (issued at or before its watermark) still verifies
    # while the plane is down — static stability is not "refuse everything".
    stable_list = RevocationList("proj-a", 2, now, set(), reachable=False)
    p.allowed("K04-6e a known authority still verifies while down",
              lambda: verify(authority.to_bytes(), anchor, git_proxy("alpha", "git.read", "proj-a"),
                             stable_list, now),
              "issued within the last known list's watermark")

    # New authorities are refused while the plane is down: one issued after the watermark cannot be
    # checked against a current-enough list, so the gate does not admit it (SP-V02-3).
    later = now + timedelta(minutes=5)
    new_authority = issue(
        issuer,
        Grant("p-1", "proj-a", "linked", [("repo:alpha", "git.read")], {"pod_minutes": 5},
              later, expires),
    )
    p.refused("K04-6f a new authority is refused while down", "new_authority_while_stale",
              lambda: verify(new_authority.to_bytes(), anchor,
                             git_proxy("alpha", "git.read", "proj-a"), stable_list, later))
    # The control: the same new authority verifies once the plane is reachable again (the fresh list's
    # watermark now covers it).
    reachable_now = RevocationList("proj-a", 3, later, set(), reachable=True)
    p.allowed("K04-6g the same new authority verifies once reachable",
              lambda: verify(new_authority.to_bytes(), anchor,
                             git_proxy("alpha", "git.read", "proj-a"), reachable_now, later),
              "a fresher list covers it")

    # -----------------------------------------------------------------------
    p.banner("Result")
    print(f"  {p.passed} PASS · {p.failed} FAIL\n")
    return 1 if p.failed else 0


if __name__ == "__main__":
    sys.exit(main())
