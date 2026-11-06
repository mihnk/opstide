package release

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Health verdicts. They say what happened after the rollout, and are kept
// apart from the rollout's own state: a release can be completed and degraded.
const (
	HealthObserving    = "still_observing"
	HealthHealthy      = "healthy"
	HealthDegraded     = "degraded"
	HealthInsufficient = "insufficient_evidence"
)

// Windows are the observation lengths the product offers, in minutes.
var Windows = []int{5, 10, 15, 30}

// DefaultWindow is used when none is configured.
const DefaultWindow = 10 * time.Minute

// ValidWindow reports whether minutes is one of the offered lengths.
func ValidWindow(minutes int) bool {
	for _, w := range Windows {
		if w == minutes {
			return true
		}
	}
	return false
}

// Episode is a stretch after completion in which fewer replicas were
// available than wanted. Until is zero while it lasts.
type Episode struct {
	Since time.Time `json:"since"`
	Until time.Time `json:"until,omitzero"`
	// Scaling is set when the episode began because the Deployment's replica
	// target was raised while at least the earlier target was available. Episodes
	// that began any other way have none.
	Scaling *ScaleOpen `json:"scaling,omitempty"`
}

// ScaleOpen records how an availability episode began after a replica target
// increase. From and To are the target before and after, At is when OpsTide saw
// the change, and MinAvailable is the lowest available count seen since (sampled
// whenever the Deployment's status changed).
type ScaleOpen struct {
	From         int       `json:"from"`
	To           int       `json:"to"` // the highest target seen while the episode lasted
	At           time.Time `json:"at"`
	MinAvailable int       `json:"minAvailable"`
	// Last is the available count at the previous status change. Lost is set when it
	// fell from LostFrom to LostTo while the target was at least LostFrom: a replica
	// that was serving was lost, which is not the Deployment catching up.
	Last     int  `json:"last,omitempty"`
	Lost     bool `json:"lost,omitempty"`
	LostFrom int  `json:"lostFrom,omitempty"`
	LostTo   int  `json:"lostTo,omitempty"`
}

// AssessmentVersion is the rule set Assess applies. 1 is the contract of
// M2.5: any availability shortfall longer than the grace period counts. 2 does
// not count a shortfall that began with a replica target increase and never
// fell below the earlier target; it is reported as context instead. A release
// keeps the version it was first judged under, so a stored verdict is never
// re-read under rules it was not given.
const AssessmentVersion = 2

// Trouble is a time the rollout itself was stalled or failed. It stays on the
// release even if the rollout later completes: a rollout that had to be given
// up on and then recovered is not a clean one.
type Trouble struct {
	State  string    `json:"state"` // stalled | failed
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
}

// unavailableGrace is how long availability may stay below the target after
// completion before it counts. A single Pod being replaced is not a finding.
const unavailableGrace = 60 * time.Second

// PodInput is one Pod of the release, as stored.
type PodInput struct {
	Name  string
	State PodState
}

// Input is everything the assessment reads. It is a pure function of this:
// the same input gives the same verdict, wherever it is computed.
type Input struct {
	Origin       string // observed | adopted
	Rollout      string // the release's rollout state
	RolloutNote  string
	ObservedAt   time.Time
	CompletedAt  time.Time
	SupersededAt time.Time
	Window       time.Duration
	Desired      int
	Pods         []PodInput
	Episodes     []Episode
	Trouble      []Trouble
	Now          time.Time
	// Version is the rule set to apply; 0 and 1 are the M2.5 rules.
	Version int
	// Rollback says the release reuses an earlier release's ReplicaSet: what
	// its Pods did before the release was observed belongs to the earlier one.
	Rollback bool
	// Watched says whether Deployments, ReplicaSets and Pods were all being
	// watched without a gap over [from, to]. A nil Watched means unknown.
	Watched func(from, to time.Time) bool
}

// Finding is one observation that counts against a release.
type Finding struct {
	Kind      string    `json:"kind"` // oom, restart, waiting, notready, evicted, terminated, unavailable, rollout
	At        time.Time `json:"at,omitzero"`
	Observed  time.Time `json:"observed,omitzero"`
	Pod       string    `json:"pod,omitempty"`
	Container string    `json:"container,omitempty"`
	Summary   string    `json:"summary"`
}

// Verdict is the assessment, with the reasons it was reached.
type Verdict struct {
	Health      string    `json:"health"`
	Reasons     []string  `json:"reasons"`
	Findings    []Finding `json:"findings"`
	WindowStart time.Time `json:"windowStart,omitzero"`
	WindowEnd   time.Time `json:"windowEnd,omitzero"` // zero until the rollout completes
	Evidence    []string  `json:"evidence,omitempty"` // why evidence is insufficient, if it is
	// Context lists observed facts that were deliberately not counted against the
	// release, each with the rule that set it aside and the values it used.
	Context []Note `json:"context"`
	// Version is the rule set that produced this verdict.
	Version int `json:"version"`
}

// Note is an observation kept apart from the findings, with the rule applied.
type Note struct {
	Rule    string            `json:"rule"`
	Summary string            `json:"summary"`
	Inputs  map[string]string `json:"inputs,omitempty"`
}

// Assess judges a release.
//
// The window starts when the release was first observed and ends Window after
// the rollout completed, so the first minutes after the Pods are really up are
// covered however long the rollout took. Healthy needs: a completed rollout,
// a closed window, nothing against the release, and enough evidence. The
// expected churn of a rolling update (Pods of the old revision terminating,
// new Pods Pending before they are Ready) is never a finding.
func Assess(in Input) Verdict {
	v := Verdict{WindowStart: in.ObservedAt, Findings: []Finding{}, Reasons: []string{}, Context: []Note{}, Version: max(in.Version, 1)}
	if !in.CompletedAt.IsZero() {
		v.WindowEnd = in.CompletedAt.Add(in.Window)
	}
	// What happens once a newer revision took over belongs to that revision.
	end := v.WindowEnd
	if !in.SupersededAt.IsZero() && (end.IsZero() || in.SupersededAt.Before(end)) {
		end = in.SupersededAt
	}
	inWindow := func(t time.Time) bool {
		if t.IsZero() {
			return true // seen, but Kubernetes gave no time: the observation counts
		}
		floor := in.ObservedAt.Add(-time.Minute)
		if in.Rollback {
			floor = in.ObservedAt
		}
		if t.Before(floor) {
			return false
		}
		return end.IsZero() || !t.After(end)
	}

	if in.Origin == "adopted" {
		v.Health = HealthInsufficient
		v.Evidence = append(v.Evidence, "The release existed before OpsTide started watching, so its rollout and its first minutes were not observed.")
		v.Reasons = append(v.Reasons, v.Evidence...)
		return v
	}

	for _, t := range in.Trouble {
		v.Findings = append(v.Findings, Finding{Kind: "rollout", At: t.At, Summary: fmt.Sprintf("The rollout was %s: %s", t.State, t.Reason)})
	}
	// A rollout that is stalled or failed right now counts even before the
	// controller's condition has been written to the release's history.
	if (in.Rollout == StateStalled || in.Rollout == StateFailed) && len(in.Trouble) == 0 {
		v.Findings = append(v.Findings, Finding{Kind: "rollout", Summary: fmt.Sprintf("The rollout is %s: %s", in.Rollout, in.RolloutNote)})
	}
	for _, p := range in.Pods {
		for name, c := range p.State.Containers {
			for _, o := range c.OOM {
				if !o.Observed.IsZero() && inWindow(o.Observed) {
					v.Findings = append(v.Findings, Finding{Kind: "oom", At: o.At, Observed: o.Observed, Pod: p.Name, Container: name,
						Summary: fmt.Sprintf("Container %s in pod %s was OOMKilled", name, p.Name)})
				}
			}
			for _, r := range c.Restarted {
				if inWindow(r.Observed) {
					s := fmt.Sprintf("Container %s in pod %s restarted", name, p.Name)
					if r.Reason != "" {
						s += " (" + r.Reason + ")"
					}
					if r.Delta > 1 {
						s += fmt.Sprintf(", %d times since last seen", r.Delta)
					}
					v.Findings = append(v.Findings, Finding{Kind: "restart", At: r.At, Observed: r.Observed, Pod: p.Name, Container: name, Summary: s})
				}
			}
			for _, w := range c.Waiting {
				if !w.Observed.IsZero() && inWindow(w.Observed) {
					v.Findings = append(v.Findings, Finding{Kind: "waiting", Observed: w.Observed, Pod: p.Name, Container: name,
						Summary: fmt.Sprintf("Container %s in pod %s is waiting: %s", name, p.Name, w.Reason)})
				}
			}
		}
		for _, t := range p.State.Transitions {
			// Ready to NotReady, for a Pod that was not being deleted at the time.
			// A Pod already being deleted when it went NotReady is the rollout at work.
			endedWhileDeleting := p.State.Deleting && !p.State.DeletedAt.IsZero() && !t.At.Before(p.State.DeletedAt.Add(-time.Second))
			if !t.Ready && inWindow(t.At) && !endedWhileDeleting {
				v.Findings = append(v.Findings, Finding{Kind: "notready", At: t.At, Pod: p.Name, Summary: fmt.Sprintf("Pod %s went from Ready to NotReady", p.Name)})
			}
		}
		if p.State.Evicted && inWindow(p.State.EvictedAt) {
			v.Findings = append(v.Findings, Finding{Kind: "evicted", Observed: p.State.EvictedAt, Pod: p.Name, Summary: fmt.Sprintf("Pod %s was evicted", p.Name)})
		}
		// A Pod that ended on its own with a failure, not because it was deleted.
		if p.State.Terminated && !p.State.Deleting && p.State.TermReason == "Failed" && !p.State.Evicted {
			seen := p.State.TermSeen
			if seen.IsZero() {
				seen = p.State.TermAt
			}
			if inWindow(seen) {
				v.Findings = append(v.Findings, Finding{Kind: "terminated", At: p.State.TermAt, Observed: seen, Pod: p.Name, Summary: fmt.Sprintf("Pod %s ended in phase Failed", p.Name)})
			}
		}
	}
	for _, e := range in.Episodes {
		until := e.Until
		if until.IsZero() {
			until = in.Now
		}
		if !inWindow(e.Since) || until.Sub(e.Since) < unavailableGrace {
			continue
		}
		if sc := e.Scaling; sc != nil && in.Version >= 2 {
			inputs := map[string]string{"from": itoa(sc.From), "to": itoa(sc.To), "minAvailable": itoa(sc.MinAvailable), "since": e.Since.UTC().Format(time.RFC3339)}
			if sc.MinAvailable >= sc.From && !sc.Lost {
				// Rule scaling.convergence: the replica target was raised from From to To
				// and the available count never fell below From. The shortfall is the
				// Deployment catching up with the new target, not a loss of what was serving.
				s := fmt.Sprintf("Desired replicas changed from %d to %d; available replicas stayed at or above %d (lowest observed: %d) while the new target was being reached", sc.From, sc.To, sc.From, sc.MinAvailable)
				if e.Until.IsZero() {
					s += ", and it had not been reached yet"
				}
				v.Context = append(v.Context, Note{Rule: "scaling.convergence", Summary: s, Inputs: inputs})
				continue
			}
			// Rule scaling.below_earlier_target: available fell below the target that
			// held before the increase. Counted, with the scaling stated.
			// Rule scaling.replica_lost: available fell while the target was being reached,
			// so a replica that was serving was lost. Counted, with the scaling stated.
			if sc.Lost && sc.MinAvailable >= sc.From {
				v.Findings = append(v.Findings, Finding{Kind: "unavailable", At: e.Since, Summary: fmt.Sprintf("Available replicas fell from %d to %d while the target was raised from %d to %d", sc.LostFrom, sc.LostTo, sc.From, sc.To)})
				continue
			}
			v.Findings = append(v.Findings, Finding{Kind: "unavailable", At: e.Since, Summary: fmt.Sprintf("Available replicas fell to %d, below the earlier target of %d, while the target was raised from %d to %d", sc.MinAvailable, sc.From, sc.From, sc.To)})
			continue
		}
		s := fmt.Sprintf("Fewer replicas were available than wanted for %s", until.Sub(e.Since).Round(time.Second))
		if e.Until.IsZero() {
			s += " and still are"
		}
		v.Findings = append(v.Findings, Finding{Kind: "unavailable", At: e.Since, Summary: s})
	}
	sort.SliceStable(v.Findings, func(i, j int) bool {
		a, b := v.Findings[i], v.Findings[j]
		ta, tb := firstTime(a), firstTime(b)
		return ta.Before(tb)
	})

	// The window is closed once it ran out, or once a newer revision took over
	// (what happens after that belongs to the newer one). Until then nothing
	// can be called final: a window that is still open can only turn degraded.
	closed := !in.SupersededAt.IsZero() || (!v.WindowEnd.IsZero() && !in.Now.Before(v.WindowEnd))

	// Evidence: was OpsTide able to see all of it?
	to := in.Now
	if !end.IsZero() && end.Before(to) {
		to = end
	}
	if in.Watched != nil && !in.Watched(in.ObservedAt, to) {
		v.Evidence = append(v.Evidence, "OpsTide was not watching Deployments, ReplicaSets and Pods without a gap during the observation window.")
	}
	if in.Watched == nil {
		v.Evidence = append(v.Evidence, "Observation coverage could not be checked.")
	}
	if closed && in.Rollout == StateCompleted && in.Desired > 0 && len(in.Pods) == 0 {
		v.Evidence = append(v.Evidence, "No Pod of this release was ever observed, so nothing can be said about its Pods.")
	}
	if !in.SupersededAt.IsZero() && in.CompletedAt.IsZero() {
		v.Evidence = append(v.Evidence, "A newer revision replaced it before its rollout completed, so no post-deployment window followed.")
	} else if !in.SupersededAt.IsZero() && !v.WindowEnd.IsZero() && in.SupersededAt.Before(v.WindowEnd) {
		v.Evidence = append(v.Evidence, "A newer revision replaced it before its observation window ended.")
	}

	for _, f := range v.Findings {
		v.Reasons = append(v.Reasons, f.Summary)
	}
	for _, n := range v.Context {
		v.Reasons = append(v.Reasons, "Not counted against the release ("+n.Rule+"): "+n.Summary+".")
	}
	switch {
	case len(v.Findings) > 0:
		v.Health = HealthDegraded
		if len(v.Evidence) > 0 {
			v.Reasons = append(v.Reasons, "Evidence is also incomplete: "+strings.Join(v.Evidence, " "))
		}
	case !closed:
		v.Health = HealthObserving
		if in.CompletedAt.IsZero() {
			v.Reasons = append(v.Reasons, "The rollout has not completed; the observation window starts when it does.")
		} else {
			v.Reasons = append(v.Reasons, fmt.Sprintf("Observing until %s; nothing against the release so far.", v.WindowEnd.UTC().Format("15:04 UTC")))
		}
		if len(v.Evidence) > 0 {
			v.Reasons = append(v.Reasons, "Evidence is already incomplete, which will make the verdict insufficient unless something is found: "+strings.Join(v.Evidence, " "))
		}
	case len(v.Evidence) > 0:
		v.Health = HealthInsufficient
		v.Reasons = append(v.Reasons, v.Evidence...)
	default:
		v.Health = HealthHealthy
		loss := "lasting loss of availability"
		if len(v.Context) > 0 {
			// A raised replica target that had not been reached is not a loss, and the
			// verdict does not claim there was none.
			loss = "loss of availability below the earlier target"
		}
		v.Reasons = append(v.Reasons, fmt.Sprintf("The rollout completed and %d minutes after it passed with no restart, OOMKill, waiting reason, NotReady transition or %s, and OpsTide watched throughout.", int(in.Window/time.Minute), loss))
	}
	return v
}

func itoa(n int) string { return strconv.Itoa(n) }

func firstTime(f Finding) time.Time {
	if !f.At.IsZero() {
		return f.At
	}
	return f.Observed
}

// SignificantTypes are the entry types the release list shows as "recent
// events": what an operator would want to see without opening the release.
var SignificantTypes = []string{
	"release.stalled", "release.failed", "release.health.degraded", "release.health.healthy", "release.unavailable",
	"pod.oomkilled", "pod.restarted", "pod.waiting", "pod.evicted", "pod.notready",
}
