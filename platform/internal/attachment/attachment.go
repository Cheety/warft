// Package attachment is SP-K01-6 in one place: "attachments are data like text — type and size
// check at intake, stored content-addressed, mounted read-only into exactly the pod that needs
// them, never executable".
//
// The numbers and the media types are OP-5's, read from op5-policy.tsv rather than written here.
// That file is the machine-readable half of decisions/OP-5.md and acceptance/t01-intake.sh holds
// the two against each other, so a limit cannot move in the code without moving in the ruling.
//
// Both roles that touch an attachment use this package: the adapter, which has the bytes and does
// the check at intake, and the control plane, which has only the metadata and re-checks it before
// it becomes state. That is why this is a module of its own rather than part of either — a role
// reaches another role over the wire or not at all (decisions/module-dependencies.md), so what
// both must agree on cannot live in one of them.
package attachment

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

//go:embed op5-policy.tsv
var policySource string

// sniffLength is what http.DetectContentType reads. The witness in the policy is decided from
// these bytes and no more, so an attachment is judged by its head, not by its size.
const sniffLength = 512

// Policy is OP-5, parsed.
type Policy struct {
	MaxBytes      int64
	MaxTotalBytes int64
	MaxCount      int

	// Types maps a permitted media type to the witness its leading bytes must produce:
	// "text" for anything http.DetectContentType calls text/plain, an exact type otherwise.
	Types map[string]string
}

// Ruled is OP-5 as the binary carries it.
func Ruled() Policy {
	p, err := parsePolicy(policySource)
	if err != nil {
		// The file is embedded at build time; a malformed one is a broken build, not a runtime
		// condition a caller could handle.
		panic("op5-policy.tsv: " + err.Error())
	}
	return p
}

func parsePolicy(src string) (Policy, error) {
	p := Policy{Types: map[string]string{}}
	seen := map[string]bool{}

	sc := bufio.NewScanner(strings.NewReader(src))
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		switch {
		case f[0] == "limit" && len(f) == 3:
			n, err := strconv.ParseInt(f[2], 10, 64)
			if err != nil || n <= 0 {
				return p, fmt.Errorf("limit %s: %q is not a positive number", f[1], f[2])
			}
			switch f[1] {
			case "attachment_max_bytes":
				p.MaxBytes = n
			case "envelope_max_total_bytes":
				p.MaxTotalBytes = n
			case "envelope_max_attachments":
				p.MaxCount = int(n)
			default:
				return p, fmt.Errorf("limit %q is not one OP-5 rules", f[1])
			}
			seen[f[1]] = true
		case f[0] == "media_type" && len(f) == 3:
			p.Types[f[1]] = f[2]
		default:
			return p, fmt.Errorf("line %q is neither a limit nor a media type", line)
		}
	}
	if err := sc.Err(); err != nil {
		return p, err
	}
	for _, want := range []string{"attachment_max_bytes", "envelope_max_total_bytes", "envelope_max_attachments"} {
		if !seen[want] {
			return p, fmt.Errorf("the ruling names %s and the file does not", want)
		}
	}
	if len(p.Types) == 0 {
		return p, fmt.Errorf("no permitted media type: an empty allowlist is not a conservative one, it is a broken file")
	}
	return p, nil
}

// executableMagic is the "never executable" half of SP-K01-6, and it is deliberately not the type
// list: an ELF named text/plain is refused for being an ELF, whatever it claims. The leading bytes
// of the five executable formats a Linux platform can plausibly be handed.
var executableMagic = []struct {
	prefix []byte
	name   string
}{
	{[]byte("\x7fELF"), "an ELF binary"},
	{[]byte("#!"), "a #! interpreter line"},
	{[]byte("MZ"), "a PE/COFF binary"},
	{[]byte("\xfe\xed\xfa\xce"), "a Mach-O binary"},
	{[]byte("\xfe\xed\xfa\xcf"), "a Mach-O binary"},
	{[]byte("\xce\xfa\xed\xfe"), "a Mach-O binary"},
	{[]byte("\xcf\xfa\xed\xfe"), "a Mach-O binary"},
	{[]byte("\xca\xfe\xba\xbe"), "a fat binary or a Java class"},
	{[]byte("\x00asm"), "a WebAssembly module"},
}

// Candidate is a file offered to intake, with the media type its sender claims for it. The claim
// is what CheckContent then holds against the bytes.
type Candidate struct {
	Path      string
	MediaType string
}

// Accepted is one attachment that passed intake: the reference the envelope carries, never the
// payload (SP-K01-6, and the proto's rule 3 — no blobs on the wire).
type Accepted struct {
	ContentHash string // "sha256:<hex>", the name the store filed it under
	MediaType   string
	SizeBytes   int64
}

// CheckMetadata is the half of the ruling that needs no bytes: a permitted media type and a size
// within the per-attachment limit. The control plane has exactly this much when an envelope
// arrives, and re-checks it before the attachment becomes state.
func (p Policy) CheckMetadata(mediaType string, size int64) error {
	if _, ok := p.Types[mediaType]; !ok {
		return fmt.Errorf("media type %q is not on OP-5's list (%s)", mediaType, p.permitted())
	}
	if size <= 0 {
		return fmt.Errorf("an attachment of %d bytes is not an attachment", size)
	}
	if size > p.MaxBytes {
		return fmt.Errorf("%d bytes exceeds OP-5's attachment_max_bytes of %d", size, p.MaxBytes)
	}
	return nil
}

// CheckEnvelope is the half that is about the set rather than the file: OP-5 bounds the count and
// the total as well, because a hundred lawful attachments are an unlawful envelope.
func (p Policy) CheckEnvelope(sizes []int64) error {
	if len(sizes) > p.MaxCount {
		return fmt.Errorf("%d attachments exceed OP-5's envelope_max_attachments of %d", len(sizes), p.MaxCount)
	}
	var total int64
	for _, s := range sizes {
		total += s
	}
	if total > p.MaxTotalBytes {
		return fmt.Errorf("%d bytes over %d attachments exceed OP-5's envelope_max_total_bytes of %d",
			total, len(sizes), p.MaxTotalBytes)
	}
	return nil
}

// CheckContent decides the type from the bytes. The claimed type has already passed
// CheckMetadata; here the leading bytes must not be an executable and must produce the witness
// OP-5 records for that type. A claim the content contradicts is a refusal — otherwise the
// allowlist would be a courtesy.
func (p Policy) CheckContent(mediaType string, head []byte) error {
	for _, m := range executableMagic {
		if bytes.HasPrefix(head, m.prefix) {
			return fmt.Errorf("the content begins with %s — an attachment is never executable (SP-K01-6)", m.name)
		}
	}
	witness, ok := p.Types[mediaType]
	if !ok {
		return fmt.Errorf("media type %q is not on OP-5's list (%s)", mediaType, p.permitted())
	}
	detected := http.DetectContentType(head)
	if witness == "text" {
		if !strings.HasPrefix(detected, "text/plain") {
			return fmt.Errorf("declared %s, but the content sniffs as %s — OP-5 decides the type from the bytes", mediaType, detected)
		}
		return nil
	}
	if detected != witness {
		return fmt.Errorf("declared %s, but the content sniffs as %s — OP-5 decides the type from the bytes", mediaType, detected)
	}
	return nil
}

func (p Policy) permitted() string {
	names := make([]string, 0, len(p.Types))
	for t := range p.Types {
		names = append(names, t)
	}
	// A stable order so a refusal reads the same twice; the list is six entries, not a hot path.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return strings.Join(names, ", ")
}

// Store is the content-addressed area attachments are filed in. One directory, one file per
// content hash, mode 0444 — the pod mounts it read-only (AP-3.3) and the mode says the same thing
// on the node, so neither guard rests on the other.
type Store struct {
	root   string
	policy Policy
}

// DefaultRoot lies on /data/work: an attachment is job input, not control state, so it belongs on
// the volume a reinstall may take (SP-A05-1) rather than on the one that survives.
const DefaultRoot = "/data/work/attachments"

func NewStore(root string, p Policy) *Store {
	if root == "" {
		root = DefaultRoot
	}
	return &Store{root: root, policy: p}
}

// Accept is the check at intake, in the order SP-K01-6 names it: size, then type, then filed
// under its content hash. Nothing is written before every check has passed, so a refused
// attachment leaves no object behind.
func (s *Store) Accept(path, mediaType string) (Accepted, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Accepted{}, err
	}
	if info.IsDir() {
		return Accepted{}, fmt.Errorf("%s is a directory; an attachment is a file", path)
	}
	size := info.Size()
	if err := s.policy.CheckMetadata(mediaType, size); err != nil {
		return Accepted{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}

	f, err := os.Open(path)
	if err != nil {
		return Accepted{}, err
	}
	defer f.Close()

	head := make([]byte, sniffLength)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return Accepted{}, err
	}
	head = head[:n]
	if err := s.policy.CheckContent(mediaType, head); err != nil {
		return Accepted{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Accepted{}, err
	}
	sum := sha256.New()
	written, err := io.Copy(sum, f)
	if err != nil {
		return Accepted{}, err
	}
	// The file was measured before it was read. If it grew between the two, the size that was
	// checked is not the size that would be stored, and the check would be about nothing.
	if written != size {
		return Accepted{}, fmt.Errorf("%s changed while it was being read (%d bytes checked, %d read)",
			filepath.Base(path), size, written)
	}
	hash := "sha256:" + hex.EncodeToString(sum.Sum(nil))

	if err := s.file(hash, path); err != nil {
		return Accepted{}, err
	}
	return Accepted{ContentHash: hash, MediaType: mediaType, SizeBytes: size}, nil
}

// AcceptAll is intake for one envelope's worth of attachments. The envelope-level limits are
// checked before a single byte is stored: an envelope that breaks the count or the total is
// refused whole, so a refusal never leaves half its attachments behind.
func (s *Store) AcceptAll(cands []Candidate) ([]Accepted, error) {
	if len(cands) == 0 {
		return nil, nil
	}
	if len(cands) > s.policy.MaxCount {
		return nil, fmt.Errorf("%d attachments exceed OP-5's envelope_max_attachments of %d",
			len(cands), s.policy.MaxCount)
	}
	sizes := make([]int64, 0, len(cands))
	for _, c := range cands {
		info, err := os.Stat(c.Path)
		if err != nil {
			return nil, err
		}
		sizes = append(sizes, info.Size())
	}
	if err := s.policy.CheckEnvelope(sizes); err != nil {
		return nil, err
	}
	out := make([]Accepted, 0, len(cands))
	for _, c := range cands {
		a, err := s.Accept(c.Path, c.MediaType)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// Policy is the ruling this store enforces, so a caller that has the store need not carry the
// policy beside it.
func (s *Store) Policy() Policy { return s.policy }

// Path is where a content hash lies in the store.
func (s *Store) Path(hash string) string {
	name := strings.TrimPrefix(hash, "sha256:")
	// Two levels of fan-out: one directory with a million entries is a directory nothing wants
	// to list, and the reaper of AP-3.3 will want to.
	return filepath.Join(s.root, "sha256", name[:2], name)
}

// file copies the content under its hash. Content-addressed means the same bytes are the same
// object: a second envelope carrying the same attachment finds it already there, and that is a
// hit rather than a conflict.
func (s *Store) file(hash, from string) error {
	dst := s.Path(hash)
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	// Written under a temporary name and renamed into place: a reader never sees a half-written
	// attachment under a hash that promises the whole of it.
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".incoming-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	src, err := os.Open(from)
	if err != nil {
		tmp.Close()
		return err
	}
	_, err = io.Copy(tmp, src)
	src.Close()
	if err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// 0444 before the rename, so the file is never briefly writable under its final name.
	if err := os.Chmod(tmp.Name(), 0o444); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}
