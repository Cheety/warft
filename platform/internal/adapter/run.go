// `workpod adapter` — the command surface of the CLI adapter.
//
// The three sub-commands are the contract's four methods, made visible from the artifact:
// `identity` is identity(), `capabilities` is capabilities(), and `submit` runs receive() to shape
// the invocation and respond() to render what came back. There is no fifth thing an adapter does.

package adapter

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	workpodv1 "github.com/Cheety/warft/platform/api/workpodv1"
	"github.com/Cheety/warft/platform/internal/attachment"
	"github.com/Cheety/warft/platform/internal/boot"
	"github.com/Cheety/warft/platform/internal/ids"
)

// Run is `workpod adapter <command> [flags]`.
func Run(args []string, v boot.Values, out io.Writer) error {
	if len(args) == 0 {
		return usage()
	}
	switch args[0] {
	case "submit":
		return submit(args[1:], v, out)
	case "identity":
		return report(args[1:], out, func(c *CLI) (string, error) { return c.Identity() })
	case "capabilities":
		return report(args[1:], out, func(c *CLI) (string, error) {
			caps := c.Capabilities()
			limit := "unbounded"
			if caps.CharacterLimit > 0 {
				limit = fmt.Sprintf("%d", caps.CharacterLimit)
			}
			return fmt.Sprintf("channel         %s\nthreads         %t\nattachments     %t\nbuttons         %t\ncharacter_limit %s",
				Channel, caps.Threads, caps.Attachments, caps.Buttons, limit), nil
		})
	default:
		return usage()
	}
}

func usage() error {
	return fmt.Errorf(`workpod adapter <command>

  submit         shape an invocation into an envelope and hand it to intake (T-01)
  identity       the external identifier this device certificate stands for
  capabilities   what this channel declares about itself`)
}

// report is identity() and capabilities(): both need the certificate and nothing else.
func report(args []string, out io.Writer, f func(*CLI) (string, error)) error {
	fs := flag.NewFlagSet("adapter", flag.ContinueOnError)
	cert := fs.String("device-cert", DefaultDeviceCert, "the device certificate this channel is confidential by")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := NewCLI(*cert, "")
	if err != nil {
		return err
	}
	s, err := f(c)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, s)
	return nil
}

// multi is a flag that may be given more than once — acceptance criteria, assumptions, bounds and
// attachments are lists, and a list given as one comma-separated string cannot hold a statement
// with a comma in it.
type multi []string

func (m *multi) String() string     { return strings.Join(*m, ", ") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func submit(args []string, v boot.Values, out io.Writer) error {
	fs := flag.NewFlagSet("adapter submit", flag.ContinueOnError)

	control := fs.String("control", v.Control, "the control plane's address")
	cert := fs.String("device-cert", DefaultDeviceCert, "the device certificate this channel is confidential by")
	store := fs.String("store", attachment.DefaultRoot, "the content-addressed attachment store")
	deadline := fs.Duration("deadline", 30*time.Second, "how long to wait for intake")

	cell := fs.String("cell", v.Cell, "the cell (SP-K01-3)")
	project := fs.String("project", "", "the project (SP-K01-4)")
	messageID := fs.String("message-id", "", "this channel's message identity; the idempotency key (SP-T01-7)")
	text := fs.String("text", "", "the message; data, never instruction (SP-T01-9)")
	textFile := fs.String("text-file", "", "read the message from a file instead")
	thread := fs.String("thread", "", "the thread reference; a thread equals a project")
	platform := fs.String("platform", "", "the runner pool: alpine · windows · macos · remote")
	var attach multi
	fs.Var(&attach, "attach", "PATH:MEDIA-TYPE, repeatable; checked against OP-5 at intake")

	// The job, by hand — decisions/jobs-by-hand.md. Absent --goal, the envelope is stored and no
	// job is created: no acceptance criterion, no job (SP-Q01-6).
	goal := fs.String("goal", "", "the goal, stated by hand until Q-01 derives it (AP-5.1)")
	var criteria, assumptions, repos, paths, environments multi
	fs.Var(&criteria, "acceptance", "an acceptance criterion, repeatable; without one there is no job (SP-Q01-6)")
	fs.Var(&assumptions, "assumption", "an assumption, repeatable; objects, not prose (SP-Q01-5)")
	fs.Var(&repos, "repo", "a repository inside the bounds, repeatable")
	fs.Var(&paths, "path", "a path inside the bounds, repeatable")
	fs.Var(&environments, "environment", "an environment inside the bounds, repeatable")
	evidence := fs.String("evidence", "tests.new", "the evidence class every acceptance criterion demands")
	risk := fs.String("risk", "", "reversible · costly · irreversible (Q-01)")
	podMinutes := fs.Uint64("budget-pod-minutes", 0, "the pod-minute pot (V-04)")
	tokens := fs.Uint64("budget-tokens", 0, "the token pot (V-04)")
	money := fs.Uint64("budget-money-micros", 0, "the money pot in micros (V-04)")
	class := fs.String("class", "", "tiny · small · medium · large — the captain's `size` step, by hand")
	imageHash := fs.String("image-hash", "", "the container image (T-03), by hand until AP-4.2 resolves it")
	pipelineVersion := fs.String("pipeline-version", "", "pipeline@version (T-05)")
	localityGroup := fs.String("locality-group", "", "the locality group (V-02)")
	priority := fs.String("priority", "batch", "interactive · batch · maintenance · background")

	if err := fs.Parse(args); err != nil {
		return err
	}

	body := *text
	if *textFile != "" {
		if body != "" {
			return fmt.Errorf("--text and --text-file both name the message; give one")
		}
		b, err := os.ReadFile(*textFile)
		if err != nil {
			return err
		}
		body = string(b)
	}

	cands, err := parseAttachments(attach)
	if err != nil {
		return err
	}

	byHand, err := handWritten(*goal, criteria, assumptions, repos, paths, environments,
		*evidence, *risk, *class, *imageHash, *pipelineVersion, *localityGroup, *priority,
		*podMinutes, *tokens, *money)
	if err != nil {
		return err
	}

	c, err := NewCLI(*cert, *store)
	if err != nil {
		return err
	}
	env, err := c.Receive(Invocation{
		Cell:        *cell,
		Project:     *project,
		MessageID:   *messageID,
		Text:        body,
		Thread:      *thread,
		Attachments: cands,
		Platform:    *platform,
		ByHand:      byHand,
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *deadline)
	defer cancel()

	conn, err := dial(*control)
	if err != nil {
		return err
	}
	defer conn.Close()

	ack, err := workpodv1.NewControlPlaneClient(conn).SubmitEnvelope(ctx, env)
	if err != nil {
		return err
	}

	// respond(): what came back, rendered into the language of the channel. The detail carries
	// the one fact this work package exists for — whether the message had been seen before.
	detail := "envelope " + env.GetId()
	switch {
	case ack.GetDeduplicated():
		detail = "the same message was already delivered; this is the job it produced then (SP-T01-7)"
	case ack.GetOrderId() == "":
		detail = "envelope " + env.GetId() + " stored; no acceptance criterion, no job (SP-Q01-6)"
	case !ack.GetAdmitted():
		// V-04's refusal, rendered into the channel it came from. The options travel with it: a
		// pot that ran out answers with what can be done about it, never with a truncated result
		// (SP-V04-2).
		detail = "not admitted — " + ack.GetRefusal()
		for _, o := range ack.GetOptions() {
			detail += "\n  · " + o
		}
	}
	line, err := c.Respond(&workpodv1.Event{
		Id:        ids.New(),
		Project:   env.GetProject(),
		OrderId:   ack.GetOrderId(),
		Kind:      workpodv1.Event_ACCEPTED,
		Detail:    detail,
		Verbosity: 1,
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(out, line)
	return nil
}

// parseAttachments reads PATH:MEDIA-TYPE. Split at the last colon, because a path may hold one and
// a media type may not.
func parseAttachments(specs multi) ([]attachment.Candidate, error) {
	out := make([]attachment.Candidate, 0, len(specs))
	for _, s := range specs {
		i := strings.LastIndex(s, ":")
		if i <= 0 || i == len(s)-1 {
			return nil, fmt.Errorf("--attach %q: expected PATH:MEDIA-TYPE", s)
		}
		out = append(out, attachment.Candidate{Path: s[:i], MediaType: s[i+1:]})
	}
	return out, nil
}

// handWritten assembles what a captain would decide and Q-01 would derive. It is all or nothing:
// a goal without the rest is an intent contract with holes in it, and the state contract has no
// place to put a hole.
func handWritten(goal string, criteria, assumptions, repos, paths, environments multi,
	evidence, risk, class, imageHash, pipelineVersion, localityGroup, priority string,
	podMinutes, tokens, money uint64) (*workpodv1.HandWrittenJob, error) {

	if goal == "" {
		// No job asked for. The envelope is still an envelope; intake stores it and creates
		// nothing (SP-Q01-6).
		if len(criteria) > 0 || class != "" || imageHash != "" {
			return nil, fmt.Errorf("--acceptance, --class and --image-hash describe a job; --goal is what asks for one")
		}
		return nil, nil
	}
	if len(criteria) == 0 {
		return nil, fmt.Errorf("no acceptance criterion, no job (SP-Q01-6); give at least one --acceptance")
	}

	ev, err := parseEvidence(evidence)
	if err != nil {
		return nil, err
	}
	rk, err := parseRisk(risk)
	if err != nil {
		return nil, err
	}
	cl, err := parseClass(class)
	if err != nil {
		return nil, err
	}
	pr, err := parsePriority(priority)
	if err != nil {
		return nil, err
	}
	for name, value := range map[string]string{
		"--image-hash":       imageHash,
		"--pipeline-version": pipelineVersion,
		"--locality-group":   localityGroup,
	} {
		if value == "" {
			return nil, fmt.Errorf("%s is what a captain would decide and there is none yet (E-11 step 3); state it", name)
		}
	}

	acc := make([]*workpodv1.Acceptance, 0, len(criteria))
	for _, s := range criteria {
		acc = append(acc, &workpodv1.Acceptance{
			Id:               ids.New(),
			Statement:        s,
			RequiredEvidence: ev,
			// Q-02: a criterion a machine can check is the only kind this channel can state.
			// A human acceptance is a fact recorded elsewhere (HumanAcceptance), not a flag.
			MachineCheckable: true,
		})
	}
	asm := make([]*workpodv1.Assumption, 0, len(assumptions))
	for _, s := range assumptions {
		asm = append(asm, &workpodv1.Assumption{Id: ids.New(), Statement: s})
	}

	return &workpodv1.HandWrittenJob{
		Spec: &workpodv1.Spec{
			// id, version and envelope_id are intake's: the spec is version 1 of a new
			// specification, and the envelope it belongs to is the one being submitted.
			Goal:        goal,
			Acceptance:  acc,
			Assumptions: asm,
			Bounds: &workpodv1.Bounds{
				Repos:        repos,
				Paths:        paths,
				Environments: environments,
			},
			Budget: &workpodv1.Budget{
				PodMinutes:  podMinutes,
				Tokens:      tokens,
				MoneyMicros: money,
			},
			RiskClass: rk,
		},
		Class:           cl,
		ImageHash:       imageHash,
		PipelineVersion: pipelineVersion,
		LocalityGroup:   localityGroup,
		Priority:        pr,
		// The reservation against this share is AP-3.6's; stating it is the human's.
		BudgetShare: &workpodv1.Budget{PodMinutes: podMinutes, Tokens: tokens, MoneyMicros: money},
	}, nil
}

// The four enumerations are spelled here exactly as contract/schema.sql spells them, so a flag and
// a row read the same and neither needs a translation table to be understood.

func parseEvidence(s string) (workpodv1.EvidenceClass, error) {
	m := map[string]workpodv1.EvidenceClass{
		"artifact.identical": workpodv1.EvidenceClass_ARTIFACT_IDENTICAL,
		"types.lint":         workpodv1.EvidenceClass_TYPES_LINT,
		"tests.existing":     workpodv1.EvidenceClass_TESTS_EXISTING,
		"tests.new":          workpodv1.EvidenceClass_TESTS_NEW,
		"mutation.diff":      workpodv1.EvidenceClass_MUTATION_DIFF,
		"review.independent": workpodv1.EvidenceClass_REVIEW_INDEPENDENT,
		"human":              workpodv1.EvidenceClass_HUMAN,
	}
	if v, ok := m[s]; ok {
		return v, nil
	}
	return 0, fmt.Errorf("--evidence %q is none of the seven classes of Q-02", s)
}

func parseRisk(s string) (workpodv1.Reversibility, error) {
	m := map[string]workpodv1.Reversibility{
		"reversible":   workpodv1.Reversibility_REVERSIBLE,
		"costly":       workpodv1.Reversibility_COSTLY,
		"irreversible": workpodv1.Reversibility_IRREVERSIBLE,
	}
	if v, ok := m[s]; ok {
		return v, nil
	}
	return 0, fmt.Errorf("--risk %q is none of reversible, costly, irreversible (Q-01 clarifies by reversibility)", s)
}

func parseClass(s string) (workpodv1.ResourceClass, error) {
	m := map[string]workpodv1.ResourceClass{
		"tiny":   workpodv1.ResourceClass_TINY,
		"small":  workpodv1.ResourceClass_SMALL,
		"medium": workpodv1.ResourceClass_MEDIUM,
		"large":  workpodv1.ResourceClass_LARGE,
	}
	if v, ok := m[s]; ok {
		return v, nil
	}
	return 0, fmt.Errorf("--class %q is none of the four of R-A", s)
}

func parsePriority(s string) (workpodv1.Priority, error) {
	m := map[string]workpodv1.Priority{
		"interactive": workpodv1.Priority_INTERACTIVE,
		"batch":       workpodv1.Priority_BATCH,
		"maintenance": workpodv1.Priority_MAINTENANCE,
		"background":  workpodv1.Priority_BACKGROUND,
	}
	if v, ok := m[s]; ok {
		return v, nil
	}
	return 0, fmt.Errorf("--priority %q is none of the four of R-B", s)
}

// dial holds the same bound the control plane's listener and the worker's client hold: plaintext
// stays on the machine until a connection can state its role and cell instead of claiming them
// (contract/identity.md, AP-6.1).
func dial(addr string) (*grpc.ClientConn, error) {
	if addr == "" {
		return nil, fmt.Errorf("no control address: --control, or the `control` boot value (SP-A04-1)")
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("control address %q: %w", addr, err)
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("control address %q is not loopback — this channel carries an authority and crossing machines needs the certificate name of contract/identity.md (AP-6.1)", addr)
	}
	return grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
}
