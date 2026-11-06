package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mihnk/opstide/internal/store"
)

func ev(id, uid, reason, typ, kind, name, regUID string, at time.Time) store.Evidence {
	return store.Evidence{ID: id, Source: store.SourceEventsV1, SourceUID: uid, Namespace: "shop", Name: id, Reason: reason, EventType: typ, Message: reason + " message",
		Regarding: store.Ref{Kind: kind, Name: name, UID: regUID}, LastAt: at, FirstAt: at, ObservedAt: at.Add(time.Second), Origin: store.OriginObservedEvidence}
}

func evidenceServer(t *testing.T) *httptest.Server {
	t.Helper()
	at := base.Add(-2 * time.Hour)
	rel := relRow("rA", "payments-api", "5", 5, at, "completed", "healthy", "observed", "", snap("api:1", "", 3))
	rel.WorkloadID, rel.ReplicaSetUID, rel.CompletedAt, rel.ObserveUntil = "d-pay", "rs-A", at.Add(2*time.Minute), at.Add(12*time.Minute)
	return setup(t, base, nil, func(ctx context.Context, w *store.Writer) error {
		if err := w.UpsertWorkload(ctx, store.Workload{ID: "d-pay", Namespace: "shop", Name: "payments-api", FirstSeen: at, LastSeen: base}); err != nil {
			return err
		}
		if err := w.SetWorkloadSpecReplicas(ctx, "d-pay", 5); err != nil {
			return err
		}
		if err := w.UpsertRelease(ctx, rel); err != nil {
			return err
		}
		pod := ev("e1", "u1", "Unhealthy", "Warning", "Pod", "payments-api-5-a", "pod-a", at.Add(10*time.Minute))
		pod.ReplicaSetUID, pod.WorkloadID, pod.LinkVia, pod.Count, pod.CountBasis = "rs-A", "d-pay", "Pod ownerReference to ReplicaSet to Deployment (recorded)", 4, "deprecatedCount"
		rs := ev("e2", "u2", "SuccessfulCreate", "Normal", "ReplicaSet", "payments-api-5", "rs-A", at.Add(time.Minute))
		rs.ReplicaSetUID, rs.WorkloadID, rs.LinkVia = "rs-A", "d-pay", "regarding.uid"
		dep := ev("e3", "u3", "ScalingReplicaSet", "Normal", "Deployment", "payments-api", "d-pay", at.Add(30*time.Second))
		dep.WorkloadID, dep.LinkVia = "d-pay", "regarding.uid"
		old := ev("e4", "u4", "Unhealthy", "Warning", "Pod", "payments-api-4-z", "pod-z", at.Add(5*time.Minute))
		old.ReplicaSetUID, old.WorkloadID = "rs-OLD", "d-pay"
		early := ev("e5", "u5", "ScalingReplicaSet", "Normal", "Deployment", "payments-api", "d-pay", at.Add(-3*time.Hour))
		early.WorkloadID = "d-pay"
		noUID := ev("e6", "u6", "FailedMount", "Warning", "Pod", "ghost", "", at.Add(20*time.Minute))
		noUID.FirstAt, noUID.LastAt, noUID.Count = time.Time{}, time.Time{}, 0 // the source gave no times and no count
		for _, e := range []store.Evidence{pod, rs, dep, old, early, noUID} {
			if err := w.SaveEvidence(ctx, e); err != nil {
				return err
			}
		}
		if err := w.SaveHPA(ctx, store.HPA{UID: "hpa-1", Namespace: "shop", Name: "payments", TargetKind: "Deployment", TargetName: "payments-api", TargetUID: "d-pay", WorkloadID: "d-pay",
			MaxReplicas: 10, CurrentReplicas: 3, DesiredReplicas: 5, Conditions: `[{"type":"ScalingActive","status":"True","reason":"ValidMetricFound"}]`, Metrics: "[]", FirstSeenAt: at}); err != nil {
			return err
		}
		_, err := w.AddEvent(ctx, store.Event{ID: "hpa.desired_replicas_changed/hpa-1/1/5", Type: "hpa.desired_replicas_changed", Namespace: "shop", Kind: "HorizontalPodAutoscaler",
			ResourceUID: "hpa-1", ResourceName: "payments", WorkloadID: "d-pay", ObservedAt: at.Add(40 * time.Minute), Severity: "info", Confidence: store.Observed,
			Summary: "HPA payments reported desired replicas changed from 3 to 5", Facts: map[string]string{"hpa": "payments", "from": "3", "to": "5"}})
		if err != nil {
			return err
		}
		// A replica-target change is tied both to the release current when it was seen and to its Deployment.
		if _, err = w.AddEvent(ctx, store.Event{ID: "workload.replica_target_changed/d-pay/1/5", Type: "workload.replica_target_changed", Namespace: "shop", Kind: "Deployment",
			ResourceUID: "d-pay", ResourceName: "payments-api", ReleaseID: "rA", WorkloadID: "d-pay", ObservedAt: at.Add(50 * time.Minute), Severity: "info", Confidence: store.Observed,
			Summary: "payments-api replica target changed from 3 to 5", Facts: map[string]string{"from": "3", "to": "5"}}); err != nil {
			return err
		}
		_, err = w.AddEvent(ctx, store.Event{ID: "evidence.event/u1", Type: "evidence.event", Namespace: "shop", Kind: "Pod", ResourceName: "payments-api-5-a", Reason: "Unhealthy",
			WorkloadID: "d-pay", ObservedAt: at.Add(10 * time.Minute), Severity: "warning", Confidence: store.Observed, Summary: "Kubernetes reported Unhealthy for Pod payments-api-5-a"})
		return err
	})
}

func TestEvidenceRecordsStateWhatWasReportedAndWhatIsUnknown(t *testing.T) {
	srv := evidenceServer(t)
	var l EvidenceList
	if code := get(t, srv, "/api/v1/evidence?within=30d&namespace=shop&limit=100", &l); code != 200 {
		t.Fatal(code)
	}
	byID := map[string]EvidenceRecord{}
	for _, r := range l.Records {
		byID[r.ID] = r
	}
	if len(l.Records) != 6 {
		t.Fatalf("%d records", len(l.Records))
	}
	e1 := byID["e1"]
	if e1.Kind != "KubernetesEvent" || e1.Certainty != "OBSERVED" || e1.Relationship != Direct || e1.Count == nil || e1.Count.Value != 4 || e1.Count.Basis != "deprecatedCount" ||
		e1.OccurredAt.Last == nil || e1.ObservedAt.IsZero() || e1.SourceDeletedAt != nil {
		t.Fatalf("%+v", e1)
	}
	var rs *EvidenceLink
	for i, lk := range e1.Links {
		if lk.Kind == "ReplicaSet" {
			rs = &e1.Links[i]
		}
	}
	if rs == nil || rs.Relationship != Ownership || !strings.Contains(rs.Via, "ownerReference") {
		t.Fatalf("a Pod is linked to its ReplicaSet through ownership: %+v", e1.Links)
	}
	// Nothing the source did not give is made up.
	e6 := byID["e6"]
	if e6.Relationship != Unresolved || e6.Count != nil || e6.OccurredAt.First != nil || e6.OccurredAt.Last != nil || len(e6.Links) != 1 || e6.Links[0].Relationship != Unresolved {
		t.Fatalf("%+v", e6)
	}
	if byID["e2"].Links[0].Relationship != Direct {
		t.Fatalf("a ReplicaSet named by UID: %+v", byID["e2"].Links)
	}
	// Filters and paging.
	var f EvidenceList
	get(t, srv, "/api/v1/evidence?within=30d&reason=Unhealthy&type=Warning", &f)
	if len(f.Records) != 2 {
		t.Fatalf("reason filter: %d", len(f.Records))
	}
	get(t, srv, "/api/v1/evidence?within=30d&uid=pod-a", &f)
	if len(f.Records) != 1 || f.Records[0].ID != "e1" {
		t.Fatalf("uid filter: %+v", f.Records)
	}
	var p1, p2 EvidenceList
	get(t, srv, "/api/v1/evidence?within=30d&limit=4", &p1)
	get(t, srv, "/api/v1/evidence?within=30d&limit=4&cursor="+p1.NextCursor, &p2)
	if len(p1.Records) != 4 || p1.NextCursor == "" || len(p2.Records) != 2 || p2.NextCursor != "" {
		t.Fatalf("paging: %d %d", len(p1.Records), len(p2.Records))
	}
	for _, bad := range []string{"?type=Error", "?limit=0", "?cursor=x", "?within=forever"} {
		if code := get(t, srv, "/api/v1/evidence"+bad, nil); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	var one EvidenceOne
	if code := get(t, srv, "/api/v1/evidence/e1", &one); code != 200 || one.Record.Reason != "Unhealthy" {
		t.Fatal(code)
	}
	if code := get(t, srv, "/api/v1/evidence/nope", nil); code != 404 {
		t.Fatal(code)
	}
}

func TestReleaseEvidenceSaysHowEachRecordRelatesToTheRelease(t *testing.T) {
	srv := evidenceServer(t)
	var re ReleaseEvidence
	if code := get(t, srv, "/api/v1/releases/rA/evidence", &re); code != 200 {
		t.Fatal(code)
	}
	rel := map[string]ReleaseRecord{}
	for _, r := range re.Records {
		rel[r.ID] = r
	}
	if len(rel) != 3 || rel["e1"].ID == "" || rel["e2"].ID == "" || rel["e3"].ID == "" {
		t.Fatalf("the release's evidence is its ReplicaSet, that ReplicaSet's Pods, and its Deployment during the release: %v", re.Records)
	}
	for id, want := range map[string]string{"e2": Direct, "e1": Ownership, "e3": SameResourceContext} {
		if rel[id].ReleaseRelationship != want || rel[id].ReleaseVia == "" {
			t.Errorf("%s: %s (%s), want %s", id, rel[id].ReleaseRelationship, rel[id].ReleaseVia, want)
		}
	}
	if rel["e3"].Scope != "workload" || rel["e1"].Scope != "release" {
		t.Errorf("scopes: %s %s", rel["e3"].Scope, rel["e1"].Scope)
	}
	if re.Counts[Direct] != 1 || re.Counts[Ownership] != 1 || re.Counts[SameResourceContext] != 1 || re.Counts["warnings"] != 1 {
		t.Errorf("%v", re.Counts)
	}
	if len(re.Scaling) != 2 {
		t.Errorf("%+v", re.Scaling)
	}
	if re.Summary.RootCause != "Not established by OpsTide." || len(re.Summary.Facts) == 0 || !re.Summary.Complete {
		t.Fatalf("%+v", re.Summary)
	}
	var lines []string
	for _, f := range re.Summary.Facts {
		lines = append(lines, f.Text)
	}
	all := strings.Join(lines, "\n")
	if !strings.Contains(all, "Kubernetes reported Unhealthy for Pod payments-api-5-a") || !strings.Contains(all, "HPA payments reported desired replicas changed from 3 to 5") {
		t.Fatalf("%s", all)
	}
	for _, banned := range []string{"because", "caused", "probably", "likely"} {
		if strings.Contains(strings.ToLower(all), banned) {
			t.Errorf("summary uses %q", banned)
		}
	}

	// The timeline can include them, each entry keeping its type.
	var tl Entries
	get(t, srv, "/api/v1/releases/rA/events?include=evidence,scaling", &tl)
	types := map[string]int{}
	for _, e := range tl.Entries {
		types[e.Type]++
	}
	if types["evidence.record"] != 3 || types["hpa.desired_replicas_changed"] != 1 || types["workload.replica_target_changed"] != 1 {
		t.Fatalf("an entry reachable two ways must appear once: %v", types)
	}
	if code := get(t, srv, "/api/v1/releases/rA/events?include=guesses", nil); code != 400 {
		t.Fatal(code)
	}
}

func TestWorkloadScalingAndDefaultLog(t *testing.T) {
	srv := evidenceServer(t)
	var sc WorkloadScaling
	if code := get(t, srv, "/api/v1/workloads/d-pay/scaling", &sc); code != 200 {
		t.Fatal(code)
	}
	if sc.ReplicaTarget == nil || *sc.ReplicaTarget != 5 || len(sc.HPAs) != 1 || sc.HPAs[0].DesiredReplicas != 5 || sc.HPAs[0].TargetRelation != SameResourceContext || len(sc.Transitions) != 2 {
		t.Fatalf("%+v", sc)
	}
	if code := get(t, srv, "/api/v1/workloads/nope/scaling", nil); code != 404 {
		t.Fatal(code)
	}
	// The unfiltered Log does not carry Kubernetes Event entries; asking for them does.
	var def, k8s, byReason Entries
	get(t, srv, "/api/v1/events?within=30d", &def)
	get(t, srv, "/api/v1/events?within=30d&show=kubernetes", &k8s)
	get(t, srv, "/api/v1/events?within=30d&reason=Unhealthy", &byReason)
	for _, e := range def.Entries {
		if strings.HasPrefix(e.Type, "evidence.") {
			t.Fatalf("the default Log carries %s", e.Type)
		}
	}
	if len(k8s.Entries) != 1 || k8s.Entries[0].Reason != "Unhealthy" || len(byReason.Entries) != 1 {
		t.Fatalf("%+v / %+v", k8s.Entries, byReason.Entries)
	}
	var scaling Entries
	get(t, srv, "/api/v1/events?within=30d&show=scaling&workload=d-pay&kind=HorizontalPodAutoscaler", &scaling)
	if len(scaling.Entries) != 1 {
		t.Fatalf("%+v", scaling.Entries)
	}
}
