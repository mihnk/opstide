package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 4, 5, 0, 0, 0, time.UTC)

func open(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "opstide.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	s.now = func() time.Time { return t0 }
	return s, ctx
}

func update(t *testing.T, s *Store, fn func(w *Writer) error) {
	t.Helper()
	if err := s.Update(context.Background(), fn); err != nil {
		t.Fatal(err)
	}
}

func TestOpenMigratesOnceAndRefusesNewerSchemas(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "opstide.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := s.SchemaVersion(ctx); v != CurrentSchema {
		t.Fatalf("schema %d", v)
	}
	if err := s.Writable(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(ctx, path); err == nil {
		t.Fatal("opened a database from a newer release")
	}
}

func TestOperationRoundTripKeepsFirstSeen(t *testing.T) {
	s, ctx := open(t)
	four := 4 * time.Minute
	op := Operation{
		ID: "uid-1", Namespace: "payments", Name: "payment-reconciliation", DisplayName: "Payment reconciliation",
		Owner: "team-payments", ExpectedDuration: &four, MetadataProblems: []string{"runbook is not https"},
		Schedule: "*/30 * * * *", TimeZone: "Europe/Istanbul", ConcurrencyPolicy: "Forbid",
		SuccessfulHistory: 3, FailedHistory: 1, CreatedAt: t0.Add(-48 * time.Hour), FirstSeenAt: t0, LastSeenAt: t0,
	}
	update(t, s, func(w *Writer) error { return w.UpsertOperation(ctx, op) })
	op2 := op
	op2.FirstSeenAt, op2.LastSeenAt, op2.Suspend = t0.Add(time.Hour), t0.Add(time.Hour), true
	update(t, s, func(w *Writer) error { return w.UpsertOperation(ctx, op2) })

	got, ok, err := s.Operation(ctx, "uid-1")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if !got.FirstSeenAt.Equal(t0) || !got.LastSeenAt.Equal(t0.Add(time.Hour)) || !got.Suspend || *got.ExpectedDuration != four ||
		got.StartingDeadline != nil || got.MetadataProblems[0] != "runbook is not https" || !got.DeletedAt.IsZero() {
		t.Fatalf("got %+v", got)
	}

	update(t, s, func(w *Writer) error { return w.MarkOperationDeleted(ctx, "uid-1", t0.Add(2*time.Hour)) })
	if live, _ := s.Operations(ctx, false); len(live) != 0 {
		t.Fatalf("deleted operation listed: %+v", live)
	}
	if all, _ := s.Operations(ctx, true); len(all) != 1 || !all[0].DeletedAt.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("all: %+v", all)
	}
	if _, ok, _ := s.Operation(ctx, "nope"); ok {
		t.Fatal("unknown operation found")
	}
}

func TestRunsInWindowIncludeOnesStillActive(t *testing.T) {
	s, ctx := open(t)
	exit1 := int32(1)
	runs := []Run{
		{ID: "old-done", OperationID: "op", Namespace: "ns", JobName: "j-1", CreatedAt: t0.Add(-3 * time.Hour), FinishedAt: t0.Add(-170 * time.Minute), Status: RunSucceeded, ObservedAt: t0},
		{ID: "old-active", OperationID: "op", Namespace: "ns", JobName: "j-2", CreatedAt: t0.Add(-2 * time.Hour), Status: RunRunning, ObservedAt: t0},
		{ID: "in", OperationID: "op", Namespace: "ns", JobName: "j-3", CreatedAt: t0.Add(10 * time.Minute), ScheduledAt: t0.Add(10 * time.Minute), ScheduledBasis: "name",
			FinishedAt: t0.Add(25 * time.Minute), Status: RunFailed, StatusReason: "BackoffLimitExceeded", Attempts: 2,
			Pods: []Pod{{Name: "j-3-abc", UID: "p1", Phase: "Failed", ExitCode: &exit1, FinishedAt: t0.Add(25 * time.Minute)}}, ObservedAt: t0},
		{ID: "other", OperationID: "op2", Namespace: "ns", JobName: "k", CreatedAt: t0.Add(10 * time.Minute), Status: RunSucceeded, ObservedAt: t0},
	}
	update(t, s, func(w *Writer) error {
		for _, r := range runs {
			if err := w.UpsertRun(ctx, r); err != nil {
				return err
			}
		}
		return nil
	})
	got, err := s.RunsForOperation(ctx, "op", t0, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "in" || got[1].ID != "old-active" {
		t.Fatalf("got %+v", got)
	}
	if p := got[0].Pods; len(p) != 1 || *p[0].ExitCode != 1 || !p[0].FinishedAt.Equal(t0.Add(25*time.Minute)) || got[0].ScheduledBasis != "name" {
		t.Fatalf("pods: %+v", got[0])
	}
}

func TestEventsAreIdempotentAndPaginate(t *testing.T) {
	s, ctx := open(t)
	update(t, s, func(w *Writer) error {
		for i := range 5 {
			e := Event{ID: "job.succeeded/" + string(rune('a'+i)), Type: "job.succeeded", Namespace: "payments",
				ResourceName: "payment-reconciliation-" + string(rune('a'+i)), OccurredAt: t0.Add(time.Duration(i) * time.Minute),
				ObservedAt: t0.Add(time.Hour), Severity: "info", Confidence: Derived, Summary: "Payment reconciliation succeeded"}
			if ok, err := w.AddEvent(ctx, e); err != nil || !ok {
				return err
			}
		}
		// Same occurrence seen again after a relist: not added.
		ok, err := w.AddEvent(ctx, Event{ID: "job.succeeded/a", Type: "job.succeeded", ObservedAt: t0.Add(2 * time.Hour), Severity: "info", Confidence: Derived, Summary: "dup"})
		if ok {
			t.Error("duplicate added")
		}
		// No source time: sorts by when it was seen.
		_, err2 := w.AddEvent(ctx, Event{ID: "collection.gap/x", Type: "collection.gap", ObservedAt: t0.Add(30 * time.Minute), Severity: "warning", Confidence: Unknown, Summary: "gap 50% done"})
		if err == nil {
			err = err2
		}
		return err
	})

	page, err := s.Events(ctx, EventQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || page.Events[0].Type != "collection.gap" || page.Events[1].ID != "job.succeeded/e" || page.NextCursor == "" {
		t.Fatalf("page 1: %+v", page)
	}
	var ids []string
	for c := page.NextCursor; c != ""; {
		p, err := s.Events(ctx, EventQuery{Limit: 2, Cursor: c})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range p.Events {
			ids = append(ids, e.ID)
		}
		c = p.NextCursor
	}
	if len(ids) != 4 || ids[3] != "job.succeeded/a" {
		t.Fatalf("rest: %v", ids)
	}

	for _, tc := range []struct {
		q    EventQuery
		want int
	}{
		{EventQuery{Types: []string{"job."}}, 5},
		{EventQuery{Types: []string{"collection.gap"}}, 1},
		{EventQuery{Text: "100%"}, 0},
		{EventQuery{Text: "50%"}, 1},
		{EventQuery{Text: "reconciliation-c"}, 1},
		{EventQuery{From: t0.Add(2 * time.Minute), To: t0.Add(4 * time.Minute)}, 2},
		{EventQuery{Namespace: "data"}, 0},
	} {
		p, err := s.Events(ctx, tc.q)
		if err != nil || len(p.Events) != tc.want {
			t.Errorf("%+v: %d events, %v", tc.q, len(p.Events), err)
		}
	}
	if _, err := s.Events(ctx, EventQuery{Cursor: "x"}); err == nil {
		t.Error("bad cursor accepted")
	}
}

func TestCoverageIntervals(t *testing.T) {
	s, ctx := open(t)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	update(t, s, func(w *Writer) error { return w.OpenInterval(ctx, "jobs", Healthy, at(0)) })
	// Watch error, then the watch resumes from its resourceVersion: no gap.
	update(t, s, func(w *Writer) error { return w.CloseInterval(ctx, "jobs", at(10), "watch error") })
	update(t, s, func(w *Writer) error {
		ok, err := w.ReopenInterval(ctx, "jobs")
		if !ok {
			t.Error("not reopened")
		}
		return err
	})
	// Watch error that needs a relist: a real gap 20 → 24.
	update(t, s, func(w *Writer) error { return w.CloseInterval(ctx, "jobs", at(20), "watch expired") })
	update(t, s, func(w *Writer) error { return w.OpenInterval(ctx, "jobs", Healthy, at(24)) })
	update(t, s, func(w *Writer) error {
		ok, err := w.ReopenInterval(ctx, "jobs")
		if ok {
			t.Error("reopened while one is open")
		}
		return err
	})

	ivs, err := s.Coverage(ctx, at(0), at(60))
	if err != nil {
		t.Fatal(err)
	}
	if len(ivs) != 2 || !ivs[0].EndedAt.Equal(at(20)) || ivs[0].EndReason != "watch expired" || !ivs[1].StartedAt.Equal(at(24)) || !ivs[1].EndedAt.IsZero() {
		t.Fatalf("intervals: %+v", ivs)
	}
	if ivs, _ := s.Coverage(ctx, at(21), at(23)); len(ivs) != 0 {
		t.Fatalf("gap overlaps: %+v", ivs)
	}

	// The process dies after its heartbeat at 40; the next one closes the
	// open interval there.
	update(t, s, func(w *Writer) error { return w.Heartbeat(ctx, at(40)) })
	update(t, s, func(w *Writer) error {
		n, err := w.CloseAfterRestart(ctx)
		if n != 1 {
			t.Errorf("closed %d", n)
		}
		return err
	})
	ivs, _ = s.Coverage(ctx, at(0), at(60))
	if !ivs[1].EndedAt.Equal(at(40)) || ivs[1].EndReason != "collector stopped" {
		t.Fatalf("after restart: %+v", ivs[1])
	}

	// A process that dies before its first heartbeat claims nothing past
	// the start of its interval.
	update(t, s, func(w *Writer) error { return w.OpenInterval(ctx, "pods", Healthy, at(50)) })
	update(t, s, func(w *Writer) error { _, err := w.CloseAfterRestart(ctx); return err })
	ivs, _ = s.Coverage(ctx, at(0), at(60))
	if last := ivs[len(ivs)-1]; last.Scope != "pods" || !last.EndedAt.Equal(at(50)) {
		t.Fatalf("no heartbeat since: %+v", last)
	}
	if last, _ := s.LastWrite(ctx); !last.Equal(at(40)) {
		t.Fatalf("last write %v", last)
	}
}

func TestPruneKeepsLiveOperationsAndCapsEvents(t *testing.T) {
	s, ctx := open(t)
	old := t0.AddDate(0, 0, -40)
	update(t, s, func(w *Writer) error {
		for i := range 10 {
			at := t0.Add(-time.Duration(i) * time.Hour)
			if i >= 8 {
				at = old
			}
			if _, err := w.AddEvent(ctx, Event{ID: "e" + string(rune('0'+i)), Type: "job.created", OccurredAt: at, ObservedAt: at, Severity: "info", Confidence: Observed, Summary: "x"}); err != nil {
				return err
			}
		}
		for _, op := range []Operation{
			{ID: "live", Namespace: "a", Name: "a", Schedule: "@daily", CreatedAt: old, FirstSeenAt: old, LastSeenAt: old},
			{ID: "gone", Namespace: "a", Name: "b", Schedule: "@daily", CreatedAt: old, FirstSeenAt: old, LastSeenAt: old, DeletedAt: old},
		} {
			if err := w.UpsertOperation(ctx, op); err != nil {
				return err
			}
		}
		if err := w.UpsertRun(ctx, Run{ID: "r-old", OperationID: "live", Namespace: "a", JobName: "a-1", CreatedAt: old, FinishedAt: old, Status: RunSucceeded, ObservedAt: old}); err != nil {
			return err
		}
		return w.UpsertRun(ctx, Run{ID: "r-active", OperationID: "live", Namespace: "a", JobName: "a-2", CreatedAt: old, Status: RunRunning, ObservedAt: old})
	})
	r, err := s.Prune(ctx, t0.AddDate(0, 0, -30), 5)
	if err != nil {
		t.Fatal(err)
	}
	if r.Events != 5 || r.Runs != 1 || r.Operations != 1 {
		t.Fatalf("pruned %+v", r)
	}
	if _, ok, _ := s.Operation(ctx, "live"); !ok {
		t.Fatal("live operation pruned")
	}
	if _, ok, _ := s.Run(ctx, "r-active"); !ok {
		t.Fatal("active run pruned")
	}
	st, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Events != 5 || !st.OldestEvent.Equal(t0.Add(-4*time.Hour)) || !st.LastPrune.Equal(t0) || st.LastPruneRemoved != 7 || st.Bytes == 0 || st.SchemaVersion != CurrentSchema {
		t.Fatalf("stats %+v", st)
	}
	if err := s.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRunHistogramAndFirstWatched(t *testing.T) {
	s, ctx := open(t)
	if first, err := s.FirstWatched(ctx); err != nil || !first.IsZero() {
		t.Fatalf("nothing watched yet: %v %v", first, err)
	}
	hour := func(h, m int) time.Time { return t0.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
	update(t, s, func(w *Writer) error {
		for i, c := range []struct {
			at     time.Time
			status RunStatus
		}{
			{hour(0, 5), RunSucceeded}, {hour(0, 40), RunFailed}, {hour(0, 50), RunSucceeded},
			{hour(2, 10), RunFailed}, {hour(5, 0), RunSucceeded},
		} {
			id := string(rune('a' + i))
			if err := w.UpsertRun(ctx, Run{ID: id, OperationID: "op", Namespace: "ns", JobName: id, CreatedAt: c.at, Status: c.status, ObservedAt: c.at}); err != nil {
				return err
			}
		}
		if err := w.OpenInterval(ctx, "jobs/payments", Healthy, hour(-3, 0)); err != nil {
			return err
		}
		return w.OpenInterval(ctx, "pods", Healthy, hour(-9, 0)) // not a Jobs scope
	})
	got, err := s.RunHistogram(ctx, t0, hour(5, 0), time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Runs != 3 || got[0].Failed != 1 || !got[0].Start.Equal(t0) || got[1].Runs != 1 || got[1].Failed != 1 || !got[1].Start.Equal(hour(2, 0)) {
		t.Fatalf("histogram %+v", got)
	}
	if first, _ := s.FirstWatched(ctx); !first.Equal(hour(-3, 0)) {
		t.Fatalf("first watched %v", first)
	}
	by, err := s.FirstWatchedByScope(ctx)
	if err != nil || len(by) != 1 || !by["jobs/payments"].Equal(hour(-3, 0)) {
		t.Fatalf("by scope %v %v", by, err)
	}
	// Hours as a +05:30 reader counts them start on the half hour.
	ist, err := s.RunHistogram(ctx, t0, hour(6, 0), time.Hour, 5*time.Hour+30*time.Minute)
	if err != nil || len(ist) < 2 || !ist[0].Start.Equal(t0.Add(-30*time.Minute)) {
		t.Fatalf("histogram in a +05:30 zone: %+v %v", ist, err)
	}
}
