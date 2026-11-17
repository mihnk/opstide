package api

import (
	"context"
	"time"

	"github.com/mihnk/opstide/internal/store"
)

// Deadline says whether a CronJob that declares opstide.mihnk.org/must-succeed-within has had a run succeed within
// that long, as far as OpsTide can tell. It is DERIVED: a rule applied to the runs OpsTide recorded and to when it was
// watching.
type Deadline struct {
	// State is one of:
	//   met       a run succeeded within the limit
	//   waiting   no run has succeeded yet, but the CronJob is younger than the limit
	//   overdue   OpsTide watched all of the last <limit> and no run succeeded in it
	//   unknown   OpsTide cannot say (Reason says why); never reported as overdue
	//   suspended the CronJob is suspended and is not judged
	State         string    `json:"state"`
	WithinSeconds int64     `json:"withinSeconds"`
	LastSuccess   time.Time `json:"lastSuccess,omitzero"` // when the latest succeeded run OpsTide holds finished
	// DueBy is, while met or waiting, when it becomes overdue without another success; once overdue, when it became
	// overdue (zero when OpsTide holds no success to count from).
	DueBy time.Time `json:"dueBy,omitzero"`
	// Reason, when unknown: before_first_observation (OpsTide has watched for less than the limit; KnownFrom is when it
	// will have), outside_retention (the limit reaches past the history OpsTide keeps), not_watched (OpsTide was not
	// watching for a stretch in which a succeeded Job could have finished and been deleted before it looked again; Gaps).
	Reason    string    `json:"reason,omitempty"`
	KnownFrom time.Time `json:"knownFrom,omitzero"`
	Gaps      []Span    `json:"gaps,omitempty"`
}

type deadlineInput struct {
	within       time.Duration
	lastSuccess  time.Time // zero when OpsTide holds no succeeded run
	created      time.Time // when the CronJob was created
	firstWatched time.Time // when its Jobs were first watched; zero when never
	now          time.Time
	suspended    bool
	// keptFor is how long the cluster keeps a succeeded Job once it has finished: nil while it stays until a newer one
	// replaces it (successfulJobsHistoryLimit of 1 or more, no TTL), 0 when it is not kept at all.
	keptFor *time.Duration
	// retention is how far back OpsTide holds runs; 0 when unbounded.
	retention time.Duration
	// gaps is when the CronJob's Jobs were not watched in [from, to); a stretch that lasts until now is still open.
	gaps func(from, to time.Time) []Span
}

// deadlineOf applies the rule. Overdue is claimed only when it is known: OpsTide watched the CronJob's Jobs over the
// whole of the last <limit> (or saw everything that happened in a stretch it missed, because a succeeded Job outlives
// the stretch in the cluster and was seen at the next look) and recorded no run that succeeded in it.
func deadlineOf(in deadlineInput) Deadline {
	d := Deadline{WithinSeconds: int64(in.within / time.Second), LastSuccess: in.lastSuccess}
	if in.suspended {
		d.State = "suspended"
		return d
	}
	start := in.now.Add(-in.within)
	if !in.lastSuccess.IsZero() && !in.lastSuccess.Before(start) {
		d.State, d.DueBy = "met", in.lastSuccess.Add(in.within)
		return d
	}
	if start.Before(in.created) {
		d.State, d.DueBy = "waiting", in.created.Add(in.within)
		return d
	}
	if in.firstWatched.IsZero() || start.Before(in.firstWatched) {
		d.State, d.Reason = "unknown", "before_first_observation"
		if !in.firstWatched.IsZero() {
			d.KnownFrom = in.firstWatched.Add(in.within)
		}
		return d
	}
	if in.retention > 0 && in.within > in.retention {
		d.State, d.Reason = "unknown", "outside_retention"
		return d
	}
	var hiding []Span
	for _, g := range in.gaps(start, in.now) {
		open := !g.To.Before(in.now)
		if open || (in.keptFor != nil && g.To.Sub(g.From) >= *in.keptFor) {
			hiding = append(hiding, g)
		}
	}
	if len(hiding) > 0 {
		d.State, d.Reason, d.Gaps = "unknown", "not_watched", hiding
		return d
	}
	d.State = "overdue"
	if !in.lastSuccess.IsZero() {
		d.DueBy = in.lastSuccess.Add(in.within)
	}
	return d
}

// keptFor is how long the cluster keeps op's succeeded Jobs once finished (see deadlineInput.keptFor).
func keptFor(op store.Operation) *time.Duration {
	if op.SuccessfulHistory == 0 {
		z := time.Duration(0)
		return &z
	}
	return op.JobTTL
}

// deadline is op's Deadline, or nil when it declares none or is deleted. cov must reach back to now minus its limit.
func (s *Server) deadline(op store.Operation, lastSuccess time.Time, cov *coverage, now time.Time) *Deadline {
	if op.SucceedWithin == nil || !op.DeletedAt.IsZero() {
		return nil
	}
	d := deadlineOf(deadlineInput{
		within: *op.SucceedWithin, lastSuccess: lastSuccess, created: op.CreatedAt,
		firstWatched: cov.firstWatched(op.Namespace), now: now, suspended: op.Suspend, keptFor: keptFor(op),
		retention: time.Duration(s.cfg.Retention.Days) * 24 * time.Hour,
		gaps:      func(from, to time.Time) []Span { return cov.jobGaps([]string{op.Namespace}, from, to) },
	})
	return &d
}

// longestWithin is the longest success deadline among ops; 0 when none declares one.
func longestWithin(ops []store.Operation) time.Duration {
	var longest time.Duration
	for _, op := range ops {
		if op.SucceedWithin != nil && *op.SucceedWithin > longest {
			longest = *op.SucceedWithin
		}
	}
	return longest
}

// OperationDeadline is one live CronJob's Deadline, for /metrics.
type OperationDeadline struct {
	Namespace, Name string
	Deadline        Deadline
}

// Deadlines lists the live CronJobs that declare a success deadline, with where each stands now.
func (s *Server) Deadlines(ctx context.Context) ([]OperationDeadline, error) {
	ops, err := s.db.Operations(ctx, false)
	if err != nil {
		return nil, err
	}
	longest := longestWithin(ops)
	if longest == 0 {
		return nil, nil
	}
	now := s.now().UTC()
	last, err := s.db.LastSuccesses(ctx)
	if err != nil {
		return nil, err
	}
	cov, err := loadCoverage(ctx, s.db, now.Add(-longest), now, now)
	if err != nil {
		return nil, err
	}
	var out []OperationDeadline
	for _, op := range ops {
		if d := s.deadline(op, last[op.ID], cov, now); d != nil {
			out = append(out, OperationDeadline{Namespace: op.Namespace, Name: op.Name, Deadline: *d})
		}
	}
	return out, nil
}
