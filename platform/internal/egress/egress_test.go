package egress

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	workpodv1 "github.com/Cheety/warft/platform/api/workpodv1"
)

// resolves is a stub for the tests that are not about resolution. The requirement it stands in for
// — SP-B02-3, "the proxy resolves" — is checked by TestTheGateResolvesTheName and by the run in
// acceptance/k03-outbox.sh; here it only keeps a unit test off the network.
func resolves(context.Context, string) ([]string, error) { return []string{"203.0.113.1"}, nil }

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}

// SP-B02-4: an allowlist per job, derived from the authority — target, method and size limit, all
// three. The table is the derivation, and the levels are ordered widest last.
func TestTheAllowlistIsDerivedFromTheAuthorityLevel(t *testing.T) {
	public, err := Ruled.Derive("public")
	if err != nil {
		t.Fatal(err)
	}
	confidential, err := Ruled.Derive("confidential")
	if err != nil {
		t.Fatal(err)
	}
	if len(confidential.Targets) <= len(public.Targets) {
		t.Fatalf("a wider level did not derive more targets: %d vs %d", len(confidential.Targets), len(public.Targets))
	}
	// A level permits the operations of every level at or below it, so what `public` reaches,
	// `confidential` reaches too.
	for _, target := range public.Targets {
		found := false
		for _, c := range confidential.Targets {
			if c == target {
				found = true
			}
		}
		if !found {
			t.Fatalf("confidential does not derive %s, which public does", target)
		}
	}
	if confidential.SizeLimit < public.SizeLimit {
		t.Fatalf("a wider level derived a smaller size limit: %d < %d", confidential.SizeLimit, public.SizeLimit)
	}
}

// Failing closed is the cheap half of B-02 and the one usually missing: a level added to the enum
// and forgotten in the table must break loudly rather than quietly reach nothing.
func TestAnUnknownLevelReachesNothingAndSaysSo(t *testing.T) {
	if _, err := Ruled.Derive("secret"); err == nil {
		t.Fatal("an authority level nobody derived an allowlist for was accepted")
	}
	if _, err := Ruled.Derive(""); err == nil {
		t.Fatal("the empty level derived an allowlist")
	}
}

// SP-B02-4's three questions, each answered on its own so a refusal names which one failed.
func TestPermitsChecksTargetMethodAndSize(t *testing.T) {
	a := Allowance{Targets: []string{"proxy.golang.org", "*.github.com"}, Methods: []string{"GET"}, SizeLimit: 100}
	if err := a.Permits("proxy.golang.org", "GET", 50); err != nil {
		t.Fatalf("a permitted request was refused: %v", err)
	}
	if err := a.Permits("api.github.com", "GET", 50); err != nil {
		t.Fatalf("the wildcard did not match: %v", err)
	}
	if err := a.Permits("evil.example.org", "GET", 50); err == nil {
		t.Fatal("a target outside the allowlist was permitted")
	}
	if err := a.Permits("proxy.golang.org", "POST", 50); err == nil {
		t.Fatal("a method outside the allowlist was permitted")
	}
	if err := a.Permits("proxy.golang.org", "GET", 200); err == nil {
		t.Fatal("a size beyond the limit was permitted")
	}
}

// A wildcard may not become a route. `*.github.com` matches a subdomain and the domain itself, and
// a bare `*` matches nothing at all.
func TestTheWildcardIsNotAnEscapeHatch(t *testing.T) {
	a := Allowance{Targets: []string{"*.github.com"}, Methods: []string{"GET"}, SizeLimit: 100}
	if err := a.Permits("api.github.com", "GET", 1); err != nil {
		t.Fatalf("a subdomain was refused: %v", err)
	}
	// The suffix must be on a label boundary, or `notgithub.com` would match `*.github.com`.
	if err := a.Permits("evilgithub.com", "GET", 1); err == nil {
		t.Fatal("a host that merely ends in the pattern was permitted")
	}
	if (Allowance{Targets: []string{"*"}, Methods: []string{"GET"}, SizeLimit: 1}).Permits("anything.org", "GET", 1) == nil {
		t.Fatal("a bare `*` matched — an allowlist that matches everything is a route")
	}
}

// SP-B02-3: no name resolution in the pod. A pod that sends an address has resolved something, and
// the only way it could have is a resolver it must not have.
func TestAnAddressIsRefusedBecauseThePodResolvesNothing(t *testing.T) {
	a := Allowance{Targets: []string{"proxy.golang.org"}, Methods: []string{"GET"}, SizeLimit: 100}
	for _, addr := range []string{"93.184.216.34", "::1", "127.0.0.1"} {
		if err := a.Permits(addr, "GET", 1); err == nil {
			t.Fatalf("%s was accepted as a target — the pod resolves nothing (SP-B02-3)", addr)
		}
	}
}

// SP-B02-4: per job, not per node. Two jobs on the same node reach different sets of targets, and
// the lookup happens per order at every single forward.
func TestTheAllowlistIsPerJobAndNotPerNode(t *testing.T) {
	dir := t.TempDir()
	narrow, err := Ruled.Derive("public")
	if err != nil {
		t.Fatal(err)
	}
	wide, err := Ruled.Derive("confidential")
	if err != nil {
		t.Fatal(err)
	}
	if err := Grant(dir, "order-narrow", narrow); err != nil {
		t.Fatal(err)
	}
	if err := Grant(dir, "order-wide", wide); err != nil {
		t.Fatal(err)
	}
	g := &Gate{Journal: filepath.Join(t.TempDir(), "rejections.jsonl"), Grants: dir, Resolve: resolves, Do: func(*http.Request) (*http.Response, error) { return response(200, "ok"), nil }}

	// A target only the wide job may reach. Both jobs ask for it, on the same node, in the same
	// process — and only one of them gets it.
	target := "https://" + wide.Targets[len(wide.Targets)-1]
	if strings.HasPrefix(target, "https://*.") {
		target = strings.Replace(target, "*.", "host.", 1)
	}
	for _, host := range narrow.Targets {
		if strings.Contains(target, host) {
			t.Skip("the table's widest target is also a narrow one; nothing to distinguish")
		}
	}

	res, err := g.Forward(context.Background(), &workpodv1.EgressRequest{OrderId: "order-narrow", Target: target, Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.GetDenied() {
		t.Fatalf("the narrow job reached %s — the allowlist is the job's, not the node's", target)
	}
	// The refusal names the target, because SP-B02-5 wants it in the display and not only in a log.
	if !strings.Contains(res.GetDeniedReason(), strings.TrimPrefix(target, "https://")) {
		t.Fatalf("the refusal does not name the target: %q", res.GetDeniedReason())
	}
	if len(g.Rejected()) != 1 {
		t.Fatalf("%d rejections kept for the display, expected 1 (SP-B02-5)", len(g.Rejected()))
	}
}

// A job nobody granted anything reaches nothing. Default deny stated as the absence of a grant.
func TestAJobWithNoGrantReachesNothing(t *testing.T) {
	g := &Gate{Journal: filepath.Join(t.TempDir(), "rejections.jsonl"), Grants: t.TempDir(), Do: func(*http.Request) (*http.Response, error) {
		t.Fatal("a job with no allowlist reached the network")
		return nil, nil
	}}
	res, err := g.Forward(context.Background(), &workpodv1.EgressRequest{
		OrderId: "never-granted", Target: "https://proxy.golang.org/x", Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.GetDenied() {
		t.Fatal("a job with no grant was forwarded")
	}
}

// A request with no order has no allowlist at all, and an allowlist is what SP-B02-4 requires per
// job — so there is nothing to fall back to.
func TestARequestWithoutAnOrderIsRefused(t *testing.T) {
	g := &Gate{Grants: t.TempDir()}
	res, err := g.Forward(context.Background(), &workpodv1.EgressRequest{Target: "https://proxy.golang.org/x"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.GetDenied() {
		t.Fatal("a request without an order was forwarded")
	}
}

// SP-B01-4: keys are inserted here and nowhere earlier. The request that leaves the gate carries
// one; nothing the pod sent did.
func TestTheGateInsertsTheKey(t *testing.T) {
	dir := t.TempDir()
	a := Allowance{Level: "public", Targets: []string{"proxy.golang.org"}, Methods: []string{"GET"}, SizeLimit: 1000}
	if err := Grant(dir, "o1", a); err != nil {
		t.Fatal(err)
	}
	var sent *http.Request
	g := &Gate{
		Journal: filepath.Join(t.TempDir(), "rejections.jsonl"),
		Grants:  dir,
		Resolve: resolves,
		Keys:    func(host string) (string, error) { return "secret-for-" + host, nil },
		Do:      func(r *http.Request) (*http.Response, error) { sent = r; return response(200, "ok"), nil },
	}
	if _, err := g.Forward(context.Background(), &workpodv1.EgressRequest{
		OrderId: "o1", Target: "https://proxy.golang.org/x", Method: "GET"}); err != nil {
		t.Fatal(err)
	}
	if sent == nil {
		t.Fatal("nothing was forwarded")
	}
	if sent.Header.Get("Authorization") != "Bearer secret-for-proxy.golang.org" {
		t.Fatalf("the gate inserted %q", sent.Header.Get("Authorization"))
	}
}

// The size limit is enforced on what arrives, not on what the target claims about it.
func TestTheSizeLimitIsEnforcedOnWhatArrives(t *testing.T) {
	dir := t.TempDir()
	if err := Grant(dir, "o1", Allowance{Targets: []string{"proxy.golang.org"}, Methods: []string{"GET"}, SizeLimit: 8}); err != nil {
		t.Fatal(err)
	}
	g := &Gate{Journal: filepath.Join(t.TempDir(), "rejections.jsonl"), Grants: dir, Resolve: resolves, Do: func(*http.Request) (*http.Response, error) {
		// A target that lies about its length: the header says 1, the body is 40 bytes.
		r := response(200, strings.Repeat("x", 40))
		r.Header.Set("Content-Length", "1")
		return r, nil
	}}
	res, err := g.Forward(context.Background(), &workpodv1.EgressRequest{
		OrderId: "o1", Target: "https://proxy.golang.org/x", Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.GetDenied() {
		t.Fatalf("%d bytes came back through a limit of 8", len(res.GetBodyRef()))
	}
}

// A response exactly at the limit passes: the limit is a limit and not an off-by-one.
func TestAResponseAtTheLimitPasses(t *testing.T) {
	dir := t.TempDir()
	if err := Grant(dir, "o1", Allowance{Targets: []string{"proxy.golang.org"}, Methods: []string{"GET"}, SizeLimit: 8}); err != nil {
		t.Fatal(err)
	}
	g := &Gate{Journal: filepath.Join(t.TempDir(), "rejections.jsonl"), Grants: dir, Resolve: resolves, Do: func(*http.Request) (*http.Response, error) { return response(200, "12345678"), nil }}
	res, err := g.Forward(context.Background(), &workpodv1.EgressRequest{
		OrderId: "o1", Target: "https://proxy.golang.org/x", Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	if res.GetDenied() {
		t.Fatalf("a response exactly at the limit was refused: %s", res.GetDeniedReason())
	}
	if string(res.GetBodyRef()) != "12345678" {
		t.Fatalf("body %q", res.GetBodyRef())
	}
}

// A grant written and read back is the same allowance. The file on /var is what the gate consults,
// so a round trip that loses a field would lose a boundary.
func TestAGrantSurvivesTheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := Allowance{Level: "linked", Targets: []string{"a.example.org", "*.b.org"}, Methods: []string{"GET", "HEAD"}, SizeLimit: 4242}
	if err := Grant(dir, "o1", want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadGrant(dir, "o1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Level != want.Level || got.SizeLimit != want.SizeLimit ||
		strings.Join(got.Targets, ",") != strings.Join(want.Targets, ",") ||
		strings.Join(got.Methods, ",") != strings.Join(want.Methods, ",") {
		t.Fatalf("read back %+v, wrote %+v", got, want)
	}
}

func TestHostOf(t *testing.T) {
	for target, want := range map[string]string{
		"https://proxy.golang.org/x/y?z=1": "proxy.golang.org",
		"https://api.github.com":           "api.github.com",
		"https://host.example.org:8443/p":  "host.example.org",
	} {
		got, err := hostOf(target)
		if err != nil || got != want {
			t.Fatalf("%s -> %q (%v), expected %q", target, got, err, want)
		}
	}
	// The gate speaks one scheme. Anything else is not an egress target (SP-B02-1).
	for _, bad := range []string{"http://plain.example.org", "git+/repo#main", "proxy.golang.org"} {
		if _, err := hostOf(bad); err == nil {
			t.Fatalf("%q was taken for an egress target", bad)
		}
	}
}

// SP-B02-3: the proxy resolves. The gate looks the name up itself before it forwards, so a name
// that does not resolve is refused here with a cause rather than by a transport with a stack trace.
func TestTheGateResolvesTheName(t *testing.T) {
	dir := t.TempDir()
	if err := Grant(dir, "o1", Allowance{Targets: []string{"proxy.golang.org"}, Methods: []string{"GET"}, SizeLimit: 100}); err != nil {
		t.Fatal(err)
	}
	asked := ""
	g := &Gate{
		Grants: dir,
		Resolve: func(_ context.Context, host string) ([]string, error) {
			asked = host
			return nil, errors.New("NXDOMAIN")
		},
		Do: func(*http.Request) (*http.Response, error) {
			t.Fatal("the gate forwarded a name it could not resolve")
			return nil, nil
		},
	}
	res, err := g.Forward(context.Background(), &workpodv1.EgressRequest{
		OrderId: "o1", Target: "https://proxy.golang.org/x", Method: "GET"})
	if err != nil {
		t.Fatal(err)
	}
	if asked != "proxy.golang.org" {
		t.Fatalf("the gate resolved %q", asked)
	}
	if !res.GetDenied() || !strings.Contains(res.GetDeniedReason(), "resolve") {
		t.Fatalf("an unresolvable name was not refused with a cause: %+v", res)
	}
}
