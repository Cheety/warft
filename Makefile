# Workpod platform — build order.
#
# `make acceptance` is the instrument. Progress means rows turn green through a run — not that
# there are more files (A-06, Q-02). It fails while anything is red, so it can stand in CI
# unchanged from day one.
#
# Exit codes: `acceptance/registry.py` exits 1 when a check is red, as AP-0.3 asks. GNU make
# normalizes any recipe failure to 2, so `make acceptance` exits 2 rather than 1. Both are
# non-zero and both fail a build; where the exact code matters, call the script directly.

.PHONY: acceptance acceptance-sync image help

help:
	@echo "make acceptance       report the state of the 212 checks; fails while anything is red"
	@echo "make acceptance-sync  take new checks over from 03-acceptance-matrix.md into the registry"
	@echo
	@echo "make image            build the image twice and compare — AB-A03-2 (AP-0.2)"
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
