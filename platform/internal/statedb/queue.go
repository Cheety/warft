// The queue — in Postgres, with SKIP LOCKED, and with no second broker (SP-RB-7, SP-E02-2).
//
// R-B decides the order and `internal/scheduling` holds that decision; what lives here is the
// transaction that reads it out of the state contract. The ORDER BY below is generated from
// decisions/aging.md's three keys rather than written twice, because a queue ordered one way in Go
// and another way in SQL is one ordering and one bug.
//
// There is no exchange, no stream and no third-party queue. `"order"` is the queue, the state is a
// column of it, and `FOR UPDATE SKIP LOCKED` is what hands a row to exactly one taker — which is
// E-02's whole argument: one database is one thing to operate, and a broker beside it would be a
// second place the same rows exist.
package statedb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Cheety/warft/platform/internal/ids"
	"github.com/Cheety/warft/platform/internal/scheduling"
)

// Queued is one job waiting in the queue, as the scheduler reads it out.
type Queued struct {
	OrderID       string
	Project       string
	Priority      scheduling.Priority
	Repository    string
	Since         time.Time
	Overdue       bool
	Ratio         float64
	LocalityGroup string
}

// Waiting is the same row in the shape the ordering rules speak.
func (q Queued) Waiting() scheduling.Waiting {
	return scheduling.Waiting{OrderID: q.OrderID, Priority: q.Priority, Since: q.Since}
}

// Enqueue is `admitted -> queued`: the step between the reservation of V-04 and the queue of R-B.
// It is the control plane's write (K-02), and it is separate from admission on purpose — a job may
// be admitted, hold its pots, and still wait for a queue that is blocked by the pressure ladder.
func Enqueue(ctx context.Context, pool *pgxpool.Pool, orderID string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a rollback after a commit is a no-op

	if _, err := tx.Exec(ctx, `SET LOCAL workpod.writer = 'control'`); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE "order" SET state = 'queued' WHERE id = $1 AND state = 'admitted'`, orderID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		var state string
		if err := tx.QueryRow(ctx, `SELECT state::text FROM "order" WHERE id = $1`, orderID).Scan(&state); err != nil {
			return fmt.Errorf("no order %s", orderID)
		}
		return fmt.Errorf("order %s is %s; the queue is entered from `admitted` (K-02)", orderID, state)
	}
	return tx.Commit(ctx)
}

// orderByAging is decisions/aging.md's three keys as SQL, generated from SP-RB-2's bounds so the
// numbers have one source.
//
// `waited` is measured from `updated_at`, which is when the transition trigger stamped the row as
// it entered `queued`. What SP-RB-2 promises is a queue time, not a time since the envelope
// arrived, and a job that has not moved since it was queued has no later stamp than that one.
func orderByAging() string {
	var bound, rank strings.Builder
	bound.WriteString("CASE o.priority::text")
	rank.WriteString("CASE o.priority::text")
	for _, b := range scheduling.Bounds() {
		rank.WriteString(fmt.Sprintf(" WHEN '%s' THEN %d", b.Priority, b.Rank))
		if b.Unbounded() {
			// SP-RB-2's fourth row: `background` waits unbounded, so it is never overdue and NULL
			// is the honest value for "the bound it is past".
			continue
		}
		bound.WriteString(fmt.Sprintf(" WHEN '%s' THEN %g", b.Priority, b.Wait.Seconds()))
	}
	bound.WriteString(" END")
	rank.WriteString(" ELSE 99 END")

	waited := "extract(epoch FROM (now() - o.updated_at))"
	overdue := fmt.Sprintf("(%s IS NOT NULL AND %s > %s)", bound.String(), waited, bound.String())
	ratio := fmt.Sprintf("CASE WHEN %s THEN %s / %s ELSE 0 END", overdue, waited, bound.String())

	// Key 2 is zero for every job inside its bound, so key 3 — the priority column — is what
	// decides between them. That is the difference between aging and a second priority order.
	return fmt.Sprintf("ORDER BY %s DESC, %s DESC, %s ASC, o.updated_at ASC, o.id ASC",
		overdue, ratio, rank.String())
}

// OrderBySQL exposes the generated clause so a check can read the ordering the database will
// actually use, instead of a description of it.
func OrderBySQL() string { return orderByAging() }

// Claim hands the head of the queue to exactly one taker.
//
// The rows are selected `FOR UPDATE SKIP LOCKED` and the callback runs *inside* that transaction,
// which is what makes the guarantee real: a second Claim running at the same moment skips the
// locked rows and gets the next ones, and neither of them waits. If the callback returns an error
// the transaction is rolled back and the jobs are in the queue again — a taker that died holding a
// claim has claimed nothing.
//
// Turning a claim into a lease — an `attempt`, a `lease` row, a node and a deadline — is AP-6.2's
// work (SP-V02-1). What this owns is the order the jobs come out in and the promise that each one
// comes out once.
func Claim(ctx context.Context, pool *pgxpool.Pool, cell, localityGroup string, n int,
	fn func(context.Context, pgx.Tx, []Queued) error) error {

	if n <= 0 {
		return fmt.Errorf("a claim of %d jobs is not a claim", n)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a rollback after a commit is a no-op

	claimed, err := claimRows(ctx, tx, cell, localityGroup, n)
	if err != nil {
		return err
	}
	if fn != nil {
		if err := fn(ctx, tx, claimed); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func claimRows(ctx context.Context, tx pgx.Tx, cell, localityGroup string, n int) ([]Queued, error) {
	where := `o.cell = $1 AND o.state = 'queued'`
	args := []any{cell, n}
	if localityGroup != "" {
		// OP-8's sticky assignment: a worker asks for its own group first. An empty group means
		// the whole cell, which is what a single-node cell has.
		where += ` AND o.locality_group = $3`
		args = append(args, localityGroup)
	}
	// The repository is the order's locality group. OP-8 makes that group the sticky assignment
	// `repository -> node`, so it is the state contract's name for what SP-RC-6 calls a repository;
	// a second column holding the same string would be a second thing to keep true.
	query := fmt.Sprintf(`
		SELECT o.id::text, o.project::text, o.priority::text, o.locality_group, o.updated_at
		  FROM "order" o
		 WHERE %s
		 %s
		 FOR UPDATE OF o SKIP LOCKED
		 LIMIT $2`, where, orderByAging())

	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Queued
	for rows.Next() {
		var q Queued
		var priority string
		if err := rows.Scan(&q.OrderID, &q.Project, &priority, &q.LocalityGroup, &q.Since); err != nil {
			return nil, err
		}
		q.Priority = scheduling.Priority(priority)
		q.Repository = q.LocalityGroup
		w := q.Waiting()
		now := time.Now()
		q.Overdue, q.Ratio = w.Overdue(now), w.Ratio(now)
		out = append(out, q)
	}
	return out, rows.Err()
}

// Head reads the queue in its ruled order without claiming anything, for a report or a check. It
// takes no locks, so two callers see the same queue — which is exactly why it may not be used to
// hand work out.
func Head(ctx context.Context, pool *pgxpool.Pool, cell string, n int) ([]Queued, error) {
	query := fmt.Sprintf(`
		SELECT o.id::text, o.project::text, o.priority::text, o.locality_group, o.updated_at
		  FROM "order" o
		 WHERE o.cell = $1 AND o.state = 'queued'
		 %s
		 LIMIT $2`, orderByAging())
	rows, err := pool.Query(ctx, query, cell, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := time.Now()
	var out []Queued
	for rows.Next() {
		var q Queued
		var priority string
		if err := rows.Scan(&q.OrderID, &q.Project, &priority, &q.LocalityGroup, &q.Since); err != nil {
			return nil, err
		}
		q.Priority = scheduling.Priority(priority)
		w := q.Waiting()
		q.Overdue, q.Ratio = w.Overdue(now), w.Ratio(now)
		out = append(out, q)
	}
	return out, rows.Err()
}

// Running is what is running in a cell, in the queue's own order. The freeze rung reads it
// backwards: SP-RC-3 freezes the lowest priority first, which is the job a fresh queue would have
// served last (decisions/escalation-ladder.md).
func Running(ctx context.Context, pool *pgxpool.Pool, cell string) ([]Queued, error) {
	query := fmt.Sprintf(`
		SELECT o.id::text, o.project::text, o.priority::text, o.locality_group, o.updated_at
		  FROM "order" o
		 WHERE o.cell = $1 AND o.state = 'running'
		 %s`, orderByAging())
	rows, err := pool.Query(ctx, query, cell)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Queued
	for rows.Next() {
		var q Queued
		var priority string
		if err := rows.Scan(&q.OrderID, &q.Project, &priority, &q.LocalityGroup, &q.Since); err != nil {
			return nil, err
		}
		q.Priority = scheduling.Priority(priority)
		q.Repository = q.LocalityGroup
		out = append(out, q)
	}
	return out, rows.Err()
}

// RecordEscalation is SP-RC-3's last rung as far as this stage takes it: the trail says which cell
// escalated, to which rung, and which signals demanded it. B-03's four waking alerts are AP-3.8's,
// and this is the record the alert will read (decisions/escalation-ladder.md).
//
// It carries no project, because pressure is a property of a node and not of anyone's work. The
// audit table needs one, so the escalation is written against the project of the job that was
// frozen for it, or — when nothing was running — against every project's ancestor, the cell's own
// row. A cell with no project at all cannot escalate, and cannot have pods either.
func RecordEscalation(ctx context.Context, pool *pgxpool.Pool, cell, rung string, signals []string) error {
	var project string
	err := pool.QueryRow(ctx, `SELECT id::text FROM project WHERE cell = $1 ORDER BY id LIMIT 1`, cell).Scan(&project)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("cell %s holds no project, so it holds no pods and nothing to escalate about", cell)
		}
		return err
	}
	detail, err := json.Marshal(map[string]any{"rung": rung, "signals": signals})
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO audit (id, cell, project, actor, action, subject, detail, retain_until)
		VALUES ($1, $2, $3::uuid, 'control', 'scheduler.escalated', $4, $5::jsonb, now() + interval '365 days')`,
		ids.New(), cell, project, "cell:"+cell, detail)
	return err
}

// Freeze is SP-RB-4: preempting means freezing, not aborting. The pod loses its slot and keeps its
// state, its attempt and everything it has spent — `running -> frozen` is a transition of the state
// contract and `frozen -> running` is the way back out of it.
//
// The writer is the worker, because the freeze happens on the node where the pod is (K-02: one
// field, one writer). The scheduler decides it; the worker performs it and writes it.
func Freeze(ctx context.Context, pool *pgxpool.Pool, orderID, reason string) error {
	return moveRunning(ctx, pool, orderID, "running", "frozen", reason)
}

// Thaw is the way back: `frozen -> running`. A preemption that could never end would be a kill with
// a gentler name (the same argument the runner's own state machine is built on).
func Thaw(ctx context.Context, pool *pgxpool.Pool, orderID, reason string) error {
	return moveRunning(ctx, pool, orderID, "frozen", "running", reason)
}

func moveRunning(ctx context.Context, pool *pgxpool.Pool, orderID, from, to, reason string) error {
	if reason == "" {
		return fmt.Errorf("a freeze states why: the duty officer reading the trail is the reason B-03 exists")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a rollback after a commit is a no-op

	if _, err := tx.Exec(ctx, `SET LOCAL workpod.writer = 'worker'`); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx,
		`UPDATE "order" SET state = $3::order_state WHERE id = $1 AND state = $2::order_state`,
		orderID, from, to)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		var state string
		if err := tx.QueryRow(ctx, `SELECT state::text FROM "order" WHERE id = $1`, orderID).Scan(&state); err != nil {
			return fmt.Errorf("no order %s", orderID)
		}
		return fmt.Errorf("order %s is %s, not %s", orderID, state, from)
	}

	var cell, project string
	if err := tx.QueryRow(ctx, `SELECT cell, project::text FROM "order" WHERE id = $1`, orderID).
		Scan(&cell, &project); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit (id, cell, project, actor, action, subject, detail, retain_until)
		VALUES ($1, $2, $3::uuid, 'control', $4, $5, jsonb_build_object('reason', $6::text), now() + interval '365 days')`,
		ids.New(), cell, project, "scheduler."+to, "order:"+orderID, reason); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RecordPhase is SP-RC-6: peak RSS and runtime per repository and phase, folded into the profile
// admission later decides from. One call is one finished phase of one job.
//
// Both aggregates are maxima. Admission is asking whether a job fits, and a mean answers a
// different question — half the runs of a repository whose mean fits do not fit.
func RecordPhase(ctx context.Context, pool *pgxpool.Pool, cell, project, repository string,
	phase scheduling.Phase, peakRSS int64, runtime time.Duration) error {

	if repository == "" {
		return fmt.Errorf("a profile is per repository and phase (SP-RC-6); this observation names no repository")
	}
	if peakRSS < 0 {
		return fmt.Errorf("a peak RSS of %d bytes is not a measurement", peakRSS)
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO phase_profile (cell, project, repository, phase, runs, peak_rss_bytes, runtime_ms)
		VALUES ($1, $2::uuid, $3, $4, 1, $5, $6)
		ON CONFLICT (cell, project, repository, phase) DO UPDATE SET
		  runs           = phase_profile.runs + 1,
		  peak_rss_bytes = greatest(phase_profile.peak_rss_bytes, excluded.peak_rss_bytes),
		  runtime_ms     = greatest(phase_profile.runtime_ms, excluded.runtime_ms),
		  updated_at     = now()`,
		cell, project, repository, string(phase), peakRSS, runtime.Milliseconds())
	return err
}

// ProfileOf reads a repository's history in one phase. A repository nobody has run reports a
// profile of zero runs, which is not an error — it is the answer, and Decide knows what to do with
// it (admit on pressure alone until there are three).
func ProfileOf(ctx context.Context, pool *pgxpool.Pool, cell, project, repository string,
	phase scheduling.Phase) (scheduling.Profile, error) {

	p := scheduling.Profile{Repository: repository, Phase: phase}
	var runtimeMS int64
	err := pool.QueryRow(ctx, `
		SELECT runs, peak_rss_bytes, runtime_ms FROM phase_profile
		 WHERE cell = $1 AND project = $2::uuid AND repository = $3 AND phase = $4`,
		cell, project, repository, string(phase)).Scan(&p.Runs, &p.PeakRSS, &runtimeMS)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return p, nil
		}
		return p, err
	}
	p.Runtime = time.Duration(runtimeMS) * time.Millisecond
	return p, nil
}
