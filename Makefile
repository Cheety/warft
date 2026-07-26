# Workpod platform — build order.
#
# `make acceptance` is the instrument. Progress means rows turn green through a run — not that
# there are more files (A-06, Q-02). It fails while anything is red, so it can stand in CI
# unchanged from day one.
#
# Exit codes: `acceptance/registry.py` exits 1 when a check is red, as AP-0.3 asks. GNU make
# normalizes any recipe failure to 2, so `make acceptance` exits 2 rather than 1. Both are
# non-zero and both fail a build; where the exact code matters, call the script directly.

.PHONY: acceptance acceptance-sync image verify image-acceptance calibration help

help:
	@echo "make acceptance       report the state of the 212 checks; fails while anything is red"
	@echo "make acceptance-sync  take new checks over from 03-acceptance-matrix.md into the registry"
	@echo
	@echo "make image            build the image twice and compare — AB-A03-2 (AP-0.2)"
	@echo "make verify           check that build against the seal — AB-A03-7 (needs no key)"
	@echo "make image-acceptance boot that build and check it — AB-E01-1, AB-A02-1, AB-A06-* (AP-1.2)"
	@echo "make calibration      500 pods, 20 active, the five constants — AB-A06-13, AB-E05-1 (AP-1.3)"
	@echo
	@echo "acceptance/registry.py       the same report, exit 1 when red (use this in CI)"
	@echo "acceptance/registry.py sync  the same sync"

acceptance:
	@acceptance/registry.py

acceptance-sync:
	@acceptance/registry.py sync

# AP-0.2: reproducibility is a property of a run, not of a configuration file.
image:
	@image/build.sh

# Not listed with a `seal` target on purpose: sealing needs the private key, and by
# decisions/signing-key.md that key is not on a build machine. Verifying is the half that belongs in
# a build.
verify:
	@image/verify.sh

# The rows that need a machine rather than an artifact. They boot the image built by `make image` —
# E-11's "A-06 as a script against a bare mkosi VM". The first two are AP-1.1's; the third is the
# list itself, and it is the longest of the three because it damages a copy of the image and boots
# that too.
image-acceptance:
	@acceptance/e01-kernel.sh
	@acceptance/a02-roles.sh
	@acceptance/a06-acceptance.sh

# AP-1.3. A-06's last row, and the measurement stage 1 ends with: a fleet of 500 pods on a machine
# larger than a check needs, and the five constants of E-05 measured on it. It is its own target
# because it boots twice more and takes minutes rather than seconds — and because the numbers it
# prints are read by a person before they land in decisions/E-05.md.
calibration:
	@acceptance/calibration.sh
