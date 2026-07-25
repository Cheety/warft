---
# Template for a catalog entry (F-01, F-07).
# Shell: the open Agent Skills standard. What the standard does not know sits under metadata:,
# which it deliberately leaves open. Whoever uses only name, description and the body runs in any
# runtime that supports the standard (SP-F07-4).

name: interface.change

# The description is the resolution surface (F-01). It says what the capability is for AND what it
# is explicitly not for — the coverage check (F-04) decides on it whether a job is covered. A
# capability you only find when you already know it is not one.
description: >
  Changes an interface and adapts all callers. Evidences the adaptation through a fact query, not
  through a claim. Do not use for: new interfaces (that is a design, not a rebuild), for changes to
  more than one module at a time (use module.extract as a campaign for that), and not when the
  caller graph for the language is not indexed — then gap.report.

# Tool side of the precondition. The pod has no network; everything here is local (T-04).
allowed-tools:
  - fact.query
  - codemod.apply
  - check.types
  - check.tests
  - artifact.compare
  - gap.report

metadata:
  # F-02: call downward only. handle → nothing, procedure → handles, campaign → procedures.
  order: procedure

  # F-01: enforcement instead of an admonition in the prompt. An envelope at level public cannot
  # even resolve a writing capability (SP-F01-2).
  min_authority: linked

  # F-01: what must be true before I start. If it is missing, the pod fails after twenty minutes
  # on something that stood in a table beforehand.
  precondition:
    - The target language has a SCIP index in this cell (E-09).
    - The caller graph for the target symbol is derivable, not estimated.
    - Tests exist at the anchor, or the pipeline has switched to human acceptance (G-07).

  # F-01 and Q-02: which evidence class I deliver. Without this field, "done" means again whatever
  # the agent takes it to mean.
  evidence: tests.existing
  evidence_claim: >
    All callers are adapted — evidenced through fact.query, not claimed.

  # F-01: the boundary is the most valuable part. What I cannot do, and what I do then.
  boundary:
    - More than 200 affected call sites → a split proposal instead of execution.
    - A caller found in an unindexed language → gap.report, do not guess.
    - Reflection, dynamic loading or a plugin registry in the path → gap.report (G-02).
  boundary_exit: gap.report

  # F-01: an abort condition per capability, tighter than the global rework round from T-05.
  # A handle that fails twice is broken, not unlucky.
  max_attempts: 2

  # F-05 and Q-04: intake condition. Without three cases in the regression corpus, no intake.
  corpus_cases:
    - corpus/interface.change/rename-exported-symbol
    - corpus/interface.change/widen-parameter-type
    - corpus/interface.change/caller-in-unindexed-language   # must end in gap.report

  # F-04: pinned per job via the content hash, never "latest" (SP-F04-5).
  # Computed at check-in, not maintained by hand.
  content_hash: <set at check-in>
---

# Procedure

The body is the `procedure` from F-01. Deterministic steps lie beside it as `scripts/`; their code
never enters the context, only their output — exactly the cut from F-02.

**The model sits at exactly one place in this procedure: in step 2.** It decides the *whether*, never
the *how*.

1. **Resolve the anchor.** `fact.query`: symbol, signature, callers, types, ownership. Derived, never
   guessed (G-01). The result is a list, not prose.

2. **Decide (the only model step).** Is the change mechanically derivable, and which call sites fall
   into which equivalence class? Output: an assignment, not code.

3. **Execute.** `codemod.apply` per class — deterministic, including all references. No hand-written
   patch for something a codemod can do (G-04: 95 % mechanical).

4. **Evidence it.** `check.types` → `check.tests` → where a transformation is meant to preserve
   behavior, additionally `artifact.compare` for bit-identity (G-06, the sharpest cheap oracle).

5. **Be able to stop.** If a boundary from `metadata.boundary` applies, then `gap.report` with a split
   proposal — never silence, never half a result without a note (F-05).

6. **Write back.** Attach judgement candidates with a citation to their facts to the job ("this call
   goes through a registry"). That is the only path that makes the system cheaper across thousands of
   jobs (G-07).

## What this capability does not do

It does not change an interface for which there is no acceptance criterion from the specification —
that is a finding for Q-01, not a job for this procedure. It does not merge anything that only looks
the same by accident. And it removes nothing: removal instead of deletion is a different path with a
different kind of evidence (G-02).
