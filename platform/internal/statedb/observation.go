// B-03 in the state contract: the trace of a job, the pod logs that are its evidence, the refused
// targets a display shows, and the one query that resolves the provenance chain backwards.
//
// Everything here is state, which is why it lives in the step that owns the state contract rather
// than in the module that holds B-03's rules (`internal/observation`, a role above this one). What
// the rules module may not do is reach into the database; what this module may not do is decide
// what wakes a human. The seam is decisions/module-dependencies.md's, and it is the same one the
// budget and the scheduling rules already stand on.

package statedb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Cheety/warft/platform/internal/ids"
)

// Span is one phase of a job as it ran — a row of `job_span`.
//
// The job is the trace: `OrderID` with `Attempt` identifies it, and there is no trace id, because
// the envelope and the result are already reachable from the order (SP-K01-7) and a second
// identifier would be a second thing to lose.
type Span struct {
	OrderID string `json:"order_id"`
	Attempt int    `json:"attempt"`
	Seq     int    `json:"seq"`
	Cell    string `json:"cell"`
	Project string `json:"project"`

	Phase   string `json:"phase"`
	Outcome string `json:"outcome"`
	Round   int    `json:"round"`
	Detail  string `json:"detail"`

	StartedAt  time.Time `json:"started_at"`
	DurationMS int64     `json:"duration_ms"`

	CostPodMinutes  int64 `json:"cost_pod_minutes"`
	CostTokens      int64 `json:"cost_tokens"`
	CostMoneyMicros int64 `json:"cost_money_micros"`

	Evidence        string `json:"evidence,omitempty"`
	ModelVersion    string `json:"model_version,omitempty"`
	PromptVersion   string `json:"prompt_version,omitempty"`
	PipelineVersion string `json:"pipeline_version"`
}

// Trace is what SP-B03-1 asks for: one trace per job, the phases as spans, with the job's own
// attributes over them.
type Trace struct {
	OrderID         string    `json:"order_id"`
	Attempt         int       `json:"attempt"`
	Cell            string    `json:"cell"`
	Project         string    `json:"project"`
	State           string    `json:"state"`
	Cause           string    `json:"cause,omitempty"`
	Evidence        string    `json:"evidence,omitempty"`
	ModelVersion    string    `json:"model_version,omitempty"`
	PromptVersion   string    `json:"prompt_version,omitempty"`
	PipelineVersion string    `json:"pipeline_version"`
	Spans           []Span    `json:"spans"`
	SpentPodMinutes int64     `json:"spent_pod_minutes"`
	SpentTokens     int64     `json:"spent_tokens"`
	SpentMoney      int64     `json:"spent_money_micros"`
	Logs            []PodLog  `json:"logs"`
	FirstProgress   *float64  `json:"time_to_first_progress_seconds,omitempty"`
	ReceivedAt      time.Time `json:"envelope_received_at"`
}

// PodLog is one pod's console, kept on the node and hung on the job (SP-B03-4).
type PodLog struct {
	OrderID     string    `json:"order_id"`
	Attempt     int       `json:"attempt"`
	NodeID      string    `json:"node_id"`
	ContentHash string    `json:"content_hash"`
	Path        string    `json:"path"`
	Bytes       int64     `json:"bytes"`
	WrittenAt   time.Time `json:"written_at"`
}

// Rejection is one refused target as the egress gate wrote it into its journal. The gate stands on
// the work node and cannot reach this database (SP-B02-2, decisions/module-dependencies.md), so the
// journal is folded in here.
type Rejection struct {
	OrderID string    `json:"order_id"`
	Target  string    `json:"target"`
	Method  string    `json:"method"`
	Reason  string    `json:"reason"`
	At      time.Time `json:"at"`
}

// RecordSpans writes a trace, or a piece of one. The attempt must exist — a span hanging off an
// order without an attempt could not say which run it described (SP-K02-2) — and the sequence is
// the caller's, because the order the phases happened in is the thing the trace is for.
//
// Writing the same span twice is not an error: a worker that reports its phases and then dies
// before its state write is a job that gets reported again, and B-03 must not turn that into two
// traces (the same argument K-03's domain key rests on).
func RecordSpans(ctx context.Context, pool *pgxpool.Pool, spans []Span) error {
	if len(spans) == 0 {
		return nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	for _, s := range spans {
		if s.PipelineVersion == "" {
			return fmt.Errorf("span %s/%d/%d carries no pipeline version — SP-Q04-4 puts it on the job log, not only in the cache key",
				s.OrderID, s.Attempt, s.Seq)
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO job_span (order_id, attempt, seq, cell, project, phase, outcome, round, detail,
			                      started_at, duration_ms, cost_pod_minutes, cost_tokens,
			                      cost_money_micros, evidence, model_version, prompt_version,
			                      pipeline_version)
			VALUES ($1, $2, $3, $4, $5::uuid, $6, $7, $8, $9, $10, $11, $12, $13, $14,
			        nullif($15,'')::evidence_class, nullif($16,''), nullif($17,''), $18)
			ON CONFLICT (order_id, attempt, seq) DO NOTHING`,
			s.OrderID, s.Attempt, s.Seq, s.Cell, s.Project, s.Phase, s.Outcome, s.Round, s.Detail,
			s.StartedAt, s.DurationMS, s.CostPodMinutes, s.CostTokens, s.CostMoneyMicros,
			s.Evidence, s.ModelVersion, s.PromptVersion, s.PipelineVersion)
		if err != nil {
			return fmt.Errorf("span %s/%d/%d: %w", s.OrderID, s.Attempt, s.Seq, err)
		}
	}
	return tx.Commit(ctx)
}

// EnsureAttempt writes the attempt row a trace hangs off, if it is not there yet. The attempt is
// K-02's unit of retry and AP-6.2 will create it when it grants a lease; until then a job driven by
// hand needs it, and creating it twice is not an event (decisions/jobs-by-hand.md).
func EnsureAttempt(ctx context.Context, pool *pgxpool.Pool, orderID string, attempt int) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO attempt (order_id, attempt, cell, project)
		SELECT o.id, $2, o.cell, o.project FROM "order" o WHERE o.id = $1
		ON CONFLICT (order_id, attempt) DO NOTHING`, orderID, attempt)
	return err
}

// TraceOf reads one job's trace. Attempt 0 means "the attempt the order is on", which is the one a
// duty officer means when they name a job without naming a run.
func TraceOf(ctx context.Context, pool *pgxpool.Pool, orderID string, attempt int) (Trace, error) {
	var t Trace
	var cause, evidence, model, prompt *string
	err := pool.QueryRow(ctx, `
		SELECT o.id::text, CASE WHEN $2 = 0 THEN o.attempt ELSE $2 END, o.cell, o.project::text,
		       o.state::text, o.cause::text, o.evidence::text, o.model_version, o.prompt_version,
		       o.pipeline_version, o.spent_pod_minutes, o.spent_tokens, o.spent_money_micros,
		       e.received_at
		  FROM "order" o
		  JOIN spec s ON s.id = o.spec_id AND s.version = o.spec_version
		  JOIN envelope e ON e.id = s.envelope_id
		 WHERE o.id = $1`, orderID, attempt).
		Scan(&t.OrderID, &t.Attempt, &t.Cell, &t.Project, &t.State, &cause, &evidence,
			&model, &prompt, &t.PipelineVersion, &t.SpentPodMinutes, &t.SpentTokens,
			&t.SpentMoney, &t.ReceivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, fmt.Errorf("no job %s in this cell", orderID)
	}
	if err != nil {
		return t, err
	}
	t.Cause, t.Evidence = deref(cause), deref(evidence)
	t.ModelVersion, t.PromptVersion = deref(model), deref(prompt)

	rows, err := pool.Query(ctx, `
		SELECT order_id::text, attempt, seq, cell, project::text, phase, outcome, round, detail,
		       started_at, duration_ms, cost_pod_minutes, cost_tokens, cost_money_micros,
		       evidence::text, model_version, prompt_version, pipeline_version
		  FROM job_span WHERE order_id = $1 AND attempt = $2 ORDER BY seq`, t.OrderID, t.Attempt)
	if err != nil {
		return t, err
	}
	defer rows.Close()
	for rows.Next() {
		var s Span
		var ev, mv, pv *string
		if err := rows.Scan(&s.OrderID, &s.Attempt, &s.Seq, &s.Cell, &s.Project, &s.Phase,
			&s.Outcome, &s.Round, &s.Detail, &s.StartedAt, &s.DurationMS, &s.CostPodMinutes,
			&s.CostTokens, &s.CostMoneyMicros, &ev, &mv, &pv, &s.PipelineVersion); err != nil {
			return t, err
		}
		s.Evidence, s.ModelVersion, s.PromptVersion = deref(ev), deref(mv), deref(pv)
		t.Spans = append(t.Spans, s)
	}
	if err := rows.Err(); err != nil {
		return t, err
	}

	// SP-B03-2's first SLO, measured on the trace it is a property of: from the envelope arriving
	// to the first phase that actually ran. A job whose phases were all skipped has no first
	// progress, and the field stays empty rather than reporting a zero.
	for _, s := range t.Spans {
		if s.Outcome == "ran" {
			d := s.StartedAt.Sub(t.ReceivedAt).Seconds()
			t.FirstProgress = &d
			break
		}
	}

	logs, err := PodLogsOf(ctx, pool, t.OrderID, t.Attempt)
	if err != nil {
		return t, err
	}
	t.Logs = logs
	return t, nil
}

// RecordPodLog hangs a pod's console on the job (SP-B03-4). The body stays where it was written —
// on the node, immediately — and this row is what makes it reachable from the order after the pod
// is gone.
func RecordPodLog(ctx context.Context, pool *pgxpool.Pool, l PodLog) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO pod_log (order_id, attempt, cell, project, node_id, content_hash, path, bytes)
		SELECT o.id, $2, o.cell, o.project, $3, $4, $5, $6 FROM "order" o WHERE o.id = $1
		ON CONFLICT (order_id, attempt, content_hash) DO NOTHING`,
		l.OrderID, l.Attempt, l.NodeID, l.ContentHash, l.Path, l.Bytes)
	return err
}

// PodLogsOf is the other direction, and the one AB-B03-4 reads: given a job, which logs are its
// evidence and where do they lie.
func PodLogsOf(ctx context.Context, pool *pgxpool.Pool, orderID string, attempt int) ([]PodLog, error) {
	rows, err := pool.Query(ctx, `
		SELECT order_id::text, attempt, node_id, content_hash, path, bytes, written_at
		  FROM pod_log WHERE order_id = $1 AND attempt = $2 ORDER BY written_at`, orderID, attempt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PodLog
	for rows.Next() {
		var l PodLog
		if err := rows.Scan(&l.OrderID, &l.Attempt, &l.NodeID, &l.ContentHash, &l.Path,
			&l.Bytes, &l.WrittenAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// FoldRejections takes the egress gate's journal into the display (SP-B02-5). It returns how many
// rows were new: folding the same journal twice adds nothing, which is what lets a node hand its
// journal over on every drain without anybody tracking what was carried before.
func FoldRejections(ctx context.Context, pool *pgxpool.Pool, nodeID string, rs []Rejection) (int, error) {
	added := 0
	for _, r := range rs {
		tag, err := pool.Exec(ctx, `
			INSERT INTO egress_rejection (id, cell, project, order_id, node_id, at, target, method, reason)
			SELECT $1, o.cell, o.project, o.id, $2, $3, $4, $5, $6 FROM "order" o WHERE o.id = $7
			ON CONFLICT (order_id, at, target, method) DO NOTHING`,
			ids.New(), nodeID, r.At, r.Target, r.Method, r.Reason, r.OrderID)
		if err != nil {
			return added, fmt.Errorf("rejection for order %s: %w", r.OrderID, err)
		}
		added += int(tag.RowsAffected())
	}
	return added, nil
}

// Cluster is what SP-B02-5 asks to be visible: not a line in a journal but a target that keeps
// being refused, with the project it was refused for and how often.
type Cluster struct {
	Project string    `json:"project"`
	Target  string    `json:"target"`
	Count   int       `json:"count"`
	Orders  int       `json:"orders"`
	First   time.Time `json:"first_seen"`
	Last    time.Time `json:"last_seen"`
	Reason  string    `json:"one_reason"`
}

// RejectionClusters is the display. Ordered by how often, because the cluster is the finding — a
// single refusal is a job with a wrong allowlist and twenty of them are an early warning.
func RejectionClusters(ctx context.Context, pool *pgxpool.Pool, cell string, window time.Duration) ([]Cluster, error) {
	rows, err := pool.Query(ctx, `
		SELECT project::text, target, count(*)::int, count(DISTINCT order_id)::int,
		       min(at), max(at), min(reason)
		  FROM egress_rejection
		 WHERE cell = $1 AND at >= now() - $2::interval
		 GROUP BY project, target
		 ORDER BY count(*) DESC, max(at) DESC`, cell, window.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Cluster
	for rows.Next() {
		var c Cluster
		if err := rows.Scan(&c.Project, &c.Target, &c.Count, &c.Orders, &c.First, &c.Last, &c.Reason); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Provenance is SP-K01-7's chain, resolved backwards: `order -> spec@version -> envelope ->
// channel_message_id`, and forwards to what the job actually produced.
type Provenance struct {
	OrderID          string     `json:"order_id"`
	Attempt          int        `json:"attempt"`
	State            string     `json:"state"`
	Cause            string     `json:"cause,omitempty"`
	Evidence         string     `json:"evidence,omitempty"`
	SpecID           string     `json:"spec_id"`
	SpecVersion      int64      `json:"spec_version"`
	Goal             string     `json:"goal"`
	EnvelopeID       string     `json:"envelope_id"`
	Channel          string     `json:"channel"`
	ChannelMessageID string     `json:"channel_message_id"`
	Sender           string     `json:"sender_external"`
	ReceivedAt       time.Time  `json:"received_at"`
	PatchHash        string     `json:"patch_hash,omitempty"`
	Target           string     `json:"target,omitempty"`
	ExecutedAt       *time.Time `json:"executed_at,omitempty"`
	ReceiptID        string     `json:"receipt_id,omitempty"`
	IssuedBy         string     `json:"issued_by,omitempty"`
	ModelVersion     string     `json:"model_version,omitempty"`
	PromptVersion    string     `json:"prompt_version,omitempty"`
	PipelineVersion  string     `json:"pipeline_version"`
	Spans            int        `json:"spans"`
	Logs             int        `json:"pod_logs"`
	Rejections       int        `json:"egress_rejections"`
}

// provenanceSQL is *one* statement, which is the requirement rather than a property of it:
// SP-K01-7 says backward resolution must be a query and not a reading of logs, and AB-K01-7 reads
// "patch -> job -> specification version -> envelope -> channel message in **one** query".
//
// The one parameter matches any link of the chain — the order, the patch's content hash, or the
// channel message the whole thing started as — so the direction the caller happens to be standing
// in does not change the query. The counts hang off lateral subqueries for the same reason: a
// second round trip to count the spans would make the sentence "in one query" false.
const provenanceSQL = `
SELECT o.id::text, o.attempt, o.state::text, coalesce(o.cause::text,''), coalesce(o.evidence::text,''),
       s.id::text, s.version, s.goal,
       e.id::text, e.channel, e.channel_message_id, e.sender_external, e.received_at,
       coalesce(ob.content_hash,''), coalesce(ob.target,''), ob.executed_at,
       coalesce(r.id::text,''), coalesce(r.issued_by,''),
       coalesce(o.model_version,''), coalesce(o.prompt_version,''), o.pipeline_version,
       spans.n, logs.n, rej.n
  FROM "order" o
  JOIN spec s     ON s.id = o.spec_id AND s.version = o.spec_version
  JOIN envelope e ON e.id = s.envelope_id
  LEFT JOIN outbox ob ON ob.order_id = o.id
  LEFT JOIN receipt r ON r.order_id = ob.order_id AND r.target = ob.target
                     AND r.content_hash = ob.content_hash
  LEFT JOIN LATERAL (SELECT count(*)::int AS n FROM job_span j WHERE j.order_id = o.id) spans ON true
  LEFT JOIN LATERAL (SELECT count(*)::int AS n FROM pod_log p WHERE p.order_id = o.id) logs ON true
  LEFT JOIN LATERAL (SELECT count(*)::int AS n FROM egress_rejection x WHERE x.order_id = o.id) rej ON true
 WHERE o.id::text = $1 OR ob.content_hash = $1 OR e.channel_message_id = $1
 ORDER BY ob.executed_at DESC NULLS LAST
 LIMIT 1`

// ProvenanceSQL exposes the statement so a check can run it itself and see that it is one — the
// same way OrderBySQL exposes the queue's ordering.
func ProvenanceSQL() string { return provenanceSQL }

// Resolve answers SP-K01-7 from whichever end of the chain the caller is holding.
func Resolve(ctx context.Context, pool *pgxpool.Pool, anchor string) (Provenance, error) {
	var p Provenance
	err := pool.QueryRow(ctx, provenanceSQL, anchor).Scan(
		&p.OrderID, &p.Attempt, &p.State, &p.Cause, &p.Evidence,
		&p.SpecID, &p.SpecVersion, &p.Goal,
		&p.EnvelopeID, &p.Channel, &p.ChannelMessageID, &p.Sender, &p.ReceivedAt,
		&p.PatchHash, &p.Target, &p.ExecutedAt, &p.ReceiptID, &p.IssuedBy,
		&p.ModelVersion, &p.PromptVersion, &p.PipelineVersion,
		&p.Spans, &p.Logs, &p.Rejections)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, fmt.Errorf("nothing in this cell answers to %q — an order, a patch hash or a channel message id resolves the chain (SP-K01-7)", anchor)
	}
	return p, err
}

// ProjectCost is SP-B03-6: cost visibility per project. What a project spent, and what it has
// reserved but not yet spent, because a bill that showed only the second would surprise somebody.
type ProjectCost struct {
	Project     string `json:"project"`
	Jobs        int    `json:"jobs"`
	PodMinutes  int64  `json:"spent_pod_minutes"`
	Tokens      int64  `json:"spent_tokens"`
	MoneyMicros int64  `json:"spent_money_micros"`
	HeldMinutes int64  `json:"reserved_pod_minutes"`
	HeldTokens  int64  `json:"reserved_tokens"`
	HeldMicros  int64  `json:"reserved_money_micros"`
	Acceptances int    `json:"jobs_with_evidence"`
}

// CostPerProject reads the cell's projects, or one of them.
func CostPerProject(ctx context.Context, pool *pgxpool.Pool, cell, project string) ([]ProjectCost, error) {
	// The reservations are aggregated before the join, not after it: a job holds one pot per scope
	// (V-04), and joining them row by row would multiply every one of the job's own numbers by how
	// many pots it happens to hold. A bill that counted four jobs where there is one would be worse
	// than no bill.
	rows, err := pool.Query(ctx, `
		SELECT o.project::text, count(*)::int,
		       coalesce(sum(o.spent_pod_minutes),0), coalesce(sum(o.spent_tokens),0),
		       coalesce(sum(o.spent_money_micros),0),
		       coalesce(sum(r.pod_minutes),0), coalesce(sum(r.tokens),0), coalesce(sum(r.money_micros),0),
		       count(*) FILTER (WHERE o.evidence IS NOT NULL)::int
		  FROM "order" o
		  LEFT JOIN LATERAL (
		    SELECT sum(pod_minutes) AS pod_minutes, sum(tokens) AS tokens,
		           sum(money_micros) AS money_micros
		      FROM budget_reservation b
		     WHERE b.order_id = o.id AND b.released_at IS NULL
		  ) r ON true
		 WHERE o.cell = $1 AND ($2 = '' OR o.project::text = $2)
		 GROUP BY o.project
		 ORDER BY sum(o.spent_money_micros) DESC
	`, cell, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectCost
	for rows.Next() {
		var c ProjectCost
		if err := rows.Scan(&c.Project, &c.Jobs, &c.PodMinutes, &c.Tokens, &c.MoneyMicros,
			&c.HeldMinutes, &c.HeldTokens, &c.HeldMicros, &c.Acceptances); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SampleQueue records one queue-depth sample, which is what decisions/alerts.md's slot 2 is read
// from. The moment is a parameter so a check can state a series instead of waiting twenty minutes
// for one.
func SampleQueue(ctx context.Context, pool *pgxpool.Pool, cell string, at time.Time) (int, error) {
	var depth int
	err := pool.QueryRow(ctx, `
		INSERT INTO queue_sample (cell, at, depth)
		SELECT $1, $2, count(*)::int FROM "order" WHERE cell = $1 AND state = 'queued'
		ON CONFLICT (cell, at) DO UPDATE SET depth = excluded.depth
		RETURNING depth`, cell, at).Scan(&depth)
	return depth, err
}

// QueueSeries reads the last n samples, oldest first — the shape the monotonicity rule reads.
func QueueSeries(ctx context.Context, pool *pgxpool.Pool, cell string, n int) ([]int, error) {
	rows, err := pool.Query(ctx, `
		SELECT depth FROM (
		  SELECT at, depth FROM queue_sample WHERE cell = $1 ORDER BY at DESC LIMIT $2
		) recent ORDER BY at`, cell, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var d int
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RejectionRates is slot 3's measurement: how many refusals in the last hour, and the mean of the
// hours before it.
func RejectionRates(ctx context.Context, pool *pgxpool.Pool, cell string, priorHours int, now time.Time) (last int, mean float64, err error) {
	err = pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE at >= $2::timestamptz - interval '1 hour')::int,
		       count(*) FILTER (WHERE at <  $2::timestamptz - interval '1 hour'
		                          AND at >= $2::timestamptz - make_interval(hours => $3 + 1))::float8 / $3
		  FROM egress_rejection WHERE cell = $1`, cell, now, priorHours).Scan(&last, &mean)
	return last, mean, err
}

// PotAtCap is slot 4's measurement: a daily pot that is nearly spent while its day is not.
type PotAtCap struct {
	Pot      string  `json:"pot"`
	Scope    string  `json:"scope"`
	Resource string  `json:"resource"`
	Fraction float64 `json:"fraction_of_cap"`
	Day      string  `json:"day"`
}

// PotsAtCap reads the daily pots of a cell against the fraction the ruling names. Only the two
// daily scopes are read: a pot without a day has no "prematurely" to be early against.
func PotsAtCap(ctx context.Context, pool *pgxpool.Pool, cell string, fraction float64) ([]PotAtCap, error) {
	rows, err := pool.Query(ctx, `
		SELECT id::text, scope, resource, share, day::text FROM (
		  SELECT id, scope, day, 'pod_minutes' AS resource,
		         CASE WHEN pod_minutes_cap  > 0 THEN pod_minutes_reserved::float8  / pod_minutes_cap  ELSE 0 END AS share,
		         cell FROM budget_pot
		  UNION ALL
		  SELECT id, scope, day, 'tokens',
		         CASE WHEN tokens_cap       > 0 THEN tokens_reserved::float8       / tokens_cap       ELSE 0 END,
		         cell FROM budget_pot
		  UNION ALL
		  SELECT id, scope, day, 'money',
		         CASE WHEN money_cap_micros > 0 THEN money_reserved_micros::float8 / money_cap_micros ELSE 0 END,
		         cell FROM budget_pot
		) p
		 WHERE cell = $1 AND day IS NOT NULL AND share >= $2
		 ORDER BY share DESC`, cell, fraction)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PotAtCap
	for rows.Next() {
		var p PotAtCap
		if err := rows.Scan(&p.Pot, &p.Scope, &p.Resource, &p.Fraction, &p.Day); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Counts is what R-D's occupancy table reads out of the cell in operation: jobs by state, and the
// work nodes they run on. Nothing here is computed from a planning value (SP-RD-3).
type Counts struct {
	Active    int `json:"active"`
	Queued    int `json:"queued"`
	Frozen    int `json:"frozen"`
	WorkNodes int `json:"work_nodes"`
}

// CellCounts reads them in one statement.
func CellCounts(ctx context.Context, pool *pgxpool.Pool, cell string) (Counts, error) {
	var c Counts
	err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE state = 'running')::int,
		       count(*) FILTER (WHERE state = 'queued')::int,
		       count(*) FILTER (WHERE state = 'frozen')::int,
		       (SELECT count(*)::int FROM node n WHERE n.cell = $1 AND n.role IN ('work','all'))
		  FROM "order" WHERE cell = $1`, cell).
		Scan(&c.Active, &c.Queued, &c.Frozen, &c.WorkNodes)
	return c, err
}

// AppendAudit writes one entry of B-03's trail. The period is the tenant's: SP-E07-2 gives the
// audit twelve months and adds "extendable per tenant", so a cell may name a longer one in its
// `retention` property and never a shorter one — a tenant able to shorten the trail could make an
// authority nobody granted disappear.
func AppendAudit(ctx context.Context, pool *pgxpool.Pool, cell, project, actor, action, subject string, detail map[string]any) error {
	body, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO audit (id, cell, project, actor, action, subject, detail, retain_until)
		SELECT $1, c.id, nullif($3,'')::uuid, $4, $5, $6, $7::jsonb,
		       now() + make_interval(days => greatest(365, coalesce((c.retention->>'audit_days')::int, 0)))
		  FROM cell c WHERE c.id = $2`,
		ids.New(), cell, project, actor, action, subject, body)
	return err
}

// AuditEntry is one row of the trail, as a display reads it.
type AuditEntry struct {
	At          time.Time `json:"at"`
	Actor       string    `json:"actor"`
	Action      string    `json:"action"`
	Subject     string    `json:"subject"`
	Detail      string    `json:"detail"`
	RetainUntil time.Time `json:"retain_until"`
}

// AuditOf reads the trail of a cell, newest first, optionally about one subject.
func AuditOf(ctx context.Context, pool *pgxpool.Pool, cell, subject string, limit int) ([]AuditEntry, error) {
	rows, err := pool.Query(ctx, `
		SELECT at, actor, action, subject, detail::text, retain_until
		  FROM audit WHERE cell = $1 AND ($2 = '' OR subject = $2)
		 ORDER BY at DESC LIMIT $3`, cell, subject, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.At, &e.Actor, &e.Action, &e.Subject, &e.Detail, &e.RetainUntil); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Effect is one entry of a node's outbox as the cell records it: what a job produced, which gate
// let it through, and the receipt that came back (SP-K03-1).
type Effect struct {
	OrderID          string     `json:"order_id"`
	Target           string     `json:"target"`
	ContentHash      string     `json:"content_hash"`
	PayloadRef       string     `json:"payload_ref"`
	RequiresRegister bool       `json:"requires_register"`
	ExecutedAt       *time.Time `json:"executed_at,omitempty"`
	IssuedBy         string     `json:"issued_by,omitempty"`
	ExternalID       string     `json:"external_id,omitempty"`
}

// FoldEffects takes a node's outbox into the state contract, so that the provenance chain reaches
// the patch and not only the job that made it (SP-K01-7).
//
// The outbox itself lies on the node as files, because that is where a domain key can be arbitrated
// by the kernel between two workers with no leader (decisions/gates-and-the-outbox.md). This is the
// other half of that arrangement: the record of what was executed belongs in the cell, or the chain
// `patch -> job -> spec -> envelope -> channel message` would break at its first link the moment a
// node is reinstalled. Folding twice adds nothing — the domain key is the primary key here too.
func FoldEffects(ctx context.Context, pool *pgxpool.Pool, effects []Effect) (int, error) {
	added := 0
	for _, e := range effects {
		tag, err := pool.Exec(ctx, `
			WITH j AS (SELECT id, cell, project FROM "order" WHERE id = $1)
			INSERT INTO outbox (order_id, target, content_hash, cell, project, payload_ref,
			                    requires_register, executed_at)
			SELECT j.id, $2, $3, j.cell, j.project, $4, $5, $6 FROM j
			ON CONFLICT (order_id, target, content_hash)
			DO UPDATE SET executed_at = coalesce(outbox.executed_at, excluded.executed_at)`,
			e.OrderID, e.Target, e.ContentHash, e.PayloadRef, e.RequiresRegister, e.ExecutedAt)
		if err != nil {
			return added, fmt.Errorf("effect %s %s: %w", e.OrderID, e.Target, err)
		}
		added += int(tag.RowsAffected())
		if e.IssuedBy == "" || e.ExecutedAt == nil {
			continue
		}
		// The receipt names the gate that executed the entry (SP-K03-1). Its own id is minted here
		// because the gate answers with the external id of what it did, not with a row id.
		if _, err := pool.Exec(ctx, `
			INSERT INTO receipt (id, cell, project, order_id, target, content_hash, issued_by, issued_at)
			SELECT $1, o.cell, o.project, o.order_id, o.target, o.content_hash, $2, $3
			  FROM outbox o
			 WHERE o.order_id = $4 AND o.target = $5 AND o.content_hash = $6
			   AND NOT EXISTS (SELECT 1 FROM receipt r WHERE r.order_id = o.order_id
			                     AND r.target = o.target AND r.content_hash = o.content_hash)`,
			ids.New(), e.IssuedBy, e.ExecutedAt, e.OrderID, e.Target, e.ContentHash); err != nil {
			return added, fmt.Errorf("receipt for %s %s: %w", e.OrderID, e.Target, err)
		}
	}
	return added, nil
}
