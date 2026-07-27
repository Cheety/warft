// Package statedb initializes the state database's data directory on /data/db — the one area of
// SP-A05-1 that survives a reinstall, and the only state database there is (SP-E02-2, E-02).
//
// This is `workpod db-init`, run as ExecStartPre of workpod-db.service with root privilege while
// the service itself runs as postgres. It creates the cluster once and touches nothing on any
// later boot: state has exactly one writer, and after initialization that writer is Postgres.
package statedb

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"
)

const pgData = "/data/db/pg"

// Init creates the Postgres cluster under /data/db if none exists.
func Init() error {
	if _, err := os.Stat(pgData + "/PG_VERSION"); err == nil {
		return nil
	}
	u, err := user.Lookup("postgres")
	if err != nil {
		return fmt.Errorf("no postgres user in the image — postgresql-server belongs to the userland (SP-A02-3): %w", err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)

	if err := os.MkdirAll(pgData, 0o700); err != nil {
		return err
	}
	if err := os.Chown(pgData, uid, gid); err != nil {
		return err
	}

	// initdb refuses to run as root, rightly. --auth=peer: the socket is the only door
	// (listen_addresses is empty in the unit), and peer makes the caller's identity the
	// credential — stated by the kernel, not claimed by a password.
	cmd := exec.Command("initdb", "--pgdata="+pgData, "--auth=peer", "--encoding=UTF8", "--locale=C.UTF-8", "--no-instructions")
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}}
	cmd.Env = append(os.Environ(), "HOME=/tmp")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("initdb: %w\n%s", err, out)
	}
	if err := mapCallers(); err != nil {
		return err
	}
	fmt.Println("state database initialized on /data/db — the only area that survives a reinstall (SP-A05-1)")
	return nil
}

// mapCallers lets the control plane through its own front door.
//
// The socket is the only door and peer authentication is the credential: the kernel states which
// OS user is knocking. The control plane runs as root (it holds the node's cgroups), the cluster's
// one role is `postgres`, and peer with no map demands that the two names be equal — so without
// this the plane would be locked out of the database it owns.
//
// An ident map is the narrow answer: it names exactly which OS users may present as `postgres`,
// which is a shorter list than a second role with its own grants would be, and it leaves the
// method itself untouched. Nothing is opened to the network; listen_addresses stays empty.
func mapCallers() error {
	hbaPath := pgData + "/pg_hba.conf"
	hba, err := os.ReadFile(hbaPath)
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, line := range strings.Split(string(hba), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == "local" &&
			fields[len(fields)-1] == "peer" {
			line += " map=workpod"
		}
		b.WriteString(line + "\n")
	}
	if err := os.WriteFile(hbaPath, []byte(strings.TrimSuffix(b.String(), "\n")), 0o600); err != nil {
		return err
	}

	// MAPNAME  SYSTEM-USERNAME  PG-USERNAME
	const identMap = "\n# The platform's own callers (workpod db-init):\n" +
		"workpod\troot\t\tpostgres\n" +
		"workpod\tpostgres\tpostgres\n"
	f, err := os.OpenFile(pgData+"/pg_ident.conf", os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(identMap)
	return err
}
