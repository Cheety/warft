// Package observation is B-03: the unit of observation is the job, and there are exactly four
// alerts that may wake a human.
//
// What lives here is what is a rule rather than a query — the alert catalog of decisions/alerts.md
// with its thresholds, the four SLOs of SP-B03-2, and R-D's occupancy table with the two sources
// SP-RD-2 puts under the same six places. The rows a trace is made of are written and read by the
// step that owns the state contract (`internal/statedb`), because a trace is state; the entry point
// that prints all of it is `cli.go` beside this file.
//
// The retention periods are deliberately not here. SP-E07-2 gives them per kind of data and the
// state contract applies them at the moment of the insert — a second copy in this package would be
// a second thing to keep true, and the one that is not written down is the one that would be wrong.
package observation

// SLO is one of SP-B03-2's four. They are named here and not all of them are measured yet:
// `escape_rate` needs Q-03's corpus, which is stage 5's work. Naming a service level without
// measuring it is the honest half — a display showing a number nobody computed would be worse than
// an empty place (Q-02).
type SLO struct {
	Name     string `json:"name"`
	Applies  string `json:"applies_to"`
	Source   string `json:"source"`
	Measured bool   `json:"measured"`
}

// SLOs is SP-B03-2's list, in its own order.
func SLOs() []SLO {
	return []SLO{
		{"time_to_first_progress", "interactive jobs", "job_span: the first span that ran", true},
		{"no_clarification_rate", "per project and tenant", "Q-01's intent contract (stage 4)", false},
		{"escape_rate", "per project and tenant", "Q-03's corpus (stage 5)", false},
		{"cost_per_acceptance", "per project and tenant", "job_span: cost until an evidence class", true},
	}
}
