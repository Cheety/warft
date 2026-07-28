// Intake — SubmitEnvelope, the RPC the contract has always answered with an order id (T-01, K-01).
//
// This file is where the wire meets the state contract. The proto's enumerations are numbers and
// the schema's are words, and the translation lives here on purpose: `internal/statedb` is a step
// and knows only the database, `api/workpodv1` is the contract and reaches back into nothing, so
// the role between them is the only place that may know both (decisions/module-dependencies.md).

package controlplane

import (
	"context"
	"encoding/json"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	workpodv1 "github.com/Cheety/warft/platform/api/workpodv1"
	"github.com/Cheety/warft/platform/internal/attachment"
	"github.com/Cheety/warft/platform/internal/statedb"
)

// SubmitEnvelope is intake. The envelope becomes state; if it carries a job stated by hand it
// becomes one job, and a redelivery of the same message becomes none (SP-T01-7).
func (s *server) SubmitEnvelope(ctx context.Context, env *workpodv1.Envelope) (*workpodv1.EnvelopeAck, error) {
	pool := s.pool()
	if pool == nil {
		return nil, status.Error(codes.Unavailable, "the state database is not ready — intake writes state or refuses; it never accepts into nothing")
	}
	e, err := toStateDB(env, s.policy)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	res, err := statedb.Submit(ctx, pool, e)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "intake: %v", err)
	}
	logIntake(e, res)
	ack := &workpodv1.EnvelopeAck{OrderId: res.OrderID, Deduplicated: res.Deduplicated}
	if res.OrderID != "" {
		// V-04: the pots are reserved at admission, and admission is here — the same call the job
		// was created in. A redelivery finds its order already admitted and reserves nothing a
		// second time (SP-T01-7).
		s.admitAtIntake(ctx, pool, e.Cell, res.OrderID, ack)
	}
	return ack, nil
}

// toStateDB validates the envelope and translates it. The checks here are the ones the platform
// owes whoever is on the other end of the wire: an adapter runs as a workload of its own from
// AP-5.7 (SP-T01-3), and a plane that trusted an adapter's arithmetic would be trusting a workload.
func toStateDB(env *workpodv1.Envelope, policy attachment.Policy) (statedb.Envelope, error) {
	var zero statedb.Envelope
	for name, value := range map[string]string{
		"id":                 env.GetId(),
		"cell":               env.GetCell(),
		"project":            env.GetProject(),
		"channel":            env.GetChannel(),
		"channel_message_id": env.GetChannelMessageId(),
		"sender_external":    env.GetSenderExternal(),
	} {
		if value == "" {
			return zero, fmt.Errorf("an envelope carries its %s", name)
		}
	}
	if env.GetIdempotency() == "" {
		return zero, fmt.Errorf("every envelope needs an idempotency key (SP-T01-7)")
	}
	authority, err := authorityLevel(env.GetAuthority())
	if err != nil {
		return zero, err
	}

	// OP-5 again, on the metadata this side has. The adapter checked the bytes at intake; the
	// plane cannot re-read them and does not pretend to — it re-checks what a wire message can
	// misstate: the type, the size, the count and the total.
	sizes := make([]int64, 0, len(env.GetAttachments()))
	atts := make([]statedb.Attachment, 0, len(env.GetAttachments()))
	for _, a := range env.GetAttachments() {
		size := int64(a.GetSizeBytes())
		if err := policy.CheckMetadata(a.GetMediaType(), size); err != nil {
			return zero, err
		}
		if a.GetContentHash() == "" {
			return zero, fmt.Errorf("an attachment is carried as a reference, and this one has none")
		}
		sizes = append(sizes, size)
		atts = append(atts, statedb.Attachment{
			ContentHash: a.GetContentHash(),
			MediaType:   a.GetMediaType(),
			SizeBytes:   size,
		})
	}
	if err := policy.CheckEnvelope(sizes); err != nil {
		return zero, err
	}

	received := env.GetReceivedAt()
	if received == nil {
		received = timestamppb.Now()
	}

	e := statedb.Envelope{
		ID:             env.GetId(),
		Cell:           env.GetCell(),
		Project:        env.GetProject(),
		Channel:        env.GetChannel(),
		MessageID:      env.GetChannelMessageId(),
		SenderExternal: env.GetSenderExternal(),
		Authority:      authority,
		Text:           env.GetText(),
		Attachments:    atts,
		Thread:         env.GetThread(),
		ReceivedAt:     received.AsTime(),
		Idempotency:    env.GetIdempotency(),
		Platform:       env.GetPlatform(),
	}
	if e.Platform == "" {
		e.Platform = "alpine"
	}

	if env.GetByHand() != nil {
		job, err := toJob(env.GetByHand())
		if err != nil {
			return zero, err
		}
		e.Job = job
	}
	return e, nil
}

func toJob(h *workpodv1.HandWrittenJob) (*statedb.Job, error) {
	spec := h.GetSpec()
	if spec == nil {
		return nil, fmt.Errorf("a job stated by hand states its spec (decisions/jobs-by-hand.md)")
	}
	if spec.GetGoal() == "" {
		return nil, fmt.Errorf("a spec states a goal")
	}
	if len(spec.GetAcceptance()) == 0 {
		return nil, fmt.Errorf("no acceptance criterion, no job (SP-Q01-6)")
	}
	risk, err := reversibility(spec.GetRiskClass())
	if err != nil {
		return nil, err
	}
	class, err := resourceClass(h.GetClass())
	if err != nil {
		return nil, err
	}
	prio, err := priority(h.GetPriority())
	if err != nil {
		return nil, err
	}
	for name, value := range map[string]string{
		"image_hash":       h.GetImageHash(),
		"pipeline_version": h.GetPipelineVersion(),
		"locality_group":   h.GetLocalityGroup(),
	} {
		if value == "" {
			return nil, fmt.Errorf("a job stated by hand states its %s (E-11 step 3)", name)
		}
	}

	acc := make([]statedb.Acceptance, 0, len(spec.GetAcceptance()))
	for _, a := range spec.GetAcceptance() {
		ev, err := evidenceClass(a.GetRequiredEvidence())
		if err != nil {
			return nil, err
		}
		if a.GetId() == "" || a.GetStatement() == "" {
			return nil, fmt.Errorf("an acceptance criterion is an object with a statement (SP-Q01-6)")
		}
		acc = append(acc, statedb.Acceptance{
			ID:               a.GetId(),
			Statement:        a.GetStatement(),
			RequiredEvidence: ev,
			MachineCheckable: a.GetMachineCheckable(),
		})
	}
	asm := make([]statedb.Assumption, 0, len(spec.GetAssumptions()))
	for _, a := range spec.GetAssumptions() {
		if a.GetId() == "" || a.GetStatement() == "" {
			return nil, fmt.Errorf("an assumption is an object, not prose (SP-Q01-5)")
		}
		asm = append(asm, statedb.Assumption{ID: a.GetId(), Statement: a.GetStatement()})
	}

	bounds, err := boundsJSON(spec.GetBounds())
	if err != nil {
		return nil, err
	}
	budget, err := budgetJSON(spec.GetBudget())
	if err != nil {
		return nil, err
	}
	share, err := budgetJSON(h.GetBudgetShare())
	if err != nil {
		return nil, err
	}

	return &statedb.Job{
		Goal:            spec.GetGoal(),
		Acceptance:      acc,
		Assumptions:     asm,
		BoundsJSON:      bounds,
		BudgetJSON:      budget,
		RiskClass:       risk,
		Class:           class,
		ImageHash:       h.GetImageHash(),
		PipelineVersion: h.GetPipelineVersion(),
		LocalityGroup:   h.GetLocalityGroup(),
		Priority:        prio,
		BudgetShareJSON: share,
	}, nil
}

func boundsJSON(b *workpodv1.Bounds) (string, error) {
	// The names are the proto's own, so a bounds column read back beside a proto message needs no
	// glossary between them.
	v := map[string][]string{
		"repos":        b.GetRepos(),
		"paths":        b.GetPaths(),
		"environments": b.GetEnvironments(),
	}
	out, err := json.Marshal(v)
	return string(out), err
}

func budgetJSON(b *workpodv1.Budget) (string, error) {
	v := map[string]uint64{
		"pod_minutes":  b.GetPodMinutes(),
		"tokens":       b.GetTokens(),
		"money_micros": b.GetMoneyMicros(),
	}
	out, err := json.Marshal(v)
	return string(out), err
}

// The four translations below turn a proto enumeration into the word contract/schema.sql uses.
// UNSPECIFIED is refused rather than defaulted: a zero value that becomes a resource class is a
// decision nobody made.

func authorityLevel(a workpodv1.AuthorityLevel) (string, error) {
	switch a {
	case workpodv1.AuthorityLevel_PUBLIC:
		return "public", nil
	case workpodv1.AuthorityLevel_LINKED:
		return "linked", nil
	case workpodv1.AuthorityLevel_CONFIDENTIAL:
		return "confidential", nil
	}
	return "", fmt.Errorf("the authority comes from the channel and this envelope names none (SP-T01-4)")
}

func reversibility(r workpodv1.Reversibility) (string, error) {
	switch r {
	case workpodv1.Reversibility_REVERSIBLE:
		return "reversible", nil
	case workpodv1.Reversibility_COSTLY:
		return "costly", nil
	case workpodv1.Reversibility_IRREVERSIBLE:
		return "irreversible", nil
	}
	return "", fmt.Errorf("a spec is clarified by reversibility, and this one states none (SP-Q01-3)")
}

func resourceClass(c workpodv1.ResourceClass) (string, error) {
	switch c {
	case workpodv1.ResourceClass_TINY:
		return "tiny", nil
	case workpodv1.ResourceClass_SMALL:
		return "small", nil
	case workpodv1.ResourceClass_MEDIUM:
		return "medium", nil
	case workpodv1.ResourceClass_LARGE:
		return "large", nil
	}
	return "", fmt.Errorf("a job carries one of R-A's four resource classes")
}

func priority(p workpodv1.Priority) (string, error) {
	switch p {
	case workpodv1.Priority_INTERACTIVE:
		return "interactive", nil
	case workpodv1.Priority_BATCH:
		return "batch", nil
	case workpodv1.Priority_MAINTENANCE:
		return "maintenance", nil
	case workpodv1.Priority_BACKGROUND:
		return "background", nil
	}
	return "", fmt.Errorf("a job carries one of R-B's four priorities")
}

func evidenceClass(e workpodv1.EvidenceClass) (string, error) {
	switch e {
	case workpodv1.EvidenceClass_ARTIFACT_IDENTICAL:
		return "artifact.identical", nil
	case workpodv1.EvidenceClass_TYPES_LINT:
		return "types.lint", nil
	case workpodv1.EvidenceClass_TESTS_EXISTING:
		return "tests.existing", nil
	case workpodv1.EvidenceClass_TESTS_NEW:
		return "tests.new", nil
	case workpodv1.EvidenceClass_MUTATION_DIFF:
		return "mutation.diff", nil
	case workpodv1.EvidenceClass_REVIEW_INDEPENDENT:
		return "review.independent", nil
	case workpodv1.EvidenceClass_HUMAN:
		return "human", nil
	}
	return "", fmt.Errorf("an acceptance criterion names the evidence it demands (Q-02)")
}
