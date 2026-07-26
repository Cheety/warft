// Package disk is the `disk` step of the A-04 start sequence: find, decrypt, otherwise create
// (SP-A04-2), against the layout of SP-A05-1.
//
//	area          partition label   survives an update   survives a reinstall
//	/var          workpod-var       yes                  no
//	/data/work    workpod-work      yes                  no  (reproducible)
//	/data/db      workpod-db        yes                  yes — the only one
//
// The step is mechanical on purpose: it finds partitions by their GPT label, creates them on the
// named empty disks when they do not exist, makes filesystems where none are, and mounts. It does
// not judge the result — judging is the selftest's (SP-A04-2), so that a layout which violates
// A-05 is refused by the same instrument that refuses every other broken precondition.
//
// The disks are addressed by ID: /dev/disk/by-id/*workpod-data* carries /var and /data/db,
// /dev/disk/by-id/*workpod-work* carries /data/work. The work disk is separate from the data disk
// (SP-A05-3), and a machine that offers only one disk gets no layout rather than a merged one.
// The ID comes with the machine the way the five boot values do — instance data names the disks
// it attaches; nothing here guesses which spindle to overwrite.
package disk

import (
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Cheety/warft/platform/internal/boot"
)

const (
	byID        = "/dev/disk/by-id"
	byPartlabel = "/dev/disk/by-partlabel"

	labelVar  = "workpod-var"
	labelWork = "workpod-work"
	labelDB   = "workpod-db"

	mapperVar = "/dev/mapper/workpod-var"

	mountVar  = "/var"
	mountWork = "/data/work"
	mountDB   = "/data/db"

	// deviceWait bounds how long the step waits for udev to surface the disks and the partition
	// labels it creates. Thirty seconds is generous for virtio and small metal alike; a machine
	// slower than that has a problem this step should name, not absorb.
	deviceWait = 30 * time.Second
)

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func say(format string, a ...any) { fmt.Printf("A-04 disk: "+format+"\n", a...) }

// Run is `workpod disk` — the whole of SP-A04-2's disk step.
//
// It validates the five boot values first (SP-A04-1): the disk step is the head of the sequence,
// and a node that was handed less than its five values refuses here, before it touches a disk.
func Run() error {
	v := boot.Read()
	if err := v.Validate(); err != nil {
		return fmt.Errorf("refusing before the disk is touched: %w", err)
	}
	say("boot values complete: role=%s cell=%s control=%s locality_group=%q", v.Role, v.Cell, v.Control, v.LocalityGroup)

	parts, err := findOrCreate(v)
	if err != nil {
		return err
	}

	// /var first: decrypt or format, then mount. The mount replaces the volatile tmpfs the image
	// boots with — everything the platform keeps under /var from here on lands on the disk that
	// survives an update.
	varDev, err := prepareVar(parts.varPart)
	if err != nil {
		return err
	}
	if err := ensureFS(parts.workPart, labelWork); err != nil {
		return err
	}
	if v.NeedsDB() {
		if err := ensureFS(parts.dbPart, labelDB); err != nil {
			return err
		}
	}

	if err := mount(varDev, mountVar); err != nil {
		return err
	}
	if err := mount(parts.workPart, mountWork); err != nil {
		return err
	}
	if v.NeedsDB() {
		if err := mount(parts.dbPart, mountDB); err != nil {
			return err
		}
	}
	// The image boots with a volatile /var and this mount lands over it, so the standard /var
	// structure the early boot created on the tmpfs has to be recreated on the disk. tmpfiles
	// grumbles about lines it cannot apply this late; what matters is the structure, not its
	// exit code.
	_ = run("systemd-tmpfiles", "--create", "--prefix=/var")
	if err := os.MkdirAll("/var/lib/workpod", 0o755); err != nil {
		return err
	}
	say("layout stands: /var, /data/work%s (SP-A05-1)", map[bool]string{true: ", /data/db", false: ""}[v.NeedsDB()])
	return nil
}

// Reinstall is `workpod disk reinstall` — the installer's act when a node is set up anew over
// existing disks. It wipes the filesystem signatures of /var and /data/work and leaves /data/db
// untouched: SP-A05-1's last column says only /data/db survives a reinstall, and this is the
// mechanism that makes the other two rows true. The next run of the disk step finds the
// partitions bare and formats them fresh.
func Reinstall() error {
	varPart, err1 := waitFor(filepath.Join(byPartlabel, labelVar), deviceWait)
	workPart, err2 := waitFor(filepath.Join(byPartlabel, labelWork), deviceWait)
	if err1 != nil || err2 != nil {
		return fmt.Errorf("reinstall wipes an existing layout; there is none (no %s/%s partitions)", labelVar, labelWork)
	}
	// /var is always a mount (the image boots it volatile), so the question is whether the DISK
	// step's /var stands there — tmpfs means it does not, and the reinstall may proceed.
	if fsAt(mountVar) == "btrfs" || mountedSource(mountWork) != "" {
		return fmt.Errorf("the layout is mounted; a reinstall happens before the disk step, not beside it")
	}
	// wipefs on the LUKS or btrfs signature is the erase: for an encrypted /var it destroys the
	// only copy of the key slots, which is a crypto-erase of everything behind them.
	if err := run("wipefs", "--all", varPart); err != nil {
		return err
	}
	if err := run("wipefs", "--all", workPart); err != nil {
		return err
	}
	say("reinstall: /var and /data/work wiped, /data/db kept — only /data/db survives a reinstall (SP-A05-1)")
	return nil
}

type partitions struct {
	varPart, workPart, dbPart string
}

// findOrCreate resolves the three partitions by label, creating the partition tables on the named
// empty disks when a node starts on bare metal for the first time.
func findOrCreate(v boot.Values) (partitions, error) {
	var p partitions
	var err error

	p.varPart, err = waitFor(filepath.Join(byPartlabel, labelVar), 2*time.Second)
	if err == nil {
		// find: the layout exists. The work partition must be there too; the db partition only
		// where the role carries one.
		if p.workPart, err = waitFor(filepath.Join(byPartlabel, labelWork), 2*time.Second); err != nil {
			return p, fmt.Errorf("found %s but no %s — half a layout is no layout", labelVar, labelWork)
		}
		if v.NeedsDB() {
			if p.dbPart, err = waitFor(filepath.Join(byPartlabel, labelDB), 2*time.Second); err != nil {
				return p, fmt.Errorf("role %s keeps its state on /data/db and there is no %s partition", v.Role, labelDB)
			}
		}
		say("found the layout by partition label")
		return p, nil
	}

	// create: no layout yet. The two disks are found by ID; each must be bare, because the one
	// thing this step never does is guess that a disk with content is expendable.
	dataDisk, err := diskByID("workpod-data")
	if err != nil {
		return p, err
	}
	workDisk, err := diskByID("workpod-work")
	if err != nil {
		return p, err
	}
	for _, d := range []string{dataDisk, workDisk} {
		if hasSignature(d) {
			return p, fmt.Errorf("%s carries a filesystem or partition table; creating a layout would destroy it — refusing", d)
		}
	}

	// The data disk: /var beside /data/db, a quarter to /var. The split is an implementation
	// default, not a spec value — SP-A05-1 fixes what lives where and what survives, not sizes.
	if v.NeedsDB() {
		size, err := diskBytes(dataDisk)
		if err != nil {
			return p, err
		}
		varMiB := size / 4 / (1024 * 1024)
		script := fmt.Sprintf("label: gpt\nname=%s, size=%dMiB\nname=%s\n", labelVar, varMiB, labelDB)
		if err := sfdisk(dataDisk, script); err != nil {
			return p, err
		}
	} else {
		script := fmt.Sprintf("label: gpt\nname=%s\n", labelVar)
		if err := sfdisk(dataDisk, script); err != nil {
			return p, err
		}
	}
	if err := sfdisk(workDisk, fmt.Sprintf("label: gpt\nname=%s\n", labelWork)); err != nil {
		return p, err
	}
	_ = run("udevadm", "settle")

	if p.varPart, err = waitFor(filepath.Join(byPartlabel, labelVar), deviceWait); err != nil {
		return p, err
	}
	if p.workPart, err = waitFor(filepath.Join(byPartlabel, labelWork), deviceWait); err != nil {
		return p, err
	}
	if v.NeedsDB() {
		if p.dbPart, err = waitFor(filepath.Join(byPartlabel, labelDB), deviceWait); err != nil {
			return p, err
		}
	}
	say("created the layout: data disk %s, work disk %s (SP-A05-3: separate)", dataDisk, workDisk)
	return p, nil
}

// prepareVar decrypts or formats the /var partition and returns the device to mount.
//
// SP-A05-4 rules a TPM binding, not a passphrase. Where the machine has a TPM the partition is
// LUKS2 with the key enrolled against it and no other way in; where it has none, /var stays
// plain and this step says so out loud — "TPM-bound where possible" is AP-6.1's own phrasing,
// and AB-A05-4 (the node starts without a human) is the row that measures the binding.
func prepareVar(part string) (string, error) {
	typ := blkidType(part)
	switch typ {
	case "crypto_LUKS":
		// decrypt: the TPM answers, no human does (SP-A05-4).
		if err := run("/usr/lib/systemd/systemd-cryptsetup", "attach", labelVar, part, "-", "tpm2-device=auto"); err != nil {
			return "", fmt.Errorf("the /var partition is encrypted and the TPM did not open it: %w", err)
		}
		say("/var decrypted against the TPM")
		return mapperVar, nil
	case "btrfs":
		say("/var found plain — the TPM binding lands with AP-6.1 (AB-A05-4)")
		return part, nil
	case "":
		return formatVar(part)
	default:
		return "", fmt.Errorf("%s carries %q, which is not this platform's /var — refusing", part, typ)
	}
}

func formatVar(part string) (string, error) {
	if _, err := os.Stat("/dev/tpmrm0"); err == nil {
		if dev, err := formatVarLUKS(part); err == nil {
			return dev, nil
		} else {
			// The TPM is there but the enrollment failed — say so and fall through to plain
			// rather than leaving the node without a /var. The gap has a row (AB-A05-4, AP-6.1);
			// a node that cannot start has none.
			say("TPM present but the LUKS enrollment failed (%v) — /var stays plain, AB-A05-4 stays red", err)
			_ = run("wipefs", "--all", part)
		}
	} else {
		say("no TPM on this machine — /var stays plain until AP-6.1 (SP-A05-4, AB-A05-4)")
	}
	if err := run("mkfs.btrfs", "--quiet", "--label", labelVar, part); err != nil {
		return "", err
	}
	return part, nil
}

func formatVarLUKS(part string) (string, error) {
	// The format key exists only long enough to enroll the TPM: written to /run, used twice,
	// destroyed. After the enrollment the TPM slot is the only slot, which is exactly "a TPM
	// binding, not a passphrase" (SP-A05-4).
	kf := filepath.Join(boot.RunDir, "var.newkey")
	if err := os.MkdirAll(boot.RunDir, 0o755); err != nil {
		return "", err
	}
	key := make([]byte, 64)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	if err := os.WriteFile(kf, key, 0o600); err != nil {
		return "", err
	}
	defer os.Remove(kf)

	// pbkdf2 with a forced low iteration count is not a weakening here: the slot key is 64 bytes
	// from the kernel's RNG, and a memory-hard KDF defends low-entropy passphrases, of which this
	// is the opposite. What argon2id would add is seconds of boot time per unlock and nothing
	// else. SP-A05-4 forbids passphrases; this is the shape of that rule in the format call.
	if err := run("cryptsetup", "luksFormat", "--batch-mode", "--type", "luks2",
		"--pbkdf", "pbkdf2", "--pbkdf-force-iterations", "1000", "--key-file", kf, part); err != nil {
		return "", err
	}
	if err := run("systemd-cryptenroll", "--unlock-key-file="+kf, "--tpm2-device=auto", part); err != nil {
		return "", err
	}
	if err := run("systemd-cryptenroll", "--wipe-slot=0", part); err != nil {
		return "", err
	}
	if err := run("/usr/lib/systemd/systemd-cryptsetup", "attach", labelVar, part, "-", "tpm2-device=auto"); err != nil {
		return "", err
	}
	if err := run("mkfs.btrfs", "--quiet", "--label", labelVar, mapperVar); err != nil {
		return "", err
	}
	say("/var encrypted, the key enrolled against the TPM and nowhere else (SP-A05-4)")
	return mapperVar, nil
}

// ensureFS makes a btrfs where the partition has no filesystem yet — the state a fresh partition
// and a reinstalled one share. A foreign filesystem is refused, never overwritten.
func ensureFS(part, label string) error {
	switch typ := blkidType(part); typ {
	case "btrfs":
		return nil
	case "":
		return run("mkfs.btrfs", "--quiet", "--label", label, part)
	default:
		return fmt.Errorf("%s carries %q, not btrfs — refusing to overwrite it (SP-A05-2)", part, typ)
	}
}

// mount puts dev on target unless that exact device already stands there. The comparison is by
// device, not by path: /var is always a mount (the image boots it volatile), and the whole point
// of this step is to put the disk over the tmpfs — a target-only check would skip it.
func mount(dev, target string) error {
	if src := mountedSource(target); src != "" && src == devMajMin(dev) {
		return nil
	}
	if err := run("mount", "-t", "btrfs", dev, target); err != nil {
		return err
	}
	say("%s mounted from %s", target, dev)
	return nil
}

// mountedSource returns the major:minor mounted at target — the last mountinfo entry wins, so an
// over-mounted /var reads as the disk, not the tmpfs beneath it. Empty when nothing is mounted.
func mountedSource(target string) string {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return ""
	}
	src := ""
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) > 4 && f[4] == target {
			src = f[2]
		}
	}
	return src
}

// fsAt returns the filesystem type mounted at target, last entry winning.
func fsAt(target string) string {
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return ""
	}
	typ := ""
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) > 4 && f[4] == target {
			for i, tok := range f {
				if tok == "-" && i+1 < len(f) {
					typ = f[i+1]
				}
			}
		}
	}
	return typ
}

func devMajMin(dev string) string {
	fi, err := os.Stat(dev)
	if err != nil {
		return ""
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d", unix.Major(uint64(st.Rdev)), unix.Minor(uint64(st.Rdev)))
}

func waitFor(path string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return resolved, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("no device at %s within %s", path, timeout)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// diskByID finds the one disk whose /dev/disk/by-id name contains the given tag, waiting for
// udev to surface it.
func diskByID(tag string) (string, error) {
	deadline := time.Now().Add(deviceWait)
	for {
		entries, _ := os.ReadDir(byID)
		// Deduplicated over the resolved device: udev may give one disk several by-id names,
		// and aliases of one disk are one disk, not an ambiguity.
		devices := map[string]bool{}
		for _, e := range entries {
			name := e.Name()
			// -part suffixes are partitions of the disk, not the disk.
			if !strings.Contains(name, tag) || strings.Contains(name, "-part") {
				continue
			}
			if dev, err := filepath.EvalSymlinks(filepath.Join(byID, name)); err == nil {
				devices[dev] = true
			}
		}
		if len(devices) == 1 {
			for dev := range devices {
				return dev, nil
			}
		}
		if len(devices) > 1 {
			return "", fmt.Errorf("%d disks match %q under %s — ambiguity is refusal, not choice", len(devices), tag, byID)
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("no disk matching %q under %s within %s — the disk layout of SP-A05-1 needs a data disk and a separate work disk (SP-A05-3)", tag, byID, deviceWait)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func hasSignature(dev string) bool {
	out, err := exec.Command("blkid", "--probe", "--output", "export", dev).Output()
	// blkid exits 2 when nothing was found — the bare-disk case.
	if err != nil {
		return false
	}
	s := string(out)
	return strings.Contains(s, "TYPE=") || strings.Contains(s, "PTTYPE=")
}

func blkidType(dev string) string {
	out, err := exec.Command("blkid", "--probe", "--output", "value", "--match-tag", "TYPE", dev).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func diskBytes(dev string) (int64, error) {
	base := filepath.Base(dev)
	b, err := os.ReadFile(filepath.Join("/sys/class/block", base, "size"))
	if err != nil {
		return 0, err
	}
	sectors, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0, err
	}
	return sectors * 512, nil
}

func sfdisk(dev, script string) error {
	cmd := exec.Command("sfdisk", "--quiet", dev)
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sfdisk %s: %w\n%s", dev, err, strings.TrimSpace(string(out)))
	}
	return nil
}
