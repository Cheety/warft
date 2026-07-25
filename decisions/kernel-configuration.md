# Decision: what "the kernel configuration is a file" means with a distribution kernel

**Status:** ruled · **Date:** 2026-07-26 · **Affects:** SP-A02-2, SP-E01-1, AB-E01-1, AP-1.1, AP-1.2.

E-01 rules two things that pull against each other. It takes **the Fedora kernel** — "the security
pipeline stays with Fedora" — and it justifies that choice with "with mkosi the kernel configuration
is a file — the measurement from A-06 then changes one line instead of an architecture". `AB-E01-1`
turns the second half into a check: *the kernel configuration is a file in the repository*.

A Fedora kernel is a binary somebody else configured. There is no `.config` here to edit, and
compiling one would give up the reason the base was chosen. So the file cannot be the configuration
that produced the kernel. This decides what it is instead.

## Ruling

**`image/kernel-requirements.conf` is that file: the options SP-A02-2 demands, written as
requirements against the kernel the image ships, not as input to a build.**
`acceptance/e01-kernel.sh` reads it and checks the image against it; `AB-E01-1` is evidenced by that
run, not by the file existing.

Every requirement is one of four lines — an option that must be built in, an option that may be a
module *and then the module must be in the image*, an option that must not be set, or a directory
of modules that must be absent. The check reads the kernel's own `config` file at
`/usr/lib/modules/<kver>/config`, which Fedora ships in `kernel-core`, so the answer comes from the
built image rather than from the package list that was meant to produce it.

**SP-A02-2's second half — "Out: unused filesystems, sound, legacy drivers" — is realized by leaving
modules out of the image, not by leaving options out of a kernel.** The mechanism is
`KernelModules=` in `image/mkosi.conf` (and installing neither `kernel-modules` nor
`kernel-modules-extra`); the requirement is the `exclude` lines in `kernel-requirements.conf`. A
driver whose module is not in the image cannot be loaded, which is what the "Out" list is for.

**Which drivers count as "legacy" is a judgement made here, not read off the specification.** The
list names parallel port, PC Card, Memory Stick, 1-Wire and the GPIB lab bus, and the filesystems
name themselves. It is meant to be extended; every addition is one line and one rebuild.

## Rationale

1. **The requirement E-01 actually states is checkability, not authorship.** Its argument is that a
   red row in A-06 must cost one line rather than an architecture. A requirements file delivers
   exactly that: when `AB-A06-5` (CRIU) goes red, the line that says so is in this repository, and
   the next step — Fedora's kernel does not do it, so the base changes — is E-01's own overturn
   condition. Authoring the configuration would not make that step shorter.
2. **A file nobody checks is not evidence** (Q-02). A copy of Fedora's `.config` committed here would
   satisfy the words of `AB-E01-1` and prove nothing: it would be a snapshot of an input we do not
   control, drifting silently on the next package bump. A requirements file that a run compares
   against the shipped kernel cannot drift without failing.
3. **The two halves of SP-A02-2 have different mechanisms, and saying so is cheaper than pretending
   otherwise.** "In the kernel" is a property of Fedora's build and can only be checked. "Out" is a
   property of *this* image and can be enforced. Writing both into one file, with the check treating
   them differently, keeps the specification's sentence intact.
4. **It is the same discipline as the package pin.** `LocalMirror=` does not build packages, it fixes
   which ones we get and makes a rebuild comparable. This file does not build a kernel, it fixes
   which kernel we accept.

## Consequences

- `AB-E01-1` is evidenced by a run of `acceptance/e01-kernel.sh` against a booted image, named in
  `acceptance/registry.tsv` like every other green row.
- A required option that Fedora stops setting is a failing check, not a silent regression — and the
  gate in AP-1.3 ("if a kernel requirement is missing and cannot be reconfigured, the base changes,
  not the order") is reached by a red row rather than by noticing.
- `CONFIG_CHECKPOINT_RESTORE` is in the file like any other line, but it is the one E-01 hangs on:
  `AB-A06-5` measures whether CRIU actually works, and this file only says the option is set.

## Overturned by

A kernel of our own. If a required option is missing from the Fedora kernel and cannot be obtained
from it, this decision does not survive the change of base — E-01's overturn condition ("then
NixOS") takes over, and with an own kernel build the file becomes the configuration rather than a
statement about someone else's.
