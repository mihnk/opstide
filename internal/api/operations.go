package api

import (
	"net/http"
	"sort"
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
	cov, err := loadCoverage(ctx, s.db, from.Add(-24*time.Hour), todayTo, now)
	if err != nil {
		return out, err
	}
	sort.Slice(ops, func(i, j int) bool { return byName(ops[i], ops[j]) })
	for _, op := range ops {
		v, err := s.view(ctx, op, byOp[op.ID])
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
	if last != nil && (last.Status == string(store.RunFailed) || last.Status == string(store.RunUnknown)) {
		why = append(why, "last run "+last.Status)
	}
	dayAgo := now.Add(-24 * time.Hour)
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
	Schema      int       `json:"schema"`
	TimeZone    string    `json:"timeZone"`
	Now         time.Time `json:"now"`
	Operation   Operation `json:"operation"`
	Next        time.Time `json:"next,omitzero"`
	Suspensions []Span    `json:"suspensions"` // To is zero while still suspended
	Days        []DayLane `json:"days"`        // the last 7 days, oldest first
	Gaps        []Span    `json:"gaps"`        // when its Jobs were not watched in those days
	Runs        []Run     `json:"runs"`        // newest first
	MoreRuns    bool      `json:"moreRuns"`
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
	v, err := s.view(ctx, op, windowRuns)
	if err != nil {
		return OperationDetail{}, err
	}
	cov, err := loadCoverage(ctx, s.db, from.Add(-24*time.Hour), todayTo, now)
	if err != nil {
		return OperationDetail{}, err
	}
	out := OperationDetail{Schema: Schema, TimeZone: loc.String(), Now: now, Operation: v.operation(), Suspensions: []Span{}, Runs: []Run{}}
	if v.sched != nil && !op.Suspend && op.DeletedAt.IsZero() {
		out.Next = v.sched.Next(now)
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
	out.Gaps = cov.jobGaps([]string{op.Namespace}, from, end)
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
