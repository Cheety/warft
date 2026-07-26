// Package controlplane serves the ControlPlane service of contract/platform.proto — the one
// interface schema of the system (E-10).
//
// In AP-3.1 the plane does what A-04's last step needs and nothing more: it accepts a node's
// capacity request, which is the moment "register (pulling begins)" — V-02's ferry is pull, the
// control plane never calls. Everything else on the service answers with the work package that
// builds it, so the surface of E-10 exists in the one artifact (AB-E02-1) without a fake behind
// it.
//
// The listener binds loopback only. mTLS with the certificate name of contract/identity.md is
// enrollment's to wire (AP-6.1); until a connection can state role and cell instead of claiming
// them, a plaintext plane must not be reachable from another machine.
package controlplane

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	workpodv1 "github.com/Cheety/warft/platform/api/workpodv1"
)

type server struct {
	workpodv1.UnimplementedControlPlaneServer

	mu    sync.Mutex
	nodes map[string]string // node_id → cell
}

// Serve is `workpod control`: bind the address the `control` boot value names and serve until
// stopped.
func Serve(addr string) error {
	if err := requireLoopback(addr); err != nil {
		return err
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s := grpc.NewServer()
	workpodv1.RegisterControlPlaneServer(s, &server{nodes: map[string]string{}})
	log.Printf("control plane serving on %s (loopback only until AP-6.1 gives connections a name)", addr)
	return s.Serve(lis)
}

func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("control address %q: %w", addr, err)
	}
	ip := net.ParseIP(host)
	if host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return nil
	}
	return fmt.Errorf("control address %q is not loopback — a plaintext control plane stays on the machine; crossing machines needs the certificate name of contract/identity.md (AP-6.1)", addr)
}

// RequestCapacity is the register step's other half: a node that asks for capacity is a node
// that has started pulling (SP-A04-2). The header is the acknowledgement; the stream then stays
// open because leases travel on it — and before AP-3.2 there are no jobs to lease, so an open,
// empty stream is the truthful answer.
func (s *server) RequestCapacity(req *workpodv1.CapacityRequest, stream grpc.ServerStreamingServer[workpodv1.Lease]) error {
	if req.GetNodeId() == "" || req.GetCell() == "" {
		return status.Error(codes.InvalidArgument, "a capacity request names node_id and cell — identity is stated, not inferred")
	}
	s.mu.Lock()
	s.nodes[req.GetNodeId()] = req.GetCell()
	n := len(s.nodes)
	s.mu.Unlock()

	if err := stream.SendHeader(metadata.Pairs("workpod-registered", req.GetNodeId())); err != nil {
		return err
	}
	log.Printf("node %s (cell %s) registered; pulling begins — %d node(s), memory pressure avg10 %.2f",
		req.GetNodeId(), req.GetCell(), n, req.GetMemoryPressureAvg10())

	<-stream.Context().Done()
	log.Printf("node %s stopped pulling", req.GetNodeId())
	return nil
}

// SendHeartbeat accepts the worker's heartbeats. Halt notices ride the return stream once the
// halt file exists (AP-3.6); until then there is nothing truthful to send.
func (s *server) SendHeartbeat(stream grpc.BidiStreamingServer[workpodv1.Heartbeat, workpodv1.HaltNotice]) error {
	for {
		if _, err := stream.Recv(); err != nil {
			return nil
		}
	}
}

func notBuilt(what, ap string) error {
	return status.Errorf(codes.Unimplemented, "%s is %s's to build — this plane refuses rather than pretends (Q-02)", what, ap)
}

// Enroll turns a single-use token into a certificate with role and cell in the name — AP-6.1's
// flow (B-01). Refusing here is what keeps `all` and `control` honest too: they register without
// a token because they are the plane (SP-A04-1), not because enrollment quietly waves nodes
// through.
func (s *server) Enroll(_ context.Context, _ *workpodv1.EnrollRequest) (*workpodv1.EnrollResponse, error) {
	return nil, notBuilt("enrollment (B-01: token → certificate with role and cell in the name)", "AP-6.1")
}

func (s *server) SubmitEnvelope(_ context.Context, _ *workpodv1.Envelope) (*workpodv1.EnvelopeAck, error) {
	return nil, notBuilt("intake (T-01: envelope → job with an idempotency key)", "AP-3.2")
}

func (s *server) SubmitReport(_ context.Context, _ *workpodv1.Report) (*workpodv1.ReportAck, error) {
	return nil, notBuilt("reports (there are no jobs before the adapter exists)", "AP-3.2")
}

func (s *server) PublishEvent(_ context.Context, _ *workpodv1.Event) (*workpodv1.EventAck, error) {
	return nil, notBuilt("events back into the channels (T-02)", "AP-3.2")
}
