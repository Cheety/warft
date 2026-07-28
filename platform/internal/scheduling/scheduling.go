// Package scheduling is R-B and R-C as rules: which token a phase holds, how a waiting job rises,
// what the six pressure signals mean, and what three runs of a repository let admission decide
// mechanically.
//
// It holds no state that outlives a decision and touches no database. Ordering the queue is a
// transaction over `"order"` with SKIP LOCKED, and that transaction belongs to the step that owns
// the state contract (`internal/statedb`); reading the pressure files belongs to the role that runs
// on a node (`internal/scheduler`). What belongs here is everything both of them must agree on —
// the rulings of decisions/phase-tokens.md and decisions/aging.md, SP-RB-2's four bounds, SP-RC-2's
// six signals with OP-6's hysteresis, and SP-RC-3's five rungs. A base module is how two ranks
// share a rule without one of them owning it (decisions/module-dependencies.md).
//
// It deliberately does not read acceptance/e05-constants.tsv. SP-RD-3 and the boundary of AP-3.7
// say the five constants of E-05 are planning values and that admission does not read them: this
// package decides from PSI and from measured peak RSS, and the one place the five constants would
// otherwise creep in — "how much RAM does a pod need" — is answered by a profile of three runs
// (SP-RC-6) or not at all.
//
// The phase names are T-05's spine, but the type is this package's own: a base module imports
// nothing, so the join between the seven phases and the three tokens is checked rather than
// compiled — `internal/scheduler`'s test holds this table against runner.Spine().
package scheduling

import (
	"bufio"
	_ "embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed phase-tokens.tsv
var phaseTokenSource string

// Phase is one step of T-05's spine, by the name the pipeline uses.
type Phase string

// Class is one of SP-RB-1's three token classes. A pod holds the token of its current phase and no
// other.
type Class string

const (
	// ClassNet is planning and reworking: many slots, because a pod waiting for a model response
	// costs a frozen pod's memory and no core.
	ClassNet Class = "net"
	// ClassIO is preparing and clearing up: few, and exactly one under io pressure (SP-RC-2).
	ClassIO Class = "io"
	// ClassCPURAM is building and checking: the bottleneck, and the reason a single counter of
	// "active slots" is too coarse.
	ClassCPURAM Class = "cpu·ram"
)

// Classes lists the three in the order a report prints them: the many, the few, the bottleneck.
func Classes() []Class { return []Class{ClassNet, ClassIO, ClassCPURAM} }

// PhaseTokens is decisions/phase-tokens.md as the binary carries it.
type PhaseTokens struct {
	table map[Phase]Class
	order []Phase
}

// RuledTokens is the phase-to-token table the program was built with.
func RuledTokens() PhaseTokens {
	t, err := parsePhaseTokens(phaseTokenSource)
	if err != nil {
		// The file is embedded at build time; a malformed one is a broken build, not a runtime
		// condition a caller could handle.
		panic("phase-tokens.tsv: " + err.Error())
	}
	return t
}

// Of is the token a phase holds. An unknown phase is an error and never a default: a phase whose
// token was guessed is a phase nobody ruled, and the scheduler refuses to schedule it.
func (t PhaseTokens) Of(p Phase) (Class, error) {
	c, ok := t.table[p]
	if !ok {
		return "", fmt.Errorf("decisions/phase-tokens.md rules no token for the phase %q", p)
	}
	return c, nil
}

// PhaseToken is one row of the ruling.
type PhaseToken struct {
	Phase Phase
	Class Class
}

// Rows returns the whole table in the spine's order, for a check or a report to read back.
func (t PhaseTokens) Rows() []PhaseToken {
	rows := make([]PhaseToken, 0, len(t.table))
	for _, p := range t.order {
		rows = append(rows, PhaseToken{Phase: p, Class: t.table[p]})
	}
	return rows
}

// Phases is the set the table covers, sorted, so a caller can hold it against T-05's spine.
func (t PhaseTokens) Phases() []Phase {
	ps := make([]Phase, 0, len(t.table))
	for p := range t.table {
		ps = append(ps, p)
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i] < ps[j] })
	return ps
}

func parsePhaseTokens(src string) (PhaseTokens, error) {
	t := PhaseTokens{table: map[Phase]Class{}}
	valid := map[Class]bool{}
	for _, c := range Classes() {
		valid[c] = true
	}
	sc := bufio.NewScanner(strings.NewReader(src))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 2 {
			return t, fmt.Errorf("a row is `phase<TAB>token`, not %q", line)
		}
		if f[0] == "phase" {
			continue // the header
		}
		phase, class := Phase(f[0]), Class(f[1])
		if !valid[class] {
			return t, fmt.Errorf("%q is not one of SP-RB-1's three tokens", class)
		}
		if _, dup := t.table[phase]; dup {
			return t, fmt.Errorf("the phase %q stands twice; a pod holds one token, so a phase has one", phase)
		}
		t.table[phase] = class
		t.order = append(t.order, phase)
	}
	if err := sc.Err(); err != nil {
		return t, err
	}
	if len(t.table) == 0 {
		return t, fmt.Errorf("no rows — the table is what joins the spine to the tokens")
	}
	return t, nil
}
