package collector

import (
	"context"
	"errors"
	"testing"
	"time"

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
