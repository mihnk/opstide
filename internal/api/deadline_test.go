package api

import (
	"context"
	"testing"
	"time"

	"github.com/mihnk/opstide/internal/store"
)

func TestDeadlineIsOverdueOnlyWhenOpsTideCouldHaveSeenASuccess(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	h := func(n float64) time.Duration { return time.Duration(n * float64(time.Hour)) }
	ago := func(n float64) time.Time { return now.Add(-h(n)) }
	dur := func(d time.Duration) *time.Duration { return &d }
	noGaps := func(from, to time.Time) []Span { return nil }
	gapsOf := func(spans ...Span) func(from, to time.Time) []Span {
		return func(from, to time.Time) []Span { return spans }
	}
	base := deadlineInput{within: 26 * time.Hour, created: ago(24 * 60), firstWatched: ago(24 * 40), now: now, retention: 30 * 24 * time.Hour, gaps: noGaps}
	cases := []struct {
		name   string
		tune   func(*deadlineInput)
		state  string
		reason string
		dueBy  time.Time
	}{
		{"succeeded 20 h ago", func(in *deadlineInput) { in.lastSuccess = ago(20) }, "met", "", ago(20).Add(26 * time.Hour)},
		{"succeeded exactly at the limit", func(in *deadlineInput) { in.lastSuccess = ago(26) }, "met", "", now},
		{"last success 30 h ago", func(in *deadlineInput) { in.lastSuccess = ago(30) }, "overdue", "", ago(4)},
		{"never succeeded, watched all along", func(in *deadlineInput) {}, "overdue", "", time.Time{}},
		{"created 3 h ago", func(in *deadlineInput) { in.created = ago(3) }, "waiting", "", ago(3).Add(26 * time.Hour)},
		{"watched for 10 h only", func(in *deadlineInput) { in.lastSuccess, in.firstWatched = ago(30), ago(10) }, "unknown", "before_first_observation", time.Time{}},
		{"never watched", func(in *deadlineInput) { in.firstWatched = time.Time{} }, "unknown", "before_first_observation", time.Time{}},
		{"limit longer than the history kept", func(in *deadlineInput) {
			in.within, in.created, in.firstWatched = 40*24*time.Hour, ago(24*90), ago(24*90)
		}, "unknown", "outside_retention", time.Time{}},
		{"suspended", func(in *deadlineInput) { in.suspended, in.lastSuccess = true, ago(30) }, "suspended", "", time.Time{}},
		// A restart's short gap: the latest succeeded Job is still in the cluster when OpsTide looks again.
		{"short gap, Jobs kept until replaced", func(in *deadlineInput) {
			in.lastSuccess, in.gaps = ago(30), gapsOf(Span{ago(5), ago(5).Add(time.Minute)})
		}, "overdue", "", ago(4)},
		{"gap longer than the Job TTL", func(in *deadlineInput) {
			in.lastSuccess, in.keptFor = ago(30), dur(10*time.Minute)
			in.gaps = gapsOf(Span{ago(5), ago(4)})
		}, "unknown", "not_watched", time.Time{}},
		{"gap shorter than the Job TTL", func(in *deadlineInput) {
			in.lastSuccess, in.keptFor = ago(30), dur(2*time.Hour)
			in.gaps = gapsOf(Span{ago(5), ago(4)})
		}, "overdue", "", ago(4)},
		{"successful Jobs not kept", func(in *deadlineInput) {
			in.lastSuccess, in.keptFor = ago(30), dur(0)
			in.gaps = gapsOf(Span{ago(5), ago(5).Add(time.Minute)})
		}, "unknown", "not_watched", time.Time{}},
		{"not watching now", func(in *deadlineInput) {
			in.lastSuccess, in.gaps = ago(30), gapsOf(Span{ago(1), now})
		}, "unknown", "not_watched", time.Time{}},
	}
	for _, c := range cases {
		in := base
		c.tune(&in)
		d := deadlineOf(in)
		if d.State != c.state || d.Reason != c.reason || !d.DueBy.Equal(c.dueBy) {
			t.Errorf("%s: got %s/%s due %v, want %s/%s due %v", c.name, d.State, d.Reason, d.DueBy, c.state, c.reason, c.dueBy)
		}
		if d.WithinSeconds != int64(in.within/time.Second) || !d.LastSuccess.Equal(in.lastSuccess) {
			t.Errorf("%s: within %d last %v", c.name, d.WithinSeconds, d.LastSuccess)
		}
		if c.reason == "not_watched" && len(d.Gaps) == 0 {
			t.Errorf("%s: an unknown for not watching names the stretches", c.name)
		}
	}
	if d := deadlineOf(func() deadlineInput { in := base; in.lastSuccess, in.firstWatched = ago(30), ago(10); return in }()); !d.KnownFrom.Equal(ago(10).Add(26 * time.Hour)) {
		t.Errorf("known from %v", d.KnownFrom)
	}
}

// End to end: an overdue CronJob says so on Schedules (with a reason under "Needs a look") and on its own page, also
// one that never succeeded while watched all along; one without the annotation has no deadline.
func TestSchedulesShowSuccessDeadlines(t *testing.T) {
	now := base.Add(12 * time.Hour)
	within := 26 * time.Hour
	backup := operation("backup", "15 3 * * *")
	backup.SucceedWithin = &within
	report := operation("report", "0 21 * * *")
	report.SucceedWithin = &within
	report.Namespace = "other"
	plain := operation("plain", "15 3 * * *")
	srv := setup(t, now, []store.Operation{backup, report, plain}, func(ctx context.Context, w *store.Writer) error {
		if err := w.OpenInterval(ctx, "jobs", store.Healthy, base.AddDate(0, 0, -20)); err != nil {
			return err
		}
		if err := succeeded(w, ctx, "backup", now.Add(-30*time.Hour)); err != nil {
			return err
		}
		return succeeded(w, ctx, "plain", now.Add(-30*time.Hour))
	})
	var ops Operations
	if code := get(t, srv, "/api/v1/operations", &ops); code != 200 {
		t.Fatal(code)
	}
	by := map[string]OperationRow{}
	for _, o := range ops.Operations {
		by[o.ID] = o
	}
	b := by["backup"]
	if b.Deadline == nil || b.Deadline.State != "overdue" || b.SucceedWithinSeconds != int64(within/time.Second) {
		t.Fatalf("backup: %+v within %d", b.Deadline, b.SucceedWithinSeconds)
	}
	found := false
	for _, w := range b.Attention {
		found = found || w == "no success within its limit"
	}
	if !found {
		t.Errorf("backup needs a look: %v", b.Attention)
	}
	if by["plain"].Deadline != nil {
		t.Error("no annotation, no deadline")
	}
	if d := by["report"].Deadline; d == nil || d.State != "overdue" {
		t.Errorf("report, watched all along and never succeeded: %+v", d)
	}
	var one OperationDetail
	if code := get(t, srv, "/api/v1/operations/backup", &one); code != 200 || one.Deadline == nil || one.Deadline.State != "overdue" {
		t.Fatalf("detail: %d %+v", code, one.Deadline)
	}
}

func TestDeadlinesForMetricsListOnlyCronJobsThatDeclareOne(t *testing.T) {
	now := base.Add(12 * time.Hour)
	within := 26 * time.Hour
	backup := operation("backup", "15 3 * * *")
	backup.SucceedWithin = &within
	ctx := context.Background()
	db, err := store.Open(ctx, t.TempDir()+"/opstide.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Update(ctx, func(w *store.Writer) error {
		for _, op := range []store.Operation{backup, operation("plain", "* * * * *")} {
			if err := w.UpsertOperation(ctx, op); err != nil {
				return err
			}
		}
		if err := w.OpenInterval(ctx, "jobs", store.Healthy, base.AddDate(0, 0, -20)); err != nil {
			return err
		}
		return succeeded(w, ctx, "backup", now.Add(-2*time.Hour))
	}); err != nil {
		t.Fatal(err)
	}
	s := New(db, watcher{}, Config{Retention: store.Retention{Days: 30}}, nil)
	s.now = func() time.Time { return now }
	ds, err := s.Deadlines(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 || ds[0].Name != "backup" || ds[0].Deadline.State != "met" {
		t.Fatalf("%+v", ds)
	}
}
