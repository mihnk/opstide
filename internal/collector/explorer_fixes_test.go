package collector

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func nodeNamed(name, uid string, created time.Time) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(uid), CreationTimestamp: metav1.NewTime(created)}}
}

// Without Node observation OpsTide knows nothing about which Node has a name, so an Event that names a Node is not
// linked to one, even when an earlier record of a Node of that name exists.
func TestNodeEventsAreNotLinkedByNameWhenNodesAreNotObserved(t *testing.T) {
	db := openStore(t)
	client, ctx := start(t, db, Options{})
	nev := podEvent("node-a.ready", "ev-n1", "node-a", "NodeNotReady", time.Now())
	nev.Regarding.Kind, nev.Regarding.UID, nev.Namespace = "Node", "node-a", "default"
	if _, err := client.EventsV1().Events("default").Create(ctx, nev, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "evidence", func() bool { _, ok := evidenceOf(t, db, "ev-n1"); return ok })
	if e, _ := evidenceOf(t, db, "ev-n1"); e.NodeID != "" {
		t.Fatalf("no Node is observed, so the name is not a reference to one: %+v", e)
	}
}

// A Node recreated under the same name is another Node: an Event from the time of the earlier one is linked to it, an
// Event from before either existed is left unresolved.
func TestNodeEventsFollowTheLifetimeOfTheNodeOfThatName(t *testing.T) {
	db := openStore(t)
	client, ctx := start(t, db, Options{ObserveNodes: true})
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := client.CoreV1().Nodes().Create(ctx, nodeNamed("node-a", "n-1", now.Add(-10*time.Hour)), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "first node", func() bool { _, ok, _ := db.NodeByUID(ctx, "n-1"); return ok })
	_ = client.CoreV1().Nodes().Delete(ctx, "node-a", metav1.DeleteOptions{})
	eventually(t, "removed", func() bool { n, _, _ := db.NodeByUID(ctx, "n-1"); return !n.DeletedAt.IsZero() })
	if _, err := client.CoreV1().Nodes().Create(ctx, nodeNamed("node-a", "n-2", now), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "second node", func() bool { _, ok, _ := db.NodeByUID(ctx, "n-2"); return ok })
	for id, at := range map[string]time.Time{"ev-early": now.Add(-6 * time.Hour), "ev-before": now.Add(-30 * time.Hour)} {
		nev := podEvent("node-a."+id, id, "node-a", "NodeNotReady", at)
		nev.Regarding.Kind, nev.Regarding.UID, nev.Namespace = "Node", "node-a", "default"
		if _, err := client.EventsV1().Events("default").Create(ctx, nev, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "events", func() bool {
		_, a := evidenceOf(t, db, "ev-early")
		_, b := evidenceOf(t, db, "ev-before")
		return a && b
	})
	if e, _ := evidenceOf(t, db, "ev-early"); e.NodeID != "n-1" {
		t.Fatalf("an Event from the earlier Node's time belongs to it, not to the one recreated later: %+v", e)
	}
	if e, _ := evidenceOf(t, db, "ev-before"); e.NodeID != "" {
		t.Fatalf("an Event from before any Node of that name existed stays unresolved: %+v", e)
	}
}

// A StatefulSet that was already there when OpsTide began watching is not "changed now": its last change is a baseline
// taken from the source's own times.
func TestAdoptedWorkloadsGetABaselineNotAChangeNow(t *testing.T) {
	db := openStore(t)
	created := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Second)
	rev := created.Add(24 * time.Hour)
	sts := statefulSet(1, "web-aaa", "web-aaa", 1, 1, appsv1.RollingUpdateStatefulSetStrategyType, 0, 1, 1)
	sts.CreationTimestamp = metav1.NewTime(created)
	_, ctx := start(t, db, Options{}, sts, controllerRevision("StatefulSet", "web", "sts-1", "aaa", 1, "web:1", rev))
	eventually(t, "state", func() bool { return stateOf(t, db, "sts-1").Kind != "" })
	_ = ctx
	st := stateOf(t, db, "sts-1")
	if st.ChangeBasis != "baseline" || !st.LastChangeAt.Equal(rev) {
		t.Fatalf("expected the newest ControllerRevision's time as a baseline: %v %q", st.LastChangeAt, st.ChangeBasis)
	}
}
