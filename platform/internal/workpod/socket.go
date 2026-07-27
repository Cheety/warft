package workpod

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	workpodv1 "github.com/Cheety/warft/platform/api/workpodv1"
	"github.com/Cheety/warft/platform/internal/runner"
)

// harnessSocket is SP-T04-2's "the only way out is a Unix socket". One per pod, on the host side of
// a bind mount, serving the Harness service of contract/platform.proto — the same interface schema
// as everything else (SP-E10-1), spoken over a file instead of over a port.
//
// A socket per pod rather than one for all of them, because the socket *is* the pod's identity here:
// the server knows which job it is talking to from which file the connection came in on, and never
// from anything the pod says about itself. That is the same rule intake lives by (SP-T01-9) — the
// authority comes from the channel, never from the message.
type harnessSocket struct {
	workpodv1.UnimplementedHarnessServer

	job     runner.Job
	gapsDir string
	srv     *grpc.Server
	lis     net.Listener

	mu   sync.Mutex
	last time.Time
}

// serveHarness opens the pod's socket and serves on it until the pod is reaped.
func (w *Workpod) serveHarness(podID string, job runner.Job, path string) (*harnessSocket, error) {
	_ = os.Remove(path)
	lis, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("the pod's only way out could not be opened at %s: %w", path, err)
	}
	h := &harnessSocket{job: job, gapsDir: filepath.Join(w.Store.Var, "gaps"), lis: lis}
	h.srv = grpc.NewServer()
	workpodv1.RegisterHarnessServer(h.srv, h)
	go func() {
		if err := h.srv.Serve(lis); err != nil {
			w.logf("pod %s: the harness socket ended: %v", podID, err)
		}
	}()
	return h, nil
}

func (h *harnessSocket) close() {
	if h == nil {
		return
	}
	h.srv.Stop()
	_ = h.lis.Close()
}

// lastCallAfter reports whether the pod called out since the given moment. The quiet detector of
// SP-T04-3 needs both halves — a pod blocked on a model response spends no CPU and is not idle.
func (h *harnessSocket) lastCallAfter(t time.Time) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.last.After(t)
}

func (h *harnessSocket) touch() {
	h.mu.Lock()
	h.last = time.Now()
	h.mu.Unlock()
}

// QueryFacts is E-09's fact store, which does not exist yet. It refuses by the name of the work
// package that builds it, the way the binary's unbuilt entry points do: a pod that got an empty
// result would take "no facts" for an answer instead of for a missing component.
func (h *harnessSocket) QueryFacts(ctx context.Context, q *workpodv1.FactQuery) (*workpodv1.FactResult, error) {
	h.touch()
	return nil, status.Errorf(codes.Unimplemented,
		"the fact store is AP-4.1's — SCIP, Parquet and DuckDB in the harness (E-09, SP-E09-3)")
}

// EnqueueEffect is K-03's outbox. The pod never acts itself, and until the outbox and the two gates
// exist (AP-3.5) there is nothing to hand an effect to. Refusing is the honest answer; accepting and
// dropping it would be the double-push this platform has a whole panel about.
func (h *harnessSocket) EnqueueEffect(ctx context.Context, e *workpodv1.OutboxEntry) (*workpodv1.EffectAck, error) {
	h.touch()
	return nil, status.Errorf(codes.Unimplemented,
		"the outbox and both gates are AP-3.5's — a pod's effect is recorded before it is executed (K-03)")
}

// ReportGap is F-05's "stopping is a usable result", and it works. It is written to /var on the
// host, outside the pod: SP-T04-2 says no logs in the pod, and a gap the pod kept would be a gap
// that died with it.
func (h *harnessSocket) ReportGap(ctx context.Context, g *workpodv1.GapReport) (*workpodv1.EffectAck, error) {
	h.touch()
	if err := os.MkdirAll(h.gapsDir, 0o755); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	record := struct {
		OrderID       string    `json:"order_id"`
		Attempt       uint32    `json:"attempt"`
		At            time.Time `json:"at"`
		MissingFacts  []string  `json:"missing_facts,omitempty"`
		MissingSkills []string  `json:"missing_skills,omitempty"`
		FossilCount   uint32    `json:"fossil_count,omitempty"`
		SplitProposal string    `json:"split_proposal,omitempty"`
	}{
		OrderID: h.job.OrderID, Attempt: h.job.Attempt, At: time.Now().UTC(),
		MissingFacts: g.GetMissingFacts(), MissingSkills: g.GetMissingSkills(),
		FossilCount: g.GetFossilCount(), SplitProposal: g.GetSplitProposal(),
	}
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	path := filepath.Join(h.gapsDir, PodID(h.job)+".json")
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	return &workpodv1.EffectAck{Accepted: true}, nil
}
