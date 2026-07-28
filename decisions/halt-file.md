# Decision: the halt file — its place, its format, and how it expires

**Status:** ruled · **Date:** 2026-07-28 · **Affects:** SP-E08-3, SP-E08-4, SP-V04-2, AB-E08-3,
AP-3.6.

SP-E08-3 asks for a second path: "a file on the control node that is read at every admission
decision — so that it also takes effect when the API no longer answers". SP-E08-4 makes the expiry
after 60 minutes mandatory rather than a convenience. The two together leave one question the
specification does not answer, and admission cannot be written without an answer to it: **how does a
file expire, and how is a file renewed when the API — the thing `halt.renew` normally goes through —
is the part that is broken?**

## Ruling

### The place

`/var/lib/workpod/halt` on the control node. It is under `/var/lib` and not under `/etc` because it
is state and not configuration, and not under `/run` because a halt must survive the restart of the
service it halts — a halt that a reboot cleared would be a halt that failed at the one moment it was
being counted on.

### The format

One `key: value` per line, so a person can write it with an editor over a serial console with no
platform running:

```
reason: the model provider is answering nonsense
set_by: the duty officer's name
set_at: 2026-07-28T09:12:00Z
```

`reason` is mandatory by SP-E08-2 and `set_by` names the person. `set_at` is optional and RFC 3339
when present. A `set_at` that cannot be parsed is an **error**, not an ignored line: a halt whose age
cannot be read cannot expire, and SP-E08-4 does not permit a halt that never expires.

**The file existing is the halt.** A missing `reason` does not void it — the halt stands and the
refusal says the rationale is missing. Refusing to honour a halt because its note is incomplete is
the wrong direction of failure for the one control that is allowed to be unilateral.

### The expiry, and the renewal

**The halt expires 60 minutes after the later of `set_at` and the file's modification time.**
Renewal on this path is therefore `touch /var/lib/workpod/halt` — one command, no API, no database,
available from the same serial console the file was written from.

That is the whole ruling, and it is the part the specification left open. `halt.renew` over the API
rewrites the row in the state database (E-08's table of actions); on the file path there is no API to
call, so the renewal is the filesystem operation that already means "this is still current". Clearing
the halt is deleting the file, which is `halt.clear` and is logged like any other (SP-E08-2).

### Both paths, and which one answers

Admission reads both and either one halts the cell. When both are in force the **file** is the one
named in the refusal, because it is the one somebody wrote while the other path was not working. When
the file says nothing and the state database cannot be asked at all, admission refuses — not because
a halt was found, but because one of the two paths is unreadable and admission does not guess.

## Rationale

1. **The renewal has to work on the path that exists for the API being down.** A file whose only
   renewal ran through the API would expire exactly when nobody could stop it from expiring, and the
   cell would resume by itself in the middle of the incident that halted it. Modification time is the
   one renewal that needs nothing but the filesystem.
2. **Modification time is not a clever reuse — it is what the operation already means.** `touch`
   exists precisely to say "current as of now". Storing a second expiry timestamp inside the file
   would create two answers to one question, and would make renewal an edit rather than a command.
3. **60 minutes is not shortened for the file.** SP-E08-4's number is about attention, not about the
   path: a halt that persists without anybody looking is the failure mode either way. The duty
   officer renews every 60 minutes on both paths, and the command differs, not the interval.
4. **The file wins the naming when both are in force** because it carries more information: the API
   path being usable is the ordinary case, so a file written anyway is evidence about the incident.
5. **An unreadable file is not an absent one.** A permission error or an I/O error on
   `/var/lib/workpod/halt` refuses admission rather than admitting. The safe direction may be fast
   (E-08); the unsafe one may not be accidental.

## Consequences

- `AB-E08-3` turns green through `acceptance/v04-budget.sh`: with the file in place and the control
  plane's API stopped, `workpod control admit` admits nothing, while an order already running walks
  to its terminal state untouched.
- The image ships no halt file and creates no halt directory beyond `/var/lib/workpod`, which the
  disk step already makes. A halt file in an image would be a halted cell on first boot.
- Both paths report over the heartbeat: `SendHeartbeat` answers with a `HaltNotice` carrying the halt
  in force, so a worker learns of it without polling either path itself.

## Overturned by

A control plane that is highly available across nodes. The file is per node, and "the control node"
is a single place only while V-01's layers are one machine; the day the control layer is three nodes,
the second path becomes a file on each of them and this ruling needs a sentence about what two
disagreeing files mean. Until then, one node, one file.
