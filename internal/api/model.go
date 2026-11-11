package api

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/mihnk/opstide/internal/schedule"
	"github.com/mihnk/opstide/internal/store"
)

// Span is a stretch of time.
type Span struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// coverage holds the watched intervals per scope, merged, with open
// intervals ending now.
type coverage struct {
	byScope map[string][]Span
	// first is when each Jobs scope was first watched, from the whole record:
	// an outage that began before the loaded stretch must still be a gap.
	first map[string]time.Time
}

func loadCoverage(ctx context.Context, db *store.Store, from, to, now time.Time) (*coverage, error) {
	ivs, err := db.Coverage(ctx, from, to)
	if err != nil {
		return nil, err
	}
	c := &coverage{byScope: map[string][]Span{}}
	first, err := db.FirstWatchedByScope(ctx)
	if err != nil {
		return nil, err
	}
	c.first = first
	for _, iv := range ivs {
		end := iv.EndedAt
		if end.IsZero() {
			end = now
		}
		c.byScope[iv.Scope] = append(c.byScope[iv.Scope], Span{iv.StartedAt, end})
	}
	for k, v := range c.byScope {
		c.byScope[k] = merge(v)
	}
	return c, nil
}

func merge(spans []Span) []Span {
	sort.Slice(spans, func(i, j int) bool { return spans[i].From.Before(spans[j].From) })
	var out []Span
	for _, s := range spans {
		if n := len(out); n > 0 && !s.From.After(out[n-1].To) {
			if s.To.After(out[n-1].To) {
				out[n-1].To = s.To
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// jobs is when Jobs in ns were watched: cluster-wide or that namespace.
func (c *coverage) jobs(ns string) []Span {
	return merge(append(append([]Span(nil), c.byScope["jobs"]...), c.byScope["jobs/"+ns]...))
}

func (c *coverage) covered(ns string) schedule.Coverage {
	spans := c.jobs(ns)
	return func(from, to time.Time) bool {
		for _, s := range spans {
			if !s.From.After(from) && !s.To.Before(to) {
				return true
			}
		}
		return false
	}
}

// gaps is the parts of [from, to) that spans do not cover.
func gaps(spans []Span, from, to time.Time) []Span {
	var out []Span
	at := from
	for _, s := range spans {
		if !s.To.After(at) {
			continue
		}
		if !s.From.Before(to) {
			break
		}
		if s.From.After(at) {
			out = append(out, Span{at, s.From})
		}
		at = s.To
		if !at.Before(to) {
			return out
		}
	}
	if at.Before(to) {
		out = append(out, Span{at, to})
	}
	return out
}

// firstWatched is when Jobs in ns were first watched (cluster-wide or in ns,
// whichever was first); zero when they never were.
func (c *coverage) firstWatched(ns string) time.Time {
	first := c.first["jobs"]
	if t := c.first["jobs/"+ns]; !t.IsZero() && (first.IsZero() || t.Before(first)) {
		first = t
	}
	return first
}

// jobGaps is when Jobs in any of namespaces were not watched, after they
// were first watched. Time before that is not a gap: OpsTide had not
// started, and says nothing about it.
func (c *coverage) jobGaps(namespaces []string, from, to time.Time) []Span {
	if !from.Before(to) {
		return nil
	}
	var all []Span
	for _, ns := range namespaces {
		first := c.firstWatched(ns)
		if first.IsZero() {
			continue
		}
		f := from
		if first.After(f) {
			f = first
		}
		// The loaded intervals may all start after f: then f up to the first is a gap.
		all = append(all, gaps(c.jobs(ns), f, to)...)
	}
	return merge(all)
}

// Mark is one thing on a timeline: a run, or an expected time that did
// not become one.
type Mark struct {
	Start time.Time `json:"start"`
	// End is when a run finished (now while it runs), or an expected
	// time plus the expected duration; zero when there is nothing to
	// size it by.
	End    time.Time `json:"end,omitzero"`
	State  string    `json:"state"`
	RunID  string    `json:"runId,omitempty"`
	Count  int       `json:"count,omitempty"` // expected times merged into this mark
	Slow   bool      `json:"slow,omitempty"`
	Manual bool      `json:"manual,omitempty"`
	Why    string    `json:"why,omitempty"`
	// Maintenance names the configured maintenance window the mark's time falls in (DERIVED context; the mark is
	// drawn and counted all the same).
	Maintenance string `json:"maintenance,omitempty"`
}

// Run is a run as the API shows it.
type Run struct {
	ID          string      `json:"id"`
	OperationID string      `json:"operationId,omitempty"`
	Namespace   string      `json:"namespace"`
	Job         string      `json:"job"`
	ScheduledAt time.Time   `json:"scheduledAt,omitzero"`
	Basis       string      `json:"basis,omitempty"` // annotation | name | manual
	CreatedAt   time.Time   `json:"createdAt"`
	StartedAt   time.Time   `json:"startedAt,omitzero"`
	FinishedAt  time.Time   `json:"finishedAt,omitzero"`
	Status      string      `json:"status"`
	Reason      string      `json:"reason,omitempty"`
	Seconds     int64       `json:"seconds,omitempty"` // so far, while running
	Slow        bool        `json:"slow,omitempty"`
	Attempts    int         `json:"attempts"`
	Pods        []store.Pod `json:"pods,omitempty"`
}

func runView(r store.Run, expected *time.Duration, now time.Time) Run {
	v := Run{ID: r.ID, OperationID: r.OperationID, Namespace: r.Namespace, Job: r.JobName, ScheduledAt: r.ScheduledAt,
		Basis: r.ScheduledBasis, CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
		Status: string(r.Status), Reason: r.StatusReason, Attempts: r.Attempts}
	start := r.StartedAt
	if start.IsZero() {
		start = r.CreatedAt
	}
	end := r.FinishedAt
	if end.IsZero() && r.Status == store.RunRunning {
		end = now
	}
	if !end.IsZero() && end.After(start) {
		v.Seconds = int64(end.Sub(start) / time.Second)
		v.Slow = expected != nil && end.Sub(start) > *expected
	}
	return v
}

// opView gathers what slots and marks need for one operation.
type opView struct {
	op       store.Operation
	sched    *schedule.Schedule
	schedErr string
	runs     []store.Run
	susp     []schedule.Window
}

// view builds what one operation needs. susp holds the suspend and resume
// entries of all operations when the caller has them (one query for a whole
// screen); nil makes view ask for this operation's alone.
func (s *Server) view(ctx context.Context, op store.Operation, runs []store.Run, susp map[string][]store.Event) (opView, error) {
	v := opView{op: op, runs: runs}
	sc, err := schedule.Parse(op.Schedule, op.TimeZone, s.cfg.ControllerZone)
	if err != nil {
		v.schedErr = err.Error()
	} else {
		v.sched = sc
	}
	if susp == nil {
		if susp, err = s.db.SuspensionEvents(ctx, op.ID); err != nil {
			return v, err
		}
	}
	v.susp = suspensionWindows(op, susp[op.ID])
	return v, nil
}

// suspensionWindows rebuilds when spec.suspend was true from the logged
// suspends and resumes. A CronJob already suspended when OpsTide first saw
// it counts as suspended from then.
func suspensionWindows(op store.Operation, evs []store.Event) []schedule.Window {
	sort.Slice(evs, func(i, j int) bool { return evs[i].At().Before(evs[j].At()) })
	var ws []schedule.Window
	var open *schedule.Window
	for i, e := range evs {
		switch e.Type {
		case "cronjob.suspended":
			if open == nil {
				ws = append(ws, schedule.Window{Start: e.At()})
				open = &ws[len(ws)-1]
			}
		case "cronjob.resumed":
			if open != nil {
				open.End, open = e.At(), nil
			} else if i == 0 {
				ws = append(ws, schedule.Window{Start: op.FirstSeenAt, End: e.At()})
			}
		}
	}
	if op.Suspend && open == nil && (len(evs) == 0 || evs[len(evs)-1].Type != "cronjob.suspended") {
		start := op.FirstSeenAt
		if len(evs) > 0 {
			start = evs[len(evs)-1].At()
		}
		ws = append(ws, schedule.Window{Start: start})
	}
	return ws
}

func toSlotRuns(runs []store.Run) []schedule.Run {
	out := make([]schedule.Run, 0, len(runs))
	for _, r := range runs {
		fin := r.FinishedAt
		if fin.IsZero() && r.Status == store.RunUnknown {
			fin = r.UpdatedAt
		}
		out = append(out, schedule.Run{ID: r.ID, ScheduledAt: r.ScheduledAt, CreatedAt: r.CreatedAt, FinishedAt: fin})
	}
	return out
}

// slots classifies the expected times in [from, to). Time before Jobs in
// the operation's namespace were first watched is not judged: no run is
// "expected" of a time OpsTide did not see, and nothing there is a gap.
func (v opView) slots(cov *coverage, from, to, now time.Time) ([]schedule.Slot, bool) {
	if v.sched == nil {
		return nil, false
	}
	first := cov.firstWatched(v.op.Namespace)
	if first.IsZero() {
		return nil, false
	}
	if first.After(from) {
		from = first
	}
	so := schedule.Operation{CreatedAt: v.op.CreatedAt, DeletedAt: v.op.DeletedAt, ConcurrencyPolicy: v.op.ConcurrencyPolicy,
		StartingDeadline: v.op.StartingDeadline, Suspensions: v.susp}
	return schedule.Slots(v.sched, so, toSlotRuns(v.runs), cov.covered(v.op.Namespace), from, to, now)
}

// mergeExpectedAbove: a lane with more expected (future) times than this
// shows each stretch of them as one mark.
const mergeExpectedAbove = 24

// marks lays out the operation's [from, to): its runs at their times and
// the expected times that did not become runs.
func (v opView) marks(slots []schedule.Slot, from, to, now time.Time) []Mark {
	byID := map[string]store.Run{}
	for _, r := range v.runs {
		byID[r.ID] = r
	}
	used := map[string]bool{}
	var out []Mark
	for _, sl := range slots {
		if sl.State == schedule.Unconfirmed {
			continue // drawn as a gap across the lanes, not as a mark of its own
		}
		if sl.State == schedule.Ran {
			if r, ok := byID[sl.RunID]; ok {
				used[r.ID] = true
				out = append(out, v.runMark(r, now))
			}
			continue
		}
		m := Mark{Start: sl.At, State: string(sl.State), Why: sl.Why}
		if v.op.ExpectedDuration != nil {
			m.End = sl.At.Add(*v.op.ExpectedDuration)
		}
		out = append(out, m)
	}
	for _, r := range v.runs {
		if used[r.ID] || r.CreatedAt.Before(from) || !r.CreatedAt.Before(to) {
			continue
		}
		out = append(out, v.runMark(r, now))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	expected := 0
	for _, m := range out {
		if m.State == "expected" {
			expected++
		}
	}
	if expected > mergeExpectedAbove {
		out = mergeExpected(out)
	}
	return out
}

func (v opView) runMark(r store.Run, now time.Time) Mark {
	rv := runView(r, v.op.ExpectedDuration, now)
	start := r.StartedAt
	if start.IsZero() {
		start = r.CreatedAt
	}
	end := r.FinishedAt
	if end.IsZero() && (r.Status == store.RunRunning || r.Status == store.RunPending) {
		end = now
	}
	return Mark{Start: start, End: end, State: string(r.Status), RunID: r.ID, Slow: rv.Slow, Manual: r.ScheduledBasis == "manual"}
}

func mergeExpected(ms []Mark) []Mark {
	var out []Mark
	for _, m := range ms {
		if n := len(out); n > 0 && m.State == "expected" && out[n-1].State == "expected" {
			last := &out[n-1]
			if last.Count == 0 {
				last.Count = 1
			}
			last.Count++
			last.End = m.Start
			if !m.End.IsZero() {
				last.End = m.End
			}
			continue
		}
		out = append(out, m)
	}
	return out
}

// Operation is a CronJob as the API shows it.
type Operation struct {
	ID               string    `json:"id"`
	Namespace        string    `json:"namespace"`
	Name             string    `json:"name"` // the CronJob's name
	DisplayName      string    `json:"displayName,omitempty"`
	Description      string    `json:"description,omitempty"`
	Owner            string    `json:"owner,omitempty"`
	Criticality      string    `json:"criticality,omitempty"`
	RunbookURL       string    `json:"runbookUrl,omitempty"`
	ExpectedSeconds  int64     `json:"expectedSeconds,omitempty"`
	MetadataProblems []string  `json:"metadataProblems,omitempty"`
	Schedule         string    `json:"schedule"`
	TimeZone         string    `json:"timeZone,omitempty"`
	ZoneAssumed      string    `json:"zoneAssumed,omitempty"` // the zone used when spec.timeZone is unset
	ScheduleError    string    `json:"scheduleError,omitempty"`
	Suspended        bool      `json:"suspended,omitempty"`
	Concurrency      string    `json:"concurrency"`
	DeadlineSeconds  *int64    `json:"startingDeadlineSeconds,omitempty"`
	KeepSucceeded    int       `json:"successfulJobsHistoryLimit"`
	KeepFailed       int       `json:"failedJobsHistoryLimit"`
	CreatedAt        time.Time `json:"createdAt"`
	FirstSeenAt      time.Time `json:"firstSeenAt"`
	DeletedAt        time.Time `json:"deletedAt,omitzero"`
}

func (v opView) operation() Operation {
	op := v.op
	o := Operation{ID: op.ID, Namespace: op.Namespace, Name: op.Name, DisplayName: op.DisplayName, Description: op.Description,
		Owner: op.Owner, Criticality: op.Criticality, RunbookURL: op.RunbookURL, MetadataProblems: op.MetadataProblems,
		Schedule: op.Schedule, TimeZone: op.TimeZone, ScheduleError: v.schedErr, Suspended: op.Suspend,
		Concurrency: op.ConcurrencyPolicy, KeepSucceeded: op.SuccessfulHistory, KeepFailed: op.FailedHistory,
		CreatedAt: op.CreatedAt, FirstSeenAt: op.FirstSeenAt, DeletedAt: op.DeletedAt}
	if op.ExpectedDuration != nil {
		o.ExpectedSeconds = int64(*op.ExpectedDuration / time.Second)
	}
	if op.StartingDeadline != nil {
		d := int64(*op.StartingDeadline / time.Second)
		o.DeadlineSeconds = &d
	}
	if v.sched != nil && v.sched.ZoneAssumed {
		o.ZoneAssumed = v.sched.Location().String()
	}
	return o
}

func displayName(op store.Operation) string {
	if op.DisplayName != "" {
		return op.DisplayName
	}
	return op.Name
}

func groupRuns(runs []store.Run) map[string][]store.Run {
	out := map[string][]store.Run{}
	for _, r := range runs {
		if r.OperationID != "" {
			out[r.OperationID] = append(out[r.OperationID], r)
		}
	}
	return out
}

// alive reports whether op existed at some point in [from, to).
func alive(op store.Operation, from, to time.Time) bool {
	return op.CreatedAt.Before(to) && (op.DeletedAt.IsZero() || op.DeletedAt.After(from))
}

func byName(a, b store.Operation) bool {
	an, bn := strings.ToLower(displayName(a)), strings.ToLower(displayName(b))
	if an != bn {
		return an < bn
	}
	return a.Namespace < b.Namespace
}
