package collector

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/mihnk/opstide/internal/store"
)

// When OpsTide gives up writing an object, the record must not claim it watched continuously: the scope's interval is
// closed where the failures began and reopened now, a Log entry names the object, and the count is kept.
func TestGivingUpOnAnObjectRecordsACoverageGap(t *testing.T) {
	db := openStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	clock := now.Add(-time.Hour)
	c := New(fake.NewClientset(), db, Options{Now: func() time.Time { return clock }})
	c.cov.listStarted("deployments")
	c.cov.listDone("deployments") // an interval is open, as after a normal sync, an hour ago
	clock = now
	since := now.Add(-90 * time.Second)

	c.giveUp(item{kind: Deployments, namespace: "shop", name: "api"}, since, errors.New("database is locked"))

	if n, at := c.DroppedObjects(); n != 1 || !at.Equal(now) {
		t.Fatalf("%d %v", n, at)
	}
	ivs, err := db.Coverage(context.Background(), now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var closed, open int
	for _, iv := range ivs {
		if iv.Scope != "deployments" {
			continue
		}
		if iv.EndedAt.IsZero() {
			open++
			if iv.StartedAt.Before(now) {
				t.Fatalf("the interval after the gap starts now, not earlier: %+v", iv)
			}
		} else {
			closed++
			if !iv.EndedAt.Equal(since) {
				t.Fatalf("the earlier interval ends where the failures began: %+v", iv)
			}
		}
	}
	if closed != 1 || open != 1 {
		t.Fatalf("closed %d open %d: %+v", closed, open, ivs)
	}
	got := events(t, db, store.EventQuery{Limit: 20})
	var gap *store.Event
	for _, e := range got {
		if e.Type == "collection.gap" && e.Facts["object"] != "" {
			e := e
			gap = &e
		}
	}
	if gap == nil || gap.Facts["object"] != "deployments shop/api" || gap.Facts["error"] != "database is locked" || gap.Facts["from"] != since.Format(time.RFC3339) {
		t.Fatalf("a Log entry says which object and since when: %+v", gap)
	}
}

// Kubernetes Events and autoscalers wait on their own queue, so a burst of them cannot delay a Job or a Pod.
func TestEventsAndAutoscalersHaveTheirOwnQueue(t *testing.T) {
	c := New(fake.NewClientset(), openStore(t), Options{})
	c.add(item{kind: Events, namespace: "shop", name: "e1"})
	c.add(item{kind: HPAs, namespace: "shop", name: "h1"})
	c.add(item{kind: Jobs, namespace: "shop", name: "j1"})
	c.add(item{kind: DeployPods, namespace: "shop", name: "p1"})
	if c.lowQueue.Len() != 2 || c.queue.Len() != 2 {
		t.Fatalf("low %d, main %d", c.lowQueue.Len(), c.queue.Len())
	}
	if got := c.Stats().Queued; got != 4 {
		t.Fatalf("Queued counts both: %d", got)
	}
}

func mf(manager string, op metav1.ManagedFieldsOperationType, sub, fields string, at time.Time) metav1.ManagedFieldsEntry {
	t := metav1.NewTime(at)
	return metav1.ManagedFieldsEntry{Manager: manager, Operation: op, Subresource: sub, Time: &t, FieldsType: "FieldsV1", FieldsV1: &metav1.FieldsV1{Raw: []byte(fields)}}
}

// The change ledger names the client that wrote an object's spec, from managedFields: only the manager's name, the kind of
// operation and when; writes that touch only status or metadata are not changes of the spec, and no field is kept.
func TestSpecWritesAreRecordedWithTheirFieldManager(t *testing.T) {
	db := openStore(t)
	old := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	d := deployment("1", 3, 3, 3)
	d.ManagedFields = []metav1.ManagedFieldsEntry{
		mf("argocd-controller", metav1.ManagedFieldsOperationApply, "", `{"f:spec":{"f:replicas":{},"f:template":{"f:spec":{"f:containers":{"k:{\"name\":\"app\"}":{"f:image":{}}}}}}}`, old),
		mf("kube-controller-manager", metav1.ManagedFieldsOperationUpdate, "status", `{"f:status":{"f:readyReplicas":{}}}`, old.Add(time.Hour)),
		mf("kube-controller-manager", metav1.ManagedFieldsOperationUpdate, "", `{"f:metadata":{"f:annotations":{"f:deployment.kubernetes.io/revision":{}}}}`, old.Add(2*time.Hour)),
		// Kubernetes gives some writes no time (the scale subresource's): they are left out, not given a date.
		{Manager: "kubectl", Operation: metav1.ManagedFieldsOperationUpdate, Subresource: "scale", FieldsType: "FieldsV1", FieldsV1: &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:replicas":{}}}`)}},
	}
	client, ctx := start(t, db, Options{}, d)
	ledger := func() []store.Event {
		var out []store.Event
		pg, _ := db.Events(ctx, store.EventQuery{Types: []string{"deployment.spec_written"}, Limit: 50})
		out = append(out, pg.Events...)
		return out
	}
	eventually(t, "the argocd write", func() bool { return len(ledger()) == 1 })
	e := ledger()[0]
	if e.Facts["fieldManager"] != "argocd-controller" || e.Facts["operation"] != "Apply" || !e.OccurredAt.Equal(old) || e.Facts["seen"] != "already there when OpsTide began watching" {
		t.Fatalf("%+v", e)
	}
	for k, v := range e.Facts {
		if k == "fields" || len(v) > 80 {
			t.Fatalf("no field set or value is kept: %s=%s", k, v)
		}
	}
	// An object written by another client after OpsTide began is a new entry, seen as it happened. (Added to the
	// tracker directly: the fake clientset's field-managed tracker rewrites managedFields on Create and Update.)
	d2 := deployment("1", 2, 2, 2)
	d2.Name, d2.UID = "web", "dep-2"
	d2.ManagedFields = []metav1.ManagedFieldsEntry{mf("kubectl-edit", metav1.ManagedFieldsOperationUpdate, "", `{"f:spec":{"f:replicas":{}}}`, time.Now().UTC().Add(time.Second).Truncate(time.Second))}
	if err := client.Tracker().Add(d2); err != nil { // as stored: the typed client would rewrite managedFields
		t.Fatal(err)
	}
	eventually(t, "the kubectl write", func() bool { return len(ledger()) == 2 })
	var edit store.Event
	for _, x := range ledger() {
		if x.Facts["fieldManager"] == "kubectl-edit" {
			edit = x
		}
	}
	if edit.Facts["seen"] != "recorded after OpsTide started watching" || edit.ResourceName != "web" {
		t.Fatalf("%+v", edit)
	}
	// Seeing the same writes again (a relist, a restart) adds nothing.
	time.Sleep(200 * time.Millisecond)
	if n := len(ledger()); n != 2 {
		t.Fatalf("%d entries", n)
	}
}
