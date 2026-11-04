package api

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/mihnk/opstide/internal/schedule"
	"github.com/mihnk/opstide/internal/store"
)

// Day is the Today screen: one calendar day in the reader's zone.
type Day struct {
	Schema    int       `json:"schema"`
	Cluster   string    `json:"cluster"`
	Date      string    `json:"date"`
	TimeZone  string    `json:"timeZone"`
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	Now       time.Time `json:"now"`
	LastWrite time.Time `json:"lastWrite,omitzero"`
	Counts    Counts    `json:"counts"`
	// Gaps are the parts of the day, up to now, when Jobs were not watched.
	Gaps       []Span      `json:"gaps"`
	Lanes      []Lane      `json:"lanes"`
	Idle       int         `json:"idle"` // operations with nothing due or run this day
	Rollouts   []Rollout   `json:"rollouts"`
	Exceptions []Exception `json:"exceptions"`
	Upcoming   []Upcoming  `json:"upcoming"`
}

// Counts sums the day's runs and expected times.
type Counts struct {
	Observed    int `json:"observed"` // runs created this day
	Succeeded   int `json:"succeeded"`
	Failed      int `json:"failed"`
	Running     int `json:"running"`
	Unknown     int `json:"unknown"`
	Unconfirmed int `json:"unconfirmed"`
	Missed      int `json:"missed"`
	Skipped     int `json:"skipped"`
	StillDue    int `json:"stillDue"` // expected or due, not yet run
}

// Lane is one operation's day.
type Lane struct {
	OperationID     string `json:"operationId"`
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	Schedule        string `json:"schedule"`
	TimeZone        string `json:"timeZone,omitempty"`
	Suspended       bool   `json:"suspended,omitempty"`
	ExpectedSeconds int64  `json:"expectedSeconds,omitempty"`
	ScheduleError   string `json:"scheduleError,omitempty"`
	Marks           []Mark `json:"marks"`
	Truncated       bool   `json:"truncated,omitempty"`
}

// Rollout is a finished Deployment rollout.
type Rollout struct {
	At        time.Time `json:"at"`
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	Revision  string    `json:"revision"`
	Summary   string    `json:"summary"`
}

// Exception is something on the day that needs a look.
type Exception struct {
	At          time.Time `json:"at"`
	Kind        string    `json:"kind"` // failed | lost | missed | unconfirmed | skipped | slow
	OperationID string    `json:"operationId"`
	Name        string    `json:"name"`
	RunID       string    `json:"runId,omitempty"`
	ScheduledAt time.Time `json:"scheduledAt,omitzero"`
	Seconds     int64     `json:"seconds,omitempty"`
	Expected    int64     `json:"expectedSeconds,omitempty"`
	Attempts    int       `json:"attempts,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	Owner       string    `json:"owner,omitempty"`
	RunbookURL  string    `json:"runbookUrl,omitempty"`
	Why         string    `json:"why,omitempty"`
}

// Upcoming is an operation's next expected time today.
type Upcoming struct {
	At          time.Time `json:"at"`
	OperationID string    `json:"operationId"`
	Name        string    `json:"name"`
	Namespace   string    `json:"namespace"`
}

func (s *Server) day(w http.ResponseWriter, r *http.Request) {
	v, err := s.buildDay(r)
	reply(w, r, s.log, v, err)
}

func (s *Server) buildDay(r *http.Request) (Day, error) {
	ctx := r.Context()
	loc, err := zone(r)
	if err != nil {
		return Day{}, err
	}
	now := s.now().UTC()
	date, err := dateParam(r, loc, now)
	if err != nil {
		return Day{}, err
	}
	from, to := dayBounds(date, loc)
	d := Day{Schema: Schema, Cluster: s.cfg.Cluster, Date: from.Format("2006-01-02"), TimeZone: loc.String(),
		From: from.UTC(), To: to.UTC(), Now: now, Gaps: []Span{}, Lanes: []Lane{}, Rollouts: []Rollout{},
		Exceptions: []Exception{}, Upcoming: []Upcoming{}}
	if d.LastWrite, err = s.db.LastWrite(ctx); err != nil {
		return d, err
	}

	ops, err := s.db.Operations(ctx, true)
	if err != nil {
		return d, err
	}
	runs, err := s.db.RunsBetween(ctx, from, to)
	if err != nil {
		return d, err
	}
	byOp := groupRuns(runs)
	// A day back, so start windows that began yesterday can be judged.
	cov, err := loadCoverage(ctx, s.db, from.Add(-24*time.Hour), to, now)
	if err != nil {
		return d, err
	}

	namespaces := map[string]bool{}
	for _, op := range ops {
		if !alive(op, from, to) {
			continue
		}
		namespaces[op.Namespace] = true
		v, err := s.view(ctx, op, byOp[op.ID])
		if err != nil {
			return d, err
		}
		slots, truncated := v.slots(cov, from, to, now)
		lane := Lane{OperationID: op.ID, Name: displayName(op), Namespace: op.Namespace, Schedule: op.Schedule, TimeZone: op.TimeZone,
			Suspended: op.Suspend, ScheduleError: v.schedErr, Truncated: truncated}
		if op.ExpectedDuration != nil {
			lane.ExpectedSeconds = int64(*op.ExpectedDuration / time.Second)
		}
		lane.Marks = v.marks(slots, from, to, now)
		if onlySuspended(lane.Marks) && v.schedErr == "" {
			d.Idle++
			continue
		}
		d.Lanes = append(d.Lanes, lane)
		s.tally(&d, v, slots, from, to, now)
	}
	sort.SliceStable(d.Lanes, func(i, j int) bool {
		a, b := d.Lanes[i], d.Lanes[j]
		switch {
		case len(a.Marks) == 0 || len(b.Marks) == 0:
			return len(a.Marks) > len(b.Marks)
		case !a.Marks[0].Start.Equal(b.Marks[0].Start):
			return a.Marks[0].Start.Before(b.Marks[0].Start)
		}
		return a.Name < b.Name
	})
	sort.Slice(d.Exceptions, func(i, j int) bool { return d.Exceptions[i].At.After(d.Exceptions[j].At) })
	sort.Slice(d.Upcoming, func(i, j int) bool { return d.Upcoming[i].At.Before(d.Upcoming[j].At) })
	if len(d.Upcoming) > 8 {
		d.Upcoming = d.Upcoming[:8]
	}

	nsList := make([]string, 0, len(namespaces))
	for ns := range namespaces {
		nsList = append(nsList, ns)
	}
	if len(nsList) == 0 {
		nsList = append(nsList, s.cfg.Namespaces...)
		if len(nsList) == 0 {
			nsList = []string{""}
		}
	}
	end := to
	if now.Before(end) {
		end = now
	}
	d.Gaps = append(d.Gaps, cov.jobGaps(nsList, from, end)...)

	d.Rollouts, err = s.rollouts(ctx, from, to)
	return d, err
}

// onlySuspended: nothing ran and nothing was due but suspended times, so
// the operation is idle on that day rather than a lane of its own.
func onlySuspended(ms []Mark) bool {
	for _, m := range ms {
		if m.State != "suspended" {
			return false
		}
	}
	return true
}

// tally adds one operation's day to the counts, exceptions and upcoming.
func (s *Server) tally(d *Day, v opView, slots []schedule.Slot, from, to, now time.Time) {
	op := v.op
	exc := func(e Exception) {
		e.OperationID, e.Name, e.Owner, e.RunbookURL = op.ID, displayName(op), op.Owner, op.RunbookURL
		if op.ExpectedDuration != nil {
			e.Expected = int64(*op.ExpectedDuration / time.Second)
		}
		d.Exceptions = append(d.Exceptions, e)
	}
	for _, r := range v.runs {
		if r.CreatedAt.Before(from) || !r.CreatedAt.Before(to) {
			continue
		}
		d.Counts.Observed++
		rv := runView(r, op.ExpectedDuration, now)
		switch r.Status {
		case store.RunSucceeded:
			d.Counts.Succeeded++
			if rv.Slow {
				exc(Exception{At: r.FinishedAt, Kind: "slow", RunID: r.ID, ScheduledAt: r.ScheduledAt, Seconds: rv.Seconds, Attempts: r.Attempts})
			}
		case store.RunFailed:
			d.Counts.Failed++
			exc(Exception{At: r.FinishedAt, Kind: "failed", RunID: r.ID, ScheduledAt: r.ScheduledAt, Seconds: rv.Seconds, Attempts: r.Attempts, Reason: r.StatusReason})
		case store.RunUnknown:
			d.Counts.Unknown++
			exc(Exception{At: r.UpdatedAt, Kind: "lost", RunID: r.ID, ScheduledAt: r.ScheduledAt, Reason: r.StatusReason})
		default:
			d.Counts.Running++
		}
	}
	var next time.Time
	for _, sl := range slots {
		switch sl.State {
		case schedule.Unconfirmed:
			d.Counts.Unconfirmed++
			exc(Exception{At: sl.At, Kind: "unconfirmed", ScheduledAt: sl.At, Why: sl.Why})
		case schedule.Missed:
			d.Counts.Missed++
			exc(Exception{At: sl.At, Kind: "missed", ScheduledAt: sl.At, Why: sl.Why})
		case schedule.Skipped:
			d.Counts.Skipped++
			exc(Exception{At: sl.At, Kind: "skipped", ScheduledAt: sl.At, Why: sl.Why})
		case schedule.Due, schedule.Expected:
			d.Counts.StillDue++
			if sl.State == schedule.Expected && next.IsZero() {
				next = sl.At
			}
		}
	}
	if !next.IsZero() {
		d.Upcoming = append(d.Upcoming, Upcoming{At: next, OperationID: op.ID, Name: displayName(op), Namespace: op.Namespace})
	}
}

func (s *Server) rollouts(ctx context.Context, from, to time.Time) ([]Rollout, error) {
	page, err := s.db.Events(ctx, store.EventQuery{From: from, To: to, Types: []string{"deployment.rollout"}, Limit: 500})
	if err != nil {
		return nil, err
	}
	out := []Rollout{}
	for i := len(page.Events) - 1; i >= 0; i-- {
		e := page.Events[i]
		out = append(out, Rollout{At: e.At(), Namespace: e.Namespace, Name: e.ResourceName, Revision: e.Facts["revision"], Summary: e.Summary})
	}
	return out, nil
}
