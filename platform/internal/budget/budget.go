// Package budget is V-04 in one place: three pots, four scopes, three authority levels, and what a
// refusal says when one of them is empty.
//
// It holds no state and touches no database. Reserving is a transaction over `budget_pot`, and that
// transaction belongs to the step that owns the state contract (`internal/statedb`); what belongs
// here is everything both that step and the roles above it must agree on — OP-1's caps, the shape of
// a demand, the shape of a refusal, and the halt as it is read from a file. A base module is how two
// roles share a rule without one of them owning it (decisions/module-dependencies.md).
//
// The caps are OP-1's, read from op1-pots.tsv rather than written here, so a cap cannot move in the
// program without moving in the ruling — acceptance/v04-budget.sh holds the two against each other.
package budget

import (
	"bufio"
	_ "embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

//go:embed op1-pots.tsv
var potsSource string

// Scope is one of SP-V04-5's three purposes, plus SP-T01-8's channel limit: per envelope against
// abuse, per project against outliers, per principal and day against the bill, per principal and
// channel against one channel taking the day with it.
type Scope string

const (
	ScopeEnvelope            Scope = "envelope"
	ScopeProject             Scope = "project"
	ScopePrincipalDay        Scope = "principal_day"
	ScopePrincipalChannelDay Scope = "principal_channel_day"
)

// Scopes is the order a job is checked in: the narrowest pot first, so a refusal names the smallest
// thing that was in the way rather than the largest.
func Scopes() []Scope {
	return []Scope{ScopeEnvelope, ScopeProject, ScopePrincipalDay, ScopePrincipalChannelDay}
}

// Levels are the authority levels of T-01, weakest first. The level comes from the channel, never
// from the text (SP-T01-9).
func Levels() []string { return []string{"public", "linked", "confidential"} }

// Pots is the triple V-04 counts in: pod minutes reserved at admission, tokens taken in advance per
// job, money as micro-euros. The same shape serves as a cap, as a reservation and as a spend.
type Pots struct {
	PodMinutes  int64
	Tokens      int64
	MoneyMicros int64
}

// Resources names the three in the order a refusal reports them.
func (p Pots) Resources() []string { return []string{"pod_minutes", "tokens", "money"} }

// Get reads one of the three by the name the state contract and OP-1 use.
func (p Pots) Get(resource string) (int64, error) {
	switch resource {
	case "pod_minutes":
		return p.PodMinutes, nil
	case "tokens":
		return p.Tokens, nil
	case "money":
		return p.MoneyMicros, nil
	}
	return 0, fmt.Errorf("V-04 counts three pots and %q is not one of them", resource)
}

// Caps is OP-1's table, parsed: the cap of every pot, per authority level and scope.
type Caps struct {
	table map[capKey]Pots
}

type capKey struct {
	level string
	scope Scope
}

// Ruled is OP-1 as the binary carries it.
func Ruled() Caps {
	c, err := parseCaps(potsSource)
	if err != nil {
		// The file is embedded at build time; a malformed one is a broken build, not a runtime
		// condition a caller could handle.
		panic("op1-pots.tsv: " + err.Error())
	}
	return c
}

// For is the cap of one pot. An unknown level or scope is an error and never a default: a pot whose
// cap was guessed is a cap nobody ruled.
func (c Caps) For(level string, scope Scope) (Pots, error) {
	p, ok := c.table[capKey{level, scope}]
	if !ok {
		return Pots{}, fmt.Errorf("OP-1 rules no pot for authority %q at scope %q", level, scope)
	}
	return p, nil
}

// Rows returns the whole table, sorted, for a check or a report to read back.
func (c Caps) Rows() []Row {
	rows := make([]Row, 0, len(c.table))
	for k, v := range c.table {
		rows = append(rows, Row{Level: k.level, Scope: k.scope, Caps: v})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Level != rows[j].Level {
			return rows[i].Level < rows[j].Level
		}
		return rows[i].Scope < rows[j].Scope
	})
	return rows
}

// Row is one line of OP-1's table.
type Row struct {
	Level string
	Scope Scope
	Caps  Pots
}

func parseCaps(src string) (Caps, error) {
	c := Caps{table: map[capKey]Pots{}}
	sc := bufio.NewScanner(strings.NewReader(src))
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if f[0] != "pot" || len(f) != 6 {
			return c, fmt.Errorf("%q is not a pot row", line)
		}
		level, scope := f[1], Scope(f[2])
		n := make([]int64, 3)
		for i, raw := range f[3:] {
			v, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || v <= 0 {
				return c, fmt.Errorf("%s/%s: %q is not a positive number", level, scope, raw)
			}
			n[i] = v
		}
		key := capKey{level, scope}
		if _, dup := c.table[key]; dup {
			return c, fmt.Errorf("%s/%s is ruled twice", level, scope)
		}
		c.table[key] = Pots{PodMinutes: n[0], Tokens: n[1], MoneyMicros: n[2]}
	}
	if err := sc.Err(); err != nil {
		return c, err
	}
	// The table is complete or it is not a table: every level times every scope. A missing row
	// would otherwise become a refusal at admission on the day that combination first arrives.
	for _, level := range Levels() {
		for _, scope := range Scopes() {
			if _, ok := c.table[capKey{level, scope}]; !ok {
				return c, fmt.Errorf("OP-1 rules no pot for %s/%s", level, scope)
			}
		}
	}
	return c, nil
}

// Refusal is what admission answers with when a pot cannot carry a job: which pot, at which scope
// and level, what was asked for, what is free — and what the sender can do about it.
//
// SP-V04-2 makes the last part a requirement rather than a courtesy: running out of tokens produces
// a reply with options, never a silent truncation. A refusal that only said "no" would be the same
// failure in politer words.
type Refusal struct {
	Scope    Scope
	Level    string
	Resource string
	Want     int64
	Free     int64
	Cap      int64
	Options  []string
}

// Cause is the state contract's cause_code for every budget refusal. SP-K02-3: no terminal state
// without a cause, and a job that was never admitted is refused with one too.
const Cause = "budget.exhausted"

// Exhausted builds the refusal for one pot and one resource.
func Exhausted(scope Scope, level, resource string, want, free, limit int64) Refusal {
	return Refusal{
		Scope: scope, Level: level, Resource: resource,
		Want: want, Free: free, Cap: limit,
		Options: options(scope, resource),
	}
}

// options is SP-V04-2 per pot. Money has exactly one, and that is the point of it: it is a hard
// limit, and only a human raises it — which by E-08 is two people.
func options(scope Scope, resource string) []string {
	switch resource {
	case "pod_minutes":
		return []string{
			"run the job in a smaller resource class — the minutes are counted per pod, not per request",
			"wait for the pot: " + refill(scope),
			"ask for the cap to be raised (cap.raise is two people, E-08)",
		}
	case "tokens":
		return []string{
			"split the goal into two jobs, each with its own acceptance criterion",
			"narrow the bounds — fewer repositories and paths is fewer tokens",
			"wait for the pot: " + refill(scope),
			"ask for the cap to be raised (cap.raise is two people, E-08)",
		}
	case "money":
		return []string{"a human raises the daily cap, and that is two people (E-08)"}
	}
	return nil
}

func refill(scope Scope) string {
	switch scope {
	case ScopePrincipalDay, ScopePrincipalChannelDay:
		return "it refills at the turn of the day"
	case ScopeEnvelope:
		return "a new message is a new envelope pot"
	}
	// The project pot refills only as the jobs holding it reach a terminal state (SP-V04-3).
	return "it refills as this project's running jobs reach a terminal state"
}

// Error makes a refusal usable where an error is expected. It names the pot, the numbers and the
// options, because the message is what reaches the sender's channel.
func (r Refusal) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "budget.exhausted: the %s pot of this %s (authority %s) has %d of %d %s free, and this job asks for %d",
		r.Resource, r.Scope, r.Level, r.Free, r.Cap, r.Resource, r.Want)
	for _, o := range r.Options {
		b.WriteString("\n  · ")
		b.WriteString(o)
	}
	return b.String()
}

// Halted is the refusal that is not about a pot: the duty officer has halted the cell (E-08). It is
// stated separately because it says something else — nothing is exhausted, and nothing the sender
// can change would admit this job.
type Halted struct {
	Reason string
	SetBy  string
	Source string // "file" or "api" — which of SP-E08-3's two paths this came from
}

func (h Halted) Error() string {
	return fmt.Sprintf("halt in force (%s, set by %s, %s): no job is admitted; jobs already running run to completion (E-08, SP-V04-2)",
		h.Reason, h.SetBy, h.Source)
}
