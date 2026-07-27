package workpod

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Cheety/warft/platform/internal/runner"
)

// Manifest is a container image as this node holds it: a skeleton of mount points and a list of
// read-only layers to lay over it. It is what SP-T04-1 calls "a root filesystem from shared
// read-only layers" — shared because the layers are mounted, never copied, so a hundred pods on one
// image cost one copy of it.
//
// The harness is not in here, and that absence is SP-E02-4's mechanism: the digest below covers the
// image and nothing else, so replacing /usr/bin/workpod on the host changes what every pod runs
// without changing a single image (AB-E02-4).
type Manifest struct {
	// Skeleton is the directory laid down as the pod's root: the mount points and the symbolic
	// links between them, and nothing with content in it. Shared read-only between pods.
	Skeleton       string `json:"skeleton"`
	SkeletonDigest string `json:"skeleton_digest"`

	Layers []Layer  `json:"layers"`
	Env    []string `json:"env"`

	Requirements runner.Requirements `json:"requirements"`
	// RequirementHash is the index key this manifest was published under (SP-T03-1). Carried in
	// the manifest as well so an image found by digest can still say what it was built to satisfy.
	RequirementHash string `json:"requirement_hash"`
}

// Layer is one read-only tree, mounted into the pod where the image says.
type Layer struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Digest      string `json:"digest"`
}

// Digest is the image's identity: SP-T03-4's "content-addressed". It covers what a pod would see —
// the skeleton, the layers and the environment — and deliberately not the requirements, so that two
// requirement sets that resolve to the same tree are one image and not two.
func (m Manifest) Digest() string {
	var b strings.Builder
	fmt.Fprintf(&b, "skeleton=%s\n", m.SkeletonDigest)
	layers := append([]Layer(nil), m.Layers...)
	sort.Slice(layers, func(i, j int) bool { return layers[i].Destination < layers[j].Destination })
	for _, l := range layers {
		fmt.Fprintf(&b, "layer=%s %s\n", l.Destination, l.Digest)
	}
	env := append([]string(nil), m.Env...)
	sort.Strings(env)
	for _, e := range env {
		fmt.Fprintf(&b, "env=%s\n", e)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Miss is what a lookup that found nothing returns: the requirement hash that was not in the index,
// and the build job written for it. SP-T03-1's "a miss: a build job", as an error a caller can act
// on rather than as a nil.
type Miss struct {
	RequirementHash string
	BuildJobPath    string
}

func (m *Miss) Error() string {
	return fmt.Sprintf("no image for requirement hash %s — a build job stands at %s (SP-T03-1)", m.RequirementHash, m.BuildJobPath)
}

// Resolve is SP-T03-1: form the requirement hash, look it up, and on a miss produce a build job.
//
// A job that already names an image (order.image_hash) skips the index — the requirement hash exists
// to *find* an image, and a job that has one has nothing to find.
func (s Store) Resolve(job runner.Job) (Manifest, error) {
	if job.ImageDigest != "" {
		return s.Manifest(job.ImageDigest)
	}
	hash := job.Requirements.Hash()
	digest, err := os.ReadFile(s.indexEntry(hash))
	if err != nil {
		path, werr := s.writeBuildJob(job, hash)
		if werr != nil {
			return Manifest{}, fmt.Errorf("no image for %s, and the build job could not be written: %w", hash, werr)
		}
		return Manifest{}, &Miss{RequirementHash: hash, BuildJobPath: path}
	}
	return s.Manifest(strings.TrimSpace(string(digest)))
}

// Manifest reads one image by its digest and checks that the digest is the one the content produces.
// A manifest whose name is not its content is a broken index entry, not an image — content
// addressing that is not verified is a naming convention.
func (s Store) Manifest(digest string) (Manifest, error) {
	b, err := os.ReadFile(s.manifestPath(digest))
	if err != nil {
		return Manifest{}, fmt.Errorf("no image %s on this node: %w", digest, err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, fmt.Errorf("image %s: %w", digest, err)
	}
	if got := m.Digest(); got != digest {
		return Manifest{}, fmt.Errorf("image %s carries the content of %s — content-addressed means the name is the content (SP-T03-4)", digest, got)
	}
	return m, nil
}

// Import publishes an image: it seals the skeleton as a read-only subvolume, writes the manifest
// under its own digest and points the requirement hash at it.
//
// This is the index's write side and nothing more. Producing the tree — the image specification, the
// build, the smoke test of SP-T03-2 — is the `image.build` procedure of AP-5.2; until it exists an
// image gets onto a node by being imported here, which decisions/pod-runtime.md §1 is the ruling
// for.
func (s Store) Import(skeleton string, layers []Layer, env []string, req runner.Requirements) (Manifest, error) {
	digest, err := treeDigest(skeleton)
	if err != nil {
		return Manifest{}, fmt.Errorf("skeleton %s: %w", skeleton, err)
	}
	m := Manifest{
		Skeleton:        "",
		SkeletonDigest:  digest,
		Layers:          layers,
		Env:             env,
		Requirements:    req,
		RequirementHash: req.Hash(),
	}
	for i, l := range m.Layers {
		if l.Digest != "" {
			continue
		}
		// A layer digest is not computed here. Layers are whole filesystem trees — the node's own
		// /usr is a gigabyte under dm-verity — and hashing one on every import would make
		// publishing an image cost more than building it. The caller names the digest it knows
		// (a verity roothash, a build's own output); an unnamed layer takes its source path, which
		// is honest about being a reference rather than a content address.
		m.Layers[i].Digest = "path:" + l.Source
	}

	d := m.Digest()
	m.Skeleton = filepath.Join(s.Work, "images", strings.TrimPrefix(d, "sha256:"))
	if err := os.MkdirAll(filepath.Join(s.Work, "images"), 0o755); err != nil {
		return Manifest{}, err
	}
	if _, err := os.Stat(m.Skeleton); err != nil {
		if err := subvolumeCreate(m.Skeleton); err != nil {
			return Manifest{}, err
		}
		if err := copyTree(skeleton, m.Skeleton); err != nil {
			return Manifest{}, err
		}
		if err := subvolumeSetReadOnly(m.Skeleton, true); err != nil {
			return Manifest{}, err
		}
	}

	// The manifest is written read-only under its own digest, so importing the same image twice
	// finds the file already there rather than failing to overwrite it. Skeleton is a path and not
	// content, which is why it is not in the digest and may be filled in before writing.
	if _, err := os.Stat(s.manifestPath(d)); err != nil {
		body, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return Manifest{}, err
		}
		if err := os.WriteFile(s.manifestPath(d), append(body, '\n'), 0o444); err != nil {
			return Manifest{}, err
		}
	}
	if err := os.MkdirAll(filepath.Join(s.Work, "index"), 0o755); err != nil {
		return Manifest{}, err
	}
	if err := os.WriteFile(s.indexEntry(m.RequirementHash), []byte(d+"\n"), 0o644); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func (s Store) manifestPath(digest string) string {
	return filepath.Join(s.Work, "images", strings.TrimPrefix(digest, "sha256:")+".json")
}

func (s Store) indexEntry(requirementHash string) string {
	return filepath.Join(s.Work, "index", strings.TrimPrefix(requirementHash, "sha256:"))
}

// buildJob is the record a miss leaves behind: a HandWrittenJob in the sense of
// decisions/jobs-by-hand.md, keyed by the requirement hash so that two pods missing the same image
// produce one build job and not two.
type buildJob struct {
	Idempotency  string              `json:"idempotency"`
	Cell         string              `json:"cell"`
	Project      string              `json:"project"`
	Class        string              `json:"class"`
	Platform     string              `json:"platform"`
	Requirements runner.Requirements `json:"requirements"`
	RequestedBy  string              `json:"requested_by"`
	RequestedAt  time.Time           `json:"requested_at"`
	Goal         string              `json:"goal"`
	Acceptance   string              `json:"acceptance"`
}

func (s Store) writeBuildJob(job runner.Job, hash string) (string, error) {
	dir := filepath.Join(s.Var, "buildjobs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, strings.TrimPrefix(hash, "sha256:")+".json")
	if _, err := os.Stat(path); err == nil {
		// One build job per requirement hash. The key is the hash rather than the order, because
		// the image is what is missing and the image is the same for every order that misses it.
		return path, nil
	}
	b := buildJob{
		Idempotency:  "image-build:" + hash,
		Cell:         job.Cell,
		Project:      job.Project,
		Class:        string(largeClass),
		Platform:     string(runner.Alpine),
		Requirements: job.Requirements,
		RequestedBy:  job.OrderID,
		RequestedAt:  time.Now().UTC(),
		Goal:         "build the container image for requirement hash " + hash,
		// SP-T03-2: published in the index only after a smoke test. That is the acceptance
		// criterion of the build job, and Q-01 forbids a job without one.
		Acceptance: "the image builds, passes its smoke test, and stands in the index under " + hash,
	}
	body, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, append(body, '\n'), 0o644)
}

// largeClass is what a build job is sized at: SP-RA-1's own table gives `large` the row "monorepo,
// E2E, image build".
const largeClass = "large"

// treeDigest is the content address of a small directory tree: every path with its mode, size and —
// for a regular file — the hash of its bytes. Used for the skeleton, which is mount points and
// symbolic links; it is not used on a layer, which is a filesystem.
func treeDigest(root string) (string, error) {
	var b strings.Builder
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(&b, "l %s -> %s\n", rel, target)
		case d.IsDir():
			fmt.Fprintf(&b, "d %s %o\n", rel, info.Mode().Perm())
		case info.Mode().IsRegular():
			sum, err := fileDigest(path)
			if err != nil {
				return err
			}
			fmt.Fprintf(&b, "f %s %o %d %s\n", rel, info.Mode().Perm(), info.Size(), sum)
		default:
			return fmt.Errorf("%s is neither a file, a directory nor a link — a skeleton holds mount points, not devices", rel)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func fileDigest(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
