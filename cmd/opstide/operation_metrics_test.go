package main

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mihnk/opstide/internal/collector"
	"github.com/mihnk/opstide/internal/metrics"
	"github.com/mihnk/opstide/internal/store"
	"k8s.io/client-go/kubernetes/fake"
)

// An alert on "the backup has not succeeded for 26 hours" needs a time per CronJob: when OpsTide last saw a run end
// well, end badly and begin. A CronJob that has never succeeded has no success series at all (not a zero).
func TestMetricsGiveEachCronJobItsLastSuccessAndFailure(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "opstide.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	if err := db.Update(ctx, func(w *store.Writer) error {
		for _, o := range []store.Operation{
			{ID: "a", Namespace: "data", Name: "backup", Schedule: "0 2 * * *", ConcurrencyPolicy: "Forbid", CreatedAt: now, FirstSeenAt: now, LastSeenAt: now},
			{ID: "b", Namespace: "data", Name: "report", Schedule: "0 * * * *", ConcurrencyPolicy: "Allow", Suspend: true, CreatedAt: now, FirstSeenAt: now, LastSeenAt: now},
		} {
			if err := w.UpsertOperation(ctx, o); err != nil {
				return err
			}
		}
		run := func(id, op string, st store.RunStatus, at time.Time) error {
			return w.UpsertRun(ctx, store.Run{ID: id, OperationID: op, Namespace: "data", JobName: id, CreatedAt: at, StartedAt: at, FinishedAt: at.Add(time.Minute), Status: st, ObservedAt: at})
		}
		if err := run("r1", "a", store.RunSucceeded, now.Add(-30*time.Hour)); err != nil {
			return err
		}
		if err := run("r2", "a", store.RunFailed, now.Add(-5*time.Hour)); err != nil {
			return err
		}
		return run("r3", "b", store.RunFailed, now.Add(-2*time.Hour)) // report has never succeeded
	}); err != nil {
		t.Fatal(err)
	}
	col := collector.New(fake.NewClientset(), db, collector.Options{})
	reg := &metrics.Registry{}
	reg.Add(ownMetrics(db, col, "test", "c", now))
	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`opstide_operation_last_success_timestamp_seconds{cronjob_namespace="data",cronjob="backup"} ` + strconv.FormatFloat(float64(now.Add(-30*time.Hour+time.Minute).Unix()), 'g', -1, 64),
		`opstide_operation_last_failure_timestamp_seconds{cronjob_namespace="data",cronjob="backup"}`,
		`opstide_operation_last_failure_timestamp_seconds{cronjob_namespace="data",cronjob="report"}`,
		`opstide_operation_suspended{cronjob_namespace="data",cronjob="report"} 1`,
		"opstide_operations 2",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, `opstide_operation_last_success_timestamp_seconds{cronjob_namespace="data",cronjob="report"}`) {
		t.Error("a CronJob that never succeeded has no success series")
	}
}
