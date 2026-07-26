// Package worker is the `register` step of the A-04 start sequence, and the work layer's agent
// after it: the node asks the control plane for capacity and pulling begins (SP-A04-2, V-02).
//
// The register step requires the selftest marker. The unit graph already refuses to start this
// without a passed selftest — the check here is the same rule in the binary, so that neither
// guard depends on the other standing (SP-A04-3).
package worker

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	workpodv1 "github.com/Cheety/warft/platform/api/workpodv1"
	"github.com/Cheety/warft/platform/internal/boot"
	"github.com/Cheety/warft/platform/internal/cgroup"
)

// WorkSlice is where the work layer lives on every node (SP-V01-1); the pressure reported with
// each capacity request is read there (SP-RC-1).
const WorkSlice = "workpod-work.slice"

// registerWait bounds how long the node waits for the plane to acknowledge the registration.
const registerWait = 30 * time.Second

// Run is `workpod worker`.
func Run(v boot.Values) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if _, err := os.Stat(boot.SelftestMarker); err != nil {
		return fmt.Errorf("no selftest marker at %s — a failed selftest means: do not enroll (SP-A04-3)", boot.SelftestMarker)
	}

	nodeID, err := ensureNodeID()
	if err != nil {
		return err
	}

	stream, closeConn, err := register(v, nodeID)
	if err != nil {
		return err
	}
	defer closeConn()

	// Pulling begins: the stream is the ferry's near end. Leases arrive on it once jobs exist
	// (AP-3.2); until then holding it open is the pull.
	for {
		if _, err := stream.Recv(); err != nil {
			return fmt.Errorf("the pull stream ended: %w", err)
		}
		// A lease before AP-3.2 built intake would be a job from nowhere.
		return fmt.Errorf("received a lease, but nothing can run one before AP-3.3 — refusing it")
	}
}

// Ping is `workpod ping` — one round trip over the same path registering takes. It exists so a
// probe can ask "is the plane still answering?" with the pull path itself instead of a synthetic
// health endpoint (AB-RC-4's "access operable").
func Ping(addr, cell string, deadline time.Duration) error {
	v := boot.Values{Control: addr, Cell: cell}
	if v.Control == "" || v.Cell == "" {
		b := boot.Read()
		if v.Control == "" {
			v.Control = b.Control
		}
		if v.Cell == "" {
			v.Cell = b.Cell
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	conn, err := dial(v.Control)
	if err != nil {
		return err
	}
	defer conn.Close()

	start := time.Now()
	stream, err := workpodv1.NewControlPlaneClient(conn).RequestCapacity(ctx, &workpodv1.CapacityRequest{
		NodeId: "probe-" + randomHex(4),
		Cell:   v.Cell,
	})
	if err != nil {
		return err
	}
	if _, err := stream.Header(); err != nil {
		return fmt.Errorf("the plane did not acknowledge within %s: %w", deadline, err)
	}
	fmt.Printf("pong from %s in %s\n", v.Control, time.Since(start).Round(time.Millisecond))
	return nil
}

func register(v boot.Values, nodeID string) (grpc.ServerStreamingClient[workpodv1.Lease], func(), error) {
	conn, err := dial(v.Control)
	if err != nil {
		return nil, nil, err
	}

	// Retried inside the register window: on `role = all` the plane starts in the same
	// transaction as the worker, ordered but not readiness-gated, so the first request can land
	// before the listener is up. Registering is an upsert on the plane's side, which is what
	// makes the retry safe. The window is bounded, because "registering" that never completes is
	// a state A-04 does not have.
	var stream grpc.ServerStreamingClient[workpodv1.Lease]
	deadline := time.Now().Add(registerWait)
	for {
		psi := cgroup.Pressure(WorkSlice)
		attemptCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		s, err := workpodv1.NewControlPlaneClient(conn).RequestCapacity(context.Background(), &workpodv1.CapacityRequest{
			NodeId:              nodeID,
			Cell:                v.Cell,
			LocalityGroup:       v.LocalityGroup,
			MemoryPressureAvg10: psi.MemorySomeAvg10,
			CpuPressureAvg60:    psi.CPUSomeAvg60,
			IoPressureAvg10:     psi.IOFullAvg10,
		})
		if err == nil {
			// Header() honors its own context; the bounded one only guards this attempt.
			type ack struct{ err error }
			done := make(chan ack, 1)
			go func() { _, herr := s.Header(); done <- ack{herr} }()
			select {
			case a := <-done:
				err = a.err
			case <-attemptCtx.Done():
				err = attemptCtx.Err()
			}
			if err == nil {
				cancel()
				stream = s
				break
			}
		}
		cancel()
		if time.Now().After(deadline) {
			conn.Close()
			return nil, nil, fmt.Errorf("the control plane at %s did not accept the registration within %s: %w", v.Control, registerWait, err)
		}
		time.Sleep(time.Second)
	}

	if err := os.MkdirAll(boot.RunDir, 0o755); err != nil {
		conn.Close()
		return nil, nil, err
	}
	body := fmt.Sprintf("node_id=%s\ncell=%s\nregistered_at=%s\n", nodeID, v.Cell, time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(boot.RegisteredMarker, []byte(body), 0o644); err != nil {
		conn.Close()
		return nil, nil, err
	}
	fmt.Printf("registered as %s (cell %s) with %s; pulling begins (SP-A04-2)\n", nodeID, v.Cell, v.Control)
	return stream, func() { conn.Close() }, nil
}

func dial(addr string) (*grpc.ClientConn, error) {
	if err := requireLoopback(addr); err != nil {
		return nil, err
	}
	return grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

// requireLoopback keeps the plaintext stopgap on the machine. A worker on its own node talks to
// the plane across the network — that conversation carries an authority and needs the certificate
// name of contract/identity.md on both ends, which enrollment wires in AP-6.1.
func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("control address %q: %w", addr, err)
	}
	ip := net.ParseIP(host)
	if host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return nil
	}
	return fmt.Errorf("control address %q is not loopback — plaintext does not cross a machine boundary; enrollment and mTLS are AP-6.1's (contract/identity.md)", addr)
}

// ensureNodeID reads or mints this node's identity. UUID v7 like every identifier a producer
// mints here (SP-K01-2): time-ordered, random tail, lowercase — which also satisfies the
// [a-z0-9-]+ grammar of contract/identity.md.
func ensureNodeID() (string, error) {
	if b, err := os.ReadFile(boot.NodeIDFile); err == nil {
		id := string(b)
		if len(id) > 0 {
			return trimNewline(id), nil
		}
	}
	id := uuidV7()
	if err := os.MkdirAll(filepath.Dir(boot.NodeIDFile), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(boot.NodeIDFile, []byte(id+"\n"), 0o644); err != nil {
		return "", err
	}
	return id, nil
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func uuidV7() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	ms := uint64(time.Now().UnixMilli())
	binary.BigEndian.PutUint64(b[:8], ms<<16|uint64(binary.BigEndian.Uint16(b[6:8])))
	b[6] = (b[6] & 0x0f) | 0x70 // version 7
	b[8] = (b[8] & 0x3f) | 0x80 // variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}
