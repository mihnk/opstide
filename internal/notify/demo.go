package notify

import (
	"context"
	"time"

	"github.com/mihnk/opstide/internal/api"
	"github.com/mihnk/opstide/internal/schedule"
)

// Demo is the morning summary of `opstide -demo`: one made-up Slack destination that took this morning's summary.
// Nothing is ever sent.
type Demo struct {
	now   func() time.Time
	sched *schedule.Schedule
}

const demoSchedule = "30 8 * * 1-5"

// NewDemo makes the demo's notifier.
func NewDemo(now func() time.Time) *Demo {
	sc, _ := schedule.Parse(demoSchedule, "UTC", nil)
	return &Demo{now: now, sched: sc}
}

// Status says a summary goes to #ops-morning on weekdays at 08:30, and went there the last time it was due.
func (d *Demo) Status() api.NotifyStatus {
	now := d.now().UTC()
	var last time.Time
	if times, _ := d.sched.Between(now.Add(-7*24*time.Hour), now); len(times) > 0 {
		last = times[len(times)-1]
	}
	dest := api.DestinationStatus{Name: "#ops-morning", Type: Slack, Source: "Secret opstide-notify, key slack"}
	if !last.IsZero() {
		at := last.Add(2 * time.Second)
		dest.Last, dest.LastOK = &api.Delivery{At: at, OK: true, Status: 200}, at
	}
	return api.NotifyStatus{Configured: true, Demo: true, Schedule: demoSchedule, TimeZone: "UTC", Covers: "since-18h", Language: "en",
		Include: []string{"failures", "troubles", "gaps", "due"}, Next: d.sched.Next(now), LastSent: last, Destinations: []api.DestinationStatus{dest}}
}

// Test sends nothing, and says so.
func (d *Demo) Test(context.Context, string) (api.Delivery, error) {
	return api.Delivery{At: d.now().UTC(), Test: true, Error: "The demo sends nothing."}, nil
}
