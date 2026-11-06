package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func explorerFixture(t *testing.T) (*Store, context.Context) {
	t.Helper()
	s, ctx := open(t)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	update(t, s, func(w *Writer) error {
		add := func(id, typ, ns, kind, uid, name, conf, wl, rel string, when time.Time) error {
			_, err := w.AddEvent(ctx, Event{ID: id, Type: typ, Namespace: ns, Kind: kind, ResourceUID: uid, ResourceName: name, WorkloadID: wl, ReleaseID: rel,
				OccurredAt: when, ObservedAt: when.Add(time.Second), Severity: "info", Confidence: Confidence(conf), Summary: id + " summary"})
			return err
		}
		for _, e := range []struct {
			id, typ, ns, kind, uid, name, conf, wl, rel string
			m                                           int
		}{
			{"release.observed/r1", "release.observed", "shop", "Deployment", "d1", "api", "observed", "d1", "r1", 1},
			{"pod.created/p1", "pod.created", "shop", "Pod", "p1", "api-a", "observed", "d1", "r1", 2},
			{"pod.oomkilled/p1", "pod.oomkilled", "shop", "Pod", "p1", "api-a", "observed", "d1", "r1", 3},
			{"job.failed/j1", "job.failed", "batch", "Job", "j1", "nightly-1", "derived", "", "", 4},
			{"node.condition_changed/n1", "node.condition_changed", "", "Node", "n1", "node-a", "observed", "", "", 5},
			{"collection.gap/x", "collection.gap", "", "", "", "", "unknown", "", "", 6},
			{"evidence.event/su1", "evidence.event", "shop", "Pod", "p1", "api-a", "observed", "d1", "", 7}, // the Log entry of a Kubernetes Event: not an Explorer record
		} {
			if err := add(e.id, e.typ, e.ns, e.kind, e.uid, e.name, e.conf, e.wl, e.rel, at(e.m)); err != nil {
				return err
			}
		}
		ev := func(id, reason, typ string, first time.Time) Evidence {
			return Evidence{ID: id, Source: SourceEventsV1, SourceUID: "s" + id, Namespace: "shop", Name: id, Reason: reason, EventType: typ,
				Regarding: Ref{Kind: "Pod", Name: "api-a", UID: "p1"}, FirstAt: first, LastAt: first, ObservedAt: first.Add(time.Second), Origin: OriginObservedEvidence, ReplicaSetUID: "rs1", WorkloadID: "d1"}
		}
		for _, e := range []Evidence{ev("e1", "Unhealthy", "Warning", at(7)), ev("e2", "Pulled", "Normal", at(8)), ev("e3", "BackOff", "Warning", at(9))} {
			if err := w.SaveEvidence(ctx, e); err != nil {
				return err
			}
		}
		return nil
	})
	return s, ctx
}

func ids(p ExplorerPage) string {
	var out []string
	for _, r := range p.Rows {
		out = append(out, r.ID)
	}
	return strings.Join(out, ",")
}

func TestExplorerMergesLogAndEventsOnceInRecordTimeOrder(t *testing.T) {
	s, ctx := explorerFixture(t)
	q := ExplorerQuery{From: t0, To: t0.Add(time.Hour), Limit: 50}
	p, err := s.Explorer(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, r := range p.Rows {
		switch {
		case r.Event != nil:
			if strings.HasPrefix(r.Event.Type, "evidence.") {
				t.Fatalf("the Log entry of a Kubernetes Event is the same record as its evidence row: %+v", r.Event)
			}
			kinds = append(kinds, r.Event.Type)
		default:
			kinds = append(kinds, "k:"+r.Evidence.Reason)
		}
	}
	// Newest first; the Normal Event and the routine Pod entry are left out and counted.
	want := "k:BackOff,k:Unhealthy,collection.gap,node.condition_changed,job.failed,pod.oomkilled,release.observed"
	if got := strings.Join(kinds, ","); got != want {
		t.Fatalf("%s\nwant %s", got, want)
	}
	if p.SuppressedRoutine != 2 {
		t.Fatalf("suppressed %d", p.SuppressedRoutine)
	}
	q.Routine = true
	all, _ := s.Explorer(ctx, q)
	if len(all.Rows) != 9 || all.SuppressedRoutine != 0 {
		t.Fatalf("%d rows, suppressed %d", len(all.Rows), all.SuppressedRoutine)
	}
}

// Pages are cut by an immutable record time and a unique tie-break, so every record is seen exactly once, also when
// many records share a time, and a Kubernetes Event whose count grows does not move.
func TestExplorerCursorSeesEveryRecordOnceAndRecordsDoNotMove(t *testing.T) {
	s, ctx := open(t)
	same := t0.Add(time.Minute)
	update(t, s, func(w *Writer) error {
		for i := range 60 {
			if _, err := w.AddEvent(ctx, Event{ID: fmt.Sprintf("pod.restarted/%03d", i), Type: "pod.restarted", Namespace: "shop", Kind: "Pod", ResourceUID: fmt.Sprintf("p%d", i), OccurredAt: same, ObservedAt: same, Severity: "info", Confidence: Observed, Summary: "x"}); err != nil {
				return err
			}
			if err := w.SaveEvidence(ctx, Evidence{ID: fmt.Sprintf("ev%03d", i), Source: SourceEventsV1, SourceUID: fmt.Sprintf("s%d", i), Namespace: "shop", Name: "e", Reason: "BackOff", EventType: "Warning",
				Regarding: Ref{Kind: "Pod", UID: fmt.Sprintf("p%d", i)}, FirstAt: same, LastAt: same, ObservedAt: same, Origin: OriginObservedEvidence}); err != nil {
				return err
			}
		}
		return nil
	})
	walk := func(asc bool) []string {
		var got []string
		cursor := ""
		for range 100 {
			p, err := s.Explorer(ctx, ExplorerQuery{From: t0, To: t0.Add(time.Hour), Limit: 7, Cursor: cursor, Asc: asc})
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range p.Rows {
				got = append(got, r.ID)
			}
			if p.NextCursor == "" {
				return got
			}
			cursor = p.NextCursor
		}
		t.Fatal("did not end")
		return nil
	}
	desc := walk(false)
	if len(desc) != 120 {
		t.Fatalf("%d records seen", len(desc))
	}
	seen := map[string]bool{}
	for _, id := range desc {
		if seen[id] {
			t.Fatalf("%s seen twice", id)
		}
		seen[id] = true
	}
	asc := walk(true)
	for i := range asc {
		if asc[i] != desc[len(desc)-1-i] {
			t.Fatalf("ascending is not the exact reverse at %d", i)
		}
	}
	// A Kubernetes Event's count grows and it is last seen later: its place in the order does not change.
	before := walk(false)
	update(t, s, func(w *Writer) error {
		e, _, err := w.EvidenceByUID(ctx, "s5")
		if err != nil {
			return err
		}
		e.Count, e.LastAt = 40, same.Add(30*time.Minute)
		return w.SaveEvidence(ctx, e)
	})
	after := walk(false)
	if strings.Join(before, ",") != strings.Join(after, ",") {
		t.Fatal("a record moved because it was updated")
	}
}

func TestExplorerFiltersCombine(t *testing.T) {
	s, ctx := explorerFixture(t)
	base := ExplorerQuery{From: t0, To: t0.Add(time.Hour), Limit: 50, Routine: true}
	run := func(mut func(*ExplorerQuery)) string {
		q := base
		mut(&q)
		p, err := s.Explorer(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return ids(p)
	}
	n := func(p string) int { return len(strings.Split(p, ",")) }
	if got := run(func(q *ExplorerQuery) { q.Namespace = "batch" }); !strings.HasPrefix(got, "e.") || n(got) != 1 {
		t.Fatalf("namespace: %s", got)
	}
	if got := run(func(q *ExplorerQuery) { q.Groups = []string{"events"} }); n(got) != 3 || strings.Contains(got, "e.") {
		t.Fatalf("group events: %s", got)
	}
	if got := run(func(q *ExplorerQuery) { q.Groups = []string{"pods"} }); n(got) != 2 {
		t.Fatalf("group pods: %s", got)
	}
	if got := run(func(q *ExplorerQuery) { q.Reason = "BackOff" }); n(got) != 1 || !strings.HasPrefix(got, "k.") {
		t.Fatalf("reason: %s", got)
	}
	if got := run(func(q *ExplorerQuery) { q.UID = "p1" }); n(got) != 2+3 { // two Pod entries and three Kubernetes Events
		t.Fatalf("uid: %s", got)
	}
	if got := run(func(q *ExplorerQuery) { q.Provenance = []string{"derived"} }); n(got) != 1 {
		t.Fatalf("derived: %s", got)
	}
	if got := run(func(q *ExplorerQuery) { q.Provenance = []string{"unknown"} }); n(got) != 1 {
		t.Fatalf("unknown: %s", got)
	}
	if got := run(func(q *ExplorerQuery) { q.Sources = []string{SourceCoverage} }); n(got) != 1 {
		t.Fatalf("coverage: %s", got)
	}
	if got := run(func(q *ExplorerQuery) { q.Sources = []string{SourceOpsTideRule} }); n(got) != 1 {
		t.Fatalf("rule: %s", got)
	}
	if got := run(func(q *ExplorerQuery) { q.Sources = []string{"kubernetes.events.k8s.io/v1"} }); n(got) != 3 || strings.Contains(got, "e.") {
		t.Fatalf("event source: %s", got)
	}
	if got := run(func(q *ExplorerQuery) { q.Types = []string{EvidenceTypeName, "node.condition_changed"} }); n(got) != 4 {
		t.Fatalf("types: %s", got)
	}
	if got := run(func(q *ExplorerQuery) { q.WorkloadID = "d1" }); n(got) != 3+3 {
		t.Fatalf("workload: %s", got)
	}
	if got := run(func(q *ExplorerQuery) { q.Text = "nightly" }); n(got) != 1 {
		t.Fatalf("text: %s", got)
	}
	if got := run(func(q *ExplorerQuery) { q.NodeID = "n1" }); n(got) != 1 {
		t.Fatalf("node: %s", got)
	}
	// A release: its entries, and the Kubernetes Events of its ReplicaSet's Pods while it was current.
	if got := run(func(q *ExplorerQuery) {
		q.ReleaseID, q.ReleaseReplicaSet, q.ReleaseWorkload, q.ReleaseFrom = "r1", "rs1", "d1", t0
	}); n(got) != 3+3 {
		t.Fatalf("release: %s", got)
	}
	if got := run(func(q *ExplorerQuery) { q.From, q.To = t0.Add(4*time.Minute), t0.Add(6*time.Minute) }); n(got) != 2 {
		t.Fatalf("range: %s", got)
	}
	if _, err := s.Explorer(ctx, ExplorerQuery{}); err == nil {
		t.Fatal("a range is required")
	}
	if _, err := s.Explorer(ctx, ExplorerQuery{From: t0, To: t0.Add(time.Hour), Cursor: "junk"}); err == nil {
		t.Fatal("a bad cursor is an error")
	}
}

func TestExplorerFacetsLeaveOutTheirOwnFilter(t *testing.T) {
	s, ctx := explorerFixture(t)
	q := ExplorerQuery{From: t0, To: t0.Add(time.Hour), Routine: true, Groups: []string{"pods"}}
	f, err := s.ExplorerFacets(ctx, q, 10)
	if err != nil {
		t.Fatal(err)
	}
	if f.Groups["pods"] != 2 || f.Groups["events"] != 3 || f.Groups["nodes"] != 1 || f.Groups["operations"] != 1 {
		t.Fatalf("groups ignore their own filter: %v", f.Groups)
	}
	if f.Provenance["observed"] == 0 || f.Provenance["derived"] != 0 {
		t.Fatalf("provenance follows the other filters: %v", f.Provenance)
	}
	if len(f.Namespaces) != 1 || f.Namespaces[0].Name != "shop" || f.Namespaces[0].Count != 2 {
		t.Fatalf("%+v", f.Namespaces)
	}
	q.Groups = []string{"events"}
	f, err = s.ExplorerFacets(ctx, q, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Reasons) != 3 || f.Reasons[0].Count != 1 || f.Kinds["Pod"] != 3 {
		t.Fatalf("%+v %v", f.Reasons, f.Kinds)
	}
}

func TestExplorerRowsByIDSaysWhatIsGone(t *testing.T) {
	s, ctx := explorerFixture(t)
	p, _ := s.Explorer(ctx, ExplorerQuery{From: t0, To: t0.Add(time.Hour), Routine: true, Limit: 50})
	one, other := p.Rows[0].ID, p.Rows[len(p.Rows)-1].ID
	rows, missing, err := s.ExplorerRowsByID(ctx, []string{other, "e.000000099999", one, other, "junk", "k.nope"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != other || rows[1].ID != one || strings.Join(missing, ",") != "e.000000099999,junk,k.nope" && strings.Join(missing, ",") != "e.000000099999,k.nope,junk" {
		t.Fatalf("%v %v", ids(ExplorerPage{Rows: rows}), missing)
	}
	// Retention takes a record away; asking for it says so.
	if _, err := s.Prune(ctx, t0.Add(24*time.Hour), 1000); err != nil {
		t.Fatal(err)
	}
	rows, missing, _ = s.ExplorerRowsByID(ctx, []string{one})
	if len(rows) != 0 || len(missing) != 1 {
		t.Fatalf("%v %v", rows, missing)
	}
	if old, _ := s.OldestRecordTime(ctx); !old.IsZero() {
		t.Fatalf("nothing is held: %v", old)
	}
}

// The Explorer's queries are answered from indexes and the time range, not by reading the whole history.
func TestExplorerQueriesUseIndexes(t *testing.T) {
	s, ctx := open(t)
	from, to := t0, t0.Add(time.Hour)
	for name, q := range map[string]ExplorerQuery{
		"range":    {From: from, To: to},
		"uid":      {From: from, To: to, UID: "u1"},
		"workload": {From: from, To: to, WorkloadID: "w1"},
		"run":      {From: from, To: to, RunID: "r1"},
		"release":  {From: from, To: to, ReleaseID: "rel1"},
		"reason":   {From: from, To: to, Reason: "BackOff"},
		"cursor":   {From: from, To: to, Cursor: "1000.e.000000000001"},
	} {
		cur, _ := parseExplorerCursor(q.Cursor)
		for table, c := range map[string]clause{"events": q.eventsClause(cur), "evidence": q.evidenceClause(cur)} {
			rows, err := s.db.QueryContext(ctx, `EXPLAIN QUERY PLAN SELECT * FROM `+table+where(c)+` ORDER BY 1 LIMIT 101`, c.args...) //nolint:gosec // test: fixed clauses
			if err != nil {
				t.Fatalf("%s/%s: %v", name, table, err)
			}
			var plan []string
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				plan = append(plan, detail)
			}
			rows.Close()
			for _, d := range plan {
				if strings.HasPrefix(d, "SCAN "+table) && !strings.Contains(d, "USING") {
					t.Errorf("%s/%s reads the whole table: %v", name, table, plan)
				}
			}
		}
	}
}

// A Kubernetes Event first recorded without a time keeps its place when Kubernetes later gives it one: the record time
// is stored once, so a reader paging through the timeline cannot see it skipped or repeated.
func TestEvidenceRecordTimeNeverMoves(t *testing.T) {
	s, ctx := open(t)
	e := Evidence{ID: "k1", Source: SourceEventsV1, SourceUID: "s1", Namespace: "ns", Name: "n", Reason: "BackOff", EventType: "Warning",
		Regarding: Ref{Kind: "Pod", Name: "p", UID: "u"}, ObservedAt: t0, Origin: OriginObservedEvidence}
	update(t, s, func(w *Writer) error { return w.SaveEvidence(ctx, e) })
	e.FirstAt, e.LastAt = t0.Add(-time.Hour), t0.Add(-time.Hour)
	update(t, s, func(w *Writer) error { return w.SaveEvidence(ctx, e) })
	pg, err := s.Explorer(ctx, ExplorerQuery{From: t0.Add(-3 * time.Hour), To: t0.Add(time.Hour), Routine: true})
	if err != nil || len(pg.Rows) != 1 {
		t.Fatalf("%v %+v", err, pg.Rows)
	}
	if !pg.Rows[0].Key.Equal(t0) {
		t.Fatalf("the record time moved to %v when Kubernetes added a timestamp", pg.Rows[0].Key)
	}
	if pg.Rows[0].Evidence.FirstAt.IsZero() {
		t.Fatal("the source's own time is still stored")
	}
}

// The filters the Log offered live in the Explorer: severity, the owner of the operation, and job.created and reported
// counts count as routine.
func TestExplorerSeverityOwnerAndRoutine(t *testing.T) {
	s, ctx := open(t)
	op := Operation{ID: "op1", Namespace: "ns", Name: "backup", Owner: "data-team", Schedule: "0 * * * *", ConcurrencyPolicy: "Allow", CreatedAt: t0, FirstSeenAt: t0, LastSeenAt: t0}
	update(t, s, func(w *Writer) error {
		if err := w.UpsertOperation(ctx, op); err != nil {
			return err
		}
		add := func(id, typ, sev, opid string) error {
			_, err := w.AddEvent(ctx, Event{ID: id, Type: typ, Namespace: "ns", Kind: "Job", ResourceUID: id, ResourceName: id, OperationID: opid, OccurredAt: t0, ObservedAt: t0, Severity: sev, Confidence: Observed, Summary: id})
			return err
		}
		for _, e := range [][4]string{{"a", "job.failed", "error", "op1"}, {"b", "job.succeeded", "info", "op1"}, {"c", "job.failed", "error", ""}, {"d", "job.created", "info", "op1"}, {"e", "statefulset.counts_changed", "info", ""}} {
			if err := add(e[0], e[1], e[2], e[3]); err != nil {
				return err
			}
		}
		return nil
	})
	count := func(q ExplorerQuery) int {
		q.From, q.To, q.Limit = t0.Add(-time.Hour), t0.Add(time.Hour), 100
		pg, err := s.Explorer(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return len(pg.Rows)
	}
	if n := count(ExplorerQuery{Severities: []string{"error"}}); n != 2 {
		t.Fatalf("errors: %d", n)
	}
	if n := count(ExplorerQuery{Owner: "data-team"}); n != 2 { // a and b; d is routine and hidden
		t.Fatalf("owner: %d", n)
	}
	if n := count(ExplorerQuery{Owner: "data-team", Severities: []string{"error"}}); n != 1 {
		t.Fatalf("owner and severity: %d", n)
	}
	pg, _ := s.Explorer(ctx, ExplorerQuery{From: t0.Add(-time.Hour), To: t0.Add(time.Hour), Limit: 100})
	if pg.SuppressedRoutine != 2 || len(pg.Rows) != 3 {
		t.Fatalf("job.created and reported counts are routine: suppressed %d, shown %d", pg.SuppressedRoutine, len(pg.Rows))
	}
}
