// `workpod scheduler <subcommand>` — the entry points a check, a duty officer and the node itself
// use.
//
// The subcommands exist for the same reason `workpod control admit` does: a row of the acceptance
// matrix has to be provable by running the program, and a scheduler that could only be observed
// from inside a live cell under real memory pressure would be a scheduler whose rows turn green
// through explanation. `pressure --samples` replays a reading rather than causing one, which is the
// only way to watch a 30-second release hold without waiting 30 seconds — and `pressure --slice` is
// the same code against the real cgroup files, so the replay is a shortcut and never a stand-in.

package scheduler

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Cheety/warft/platform/internal/cgroup"
	"github.com/Cheety/warft/platform/internal/scheduling"
	"github.com/Cheety/warft/platform/internal/statedb"
)

// Command is `workpod scheduler <subcommand>`.
func Command(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("scheduler tokens · priorities · order · order-sql · pressure · predict · queue · enqueue · freeze · thaw · record")
	}
	switch args[0] {
	case "tokens":
		return tokensCommand(args[1:], out)
	case "priorities":
		return prioritiesCommand(out)
	case "order":
		return orderCommand(args[1:], out)
	case "order-sql":
		fmt.Fprintln(out, statedb.OrderBySQL())
		return nil
	case "pressure":
		return pressureCommand(args[1:], out)
	case "predict":
		return predictCommand(args[1:], out)
	case "queue":
		return queueCommand(args[1:], out)
	case "enqueue":
		return enqueueCommand(args[1:], out)
	case "freeze", "thaw":
		return freezeCommand(args[0], args[1:], out)
	case "record":
		return recordCommand(args[1:], out)
	}
	return fmt.Errorf("`workpod scheduler %s` is not a command", args[0])
}

func encode(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// tokensCommand prints the ruled join and the three pools a node of `--cores` cores offers. It is
// how acceptance/rb-scheduler.sh holds decisions/phase-tokens.md against the file the binary
// embeds.
func tokensCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("scheduler tokens", flag.ContinueOnError)
	cores := fs.Int("cores", runtime.NumCPU(), "the cores the work layer was allocated (SP-RC-5)")
	trace := fs.String("trace", "", "run a pool: TSV `pod<TAB>step`, where step is a phase, `wait`, `exclusive` or `leave`")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *trace != "" {
		b, err := os.ReadFile(*trace)
		if err != nil {
			return err
		}
		pool := scheduling.NewPool(scheduling.SizesFor(*cores))
		for n, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			f := strings.Fields(line)
			if len(f) != 2 {
				return fmt.Errorf("trace line %d: `pod<TAB>step`, not %q", n+1, line)
			}
			step, err := traceStep(pool, f[0], f[1])
			if err != nil {
				return err
			}
			if err := encode(out, step); err != nil {
				return err
			}
		}
		return nil
	}

	type row struct {
		Phase string `json:"phase"`
		Token string `json:"token"`
	}
	body := struct {
		Phases []row              `json:"phases"`
		Sizes  scheduling.Sizes   `json:"sizes"`
		Cores  int                `json:"cores"`
		Ruling string             `json:"ruling"`
		Class  []scheduling.Class `json:"classes"`
	}{Sizes: scheduling.SizesFor(*cores), Cores: *cores,
		Ruling: "decisions/phase-tokens.md", Class: scheduling.Classes()}
	for _, r := range scheduling.RuledTokens().Rows() {
		body.Phases = append(body.Phases, row{string(r.Phase), string(r.Class)})
	}
	return encode(out, body)
}

// prioritiesCommand prints SP-RB-2's table as the program carries it, so a check can hold the four
// bounds and the one "may preempt" against §12.2 of the specification rather than against a
// description of it.
func prioritiesCommand(out io.Writer) error {
	type row struct {
		Priority   string `json:"priority"`
		Wait       string `json:"waits_at_most"`
		MayPreempt bool   `json:"may_preempt"`
		Rank       int    `json:"rank"`
	}
	var body []row
	for _, b := range scheduling.Bounds() {
		wait := b.Wait.String()
		if b.Unbounded() {
			wait = "unbounded"
		}
		body = append(body, row{string(b.Priority), wait, b.MayPreempt, b.Rank})
	}
	return encode(out, body)
}

// traceStep is one line of `scheduler tokens --trace`: a pod entering a phase, beginning to wait
// for a model response, taking the node exclusively, or leaving.
//
// It exists so that SP-RB-1 can be checked by running the pool rather than by reading it. `wait` is
// the requirement's own closing sentence, and the trace prints what the pod holds after every step
// — which is where "a waiting pod holds no CPU token" is either true or not.
func traceStep(pool *scheduling.Pool, pod, step string) (map[string]any, error) {
	out := map[string]any{"pod": pod, "step": step}
	switch step {
	case "wait":
		out["returned"] = string(pool.Wait(pod))
		out["granted"] = false
	case "leave":
		pool.Leave(pod)
		out["granted"] = false
	case "exclusive":
		g, err := pool.EnterExclusive(pod)
		if err != nil {
			return nil, err
		}
		out["granted"], out["class"], out["reason"] = g.Granted, string(g.Class), g.Reason
	default:
		g, err := pool.Enter(pod, scheduling.Phase(step))
		if err != nil {
			return nil, err
		}
		out["granted"], out["class"], out["reason"] = g.Granted, string(g.Class), g.Reason
	}
	held, holds := pool.Holds(pod)
	out["holds"] = ""
	if holds {
		out["holds"] = string(held)
	}
	counts := map[string]int{}
	for _, c := range scheduling.Classes() {
		counts[string(c)] = pool.Held(c)
	}
	out["held"] = counts
	out["free_cpu_ram"] = pool.Free(scheduling.ClassCPURAM)
	out["exclusive"] = pool.Exclusive()
	return out, nil
}

// queueFile is what `scheduler order` reads: a queue as it stood at one moment. The check writes
// one, the program orders it, and the order is the ruling's rather than the check's.
type queueFile struct {
	// Now is the moment the queue is ordered at, in seconds since the epoch. A file rather than
	// the clock, because "waited 400 seconds" has to be stateable without waiting 400 seconds.
	Now  int64 `json:"now"`
	Jobs []struct {
		OrderID          string `json:"order_id"`
		Priority         string `json:"priority"`
		WaitedSeconds    int64  `json:"waited_seconds"`
		Large            bool   `json:"large,omitempty"`
		PredictedSeconds int64  `json:"predicted_seconds,omitempty"`
	} `json:"jobs"`
}

// orderCommand applies decisions/aging.md to a stated queue and prints the order, with each job's
// overdue flag and ratio beside it — so a check can see *why* the order is what it is rather than
// only that it changed.
func orderCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("scheduler order", flag.ContinueOnError)
	path := fs.String("queue", "", "a queue as JSON: {now, jobs:[{order_id, priority, waited_seconds}]}")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" {
		return fmt.Errorf("which queue? --queue <file.json>")
	}
	b, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	var qf queueFile
	if err := json.Unmarshal(b, &qf); err != nil {
		return err
	}
	now := time.Unix(qf.Now, 0).UTC()
	if qf.Now == 0 {
		now = time.Now()
	}

	waiting := make([]scheduling.Waiting, 0, len(qf.Jobs))
	for _, j := range qf.Jobs {
		if _, err := scheduling.BoundOf(scheduling.Priority(j.Priority)); err != nil {
			return err
		}
		waiting = append(waiting, scheduling.Waiting{
			OrderID:          j.OrderID,
			Priority:         scheduling.Priority(j.Priority),
			Since:            now.Add(-time.Duration(j.WaitedSeconds) * time.Second),
			Large:            j.Large,
			PredictedRuntime: time.Duration(j.PredictedSeconds) * time.Second,
		})
	}

	type ordered struct {
		OrderID  string  `json:"order_id"`
		Priority string  `json:"priority"`
		Waited   float64 `json:"waited_seconds"`
		Overdue  bool    `json:"overdue"`
		Ratio    float64 `json:"ratio"`
	}
	var body []ordered
	for _, w := range scheduling.Order(waiting, now) {
		body = append(body, ordered{w.OrderID, string(w.Priority),
			w.Waited(now).Seconds(), w.Overdue(now), w.Ratio(now)})
	}
	return encode(out, body)
}

// pressureCommand runs the reader: either against the real pods slice or against a stated series of
// readings. Both go through the same Scheduler and the same ladder.
func pressureCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("scheduler pressure", flag.ContinueOnError)
	samples := fs.String("samples", "", "replay a reading: TSV `t_seconds mem_some mem_full io_full cpu_some events_high pgmajfault`")
	slice := fs.String("slice", "", "read the real pressure files of a slice unit (SP-RC-1)")
	path := fs.String("cgroup", "", "read the real pressure files of a cgroup path")
	ticks := fs.Int("ticks", 5, "how many samples to take when reading a live slice")
	cores := fs.Int("cores", runtime.NumCPU(), "the cores the work layer was allocated")
	thresholds := fs.Bool("thresholds", false, "print SP-RC-2 with OP-6's four numbers and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *thresholds {
		type row struct {
			Signal       string  `json:"signal"`
			Enter        float64 `json:"enter"`
			EnterSamples int     `json:"enter_samples"`
			Release      float64 `json:"release"`
			ReleaseHold  string  `json:"release_hold"`
			Rung         string  `json:"rung"`
			Reaction     string  `json:"reaction"`
		}
		var body struct {
			Interval   string   `json:"sample_interval"`
			Ladder     []string `json:"ladder"`
			Thresholds []row    `json:"thresholds"`
		}
		body.Interval = scheduling.SampleInterval.String()
		for _, r := range scheduling.Ladder() {
			body.Ladder = append(body.Ladder, string(r))
		}
		for _, t := range scheduling.Thresholds() {
			body.Thresholds = append(body.Thresholds, row{string(t.Signal), t.Enter, t.EnterSamples,
				t.Release, t.ReleaseHold.String(), string(t.Rung), t.Reaction})
		}
		return encode(out, body)
	}

	s, turns := replayScheduler(*cores)
	switch {
	case *samples != "":
		series, err := readSamples(*samples)
		if err != nil {
			return err
		}
		for _, sample := range series {
			next := sample
			s.Read = func() scheduling.Sample { return next }
			if err := encode(out, s.Tick(context.Background())); err != nil {
				return err
			}
		}
	case *slice != "" || *path != "":
		p := *path
		if p == "" {
			var err error
			if p, err = cgroup.UnitPath(*slice); err != nil {
				return err
			}
		}
		s.Read = SliceReader(p)
		for i := 0; i < *ticks; i++ {
			if err := encode(out, s.Tick(context.Background())); err != nil {
				return err
			}
			if i < *ticks-1 {
				time.Sleep(scheduling.SampleInterval)
			}
		}
	default:
		return fmt.Errorf("read what? --samples <file>, --slice <unit> or --cgroup <path>")
	}
	_ = turns
	return nil
}

// replayScheduler is a scheduler whose four outside actions are recorded rather than performed.
// Freezing a pod that does not exist would be a pretense; recording that the rung was reached, in
// order, is the observation AB-RC-3 asks for.
func replayScheduler(cores int) (*Scheduler, *[]string) {
	acts := &[]string{}
	s := New("", scheduling.NewPool(scheduling.SizesFor(cores)), func() scheduling.Sample { return scheduling.Sample{} })
	s.Throttle = func(weight int) error {
		*acts = append(*acts, fmt.Sprintf("cpu.weight=%d", weight))
		return nil
	}
	s.FreezeOne = func(context.Context) (string, error) {
		*acts = append(*acts, "freeze")
		return "", nil
	}
	s.Checkpoint = func(_ context.Context, pod string) error {
		*acts = append(*acts, "checkpoint "+pod)
		return nil
	}
	s.Escalate = func(_ context.Context, signals []scheduling.Signal) error {
		*acts = append(*acts, fmt.Sprintf("escalate %v", signals))
		return nil
	}
	return s, acts
}

// readSamples parses the replay format. A malformed row is an error rather than a skipped line: a
// reading that was silently dropped would change the sample count, and every hold time in OP-6 is
// counted in samples.
func readSamples(path string) ([]scheduling.Sample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []scheduling.Sample
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if f[0] == "t_seconds" {
			continue // the header
		}
		if len(f) != 7 {
			return nil, fmt.Errorf("line %d: seven columns, not %d", n, len(f))
		}
		nums := make([]float64, 7)
		for i, s := range f {
			v, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return nil, fmt.Errorf("line %d, column %d: %w", n, i+1, err)
			}
			nums[i] = v
		}
		out = append(out, scheduling.Sample{
			At:               time.Unix(int64(nums[0]), 0).UTC(),
			MemorySomeAvg10:  nums[1],
			MemoryFullAvg10:  nums[2],
			IOFullAvg10:      nums[3],
			CPUSomeAvg60:     nums[4],
			MemoryEventsHigh: uint64(nums[5]),
			PgMajFault:       uint64(nums[6]),
		})
	}
	return out, sc.Err()
}

// predictCommand is SP-RC-6's mechanical admission, asked once. With `--runs` it decides from a
// stated profile; with `--cell`, `--project` and `--repository` it reads the profile out of the
// state contract, which is where the runs actually accumulate.
func predictCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("scheduler predict", flag.ContinueOnError)
	runs := fs.Int("runs", -1, "state a profile instead of reading one: how many runs it rests on")
	peak := fs.Int64("peak", 0, "the measured peak RSS of those runs, in bytes")
	free := fs.Int64("free", 0, "how many bytes the node has free")
	cell := fs.String("cell", "", "read the profile from the state contract: which cell")
	project := fs.String("project", "", "which project")
	repository := fs.String("repository", "", "which repository (the order's locality group)")
	phase := fs.String("phase", "check", "which of T-05's seven phases")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *free <= 0 {
		return fmt.Errorf("a prediction is held against what is free: --free <bytes>")
	}

	var profile scheduling.Profile
	if *runs >= 0 {
		profile = scheduling.Profile{Repository: *repository, Phase: scheduling.Phase(*phase),
			Runs: *runs, PeakRSS: *peak}
		if profile.Repository == "" {
			profile.Repository = "stated"
		}
	} else {
		if *cell == "" || *project == "" || *repository == "" {
			return fmt.Errorf("read a profile with --cell --project --repository, or state one with --runs")
		}
		ctx := context.Background()
		pool, err := statedb.Open(ctx)
		if err != nil {
			return err
		}
		defer pool.Close()
		if profile, err = statedb.ProfileOf(ctx, pool, *cell, *project, *repository,
			scheduling.Phase(*phase)); err != nil {
			return err
		}
	}

	v := scheduling.Decide(profile, *free)
	return encode(out, struct {
		Profile scheduling.Profile `json:"profile"`
		Free    int64              `json:"free_bytes"`
		Verdict scheduling.Verdict `json:"verdict"`
	}{profile, *free, v})
}

// queueCommand reads or claims the queue. `--claim` takes rows with FOR UPDATE SKIP LOCKED and
// holds them for `--hold`, which is how a check can run two claims at once and see that neither
// waits and neither gets the other's rows (AB-E02-2).
func queueCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("scheduler queue", flag.ContinueOnError)
	cell := fs.String("cell", "", "which cell's queue")
	group := fs.String("locality-group", "", "only this locality group (OP-8's sticky assignment)")
	limit := fs.Int("limit", 20, "how many jobs to show")
	claim := fs.Int("claim", 0, "claim this many with SKIP LOCKED instead of only reading")
	hold := fs.Duration("hold", 0, "hold the claim open this long before committing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cell == "" {
		return fmt.Errorf("which cell? --cell")
	}
	ctx := context.Background()
	pool, err := statedb.Open(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	if *claim <= 0 {
		head, err := statedb.Head(ctx, pool, *cell, *limit)
		if err != nil {
			return err
		}
		return encode(out, head)
	}

	var claimed []statedb.Queued
	err = statedb.Claim(ctx, pool, *cell, *group, *claim, func(ctx context.Context, _ pgx.Tx, q []statedb.Queued) error {
		claimed = q
		if *hold > 0 {
			time.Sleep(*hold)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return encode(out, claimed)
}

func enqueueCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("scheduler enqueue", flag.ContinueOnError)
	order := fs.String("order", "", "the admitted order to put in the queue")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *order == "" {
		return fmt.Errorf("which order? --order")
	}
	ctx := context.Background()
	pool, err := statedb.Open(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := statedb.Enqueue(ctx, pool, *order); err != nil {
		return err
	}
	fmt.Fprintf(out, "order %s is queued; the order it comes out in is decisions/aging.md's (SP-RB-3)\n", *order)
	return nil
}

// freezeCommand is SP-RB-4 from the outside: it freezes an order, a cgroup, or both. The cgroup
// half is the kernel's freezer — the processes stop and keep everything they had, which is what
// makes a preemption a preemption (decisions/escalation-ladder.md).
func freezeCommand(verb string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("scheduler "+verb, flag.ContinueOnError)
	order := fs.String("order", "", "the order to move `running -> frozen` (or back)")
	path := fs.String("cgroup", "", "the pod's cgroup to freeze with cgroup.freeze")
	reason := fs.String("reason", "", "why — a freeze without a reason is a freeze nobody can account for")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *order == "" && *path == "" {
		return fmt.Errorf("freeze what? --order, --cgroup, or both")
	}

	body := map[string]any{"verb": verb}
	if *path != "" {
		before, err := cgroup.Procs(*path)
		if err != nil {
			return err
		}
		if verb == "freeze" {
			err = cgroup.Freeze(*path)
		} else {
			err = cgroup.Thaw(*path)
		}
		if err != nil {
			return err
		}
		after, err := cgroup.Procs(*path)
		if err != nil {
			return err
		}
		body["cgroup"] = *path
		body["procs_before"] = before
		body["procs_after"] = after
		body["frozen"] = cgroup.Frozen(*path)
	}

	if *order != "" {
		if *reason == "" {
			return fmt.Errorf("a freeze states why (--reason): the duty officer reading the trail is why B-03 exists")
		}
		ctx := context.Background()
		pool, err := statedb.Open(ctx)
		if err != nil {
			return err
		}
		defer pool.Close()
		if verb == "freeze" {
			err = statedb.Freeze(ctx, pool, *order, *reason)
		} else {
			err = statedb.Thaw(ctx, pool, *order, *reason)
		}
		if err != nil {
			return err
		}
		body["order"] = *order
		body["state"] = map[string]string{"freeze": "frozen", "thaw": "running"}[verb]
	}
	return encode(out, body)
}

// recordCommand is SP-RC-6's other half: one finished phase, folded into the profile admission will
// decide from. The worker writes these with its report from AP-3.8; until then it is a command, so
// "after three runs" can be checked against three runs somebody performed rather than three nobody
// measured.
func recordCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("scheduler record", flag.ContinueOnError)
	cell := fs.String("cell", "", "which cell")
	project := fs.String("project", "", "which project")
	repository := fs.String("repository", "", "which repository (the order's locality group)")
	phase := fs.String("phase", "check", "which of T-05's seven phases")
	peak := fs.Int64("peak", 0, "the peak RSS of this run, in bytes")
	runtimeMS := fs.Int64("runtime-ms", 0, "how long the phase took, in milliseconds")
	cgroupPath := fs.String("cgroup", "", "read the peak out of a cgroup instead of stating it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cell == "" || *project == "" || *repository == "" {
		return fmt.Errorf("--cell, --project and --repository name the profile this run belongs to")
	}
	if *cgroupPath != "" {
		if p, ok := cgroup.MemoryPeak(*cgroupPath); ok {
			*peak = int64(p)
		} else {
			c, err := cgroup.MemoryCurrent(*cgroupPath)
			if err != nil {
				return err
			}
			*peak = int64(c)
		}
	}
	ctx := context.Background()
	pool, err := statedb.Open(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := statedb.RecordPhase(ctx, pool, *cell, *project, *repository,
		scheduling.Phase(*phase), *peak, time.Duration(*runtimeMS)*time.Millisecond); err != nil {
		return err
	}
	p, err := statedb.ProfileOf(ctx, pool, *cell, *project, *repository, scheduling.Phase(*phase))
	if err != nil {
		return err
	}
	return encode(out, struct {
		Profile    scheduling.Profile `json:"profile"`
		Mechanical bool               `json:"mechanical"`
	}{p, p.Mechanical()})
}
