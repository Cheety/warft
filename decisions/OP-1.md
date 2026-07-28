# OP-1 — Starting values of the pots per authority level (pod minutes per envelope / project / principal-day)

**Status:** ruled · **Due before:** AP-3.6 — ruled in time, 2026-07-28, in AP-3.6 · **Panels:**
V-04, T-01 · **Source:** §19 of `01-specification.md`.

§19 left the numbers open and proposed a direction. AP-3.6 needs numbers, because SP-V04-3 reserves
**at admission**: a pot without a cap reserves nothing, and a cost control that only strikes in the
evaluation is none.

## To be decided

Starting values of the pots per authority level (pod minutes per envelope / project / principal-day)

## Proposed ruling

`public` very small, `linked` medium, `confidential` a tenant cap

## Ruling

### One table, four scopes, three levels

The caps are ruled per **authority level** and per **scope**, and a pot is keyed by both. A pot per
level is what makes "public very small" true in operation rather than on paper: a stranger in an open
channel cannot spend the day a confidential channel was granted, because the two draw from different
pots even when they belong to the same principal.

The fourth scope is SP-T01-8's: **a limit in pod minutes per principal and channel, not in
requests.** A chat platform that redelivers, or one channel that runs hot, cannot take the whole day
with it.

| Level | Scope | `pod_minutes` | `tokens` | `money` (µ€) |
|---|---|---|---|---|
| `public` | `envelope` | 5 | 40000 | 200000 |
| `public` | `project` | 60 | 480000 | 2400000 |
| `public` | `principal_day` | 120 | 960000 | 4800000 |
| `public` | `principal_channel_day` | 60 | 960000 | 4800000 |
| `linked` | `envelope` | 30 | 240000 | 1200000 |
| `linked` | `project` | 480 | 3840000 | 19200000 |
| `linked` | `principal_day` | 960 | 7680000 | 38400000 |
| `linked` | `principal_channel_day` | 480 | 7680000 | 38400000 |
| `confidential` | `envelope` | 120 | 960000 | 4800000 |
| `confidential` | `project` | 2880 | 23040000 | 115200000 |
| `confidential` | `principal_day` | 5760 | 46080000 | 230400000 |
| `confidential` | `principal_channel_day` | 2880 | 46080000 | 230400000 |

`acceptance/v04-budget.sh` reads this table and
[`platform/internal/budget/op1-pots.tsv`](../platform/internal/budget/op1-pots.tsv) and holds them
against each other, the way `t01-intake.sh` holds OP-5 against the file the binary embeds. A number
in one and not the other is drift, and drift is an error here.

### How long a pot lasts

**Every pot but the envelope pot is a pot of one day**, in the cell's own UTC day; the envelope pot
is a pot of one envelope, which is shorter than a day and ends with the message. That has to be
ruled, because SP-V04-3 releases only the *unspent* part at the terminal state: what a job actually
spends stays counted in its pots, so a pot without a period would become a lifetime cap — a `public`
project that had spent sixty pod minutes since it was created would never run again, which is not
what "per project against outliers" means.

### The three rules the numbers obey

The table is not twelve free numbers. Three rules generate it, and the acceptance script checks the
rules as well as the values — a table that can only be copied is a table nobody can extend.

1. **`tokens = 8000 · pod_minutes`.** A pod minute that spends more than eight thousand tokens is
   waiting on a model, not working; a pod minute that spends none is a build. Eight thousand is the
   dividing line between the two, and it makes the token pot bind first on exactly the jobs it should
   bind on.
2. **`money = 5 µ€ · tokens`,** i.e. 5 € per million tokens — a mid-sized model at today's mixed
   input/output price. Money is therefore never the pot that refuses first; it is the pot that
   refuses when a *day* went wrong, which is what SP-V04-2 makes a hard limit.
3. **The channel row binds `pod_minutes` only.** Its token and money caps are the principal-day ones,
   so a channel pot can never be the pot that refuses a token or a euro — SP-T01-8 asks for a limit
   in pod minutes, and a limit that also silently capped money would be a second daily cap wearing a
   channel's name.

Between the scopes: `envelope · 12 = project`, `project · 2 = principal_day`,
`principal_day / 2 = principal_channel_day`. Between the levels: `public · 6 = linked`,
`linked · 6 = confidential` on the day, with the envelope kept deliberately flatter (5 → 30 → 120),
because an envelope is a single message and the level says how much one message may be trusted with,
not how much its sender may spend.

### Money above the pots

`money` has a second bound the other two pots do not have, because SP-V04-1 says it is *a daily cap
per principal* and SP-V04-2 makes it the one only a human raises:

**The sum of money reserved across all of a principal's `principal_day` pots on one day may not
exceed `principal.daily_money_cap_micros`.** The pots are per level, that column is not, so the
column is the ceiling over all three levels at once. Its starting value is the `principal_day` money
cap of the highest level the principal is entitled to; raising it is `cap.raise`, and `cap.raise` is
two people (E-08).

### What refusal means, per pot

SP-V04-2 gives each pot its own failure, and admission answers accordingly:

| Pot | Exhausted means | The reply |
|---|---|---|
| `pod_minutes` | no new job is admitted; jobs already running run to completion | the free minutes, when the pot refills, and the smaller class that would fit |
| `tokens` | never a silent truncation | options: split the goal, narrow the bounds, raise the cap (two people), wait for the pot |
| `money` | a hard limit | one option: a human raises it, and that is two people (E-08) |

The refusal names the pot, the scope, the level, what was asked for and what is free.
`budget.exhausted` is a `cause_code` of the state contract, so a job refused at admission is refused
with a cause and not with a shrug.

## Rationale

1. **A public envelope buys one small pod, once.** Five pod minutes is a `small` pod (0.3 CPU,
   384 MB by SP-RA-1) for five minutes, or a `tiny` one for longer. That is enough to read a
   repository and answer, and not enough to be worth abusing — which is exactly what SP-V04-5 asks
   the envelope cap to be for.
2. **The day is two projects wide, and one channel is half a day.** A principal who runs one project
   into its cap still has a day left for the next; a principal whose Discord goes into a redelivery
   loop still has half the day left on every other channel. Both are SP-V04-5's "against outliers"
   and SP-T01-8 read as arithmetic.
3. **Levels multiply by six, not by a hundred.** `confidential` at 5760 pod minutes a day is four
   machine-days of pod time — a tenant cap, and still a number a person can hold in their head when
   the bill arrives. A cap nobody can estimate is a cap nobody notices being wrong.
4. **The generating rules are the point.** The day a model gets cheaper, rule 2 changes and twelve
   numbers follow; the day the platform's work becomes build-heavy rather than model-heavy, rule 1
   changes and the token pots follow. Twelve independently chosen numbers would each have to be
   re-argued, and in practice would not be.
5. **These are starting values.** They are the pots a cell begins with, not a measurement. The first
   real fleet moves them, and moving them is a change to this file — that is what makes them a
   decision rather than a constant in a program.

## Overturned by

The first week of real traffic. Concretely: any of the three refusal reasons happening to a job that
should have run — a `linked` principal hitting the day cap on ordinary work, an envelope cap refusing
a job a human would have approved — is the signal, and the answer is a new table here with the run
that showed it. Rule 2 is overturned by a price list; rule 1 by the first measurement of tokens per
pod minute out of AP-3.8's traces.
