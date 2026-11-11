package api

import (
	"context"
	"net/http/httptest"
	"strconv"
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
	return beforeServerWith(t, tune, nil)
}

// beforeServerWith is beforeServer with more written after its stories.
func beforeServerWith(t *testing.T, tune func(*Config), also func(context.Context, *store.Writer) error) *httptest.Server {
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
		// A Pod on a Node OpsTide holds no record of.
		if err := w.UpsertPod(ctx, store.PodRecord{UID: "p-2", Namespace: "shop", Name: "api-2", Kind: store.KindDeployment, WorkloadID: "d-1", Node: "node-z", CreatedAt: base.Add(-5 * time.Hour), State: "{}"}); err != nil {
			return err
		}
		// The Deployment's template named mid-config from 40 minutes before the restart until a minute after it, when
		// it started naming late-config instead.
		if err := w.SetConfigRefs(ctx, "d-1", store.KindDeployment, "shop", "api", []store.ConfigName{{Kind: store.KindConfigMap, Name: "mid-config"}}, fail.Add(-40*time.Minute)); err != nil {
			return err
		}
		if err := w.SetConfigRefs(ctx, "d-1", store.KindDeployment, "shop", "api", []store.ConfigName{{Kind: store.KindConfigMap, Name: "late-config"}}, fail.Add(time.Minute)); err != nil {
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
			// At the failure's very millisecond: written before it (a smaller id: before it on the timeline), and after.
			{"same-ms", "cronjob.suspended", "ns", "CronJob", "cj-a", "cj-a", fail, op("cj-a")},
			{"cfg", "configmap.data_written", "ns", "ConfigMap", "cm-1", "job-config", fail.Add(-5 * time.Minute), nil},
			{"other-spec", "cronjob.spec_written", "ns", "CronJob", "cj-other", "cj-other", fail.Add(-10 * time.Minute), op("cj-other")},
			{"other-cfg", "configmap.data_written", "ns", "ConfigMap", "cm-2", "other-config", fail.Add(-9 * time.Minute), nil},
			{"old-spec", "cronjob.spec_written", "ns", "CronJob", "cj-a", "cj-a", fail.Add(-3 * time.Hour), op("cj-a")},
			{"later", "cronjob.schedule_changed", "ns", "CronJob", "cj-a", "cj-a", fail.Add(5 * time.Minute), op("cj-a")},
			{"fail", "job.failed", "ns", "Job", "run-1", "cj-a-1", fail, func(e *store.Event) {
				e.OperationID, e.RunID, e.Severity, e.Confidence = "cj-a", "run-1", "error", store.Derived
			}},
			{"same-ms-after", "cronjob.resumed", "ns", "CronJob", "cj-a", "cj-a", fail, op("cj-a")},
			// The other CronJob's run failed two hours earlier, with nothing changed before it.
			{"other-fail", "job.failed", "ns", "Job", "run-9", "cj-other-1", fail.Add(-2 * time.Hour), func(e *store.Event) {
				e.OperationID, e.RunID, e.Severity, e.Confidence = "cj-other", "run-9", "error", store.Derived
			}},
			// The Deployment Pod story.
			{"d-spec", "deployment.spec_written", "shop", "Deployment", "d-1", "api", fail.Add(-20 * time.Minute), wl},
			{"n-cond", "node.condition_changed", "", "Node", "n-old", "node-a", fail.Add(-8 * time.Minute), nil},
			{"n-new-cond", "node.condition_changed", "", "Node", "n-new", "node-a", fail.Add(30 * time.Minute), nil},
			{"hpa", "hpa.desired_replicas_changed", "shop", "HorizontalPodAutoscaler", "hpa-1", "api-hpa", fail.Add(-2 * time.Minute), wl},
			{"mid-early", "configmap.data_written", "shop", "ConfigMap", "cm-m", "mid-config", fail.Add(-50 * time.Minute), nil},
			{"mid-cfg", "configmap.data_written", "shop", "ConfigMap", "cm-m", "mid-config", fail.Add(-30 * time.Minute), nil},
			{"late-cfg", "configmap.data_written", "shop", "ConfigMap", "cm-l", "late-config", fail.Add(-3 * time.Minute), nil},
			{"restart", "pod.restarted", "shop", "Pod", "p-1", "api-1", fail, func(e *store.Event) { e.WorkloadID, e.Severity = "d-1", "warning" }},
			{"restart-z", "pod.restarted", "shop", "Pod", "p-2", "api-2", fail, func(e *store.Event) { e.WorkloadID, e.Severity = "d-1", "warning" }},
			// A Pod with no owner recorded, and nothing before it: an empty answer.
			{"lonely", "pod.restarted", "shop", "Pod", "p-9", "lonely", fail, nil},
		} {
			if err := add(e.id, e.typ, e.ns, e.kind, e.uid, e.name, e.at, e.set); err != nil {
				return err
			}
		}
		if also != nil {
			return also(ctx, w)
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
	for _, x := range []string{"other-spec", "other-cfg", "later", "old-spec", "fail", "same-ms-after"} {
		if _, ok := got[x]; ok {
			t.Fatalf("%s is unrelated, after the record (on the timeline), outside the window or the record itself: %+v", x, got)
		}
	}
	if c, ok := got["same-ms"]; !ok || c.SecondsBefore != 0 {
		t.Fatalf("a change at the record's millisecond that the timeline puts before it is listed: %+v", got)
	}
	if len(b.Changes) != 3 || b.Changes[0].Record.Summary != "same-ms" || b.Changes[1].Record.Summary != "cfg" {
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
	if c, ok := got["mid-cfg"]; !ok || c.Classification != Reference || c.Subject.Name != "mid-config" {
		t.Fatalf("the ConfigMap the template named then: %+v", got)
	}
	if c, ok := got["hpa"]; !ok || c.Classification != SameResourceContext || c.Subject.Kind != "HorizontalPodAutoscaler" || c.Subject.UID != "hpa-1" {
		t.Fatalf("an autoscaler's change recorded against the Deployment is about the autoscaler, not the owner: %+v", got)
	}
	for _, x := range []string{"n-new-cond", "mid-early", "late-cfg"} {
		if _, ok := got[x]; ok {
			t.Fatalf("%s: a later Node of the same name, a write before the template named the ConfigMap, a ConfigMap named only after the record: %+v", x, got)
		}
	}
	if len(got) != 4 {
		t.Fatalf("only those four: %+v", got)
	}
	for _, r := range b.Related {
		if r.Name == "late-config" {
			t.Fatalf("a ConfigMap the template only named after the record is not related: %+v", b.Related)
		}
	}
	if b.Coverage.State != CovGap || len(b.Coverage.Unresolved) != 0 { // the ConfigMap watch's gap is in these 6 hours
		t.Fatalf("everything related was resolved: %+v", b.Coverage)
	}
}

// A Pod whose Node OpsTide does not hold (Node observation off): the Node is unresolved, its watch is listed as off,
// and the window never reads as watched.
func TestBeforeAnUnresolvedNodeIsNotWatchedThroughout(t *testing.T) {
	srv := beforeServer(t, nil)
	var b ExplorerBefore
	if code := get(t, srv, "/api/v1/explorer/records/"+recordID(t, srv, "restart-z")+"/before?window=15m", &b); code != 200 {
		t.Fatal(code)
	}
	found := false
	for _, r := range b.Related {
		found = found || (r.Kind == "Node" && r.Name == "node-z" && r.Classification == Unresolved)
	}
	if !found {
		t.Fatalf("the Node is listed, unresolved: %+v", b.Related)
	}
	var nodes *BeforeScope
	for i, sc := range b.Coverage.Scopes {
		if sc.Scope == "nodes" {
			nodes = &b.Coverage.Scopes[i]
		}
	}
	if nodes == nil || nodes.State != CovDisabled {
		t.Fatalf("the Node watch it depends on is off: %+v", b.Coverage.Scopes)
	}
	if b.Coverage.State != CovUnresolved || len(b.Coverage.Unresolved) == 0 || b.Coverage.Unresolved[0] != unresolvedSubject {
		t.Fatalf("not watched throughout: %+v", b.Coverage)
	}
}

func TestBeforeEmptyStatesAreDistinct(t *testing.T) {
	srv := beforeServer(t, nil)
	lonely := recordID(t, srv, "lonely")
	var b ExplorerBefore
	// A Pod with no owner recorded: nothing it depends on could be looked at, so an empty list is not "watched and calm".
	if code := get(t, srv, "/api/v1/explorer/records/"+lonely+"/before?window=15m", &b); code != 200 || b.Empty == nil || b.Empty.Kind != CovUnresolved || len(b.Changes) != 0 {
		t.Fatalf("%d %+v", code, b.Empty)
	}
	if b.Coverage.State != CovUnresolved || !has(b.Coverage.Unresolved, unresolvedSubject) || !has(b.Coverage.Unresolved, unresolvedNothing) {
		t.Fatalf("%+v", b.Coverage)
	}
	found := false
	for _, r := range b.Related {
		found = found || r.Classification == Unresolved
	}
	if !found {
		t.Fatalf("a Pod with no recorded owner says so: %+v", b.Related)
	}
	// The other CronJob's failed run: its watch whole, nothing recorded.
	var b1 ExplorerBefore
	if code := get(t, srv, "/api/v1/explorer/records/"+recordID(t, srv, "other-fail")+"/before?window=15m", &b1); code != 200 || b1.Empty == nil || b1.Empty.Kind != "no_changes_recorded" || len(b1.Changes) != 0 {
		t.Fatalf("%d %+v %+v", code, b1.Empty, b1.Coverage)
	}
	// A shorter window misses the ConfigMap watch's gap.
	srv2 := beforeServer(t, nil)
	var b2 ExplorerBefore
	if code := get(t, srv2, "/api/v1/explorer/records/"+recordID(t, srv2, "fail")+"/before?window=15m", &b2); code != 200 {
		t.Fatal(code)
	}
	if len(b2.Changes) != 3 || b2.Coverage.State != CovWatching { // the writes 0, 5 and 14 minutes before; the ConfigMap gap was earlier
		t.Fatalf("%+v %+v", b2.Changes, b2.Coverage)
	}
	// The longest window still lies inside what was watched (from 30 h before now).
	var b3 ExplorerBefore
	if code := get(t, srv2, "/api/v1/explorer/records/"+recordID(t, srv2, "other-fail")+"/before?window=24h", &b3); code != 200 {
		t.Fatal(code)
	}
	if b3.Empty == nil || b3.Empty.Kind != "no_changes_recorded" || b3.Coverage.HeldFrom != nil {
		t.Fatalf("watched for 30 h, so a 24 h window is covered: %+v %+v", b3.Empty, b3.Coverage)
	}
}

// Records older than the age limit, or than what a size budget left, are gone though the watch's coverage stays: that
// part of the window is not covered.
func TestBeforeOutsideRetention(t *testing.T) {
	// The age limit: a day, so the 24 hours before a record three hours old reach past it.
	srv := beforeServer(t, func(c *Config) { c.Retention.Days = 1 })
	var b ExplorerBefore
	if code := get(t, srv, "/api/v1/explorer/records/"+recordID(t, srv, "other-fail")+"/before?window=24h", &b); code != 200 {
		t.Fatal(code)
	}
	if b.Coverage.State != CovOutsideRetn || b.Coverage.HeldFrom == nil || !b.Coverage.HeldFrom.Equal(base.Add(-24*time.Hour)) || b.Empty == nil || b.Empty.Kind != CovOutsideRetn {
		t.Fatalf("%+v %+v", b.Coverage, b.Empty)
	}
	// A size budget cut the Log: its oldest record held is old-spec, four hours before now; before that is not held.
	srv2 := beforeServerWith(t, nil, func(ctx context.Context, w *store.Writer) error {
		return w.SetMeta(ctx, "cap_pruned_at_log", strconv.FormatInt(base.Add(-time.Hour).UnixMilli(), 10))
	})
	var b2 ExplorerBefore
	if code := get(t, srv2, "/api/v1/explorer/records/"+recordID(t, srv2, "other-fail")+"/before?window=6h", &b2); code != 200 {
		t.Fatal(code)
	}
	if b2.Coverage.State != CovOutsideRetn || b2.Coverage.HeldFrom == nil || !b2.Coverage.HeldFrom.Equal(base.Add(-4*time.Hour)) || b2.Empty == nil || b2.Empty.Kind != CovOutsideRetn {
		t.Fatalf("%+v %+v", b2.Coverage, b2.Empty)
	}
	// A window inside what is held is not affected.
	var b3 ExplorerBefore
	if code := get(t, srv2, "/api/v1/explorer/records/"+recordID(t, srv2, "other-fail")+"/before?window=15m", &b3); code != 200 || b3.Coverage.HeldFrom != nil || b3.Coverage.State != CovWatching {
		t.Fatalf("%d %+v", code, b3.Coverage)
	}
}

// A watch turned off and never watched is reported off, not "not yet watched"; and a watch is needed in the namespace
// of what depends on it, not merely somewhere.
func TestBeforeCoverageIsByNamespaceAndSaysWhatIsOff(t *testing.T) {
	at := base.Add(-time.Hour)
	srv := setupWith(t, base, []store.Operation{operation("cj-a", "0 * * * *")}, func(ctx context.Context, w *store.Writer) error {
		for _, sc := range []string{"cronjobs/ns", "deployments/ns", "replicasets/ns", "hpas/ns", "deployments/shop", "hpas/shop"} {
			if err := w.OpenInterval(ctx, sc, store.Healthy, base.Add(-30*time.Hour)); err != nil {
				return err
			}
		}
		if err := w.SetConfigRefs(ctx, "cj-a", "CronJob", "ns", "cj-a", []store.ConfigName{{Kind: store.KindConfigMap, Name: "job-config"}}, base.Add(-30*time.Hour)); err != nil {
			return err
		}
		if err := w.UpsertWorkload(ctx, store.Workload{ID: "d-1", Kind: store.KindDeployment, Origin: store.OriginObserved, Namespace: "shop", Name: "api", FirstSeen: base.Add(-30 * time.Hour), LastSeen: base}); err != nil {
			return err
		}
		for _, e := range []store.Event{
			{ID: "f", Type: "job.failed", Namespace: "ns", Kind: "Job", ResourceUID: "r", ResourceName: "j", OperationID: "cj-a", RunID: "r", Severity: "error", Confidence: store.Derived, Summary: "f"},
			{ID: "p", Type: "pod.restarted", Namespace: "shop", Kind: "Pod", ResourceUID: "p", ResourceName: "api-1", WorkloadID: "d-1", Severity: "warning", Confidence: store.Observed, Summary: "p"},
		} {
			e.OccurredAt, e.ObservedAt = at, at
			if _, err := w.AddEvent(ctx, e); err != nil {
				return err
			}
		}
		return nil
	}, func(c *Config) { c.Namespaces = []string{"ns", "shop"} })
	scopes := func(id string) (BeforeCoverage, map[string]BeforeScope) {
		var b ExplorerBefore
		if code := get(t, srv, "/api/v1/explorer/records/"+id+"/before?window=15m", &b); code != 200 {
			t.Fatal(code)
		}
		out := map[string]BeforeScope{}
		for _, sc := range b.Coverage.Scopes {
			out[sc.Scope] = sc
		}
		return b.Coverage, out
	}
	cov, sc := scopes(recordID(t, srv, "f"))
	if c := sc["configmaps/ns"]; c.State != CovDisabled || !strings.Contains(c.Note, "turned off") || sc["cronjobs/ns"].State != CovWatching || cov.State != CovGap {
		t.Fatalf("ConfigMaps are off, not unwatched: %+v %+v", sc, cov)
	}
	cov, sc = scopes(recordID(t, srv, "p"))
	if _, ok := sc["replicasets/ns"]; ok {
		t.Fatalf("a Deployment in shop does not depend on the watch in ns: %+v", sc)
	}
	if c := sc["replicasets/shop"]; c.State != CovBeforeFirst || c.Namespace != "shop" || sc["deployments/shop"].State != CovWatching || cov.State != CovGap {
		t.Fatalf("the ReplicaSet watch in shop is missing, though one in ns is there: %+v %+v", sc, cov)
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
