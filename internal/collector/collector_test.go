package collector

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/mihnk/opstide/internal/store"
)

var (
	t0      = time.Date(2026, 11, 4, 5, 0, 0, 0, time.UTC) // 08:00 Europe/Istanbul
	ptrTrue = func() *bool { b := true; return &b }()
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "opstide.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func start(t *testing.T, db *store.Store, opt Options, objs ...runtime.Object) (*fake.Clientset, context.Context) {
	t.Helper()
	client := fake.NewClientset(objs...)
	// A current cluster serves events.k8s.io/v1.
	client.Resources = []*metav1.APIResourceList{{GroupVersion: "events.k8s.io/v1", APIResources: []metav1.APIResource{{Name: "events", Kind: "Event"}}}}
	c := New(client, db, opt)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = c.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	eventually(t, "synced", func() bool { return c.Synced(ctx) == nil })
	eventually(t, "evidence watches", c.evidenceSynced)
	return client, ctx
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func events(t *testing.T, db *store.Store, q store.EventQuery) map[string]store.Event {
	t.Helper()
	p, err := db.Events(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.Event{}
	for _, e := range p.Events {
		out[e.Type] = e
	}
	return out
}

func cronJob(ns, name, uid string, ann map[string]string) *batchv1.CronJob {
	tz := "Europe/Istanbul"
	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID(uid), Annotations: ann, ResourceVersion: "1",
			CreationTimestamp: metav1.NewTime(t0.AddDate(0, 0, -30))},
		Spec: batchv1.CronJobSpec{Schedule: "*/30 * * * *", TimeZone: &tz, ConcurrencyPolicy: batchv1.ForbidConcurrent},
	}
}

func job(cj *batchv1.CronJob, name, uid string, created time.Time) *batchv1.Job {
	j := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: cj.Namespace, Name: name, UID: types.UID(uid),
		CreationTimestamp: metav1.NewTime(created)}}
	j.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "CronJob", Name: cj.Name, UID: cj.UID, Controller: ptrTrue}}
	return j
}

func pod(j *batchv1.Job, name string, exit int32, at time.Time) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: j.Namespace, Name: name, UID: types.UID("uid-" + name),
			Labels:          map[string]string{"job-name": j.Name},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: j.Name, UID: j.UID, Controller: ptrTrue}}},
		Status: corev1.PodStatus{Phase: corev1.PodFailed, StartTime: &metav1.Time{Time: at.Add(-time.Minute)},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "main", State: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{ExitCode: exit, Reason: "Error", FinishedAt: metav1.NewTime(at)}}}}},
	}
}

func TestRecordsAFailedRunWithItsPods(t *testing.T) {
	db := openStore(t)
	cj := cronJob("payments", "payment-reconciliation", "cj-1", map[string]string{
		annName: "Payment reconciliation", annOwner: "team-payments", annCriticality: "High",
		annRunbook: "https://wiki.example.com/runbooks/payment-reconciliation", annDuration: "4m",
	})
	j := job(cj, "payment-reconciliation-29896140", "job-1", t0.Add(time.Second))
	j.Annotations = map[string]string{annScheduledTimestamp: "2026-11-04T08:00:00+03:00"}
	j.Status = batchv1.JobStatus{Failed: 2, StartTime: &metav1.Time{Time: t0.Add(3 * time.Second)}, Conditions: []batchv1.JobCondition{
		{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue},
		{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded", LastTransitionTime: metav1.NewTime(t0.Add(15*time.Minute + 40*time.Second))},
	}}
	client, ctx := start(t, db, Options{}, cj, j,
		pod(j, "payment-reconciliation-29896140-7xq4d", 1, t0.Add(7*time.Minute)),
		pod(j, "payment-reconciliation-29896140-k8d2q", 1, t0.Add(15*time.Minute)))

	var run store.Run
	eventually(t, "the run", func() bool {
		r, ok, _ := db.Run(ctx, "job-1")
		run = r
		return ok && len(r.Pods) == 2
	})
	if run.Status != store.RunFailed || run.StatusReason != "BackoffLimitExceeded" || run.OperationID != "cj-1" ||
		!run.ScheduledAt.Equal(t0) || run.ScheduledBasis != "annotation" || run.Attempts != 2 ||
		!run.FinishedAt.Equal(t0.Add(15*time.Minute+40*time.Second)) || *run.Pods[0].ExitCode != 1 || run.Pods[0].Name != "payment-reconciliation-29896140-7xq4d" {
		t.Fatalf("run %+v", run)
	}
	op, _, _ := db.Operation(ctx, "cj-1")
	if op.DisplayName != "Payment reconciliation" || op.Criticality != "high" || op.RunbookURL == "" || *op.ExpectedDuration != 4*time.Minute ||
		op.TimeZone != "Europe/Istanbul" || op.ConcurrencyPolicy != "Forbid" || len(op.MetadataProblems) != 0 {
		t.Fatalf("operation %+v", op)
	}
	ev := events(t, db, store.EventQuery{OperationID: "cj-1"})
	failed := ev["job.failed"]
	if failed.Confidence != store.Derived || failed.Severity != "error" || !failed.OccurredAt.Equal(run.FinishedAt) ||
		failed.Facts["reason"] != "BackoffLimitExceeded" || failed.Facts["scheduledAt"] != "2026-11-04T05:00:00Z" ||
		!strings.HasPrefix(failed.Summary, "Payment reconciliation failed after 2 attempts") {
		t.Fatalf("failed entry %+v", failed)
	}
	if ev["job.created"].Confidence != store.Observed || ev["cronjob.created"].ID != "cronjob.created/cj-1" {
		t.Fatalf("entries %+v", ev)
	}

	// Suspending is recorded once, observed.
	cj2 := cj.DeepCopy()
	cj2.Spec.Suspend, cj2.ResourceVersion = ptrTrue, "2"
	if _, err := client.BatchV1().CronJobs("payments").Update(ctx, cj2, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "suspended entry", func() bool { _, ok := events(t, db, store.EventQuery{})["cronjob.suspended"]; return ok })

	// A running Job deleted before it finished: outcome unknown.
	running := job(cj, "payment-reconciliation-29896170", "job-2", t0.Add(30*time.Minute))
	running.Status.Active = 1
	if _, err := client.BatchV1().Jobs("payments").Create(ctx, running, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "running run", func() bool { r, ok, _ := db.Run(ctx, "job-2"); return ok && r.Status == store.RunRunning })
	if err := client.BatchV1().Jobs("payments").Delete(ctx, running.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "lost run", func() bool { r, _, _ := db.Run(ctx, "job-2"); return r.Status == store.RunUnknown })
	if r, _, _ := db.Run(ctx, "job-2"); !r.ScheduledAt.Equal(t0.Add(30*time.Minute)) || r.ScheduledBasis != "name" {
		t.Fatalf("scheduled from the name: %+v", r)
	}

	// Deleting the finished Job (history limit) changes nothing about it.
	if err := client.BatchV1().Jobs("payments").Delete(ctx, j.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := client.BatchV1().CronJobs("payments").Delete(ctx, cj.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "deleted operation", func() bool { op, _, _ := db.Operation(ctx, "cj-1"); return !op.DeletedAt.IsZero() })
	if r, _, _ := db.Run(ctx, "job-1"); r.Status != store.RunFailed || len(r.Pods) != 2 {
		t.Fatalf("finished run changed: %+v", r)
	}
	if e := events(t, db, store.EventQuery{})["cronjob.deleted"]; e.Confidence != store.Observed {
		t.Fatalf("deleted entry %+v", e)
	}
}

func TestReconcilesWhatVanishedWhileStopped(t *testing.T) {
	db := openStore(t)
	ctx := context.Background()
	if err := db.Update(ctx, func(w *store.Writer) error {
		if err := w.UpsertOperation(ctx, store.Operation{ID: "gone", Namespace: "data", Name: "backup", Schedule: "@daily",
			CreatedAt: t0, FirstSeenAt: t0, LastSeenAt: t0}); err != nil {
			return err
		}
		return w.UpsertRun(ctx, store.Run{ID: "active", OperationID: "gone", Namespace: "data", JobName: "backup-1",
			CreatedAt: t0, Status: store.RunRunning, ObservedAt: t0})
	}); err != nil {
		t.Fatal(err)
	}
	start(t, db, Options{})
	if op, _, _ := db.Operation(ctx, "gone"); op.DeletedAt.IsZero() {
		t.Fatal("operation not marked deleted")
	}
	if r, _, _ := db.Run(ctx, "active"); r.Status != store.RunUnknown {
		t.Fatalf("run %+v", r)
	}
	ev := events(t, db, store.EventQuery{})
	if ev["cronjob.deleted"].Confidence != store.Unknown || ev["job.lost"].Confidence != store.Unknown {
		t.Fatalf("entries %+v", ev)
	}
}

func TestNamespacedCollectionIgnoresOtherNamespaces(t *testing.T) {
	db := openStore(t)
	_, ctx := start(t, db, Options{Namespaces: []string{"payments"}},
		cronJob("payments", "a", "in", nil), cronJob("data", "b", "out", nil))
	eventually(t, "operation", func() bool { _, ok, _ := db.Operation(ctx, "in"); return ok })
	if _, ok, _ := db.Operation(ctx, "out"); ok {
		t.Fatal("recorded a CronJob from an unwatched namespace")
	}
	ivs, _ := db.Coverage(ctx, t0.AddDate(-1, 0, 0), time.Now().Add(time.Hour))
	scopes := map[string]bool{}
	for _, iv := range ivs {
		scopes[iv.Scope] = true
	}
	// Four before releases; ReplicaSets and Deployment Pods add two, Events and
	// autoscalers two more, StatefulSets, DaemonSets, ControllerRevisions and their Pods four more,
	// all only in payments. Nodes are cluster-scoped and off unless asked for.
	if !scopes["jobs/payments"] || !scopes["cronjobs/payments"] || !scopes["replicasets/payments"] || !scopes["deploypods/payments"] ||
		!scopes["events/payments"] || !scopes["hpas/payments"] || !scopes["statefulsets/payments"] || !scopes["daemonsets/payments"] ||
		!scopes["controllerrevisions/payments"] || !scopes["workloadpods/payments"] || scopes["nodes"] || len(scopes) != 12 {
		t.Fatalf("coverage scopes %v", scopes)
	}
}

func TestRolloutRecordedOnceWhenComplete(t *testing.T) {
	db := openStore(t)
	two := int32(2)
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "payments", Name: "payments-api", UID: "d-1", Generation: 7,
			Annotations: map[string]string{annRevision: "43"}},
		Spec: appsv1.DeploymentSpec{Replicas: &two, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Image: "ghcr.io/example/payments-api:1.8.0"}}}}},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 7, Replicas: 3, UpdatedReplicas: 2, AvailableReplicas: 2,
			Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue, Reason: "ReplicaSetUpdated"}}},
	}
	client, ctx := start(t, db, Options{}, d)
	time.Sleep(100 * time.Millisecond)
	if _, ok := events(t, db, store.EventQuery{})["deployment.rollout"]; ok {
		t.Fatal("rollout recorded while still in progress")
	}
	d2 := d.DeepCopy()
	d2.Status.Replicas = 2
	d2.Status.Conditions[0].Reason = "NewReplicaSetAvailable"
	d2.Status.Conditions[0].LastUpdateTime = metav1.NewTime(t0.Add(2 * time.Minute))
	if _, err := client.AppsV1().Deployments("payments").UpdateStatus(ctx, d2, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "rollout", func() bool { _, ok := events(t, db, store.EventQuery{})["deployment.rollout"]; return ok })
	e := events(t, db, store.EventQuery{})["deployment.rollout"]
	if e.ID != "deployment.rollout/d-1/43" || !e.OccurredAt.Equal(t0.Add(2*time.Minute)) || e.Facts["images"] != "ghcr.io/example/payments-api:1.8.0" {
		t.Fatalf("rollout %+v", e)
	}
}

func TestDescribeKeepsOnlySafeValues(t *testing.T) {
	var op store.Operation
	describe(&op, map[string]string{
		annName:        strings.Repeat("x", 130),
		annRunbook:     "javascript:alert(1)",
		annCriticality: "urgent",
		annDuration:    "forever",
		annDescription: "  Matches settlements.  ",
	})
	if len([]rune(op.DisplayName)) != 120 || op.RunbookURL != "" || op.Criticality != "" || op.ExpectedDuration != nil ||
		op.Description != "Matches settlements." || len(op.MetadataProblems) != 4 {
		t.Fatalf("%+v", op)
	}
	op = store.Operation{}
	describe(&op, map[string]string{annRunbook: "http://wiki/x"})
	if op.RunbookURL != "" || len(op.MetadataProblems) != 1 {
		t.Fatalf("http runbook kept: %+v", op)
	}
}
