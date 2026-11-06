package release

import (
	"fmt"
	"sort"
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
}

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
	v := Verdict{WindowStart: in.ObservedAt, Findings: []Finding{}, Reasons: []string{}}
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
		v.Reasons = append(v.Reasons, fmt.Sprintf("The rollout completed and %d minutes after it passed with no restart, OOMKill, waiting reason, NotReady transition or lasting loss of availability, and OpsTide watched throughout.", int(in.Window/time.Minute)))
	}
	return v
}

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
