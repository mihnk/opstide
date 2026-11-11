package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// References open, end and reopen; a gone workload names nothing; config entries are history, never routine volume
// (except Helm's release records and folded writes, see TestConfigRetention).
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
		for _, typ := range []string{"configmap.data_written", "configmap.created", "secret.written", "secret.deleted"} {
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

// Retention removes ConfigMaps and Secrets deleted before the cut, references ended before it and references of
// workloads whose records were pruned; Helm's release records and folded writes are counted with the routine volume.
func TestConfigRetention(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "opstide.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 11, 4, 5, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	old, recent := now.AddDate(0, 0, -40), now.AddDate(0, 0, -1)
	cut := now.AddDate(0, 0, -30)
	if err := s.Update(ctx, func(w *Writer) error {
		for _, o := range []ConfigObject{
			{UID: "gone-old", Kind: KindConfigMap, Namespace: "a", Name: "x", FirstSeenAt: old, LastSeenAt: old, DeletedAt: old, Origin: OriginObserved},
			{UID: "gone-recent", Kind: KindConfigMap, Namespace: "a", Name: "y", FirstSeenAt: old, LastSeenAt: recent, DeletedAt: recent, Origin: OriginObserved},
			{UID: "live-old", Kind: KindSecret, Namespace: "a", Name: "z", FirstSeenAt: old, LastSeenAt: old, Origin: OriginAdopted},
		} {
			if err := w.SaveConfigObject(ctx, o); err != nil {
				return err
			}
		}
		// d-ended: its reference ended long ago. sts-pruned: a StatefulSet whose own record is gone. d-live: a live
		// Deployment OpsTide never put in its workloads table; its open reference stays.
		if err := w.SetConfigRefs(ctx, "d-ended", "Deployment", "a", "old", []ConfigName{{KindConfigMap, "x"}}, old); err != nil {
			return err
		}
		if err := w.EndConfigRefs(ctx, "d-ended", old.Add(time.Hour)); err != nil {
			return err
		}
		if err := w.SetConfigRefs(ctx, "sts-pruned", "StatefulSet", "a", "db", []ConfigName{{KindConfigMap, "y"}}, old); err != nil {
			return err
		}
		if err := w.SetConfigRefs(ctx, "d-live", "Deployment", "a", "api", []ConfigName{{KindConfigMap, "x"}}, old); err != nil {
			return err
		}
		for i, typ := range []string{"configmap.helm_release_written", "secret.helm_release_created", "configmap.writes_folded", "configmap.data_written"} {
			if _, err := w.AddEvent(ctx, Event{ID: typ, Type: typ, OccurredAt: recent.Add(time.Duration(i) * time.Minute), ObservedAt: recent, Severity: "info", Confidence: Observed}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r, err := s.PruneBudgets(ctx, cut, Retention{MaxEvents: 100, MaxRoutine: 1})
	if err != nil {
		t.Fatal(err)
	}
	if r.Capped["routine"] != 2 {
		t.Fatalf("Helm's release records and folded writes are routine volume: %+v", r)
	}
	ev, _ := s.Events(ctx, EventQuery{Limit: 10})
	if len(ev.Events) != 2 {
		t.Fatalf("%+v", ev.Events)
	}
	var uids []string
	rows, _ := s.db.QueryContext(ctx, `SELECT uid FROM config_objects ORDER BY uid`)
	for rows.Next() {
		var u string
		_ = rows.Scan(&u)
		uids = append(uids, u)
	}
	rows.Close()
	if len(uids) != 2 || uids[0] != "gone-recent" || uids[1] != "live-old" {
		t.Fatalf("deleted before the cut goes, the rest stays: %v", uids)
	}
	var refs []string
	rows, _ = s.db.QueryContext(ctx, `SELECT workload_id FROM config_refs ORDER BY workload_id`)
	for rows.Next() {
		var u string
		_ = rows.Scan(&u)
		refs = append(refs, u)
	}
	rows.Close()
	if len(refs) != 1 || refs[0] != "d-live" {
		t.Fatalf("ended and orphaned references go: %v", refs)
	}
}

// The hash key is made once, kept, and is 32 random bytes.
func TestConfigHashKeyIsMadeOnce(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "opstide.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, err := s.ConfigHashKey(ctx)
	if err != nil || len(a) != 32 {
		t.Fatal(a, err)
	}
	b, _ := s.ConfigHashKey(ctx)
	if string(a) != string(b) {
		t.Fatal("the key changed")
	}
}

// A workload's history includes a ConfigMap's entries only while its template named the ConfigMap.
func TestWorkloadHistoryConfigWindow(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "opstide.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t0 := time.Date(2026, 11, 4, 5, 0, 0, 0, time.UTC)
	if err := s.Update(ctx, func(w *Writer) error {
		if err := w.SaveConfigObject(ctx, ConfigObject{UID: "cm-1", Kind: KindConfigMap, Namespace: "a", Name: "x", FirstSeenAt: t0, LastSeenAt: t0, Origin: OriginObserved}); err != nil {
			return err
		}
		if err := w.SetConfigRefs(ctx, "d-1", "Deployment", "a", "api", []ConfigName{{KindConfigMap, "x"}}, t0.Add(time.Hour)); err != nil {
			return err
		}
		if err := w.EndConfigRefs(ctx, "d-1", t0.Add(3*time.Hour)); err != nil {
			return err
		}
		for i, at := range []time.Duration{0, 2 * time.Hour, 4 * time.Hour} {
			if _, err := w.AddEvent(ctx, Event{ID: "e" + string(rune('0'+i)), Type: "configmap.data_written", ResourceUID: "cm-1", OccurredAt: t0.Add(at), ObservedAt: t0, Severity: "info", Confidence: Observed}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	win, err := s.ConfigUIDs(ctx, "d-1")
	if err != nil || len(win) != 1 || !win[0].From.Equal(t0.Add(time.Hour)) || !win[0].To.Equal(t0.Add(3*time.Hour)) {
		t.Fatalf("%+v %v", win, err)
	}
	pg, _ := s.Events(ctx, EventQuery{WorkloadID: "d-1", AlsoResources: win, Limit: 10})
	if len(pg.Events) != 1 || pg.Events[0].ID != "e1" {
		t.Fatalf("only the entry inside the window: %+v", pg.Events)
	}
}
