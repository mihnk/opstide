// Package schedule works out when a CronJob was expected to run and what
// became of each expected time.
//
// Expected times come from spec.schedule in spec.timeZone, evaluated with
// the same cron library the Kubernetes CronJob controller uses, so
// daylight-saving edges behave the same way: a time inside a skipped hour
// does not fire and a time inside a repeated hour fires twice.
package schedule

import (
	"fmt"
	"time"
	_ "time/tzdata" // zone data inside the binary; distroless images may lack it

	"github.com/robfig/cron/v3"
)

// MaxSlots bounds how many expected times one query may produce, so a
// "* * * * *" schedule over a month cannot exhaust memory.
const MaxSlots = 10_000

// Schedule is a parsed CronJob schedule in its time zone.
type Schedule struct {
	spec cron.Schedule
	loc  *time.Location
	// ZoneAssumed is true when the CronJob sets no spec.timeZone and the
	// controller's zone was assumed.
	ZoneAssumed bool
}

// Parse parses a CronJob schedule. timeZone is spec.timeZone; when it is
// empty, controllerZone (the kube-controller-manager's zone, normally UTC)
// is used and ZoneAssumed is set.
func Parse(schedule, timeZone string, controllerZone *time.Location) (*Schedule, error) {
	spec, err := cron.ParseStandard(schedule)
	if err != nil {
		return nil, fmt.Errorf("schedule %q: %w", schedule, err)
	}
	s := &Schedule{spec: spec, loc: controllerZone}
	if timeZone == "" {
		s.ZoneAssumed = true
		if s.loc == nil {
			s.loc = time.UTC
		}
		return s, nil
	}
	if s.loc, err = time.LoadLocation(timeZone); err != nil {
		return nil, fmt.Errorf("time zone %q: %w", timeZone, err)
	}
	return s, nil
}

// Location is the zone the schedule is evaluated in.
func (s *Schedule) Location() *time.Location { return s.loc }

// Next is the first expected time strictly after t.
func (s *Schedule) Next(t time.Time) time.Time { return s.spec.Next(t.In(s.loc)).UTC() }

// Between returns the expected times in [from, to), in UTC, at most
// MaxSlots of them; truncated reports whether there were more.
func (s *Schedule) Between(from, to time.Time) (times []time.Time, truncated bool) {
	// Next works in whole seconds and returns times strictly after its
	// argument, so start a second early and drop anything before from.
	for t := s.spec.Next(from.In(s.loc).Add(-time.Second)); !t.IsZero() && t.Before(to); t = s.spec.Next(t) {
		if t.Before(from) {
			continue
		}
		if len(times) == MaxSlots {
			return times, true
		}
		times = append(times, t.UTC())
	}
	return times, false
}
