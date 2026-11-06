package store

import (
	"context"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"sort"
	"strings"
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
	// Lists leave the pods out (they are most of a row); a run's own read has them.
	if len(got[0].Pods) != 0 || got[0].ScheduledBasis != "name" {
		t.Fatalf("a list carries pods: %+v", got[0])
	}
	full, ok, err := s.Run(ctx, "in")
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	if p := full.Pods; len(p) != 1 || *p[0].ExitCode != 1 || !p[0].FinishedAt.Equal(t0.Add(25*time.Minute)) {
		t.Fatalf("pods: %+v", full)
	}
}

func TestSuspensionEventsAreGroupedOldestFirst(t *testing.T) {
	s, ctx := open(t)
	update(t, s, func(w *Writer) error {
		for i, e := range []struct{ op, typ string }{
			{"a", "cronjob.suspended"}, {"b", "cronjob.suspended"}, {"a", "cronjob.resumed"}, {"a", "cronjob.suspended"},
		} {
			at := t0.Add(time.Duration(i) * time.Minute)
			if _, err := w.AddEvent(ctx, Event{ID: fmt.Sprintf("%s/%d", e.typ, i), Type: e.typ, OperationID: e.op, OccurredAt: at, ObservedAt: at,
				Severity: "info", Confidence: Observed, Summary: "x"}); err != nil {
				return err
			}
		}
		// Unrelated entries and a flood past one page must not matter.
		for i := range 700 {
			if _, err := w.AddEvent(ctx, Event{ID: fmt.Sprintf("noise/%d", i), Type: "job.created", OperationID: "a", ObservedAt: t0, Severity: "info", Confidence: Observed, Summary: "x"}); err != nil {
				return err
			}
		}
		return nil
	})
	all, err := s.SuspensionEvents(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, e := range all["a"] {
		types = append(types, e.Type)
	}
	if strings.Join(types, ",") != "cronjob.suspended,cronjob.resumed,cronjob.suspended" || len(all["b"]) != 1 || len(all) != 2 {
		t.Fatalf("grouped: %v / %d groups", types, len(all))
	}
	one, _ := s.SuspensionEvents(ctx, "b")
	if len(one) != 1 || len(one["b"]) != 1 {
		t.Fatalf("one operation: %v", one)
	}
}

func TestMigrationTwoAddsIndexesToAnExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Take it back to version 1, as a database written before the indexes would be.
	if _, err := s.db.ExecContext(ctx, `DROP INDEX events_type_at; DROP INDEX events_severity_at; DROP INDEX runs_created; DROP INDEX runs_finished; DROP INDEX runs_active; DROP INDEX runs_lost; DROP INDEX runs_updated; DROP INDEX events_node; DROP TABLE workload_state; DROP TABLE workload_revisions; DROP TABLE nodes; DROP TABLE pod_owners; DROP INDEX events_workload; DROP INDEX events_reason; ALTER TABLE events DROP COLUMN reason; ALTER TABLE events DROP COLUMN workload_id; DROP TABLE evidence; DROP TABLE hpas; DROP INDEX events_release; ALTER TABLE events DROP COLUMN release_id; DROP TABLE workloads; DROP TABLE releases; DROP TABLE release_pods; PRAGMA user_version = 1`); err != nil {
		t.Fatalf("rewinding to version 1: %v", err)
	}
	s.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name IN ('events_type_at','events_severity_at','runs_created','runs_finished','runs_active','runs_lost','runs_updated')`).Scan(&n); err != nil || n != 7 {
		t.Fatalf("indexes after migrating: %d %v", n, err)
	}
	// INDEXED BY must find its indexes on a migrated database too, not only a fresh one.
	if _, err := s.RunsBetween(ctx, t0, t0.Add(time.Hour)); err != nil {
		t.Fatalf("RunsBetween on a migrated database: %v", err)
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

// The big record is read by window many times a minute. RunsBetween names
// its indexes (INDEXED BY), so SQLite refuses to run it if one cannot serve
// its lookup; this makes sure it still runs, and that the other hot lookups
// have their index.
func TestWindowQueriesUseIndexes(t *testing.T) {
	s, ctx := open(t)
	if _, err := s.RunsBetween(ctx, t0, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	explain := func(q string, args ...any) string {
		rows, err := s.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+q, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		return strings.Join(plan, "\n")
	}
	// The suspension lookup exactly as Events builds it.
	if p := explain(`SELECT seq FROM events WHERE (type = ? OR type = ?) ORDER BY at DESC, seq DESC LIMIT 501`, "cronjob.suspended", "cronjob.resumed"); !strings.Contains(p, "events_type_at") {
		t.Errorf("suspension lookup does not use its index:\n%s", p)
	}
	if p := explain(`SELECT coalesce(max(updated_at), 0) FROM runs`); !strings.Contains(p, "runs_updated") {
		t.Errorf("Version scans every run:\n%s", p)
	}
}

// What RunsBetween returns must be what the one-OR query used to return.
func TestRunsBetweenKeepsRunsThatStillMatter(t *testing.T) {
	s, ctx := open(t)
	from, to := t0, t0.Add(time.Hour)
	before := from.Add(-3 * time.Hour)
	mk := func(id string, created time.Time, st RunStatus, fin, upd time.Time) Run {
		return Run{ID: id, OperationID: "op", Namespace: "ns", JobName: id, CreatedAt: created, FinishedAt: fin, Status: st, ObservedAt: created, UpdatedAt: upd}
	}
	// UpsertRun stamps updated_at from the store's clock, so each run is
	// written as of the time its "last news" should be.
	write := func(asOf time.Time, runs ...Run) {
		s.now = func() time.Time { return asOf }
		update(t, s, func(w *Writer) error {
			for _, r := range runs {
				if err := w.UpsertRun(ctx, r); err != nil {
					return err
				}
			}
			return nil
		})
	}
	write(before, mk("old-lost-long-ago", before, RunUnknown, time.Time{}, before))
	write(from.Add(20*time.Minute),
		mk("old-lost-inside", before, RunUnknown, time.Time{}, time.Time{}),
		// lost inside the window AND finished inside it: once, not twice
		mk("old-lost-and-finished", before, RunUnknown, from.Add(15*time.Minute), time.Time{}))
	write(from, mk("in-window", from.Add(5*time.Minute), RunSucceeded, from.Add(6*time.Minute), time.Time{}),
		mk("after-window", to.Add(time.Minute), RunSucceeded, to.Add(2*time.Minute), time.Time{}),
		mk("old-done", before, RunSucceeded, before.Add(time.Minute), time.Time{}),
		mk("old-running", before, RunRunning, time.Time{}, time.Time{}),
		mk("old-pending", before, RunPending, time.Time{}, time.Time{}),
		mk("old-finished-inside", before, RunFailed, from.Add(10*time.Minute), time.Time{}))
	got, err := s.RunsBetween(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, r := range got {
		seen[r.ID]++
	}
	want := map[string]int{"in-window": 1, "old-running": 1, "old-pending": 1, "old-finished-inside": 1, "old-lost-inside": 1, "old-lost-and-finished": 1}
	if len(seen) != len(want) {
		t.Fatalf("got %v, want %v", seen, want)
	}
	for id, n := range want {
		if seen[id] != n {
			t.Errorf("%s: %d times, want %d (all: %v)", id, seen[id], n, seen)
		}
	}
	for i := 1; i < len(got); i++ {
		if got[i].CreatedAt.After(got[i-1].CreatedAt) {
			t.Fatal("not newest first")
		}
	}
}

// RunsBetween was one query with an OR; it is four lookups now. They must
// agree on everything, boundaries included.
func TestRunsBetweenAgreesWithTheOneQueryItReplaced(t *testing.T) {
	s, ctx := open(t)
	rng := rand.New(rand.NewPCG(7, 9))
	base := t0
	minute := func(n int) time.Time { return base.Add(time.Duration(n) * time.Minute) }
	statuses := []RunStatus{RunPending, RunRunning, RunSucceeded, RunFailed, RunUnknown}
	// Few distinct minutes, so created/finished/updated often equal a window edge.
	var runs []Run
	for i := range 250 {
		st := statuses[rng.IntN(len(statuses))]
		r := Run{ID: fmt.Sprintf("r%03d", i), OperationID: "op", Namespace: "ns", JobName: "j", CreatedAt: minute(rng.IntN(40)), Status: st, ObservedAt: base}
		if rng.IntN(3) > 0 {
			r.FinishedAt = r.CreatedAt.Add(time.Duration(rng.IntN(20)-3) * time.Minute) // sometimes before it was created
		}
		runs = append(runs, r)
	}
	for i := range runs {
		s.now = func() time.Time { return minute(rng.IntN(40)) } // updated_at
		update(t, s, func(w *Writer) error { return w.UpsertRun(ctx, runs[i]) })
	}
	old := func(from, to time.Time) []string {
		rows, err := s.db.QueryContext(ctx, `SELECT id FROM runs WHERE created_at < ? AND (created_at >= ? OR `+overlapsFrom+`) ORDER BY created_at DESC, id`,
			to.UnixMilli(), from.UnixMilli(), from.UnixMilli(), from.UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			rows.Scan(&id)
			ids = append(ids, id)
		}
		return ids
	}
	for _, analyze := range []bool{false, true} {
		if analyze {
			if err := s.Analyze(ctx); err != nil {
				t.Fatal(err)
			}
		}
		for range 100 {
			from, to := minute(rng.IntN(45)), minute(rng.IntN(60)+1)
			if !from.Before(to) {
				if got, err := s.RunsBetween(ctx, from, to); err != nil || len(got) != 0 {
					t.Fatalf("an empty or backwards window returned %d runs (%v)", len(got), err)
				}
				continue
			}
			got, err := s.RunsBetween(ctx, from, to)
			if err != nil {
				t.Fatal(err)
			}
			var gotIDs []string
			for i, r := range got {
				gotIDs = append(gotIDs, r.ID)
				if i > 0 && r.CreatedAt.After(got[i-1].CreatedAt) {
					t.Fatalf("not newest first at %d", i)
				}
			}
			want := old(from, to)
			sort.Strings(gotIDs)
			sort.Strings(want)
			if strings.Join(gotIDs, ",") != strings.Join(want, ",") {
				t.Fatalf("window %d-%d (analyzed: %v): %d runs, the old query gave %d\nnew %v\nold %v",
					from.Sub(base)/time.Minute, to.Sub(base)/time.Minute, analyze, len(gotIDs), len(want), gotIDs, want)
			}
		}
	}
}

// A rollback reuses a ReplicaSet whose Pods never stopped: both the old and the new release
// of that ReplicaSet own those Pods, each for its own time.
func TestReleasePodsAreThoseOfTheReplicaSetThatLivedDuringTheRelease(t *testing.T) {
	s, ctx := open(t)
	mk := func(id, rs string, rev int64, observed time.Time, superseded time.Time) Release {
		return Release{ID: id, WorkloadID: "d", Namespace: "ns", Workload: "api", Revision: fmt.Sprint(rev), RevisionNum: rev, ReplicaSetUID: rs, ReplicaSetName: rs,
			Fingerprint: "f", Origin: OriginObserved, ObservedAt: observed, SupersededAt: superseded, State: ReleaseCompleted, StateAt: observed, Health: HealthObserving,
			HealthReasons: "[]", WindowSeconds: 600, Episodes: "[]", Trouble: "[]", Snapshot: "{}", ImageIDs: "{}"}
	}
	r42 := mk("r42", "rsA", 42, t0, t0.Add(2*time.Hour))
	r43 := mk("r43", "rsB", 43, t0.Add(2*time.Hour), t0.Add(3*time.Hour))
	r44 := mk("r44", "rsA", 44, t0.Add(3*time.Hour), time.Time{})
	update(t, s, func(w *Writer) error {
		for _, r := range []Release{r42, r43, r44} {
			if err := w.UpsertRelease(ctx, r); err != nil {
				return err
			}
		}
		pod := func(uid, rs string, created, ended time.Time) PodRecord {
			return PodRecord{UID: uid, ReleaseID: "x", Namespace: "ns", Name: uid, ReplicaSetUID: rs, WorkloadID: "d", CreatedAt: created, TerminatedAt: ended, State: "{}"}
		}
		for _, p := range []PodRecord{
			pod("a1", "rsA", t0.Add(time.Minute), time.Time{}),            // kept running through the rollback
			pod("a0", "rsA", t0.Add(time.Minute), t0.Add(30*time.Minute)), // ended during 42
			pod("b1", "rsB", t0.Add(2*time.Hour+time.Minute), time.Time{}),
		} {
			if err := w.UpsertPod(ctx, p); err != nil {
				return err
			}
		}
		return nil
	})
	names := func(r Release) string {
		ps, err := s.ReleasePods(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, p := range ps {
			out = append(out, p.UID)
		}
		return strings.Join(out, ",")
	}
	if got := names(r42); got != "a0,a1" {
		t.Errorf("42: %q", got)
	}
	if got := names(r43); got != "b1" {
		t.Errorf("43: %q", got)
	}
	if got := names(r44); got != "a1" {
		t.Errorf("44: %q (a0 ended long before it)", got)
	}
}

// The release timeline is read many times a minute: its query must be served by the partial index.
func TestReleaseEntriesQueryUsesItsIndex(t *testing.T) {
	s, ctx := open(t)
	rows, err := s.db.QueryContext(ctx, `EXPLAIN QUERY PLAN SELECT seq FROM events WHERE release_id = ? AND release_id <> '' ORDER BY at DESC, seq DESC LIMIT 101`, "r1")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var d string
		if err := rows.Scan(&id, &parent, &unused, &d); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, d)
	}
	if !strings.Contains(strings.Join(plan, "\n"), "events_release") {
		t.Fatalf("the release timeline scans the events table:\n%s", strings.Join(plan, "\n"))
	}
	// And the real query (through Events) states the term the partial index needs.
	if _, err := s.Events(ctx, EventQuery{ReleaseID: "r1", Limit: 10}); err != nil {
		t.Fatal(err)
	}
}

func releaseFixture(id, rs string, rev int64, observed time.Time) Release {
	return Release{ID: id, WorkloadID: "d", Namespace: "ns", Workload: "api", Revision: fmt.Sprint(rev), RevisionNum: rev, ReplicaSetUID: rs, ReplicaSetName: rs,
		Fingerprint: "f", Origin: OriginObserved, ObservedAt: observed, State: ReleaseCompleted, StateAt: observed, Health: HealthObserving,
		HealthReasons: "[]", WindowSeconds: 600, Episodes: "[]", Trouble: "[]", Snapshot: "{}", ImageIDs: "{}"}
}

// Pods that ended before a rollback began belong to the release before it. A release first seen
// as its ReplicaSet came up still gets a minute of slack for the Pods created just before.
func TestRollbackReleaseHasNoSlackForPodsThatAlreadyEnded(t *testing.T) {
	s, ctx := open(t)
	first := releaseFixture("r1", "rsA", 1, t0)
	other := releaseFixture("r2", "rsB", 2, t0.Add(time.Hour))
	back := releaseFixture("r3", "rsA", 3, t0.Add(2*time.Hour))
	fresh := releaseFixture("r4", "rsC", 4, t0.Add(3*time.Hour))
	update(t, s, func(w *Writer) error {
		for _, r := range []Release{first, other, back, fresh} {
			if err := w.UpsertRelease(ctx, r); err != nil {
				return err
			}
		}
		pod := func(uid, rs string, created, ended time.Time) PodRecord {
			return PodRecord{UID: uid, ReleaseID: "x", Namespace: "ns", Name: uid, ReplicaSetUID: rs, WorkloadID: "d", CreatedAt: created, TerminatedAt: ended, State: "{}"}
		}
		for _, p := range []PodRecord{
			pod("ended30s", "rsA", t0.Add(time.Minute), back.ObservedAt.Add(-30*time.Second)), // ended just before the rollback
			pod("running", "rsA", t0.Add(time.Minute), time.Time{}),
			pod("early", "rsC", fresh.ObservedAt.Add(-time.Hour), fresh.ObservedAt.Add(-30*time.Second)), // created and ended just before a fresh release was noticed
		} {
			if err := w.UpsertPod(ctx, p); err != nil {
				return err
			}
		}
		return nil
	})
	uids := func(r Release) string {
		ps, err := s.ReleasePods(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, p := range ps {
			out = append(out, p.UID)
		}
		return strings.Join(out, ",")
	}
	if got := uids(back); got != "running" {
		t.Errorf("rollback: %q, a Pod that ended before it began is not its own", got)
	}
	if got := uids(fresh); got != "early" {
		t.Errorf("fresh release: %q, a Pod gone 30s before it was noticed keeps the minute of slack", got)
	}
	if rb, _ := s.IsRollback(ctx, back); !rb {
		t.Error("r3 reuses r1's ReplicaSet")
	}
	if rb, _ := s.IsRollback(ctx, first); rb {
		t.Error("r1 is the first use of its ReplicaSet")
	}
}

// A degraded release stays under sweep while its window lasts; once the window is over, or it
// was replaced, it is settled and not swept.
func TestOpenReleasesIncludesADegradedReleaseWhileItsWindowIsOpen(t *testing.T) {
	s, ctx := open(t)
	mk := func(id, health string, until time.Time, superseded time.Time) Release {
		r := releaseFixture(id, "rs-"+id, 1, t0)
		r.WorkloadID, r.Health, r.ObserveUntil, r.SupersededAt = "d-"+id, health, until, superseded
		return r
	}
	update(t, s, func(w *Writer) error {
		for _, r := range []Release{
			mk("observing", HealthObserving, t0.Add(time.Minute), time.Time{}),
			mk("degradedOpen", HealthDegraded, t0.Add(time.Minute), time.Time{}),
			mk("degradedNoWindowYet", HealthDegraded, time.Time{}, time.Time{}),
			mk("degradedClosed", HealthDegraded, t0.Add(-time.Minute), time.Time{}),
			mk("degradedReplaced", HealthDegraded, t0.Add(time.Minute), t0.Add(-time.Second)),
			mk("healthy", HealthHealthy, t0.Add(-time.Minute), time.Time{}),
		} {
			if err := w.UpsertRelease(ctx, r); err != nil {
				return err
			}
		}
		return nil
	})
	open, err := s.OpenReleases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range open {
		ids = append(ids, r.ID)
	}
	sort.Strings(ids)
	if got := strings.Join(ids, ","); got != "degradedNoWindowYet,degradedOpen,observing" {
		t.Fatalf("%s", got)
	}
}

// Retention removes the Pods of releases that are gone, but a Pod that is still running belongs
// to the ReplicaSet of a release that is kept, whichever release it was first recorded under.
func TestRetentionKeepsLivePodsOfAReusedReplicaSet(t *testing.T) {
	s, ctx := open(t)
	old := releaseFixture("old", "rsA", 1, t0.AddDate(0, 0, -60))
	other := releaseFixture("other", "rsB", 2, t0.AddDate(0, 0, -50))
	back := releaseFixture("back", "rsA", 3, t0.Add(-time.Hour)) // the newest: always kept
	update(t, s, func(w *Writer) error {
		for _, r := range []Release{old, other, back} {
			if err := w.UpsertRelease(ctx, r); err != nil {
				return err
			}
		}
		for _, p := range []PodRecord{
			{UID: "live", ReleaseID: "old", Namespace: "ns", Name: "live", ReplicaSetUID: "rsA", WorkloadID: "d", CreatedAt: t0.AddDate(0, 0, -59), State: "{}"},
			{UID: "orphan", ReleaseID: "other", Namespace: "ns", Name: "orphan", ReplicaSetUID: "rsB", WorkloadID: "d", CreatedAt: t0.AddDate(0, 0, -49), State: "{}"},
		} {
			if err := w.UpsertPod(ctx, p); err != nil {
				return err
			}
		}
		return nil
	})
	if _, err := s.Prune(ctx, t0.AddDate(0, 0, -30), 1000); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.PodByUID(ctx, "live"); !ok {
		t.Error("a running Pod of a kept release was deleted because it was recorded under an older release")
	}
	if _, ok, _ := s.PodByUID(ctx, "orphan"); ok {
		t.Error("a Pod of a release that is gone was kept")
	}
}

// Migration 4 adds evidence and autoscaler tables and the columns that tie
// entries to them, without touching what migration 3 stored: releases already
// judged keep assessment version 1.
func TestMigrationFourKeepsReleasesOnTheirOwnRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v3.db")
	ctx := context.Background()
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DROP INDEX events_node; DROP TABLE workload_state; DROP TABLE workload_revisions; DROP TABLE nodes; DROP TABLE pod_owners; DROP INDEX workloads_kind; ALTER TABLE workloads DROP COLUMN kind; ALTER TABLE workloads DROP COLUMN origin; DROP INDEX release_pods_workload; DROP INDEX release_pods_node; ALTER TABLE release_pods DROP COLUMN kind; ALTER TABLE release_pods DROP COLUMN revision; ALTER TABLE release_pods DROP COLUMN node; ALTER TABLE release_pods DROP COLUMN ordinal;
DROP INDEX events_workload; DROP INDEX events_reason; ALTER TABLE events DROP COLUMN reason; ALTER TABLE events DROP COLUMN workload_id;
DROP TABLE evidence; DROP TABLE hpas; ALTER TABLE workloads DROP COLUMN spec_replicas; ALTER TABLE releases DROP COLUMN assessment_version; PRAGMA user_version = 3`); err != nil {
		t.Fatalf("rewinding to version 3: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO releases (id, workload_id, namespace, workload, revision, revision_num, replicaset_uid, replicaset_name, fingerprint, origin,
observed_at, state, state_at, health, window_seconds, snapshot, updated_at) VALUES ('old','d','ns','api','1',1,'rs','rs','f','observed',1,'completed',1,'healthy',600,'{}',1)`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, ok, err := s.Release(ctx, "old")
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if r.AssessmentVersion != 1 {
		t.Fatalf("a release judged before scaling evidence existed must keep the rules it was judged by: version %d", r.AssessmentVersion)
	}
	if _, err := s.Evidence(ctx, EvidenceQuery{}); err != nil {
		t.Fatal(err)
	}
}

// A release's Events are those that overlap its time: one that began while the release was current and
// kept repeating after a newer revision took over is still its own; one that began later is not.
func TestReleaseEvidenceIncludesEventsThatKeptRepeatingAfterSupersession(t *testing.T) {
	s, ctx := open(t)
	end := t0.Add(time.Hour)
	mk := func(id, uid string, first, last time.Time) Evidence {
		return Evidence{ID: id, Source: SourceEventsV1, SourceUID: uid, Namespace: "ns", Name: id, Reason: "BackOff", EventType: "Warning",
			Regarding: Ref{Kind: "Pod", Name: "p", UID: "p-" + id}, FirstAt: first, LastAt: last, ObservedAt: first, Origin: OriginObservedEvidence, ReplicaSetUID: "rsA", WorkloadID: "d"}
	}
	update(t, s, func(w *Writer) error {
		for _, e := range []Evidence{
			mk("repeats", "u1", t0.Add(10*time.Minute), end.Add(30*time.Minute)), // began under the release, still repeating
			mk("inside", "u2", t0.Add(5*time.Minute), t0.Add(20*time.Minute)),
			mk("later", "u3", end.Add(5*time.Minute), end.Add(10*time.Minute)), // began under the next revision
			mk("before", "u4", t0.Add(-3*time.Hour), t0.Add(-2*time.Hour)),
		} {
			if err := w.SaveEvidence(ctx, e); err != nil {
				return err
			}
		}
		return nil
	})
	pg, err := s.ReleaseEvidence(ctx, ReleaseEvidenceQuery{ReplicaSetUID: "rsA", WorkloadID: "d", From: t0, To: end, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range pg.Records {
		ids = append(ids, e.ID)
	}
	sort.Strings(ids)
	if strings.Join(ids, ",") != "inside,repeats" {
		t.Fatalf("%v", ids)
	}
}

// A Deployment deleted and created again under the same name has a new UID; the autoscaler that names it
// follows, with the Events recorded about it.
func TestAutoscalerFollowsARecreatedDeployment(t *testing.T) {
	s, ctx := open(t)
	update(t, s, func(w *Writer) error {
		if err := w.SaveHPA(ctx, HPA{UID: "h", Namespace: "ns", Name: "api", TargetKind: "Deployment", TargetName: "api", TargetUID: "old", WorkloadID: "old", Conditions: "[]", Metrics: "[]", FirstSeenAt: t0}); err != nil {
			return err
		}
		if err := w.SaveEvidence(ctx, Evidence{ID: "e", Source: SourceEventsV1, SourceUID: "u", Namespace: "ns", Name: "e", Regarding: Ref{Kind: "HorizontalPodAutoscaler", Name: "api", UID: "h"},
			ObservedAt: t0, Origin: OriginObservedEvidence, HPAUID: "h", WorkloadID: "old"}); err != nil {
			return err
		}
		return w.RelinkHPAs(ctx, "ns", "api", "new")
	})
	hs, err := s.HPAsOfWorkload(ctx, "new")
	if err != nil || len(hs) != 1 || hs[0].TargetUID != "new" {
		t.Fatalf("%v %+v", err, hs)
	}
	if old, _ := s.HPAsOfWorkload(ctx, "old"); len(old) != 0 {
		t.Fatal("still tied to the old Deployment")
	}
	pg, _ := s.Evidence(ctx, EvidenceQuery{WorkloadID: "new"})
	if len(pg.Records) != 1 {
		t.Fatal("the autoscaler's Events did not follow")
	}
}

// Migration 5 gives every existing Deployment the kind it already had, and names the
// workload on the release and rollout entries that did not yet.
func TestMigrationFiveKeepsDeploymentsAndNamesTheirEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v4.db")
	ctx := context.Background()
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DROP INDEX events_node; DROP TABLE workload_state; DROP TABLE workload_revisions; DROP TABLE nodes; DROP TABLE pod_owners;
DROP INDEX workloads_kind; ALTER TABLE workloads DROP COLUMN kind; ALTER TABLE workloads DROP COLUMN origin;
DROP INDEX release_pods_workload; DROP INDEX release_pods_node; ALTER TABLE release_pods DROP COLUMN kind; ALTER TABLE release_pods DROP COLUMN revision; ALTER TABLE release_pods DROP COLUMN node; ALTER TABLE release_pods DROP COLUMN ordinal;
DROP INDEX evidence_record_key; ALTER TABLE evidence DROP COLUMN record_key; DROP INDEX evidence_node; ALTER TABLE evidence DROP COLUMN node_id; PRAGMA user_version = 4`); err != nil {
		t.Fatalf("rewinding to version 4: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO workloads (id, namespace, name, first_seen_at, last_seen_at) VALUES ('d1','ns','api',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO events (id, type, namespace, kind, resource_uid, resource_name, observed_at, recorded_at, at, severity, confidence, summary)
VALUES ('e1','release.observed','ns','Deployment','d1','api',1,1,1,'info','observed','x'), ('e2','job.created','ns','Job','j1','job',1,1,1,'info','observed','y')`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wl, ok, err := s.WorkloadByID(ctx, "d1")
	if err != nil || !ok || wl.Kind != KindDeployment {
		t.Fatalf("%+v %v %v", wl, ok, err)
	}
	pg, err := s.Events(ctx, EventQuery{WorkloadID: "d1"})
	if err != nil || len(pg.Events) != 1 || pg.Events[0].ID != "e1" {
		t.Fatalf("the release entry must name its workload, and the Job entry must not: %v %+v", err, pg.Events)
	}
}

// A rollback gives an existing ControllerRevision a new, higher number: the stored number follows it, so the
// revision sorts and compares as the newest.
func TestRevisionRenumberingByARollbackIsKept(t *testing.T) {
	s, ctx := open(t)
	mk := func(id string, n int64) WorkloadRevision {
		return WorkloadRevision{ID: id, WorkloadID: "w", Kind: KindStatefulSet, Name: id, Number: n, ControllerRevisionUID: "u-" + id, ObservedAt: t0, Origin: OriginObserved, Fingerprint: id, Snapshot: "{}"}
	}
	update(t, s, func(w *Writer) error {
		for _, r := range []WorkloadRevision{mk("r1", 1), mk("r2", 2)} {
			if _, err := w.AddRevision(ctx, r); err != nil {
				return err
			}
		}
		return nil
	})
	update(t, s, func(w *Writer) error { _, err := w.AddRevision(ctx, mk("r1", 3)); return err }) // rolled back to r1
	revs, err := s.Revisions(ctx, "w")
	if err != nil || len(revs) != 2 || revs[1].Name != "r1" || revs[1].Number != 3 {
		t.Fatalf("%v %+v", err, revs)
	}
}

// Events about an earlier Node of the same name are not linked to a later one, and an Event is linked only when it states this Node's UID or its name as the UID.
func TestNodeEvidenceRelinkIsBoundedByUIDAndTime(t *testing.T) {
	s, ctx := open(t)
	ev := func(id, uid string, at time.Time) Evidence {
		return Evidence{ID: id, Source: SourceEventsV1, SourceUID: "s" + id, Namespace: "default", Name: id, Regarding: Ref{Kind: "Node", Name: "node-a", UID: uid}, FirstAt: at, LastAt: at, ObservedAt: at, Origin: OriginObservedEvidence}
	}
	update(t, s, func(w *Writer) error {
		for _, e := range []Evidence{ev("kubelet", "node-a", t0), ev("old", "old-real-uid", t0), ev("early", "node-a", t0.Add(-time.Hour)), ev("direct", "n-new", t0), ev("justbefore", "node-a", t0.Add(-30*time.Second))} {
			if err := w.SaveEvidence(ctx, e); err != nil {
				return err
			}
		}
		return w.RelinkEvidenceOfNode(ctx, "n-new", "node-a", t0)
	})
	got := map[string]string{}
	pg, _ := s.Evidence(ctx, EvidenceQuery{Limit: 10})
	for _, e := range pg.Records {
		got[e.ID] = e.NodeID
	}
	if got["kubelet"] != "n-new" || got["direct"] != "n-new" || got["old"] != "" || got["early"] != "" || got["justbefore"] != "" {
		t.Fatalf("%v", got)
	}
}

// A Node recreated under the same name is a different Node: an Event at a given time is attributed only to the Node of
// that name that existed then, and to none when that is not exactly one.
func TestNodeIDByNameAtRespectsTheNodeLifetime(t *testing.T) {
	s, ctx := open(t)
	old := t0.Add(-10 * time.Hour)
	update(t, s, func(w *Writer) error {
		if err := w.SaveNode(ctx, Node{UID: "n-old", Name: "node-a", FirstSeenAt: old, LastSeenAt: old, CreatedAt: old, Origin: OriginObserved}); err != nil {
			return err
		}
		if err := w.MarkNodeDeleted(ctx, "n-old", t0.Add(-5*time.Hour)); err != nil {
			return err
		}
		return w.SaveNode(ctx, Node{UID: "n-new", Name: "node-a", FirstSeenAt: t0, LastSeenAt: t0, CreatedAt: t0, Origin: OriginObserved})
	})
	update(t, s, func(w *Writer) error {
		for at, want := range map[time.Time]string{
			t0.Add(-8 * time.Hour):  "n-old",
			t0.Add(-5 * time.Hour):  "n-old",
			t0.Add(-3 * time.Hour):  "", // between the two: no Node of that name existed
			t0.Add(time.Minute):     "n-new",
			t0.Add(-11 * time.Hour): "", // before either
		} {
			got, err := w.NodeIDByNameAt(ctx, "node-a", at)
			if err != nil || got != want {
				t.Errorf("at %v: got %q, want %q (%v)", at, got, want, err)
			}
		}
		return nil
	})
}

func TestScopeLastWatched(t *testing.T) {
	s, ctx := open(t)
	if _, ok, err := s.ScopeLastWatched(ctx, "Node", t0); err != nil || ok {
		t.Fatalf("never watched: %v %v", ok, err)
	}
	update(t, s, func(w *Writer) error { return w.OpenInterval(ctx, "Node", "healthy", t0.Add(-3*time.Hour)) })
	update(t, s, func(w *Writer) error { return w.CloseInterval(ctx, "Node", t0.Add(-2*time.Hour), "disabled") })
	at, ok, err := s.ScopeLastWatched(ctx, "Node", t0)
	if err != nil || !ok || !at.Equal(t0.Add(-2*time.Hour)) {
		t.Fatalf("closed interval: %v %v %v", at, ok, err)
	}
	update(t, s, func(w *Writer) error { return w.OpenInterval(ctx, "Node", "healthy", t0.Add(-time.Hour)) })
	if at, ok, _ := s.ScopeLastWatched(ctx, "Node", t0); !ok || !at.Equal(t0) {
		t.Fatalf("open interval reads as now: %v %v", at, ok)
	}
}

func TestWorkloadStateKeepsItsChangeBasis(t *testing.T) {
	s, ctx := open(t)
	update(t, s, func(w *Writer) error {
		if err := w.UpsertWorkload(ctx, Workload{ID: "w", Kind: KindStatefulSet, Namespace: "ns", Name: "db", FirstSeen: t0, LastSeen: t0}); err != nil {
			return err
		}
		return w.SaveWorkloadState(ctx, WorkloadState{WorkloadID: "w", Kind: KindStatefulSet, LastChangeAt: t0.Add(-48 * time.Hour), ChangeBasis: "baseline"})
	})
	update(t, s, func(w *Writer) error {
		st, ok, err := w.WorkloadState(ctx, "w")
		if err != nil || !ok || st.ChangeBasis != "baseline" || !st.LastChangeAt.Equal(t0.Add(-48*time.Hour)) {
			t.Fatalf("%+v %v %v", st, ok, err)
		}
		st.ChangeBasis = "garbage"
		return w.SaveWorkloadState(ctx, st)
	})
	update(t, s, func(w *Writer) error {
		if st, _, _ := w.WorkloadState(ctx, "w"); st.ChangeBasis != "observed" {
			t.Fatalf("an unknown basis is stored as observed: %q", st.ChangeBasis)
		}
		return nil
	})
}

// Migration 6 must not make Deployments look recently changed: a Deployment's last change becomes the time of its
// newest recorded release, marked as a baseline, not the time the state row was written.
func TestMigrationSixKeepsDeploymentsFromLookingRecentlyChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v5.db")
	ctx := context.Background()
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	recent := time.Now().UTC().Truncate(time.Second)
	old := recent.Add(-72 * time.Hour)
	if err := s.Update(ctx, func(w *Writer) error {
		if err := w.UpsertWorkload(ctx, Workload{ID: "d", Kind: KindDeployment, Namespace: "ns", Name: "api", FirstSeen: old.Add(-time.Hour), LastSeen: recent}); err != nil {
			return err
		}
		r := Release{ID: "r1", WorkloadID: "d", Namespace: "ns", Workload: "api", Revision: "1", RevisionNum: 1, ReplicaSetUID: "rs", ReplicaSetName: "rs",
			Fingerprint: "f", Origin: OriginObserved, ObservedAt: old, State: ReleaseCompleted, StateAt: old, Health: HealthObserving,
			HealthReasons: "[]", WindowSeconds: 600, Episodes: "[]", Trouble: "[]", Snapshot: "{}", ImageIDs: "{}"}
		if err := w.UpsertRelease(ctx, r); err != nil {
			return err
		}
		// The state row as migration 5 wrote it: stamped with the time it was recorded.
		return w.SaveWorkloadState(ctx, WorkloadState{WorkloadID: "d", Kind: KindDeployment, LastChangeAt: recent})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DROP INDEX evidence_record_key; ALTER TABLE evidence DROP COLUMN record_key; ALTER TABLE workload_state DROP COLUMN change_basis; PRAGMA user_version = 5`); err != nil {
		t.Fatalf("rewinding to version 5: %v", err)
	}
	s.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	wl, ok, err := s.WorkloadByID(ctx, "d")
	if err != nil || !ok || !wl.HasState {
		t.Fatalf("%+v %v %v", wl, ok, err)
	}
	if !wl.State.LastChangeAt.Equal(old) || wl.State.ChangeBasis != "baseline" {
		t.Fatalf("the last change must be the newest release's time, as a baseline: %v %q", wl.State.LastChangeAt, wl.State.ChangeBasis)
	}
}

// Migration 6 drops a Node link made by exact name under the earlier, time-blind rule where the Node did not exist
// when the Event began, and keeps one where it did.
func TestMigrationSixRevisitsNodeLinksMadeByName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v5.db")
	ctx := context.Background()
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	const via = "regarding names the Node (exact name; kubelet reports the name as the UID)"
	if err := s.Update(ctx, func(w *Writer) error {
		if err := w.SaveNode(ctx, Node{UID: "n-new", Name: "node-a", FirstSeenAt: t0, LastSeenAt: t0, CreatedAt: t0, Origin: OriginObserved}); err != nil {
			return err
		}
		for id, at := range map[string]time.Time{"before": t0.Add(-time.Hour), "after": t0.Add(time.Hour)} {
			e := Evidence{ID: id, Source: SourceEventsV1, SourceUID: "s" + id, Namespace: "default", Name: id, Regarding: Ref{Kind: "Node", Name: "node-a", UID: "node-a"},
				FirstAt: at, LastAt: at, ObservedAt: at, Origin: OriginObservedEvidence, NodeID: "n-new", LinkVia: via}
			if err := w.SaveEvidence(ctx, e); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DROP INDEX evidence_record_key; ALTER TABLE evidence DROP COLUMN record_key; ALTER TABLE workload_state DROP COLUMN change_basis; PRAGMA user_version = 5`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pg, _ := s.Evidence(ctx, EvidenceQuery{Limit: 10})
	got := map[string]string{}
	for _, e := range pg.Records {
		got[e.ID] = e.NodeID
	}
	if got["before"] != "" || got["after"] != "n-new" {
		t.Fatalf("%v", got)
	}
	// Migration 7 gave every existing Event its record time, so the Explorer still finds them.
	ex, err := s.Explorer(ctx, ExplorerQuery{From: t0.Add(-3 * time.Hour), To: t0.Add(3 * time.Hour), Routine: true})
	if err != nil || len(ex.Rows) != 2 {
		t.Fatalf("%v %d", err, len(ex.Rows))
	}
}
