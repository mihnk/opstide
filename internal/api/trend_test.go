package api

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mihnk/opstide/internal/store"
)

func TestNearestRank(t *testing.T) {
	s := func(v ...int) []time.Duration {
		out := make([]time.Duration, len(v))
		for i, x := range v {
			out[i] = time.Duration(x) * time.Second
		}
		return out
	}
	for _, c := range []struct {
		in       []time.Duration
		p50, p95 time.Duration
	}{
		{s(7), 7 * time.Second, 7 * time.Second},
		{s(10, 20), 10 * time.Second, 20 * time.Second},
		{s(10, 20, 30), 20 * time.Second, 30 * time.Second},
		{s(10, 20, 30, 40, 50), 30 * time.Second, 50 * time.Second},
		{s(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20), 10 * time.Second, 19 * time.Second},
	} {
		if p := nearestRank(c.in, 0.5); p != c.p50 {
			t.Errorf("%v p50 %v", c.in, p)
		}
		if p := nearestRank(c.in, 0.95); p != c.p95 {
			t.Errorf("%v p95 %v", c.in, p)
		}
	}
}

// trendServer: now is 4 November 12:00 UTC, so (in UTC) recent is 29 October 00:00 to now and the baseline 6 to 29
// October. Jobs were watched from 1 October, except 30 October 10:00–11:00.
func trendServer(t *testing.T, tune func(*Config)) *httptest.Server {
	t.Helper()
	day := func(m time.Month, d, h int) time.Time { return time.Date(2026, m, d, h, 0, 0, 0, time.UTC) }
	young := operation("young", "0 3 * * *")
	young.CreatedAt = day(10, 20, 0)
	ops := []store.Operation{operation("slow", "0 3 * * *"), operation("few", "0 3 * * *"), young}
	n := 0
	run := func(w *store.Writer, ctx context.Context, op string, start time.Time, took time.Duration, st store.RunStatus) error {
		n++
		r := store.Run{ID: op + "-" + itoa(n), OperationID: op, Namespace: "ns", JobName: op, CreatedAt: start.Add(-time.Second), StartedAt: start,
			FinishedAt: start.Add(took), Status: st, Attempts: 1, ObservedAt: start}
		if st == store.RunRunning || st == store.RunUnknown {
			r.FinishedAt = time.Time{}
		}
		return w.UpsertRun(ctx, r)
	}
	srv := setupWith(t, base, ops, func(ctx context.Context, w *store.Writer) error {
		if err := w.OpenInterval(ctx, "jobs", store.Healthy, day(10, 1, 0)); err != nil {
			return err
		}
		if err := w.CloseInterval(ctx, "jobs", day(10, 30, 10), "watch expired"); err != nil {
			return err
		}
		if err := w.OpenInterval(ctx, "jobs", store.Healthy, day(10, 30, 11)); err != nil {
			return err
		}
		if err := w.Heartbeat(ctx, base); err != nil {
			return err
		}
		for _, op := range []string{"slow", "few", "young"} {
			for d := 10; d < 20; d++ { // baseline: ten runs of a minute
				if err := run(w, ctx, op, day(10, d, 3), time.Minute, store.RunSucceeded); err != nil {
					return err
				}
			}
		}
		// slow, recent: six succeeded runs, and runs that do not count.
		for i, took := range []int{90, 95, 100, 105, 110, 300} {
			if err := run(w, ctx, "slow", day(10, 29, 3).AddDate(0, 0, i), time.Duration(took)*time.Second, store.RunSucceeded); err != nil {
				return err
			}
		}
		for _, st := range []store.RunStatus{store.RunFailed, store.RunUnknown, store.RunRunning} {
			if err := run(w, ctx, "slow", day(11, 3, 9), 3*time.Hour, st); err != nil {
				return err
			}
		}
		// A succeeded run with no recorded start has no duration.
		if err := w.UpsertRun(ctx, store.Run{ID: "slow-nostart", OperationID: "slow", Namespace: "ns", JobName: "slow", CreatedAt: day(11, 2, 5), FinishedAt: day(11, 2, 9),
			Status: store.RunSucceeded, ObservedAt: day(11, 2, 5)}); err != nil {
			return err
		}
		// few: four recent runs, twice as long; young: the same as its baseline.
		for i := range 4 {
			if err := run(w, ctx, "few", day(10, 30, 3).AddDate(0, 0, i), 2*time.Minute, store.RunSucceeded); err != nil {
				return err
			}
		}
		for i := range 5 {
			if err := run(w, ctx, "young", day(10, 29, 3).AddDate(0, 0, i), time.Minute, store.RunSucceeded); err != nil {
				return err
			}
		}
		return nil
	}, tune)
	return srv
}

func TestDurationTrendInTheOperationsList(t *testing.T) {
	srv := trendServer(t, func(*Config) {})
	var ops Operations
	if code := get(t, srv, "/api/v1/operations?tz=UTC", &ops); code != 200 {
		t.Fatal(code)
	}
	by := map[string]DurationTrend{}
	for _, o := range ops.Operations {
		by[o.ID] = o.Duration
	}
	slow := by["slow"]
	if slow.Provenance != ProvDerived || slow.Recent.Runs != 6 || *slow.Recent.P50Seconds != 100 || *slow.Recent.P95Seconds != 300 ||
		slow.Baseline.Runs != 10 || *slow.Baseline.P50Seconds != 60 || *slow.Baseline.P95Seconds != 60 {
		t.Fatalf("slow %+v", slow)
	}
	if !slow.Enough || slow.Ratio == nil || *slow.Ratio != 1.67 || !slow.Longer || slow.MinRuns != 5 || slow.LongerAt != 1.3 {
		t.Fatalf("slow ratio %+v", slow)
	}
	if !slow.Recent.From.Equal(time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)) || !slow.Recent.To.Equal(base) ||
		!slow.Baseline.From.Equal(time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)) || !slow.Baseline.To.Equal(slow.Recent.From) {
		t.Fatalf("periods %+v %+v", slow.Recent, slow.Baseline)
	}
	// The hour not watched on 30 October: recent is not whole, and the runs it may hold are not counted as absent.
	if slow.Recent.Whole || slow.Recent.NotWatchedSeconds != 3600 || !slow.Baseline.Whole || slow.Baseline.NotWatchedSeconds != 0 {
		t.Fatalf("coverage %+v %+v", slow.Recent, slow.Baseline)
	}
	if slow.Points != nil || slow.Basis != "" {
		t.Fatal("the list carries no per-run points")
	}
	few := by["few"]
	if few.Enough || few.Ratio != nil || few.Longer || few.Recent.Runs != 4 || *few.Recent.P50Seconds != 120 || few.Baseline.Runs != 10 {
		t.Fatalf("too few runs %+v", few)
	}
	young := by["young"]
	if !young.Enough || *young.Ratio != 1 || young.Longer || !young.Baseline.Whole {
		t.Fatalf("young %+v", young)
	}

	// The reader's zone moves where the 7 days start.
	if code := get(t, srv, "/api/v1/operations?tz=Europe/Istanbul", &ops); code != 200 {
		t.Fatal(code)
	}
	if d := ops.Operations[0].Duration; !d.Recent.From.Equal(time.Date(2026, 10, 28, 21, 0, 0, 0, time.UTC)) {
		t.Fatalf("istanbul %+v", d.Recent)
	}
}

func TestDurationTrendInTheOperationDetail(t *testing.T) {
	srv := trendServer(t, func(*Config) {})
	var d OperationDetail
	if code := get(t, srv, "/api/v1/operations/slow?tz=UTC", &d); code != 200 {
		t.Fatal(code)
	}
	tr := d.Duration
	if tr.Recent.Runs != 6 || tr.Baseline.Runs != 10 || *tr.Ratio != 1.67 || !tr.Longer || tr.Basis == "" || len(tr.Points) != 16 {
		t.Fatalf("detail %+v", tr)
	}
	for i := 1; i < len(tr.Points); i++ {
		if tr.Points[i].StartedAt.Before(tr.Points[i-1].StartedAt) {
			t.Fatal("points are oldest first")
		}
	}
	if p := tr.Points[len(tr.Points)-1]; !p.Recent || p.Seconds != 300 || p.RunID == "" || tr.Points[0].Recent || tr.Points[0].Seconds != 60 {
		t.Fatalf("points %+v", tr.Points)
	}
}

func TestDurationTrendShortRetention(t *testing.T) {
	srv := trendServer(t, func(c *Config) { c.Retention.Days = 10 })
	var d OperationDetail
	if code := get(t, srv, "/api/v1/operations/slow?tz=UTC", &d); code != 200 {
		t.Fatal(code)
	}
	b := d.Duration.Baseline
	// Kept for 10 days: the baseline is 25 October 12:00 to 29 October, holds no run, and is not whole.
	if !b.ShortenedByRetention || b.Whole || !b.From.Equal(base.AddDate(0, 0, -10)) || b.Runs != 0 || b.P50Seconds != nil || d.Duration.Enough || d.Duration.Ratio != nil {
		t.Fatalf("short retention %+v", d.Duration)
	}
}

// Across a DST change the retention boundary is read in the reader's zone: with 30 days kept, the 23-day baseline
// (29 local days back from today's midnight) is never shortened, whatever the hour. In UTC, 30×24 h before 23:30 on
// 28 November (New York) was after the baseline's local midnight once the clocks had gone back.
func TestDurationTrendRetentionAcrossDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip(err)
	}
	s := &Server{cfg: Config{Retention: store.Retention{Days: 30}}}
	for h := 0; h < 24; h++ {
		now := time.Date(2026, 11, 28, h, 30, 0, 0, ny)
		_, b := s.trendPeriods(now, ny)
		if b.ShortenedByRetention || !b.From.Equal(time.Date(2026, 10, 30, 0, 0, 0, 0, ny)) {
			t.Fatalf("%v: %+v", now, b)
		}
	}
	// Kept for 20 days, it is shortened at every hour, from the later of the two boundaries.
	s.cfg.Retention.Days = 20
	now := time.Date(2026, 11, 28, 23, 30, 0, 0, ny)
	if _, b := s.trendPeriods(now, ny); !b.ShortenedByRetention || !b.From.Equal(now.UTC().AddDate(0, 0, -20)) {
		t.Fatalf("20 days: %+v", b)
	}
}

// Sub-second runs: the whole-second fields round to 0 s and 1 s, the millisecond fields carry what the ratio is of.
func TestDurationTrendSubSecond(t *testing.T) {
	ms := func(n int, v int) []time.Duration {
		out := make([]time.Duration, n)
		for i := range out {
			out[i] = time.Duration(v) * time.Millisecond
		}
		return out
	}
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	recent := DurationPeriod{From: now.AddDate(0, 0, -7), To: now}
	baseline := DurationPeriod{From: now.AddDate(0, 0, -30), To: now.AddDate(0, 0, -7)}
	cov := &coverage{byScope: map[string][]Span{}, first: map[string]time.Time{}}
	tr := durationTrend(store.Operation{Namespace: "x"}, ms(5, 900), ms(5, 400), recent, baseline, cov, nil)
	if *tr.Recent.P50Seconds != 1 || *tr.Baseline.P50Seconds != 0 || *tr.Recent.P50Ms != 900 || *tr.Baseline.P50Ms != 400 || *tr.Ratio != 2.25 || !tr.Longer {
		t.Fatalf("%+v", tr)
	}
}
