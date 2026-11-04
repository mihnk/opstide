package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mihnk/opstide/internal/collector"
	"github.com/mihnk/opstide/internal/store"
)

type watcher struct{ problems []collector.Problem }

func (watcher) Synced(context.Context) error    { return nil }
func (w watcher) Problems() []collector.Problem { return w.problems }

var ist, _ = time.LoadLocation("Europe/Istanbul")

// local is a time on Wednesday 4 November 2026 in Istanbul (UTC+3).
func local(h, m int) time.Time { return time.Date(2026, 11, 4, h, m, 0, 0, ist).UTC() }

// fixture is the day in the design: payment reconciliation every 30 min,
// failed at 08:00 and running at 10:00; inventory sync due at 03:12 inside
// a 03:10–03:14 collection gap; now is 10:06.
func fixture(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "opstide.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	four := 4 * time.Minute
	created := local(0, 0).AddDate(0, -1, 0)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.Update(ctx, func(w *store.Writer) error {
		for _, op := range []store.Operation{
			{ID: "recon", Namespace: "payments", Name: "payment-reconciliation", DisplayName: "Payment reconciliation", Owner: "team-payments",
				RunbookURL: "https://wiki.example.com/r", ExpectedDuration: &four, Schedule: "*/30 * * * *", TimeZone: "Europe/Istanbul",
				ConcurrencyPolicy: "Forbid", SuccessfulHistory: 3, FailedHistory: 1, CreatedAt: created, FirstSeenAt: created, LastSeenAt: created},
			{ID: "inv", Namespace: "warehouse", Name: "inventory-sync", Schedule: "12 3 * * *", TimeZone: "Europe/Istanbul",
				ConcurrencyPolicy: "Allow", CreatedAt: created, FirstSeenAt: created, LastSeenAt: created},
			{ID: "weekly", Namespace: "data", Name: "weekly-report", Schedule: "0 6 * * 1", ConcurrencyPolicy: "Allow",
				CreatedAt: created, FirstSeenAt: created, LastSeenAt: created},
		} {
			if err := w.UpsertOperation(ctx, op); err != nil {
				return err
			}
		}
		for at := local(0, 0); at.Before(local(10, 1)); at = at.Add(30 * time.Minute) {
			r := store.Run{ID: "r" + at.In(ist).Format("1504"), OperationID: "recon", Namespace: "payments", JobName: "payment-reconciliation-x",
				ScheduledAt: at, ScheduledBasis: "name", CreatedAt: at.Add(time.Second), StartedAt: at.Add(3 * time.Second),
				FinishedAt: at.Add(3 * time.Minute), Status: store.RunSucceeded, Attempts: 1, ObservedAt: at}
			switch at {
			case local(8, 0):
				r.Status, r.StatusReason, r.FinishedAt, r.Attempts = store.RunFailed, "BackoffLimitExceeded", at.Add(15*time.Minute+40*time.Second), 2
			case local(10, 0):
				r.Status, r.FinishedAt = store.RunRunning, time.Time{}
			}
			if err := w.UpsertRun(ctx, r); err != nil {
				return err
			}
		}
		if _, err := w.AddEvent(ctx, store.Event{ID: "job.failed/r0800", Type: "job.failed", Namespace: "payments", OperationID: "recon", RunID: "r0800",
			OccurredAt: local(8, 15), ObservedAt: local(8, 15), Severity: "error", Confidence: store.Derived, Summary: "Payment reconciliation failed after 2 attempts"}); err != nil {
			return err
		}
		if _, err := w.AddEvent(ctx, store.Event{ID: "deployment.rollout/d/43", Type: "deployment.rollout", Namespace: "payments", ResourceName: "payments-api",
			OccurredAt: local(9, 42), ObservedAt: local(9, 42), Severity: "info", Confidence: store.Derived, Summary: "payments-api rolled out revision 43",
			Facts: map[string]string{"revision": "43"}}); err != nil {
			return err
		}
		// Watching since a week ago, with a gap 03:10–03:14 today.
		if err := w.OpenInterval(ctx, "jobs", store.Healthy, local(0, 0).AddDate(0, 0, -8)); err != nil {
			return err
		}
		if err := w.CloseInterval(ctx, "jobs", local(3, 10), "watch expired"); err != nil {
			return err
		}
		if err := w.OpenInterval(ctx, "jobs", store.Healthy, local(3, 14)); err != nil {
			return err
		}
		return w.Heartbeat(ctx, local(10, 6))
	}))
	s := New(db, watcher{problems: []collector.Problem{{Scope: "pods/kube-system", Err: "forbidden"}}},
		Config{Version: "test", Cluster: "prod-eu-west-1", Retention: store.Retention{Days: 30, MaxEvents: 2_000_000}, StartedAt: local(0, 0)}, nil)
	s.now = func() time.Time { return local(10, 6) }
	mux := http.NewServeMux()
	s.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv
}

func get(t *testing.T, srv *httptest.Server, path string, v any) int {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if v != nil {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func TestDay(t *testing.T) {
	_, srv := fixture(t)
	var d Day
	if code := get(t, srv, "/api/v1/day?tz=Europe/Istanbul", &d); code != 200 {
		t.Fatal(code)
	}
	c := d.Counts
	// 21 reconciliation runs 00:00–10:00: 19 ok, 1 failed, 1 running.
	if d.Date != "2026-11-04" || c.Observed != 21 || c.Succeeded != 19 || c.Failed != 1 || c.Running != 1 || c.Unconfirmed != 1 || c.Missed != 0 {
		t.Fatalf("counts %+v", c)
	}
	// 10:30 … 23:30: 27 reconciliation runs still to come.
	if c.StillDue != 27 {
		t.Fatalf("still due %d", c.StillDue)
	}
	if len(d.Gaps) != 1 || !d.Gaps[0].From.Equal(local(3, 10)) || !d.Gaps[0].To.Equal(local(3, 14)) {
		t.Fatalf("gaps %+v", d.Gaps)
	}
	// The weekly report (Mondays) is not due on a Wednesday: idle, not a lane.
	if len(d.Lanes) != 2 || d.Idle != 1 || d.Lanes[0].Name != "Payment reconciliation" || d.Lanes[1].Name != "inventory-sync" {
		t.Fatalf("lanes %d idle %d: %+v", len(d.Lanes), d.Idle, d.Lanes)
	}
	var failed, slowOrRunning int
	for _, m := range d.Lanes[0].Marks {
		switch m.State {
		case "failed":
			failed++
			if !m.Slow || m.RunID != "r0800" {
				t.Errorf("failed mark %+v", m)
			}
		case "running":
			slowOrRunning++
			if !m.End.Equal(local(10, 6)) {
				t.Errorf("running mark ends %v", m.End)
			}
		}
	}
	// 21 runs, then the 27 expected times still to come as one mark.
	if failed != 1 || slowOrRunning != 1 || len(d.Lanes[0].Marks) != 22 || d.Lanes[0].Marks[21].Count != 27 {
		t.Fatalf("marks: failed %d running %d total %d", failed, slowOrRunning, len(d.Lanes[0].Marks))
	}
	if m := d.Lanes[1].Marks; len(m) != 1 || m[0].State != "unconfirmed" || !m[0].Start.Equal(local(3, 12)) {
		t.Fatalf("inventory marks %+v", m)
	}
	kinds := map[string]Exception{}
	for _, e := range d.Exceptions {
		kinds[e.Kind] = e
	}
	if f := kinds["failed"]; f.Attempts != 2 || f.Seconds != 937 || f.Expected != 240 || f.Reason != "BackoffLimitExceeded" || f.Owner != "team-payments" {
		t.Fatalf("failed exception %+v", f)
	}
	if _, ok := kinds["unconfirmed"]; !ok || d.Exceptions[0].Kind != "failed" {
		t.Fatalf("exceptions %+v", d.Exceptions)
	}
	// Inventory sync's next run is tomorrow: not in today's list.
	if len(d.Upcoming) != 1 || !d.Upcoming[0].At.Equal(local(10, 30)) {
		t.Fatalf("upcoming %+v", d.Upcoming)
	}
	if len(d.Rollouts) != 1 || d.Rollouts[0].Revision != "43" || !d.LastWrite.Equal(local(10, 6)) {
		t.Fatalf("rollouts %+v lastWrite %v", d.Rollouts, d.LastWrite)
	}

	// Another zone moves the day's edges.
	if code := get(t, srv, "/api/v1/day?tz=UTC&date=2026-11-03", &d); code != 200 || d.Date != "2026-11-03" || !d.From.Equal(time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("utc day: %d %+v", code, d.From)
	}
	for _, bad := range []string{"?tz=Mars/Base", "?date=4.11.2026", "?tz=" + strings.Repeat("x", 70)} {
		if code := get(t, srv, "/api/v1/day"+bad, nil); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
}

func TestOperations(t *testing.T) {
	_, srv := fixture(t)
	var o Operations
	if code := get(t, srv, "/api/v1/operations?tz=Europe/Istanbul", &o); code != 200 {
		t.Fatal(code)
	}
	if len(o.Days) != 7 || o.Days[6] != "2026-11-04" || len(o.Operations) != 3 {
		t.Fatalf("%+v", o)
	}
	rows := map[string]OperationRow{}
	for _, r := range o.Operations {
		rows[r.ID] = r
	}
	recon := rows["recon"]
	// Before today the record has no runs while watching: missed every day.
	if recon.Week[6].State != "failed" || recon.Week[6].Runs != 21 || recon.Week[0].State != "missed" {
		t.Fatalf("recon week %+v", recon.Week)
	}
	if recon.LastRun == nil || recon.LastRun.Status != "running" || !recon.Next.Equal(local(10, 30)) || recon.ExpectedSeconds != 240 ||
		len(recon.Attention) == 0 || recon.Attention[0] != "failed in the last 24 h" {
		t.Fatalf("recon %+v", recon)
	}
	if inv := rows["inv"]; inv.Week[6].State != "unconfirmed" || len(inv.Attention) == 0 {
		t.Fatalf("inventory %+v", inv)
	}
	if wk := rows["weekly"]; wk.ZoneAssumed != "UTC" || wk.Week[6].State != "none" {
		t.Fatalf("weekly %+v", wk)
	}
}

func TestOperationDetailAndRun(t *testing.T) {
	_, srv := fixture(t)
	var d OperationDetail
	if code := get(t, srv, "/api/v1/operations/recon?tz=Europe/Istanbul&runs=5", &d); code != 200 {
		t.Fatal(code)
	}
	if len(d.Days) != 7 || len(d.Days[6].Marks) != 22 || d.TimesToday != 48 || len(d.Runs) != 5 || !d.MoreRuns || d.Runs[0].ID != "r1000" ||
		len(d.Gaps) != 1 || d.Operation.Concurrency != "Forbid" || !d.Next.Equal(local(10, 30)) {
		t.Fatalf("detail: days %d marks %d runs %d first %s more %v gaps %+v next %v", len(d.Days), len(d.Days[6].Marks), len(d.Runs), d.Runs[0].ID, d.MoreRuns, d.Gaps, d.Next)
	}
	var older OperationDetail
	get(t, srv, "/api/v1/operations/recon?runs=5&before="+d.Runs[4].CreatedAt.Format(time.RFC3339Nano), &older)
	if len(older.Runs) != 5 || older.Runs[0].CreatedAt.After(d.Runs[4].CreatedAt) || older.Runs[0].ID == d.Runs[4].ID {
		t.Fatalf("older page %+v", older.Runs)
	}
	if code := get(t, srv, "/api/v1/operations/nope", nil); code != 404 {
		t.Fatalf("unknown operation: %d", code)
	}

	var rd RunDetail
	if code := get(t, srv, "/api/v1/runs/r0800", &rd); code != 200 {
		t.Fatal(code)
	}
	if rd.Run.Status != "failed" || !rd.Run.Slow || rd.Operation == nil || rd.Operation.Name != "payment-reconciliation" || len(rd.Entries) != 1 {
		t.Fatalf("run %+v", rd)
	}
}

func TestEventsAndSystem(t *testing.T) {
	_, srv := fixture(t)
	var e Entries
	if code := get(t, srv, "/api/v1/events?show=runs,starts", &e); code != 200 || len(e.Entries) != 1 || e.Entries[0].Type != "job.failed" {
		t.Fatalf("%d %+v", code, e)
	}
	get(t, srv, "/api/v1/events?q=payments-api", &e)
	if len(e.Entries) != 1 || e.Entries[0].Facts["revision"] != "43" {
		t.Fatalf("search %+v", e)
	}
	get(t, srv, "/api/v1/events?within=1h", &e)
	if len(e.Entries) != 1 || e.Entries[0].Type != "deployment.rollout" {
		t.Fatalf("within an hour of 10:06: %+v", e.Entries)
	}
	for _, bad := range []string{"?show=everything", "?limit=-1", "?from=yesterday", "?cursor=x", "?within=forever"} {
		if code := get(t, srv, "/api/v1/events"+bad, nil); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	if code := get(t, srv, "/api/v1/nothing", nil); code != 404 {
		t.Errorf("unknown endpoint: %d", code)
	}

	var sys System
	if code := get(t, srv, "/api/v1/system?tz=Europe/Istanbul", &sys); code != 200 {
		t.Fatal(code)
	}
	if !sys.Synced || sys.Storage.RetentionDays != 30 || len(sys.Integrations) != 4 || len(sys.Scopes) != 2 {
		t.Fatalf("system %+v", sys)
	}
	if j := sys.Scopes[0]; j.Kind != "jobs" || len(j.Watching) != 2 || !j.Watching[0].To.Equal(local(3, 10)) {
		t.Fatalf("jobs scope %+v", sys.Scopes)
	}
	if p := sys.Scopes[1]; p.Scope != "pods/kube-system" || p.Problem != "forbidden" {
		t.Fatalf("problem scope %+v", p)
	}
}

func TestStreamSaysHelloAndPings(t *testing.T) {
	_, srv := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	var events []string
	for sc.Scan() && len(events) < 2 {
		if line := sc.Text(); strings.HasPrefix(line, "event: ") {
			events = append(events, strings.TrimPrefix(line, "event: "))
		}
	}
	if len(events) != 2 || events[0] != "hello" || events[1] != "ping" {
		t.Fatalf("events %v", events)
	}
}
