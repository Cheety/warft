// Package boot carries the five boot values of SP-A04-1 and the two run-state markers of the
// A-04 start sequence.
//
// The first start of a node receives exactly five values from outside — role, cell, control,
// enrollment_token, locality_group — and nothing else (SP-A04-1). They arrive as systemd
// credentials (E-01), the same door image/vm.sh and a cloud's instance data use, and the same one
// the role generator in the image already reads. There is no configuration file to fall back to:
// a value that is missing here is missing, and the sequence says so instead of inventing one
// (SP-A04-4 — nothing is reconfigured afterwards).
package boot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Values are the five boot values from SP-A04-1, in the order of its table. trust_anchor is not
// among them: it lies in the system image, not in instance data, and arrives with enrollment
// (AP-6.1).
type Values struct {
	Role            string
	Cell            string
	Control         string
	EnrollmentToken string
	LocalityGroup   string
}

// Roles are the four from SP-A02-1, verbatim. `all` is a role like the other three, not a
// wildcard (contract/identity.md).
var Roles = []string{"all", "control", "knowledge", "work"}

const (
	// RunDir is where the sequence leaves its run state. Under /run on purpose: run state does
	// not survive a reboot, so a node can never present yesterday's selftest as today's.
	RunDir = "/run/workpod"

	// SelftestMarker exists if and only if the selftest passed on this boot. The register step
	// requires it (SP-A04-3) — in the unit graph and in the binary, so removing one guard still
	// leaves the other.
	SelftestMarker = RunDir + "/selftest.passed"

	// RegisteredMarker is written by the worker once the control plane has accepted its capacity
	// request — the moment SP-A04-2 calls "register (pulling begins)".
	RegisteredMarker = RunDir + "/registered"

	// NodeIDFile holds this node's identity, created at first start. It lives on /var and so
	// survives an update but not a reinstall (SP-A05-1) — a reinstalled node is a new node.
	NodeIDFile = "/var/lib/workpod/node-id"
)

// Dir returns the systemd credentials directory, the one place boot values come from.
func Dir() string {
	if d := os.Getenv("CREDENTIALS_DIRECTORY"); d != "" {
		return d
	}
	return "/run/credentials/@system"
}

func readCredential(name string) string {
	b, err := os.ReadFile(filepath.Join(Dir(), "workpod."+name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Read collects the five values from the credentials directory. Absence is not an error here —
// Validate is where a missing value becomes a named refusal, so that the caller can say which
// step of A-04 refused.
func Read() Values {
	return Values{
		Role:            readCredential("role"),
		Cell:            readCredential("cell"),
		Control:         readCredential("control"),
		EnrollmentToken: readCredential("enrollment_token"),
		LocalityGroup:   readCredential("locality_group"),
	}
}

// Validate holds the values against SP-A04-1's table: role, cell and control are required for
// every node; the enrollment token for every role but the control plane's (and `all` carries its
// own control plane); locality_group is the one optional value.
func (v Values) Validate() error {
	valid := false
	for _, r := range Roles {
		if v.Role == r {
			valid = true
			break
		}
	}
	switch {
	case v.Role == "":
		return fmt.Errorf("the first start receives exactly five values (SP-A04-1); role is missing")
	case !valid:
		return fmt.Errorf("role %q is none of all, control, knowledge, work (SP-A02-1)", v.Role)
	case v.Cell == "":
		return fmt.Errorf("the first start receives exactly five values (SP-A04-1); cell is missing")
	case v.Control == "":
		return fmt.Errorf("the first start receives exactly five values (SP-A04-1); control is missing")
	case v.EnrollmentToken == "" && !v.CarriesControlPlane():
		return fmt.Errorf("the first start receives exactly five values (SP-A04-1); enrollment_token is missing and role %q is not the control plane", v.Role)
	}
	return nil
}

// CarriesControlPlane says whether this node runs the control plane itself — the one case
// SP-A04-1 exempts from the enrollment token.
func (v Values) CarriesControlPlane() bool { return v.Role == "all" || v.Role == "control" }

// NeedsDB says whether the disk layout of SP-A05-1 gives this node a /data/db — "only control",
// and `all` carries the control layer.
func (v Values) NeedsDB() bool { return v.Role == "all" || v.Role == "control" }

// RunsWorker says whether this node registers for work.
func (v Values) RunsWorker() bool { return v.Role == "all" || v.Role == "work" }
