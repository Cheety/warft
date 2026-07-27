package workpod

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Cheety/warft/platform/internal/allocation"
	"github.com/Cheety/warft/platform/internal/runner"
)

// The OCI runtime configuration, as much of it as a workpod uses. Written out by hand rather than
// taken from a library: the whole of what this program needs is four objects, and a dependency on
// the specification's Go types would be a module in the build for a struct definition.
type ociSpec struct {
	Version  string     `json:"ociVersion"`
	Process  ociProcess `json:"process"`
	Root     ociRoot    `json:"root"`
	Hostname string     `json:"hostname"`
	Mounts   []ociMount `json:"mounts"`
	Linux    ociLinux   `json:"linux"`
}

type ociProcess struct {
	Terminal        bool        `json:"terminal"`
	User            ociUser     `json:"user"`
	Args            []string    `json:"args"`
	Env             []string    `json:"env"`
	Cwd             string      `json:"cwd"`
	Capabilities    ociCaps     `json:"capabilities"`
	NoNewPrivileges bool        `json:"noNewPrivileges"`
	Rlimits         []ociRlimit `json:"rlimits"`
}

type ociUser struct {
	UID uint32 `json:"uid"`
	GID uint32 `json:"gid"`
}

type ociCaps struct {
	Bounding    []string `json:"bounding"`
	Effective   []string `json:"effective"`
	Permitted   []string `json:"permitted"`
	Inheritable []string `json:"inheritable"`
	Ambient     []string `json:"ambient"`
}

type ociRlimit struct {
	Type string `json:"type"`
	Hard uint64 `json:"hard"`
	Soft uint64 `json:"soft"`
}

type ociRoot struct {
	Path     string `json:"path"`
	Readonly bool   `json:"readonly"`
}

type ociMount struct {
	Destination string   `json:"destination"`
	Type        string   `json:"type,omitempty"`
	Source      string   `json:"source,omitempty"`
	Options     []string `json:"options,omitempty"`
}

type ociLinux struct {
	Namespaces    []ociNamespace `json:"namespaces"`
	CgroupsPath   string         `json:"cgroupsPath"`
	MaskedPaths   []string       `json:"maskedPaths"`
	ReadonlyPaths []string       `json:"readonlyPaths"`
}

type ociNamespace struct {
	Type string `json:"type"`
}

// PodSlice is where a pod's cgroup goes: inside the work layer of V-01, never in system.slice —
// SP-RC-4 reserves that one for the control plane, and a pod that ate into the reservation would be
// eating the thing that is supposed to survive it.
//
// The format is runc's systemd form, `slice:prefix:name`, which makes the pod's cgroup a transient
// scope with delegation rather than a directory created behind systemd's back.
const PodSlice = "workpod-work.slice"

// cgroupsPath names the pod's cgroup in the form the cgroup manager of this machine takes.
//
// On a node that is always systemd's `slice:prefix:name`, which makes the pod a transient scope with
// delegation under the work layer. The detection is the one runc and containerd use themselves —
// /run/systemd/system exists exactly when systemd is the init of this machine — and it matters
// because the runner is also run where systemd is not: a container in a CI leg, which is where the
// bundle, the mount table and the reaper are exercised without a whole machine around them.
// SP-A02-4 guarantees systemd on a node, so on a node this is never the second branch.
func cgroupsPath(podID string) string {
	if systemdManaged() {
		return PodSlice + ":workpod:" + podID
	}
	return "/" + strings.TrimSuffix(PodSlice, ".slice") + "/" + podID
}

func systemdManaged() bool {
	fi, err := os.Stat("/run/systemd/system")
	return err == nil && fi.IsDir()
}

// writeBundle lays down the OCI bundle of one pod: a root filesystem of the image's mount points,
// the image's read-only layers over it, the working copy, the harness, and the one socket that is
// the pod's only way out.
//
// Everything a pod is *not* is decided here, and by construction rather than by a list of
// prohibitions (SP-T04-2):
//
//   - **no network**: a fresh network namespace with nothing put into it. There is no interface to
//     configure, no route, and — because the image skeleton carries no /etc/resolv.conf — no
//     resolver either (AB-T04-2, AB-B02-3).
//   - **no LLM key, no Git token**: the environment is *built*, never inherited. The runner's own
//     environment does not reach the pod, so a key that is on the node cannot arrive in the pod by
//     forgetting to remove it. Keys are the gates' (AP-3.5).
//   - **no logs**: nothing writable in the pod outlives it. /tmp and /run are tmpfs, the working
//     copy leaves as a patch, and everything the pod says goes to the host through the socket.
func (s Store) writeBundle(podID string, m Manifest, job runner.Job, a allocation.Allocation) (string, error) {
	bundle := filepath.Join(s.RunDir(podID), "bundle")
	rootfs := filepath.Join(bundle, "rootfs")
	if err := os.MkdirAll(rootfs, 0o755); err != nil {
		return "", err
	}
	// The root filesystem is the image's skeleton: mount points and the symbolic links between
	// them, and nothing with content. Every byte a pod reads comes from a layer below, mounted
	// read-only and shared with every other pod on the same image (SP-T04-1).
	if err := copyTree(m.Skeleton, rootfs); err != nil {
		return "", fmt.Errorf("laying down the image skeleton: %w", err)
	}

	podRun := filepath.Join(s.RunDir(podID), "pod")
	spec := ociSpec{
		Version: "1.0.2",
		Process: ociProcess{
			Terminal: false,
			User:     ociUser{UID: 0, GID: 0},
			// The harness is the pod's init process. Not the job's command: the job runs *under*
			// the harness, which is what makes the patch and the report a property of the pod
			// rather than of whatever the command happened to leave behind.
			Args: []string{runner.PodHarness, "harness"},
			Env:  podEnvironment(m, a),
			Cwd:  runner.PodWorkDir,
			// No capability in any set. A pod is a build, not an administrator: it reads and
			// writes files owned by the user it runs as and needs nothing the kernel gates.
			Capabilities: ociCaps{
				Bounding: []string{}, Effective: []string{}, Permitted: []string{},
				Inheritable: []string{}, Ambient: []string{},
			},
			NoNewPrivileges: true,
			Rlimits:         []ociRlimit{{Type: "RLIMIT_NOFILE", Hard: 8192, Soft: 8192}},
		},
		Root:     ociRoot{Path: "rootfs", Readonly: true},
		Hostname: "workpod",
		Mounts:   podMounts(m, s.PodDir(podID), podRun),
		Linux: ociLinux{
			Namespaces: []ociNamespace{
				{Type: "pid"}, {Type: "ipc"}, {Type: "uts"}, {Type: "mount"},
				// The one that matters: a network namespace with nothing in it (SP-T04-2).
				{Type: "network"},
				{Type: "cgroup"},
			},
			CgroupsPath: cgroupsPath(podID),
			MaskedPaths: []string{
				"/proc/acpi", "/proc/asound", "/proc/kcore", "/proc/keys",
				"/proc/latency_stats", "/proc/timer_list", "/proc/timer_stats",
				"/proc/sched_debug", "/sys/firmware", "/proc/scsi",
			},
			ReadonlyPaths: []string{
				"/proc/bus", "/proc/fs", "/proc/irq", "/proc/sys", "/proc/sysrq-trigger",
			},
		},
	}

	body, err := json.MarshalIndent(spec, "", "\t")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(bundle, "config.json"), append(body, '\n'), 0o644); err != nil {
		return "", err
	}
	return bundle, nil
}

// podEnvironment is the pod's whole environment, assembled from three sources and no fourth: what
// the image needs, what SP-RC-5 injects from the allocation, and the two paths of
// decisions/pod-runtime.md §3. os.Environ() is deliberately not among them.
func podEnvironment(m Manifest, a allocation.Allocation) []string {
	env := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=" + runner.PodWorkDir,
		"TMPDIR=/tmp",
	}
	env = append(env, m.Env...)
	// SP-RC-5, last, so a class always beats an image: `os.cpus()` in a container reports the
	// host's cores, and an image that set MAKEFLAGS for the machine it was built on would
	// otherwise hand a `tiny` pod the build parallelism of a build server.
	return append(env, a.Environment()...)
}

func podMounts(m Manifest, workingCopy, podRun string) []ociMount {
	mounts := []ociMount{
		{Destination: "/proc", Type: "proc", Source: "proc",
			Options: []string{"nosuid", "noexec", "nodev"}},
		{Destination: "/dev", Type: "tmpfs", Source: "tmpfs",
			Options: []string{"nosuid", "strictatime", "mode=755", "size=65536k"}},
		{Destination: "/dev/pts", Type: "devpts", Source: "devpts",
			Options: []string{"nosuid", "noexec", "newinstance", "ptmxmode=0666", "mode=0620"}},
		{Destination: "/dev/shm", Type: "tmpfs", Source: "shm",
			Options: []string{"nosuid", "noexec", "nodev", "mode=1777", "size=65536k"}},
		{Destination: "/dev/mqueue", Type: "mqueue", Source: "mqueue",
			Options: []string{"nosuid", "noexec", "nodev"}},
		{Destination: "/sys", Type: "sysfs", Source: "sysfs",
			Options: []string{"nosuid", "noexec", "nodev", "ro"}},
	}

	// The image's layers: shared, read-only, and mounted where the image says they go.
	for _, l := range m.Layers {
		mounts = append(mounts, ociMount{
			Destination: l.Destination, Type: "bind", Source: l.Source,
			Options: []string{"rbind", "ro", "nosuid", "nodev"},
		})
	}

	mounts = append(mounts,
		// The writable places, all three of them volatile. A pod that wants to keep something has
		// to hand it out through the patch or the socket.
		ociMount{Destination: "/tmp", Type: "tmpfs", Source: "tmpfs",
			Options: []string{"nosuid", "nodev", "mode=1777", "size=262144k"}},
		ociMount{Destination: "/run", Type: "tmpfs", Source: "tmpfs",
			Options: []string{"nosuid", "nodev", "mode=755", "size=65536k"}},

		// The working copy: the CoW snapshot, and the only thing in the pod whose changes are the
		// point of the pod.
		ociMount{Destination: runner.PodWorkDir, Type: "bind", Source: workingCopy,
			Options: []string{"rbind", "rw", "nosuid", "nodev"}},

		// SP-E02-4: the agent harness is the same binary, mounted read-only. It is bound from the
		// host's own /usr/bin/workpod and not from any image layer, which is what makes a harness
		// update an image update rather than a rebuild of every container image (AB-E02-4).
		ociMount{Destination: runner.PodHarness, Type: "bind", Source: harnessBinary(),
			Options: []string{"rbind", "ro", "nosuid", "nodev"}},

		// The job, read-only: what the pod is for is not something the pod may edit.
		ociMount{Destination: runner.PodJobFile, Type: "bind", Source: filepath.Join(podRun, "job.json"),
			Options: []string{"rbind", "ro", "nosuid", "nodev", "noexec"}},

		// The only way out (SP-T04-2). Everything the pod cannot do itself — a fact query, an
		// effect, a gap report — goes through this one file.
		ociMount{Destination: runner.PodSocket, Type: "bind", Source: filepath.Join(podRun, "harness.sock"),
			Options: []string{"rbind", "rw", "nosuid", "nodev", "noexec"}},

		// Where the patch and the report are left. Read by the host after the pod is gone, which is
		// why it is a bind from /run and not a directory inside the working copy: a patch that lived
		// in the tree it describes would be part of itself.
		ociMount{Destination: runner.PodOutDir, Type: "bind", Source: filepath.Join(podRun, "out"),
			Options: []string{"rbind", "rw", "nosuid", "nodev", "noexec"}},
	)
	return mounts
}

// harnessBinary is the running binary's own path. SP-E02-4's "the same binary" is meant literally:
// the pod runs the file the worker is running, not a copy of it and not a build of it.
func harnessBinary() string {
	if p, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return resolved
		}
		return p
	}
	return "/usr/bin/workpod"
}
