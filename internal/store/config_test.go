package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// References open, end and reopen; a gone workload names nothing; config entries are history, never routine volume.
func TestConfigRefsAndConfigEntriesAreKept(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "opstide.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t0 := time.Date(2026, 11, 4, 5, 0, 0, 0, time.UTC)
	cm := ConfigName{KindConfigMap, "api-config"}
	sec := ConfigName{KindSecret, "api-db"}
	step := func(at time.Time, names ...ConfigName) {
		t.Helper()
		if err := s.Update(ctx, func(w *Writer) error { return w.SetConfigRefs(ctx, "d-1", "Deployment", "shop", "api", names, at) }); err != nil {
			t.Fatal(err)
		}
	}
	step(t0, cm, sec)
	step(t0.Add(time.Hour), cm)
	refs, _ := s.WorkloadConfigRefs(ctx, "d-1")
	if len(refs) != 2 || !refs[0].EndedAt.IsZero() || refs[1].RefName != "api-db" || !refs[1].EndedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("%+v", refs)
	}
	if r, _ := s.ConfigReferrers(ctx, "shop", KindSecret, "api-db", t0.Add(2*time.Hour)); len(r) != 0 {
		t.Fatalf("a reference that ended before the time is not listed: %+v", r)
	}
	if r, _ := s.ConfigReferrers(ctx, "shop", KindSecret, "api-db", t0.Add(30*time.Minute)); len(r) != 1 {
		t.Fatalf("one that ended after it is: %+v", r)
	}
	step(t0.Add(2*time.Hour), cm, sec)
	if r, _ := s.ConfigReferrers(ctx, "shop", KindSecret, "api-db", t0.Add(3*time.Hour)); len(r) != 1 || !r[0].EndedAt.IsZero() {
		t.Fatalf("reopened: %+v", r)
	}
	if err := s.Update(ctx, func(w *Writer) error { return w.MarkWorkloadDeleted(ctx, "d-1", t0.Add(4*time.Hour)) }); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.ConfigReferrers(ctx, "shop", KindConfigMap, "api-config", t0.Add(5*time.Hour)); len(r) != 0 {
		t.Fatalf("a deleted workload names nothing: %+v", r)
	}

	// Config entries are not in the routine-volume budget.
	if err := s.Update(ctx, func(w *Writer) error {
		for _, typ := range []string{"configmap.data_written", "configmap.created", "secret.metadata_written", "secret.deleted"} {
			if _, err := w.AddEvent(ctx, Event{ID: typ, Type: typ, ObservedAt: t0, Severity: "info", Confidence: Observed}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE `+routineEntries).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d routine, %v", n, err)
	}
}
