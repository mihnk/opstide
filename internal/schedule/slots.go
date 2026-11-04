package schedule

import (
	"sort"
	"time"
)

// State is what became of one expected time.
type State string

const (
	// Expected: still in the future.
	Expected State = "expected"
	// Due: the time has passed but the controller may still start it
	// (within startingDeadlineSeconds, or a short grace without one).
	Due State = "due"
	// Ran: a Job scheduled for this time was observed.
	Ran State = "ran"
	// Skipped: concurrencyPolicy Forbid and the previous run was still
	// active, so the controller did not start one. Derived.
	Skipped State = "skipped"
	// Suspended: the CronJob was suspended at that time.
	Suspended State = "suspended"
	// Unconfirmed: no Job seen, but OpsTide was not watching Jobs for the
	// whole start window, so it cannot say one was not created.
	Unconfirmed State = "unconfirmed"
	// Missed: no Job was created although OpsTide watched the whole start
	// window and nothing in the CronJob's settings explains it. Derived.
	Missed State = "missed"
)

// Grace is how long after an expected time, with no startingDeadlineSeconds,
// a Job may appear before the time counts as missed. The controller
// normally creates Jobs within seconds.
const Grace = 2 * time.Minute

// Operation is what slot classification needs from a CronJob.
type Operation struct {
	CreatedAt         time.Time
	DeletedAt         time.Time // zero while it exists
	ConcurrencyPolicy string    // Allow | Forbid | Replace
	StartingDeadline  *time.Duration
	// Suspensions are the windows in which spec.suspend was true; an open
	// window has a zero End.
	Suspensions []Window
}

// Window is a span of time; a zero End means it has not ended.
type Window struct{ Start, End time.Time }

func (w Window) contains(t time.Time) bool {
	return !t.Before(w.Start) && (w.End.IsZero() || t.Before(w.End))
}

// Run is what slot classification needs from an observed Job.
type Run struct {
	ID          string
	ScheduledAt time.Time // zero for a Job not started by the schedule
	CreatedAt   time.Time
	FinishedAt  time.Time // zero while active
}

// Coverage reports whether Jobs were watched continuously over [from, to].
type Coverage func(from, to time.Time) bool

// Slot is one expected time and what became of it.
type Slot struct {
	At    time.Time
	State State
	RunID string // for Ran
	// Why explains Skipped, Suspended, Unconfirmed and Missed in words an
	// operator can act on.
	Why string
}

// Slots classifies every expected time in [from, to) as of now. runs must
// include any run still active at from, so Forbid can be judged at the
// window's first slot.
func Slots(s *Schedule, op Operation, runs []Run, covered Coverage, from, to, now time.Time) (slots []Slot, truncated bool) {
	if from.Before(op.CreatedAt) {
		from = op.CreatedAt
	}
	if !op.DeletedAt.IsZero() && op.DeletedAt.Before(to) {
		to = op.DeletedAt
	}
	if !from.Before(to) {
		return nil, false
	}
	times, truncated := s.Between(from, to)

	byTime := map[int64]string{}
	ordered := append([]Run(nil), runs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].CreatedAt.Before(ordered[j].CreatedAt) })
	for _, r := range ordered {
		if !r.ScheduledAt.IsZero() {
			byTime[r.ScheduledAt.Unix()] = r.ID
		}
	}
	window := Grace
	if op.StartingDeadline != nil {
		window = *op.StartingDeadline
	}

	slots = make([]Slot, 0, len(times))
	for _, t := range times {
		sl := Slot{At: t}
		switch id, ok := byTime[t.Unix()]; {
		case ok:
			sl.State, sl.RunID = Ran, id
		case suspendedAt(op.Suspensions, t):
			sl.State, sl.Why = Suspended, "The CronJob was suspended."
		case t.After(now):
			sl.State = Expected
		case op.ConcurrencyPolicy == "Forbid" && activeAt(ordered, t):
			sl.State, sl.Why = Skipped, "The previous run was still active and concurrencyPolicy is Forbid."
		case now.Before(t.Add(window)):
			sl.State = Due
		case !covered(t, t.Add(window)):
			sl.State, sl.Why = Unconfirmed, "No Job was seen, but OpsTide was not watching Jobs for all of its start window."
		default:
			sl.State, sl.Why = Missed, "No Job was created for this time, and OpsTide was watching throughout its start window."
		}
		slots = append(slots, sl)
	}
	return slots, truncated
}

func suspendedAt(ws []Window, t time.Time) bool {
	for _, w := range ws {
		if w.contains(t) {
			return true
		}
	}
	return false
}

// activeAt reports whether some run was started before t and had not
// finished by t.
func activeAt(runs []Run, t time.Time) bool {
	for _, r := range runs {
		if r.CreatedAt.Before(t) && (r.FinishedAt.IsZero() || r.FinishedAt.After(t)) {
			return true
		}
	}
	return false
}

// JobNameTime returns the scheduled time the CronJob controller encoded in
// a Job's name: "<cronjob>-<minutes since the Unix epoch>". ok is false
// when name does not have that form for this CronJob.
func JobNameTime(cronJob, job string) (t time.Time, ok bool) {
	if len(job) <= len(cronJob)+1 || job[:len(cronJob)] != cronJob || job[len(cronJob)] != '-' {
		return time.Time{}, false
	}
	var minutes int64
	for _, c := range job[len(cronJob)+1:] {
		if c < '0' || c > '9' {
			return time.Time{}, false
		}
		minutes = minutes*10 + int64(c-'0')
		if minutes > 1<<40 {
			return time.Time{}, false
		}
	}
	return time.Unix(minutes*60, 0).UTC(), true
}
