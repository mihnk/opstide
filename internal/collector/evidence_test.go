package collector

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mihnk/opstide/internal/release"
	"github.com/mihnk/opstide/internal/store"
)

func podEvent(name, uid, pod, reason string, created time.Time) *eventsv1.Event {
	return &eventsv1.Event{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: name, UID: types.UID(uid), CreationTimestamp: metav1.NewTime(created)},
		Reason:     reason, Type: "Warning", Note: "Readiness probe failed: HTTP probe failed with statuscode: 503", ReportingController: "kubelet", ReportingInstance: "node-1",
		Regarding:       corev1.ObjectReference{Kind: "Pod", Namespace: "shop", Name: pod, UID: types.UID("uid-" + pod)},
		DeprecatedCount: 1, DeprecatedFirstTimestamp: metav1.NewTime(created), DeprecatedLastTimestamp: metav1.NewTime(created),
	}
}

func evidenceOf(t *testing.T, db *store.Store, sourceUID string) (store.Evidence, bool) {
	t.Helper()
	e, ok, err := db.EvidenceByID(context.Background(), EvidenceID(sourceUID))
	if err != nil {
		t.Fatal(err)
	}
	return e, ok
}

func countRecords(t *testing.T, db *store.Store) int {
	t.Helper()
	p, err := db.Evidence(context.Background(), store.EvidenceQuery{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	return len(p.Records)
}

func entriesOfType(t *testing.T, db *store.Store, typ string) []store.Event {
	t.Helper()
	p, err := db.Events(context.Background(), store.EventQuery{Types: []string{typ}, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return p.Events
}

// releaseWithPod makes revision 42 of shop/api with one Pod and waits until both are recorded.
func releaseWithPod(t *testing.T, db *store.Store, opt Options) (*fake.Clientset, context.Context, time.Time) {
	t.Helper()
	client, ctx := start(t, db, opt)
	now := time.Now().UTC().Truncate(time.Second)
	_, _ = client.AppsV1().ReplicaSets("shop").Create(ctx, replicaSet("42", 1, now, "api:1"), metav1.CreateOptions{})
	_, _ = client.AppsV1().Deployments("shop").Create(ctx, deployment("42", 1, 1, 1, completeCond(now)), metav1.CreateOptions{})
	_, _ = client.CoreV1().Pods("shop").Create(ctx, deployPod("42", "api-42-a", now, true, corev1.ContainerStatus{Name: "api"}), metav1.CreateOptions{})
	eventually(t, "release and pod", func() bool {
		_, ok, _ := db.PodByUID(ctx, "uid-api-42-a")
		return hasRelease(db, "42") && ok //nolint:contextcheck // a test helper that reads the store itself
	})
	return client, ctx, now
}

// A Warning Event becomes one evidence record and one Log entry, linked to the
// release through the Pod's ownerReferences; a growing count changes the record,
// it does not add records, and the Log is not written for every increase.
func TestEventIsRecordedOnceAndItsCountUpdatesInPlace(t *testing.T) {
	db := openStore(t)
	clk := newClock(time.Now())
	client, ctx, now := releaseWithPod(t, db, Options{Now: clk.now})
	ev := podEvent("api-42-a.1", "ev-1", "api-42-a", "Unhealthy", now)
	if _, err := client.EventsV1().Events("shop").Create(ctx, ev, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "evidence", func() bool { _, ok := evidenceOf(t, db, "ev-1"); return ok })
	got, _ := evidenceOf(t, db, "ev-1")
	if got.Source != store.SourceEventsV1 || got.Reason != "Unhealthy" || got.EventType != "Warning" || got.Regarding.UID != "uid-api-42-a" || got.Reporter != "kubelet/node-1" {
		t.Fatalf("%+v", got)
	}
	if got.ReplicaSetUID != "rs-42" || got.WorkloadID != "dep-1" || !strings.Contains(got.LinkVia, "ownerReference") {
		t.Fatalf("link: %+v", got)
	}
	if got.Count != 1 || got.CountBasis != "deprecatedCount" || got.Origin != store.OriginObservedEvidence {
		t.Fatalf("%+v", got)
	}
	if n := len(entriesOfType(t, db, "evidence.event")); n != 1 {
		t.Fatalf("%d log entries", n)
	}

	// Kubernetes aggregates: the same Event, a larger count, still within the limit on
	// how often the Log shows a growing count.
	clk.add(time.Minute)
	up := ev.DeepCopy()
	up.ResourceVersion = "2"
	up.DeprecatedCount, up.DeprecatedLastTimestamp = 5, metav1.NewTime(now.Add(time.Minute))
	_, _ = client.EventsV1().Events("shop").Update(ctx, up, metav1.UpdateOptions{})
	eventually(t, "count 5", func() bool { e, _ := evidenceOf(t, db, "ev-1"); return e.Count == 5 })
	if n := countRecords(t, db); n != 1 {
		t.Fatalf("an aggregated Event made %d records", n)
	}
	if n := len(entriesOfType(t, db, "evidence.count_changed")); n != 0 {
		t.Fatalf("the Log was written for every increase (%d)", n)
	}
	// Past the limit, an increase is one explicit count-change entry, never several occurrences.
	clk.add(10 * time.Minute)
	up2 := up.DeepCopy()
	up2.ResourceVersion = "3"
	up2.DeprecatedCount = 9
	_, _ = client.EventsV1().Events("shop").Update(ctx, up2, metav1.UpdateOptions{})
	eventually(t, "count entry", func() bool { return len(entriesOfType(t, db, "evidence.count_changed")) == 1 })
	if e := entriesOfType(t, db, "evidence.count_changed")[0]; e.Facts["countFrom"] != "1" || e.Facts["count"] != "9" || e.Reason != "Unhealthy" || e.WorkloadID != "dep-1" {
		t.Fatalf("%+v", e)
	}
	if n := countRecords(t, db); n != 1 {
		t.Fatalf("%d records", n)
	}
}

// Kubernetes dropping an Event does not remove the evidence.
func TestEvidenceOutlivesTheEventKubernetesDeletes(t *testing.T) {
	db := openStore(t)
	client, ctx, now := releaseWithPod(t, db, Options{})
	_, _ = client.EventsV1().Events("shop").Create(ctx, podEvent("e", "ev-2", "api-42-a", "BackOff", now), metav1.CreateOptions{})
	eventually(t, "evidence", func() bool { _, ok := evidenceOf(t, db, "ev-2"); return ok })
	_ = client.EventsV1().Events("shop").Delete(ctx, "e", metav1.DeleteOptions{})
	eventually(t, "marked gone", func() bool { e, _ := evidenceOf(t, db, "ev-2"); return !e.SourceDeletedAt.IsZero() })
	if e, ok := evidenceOf(t, db, "ev-2"); !ok || e.Reason != "BackOff" || e.Message == "" {
		t.Fatalf("the record must stay as captured: %+v", e)
	}
}

// An Event already there when OpsTide starts is adopted: it keeps the times
// Kubernetes gave it and is never dated at startup. An Event whose resource
// OpsTide never saw is kept, unresolved; so is one that names no UID.
func TestExistingEventIsAdoptedAndUnresolvedStaysUnresolved(t *testing.T) {
	db := openStore(t)
	before := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	noUID := podEvent("n", "ev-4", "ghost", "FailedMount", before)
	noUID.Regarding.UID = ""
	started := time.Now().UTC()
	start(t, db, Options{}, podEvent("a", "ev-3", "ghost", "FailedMount", before), noUID)
	eventually(t, "both", func() bool { return countRecords(t, db) == 2 })
	a, _ := evidenceOf(t, db, "ev-3")
	if a.Origin != store.OriginAdoptedEvidence || a.ObservedAt.Before(started.Add(-time.Second)) {
		t.Fatalf("%+v", a)
	}
	if !a.FirstAt.Equal(before) || !a.LastAt.Equal(before) || !a.SortTime().Equal(before) {
		t.Fatalf("the times must be the ones Kubernetes gave: first %v last %v", a.FirstAt, a.LastAt)
	}
	if a.ReplicaSetUID != "" || a.WorkloadID != "" || a.LinkVia != "" {
		t.Fatalf("a Pod OpsTide never saw must stay unresolved: %+v", a)
	}
	if n, _ := evidenceOf(t, db, "ev-4"); n.Regarding.UID != "" || n.ReplicaSetUID != "" || n.LinkVia != "" {
		t.Fatalf("an Event with no UID must not be linked by name: %+v", n)
	}
	es := entriesOfType(t, db, "evidence.event")
	if len(es) == 0 || !es[0].OccurredAt.Equal(before) || !strings.Contains(es[0].Summary, "already present") {
		t.Fatalf("the timeline must not imply it began at startup: %+v", es)
	}
}

// The Pod of an Event may be recorded after the Event: the link is made then, from the Pod's ownerReference.
func TestEventAboutAPodIsLinkedWhenThePodIsRecordedLater(t *testing.T) {
	db := openStore(t)
	client, ctx := start(t, db, Options{})
	now := time.Now().UTC().Truncate(time.Second)
	_, _ = client.EventsV1().Events("shop").Create(ctx, podEvent("e", "ev-5", "api-42-a", "FailedScheduling", now), metav1.CreateOptions{})
	eventually(t, "evidence", func() bool { _, ok := evidenceOf(t, db, "ev-5"); return ok })
	if e, _ := evidenceOf(t, db, "ev-5"); e.ReplicaSetUID != "" {
		t.Fatalf("linked before the Pod was known: %+v", e)
	}
	_, _ = client.AppsV1().ReplicaSets("shop").Create(ctx, replicaSet("42", 1, now, "api:1"), metav1.CreateOptions{})
	_, _ = client.AppsV1().Deployments("shop").Create(ctx, deployment("42", 1, 1, 1, completeCond(now)), metav1.CreateOptions{})
	_, _ = client.CoreV1().Pods("shop").Create(ctx, deployPod("42", "api-42-a", now, true, corev1.ContainerStatus{Name: "api"}), metav1.CreateOptions{})
	eventually(t, "linked", func() bool {
		e, _ := evidenceOf(t, db, "ev-5")
		return e.ReplicaSetUID == "rs-42" && e.WorkloadID == "dep-1"
	})
}

// One API is read, never both: with core/v1 chosen, an events.k8s.io Event is not read.
func TestEventsAreReadThroughOneAPIOnly(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	coreEv := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "c", UID: "ev-6", CreationTimestamp: metav1.NewTime(now)},
		Reason: "Killing", Type: "Warning", Message: "Stopping container", Count: 3,
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "api-42-a", UID: "uid-api-42-a"},
		FirstTimestamp: metav1.NewTime(now), LastTimestamp: metav1.NewTime(now), Source: corev1.EventSource{Component: "kubelet"}}
	db := openStore(t)
	client, ctx := start(t, db, Options{EventsSource: store.SourceCoreV1})
	_, _ = client.CoreV1().Events("shop").Create(ctx, coreEv, metav1.CreateOptions{})
	_, _ = client.EventsV1().Events("shop").Create(ctx, podEvent("v1", "ev-7", "api-42-a", "Unhealthy", now), metav1.CreateOptions{})
	eventually(t, "core event", func() bool { _, ok := evidenceOf(t, db, "ev-6"); return ok })
	time.Sleep(300 * time.Millisecond)
	e, _ := evidenceOf(t, db, "ev-6")
	if e.Source != store.SourceCoreV1 || e.Count != 3 || e.CountBasis != "count" || e.Reporter != "kubelet" {
		t.Fatalf("%+v", e)
	}
	if _, ok := evidenceOf(t, db, "ev-7"); ok || countRecords(t, db) != 1 {
		t.Fatal("the second API was read as well")
	}
}

// An Event about something OpsTide does not follow is not kept.
func TestEventsAboutOtherKindsAreNotKept(t *testing.T) {
	db := openStore(t)
	client, ctx := start(t, db, Options{})
	now := time.Now().UTC()
	node := podEvent("n", "ev-8", "node-1", "NodeNotReady", now)
	node.Regarding.Kind = "Node"
	_, _ = client.EventsV1().Events("shop").Create(ctx, node, metav1.CreateOptions{})
	_, _ = client.EventsV1().Events("shop").Create(ctx, podEvent("p", "ev-9", "api-42-a", "Unhealthy", now), metav1.CreateOptions{})
	eventually(t, "pod event", func() bool { _, ok := evidenceOf(t, db, "ev-9"); return ok })
	if _, ok := evidenceOf(t, db, "ev-8"); ok {
		t.Fatal("an Event about a Node was recorded")
	}
}

func TestLongMessageIsCutAndSaid(t *testing.T) {
	db := openStore(t)
	client, ctx := start(t, db, Options{})
	ev := podEvent("l", "ev-10", "api-42-a", "Failed", time.Now().UTC())
	ev.Note = strings.Repeat("é", 2000)
	_, _ = client.EventsV1().Events("shop").Create(ctx, ev, metav1.CreateOptions{})
	eventually(t, "evidence", func() bool { _, ok := evidenceOf(t, db, "ev-10"); return ok })
	e, _ := evidenceOf(t, db, "ev-10")
	if len(e.Message) > maxMessage || !e.MessageTruncated || strings.ContainsRune(e.Message, '�') {
		t.Fatalf("%d bytes, truncated=%v", len(e.Message), e.MessageTruncated)
	}
}

func hpaOf(desired int32, cond ...autoscalingv2.HorizontalPodAutoscalerCondition) *autoscalingv2.HorizontalPodAutoscaler {
	min := int32(2)
	return &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api", UID: "hpa-1"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{MinReplicas: &min, MaxReplicas: 10,
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "api"}},
		Status: autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 3, DesiredReplicas: desired, Conditions: cond},
	}
}

// An autoscaler's first sight is a baseline; later changes to desired replicas and
// conditions are entries saying what Kubernetes reported, tied to the Deployment it names.
func TestAutoscalerTransitionsAreRecordedWithoutClaimingACause(t *testing.T) {
	db := openStore(t)
	client, ctx, _ := releaseWithPod(t, db, Options{})
	// Created with no status yet, as Kubernetes does; the status that follows is a baseline.
	empty := hpaOf(0)
	empty.Status.CurrentReplicas = 0
	_, _ = client.AutoscalingV2().HorizontalPodAutoscalers("shop").Create(ctx, empty, metav1.CreateOptions{})
	eventually(t, "hpa", func() bool { hs, _ := db.HPAsOfWorkload(ctx, "dep-1"); return len(hs) == 1 })
	first := hpaOf(3, autoscalingv2.HorizontalPodAutoscalerCondition{Type: autoscalingv2.AbleToScale, Status: corev1.ConditionTrue, Reason: "ReadyForNewScale"})
	first.ResourceVersion = "2"
	_, _ = client.AutoscalingV2().HorizontalPodAutoscalers("shop").Update(ctx, first, metav1.UpdateOptions{})
	eventually(t, "status", func() bool {
		hs, _ := db.HPAsOfWorkload(ctx, "dep-1")
		return len(hs) == 1 && hs[0].DesiredReplicas == 3
	})
	if n := len(entriesOfType(t, db, "hpa.desired_replicas_changed")) + len(entriesOfType(t, db, "hpa.condition_changed")); n != 0 {
		t.Fatal("the first status an autoscaler reports is a baseline, not a transition")
	}
	up := hpaOf(8, autoscalingv2.HorizontalPodAutoscalerCondition{Type: autoscalingv2.ScalingLimited, Status: corev1.ConditionTrue, Reason: "TooManyReplicas", Message: "desired exceeds max"})
	up.ResourceVersion = "3"
	_, _ = client.AutoscalingV2().HorizontalPodAutoscalers("shop").Update(ctx, up, metav1.UpdateOptions{})
	eventually(t, "transitions", func() bool {
		return len(entriesOfType(t, db, "hpa.desired_replicas_changed")) == 1 && len(entriesOfType(t, db, "hpa.condition_changed")) == 1
	})
	d := entriesOfType(t, db, "hpa.desired_replicas_changed")[0]
	if d.Facts["from"] != "3" || d.Facts["to"] != "8" || d.WorkloadID != "dep-1" || d.Facts["targetRelationship"] != "SAME_RESOURCE_CONTEXT" || d.Confidence != store.Observed {
		t.Fatalf("%+v", d)
	}
	low := strings.ToLower(d.Summary)
	if strings.Contains(low, "because") || strings.Contains(low, "caused") || strings.Contains(low, "traffic") {
		t.Fatalf("an entry must not state a cause: %q", d.Summary)
	}
	c := entriesOfType(t, db, "hpa.condition_changed")[0]
	if c.Reason != "TooManyReplicas" || c.Facts["condition"] != "ScalingLimited" {
		t.Fatalf("%+v", c)
	}
}

// A Deployment's replica target changing is its own fact: it is not a release, and it
// does not say an autoscaler did it. The availability shortfall that follows a raised
// target is recorded as having begun with it.
func TestReplicaTargetChangeIsNotAReleaseAndOpensAScalingEpisode(t *testing.T) {
	db := openStore(t)
	client, ctx := start(t, db, Options{})
	now := time.Now().UTC().Truncate(time.Second)
	_, _ = client.AppsV1().ReplicaSets("shop").Create(ctx, replicaSet("42", 3, now, "api:1"), metav1.CreateOptions{})
	_, _ = client.AppsV1().Deployments("shop").Create(ctx, deployment("42", 3, 3, 3, completeCond(now)), metav1.CreateOptions{})
	eventually(t, "completed", func() bool { return hasRelease(db, "42") && relOf(t, db, "42").State == store.ReleaseCompleted })
	if relOf(t, db, "42").AssessmentVersion != release.AssessmentVersion {
		t.Fatal("a new release is judged by the current rules")
	}
	d := deployment("42", 5, 5, 3, completeCond(now))
	d.ResourceVersion = "2"
	_, _ = client.AppsV1().Deployments("shop").Update(ctx, d, metav1.UpdateOptions{})
	eventually(t, "entry", func() bool { return len(entriesOfType(t, db, "workload.replica_target_changed")) == 1 })
	e := entriesOfType(t, db, "workload.replica_target_changed")[0]
	if e.Facts["from"] != "3" || e.Facts["to"] != "5" || strings.Contains(strings.ToLower(e.Summary), "autoscal") {
		t.Fatalf("%+v", e)
	}
	if hasRelease(db, "43") {
		t.Fatal("a replica change was made a release")
	}
	eps := release.DecodeEpisodes(relOf(t, db, "42").Episodes)
	if len(eps) != 1 || eps[0].Scaling == nil || eps[0].Scaling.From != 3 || eps[0].Scaling.To != 5 || eps[0].Scaling.MinAvailable != 3 {
		t.Fatalf("%+v", eps)
	}
	// Available drops below the earlier target: recorded, and no longer a convergence.
	d2 := deployment("42", 5, 5, 2, completeCond(now))
	d2.ResourceVersion = "3"
	_, _ = client.AppsV1().Deployments("shop").Update(ctx, d2, metav1.UpdateOptions{})
	eventually(t, "min", func() bool {
		eps := release.DecodeEpisodes(relOf(t, db, "42").Episodes)
		return len(eps) == 1 && eps[0].Scaling != nil && eps[0].Scaling.MinAvailable == 2
	})
}

// Which API Events are read through follows discovery: events.k8s.io/v1 when the cluster serves it,
// core/v1 when it does not; a failure to ask is not taken to mean there is no events.k8s.io.
func TestEventsSourceFollowsDiscovery(t *testing.T) {
	has := fake.NewClientset()
	has.Resources = []*metav1.APIResourceList{{GroupVersion: "events.k8s.io/v1", APIResources: []metav1.APIResource{{Name: "events"}}}}
	if got := New(has, openStore(t), Options{}).pickEventsSource(); got != store.SourceEventsV1 {
		t.Fatalf("%s", got)
	}
	if got := New(fake.NewClientset(), openStore(t), Options{}).pickEventsSource(); got != store.SourceCoreV1 {
		t.Fatalf("a cluster without events.k8s.io must be read through core/v1: %s", got)
	}
	if got := New(fake.NewClientset(), openStore(t), Options{EventsSource: store.SourceEventsV1}).pickEventsSource(); got != store.SourceEventsV1 {
		t.Fatalf("%s", got)
	}
}

// Run and CronJob links made while the Job and its Pod existed survive their garbage collection:
// the next sync of the Event must not replace them with nothing.
func TestKnownLinksSurviveWhenTheCachesCanNoLongerSay(t *testing.T) {
	prev := store.Evidence{ReplicaSetUID: "rs", WorkloadID: "d", RunID: "job-1", OperationID: "cj-1", HPAUID: "h", LinkVia: "Pod ownerReference to Job"}
	ev := store.Evidence{}
	keepLinks(&ev, prev)
	if ev.RunID != "job-1" || ev.OperationID != "cj-1" || ev.ReplicaSetUID != "rs" || ev.WorkloadID != "d" || ev.LinkVia == "" || ev.HPAUID != "h" {
		t.Fatalf("%+v", ev)
	}
	fresh := store.Evidence{RunID: "job-2", LinkVia: "regarding.uid"}
	keepLinks(&fresh, prev)
	if fresh.RunID != "job-2" || fresh.LinkVia != "regarding.uid" {
		t.Fatalf("a link the caches do know must win: %+v", fresh)
	}
}

// An Event dropped by Kubernetes while OpsTide was not running is marked gone at the next start,
// and one still present is not.
func TestEventDroppedWhileOpsTideWasDownIsMarkedAtStart(t *testing.T) {
	db := openStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	keep := podEvent("keep", "ev-k", "api-42-a", "Unhealthy", now)
	drop := podEvent("drop", "ev-d", "api-42-a", "BackOff", now)
	c1 := fake.NewClientset(keep, drop)
	c1.Resources = []*metav1.APIResourceList{{GroupVersion: "events.k8s.io/v1", APIResources: []metav1.APIResource{{Name: "events"}}}}
	col := New(c1, db, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = col.Run(ctx) }()
	eventually(t, "synced", func() bool { return col.Synced(ctx) == nil && col.evidenceSynced() })
	eventually(t, "both", func() bool { return countRecords(t, db) == 2 })
	cancel()
	<-done

	c2 := fake.NewClientset(keep) // the second Event expired while OpsTide was down
	c2.Resources = c1.Resources
	col2 := New(c2, db, Options{})
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() { defer close(done2); _ = col2.Run(ctx2) }()
	defer func() { cancel2(); <-done2 }()
	eventually(t, "marked", func() bool { e, _ := evidenceOf(t, db, "ev-d"); return !e.SourceDeletedAt.IsZero() })
	if e, _ := evidenceOf(t, db, "ev-k"); !e.SourceDeletedAt.IsZero() {
		t.Fatal("an Event that still exists was marked gone")
	}
	if e, _ := evidenceOf(t, db, "ev-d"); e.Reason != "BackOff" {
		t.Fatalf("the record must stay: %+v", e)
	}
}
