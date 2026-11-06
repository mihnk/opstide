package collector

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/mihnk/opstide/internal/release"
	"github.com/mihnk/opstide/internal/store"
)

// A Deployment "api" at revision rev, with its ReplicaSet and the Pods of that ReplicaSet.

func deployment(rev string, want, updated, available int32, conds ...appsv1.DeploymentCondition) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api", UID: "dep-1", Generation: 5, Annotations: map[string]string{release.AnnRevision: rev}},
		Spec:       appsv1.DeploymentSpec{Replicas: &want, Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType}},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 5, Replicas: want, UpdatedReplicas: updated, ReadyReplicas: available, AvailableReplicas: available, Conditions: conds},
	}
}

func completeCond(at time.Time) appsv1.DeploymentCondition {
	return appsv1.DeploymentCondition{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: "NewReplicaSetAvailable", LastUpdateTime: metav1.NewTime(at)}
}

func replicaSet(rev string, replicas int32, created time.Time, image string) *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-" + rev, UID: types.UID("rs-" + rev), CreationTimestamp: metav1.NewTime(created),
			Labels: map[string]string{"pod-template-hash": "h" + rev}, Annotations: map[string]string{release.AnnRevision: rev},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "api", UID: "dep-1", Controller: ptrTrue}}},
		Spec: appsv1.ReplicaSetSpec{Replicas: &replicas, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "api", Image: image,
			Env:       []corev1.EnvVar{{Name: "DB_PASSWORD", Value: "hunter2-literal"}},
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")}},
		}}}}},
		Status: appsv1.ReplicaSetStatus{Replicas: replicas},
	}
}

func deployPod(rev, name string, created time.Time, ready bool, statuses ...corev1.ContainerStatus) *corev1.Pod {
	cond := corev1.ConditionFalse
	if ready {
		cond = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: name, UID: types.UID("uid-" + name), CreationTimestamp: metav1.NewTime(created),
			Labels:          map[string]string{"pod-template-hash": "h" + rev},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "api-" + rev, UID: types.UID("rs-" + rev), Controller: ptrTrue}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: statuses,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: cond, LastTransitionTime: metav1.NewTime(created.Add(20 * time.Second))}}},
	}
}

func relOf(t *testing.T, db *store.Store, rev string) store.Release {
	t.Helper()
	page, err := db.Releases(context.Background(), store.ReleaseQuery{WithHistory: true, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range page.Releases {
		if r.Revision == rev {
			return r
		}
	}
	t.Fatalf("no release for revision %s in %d", rev, len(page.Releases))
	return store.Release{}
}

func hasRelease(db *store.Store, rev string) bool {
	page, _ := db.Releases(context.Background(), store.ReleaseQuery{WithHistory: true, Limit: 50})
	for _, r := range page.Releases {
		if r.Revision == rev {
			return true
		}
	}
	return false
}

func entryTypes(t *testing.T, db *store.Store, releaseID string) []string {
	t.Helper()
	p, err := db.Events(context.Background(), store.EventQuery{ReleaseID: releaseID, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := len(p.Events) - 1; i >= 0; i-- {
		out = append(out, p.Events[i].Type)
	}
	return out
}

func contains(list []string, s string) int {
	n := 0
	for _, x := range list {
		if x == s {
			n++
		}
	}
	return n
}

// A new revision appears while OpsTide watches: it becomes an observed release,
// the snapshot holds no secret value, its ReplicaSet and Pods are tied to it by UID.
func TestNewRevisionBecomesAnObservedReleaseWithItsPods(t *testing.T) {
	db := openStore(t)
	client, ctx := start(t, db, Options{})
	now := time.Now().UTC().Truncate(time.Second)
	rs := replicaSet("42", 2, now, "ghcr.io/example/api:1.9.0")
	d := deployment("42", 2, 2, 0, appsv1.DeploymentCondition{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: "ReplicaSetUpdated"})
	for _, o := range []func() error{
		func() error {
			_, err := client.AppsV1().ReplicaSets("shop").Create(ctx, rs, metav1.CreateOptions{})
			return err
		},
		func() error {
			_, err := client.AppsV1().Deployments("shop").Create(ctx, d, metav1.CreateOptions{})
			return err
		},
	} {
		if err := o(); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "release", func() bool { return hasRelease(db, "42") })
	r := relOf(t, db, "42")
	if r.Origin != store.OriginObserved || r.ReplicaSetUID != "rs-42" || r.WorkloadID != "dep-1" || r.State == store.ReleaseCompleted {
		t.Fatalf("release %+v", r)
	}
	if strings.Contains(r.Snapshot, "hunter2-literal") || !strings.Contains(r.Snapshot, "DB_PASSWORD") || !strings.Contains(r.Snapshot, "ghcr.io/example/api:1.9.0") {
		t.Fatalf("snapshot %s", r.Snapshot)
	}
	// Pods of the ReplicaSet: tied to the release by the ReplicaSet's UID, with their own entries.
	for _, name := range []string{"api-42-a", "api-42-b"} {
		if _, err := client.CoreV1().Pods("shop").Create(ctx, deployPod("42", name, now, true, corev1.ContainerStatus{Name: "api"}), metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "pods", func() bool { p, _ := db.ReleasePods(ctx, r); return len(p) == 2 })
	eventually(t, "pod entries", func() bool {
		ts := entryTypes(t, db, r.ID)
		return contains(ts, "pod.created") == 2 && contains(ts, "pod.ready") == 2 && contains(ts, "release.observed") == 1
	})
	// The rollout completes: the rollout entry joins the release, health starts observing.
	d2 := deployment("42", 2, 2, 2, completeCond(now.Add(time.Minute)))
	if _, err := client.AppsV1().Deployments("shop").UpdateStatus(ctx, d2, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "completed", func() bool { return relOf(t, db, "42").State == store.ReleaseCompleted })
	r = relOf(t, db, "42")
	if r.Health != store.HealthObserving || r.ObserveUntil.IsZero() || !r.CompletedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("after completion: %+v", r)
	}
	if contains(entryTypes(t, db, r.ID), "deployment.rollout") != 1 {
		t.Fatalf("entries %v", entryTypes(t, db, r.ID))
	}
}

// An OOMKilled container becomes one entry naming the Pod and container, counted once
// however often the informer repeats it, and the release becomes Degraded with the reason.
func TestOOMKilledIsAttributedOnceAndDegradesTheRelease(t *testing.T) {
	db := openStore(t)
	client, ctx := start(t, db, Options{})
	now := time.Now().UTC().Truncate(time.Second)
	_, _ = client.AppsV1().ReplicaSets("shop").Create(ctx, replicaSet("42", 1, now, "api:1"), metav1.CreateOptions{})
	_, _ = client.AppsV1().Deployments("shop").Create(ctx, deployment("42", 1, 1, 1, completeCond(now)), metav1.CreateOptions{})
	eventually(t, "release", func() bool { return hasRelease(db, "42") })
	r := relOf(t, db, "42")
	p := deployPod("42", "api-42-a", now, true, corev1.ContainerStatus{Name: "api"})
	if _, err := client.CoreV1().Pods("shop").Create(ctx, p, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "pod", func() bool { ps, _ := db.ReleasePods(ctx, r); return len(ps) == 1 })
	oom := &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(now.Add(2 * time.Minute))}
	for i := range 3 { // the same fact, three updates
		p2 := p.DeepCopy()
		p2.ResourceVersion = string(rune('2' + i))
		p2.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "api", RestartCount: 1, LastTerminationState: corev1.ContainerState{Terminated: oom}}}
		if _, err := client.CoreV1().Pods("shop").UpdateStatus(ctx, p2, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "degraded", func() bool { return relOf(t, db, "42").Health == store.HealthDegraded })
	time.Sleep(200 * time.Millisecond)
	ts := entryTypes(t, db, r.ID)
	if contains(ts, "pod.oomkilled") != 1 || contains(ts, "pod.restarted") != 1 || contains(ts, "release.health.degraded") != 1 {
		t.Fatalf("entries %v", ts)
	}
	got := relOf(t, db, "42")
	reasons := strings.Join(release.DecodeReasons(got.HealthReasons), " ")
	if !strings.Contains(reasons, "api-42-a") || !strings.Contains(reasons, "OOMKilled") {
		t.Fatalf("reasons %q", reasons)
	}
	evs, _ := db.Events(ctx, store.EventQuery{ReleaseID: r.ID, Limit: 50})
	for _, e := range evs.Events {
		if e.Type == "pod.oomkilled" && (e.Facts["container"] != "api" || e.ResourceName != "api-42-a" || !e.OccurredAt.Equal(now.Add(2*time.Minute))) {
			t.Fatalf("oom entry %+v", e)
		}
	}
}

// A newer revision replaces the old release; the old one keeps its history, and a rollout
// the controller gave up on is stalled and degraded, never healthy.
func TestSupersededStalledAndKeptAfterKubernetesCleansUp(t *testing.T) {
	db := openStore(t)
	client, ctx := start(t, db, Options{})
	now := time.Now().UTC().Truncate(time.Second)
	_, _ = client.AppsV1().ReplicaSets("shop").Create(ctx, replicaSet("42", 1, now, "api:1"), metav1.CreateOptions{})
	_, _ = client.AppsV1().Deployments("shop").Create(ctx, deployment("42", 1, 1, 1, completeCond(now)), metav1.CreateOptions{})
	eventually(t, "42", func() bool { return hasRelease(db, "42") && relOf(t, db, "42").State == store.ReleaseCompleted })

	_, _ = client.AppsV1().ReplicaSets("shop").Create(ctx, replicaSet("43", 1, now.Add(time.Hour), "api:2"), metav1.CreateOptions{})
	stalled := deployment("43", 1, 1, 0, appsv1.DeploymentCondition{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded", Message: "deadline exceeded", LastUpdateTime: metav1.NewTime(now.Add(2 * time.Hour))})
	_, _ = client.AppsV1().Deployments("shop").Update(ctx, stalled, metav1.UpdateOptions{})
	eventually(t, "43 stalled", func() bool { return hasRelease(db, "43") && relOf(t, db, "43").State == store.ReleaseStalled })
	eventually(t, "42 superseded", func() bool { return !relOf(t, db, "42").SupersededAt.IsZero() })
	old, cur := relOf(t, db, "42"), relOf(t, db, "43")
	if old.State != store.ReleaseCompleted || cur.PrevID != old.ID || cur.Health != store.HealthDegraded {
		t.Fatalf("old %+v\ncur %+v", old, cur)
	}
	if got := entryTypes(t, db, cur.ID); contains(got, "release.stalled") != 1 || contains(got, "release.health.degraded") != 1 {
		t.Fatalf("entries %v", got)
	}
	if got := entryTypes(t, db, old.ID); contains(got, "release.superseded") != 1 {
		t.Fatalf("entries %v", got)
	}
	// Kubernetes cleans up: ReplicaSet gone. The release and its snapshot stay.
	if err := client.AppsV1().ReplicaSets("shop").Delete(ctx, "api-42", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if relOf(t, db, "42").Snapshot == "" {
		t.Fatal("history lost")
	}
}

// What existed before OpsTide is adopted: no assessment, no replayed Pod history.
func TestAdoptedReleaseIsNotAssessedAndItsPodsStayQuiet(t *testing.T) {
	db := openStore(t)
	old := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	prev := replicaSet("41", 0, old.Add(-time.Hour), "api:0")
	cur := replicaSet("42", 1, old, "api:1")
	oom := &corev1.ContainerStateTerminated{Reason: "OOMKilled", FinishedAt: metav1.NewTime(old.Add(time.Hour))}
	p := deployPod("42", "api-42-a", old, true, corev1.ContainerStatus{Name: "api", RestartCount: 3, LastTerminationState: corev1.ContainerState{Terminated: oom}})
	client, ctx := start(t, db, Options{}, prev, cur, deployment("42", 1, 1, 1, completeCond(old)), p)
	eventually(t, "adopted", func() bool { return hasRelease(db, "42") && hasRelease(db, "41") })
	r := relOf(t, db, "42")
	if r.Origin != store.OriginAdopted || r.Health != store.HealthInsufficient || r.State != store.ReleaseCompleted {
		t.Fatalf("%+v", r)
	}
	if strings.Contains(strings.Join(release.DecodeReasons(r.HealthReasons), " "), "before OpsTide") == false {
		t.Fatalf("reasons %s", r.HealthReasons)
	}
	if got := relOf(t, db, "41"); got.Origin != store.OriginAdopted || got.State != store.ReleaseSuperseded || r.PrevID != got.ID {
		t.Fatalf("previous %+v / %s", got, r.PrevID)
	}
	eventually(t, "pod", func() bool { ps, _ := db.ReleasePods(ctx, r); return len(ps) == 1 })
	time.Sleep(200 * time.Millisecond)
	for _, ty := range entryTypes(t, db, r.ID) {
		if strings.HasPrefix(ty, "pod.") || ty == "release.observed" {
			t.Fatalf("history of an adopted release was replayed: %v", entryTypes(t, db, r.ID))
		}
	}
	// A restart after adoption is real: counted from the baseline (3 -> 4 is one restart).
	p2 := p.DeepCopy()
	p2.ResourceVersion = "9"
	p2.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "api", RestartCount: 4,
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1, FinishedAt: metav1.NewTime(time.Now().Add(-time.Minute))}}}}
	if _, err := client.CoreV1().Pods("shop").UpdateStatus(ctx, p2, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "restart after adoption", func() bool { return contains(entryTypes(t, db, r.ID), "pod.restarted") == 1 })
	evs, _ := db.Events(ctx, store.EventQuery{ReleaseID: r.ID, Limit: 50})
	for _, e := range evs.Events {
		if e.Type == "pod.restarted" && e.Facts["delta"] != "1" {
			t.Fatalf("the 3 restarts before adoption were counted: %+v", e.Facts)
		}
	}
	if got := relOf(t, db, "42"); got.Health != store.HealthInsufficient {
		t.Fatalf("an adopted release is assessed: %+v", got)
	}
}

// Pods leaving: replaced by a newer revision is information, not failure; a Pod that vanished
// while OpsTide was down says its end is unknown.
func TestPodTerminationDuringReplacementIsNotAFailure(t *testing.T) {
	db := openStore(t)
	client, ctx := start(t, db, Options{})
	now := time.Now().UTC().Truncate(time.Second)
	_, _ = client.AppsV1().ReplicaSets("shop").Create(ctx, replicaSet("42", 1, now, "api:1"), metav1.CreateOptions{})
	_, _ = client.AppsV1().Deployments("shop").Create(ctx, deployment("42", 1, 1, 1, completeCond(now)), metav1.CreateOptions{})
	eventually(t, "42", func() bool { return hasRelease(db, "42") })
	r := relOf(t, db, "42")
	_, _ = client.CoreV1().Pods("shop").Create(ctx, deployPod("42", "api-42-a", now, true, corev1.ContainerStatus{Name: "api"}), metav1.CreateOptions{})
	eventually(t, "pod", func() bool { ps, _ := db.ReleasePods(ctx, r); return len(ps) == 1 })
	// Revision 43 takes over.
	_, _ = client.AppsV1().ReplicaSets("shop").Create(ctx, replicaSet("43", 1, now.Add(time.Minute), "api:2"), metav1.CreateOptions{})
	_, _ = client.AppsV1().Deployments("shop").Update(ctx, deployment("43", 1, 1, 0), metav1.UpdateOptions{})
	eventually(t, "superseded", func() bool { return !relOf(t, db, "42").SupersededAt.IsZero() })
	if err := client.CoreV1().Pods("shop").Delete(ctx, "api-42-a", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "terminated", func() bool { return contains(entryTypes(t, db, r.ID), "pod.terminated") == 1 })
	evs, _ := db.Events(ctx, store.EventQuery{ReleaseID: r.ID, Limit: 50})
	for _, e := range evs.Events {
		if e.Type == "pod.terminated" && (e.Severity != "info" || !strings.Contains(e.Summary, "replaced by a newer revision")) {
			t.Fatalf("an ordinary rollout termination was classified as trouble: %+v", e)
		}
	}
	if got := relOf(t, db, "42"); got.Health == store.HealthDegraded {
		t.Fatalf("a rollout termination degraded the old release: %+v", got)
	}
}

func TestPodVanishedWhileOpsTideWasDownIsRecordedAsUnknown(t *testing.T) {
	db := openStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	// First run: a Pod is recorded as live.
	c1 := fake.NewClientset(replicaSet("42", 1, now, "api:1"), deployment("42", 1, 1, 1, completeCond(now)), deployPod("42", "api-42-a", now, true, corev1.ContainerStatus{Name: "api"}))
	col := New(c1, db, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = col.Run(ctx) }()
	eventually(t, "synced", func() bool { return col.Synced(ctx) == nil })
	eventually(t, "pod", func() bool { ps, _ := db.LivePods(context.Background()); return len(ps) == 1 })
	cancel()
	<-done
	// Second run: the Pod no longer exists.
	c2 := fake.NewClientset(replicaSet("42", 1, now, "api:1"), deployment("42", 1, 1, 1, completeCond(now)))
	col2 := New(c2, db, Options{})
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() { defer close(done2); _ = col2.Run(ctx2) }()
	defer func() { cancel2(); <-done2 }()
	eventually(t, "unknown end", func() bool {
		r := relOf(t, db, "42")
		return contains(entryTypes(t, db, r.ID), "pod.terminated") == 1
	})
	r := relOf(t, db, "42")
	evs, _ := db.Events(context.Background(), store.EventQuery{ReleaseID: r.ID, Limit: 50})
	for _, e := range evs.Events {
		if e.Type == "pod.terminated" && (e.Confidence != store.Unknown || !strings.Contains(e.Summary, "not known")) {
			t.Fatalf("%+v", e)
		}
	}
	if ps, _ := db.LivePods(context.Background()); len(ps) != 0 {
		t.Fatal("the vanished Pod is still live")
	}
}
