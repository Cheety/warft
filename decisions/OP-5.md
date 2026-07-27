# OP-5 — Size limits for attachments and permitted MIME types

**Status:** ruled · **Due before:** AP-3.2 — ruled in time, 2026-07-27, in AP-3.2 · **Panels:**
K-01, T-01 · **Source:** §19 of `01-specification.md`.

§19 left this value open and proposed a direction rather than a number. This ruling turns the
direction into three limits and a closed list of media types, because SP-K01-6 puts the check *at
intake* — the one place where an attachment is still a candidate rather than an object with a
retention — and a check at intake needs a number to check against.

## To be decided

Size limits for attachments and permitted MIME types

## Proposed ruling

conservative; widening only as a decision

## Ruling

### The three limits

| Parameter | Value |
|---|---|
| `attachment_max_bytes` | 4194304 — 4 MiB, the largest single attachment intake accepts |
| `envelope_max_total_bytes` | 16777216 — 16 MiB, the largest sum over one envelope's attachments |
| `envelope_max_attachments` | 8 — the most attachments one envelope may carry |

All three bind together. A limit per attachment alone is no limit: a hundred lawful attachments are
an unlawful envelope, and the thing admission has to reason about (V-04) is the envelope, not the
file.

### The permitted media types

| Media type | Must sniff as | Why an order carries it |
|---|---|---|
| `text/plain` | `text` | logs, stack traces, command output |
| `text/markdown` | `text` | a specification or a note written by hand |
| `text/csv` | `text` | a table of measurements |
| `application/json` | `text` | a machine's answer, an API response, a manifest |
| `image/png` | `image/png` | a screenshot of the failure |
| `image/jpeg` | `image/jpeg` | the same, from a camera or a phone |

Nothing on this list is a container format. An archive, a PDF or an office document carries other
files inside it, so a check of one is a check of the wrapper and not of the content — and SP-K01-6
asks for the check at intake, not for one deferred to whoever unpacks it later.

**The type is decided from the bytes.** The sender names a media type; intake sniffs the leading
bytes and refuses when the two contradict each other. A named type that is merely believed would
make this list a courtesy.

**Never executable is enforced twice, and neither guard is the type list.** Intake refuses content
whose leading bytes are an executable — ELF, a `#!` line, PE/COFF, Mach-O, WebAssembly — whatever
media type is claimed, and stores every accepted attachment mode `0444`. The state contract carries
the second guard: `attachment.executable` is `boolean NOT NULL CHECK (executable = false)`, so
recording an executable attachment is impossible in the database as well. SP-K01-6 names the type
check and "never executable" as separate requirements, and they are separately enforced.

### Where the numbers live

`platform/internal/attachment/op5-policy.tsv` is the machine-readable half of this file, compiled
into the binary. `acceptance/t01-intake.sh` holds the two against each other on every run — a number
or a media type in one that is not in the other is drift, and the run fails. This is the shape
`decisions/E-05.md` and `acceptance/e05-constants.tsv` already have: the ruling is the source, the
file is what a program can read, and a check joins them so neither can move alone.

It is deliberately *not* a table in `contract/schema.sql`, the way OP-4's constants are. OP-4's
numbers are read by the control plane while it runs, from the database it already holds open. These
are read at intake, by an adapter that SP-T01-3 will later run as a workload of its own — with no
database, by design. A constant an adapter must know is a constant that travels in the artifact.

## Rationale

1. **Conservative, because widening is cheap and narrowing is not.** An accepted attachment is a
   stored object with a retention (E-07) and a place in the provenance chain (SP-K01-7). Once a
   channel has sent a type successfully, refusing it later breaks a workflow that worked; adding a
   type breaks nothing. That asymmetry is the whole of §19's proposal, and it decides the direction
   of every number here.
2. **4 MiB is chosen so the common case never meets it.** A long build log, a large screenshot, a
   CSV of a day's measurements all sit far below it; a core dump, a video or a repository tarball sit
   far above. The limit separates the two without a judgement call in between, which is what makes it
   checkable rather than arguable.
3. **The list is what intake can check, not what someone might send.** Every entry is a format whose
   first bytes decide what it is. That is the property that makes the sniff possible, and it is the
   same property container formats lack — which is why they are absent rather than merely capped.
4. **Text is data, and stays data (SP-T01-9).** Four of the six entries are text, and text from a
   channel is never an instruction. The type check is not what protects against injection — B-04
   does — but a list without executables and without containers is what keeps the attachment a thing
   the platform *reads* rather than a thing it might run.
5. **It is ruled here rather than guessed in code, because that is the reason OP-5 exists.** The
   issue's own boundary says so: the value is decided and filed, never guessed anywhere in the code
   (V-05).

## Consequences

- `AP-3.2` is unblocked; `platform/internal/attachment` enforces exactly these numbers and refuses
  anything else, and `acceptance/t01-intake.sh` probes every refusal — an oversized attachment, an
  oversized envelope, a ninth attachment, a type off the list, a claimed type the bytes contradict
  and an ELF wearing a permitted type.
- The check lives in the artifact, so it holds wherever intake runs. When SP-T01-3 makes adapters
  workloads of their own (AP-5.7), the control plane re-checks what an adapter sends against this
  same file in the same binary — one artifact, one policy (E-02).
- Any widening is a new revision of this file *and* of `op5-policy.tsv`, in one commit. The check
  fails on either alone.

## Overturned by

A measurement rather than an argument: attachments refused at intake for a size or a type the work
genuinely needed, counted over a period of operation. AP-3.8's observation is what can count them —
a refusal at intake is an event with a cause, so the number exists without anyone collecting it by
hand. Three shapes of measurement each move a different line: refusals at `attachment_max_bytes` for
a format already on the list ask for a larger number; refusals of a type that recurs across
principals ask for an entry; refusals at `envelope_max_attachments` while the total stays far below
16 MiB ask whether the count is doing any work at all. Whichever arrives first, it arrives as a new
ruling here, never as a flag.
