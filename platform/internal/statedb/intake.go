// Intake — the envelope becoming state, and the one job it may produce (T-01, K-01).
//
// Everything here speaks the state contract's own vocabulary: the enumerations are spelled as
// contract/schema.sql spells them, and no message of contract/platform.proto appears. That is the
// module contract holding (decisions/module-dependencies.md): this is a step, the control plane is
// a role, and the role is what translates between the wire and the database.
//
// The whole of an intake is one transaction. SP-T01-7's key does its work at its edge: the second
// delivery of a message conflicts on UNIQUE (channel, idempotency), writes nothing, and is answered
// with the job the first delivery produced.

package statedb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Cheety/warft/platform/internal/ids"
)

// SocketDir is the state database's only door: listen_addresses is empty and the Unix socket lives
// here (workpod-db.service). The connection is not configured anywhere — it follows from the disk
// layout, and SP-A04-4 leaves no room for a sixth boot value to point it elsewhere.
const SocketDir = "/run/workpod-db"

// Database is the one state database of a cell (SP-E02-2, E-02).
const Database = "workpod"

// SchemaPath is where the image carries contract/schema.sql. The program does not hold its own copy
// of the DDL: two copies of a contract are two contracts, and image/build.sh stages this one from
// the file the acceptance scripts check.
const SchemaPath = "/usr/share/workpod/schema.sql"

// DSN is the connection the control plane makes to its own state database. The user is `postgres`
// because peer authentication on the socket is the credential: the kernel states who is knocking
// (workpod-db.service), and statedb.Init maps the caller onto this role.
func DSN(database string) string {
	return fmt.Sprintf("host=%s user=postgres dbname=%s", SocketDir, database)
}

// Open connects to the state database. pgxpool connects lazily, so this returns before Postgres is
// necessarily up — which is deliberate: the control plane serves A-04's register step whether or
// not the queue is reachable yet, and an intake that arrives too early is refused by name rather
// than by a plane that never started.
func Open(ctx context.Context) (*pgxpool.Pool, error) {
	dsn := os.Getenv("WORKPOD_DB_DSN")
	if dsn == "" {
		dsn = DSN(Database)
	}
	return pgxpool.New(ctx, dsn)
}

// Ensure creates the state database and loads the contract into it, once. Both steps are
// idempotent: a node that has already been through them does nothing on the next boot.
func Ensure(ctx context.Context) error {
	maintenance := os.Getenv("WORKPOD_DB_MAINTENANCE_DSN")
	if maintenance == "" {
		maintenance = DSN("postgres")
	}
	admin, err := pgx.Connect(ctx, maintenance)
	if err != nil {
		return fmt.Errorf("the state database is not reachable on %s: %w", SocketDir, err)
	}
	defer admin.Close(ctx)

	var exists bool
	if err := admin.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", Database).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		// CREATE DATABASE cannot run inside a transaction and takes no parameter for its name;
		// the name is a constant of this program, never anything a caller supplies.
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+Database); err != nil {
			return err
		}
	}

	pool, err := Open(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	var loaded bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('public.envelope') IS NOT NULL").Scan(&loaded); err != nil {
		return err
	}
	if loaded {
		return nil
	}
	ddl, err := os.ReadFile(schemaPath())
	if err != nil {
		return fmt.Errorf("no state contract at %s — the image carries contract/schema.sql as itself: %w", schemaPath(), err)
	}
	// No arguments, so pgx uses the simple protocol and the whole file — BEGIN, the DDL, COMMIT —
	// runs as one statement batch, the way psql would run it.
	if _, err := pool.Exec(ctx, string(ddl)); err != nil {
		return fmt.Errorf("loading %s: %w", schemaPath(), err)
	}
	return nil
}

func schemaPath() string {
	if p := os.Getenv("WORKPOD_SCHEMA"); p != "" {
		return p
	}
	return SchemaPath
}

// Attachment is one accepted attachment, already checked against OP-5 and filed under its content
// hash. The row holds the reference; the bytes lie in the store (SP-K01-6).
type Attachment struct {
	ContentHash string
	MediaType   string
	SizeBytes   int64
}

// Acceptance is Q-01's "how you recognize it". Without at least one there is no job (SP-Q01-6).
type Acceptance struct {
	ID               string
	Statement        string
	RequiredEvidence string
	MachineCheckable bool
}

// Assumption is an object, not prose: it stands individually and is revoked individually
// (SP-Q01-5).
type Assumption struct {
	ID        string
	Statement string
}

// Job is what a captain would decide and Q-01 would derive, stated by hand until AP-5.1 and AP-5.5
// (decisions/jobs-by-hand.md).
type Job struct {
	Goal        string
	Acceptance  []Acceptance
	Assumptions []Assumption
	BoundsJSON  string
	BudgetJSON  string
	RiskClass   string

	Class           string
	ImageHash       string
	PipelineVersion string
	LocalityGroup   string
	Priority        string
	BudgetShareJSON string
}

// Envelope is what intake accepts, in the state contract's own words.
type Envelope struct {
	ID             string
	Cell           string
	Project        string
	Channel        string
	MessageID      string
	SenderExternal string
	Authority      string
	Text           string
	Attachments    []Attachment
	Thread         string
	ReceivedAt     time.Time
	Idempotency    string
	Platform       string
	Job            *Job
}

// Result is what intake answers with.
type Result struct {
	EnvelopeID string
	OrderID    string
	// Deduplicated: this message had been delivered before, so this is the job it produced then
	// and not a second one (SP-T01-7).
	Deduplicated bool
	// FirstContact: no identity_link stands behind the sender, so nothing was attributed and no
	// job was created (SP-T01-5). The invitation that follows is AP-5.7's.
	FirstContact bool
}

// retention is E-07's, as contract/schema.sql states it on envelope.purge_after: the payload is
// purged after 30 days and the reference remains.
const retention = 30 * 24 * time.Hour

// Submit is one intake, whole or not at all.
func Submit(ctx context.Context, pool *pgxpool.Pool, e Envelope) (Result, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a rollback after a commit is a no-op

	// Attribution is never automatic (SP-T01-5): the link is looked up, never created here.
	var principal *string
	err = tx.QueryRow(ctx, `SELECT principal::text FROM identity_link WHERE external_id = $1`,
		e.SenderExternal).Scan(&principal)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Result{}, err
	}

	hashes := make([]string, 0, len(e.Attachments))
	for _, a := range e.Attachments {
		hashes = append(hashes, a.ContentHash)
	}

	var inserted string
	err = tx.QueryRow(ctx, `
		INSERT INTO envelope (id, cell, project, channel, channel_message_id, sender_external,
		                      principal, authority, text_body, attachments, thread, received_at,
		                      idempotency, platform, purge_after)
		VALUES ($1, $2, $3, $4, $5, $6, $7::uuid, $8::authority_level, $9, $10, NULLIF($11, ''),
		        $12, $13, $14, $15)
		ON CONFLICT (channel, idempotency) DO NOTHING
		RETURNING id::text`,
		e.ID, e.Cell, e.Project, e.Channel, e.MessageID, e.SenderExternal,
		principal, e.Authority, e.Text, hashes, e.Thread, e.ReceivedAt,
		e.Idempotency, e.Platform, e.ReceivedAt.Add(retention)).Scan(&inserted)

	if errors.Is(err, pgx.ErrNoRows) {
		// SP-T01-7, doing exactly what it is for. The message was delivered before; the answer is
		// the job it produced then, and this transaction writes nothing.
		res, err := existing(ctx, tx, e.Channel, e.Idempotency)
		if err != nil {
			return Result{}, err
		}
		return res, tx.Commit(ctx)
	}
	if err != nil {
		return Result{}, err
	}

	// Content-addressed, so the same bytes are the same object: a second envelope carrying an
	// attachment the store already holds finds the row already there, and that is a hit.
	for _, a := range e.Attachments {
		if _, err := tx.Exec(ctx, `
			INSERT INTO attachment (content_hash, cell, project, media_type, size_bytes, executable)
			VALUES ($1, $2, $3, $4, $5, false)
			ON CONFLICT (content_hash) DO NOTHING`,
			a.ContentHash, e.Cell, e.Project, a.MediaType, a.SizeBytes); err != nil {
			return Result{}, err
		}
	}

	switch {
	case e.Job == nil:
		// An envelope is an envelope. No acceptance criterion, no job (SP-Q01-6).
		return Result{EnvelopeID: e.ID}, tx.Commit(ctx)
	case principal == nil:
		// First contact produces an invitation, not a job, and never an attribution (SP-T01-5).
		return Result{EnvelopeID: e.ID, FirstContact: true}, tx.Commit(ctx)
	}

	orderID, err := createJob(ctx, tx, e, *principal)
	if err != nil {
		return Result{}, err
	}
	return Result{EnvelopeID: e.ID, OrderID: orderID}, tx.Commit(ctx)
}

// existing answers a redelivery: the envelope of the first delivery, and the job it produced. The
// job is found through the provenance chain SP-K01-7 names — order → spec@version → envelope — so
// it is a query and not a second key to keep in step.
func existing(ctx context.Context, tx pgx.Tx, channel, idempotency string) (Result, error) {
	res := Result{Deduplicated: true}
	if err := tx.QueryRow(ctx,
		`SELECT id::text FROM envelope WHERE channel = $1 AND idempotency = $2`,
		channel, idempotency).Scan(&res.EnvelopeID); err != nil {
		return Result{}, err
	}
	var orderID *string
	err := tx.QueryRow(ctx, `
		SELECT o.id::text
		  FROM "order" o
		  JOIN spec s ON s.id = o.spec_id AND s.version = o.spec_version
		 WHERE s.envelope_id = $1
		 ORDER BY o.created_at
		 LIMIT 1`, res.EnvelopeID).Scan(&orderID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Result{}, err
	}
	if orderID != nil {
		res.OrderID = *orderID
	}
	return res, nil
}

func createJob(ctx context.Context, tx pgx.Tx, e Envelope, principal string) (string, error) {
	j := e.Job
	if len(j.Acceptance) == 0 {
		return "", fmt.Errorf("no acceptance criterion, no job (SP-Q01-6)")
	}

	specID := ids.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO spec (id, version, cell, project, envelope_id, goal, bounds, budget, risk_class)
		VALUES ($1, 1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8::reversibility)`,
		specID, e.Cell, e.Project, e.ID, j.Goal, j.BoundsJSON, j.BudgetJSON, j.RiskClass); err != nil {
		return "", err
	}

	for _, a := range j.Acceptance {
		if _, err := tx.Exec(ctx, `
			INSERT INTO acceptance (id, cell, project, spec_id, spec_version, statement,
			                        required_evidence, machine_checkable)
			VALUES ($1, $2, $3, $4, 1, $5, $6::evidence_class, $7)`,
			a.ID, e.Cell, e.Project, specID, a.Statement, a.RequiredEvidence, a.MachineCheckable); err != nil {
			return "", err
		}
	}
	for _, a := range j.Assumptions {
		if _, err := tx.Exec(ctx, `
			INSERT INTO assumption (id, cell, project, spec_id, spec_version, statement)
			VALUES ($1, $2, $3, $4, 1, $5)`,
			a.ID, e.Cell, e.Project, specID, a.Statement); err != nil {
			return "", err
		}
	}

	// The locality group is a row of its own (V-02): the order references it, so a group a node
	// could be placed against has to exist before a job names it.
	if _, err := tx.Exec(ctx, `
		INSERT INTO locality_group (id, cell) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		j.LocalityGroup, e.Cell); err != nil {
		return "", err
	}

	orderID := ids.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO "order" (id, cell, project, spec_id, spec_version, class, platform, image_hash,
		                     pipeline_version, authority_ref, budget_share, locality_group,
		                     idempotency, priority)
		VALUES ($1, $2, $3, $4, 1, $5::resource_class, $6, $7, $8, $9, $10::jsonb, $11, $12,
		        $13::priority)`,
		orderID, e.Cell, e.Project, specID, j.Class, e.Platform, j.ImageHash,
		j.PipelineVersion, authorityRef(e.ID), j.BudgetShareJSON, j.LocalityGroup,
		orderKey(e), j.Priority); err != nil {
		return "", err
	}
	return orderID, nil
}

// authorityRef is decisions/jobs-by-hand.md: until a lease mints a Biscuit (SP-K04-5, AP-6.2), the
// reference names the envelope whose channel decided the level — the grant's origin, resolvable in
// one query to the level, the principal, the project and the cell.
func authorityRef(envelopeID string) string { return "envelope:" + envelopeID }

// orderKey carries the envelope's idempotency key into the order, qualified by the channel. The
// envelope's key is unique within its channel (UNIQUE (channel, idempotency)); the order's is
// unique within its project (UNIQUE (project, idempotency, attempt)), and a project spans
// channels — so without the channel two platforms could collide on a key neither of them chose.
func orderKey(e Envelope) string { return e.Channel + ":" + e.Idempotency }
