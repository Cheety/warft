// SP-RC-6: predict instead of clean up. Peak RSS and runtime per repository and phase, and after
// three runs an admission that is mechanical rather than hopeful.

package scheduling

import (
	"fmt"
	"time"
)

// RunsForMechanical is SP-RC-6's "after three runs". Below it there is a profile but no prediction:
// two measurements of a repository's `check` phase are two anecdotes, and admitting on them would
// be the confidence Q-02 rejects.
const RunsForMechanical = 3

// RefuseAbove is SP-RC-6's "above 90 % a job does not start at all, but reports back with options".
// It is a share of what the node has free, not of what it has.
const RefuseAbove = 0.90

// Observation is what one finished phase leaves behind. The worker records it; nothing predicts
// from a single one.
type Observation struct {
	Repository string
	Phase      Phase
	PeakRSS    int64 // bytes
	Runtime    time.Duration
}

// Profile is a repository's history in one phase, as admission reads it: how many runs it rests on,
// the largest peak RSS seen, and the longest runtime.
//
// Both aggregates are maxima and not averages, deliberately. Admission is asking "will this fit",
// and a mean answers a different question — half the runs of a repository whose mean fits do not
// fit. SP-RC-6's own reaction to a wrongly classified pod is to raise the class, never to hope for
// the average.
type Profile struct {
	Repository string
	Phase      Phase
	Runs       int
	PeakRSS    int64
	Runtime    time.Duration
}

// Add folds one observation into a profile. The repository and phase of the first observation are
// the profile's; a later observation of another repository is an error rather than a silent merge.
func (p *Profile) Add(o Observation) error {
	if p.Runs == 0 {
		p.Repository, p.Phase = o.Repository, o.Phase
	}
	if p.Repository != o.Repository || p.Phase != o.Phase {
		return fmt.Errorf("a profile is one repository in one phase: %s/%s cannot take %s/%s",
			p.Repository, p.Phase, o.Repository, o.Phase)
	}
	if o.PeakRSS < 0 {
		return fmt.Errorf("a peak RSS of %d bytes is not a measurement", o.PeakRSS)
	}
	p.Runs++
	if o.PeakRSS > p.PeakRSS {
		p.PeakRSS = o.PeakRSS
	}
	if o.Runtime > p.Runtime {
		p.Runtime = o.Runtime
	}
	return nil
}

// Mechanical is SP-RC-6's threshold: with three runs behind it, admission decides from this profile
// and stops guessing.
func (p Profile) Mechanical() bool { return p.Runs >= RunsForMechanical }

// Verdict is one mechanical admission decision.
type Verdict struct {
	// Admit is whether the job may start now.
	Admit bool
	// Mechanical is whether the decision rests on three runs. When it is false the decision was
	// made without a prediction — the job is admitted on PSI alone, which is what a repository
	// nobody has measured gets.
	Mechanical bool
	// Exclusive is SP-RB-5: the job fits, but only alone.
	Exclusive bool
	// Share is the predicted peak RSS as a fraction of what is free.
	Share  float64
	Reason string
	// Options is SP-RC-6's "reports back with options" and SP-V04-2's shape: a refusal that only
	// said no would be a silent truncation in politer words.
	Options []string
}

// Decide is the mechanical admission of SP-RC-6.
//
// It reads two numbers and no third: the profile's measured peak RSS, and how many bytes the node
// has free. The five constants of E-05 are not among them — SP-RD-3 and the boundary of AP-3.7 both
// say admission does not read them, because they are planning values for the occupancy table and a
// planning value that decides admissions is a measurement nobody took.
func Decide(p Profile, freeBytes int64) Verdict {
	if !p.Mechanical() {
		return Verdict{
			Admit: true, Mechanical: false,
			Reason: fmt.Sprintf("%s/%s has %d of the %d runs SP-RC-6 asks for; admission decides on pressure alone until then",
				p.Repository, p.Phase, p.Runs, RunsForMechanical),
		}
	}
	if freeBytes <= 0 {
		return Verdict{
			Mechanical: true, Share: 1,
			Reason:  "the node reports nothing free; a prediction cannot be held against an unknown",
			Options: options(p),
		}
	}
	share := float64(p.PeakRSS) / float64(freeBytes)
	switch {
	case share > RefuseAbove:
		return Verdict{
			Mechanical: true, Share: share,
			Reason: fmt.Sprintf("%s/%s peaked at %d bytes over %d runs, which is %.0f %% of what is free; above %.0f %% a job does not start (SP-RC-6)",
				p.Repository, p.Phase, p.PeakRSS, p.Runs, share*100, RefuseAbove*100),
			Options: options(p),
		}
	case share > ExclusiveShare:
		return Verdict{
			Admit: true, Mechanical: true, Exclusive: true, Share: share,
			Reason: fmt.Sprintf("%s/%s takes %.0f %% of what is free; above %.0f %% it runs alone and holds every cpu·ram token (SP-RB-5)",
				p.Repository, p.Phase, share*100, ExclusiveShare*100),
		}
	}
	return Verdict{
		Admit: true, Mechanical: true, Share: share,
		Reason: fmt.Sprintf("%s/%s peaked at %d bytes over %d runs, %.0f %% of what is free",
			p.Repository, p.Phase, p.PeakRSS, p.Runs, share*100),
	}
}

// options is what a refused job is told. Each one is something the sender or the duty officer can
// actually do; "try again later" is not among them, because it is what happens anyway.
func options(p Profile) []string {
	return []string{
		fmt.Sprintf("run %s/%s on a node with more than %d bytes free", p.Repository, p.Phase, p.PeakRSS),
		"split the job so that no single phase needs the whole node",
		fmt.Sprintf("wait for the exclusive run to end; %s/%s is queued, not refused", p.Repository, p.Phase),
	}
}
