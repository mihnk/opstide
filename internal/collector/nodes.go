package collector

import (
	"context"
	"fmt"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/mihnk/opstide/internal/store"
)

// Nodes are cluster-scoped. They are watched only when asked for (Options.ObserveNodes),
// which needs list and watch on nodes cluster-wide; nothing else in OpsTide does. What is
// kept is the Node's identity, spec.unschedulable, and the five conditions that describe
// its state, each with the status, reason, message and transition time Kubernetes gave and
// the time OpsTide observed the status. Capacity, addresses, labels, taints and images are
// not read.

// nodeConditionTypes is the allow-list, in the order they are shown.
var nodeConditionTypes = []string{"Ready", "MemoryPressure", "DiskPressure", "PIDPressure", "NetworkUnavailable"}

// NodeCondition is a Node condition as reported, with when OpsTide first saw its status.
type NodeCondition struct {
	Type       string    `json:"type"`
	Status     string    `json:"status"`
	Reason     string    `json:"reason,omitempty"`
	Message    string    `json:"message,omitempty"`
	Transition time.Time `json:"transition,omitzero"` // the source's lastTransitionTime
	Observed   time.Time `json:"observed,omitzero"`   // when OpsTide first saw this status
}

func trimNode(obj any) (any, error) {
	n, ok := obj.(*corev1.Node)
	if !ok {
		return obj, nil
	}
	out := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: n.Name, UID: n.UID, ResourceVersion: n.ResourceVersion, CreationTimestamp: n.CreationTimestamp, DeletionTimestamp: n.DeletionTimestamp},
		Spec: corev1.NodeSpec{Unschedulable: n.Spec.Unschedulable}}
	for _, cd := range n.Status.Conditions {
		for _, t := range nodeConditionTypes {
			if string(cd.Type) == t {
				cd.LastHeartbeatTime = metav1.Time{} // the heartbeat changes constantly and says nothing new
				out.Status.Conditions = append(out.Status.Conditions, cd)
			}
		}
	}
	return out, nil
}

func (c *Collector) addNodeInformer() {
	core := c.client.CoreV1()
	c.nodes = c.optionalInformer("", Nodes, &corev1.Node{}, nil,
		func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return core.Nodes().List(ctx, o)
		},
		func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			return core.Nodes().Watch(ctx, o)
		}, Nodes)
	_ = c.nodes.SetTransform(trimNode)
	c.handle(c.nodes, true, func(o metav1.Object) (item, bool) { return item{kind: Nodes, name: o.GetName()}, true })
}

func nodeEntry(n store.Node, typ, id, severity, summary string, facts map[string]string, occurred, now time.Time) store.Event {
	if facts == nil {
		facts = map[string]string{}
	}
	facts["node"] = n.Name
	return store.Event{ID: "node." + id, Type: "node." + typ, Kind: "Node", ResourceUID: n.UID, ResourceName: n.Name, OccurredAt: occurred, ObservedAt: now,
		Severity: severity, Confidence: store.Observed, Summary: summary, Facts: facts}
}

// syncNode records a Node's state and the changes between two observations of it.
func (c *Collector) syncNode(ctx context.Context, it item) error {
	if c.nodes == nil {
		return nil
	}
	obj, exists, err := c.nodes.GetIndexer().GetByKey(it.name)
	if err != nil {
		return err
	}
	now := c.now()
	var node *corev1.Node
	if exists {
		node, _ = obj.(*corev1.Node)
		if node != nil && it.uid != "" && node.UID != it.uid {
			node = nil // replaced by a Node of the same name
		}
	}
	if node == nil {
		if it.uid == "" {
			return nil
		}
		return c.db.Update(ctx, func(w *store.Writer) error {
			n, ok, err := w.NodeByUID(ctx, string(it.uid))
			if err != nil || !ok || !n.DeletedAt.IsZero() {
				return err
			}
			at := now
			if it.deletedAt > 0 {
				at = msTime(it.deletedAt)
			}
			if err := w.MarkNodeDeleted(ctx, n.UID, at); err != nil {
				return err
			}
			note := ""
			if it.deletedAt == 0 {
				note = " (when it was removed is not known; this is when OpsTide noticed)"
			}
			_, err = c.addEvent(ctx, w, nodeEntry(n, "deleted", "deleted/"+n.UID, "info", fmt.Sprintf("Node %s removed%s", n.Name, note), nil, time.Time{}, now), now)
			return err
		})
	}
	return c.db.Update(ctx, func(w *store.Writer) error {
		prev, had, err := w.NodeByUID(ctx, string(node.UID))
		if err != nil {
			return err
		}
		next := store.Node{UID: string(node.UID), Name: node.Name, FirstSeenAt: now, LastSeenAt: now, CreatedAt: mt(&node.CreationTimestamp), Unschedulable: node.Spec.Unschedulable}
		oldByType := map[string]NodeCondition{}
		if had {
			next.FirstSeenAt, next.Origin = prev.FirstSeenAt, prev.Origin
			for _, cd := range decodeJSON[NodeCondition](prev.Conditions) {
				oldByType[cd.Type] = cd
			}
		} else {
			// A Node that was there when OpsTide began watching is adopted; one created while it
			// watched is observed. Only the second writes entries: a cluster's Nodes are not a log.
			next.Origin = store.OriginAdopted
			if !next.CreatedAt.IsZero() {
				end := endOfSecond(next.CreatedAt)
				if ok, err := w.WatchedThroughout(ctx, scopeName(Nodes, ""), end, end); err == nil && ok {
					next.Origin = store.OriginObserved
				}
			}
		}
		var conds []NodeCondition
		for _, t := range nodeConditionTypes {
			for _, cd := range node.Status.Conditions {
				if string(cd.Type) != t {
					continue
				}
				nc := NodeCondition{Type: t, Status: string(cd.Status), Reason: cut(cd.Reason, 128), Message: cut(cd.Message, 512), Transition: mt(&cd.LastTransitionTime), Observed: now}
				if o, ok := oldByType[t]; ok && o.Status == nc.Status {
					nc.Observed = o.Observed // the status has not changed since OpsTide first saw it
				}
				conds = append(conds, nc)
			}
		}
		next.Conditions = encodeJSON(conds)
		next.StateHash = hashID(fmt.Sprintf("%v|%s", next.Unschedulable, next.Conditions))
		if had && prev.StateHash == next.StateHash && prev.DeletedAt.IsZero() {
			return nil
		}
		if err := w.SaveNode(ctx, next); err != nil {
			return err
		}
		since := next.CreatedAt
		if since.IsZero() {
			since = next.FirstSeenAt
		}
		if err := w.RelinkEvidenceOfNode(ctx, next.UID, next.Name, since); err != nil {
			return err
		}
		stamp := strconv.FormatInt(now.UnixMilli(), 10)
		if !had {
			if next.Origin != store.OriginObserved {
				return nil
			}
			_, err := c.addEvent(ctx, w, nodeEntry(next, "observed", "observed/"+next.UID, "info", fmt.Sprintf("Node %s created (observed joining the cluster)", next.Name),
				map[string]string{"origin": next.Origin}, next.CreatedAt, now), now)
			return err
		}
		if prev.Unschedulable != next.Unschedulable {
			// spec.unschedulable changing proves only that the field changed: not who or what changed it,
			// and not whether Pods can be scheduled (taints and conditions are separate).
			if _, err := c.addEvent(ctx, w, nodeEntry(next, "unschedulable_changed", "unschedulable_changed/"+next.UID+"/"+stamp, "info",
				fmt.Sprintf("Node %s spec.unschedulable changed from %t to %t", next.Name, prev.Unschedulable, next.Unschedulable),
				map[string]string{"from": strconv.FormatBool(prev.Unschedulable), "to": strconv.FormatBool(next.Unschedulable)}, time.Time{}, now), now); err != nil {
				return err
			}
		}
		for _, cd := range conds {
			if len(oldByType) == 0 {
				break // the first conditions a Node reports (it is created with none) are a baseline, not a change
			}
			o, seen := oldByType[cd.Type]
			if seen && o.Status == cd.Status {
				continue
			}
			from := "not reported"
			if seen {
				from = o.Status
			}
			sev := "info"
			if (cd.Type == "Ready" && cd.Status != "True") || (cd.Type != "Ready" && cd.Status == "True") {
				sev = "warning"
			}
			e := nodeEntry(next, "condition_changed", "condition_changed/"+next.UID+"/"+cd.Type+"/"+stamp, sev,
				fmt.Sprintf("Node %s reported condition %s %s (status %s to %s)", next.Name, cd.Type, orNone(cd.Reason), from, cd.Status),
				map[string]string{"condition": cd.Type, "from": from, "to": cd.Status, "reason": cd.Reason, "message": cut(cd.Message, 512)}, cd.Transition, now)
			e.Reason = cd.Reason
			if _, err := c.addEvent(ctx, w, e, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// reconcileAbsentNodes marks Nodes that were removed while OpsTide was not running.
func (c *Collector) reconcileAbsentNodes(ctx context.Context) error {
	if c.nodes == nil {
		return nil
	}
	live, err := c.db.LiveNodes(ctx)
	if err != nil {
		return err
	}
	for _, n := range live {
		obj, exists, err := c.nodes.GetIndexer().GetByKey(n.Name)
		if err != nil {
			return err
		}
		if o, ok := obj.(*corev1.Node); exists && ok && o.UID == types.UID(n.UID) {
			continue
		}
		c.queue.Add(item{kind: Nodes, name: n.Name, uid: types.UID(n.UID), tombstone: true})
	}
	return nil
}
