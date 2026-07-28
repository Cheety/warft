# Workpod platform — build order.
#
# `make acceptance` is the instrument. Progress means rows turn green through a run — not that
# there are more files (A-06, Q-02). It fails while anything is red, so it can stand in CI
# unchanged from day one.
#
# Exit codes: `acceptance/registry.py` exits 1 when a check is red, as AP-0.3 asks. GNU make
# normalizes any recipe failure to 2, so `make acceptance` exits 2 rather than 1. Both are
# non-zero and both fail a build; where the exact code matters, call the script directly.

.PHONY: acceptance acceptance-sync image verify image-acceptance calibration contract platform intake budget scheduler observation boot runner pipeline outbox decisions proto help

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
	@echo "make intake           the CLI adapter and intake against a real database — AB-T01-7, AB-K01-6 (AP-3.2)"
	@echo "make budget           the pots, the caps, the share-out and the halt with two paths — AB-V04-1, AB-V04-2, AB-V04-4, AB-T01-8, AB-E08-3 (AP-3.6)"
	@echo "make scheduler        tokens per phase, aging, the PSI ladder and the queue with SKIP LOCKED — AB-RB-*, AB-RC-1, AB-RC-3, AB-RC-6, AB-RD-3, AB-E05-2, AB-V01-4, AB-E02-2, AB-E02-5 (AP-3.7)"
	@echo "make observation     one trace per job, four alerts, the trail and the one query — AB-B03-1, AB-B03-3, AB-B03-4, AB-K01-7, AB-Q04-4, AB-RD-2, AB-B02-5, AB-A05-5 (AP-3.8)"
	@echo "make boot             four boots along A-04: sequence, layers, pressure, reinstall — AB-A04-1, AB-A04-3, AB-A05-1, AB-RC-4, AB-V01-1 (AP-3.1)"
	@echo "make runner           pods on a node: the contract, no network, the lifecycle, the reaper — AB-T03-1, AB-T04-*, AB-RA-*, AB-RC-5, AB-B02-3, AB-E02-4 (AP-3.3)"
	@echo "make pipeline         the fixed spine, the seven places, the bounded loop — AB-T05-1, AB-T05-2, AB-T05-3 (AP-3.4)"
	@echo "make outbox           the outbox, both gates, one push out of two attempts — AB-K03-*, AB-A06-11, AB-B01-4, AB-B02-2, AB-B02-4 (AP-3.5)"
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

# AP-3.2. The CLI adapter and intake, against a Postgres 16 the script spins itself: OP-5's ruling
# against the file the binary embeds, the four methods of T-01, the same message delivered twice,
# and every attachment OP-5 refuses.
intake:
	@acceptance/t01-intake.sh

# AP-3.6. V-04 and E-08 against a Postgres 16 the script spins itself: OP-1's caps against the file
# the binary embeds, the reservation at admission and the release at the terminal state, the channel
# limit that counts minutes rather than requests, the token pot answering with options, the
# bottleneck shared out between a heavy and a light sender, and the halt file deciding with the
# plane stopped. `acceptance/v04-budget.sh host` is the half that needs no machine.
budget:
	@acceptance/v04-budget.sh

# AP-3.7. R-B and R-C against the program and against a Postgres 16 the script spins itself: the
# phase-to-token ruling against the file the binary embeds, SP-RB-2's four bounds and SP-RC-2's six
# signals against the tables the program prints, OP-6's four numbers per signal, a replayed pressure
# series climbing all five rungs in order, three recorded runs turning admission mechanical, two
# concurrent claims that skip each other's rows, and a job frozen and thawed with its attempt and
# its spend intact. `acceptance/rb-scheduler.sh host` is the half that needs no database.
scheduler:
	@acceptance/rb-scheduler.sh

# AP-3.8. B-03 against a real state database: the alert catalog in its three places, a job from the
# channel message to the patch, its trace with the phases as spans, the pod log hung on the job, the
# egress gate's refusals folded into a display, the audit trail with the tenant's own period, and the
# one query that resolves the chain from either end. `acceptance/b03-observation.sh host` is the half
# that needs no database.
observation:
	@acceptance/b03-observation.sh

# AP-3.1. The A-04 start sequence against the image `make image` built: four boots — the sequence
# to a registered node, the reinstall that only /data/db survives, the withheld boot value, and
# the failed selftest that must not register.
boot:
	@acceptance/a04-boot.sh

# AP-3.3. The runner and the workpod against the image `make image` built: the four classes read
# back out of the pods' own cgroups, a pod with no network and no keys, the lifecycle to the CRIU
# dump, and three orphans that do not survive a worker restart. The two tables are checked on the
# host first — a boot that measured a program the ruling does not describe would measure nothing.
runner:
	@acceptance/t04-runner.sh

# AP-3.4. T-05's spine against the panel, OP-2's ceilings against the ruling, and then one boot: a
# job that delivers, a job that demands a plan nobody can write and a job that cannot be solved —
# all three through the same seven steps, the last one ending after the ruled number of rounds with
# a diff, logs and an assessment. `acceptance/t05-pipeline.sh host` is the half that needs no
# machine and runs against a working tree.
pipeline:
	@acceptance/t05-pipeline.sh

# AP-3.5. K-03's chain against the two gates that execute it: the real binary, both gates on their
# own Unix sockets, a real bare repository, and a job run twice that pushes once. The sources are
# checked first — the allowlist table against contract/schema.sql's levels, the units against the
# roles SP-B02-2 splits them over — because a run that measured a program the specification does not
# describe would measure nothing. `acceptance/k03-outbox.sh host` is the half that needs no build.
outbox:
	@acceptance/k03-outbox.sh

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
