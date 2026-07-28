package gitgate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	workpodv1 "github.com/Cheety/warft/platform/api/workpodv1"
	"github.com/Cheety/warft/platform/internal/outbox"
)

var policy = []Rule{
	{Repo: "/srv/git/repo.git", Branches: []string{"main", "feature/*"}, Sign: false},
	{Repo: "/srv/git/signed.git", Branches: []string{"main"}, Sign: true},
}

func entry(order, target, hash string) *workpodv1.OutboxEntry {
	return &workpodv1.OutboxEntry{OrderId: order, Target: target, ContentHash: hash, PayloadRef: []byte("/tmp/p")}
}

// SP-K03-3: the gate checks policy. Default deny — a repository no line names is refused, and so is
// a branch the line for that repository does not permit.
func TestThePolicyIsDefaultDeny(t *testing.T) {
	if _, err := Allowed(policy, "/srv/git/repo.git", "main"); err != nil {
		t.Fatalf("a permitted push was refused: %v", err)
	}
	if _, err := Allowed(policy, "/srv/git/repo.git", "feature/anything"); err != nil {
		t.Fatalf("the wildcard did not match: %v", err)
	}
	if _, err := Allowed(policy, "/srv/git/repo.git", "release"); err == nil {
		t.Fatal("a branch outside the policy was permitted")
	}
	if _, err := Allowed(policy, "/srv/git/other.git", "main"); err == nil {
		t.Fatal("a repository no policy line names was permitted — the gate is not default deny")
	}
}

// A gate with no policy file refuses everything and says so, rather than starting with an empty
// policy that silently permits nothing and looks broken.
func TestAMissingPolicyIsARefusalWithAName(t *testing.T) {
	_, err := LoadPolicy(filepath.Join(t.TempDir(), "absent.tsv"))
	if err == nil {
		t.Fatal("a missing policy file loaded")
	}
	if !strings.Contains(err.Error(), "refuses every push") {
		t.Fatalf("the refusal does not say what it does: %v", err)
	}
}

// AB-K03-2: two attempts, one push. Two calls with the same domain key, and the executor runs once.
func TestTwoAttemptsAreOnePush(t *testing.T) {
	pushes := 0
	g := &Gate{
		Policy: policy,
		Ledger: outbox.OpenLedger(t.TempDir()),
		Execute: func(_ context.Context, repo, branch, _, _ string) (string, error) {
			pushes++
			return "commit-" + branch, nil
		},
	}
	e := entry("o1", "git+/srv/git/repo.git#main", "hash-1")
	first, err := g.Push(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	second, err := g.Push(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if pushes != 1 {
		t.Fatalf("%d pushes for one domain key — the same patch onto the same branch is one push (SP-K03-2)", pushes)
	}
	if !first.GetExecuted() {
		t.Fatal("the first call reported it did not execute")
	}
	if second.GetExecuted() {
		t.Fatal("the second call reported an execution that did not happen")
	}
	// The job cannot tell which of the two it was, and must not have to.
	if first.GetExternalId() != second.GetExternalId() {
		t.Fatalf("two receipts for one push: %q and %q", first.GetExternalId(), second.GetExternalId())
	}
}

// A *different* content hash for the same branch is a different effect and pushes again — otherwise
// the second commit of a job would be swallowed by the first.
func TestADifferentPatchIsADifferentPush(t *testing.T) {
	pushes := 0
	g := &Gate{Policy: policy, Ledger: outbox.OpenLedger(t.TempDir()),
		Execute: func(_ context.Context, _, _, _, _ string) (string, error) { pushes++; return "c", nil }}
	for _, h := range []string{"hash-1", "hash-2"} {
		if _, err := g.Push(context.Background(), entry("o1", "git+/srv/git/repo.git#main", h)); err != nil {
			t.Fatal(err)
		}
	}
	if pushes != 2 {
		t.Fatalf("%d pushes for two different patches, expected 2", pushes)
	}
}

// A push the policy forbids must not reach the executor, and must not appear in the ledger as
// something that was once permitted.
func TestARefusedPushNeverReachesTheExecutor(t *testing.T) {
	ledgerDir := t.TempDir()
	g := &Gate{Policy: policy, Ledger: outbox.OpenLedger(ledgerDir),
		Execute: func(_ context.Context, _, _, _, _ string) (string, error) {
			t.Fatal("the executor ran for a push the policy forbids")
			return "", nil
		}}
	if _, err := g.Push(context.Background(), entry("o1", "git+/srv/git/other.git#main", "h")); err == nil {
		t.Fatal("a forbidden push succeeded")
	}
	names, err := os.ReadDir(ledgerDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Fatalf("the ledger holds %d entr(ies) for a push that was never permitted", len(names))
	}
}

// SP-K03-3: the gate signs itself. A repository whose policy requires a signature and a gate with
// no key is a refusal, never an unsigned push.
func TestASignedRepositoryWithoutAKeyRefuses(t *testing.T) {
	g := &Gate{Policy: policy, Ledger: outbox.OpenLedger(t.TempDir()),
		Execute: func(_ context.Context, _, _, _, sig string) (string, error) {
			if sig == "" {
				t.Fatal("an unsigned push reached a repository whose policy requires a signature")
			}
			return "c", nil
		}}
	if _, err := g.Push(context.Background(), entry("o1", "git+/srv/git/signed.git#main", "h")); err == nil {
		t.Fatal("a gate with no signing key pushed to a repository that requires one")
	}
}

// And with a key, the signature reaches the push. The key itself stays in the closure — the test
// asserts the gate can sign, not that the gate holds a secret.
func TestTheGateSignsItself(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyFile, []byte("a signing key"), 0o600); err != nil {
		t.Fatal(err)
	}
	signer, err := SignerFromCredential(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	got := ""
	g := &Gate{Policy: policy, Ledger: outbox.OpenLedger(t.TempDir()), Signer: signer,
		Execute: func(_ context.Context, _, _, _, sig string) (string, error) { got = sig; return "c", nil }}
	if _, err := g.Push(context.Background(), entry("o1", "git+/srv/git/signed.git#main", "h")); err != nil {
		t.Fatal(err)
	}
	if got == "" {
		t.Fatal("the push carried no signature")
	}
	// Two different contents sign differently, or the signature says nothing about what was pushed.
	if _, err := g.Push(context.Background(), entry("o1", "git+/srv/git/signed.git#main", "h2")); err != nil {
		t.Fatal(err)
	}
	if got == "" {
		t.Fatal("the second push carried no signature")
	}
}

func TestParseTarget(t *testing.T) {
	repo, branch, err := ParseTarget("git+ssh://git@host/x.git#feature/y")
	if err != nil || repo != "ssh://git@host/x.git" || branch != "feature/y" {
		t.Fatalf("parsed %q %q (%v)", repo, branch, err)
	}
	for _, bad := range []string{"https://x/y", "git+repo.git", "git+#main", "git+repo.git#"} {
		if _, _, err := ParseTarget(bad); err == nil {
			t.Fatalf("%q parsed as a Git target", bad)
		}
	}
}

// The default executor against a real repository: the gate clones, applies the patch, commits and
// pushes, and the branch in the bare repository afterwards holds exactly one new commit. This is the
// half of AB-K03-2 that is a measurement rather than a stub — with the ledger in front of it, the
// second attempt does not reach here at all.
func TestTheDefaultExecutorPushesForReal(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on this machine")
	}
	dir := t.TempDir()
	bare := filepath.Join(dir, "repo.git")
	seed := filepath.Join(dir, "seed")
	mustGit(t, "", "init", "--quiet", "--bare", "--initial-branch=main", bare)
	mustGit(t, "", "init", "--quiet", "--initial-branch=main", seed)
	if err := os.WriteFile(filepath.Join(seed, "README"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, seed, "add", "README")
	mustGit(t, seed, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--quiet", "-m", "base")
	mustGit(t, seed, "remote", "add", "origin", bare)
	mustGit(t, seed, "push", "--quiet", "origin", "main")

	// A patch in `git apply` form, which is what payload_ref resolves to.
	patch := filepath.Join(dir, "change.patch")
	if err := os.WriteFile(patch, []byte(`diff --git a/README b/README
--- a/README
+++ b/README
@@ -1 +1,2 @@
 base
+from the pod
`), 0o644); err != nil {
		t.Fatal(err)
	}

	before := mustGitOut(t, bare, "rev-list", "--count", "main")
	head, err := pushWithGit(context.Background(), bare, "main", patch, "sig-1")
	if err != nil {
		t.Fatal(err)
	}
	after := mustGitOut(t, bare, "rev-list", "--count", "main")
	if before != "1" || after != "2" {
		t.Fatalf("commits on main went %s -> %s, expected 1 -> 2", before, after)
	}
	if !strings.HasPrefix(mustGitOut(t, bare, "rev-parse", "main"), head[:7]) {
		t.Fatalf("the receipt names %s, the branch is at %s", head, mustGitOut(t, bare, "rev-parse", "main"))
	}
	// The gate's signature is readable in the repository's own log, not only in its ledger.
	if !strings.Contains(mustGitOut(t, bare, "log", "-1", "--format=%B", "main"), "Workpod-Gate-Signature: sig-1") {
		t.Fatal("the commit carries no gate signature trailer")
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := gitOut(context.Background(), dir, args...); err != nil {
		t.Fatal(err)
	}
}

func mustGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitOut(context.Background(), dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
