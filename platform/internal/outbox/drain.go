package outbox

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	workpodv1 "github.com/Cheety/warft/platform/api/workpodv1"
)

// The two sockets. Unix sockets and not ports, because SP-B02-6 means what it says: no open port is
// no open port, and a gate reachable only through a file on the machine cannot be reached from
// another one even by accident. That is also what makes AB-B02-2's "no central throughput
// bottleneck" a property of the shape rather than of a configuration — the egress gate a work node
// talks to is on the work node, because a socket has no other end anywhere else.
const (
	GitSocket    = "/run/workpod/git-gate.sock"
	EgressSocket = "/run/workpod/egress-gate.sock"
)

// Sockets is where a drain finds the two gates.
type Sockets struct {
	Git    string
	Egress string
}

// DefaultSockets is the layout of a node.
func DefaultSockets() Sockets { return Sockets{Git: GitSocket, Egress: EgressSocket} }

func (s Sockets) for_(g Gate) string {
	if g == Git {
		return s.Git
	}
	return s.Egress
}

// Drained is what one pass over the outbox did.
type Drained struct {
	Executed     int // the gate acted, and the receipt is in the entry
	Deduplicated int // the gate had done it before; the receipt came out of its ledger
	Denied       int // refused by policy or allowlist, with the cause in the entry
	Asked        int // the acknowledgement is missing on a target that may not be called twice
}

// Drain is the middle of SP-K03-1's chain: outbox → gate → receipt back into the job. It is a pull,
// like everything else in V-02 — the worker walks its own outbox and calls the gates; no gate ever
// reaches into a node.
//
// One pass, not a loop. The caller decides the cadence, so that a drain can be a unit's timer on a
// node and a single call in a probe without the function knowing the difference.
func Drain(ctx context.Context, s *Store, sockets Sockets, logf func(string, ...any)) (Drained, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	var d Drained

	// SP-K03-4's second sentence, before anything else this pass does. An entry left in Executing
	// on a non-idempotent target is a gate call whose acknowledgement never arrived, and the
	// requirement is to *ask* about it — so the drain moves it to Asking and says so, rather than
	// leaving it in a state nobody is looking at. Doing it first means a duty officer sees the
	// question in the same output as the work, and doing it through Begin means the transition is
	// the store's one and only path there: Begin refuses, and the refusal is what performs it.
	stranded, err := s.Unanswered()
	if err != nil {
		return d, err
	}
	for _, e := range stranded {
		if e.State != Executing {
			continue
		}
		if _, err := s.Begin(e.Key()); err != nil {
			if !errors.Is(err, ErrAskDoNotRetry) {
				return d, err
			}
			logf("outbox: %s needs asking, not a second call (SP-K03-4)", e.Key())
			d.Asked++
			continue
		}
		// Begin succeeded, which for a Unanswered entry means the register did not hold. That is a
		// broken invariant rather than a condition to carry on through.
		return d, fmt.Errorf("%s was in %s on a non-idempotent target and Begin permitted it — the register is broken (SP-K03-4)", e.Key(), e.State)
	}

	pending, err := s.Pending()
	if err != nil {
		return d, err
	}
	for _, e := range pending {
		gate, err := GateFor(e.Target)
		if err != nil {
			if _, dErr := s.Deny(e.Key(), err.Error()); dErr != nil {
				return d, dErr
			}
			d.Denied++
			continue
		}
		// The register's first half, on the outbox's side: written down before the gate is
		// called. This is where SP-K03-4's refusal happens for an entry a dead worker left in
		// Executing on a non-idempotent target.
		if _, err := s.Begin(e.Key()); err != nil {
			if errors.Is(err, ErrAskDoNotRetry) {
				logf("outbox: %s needs asking, not a second call (SP-K03-4)", e.Key())
				d.Asked++
				continue
			}
			return d, err
		}
		receipt, deduplicated, err := call(ctx, sockets.for_(gate), gate, e)
		if err != nil {
			if _, dErr := s.Deny(e.Key(), err.Error()); dErr != nil {
				return d, dErr
			}
			logf("outbox: %s refused by the %s: %v", e.Key(), gate, err)
			d.Denied++
			continue
		}
		if _, err := s.Acknowledge(e.Key(), receipt); err != nil {
			return d, err
		}
		if deduplicated {
			d.Deduplicated++
			logf("outbox: %s was already executed — the %s's ledger answered (SP-K03-2)", e.Key(), gate)
		} else {
			d.Executed++
			logf("outbox: %s executed by the %s, receipt %s", e.Key(), gate, receipt.ExternalID)
		}
	}
	return d, nil
}

// call is one gate call. The bool is the gate's own answer to "had you done this before?" — a
// receipt whose `executed` is false is the ledger speaking, not a failure.
func call(ctx context.Context, socket string, gate Gate, e Entry) (Receipt, bool, error) {
	conn, err := Dial(ctx, socket)
	if err != nil {
		return Receipt{}, false, err
	}
	defer conn.Close()

	entry := &workpodv1.OutboxEntry{
		OrderId: e.Order, Target: e.Target, ContentHash: e.ContentHash,
		PayloadRef: []byte(e.PayloadRef), RequiresRegister: e.RequiresRegister,
	}
	switch gate {
	case Git:
		r, err := workpodv1.NewGitGateClient(conn).Push(ctx, entry)
		if err != nil {
			return Receipt{}, false, err
		}
		return Receipt{Executed: true, ExternalID: r.GetExternalId(), At: r.GetAt().AsTime()}, !r.GetExecuted(), nil
	case Egress:
		res, err := workpodv1.NewEgressGateClient(conn).Forward(ctx, &workpodv1.EgressRequest{
			OrderId: e.Order, Target: e.Target, Method: "GET", BodyRef: []byte(e.PayloadRef),
		})
		if err != nil {
			return Receipt{}, false, err
		}
		if res.GetDenied() {
			return Receipt{}, false, fmt.Errorf("%s", res.GetDeniedReason())
		}
		return Receipt{Executed: true, ExternalID: fmt.Sprintf("%d", res.GetStatus()), At: time.Now().UTC()}, false, nil
	}
	return Receipt{}, false, fmt.Errorf("no gate %q", gate)
}

// Dial opens a connection to a gate's Unix socket. Insecure credentials are not a gap here: the
// peer is a file on this machine with its own owner and mode, and a TLS handshake over it would
// authenticate the same process to itself. What crosses a machine boundary is authenticated by the
// certificate name of contract/identity.md, and nothing here does.
func Dial(ctx context.Context, socket string) (*grpc.ClientConn, error) {
	return grpc.NewClient("passthrough:///"+socket,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		}))
}
