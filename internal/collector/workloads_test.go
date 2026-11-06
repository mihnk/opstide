package collector

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/mihnk/opstide/internal/release"
	"github.com/mihnk/opstide/internal/store"
	"github.com/mihnk/opstide/internal/workload"
)

var ptrTrueV = func() *bool { b := true; return &b }()

func ownerRef(kind, name, uid string) []metav1.OwnerReference {
	return []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: kind, Name: name, UID: types.UID(uid), Controller: ptrTrueV}}
}

// controllerRevision is what Kubernetes stores for a StatefulSet or DaemonSet revision: the
// template, literal environment values included.
func controllerRevision(kind, owner, ownerUID, hash string, rev int64, image string, created time.Time) *appsv1.ControllerRevision {
	data := `{"spec":{"template":{"$patch":"replace","metadata":{"labels":{"app":"web"}},"spec":{"containers":[{"name":"web","image":"` + image +
		`","env":[{"name":"DB_PASSWORD","value":"hunter2-literal"}],"command":["run","--token=abc123"]}]}}}}`
	return &appsv1.ControllerRevision{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: owner + "-" + hash, UID: types.UID("cr-" + owner + "-" + hash), CreationTimestamp: metav1.NewTime(created),
			Labels: map[string]string{"controller.kubernetes.io/hash": hash}, OwnerReferences: ownerRef(kind, owner, ownerUID)},
		Data: runtime.RawExtension{Raw: []byte(data)}, Revision: rev,
	}
}

func statefulSet(replicas int32, current, update string, updated, ready int32, strategy appsv1.StatefulSetUpdateStrategyType, partition int32, gen, observed int64) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web", UID: "sts-1", Generation: gen, CreationTimestamp: metav1.NewTime(time.Now())},
		Spec: appsv1.StatefulSetSpec{Replicas: &replicas, PodManagementPolicy: appsv1.OrderedReadyPodManagement,
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: strategy, RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: &partition}}},
		Status: appsv1.StatefulSetStatus{ObservedGeneration: observed, Replicas: replicas, ReadyReplicas: ready, AvailableReplicas: ready, UpdatedReplicas: updated, CurrentReplicas: replicas - updated,
			CurrentRevision: current, UpdateRevision: update},
	}
}

func typesOf(t *testing.T, db *store.Store, workloadID string) []string {
	t.Helper()
	pg, err := db.Events(context.Background(), store.EventQuery{WorkloadID: workloadID, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range pg.Events {
		if strings.HasSuffix(e.Type, ".spec_written") {
			continue // the change ledger is apart from what these tests follow
		}
		out = append(out, e.Type)
	}
	return out
}

func stateOf(t *testing.T, db *store.Store, id string) store.WorkloadState {
	t.Helper()
	w, ok, err := db.WorkloadByID(context.Background(), id)
	if err != nil || !ok || !w.HasState {
		return store.WorkloadState{}
	}
	return w.State
}

// A StatefulSet is followed through its own status and its ControllerRevisions: a first sight
// is a baseline, each change is an entry that quotes what Kubernetes reported, a partitioned
// rollout is not a failure, and an OnDelete template change is not a completed rollout.
func TestStatefulSetLifecycle(t *testing.T) {
	db := openStore(t)
	client, ctx := start(t, db, Options{})
	now := time.Now().UTC().Truncate(time.Second)
	for _, o := range []*appsv1.ControllerRevision{
		controllerRevision("StatefulSet", "web", "sts-1", "aaa", 1, "web:1", now),
		controllerRevision("StatefulSet", "web", "sts-1", "bbb", 2, "web:2", now),
	} {
		if _, err := client.AppsV1().ControllerRevisions("shop").Create(ctx, o, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	sts := statefulSet(5, "web-aaa", "web-aaa", 5, 5, appsv1.RollingUpdateStatefulSetStrategyType, 0, 1, 1)
	_, _ = client.AppsV1().StatefulSets("shop").Create(ctx, sts, metav1.CreateOptions{})
	eventually(t, "state", func() bool { return stateOf(t, db, "sts-1").Rollout == workload.Completed })
	st := stateOf(t, db, "sts-1")
	if st.Strategy != "RollingUpdate" || st.PodManagement != "OrderedReady" || st.Partition == nil || *st.Partition != 0 || st.Desired != 5 {
		t.Fatalf("%+v", st)
	}
	if got := typesOf(t, db, "sts-1"); len(got) != 1 || got[0] != "statefulset.observed" {
		t.Fatalf("a first sight writes one entry and no rollout: %v", got)
	}
	// Revisions, with their snapshots and none of the literal values.
	eventually(t, "revisions", func() bool { r, _ := db.Revisions(ctx, "sts-1"); return len(r) == 2 })
	revs, _ := db.Revisions(ctx, "sts-1")
	if revs[0].Number != 1 || revs[1].Number != 2 || revs[0].Fingerprint == revs[1].Fingerprint {
		t.Fatalf("%+v", revs)
	}
	for _, r := range revs {
		if strings.Contains(r.Snapshot, "hunter2") || strings.Contains(r.Snapshot, "abc123") || !strings.Contains(r.Snapshot, "DB_PASSWORD") {
			t.Fatalf("the snapshot must hold the variable's name and never its value or the command: %s", r.Snapshot)
		}
		if r.Origin != store.OriginObserved {
			t.Errorf("a revision created while watching is observed: %s", r.Origin)
		}
	}
	// The update begins: update revision bbb, nothing updated yet.
	up := statefulSet(5, "web-aaa", "web-bbb", 0, 5, appsv1.RollingUpdateStatefulSetStrategyType, 0, 2, 2)
	up.ResourceVersion = "2"
	_, _ = client.AppsV1().StatefulSets("shop").Update(ctx, up, metav1.UpdateOptions{})
	eventually(t, "updating", func() bool { return stateOf(t, db, "sts-1").Rollout == workload.Updating })
	got := strings.Join(typesOf(t, db, "sts-1"), ",")
	for _, want := range []string{"statefulset.revision_changed", "statefulset.update_started"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %s", want, got)
		}
	}
	// A partition of 2 is reached at 3 of 5: stopping there is by design, not a failure.
	part := statefulSet(5, "web-aaa", "web-bbb", 3, 5, appsv1.RollingUpdateStatefulSetStrategyType, 2, 2, 2)
	part.ResourceVersion = "3"
	_, _ = client.AppsV1().StatefulSets("shop").Update(ctx, part, metav1.UpdateOptions{})
	eventually(t, "partitioned", func() bool { return stateOf(t, db, "sts-1").Rollout == workload.Partitioned })
	got = strings.Join(typesOf(t, db, "sts-1"), ",")
	if !strings.Contains(got, "statefulset.update_partitioned") || !strings.Contains(got, "statefulset.partition_changed") || strings.Contains(got, "failed") {
		t.Fatal(got)
	}
	// Counts change: one entry listing what Kubernetes reported.
	cnt := statefulSet(5, "web-aaa", "web-bbb", 3, 4, appsv1.RollingUpdateStatefulSetStrategyType, 2, 2, 2)
	cnt.ResourceVersion = "4"
	_, _ = client.AppsV1().StatefulSets("shop").Update(ctx, cnt, metav1.UpdateOptions{})
	findCounts := func(key, from string) (store.Event, bool) {
		pg, _ := db.Events(ctx, store.EventQuery{Types: []string{"statefulset.counts_changed", "daemonset.counts_changed"}, Limit: 50})
		for _, e := range pg.Events {
			if e.Facts[key] == from {
				return e, true
			}
		}
		return store.Event{}, false
	}
	eventually(t, "counts", func() bool { _, ok := findCounts("readyFrom", "5"); return ok })
	if e, _ := findCounts("readyFrom", "5"); e.Facts["readyTo"] != "4" || e.Severity != "warning" || !strings.Contains(e.Summary, "ready 5 to 4") {
		t.Fatalf("%+v", e)
	}
	// Completed: current equals update.
	done := statefulSet(5, "web-bbb", "web-bbb", 5, 5, appsv1.RollingUpdateStatefulSetStrategyType, 0, 3, 3)
	done.ResourceVersion = "5"
	_, _ = client.AppsV1().StatefulSets("shop").Update(ctx, done, metav1.UpdateOptions{})
	eventually(t, "completed", func() bool {
		return strings.Contains(strings.Join(typesOf(t, db, "sts-1"), ","), "statefulset.update_completed")
	})
	if hs, _ := db.Releases(ctx, store.ReleaseQuery{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour), WithHistory: true}); len(hs.Releases) != 0 {
		t.Fatal("a StatefulSet revision is not a Deployment release")
	}
	// Deleted: the history and the revisions stay.
	_ = client.AppsV1().StatefulSets("shop").Delete(ctx, "web", metav1.DeleteOptions{})
	eventually(t, "deleted", func() bool { w, _, _ := db.WorkloadByID(ctx, "sts-1"); return !w.DeletedAt.IsZero() })
	if !strings.Contains(strings.Join(typesOf(t, db, "sts-1"), ","), "statefulset.deleted") {
		t.Fatal("no deletion entry")
	}
	if r, _ := db.Revisions(ctx, "sts-1"); len(r) != 2 {
		t.Fatal("the revisions must outlive the StatefulSet")
	}
}

// OnDelete: a template change replaces no Pod, so it is never reported as complete.
func TestStatefulSetOnDeleteIsNotCompletedByATemplateChange(t *testing.T) {
	db := openStore(t)
	client, ctx := start(t, db, Options{})
	_, _ = client.AppsV1().StatefulSets("shop").Create(ctx, statefulSet(3, "web-aaa", "web-aaa", 3, 3, appsv1.OnDeleteStatefulSetStrategyType, 0, 1, 1), metav1.CreateOptions{})
	eventually(t, "baseline", func() bool { return stateOf(t, db, "sts-1").Rollout == workload.Completed })
	up := statefulSet(3, "web-aaa", "web-bbb", 0, 3, appsv1.OnDeleteStatefulSetStrategyType, 0, 2, 2)
	up.ResourceVersion = "2"
	_, _ = client.AppsV1().StatefulSets("shop").Update(ctx, up, metav1.UpdateOptions{})
	eventually(t, "awaiting", func() bool { return stateOf(t, db, "sts-1").Rollout == workload.AwaitingPodDeletion })
	got := strings.Join(typesOf(t, db, "sts-1"), ",")
	if !strings.Contains(got, "statefulset.update_awaiting_pod_deletion") || strings.Contains(got, "update_completed") {
		t.Fatal(got)
	}
	if st := stateOf(t, db, "sts-1"); st.Strategy != "OnDelete" || st.Partition != nil {
		t.Fatalf("%+v", st)
	}
}

func podOf(name, uid, kind, owner, ownerUID, hash, node string, created time.Time, extraLabels map[string]string) *corev1.Pod {
	labels := map[string]string{"controller-revision-hash": hash}
	for k, v := range extraLabels {
		labels[k] = v
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: name, UID: types.UID(uid), CreationTimestamp: metav1.NewTime(created), Labels: labels, OwnerReferences: ownerRef(kind, owner, ownerUID)},
		Spec:       corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "web", Ready: true}},
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(created.Add(10 * time.Second))}}},
	}
}

// StatefulSet Pods are found through their ownerReference, with their ordinal, revision and node;
// Events about them are linked to the StatefulSet.
func TestStatefulSetPodsKeepOrdinalRevisionAndNode(t *testing.T) {
	db := openStore(t)
	client, ctx := start(t, db, Options{})
	now := time.Now().UTC().Truncate(time.Second)
	_, _ = client.AppsV1().StatefulSets("shop").Create(ctx, statefulSet(2, "web-aaa", "web-aaa", 2, 2, appsv1.RollingUpdateStatefulSetStrategyType, 0, 1, 1), metav1.CreateOptions{})
	eventually(t, "sts", func() bool { return stateOf(t, db, "sts-1").Desired == 2 })
	_, _ = client.CoreV1().Pods("shop").Create(ctx, podOf("web-0", "p0", "StatefulSet", "web", "sts-1", "web-aaa", "node-a", now, nil), metav1.CreateOptions{})
	_, _ = client.CoreV1().Pods("shop").Create(ctx, podOf("web-1", "p1", "StatefulSet", "web", "sts-1", "web-aaa", "node-b", now, map[string]string{"apps.kubernetes.io/pod-index": "1"}), metav1.CreateOptions{})
	// A Pod of another controller that carries the same label is not this StatefulSet's.
	stray := podOf("other-0", "px", "ReplicaSet", "other", "rs-x", "zzz", "node-a", now, nil)
	_, _ = client.CoreV1().Pods("shop").Create(ctx, stray, metav1.CreateOptions{})
	eventually(t, "pods", func() bool { ps, _ := db.PodsOfWorkload(ctx, "sts-1", 10); return len(ps) == 2 })
	ps, _ := db.PodsOfWorkload(ctx, "sts-1", 10)
	by := map[string]store.PodRecord{}
	for _, p := range ps {
		by[p.Name] = p
	}
	if p := by["web-0"]; p.Kind != store.KindStatefulSet || p.Ordinal == nil || *p.Ordinal != 0 || p.Revision != "web-aaa" || p.Node != "node-a" {
		t.Fatalf("%+v", p)
	}
	if p := by["web-1"]; p.Ordinal == nil || *p.Ordinal != 1 || p.Node != "node-b" {
		t.Fatalf("%+v", p)
	}
	if on, _ := db.PodsOnNode(ctx, "node-a", 10); len(on) != 1 || on[0].Name != "web-0" {
		t.Fatalf("placement context: %+v", on)
	}
	// A restart of the Pod is recorded against the StatefulSet.
	re := podOf("web-0", "p0", "StatefulSet", "web", "sts-1", "web-aaa", "node-a", now, nil)
	re.ResourceVersion = "9"
	re.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "web", Ready: true, RestartCount: 1, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1, FinishedAt: metav1.NewTime(now.Add(time.Minute))}}}}
	_, _ = client.CoreV1().Pods("shop").Update(ctx, re, metav1.UpdateOptions{})
	eventually(t, "restart", func() bool { return strings.Contains(strings.Join(typesOf(t, db, "sts-1"), ","), "pod.restarted") })
	// An Event about the Pod links to the workload by the Pod's ownerReference.
	ev := podEvent("web-0.unhealthy", "ev-s1", "web-0", "Unhealthy", now)
	ev.Regarding.UID = "p0"
	_, _ = client.EventsV1().Events("shop").Create(ctx, ev, metav1.CreateOptions{})
	eventually(t, "evidence", func() bool { e, ok := evidenceOf(t, db, "ev-s1"); return ok && e.WorkloadID == "sts-1" })
	if e, _ := evidenceOf(t, db, "ev-s1"); e.ReplicaSetUID != "" || !strings.Contains(e.LinkVia, "StatefulSet") {
		t.Fatalf("%+v", e)
	}
	// The Pod goes away: its record stays, ended.
	_ = client.CoreV1().Pods("shop").Delete(ctx, "web-1", metav1.DeleteOptions{})
	eventually(t, "ended", func() bool {
		rec, _, _ := db.PodByUID(ctx, "p1")
		return !rec.TerminatedAt.IsZero()
	})
}

// A DaemonSet's revision is its newest ControllerRevision; its Pods are placed by node; node count
// changes are counts, never a new release.
func TestDaemonSetRevisionNodesAndUpdate(t *testing.T) {
	db := openStore(t)
	client, ctx := start(t, db, Options{})
	now := time.Now().UTC().Truncate(time.Second)
	_, _ = client.AppsV1().ControllerRevisions("shop").Create(ctx, controllerRevision("DaemonSet", "agent", "ds-1", "r1", 1, "agent:1", now), metav1.CreateOptions{})
	ds := func(desired, current, updated, ready, avail, mis int32, gen, obs int64) *appsv1.DaemonSet {
		return &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "agent", UID: "ds-1", Generation: gen, CreationTimestamp: metav1.NewTime(time.Now())},
			Spec:       appsv1.DaemonSetSpec{UpdateStrategy: appsv1.DaemonSetUpdateStrategy{Type: appsv1.RollingUpdateDaemonSetStrategyType}},
			Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: desired, CurrentNumberScheduled: current, UpdatedNumberScheduled: updated, NumberReady: ready, NumberAvailable: avail,
				NumberMisscheduled: mis, ObservedGeneration: obs},
		}
	}
	_, _ = client.AppsV1().DaemonSets("shop").Create(ctx, ds(3, 3, 3, 3, 3, 0, 1, 1), metav1.CreateOptions{})
	eventually(t, "baseline", func() bool { return stateOf(t, db, "ds-1").Revision == "agent-r1" })
	for i, node := range []string{"node-a", "node-b", "node-c"} {
		hash := "r1"
		_, _ = client.CoreV1().Pods("shop").Create(ctx, podOf("agent-"+strconv.Itoa(i), "dp"+strconv.Itoa(i), "DaemonSet", "agent", "ds-1", hash, node, now, nil), metav1.CreateOptions{})
	}
	eventually(t, "pods", func() bool { ps, _ := db.PodsOfWorkload(ctx, "ds-1", 10); return len(ps) == 3 })
	// A new template: a newer ControllerRevision; the rollout starts and is not complete until all are updated.
	_, _ = client.AppsV1().ControllerRevisions("shop").Create(ctx, controllerRevision("DaemonSet", "agent", "ds-1", "r2", 2, "agent:2", now.Add(time.Minute)), metav1.CreateOptions{})
	up := ds(3, 3, 1, 3, 3, 0, 2, 2)
	up.ResourceVersion = "2"
	_, _ = client.AppsV1().DaemonSets("shop").Update(ctx, up, metav1.UpdateOptions{})
	eventually(t, "updating", func() bool {
		s := stateOf(t, db, "ds-1")
		return s.Rollout == workload.Updating && s.UpdateRevision == "agent-r2"
	})
	got := strings.Join(typesOf(t, db, "ds-1"), ",")
	if !strings.Contains(got, "daemonset.revision_changed") || !strings.Contains(got, "daemonset.update_started") {
		t.Fatal(got)
	}
	// A node joins and the desired count rises: counts change, nothing else is inferred.
	grow := ds(4, 3, 1, 3, 3, 0, 2, 2)
	grow.ResourceVersion = "3"
	_, _ = client.AppsV1().DaemonSets("shop").Update(ctx, grow, metav1.UpdateOptions{})
	desiredEntry := func() (store.Event, bool) {
		pg, _ := db.Events(ctx, store.EventQuery{Types: []string{"daemonset.counts_changed"}, Limit: 50})
		for _, e := range pg.Events {
			if e.Facts["desiredFrom"] == "3" {
				return e, true
			}
		}
		return store.Event{}, false
	}
	eventually(t, "counts", func() bool { _, ok := desiredEntry(); return ok })
	if e, _ := desiredEntry(); e.Facts["desiredTo"] != "4" || strings.Contains(strings.ToLower(e.Summary), "release") {
		t.Fatalf("%+v", e)
	}
	done := ds(4, 4, 4, 4, 4, 1, 2, 2)
	done.ResourceVersion = "4"
	_, _ = client.AppsV1().DaemonSets("shop").Update(ctx, done, metav1.UpdateOptions{})
	eventually(t, "completed", func() bool {
		return strings.Contains(strings.Join(typesOf(t, db, "ds-1"), ","), "daemonset.update_completed")
	})
	if st := stateOf(t, db, "ds-1"); st.Misscheduled != 1 || st.Current != 4 {
		t.Fatalf("%+v", st)
	}
	if revs, _ := db.Revisions(ctx, "ds-1"); len(revs) != 2 || revs[0].Fingerprint == revs[1].Fingerprint {
		t.Fatalf("%+v", revs)
	}
}

// Nodes are watched only when asked for. Their transitions keep the source's status, reason, message and
// transition time; spec.unschedulable is a fact about the Node, not about who changed it.
func TestNodeTransitionsAreRecordedWhenObserved(t *testing.T) {
	db := openStore(t)
	client, ctx := start(t, db, Options{ObserveNodes: true})
	cond := func(typ string, status corev1.ConditionStatus, reason, msg string, at time.Time) corev1.NodeCondition {
		return corev1.NodeCondition{Type: corev1.NodeConditionType(typ), Status: status, Reason: reason, Message: msg, LastTransitionTime: metav1.NewTime(at), LastHeartbeatTime: metav1.NewTime(time.Now())}
	}
	now := time.Now().UTC().Truncate(time.Second)
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", UID: "n-a", CreationTimestamp: metav1.NewTime(now)},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{cond("Ready", corev1.ConditionTrue, "KubeletReady", "kubelet is posting ready status", now), cond("MemoryPressure", corev1.ConditionFalse, "KubeletHasSufficientMemory", "ok", now)}}}
	if _, err := client.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "node", func() bool { n, ok, _ := db.NodeByUID(ctx, "n-a"); return ok && n.Origin == store.OriginObserved })
	nodeEntries := func() []store.Event {
		pg, _ := db.Events(ctx, store.EventQuery{Kind: "Node", ResourceUID: "n-a", Limit: 50})
		return pg.Events
	}
	if es := nodeEntries(); len(es) != 1 || es[0].Type != "node.observed" {
		t.Fatalf("a Node created while watching is one entry: %+v", es)
	}
	// Ready goes Unknown, as the Node controller writes it.
	at := now
	n2 := node.DeepCopy()
	n2.ResourceVersion = "2"
	n2.Status.Conditions[0] = cond("Ready", corev1.ConditionUnknown, "NodeStatusUnknown", "Kubelet stopped posting node status.", at)
	_, _ = client.CoreV1().Nodes().Update(ctx, n2, metav1.UpdateOptions{})
	eventually(t, "unknown", func() bool { return len(nodeEntries()) == 2 })
	var cc store.Event
	for _, e := range nodeEntries() {
		if e.Type == "node.condition_changed" {
			cc = e
		}
	}
	if cc.Facts["from"] != "True" || cc.Facts["to"] != "Unknown" || cc.Reason != "NodeStatusUnknown" || !cc.OccurredAt.Equal(at) || cc.Facts["message"] == "" {
		t.Fatalf("the source's status, reason, message and time must be kept: %+v", cc)
	}
	low := strings.ToLower(cc.Summary)
	for _, banned := range []string{"failed", "crash", "dead", "because", "caused", "down"} {
		if strings.Contains(low, banned) {
			t.Fatalf("an Unknown condition is not called %q: %s", banned, cc.Summary)
		}
	}
	// Cordon: only the fact that it became unschedulable.
	n3 := n2.DeepCopy()
	n3.ResourceVersion = "3"
	n3.Spec.Unschedulable = true
	_, _ = client.CoreV1().Nodes().Update(ctx, n3, metav1.UpdateOptions{})
	eventually(t, "cordon", func() bool { return len(nodeEntries()) == 3 })
	for _, e := range nodeEntries() {
		if e.Type == "node.unschedulable_changed" && (strings.Contains(strings.ToLower(e.Summary), "cordon") || strings.Contains(strings.ToLower(e.Summary), "operator")) {
			t.Fatalf("who changed spec.unschedulable is not known: %s", e.Summary)
		}
	}
	// A Pod placed on the Node, and an Event naming the Node by the name kubelet reports as its UID.
	_, _ = client.CoreV1().Pods("shop").Create(ctx, podOf("web-0", "p0", "StatefulSet", "web", "sts-1", "web-aaa", "node-a", now, nil), metav1.CreateOptions{})
	nev := podEvent("node-a.ready", "ev-n1", "node-a", "NodeNotReady", now)
	nev.Regarding.Kind, nev.Regarding.UID, nev.Namespace = "Node", "node-a", "default"
	_, _ = client.EventsV1().Events("default").Create(ctx, nev, metav1.CreateOptions{})
	eventually(t, "node evidence", func() bool { e, ok := evidenceOf(t, db, "ev-n1"); return ok && e.NodeID == "n-a" })
	if e, _ := evidenceOf(t, db, "ev-n1"); !strings.Contains(e.LinkVia, "exact name") {
		t.Fatalf("a link by name must say so: %+v", e)
	}
	// Removed.
	_ = client.CoreV1().Nodes().Delete(ctx, "node-a", metav1.DeleteOptions{})
	eventually(t, "removed", func() bool { n, _, _ := db.NodeByUID(ctx, "n-a"); return !n.DeletedAt.IsZero() })
	if es := nodeEntries(); es[0].Type != "node.deleted" {
		t.Fatalf("%+v", es)
	}
}

// A Node that was there before OpsTide began watching is recorded without an entry per Node.
func TestNodesPresentAtStartAreAdoptedWithoutEntries(t *testing.T) {
	db := openStore(t)
	old := time.Now().UTC().Add(-48 * time.Hour)
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-old", UID: "n-old", CreationTimestamp: metav1.NewTime(old)},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: "Ready", Status: corev1.ConditionFalse, Reason: "KubeletNotReady", LastTransitionTime: metav1.NewTime(old)}}}}
	_, ctx := start(t, db, Options{ObserveNodes: true}, node)
	eventually(t, "node", func() bool { n, ok, _ := db.NodeByUID(ctx, "n-old"); return ok && n.Origin == store.OriginAdopted })
	if pg, _ := db.Events(ctx, store.EventQuery{Kind: "Node", Limit: 10}); len(pg.Events) != 0 {
		t.Fatalf("an adopted Node writes no entry: %+v", pg.Events)
	}
	n, _, _ := db.NodeByUID(ctx, "n-old")
	if !strings.Contains(n.Conditions, "KubeletNotReady") {
		t.Fatalf("its reported state is kept: %s", n.Conditions)
	}
}

// Without ObserveNodes no Node watch exists at all.
func TestNodesAreNotWatchedUnlessAskedFor(t *testing.T) {
	c := New(fake.NewClientset(), openStore(t), Options{})
	if c.nodes != nil {
		t.Fatal("a Node informer was made")
	}
	for _, o := range c.opt {
		if o.group == Nodes {
			t.Fatal("a Node watch is registered")
		}
	}
}

// An Event can arrive before the Pod of its Job is known. Once the Job's Pod is recorded, the Event is linked
// to the run and its CronJob from the Pod's ownerReference, without a second record and without changing its times.
func TestEventBeforeTheJobPodIsLinkedLater(t *testing.T) {
	db := openStore(t)
	client, ctx := start(t, db, Options{})
	now := time.Now().UTC().Truncate(time.Second)
	ev := podEvent("job-pod.backoff", "ev-j1", "report-1-abc", "BackOff", now)
	ev.Regarding.UID = "jp1"
	_, _ = client.EventsV1().Events("shop").Create(ctx, ev, metav1.CreateOptions{})
	eventually(t, "evidence", func() bool { _, ok := evidenceOf(t, db, "ev-j1"); return ok })
	before, _ := evidenceOf(t, db, "ev-j1")
	if before.RunID != "" || before.OperationID != "" {
		t.Fatalf("nothing is known of the Pod yet: %+v", before)
	}
	cj := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "report", UID: "cj-1", CreationTimestamp: metav1.NewTime(now)},
		Spec: batchv1.CronJobSpec{Schedule: "0 * * * *", JobTemplate: batchv1.JobTemplateSpec{}}}
	_, _ = client.BatchV1().CronJobs("shop").Create(ctx, cj, metav1.CreateOptions{})
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "report-1", UID: "job-1", CreationTimestamp: metav1.NewTime(now),
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "CronJob", Name: "report", UID: "cj-1", Controller: ptrTrueV}}}}
	_, _ = client.BatchV1().Jobs("shop").Create(ctx, job, metav1.CreateOptions{})
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "report-1-abc", UID: "jp1", CreationTimestamp: metav1.NewTime(now), Labels: map[string]string{"job-name": "report-1"},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "report-1", UID: "job-1", Controller: ptrTrueV}}}, Spec: corev1.PodSpec{NodeName: "node-z"}}
	_, _ = client.CoreV1().Pods("shop").Create(ctx, pod, metav1.CreateOptions{})
	eventually(t, "late link", func() bool { e, _ := evidenceOf(t, db, "ev-j1"); return e.RunID == "job-1" && e.OperationID == "cj-1" })
	after, _ := evidenceOf(t, db, "ev-j1")
	if after.ID != before.ID || !after.ObservedAt.Equal(before.ObservedAt) || !after.LastAt.Equal(before.LastAt) || countRecords(t, db) != 1 {
		t.Fatalf("the Event's identity and times must not change: %+v / %+v", before, after)
	}
	if !strings.Contains(after.LinkVia, "ownerReference") {
		t.Fatalf("%+v", after)
	}
	// The ownership outlives the Pod and the Job.
	_ = client.CoreV1().Pods("shop").Delete(ctx, "report-1-abc", metav1.DeleteOptions{})
	_ = client.BatchV1().Jobs("shop").Delete(ctx, "report-1", metav1.DeleteOptions{})
	time.Sleep(300 * time.Millisecond)
	ev2 := podEvent("job-pod.again", "ev-j2", "report-1-abc", "Failed", now)
	ev2.Regarding.UID = "jp1"
	_, _ = client.EventsV1().Events("shop").Create(ctx, ev2, metav1.CreateOptions{})
	eventually(t, "recorded owner", func() bool {
		e, ok := evidenceOf(t, db, "ev-j2")
		return ok && e.RunID == "job-1" && e.OperationID == "cj-1"
	})
	// And a Pod that names no Job never gets a run by similarity of names.
	ev3 := podEvent("lookalike", "ev-j3", "report-1-abd", "Failed", now)
	ev3.Regarding.UID = "other-uid"
	_, _ = client.EventsV1().Events("shop").Create(ctx, ev3, metav1.CreateOptions{})
	eventually(t, "unrelated", func() bool { _, ok := evidenceOf(t, db, "ev-j3"); return ok })
	if e, _ := evidenceOf(t, db, "ev-j3"); e.RunID != "" || e.OperationID != "" {
		t.Fatalf("no link by name: %+v", e)
	}
}

// Deployments appear in the same workload list as the other kinds, with their state and Pod placement.
func TestDeploymentsShareTheWorkloadModel(t *testing.T) {
	db := openStore(t)
	client, ctx, now := releaseWithPod(t, db, Options{})
	_ = client
	eventually(t, "state", func() bool { return stateOf(t, db, "dep-1").Desired == 1 })
	w, ok, _ := db.WorkloadByID(ctx, "dep-1")
	if !ok || w.Kind != store.KindDeployment || w.State.Revision != "42" || w.State.Strategy != "RollingUpdate" {
		t.Fatalf("%+v", w)
	}
	pg, err := db.Workloads(ctx, store.WorkloadQuery{Kind: store.KindDeployment, Limit: 10})
	if err != nil || len(pg.Rows) != 1 || pg.Rows[0].ID != "dep-1" || !pg.Rows[0].HasState {
		t.Fatalf("%v %+v", err, pg.Rows)
	}
	// Release entries name their workload.
	if hist, _ := db.Events(ctx, store.EventQuery{WorkloadID: "dep-1", Types: []string{"release."}, Limit: 20}); len(hist.Events) == 0 {
		t.Fatal("the release entries do not name their workload")
	}
	ps, _ := db.PodsOfWorkload(ctx, "dep-1", 10)
	if len(ps) != 1 || ps[0].Kind != store.KindDeployment || ps[0].Revision != "42" {
		t.Fatalf("%+v", ps)
	}
	_ = now
	_ = release.AnnRevision
	_ = eventsv1.SchemeGroupVersion
}

// Restarting OpsTide must not mark StatefulSets and DaemonSets that still exist as deleted (Deployments are
// reconciled against their own cache); one that really is gone is marked, and its history stays.
func TestRestartKeepsLiveWorkloadsAndMarksTheOnesThatWentAway(t *testing.T) {
	db := openStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	keep := statefulSet(2, "web-aaa", "web-aaa", 2, 2, appsv1.RollingUpdateStatefulSetStrategyType, 0, 1, 1)
	gone := statefulSet(1, "db-aaa", "db-aaa", 1, 1, appsv1.RollingUpdateStatefulSetStrategyType, 0, 1, 1)
	gone.Name, gone.UID = "db", "sts-gone"
	ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "agent", UID: "ds-1", Generation: 1, CreationTimestamp: metav1.NewTime(now)},
		Status: appsv1.DaemonSetStatus{ObservedGeneration: 1}}
	dep := deployment("42", 1, 1, 1, completeCond(now))
	run := func(objs ...runtime.Object) (cancel func()) {
		c := fake.NewClientset(objs...)
		c.Resources = []*metav1.APIResourceList{{GroupVersion: "events.k8s.io/v1", APIResources: []metav1.APIResource{{Name: "events"}}}}
		col := New(c, db, Options{})
		ctx, stop := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); _ = col.Run(ctx) }()
		eventually(t, "synced", func() bool { return col.Synced(ctx) == nil && col.evidenceSynced() })
		return func() { stop(); <-done }
	}
	stop := run(keep, gone, ds, dep, replicaSet("42", 1, now, "api:1"))
	eventually(t, "all recorded", func() bool {
		return stateOf(t, db, "sts-1").Desired == 2 && stateOf(t, db, "sts-gone").Desired == 1 && hasRelease(db, "42")
	})
	time.Sleep(200 * time.Millisecond)
	stop()
	// Second run: db is gone; everything else still exists.
	stop2 := run(keep, ds, dep, replicaSet("42", 1, now, "api:1"))
	defer stop2()
	eventually(t, "the missing one is marked", func() bool {
		w, _, _ := db.WorkloadByID(context.Background(), "sts-gone")
		return !w.DeletedAt.IsZero()
	})
	time.Sleep(300 * time.Millisecond)
	for _, id := range []string{"sts-1", "ds-1", "dep-1"} {
		if w, ok, _ := db.WorkloadByID(context.Background(), id); !ok || !w.DeletedAt.IsZero() {
			t.Fatalf("%s still exists and must not be marked deleted: %+v", id, w)
		}
	}
	if !strings.Contains(strings.Join(typesOf(t, db, "sts-gone"), ","), "statefulset.deleted") {
		t.Fatal("no deletion entry for the one that went away")
	}
}

// A DaemonSet is processed before its ControllerRevisions have arrived: its revision is unknown, not changed,
// and learning it later writes no "revision changed" entry.
func TestDaemonSetRevisionLearnedLaterIsNotAChange(t *testing.T) {
	db := openStore(t)
	client, ctx := start(t, db, Options{})
	now := time.Now().UTC().Truncate(time.Second)
	ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "agent", UID: "ds-1", Generation: 1, CreationTimestamp: metav1.NewTime(now)},
		Status: appsv1.DaemonSetStatus{ObservedGeneration: 1, DesiredNumberScheduled: 2, UpdatedNumberScheduled: 2}}
	_, _ = client.AppsV1().DaemonSets("shop").Create(ctx, ds, metav1.CreateOptions{})
	eventually(t, "baseline", func() bool { w, ok, _ := db.WorkloadByID(ctx, "ds-1"); return ok && w.HasState })
	if st := stateOf(t, db, "ds-1"); st.Revision != "" {
		t.Fatalf("no ControllerRevision is known: %+v", st)
	}
	// Status changes while the revision is still unknown: it must stay unknown, and say nothing about a template.
	up := ds.DeepCopy()
	up.ResourceVersion = "2"
	up.Status.NumberReady = 2
	_, _ = client.AppsV1().DaemonSets("shop").Update(ctx, up, metav1.UpdateOptions{})
	eventually(t, "ready", func() bool { return stateOf(t, db, "ds-1").Ready == 2 })
	// Now the ControllerRevision arrives.
	_, _ = client.AppsV1().ControllerRevisions("shop").Create(ctx, controllerRevision("DaemonSet", "agent", "ds-1", "r1", 1, "agent:1", now), metav1.CreateOptions{})
	eventually(t, "learned", func() bool { return stateOf(t, db, "ds-1").Revision == "agent-r1" })
	got := strings.Join(typesOf(t, db, "ds-1"), ",")
	if strings.Contains(got, "revision_changed") || strings.Contains(got, "update_started") {
		t.Fatalf("learning the first revision is not a template change: %s", got)
	}
	// A real change afterwards is one.
	_, _ = client.AppsV1().ControllerRevisions("shop").Create(ctx, controllerRevision("DaemonSet", "agent", "ds-1", "r2", 2, "agent:2", now.Add(time.Minute)), metav1.CreateOptions{})
	eventually(t, "changed", func() bool { return stateOf(t, db, "ds-1").Revision == "agent-r2" })
	if !strings.Contains(strings.Join(typesOf(t, db, "ds-1"), ","), "daemonset.revision_changed") {
		t.Fatal("a change from one revision to another is recorded")
	}
}

// Only apps workloads own Pods here: a third-party Kind with the same name, in another API group, is not tracked.
func TestPodOfAThirdPartyKindIsNotAStatefulSetPod(t *testing.T) {
	db := openStore(t)
	client, ctx := start(t, db, Options{})
	now := time.Now().UTC().Truncate(time.Second)
	_, _ = client.AppsV1().StatefulSets("shop").Create(ctx, statefulSet(1, "web-aaa", "web-aaa", 1, 1, appsv1.RollingUpdateStatefulSetStrategyType, 0, 1, 1), metav1.CreateOptions{})
	eventually(t, "sts", func() bool { return stateOf(t, db, "sts-1").Desired == 1 })
	foreign := podOf("kr-0", "pk", "StatefulSet", "kr", "kruise-uid", "kr-abc", "node-a", now, nil)
	foreign.OwnerReferences[0].APIVersion = "apps.kruise.io/v1beta1"
	_, _ = client.CoreV1().Pods("shop").Create(ctx, foreign, metav1.CreateOptions{})
	_, _ = client.CoreV1().Pods("shop").Create(ctx, podOf("web-0", "p0", "StatefulSet", "web", "sts-1", "web-aaa", "node-a", now, nil), metav1.CreateOptions{})
	eventually(t, "own pod", func() bool { ps, _ := db.PodsOfWorkload(ctx, "sts-1", 10); return len(ps) == 1 })
	time.Sleep(400 * time.Millisecond)
	if rec, ok, _ := db.PodByUID(ctx, "pk"); ok {
		t.Fatalf("a Pod owned by another API group's StatefulSet was recorded: %+v", rec)
	}
}
