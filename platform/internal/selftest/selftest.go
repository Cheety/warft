// Package selftest is the `selftest` step of the A-04 start sequence — a subset of A-06, run by
// the node on itself before it registers (SP-A04-2). A failed selftest means: do not enroll
// (SP-A04-3). The pass is recorded as a marker under /run, which the register step requires; the
// marker cannot survive a reboot, so every boot earns its own.
//
// The subset holds what registering would rely on:
//
//	verity      the root is the sealed artifact, checked block by block (SP-A03-3)
//	cgroups     cgroup v2 unified with PSI — R-A and R-C stand on both (SP-A02-2)
//	slices      the role's layer slices are up (SP-V01-1, SP-A02-1)
//	memory.min  the system slice is reserved, the net beneath it armed (SP-RC-4)
//	layout      /var, /data/work, /data/db mounted per SP-A05-1; reflink works on the work
//	            volume (SP-A05-2); the work disk is not the data disk (SP-A05-3)
//	database    the state database answers on its socket — control roles only (SP-E02-2)
//	clock       time discipline is active before enrollment (SP-A04-5); whether a server was
//	            reached depends on there being a network, so synchronization is reported, not
//	            demanded — the same stance A-06 takes for AB-K04-7
package selftest

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Cheety/warft/platform/internal/boot"
	"github.com/Cheety/warft/platform/internal/cgroup"
)

const dbSocket = "/run/workpod-db/.s.PGSQL.5432"

// systemSliceMin is SP-RC-4's default of 4 GB, as the drop-in in the image writes it.
const systemSliceMin = 4 * 1024 * 1024 * 1024

type check struct {
	name   string
	detail string
	ok     bool
}

// Run is `workpod selftest`. It prints one line per check, writes the marker if and only if
// everything held, and exits non-zero otherwise — which is what keeps the register unit from
// starting (SP-A04-3).
func Run() error {
	v := boot.Read()
	if err := v.Validate(); err != nil {
		return err
	}

	var checks []check
	add := func(name string, ok bool, detail string) {
		checks = append(checks, check{name, detail, ok})
	}

	checkVerity(add)
	checkCgroups(add)
	checkSlices(v, add)
	checkMemoryMin(add)
	checkLayout(v, add)
	if v.NeedsDB() {
		checkDB(add)
	}
	checkClock(add)

	failed := 0
	for _, c := range checks {
		state := "PASS"
		if !c.ok {
			state = "FAIL"
			failed++
		}
		fmt.Printf("selftest %s  %-24s %s\n", state, c.name, c.detail)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d checks failed — the node does not register (SP-A04-3)", failed, len(checks))
	}

	if err := os.MkdirAll(boot.RunDir, 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf("passed_at=%s\nchecks=%d\nrole=%s\n", time.Now().UTC().Format(time.RFC3339), len(checks), v.Role)
	if err := os.WriteFile(boot.SelftestMarker, []byte(body), 0o644); err != nil {
		return err
	}
	fmt.Printf("selftest: %d checks passed — the node may register (SP-A04-2)\n", len(checks))
	return nil
}

func checkVerity(add func(string, bool, string)) {
	cmdline, _ := os.ReadFile("/proc/cmdline")
	hasHash := strings.Contains(string(cmdline), "roothash=")
	add("verity roothash", hasHash, "roothash= on the kernel command line")

	verity := false
	dms, _ := filepath.Glob("/sys/devices/virtual/block/dm-*/dm/uuid")
	for _, f := range dms {
		b, err := os.ReadFile(f)
		if err == nil && strings.HasPrefix(string(b), "CRYPT-VERITY-") {
			verity = true
		}
	}
	add("dm-verity carries the root", verity, "checked block by block as it is read (SP-A03-3)")
}

func checkCgroups(add func(string, bool, string)) {
	b, err := os.ReadFile("/sys/fs/cgroup/cgroup.controllers")
	unified := err == nil && strings.Contains(string(b), "memory")
	add("cgroup v2 unified", unified, "memory controller on the unified hierarchy (SP-A02-2)")

	_, err = os.ReadFile("/proc/pressure/memory")
	add("PSI", err == nil, "pressure, not utilization, is what R-C reads (SP-RC-1)")
}

// layerSlices names the slices a role carries: the four layers on one machine, each layer's own
// on a dedicated node (SP-V01-1).
func layerSlices(v boot.Values) []string {
	switch v.Role {
	case "all":
		return []string{"workpod-control.slice", "workpod-captain.slice", "workpod-knowledge.slice", "workpod-work.slice"}
	case "control":
		return []string{"workpod-control.slice"}
	case "knowledge":
		return []string{"workpod-knowledge.slice"}
	case "work":
		return []string{"workpod-work.slice"}
	}
	return nil
}

func checkSlices(v boot.Values, add func(string, bool, string)) {
	for _, s := range layerSlices(v) {
		out, err := exec.Command("systemctl", "is-active", s).Output()
		active := err == nil && strings.TrimSpace(string(out)) == "active"
		add(s, active, "a layer of V-01, as a slice on this machine")
	}
}

func checkMemoryMin(add func(string, bool, string)) {
	min, err := cgroup.MemoryMin("system.slice")
	ok := err == nil && min >= systemSliceMin
	detail := fmt.Sprintf("system.slice memory.min=%d — reserves control plane, proxies and access (SP-RC-4)", min)
	if err != nil {
		detail = err.Error()
	}
	add("memory.min", ok, detail)
}

func checkLayout(v boot.Values, add func(string, bool, string)) {
	varSrc, varFS := mountEntry("/var")
	varDisk := underlyingDisk(varSrc)
	add("/var mounted", varFS == "btrfs", fmt.Sprintf("fstype %q on disk %q (SP-A05-1)", varFS, varDisk))

	workSrc, workFS := mountEntry("/data/work")
	workDisk := underlyingDisk(workSrc)
	add("/data/work mounted", workFS == "btrfs", fmt.Sprintf("fstype %q on disk %q (SP-A05-1)", workFS, workDisk))

	sep := varDisk != "" && workDisk != "" && varDisk != workDisk
	add("work disk separate", sep, fmt.Sprintf("work on %q, data on %q — the work disk separate from the data disk (SP-A05-3)", workDisk, varDisk))

	if v.NeedsDB() {
		dbSrc, dbFS := mountEntry("/data/db")
		dbDisk := underlyingDisk(dbSrc)
		add("/data/db mounted", dbFS != "", fmt.Sprintf("fstype %q on disk %q — the only area that survives a reinstall (SP-A05-1)", dbFS, dbDisk))
		sepDB := dbDisk != "" && workDisk != "" && dbDisk != workDisk
		add("db off the work disk", sepDB, "state does not share a spindle with snapshots (SP-A05-3)")
	}

	add("reflink on /data/work", reflinkWorks("/data/work"), "an O(1) copy is a foundation, not a preference (SP-A05-2)")
}

func checkDB(add func(string, bool, string)) {
	// The database may still be starting when this runs; a socket that appears within the wait
	// is up, one that does not is down. Thirty seconds is the disk step's own device patience.
	deadline := time.Now().Add(30 * time.Second)
	for {
		conn, err := net.DialTimeout("unix", dbSocket, 2*time.Second)
		if err == nil {
			conn.Close()
			add("state database", true, "Postgres answers on "+dbSocket+" (SP-E02-2, on /data/db)")
			return
		}
		if time.Now().After(deadline) {
			add("state database", false, fmt.Sprintf("no answer on %s: %v", dbSocket, err))
			return
		}
		time.Sleep(time.Second)
	}
}

func checkClock(add func(string, bool, string)) {
	out, err := exec.Command("systemctl", "is-active", "chronyd.service").Output()
	active := err == nil && strings.TrimSpace(string(out)) == "active"
	sync, _ := exec.Command("timedatectl", "show", "--property=NTPSynchronized", "--value").Output()
	add("clock discipline", active,
		fmt.Sprintf("chronyd active, synchronized=%s — the clock before enrollment (SP-A04-5); reaching a server needs a network and is reported, not demanded", strings.TrimSpace(string(sync))))
}

// mountEntry returns the source device path and filesystem type mounted at target — the LAST
// mountinfo entry wins, which is what makes an over-mounted volatile /var read as the disk, not
// the tmpfs. The source is taken as a path, not as mountinfo's major:minor: btrfs reports an
// anonymous device number there (0:xx, one per superblock), and /sys knows no such block device
// — the first CI run of AP-3.1 measured every disk as "" through exactly that hole.
func mountEntry(target string) (src, fstype string) {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 5 || f[4] != target {
			continue
		}
		for i, tok := range f {
			if tok == "-" && i+2 < len(f) {
				fstype, src = f[i+1], f[i+2]
			}
		}
	}
	return src, fstype
}

// underlyingDisk walks a device path down to the whole disk that carries it, through device
// mapper (an encrypted /var) and partitions alike.
func underlyingDisk(src string) string {
	if !strings.HasPrefix(src, "/dev/") {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(src)
	if err != nil {
		return ""
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return ""
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	majmin := fmt.Sprintf("%d:%d", unix.Major(uint64(st.Rdev)), unix.Minor(uint64(st.Rdev)))
	for range 8 {
		sys, err := filepath.EvalSymlinks(filepath.Join("/sys/dev/block", majmin))
		if err != nil {
			return ""
		}
		// device mapper: descend into the (single) slave underneath.
		slaves, _ := os.ReadDir(filepath.Join(sys, "slaves"))
		if len(slaves) > 0 {
			b, err := os.ReadFile(filepath.Join(sys, "slaves", slaves[0].Name(), "dev"))
			if err != nil {
				return ""
			}
			majmin = strings.TrimSpace(string(b))
			continue
		}
		// partition: the parent directory is the disk.
		if _, err := os.Stat(filepath.Join(sys, "partition")); err == nil {
			return filepath.Base(filepath.Dir(sys))
		}
		return filepath.Base(sys)
	}
	return ""
}

// reflinkWorks proves SP-A05-2 by doing it: write a file, clone it with FICLONE, and let the
// kernel say whether the filesystem can. A statfs on the fs type would check a name; this checks
// the property the platform actually uses.
func reflinkWorks(dir string) bool {
	src, err := os.CreateTemp(dir, ".selftest-reflink-src-*")
	if err != nil {
		return false
	}
	defer os.Remove(src.Name())
	defer src.Close()
	if _, err := src.Write(make([]byte, 65536)); err != nil {
		return false
	}
	dst, err := os.CreateTemp(dir, ".selftest-reflink-dst-*")
	if err != nil {
		return false
	}
	defer os.Remove(dst.Name())
	defer dst.Close()
	return unix.IoctlFileClone(int(dst.Fd()), int(src.Fd())) == nil
}
