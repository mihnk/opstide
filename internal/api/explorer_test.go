package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mihnk/opstide/internal/store"
)

// explorerServer holds a small history: coverage with a one-hour gap in the Events watch, a derived release entry,
// observed Pod and Node entries, and Kubernetes Events whose messages must not leave the server unasked.
func explorerServer(t *testing.T, tune func(*Config)) *httptest.Server {
	t.Helper()
	at := base.Add(-6 * time.Hour)
	if tune == nil {
		tune = func(*Config) {}
	}
	return setupWith(t, base, nil, func(ctx context.Context, w *store.Writer) error {
		for _, sc := range []string{"deployments", "replicasets", "deploypods", "pods", "jobs", "cronjobs", "statefulsets", "daemonsets", "workloadpods", "hpas"} {
			if err := w.OpenInterval(ctx, sc, store.Healthy, base.Add(-30*time.Hour)); err != nil {
				return err
			}
		}
		// Events: watched, lost for an hour, watched again.
		if err := w.OpenInterval(ctx, "events", store.Healthy, base.Add(-30*time.Hour)); err != nil {
			return err
		}
		if err := w.CloseInterval(ctx, "events", base.Add(-10*time.Hour), "watch lost"); err != nil {
			return err
		}
		if err := w.OpenInterval(ctx, "events", store.Healthy, base.Add(-9*time.Hour)); err != nil {
			return err
		}
		if err := w.UpsertWorkload(ctx, store.Workload{ID: "d-1", Kind: store.KindDeployment, Origin: store.OriginObserved, Namespace: "shop", Name: "api", FirstSeen: at, LastSeen: base}); err != nil {
			return err
		}
		rel := relRow("rx", "api", "7", 7, at, "completed", "healthy", "observed", "", snap("api:1", "", 3))
		rel.WorkloadID, rel.ReplicaSetUID = "d-1", "rs-x"
		rel.AssessmentVersion = 2
		if err := w.UpsertRelease(ctx, rel); err != nil {
			return err
		}
		add := func(id, typ, kind, uid, name string, conf store.Confidence, rel, wl string, when time.Time, facts map[string]string) error {
			_, err := w.AddEvent(ctx, store.Event{ID: id, Type: typ, Namespace: "shop", Kind: kind, ResourceUID: uid, ResourceName: name, ReleaseID: rel, WorkloadID: wl,
				OccurredAt: when, ObservedAt: when.Add(time.Second), Severity: "info", Confidence: conf, Summary: id, Facts: facts})
			return err
		}
		if err := add("a1", "release.health.degraded", "Deployment", "d-1", "api", store.Derived, "rx", "d-1", at.Add(10*time.Minute), map[string]string{"rule": "restarts", "pods": "2"}); err != nil {
			return err
		}
		if err := add("a2", "pod.created", "Pod", "p-1", "api-1", store.Observed, "", "d-1", at.Add(20*time.Minute), nil); err != nil {
			return err
		}
		if err := add("a3", "node.condition_changed", "Node", "n-a", "node-a", store.Observed, "", "", at.Add(30*time.Minute),
			map[string]string{"condition": "Ready", "from": "True", "to": "Unknown", "reason": "NodeStatusUnknown", "message": "Kubelet stopped posting node status."}); err != nil {
			return err
		}
		if err := add("a4", "pod.restarted", "Pod", "p-1", "api-1", store.Observed, "", "d-1", at.Add(40*time.Minute), nil); err != nil {
			return err
		}
		for _, e := range []store.Evidence{
			ev("k1", "su1", "BackOff", "Warning", "Pod", "api-1", "p-1", at.Add(41*time.Minute)),
			ev("k2", "su2", "Pulled", "Normal", "Pod", "api-1", "p-1", at.Add(15*time.Minute)),
			ev("k3", "su3", "NodeNotReady", "Warning", "Node", "node-a", "node-a", at.Add(31*time.Minute)),
		} {
			e.WorkloadID = "d-1"
			if e.Regarding.Kind == "Node" {
				e.WorkloadID = ""
			}
			if err := w.SaveEvidence(ctx, e); err != nil {
				return err
			}
		}
		return nil
	}, tune)
}

func post(t *testing.T, srv *httptest.Server, path, ctype string, body any) (int, []byte) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(b))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes()
}

func ids(l ExplorerList) string {
	var out []string
	for _, r := range l.Records {
		out = append(out, r.ID)
	}
	return strings.Join(out, ",")
}

func TestExplorerListShowsEachRecordOnceAndHidesOnlyRoutineAndSaysSo(t *testing.T) {
	srv := explorerServer(t, nil)
	var l ExplorerList
	if code := get(t, srv, "/api/v1/explorer?range=24h", &l); code != 200 {
		t.Fatal(code)
	}
	types := map[string]int{}
	for _, r := range l.Records {
		types[r.Type]++
		if r.Provenance == "" || r.Source == "" || r.Group == "" || r.At.IsZero() {
			t.Fatalf("every record carries provenance, source, group and time: %+v", r)
		}
		if r.Message != nil {
			t.Fatalf("a list never carries Event messages: %+v", r)
		}
	}
	// pod.created and the Normal Event are routine: hidden, counted. Nothing else is hidden.
	if l.SuppressedRoutine != 2 || types["pod.created"] != 0 || types["kubernetes.event"] != 2 || types["release.health.degraded"] != 1 || types["node.condition_changed"] != 1 || types["pod.restarted"] != 1 {
		t.Fatalf("suppressed=%d %v", l.SuppressedRoutine, types)
	}
	if l.OrderBasis == "" || l.Order != "desc" {
		t.Fatalf("the ordering must be stated: %+v", l)
	}
	var all ExplorerList
	get(t, srv, "/api/v1/explorer?range=24h&routine=1", &all)
	if len(all.Records) != len(l.Records)+2 || all.SuppressedRoutine != 0 {
		t.Fatalf("routine=1 shows what was hidden: %d vs %d", len(all.Records), len(l.Records))
	}
	for _, r := range all.Records {
		if r.Type == "evidence.event" {
			t.Fatalf("a Kubernetes Event appears once, as the Event, not also as its Log entry: %+v", r)
		}
	}
}

func TestExplorerPagingIsStable(t *testing.T) {
	srv := explorerServer(t, nil)
	var whole, p1, p2, p3 ExplorerList
	get(t, srv, "/api/v1/explorer?range=24h&routine=1", &whole)
	get(t, srv, "/api/v1/explorer?range=24h&routine=1&limit=3", &p1)
	get(t, srv, "/api/v1/explorer?range=24h&routine=1&limit=3&cursor="+p1.NextCursor, &p2)
	if p2.NextCursor != "" {
		get(t, srv, "/api/v1/explorer?range=24h&routine=1&limit=3&cursor="+p2.NextCursor, &p3)
	}
	got := strings.Trim(ids(p1)+","+ids(p2)+","+ids(p3), ",")
	if got != ids(whole) || p1.NextCursor == "" {
		t.Fatalf("pages must add up to the whole, once each:\n%s\n%s", got, ids(whole))
	}
	var asc ExplorerList
	get(t, srv, "/api/v1/explorer?range=24h&routine=1&order=asc", &asc)
	if asc.Records[0].ID != whole.Records[len(whole.Records)-1].ID {
		t.Fatal("ascending is the reverse")
	}
}

func TestExplorerFiltersAreValidatedAndCombine(t *testing.T) {
	srv := explorerServer(t, nil)
	for _, bad := range []string{
		"?range=2y", "?to=2026-11-04T00:00:00Z", "?from=nonsense", "?from=2026-11-04T10:00:00Z&to=2026-11-04T09:00:00Z", "?from=2024-01-01T00:00:00Z&to=2026-11-04T00:00:00Z",
		"?group=nonsense", "?source=elsewhere", "?provenance=guessed", "?type=Bad Type", "?limit=0", "?cursor=junk", "?order=sideways", "?q=" + strings.Repeat("x", 201), "?release=unknown-release", "?surprise=1",
	} {
		if code := get(t, srv, "/api/v1/explorer"+bad, nil); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	cases := map[string]string{
		"?group=releases": "release.health.degraded", "?provenance=derived": "release.health.degraded", "?kind=Node&routine=1": "kubernetes.event,node.condition_changed",
		"?source=kubernetes.events.k8s.io/v1": "kubernetes.event,kubernetes.event", "?reason=BackOff": "kubernetes.event", "?workload=d-1&group=pods&routine=1": "pod.restarted,pod.created",
		"?type=pod.restarted": "pod.restarted", "?q=node-a": "kubernetes.event,node.condition_changed", "?namespace=elsewhere": "",
	}
	for q, want := range cases {
		var l ExplorerList
		if code := get(t, srv, "/api/v1/explorer"+q, &l); code != 200 {
			t.Errorf("%s: %d", q, code)
			continue
		}
		var got []string
		for _, r := range l.Records {
			got = append(got, r.Type)
		}
		if !sameSet(got, strings.Split(want, ",")) {
			t.Errorf("%s: %v, want %s", q, got, want)
		}
	}
	var rel ExplorerList
	get(t, srv, "/api/v1/explorer?release=rx&routine=1", &rel)
	if len(rel.Records) == 0 {
		t.Fatal("a release filter finds the release's entries and the Events about its workload")
	}
}

func sameSet(a, b []string) bool {
	if len(a) == 1 && a[0] == "" {
		a = nil
	}
	if len(b) == 1 && b[0] == "" {
		b = nil
	}
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, x := range a {
		m[x]++
	}
	for _, x := range b {
		m[x]--
	}
	for _, n := range m {
		if n != 0 {
			return false
		}
	}
	return true
}

func TestExplorerFilterChoicesCount(t *testing.T) {
	srv := explorerServer(t, nil)
	var f ExplorerFilters
	if code := get(t, srv, "/api/v1/explorer/filters?range=24h", &f); code != 200 {
		t.Fatal(code)
	}
	if f.Total != 7 || len(f.QuickRanges) != 5 || len(f.Sources) != 5 {
		t.Fatalf("%+v", f)
	}
	counts := map[string]int{}
	for _, g := range f.Groups {
		counts[g.Value] = g.Count
	}
	if counts["events"] != 3 || counts["releases"] != 1 || counts["pods"] != 2 || counts["nodes"] != 1 {
		t.Fatalf("%v", counts)
	}
}

func TestExplorerDetailShowsDerivationRelationshipsAndLabelsLiveStateAsCurrent(t *testing.T) {
	srv := explorerServer(t, nil)
	var l ExplorerList
	get(t, srv, "/api/v1/explorer?range=24h&routine=1", &l)
	byType := map[string]string{}
	for _, r := range l.Records {
		byType[r.Type+"/"+r.Reason] = r.ID
	}
	var d ExplorerDetail
	if code := get(t, srv, "/api/v1/explorer/records/"+byType["release.health.degraded/"], &d); code != 200 {
		t.Fatal(code)
	}
	if d.Provenance.Class != "DERIVED" || d.Derivation == nil || d.Derivation.RuleID != "release.health.degraded" || d.Derivation.RuleVersion != "2" || d.Derivation.Inputs["rule"] != "restarts" || len(d.Derivation.EvidenceRefs) == 0 {
		t.Fatalf("a derived fact names its rule, version, inputs and references: %+v", d.Derivation)
	}
	if d.Original.Kind != "log_entry" || d.Original.Entry == nil || d.Observation.TimeBasis != "occurredAt" || d.Observation.ObservedAt.IsZero() || d.Coverage.State != CovWatching {
		t.Fatalf("%+v %+v", d.Original, d.Coverage)
	}
	if d.Current == nil || !strings.Contains(d.Current.Label, "not the state at this record's time") || d.Current.Path != "/workloads/d-1" {
		t.Fatalf("live state is labelled as current: %+v", d.Current)
	}
	var rels = map[string]Relationship{}
	for _, r := range d.Relationships {
		rels[r.Kind] = r
	}
	if rels["Release"].Classification != Direct || rels["Release"].Resolved == nil || !*rels["Release"].Resolved || rels["Workload"].Classification != Direct || rels["Workload"].Path != "/workloads/d-1" {
		t.Fatalf("%+v", d.Relationships)
	}
	// A Kubernetes Event: message shown in the drilldown, link to the Workload by ownership, Node left unresolved.
	var k ExplorerDetail
	get(t, srv, "/api/v1/explorer/records/"+byType["kubernetes.event/BackOff"], &k)
	if k.Record.Message == nil || *k.Record.Message == "" || k.Original.Event == nil || k.Provenance.Class != "OBSERVED" || k.Derivation != nil {
		t.Fatalf("%+v", k)
	}
	var n ExplorerDetail
	get(t, srv, "/api/v1/explorer/records/"+byType["kubernetes.event/NodeNotReady"], &n)
	if len(n.Relationships) != 1 || n.Relationships[0].Kind != "Node" || n.Relationships[0].Classification != Unresolved {
		t.Fatalf("a Node Event with no recorded Node says it is unresolved: %+v", n.Relationships)
	}
	for _, bad := range []string{"nope", "e.x", "k."} {
		if code := get(t, srv, "/api/v1/explorer/records/"+bad, nil); code != 400 && code != 404 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	if code := get(t, srv, "/api/v1/explorer/records/e.999999999999", nil); code != 404 {
		t.Fatalf("a pruned or unknown record is 404: %d", code)
	}
}

func TestExplorerContextShowsGapsAndTheFiveEmptyStatesAreDistinct(t *testing.T) {
	srv := explorerServer(t, nil)
	var c ExplorerContext
	if code := get(t, srv, "/api/v1/explorer/context?range=24h", &c); code != 200 {
		t.Fatal(code)
	}
	states := map[string]ExplorerScope{}
	for _, s := range c.Scopes {
		states[s.Scope] = s
	}
	if states["events"].State != CovGap || len(states["events"].Gaps) != 1 || states["deployments"].State != CovWatching || states["nodes"].State != CovDisabled {
		t.Fatalf("%+v", states)
	}
	if states["nodes"].Note == "" || c.FirstObservedAt == nil || c.Retention.BoundaryAt == nil {
		t.Fatalf("disabled Nodes say so; first observation and the retention boundary are given: %+v", c)
	}
	empty := func(q string) *ExplorerEmpty {
		var l ExplorerList
		if code := get(t, srv, "/api/v1/explorer"+q, &l); code != 200 {
			t.Fatalf("%s: %d", q, code)
		}
		return l.Empty
	}
	if e := empty("?range=24h&namespace=elsewhere&group=releases"); e == nil || e.Kind != CovNoMatches {
		t.Fatalf("watched throughout, nothing matches: %+v", e)
	}
	if e := empty("?range=24h&namespace=elsewhere&group=events"); e == nil || e.Kind != CovGap {
		t.Fatalf("a gap in the Events watch: %+v", e)
	}
	if e := empty("?range=24h&group=nodes&provenance=unknown"); e == nil || e.Kind != CovSourceUnavil {
		t.Fatalf("Node observation is off: %+v", e)
	}
	if e := empty("?from=2026-11-04T00:00:00Z&to=2026-11-04T01:00:00Z&namespace=x"); e == nil || e.Kind != CovOutsideRetn {
		t.Fatalf("%+v", e)
	}
	old := base.AddDate(0, 0, -60)
	if e := empty("?from=" + old.Format(time.RFC3339) + "&to=" + old.Add(time.Hour).Format(time.RFC3339)); e == nil || e.Kind != CovOutsideRetn {
		t.Fatalf("older than retention: %+v", e)
	}
	before := base.Add(-35 * time.Hour)
	if e := empty("?from=" + before.Format(time.RFC3339) + "&to=" + before.Add(time.Hour).Format(time.RFC3339)); e == nil || e.Kind != CovBeforeFirst {
		t.Fatalf("before OpsTide first watched: %+v", e)
	}
}

func TestExplorerStateNeverReconstructsWhatWasNotRecorded(t *testing.T) {
	srv := explorerServer(t, nil)
	at := base.Add(-6 * time.Hour)
	st := func(q string) ExplorerState {
		var s ExplorerState
		if code := get(t, srv, "/api/v1/explorer/state?"+q, &s); code != 200 {
			t.Fatalf("%s: %d", q, code)
		}
		return s
	}
	s := st("uid=d-1&kind=Deployment&at=" + at.Add(time.Hour).Format(time.RFC3339))
	if s.Basis != StateTransition || s.Transition == nil || s.Transition.Type != "release.health.degraded" || !strings.Contains(s.Statement, "latest known change") {
		t.Fatalf("%+v", s)
	}
	s = st("uid=d-1&kind=Deployment&at=" + at.Add(10*time.Minute).Format(time.RFC3339))
	if s.Basis != StateObservedAt {
		t.Fatalf("at the exact time of a recorded change: %+v", s)
	}
	s = st("uid=d-1&kind=Deployment&at=" + base.Add(-40*time.Hour).Format(time.RFC3339))
	if s.Basis != StateUnknown || !strings.Contains(s.Statement, "Not enough recorded evidence to determine this state.") {
		t.Fatalf("before OpsTide watched: %+v", s)
	}
	s = st("uid=nothing-recorded&kind=Deployment&at=" + base.Add(-time.Hour).Format(time.RFC3339))
	if s.Basis != StateUnknown || s.Transition != nil {
		t.Fatalf("%+v", s)
	}
	s = st("uid=n-a&kind=Node&at=" + base.Add(-time.Hour).Format(time.RFC3339))
	if s.Basis != StateUnknown || !strings.Contains(s.Coverage.Note, "Node observation is off") {
		t.Fatalf("with Node observation off the state of a Node is not known: %+v", s)
	}
	for _, bad := range []string{"uid=&at=" + base.Format(time.RFC3339), "uid=x", "uid=x&at=" + base.Add(24*time.Hour).Format(time.RFC3339)} {
		if code := get(t, srv, "/api/v1/explorer/state?"+bad, nil); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
}

func TestExplorerExportIsVersionedOrderedBoundedAndLeavesMessagesOutUnlessAsked(t *testing.T) {
	srv := explorerServer(t, nil)
	var doc ExportDoc
	resp, err := http.Get(srv.URL + "/api/v1/explorer/export?range=24h&routine=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") {
		t.Fatalf("an export is a download: %q", cd)
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.Schema != "opstide.explorer.export" || doc.Version != "1.0" || doc.Mode != "filtered" || doc.Coverage == nil || doc.Filters == nil || doc.Truncation != nil || len(doc.Records) != 7 {
		t.Fatalf("%+v", doc)
	}
	for i := 1; i < len(doc.Records); i++ {
		a, b := doc.Records[i-1], doc.Records[i]
		if b.RecordTime.Before(a.RecordTime) || (b.RecordTime.Equal(a.RecordTime) && b.ID < a.ID) {
			t.Fatalf("oldest first, ties by id: %s then %s", a.ID, b.ID)
		}
	}
	raw := mustGetBody(t, srv, "/api/v1/explorer/export?range=24h&routine=1")
	for _, secret := range []string{"BackOff message", "Kubelet stopped posting node status."} {
		if strings.Contains(raw, secret) {
			t.Fatalf("messages must not be exported unless asked for: found %q", secret)
		}
	}
	if !strings.Contains(raw, `"rule"`) || !strings.Contains(raw, `"classification"`) {
		t.Fatal("rule references and relationship classifications are kept")
	}
	with := mustGetBody(t, srv, "/api/v1/explorer/export?range=24h&routine=1&messages=1")
	if !strings.Contains(with, "BackOff message") || !strings.Contains(with, "Kubelet stopped posting node status.") || !strings.Contains(with, `"includeMessages": true`) {
		t.Fatal("messages=1 includes them and says so")
	}
}

func mustGetBody(t *testing.T, srv *httptest.Server, path string) string {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return buf.String()
}

func TestExplorerSelectedExportAndLookup(t *testing.T) {
	srv := explorerServer(t, nil)
	var l ExplorerList
	get(t, srv, "/api/v1/explorer?range=24h&routine=1", &l)
	pick := []string{l.Records[0].ID, l.Records[2].ID, l.Records[0].ID, "e.000000099999"}
	code, body := post(t, srv, "/api/v1/explorer/export", "application/json", map[string]any{"ids": pick})
	if code != 200 {
		t.Fatal(code, string(body))
	}
	var doc ExportDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Mode != "selected" || len(doc.Records) != 2 || len(doc.Missing) != 1 || doc.Missing[0] != "e.000000099999" || doc.Options.IncludeMessages {
		t.Fatalf("a duplicate is one record, an unknown id is listed as missing: %+v", doc)
	}
	var lk ExplorerLookup
	code, body = post(t, srv, "/api/v1/explorer/lookup", "application/json", map[string]any{"ids": pick})
	if err := json.Unmarshal(body, &lk); code != 200 || err != nil || len(lk.Records) != 2 || len(lk.Missing) != 1 {
		t.Fatalf("%d %v %+v", code, err, lk)
	}
	if code, _ := post(t, srv, "/api/v1/explorer/export", "text/plain", map[string]any{"ids": pick}); code != 400 {
		t.Fatalf("a body that is not declared JSON is refused: %d", code)
	}
	if code, _ := post(t, srv, "/api/v1/explorer/lookup", "application/json", map[string]any{"ids": []string{}}); code != 400 {
		t.Fatal(code)
	}
	if code, _ := post(t, srv, "/api/v1/explorer/lookup", "application/json", map[string]any{"ids": []string{"not an id"}}); code != 400 {
		t.Fatal(code)
	}
	tooMany := make([]string, maxSelectedIDs+1)
	for i := range tooMany {
		tooMany[i] = "e.000000000001"
	}
	if code, _ := post(t, srv, "/api/v1/explorer/lookup", "application/json", map[string]any{"ids": tooMany}); code != 400 {
		t.Fatal(code)
	}
	if code, _ := post(t, srv, "/api/v1/explorer/lookup", "application/json", map[string]any{"ids": pick, "extra": 1}); code != 400 {
		t.Fatal("an unknown field is refused")
	}
}

// A range that starts before OpsTide was watching is never answered "watching throughout", also when nothing matches.
func TestExplorerEmptyResultIsNotCalledWholeWhenPartOfTheRangeIsNotCovered(t *testing.T) {
	srv := explorerServer(t, nil)
	var l ExplorerList
	// 30 h back: OpsTide first watched at base-30h, so 0-6 h of this is covered only from that moment; 40 h reaches before it.
	get(t, srv, "/api/v1/explorer?from="+base.Add(-40*time.Hour).Format(time.RFC3339)+"&to="+base.Format(time.RFC3339)+"&namespace=elsewhere&group=releases", &l)
	if l.Empty == nil || l.Empty.Kind == CovNoMatches {
		t.Fatalf("part of the range is before OpsTide watched: %+v", l.Empty)
	}
	// A type or source filter that implies Events depends on the Events watch (which has a gap in this fixture).
	var g ExplorerList
	get(t, srv, "/api/v1/explorer?range=24h&namespace=elsewhere&type=kubernetes.event", &g)
	if g.Empty == nil || g.Empty.Kind != CovGap {
		t.Fatalf("%+v", g.Empty)
	}
}

// The count of hidden routine records follows the type filter: a filter that names no routine type hides none.
func TestExplorerRoutineCountFollowsTheTypeFilter(t *testing.T) {
	srv := explorerServer(t, nil)
	var l ExplorerList
	get(t, srv, "/api/v1/explorer?range=24h&type=release.health.degraded", &l)
	if l.SuppressedRoutine != 0 || len(l.Records) != 1 {
		t.Fatalf("%d %d", l.SuppressedRoutine, len(l.Records))
	}
	get(t, srv, "/api/v1/explorer?range=24h&type=pod.created", &l)
	if l.SuppressedRoutine != 1 {
		t.Fatalf("asking for a routine type hides it and counts it: %d", l.SuppressedRoutine)
	}
}

// A record about a Pod leads on to the revision it belongs to: the release of a Deployment's Pod, the ControllerRevision a
// StatefulSet Pod's label names; a revision OpsTide does not hold is said to be not recorded.
func TestExplorerDetailLeadsFromAPodToItsRevision(t *testing.T) {
	at := base.Add(-2 * time.Hour)
	srv := setupWith(t, base, nil, func(ctx context.Context, w *store.Writer) error {
		for _, wl := range []store.Workload{{ID: "sts-1", Kind: store.KindStatefulSet, Namespace: "shop", Name: "web", FirstSeen: at, LastSeen: at}, {ID: "d-1", Kind: store.KindDeployment, Namespace: "shop", Name: "api", FirstSeen: at, LastSeen: at}} {
			if err := w.UpsertWorkload(ctx, wl); err != nil {
				return err
			}
		}
		if _, err := w.AddRevision(ctx, store.WorkloadRevision{ID: "wr1", WorkloadID: "sts-1", Kind: store.KindStatefulSet, Name: "web-aaa", Hash: "aaa", Number: 1, ControllerRevisionUID: "u1", ObservedAt: at, Origin: store.OriginObserved, Fingerprint: "f", Snapshot: "{}"}); err != nil {
			return err
		}
		rel := relRow("rx", "api", "7", 7, at, "completed", "healthy", "observed", "", snap("api:1", "", 3))
		rel.WorkloadID = "d-1"
		if err := w.UpsertRelease(ctx, rel); err != nil {
			return err
		}
		pod := func(uid, kind, wl, rev, rel string) store.PodRecord {
			return store.PodRecord{UID: uid, Namespace: "shop", Name: uid, Kind: kind, WorkloadID: wl, Revision: rev, ReleaseID: rel, CreatedAt: at, State: "{}"}
		}
		for _, p := range []store.PodRecord{pod("p-sts", store.KindStatefulSet, "sts-1", "web-aaa", ""), pod("p-gone", store.KindStatefulSet, "sts-1", "web-zzz", ""), pod("p-dep", store.KindDeployment, "d-1", "", "rx")} {
			if err := w.UpsertPod(ctx, p); err != nil {
				return err
			}
			e := ev("k-"+p.UID, "su-"+p.UID, "BackOff", "Warning", "Pod", p.Name, p.UID, at.Add(time.Minute))
			if err := w.SaveEvidence(ctx, e); err != nil {
				return err
			}
		}
		return nil
	}, func(*Config) {})
	rel := func(pod string) Relationship {
		var d ExplorerDetail
		var l ExplorerList
		get(t, srv, "/api/v1/explorer?range=24h&routine=1&uid="+pod, &l)
		if len(l.Records) != 1 {
			t.Fatalf("%s: %d records", pod, len(l.Records))
		}
		get(t, srv, "/api/v1/explorer/records/"+l.Records[0].ID, &d)
		for _, r := range d.Relationships {
			if r.Kind == "Revision" || r.Kind == "Release" {
				return r
			}
		}
		t.Fatalf("%s: no revision relationship in %+v", pod, d.Relationships)
		return Relationship{}
	}
	if r := rel("p-sts"); r.Kind != "Revision" || r.Name != "web-aaa" || r.Resolved == nil || !*r.Resolved || r.Path != "/workloads/sts-1?tab=revisions" {
		t.Fatalf("%+v", r)
	}
	if r := rel("p-gone"); r.Resolved == nil || *r.Resolved || r.Path != "" || r.Note == "" {
		t.Fatalf("a revision OpsTide does not hold is said so: %+v", r)
	}
	if r := rel("p-dep"); r.Kind != "Release" || r.Path != "/releases/rx" || r.Classification != Ownership {
		t.Fatalf("%+v", r)
	}
}
