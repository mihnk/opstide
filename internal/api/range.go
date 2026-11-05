package api

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mihnk/opstide/internal/schedule"
	"github.com/mihnk/opstide/internal/store"
)

const (
	// A lane with more marks than this in the window shows runs per hour
	// instead: a mark is at least 3 px wide, so more would fuse into a block.
	denseAbove = 120
	// A window is between 5 minutes and 7 days (the extra hour covers
	// daylight-saving days).
	minWindow = 5 * time.Minute
	maxWindow = 7*24*time.Hour + time.Hour
	// Failures in a row that make a streak of their own.
	streakOf = 3
)

// Range is the Overview screen: what happened in one window of time, from
// five minutes to seven days.
type Range struct {
	Schema    int       `json:"schema"`
	Cluster   string    `json:"cluster"`
	TimeZone  string    `json:"timeZone"`
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	Now       time.Time `json:"now"`
	LastWrite time.Time `json:"lastWrite,omitzero"`
	// Date is set when the window is one calendar day in TimeZone.
	Date string `json:"date,omitempty"`
	// BucketSeconds is the length of a bucket in lanes with mode "hourly".
	BucketSeconds int `json:"bucketSeconds"`
	// WatchedSince is when OpsTide first watched Jobs; before it, nothing
	// is claimed. Zero when it never has.
	WatchedSince time.Time `json:"watchedSince,omitzero"`
	Counts       Counts    `json:"counts"`
	// Gaps are the times in the window, after WatchedSince and up to now,
	// when Jobs were not watched.
	Gaps       []Span      `json:"gaps"`
	Lanes      []Lane      `json:"lanes"`
	Idle       int         `json:"idle"` // operations with nothing due or run in the window
	Rollouts   []Rollout   `json:"rollouts"`
	Exceptions []Exception `json:"exceptions"`
	Upcoming   []Upcoming  `json:"upcoming"`
}

// Counts sums the window's runs and expected times.
type Counts struct {
	Observed    int `json:"observed"` // runs created in the window
	Succeeded   int `json:"succeeded"`
	Failed      int `json:"failed"`
	Running     int `json:"running"`
	Unknown     int `json:"unknown"`
	Unconfirmed int `json:"unconfirmed"` // expected times inside a gap
	Missed      int `json:"missed"`
	Skipped     int `json:"skipped"`
	StillDue    int `json:"stillDue"` // expected or due, not yet run
}

// Lane is one operation in the window.
type Lane struct {
	OperationID     string `json:"operationId"`
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	Schedule        string `json:"schedule"`
	TimeZone        string `json:"timeZone,omitempty"`
	Suspended       bool   `json:"suspended,omitempty"`
	ExpectedSeconds int64  `json:"expectedSeconds,omitempty"`
	ScheduleError   string `json:"scheduleError,omitempty"`
	// Mode is "runs" (every run and expected time is a mark) or "hourly"
	// (runs per hour), chosen per lane: a busy lane would be a solid block.
	Mode      string   `json:"mode"`
	Marks     []Mark   `json:"marks"`             // mode "runs"
	Buckets   []Bucket `json:"buckets,omitempty"` // mode "hourly"
	Truncated bool     `json:"truncated,omitempty"`
}

// Bucket counts one operation's runs in one hour of the reader's clock
// (the first and last may reach outside the window).
type Bucket struct {
	Start   time.Time `json:"start"`
	Runs    int       `json:"runs"`
	Failed  int       `json:"failed,omitempty"`
	Unknown int       `json:"unknown,omitempty"`
	// Expected times in the bucket that did not start, or were skipped.
	Missed  int `json:"missed,omitempty"`
	Skipped int `json:"skipped,omitempty"`
}

// Rollout is a finished Deployment rollout.
type Rollout struct {
	At        time.Time `json:"at"`
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	Revision  string    `json:"revision"`
	Summary   string    `json:"summary"`
}

// Exception is something in the window that needs a look. Similar things
// are one entry: a CronJob that failed forty times is one line with a count.
type Exception struct {
	// Kind is failing (a streak of failures in a row), failed (the other
	// failures), lost, slow, missed, skipped or gap.
	Kind        string    `json:"kind"`
	At          time.Time `json:"at"`            // the latest time it happened
	From        time.Time `json:"from,omitzero"` // a streak or gap starts
	To          time.Time `json:"to,omitzero"`   // and ends
	Count       int       `json:"count"`
	OperationID string    `json:"operationId,omitempty"`
	Name        string    `json:"name,omitempty"`
	RunID       string    `json:"runId,omitempty"` // the latest (or, for slow, the longest) run
	ScheduledAt time.Time `json:"scheduledAt,omitzero"`
	Seconds     int64     `json:"seconds,omitempty"` // how long the run took (the longest, for slow)
	Expected    int64     `json:"expectedSeconds,omitempty"`
	Attempts    int       `json:"attempts,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	Owner       string    `json:"owner,omitempty"`
	RunbookURL  string    `json:"runbookUrl,omitempty"`
	Why         string    `json:"why,omitempty"`
	// For a gap: how many operations and expected runs fell inside it.
	Operations int `json:"operations,omitempty"`
	Runs       int `json:"expectedRuns,omitempty"`
}

// Upcoming is an operation's next expected time.
type Upcoming struct {
	At          time.Time `json:"at"`
	OperationID string    `json:"operationId"`
	Name        string    `json:"name"`
	Namespace   string    `json:"namespace"`
}

// parseSpan reads 90m, 3h or 5d.
func parseSpan(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil || days < 1 || days > 400 {
			return 0, badRequest("a span is a duration such as 90m, 3h or 5d")
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, badRequest("a span is a duration such as 90m, 3h or 5d")
	}
	return d, nil
}

// window reads which stretch of time is asked for: within=3h (ending now),
// date=YYYY-MM-DD (that calendar day in tz), from and to, or, with nothing,
// today.
func window(r *http.Request, loc *time.Location, now time.Time) (from, to time.Time, date string, err error) {
	q := r.URL.Query()
	switch {
	case q.Get("within") != "":
		d, err := parseSpan(q.Get("within"))
		if err != nil {
			return from, to, "", err
		}
		from, to = now.Add(-d), now
	case q.Get("to") != "" && q.Get("from") == "":
		return from, to, "", badRequest("to needs from")
	case q.Get("from") != "":
		if from, err = timeParam(r, "from"); err != nil {
			return from, to, "", err
		}
		to = now
		if q.Get("to") != "" {
			if to, err = timeParam(r, "to"); err != nil {
				return from, to, "", err
			}
		}
	default:
		day, err := dateParam(r, loc, now)
		if err != nil {
			return from, to, "", err
		}
		from, to = dayBounds(day, loc)
		date = from.Format("2006-01-02")
	}
	switch d := to.Sub(from); {
	case d < minWindow:
		return from, to, "", badRequest("the window is shorter than 5 minutes, or ends before it starts")
	case d > maxWindow:
		return from, to, "", badRequest("the window is longer than 7 days; the overview strip covers longer spans")
	}
	return from.UTC(), to.UTC(), date, nil
}

func (s *Server) rangeHandler(w http.ResponseWriter, r *http.Request) {
	v, err := s.buildRange(r)
	reply(w, r, s.log, v, err)
}

func (s *Server) buildRange(r *http.Request) (Range, error) {
	ctx := r.Context()
	loc, err := zone(r)
	if err != nil {
		return Range{}, err
	}
	now := s.now().UTC()
	from, to, date, err := window(r, loc, now)
	if err != nil {
		return Range{}, err
	}
	d := Range{Schema: Schema, Cluster: s.cfg.Cluster, TimeZone: loc.String(), From: from, To: to, Now: now, Date: date,
		BucketSeconds: int(time.Hour / time.Second), Gaps: []Span{}, Lanes: []Lane{}, Rollouts: []Rollout{}, Exceptions: []Exception{}, Upcoming: []Upcoming{}}
	if d.LastWrite, err = s.db.LastWrite(ctx); err != nil {
		return d, err
	}
	if d.WatchedSince, err = s.db.FirstWatched(ctx); err != nil {
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
	susp, err := s.db.SuspensionEvents(ctx, "")
	if err != nil {
		return d, err
	}
	// A day back, so start windows that began before the window can be judged.
	cov, err := loadCoverage(ctx, s.db, from.Add(-24*time.Hour), to, now)
	if err != nil {
		return d, err
	}

	var unconf []unconfirmedAt
	namespaces := map[string]bool{}
	for _, op := range ops {
		if !alive(op, from, to) {
			continue
		}
		namespaces[op.Namespace] = true
		v, err := s.view(ctx, op, byOp[op.ID], susp)
		if err != nil {
			return d, err
		}
		slots, truncated := v.slots(cov, from, to, now)
		busy := false
		for _, sl := range slots {
			if sl.State != schedule.Suspended {
				busy = true
			}
		}
		for _, r := range v.runs {
			if !r.CreatedAt.Before(from) && r.CreatedAt.Before(to) {
				busy = true
			}
		}
		if !busy && v.schedErr == "" {
			d.Idle++
			continue
		}
		lane := Lane{OperationID: op.ID, Name: displayName(op), Namespace: op.Namespace, Schedule: op.Schedule, TimeZone: op.TimeZone,
			Suspended: op.Suspend, ScheduleError: v.schedErr, Truncated: truncated, Marks: []Mark{}}
		if op.ExpectedDuration != nil {
			lane.ExpectedSeconds = int64(*op.ExpectedDuration / time.Second)
		}
		lane.Mode = "runs"
		if marks := v.marks(slots, from, to, now); markCount(marks) > denseAbove {
			lane.Mode, lane.Buckets = "hourly", buckets(v.runs, slots, from, to, loc)
		} else {
			lane.Marks = marks
		}
		d.Lanes = append(d.Lanes, lane)
		unconf = append(unconf, s.tally(&d, v, slots, from, to, now)...)
	}
	sort.SliceStable(d.Lanes, func(i, j int) bool {
		a, b := d.Lanes[i], d.Lanes[j]
		if a.Name != b.Name {
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		}
		return a.Namespace < b.Namespace
	})

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
	d.Exceptions = append(d.Exceptions, gapExceptions(d.Gaps, unconf)...)

	sort.SliceStable(d.Exceptions, func(i, j int) bool { return d.Exceptions[i].At.After(d.Exceptions[j].At) })
	sort.Slice(d.Upcoming, func(i, j int) bool { return d.Upcoming[i].At.Before(d.Upcoming[j].At) })
	if len(d.Upcoming) > 8 {
		d.Upcoming = d.Upcoming[:8]
	}
	d.Rollouts, err = s.rollouts(ctx, from, to)
	return d, err
}

// markCount is how many marks a lane would draw; an expected stretch merged
// into one mark counts as one.
func markCount(ms []Mark) int { return len(ms) }

// hourStart is the start of the reader's clock hour that t falls in.
func hourStart(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), 0, 0, 0, loc).UTC()
}

// buckets counts a lane's runs, and the times that did not become runs
// (missed, skipped), per hour of the reader's clock. A mark of the same kind
// the lane would otherwise show is not lost when the lane is too busy to
// draw every mark.
func buckets(runs []store.Run, slots []schedule.Slot, from, to time.Time, loc *time.Location) []Bucket {
	origin := hourStart(from, loc)
	byIndex := map[int]*Bucket{}
	at := func(t time.Time) *Bucket {
		i := int(t.Sub(origin) / time.Hour)
		b := byIndex[i]
		if b == nil {
			b = &Bucket{Start: origin.Add(time.Duration(i) * time.Hour)}
			byIndex[i] = b
		}
		return b
	}
	for _, sl := range slots {
		switch sl.State {
		case schedule.Missed:
			at(sl.At).Missed++
		case schedule.Skipped:
			at(sl.At).Skipped++
		}
	}
	for _, r := range runs {
		if r.CreatedAt.Before(from) || !r.CreatedAt.Before(to) {
			continue
		}
		b := at(r.CreatedAt)
		b.Runs++
		switch r.Status {
		case store.RunFailed:
			b.Failed++
		case store.RunUnknown:
			b.Unknown++
		}
	}
	out := make([]Bucket, 0, len(byIndex))
	for _, b := range byIndex {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

// unconfirmedAt is an expected time nobody was watching for, with the end of
// the window in which its Job could still have been created.
type unconfirmedAt struct {
	operationID string
	at, until   time.Time
}

// gapExceptions makes one entry per gap: how long, which operations and how
// many expected runs fell in it. Not one per expected run.
func gapExceptions(gaps []Span, unconf []unconfirmedAt) []Exception {
	out := make([]Exception, 0, len(gaps))
	ops := make([]map[string]bool, len(gaps))
	runs := make([]int, len(gaps))
	for i := range ops {
		ops[i] = map[string]bool{}
	}
	// An expected time is unconfirmed because part of its start window was not
	// watched, so it belongs to the first gap that window overlaps. Each one
	// to exactly one gap: the entries add up to Counts.Unconfirmed.
	for _, u := range unconf {
		for i, g := range gaps {
			if u.at.Before(g.To) && u.until.After(g.From) {
				runs[i]++
				ops[i][u.operationID] = true
				break
			}
		}
	}
	for i, g := range gaps {
		out = append(out, Exception{Kind: "gap", At: g.From, From: g.From, To: g.To, Count: 1, Operations: len(ops[i]), Runs: runs[i]})
	}
	return out
}

type streak struct{ runs []store.Run }

// tally adds one operation's window to the counts, exceptions and upcoming,
// and returns the expected times that fell in a gap.
func (s *Server) tally(d *Range, v opView, slots []schedule.Slot, from, to, now time.Time) []unconfirmedAt {
	op := v.op
	exc := func(e Exception) {
		e.OperationID, e.Name, e.Owner, e.RunbookURL = op.ID, displayName(op), op.Owner, op.RunbookURL
		if op.ExpectedDuration != nil {
			e.Expected = int64(*op.ExpectedDuration / time.Second)
		}
		d.Exceptions = append(d.Exceptions, e)
	}

	runs := append([]store.Run(nil), v.runs...)
	sort.Slice(runs, func(i, j int) bool { return runs[i].CreatedAt.Before(runs[j].CreatedAt) })
	var (
		failedRuns, slowRuns, lostRuns []store.Run
		cur                            streak
	)
	end := func(r store.Run) time.Time {
		if !r.FinishedAt.IsZero() {
			return r.FinishedAt
		}
		return r.CreatedAt
	}
	flush := func() {
		if len(cur.runs) >= streakOf {
			first, last := cur.runs[0], cur.runs[len(cur.runs)-1]
			exc(Exception{Kind: "failing", At: end(last), From: first.CreatedAt, To: end(last), Count: len(cur.runs),
				RunID: last.ID, ScheduledAt: last.ScheduledAt, Attempts: last.Attempts, Reason: last.StatusReason})
		} else {
			failedRuns = append(failedRuns, cur.runs...)
		}
		cur.runs = nil
	}
	for _, r := range runs {
		if r.CreatedAt.Before(from) || !r.CreatedAt.Before(to) {
			continue
		}
		d.Counts.Observed++
		switch r.Status {
		case store.RunSucceeded:
			d.Counts.Succeeded++
			flush()
			if runView(r, op.ExpectedDuration, now).Slow {
				slowRuns = append(slowRuns, r)
			}
		case store.RunFailed:
			d.Counts.Failed++
			cur.runs = append(cur.runs, r)
		case store.RunUnknown:
			d.Counts.Unknown++
			lostRuns = append(lostRuns, r)
		default:
			d.Counts.Running++
		}
	}
	flush()

	if n := len(failedRuns); n > 0 {
		last := failedRuns[n-1]
		for _, r := range failedRuns {
			if end(r).After(end(last)) {
				last = r
			}
		}
		exc(Exception{Kind: "failed", At: end(last), Count: n, RunID: last.ID, ScheduledAt: last.ScheduledAt,
			Seconds: runView(last, op.ExpectedDuration, now).Seconds, Attempts: last.Attempts, Reason: last.StatusReason})
	}
	if n := len(slowRuns); n > 0 {
		worst, latest := slowRuns[0], slowRuns[0]
		for _, r := range slowRuns {
			if runView(r, op.ExpectedDuration, now).Seconds > runView(worst, op.ExpectedDuration, now).Seconds {
				worst = r
			}
			if end(r).After(end(latest)) {
				latest = r
			}
		}
		exc(Exception{Kind: "slow", At: end(latest), Count: n, RunID: worst.ID, ScheduledAt: worst.ScheduledAt,
			Seconds: runView(worst, op.ExpectedDuration, now).Seconds})
	}
	if n := len(lostRuns); n > 0 {
		last := lostRuns[0]
		for _, r := range lostRuns {
			if r.UpdatedAt.After(last.UpdatedAt) {
				last = r
			}
		}
		exc(Exception{Kind: "lost", At: last.UpdatedAt, Count: n, RunID: last.ID, ScheduledAt: last.ScheduledAt, Reason: last.StatusReason})
	}

	var unconf []unconfirmedAt
	type group struct {
		first, last time.Time
		n           int
		why         string
	}
	var missed, skipped group
	add := func(g *group, sl schedule.Slot) {
		if g.n == 0 {
			g.first, g.why = sl.At, sl.Why
		}
		g.last = sl.At
		g.n++
	}
	startWindow := schedule.Grace
	if op.StartingDeadline != nil {
		startWindow = *op.StartingDeadline
	}
	var next time.Time
	for _, sl := range slots {
		switch sl.State {
		case schedule.Unconfirmed:
			d.Counts.Unconfirmed++
			unconf = append(unconf, unconfirmedAt{op.ID, sl.At, sl.At.Add(startWindow)})
		case schedule.Missed:
			d.Counts.Missed++
			add(&missed, sl)
		case schedule.Skipped:
			d.Counts.Skipped++
			add(&skipped, sl)
		case schedule.Due, schedule.Expected:
			d.Counts.StillDue++
			if sl.State == schedule.Expected && next.IsZero() {
				next = sl.At
			}
		}
	}
	if missed.n > 0 {
		exc(Exception{Kind: "missed", At: missed.last, From: missed.first, To: missed.last, Count: missed.n, Why: missed.why})
	}
	if skipped.n > 0 {
		exc(Exception{Kind: "skipped", At: skipped.last, From: skipped.first, To: skipped.last, Count: skipped.n, Why: skipped.why})
	}
	if !next.IsZero() {
		d.Upcoming = append(d.Upcoming, Upcoming{At: next, OperationID: op.ID, Name: displayName(op), Namespace: op.Namespace})
	}
	return unconf
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
