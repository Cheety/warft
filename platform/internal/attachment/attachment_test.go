package attachment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The embedded ruling must parse, and parse into the numbers OP-5 states. The comparison against
// decisions/OP-5.md itself is acceptance/t01-intake.sh's — this is only the guard that the file
// the binary carries is readable at all.
func TestRuledParses(t *testing.T) {
	p := Ruled()
	if p.MaxBytes != 4194304 || p.MaxTotalBytes != 16777216 || p.MaxCount != 8 {
		t.Fatalf("OP-5's limits are not what the embedded file says: %+v", p)
	}
	if len(p.Types) != 6 {
		t.Fatalf("OP-5 permits six media types, the file has %d", len(p.Types))
	}
}

func TestParsePolicyRejectsAnIncompleteRuling(t *testing.T) {
	for name, src := range map[string]string{
		"a missing limit":  "limit\tattachment_max_bytes\t10\nmedia_type\ttext/plain\ttext\n",
		"no media type":    "limit\tattachment_max_bytes\t10\nlimit\tenvelope_max_total_bytes\t20\nlimit\tenvelope_max_attachments\t2\n",
		"a limit of zero":  "limit\tattachment_max_bytes\t0\n",
		"an unknown limit": "limit\tsomething_else\t10\n",
		"a stray line":     "nonsense\n",
	} {
		if _, err := parsePolicy(src); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestCheckMetadata(t *testing.T) {
	p := Ruled()
	if err := p.CheckMetadata("text/plain", 10); err != nil {
		t.Fatalf("a small text file was refused: %v", err)
	}
	if err := p.CheckMetadata("application/zip", 10); err == nil {
		t.Error("a container format passed the allowlist")
	}
	if err := p.CheckMetadata("text/plain", p.MaxBytes+1); err == nil {
		t.Error("an attachment over attachment_max_bytes passed")
	}
	if err := p.CheckMetadata("text/plain", 0); err == nil {
		t.Error("an empty attachment passed")
	}
}

func TestCheckEnvelope(t *testing.T) {
	p := Ruled()
	nine := make([]int64, 9)
	for i := range nine {
		nine[i] = 1
	}
	if err := p.CheckEnvelope(nine); err == nil {
		t.Error("a ninth attachment passed envelope_max_attachments")
	}
	// Eight attachments, each lawful on its own, whose sum is not.
	big := make([]int64, 8)
	for i := range big {
		big[i] = p.MaxBytes
	}
	if err := p.CheckEnvelope(big); err == nil {
		t.Error("eight lawful attachments passed as a lawful envelope")
	}
}

func TestCheckContentRefusesExecutables(t *testing.T) {
	p := Ruled()
	for name, head := range map[string]string{
		"ELF":         "\x7fELF\x02\x01\x01",
		"shebang":     "#!/bin/sh\necho hello\n",
		"PE":          "MZ\x90\x00",
		"Mach-O":      "\xcf\xfa\xed\xfe",
		"WebAssembly": "\x00asm\x01\x00\x00\x00",
	} {
		// Claimed as the most innocuous type on the list: the refusal must not depend on the claim.
		err := p.CheckContent("text/plain", []byte(head))
		if err == nil {
			t.Errorf("%s passed as text/plain", name)
			continue
		}
		if !strings.Contains(err.Error(), "never executable") {
			t.Errorf("%s was refused, but not for being executable: %v", name, err)
		}
	}
}

func TestCheckContentRefusesAContradictedClaim(t *testing.T) {
	p := Ruled()
	png := "\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 32)
	if err := p.CheckContent("text/plain", []byte(png)); err == nil {
		t.Error("a PNG passed as text/plain")
	}
	if err := p.CheckContent("image/png", []byte("just some words")); err == nil {
		t.Error("text passed as image/png")
	}
	if err := p.CheckContent("image/png", []byte(png)); err != nil {
		t.Errorf("a PNG was refused as image/png: %v", err)
	}
	// Four of the six types are text; a JSON body is text/plain to a sniffer and must pass as any
	// of them, because the witness is the class and not the exact type.
	for _, claimed := range []string{"text/plain", "text/markdown", "text/csv", "application/json"} {
		if err := p.CheckContent(claimed, []byte(`{"a":1}`)); err != nil {
			t.Errorf("a JSON body was refused as %s: %v", claimed, err)
		}
	}
}

func TestAcceptFilesReadOnlyAndContentAddressed(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "log.txt")
	if err := os.WriteFile(src, []byte("a build log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(filepath.Join(dir, "store"), Ruled())

	a, err := s.Accept(src, "text/plain")
	if err != nil {
		t.Fatalf("a lawful attachment was refused: %v", err)
	}
	if !strings.HasPrefix(a.ContentHash, "sha256:") || len(a.ContentHash) != len("sha256:")+64 {
		t.Fatalf("content hash %q is not a sha256 reference", a.ContentHash)
	}
	info, err := os.Stat(s.Path(a.ContentHash))
	if err != nil {
		t.Fatalf("the store did not file it: %v", err)
	}
	if info.Mode().Perm() != 0o444 {
		t.Errorf("stored mode is %v, not 0444 — an attachment is never executable and never written twice", info.Mode().Perm())
	}

	// The same bytes are the same object: a second intake is a hit, not a conflict.
	again, err := s.Accept(src, "text/plain")
	if err != nil {
		t.Fatalf("the same attachment a second time was refused: %v", err)
	}
	if again.ContentHash != a.ContentHash {
		t.Errorf("the same bytes were filed under two names: %s and %s", a.ContentHash, again.ContentHash)
	}
}

func TestAcceptAllLeavesNothingBehindWhenItRefuses(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "store")
	s := NewStore(store, Ruled())

	cands := make([]Candidate, 0, 9)
	for i := 0; i < 9; i++ {
		p := filepath.Join(dir, string(rune('a'+i))+".txt")
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		cands = append(cands, Candidate{Path: p, MediaType: "text/plain"})
	}
	if _, err := s.AcceptAll(cands); err == nil {
		t.Fatal("nine attachments were accepted")
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Error("a refused envelope left attachments in the store")
	}
}
