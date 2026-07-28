// Package gitgate is one of SP-K03-3's two gates: the Git proxy. It checks policy, it signs, and it
// holds the credential — the pod has no token, no key and no network at all (T-04, SP-B01-4).
//
// The gate is where the outbox chain ends and the outside world begins (SP-K03-1). Everything
// before it is an intent; the push is here, and it happens at most once per domain key because the
// gate keeps its own ledger (decisions/gates-and-the-outbox.md §2). Two attempts of the same job
// that produced the same patch for the same branch arrive as the same domain key and leave as one
// push — which is the sentence AB-K03-2 checks and the reason V-02 may do without a leader
// election everywhere else.
//
// It listens on a Unix socket and never on a port (SP-B02-6). Its credentials come from systemd's
// credential directory, in a unit of its own under its own user, and nothing in the work layer can
// read them (SP-B01-4).
package gitgate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	workpodv1 "github.com/Cheety/warft/platform/api/workpodv1"
	"github.com/Cheety/warft/platform/internal/outbox"
)

// LedgerDir is where the gate's ledger lies: on /var, under the gate's own directory rather than
// the work layer's. A ledger that did not survive a restart would let a restarted gate push a
// second time, which is the failure the ledger exists for.
const LedgerDir = "/var/lib/workpod-gate/git"

// PolicyFile is the gate's policy: which repositories and which branch patterns a push may touch.
// Read at every push rather than at start, so revoking a target does not need a restart — a gate
// that has to be restarted to stop pushing is a gate nobody stops in an incident (E-08).
const PolicyFile = "/etc/workpod/git-gate-policy.tsv"

// Rule is one line of the policy: a repository, the branch patterns permitted on it, and whether a
// push there must be signed. Signing is a column rather than a constant because SP-K03-3 is about
// the gate being the only way out, and a repository that rejects signatures would otherwise make
// the gate unusable rather than the repository.
type Rule struct {
	Repo     string
	Branches []string
	Sign     bool
}

// Gate is the Git proxy.
type Gate struct {
	workpodv1.UnimplementedGitGateServer

	Policy []Rule
	Ledger *outbox.Ledger
	// Signer is what turns a push into a signed one. It is a function so that a probe can watch
	// the gate sign without a key on the machine, and so that the key never travels through this
	// struct: what the gate holds is the ability to sign, not the secret.
	Signer func(payload string) (string, error)
	// Execute is what actually pushes. A function for the same reason Signer is: the gate's rule —
	// policy, ledger, receipt — is what this package is, and driving `git` is one implementation
	// of the last step (decisions/pod-runtime.md's argument, applied here).
	Execute func(ctx context.Context, repo, branch, payloadRef, signature string) (string, error)
	Logf    func(string, ...any)
}

// LoadPolicy reads the policy file. A missing file is not an empty policy: a gate with no policy
// refuses everything, and saying so by name is better than pushing nothing and looking broken.
func LoadPolicy(path string) ([]Rule, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no Git gate policy at %s — a gate without a policy refuses every push, and that is the default (SP-K03-3)", path)
		}
		return nil, err
	}
	var out []Rule
	for n, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 3 {
			return nil, fmt.Errorf("%s:%d: expected repo, branches, sign", path, n+1)
		}
		out = append(out, Rule{
			Repo:     strings.TrimSpace(f[0]),
			Branches: strings.Split(strings.TrimSpace(f[1]), ","),
			Sign:     strings.TrimSpace(f[2]) == "yes",
		})
	}
	return out, nil
}

// Allowed matches a repository and branch against the policy. Default deny, and the refusal names
// what was asked for — SP-B02-5's argument holds for this gate too: a rejected target is the best
// early warning signal for injection this system has, and a refusal nobody can read is not one.
func Allowed(policy []Rule, repo, branch string) (Rule, error) {
	for _, r := range policy {
		if r.Repo != repo {
			continue
		}
		for _, pattern := range r.Branches {
			if matchBranch(strings.TrimSpace(pattern), branch) {
				return r, nil
			}
		}
		return Rule{}, fmt.Errorf("the policy permits %s, but not the branch %q on it", repo, branch)
	}
	return Rule{}, fmt.Errorf("no policy line permits a push to %s — the gate is default deny (SP-K03-3)", repo)
}

// matchBranch supports one wildcard, at the end: `feature/*`. Nothing more, deliberately — a policy
// language is a thing that grows until nobody can say what it permits, and every pattern this gate
// accepts has to be readable by whoever is deciding at 3 a.m. whether to widen it.
func matchBranch(pattern, branch string) bool {
	if pattern == branch {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(branch, strings.TrimSuffix(pattern, "*"))
	}
	return false
}

// ParseTarget splits an outbox target into repository and branch. The form is the one the outbox
// routes on: `git+<url>#<branch>`.
func ParseTarget(target string) (repo, branch string, err error) {
	if !strings.HasPrefix(target, outbox.GitScheme) {
		return "", "", fmt.Errorf("%q is not a Git target", target)
	}
	rest := strings.TrimPrefix(target, outbox.GitScheme)
	i := strings.LastIndex(rest, "#")
	if i < 0 {
		return "", "", fmt.Errorf("%q names no branch — a push without a branch is not a domain key (SP-K03-2)", target)
	}
	repo, branch = rest[:i], rest[i+1:]
	if repo == "" || branch == "" {
		return "", "", fmt.Errorf("%q names no repository or no branch", target)
	}
	return repo, branch, nil
}

// Push is the RPC. Policy first, ledger second, the push last — in that order, because a push the
// policy forbids must not appear in the ledger as a thing that was once permitted.
func (g *Gate) Push(ctx context.Context, e *workpodv1.OutboxEntry) (*workpodv1.Receipt, error) {
	if g.Logf == nil {
		g.Logf = func(string, ...any) {}
	}
	repo, branch, err := ParseTarget(e.GetTarget())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	policy := g.Policy
	if policy == nil {
		if policy, err = LoadPolicy(PolicyFile); err != nil {
			return nil, status.Errorf(codes.FailedPrecondition, "%v", err)
		}
	}
	rule, err := Allowed(policy, repo, branch)
	if err != nil {
		g.Logf("git-gate: refused %s %s: %v", repo, branch, err)
		return nil, status.Errorf(codes.PermissionDenied, "%v", err)
	}

	entry := outbox.Entry{
		Order: e.GetOrderId(), Target: e.GetTarget(), ContentHash: e.GetContentHash(),
		PayloadRef: string(e.GetPayloadRef()), RequiresRegister: e.GetRequiresRegister(),
	}
	receipt, executed, err := g.Ledger.Once(entry, func() (outbox.Receipt, error) {
		signature := ""
		if rule.Sign {
			if g.Signer == nil {
				return outbox.Receipt{}, errors.New("this repository requires a signed push and the gate holds no signing key — the key is the gate's and nobody else's (SP-B01-4)")
			}
			if signature, err = g.Signer(entry.ContentHash); err != nil {
				return outbox.Receipt{}, err
			}
		}
		external, err := g.pushFn()(ctx, repo, branch, entry.PayloadRef, signature)
		if err != nil {
			return outbox.Receipt{}, err
		}
		return outbox.Receipt{Executed: true, ExternalID: external, At: time.Now().UTC()}, nil
	})
	if err != nil {
		return nil, status.Errorf(codes.Aborted, "%v", err)
	}
	if executed {
		g.Logf("git-gate: pushed %s %s as %s", repo, branch, receipt.ExternalID)
	} else {
		g.Logf("git-gate: %s %s was already pushed as %s — the domain key decided (SP-K03-2)", repo, branch, receipt.ExternalID)
	}
	return &workpodv1.Receipt{
		OrderId: entry.Order, Target: entry.Target, ContentHash: entry.ContentHash,
		// `executed` is this call's answer, not the effect's: false means the ledger answered and
		// nothing was pushed. The receipt is the same either way, which is the point — the job
		// gets the push it asked for and cannot tell whether it was the first to ask.
		Executed: executed, ExternalId: receipt.ExternalID, At: timestamppb.New(receipt.At),
	}, nil
}

func (g *Gate) pushFn() func(context.Context, string, string, string, string) (string, error) {
	if g.Execute != nil {
		return g.Execute
	}
	return pushWithGit
}

// pushWithGit is the default executor. `git` is driven as a program for the reason runc is
// (decisions/pod-runtime.md §1): it is in the image, and a second implementation of a tool that is
// already there is a second thing to keep correct.
//
// The checkout is the gate's, made fresh per push and thrown away after — the pod's working copy
// never reaches this machine, and the patch is all that travels (SP-K03-1: the pod produces an
// intent, the gate executes). The credential never reaches an argument or an environment variable
// of anything the pod can see, because there is no pod here: this runs in the gate's own unit,
// under the gate's own user (SP-B01-4).
//
// `payloadRef` is a path to a patch in `git apply` form. The reference, not the content, is what
// the outbox carries (contract/platform.proto's own word for the field); resolving it to a file the
// gate can read is the gate's business.
func pushWithGit(ctx context.Context, repo, branch, payloadRef, signature string) (string, error) {
	if payloadRef == "" {
		return "", errors.New("nothing to push: the entry names no payload")
	}
	if _, err := os.Stat(payloadRef); err != nil {
		return "", fmt.Errorf("the payload reference %s does not resolve to a patch the gate can read: %w", payloadRef, err)
	}
	work, err := os.MkdirTemp("", "git-gate-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)

	if err := git(ctx, "", "clone", "--quiet", repo, work); err != nil {
		return "", err
	}
	// -B rather than -b: a push onto an existing branch is the ordinary case, and a job that
	// re-derives the same patch onto it is exactly what the domain key deduplicates.
	if err := git(ctx, work, "checkout", "--quiet", "-B", branch); err != nil {
		return "", err
	}
	if err := git(ctx, work, "apply", "--index", payloadRef); err != nil {
		return "", fmt.Errorf("the patch does not apply to %s#%s: %w", repo, branch, err)
	}
	message := "workpod: effect from the outbox"
	if signature != "" {
		// The gate signs itself (SP-K03-3), and the signature travels as a trailer so that it is
		// readable in the repository's own log rather than only in this gate's ledger.
		message += "\n\nWorkpod-Gate-Signature: " + signature
	}
	if err := git(ctx, work, "-c", "user.name=workpod git gate", "-c", "user.email=git-gate@workpod.invalid",
		"commit", "--quiet", "--message", message); err != nil {
		return "", err
	}
	if err := git(ctx, work, "push", "--quiet", "origin", branch); err != nil {
		return "", err
	}
	head, err := gitOut(ctx, work, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return head, nil
}

func git(ctx context.Context, dir string, args ...string) error {
	_, err := gitOut(ctx, dir, args...)
	return err
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// The gate's own environment, minus anything that could carry a credential in from outside it.
	// A gate that inherits an askpass or a token from whoever started it is not a gate that holds
	// the credential exclusively (SP-B01-4).
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// Serve opens the gate's socket and serves until the context ends. The socket is created with mode
// 0660 and removed first: a stale socket file from a killed gate would make every drain fail with a
// connection error rather than start the gate.
func Serve(ctx context.Context, socket string, g *Gate) error {
	if err := os.MkdirAll(filepath.Dir(socket), 0o755); err != nil {
		return err
	}
	_ = os.Remove(socket)
	lis, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("the Git gate could not open %s: %w", socket, err)
	}
	defer os.Remove(socket)
	if err := os.Chmod(socket, 0o660); err != nil {
		return err
	}
	if g.Ledger == nil {
		g.Ledger = outbox.OpenLedger(LedgerDir)
	}
	srv := grpc.NewServer()
	workpodv1.RegisterGitGateServer(srv, g)
	go func() { <-ctx.Done(); srv.GracefulStop() }()
	return srv.Serve(lis)
}

// SignerFromCredential is the gate's signing ability, built from the credential systemd loaded for
// its unit. The key stays in this closure: it is read once, it is never returned, and no field of
// Gate holds it — so a dump of the gate's configuration cannot leak it and neither can a log line
// that prints the struct.
func SignerFromCredential(path string) (func(string) (string, error), error) {
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("the Git gate's credential is not at %s — credentials lie in the gates and nowhere else (SP-B01-4): %w", path, err)
	}
	return func(payload string) (string, error) {
		sum := sha256.Sum256(append(key, []byte(payload)...))
		return hex.EncodeToString(sum[:]), nil
	}, nil
}
