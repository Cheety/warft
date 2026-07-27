# Workpod platform — build order.
#
# `make acceptance` is the instrument. Progress means rows turn green through a run — not that
# there are more files (A-06, Q-02). It fails while anything is red, so it can stand in CI
# unchanged from day one.
#
# Exit codes: `acceptance/registry.py` exits 1 when a check is red, as AP-0.3 asks. GNU make
# normalizes any recipe failure to 2, so `make acceptance` exits 2 rather than 1. Both are
# non-zero and both fail a build; where the exact code matters, call the script directly.

.PHONY: acceptance acceptance-sync image verify image-acceptance calibration contract platform boot decisions proto help

help:
	@echo "make acceptance       report the state of the 212 checks; fails while anything is red"
	@echo "make acceptance-sync  take new checks over from 03-acceptance-matrix.md into the registry"
	@echo
	@echo "make image            build the platform binary and the image twice, compare — AB-A03-2 (AP-0.2)"
	@echo "make verify           check that build against the seal — AB-A03-7 (needs no key)"
	@echo "make image-acceptance boot that build and check it — AB-E01-1, AB-A02-1, AB-A06-* (AP-1.2)"
	@echo "make calibration      500 pods, 20 active, the five constants — AB-A06-13, AB-E05-1 (AP-1.3)"
	@echo "make contract         both schemas, their probes, the state machine, the authority token and the node identity, and the working tree against HEAD — AB-E10-*, AB-K01-*, AB-K02-*, AB-K04-*, AB-B01-3, AB-V05-2 (AP-2.1 through AP-2.5)"
	@echo "make platform         the one Go binary, its seven entry points, its honest refusals — AB-E02-1 (AP-3.1)"
	@echo "make boot             four boots along A-04: sequence, layers, pressure, reinstall — AB-A04-1, AB-A04-3, AB-A05-1, AB-RC-4, AB-V01-1 (AP-3.1)"
	@echo "make decisions        the decision store, and the module contract against the imports — AB-G01-5 (AP-0.1, AP-3.1)"
	@echo "make proto            regenerate platform/api/workpodv1 from contract/platform.proto"
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

# AP-2.1 through AP-2.5. The probes prove the linters bite, the trigger refuses, widening is
# cryptographically impossible and the certified name beats the claim; the comparisons hold the
# working tree to SP-E10-3 and SP-V05-2 before a commit exists for CI to hold it. CI compares
# against the pre-push contract instead — the same tools, a different baseline.
contract:
	@acceptance/e10-schema.sh
	@old="$$(mktemp)"; \
	git show HEAD:contract/platform.proto > "$$old" && \
	acceptance/e10-additive.py "$$old" contract/platform.proto; \
	rc=$$?; rm -f "$$old"; exit $$rc
	@acceptance/k01-schema.sh
	@acceptance/k02-state.sh
	@acceptance/k04-authority.sh
	@acceptance/b01-identity.sh
	@old="$$(mktemp)"; \
	git show HEAD:contract/schema.sql > "$$old" && \
	acceptance/schema-additive.py "$$old" contract/schema.sql; \
	rc=$$?; rm -f "$$old"; exit $$rc

# AP-1.3. A-06's last row, and the measurement stage 1 ends with: a fleet of 500 pods on a machine
# larger than a check needs, and the five constants of E-05 measured on it. It is its own target
# because it boots twice more and takes minutes rather than seconds — and because the numbers it
# prints are read by a person before they land in decisions/E-05.md.
calibration:
	@acceptance/calibration.sh

# AP-3.1. The platform as one artifact: control plane, scheduler, worker, adapter, both gates and
# harness out of one statically linked Go binary — built parts serving, unbuilt parts refusing by
# the name of the package that builds them.
platform:
	@acceptance/e02-binary.sh

# AP-3.1. The A-04 start sequence against the image `make image` built: four boots — the sequence
# to a registered node, the reinstall that only /data/db survives, the withheld boot value, and
# the failed selftest that must not register.
boot:
	@acceptance/a04-boot.sh

# AP-0.1 and AP-3.1. The store is a property of the repository; the module contract is a decision
# (decisions/module-dependencies.md) held against the imports of platform/. CI runs the same script
# on every change to either side.
decisions:
	@acceptance/g01-decisions.sh

# The generated bindings are committed; this regenerates them after a (decided, additive) change
# to contract/platform.proto. The import path is mapped on the command line so the contract file
# itself stays free of Go concerns — it changes only through a decision (E-10).
proto:
	@protoc -I contract \
	  --go_out=platform --go_opt=module=github.com/Cheety/warft/platform \
	  --go_opt=Mplatform.proto=github.com/Cheety/warft/platform/api/workpodv1 \
	  --go-grpc_out=platform --go-grpc_opt=module=github.com/Cheety/warft/platform \
	  --go-grpc_opt=Mplatform.proto=github.com/Cheety/warft/platform/api/workpodv1 \
	  contract/platform.proto
