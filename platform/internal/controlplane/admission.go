// Admission in the plane — the halt on both of its paths, the decision at intake, and the three
// commands a duty officer runs (V-04, E-08).
//
// The decision itself is `internal/statedb`'s: it is a transaction over the pots and the order's
// state, and the step that owns the state contract owns it. What lives here is the role's half —
// reading both halt paths, answering the sender, and the entry points a person uses when the plane
// is not the thing they can reach.

package controlplane

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	workpodv1 "github.com/Cheety/warft/platform/api/workpodv1"
	"github.com/Cheety/warft/platform/internal/budget"
	"github.com/Cheety/warft/platform/internal/statedb"
)

// halt reads both of SP-E08-3's paths and returns the one that stops admission.
//
// The file is read first and without the database, because the file exists for the case in which the
// database or the API is the part that is broken. A database that cannot be asked while the file
// says nothing is not "no halt": the caller is told, and admission refuses rather than guessing.
func haltNow(ctx context.Context, pool *pgxpool.Pool, cell string, now time.Time) (budget.Halt, error) {
	var (
		api   budget.Halt
		dbErr error
	)
	if pool != nil {
		api, dbErr = statedb.HaltState(ctx, pool, cell)
	} else {
		dbErr = fmt.Errorf("the state database is not reachable, so the field in admission cannot be read")
	}
	return budget.ReadHalt(budget.HaltFilePath(), api, dbErr, now)
}

// admitAtIntake is SP-V04-3 where it belongs: the job is admitted — and its pots reserved — while
// intake is still on the line, not later when something bills it.
//
// A refusal is not an error of the intake: the envelope was accepted and stored, and it is the job
// that was not admitted. The ack carries the refusal, its options and its cause, which is SP-V04-2's
// "a reply with options instead of a silent truncation".
func (s *server) admitAtIntake(ctx context.Context, pool *pgxpool.Pool, cell, orderID string, ack *workpodv1.EnvelopeAck) {
	halt, err := haltNow(ctx, pool, cell, time.Now())
	if err != nil {
		// Not a refusal: nothing is exhausted and nobody halted anything. The job is not admitted
		// because admission could not be decided, and the reply says that rather than blaming a
		// pot — it carries no `cause`, because `budget.exhausted` would be the wrong one.
		ack.Refusal = "admission could not be decided: " + err.Error()
		logAdmission(orderID, ack)
		return
	}
	d, err := statedb.Admit(ctx, pool, orderID, halt, time.Now())
	if err != nil {
		ack.Refusal = "admission could not be decided: " + err.Error()
		logAdmission(orderID, ack)
		return
	}
	ack.Admitted = d.Admitted
	if !d.Admitted {
		ack.Refusal = d.Reason()
		if d.Refusal != nil {
			ack.Options = d.Refusal.Options
			ack.Cause = budget.Cause
		}
	}
	logAdmission(orderID, ack)
}

func logAdmission(orderID string, ack *workpodv1.EnvelopeAck) {
	if ack.GetAdmitted() {
		log.Printf("admission %s: admitted, reservation held in four pots (SP-V04-3)", orderID)
		return
	}
	log.Printf("admission %s: not admitted — %s", orderID, ack.GetRefusal())
}

// Command is `workpod control <subcommand>`: what a duty officer and an operator run against a cell
// whose plane may or may not be serving.
//
// `admit` is deliberately a command and not only a code path inside the plane. AB-E08-3 asks for the
// halt to take effect with the API switched off, and a check of that has to be able to try to admit
// something without an API — the same reason the halt has a second path at all.
func Command(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("control admit · control halt · control spend")
	}
	switch args[0] {
	case "admit":
		return admitCommand(args[1:], out)
	case "halt":
		return haltCommand(args[1:], out)
	case "spend":
		return spendCommand(args[1:], out)
	}
	return fmt.Errorf("`workpod control %s` is not a command; there are admit, halt and spend", args[0])
}

func admitCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("control admit", flag.ContinueOnError)
	order := fs.String("order", "", "the order to admit")
	cell := fs.String("cell", "", "admit what is waiting in this cell, sharing out the bottleneck (SP-V04-4)")
	bottleneck := fs.Int64("bottleneck", 0, "how many pod minutes are on offer this round")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*order == "") == (*cell == "") {
		return fmt.Errorf("admit one order (--order) or a cell's waiting jobs (--cell --bottleneck), not both")
	}

	ctx := context.Background()
	pool, err := statedb.Open(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	now := time.Now()
	halt, err := haltNow(ctx, pool, *cell, now)
	if err != nil {
		return err
	}

	if *order != "" {
		d, err := statedb.Admit(ctx, pool, *order, halt, now)
		if err != nil {
			return err
		}
		return report(out, d)
	}

	if *bottleneck <= 0 {
		return fmt.Errorf("a share-out needs a bottleneck: --bottleneck is how many pod minutes are on offer")
	}
	decisions, err := statedb.AdmitPending(ctx, pool, *cell, *bottleneck, halt, now)
	if err != nil {
		return err
	}
	for _, d := range decisions {
		if err := report(out, d); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "%d of the waiting jobs admitted against %d pod minutes of bottleneck (SP-V04-4)\n",
		admittedCount(decisions), *bottleneck)
	return nil
}

func admittedCount(decisions []statedb.Decision) int {
	n := 0
	for _, d := range decisions {
		if d.Admitted {
			n++
		}
	}
	return n
}

func report(out io.Writer, d statedb.Decision) error {
	body := map[string]any{
		"order":    d.OrderID,
		"admitted": d.Admitted,
		"reason":   d.Reason(),
	}
	if d.Refusal != nil {
		body["cause"] = budget.Cause
		body["pot"] = string(d.Refusal.Scope)
		body["resource"] = d.Refusal.Resource
		body["options"] = d.Refusal.Options
	}
	if d.Halted != nil {
		body["halt_source"] = d.Halted.Source
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(body)
}

// haltCommand is the second path, operated. Setting the halt is writing the file; renewing it is
// touching it; clearing it is deleting it (decisions/halt-file.md). None of the three needs the API,
// which is the case they exist for.
func haltCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("control halt", flag.ContinueOnError)
	set := fs.Bool("set", false, "halt the cell — a rationale is mandatory (SP-E08-2)")
	renew := fs.Bool("renew", false, "renew the halt for another 60 minutes (SP-E08-4)")
	clear := fs.Bool("clear", false, "lift the halt")
	reason := fs.String("reason", "", "why — reported over all adapters")
	by := fs.String("by", "", "who set it")
	api := fs.Bool("api", false, "the first path: the halt row of the state database, not the file")
	cell := fs.String("cell", "", "which cell the row belongs to (with --api)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := budget.HaltFilePath()

	// SP-E08-3 names two paths, and both are written here. The row is the one an API reaches and a
	// replica carries; the file is the one that works when the API does not. Setting either halts
	// the cell — that is what "and" means in the requirement.
	if *api {
		if *cell == "" {
			return fmt.Errorf("the row is per cell: --cell names which one")
		}
		ctx := context.Background()
		pool, err := statedb.Open(ctx)
		if err != nil {
			return err
		}
		defer pool.Close()
		switch {
		case *set || *renew:
			who := *by
			if who == "" {
				who = "unnamed"
			}
			// `halt.renew` is `halt.set` with the rationale it already carried: the halt is a state
			// with an expiry, not a log of commands (E-08).
			text := *reason
			if text == "" && *renew {
				existing, err := statedb.HaltState(ctx, pool, *cell)
				if err != nil {
					return err
				}
				text = existing.Reason
			}
			if err := statedb.SetHalt(ctx, pool, *cell, text, who, time.Now()); err != nil {
				return err
			}
			fmt.Fprintf(out, "halt set on the api path for cell %s; it expires in %s unless renewed (SP-E08-4)\n", *cell, budget.HaltExpiry)
		case *clear:
			if err := statedb.ClearHalt(ctx, pool, *cell); err != nil {
				return err
			}
			fmt.Fprintf(out, "halt cleared on the api path for cell %s — a situation report belongs with it (SP-E08-2)\n", *cell)
		}
		row, err := statedb.HaltState(ctx, pool, *cell)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "api path: %s\n", describe(row, time.Now()))
		file, err := budget.ReadHaltFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "file path %s: %s\n", path, describe(file, time.Now()))
		return nil
	}

	switch {
	case *set:
		if *reason == "" {
			return fmt.Errorf("a halt states a rationale — it is mandatory and reported over all adapters (SP-E08-2)")
		}
		who := *by
		if who == "" {
			who = "unnamed"
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		body := fmt.Sprintf("reason: %s\nset_by: %s\nset_at: %s\n", *reason, who, time.Now().UTC().Format(time.RFC3339))
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(out, "halt set at %s; it expires in %s unless renewed (SP-E08-4)\n", path, budget.HaltExpiry)
	case *renew:
		now := time.Now()
		if err := os.Chtimes(path, now, now); err != nil {
			return fmt.Errorf("nothing to renew at %s: %w", path, err)
		}
		fmt.Fprintf(out, "halt renewed at %s for another %s\n", path, budget.HaltExpiry)
	case *clear:
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Fprintf(out, "halt cleared at %s — a situation report belongs with it (SP-E08-2)\n", path)
	}

	// Whatever was asked, the answer is what both paths say now.
	file, err := budget.ReadHaltFile(path)
	if err != nil {
		return err
	}
	now := time.Now()
	fmt.Fprintf(out, "file path %s: %s\n", path, describe(file, now))

	ctx := context.Background()
	pool, dbErr := statedb.Open(ctx)
	if dbErr != nil {
		fmt.Fprintf(out, "api path: not readable (%v) — the file is what decides (SP-E08-3)\n", dbErr)
		return nil
	}
	defer pool.Close()
	row, err := statedb.HaltState(ctx, pool, "")
	if err != nil {
		fmt.Fprintf(out, "api path: not readable (%v) — the file is what decides (SP-E08-3)\n", err)
		return nil
	}
	fmt.Fprintf(out, "api path: %s\n", describe(row, now))
	return nil
}

func describe(h budget.Halt, now time.Time) string {
	switch {
	case h.Active(now):
		return fmt.Sprintf("in force — %q, set by %s, expires %s",
			h.Reason, h.SetBy, h.ExpiresAt.UTC().Format(time.RFC3339))
	case h.InForce:
		return fmt.Sprintf("expired at %s and no longer stops anything (SP-E08-4)",
			h.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return "no halt"
}

// spendCommand records what a job actually spent, which is what the release at the terminal state
// hands back the difference of (SP-V04-3). The worker will write this with its report from AP-3.8;
// until then it is a command, so the release can be checked against a number somebody stated rather
// than one nobody measured.
func spendCommand(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("control spend", flag.ContinueOnError)
	order := fs.String("order", "", "the order that spent it")
	podMinutes := fs.Int64("pod-minutes", 0, "pod minutes spent")
	tokens := fs.Int64("tokens", 0, "tokens spent")
	money := fs.Int64("money-micros", 0, "money spent, in micro-euros")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *order == "" {
		return fmt.Errorf("which order spent it? --order")
	}
	ctx := context.Background()
	pool, err := statedb.Open(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := statedb.RecordSpend(ctx, pool, *order, budget.Pots{
		PodMinutes: *podMinutes, Tokens: *tokens, MoneyMicros: *money}); err != nil {
		return err
	}
	fmt.Fprintf(out, "order %s spent %d pod minutes, %d tokens, %d µ€; the rest is released at the terminal state (SP-V04-3)\n",
		*order, *podMinutes, *tokens, *money)
	return nil
}
