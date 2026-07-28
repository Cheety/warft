// Admission — the decision that turns a job into an admitted one, and the reservation that pays for
// it (V-04, E-08).
//
// SP-V04-3 is the sentence this file exists for: reserve in advance, do not count afterwards. A cost
// control that only strikes in the evaluation is none, so the pots are moved here — inside the same
// transaction that writes `new -> admitted` — and never at billing time. What is not spent comes back
// at the terminal state, and that half is a trigger of the state contract rather than code here: a
// job whose process died between its last report and its terminal write would otherwise hold its
// reservation for ever.
//
// The halt is read before any of it (SP-E08-3). Both of its paths are passed in already read, because
// the file path exists for the case in which the state database is not the thing answering — a step
// that fetched the halt from the database would have no halt exactly when it matters.
package statedb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Cheety/warft/platform/internal/budget"
	"github.com/Cheety/warft/platform/internal/ids"
)

// Decision is one admission, answered whole: admitted with what it reserved, or refused with why and
// with what the sender can do about it.
type Decision struct {
	OrderID  string
	Admitted bool
	Reserved budget.Pots

	// Exactly one of these is set when Admitted is false.
	Refusal *budget.Refusal
	Halted  *budget.Halted

	// AlreadyAdmitted: this order was admitted before and holds its reservation from then. Asking
	// twice is not asking for twice as much (SP-T01-7's principle, one layer down).
	AlreadyAdmitted bool
}

// Reason states the decision in one line, for a log or a channel reply.
func (d Decision) Reason() string {
	switch {
	case d.Halted != nil:
		return d.Halted.Error()
	case d.Refusal != nil:
		return d.Refusal.Error()
	case d.AlreadyAdmitted:
		return "already admitted; its reservation stands"
	default:
		return fmt.Sprintf("admitted: %d pod minutes, %d tokens, %d µ€ reserved in four pots (SP-V04-3)",
			d.Reserved.PodMinutes, d.Reserved.Tokens, d.Reserved.MoneyMicros)
	}
}

// admissionRow is what one admission needs to know about the job in front of it. All of it comes out
// of one query along SP-K01-7's provenance chain: order → spec@version → envelope.
type admissionRow struct {
	cell      string
	project   string
	state     string
	envelope  string
	channel   string
	authority string
	principal string
	demand    budget.Pots
	dailyCap  int64
}

// Admit is one admission decision, whole or not at all.
//
// The order stays in `new` when it is refused. That is deliberate: a pot refills at the turn of the
// day and a halt is lifted, so a refused job is one that has not been admitted yet, not one that has
// failed — and SP-K02-3 would demand a cause for a terminal state, which "the day is not over" is
// not.
func Admit(ctx context.Context, pool *pgxpool.Pool, orderID string, halt budget.Halt, now time.Time) (Decision, error) {
	d, err := admit(ctx, pool, orderID, halt, now)
	if err != nil || d.Admitted {
		return d, err
	}
	// A refusal is a decision and B-03 records decisions. It is written on a transaction of its own,
	// because the one that would have carried it is the one that was rolled back.
	if err := auditRefusal(ctx, pool, orderID, d); err != nil {
		return d, err
	}
	return d, nil
}

func admit(ctx context.Context, pool *pgxpool.Pool, orderID string, halt budget.Halt, now time.Time) (Decision, error) {
	if halt.Active(now) {
		refusal := halt.Refusal()
		return Decision{OrderID: orderID, Halted: &refusal}, nil
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return Decision{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a rollback after a commit is a no-op

	row, err := readOrder(ctx, tx, orderID)
	if err != nil {
		return Decision{}, err
	}
	switch row.state {
	case "new":
	case "admitted":
		return Decision{OrderID: orderID, Admitted: true, AlreadyAdmitted: true,
			Reserved: row.demand}, tx.Commit(ctx)
	default:
		return Decision{}, fmt.Errorf("order %s is in state %s — admission is the step out of `new` (K-02)", orderID, row.state)
	}

	caps := budget.Ruled()
	day := now.UTC().Format("2006-01-02")

	for _, scope := range budget.Scopes() {
		limit, err := caps.For(row.authority, scope)
		if err != nil {
			return Decision{}, err
		}
		potID, err := potFor(ctx, tx, row, scope, day, limit)
		if err != nil {
			return Decision{}, err
		}
		refusal, err := reserve(ctx, tx, potID, scope, row.authority, row.demand)
		if err != nil {
			return Decision{}, err
		}
		if refusal != nil {
			return Decision{OrderID: orderID, Refusal: refusal}, nil
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO budget_reservation (order_id, pot, cell, project, pod_minutes, tokens, money_micros)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			orderID, potID, row.cell, row.project,
			row.demand.PodMinutes, row.demand.Tokens, row.demand.MoneyMicros); err != nil {
			return Decision{}, err
		}
	}

	// The one cap that is not a pot: money is a daily cap per principal (SP-V04-1), and the pots are
	// per authority level, so the column is what holds the three levels together. Only a human
	// raises it, and by E-08 that is two people.
	refusal, err := checkDailyMoney(ctx, tx, row, day)
	if err != nil {
		return Decision{}, err
	}
	if refusal != nil {
		return Decision{OrderID: orderID, Refusal: refusal}, nil
	}

	// K-02: one field, one writer. `new -> admitted` is the control plane's.
	if _, err := tx.Exec(ctx, `SET LOCAL workpod.writer = 'control'`); err != nil {
		return Decision{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE "order" SET state = 'admitted' WHERE id = $1`, orderID); err != nil {
		return Decision{}, err
	}
	if err := audit(ctx, tx, row, orderID, "admission.admitted", map[string]any{
		"pod_minutes": row.demand.PodMinutes, "tokens": row.demand.Tokens,
		"money_micros": row.demand.MoneyMicros, "authority": row.authority,
	}); err != nil {
		return Decision{}, err
	}
	return Decision{OrderID: orderID, Admitted: true, Reserved: row.demand}, tx.Commit(ctx)
}

func readOrder(ctx context.Context, tx pgx.Tx, orderID string) (admissionRow, error) {
	var r admissionRow
	var budgetJSON []byte
	var principal *string
	var dailyCap *int64
	err := tx.QueryRow(ctx, `
		SELECT o.cell, o.project::text, o.state::text, e.id::text, e.channel, e.authority::text,
		       e.principal::text, s.budget, p.daily_money_cap_micros
		  FROM "order" o
		  JOIN spec s ON s.id = o.spec_id AND s.version = o.spec_version
		  JOIN envelope e ON e.id = s.envelope_id
		  LEFT JOIN principal p ON p.id = e.principal
		 WHERE o.id = $1
		   FOR NO KEY UPDATE OF o`, orderID).
		Scan(&r.cell, &r.project, &r.state, &r.envelope, &r.channel, &r.authority,
			&principal, &budgetJSON, &dailyCap)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, fmt.Errorf("no order %s", orderID)
	}
	if err != nil {
		return r, err
	}
	if principal == nil {
		// SP-T01-5: attribution is never automatic, and a job without a principal has no day pot to
		// draw on. Intake does not create one either; this is the belt to that braces.
		return r, fmt.Errorf("order %s carries no principal — nothing was attributed, so nothing can be reserved (SP-T01-5)", orderID)
	}
	r.principal = *principal
	if dailyCap != nil {
		r.dailyCap = *dailyCap
	}

	var b struct {
		PodMinutes  int64 `json:"pod_minutes"`
		Tokens      int64 `json:"tokens"`
		MoneyMicros int64 `json:"money_micros"`
	}
	if err := json.Unmarshal(budgetJSON, &b); err != nil {
		return r, fmt.Errorf("the spec of order %s carries no readable budget: %w", orderID, err)
	}
	if b.PodMinutes <= 0 {
		// A job that asks for nothing would reserve nothing and run anyway — the exact failure
		// SP-V04-3 exists to prevent.
		return r, fmt.Errorf("order %s asks for %d pod minutes; a job reserves what it means to spend (SP-V04-3)", orderID, b.PodMinutes)
	}
	if b.Tokens < 0 || b.MoneyMicros < 0 {
		// Reserving is an addition. A negative amount would subtract, and the pot's own CHECK only
		// catches a reservation that grows past its cap — so one job asking for minus a million
		// tokens would hand every other job in the pot a refund.
		return r, fmt.Errorf("order %s asks for %d tokens and %d µ€; a budget is not a refund (SP-V04-3)",
			orderID, b.Tokens, b.MoneyMicros)
	}
	r.demand = budget.Pots{PodMinutes: b.PodMinutes, Tokens: b.Tokens, MoneyMicros: b.MoneyMicros}
	return r, nil
}

// potFor finds the pot of one scope, creating it with OP-1's caps the first time it is needed. The
// caps are written once, at creation: a pot whose cap moved under a running reservation would be a
// cap.raise nobody performed.
func potFor(ctx context.Context, tx pgx.Tx, row admissionRow, scope budget.Scope, day string, limit budget.Pots) (string, error) {
	var (
		project  *string
		envelope *string
		channel  *string
		potDay   *string
	)
	switch scope {
	case budget.ScopeEnvelope:
		project, envelope = &row.project, &row.envelope
	case budget.ScopeProject:
		// A pot of one day, like the two daily scopes: what a job spends stays counted in the pot
		// (SP-V04-3 releases only the unspent part), so a standing project pot would become a
		// lifetime cap — which OP-1 does not rule and "against outliers" does not mean.
		project, potDay = &row.project, &day
	case budget.ScopePrincipalDay:
		potDay = &day
	case budget.ScopePrincipalChannelDay:
		potDay, channel = &day, &row.channel
	default:
		return "", fmt.Errorf("V-04 knows no pot scope %q", scope)
	}

	// ON CONFLICT DO NOTHING covers all four partial unique indexes at once: two admissions racing
	// for the same pot leave one insert standing, and the select below finds it either way.
	if _, err := tx.Exec(ctx, `
		INSERT INTO budget_pot (id, cell, project, principal, scope, authority, envelope, channel, day,
		                        pod_minutes_cap, tokens_cap, money_cap_micros)
		VALUES ($1, $2, $3::uuid, $4::uuid, $5, $6::authority_level, $7::uuid, $8, $9::date, $10, $11, $12)
		ON CONFLICT DO NOTHING`,
		ids.New(), row.cell, project, row.principal, string(scope), row.authority, envelope, channel, potDay,
		limit.PodMinutes, limit.Tokens, limit.MoneyMicros); err != nil {
		return "", err
	}

	var id string
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM budget_pot
		 WHERE cell = $1 AND scope = $2 AND authority = $3::authority_level
		   AND project IS NOT DISTINCT FROM $4::uuid
		   AND envelope IS NOT DISTINCT FROM $5::uuid
		   AND channel IS NOT DISTINCT FROM $6
		   AND day IS NOT DISTINCT FROM $7::date
		   AND principal IS NOT DISTINCT FROM $8::uuid
		 FOR UPDATE`,
		row.cell, string(scope), row.authority, project, envelope, channel, potDay, row.principal).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("the %s pot: %w", scope, err)
	}
	return id, nil
}

// reserve moves the three amounts into one pot, or refuses. The check is in the UPDATE's WHERE
// clause, so the decision and the write are one statement: between a SELECT that said yes and an
// UPDATE that acted on it, another admission fits.
func reserve(ctx context.Context, tx pgx.Tx, potID string, scope budget.Scope, level string, want budget.Pots) (*budget.Refusal, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE budget_pot SET
		  pod_minutes_reserved  = pod_minutes_reserved  + $2,
		  tokens_reserved       = tokens_reserved       + $3,
		  money_reserved_micros = money_reserved_micros + $4
		 WHERE id = $1
		   AND pod_minutes_reserved  + $2 <= pod_minutes_cap
		   AND tokens_reserved       + $3 <= tokens_cap
		   AND money_reserved_micros + $4 <= money_cap_micros`,
		potID, want.PodMinutes, want.Tokens, want.MoneyMicros)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 1 {
		return nil, nil
	}

	// Nothing moved, so something is short. Read the pot back to say which — a refusal that only
	// said "no" would be the silent truncation SP-V04-2 forbids, in politer words.
	var free, limit budget.Pots
	if err := tx.QueryRow(ctx, `
		SELECT pod_minutes_cap - pod_minutes_reserved, tokens_cap - tokens_reserved,
		       money_cap_micros - money_reserved_micros,
		       pod_minutes_cap, tokens_cap, money_cap_micros
		  FROM budget_pot WHERE id = $1`, potID).
		Scan(&free.PodMinutes, &free.Tokens, &free.MoneyMicros,
			&limit.PodMinutes, &limit.Tokens, &limit.MoneyMicros); err != nil {
		return nil, err
	}
	for _, resource := range want.Resources() {
		w, _ := want.Get(resource)
		f, _ := free.Get(resource)
		c, _ := limit.Get(resource)
		if w > f {
			r := budget.Exhausted(scope, level, resource, w, f, c)
			return &r, nil
		}
	}
	return nil, fmt.Errorf("the %s pot refused a reservation it has room for — the pot moved under the transaction", scope)
}

// checkDailyMoney is OP-1's ceiling above the pots: the sum of money reserved across all of a
// principal's day pots, whatever authority level they belong to, against the column a human raises.
func checkDailyMoney(ctx context.Context, tx pgx.Tx, row admissionRow, day string) (*budget.Refusal, error) {
	if row.dailyCap <= 0 {
		if row.demand.MoneyMicros == 0 {
			return nil, nil
		}
		// A principal whose daily cap is zero may spend no money at all. That is a refusal and not
		// an error: the cap is a number a human sets, and setting it is what unblocks the job
		// (SP-V04-2, E-08).
		r := budget.Exhausted(budget.ScopePrincipalDay, row.authority, "money",
			row.demand.MoneyMicros, 0, row.dailyCap)
		return &r, nil
	}
	var reserved int64
	if err := tx.QueryRow(ctx, `
		SELECT coalesce(sum(money_reserved_micros), 0) FROM budget_pot
		 WHERE scope = 'principal_day' AND cell = $3 AND principal = $1 AND day = $2::date`,
		row.principal, day, row.cell).Scan(&reserved); err != nil {
		return nil, err
	}
	if reserved <= row.dailyCap {
		return nil, nil
	}
	free := row.dailyCap - (reserved - row.demand.MoneyMicros)
	if free < 0 {
		free = 0
	}
	r := budget.Exhausted(budget.ScopePrincipalDay, row.authority, "money",
		row.demand.MoneyMicros, free, row.dailyCap)
	return &r, nil
}

// audit writes B-03's trail entry for an admission decision. Refusals are written on their own
// transaction, because the one that would have carried them is rolled back.
func audit(ctx context.Context, tx pgx.Tx, row admissionRow, orderID, action string, detail map[string]any) error {
	body, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit (id, cell, project, actor, action, subject, detail, retain_until)
		VALUES ($1, $2, $3::uuid, 'control', $4, $5, $6::jsonb, now() + interval '365 days')`,
		ids.New(), row.cell, row.project, action, "order:"+orderID, body)
	return err
}

// auditRefusal records a decision that admitted nothing. It reads the order's cell and project
// itself, because the transaction that knew them is gone by the time this runs.
func auditRefusal(ctx context.Context, pool *pgxpool.Pool, orderID string, d Decision) error {
	if d.AlreadyAdmitted {
		return nil
	}
	var cell, project string
	err := pool.QueryRow(ctx, `SELECT cell, project::text FROM "order" WHERE id = $1`, orderID).
		Scan(&cell, &project)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	detail := map[string]any{"reason": d.Reason()}
	action := "admission.refused"
	if d.Halted != nil {
		action = "admission.halted"
		detail["halt_source"] = d.Halted.Source
	} else if d.Refusal != nil {
		detail["cause"] = budget.Cause
		detail["pot"] = string(d.Refusal.Scope)
		detail["resource"] = d.Refusal.Resource
		detail["options"] = d.Refusal.Options
	}
	body, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO audit (id, cell, project, actor, action, subject, detail, retain_until)
		VALUES ($1, $2, $3::uuid, 'control', $4, $5, $6::jsonb, now() + interval '365 days')`,
		ids.New(), cell, project, action, "order:"+orderID, body)
	return err
}

// HaltState reads the first of SP-E08-3's two paths: the `halt` row of the state database, which is
// what the API writes. The second path is a file and is read by `internal/budget`, which needs no
// database — that is the whole point of it.
//
// An empty cell reads the halt of whichever cell this database serves; E-02 gives a cell one state
// database, so there is at most one row that matters.
func HaltState(ctx context.Context, pool *pgxpool.Pool, cell string) (budget.Halt, error) {
	var h budget.Halt
	query := `SELECT reason, set_by, set_at, expires_at FROM halt`
	args := []any{}
	if cell != "" {
		query += ` WHERE cell = $1`
		args = append(args, cell)
	}
	err := pool.QueryRow(ctx, query+` ORDER BY set_at DESC LIMIT 1`, args...).
		Scan(&h.Reason, &h.SetBy, &h.SetAt, &h.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return budget.Halt{}, nil
	}
	if err != nil {
		return budget.Halt{}, err
	}
	h.InForce, h.Source = true, "api"
	return h, nil
}

// SetHalt writes the first path: the `halt` row E-08's `halt.set` and `halt.renew` go through. One
// row per cell, rewritten rather than added to — the halt is a state, not a log of commands, and
// renewing it is setting it again with a fresh expiry (SP-E08-4).
func SetHalt(ctx context.Context, pool *pgxpool.Pool, cell, reason, by string, now time.Time) error {
	if reason == "" {
		return fmt.Errorf("a halt states a rationale — it is mandatory and reported over all adapters (SP-E08-2)")
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO halt (cell, reason, set_by, set_at, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (cell) DO UPDATE SET reason = $2, set_by = $3, set_at = $4, expires_at = $5`,
		cell, reason, by, now, now.Add(budget.HaltExpiry))
	return err
}

// ClearHalt is `halt.clear` on the first path. It is one person's to do and it is logged, like
// setting it (SP-E08-2); what it must never be is silent.
func ClearHalt(ctx context.Context, pool *pgxpool.Pool, cell string) error {
	_, err := pool.Exec(ctx, `DELETE FROM halt WHERE cell = $1`, cell)
	return err
}

// Pending is one job waiting for admission, as the fairness pass sees it.
type Pending struct {
	OrderID    string
	Principal  string
	PodMinutes int64
}

// AdmitPending is SP-V04-4: when more work is waiting than the bottleneck can carry, the bottleneck
// is shared out by weighted shares rather than by a queue per tenant. `bottleneck` is how many units
// of the scarcest resource are on offer this round — pod minutes here, because that is what stage 3
// runs out of; R-C says the scarcest resource changes, and the share-out does not care which one it
// is handed.
//
// Every principal gets its weighted share; whoever wants less takes only what it wants and the rest
// is shared out again. A heavy sender therefore gets a lot and never everything, and a light sender
// is never starved behind it. Jobs are taken in the order they arrived within a principal, so
// fairness between principals does not become unfairness inside one.
//
// Every principal carries the same weight here. Weighting across tenants is the second tenant's
// work (the boundary of AP-3.6), and `budget.Share` already takes the weight the day one is ruled —
// what is not in this signature is a weight nobody has decided.
func AdmitPending(ctx context.Context, pool *pgxpool.Pool, cell string, bottleneck int64,
	halt budget.Halt, now time.Time) ([]Decision, error) {

	pending, err := pendingOrders(ctx, pool, cell)
	if err != nil {
		return nil, err
	}
	wanted := map[string]int64{}
	for _, p := range pending {
		wanted[p.Principal] += p.PodMinutes
	}
	claims := make([]budget.Claim, 0, len(wanted))
	for principal, want := range wanted {
		claims = append(claims, budget.Claim{Principal: principal, Weight: 1, Want: want})
	}
	share := map[string]int64{}
	for _, g := range budget.Share(bottleneck, claims) {
		share[g.Principal] = g.Granted
	}

	decisions := make([]Decision, 0, len(pending))
	for _, p := range pending {
		if share[p.Principal] < p.PodMinutes {
			// This principal has had its share of the bottleneck for this round. The job stays in
			// `new` — it is not refused, it is not yet its turn.
			continue
		}
		d, err := Admit(ctx, pool, p.OrderID, halt, now)
		if err != nil {
			return decisions, err
		}
		if d.Admitted {
			share[p.Principal] -= p.PodMinutes
		}
		decisions = append(decisions, d)
	}
	return decisions, nil
}

func pendingOrders(ctx context.Context, pool *pgxpool.Pool, cell string) ([]Pending, error) {
	rows, err := pool.Query(ctx, `
		SELECT o.id::text, e.principal::text, (s.budget->>'pod_minutes')::bigint
		  FROM "order" o
		  JOIN spec s ON s.id = o.spec_id AND s.version = o.spec_version
		  JOIN envelope e ON e.id = s.envelope_id
		 WHERE o.cell = $1 AND o.state = 'new' AND e.principal IS NOT NULL
		 ORDER BY o.created_at, o.id`, cell)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Pending
	for rows.Next() {
		var p Pending
		if err := rows.Scan(&p.OrderID, &p.Principal, &p.PodMinutes); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RecordSpend is what the worker's report leaves behind before the job reaches a terminal state: how
// much of the reservation was actually used. The release then hands back the difference — the trigger
// in contract/schema.sql does it, so it happens whoever writes the terminal state (SP-K02-1).
func RecordSpend(ctx context.Context, pool *pgxpool.Pool, orderID string, spent budget.Pots) error {
	if spent.PodMinutes < 0 || spent.Tokens < 0 || spent.MoneyMicros < 0 {
		return fmt.Errorf("a spend is what was used, and %+v is not that", spent)
	}
	// Only before the terminal state. The release happens at that write and hands back what these
	// columns did not claim; a spend recorded afterwards would move a number the release already
	// read, and the pot would never learn of it.
	tag, err := pool.Exec(ctx, `
		UPDATE "order" SET spent_pod_minutes = $2, spent_tokens = $3, spent_money_micros = $4
		 WHERE id = $1 AND state NOT IN ('delivered','unproven','failed','cancelled')`,
		orderID, spent.PodMinutes, spent.Tokens, spent.MoneyMicros)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		var state string
		if err := pool.QueryRow(ctx, `SELECT state::text FROM "order" WHERE id = $1`, orderID).Scan(&state); err != nil {
			return fmt.Errorf("no order %s", orderID)
		}
		return fmt.Errorf("order %s is %s: its reservation was released at that write, so a spend recorded now would be counted nowhere (SP-V04-3)", orderID, state)
	}
	return nil
}

// Reservation is what one order still holds, summed over its pots' worth of rows. It is one row per
// pot in the database; here it is the amount, because the amount is the same in each.
type Reservation struct {
	OrderID  string
	Pots     int
	Held     budget.Pots
	Released bool
}

// Held reads back what an order holds. The acceptance script uses it, and so does anyone asking why
// a pot is full.
func Held(ctx context.Context, pool *pgxpool.Pool, orderID string) (Reservation, error) {
	r := Reservation{OrderID: orderID}
	var released int
	err := pool.QueryRow(ctx, `
		SELECT count(*), coalesce(max(pod_minutes),0), coalesce(max(tokens),0),
		       coalesce(max(money_micros),0), count(released_at)
		  FROM budget_reservation WHERE order_id = $1`, orderID).
		Scan(&r.Pots, &r.Held.PodMinutes, &r.Held.Tokens, &r.Held.MoneyMicros, &released)
	if err != nil {
		return r, err
	}
	r.Released = r.Pots > 0 && released == r.Pots
	return r, nil
}
