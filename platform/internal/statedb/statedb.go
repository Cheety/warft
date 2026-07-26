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
	fmt.Println("state database initialized on /data/db — the only area that survives a reinstall (SP-A05-1)")
	return nil
}
