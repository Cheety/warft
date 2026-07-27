package workpod

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cheety/warft/platform/internal/allocation"
	"github.com/Cheety/warft/platform/internal/runner"
)

func scratch(t *testing.T) Store {
	t.Helper()
	root := t.TempDir()
	return Store{
		Work: filepath.Join(root, "work"),
		Run:  filepath.Join(root, "run"),
		Var:  filepath.Join(root, "var"),
	}
}

func aJob() runner.Job {
	return runner.Job{
		OrderID: "018f4242-0000-7000-8000-000000000001", Attempt: 1,
		Cell: "eu-c1", Project: "018f4242-0000-7000-8000-00000000000b",
		Class: "small", Command: []string{"true"},
		Requirements: runner.Requirements{Language: "go", LanguageVersion: "1.24"},
	}
}

// SP-T03-1's other half: a miss is a build job, and it is a build job that stands on the disk after
// the runner has refused.
func TestAMissProducesABuildJob(t *testing.T) {
	s := scratch(t)
	job := aJob()

	_, err := s.Resolve(job)
	var miss *Miss
	if !errors.As(err, &miss) {
		t.Fatalf("an empty index answered with %v, not with a miss", err)
	}
	if miss.RequirementHash != job.Requirements.Hash() {
		t.Errorf("the miss names %s, the job asks for %s", miss.RequirementHash, job.Requirements.Hash())
	}

	b, err := os.ReadFile(miss.BuildJobPath)
	if err != nil {
		t.Fatalf("no build job at %s: %v", miss.BuildJobPath, err)
	}
	var bj buildJob
	if err := json.Unmarshal(b, &bj); err != nil {
		t.Fatal(err)
	}
	if bj.Idempotency != "image-build:"+miss.RequirementHash {
		t.Errorf("the build job's key is %q — two pods missing the same image must produce one job", bj.Idempotency)
	}
	if bj.Acceptance == "" {
		t.Error("a job without an acceptance criterion is not a job (SP-Q01-6)")
	}
	if bj.Requirements.Language != job.Requirements.Language || bj.Requirements.LanguageVersion != job.Requirements.LanguageVersion {
		t.Errorf("the build job does not carry what is to be built: %+v", bj.Requirements)
	}
	if bj.Class != "large" {
		t.Errorf("a build job is sized %q; SP-RA-1 gives `large` the row \"monorepo, E2E, image build\"", bj.Class)
	}

	// A second miss on the same requirements must not make a second build job.
	second := aJob()
	second.OrderID = "018f4242-0000-7000-8000-000000000002"
	if _, err := s.Resolve(second); !errors.As(err, &miss) {
		t.Fatalf("the second miss answered with %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(s.Var, "buildjobs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("%d build jobs for one missing image", len(entries))
	}
}

// SP-T03-4: content-addressed. A manifest whose name is not its content is a broken index entry,
// and reading it must say so rather than run a pod on it.
func TestAManifestIsItsContent(t *testing.T) {
	s := scratch(t)
	if err := os.MkdirAll(filepath.Join(s.Work, "images"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := Manifest{SkeletonDigest: "sha256:aaa", Layers: []Layer{{Source: "/usr", Destination: "/usr", Digest: "path:/usr"}}}
	body, _ := json.Marshal(m)
	lie := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	if err := os.WriteFile(s.manifestPath(lie), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Manifest(lie); err == nil {
		t.Fatal("a manifest under the wrong name was accepted")
	}
	if err := os.WriteFile(s.manifestPath(m.Digest()), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Manifest(m.Digest()); err != nil {
		t.Fatalf("a manifest under its own name was refused: %v", err)
	}
}

// The digest covers what a pod would see and nothing else. Two images built for different
// requirements out of the same tree are one image; two images with different content are two.
func TestTheDigestCoversWhatAPodSees(t *testing.T) {
	base := Manifest{SkeletonDigest: "sha256:aaa", Layers: []Layer{{Destination: "/usr", Digest: "d1"}}, Env: []string{"A=1"}}

	other := base
	other.Requirements = runner.Requirements{Language: "go"}
	other.RequirementHash = "sha256:whatever"
	if base.Digest() != other.Digest() {
		t.Error("the requirements changed the image digest; they are why an image was built, not what it is")
	}

	for name, changed := range map[string]Manifest{
		"another skeleton": {SkeletonDigest: "sha256:bbb", Layers: base.Layers, Env: base.Env},
		"another layer":    {SkeletonDigest: base.SkeletonDigest, Layers: []Layer{{Destination: "/usr", Digest: "d2"}}, Env: base.Env},
		"another env":      {SkeletonDigest: base.SkeletonDigest, Layers: base.Layers, Env: []string{"A=2"}},
	} {
		if base.Digest() == changed.Digest() {
			t.Errorf("%s did not change the digest", name)
		}
	}

	// Layer order is not content: the same layers listed the other way round are the same image.
	two := Manifest{SkeletonDigest: "sha256:aaa", Layers: []Layer{{Destination: "/a", Digest: "1"}, {Destination: "/b", Digest: "2"}}}
	reversed := Manifest{SkeletonDigest: "sha256:aaa", Layers: []Layer{{Destination: "/b", Digest: "2"}, {Destination: "/a", Digest: "1"}}}
	if two.Digest() != reversed.Digest() {
		t.Error("the order the layers were listed in changed the image")
	}
}

// SP-T04-2, in the one function that decides it. The environment is built, never inherited: a key
// that is on the node must not be able to arrive in the pod by nobody remembering to remove it.
func TestThePodEnvironmentIsBuiltNotInherited(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-should-never-travel")
	t.Setenv("GIT_TOKEN", "ghp-should-never-travel")
	t.Setenv("GITHUB_TOKEN", "ghp-should-never-travel")

	a, err := allocation.For(allocation.Tiny)
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Join(podEnvironment(Manifest{Env: []string{"GOTOOLCHAIN=local"}}, a), "\n")
	for _, forbidden := range []string{"sk-should-never-travel", "ghp-should-never-travel", "ANTHROPIC_API_KEY", "GIT_TOKEN", "GITHUB_TOKEN"} {
		if strings.Contains(env, forbidden) {
			t.Errorf("%q reached the pod's environment:\n%s", forbidden, env)
		}
	}
	if !strings.Contains(env, "GOTOOLCHAIN=local") {
		t.Error("the image's own environment did not reach the pod")
	}
	if !strings.Contains(env, "MAKEFLAGS=-j1") {
		t.Error("SP-RC-5's concurrency did not reach the pod")
	}
}

// An image that sets a concurrency variable must not beat the class: `os.cpus()` reporting the
// host's cores is the mistake SP-RC-5 exists for, and an image baked on a build server makes it.
func TestTheClassBeatsTheImage(t *testing.T) {
	a, err := allocation.For(allocation.Tiny)
	if err != nil {
		t.Fatal(err)
	}
	env := podEnvironment(Manifest{Env: []string{"MAKEFLAGS=-j64"}}, a)
	last := ""
	for _, e := range env {
		if strings.HasPrefix(e, "MAKEFLAGS=") {
			last = e
		}
	}
	if last != "MAKEFLAGS=-j1" {
		t.Errorf("the pod's MAKEFLAGS is %q — the image beat the allocation", last)
	}
}

// The mount table is SP-T04-1 and SP-T04-2 as a list. What must be there, what must be read-only,
// and what must not be there at all.
func TestTheMountTable(t *testing.T) {
	m := Manifest{Layers: []Layer{{Source: "/usr", Destination: "/usr", Digest: "path:/usr"}}}
	mounts := podMounts(m, "/data/work/pods/p-1", "/run/workpod/pods/p-1/pod")

	byDest := map[string]ociMount{}
	for _, mo := range mounts {
		byDest[mo.Destination] = mo
	}

	readonly := func(d string) bool {
		for _, o := range byDest[d].Options {
			if o == "ro" {
				return true
			}
		}
		return false
	}

	for _, d := range []string{"/usr", runner.PodHarness, runner.PodJobFile} {
		if _, ok := byDest[d]; !ok {
			t.Errorf("%s is not mounted", d)
			continue
		}
		if !readonly(d) {
			t.Errorf("%s is writable in the pod", d)
		}
	}
	// SP-E02-4: the harness is bound from the host's own binary, not from an image layer.
	if src := byDest[runner.PodHarness].Source; src != harnessBinary() {
		t.Errorf("the harness is mounted from %q, not from the binary that is running", src)
	}
	for _, d := range []string{runner.PodWorkDir, runner.PodSocket, runner.PodOutDir} {
		if _, ok := byDest[d]; !ok {
			t.Errorf("%s is not mounted", d)
		}
		if readonly(d) {
			t.Errorf("%s is read-only; the pod has to be able to work, speak and answer", d)
		}
	}
	if byDest[runner.PodWorkDir].Source != "/data/work/pods/p-1" {
		t.Errorf("the working copy is not the snapshot: %s", byDest[runner.PodWorkDir].Source)
	}
	// Nothing that would give the pod a way out besides the socket.
	for _, forbidden := range []string{"/etc/resolv.conf", "/var/run/docker.sock", "/root/.gitconfig"} {
		if _, ok := byDest[forbidden]; ok {
			t.Errorf("%s is mounted into the pod", forbidden)
		}
	}
}

// The bundle, end to end, without runc: the fields SP-T04-2 and SP-T04-3 rest on.
func TestTheBundleIsWhatT04Describes(t *testing.T) {
	s := scratch(t)
	skeleton := filepath.Join(t.TempDir(), "skeleton")
	for _, d := range []string{"usr", "proc", "sys", "dev", "tmp", "run", "work", "harness"} {
		if err := os.MkdirAll(filepath.Join(skeleton, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	job := aJob()
	podID := PodID(job)
	if err := os.MkdirAll(filepath.Join(s.RunDir(podID), "bundle"), 0o755); err != nil {
		t.Fatal(err)
	}
	a, err := allocation.For(allocation.Small)
	if err != nil {
		t.Fatal(err)
	}
	m := Manifest{Skeleton: skeleton, Layers: []Layer{{Source: "/usr", Destination: "/usr", Digest: "path:/usr"}}}

	bundle, err := s.writeBundle(podID, m, job, a)
	if err != nil {
		t.Fatalf("writing the bundle: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(bundle, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var spec ociSpec
	if err := json.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}

	// SP-T04-2: a network namespace with nothing in it.
	network := false
	for _, ns := range spec.Linux.Namespaces {
		if ns.Type == "network" {
			network = true
		}
	}
	if !network {
		t.Error("the pod shares the host's network — SP-T04-2 says no network")
	}
	if !spec.Root.Readonly {
		t.Error("the pod's root is writable")
	}
	if spec.Process.Args[0] != runner.PodHarness {
		t.Errorf("the pod's init process is %v, not the harness", spec.Process.Args)
	}
	if spec.Process.Cwd != runner.PodWorkDir {
		t.Errorf("the pod starts in %s, not in the working copy", spec.Process.Cwd)
	}
	if !spec.Process.NoNewPrivileges {
		t.Error("a pod may gain privileges")
	}
	if n := len(spec.Process.Capabilities.Bounding) + len(spec.Process.Capabilities.Effective) +
		len(spec.Process.Capabilities.Permitted) + len(spec.Process.Capabilities.Ambient); n != 0 {
		t.Errorf("the pod carries %d capabilities; a build is not an administrator", n)
	}
	// The pod's cgroup goes into the work layer of V-01, never into the reservation of SP-RC-4.
	// Which of the two forms it takes depends on whether systemd manages this machine's cgroup
	// tree; on a node it is always the first (SP-A02-4), and the check holds for both because the
	// container this test runs in is the machine where it is not.
	if !strings.Contains(spec.Linux.CgroupsPath, "workpod-work") {
		t.Errorf("the pod's cgroup is %q, not in the work layer", spec.Linux.CgroupsPath)
	}
	if strings.Contains(spec.Linux.CgroupsPath, "system.slice") {
		t.Error("a pod in system.slice would eat the control plane's reservation (SP-RC-4)")
	}
	// The image skeleton was laid down as the pod's root.
	if _, err := os.Stat(filepath.Join(bundle, "rootfs", "usr")); err != nil {
		t.Errorf("the image skeleton is not in the rootfs: %v", err)
	}
}

func TestTreeDigest(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "usr"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("usr/bin", filepath.Join(dir, "bin")); err != nil {
		t.Fatal(err)
	}
	first, err := treeDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	again, err := treeDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Errorf("the same tree hashed twice gave %s and %s", first, again)
	}
	if err := os.WriteFile(filepath.Join(dir, "usr", "marker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := treeDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Error("a file added to the tree did not change its digest")
	}
}

// A job for a pool that has no runner is refused by name, before anything is created.
func TestAnUnbuiltPoolIsRefused(t *testing.T) {
	s := scratch(t)
	w := New(s)
	job := aJob()
	job.Platform = runner.MacOS
	if _, err := w.Run(nil, "", job); err == nil || !strings.Contains(err.Error(), "AP-8.3") {
		t.Fatalf("a macos job answered with %v", err)
	}
	if _, err := os.Stat(s.PodDir(PodID(job))); err == nil {
		t.Error("a refused job left a working copy behind")
	}
}

// The failure that turned every row of AP-3.3 red on the first run against a real node: io.latency
// took the partition's device number, and the io controller only attaches to whole devices. A
// container's loop device hides it — that is a whole device — so the test builds the sysfs of a
// machine that has a disk layout.
func TestIOLatencyNamesTheWholeDisk(t *testing.T) {
	root := t.TempDir()
	devices := filepath.Join(root, "devices")
	class := filepath.Join(root, "class", "block")
	if err := os.MkdirAll(filepath.Join(devices, "vdb", "vdb1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(devices, "loop0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(class, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(devices, "vdb", "dev"), "253:32\n")
	write(filepath.Join(devices, "vdb", "vdb1", "dev"), "253:33\n")
	write(filepath.Join(devices, "vdb", "vdb1", "partition"), "1\n")
	write(filepath.Join(devices, "loop0", "dev"), "7:0\n")
	for name, target := range map[string]string{
		"vdb":   filepath.Join(devices, "vdb"),
		"vdb1":  filepath.Join(devices, "vdb", "vdb1"),
		"loop0": filepath.Join(devices, "loop0"),
	} {
		if err := os.Symlink(target, filepath.Join(class, name)); err != nil {
			t.Fatal(err)
		}
	}

	old := sysBlock
	sysBlock = class
	defer func() { sysBlock = old }()

	for name, want := range map[string][2]uint32{
		"vdb1":  {253, 32}, // the partition resolves to its disk
		"vdb":   {253, 32}, // the disk is itself
		"loop0": {7, 0},    // no partition, no climb
	} {
		maj, min, err := wholeDiskNumbers(name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if maj != want[0] || min != want[1] {
			t.Errorf("%s: %d:%d, want %d:%d", name, maj, min, want[0], want[1])
		}
	}
	if _, _, err := wholeDiskNumbers("nothing"); err == nil {
		t.Error("a device that is not there answered with a number")
	}
}
