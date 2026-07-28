package outbox

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
)

// Command is `workpod outbox <command> [flags]` — the outbox from the outside, for a drain unit, a
// probe and a duty officer.
//
// `ask` is here for the same reason the state exists: SP-K03-4's "if the acknowledgement is
// missing, ask; do not retry" needs somewhere for the asking to be recorded, or the instruction is
// advice rather than a mechanism. There is deliberately no `retry`.
func Command(args []string, out io.Writer) error {
	if len(args) == 0 {
		return usage()
	}
	fs := flag.NewFlagSet("outbox", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dir := fs.String("dir", DefaultDir, "the outbox directory (SP-K03-6: on /var)")
	gitSocket := fs.String("git-gate", GitSocket, "the Git gate's socket")
	egressSocket := fs.String("egress-gate", EgressSocket, "the egress gate's socket")
	cause := fs.String("cause", "", "why an entry is being asked about")
	order := fs.String("order", "", "the order of the entry")
	target := fs.String("target", "", "the target of the entry")
	hash := fs.String("content-hash", "", "the content hash of the entry")
	register := fs.Bool("requires-register", false, "the target is not idempotent (SP-K03-4)")
	payload := fs.String("payload-ref", "", "a reference to the content, never the content")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	s := New(*dir)

	switch args[0] {
	case "record":
		e, fresh, err := s.Record(Entry{
			Order: *order, Target: *target, ContentHash: *hash,
			PayloadRef: *payload, RequiresRegister: *register,
		})
		if err != nil {
			return err
		}
		if !fresh {
			// Not an error. The domain key answered, and the answer is that this effect already
			// exists — which is the whole of SP-K03-2 said out loud.
			fmt.Fprintf(out, "already recorded  %s  state=%s\n", e.Key(), e.State)
			return nil
		}
		fmt.Fprintf(out, "recorded          %s\n", e.Key())
		return nil

	case "list":
		all, err := s.List()
		if err != nil {
			return err
		}
		for _, e := range all {
			fmt.Fprintf(out, "%-14s %-6s %s %s\n", e.State, register6(e.RequiresRegister), e.Key(), e.Cause)
		}
		fmt.Fprintf(out, "%d entr%s\n", len(all), plural(len(all)))
		return nil

	case "drain":
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		d, err := Drain(ctx, s, Sockets{Git: *gitSocket, Egress: *egressSocket},
			func(format string, a ...any) { fmt.Fprintf(out, format+"\n", a...) })
		fmt.Fprintf(out, "drained: %d executed, %d already done, %d denied, %d to ask about\n",
			d.Executed, d.Deduplicated, d.Denied, d.Asked)
		return err

	case "unanswered":
		all, err := s.Unanswered()
		if err != nil {
			return err
		}
		for _, e := range all {
			fmt.Fprintf(out, "%-14s %s\n                 %s\n", e.State, e.Key(), e.Cause)
		}
		fmt.Fprintf(out, "%d entr%s to ask about — never to retry (SP-K03-4)\n", len(all), plural(len(all)))
		return nil

	case "ask":
		if *cause == "" {
			return fmt.Errorf("asking needs a cause: what is being asked, and of whom")
		}
		e, err := s.Ask(Key{Order: *order, Target: *target, ContentHash: *hash}, *cause)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "asking            %s\n", e.Key())
		return nil

	default:
		return usage()
	}
}

func usage() error {
	return fmt.Errorf(`workpod outbox <command>

  record       write down an intent to act; the pod's own path is the harness socket (SP-K03-1)
  list         every entry and its state
  drain        hand what is pending to the gates and take the receipts back (SP-K03-1)
  unanswered   the entries whose acknowledgement is missing (SP-K03-4)
  ask          record that a human is being asked about one of them

There is no ` + "`retry`" + `. SP-K03-4: if the acknowledgement is missing, ask; do not retry — the
only place in the system where retrying is forbidden.`)
}

func register6(b bool) string {
	if b {
		return "reg"
	}
	return "-"
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// TargetOf builds a Git target out of a repository and a branch, in the form the gate parses.
func TargetOf(repo, branch string) string {
	return GitScheme + strings.TrimSuffix(repo, "/") + "#" + branch
}
