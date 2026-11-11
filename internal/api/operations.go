package api

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mihnk/opstide/internal/schedule"
	"github.com/mihnk/opstide/internal/store"
)

// OperationRow is one line of the Schedules screen.
type OperationRow struct {
	Operation
	Week      []DayCell `json:"week"` // the last 7 days, oldest first, today last
	LastRun   *Run      `json:"lastRun,omitempty"`
	Next      time.Time `json:"next,omitzero"` // predicted from the schedule
	Attention []string  `json:"attention,omitempty"`
	// Duration compares the recent run durations with the baseline (see DurationTrend); no per-run points here.
	Duration DurationTrend `json:"duration"`
}

// DayCell sums one day of one operation.
type DayCell struct {
	Date string `json:"date"`
	// State is the worst of the day: failed, missed, unconfirmed, ok,
	// suspended, or none (nothing due and nothing ran).
	State string `json:"state"`
	Runs  int    `json:"runs"`
}

// Operations is the Schedules screen.
type Operations struct {
	Schema     int            `json:"schema"`
	TimeZone   string         `json:"timeZone"`
	Now        time.Time      `json:"now"`
	Days       []string       `json:"days"` // dates of the week cells
	Operations []OperationRow `json:"operations"`
}

func (s *Server) operations(w http.ResponseWriter, r *http.Request) {
	v, err := s.buildOperations(r)
	reply(w, r, s.log, v, err)
}

func (s *Server) buildOperations(r *http.Request) (Operations, error) {
	ctx := r.Context()
	loc, err := zone(r)
	if err != nil {
		return Operations{}, err
	}
	now := s.now().UTC()
	todayFrom, todayTo := dayBounds(now, loc)
	from := todayFrom.AddDate(0, 0, -6)
	out := Operations{Schema: Schema, TimeZone: loc.String(), Now: now, Operations: []OperationRow{}}
	for d := from; d.Before(todayTo); d = d.AddDate(0, 0, 1) {
		out.Days = append(out.Days, d.Format("2006-01-02"))
	}

	ops, err := s.db.Operations(ctx, false)
	if err != nil {
		return out, err
	}
	runs, err := s.db.RunsBetween(ctx, from, todayTo)
	if err != nil {
		return out, err
	}
	byOp := groupRuns(runs)
	susp, err := s.db.SuspensionEvents(ctx, "")
	if err != nil {
		return out, err
	}
	recentP, baselineP := s.trendPeriods(now, loc)
	cov, err := loadCoverage(ctx, s.db, earliest(from.Add(-24*time.Hour), baselineP.From), todayTo, now)
	if err != nil {
		return out, err
	}
	// One read of every CronJob's succeeded run durations, split at the start of the recent period.
	durs, err := s.db.DurationsByOperation(ctx, baselineP.From, recentP.From, recentP.To)
	if err != nil {
		return out, err
	}
	sort.Slice(ops, func(i, j int) bool { return byName(ops[i], ops[j]) })
	for _, op := range ops {
		v, err := s.view(ctx, op, byOp[op.ID], susp)
		if err != nil {
			return out, err
		}
		row := OperationRow{Operation: v.operation()}
		slots, _ := v.slots(cov, from, todayTo, now)
		row.Week = week(out.Days, loc, slots, v.runs, now)

		last, err := s.db.RecentRuns(ctx, op.ID, time.Time{}, 1)
		if err != nil {
			return out, err
		}
		if len(last) == 1 {
			lr := runView(last[0], op.ExpectedDuration, now)
			row.LastRun = &lr
		}
		if v.sched != nil && !op.Suspend {
			row.Next = v.sched.Next(now)
		}
		row.Attention = attention(v, slots, row.LastRun, now)
		d := durs[op.ID]
		row.Duration = durationTrend(op, d.After, d.Before, recentP, baselineP, cov, nil)
		out.Operations = append(out.Operations, row)
	}
	return out, nil
}

var cellRank = map[string]int{"none": 0, "suspended": 1, "ok": 2, "unconfirmed": 3, "missed": 4, "failed": 5}

func week(days []string, loc *time.Location, slots []schedule.Slot, runs []store.Run, now time.Time) []DayCell {
	cells := make([]DayCell, len(days))
	idx := map[string]int{}
	for i, d := range days {
		cells[i] = DayCell{Date: d, State: "none"}
		idx[d] = i
	}
	worse := func(at time.Time, state string) *DayCell {
		i, ok := idx[at.In(loc).Format("2006-01-02")]
		if !ok {
			return nil
		}
		if cellRank[state] > cellRank[cells[i].State] {
			cells[i].State = state
		}
		return &cells[i]
	}
	for _, r := range runs {
		state := "ok"
		switch r.Status {
		case store.RunFailed:
			state = "failed"
		case store.RunUnknown:
			state = "unconfirmed"
		}
		if c := worse(r.CreatedAt, state); c != nil {
			c.Runs++
		}
	}
	for _, sl := range slots {
		switch sl.State {
		case schedule.Missed:
			worse(sl.At, "missed")
		case schedule.Unconfirmed:
			worse(sl.At, "unconfirmed")
		case schedule.Suspended:
			if sl.At.Before(now) {
				worse(sl.At, "suspended")
			}
		}
	}
	return cells
}

// attention says why an operation needs a look, if it does.
func attention(v opView, slots []schedule.Slot, last *Run, now time.Time) []string {
	var why []string
	if v.schedErr != "" {
		why = append(why, "schedule cannot be read")
	}
	dayAgo := now.Add(-24 * time.Hour)
	if last != nil && (last.Status == string(store.RunFailed) || last.Status == string(store.RunUnknown)) {
		why = append(why, "last run "+last.Status)
	} else {
		for _, r := range v.runs {
			if r.Status == store.RunFailed && r.FinishedAt.After(dayAgo) {
				why = append(why, "failed in the last 24 h")
				break
			}
		}
	}
	var missed, unconfirmed int
	for _, sl := range slots {
		if sl.At.Before(dayAgo) {
			continue
		}
		switch sl.State {
		case schedule.Missed:
			missed++
		case schedule.Unconfirmed:
			unconfirmed++
		}
	}
	if missed > 0 {
		why = append(why, "missed in the last 24 h")
	}
	if unconfirmed > 0 {
		why = append(why, "not confirmed in the last 24 h")
	}
	if len(v.op.MetadataProblems) > 0 {
		why = append(why, "annotations need fixing")
	}
	return why
}

// OperationDetail is the Operation detail screen.
type OperationDetail struct {
	Schema    int       `json:"schema"`
	TimeZone  string    `json:"timeZone"`
	Now       time.Time `json:"now"`
	Operation Operation `json:"operation"`
	Next      time.Time `json:"next,omitzero"`
	// TimesToday is how many times the schedule fires today, in the
	// reader's zone; 0 when it cannot be read.
	TimesToday  int       `json:"timesToday"`
	Suspensions []Span    `json:"suspensions"` // To is zero while still suspended
	Days        []DayLane `json:"days"`        // the last 7 days, oldest first
	Gaps        []Span    `json:"gaps"`        // when its Jobs were not watched in those days
	Runs        []Run     `json:"runs"`        // newest first
	MoreRuns    bool      `json:"moreRuns"`
	// Duration compares the recent run durations with the baseline, with the latest runs' durations.
	Duration DurationTrend `json:"duration"`
	// ClustrailURL opens this CronJob in Clustrail, when a link is configured.
	ClustrailURL string `json:"clustrailUrl,omitempty"`
}

// clustrailTemplate is what a link template must look like: a lowercase
// http, https or clustrail scheme written out in full, and for http and https
// a host with no placeholder in it. charts/opstide/values.schema.json carries
// the same pattern, so Helm refuses what the collector would refuse to start with.
var clustrailTemplate = regexp.MustCompile(`^((https?://[^\s/?#{}]+([/?#][^\s]*)?)|(clustrail://[^\s]*))?$`)

// ValidClustrailURL reports whether t is a usable link template.
func ValidClustrailURL(t string) error {
	if !clustrailTemplate.MatchString(t) {
		return fmt.Errorf("clustrail url %q must start with http://, https:// or clustrail://, have a host without {placeholders}, and contain no spaces", t)
	}
	return nil
}

// clustrailLink fills the template. Before a "?" the values go into a path
// (space is %20); after it they go into a query (space is +).
func (c Config) clustrailLink(namespace, name string) string {
	if c.ClustrailURL == "" {
		return ""
	}
	fill := func(part string, esc func(string) string) string {
		return strings.NewReplacer("{cluster}", esc(c.Cluster), "{namespace}", esc(namespace), "{name}", esc(name)).Replace(part)
	}
	path, query, hasQuery := strings.Cut(c.ClustrailURL, "?")
	out := fill(path, url.PathEscape)
	if hasQuery {
		out += "?" + fill(query, url.QueryEscape)
	}
	return out
}

// DayLane is one day of one operation on its own timeline.
type DayLane struct {
	Date  string    `json:"date"`
	From  time.Time `json:"from"`
	To    time.Time `json:"to"`
	Marks []Mark    `json:"marks"`
}

func (s *Server) operation(w http.ResponseWriter, r *http.Request) {
	v, err := s.buildOperation(r)
	reply(w, r, s.log, v, err)
}

func (s *Server) buildOperation(r *http.Request) (OperationDetail, error) {
	ctx := r.Context()
	loc, err := zone(r)
	if err != nil {
		return OperationDetail{}, err
	}
	limit, err := intParam(r, "runs", 50, 500)
	if err != nil {
		return OperationDetail{}, err
	}
	before, err := timeParam(r, "before")
	if err != nil {
		return OperationDetail{}, err
	}
	op, ok, err := s.db.Operation(ctx, r.PathValue("id"))
	if err != nil {
		return OperationDetail{}, err
	}
	if !ok {
		return OperationDetail{}, errNotFound
	}
	now := s.now().UTC()
	todayFrom, todayTo := dayBounds(now, loc)
	from := todayFrom.AddDate(0, 0, -6)

	windowRuns, err := s.db.RunsForOperation(ctx, op.ID, from, todayTo)
	if err != nil {
		return OperationDetail{}, err
	}
	v, err := s.view(ctx, op, windowRuns, nil)
	if err != nil {
		return OperationDetail{}, err
	}
	recentP, baselineP := s.trendPeriods(now, loc)
	cov, err := loadCoverage(ctx, s.db, earliest(from.Add(-24*time.Hour), baselineP.From), todayTo, now)
	if err != nil {
		return OperationDetail{}, err
	}
	durs, err := s.db.SucceededDurations(ctx, op.ID, baselineP.From, recentP.To)
	if err != nil {
		return OperationDetail{}, err
	}
	out := OperationDetail{Schema: Schema, TimeZone: loc.String(), Now: now, Operation: v.operation(), Suspensions: []Span{}, Runs: []Run{}}
	// Templates address a CronJob by namespace and name: a deleted one has nothing to open.
	if op.DeletedAt.IsZero() {
		out.ClustrailURL = s.cfg.clustrailLink(op.Namespace, op.Name)
	}
	if v.sched != nil && !op.Suspend && op.DeletedAt.IsZero() {
		out.Next = v.sched.Next(now)
	}
	if v.sched != nil {
		times, _ := v.sched.Between(todayFrom, todayTo)
		out.TimesToday = len(times)
	}
	for _, w := range v.susp {
		out.Suspensions = append(out.Suspensions, Span{w.Start, w.End})
	}
	for d := from; d.Before(todayTo); d = d.AddDate(0, 0, 1) {
		end := d.AddDate(0, 0, 1)
		slots, _ := v.slots(cov, d, end, now)
		out.Days = append(out.Days, DayLane{Date: d.Format("2006-01-02"), From: d.UTC(), To: end.UTC(), Marks: nonNilMarks(v.marks(slots, d, end, now))})
	}
	end := todayTo
	if now.Before(end) {
		end = now
	}
	// Before the CronJob existed nothing was expected of it, watched or not.
	gapFrom := from
	if op.CreatedAt.After(gapFrom) {
		gapFrom = op.CreatedAt
	}
	rd, bd, pts := splitRuns(durs, recentP, baselineP)
	out.Duration = durationTrend(op, rd, bd, recentP, baselineP, cov, pts)
	out.Gaps = cov.jobGaps([]string{op.Namespace}, gapFrom, end)
	if out.Gaps == nil {
		out.Gaps = []Span{}
	}

	recent, err := s.db.RecentRuns(ctx, op.ID, before, limit+1)
	if err != nil {
		return out, err
	}
	if len(recent) > limit {
		recent, out.MoreRuns = recent[:limit], true
	}
	for _, r := range recent {
		out.Runs = append(out.Runs, runView(r, op.ExpectedDuration, now))
	}
	return out, nil
}

func nonNilMarks(m []Mark) []Mark {
	if m == nil {
		return []Mark{}
	}
	return m
}

func earliest(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}
