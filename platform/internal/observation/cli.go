// `workpod observe <subcommand>` — B-03 from the outside.
//
// The subcommands exist for the reason `workpod scheduler pressure` and `workpod control admit` do:
// a row of the acceptance matrix has to be provable by running the program. A trace nobody can
// print is a trace nobody can check, and "reconstructible in one query" is a claim about a
// statement somebody can read and run (SP-K01-7).
//
// Two of them are seams rather than displays. `observe import` folds the report `workpod pod run`
// printed into the trace — which is the actual join between T-05's phase log and B-03's spans, and
// the reason a trace is a record of a run rather than something typed in afterwards. `observe
// rejections --journal` folds the egress gate's journal into the display, because the gate stands
// on the work node and may not reach the state database (SP-B02-2).

package observation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sys/unix"

	"github.com/Cheety/warft/platform/internal/cgroup"
	"github.com/Cheety/warft/platform/internal/outbox"
	"github.com/Cheety/warft/platform/internal/runner"
	"github.com/Cheety/warft/platform/internal/scheduling"
	"github.com/Cheety/warft/platform/internal/statedb"
)

// Command is `workpod observe <subcommand>`.
func Command(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("observe trace · import · log · effects · provenance · cost · alerts · rejections · occupancy · audit · slos · sample")
	}
	switch args[0] {
	case "trace":
		return traceCommand(args[1:], out)
	case "import":
		return importCommand(args[1:], out)
	case "log":
		return logCommand(args[1:], out)
	case "provenance":
		return provenanceCommand(args[1:], out)
	case "cost":
		return costCommand(args[1:], out)
	case "alerts":
		return alertsCommand(args[1:], out)
	case "effects":
		return effectsCommand(args[1:], out)
	case "rejections":
		return rejectionsCommand(args[1:], out)
	case "occupancy":
		return occupancyCommand(args[1:], out)
	case "audit":
		return auditCommand(args[1:], out)
	case "slos":
		return encode(out, SLOs())
	case "sample":
		return sampleCommand(args[1:], out)
	}
	return fmt.Errorf("`workpod observe %s` is not a command", args[0])
}

func encode(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func traceCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("observe trace", flag.ContinueOnError)
	order := fs.String("order", "", "the job — the unit of observation is the job (SP-B03-1)")
	attempt := fs.Int("attempt", 0, "which attempt; 0 is the one the order is on")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *order == "" {
		return fmt.Errorf("which job? --order")
	}
	ctx := context.Background()
	p, err := statedb.Open(ctx)
	if err != nil {
		return err
	}
	defer p.Close()
	t, err := statedb.TraceOf(ctx, p, *order, *attempt)
	if err != nil {
		return err
	}
	return encode(out, t)
}

// importCommand is the join between T-05 and B-03: the phase log a pod run produced becomes the
// spans of that job's trace, with the cost, the evidence class and the three versions SP-B03-1 and
// SP-Q04-4 want on it.
//
// The cost is spread over the phases that ran, in proportion to how long they took. A trace that
// hung the whole bill on one span would be arithmetic nobody could check; proportion is the only
// division the pod actually measured, and the sum is the job's own spend either way.
func importCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("observe import", flag.ContinueOnError)
	order := fs.String("order", "", "the job the report belongs to")
	attempt := fs.Int("attempt", 1, "the attempt it was")
	report := fs.String("report", "", "the report `workpod pod run` printed, as JSON")
	model := fs.String("model", "", "the model version this attempt ran with (SP-Q04-4)")
	prompt := fs.String("prompt", "", "the prompt version (SP-Q04-4)")
	podMinutes := fs.Int64("pod-minutes", 0, "what the attempt cost in pod minutes")
	tokens := fs.Int64("tokens", 0, "what it cost in tokens")
	money := fs.Int64("money-micros", 0, "what it cost in micro-euros")
	node := fs.String("node", "", "the node the pod ran on — where its log lies (SP-B03-4)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *order == "" || *report == "" {
		return fmt.Errorf("--order and --report name the job and the run")
	}
	b, err := os.ReadFile(*report)
	if err != nil {
		return err
	}
	var rep runner.Report
	if err := json.Unmarshal(b, &rep); err != nil {
		return fmt.Errorf("%s is not a report `workpod pod run` wrote: %w", *report, err)
	}
	if len(rep.Phases) == 0 {
		return fmt.Errorf("the report carries no phase log — a trace without spans would be a job nobody can follow (SP-B03-1)")
	}

	ctx := context.Background()
	p, err := statedb.Open(ctx)
	if err != nil {
		return err
	}
	defer p.Close()
	if err := statedb.EnsureAttempt(ctx, p, *order, *attempt); err != nil {
		return err
	}
	cell, project, pipeline, err := jobOf(ctx, p, *order)
	if err != nil {
		return err
	}
	if rep.PipelineVersion != "" {
		pipeline = rep.PipelineVersion
	}

	var totalMS int64
	for _, r := range rep.Phases {
		if r.Outcome == runner.Ran {
			totalMS += r.Millis
		}
	}
	// The clock the spans are laid out on. The report says how long each phase took but not when
	// it started, so the trace is reconstructed backwards from now over the run's own durations —
	// the durations are measured, the origin is stated, and the field says which is which.
	start := time.Now().UTC().Add(-time.Duration(totalMS) * time.Millisecond)

	spans := make([]statedb.Span, 0, len(rep.Phases))
	at := start
	for i, r := range rep.Phases {
		s := statedb.Span{
			OrderID: *order, Attempt: *attempt, Seq: i + 1, Cell: cell, Project: project,
			Phase: string(r.Phase), Outcome: string(r.Outcome), Round: r.Round, Detail: r.Detail,
			StartedAt: at, DurationMS: r.Millis,
			ModelVersion: *model, PromptVersion: *prompt, PipelineVersion: pipeline,
		}
		if r.Outcome == runner.Ran && totalMS > 0 {
			share := float64(r.Millis) / float64(totalMS)
			s.CostPodMinutes = int64(float64(*podMinutes) * share)
			s.CostTokens = int64(float64(*tokens) * share)
			s.CostMoneyMicros = int64(float64(*money) * share)
		}
		// The evidence class belongs on the phase that produced it, which is `deliver`: it is the
		// verdict of the run and not a property of every step it took (Q-02).
		if r.Phase == runner.PhaseDeliver && rep.Evidence != "" {
			s.Evidence = rep.Evidence
		}
		spans = append(spans, s)
		at = at.Add(time.Duration(r.Millis) * time.Millisecond)
	}
	if err := statedb.RecordSpans(ctx, p, spans); err != nil {
		return err
	}

	body := map[string]any{"order": *order, "attempt": *attempt, "spans": len(spans),
		"pipeline_version": pipeline}
	if rep.LogPath != "" && *node != "" {
		l, err := podLogOf(*order, *attempt, *node, rep.LogPath)
		if err != nil {
			return err
		}
		if err := statedb.RecordPodLog(ctx, p, l); err != nil {
			return err
		}
		body["pod_log"] = l
	}
	return encode(out, body)
}

// logCommand hangs one pod log on a job by hand — the same thing `import` does when the report
// names one, for a log that arrived some other way.
func logCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("observe log", flag.ContinueOnError)
	order := fs.String("order", "", "the job whose evidence this is")
	attempt := fs.Int("attempt", 1, "the attempt it belongs to")
	node := fs.String("node", "", "the node the body lies on")
	file := fs.String("file", "", "the log, on that node")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *order == "" || *node == "" || *file == "" {
		return fmt.Errorf("--order, --node and --file: a log without all three is not evidence of anything (SP-B03-4)")
	}
	l, err := podLogOf(*order, *attempt, *node, *file)
	if err != nil {
		return err
	}
	ctx := context.Background()
	p, err := statedb.Open(ctx)
	if err != nil {
		return err
	}
	defer p.Close()
	if err := statedb.RecordPodLog(ctx, p, l); err != nil {
		return err
	}
	return encode(out, l)
}

// podLogOf hashes the body where it lies. The hash is what makes the row evidence rather than a
// pointer: a log that was edited afterwards no longer answers to it.
func podLogOf(order string, attempt int, node, path string) (statedb.PodLog, error) {
	f, err := os.Open(path)
	if err != nil {
		return statedb.PodLog{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return statedb.PodLog{}, err
	}
	return statedb.PodLog{OrderID: order, Attempt: attempt, NodeID: node,
		ContentHash: fmt.Sprintf("sha256:%x", h.Sum(nil)), Path: path, Bytes: n}, nil
}

// effectsCommand folds a node's outbox into the cell, which is what makes the provenance chain
// reach the patch: the outbox lies on the node as files (decisions/gates-and-the-outbox.md) and the
// record of what a job actually produced belongs where the job does. It is the same seam as
// `rejections --journal`, for the same reason — a work node and the state database need not be the
// same machine (SP-B02-2, V-01).
func effectsCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("observe effects", flag.ContinueOnError)
	dir := fs.String("dir", outbox.DefaultDir, "the node's outbox")
	order := fs.String("order", "", "only this job's effects")
	if err := fs.Parse(args); err != nil {
		return err
	}
	entries, err := outbox.New(*dir).List()
	if err != nil {
		return err
	}
	var effects []statedb.Effect
	for _, e := range entries {
		if *order != "" && e.Order != *order {
			continue
		}
		eff := statedb.Effect{OrderID: e.Order, Target: e.Target, ContentHash: e.ContentHash,
			PayloadRef: e.PayloadRef, RequiresRegister: e.RequiresRegister}
		if e.Receipt != nil && e.Receipt.Executed {
			at := e.Receipt.At
			eff.ExecutedAt, eff.ExternalID = &at, e.Receipt.ExternalID
			// Which gate let it through is decided by the target, by the same rule the drain uses
			// to choose one — there are two gates and nothing else (SP-K03-3).
			if gate, err := outbox.GateFor(e.Target); err == nil {
				eff.IssuedBy = string(gate)
			}
		}
		effects = append(effects, eff)
	}
	ctx := context.Background()
	p, err := statedb.Open(ctx)
	if err != nil {
		return err
	}
	defer p.Close()
	added, err := statedb.FoldEffects(ctx, p, effects)
	if err != nil {
		return err
	}
	return encode(out, map[string]any{"read": len(effects), "folded": added, "from": *dir})
}

func provenanceCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("observe provenance", flag.ContinueOnError)
	anchor := fs.String("anchor", "", "any link of the chain: an order, a patch hash, or a channel message id")
	sql := fs.Bool("sql", false, "print the statement instead of running it — AB-K01-7 reads *one* query")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *sql {
		fmt.Fprintln(out, statedb.ProvenanceSQL())
		return nil
	}
	if *anchor == "" {
		return fmt.Errorf("resolve what? --anchor <order|patch hash|channel message id>")
	}
	ctx := context.Background()
	p, err := statedb.Open(ctx)
	if err != nil {
		return err
	}
	defer p.Close()
	pr, err := statedb.Resolve(ctx, p, *anchor)
	if err != nil {
		return err
	}
	return encode(out, pr)
}

func costCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("observe cost", flag.ContinueOnError)
	cell := fs.String("cell", "", "which cell")
	project := fs.String("project", "", "one project, or every project of the cell")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cell == "" {
		return fmt.Errorf("which cell? --cell")
	}
	ctx := context.Background()
	p, err := statedb.Open(ctx)
	if err != nil {
		return err
	}
	defer p.Close()
	c, err := statedb.CostPerProject(ctx, p, *cell, *project)
	if err != nil {
		return err
	}
	return encode(out, c)
}

// State is one alert as it stands: firing, quiet, or not answerable from where the question was
// asked. The third is not a failure — slot 1 is a property of a node and no state database can
// answer it — and reporting it as quiet would be a green nobody measured (Q-02).
type State struct {
	Alert
	State  string `json:"state"` // firing · quiet · not evaluable
	Detail string `json:"detail"`
}

func alertsCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("observe alerts", flag.ContinueOnError)
	cell := fs.String("cell", "", "evaluate against a cell; without one, print the catalog")
	disk := fs.String("disk", "/data/work", "the work disk `disk_filling` is measured on (SP-A05-5)")
	now := fs.Int64("now", 0, "the moment to evaluate at, in seconds since the epoch (a check states one)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cell == "" {
		return encode(out, Catalog())
	}
	at := time.Now().UTC()
	if *now > 0 {
		at = time.Unix(*now, 0).UTC()
	}
	ctx := context.Background()
	p, err := statedb.Open(ctx)
	if err != nil {
		return err
	}
	defer p.Close()

	states := make([]State, 0, len(Catalog()))
	for _, a := range Catalog() {
		s := State{Alert: a, State: "not evaluable"}
		switch a.Name {
		case "control_plane_unreachable":
			s.Detail = "measured on the node by `workpod ping`, not in the state database it would be unreachable from"
		case "queue_growing":
			s.State, s.Detail, err = queueGrowing(ctx, p, *cell)
		case "escapes_or_rejections_jumping":
			s.State, s.Detail, err = rejectionsJumping(ctx, p, *cell, at)
		case "cell_budget_exhausted_early":
			s.State, s.Detail, err = budgetEarly(ctx, p, *cell, at)
		case "disk_filling":
			s.State, s.Detail, err = diskFilling(*disk)
		case "egress_rejections_clustered":
			s.State, s.Detail, err = rejectionsClustered(ctx, p, *cell)
		}
		if err != nil {
			return err
		}
		states = append(states, s)
	}
	return encode(out, states)
}

// queueGrowing is slot 2: twenty samples, none below its predecessor, the last above the first.
// "Monotonically" is the word SP-B03-3 uses, and it is what keeps a queue that rises and falls with
// the working day from waking anybody.
func queueGrowing(ctx context.Context, p *pgxpool.Pool, cell string) (string, string, error) {
	n, err := Threshold("queue_growing", " samples")
	if err != nil {
		return "", "", err
	}
	series, err := statedb.QueueSeries(ctx, p, cell, int(n))
	if err != nil {
		return "", "", err
	}
	if len(series) < int(n) {
		return "not evaluable", fmt.Sprintf("%d of %d samples — the series is not long enough to be monotonic",
			len(series), int(n)), nil
	}
	for i := 1; i < len(series); i++ {
		if series[i] < series[i-1] {
			return "quiet", fmt.Sprintf("the queue fell at sample %d (%d after %d)", i+1, series[i], series[i-1]), nil
		}
	}
	if series[len(series)-1] <= series[0] {
		return "quiet", fmt.Sprintf("flat over %d samples at %d", len(series), series[0]), nil
	}
	return "firing", fmt.Sprintf("%d -> %d over %d samples, never falling",
		series[0], series[len(series)-1], len(series)), nil
}

// rejectionsJumping is slot 3, with the floor the ruling puts under the ratio: three times a small
// number is a small number, and an alert that fires on one is the alert that trains people to
// ignore alerts.
func rejectionsJumping(ctx context.Context, p *pgxpool.Pool, cell string, at time.Time) (string, string, error) {
	ratio, err := Threshold("escapes_or_rejections_jumping", "x the mean")
	if err != nil {
		return "", "", err
	}
	hours, err := Threshold("escapes_or_rejections_jumping", " hours before it")
	if err != nil {
		return "", "", err
	}
	floor, err := Threshold("escapes_or_rejections_jumping", " rejections in absolute")
	if err != nil {
		return "", "", err
	}
	last, mean, err := statedb.RejectionRates(ctx, p, cell, int(hours), at)
	if err != nil {
		return "", "", err
	}
	detail := fmt.Sprintf("%d in the last hour against a mean of %.1f over the %d before it (floor %d)",
		last, mean, int(hours), int(floor))
	if float64(last) >= floor && float64(last) >= ratio*mean && mean >= 0 {
		if mean == 0 && float64(last) < floor {
			return "quiet", detail, nil
		}
		return "firing", detail, nil
	}
	return "quiet", detail, nil
}

// budgetEarly is slot 4: a daily pot nearly spent while its day is not.
func budgetEarly(ctx context.Context, p *pgxpool.Pool, cell string, at time.Time) (string, string, error) {
	potShare, err := Threshold("cell_budget_exhausted_early", " % of a cap")
	if err != nil {
		return "", "", err
	}
	dayShare, err := Threshold("cell_budget_exhausted_early", " % of its day")
	if err != nil {
		return "", "", err
	}
	elapsed := float64(at.Hour()*3600+at.Minute()*60+at.Second()) / 86400 * 100
	pots, err := statedb.PotsAtCap(ctx, p, cell, potShare/100)
	if err != nil {
		return "", "", err
	}
	if len(pots) == 0 {
		return "quiet", fmt.Sprintf("no daily pot is at %.0f %% of a cap (%.0f %% of the day has passed)",
			potShare, elapsed), nil
	}
	if elapsed >= dayShare {
		return "quiet", fmt.Sprintf("%d pot(s) at the cap, but %.0f %% of the day has passed — that is spent, not early",
			len(pots), elapsed), nil
	}
	return "firing", fmt.Sprintf("%s pot %s at %.0f %% of its %s cap, %.0f %% into the day",
		pots[0].Scope, pots[0].Pot, pots[0].Fraction*100, pots[0].Resource, elapsed), nil
}

// diskFilling is SP-A05-5: the disk is the first consumable that gets an alert. It is a display and
// not a fifth waking alert (decisions/alerts.md).
func diskFilling(path string) (string, string, error) {
	full, err := Threshold("disk_filling", " % full")
	if err != nil {
		return "", "", err
	}
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return "not evaluable", fmt.Sprintf("%s: %v", path, err), nil
	}
	total := st.Blocks * uint64(st.Bsize)
	free := st.Bavail * uint64(st.Bsize)
	if total == 0 {
		return "not evaluable", path + " reports no blocks", nil
	}
	used := float64(total-free) / float64(total) * 100
	detail := fmt.Sprintf("%s at %.1f %% of %d MB (threshold %.0f %%)", path, used, total/(1<<20), full)
	if used >= full {
		return "firing", detail, nil
	}
	return "quiet", detail, nil
}

func rejectionsClustered(ctx context.Context, p *pgxpool.Pool, cell string) (string, string, error) {
	clusters, err := statedb.RejectionClusters(ctx, p, cell, 24*time.Hour)
	if err != nil {
		return "", "", err
	}
	if len(clusters) == 0 {
		return "quiet", "no target was refused in this cell in the last 24 hours", nil
	}
	return "firing", fmt.Sprintf("%d cluster(s); the largest is %s, %d refusals over %d job(s)",
		len(clusters), clusters[0].Target, clusters[0].Count, clusters[0].Orders), nil
}

// rejectionsCommand is SP-B02-5's display, and the seam that fills it. `--journal` folds what the
// egress gate wrote on a work node into the cell; without it, the command shows what is already
// there, clustered.
func rejectionsCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("observe rejections", flag.ContinueOnError)
	cell := fs.String("cell", "", "which cell")
	journal := fs.String("journal", "", "fold an egress gate's journal in (one JSON object per line)")
	node := fs.String("node", "", "the node that journal came from")
	window := fs.Duration("window", 24*time.Hour, "how far back the display reaches")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cell == "" {
		return fmt.Errorf("which cell? --cell")
	}
	ctx := context.Background()
	p, err := statedb.Open(ctx)
	if err != nil {
		return err
	}
	defer p.Close()

	folded := 0
	if *journal != "" {
		if *node == "" {
			return fmt.Errorf("--node names where the journal came from; a refusal without a node cannot be traced back to a gate")
		}
		rs, err := readJournal(*journal)
		if err != nil {
			return err
		}
		if folded, err = statedb.FoldRejections(ctx, p, *node, rs); err != nil {
			return err
		}
	}
	clusters, err := statedb.RejectionClusters(ctx, p, *cell, *window)
	if err != nil {
		return err
	}
	return encode(out, struct {
		Folded   int               `json:"folded"`
		Window   string            `json:"window"`
		Clusters []statedb.Cluster `json:"clusters"`
	}{folded, window.String(), clusters})
}

// readJournal reads the gate's journal: one JSON object per line, appended as each target was
// refused. A malformed line is an error rather than a skipped one — a refusal that was silently
// dropped is exactly the one somebody needed to see (SP-B02-5).
func readJournal(path string) ([]statedb.Rejection, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []statedb.Rejection
	dec := json.NewDecoder(f)
	for {
		var r statedb.Rejection
		if err := dec.Decode(&r); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// occupancyCommand is R-D. Without a cell it is the design calculation; with one it is the same six
// places measured, which is SP-RD-2's "the same display, a different source".
func occupancyCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("observe occupancy", flag.ContinueOnError)
	cell := fs.String("cell", "", "measure against a cell instead of planning")
	slice := fs.String("slice", "", "the pods slice unit to read cpu.pressure and memory.pressure from")
	path := fs.String("cgroup", "", "the same, by path")
	ram := fs.Int("ram", 256, "planning: the node's memory in GB")
	cores := fs.Int("cores", 96, "planning: the node's cores")
	nodes := fs.Int("nodes", 1, "planning: how many work nodes of that shape")
	fleet := fs.Int("fleet", 2000, "planning: how many workpods exist")
	rush := fs.Int("rush", 15, "planning: what share of them wants to compute, in percent")
	constants := fs.Bool("constants", false, "print the five constants the table computes with")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *constants {
		return encode(out, Constants())
	}
	if *cell == "" {
		t, err := Plan(Sliders{RAMGigabytes: *ram, Cores: *cores, WorkNodes: *nodes,
			Fleet: *fleet, RushPercent: *rush})
		if err != nil {
			return err
		}
		return encode(out, t)
	}

	r := Reading{}
	ctx := context.Background()
	p, err := statedb.Open(ctx)
	if err != nil {
		return err
	}
	defer p.Close()
	c, err := statedb.CellCounts(ctx, p, *cell)
	if err != nil {
		return err
	}
	r.Active, r.Queued, r.Frozen, r.WorkNodes = c.Active, c.Queued, c.Frozen, c.WorkNodes
	r.HaveCells = true

	slicePath := *path
	if slicePath == "" && *slice != "" {
		if slicePath, err = cgroup.UnitPath(*slice); err != nil {
			return err
		}
	}
	if slicePath != "" {
		if _, err := os.Stat(filepath.Join(slicePath, "memory.pressure")); err != nil {
			return fmt.Errorf("%s carries no memory.pressure — R-D measures, it does not estimate (SP-RD-2): %w", slicePath, err)
		}
		g := cgroup.Signals(slicePath)
		r.Sample = scheduling.Sample{At: time.Now().UTC(),
			MemorySomeAvg10: g.MemorySomeAvg10, MemoryFullAvg10: g.MemoryFullAvg10,
			IOFullAvg10: g.IOFullAvg10, CPUSomeAvg60: g.CPUSomeAvg60,
			MemoryEventsHigh: g.MemoryEventsHigh, PgMajFault: g.PgMajFault}
		r.SampleOf, r.HavePSI = slicePath, true
	}
	return encode(out, struct {
		Table
		Reading Reading `json:"reading"`
	}{Measure(r), r})
}

func auditCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("observe audit", flag.ContinueOnError)
	cell := fs.String("cell", "", "which cell")
	subject := fs.String("subject", "", "one subject, e.g. order:<id>")
	limit := fs.Int("limit", 50, "how many entries")
	actor := fs.String("actor", "", "append an entry instead of reading: who acted")
	action := fs.String("action", "", "what they did — authority.issued · gate.passed · human.accepted …")
	project := fs.String("project", "", "the project, where the entry is about one")
	detail := fs.String("detail", "{}", "the entry's detail, as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cell == "" {
		return fmt.Errorf("which cell? --cell")
	}
	ctx := context.Background()
	p, err := statedb.Open(ctx)
	if err != nil {
		return err
	}
	defer p.Close()

	if *actor != "" || *action != "" {
		if *actor == "" || *action == "" || *subject == "" {
			return fmt.Errorf("an entry names who, what and about what: --actor, --action, --subject")
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(*detail), &body); err != nil {
			return fmt.Errorf("--detail is JSON: %w", err)
		}
		if err := statedb.AppendAudit(ctx, p, *cell, *project, *actor, *action, *subject, body); err != nil {
			return err
		}
	}
	entries, err := statedb.AuditOf(ctx, p, *cell, *subject, *limit)
	if err != nil {
		return err
	}
	return encode(out, entries)
}

// sampleCommand records one queue-depth sample. The scheduler will take these on its own tick; as a
// command it is how a check states a series of twenty minutes without waiting twenty minutes, the
// same shortcut `scheduler pressure --samples` takes for PSI.
func sampleCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("observe sample", flag.ContinueOnError)
	cell := fs.String("cell", "", "which cell's queue")
	at := fs.Int64("at", 0, "the moment of the sample, in seconds since the epoch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cell == "" {
		return fmt.Errorf("which cell? --cell")
	}
	moment := time.Now().UTC()
	if *at > 0 {
		moment = time.Unix(*at, 0).UTC()
	}
	ctx := context.Background()
	p, err := statedb.Open(ctx)
	if err != nil {
		return err
	}
	defer p.Close()
	depth, err := statedb.SampleQueue(ctx, p, *cell, moment)
	if err != nil {
		return err
	}
	return encode(out, map[string]any{"cell": *cell, "at": moment, "depth": depth})
}

// jobOf reads the three things a span needs from its job and cannot invent: which cell, which
// project, and which pipeline definition the order was created under.
func jobOf(ctx context.Context, p *pgxpool.Pool, orderID string) (cell, project, pipeline string, err error) {
	err = p.QueryRow(ctx, `SELECT cell, project::text, pipeline_version FROM "order" WHERE id = $1`,
		orderID).Scan(&cell, &project, &pipeline)
	return cell, project, pipeline, err
}
