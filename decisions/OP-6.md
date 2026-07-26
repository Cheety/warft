# OP-6 — Hysteresis and hold times of the PSI thresholds

**Status:** ruled · **Due before:** AP-3.7 — ruled in time, 2026-07-26, in AP-1.3 · **Panels:** R-C
· **Source:** §19 of `01-specification.md` · **Measured by:**
[run 30204671464](https://github.com/Cheety/warft/actions/runs/30204671464).

§19 left this value open and proposed deriving it from the A-06 calibration run. That run exists now
(`acceptance/calibration.sh`), it measured the signal it is about, and this is the ruling.

## To be decided

Hysteresis and hold times of the PSI thresholds

## Proposed ruling

derive from the A-06 calibration run

## Ruling

Every threshold in SP-RC-2 gets four numbers instead of one. The thresholds themselves are unchanged
— this adds when a crossing counts and when it stops counting.

| | Value | Where it comes from |
|---|---|---|
| sampling | every 2 s | SP-RC-1, unchanged |
| enter | the threshold, met on **2 consecutive samples** (4 s) | one sample is a spike; the measured rise took 4.6 s from a standing start, so 4 s of confirmation costs almost nothing |
| release | **half** the threshold | the measured noise floor at rest is 0.00 %, so half of a 10 % threshold clears it by a wide margin |
| hold before release | **30 s** below the release threshold | the measured decay from 10.4 % to under 1 % took 26.0 s, and `avg10` cannot fall faster than its own ten-second window |

Written out for the six signals of SP-RC-2:

| Signal | Acts when | Releases when |
|---|---|---|
| `memory some avg10 > 10 %` — admit no new pods | > 10 % on two samples | < 5 % for 30 s |
| `memory full avg10 > 5 %` — freeze immediately | **> 5 % on one sample** | < 2.5 % for 30 s |
| `io full avg10 > 20 %` — I/O tokens to 1 | > 20 % on two samples | < 10 % for 30 s |
| `cpu some avg60 > 60 %` with free tokens — look for an outlier | > 60 % on two samples | < 30 % for 60 s |
| `memory.events high` rising — raise the class | two samples with a rising counter | one attempt at the higher class; this is a reclassification, not a state |
| `pgmajfault` rising fast — the hardest rung | > 100 per second on two samples | < 10 per second for 30 s |

The one exception is deliberate and is the panel's own wording: `memory full avg10 > 5 %` means
"everything is stalled — freeze immediately, do not wait", so it acts on the first sample. A hold
time on that signal would be a hold time on the reaction R-C wrote "do not wait" next to.

`cpu some avg60` releases after 60 s and not 30, because `avg60` is a six-times-longer window and the
same argument scales with it.

## Rationale

1. **The decay decides the hold time, and the metric's own window decides the decay.** `avg10` is an
   exponential average over ten seconds: after the cause is gone it keeps reporting the event while it
   forgets it. Measured: 10.4 % at the peak, under 10 % after 2.1 s, under 5 % after 9.7
   s, under 1 % after 26.0 s — two to three of its own windows. A release hold shorter than that releases on a
   signal that is still remembering something that is over, and the next sample re-triggers. That is
   flapping, and it is the failure this open point exists to prevent.
2. **Half the threshold is a band the noise cannot cross.** At rest, with 500 pods created and 480 of
   them frozen, `memory some avg10` read 0.00 % over twelve seconds of samples — an idle machine does
   not stall, so the floor is genuinely zero and any release edge clears it. The band is not for the
   idle machine: it is for the loaded one hovering near the enter edge, where a single edge would
   re-trigger on every sample that lands a tenth of a percent to either side. Half the threshold
   keeps the two edges apart by more than the signal's own jitter, which is what hysteresis is.
3. **Two samples to enter, because the rise is fast enough to afford it.** The measured event crossed
   10 % 4.6 s after the cause started, and the reactions in SP-RC-2 that use a hold — refusing
   admission, cutting I/O tokens, hunting an outlier — are all cheap to be 4 s late with. The
   reaction that is not cheap to be late with is the freeze, and that one keeps no hold.
4. **This changes numbers, not the ladder.** SP-RC-3's escalation — throttle, block, freeze,
   checkpoint, escalate — is untouched, and so are the thresholds. What was missing was the pair of
   edges around each one, and that is what an open point about hysteresis is.
5. **It is measured on the signal the platform will read**, in the pods slice of a node booted from
   the image, at 500 ms while SP-RC-1 reads at 2 s. Sampling faster than the reader is the only way to
   see what the reader will miss.

## Overturned by

A measurement showing flapping at these numbers — a threshold that enters and releases repeatedly
within a minute under a load that is not itself oscillating. Also: a change to the sampling interval
of SP-RC-1, or to the kernel's PSI averaging windows, since both numbers above are derived from the
ten-second one. The re-measurement is the same script, and AP-3.7 runs it against a node that has a
runtime on it.
