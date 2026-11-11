package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mihnk/opstide/internal/store"
)

// beforeServer holds two stories around base-1h:
//   - a CronJob whose run failed at base-1h. Before it: its own spec written (OWNERSHIP, the Job's CronJob) and the
//     ConfigMap its Pod template names written (REFERENCE); an unrelated CronJob's spec and an unrelated ConfigMap
//     written too, and a write to the CronJob after the failure. The ConfigMap watch was lost for ten minutes in the hour.
//   - a Deployment Pod that restarted at base-1h on node-a: the Deployment's spec written before it, and the Node's
//     condition changed. node-a was recreated under the same name later; the later Node's changes are not its.
func beforeServer(t *testing.T, tune func(*Config)) *httptest.Server {
	t.Helper()
	fail := base.Add(-time.Hour)
	more := tune
	tune = func(c *Config) {
		c.WatchConfigMaps = true
		if more != nil {
			more(c)
		}
	}
	ops := []store.Operation{operation("cj-a", "0 * * * *"), operation("cj-other", "0 * * * *")}
	return setupWith(t, base, ops, func(ctx context.Context, w *store.Writer) error {
		for _, sc := range []string{"cronjobs", "jobs", "deployments", "replicasets", "deploypods", "hpas", "pods", "secrets", "nodes"} {
			if err := w.OpenInterval(ctx, sc, store.Healthy, base.Add(-30*time.Hour)); err != nil {
				return err
			}
		}
		if err := w.OpenInterval(ctx, "configmaps", store.Healthy, base.Add(-30*time.Hour)); err != nil {
			return err
		}
		if err := w.CloseInterval(ctx, "configmaps", fail.Add(-30*time.Minute), "watch lost"); err != nil {
			return err
		}
		if err := w.OpenInterval(ctx, "configmaps", store.Healthy, fail.Add(-20*time.Minute)); err != nil {
			return err
		}
		if err := w.SetConfigRefs(ctx, "cj-a", "CronJob", "ns", "cj-a", []store.ConfigName{{Kind: store.KindConfigMap, Name: "job-config"}}, base.Add(-30*time.Hour)); err != nil {
			return err
		}
		if err := w.UpsertWorkload(ctx, store.Workload{ID: "d-1", Kind: store.KindDeployment, Origin: store.OriginObserved, Namespace: "shop", Name: "api", FirstSeen: base.Add(-30 * time.Hour), LastSeen: base}); err != nil {
			return err
		}
		if err := w.UpsertPod(ctx, store.PodRecord{UID: "p-1", Namespace: "shop", Name: "api-1", Kind: store.KindDeployment, WorkloadID: "d-1", Node: "node-a", CreatedAt: base.Add(-5 * time.Hour), State: "{}"}); err != nil {
			return err
		}
		for _, n := range []store.Node{
			{UID: "n-old", Name: "node-a", FirstSeenAt: base.Add(-30 * time.Hour), LastSeenAt: fail.Add(10 * time.Minute), CreatedAt: base.Add(-40 * time.Hour), DeletedAt: fail.Add(10 * time.Minute), Origin: store.OriginAdopted, Conditions: "[]"},
			{UID: "n-new", Name: "node-a", FirstSeenAt: fail.Add(20 * time.Minute), LastSeenAt: base, CreatedAt: fail.Add(20 * time.Minute), Origin: store.OriginObserved, Conditions: "[]"},
		} {
			if err := w.SaveNode(ctx, n); err != nil {
				return err
			}
		}
		add := func(id, typ, ns, kind, uid, name string, at time.Time, set func(*store.Event)) error {
			e := store.Event{ID: id, Type: typ, Namespace: ns, Kind: kind, ResourceUID: uid, ResourceName: name, OccurredAt: at, ObservedAt: at.Add(time.Second),
				Severity: "info", Confidence: store.Observed, Summary: id, Facts: map[string]string{"fieldManager": "mgr-" + id}}
			if set != nil {
				set(&e)
			}
			_, err := w.AddEvent(ctx, e)
			return err
		}
		op := func(id string) func(*store.Event) { return func(e *store.Event) { e.OperationID = id } }
		wl := func(e *store.Event) { e.WorkloadID = "d-1" }
		for _, e := range []struct {
			id, typ, ns, kind, uid, name string
			at                           time.Time
			set                          func(*store.Event)
		}{
			{"spec", "cronjob.spec_written", "ns", "CronJob", "cj-a", "cj-a", fail.Add(-14 * time.Minute), op("cj-a")},
			{"cfg", "configmap.data_written", "ns", "ConfigMap", "cm-1", "job-config", fail.Add(-5 * time.Minute), nil},
			{"other-spec", "cronjob.spec_written", "ns", "CronJob", "cj-other", "cj-other", fail.Add(-10 * time.Minute), op("cj-other")},
			{"other-cfg", "configmap.data_written", "ns", "ConfigMap", "cm-2", "other-config", fail.Add(-9 * time.Minute), nil},
			{"old-spec", "cronjob.spec_written", "ns", "CronJob", "cj-a", "cj-a", fail.Add(-3 * time.Hour), op("cj-a")},
			{"later", "cronjob.schedule_changed", "ns", "CronJob", "cj-a", "cj-a", fail.Add(5 * time.Minute), op("cj-a")},
			{"fail", "job.failed", "ns", "Job", "run-1", "cj-a-1", fail, func(e *store.Event) {
				e.OperationID, e.RunID, e.Severity, e.Confidence = "cj-a", "run-1", "error", store.Derived
			}},
			// The Deployment Pod story.
			{"d-spec", "deployment.spec_written", "shop", "Deployment", "d-1", "api", fail.Add(-20 * time.Minute), wl},
			{"n-cond", "node.condition_changed", "", "Node", "n-old", "node-a", fail.Add(-8 * time.Minute), nil},
			{"n-new-cond", "node.condition_changed", "", "Node", "n-new", "node-a", fail.Add(30 * time.Minute), nil},
			{"restart", "pod.restarted", "shop", "Pod", "p-1", "api-1", fail, func(e *store.Event) { e.WorkloadID, e.Severity = "d-1", "warning" }},
			// A Pod with no owner recorded, and nothing before it: an empty answer.
			{"lonely", "pod.restarted", "shop", "Pod", "p-9", "lonely", fail, nil},
		} {
			if err := add(e.id, e.typ, e.ns, e.kind, e.uid, e.name, e.at, e.set); err != nil {
				return err
			}
		}
		return nil
	}, tune)
}

// recordID finds a record's Explorer id by its summary (the tests write the id as the summary).
func recordID(t *testing.T, srv *httptest.Server, summary string) string {
	t.Helper()
	var l ExplorerList
	if code := get(t, srv, "/api/v1/explorer?range=24h&routine=1&limit=500&q="+summary, &l); code != 200 {
		t.Fatal(code)
	}
	for _, r := range l.Records {
		if r.Summary == summary {
			return r.ID
		}
	}
	t.Fatalf("no record %q", summary)
	return ""
}

func changesBy(b ExplorerBefore) map[string]BeforeChange {
	out := map[string]BeforeChange{}
	for _, c := range b.Changes {
		out[c.Record.Summary] = c
	}
	return out
}

func TestBeforeAFailedRunListsItsCronJobAndConfigChangesAndTheGap(t *testing.T) {
	srv := beforeServer(t, nil)
	id := recordID(t, srv, "fail")
	var b ExplorerBefore
	if code := get(t, srv, "/api/v1/explorer/records/"+id+"/before", &b); code != 200 {
		t.Fatal(code)
	}
	if b.Schema != Schema || b.Window != "1h" || !b.FailureLike || b.Order != "newest_first" || !b.To.Equal(base.Add(-time.Hour)) || !b.From.Equal(base.Add(-2*time.Hour)) {
		t.Fatalf("header: %+v", b)
	}
	got := changesBy(b)
	if c, ok := got["spec"]; !ok || c.Classification != Ownership || c.Writer != "mgr-spec" || c.SecondsBefore != 14*60 || c.Subject.Kind != "CronJob" {
		t.Fatalf("the CronJob's spec write is there as OWNERSHIP with its writer: %+v", got)
	}
	if c, ok := got["cfg"]; !ok || c.Classification != Reference || c.Subject.Name != "job-config" || c.Record.Provenance != ProvObserved {
		t.Fatalf("the referenced ConfigMap's write is there as REFERENCE: %+v", got)
	}
	for _, x := range []string{"other-spec", "other-cfg", "later", "old-spec", "fail"} {
		if _, ok := got[x]; ok {
			t.Fatalf("%s is unrelated, after the record, outside the window or the record itself: %+v", x, got)
		}
	}
	if len(b.Changes) != 2 || b.Changes[0].Record.Summary != "cfg" {
		t.Fatalf("newest first: %+v", b.Changes)
	}
	if b.Coverage.State != CovGap || b.Empty != nil {
		t.Fatalf("the ConfigMap watch had a gap in the window: %+v", b.Coverage)
	}
	var cm *BeforeScope
	for i, sc := range b.Coverage.Scopes {
		if sc.Scope == "configmaps" {
			cm = &b.Coverage.Scopes[i]
		}
		if sc.Scope == "deployments" {
			t.Fatalf("a Job's window does not depend on the Deployment watch: %+v", b.Coverage.Scopes)
		}
	}
	if cm == nil || cm.State != CovGap || len(cm.Gaps) != 1 || cm.Gaps[0].To.Sub(cm.Gaps[0].From) != 10*time.Minute {
		t.Fatalf("the ConfigMap gap is reported: %+v", b.Coverage.Scopes)
	}
	if !strings.Contains(b.Note, "does not make them a cause") {
		t.Fatalf("the note says before, not because: %q", b.Note)
	}
}

func TestBeforeAPodRestartFollowsItsOwnerAndItsNodeAtTheTime(t *testing.T) {
	srv := beforeServer(t, func(c *Config) { c.ObserveNodes = true })
	var b ExplorerBefore
	if code := get(t, srv, "/api/v1/explorer/records/"+recordID(t, srv, "restart")+"/before?window=6h", &b); code != 200 {
		t.Fatal(code)
	}
	got := changesBy(b)
	if c, ok := got["d-spec"]; !ok || c.Classification != Ownership || c.Subject.Name != "api" {
		t.Fatalf("the owning Deployment's spec write: %+v", got)
	}
	if c, ok := got["n-cond"]; !ok || c.Classification != SameResourceContext || c.Subject.UID != "n-old" {
		t.Fatalf("the Node the Pod ran on at the time: %+v", got)
	}
	if _, ok := got["n-new-cond"]; ok || len(got) != 2 {
		t.Fatalf("only those two (the later Node of the same name is another Node): %+v", got)
	}
}

func TestBeforeEmptyStatesAreDistinct(t *testing.T) {
	srv := beforeServer(t, nil)
	lonely := recordID(t, srv, "lonely")
	var b ExplorerBefore
	// The 15 minutes before: every watch whole (a Pod with no owner depends on none), nothing recorded.
	if code := get(t, srv, "/api/v1/explorer/records/"+lonely+"/before?window=15m", &b); code != 200 || b.Empty == nil || b.Empty.Kind != "no_changes_recorded" || len(b.Changes) != 0 {
		t.Fatalf("%d %+v", code, b.Empty)
	}
	found := false
	for _, r := range b.Related {
		found = found || r.Classification == Unresolved
	}
	if !found {
		t.Fatalf("a Pod with no recorded owner says so: %+v", b.Related)
	}
	// A shorter window misses the ConfigMap watch's gap.
	srv2 := beforeServer(t, nil)
	var b2 ExplorerBefore
	if code := get(t, srv2, "/api/v1/explorer/records/"+recordID(t, srv2, "fail")+"/before?window=15m", &b2); code != 200 {
		t.Fatal(code)
	}
	if len(b2.Changes) != 2 || b2.Coverage.State != CovWatching { // the two writes 5 and 14 minutes before; the ConfigMap gap was earlier
		t.Fatalf("%+v", b2.Changes)
	}
	// The longest window still lies inside what was watched (from 30 h before now).
	var b3 ExplorerBefore
	if code := get(t, srv2, "/api/v1/explorer/records/"+recordID(t, srv2, "lonely")+"/before?window=24h", &b3); code != 200 {
		t.Fatal(code)
	}
	if b3.Empty == nil || b3.Empty.Kind != "no_changes_recorded" {
		t.Fatalf("watched for 30 h, so a 24 h window is covered: %+v", b3.Empty)
	}
}

func TestBeforeEmptyWithGapsAndBeforeFirstObservation(t *testing.T) {
	at := base.Add(-time.Hour)
	srv := setupWith(t, base, []store.Operation{operation("cj-a", "0 * * * *")}, func(ctx context.Context, w *store.Writer) error {
		if err := w.OpenInterval(ctx, "cronjobs", store.Healthy, at.Add(-30*time.Minute)); err != nil {
			return err
		}
		if err := w.CloseInterval(ctx, "cronjobs", at.Add(-20*time.Minute), "lost"); err != nil {
			return err
		}
		if err := w.OpenInterval(ctx, "cronjobs", store.Healthy, at.Add(-10*time.Minute)); err != nil {
			return err
		}
		_, err := w.AddEvent(ctx, store.Event{ID: "f", Type: "job.failed", Namespace: "ns", Kind: "Job", ResourceUID: "r", ResourceName: "j", OperationID: "cj-a", RunID: "r",
			OccurredAt: at, ObservedAt: at, Severity: "error", Confidence: store.Derived, Summary: "f"})
		return err
	}, func(*Config) {})
	id := recordID(t, srv, "f")
	var b ExplorerBefore
	if code := get(t, srv, "/api/v1/explorer/records/"+id+"/before?window=15m", &b); code != 200 || b.Empty == nil || b.Empty.Kind != "no_changes_recorded_with_gaps" {
		t.Fatalf("%d %+v %+v", code, b.Empty, b.Coverage)
	}
	if code := get(t, srv, "/api/v1/explorer/records/"+id+"/before?window=1h", &b); code != 200 || b.Empty == nil || b.Empty.Kind != CovBeforeFirst || b.Coverage.State != CovBeforeFirst {
		t.Fatalf("%d %+v %+v", code, b.Empty, b.Coverage)
	}
}

func TestBeforeValidatesTheWindow(t *testing.T) {
	srv := beforeServer(t, nil)
	id := recordID(t, srv, "fail")
	for path, want := range map[string]int{
		"/before?window=48h": 400, // more than 24 hours
		"/before?window=7d":  400,
		"/before?window=2h":  400, // not one of the windows offered
		"/before?window=abc": 400,
		"/before?range=1h":   400,
		"/before?window=24h": 200,
		"/before?window=15m": 200,
		"/before":            200,
	} {
		if code := get(t, srv, "/api/v1/explorer/records/"+id+path, nil); code != want {
			t.Fatalf("%s: %d, want %d", path, code, want)
		}
	}
	if code := get(t, srv, "/api/v1/explorer/records/e.999999/before", nil); code != 404 {
		t.Fatalf("a record not held: %d", code)
	}
	if code := get(t, srv, "/api/v1/explorer/records/x!/before", nil); code != 400 {
		t.Fatalf("not an id: %d", code)
	}
}
