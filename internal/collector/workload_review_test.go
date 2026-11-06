package collector

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/mihnk/opstide/internal/release"
	"github.com/mihnk/opstide/internal/store"
)

// clock is a settable time source for the collector.
type clock struct{ ms atomic.Int64 }

func newClock(t time.Time) *clock { c := &clock{}; c.ms.Store(t.UnixMilli()); return c }
func (c *clock) now() time.Time   { return time.UnixMilli(c.ms.Load()).UTC() }
func (c *clock) add(d time.Duration) {
	c.ms.Add(d.Milliseconds())
}

func setRev(rs *appsv1.ReplicaSet, rev string) *appsv1.ReplicaSet {
	out := rs.DeepCopy()
	out.Annotations[release.AnnRevision] = rev
	return out
}

func healthOf(t *testing.T, db *store.Store, rev string) string { return relOf(t, db, rev).Health }

// The controller creates the new ReplicaSet before it updates the Deployment's
// revision, on separate watches. The new release must not be called superseded
// in between.
func TestAReplicaSetAheadOfItsDeploymentIsNotAlreadyReplaced(t *testing.T) {
	db := openStore(t)
	client, ctx := start(t, db, Options{})
	now := time.Now().UTC().Truncate(time.Second)
	_, _ = client.AppsV1().ReplicaSets("shop").Create(ctx, replicaSet("42", 1, now, "api:1"), metav1.CreateOptions{})
	_, _ = client.AppsV1().Deployments("shop").Create(ctx, deployment("42", 1, 1, 1, completeCond(now)), metav1.CreateOptions{})
	eventually(t, "42", func() bool { return hasRelease(db, "42") && relOf(t, db, "42").State == store.ReleaseCompleted })

	// Revision 43's ReplicaSet and a Pod arrive; the Deployment still says 42.
	_, _ = client.AppsV1().ReplicaSets("shop").Create(ctx, replicaSet("43", 1, now.Add(time.Minute), "api:2"), metav1.CreateOptions{})
	_, _ = client.CoreV1().Pods("shop").Create(ctx, deployPod("43", "api-43-a", now.Add(time.Minute), true, corev1.ContainerStatus{Name: "api"}), metav1.CreateOptions{})
	time.Sleep(300 * time.Millisecond)
	if hasRelease(db, "43") {
		t.Fatal("a release was made for a revision the Deployment has not reached")
	}
	if got := relOf(t, db, "42"); !got.SupersededAt.IsZero() {
		t.Fatalf("42 was superseded by a revision that is not current yet: %+v", got)
	}
	// The Deployment catches up.
	_, _ = client.AppsV1().Deployments("shop").Update(ctx, deployment("43", 1, 1, 1, completeCond(now.Add(2*time.Minute))), metav1.UpdateOptions{})
	eventually(t, "43", func() bool { return hasRelease(db, "43") })
	r43 := relOf(t, db, "43")
	if !r43.SupersededAt.IsZero() || r43.State == store.ReleaseSuperseded || !strings.Contains(r43.Snapshot, `"deployment"`) {
		t.Fatalf("43 must be current, with the Deployment's own settings captured: %+v", r43)
	}
	// The Pod that came first is read again once the release exists.
	eventually(t, "the early pod", func() bool { ps, _ := db.ReleasePods(ctx, r43); return len(ps) == 1 })
	if got := relOf(t, db, "42"); got.SupersededAt.IsZero() {
		t.Fatal("42 is not superseded once 43 is current")
	}
}

// A rollback reuses the old ReplicaSet under a new revision. The bad revision keeps
// its verdict, the running Pods count for the new release, and the entry says rollback.
func TestRollbackKeepsTheBadVerdictAndMovesTheRunningPodsToTheNewRelease(t *testing.T) {
	db := openStore(t)
	client, ctx := start(t, db, Options{})
	now := time.Now().UTC().Truncate(time.Second)
	rsA := replicaSet("42", 1, now.Add(-time.Hour), "api:1") // created long before: an old ReplicaSet
	_, _ = client.AppsV1().ReplicaSets("shop").Create(ctx, rsA, metav1.CreateOptions{})
	_, _ = client.AppsV1().Deployments("shop").Create(ctx, deployment("42", 1, 1, 1, completeCond(now)), metav1.CreateOptions{})
	eventually(t, "42", func() bool { return hasRelease(db, "42") })
	podA := deployPod("42", "api-42-a", now, true, corev1.ContainerStatus{Name: "api"})
	_, _ = client.CoreV1().Pods("shop").Create(ctx, podA, metav1.CreateOptions{})
	eventually(t, "pod A", func() bool { ps, _ := db.ReleasePods(ctx, relOf(t, db, "42")); return len(ps) == 1 }) //nolint:contextcheck // a test helper that reads the store itself

	// Revision 43 (ReplicaSet B) is rolled out and a container is OOMKilled.
	_, _ = client.AppsV1().ReplicaSets("shop").Create(ctx, replicaSet("43", 1, now.Add(time.Minute), "api:2"), metav1.CreateOptions{})
	_, _ = client.AppsV1().Deployments("shop").Update(ctx, deployment("43", 1, 1, 1, completeCond(now.Add(2*time.Minute))), metav1.UpdateOptions{})
	eventually(t, "43", func() bool { return hasRelease(db, "43") })
	podB := deployPod("43", "api-43-b", now.Add(time.Minute), true, corev1.ContainerStatus{Name: "api"})
	_, _ = client.CoreV1().Pods("shop").Create(ctx, podB, metav1.CreateOptions{})
	eventually(t, "pod B", func() bool { ps, _ := db.ReleasePods(ctx, relOf(t, db, "43")); return len(ps) == 1 }) //nolint:contextcheck // a test helper that reads the store itself
	oom := &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.NewTime(now.Add(3 * time.Minute))}
	pb2 := podB.DeepCopy()
	pb2.ResourceVersion = "9"
	pb2.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "api", RestartCount: 1, LastTerminationState: corev1.ContainerState{Terminated: oom}}}
	_, _ = client.CoreV1().Pods("shop").UpdateStatus(ctx, pb2, metav1.UpdateOptions{})
	eventually(t, "43 degraded", func() bool { return healthOf(t, db, "43") == store.HealthDegraded })

	// kubectl rollout undo: ReplicaSet A gets revision 44, Deployment says 44.
	_, _ = client.AppsV1().ReplicaSets("shop").Update(ctx, setRev(rsA, "44"), metav1.UpdateOptions{})
	_, _ = client.AppsV1().Deployments("shop").Update(ctx, deployment("44", 1, 1, 1, completeCond(now.Add(5*time.Minute))), metav1.UpdateOptions{})
	eventually(t, "44", func() bool { return hasRelease(db, "44") })
	r43, r44 := relOf(t, db, "43"), relOf(t, db, "44")
	if r43.SupersededAt.IsZero() || r43.SupersededAt.Before(r43.ObservedAt) {
		t.Fatalf("43 was superseded at %v, before it was observed at %v: a rollback must not date it from the old ReplicaSet", r43.SupersededAt, r43.ObservedAt)
	}
	if r43.Health != store.HealthDegraded {
		t.Fatalf("the release the operator rolled back from lost its verdict: %s %s", r43.Health, r43.HealthReasons)
	}
	if r44.Origin != store.OriginObserved || r44.PrevID != r43.ID || r44.ReplicaSetUID != "rs-42" {
		t.Fatalf("44: %+v", r44)
	}
	ps, _ := db.ReleasePods(ctx, r44)
	if len(ps) != 1 || ps[0].Name != "api-42-a" {
		t.Fatalf("the Pods that kept running belong to the release now serving: %+v", ps)
	}
	if got := entryTypes(t, db, r44.ID); contains(got, "release.observed") != 1 {
		t.Fatalf("%v", got)
	}
	evs, _ := db.Events(ctx, store.EventQuery{ReleaseID: r44.ID, Limit: 20})
	rolled := false
	for _, e := range evs.Events {
		rolled = rolled || (e.Type == "release.observed" && strings.Contains(e.Summary, "rolled back to the ReplicaSet of revision 42"))
	}
	if !rolled {
		t.Fatal("the entry does not say it is a rollback to an earlier ReplicaSet")
	}
	// A restart of the Pod that kept running is the new release's.
	pa2 := podA.DeepCopy()
	pa2.ResourceVersion = "10"
	pa2.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "api", RestartCount: 1, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1, FinishedAt: metav1.NewTime(now.Add(6 * time.Minute))}}}}
	_, _ = client.CoreV1().Pods("shop").UpdateStatus(ctx, pa2, metav1.UpdateOptions{})
	eventually(t, "restart on 44", func() bool { return contains(entryTypes(t, db, r44.ID), "pod.restarted") == 1 })
	if contains(entryTypes(t, db, relOf(t, db, "42").ID), "pod.restarted") != 0 {
		t.Fatal("the restart was written on the old release")
	}
}

// A settled verdict does not change later: not when a Pod is evicted a day on, and not
// because a Deployment update runs the assessment again.
func TestAClosedWindowFreezesTheVerdict(t *testing.T) {
	db := openStore(t)
	clk := newClock(time.Now().UTC().Truncate(time.Second))
	start0 := clk.now()
	client, ctx := start(t, db, Options{Now: clk.now, ReleaseWindow: 5 * time.Minute})
	_, _ = client.AppsV1().ReplicaSets("shop").Create(ctx, replicaSet("42", 1, start0, "api:1"), metav1.CreateOptions{})
	_, _ = client.AppsV1().Deployments("shop").Create(ctx, deployment("42", 1, 1, 1, completeCond(start0)), metav1.CreateOptions{})
	eventually(t, "42", func() bool { return hasRelease(db, "42") })
	p := deployPod("42", "api-42-a", start0, true, corev1.ContainerStatus{Name: "api"})
	_, _ = client.CoreV1().Pods("shop").Create(ctx, p, metav1.CreateOptions{})
	eventually(t, "pod", func() bool { ps, _ := db.ReleasePods(ctx, relOf(t, db, "42")); return len(ps) == 1 }) //nolint:contextcheck // a test helper that reads the store itself
	if h := healthOf(t, db, "42"); h != store.HealthObserving {
		t.Fatalf("mid-window: %s", h)
	}
	if contains(entryTypes(t, db, relOf(t, db, "42").ID), "release.health.insufficient") != 0 {
		t.Fatal("a transient shortage of evidence mid-window was written as a verdict")
	}
	// The window closes (any Deployment update runs the assessment).
	clk.add(10 * time.Minute)
	d := deployment("42", 1, 1, 1, completeCond(start0))
	d.Status.ReadyReplicas = 1
	d.ResourceVersion = "2"
	_, _ = client.AppsV1().Deployments("shop").UpdateStatus(ctx, d, metav1.UpdateOptions{})
	eventually(t, "healthy", func() bool { return healthOf(t, db, "42") == store.HealthHealthy })
	// A day later a Pod is evicted, and the Deployment is touched again.
	clk.add(24 * time.Hour)
	ev := p.DeepCopy()
	ev.ResourceVersion = "5"
	ev.Status.Phase, ev.Status.Reason = corev1.PodFailed, "Evicted"
	_, _ = client.CoreV1().Pods("shop").UpdateStatus(ctx, ev, metav1.UpdateOptions{})
	d.ResourceVersion = "3"
	_, _ = client.AppsV1().Deployments("shop").UpdateStatus(ctx, d, metav1.UpdateOptions{})
	eventually(t, "the eviction is recorded", func() bool { return contains(entryTypes(t, db, relOf(t, db, "42").ID), "pod.evicted") == 1 })
	time.Sleep(300 * time.Millisecond)
	if got := healthOf(t, db, "42"); got != store.HealthHealthy {
		t.Fatalf("a settled verdict changed a day later: %s", got)
	}
	if contains(entryTypes(t, db, relOf(t, db, "42").ID), "release.health.degraded") != 0 {
		t.Fatal("a late eviction was written as degradation 'during observation'")
	}
}

// A rollout that was stalled and later completed is not a clean one.
func TestAStalledRolloutThatRecoversIsNotHealthy(t *testing.T) {
	db := openStore(t)
	clk := newClock(time.Now().UTC().Truncate(time.Second))
	t0 := clk.now()
	client, ctx := start(t, db, Options{Now: clk.now, ReleaseWindow: 5 * time.Minute})
	_, _ = client.AppsV1().ReplicaSets("shop").Create(ctx, replicaSet("42", 1, t0, "api:1"), metav1.CreateOptions{})
	stalled := deployment("42", 1, 0, 0, appsv1.DeploymentCondition{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded", Message: "timed out", LastUpdateTime: metav1.NewTime(t0)})
	_, _ = client.AppsV1().Deployments("shop").Create(ctx, stalled, metav1.CreateOptions{})
	eventually(t, "stalled", func() bool { return hasRelease(db, "42") && relOf(t, db, "42").State == store.ReleaseStalled })
	_, _ = client.CoreV1().Pods("shop").Create(ctx, deployPod("42", "api-42-a", t0, true, corev1.ContainerStatus{Name: "api"}), metav1.CreateOptions{})
	clk.add(time.Minute)
	done := deployment("42", 1, 1, 1, completeCond(clk.now()))
	done.ResourceVersion = "2"
	_, _ = client.AppsV1().Deployments("shop").UpdateStatus(ctx, done, metav1.UpdateOptions{})
	eventually(t, "completed", func() bool { return relOf(t, db, "42").State == store.ReleaseCompleted })
	clk.add(10 * time.Minute)
	done.ResourceVersion = "3"
	_, _ = client.AppsV1().Deployments("shop").UpdateStatus(ctx, done, metav1.UpdateOptions{})
	time.Sleep(300 * time.Millisecond)
	r := relOf(t, db, "42")
	if r.Health != store.HealthDegraded || !strings.Contains(r.HealthReasons, "stalled") {
		t.Fatalf("a rollout that had to be given up on counted as clean: %s %s", r.Health, r.HealthReasons)
	}
	if contains(entryTypes(t, db, r.ID), "release.health.healthy") != 0 {
		t.Fatal("a healthy entry beside the degraded one")
	}
}

// kubectl rollout restart makes a new ReplicaSet with the same settings: not a rollback.
func TestRolloutRestartIsNotCalledARollback(t *testing.T) {
	db := openStore(t)
	client, ctx := start(t, db, Options{})
	now := time.Now().UTC().Truncate(time.Second)
	_, _ = client.AppsV1().ReplicaSets("shop").Create(ctx, replicaSet("42", 1, now, "api:1"), metav1.CreateOptions{})
	_, _ = client.AppsV1().Deployments("shop").Create(ctx, deployment("42", 1, 1, 1, completeCond(now)), metav1.CreateOptions{})
	eventually(t, "42", func() bool { return hasRelease(db, "42") })
	_, _ = client.AppsV1().ReplicaSets("shop").Create(ctx, replicaSet("43", 1, now.Add(time.Minute), "api:1"), metav1.CreateOptions{}) // identical template
	_, _ = client.AppsV1().Deployments("shop").Update(ctx, deployment("43", 1, 1, 1, completeCond(now.Add(2*time.Minute))), metav1.UpdateOptions{})
	eventually(t, "43", func() bool { return hasRelease(db, "43") })
	if a, b := relOf(t, db, "42"), relOf(t, db, "43"); a.Fingerprint != b.Fingerprint {
		t.Fatal("the fixture should have equal allow-listed settings")
	}
	evs, _ := db.Events(ctx, store.EventQuery{ReleaseID: relOf(t, db, "43").ID, Limit: 20})
	for _, e := range evs.Events {
		if e.Type == "release.observed" && strings.Contains(e.Summary, "rolled back") {
			t.Fatalf("a restart was called a rollback: %s", e.Summary)
		}
	}
}

// A rollback to a ReplicaSet that predates OpsTide was still watched live.
func TestRollbackToAnOldReplicaSetIsObservedNotAdopted(t *testing.T) {
	db := openStore(t)
	old := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Second)
	now := time.Now().UTC().Truncate(time.Second)
	rsA := replicaSet("4", 1, old, "api:1")
	client, ctx := start(t, db, Options{}, rsA, deployment("4", 1, 1, 1, completeCond(old)))
	eventually(t, "adopted 4", func() bool { return hasRelease(db, "4") })
	if relOf(t, db, "4").Origin != store.OriginAdopted {
		t.Fatal("the first release should be adopted")
	}
	_, _ = client.AppsV1().ReplicaSets("shop").Create(ctx, replicaSet("5", 1, now, "api:2"), metav1.CreateOptions{})
	_, _ = client.AppsV1().Deployments("shop").Update(ctx, deployment("5", 1, 1, 1, completeCond(now)), metav1.UpdateOptions{})
	eventually(t, "5", func() bool { return hasRelease(db, "5") })
	_, _ = client.AppsV1().ReplicaSets("shop").Update(ctx, setRev(rsA, "6"), metav1.UpdateOptions{})
	_, _ = client.AppsV1().Deployments("shop").Update(ctx, deployment("6", 1, 1, 1, completeCond(now.Add(time.Minute))), metav1.UpdateOptions{})
	eventually(t, "6", func() bool { return hasRelease(db, "6") })
	if got := relOf(t, db, "6"); got.Origin != store.OriginObserved || got.Health == store.HealthInsufficient && strings.Contains(got.HealthReasons, "before OpsTide") {
		t.Fatalf("a rollback that happened while OpsTide watched was adopted: %+v", got)
	}
}

// Deleting a Deployment ends the observation of its releases.
func TestDeletingADeploymentSettlesItsReleases(t *testing.T) {
	db := openStore(t)
	client, ctx := start(t, db, Options{})
	now := time.Now().UTC().Truncate(time.Second)
	_, _ = client.AppsV1().ReplicaSets("shop").Create(ctx, replicaSet("42", 1, now, "api:1"), metav1.CreateOptions{})
	_, _ = client.AppsV1().Deployments("shop").Create(ctx, deployment("42", 1, 1, 1, completeCond(now)), metav1.CreateOptions{})
	eventually(t, "42", func() bool { return hasRelease(db, "42") && healthOf(t, db, "42") == store.HealthObserving })
	if err := client.AppsV1().Deployments("shop").Delete(ctx, "api", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "settled", func() bool { return healthOf(t, db, "42") != store.HealthObserving })
	if d, _ := db.WorkloadDeleted(ctx, "dep-1"); d.IsZero() {
		t.Fatal("the workload is not marked deleted")
	}
}

// settled decides which releases are judged again, and with it which verdicts can never
// change: a closed window freezes the verdict, an adopted release is never judged, an open
// one keeps being judged, and a degraded one keeps gaining reasons only while its window is open.
func TestSettled(t *testing.T) {
	now := time.Now()
	until := func(d time.Duration) time.Time { return now.Add(d) }
	for name, tc := range map[string]struct {
		r    store.Release
		want bool
	}{
		"adopted":                           {store.Release{Origin: store.OriginAdopted, Health: store.HealthInsufficient}, true},
		"still observing":                   {store.Release{Origin: store.OriginObserved, Health: store.HealthObserving}, false},
		"healthy":                           {store.Release{Origin: store.OriginObserved, Health: store.HealthHealthy}, true},
		"insufficient":                      {store.Release{Origin: store.OriginObserved, Health: store.HealthInsufficient}, true},
		"degraded, window open":             {store.Release{Origin: store.OriginObserved, Health: store.HealthDegraded, ObserveUntil: until(time.Minute)}, false},
		"degraded, window closed":           {store.Release{Origin: store.OriginObserved, Health: store.HealthDegraded, ObserveUntil: until(-time.Minute)}, true},
		"degraded, never completed":         {store.Release{Origin: store.OriginObserved, Health: store.HealthDegraded}, false},
		"degraded, replaced before closing": {store.Release{Origin: store.OriginObserved, Health: store.HealthDegraded, SupersededAt: until(-time.Hour)}, true},
	} {
		if got := settled(tc.r, now); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

// While a ReplicaSet already carries a new revision and the Deployment does not yet say so, the
// Pods of that ReplicaSet are not written to any release: not even the one it had before. A
// rollback is such a case, and the Pod's event must land on the new release, not the old.
func TestRollbackPodEventWaitsForTheDeploymentToCatchUp(t *testing.T) {
	db := openStore(t)
	client, ctx := start(t, db, Options{})
	now := time.Now().UTC().Truncate(time.Second)
	rsA := replicaSet("42", 1, now.Add(-time.Hour), "api:1")
	_, _ = client.AppsV1().ReplicaSets("shop").Create(ctx, rsA, metav1.CreateOptions{})
	_, _ = client.AppsV1().Deployments("shop").Create(ctx, deployment("42", 1, 1, 1, completeCond(now)), metav1.CreateOptions{})
	eventually(t, "42", func() bool { return hasRelease(db, "42") })
	podA := deployPod("42", "api-42-a", now, true, corev1.ContainerStatus{Name: "api"})
	_, _ = client.CoreV1().Pods("shop").Create(ctx, podA, metav1.CreateOptions{})
	eventually(t, "pod A", func() bool { ps, _ := db.ReleasePods(ctx, relOf(t, db, "42")); return len(ps) == 1 }) //nolint:contextcheck // a test helper that reads the store itself

	// The ReplicaSet is given revision 44 first; the Deployment still says 42. Its Pod restarts.
	_, _ = client.AppsV1().ReplicaSets("shop").Update(ctx, setRev(rsA, "44"), metav1.UpdateOptions{})
	pa2 := podA.DeepCopy()
	pa2.ResourceVersion = "10"
	pa2.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "api", RestartCount: 1, LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1, FinishedAt: metav1.NewTime(now.Add(time.Minute))}}}}
	_, _ = client.CoreV1().Pods("shop").UpdateStatus(ctx, pa2, metav1.UpdateOptions{})
	time.Sleep(400 * time.Millisecond)
	if n := contains(entryTypes(t, db, relOf(t, db, "42").ID), "pod.restarted"); n != 0 {
		t.Fatal("the restart was written on the old release while the Deployment was still catching up")
	}
	// The Deployment catches up; the retried Pod lands on the new release only.
	_, _ = client.AppsV1().Deployments("shop").Update(ctx, deployment("44", 1, 1, 1, completeCond(now.Add(2*time.Minute))), metav1.UpdateOptions{})
	eventually(t, "44", func() bool { return hasRelease(db, "44") })
	eventually(t, "restart on 44", func() bool { return contains(entryTypes(t, db, relOf(t, db, "44").ID), "pod.restarted") == 1 })
	if contains(entryTypes(t, db, relOf(t, db, "42").ID), "pod.restarted") != 0 {
		t.Fatal("the restart ended up on the old release")
	}
}

// A Pod's last state is kept until its end has been written: when the write fails, the retry
// still has it.
func TestPodStashSurvivesAFailedWrite(t *testing.T) {
	db := openStore(t)
	c := New(fake.NewClientset(), db, Options{})
	p := deployPod("42", "api-42-a", time.Now(), true)
	c.gone.Store(string(p.UID), p)
	_ = db.Close() // every write now fails
	err := c.podGone(context.Background(), item{kind: DeployPods, namespace: "shop", name: p.Name, uid: p.UID})
	if err == nil {
		t.Fatal("the write could not have succeeded")
	}
	if _, ok := c.gone.Load(string(p.UID)); !ok {
		t.Fatal("the stash was dropped although the end was not written")
	}
}

// A Deployment deleted while OpsTide was not running is noticed at the next start: its
// releases stop being observed, and the deletion is dated by the time it was noticed.
func TestDeploymentDeletedWhileOpsTideWasDownIsSettledAtStart(t *testing.T) {
	db := openStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	c1 := fake.NewClientset(replicaSet("42", 1, now, "api:1"), deployment("42", 1, 1, 1, completeCond(now)))
	col := New(c1, db, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = col.Run(ctx) }()
	eventually(t, "synced", func() bool { return col.Synced(ctx) == nil })
	eventually(t, "42", func() bool { return hasRelease(db, "42") && healthOf(t, db, "42") == store.HealthObserving })
	cancel()
	<-done

	// Second run: the Deployment no longer exists.
	col2 := New(fake.NewClientset(), db, Options{})
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() { defer close(done2); _ = col2.Run(ctx2) }()
	defer func() { cancel2(); <-done2 }()
	eventually(t, "settled", func() bool { return healthOf(t, db, "42") != store.HealthObserving })
	if d, _ := db.WorkloadDeleted(context.Background(), "dep-1"); d.IsZero() {
		t.Fatal("the workload is not marked deleted")
	}
	if live, _ := db.LiveWorkloads(context.Background()); len(live) != 0 {
		t.Fatalf("still live: %+v", live)
	}
}
