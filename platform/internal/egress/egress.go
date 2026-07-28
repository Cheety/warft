// Package egress is the second of SP-K03-3's two gates: the egress proxy. It stands on the **work
// node** and not centrally (SP-B02-2) — one per node, reached over a Unix socket, so there is no
// central hop every outbound byte of the fleet has to pass through.
//
// What it does, in the order it does it:
//
//	allowlist  the job's own, looked up by order id at every forward (SP-B02-4)
//	name       the pod sent a name; an address is refused (SP-B02-3)
//	resolve    the gate resolves, because the pod has no resolver and no network (T-04)
//	keys       inserted here and nowhere earlier (SP-B01-4)
//	size       the response is bounded by the job's limit, not by the target's honesty
//
// The allowlist is per job, not per node, and that distinction is the whole requirement: a node-wide
// rule passes every test that has one job on the node and is wrong the moment there are two. The
// lookup is therefore keyed by `order_id` and consults nothing else.
package egress

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"

	workpodv1 "github.com/Cheety/warft/platform/api/workpodv1"
	"github.com/Cheety/warft/platform/internal/outbox"
)

// The derivation table of decisions/gates-and-the-outbox.md §3 — one row per authority level. It is
// a file beside the program rather than a switch in it, because AB-B02-4 is a script check: a table
// has rows a script can hold against contract/schema.sql's enum, a switch has branches a script can
// only re-implement. Embedded, for the reason ra1-classes.tsv is: the gate must derive an allowlist
// on a node where nothing but the binary was installed.
//
//go:embed b02-allowlist.tsv
var allowlistSource string

// AllowlistFile is the name a script looks the table up under.
const AllowlistFile = "b02-allowlist.tsv"

// GrantsDir is where a node keeps what each job is allowed to reach. One file per order, written
// when the job is admitted; the gate reads it and holds no state of its own.
const GrantsDir = "/var/lib/workpod/egress-grants"

// Allowance is what one job may reach: SP-B02-4's three, and nothing else. There is no "everything
// below this" flag and no escape value — a field that could mean "unbounded" is a field that will.
type Allowance struct {
	Level     string   `json:"level"`
	Targets   []string `json:"targets"`
	Methods   []string `json:"methods"`
	SizeLimit int64    `json:"size_limit"`
}

// Table is the level-derived allowlist, in the order the file names the levels: widest last.
type Table struct {
	Levels []string
	Rows   map[string]Allowance
}

// Ruled is the derivation table as it was built into the binary. Parsed once: the file is embedded
// at build time, so a second parse could not produce a different answer.
var Ruled = mustParse(allowlistSource)

func mustParse(src string) Table {
	t, err := ParseTable(src)
	if err != nil {
		// Embedded at build time: a malformed table is a broken build, not a runtime condition a
		// caller could do anything about.
		panic(AllowlistFile + ": " + err.Error())
	}
	return t
}

// LoadTable reads a derivation table from a path. Used by a probe that wants to hold the file
// against the binary; the gate itself uses Ruled.
func LoadTable(path string) (Table, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Table{}, fmt.Errorf("no allowlist derivation at %s — without it the gate reaches nothing, which is the default (SP-B02-4): %w", path, err)
	}
	return ParseTable(string(body))
}

// ParseTable reads the derivation table. A level missing from it is not a level with no targets, it
// is a level the gate has never been told about — and the difference matters, because the first
// would be a silent refusal and the second is a refusal that names its cause.
func ParseTable(body string) (Table, error) {
	path := AllowlistFile
	t := Table{Rows: map[string]Allowance{}}
	for n, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if f[0] == "level" {
			continue // the header
		}
		if len(f) < 4 {
			return Table{}, fmt.Errorf("%s:%d: expected level, targets, methods, size_limit", path, n+1)
		}
		limit, err := strconv.ParseInt(strings.TrimSpace(f[3]), 10, 64)
		if err != nil {
			return Table{}, fmt.Errorf("%s:%d: size limit: %w", path, n+1, err)
		}
		level := strings.TrimSpace(f[0])
		t.Levels = append(t.Levels, level)
		t.Rows[level] = Allowance{
			Level:     level,
			Targets:   split(f[1]),
			Methods:   split(f[2]),
			SizeLimit: limit,
		}
	}
	if len(t.Levels) == 0 {
		return Table{}, fmt.Errorf("%s names no level", path)
	}
	return t, nil
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(strings.TrimSpace(s), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Derive is SP-B02-4's "derived from the authority": the allowance of a level is its own row plus
// every row below it, because a level permits the operations of every level at or below it. The
// ordering lives in the file, and nowhere else.
//
// An unknown level is an error and not an empty allowance. Failing closed is the cheap half of
// B-02, and it is the half that is usually missing: a level added to the enum and forgotten here
// must break the gate loudly rather than quietly reach nothing.
func (t Table) Derive(level string) (Allowance, error) {
	rank := -1
	for i, l := range t.Levels {
		if l == level {
			rank = i
		}
	}
	if rank < 0 {
		return Allowance{}, fmt.Errorf("the authority level %q is not derived by the allowlist table — an unknown level reaches nothing (SP-B02-4)", level)
	}
	a := Allowance{Level: level}
	for i := 0; i <= rank; i++ {
		row := t.Rows[t.Levels[i]]
		a.Targets = append(a.Targets, row.Targets...)
		a.Methods = append(a.Methods, row.Methods...)
		if row.SizeLimit > a.SizeLimit {
			a.SizeLimit = row.SizeLimit
		}
	}
	return a, nil
}

// Permits answers SP-B02-4's three questions about one request, and it answers them in the order
// that makes a refusal readable: the target first, because that is the one an injected instruction
// tries to move.
func (a Allowance) Permits(host, method string, want int64) error {
	if err := isName(host); err != nil {
		return err
	}
	matched := false
	for _, t := range a.Targets {
		if matchHost(t, host) {
			matched = true
			break
		}
	}
	if !matched {
		return fmt.Errorf("%s is not on this job's allowlist — the allowlist is the job's, not the node's (SP-B02-4)", host)
	}
	ok := false
	for _, m := range a.Methods {
		if strings.EqualFold(m, method) {
			ok = true
			break
		}
	}
	if !ok {
		return fmt.Errorf("%s is not a method this job may use on %s (SP-B02-4)", method, host)
	}
	if want > 0 && want > a.SizeLimit {
		return fmt.Errorf("%d bytes exceeds this job's size limit of %d (SP-B02-4)", want, a.SizeLimit)
	}
	return nil
}

// isName is SP-B02-3 read as a refusal: "no name resolution in the pod. The proxy resolves; the pod
// knows only names from its allowlist." A pod that sends an address has resolved something, and the
// only way it could have is a resolver it is not supposed to have.
func isName(host string) error {
	if host == "" {
		return errors.New("the request names no host")
	}
	if net.ParseIP(host) != nil {
		return fmt.Errorf("%s is an address, not a name — the pod resolves nothing and the gate resolves everything (SP-B02-3)", host)
	}
	return nil
}

// matchHost supports one wildcard, at the front, and never a bare `*`. Same argument as the Git
// gate's branch patterns: a policy language grows until nobody can say what it permits.
func matchHost(pattern, host string) bool {
	if pattern == "*" {
		return false
	}
	if pattern == host {
		return true
	}
	if suffix, ok := strings.CutPrefix(pattern, "*."); ok {
		return strings.HasSuffix(host, "."+suffix) || host == suffix
	}
	return false
}

// Grant writes what a job may reach. Called when a job is admitted, so that the gate's lookup is a
// file read and not a call back into a control plane that may be down (V-02).
func Grant(dir, orderID string, a Allowance) error {
	if orderID == "" {
		return errors.New("a grant belongs to an order — the allowlist is per job (SP-B02-4)")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	body := fmt.Sprintf("level\t%s\ntargets\t%s\nmethods\t%s\nsize_limit\t%d\n",
		a.Level, strings.Join(a.Targets, ","), strings.Join(a.Methods, ","), a.SizeLimit)
	return os.WriteFile(filepath.Join(dir, orderID+".tsv"), []byte(body), 0o640)
}

// LoadGrant reads one job's allowance. A job with no grant reaches nothing — default deny, stated
// as the absence of a file rather than as an empty one, so that "nobody decided" and "somebody
// decided nothing" are not the same state.
func LoadGrant(dir, orderID string) (Allowance, error) {
	body, err := os.ReadFile(filepath.Join(dir, orderID+".tsv"))
	if err != nil {
		if os.IsNotExist(err) {
			return Allowance{}, fmt.Errorf("no allowlist for order %s — a job nobody granted anything reaches nothing (SP-B02-4)", orderID)
		}
		return Allowance{}, err
	}
	a := Allowance{}
	for _, line := range strings.Split(string(body), "\n") {
		f := strings.SplitN(strings.TrimSpace(line), "\t", 2)
		if len(f) != 2 {
			continue
		}
		switch f[0] {
		case "level":
			a.Level = f[1]
		case "targets":
			a.Targets = split(f[1])
		case "methods":
			a.Methods = split(f[1])
		case "size_limit":
			if a.SizeLimit, err = strconv.ParseInt(f[1], 10, 64); err != nil {
				return Allowance{}, err
			}
		}
	}
	return a, nil
}

// Gate is the egress proxy.
type Gate struct {
	workpodv1.UnimplementedEgressGateServer

	Grants string
	// Keys is what SP-B01-4 puts here and nowhere else: the credential a target needs, inserted by
	// the gate at the moment of the call. A function rather than a map field, so the secret lives
	// in a closure over the credential directory and never in a struct anything might print.
	Keys func(host string) (string, error)
	// Do is the outbound call. A field so a probe can watch the allowlist, the resolution and the
	// key insertion without reaching the internet — what AP-3.5 owns is the gate's rule, not the
	// HTTP client under it.
	Do func(req *http.Request) (*http.Response, error)
	// Resolve is the gate resolving a name (SP-B02-3). A field for the same reason Do is, and it
	// changes nothing about the requirement: the resolution happens *here* either way, and the
	// pod has no resolver in any configuration of this struct.
	Resolve func(ctx context.Context, host string) ([]string, error)
	Logf    func(string, ...any)

	// Rejected is SP-B02-5: rejected targets belong in the display, not only in the log — "the
	// best early warning signal for injection this system has". The gate keeps the last ones so
	// the console can show them without parsing a journal.
	rejected []Rejection
}

// Rejection is one refused target, kept for the display.
type Rejection struct {
	OrderID string    `json:"order_id"`
	Target  string    `json:"target"`
	Method  string    `json:"method"`
	Reason  string    `json:"reason"`
	At      time.Time `json:"at"`
}

// Rejected is what the display reads (SP-B02-5).
func (g *Gate) Rejected() []Rejection { return append([]Rejection(nil), g.rejected...) }

// Forward is the RPC. Allowlist, then name, then resolve, then key, then size — and a refusal at
// any of them is an answer with `denied` set rather than an error, because SP-B02-5 wants the
// rejected target to reach a display and a transport error does not carry one.
func (g *Gate) Forward(ctx context.Context, req *workpodv1.EgressRequest) (*workpodv1.EgressResponse, error) {
	if g.Logf == nil {
		g.Logf = func(string, ...any) {}
	}
	deny := func(format string, a ...any) (*workpodv1.EgressResponse, error) {
		reason := fmt.Sprintf(format, a...)
		g.rejected = append(g.rejected, Rejection{
			OrderID: req.GetOrderId(), Target: req.GetTarget(), Method: req.GetMethod(),
			Reason: reason, At: time.Now().UTC(),
		})
		g.Logf("egress-gate: refused %s for order %s: %s", req.GetTarget(), req.GetOrderId(), reason)
		return &workpodv1.EgressResponse{Denied: true, DeniedReason: reason}, nil
	}

	if req.GetOrderId() == "" {
		return deny("a request without an order has no allowlist, and the allowlist is per job (SP-B02-4)")
	}
	allowance, err := LoadGrant(g.grantsDir(), req.GetOrderId())
	if err != nil {
		return deny("%v", err)
	}
	host, err := hostOf(req.GetTarget())
	if err != nil {
		return deny("%v", err)
	}
	method := req.GetMethod()
	if method == "" {
		method = http.MethodGet
	}
	if err := allowance.Permits(host, method, 0); err != nil {
		return deny("%v", err)
	}

	// The gate resolves; the pod knows only names (SP-B02-3). Resolving here rather than letting
	// the HTTP client do it is what makes that requirement observable: a name that does not resolve
	// is refused by the gate with a cause, not by a transport with a stack trace.
	addrs, err := g.resolve()(ctx, host)
	if err != nil {
		return deny("the gate could not resolve %s: %v", host, err)
	}
	g.Logf("egress-gate: %s resolves to %s for order %s", host, strings.Join(addrs, ","), req.GetOrderId())

	httpReq, err := http.NewRequestWithContext(ctx, method, req.GetTarget(), nil)
	if err != nil {
		return deny("%v", err)
	}
	// The key is inserted here, at the last possible moment, and it never existed anywhere the pod
	// could see (SP-B01-4). A target with no key is not an error: not everything needs one.
	if g.Keys != nil {
		key, err := g.Keys(host)
		if err != nil {
			return deny("%v", err)
		}
		if key != "" {
			httpReq.Header.Set("Authorization", "Bearer "+key)
		}
	}

	res, err := g.do()(httpReq)
	if err != nil {
		return deny("%v", err)
	}
	defer res.Body.Close()

	// The size limit is enforced on what arrives, not on what the target claims: a Content-Length
	// is the target's opinion and this is the gate's. One byte over the limit is read so that the
	// difference between "exactly the limit" and "more than it" is detectable.
	body, err := io.ReadAll(io.LimitReader(res.Body, allowance.SizeLimit+1))
	if err != nil {
		return deny("%v", err)
	}
	if int64(len(body)) > allowance.SizeLimit {
		return deny("the response exceeds this job's size limit of %d bytes (SP-B02-4)", allowance.SizeLimit)
	}
	return &workpodv1.EgressResponse{Status: uint32(res.StatusCode), BodyRef: body}, nil
}

func (g *Gate) grantsDir() string {
	if g.Grants != "" {
		return g.Grants
	}
	return GrantsDir
}

func (g *Gate) resolve() func(context.Context, string) ([]string, error) {
	if g.Resolve != nil {
		return g.Resolve
	}
	return net.DefaultResolver.LookupHost
}

func (g *Gate) do() func(*http.Request) (*http.Response, error) {
	if g.Do != nil {
		return g.Do
	}
	client := &http.Client{Timeout: 60 * time.Second}
	return client.Do
}

// hostOf takes the host out of a target. The target is a URL because that is what the pod asked
// for; the host is what the allowlist is written in.
func hostOf(target string) (string, error) {
	rest, ok := strings.CutPrefix(target, outbox.EgressScheme)
	if !ok {
		return "", fmt.Errorf("%q is not an egress target — the gate speaks %s and nothing else (SP-B02-1)", target, outbox.EgressScheme)
	}
	host := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		host = rest[:i]
	}
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	if host == "" {
		return "", fmt.Errorf("%q names no host", target)
	}
	return host, nil
}

// Serve opens the gate's socket on the work node and serves until the context ends.
func Serve(ctx context.Context, socket string, g *Gate) error {
	if err := os.MkdirAll(filepath.Dir(socket), 0o755); err != nil {
		return err
	}
	_ = os.Remove(socket)
	lis, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("the egress gate could not open %s: %w", socket, err)
	}
	defer os.Remove(socket)
	if err := os.Chmod(socket, 0o660); err != nil {
		return err
	}
	srv := grpc.NewServer()
	workpodv1.RegisterEgressGateServer(srv, g)
	go func() { <-ctx.Done(); srv.GracefulStop() }()
	return srv.Serve(lis)
}

// KeysFromCredential is the gate's key insertion, built from the credential directory systemd
// loaded for its unit: one file per host. The directory is read at call time rather than cached,
// so revoking a key does not need a restart — the same argument the Git gate's policy is read
// under.
//
// The keys never leave this closure. Nothing in Gate holds one, so no log line that prints the
// struct and no dump of its configuration can leak one (SP-B01-4).
func KeysFromCredential(dir string) func(string) (string, error) {
	return func(host string) (string, error) {
		body, err := os.ReadFile(filepath.Join(dir, host))
		if err != nil {
			if os.IsNotExist(err) {
				return "", nil // not every target needs a key
			}
			return "", err
		}
		return strings.TrimSpace(string(body)), nil
	}
}
