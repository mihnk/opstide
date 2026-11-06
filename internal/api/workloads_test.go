package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mihnk/opstide/internal/release"
	"github.com/mihnk/opstide/internal/store"
	"github.com/mihnk/opstide/internal/workload"
)

func workloadServer(t *testing.T, observeNodes bool) *httptest.Server {
	t.Helper()
	at := base.Add(-3 * time.Hour)
	part := 2
	pod := func(uid, name, kind, wl, rev, node string, ord *int, ready bool, ended time.Time) store.PodRecord {
		st := release.PodState{Node: node, Ready: ready, Phase: "Running"}
		if !ended.IsZero() {
			st.Terminated, st.TermAt = true, ended
		}
		return store.PodRecord{UID: uid, Namespace: "shop", Name: name, Kind: kind, WorkloadID: wl, Revision: rev, Node: node, Ordinal: ord, CreatedAt: at, TerminatedAt: ended, State: release.EncodePod(st)}
	}
	zero, one := 0, 1
	return setupWith(t, base, nil, func(ctx context.Context, w *store.Writer) error {
		mk := func(id, kind, ns, name string, last time.Time) error {
			return w.UpsertWorkload(ctx, store.Workload{ID: id, Kind: kind, Origin: store.OriginObserved, Namespace: ns, Name: name, FirstSeen: at, LastSeen: last})
		}
		for _, x := range []struct{ id, kind, ns, name string }{
			{"d-1", store.KindDeployment, "shop", "api"}, {"sts-1", store.KindStatefulSet, "shop", "web"}, {"ds-1", store.KindDaemonSet, "shop", "agent"}, {"sts-2", store.KindStatefulSet, "other", "db"},
		} {
			if err := mk(x.id, x.kind, x.ns, x.name, at); err != nil {
				return err
			}
		}
		states := []store.WorkloadState{
			{WorkloadID: "d-1", Kind: "Deployment", Desired: 3, Ready: 3, Available: 3, Updated: 3, Revision: "7", UpdateRevision: "7", Strategy: "RollingUpdate", Rollout: "completed", Conditions: "[]", LastChangeAt: at.Add(10 * time.Minute)},
			{WorkloadID: "sts-1", Kind: "StatefulSet", Desired: 5, Ready: 5, Available: 5, Updated: 3, Current: 2, Revision: "web-aaa", UpdateRevision: "web-bbb", Strategy: "RollingUpdate", Partition: &part,
				PodManagement: "OrderedReady", Rollout: workload.Partitioned, RolloutNote: "partition boundary", Conditions: "[]", LastChangeAt: at.Add(2 * time.Hour)},
			{WorkloadID: "ds-1", Kind: "DaemonSet", Desired: 3, Ready: 3, Available: 3, Updated: 3, Current: 3, Misscheduled: 1, Revision: "agent-r2", UpdateRevision: "agent-r2", Strategy: "RollingUpdate", MaxUnavailable: "1",
				Rollout: workload.Completed, Conditions: "[]", LastChangeAt: at.Add(time.Hour)},
			{WorkloadID: "sts-2", Kind: "StatefulSet", Desired: 1, Ready: 1, Available: 1, Updated: 1, Revision: "db-x", UpdateRevision: "db-x", Strategy: "OnDelete", Rollout: workload.Completed, Conditions: "[]", LastChangeAt: at},
		}
		for _, st := range states {
			if err := w.SaveWorkloadState(ctx, st); err != nil {
				return err
			}
		}
		rev := func(id, wl, kind, name, hash string, n int64, snap string, obs time.Time) store.WorkloadRevision {
			fp := ""
			if s, err := release.DecodeSnapshot(snap); err == nil {
				fp = s.Fingerprint()
			}
			return store.WorkloadRevision{ID: id, WorkloadID: wl, Kind: kind, Name: name, Hash: hash, Number: n, ControllerRevisionUID: "u-" + id, CreatedAt: obs, ObservedAt: obs, Origin: store.OriginObserved, Fingerprint: fp, Snapshot: snap}
		}
		for _, rv := range []store.WorkloadRevision{
			rev("wr1", "sts-1", "StatefulSet", "web-aaa", "aaa", 1, snap("web:1", "256Mi", 0), at),
			rev("wr2", "sts-1", "StatefulSet", "web-bbb", "bbb", 2, snap("web:2", "512Mi", 0), at.Add(time.Hour)),
			rev("wr3", "ds-1", "DaemonSet", "agent-r1", "r1", 1, snap("agent:1", "", 0), at),
			rev("wr4", "ds-1", "DaemonSet", "agent-r2", "r2", 2, "{}", at.Add(time.Hour)), // a template that could not be read
		} {
			if _, err := w.AddRevision(ctx, rv); err != nil {
				return err
			}
		}
		for _, p := range []store.PodRecord{
			pod("p0", "web-0", "StatefulSet", "sts-1", "web-bbb", "node-a", &zero, true, time.Time{}),
			pod("p1", "web-1", "StatefulSet", "sts-1", "web-aaa", "node-b", &one, true, time.Time{}),
			pod("p9", "web-9", "StatefulSet", "sts-1", "web-aaa", "node-b", nil, false, at.Add(time.Hour)),
			pod("dp1", "agent-x", "DaemonSet", "ds-1", "r2", "node-a", nil, true, time.Time{}),
			pod("dp2", "agent-y", "DaemonSet", "ds-1", "r1", "node-b", nil, false, time.Time{}),
		} {
			if err := w.UpsertPod(ctx, p); err != nil {
				return err
			}
		}
		for _, n := range []store.Node{
			{UID: "n-a", Name: "node-a", FirstSeenAt: at, LastSeenAt: at, CreatedAt: at.Add(-time.Hour), Origin: store.OriginAdopted, Conditions: `[{"type":"Ready","status":"True","reason":"KubeletReady","transition":"2026-11-04T08:00:00Z","observed":"2026-11-04T09:00:00Z"}]`},
			{UID: "n-b", Name: "node-b", FirstSeenAt: at, LastSeenAt: at, CreatedAt: at.Add(-time.Hour), Origin: store.OriginAdopted, Unschedulable: true, Conditions: "[]"},
			{UID: "n-c", Name: "node-c", FirstSeenAt: at, LastSeenAt: at, CreatedAt: at.Add(-time.Hour), Origin: store.OriginAdopted, Conditions: "[]"},
			{UID: "n-old", Name: "node-old", FirstSeenAt: at, LastSeenAt: at, Origin: store.OriginObserved, DeletedAt: at.Add(time.Hour), Conditions: "[]"},
		} {
			if err := w.SaveNode(ctx, n); err != nil {
				return err
			}
		}
		ev := func(id, uid, reason, typ, kind, name, regUID string, wl, node string, when time.Time) store.Evidence {
			e := ev(id, uid, reason, typ, kind, name, regUID, when)
			e.WorkloadID, e.NodeID = wl, node
			return e
		}
		for _, e := range []store.Evidence{
			ev("sev1", "su1", "Unhealthy", "Warning", "Pod", "web-0", "p0", "sts-1", "", at.Add(90*time.Minute)),
			ev("sev2", "su2", "NodeNotReady", "Warning", "Node", "node-a", "node-a", "", "n-a", at.Add(95*time.Minute)),
		} {
			if err := w.SaveEvidence(ctx, e); err != nil {
				return err
			}
		}
		add := func(id, typ, kind, uid, name, wl string, when time.Time) error {
			_, err := w.AddEvent(ctx, store.Event{ID: id, Type: typ, Namespace: "shop", Kind: kind, ResourceUID: uid, ResourceName: name, WorkloadID: wl, ObservedAt: when, Severity: "info", Confidence: store.Observed, Summary: id})
			return err
		}
		for _, x := range []struct {
			id, typ, kind, uid, name, wl string
			at                           time.Time
		}{
			{"h1", "statefulset.update_started", "StatefulSet", "sts-1", "web", "sts-1", at.Add(time.Hour)},
			{"h2", "pod.created", "Pod", "p0", "web-0", "sts-1", at.Add(time.Hour)},
			{"h3", "pod.restarted", "Pod", "p0", "web-0", "sts-1", at.Add(80 * time.Minute)},
			{"h4", "statefulset.counts_changed", "StatefulSet", "sts-1", "web", "sts-1", at.Add(85 * time.Minute)},
			{"n1", "node.condition_changed", "Node", "n-a", "node-a", "", at.Add(95 * time.Minute)},
			{"o1", "daemonset.update_started", "DaemonSet", "ds-1", "agent", "ds-1", at.Add(time.Hour)},
		} {
			if err := add(x.id, x.typ, x.kind, x.uid, x.name, x.wl, x.at); err != nil {
				return err
			}
		}
		rel := relRow("rd7", "api", "7", 7, at, "completed", "healthy", "observed", "", snap("api:1", "", 3))
		rel.WorkloadID = "d-1"
		if err := w.UpsertRelease(ctx, rel); err != nil {
			return err
		}
		run := store.Run{ID: "job-1", Namespace: "shop", JobName: "j-1", CreatedAt: at, ObservedAt: at, Status: store.RunFailed}
		if err := w.UpsertRun(ctx, run); err != nil {
			return err
		}
		jobEv := ev("rev1", "ru1", "BackOff", "Warning", "Pod", "j-1-abc", "jp1", "", "", at.Add(time.Minute))
		jobEv.RunID, jobEv.LinkVia = "job-1", "Pod ownerReference to Job (recorded)"
		return w.SaveEvidence(ctx, jobEv)
	}, func(c *Config) { c.ObserveNodes = observeNodes })
}

func TestWorkloadListCoversAllKindsWithFiltersAndPaging(t *testing.T) {
	srv := workloadServer(t, true)
	var l WorkloadList
	if code := get(t, srv, "/api/v1/workloads", &l); code != 200 {
		t.Fatal(code)
	}
	var ids []string
	byID := map[string]WorkloadView{}
	for _, w := range l.Workloads {
		ids = append(ids, w.ID)
		byID[w.ID] = w
	}
	// Most recently changed first; no score orders them.
	if strings.Join(ids, ",") != "sts-1,ds-1,d-1,sts-2" {
		t.Fatalf("%v", ids)
	}
	st := byID["sts-1"]
	if st.Kind != "StatefulSet" || st.State == nil || st.State.Rollout != "partitioned" || st.State.Partition == nil || *st.State.Partition != 2 || st.State.Revision != "web-aaa" || st.State.UpdateRevision != "web-bbb" ||
		st.Status != "live" {
		t.Fatalf("%+v", st)
	}
	if st.Evidence.LatestWarningReason != "Unhealthy" {
		t.Fatalf("the newest Warning's reason, as Kubernetes wrote it: %+v", st.Evidence)
	}
	if ds := byID["ds-1"]; ds.State.Misscheduled != 1 || ds.State.MaxUnavailable != "1" {
		t.Fatalf("%+v", ds.State)
	}
	for q, want := range map[string]string{
		"?kind=StatefulSet": "sts-1,sts-2", "?kind=DaemonSet": "ds-1", "?kind=Deployment": "d-1", "?namespace=other": "sts-2", "?q=ag": "ds-1", "?status=deleted": "", "?status=live&kind=StatefulSet&namespace=shop": "sts-1",
		"?within=30m": "",
	} {
		var f WorkloadList
		get(t, srv, "/api/v1/workloads"+q, &f)
		var got []string
		for _, w := range f.Workloads {
			got = append(got, w.ID)
		}
		if strings.Join(got, ",") != want {
			t.Errorf("%s: %v, want %q", q, got, want)
		}
	}
	var p1, p2 WorkloadList
	get(t, srv, "/api/v1/workloads?limit=3", &p1)
	get(t, srv, "/api/v1/workloads?limit=3&cursor="+p1.NextCursor, &p2)
	if len(p1.Workloads) != 3 || p1.NextCursor == "" || len(p2.Workloads) != 1 || p2.NextCursor != "" || p2.Workloads[0].ID != "sts-2" {
		t.Fatalf("paging: %d %d", len(p1.Workloads), len(p2.Workloads))
	}
	for _, bad := range []string{"?kind=Job", "?status=gone", "?limit=0", "?cursor=x", "?within=forever"} {
		if code := get(t, srv, "/api/v1/workloads"+bad, nil); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
}

func TestWorkloadDetailHistoryAndEvidence(t *testing.T) {
	srv := workloadServer(t, true)
	var d WorkloadDetail
	if code := get(t, srv, "/api/v1/workloads/sts-1", &d); code != 200 || d.Workload.Kind != "StatefulSet" || len(d.Workload.Sections) == 0 {
		t.Fatalf("%d %+v", code, d)
	}
	if d.Workload.Evidence.LatestWarningReason != "Unhealthy" {
		t.Fatalf("the detail carries the recent evidence status too: %+v", d.Workload.Evidence)
	}
	var dep WorkloadDetail
	get(t, srv, "/api/v1/workloads/d-1", &dep)
	if dep.Release == nil || dep.Release.ID != "rd7" {
		t.Fatalf("a Deployment links to its release: %+v", dep.Release)
	}
	if code := get(t, srv, "/api/v1/workloads/nope", nil); code != 404 {
		t.Fatal(code)
	}
	var h Entries
	get(t, srv, "/api/v1/workloads/sts-1/history", &h)
	types := map[string]bool{}
	for _, e := range h.Entries {
		types[e.Type] = true
	}
	if !types["statefulset.update_started"] || !types["pod.restarted"] || !types["statefulset.counts_changed"] || types["pod.created"] || types["daemonset.update_started"] {
		t.Fatalf("history leaves out routine Pod entries and other workloads' entries: %v", types)
	}
	get(t, srv, "/api/v1/workloads/sts-1/history?pods=all", &h)
	got := false
	for _, e := range h.Entries {
		got = got || e.Type == "pod.created"
	}
	if !got {
		t.Fatal("pods=all must include the routine entries")
	}
	var ev WorkloadEvidence
	get(t, srv, "/api/v1/workloads/sts-1/evidence", &ev)
	if len(ev.Records) != 1 || ev.Records[0].Reason != "Unhealthy" || ev.Records[0].Certainty != "OBSERVED" {
		t.Fatalf("%+v", ev.Records)
	}
}

func TestWorkloadRevisionsCompareOnlyWhatCanBeCompared(t *testing.T) {
	srv := workloadServer(t, true)
	var sts WorkloadRevisions
	get(t, srv, "/api/v1/workloads/sts-1/revisions", &sts)
	if len(sts.Revisions) != 2 || sts.Revisions[0].Name != "web-bbb" || !sts.Revisions[0].IsUpdate || sts.Revisions[0].IsCurrent || !sts.Revisions[1].IsCurrent {
		t.Fatalf("%+v", sts.Revisions)
	}
	c := sts.Revisions[0].Comparison
	if !c.Available || len(c.Changes) == 0 {
		t.Fatalf("%+v", c)
	}
	var fields []string
	for _, ch := range c.Changes {
		fields = append(fields, ch.Field)
	}
	if !strings.Contains(strings.Join(fields, ","), "image") {
		t.Fatalf("%v", fields)
	}
	if first := sts.Revisions[1].Comparison; first.Available || !strings.HasPrefix(first.Reason, "Comparison unavailable") {
		t.Fatalf("the first revision has nothing before it: %+v", first)
	}
	if !strings.Contains(strings.Join(sts.Notes, " "), "partition boundary") {
		t.Fatalf("a partitioned rollout is explained, not called a failure: %v", sts.Notes)
	}
	var ds WorkloadRevisions
	get(t, srv, "/api/v1/workloads/ds-1/revisions", &ds)
	if c := ds.Revisions[0].Comparison; c.Available || !strings.Contains(c.Reason, "could not be read") {
		t.Fatalf("an unreadable template is said to be unreadable: %+v", c)
	}
	var dep WorkloadRevisions
	get(t, srv, "/api/v1/workloads/d-1/revisions", &dep)
	if len(dep.Revisions) != 1 || dep.Revisions[0].Source != "Release" || dep.Revisions[0].ReleaseID != "rd7" || !strings.Contains(dep.Revisions[0].Comparison.Reason, "Comparison unavailable") {
		t.Fatalf("%+v", dep.Revisions)
	}
}

func TestWorkloadPodsShowOrdinalRevisionAndNodePlacement(t *testing.T) {
	srv := workloadServer(t, true)
	var p WorkloadPods
	get(t, srv, "/api/v1/workloads/sts-1/pods", &p)
	if len(p.Pods) != 3 || p.Pods[0].Name != "web-0" || p.Pods[0].Ordinal == nil || *p.Pods[0].Ordinal != 0 || p.Pods[0].Revision != "web-bbb" || p.Pods[0].RevisionRole != "update" || p.Pods[0].NodeID != "n-a" {
		t.Fatalf("%+v", p.Pods)
	}
	if p.Pods[1].RevisionRole != "current" || p.Pods[1].Node != "node-b" {
		t.Fatalf("%+v", p.Pods[1])
	}
	var d WorkloadPods
	get(t, srv, "/api/v1/workloads/ds-1/pods", &d)
	if d.NodesWithoutPod == nil {
		t.Fatalf("Nodes are observed, so nodes without a Pod can be listed: %+v", d)
	}
	var gaps []string
	for _, g := range *d.NodesWithoutPod {
		gaps = append(gaps, g.Name)
	}
	if strings.Join(gaps, ",") != "node-c" {
		t.Fatalf("node-b has a Pod recorded (not ready) and node-old was removed: %v", gaps)
	}
	notes := strings.Join(d.Notes, " ")
	if !strings.Contains(notes, "not established") || !strings.Contains(notes, "1 misscheduled") || strings.Contains(strings.ToLower(notes), "because") {
		t.Fatalf("%s", notes)
	}
	// Without Node observation nothing is claimed about Nodes.
	off := workloadServer(t, false)
	var o WorkloadPods
	get(t, off, "/api/v1/workloads/ds-1/pods", &o)
	if o.NodesWithoutPod != nil || !strings.Contains(strings.Join(o.Notes, " "), "Nodes are not observed") {
		t.Fatalf("%+v", o)
	}
}

func TestNodesAreContextNotInventory(t *testing.T) {
	srv := workloadServer(t, true)
	var l NodesList
	get(t, srv, "/api/v1/nodes", &l)
	if !l.Enabled || len(l.Nodes) != 4 || l.Nodes[0].Name != "node-a" || len(l.Nodes[0].Conditions) != 1 || l.Nodes[0].Conditions[0].Transition == nil {
		t.Fatalf("%+v", l)
	}
	if !l.Nodes[1].Unschedulable || l.Nodes[3].Status != "removed" {
		t.Fatalf("%+v", l.Nodes)
	}
	var live NodesList
	get(t, srv, "/api/v1/nodes?status=live&q=node-", &live)
	if len(live.Nodes) != 3 {
		t.Fatalf("%d", len(live.Nodes))
	}
	if code := get(t, srv, "/api/v1/nodes?status=gone", nil); code != 400 {
		t.Fatal(code)
	}
	var n NodeDetail
	get(t, srv, "/api/v1/nodes/n-a", &n)
	var names []string
	for _, p := range n.Pods {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "agent-x,web-0" && strings.Join(names, ",") != "web-0,agent-x" {
		t.Fatalf("the Pods observed on the Node, by the Node name they reported: %v", names)
	}
	var h NodeHistory
	get(t, srv, "/api/v1/nodes/n-a/history", &h)
	if len(h.Entries) != 1 || h.Entries[0].Type != "node.condition_changed" || len(h.Records) != 1 || h.Records[0].Reason != "NodeNotReady" {
		t.Fatalf("%+v", h)
	}
	if code := get(t, srv, "/api/v1/nodes/nope", nil); code != 404 {
		t.Fatal(code)
	}
	off := workloadServer(t, false)
	var o NodesList
	get(t, off, "/api/v1/nodes", &o)
	if o.Enabled || len(o.Notes) == 0 || !strings.Contains(o.Notes[0], "off") {
		t.Fatalf("%+v", o)
	}
}

func TestRunEvidenceAndSystemWorkloads(t *testing.T) {
	srv := workloadServer(t, true)
	var re RunEvidence
	if code := get(t, srv, "/api/v1/runs/job-1/evidence", &re); code != 200 {
		t.Fatal(code)
	}
	if len(re.Records) != 1 || re.Records[0].Reason != "BackOff" || len(re.Notes) == 0 {
		t.Fatalf("%+v", re)
	}
	if code := get(t, srv, "/api/v1/runs/nope/evidence", nil); code != 404 {
		t.Fatal(code)
	}
	var sys System
	get(t, srv, "/api/v1/system", &sys)
	if sys.Workloads.Counts["StatefulSet"] != 2 || sys.Workloads.Counts["DaemonSet"] != 1 || sys.Workloads.Counts["Deployment"] != 1 || !sys.Workloads.Nodes.Enabled || sys.Workloads.Nodes.Live != 3 {
		t.Fatalf("%+v", sys.Workloads)
	}
	var def System
	get(t, workloadServer(t, false), "/api/v1/system", &def)
	if def.Workloads.Nodes.Enabled || !strings.Contains(def.Workloads.Nodes.Note, "Off") {
		t.Fatalf("%+v", def.Workloads.Nodes)
	}
	// The new Log groups.
	var l Entries
	get(t, srv, "/api/v1/events?within=30d&show=statefulsets,daemonsets,nodes", &l)
	types := map[string]bool{}
	for _, e := range l.Entries {
		types[e.Type] = true
	}
	if !types["statefulset.update_started"] || !types["daemonset.update_started"] || !types["node.condition_changed"] || types["statefulset.counts_changed"] {
		t.Fatalf("counts are a group of their own, off unless asked for: %v", types)
	}
	var unfiltered Entries
	get(t, srv, "/api/v1/events?within=30d", &unfiltered)
	for _, e := range unfiltered.Entries {
		if strings.HasSuffix(e.Type, ".counts_changed") {
			t.Fatalf("the unfiltered Log carries %s", e.Type)
		}
	}
	get(t, srv, "/api/v1/events?within=30d&show=counts", &l)
	if len(l.Entries) != 1 || l.Entries[0].Type != "statefulset.counts_changed" {
		t.Fatalf("%+v", l.Entries)
	}
	get(t, srv, "/api/v1/events?within=30d&kind=Node&workload=", &l)
	if len(l.Entries) != 1 || l.Entries[0].Kind != "Node" {
		t.Fatalf("%+v", l.Entries)
	}
}

// Which Nodes have no Pod of a DaemonSet comes from its live Pods, however many older Pod records were kept since:
// a long-running Pod is not lost behind newer ended ones.
func TestNodesWithoutPodUsesEveryLivePod(t *testing.T) {
	at := base.Add(-48 * time.Hour)
	srv := setupWith(t, base, nil, func(ctx context.Context, w *store.Writer) error {
		if err := w.UpsertWorkload(ctx, store.Workload{ID: "ds-9", Kind: store.KindDaemonSet, Namespace: "shop", Name: "agent", FirstSeen: at, LastSeen: at}); err != nil {
			return err
		}
		if err := w.SaveWorkloadState(ctx, store.WorkloadState{WorkloadID: "ds-9", Kind: "DaemonSet", Desired: 2, Conditions: "[]", LastChangeAt: at}); err != nil {
			return err
		}
		for _, n := range []store.Node{{UID: "n1", Name: "node-1", FirstSeenAt: at, LastSeenAt: at, Origin: store.OriginAdopted, Conditions: "[]"}, {UID: "n2", Name: "node-2", FirstSeenAt: at, LastSeenAt: at, Origin: store.OriginAdopted, Conditions: "[]"}} {
			if err := w.SaveNode(ctx, n); err != nil {
				return err
			}
		}
		st := release.EncodePod(release.PodState{Node: "node-2", Ready: true})
		if err := w.UpsertPod(ctx, store.PodRecord{UID: "old", Namespace: "shop", Name: "agent-old", Kind: "DaemonSet", WorkloadID: "ds-9", Node: "node-2", CreatedAt: at, State: st}); err != nil {
			return err
		}
		for i := range maxPodsShown + 20 { // newer Pods that ended, on the other Node
			ended := base.Add(-time.Hour)
			enc := release.EncodePod(release.PodState{Node: "node-1", Terminated: true, TermAt: ended})
			if err := w.UpsertPod(ctx, store.PodRecord{UID: "e" + string(rune('a'+i%26)) + string(rune('A'+i/26)), Namespace: "shop", Name: "agent-e", Kind: "DaemonSet", WorkloadID: "ds-9", Node: "node-1",
				CreatedAt: base.Add(-2 * time.Hour).Add(time.Duration(i) * time.Second), TerminatedAt: ended, State: enc}); err != nil {
				return err
			}
		}
		return nil
	}, func(c *Config) { c.ObserveNodes = true })
	var p WorkloadPods
	get(t, srv, "/api/v1/workloads/ds-9/pods", &p)
	if p.NodesWithoutPod == nil {
		t.Fatalf("%+v", p.Notes)
	}
	var gaps []string
	for _, g := range *p.NodesWithoutPod {
		gaps = append(gaps, g.Name)
	}
	if strings.Join(gaps, ",") != "node-1" {
		t.Fatalf("node-2 runs a live Pod older than the newest %d records: %v", maxPodsShown, gaps)
	}
	if !p.Truncated {
		t.Fatal("the list is cut and says so")
	}
}
