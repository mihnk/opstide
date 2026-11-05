package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mihnk/opstide/internal/store"
)

var base = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

func at(h, m int) time.Time { return time.Date(2026, 11, 4, h, m, 0, 0, time.UTC) }

// history: OpsTide started watching Jobs at 07:00 and was not watching from
// 09:10 to 09:40. Payments (every 30 min, expected 4 min) ran 07:00 to
// 11:30: slow at 07:30, three failures in a row from 08:00, nothing at 09:30
// (inside the gap), one more failure at 10:30. A busy operation runs every
// 2 minutes. Now is 12:00.
func history(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "opstide.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	four := 4 * time.Minute
	created := base.AddDate(0, 0, -30)
	run := func(w *store.Writer, op, ns, id string, sched time.Time, took time.Duration, status store.RunStatus) error {
		r := store.Run{ID: id, OperationID: op, Namespace: ns, JobName: id, ScheduledAt: sched, ScheduledBasis: "name",
			CreatedAt: sched.Add(time.Second), StartedAt: sched.Add(2 * time.Second), FinishedAt: sched.Add(2*time.Second + took),
			Status: status, Attempts: 1, ObservedAt: sched}
		if status == store.RunFailed {
			r.StatusReason, r.Attempts = "BackoffLimitExceeded", 2
		}
		return w.UpsertRun(ctx, r)
	}
	if err := db.Update(ctx, func(w *store.Writer) error {
		for _, op := range []store.Operation{
			{ID: "pay", Namespace: "payments", Name: "pay", DisplayName: "Payments", Owner: "team-payments", ExpectedDuration: &four,
				Schedule: "*/30 * * * *", ConcurrencyPolicy: "Allow", CreatedAt: created, FirstSeenAt: created, LastSeenAt: created},
			{ID: "busy", Namespace: "payments", Name: "busy", Schedule: "*/2 * * * *", ConcurrencyPolicy: "Allow",
				CreatedAt: created, FirstSeenAt: created, LastSeenAt: created},
		} {
			if err := w.UpsertOperation(ctx, op); err != nil {
				return err
			}
		}
		status := map[int]store.RunStatus{8*60 + 0: store.RunFailed, 8*60 + 30: store.RunFailed, 9*60 + 0: store.RunFailed, 10*60 + 30: store.RunFailed}
		for m := 7 * 60; m < 12*60; m += 30 {
			if m == 9*60+30 {
				continue // inside the gap
			}
			s, took := store.RunSucceeded, 3*time.Minute
			if st, ok := status[m]; ok {
				s = st
			}
			if m == 7*60+30 {
				took = 9 * time.Minute
			}
			if err := run(w, "pay", "payments", "pay-"+at(m/60, m%60).Format("1504"), at(m/60, m%60), took, s); err != nil {
				return err
			}
		}
		for m := 7 * 60; m < 12*60; m += 2 {
			if m >= 9*60+10 && m < 9*60+40 {
				continue
			}
			s := store.RunSucceeded
			if m%50 == 0 {
				s = store.RunFailed
			}
			if err := run(w, "busy", "payments", "busy-"+at(m/60, m%60).Format("1504"), at(m/60, m%60), 20*time.Second, s); err != nil {
				return err
			}
		}
		if err := w.OpenInterval(ctx, "jobs", store.Healthy, at(7, 0)); err != nil {
			return err
		}
		if err := w.CloseInterval(ctx, "jobs", at(9, 10), "watch expired"); err != nil {
			return err
		}
		if err := w.OpenInterval(ctx, "jobs", store.Healthy, at(9, 40)); err != nil {
			return err
		}
		return w.Heartbeat(ctx, base)
	}); err != nil {
		t.Fatal(err)
	}
	s := New(db, watcher{}, Config{Version: "test", Cluster: "c", Retention: store.Retention{Days: 30, MaxEvents: 1000}, StartedAt: at(7, 0)}, nil)
	s.now = func() time.Time { return base }
	mux := http.NewServeMux()
	s.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, s
}

func TestRangeGroupsExceptionsAndJudgesOnlyWhatItWatched(t *testing.T) {
	srv, _ := history(t)
	var d Range
	if code := get(t, srv, "/api/v1/range?within=12h&tz=UTC", &d); code != 200 {
		t.Fatal(code)
	}
	if !d.From.Equal(at(0, 0)) || !d.To.Equal(base) || !d.WatchedSince.Equal(at(7, 0)) {
		t.Fatalf("window %v – %v, watched since %v", d.From, d.To, d.WatchedSince)
	}
	// Midnight to 07:00 is before OpsTide watched: not a gap, not unconfirmed, not missed.
	if len(d.Gaps) != 1 || !d.Gaps[0].From.Equal(at(9, 10)) || !d.Gaps[0].To.Equal(at(9, 40)) {
		t.Fatalf("gaps %+v", d.Gaps)
	}
	if d.Counts.Missed != 0 || d.Counts.Skipped != 0 {
		t.Fatalf("judged time it did not watch: %+v", d.Counts)
	}
	// Payments: 1 slot (09:30) in the gap. Busy: 15 slots (09:10 … 09:38).
	if d.Counts.Unconfirmed != 16 {
		t.Fatalf("unconfirmed %d", d.Counts.Unconfirmed)
	}

	byKind := map[string][]Exception{}
	for _, e := range d.Exceptions {
		byKind[e.Kind] = append(byKind[e.Kind], e)
	}
	// Payments: 08:00, 08:30 and 09:00 are a streak of three; 10:30 stands alone.
	var streak, single *Exception
	for i, e := range byKind["failing"] {
		if e.OperationID == "pay" {
			streak = &byKind["failing"][i]
		}
	}
	for i, e := range byKind["failed"] {
		if e.OperationID == "pay" {
			single = &byKind["failed"][i]
		}
	}
	if streak == nil || streak.Count != 3 || !streak.From.Equal(at(8, 0).Add(time.Second)) || streak.Reason != "BackoffLimitExceeded" || streak.Owner != "team-payments" {
		t.Fatalf("streak %+v", streak)
	}
	if single == nil || single.Count != 1 || single.Attempts != 2 || !single.ScheduledAt.Equal(at(10, 30)) {
		t.Fatalf("single failure %+v", single)
	}
	var slow *Exception
	for i, e := range byKind["slow"] {
		if e.OperationID == "pay" {
			slow = &byKind["slow"][i]
		}
	}
	if slow == nil || slow.Count != 1 || slow.Seconds != 540 || slow.Expected != 240 {
		t.Fatalf("slow %+v", slow)
	}
	// One line for the gap, however many runs fell in it.
	if len(byKind["gap"]) != 1 {
		t.Fatalf("gap entries %+v", byKind["gap"])
	}
	if g := byKind["gap"][0]; g.Operations != 2 || g.Runs != 16 || !g.From.Equal(at(9, 10)) {
		t.Fatalf("gap %+v", g)
	}
	if len(byKind["unconfirmed"]) != 0 {
		t.Fatal("no entry per expected run")
	}

	// The lane with 9 runs draws each of them; the busy one is runs per hour.
	lanes := map[string]Lane{}
	for _, l := range d.Lanes {
		lanes[l.OperationID] = l
	}
	if p := lanes["pay"]; p.Mode != "runs" || len(p.Marks) == 0 || len(p.Buckets) != 0 {
		t.Fatalf("payments lane %+v", p)
	}
	b := lanes["busy"]
	if b.Mode != "hourly" || len(b.Marks) != 0 || len(b.Buckets) != 5 {
		t.Fatalf("busy lane %s, %d marks, %d buckets", b.Mode, len(b.Marks), len(b.Buckets))
	}
	runs, failed := 0, 0
	for _, x := range b.Buckets {
		runs += x.Runs
		failed += x.Failed
		if x.Start.Minute() != 0 || x.Start.Second() != 0 {
			t.Errorf("bucket %v not on the hour of the window", x.Start)
		}
	}
	// 07:00–11:58 is 150 slots, 15 of them in the gap; minutes 450, 500, 600, 650 and 700 fail (550 is in the gap).
	if runs != 135 || failed != 5 {
		t.Fatalf("busy runs %d failed %d", runs, failed)
	}
}

func TestRangeBeforeOpsTideWatchedIsNotJudged(t *testing.T) {
	srv, _ := history(t)
	var d Range
	// 03:00–06:00 is entirely before it watched.
	if code := get(t, srv, "/api/v1/range?from=2026-11-04T03:00:00Z&to=2026-11-04T06:00:00Z&tz=UTC", &d); code != 200 {
		t.Fatal(code)
	}
	if d.Counts.Unconfirmed != 0 || d.Counts.Missed != 0 || len(d.Gaps) != 0 || len(d.Exceptions) != 0 || len(d.Lanes) != 0 || d.Idle != 2 {
		t.Fatalf("%+v idle %d lanes %d", d.Counts, d.Idle, len(d.Lanes))
	}
}

func TestOverviewStrip(t *testing.T) {
	srv, _ := history(t)
	var o Overview
	if code := get(t, srv, "/api/v1/overview?span=24h&tz=UTC", &o); code != 200 {
		t.Fatal(code)
	}
	if !o.From.Equal(at(0, 0)) || !o.To.Equal(at(0, 0).AddDate(0, 0, 1)) || !o.WatchedSince.Equal(at(7, 0)) || o.RetentionDays != 30 || o.BucketSeconds != 3600 {
		t.Fatalf("%+v", o)
	}
	if len(o.Gaps) != 1 || !o.Gaps[0].From.Equal(at(9, 10)) {
		t.Fatalf("gaps %+v", o.Gaps)
	}
	runs, failed := 0, 0
	for _, b := range o.Buckets {
		runs += b.Runs
		failed += b.Failed
		if b.Start.Before(at(7, 0)) {
			t.Errorf("a bucket before it watched: %v", b.Start)
		}
	}
	// payments: 9 runs (10 slots minus the gap), busy: 135.
	if runs != 144 || failed != 4+5 {
		t.Fatalf("runs %d failed %d", runs, failed)
	}
	var five Overview
	get(t, srv, "/api/v1/overview?tz=UTC", &five)
	if five.Span != "5d" || !five.From.Equal(at(0, 0).AddDate(0, 0, -4)) {
		t.Fatalf("default span: %s from %v", five.Span, five.From)
	}
	var month Overview
	get(t, srv, "/api/v1/overview?span=30d&tz=UTC", &month)
	if month.BucketSeconds != 6*3600 {
		t.Fatalf("a month is drawn in 6 h buckets: %d", month.BucketSeconds)
	}
	for _, bad := range []string{"?span=forever", "?span=3d", "?tz=Mars/Base"} {
		if code := get(t, srv, "/api/v1/overview"+bad, nil); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
}

// setup builds a store with ops and whatever fn writes, and a server whose clock is now.
func setup(t *testing.T, now time.Time, ops []store.Operation, fn func(ctx context.Context, w *store.Writer) error) *httptest.Server {
	return setupWith(t, now, ops, fn, func(*Config) {})
}

func setupWith(t *testing.T, now time.Time, ops []store.Operation, fn func(ctx context.Context, w *store.Writer) error, tune func(*Config)) *httptest.Server {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "opstide.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Update(ctx, func(w *store.Writer) error {
		for _, op := range ops {
			if err := w.UpsertOperation(ctx, op); err != nil {
				return err
			}
		}
		return fn(ctx, w)
	}); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Version: "test", Cluster: "c", Retention: store.Retention{Days: 30, MaxEvents: 1000}, StartedAt: now}
	tune(&cfg)
	s := New(db, watcher{}, cfg, nil)
	s.now = func() time.Time { return now }
	mux := http.NewServeMux()
	s.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func operation(id string, schedule string) store.Operation {
	created := base.AddDate(0, 0, -60)
	return store.Operation{ID: id, Namespace: "ns", Name: id, Schedule: schedule, ConcurrencyPolicy: "Allow", CreatedAt: created, FirstSeenAt: created, LastSeenAt: created}
}

func succeeded(w *store.Writer, ctx context.Context, op string, sched time.Time) error {
	return w.UpsertRun(ctx, store.Run{ID: op + "-" + sched.Format("0102T1504"), OperationID: op, Namespace: "ns", JobName: op, ScheduledAt: sched, ScheduledBasis: "name",
		CreatedAt: sched.Add(time.Second), StartedAt: sched.Add(2 * time.Second), FinishedAt: sched.Add(20 * time.Second), Status: store.RunSucceeded, Attempts: 1, ObservedAt: sched})
}

// OpsTide watched on Oct 25 only, then was down until 09:00 today. The
// last interval ended days before the loaded stretch, yet 00:00–09:00 today
// is a gap, whichever window or strip asks about it.
func TestAnOutageBeforeTheLoadedCoverageIsStillAGap(t *testing.T) {
	long := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	srv := setup(t, base, []store.Operation{operation("o", "*/30 * * * *")}, func(ctx context.Context, w *store.Writer) error {
		if err := w.OpenInterval(ctx, "jobs", store.Healthy, long); err != nil {
			return err
		}
		if err := w.CloseInterval(ctx, "jobs", long.Add(24*time.Hour), "collector stopped"); err != nil {
			return err
		}
		if err := w.OpenInterval(ctx, "jobs", store.Healthy, at(9, 0)); err != nil {
			return err
		}
		for m := 9 * 60; m < 12*60; m += 30 {
			if err := succeeded(w, ctx, "o", at(m/60, m%60)); err != nil {
				return err
			}
		}
		return nil
	})
	for _, q := range []string{"/api/v1/range?within=12h&tz=UTC", "/api/v1/range?within=7d&tz=UTC"} {
		var d Range
		get(t, srv, q, &d)
		last := d.Gaps[len(d.Gaps)-1]
		if !last.To.Equal(at(9, 0)) || !d.WatchedSince.Equal(long) || d.Counts.Missed != 0 || d.Counts.Unconfirmed == 0 {
			t.Fatalf("%s: gaps %+v since %v counts %+v", q, d.Gaps, d.WatchedSince, d.Counts)
		}
	}
	var o Overview
	get(t, srv, "/api/v1/overview?span=5d&tz=UTC", &o)
	if len(o.Gaps) != 1 || !o.Gaps[0].To.Equal(at(9, 0)) || !o.Gaps[0].From.Equal(at(0, 0).AddDate(0, 0, -4)) {
		t.Fatalf("the strip tells the same outage: %+v", o.Gaps)
	}
}

// A busy lane drawn as runs per hour must still show the times that did
// not start: a * * * * * CronJob that stops creating Jobs.
func TestHourlyLanesKeepMissedTimes(t *testing.T) {
	srv := setup(t, base, []store.Operation{operation("o", "* * * * *")}, func(ctx context.Context, w *store.Writer) error {
		if err := w.OpenInterval(ctx, "jobs", store.Healthy, at(0, 0)); err != nil {
			return err
		}
		for m := 6 * 60; m < 9*60; m++ { // runs for 3 of the 6 hours; nothing after
			if err := succeeded(w, ctx, "o", at(m/60, m%60)); err != nil {
				return err
			}
		}
		return nil
	})
	var d Range
	get(t, srv, "/api/v1/range?from=2026-11-04T06:00:00Z&to=2026-11-04T12:00:00Z&tz=UTC", &d)
	if len(d.Lanes) != 1 || d.Lanes[0].Mode != "hourly" {
		t.Fatalf("lane %+v", d.Lanes)
	}
	runs, missed := 0, 0
	for _, b := range d.Lanes[0].Buckets {
		runs += b.Runs
		missed += b.Missed
	}
	// 09:00–11:58 did not start; the 11:59 slot is still inside its start window, so it is due.
	if runs != 180 || missed != 179 || d.Counts.Missed != 179 {
		t.Fatalf("runs %d missed %d (counts %d)", runs, missed, d.Counts.Missed)
	}
}

// The gap entries add up to the unconfirmed count, also with a long start
// window: a slot at 08:00 may start until 09:00, so a gap from 08:40 covers it.
func TestGapEntriesAddUpToUnconfirmed(t *testing.T) {
	op := operation("o", "0 8 * * *")
	hour := 3600 * time.Second
	_ = hour
	deadline := time.Hour
	op.StartingDeadline = &deadline
	srv := setup(t, base, []store.Operation{op}, func(ctx context.Context, w *store.Writer) error {
		if err := w.OpenInterval(ctx, "jobs", store.Healthy, at(0, 0)); err != nil {
			return err
		}
		if err := w.CloseInterval(ctx, "jobs", at(8, 40), "watch expired"); err != nil {
			return err
		}
		return w.OpenInterval(ctx, "jobs", store.Healthy, at(9, 0))
	})
	var d Range
	get(t, srv, "/api/v1/range?within=12h&tz=UTC", &d)
	got := 0
	for _, e := range d.Exceptions {
		if e.Kind == "gap" {
			got += e.Runs
		}
	}
	if d.Counts.Unconfirmed != 1 || got != 1 {
		t.Fatalf("unconfirmed %d, in gap entries %d: %+v", d.Counts.Unconfirmed, got, d.Exceptions)
	}
}

func TestRangeRejectsWhatItCannotAnswer(t *testing.T) {
	srv := setup(t, base, nil, func(context.Context, *store.Writer) error { return nil })
	for _, q := range []string{"?to=2026-11-04T10:00:00Z", "?within=106752d", "?within=0m", "?within=7d2h", "?from=2026-10-01T00:00:00Z&to=2026-11-04T00:00:00Z"} {
		if code := get(t, srv, "/api/v1/range"+q, nil); code != 400 {
			t.Errorf("%s: %d", q, code)
		}
	}
	if code := get(t, srv, "/api/v1/range?within=7d", nil); code != 200 {
		t.Errorf("exactly seven days: %d", code)
	}
}

// Hours of the strip start on the reader's hour, also half an hour off UTC.
func TestOverviewHoursFollowTheReadersClock(t *testing.T) {
	srv := setup(t, base, []store.Operation{operation("o", "*/30 * * * *")}, func(ctx context.Context, w *store.Writer) error {
		if err := w.OpenInterval(ctx, "jobs", store.Healthy, at(0, 0)); err != nil {
			return err
		}
		return succeeded(w, ctx, "o", at(8, 10))
	})
	var o Overview
	get(t, srv, "/api/v1/overview?span=24h&tz=Asia/Kolkata", &o)
	if len(o.Buckets) != 1 || !o.Buckets[0].Start.Equal(at(7, 30)) {
		t.Fatalf("08:10 UTC is 13:40 in India, in the hour that starts at 13:30 = 08:00 UTC? got %+v", o.Buckets)
	}
}

func TestLogFiltersBySeverityOutcomeAndOwner(t *testing.T) {
	pay, nightly, gone := operation("pay", "*/5 * * * *"), operation("nightly", "0 3 * * *"), operation("gone", "0 4 * * *")
	pay.Owner, nightly.Owner, gone.Owner = "payments-team", "data-team", "ümit-ekibi"
	gone.DeletedAt = base.Add(-time.Hour)
	srv := setup(t, base, []store.Operation{pay, nightly, gone}, func(ctx context.Context, w *store.Writer) error {
		// Newest first is f1, s1, l1, f2, g1, r1: ten minutes apart.
		for i, e := range []struct{ id, typ, op, sev string }{
			{"f1", "job.failed", "pay", "error"}, {"s1", "job.succeeded", "pay", "info"},
			{"l1", "job.lost", "pay", "warning"}, {"f2", "job.failed", "nightly", "error"},
			{"g1", "job.failed", "gone", "error"}, {"r1", "deployment.rollout", "", "info"},
		} {
			at := base.Add(-time.Duration(10*(i+1)) * time.Minute)
			if _, err := w.AddEvent(ctx, store.Event{ID: e.id, Type: e.typ, OperationID: e.op, Severity: e.sev,
				Confidence: store.Derived, ObservedAt: at, OccurredAt: at, Summary: e.id}); err != nil {
				return err
			}
		}
		return nil
	})
	ids := func(qs string) string {
		var e Entries
		if code := get(t, srv, "/api/v1/events?within=48h"+qs, &e); code != 200 {
			t.Fatalf("%s: %d", qs, code)
		}
		var out []string
		for _, x := range e.Entries {
			out = append(out, x.ID)
		}
		return strings.Join(out, ",") // newest first, as served
	}
	for qs, want := range map[string]string{
		"&show=runs&outcome=failed":                   "f1,f2,g1",
		"&show=runs&outcome=succeeded,unknown":        "s1,l1",
		"&show=runs&outcome=failed,failed":            "f1,f2,g1",
		"&show=runs,rollouts&outcome=failed":          "f1,f2,g1,r1",
		"&outcome=failed":                             "f1,f2,g1,r1", // no show: every group, runs narrowed
		"&show=runs&severity=error":                   "f1,f2,g1",
		"&show=runs&severity=warning,error":           "f1,l1,f2,g1",
		"&show=runs&severity=error&outcome=succeeded": "",
		"&show=runs,rollouts&owner=payments-team":     "f1,s1,l1",
		"&show=runs&owner=data-team&outcome=failed":   "f2",
		"&show=runs&owner=ümit-ekibi":                 "g1", // a deleted CronJob's entries stay filterable
		"&show=runs&owner=nobody":                     "",
		"&show=runs&limit=2&owner=payments-team":      "f1,s1",
	} {
		if got := ids(qs); got != want {
			t.Errorf("%s: got %q want %q", qs, got, want)
		}
	}
	// A page of a filtered log continues where it stopped.
	var first, second Entries
	get(t, srv, "/api/v1/events?within=48h&show=runs&severity=error&limit=2", &first)
	get(t, srv, "/api/v1/events?within=48h&show=runs&severity=error&limit=2&cursor="+first.NextCursor, &second)
	if len(first.Entries) != 2 || first.NextCursor == "" || len(second.Entries) != 1 || second.Entries[0].ID != "g1" {
		t.Fatalf("pages: %+v %+v", first, second)
	}
	for _, bad := range []string{"&outcome=bad", "&severity=fatal", "&show=starts&outcome=failed", "&show=rollouts&outcome=failed", "&owner=" + strings.Repeat("ü", 121)} {
		if code := get(t, srv, "/api/v1/events?x=1"+bad, nil); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	if code := get(t, srv, "/api/v1/events?owner="+strings.Repeat("ü", 120), nil); code != 200 {
		t.Errorf("120 characters: %d", code)
	}
	var o Owners
	if get(t, srv, "/api/v1/owners", &o); strings.Join(o.Owners, ",") != "data-team,payments-team,ümit-ekibi" {
		t.Fatalf("owners %v", o.Owners)
	}
}

func TestClustrailTemplatesAndLinks(t *testing.T) {
	for _, bad := range []string{
		"javascript:alert(1)", "ftp://x/{name}", "//x", "not a url", "HTTPS://x/{name}", "https:foo", "clustrail:open?n={name}",
		"{cluster}lustrail://x", "https://{name}.example/", "https://x{name}/", "https:///path", "https://x/a b", "https://x/\t", "https://x/\n",
	} {
		if ValidClustrailURL(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, ok := range []string{"", "https://c.example/{cluster}/{namespace}/{name}", "http://localhost:3000/?n={name}", "clustrail://open?ns={namespace}&name={name}", "clustrail://{cluster}/{namespace}/{name}"} {
		if err := ValidClustrailURL(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	// Path values are path-escaped, query values query-escaped.
	c := Config{Cluster: "prod eu", ClustrailURL: "https://c.example/{cluster}/{namespace}/{name}?cluster={cluster}&name={name}"}
	if got := c.clustrailLink("pay/ments", "a&b+c"); got != "https://c.example/prod%20eu/pay%2Fments/a&b+c?cluster=prod+eu&name=a%26b%2Bc" {
		t.Fatal(got)
	}
	if (Config{}).clustrailLink("a", "b") != "" {
		t.Fatal("link without a template")
	}

	// The operation detail carries it, except for a deleted CronJob.
	live, gone := operation("live", "0 3 * * *"), operation("gone", "0 3 * * *")
	gone.DeletedAt = base.Add(-time.Hour)
	noop := func(context.Context, *store.Writer) error { return nil }
	var d OperationDetail
	if get(t, setup(t, base, []store.Operation{live}, noop), "/api/v1/operations/live", &d); d.ClustrailURL != "" {
		t.Fatalf("link without a template: %q", d.ClustrailURL)
	}
	srv := setupWith(t, base, []store.Operation{live, gone}, noop, func(c *Config) { c.ClustrailURL = "clustrail://open/{namespace}/{name}" })
	if get(t, srv, "/api/v1/operations/live", &d); d.ClustrailURL != "clustrail://open/ns/live" {
		t.Fatalf("link: %q", d.ClustrailURL)
	}
	var deleted OperationDetail
	if get(t, srv, "/api/v1/operations/gone", &deleted); deleted.ClustrailURL != "" {
		t.Fatalf("a deleted CronJob has a link: %q", deleted.ClustrailURL)
	}
}
