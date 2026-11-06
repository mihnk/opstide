package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"

	"github.com/mihnk/opstide/internal/release"
	"github.com/mihnk/opstide/internal/store"
	"github.com/mihnk/opstide/internal/workload"
)

// StatefulSets and DaemonSets are followed through their own status, their
// ControllerRevisions and the Pods their ownerReferences name. They are not forced
// into the Deployment release model: there is no ReplicaSet, a revision is a
// ControllerRevision, and the rollout state comes from internal/workload's rules.
// All of these watches are optional: a cluster that forbids reading them still has
// runs, Deployments and releases recorded.
const (
	StatefulSets        = "statefulsets"
	DaemonSets          = "daemonsets"
	ControllerRevisions = "controllerrevisions"
	WorkloadPods        = "workloadpods"
	Nodes               = "nodes"
)

const (
	crOwnerIndex = "owner"
	// Pods of StatefulSets and DaemonSets carry controller-revision-hash, so the API
	// server filters out every other Pod. Deployment Pods carry it too? No: they carry
	// pod-template-hash, and are followed by their own watch.
	workloadPodSelector = "controller-revision-hash"
)

// addWorkloadInformers makes the StatefulSet, DaemonSet, ControllerRevision and
// workload Pod watches of a scope.
func (c *Collector) addWorkloadInformers(s *scope, ns string) {
	apps, core := c.client.AppsV1(), c.client.CoreV1()
	s.statefulSets = c.optionalInformer(ns, StatefulSets, &appsv1.StatefulSet{}, nil,
		func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return apps.StatefulSets(ns).List(ctx, o)
		},
		func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			return apps.StatefulSets(ns).Watch(ctx, o)
		}, StatefulSets)
	s.daemonSets = c.optionalInformer(ns, DaemonSets, &appsv1.DaemonSet{}, nil,
		func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return apps.DaemonSets(ns).List(ctx, o)
		},
		func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			return apps.DaemonSets(ns).Watch(ctx, o)
		}, DaemonSets)
	s.revisions = c.optionalInformer(ns, ControllerRevisions, &appsv1.ControllerRevision{}, cache.Indexers{crOwnerIndex: crOwnerUID},
		func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return apps.ControllerRevisions(ns).List(ctx, o)
		},
		func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			return apps.ControllerRevisions(ns).Watch(ctx, o)
		}, ControllerRevisions)
	sel := func(o *metav1.ListOptions) { o.LabelSelector = workloadPodSelector }
	s.workloadPods = c.optionalInformer(ns, WorkloadPods, &corev1.Pod{}, nil,
		func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			sel(&o)
			return core.Pods(ns).List(ctx, o)
		},
		func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			sel(&o)
			return core.Pods(ns).Watch(ctx, o)
		}, WorkloadPods)
	// Keep in memory only what is read. A ControllerRevision's data holds the whole
	// Pod template, literal environment values included: it is reduced to the
	// allow-listed snapshot the moment it arrives, so those values are never held.
	_ = s.statefulSets.SetTransform(trimStatefulSet)
	_ = s.daemonSets.SetTransform(trimDaemonSet)
	_ = s.revisions.SetTransform(trimControllerRevision)
	_ = s.workloadPods.SetTransform(trimWorkloadPod)

	c.handle(s.statefulSets, true, func(o metav1.Object) (item, bool) {
		return item{kind: StatefulSets, namespace: o.GetNamespace(), name: o.GetName()}, true
	})
	c.handle(s.daemonSets, true, func(o metav1.Object) (item, bool) {
		return item{kind: DaemonSets, namespace: o.GetNamespace(), name: o.GetName()}, true
	})
	// A ControllerRevision changing is a change to its workload.
	c.handle(s.revisions, false, func(o metav1.Object) (item, bool) {
		ref := metav1.GetControllerOf(o)
		switch {
		case !ownedByApps(ref, "StatefulSet", "DaemonSet"):
		case ref.Kind == "StatefulSet":
			return item{kind: StatefulSets, namespace: o.GetNamespace(), name: ref.Name}, true
		case ref.Kind == "DaemonSet":
			return item{kind: DaemonSets, namespace: o.GetNamespace(), name: ref.Name}, true
		}
		return item{}, false
	})
	c.handleWorkloadPods(s.workloadPods)
}

// ownedByApps says whether a controller reference names a built-in apps workload of this kind:
// a third-party Kind of the same name (another API group) is not one.
func ownedByApps(ref *metav1.OwnerReference, kinds ...string) bool {
	if ref == nil || !strings.HasPrefix(ref.APIVersion, "apps/") {
		return false
	}
	for _, k := range kinds {
		if ref.Kind == k {
			return true
		}
	}
	return false
}

func crOwnerUID(obj any) ([]string, error) {
	o, ok := obj.(metav1.Object)
	if !ok {
		return nil, nil
	}
	if ref := metav1.GetControllerOf(o); ownedByApps(ref, "StatefulSet", "DaemonSet") {
		return []string{string(ref.UID)}, nil
	}
	return nil, nil
}

func (c *Collector) handleWorkloadPods(inf cache.SharedIndexInformer) {
	enqueue := func(obj any, deleted bool) {
		tombstone := false
		if t, ok := obj.(cache.DeletedFinalStateUnknown); ok {
			obj, deleted, tombstone = t.Obj, true, true
		}
		p, ok := obj.(*corev1.Pod)
		if !ok {
			return
		}
		if !ownedByApps(metav1.GetControllerOf(p), "StatefulSet", "DaemonSet") {
			return
		}
		it := item{kind: WorkloadPods, namespace: p.Namespace, name: p.Name}
		if deleted {
			it.uid, it.tombstone = p.UID, tombstone
			if p.DeletionTimestamp != nil {
				it.deletedAt = p.DeletionTimestamp.UnixMilli()
			}
			c.gone.Store(string(p.UID), p)
		}
		c.queue.Add(it)
	}
	_, _ = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { enqueue(obj, false) },
		UpdateFunc: func(_, obj any) { enqueue(obj, false) },
		DeleteFunc: func(obj any) { enqueue(obj, true) },
	})
}

func slim(m metav1.ObjectMeta) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: m.Name, Namespace: m.Namespace, UID: m.UID, ResourceVersion: m.ResourceVersion, Generation: m.Generation,
		CreationTimestamp: m.CreationTimestamp, DeletionTimestamp: m.DeletionTimestamp, OwnerReferences: m.OwnerReferences}
}

func trimStatefulSet(obj any) (any, error) {
	s, ok := obj.(*appsv1.StatefulSet)
	if !ok {
		return obj, nil
	}
	return &appsv1.StatefulSet{ObjectMeta: slim(s.ObjectMeta), Status: s.Status, Spec: appsv1.StatefulSetSpec{
		Replicas: s.Spec.Replicas, UpdateStrategy: s.Spec.UpdateStrategy, PodManagementPolicy: s.Spec.PodManagementPolicy, MinReadySeconds: s.Spec.MinReadySeconds}}, nil
}

func trimDaemonSet(obj any) (any, error) {
	d, ok := obj.(*appsv1.DaemonSet)
	if !ok {
		return obj, nil
	}
	return &appsv1.DaemonSet{ObjectMeta: slim(d.ObjectMeta), Status: d.Status, Spec: appsv1.DaemonSetSpec{
		UpdateStrategy: d.Spec.UpdateStrategy, MinReadySeconds: d.Spec.MinReadySeconds}}, nil
}

// trimControllerRevision replaces the revision's data with the encoded snapshot of
// its Pod template (internal/release's allow-list), so the template's literal
// environment values, commands and arguments are not kept.
func trimControllerRevision(obj any) (any, error) {
	cr, ok := obj.(*appsv1.ControllerRevision)
	if !ok {
		return obj, nil
	}
	out := &appsv1.ControllerRevision{ObjectMeta: slim(cr.ObjectMeta), Revision: cr.Revision}
	if h := cr.Labels["controller.kubernetes.io/hash"]; h != "" {
		out.Labels = map[string]string{"controller.kubernetes.io/hash": h}
	}
	var d struct {
		Spec struct {
			Template json.RawMessage `json:"template"`
		} `json:"spec"`
	}
	var tpl corev1.PodTemplateSpec
	switch {
	case json.Unmarshal(cr.Data.Raw, &d) != nil || len(d.Spec.Template) == 0:
		out.Annotations = map[string]string{"opstide.mihnk.org/unreadable": "the ControllerRevision has no readable Pod template"}
	case json.Unmarshal(d.Spec.Template, &tpl) != nil:
		out.Annotations = map[string]string{"opstide.mihnk.org/unreadable": "the Pod template in the ControllerRevision could not be parsed"}
	default:
		out.Data = runtime.RawExtension{Raw: []byte(release.FromTemplate(tpl, nil).Encode())}
	}
	return out, nil
}

// trimWorkloadPod keeps what the lifecycle record reads and the two labels that
// place a Pod in its workload's revision and order.
func trimWorkloadPod(obj any) (any, error) {
	p, ok := obj.(*corev1.Pod)
	if !ok {
		return obj, nil
	}
	m := slim(p.ObjectMeta)
	for _, k := range []string{"controller-revision-hash", "apps.kubernetes.io/pod-index"} {
		if v := p.Labels[k]; v != "" {
			if m.Labels == nil {
				m.Labels = map[string]string{}
			}
			m.Labels[k] = v
		}
	}
	return &corev1.Pod{ObjectMeta: m, Spec: corev1.PodSpec{NodeName: p.Spec.NodeName}, Status: p.Status}, nil
}

// ---- observation of a StatefulSet or DaemonSet ----

type wlCondition struct {
	Type       string    `json:"type"`
	Status     string    `json:"status"`
	Reason     string    `json:"reason,omitempty"`
	Message    string    `json:"message,omitempty"`
	Transition time.Time `json:"transition,omitzero"`
}

// obs is one observation of a StatefulSet or DaemonSet, kind-independent.
type obs struct {
	kind, word    string // StatefulSet, "statefulset"
	uid, ns, name string
	created       time.Time
	deleting      time.Time
	scope         string // the coverage scope that says whether it was seen being created
	state         store.WorkloadState
	conditions    []wlCondition
	counts        workload.Counts
	report        []string // count fields this kind reports besides the common four
	revisions     []*appsv1.ControllerRevision
	rollout       workload.Rollout
}

func (c *Collector) revisionsOf(s *scope, owner types.UID) []*appsv1.ControllerRevision {
	objs, err := s.revisions.GetIndexer().ByIndex(crOwnerIndex, string(owner))
	if err != nil {
		return nil
	}
	var out []*appsv1.ControllerRevision
	for _, o := range objs {
		if cr, ok := o.(*appsv1.ControllerRevision); ok {
			out = append(out, cr)
		}
	}
	return out
}

func intOrNil(p *int32) int {
	if p == nil {
		return 0
	}
	return int(*p)
}

func (c *Collector) statefulSetObs(s *scope, sts *appsv1.StatefulSet) obs {
	strategy := string(sts.Spec.UpdateStrategy.Type)
	if strategy == "" {
		strategy = workload.RollingUpdate
	}
	desired := 1
	if sts.Spec.Replicas != nil {
		desired = int(*sts.Spec.Replicas)
	}
	st := sts.Status
	o := obs{kind: store.KindStatefulSet, word: "statefulset", uid: string(sts.UID), ns: sts.Namespace, name: sts.Name, created: mt(&sts.CreationTimestamp), deleting: mt(sts.DeletionTimestamp),
		scope: scopeName(StatefulSets, c.nsOf(sts.Namespace)), revisions: c.revisionsOf(s, sts.UID), report: []string{"current"}}
	o.counts = workload.Counts{Desired: desired, Ready: int(st.ReadyReplicas), Available: int(st.AvailableReplicas), Updated: int(st.UpdatedReplicas), Current: int(st.CurrentReplicas)}
	partition := 0
	var maxUnavailable string
	if strategy == workload.RollingUpdate && sts.Spec.UpdateStrategy.RollingUpdate != nil {
		partition = intOrNil(sts.Spec.UpdateStrategy.RollingUpdate.Partition)
		if mu := sts.Spec.UpdateStrategy.RollingUpdate.MaxUnavailable; mu != nil {
			maxUnavailable = mu.String()
		}
	}
	o.rollout = workload.ClassifyStatefulSet(workload.StatefulSetInput{Generation: sts.Generation, ObservedGeneration: st.ObservedGeneration, Replicas: desired,
		Updated: int(st.UpdatedReplicas), CurrentRevision: st.CurrentRevision, UpdateRevision: st.UpdateRevision, Strategy: strategy, Partition: partition})
	o.state = store.WorkloadState{WorkloadID: o.uid, Kind: o.kind, Generation: sts.Generation, ObservedGeneration: st.ObservedGeneration,
		Desired: desired, Ready: o.counts.Ready, Available: o.counts.Available, Updated: o.counts.Updated, Current: o.counts.Current,
		Revision: st.CurrentRevision, UpdateRevision: st.UpdateRevision, Strategy: strategy, MaxUnavailable: maxUnavailable,
		PodManagement: string(sts.Spec.PodManagementPolicy), MinReadySeconds: int(sts.Spec.MinReadySeconds), Rollout: o.rollout.State, RolloutNote: o.rollout.Note}
	if strategy == workload.RollingUpdate {
		o.state.Partition = &partition
	}
	for _, cd := range st.Conditions {
		o.conditions = append(o.conditions, wlCondition{Type: string(cd.Type), Status: string(cd.Status), Reason: cut(cd.Reason, 128), Message: cut(cd.Message, 512), Transition: mt(&cd.LastTransitionTime)})
	}
	return o
}

func (c *Collector) daemonSetObs(s *scope, d *appsv1.DaemonSet) obs {
	strategy := string(d.Spec.UpdateStrategy.Type)
	if strategy == "" {
		strategy = workload.RollingUpdate
	}
	st := d.Status
	o := obs{kind: store.KindDaemonSet, word: "daemonset", uid: string(d.UID), ns: d.Namespace, name: d.Name, created: mt(&d.CreationTimestamp), deleting: mt(d.DeletionTimestamp),
		scope: scopeName(DaemonSets, c.nsOf(d.Namespace)), revisions: c.revisionsOf(s, d.UID), report: []string{"current", "misscheduled"}}
	o.counts = workload.Counts{Desired: int(st.DesiredNumberScheduled), Ready: int(st.NumberReady), Available: int(st.NumberAvailable), Updated: int(st.UpdatedNumberScheduled),
		Current: int(st.CurrentNumberScheduled), Misscheduled: int(st.NumberMisscheduled)}
	o.rollout = workload.ClassifyDaemonSet(workload.DaemonSetInput{Generation: d.Generation, ObservedGeneration: st.ObservedGeneration,
		Desired: int(st.DesiredNumberScheduled), Updated: int(st.UpdatedNumberScheduled), Strategy: strategy})
	// A DaemonSet's status carries no revision: its newest ControllerRevision (the highest
	// revision number) is the template revision. While the ControllerRevision watch has not
	// listed yet, or none is known, the revision is unknown and stays what it was.
	newest := ""
	var top int64 = -1
	for _, cr := range o.revisions {
		if cr.Revision > top {
			top, newest = cr.Revision, cr.Name
		}
	}
	if !s.revisions.HasSynced() {
		newest = ""
	}
	o.state = store.WorkloadState{WorkloadID: o.uid, Kind: o.kind, Generation: d.Generation, ObservedGeneration: st.ObservedGeneration,
		Desired: o.counts.Desired, Ready: o.counts.Ready, Available: o.counts.Available, Updated: o.counts.Updated, Current: o.counts.Current, Misscheduled: o.counts.Misscheduled,
		Revision: newest, UpdateRevision: newest, Strategy: strategy, MinReadySeconds: int(d.Spec.MinReadySeconds), Rollout: o.rollout.State, RolloutNote: o.rollout.Note}
	if ru := d.Spec.UpdateStrategy.RollingUpdate; strategy == workload.RollingUpdate && ru != nil {
		if ru.MaxUnavailable != nil {
			o.state.MaxUnavailable = ru.MaxUnavailable.String()
		}
		if ru.MaxSurge != nil {
			o.state.MaxSurge = ru.MaxSurge.String()
		}
	}
	for _, cd := range st.Conditions {
		o.conditions = append(o.conditions, wlCondition{Type: string(cd.Type), Status: string(cd.Status), Reason: cut(cd.Reason, 128), Message: cut(cd.Message, 512), Transition: mt(&cd.LastTransitionTime)})
	}
	return o
}

func (c *Collector) syncStatefulSet(ctx context.Context, it item) error {
	s := c.scopeFor(it.namespace)
	if s == nil {
		return nil
	}
	sts, err := get[*appsv1.StatefulSet](s.statefulSets, it.namespace, it.name, it.uid)
	if err != nil {
		return err
	}
	if sts == nil {
		return c.workloadGone(ctx, store.KindStatefulSet, "statefulset", it)
	}
	return c.recordWorkload(ctx, c.statefulSetObs(s, sts))
}

func (c *Collector) syncDaemonSet(ctx context.Context, it item) error {
	s := c.scopeFor(it.namespace)
	if s == nil {
		return nil
	}
	d, err := get[*appsv1.DaemonSet](s.daemonSets, it.namespace, it.name, it.uid)
	if err != nil {
		return err
	}
	if d == nil {
		return c.workloadGone(ctx, store.KindDaemonSet, "daemonset", it)
	}
	return c.recordWorkload(ctx, c.daemonSetObs(s, d))
}

func endOfSecond(t time.Time) time.Time {
	return t.Truncate(time.Second).Add(time.Second - time.Millisecond)
}

// wlEntry builds a log entry about a workload. The workload's kind is the entry's kind.
func wlEntry(o obs, typ, id, severity, summary string, facts map[string]string, occurred, now time.Time) store.Event {
	if facts == nil {
		facts = map[string]string{}
	}
	facts["workload"], facts["kind"] = o.name, o.kind
	return store.Event{ID: o.word + "." + id, Type: o.word + "." + typ, Namespace: o.ns, Kind: o.kind, ResourceUID: o.uid, ResourceName: o.name, WorkloadID: o.uid,
		OccurredAt: occurred, ObservedAt: now, Severity: severity, Confidence: store.Observed, Summary: summary, Facts: facts}
}

func stateHash(o obs) string {
	st := o.state
	part := -1
	if st.Partition != nil {
		part = *st.Partition
	}
	b, _ := json.Marshal(o.conditions)
	return hashID(fmt.Sprintf("%d|%d|%d|%d|%d|%d|%d|%s|%s|%s|%d|%s|%s|%s|%d|%s|%s", st.Generation, st.ObservedGeneration, st.Desired, st.Ready, st.Available, st.Updated, st.Current,
		st.Revision, st.UpdateRevision, st.Strategy, part, st.MaxUnavailable, st.MaxSurge, st.PodManagement, st.Misscheduled, st.Rollout, string(b)))
}

// recordWorkload writes a StatefulSet's or DaemonSet's identity, revisions and state, and the
// entries for what changed between two observations. A first sight is a baseline: it
// writes a "first observed" entry and none for a rollout it joined in the middle.
func (c *Collector) recordWorkload(ctx context.Context, o obs) error {
	now := c.now()
	return c.db.Update(ctx, func(w *store.Writer) error {
		prev, had, err := w.WorkloadState(ctx, o.uid)
		if err != nil {
			return err
		}
		known, err := w.WorkloadKnown(ctx, o.uid)
		if err != nil {
			return err
		}
		origin := ""
		if !known {
			origin = store.OriginAdopted
			if !o.created.IsZero() {
				if ok, err := w.WatchedThroughout(ctx, o.scope, endOfSecond(o.created), endOfSecond(o.created)); err == nil && ok {
					origin = store.OriginObserved
				}
			}
		}
		if err := w.UpsertWorkload(ctx, store.Workload{ID: o.uid, Kind: o.kind, Origin: origin, Namespace: o.ns, Name: o.name, FirstSeen: now, LastSeen: now}); err != nil {
			return err
		}
		if err := c.recordRevisions(ctx, w, o, now); err != nil {
			return err
		}
		if err := w.RelinkEvidenceOfWorkload(ctx, o.uid); err != nil {
			return err
		}
		// A DaemonSet whose revision is not known right now keeps the one it had: an unfilled cache
		// is not a template change.
		if had && o.kind == store.KindDaemonSet && o.state.UpdateRevision == "" {
			o.state.Revision, o.state.UpdateRevision = prev.Revision, prev.UpdateRevision
		}
		o.state.StateHash = stateHash(o)
		o.state.Conditions = encodeJSON(o.conditions)
		o.state.LastChangeAt = now
		if had {
			if prev.StateHash == o.state.StateHash {
				return nil
			}
			o.state.LastChangeAt = now
		}
		if err := w.SaveWorkloadState(ctx, o.state); err != nil {
			return err
		}
		if !had {
			suffix := " (already present when OpsTide began watching)"
			if origin == store.OriginObserved {
				suffix = " (created while OpsTide was watching)"
			}
			facts := map[string]string{"strategy": o.state.Strategy, "revision": o.state.Revision, "updateRevision": o.state.UpdateRevision, "desired": strconv.Itoa(o.state.Desired)}
			if o.state.Partition != nil {
				facts["partition"] = strconv.Itoa(*o.state.Partition)
			}
			if o.state.PodManagement != "" {
				facts["podManagement"] = o.state.PodManagement
			}
			facts["origin"] = origin
			_, err := c.addEvent(ctx, w, wlEntry(o, "observed", "observed/"+o.uid, "info", fmt.Sprintf("%s %s first observed%s", o.kind, o.name, suffix), facts, time.Time{}, now), now)
			return err
		}
		return c.workloadEntries(ctx, w, o, prev, now)
	})
}

// recordRevisions records every ControllerRevision of the workload once, with its
// allow-listed snapshot. A revision created while its watch was running is observed;
// one that was already there is adopted, which affects only how it is labelled: its data
// is complete either way, so it can be compared.
func (c *Collector) recordRevisions(ctx context.Context, w *store.Writer, o obs, now time.Time) error {
	crScope := scopeName(ControllerRevisions, c.nsOf(o.ns))
	for _, cr := range o.revisions {
		snap := string(cr.Data.Raw)
		if snap == "" {
			snap = "{}"
		}
		fp := hashID("unreadable|" + string(cr.UID))
		if sn, err := release.DecodeSnapshot(snap); err == nil && cr.Data.Raw != nil {
			fp = sn.Fingerprint()
		} else {
			snap = "{}"
		}
		origin := store.OriginAdopted
		if created := mt(&cr.CreationTimestamp); !created.IsZero() {
			if ok, err := w.WatchedThroughout(ctx, crScope, endOfSecond(created), endOfSecond(created)); err == nil && ok {
				origin = store.OriginObserved
			}
		}
		if _, err := w.AddRevision(ctx, store.WorkloadRevision{ID: "wr" + hashID(o.uid+"|"+string(cr.UID)), WorkloadID: o.uid, Kind: o.kind, Name: cr.Name,
			Hash: cr.Labels["controller.kubernetes.io/hash"], Number: cr.Revision, ControllerRevisionUID: string(cr.UID), CreatedAt: mt(&cr.CreationTimestamp),
			ObservedAt: now, Origin: origin, Fingerprint: fp, Snapshot: snap}); err != nil {
			return err
		}
	}
	return nil
}

// workloadEntries writes what changed between the previous and the current observation.
func (c *Collector) workloadEntries(ctx context.Context, w *store.Writer, o obs, prev store.WorkloadState, now time.Time) error {
	stamp := strconv.FormatInt(now.UnixMilli(), 10)
	add := func(typ, severity, summary string, facts map[string]string, occurred time.Time) error {
		_, err := c.addEvent(ctx, w, wlEntry(o, typ, typ+"/"+o.uid+"/"+stamp, severity, summary, facts, occurred, now), now)
		return err
	}
	// A change is from one revision to another: learning the first one is not a change.
	revChanged := prev.UpdateRevision != "" && o.state.UpdateRevision != "" && prev.UpdateRevision != o.state.UpdateRevision
	if revChanged {
		if err := add("revision_changed", "info", fmt.Sprintf("%s %s update revision changed from %s to %s", o.kind, o.name, prev.UpdateRevision, o.state.UpdateRevision),
			map[string]string{"from": prev.UpdateRevision, "to": o.state.UpdateRevision}, time.Time{}); err != nil {
			return err
		}
	}
	if o.kind == store.KindStatefulSet && prev.Revision != o.state.Revision && prev.Revision != "" && o.state.Revision != "" {
		if err := add("current_revision_changed", "info", fmt.Sprintf("%s %s current revision changed from %s to %s", o.kind, o.name, prev.Revision, o.state.Revision),
			map[string]string{"from": prev.Revision, "to": o.state.Revision}, time.Time{}); err != nil {
			return err
		}
	}
	if o.kind == store.KindStatefulSet && partitionOf(prev) != partitionOf(o.state) && prev.Strategy == o.state.Strategy {
		if err := add("partition_changed", "info", fmt.Sprintf("%s %s rolling update partition changed from %d to %d", o.kind, o.name, partitionOf(prev), partitionOf(o.state)),
			map[string]string{"from": strconv.Itoa(partitionOf(prev)), "to": strconv.Itoa(partitionOf(o.state))}, time.Time{}); err != nil {
			return err
		}
	}
	if prev.Strategy != "" && prev.Strategy != o.state.Strategy {
		if err := add("strategy_changed", "info", fmt.Sprintf("%s %s update strategy changed from %s to %s", o.kind, o.name, prev.Strategy, o.state.Strategy),
			map[string]string{"from": prev.Strategy, "to": o.state.Strategy}, time.Time{}); err != nil {
			return err
		}
	}
	for _, typ := range workload.Entries(workload.Rollout{State: prev.Rollout}, o.rollout, revChanged) {
		summary := map[string]string{
			"update_started":               "%s %s update started",
			"update_partitioned":           "%s %s update reached its partition boundary",
			"update_awaiting_pod_deletion": "%s %s template changed under OnDelete: Pods are replaced only when deleted",
			"update_completed":             "%s %s update completed",
		}[typ]
		facts := map[string]string{"rollout": o.rollout.State, "note": o.rollout.Note, "updateRevision": o.state.UpdateRevision}
		if err := add(typ, "info", fmt.Sprintf(summary, o.kind, o.name), facts, time.Time{}); err != nil {
			return err
		}
	}
	pc := workload.Counts{Desired: prev.Desired, Ready: prev.Ready, Available: prev.Available, Updated: prev.Updated, Current: prev.Current, Misscheduled: prev.Misscheduled}
	if ch := workload.Changes(pc, o.counts, o.report...); len(ch) > 0 {
		facts := map[string]string{}
		for _, x := range ch {
			facts[x.Field+"From"], facts[x.Field+"To"] = strconv.Itoa(x.From), strconv.Itoa(x.To)
		}
		sev := "info"
		if o.counts.Available < o.counts.Desired && o.counts.Available < pc.Available {
			sev = "warning" // fewer available than wanted, and fewer than before: as reported
		}
		if err := add("counts_changed", sev, fmt.Sprintf("%s %s reported %s", o.kind, o.name, workload.Describe(ch)), facts, time.Time{}); err != nil {
			return err
		}
	}
	old := map[string]wlCondition{}
	for _, cd := range decodeJSON[wlCondition](prev.Conditions) {
		old[cd.Type] = cd
	}
	for _, cd := range o.conditions {
		p, seen := old[cd.Type]
		if seen && p.Status == cd.Status && p.Reason == cd.Reason {
			continue
		}
		from := "not reported"
		if seen {
			from = p.Status
		}
		occurred := time.Time{}
		if from != cd.Status {
			occurred = cd.Transition
		}
		e := wlEntry(o, "condition_changed", "condition_changed/"+o.uid+"/"+cd.Type+"/"+stamp, "info",
			fmt.Sprintf("%s %s reported condition %s %s (status %s to %s)", o.kind, o.name, cd.Type, orNone(cd.Reason), from, cd.Status),
			map[string]string{"condition": cd.Type, "from": from, "to": cd.Status, "reason": cd.Reason, "message": cut(cd.Message, 512)}, occurred, now)
		e.Reason = cd.Reason
		if _, err := c.addEvent(ctx, w, e, now); err != nil {
			return err
		}
	}
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func partitionOf(s store.WorkloadState) int {
	if s.Partition == nil {
		return 0
	}
	return *s.Partition
}

// workloadGone records that a StatefulSet or DaemonSet no longer exists. Its history stays.
func (c *Collector) workloadGone(ctx context.Context, kind, word string, it item) error {
	if it.uid == "" {
		return nil
	}
	now := c.now()
	at := now
	if it.deletedAt > 0 {
		at = msTime(it.deletedAt)
	}
	return c.db.Update(ctx, func(w *store.Writer) error {
		known, err := w.WorkloadKnown(ctx, string(it.uid))
		if err != nil || !known {
			return err
		}
		if wl, ok, err := c.db.WorkloadByID(ctx, string(it.uid)); err != nil {
			return err
		} else if ok && !wl.DeletedAt.IsZero() {
			return nil
		}
		if err := w.MarkWorkloadDeleted(ctx, string(it.uid), at); err != nil {
			return err
		}
		o := obs{kind: kind, word: word, uid: string(it.uid), ns: it.namespace, name: it.name}
		note := ""
		if it.deletedAt == 0 {
			note = " (when it was deleted is not known; this is when OpsTide noticed)"
		}
		_, err = c.addEvent(ctx, w, wlEntry(o, "deleted", "deleted/"+o.uid, "info", fmt.Sprintf("%s %s deleted%s", kind, it.name, note), nil, time.Time{}, now), now)
		return err
	})
}

// ---- Pods of StatefulSets and DaemonSets ----

// syncWorkloadPod records the lifecycle of a Pod owned by a StatefulSet or DaemonSet. The
// owner is the one its ownerReference names (by UID); the revision is the Pod's own
// controller-revision-hash label; the Node is where the Pod reported it runs.
func (c *Collector) syncWorkloadPod(ctx context.Context, it item) error {
	s := c.scopeFor(it.namespace)
	if s == nil {
		return nil
	}
	pod, err := get[*corev1.Pod](s.workloadPods, it.namespace, it.name, it.uid)
	if err != nil {
		return err
	}
	if pod == nil {
		if it.uid == "" {
			return nil
		}
		return c.workloadPodGone(ctx, it)
	}
	ref := metav1.GetControllerOf(pod)
	if !ownedByApps(ref, "StatefulSet", "DaemonSet") {
		return nil
	}
	now := c.now()
	return c.db.Update(ctx, func(w *store.Writer) error {
		wl, ok, err := c.db.WorkloadByID(ctx, string(ref.UID))
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("the %s %s of Pod %s is not recorded yet", ref.Kind, ref.Name, pod.Name)
		}
		rec, had, err := w.Pod(ctx, string(pod.UID))
		if err != nil {
			return err
		}
		var prev *release.PodState
		if had {
			if st, err := release.DecodePod(rec.State); err == nil {
				prev = &st
			}
		}
		// A Pod already on record is observed as it changes; one first seen after the workload
		// was is observed from its creation; an older one only sets a baseline.
		emit := had || !pod.CreationTimestamp.Add(time.Second).Before(wl.FirstSeen)
		history := false
		if prev == nil {
			end := endOfSecond(pod.CreationTimestamp.Time)
			ok, err := w.WatchedThroughout(ctx, scopeName(WorkloadPods, c.nsOf(pod.Namespace)), end, end)
			history = err == nil && ok
		}
		next, sigs := release.Observe(prev, pod, now, emit, history)
		enc := release.EncodePod(next)
		revision := pod.Labels["controller-revision-hash"]
		rowPod := store.PodRecord{Kind: wl.Kind, Revision: revision, Node: pod.Spec.NodeName, UID: string(pod.UID), Namespace: pod.Namespace, Name: pod.Name,
			WorkloadID: string(ref.UID), CreatedAt: mt(&pod.CreationTimestamp), TerminatedAt: next.TermAt, State: enc}
		if wl.Kind == store.KindStatefulSet {
			if n, ok := workload.Ordinal(pod.Name, ref.Name, pod.Labels["apps.kubernetes.io/pod-index"]); ok {
				rowPod.Ordinal = &n
			}
		}
		if had && rec.State == enc && rec.Revision == revision && (rec.Node == pod.Spec.NodeName || pod.Spec.NodeName == "") {
			return nil
		}
		if err := w.UpsertPod(ctx, rowPod); err != nil {
			return err
		}
		if !had {
			if err := w.RelinkEvidenceOfPod(ctx, string(pod.UID), "", string(ref.UID), "Pod ownerReference to "+wl.Kind+" (recorded)"); err != nil {
				return err
			}
		}
		for _, sg := range sigs {
			e := c.workloadPodEvent(wl, pod, sg, now, store.Observed)
			if added, err := c.addEvent(ctx, w, e, now); err != nil {
				return err
			} else if added {
				c.wm.podEvent(sg.Type)
			}
		}
		return nil
	})
}

func (c *Collector) workloadPodEvent(wl store.WorkloadRow, p *corev1.Pod, sg release.Signal, now time.Time, conf store.Confidence) store.Event {
	facts := map[string]string{"workload": wl.Name, "kind": wl.Kind}
	if sg.Container != "" {
		facts["container"] = sg.Container
	}
	if rev := p.Labels["controller-revision-hash"]; rev != "" {
		facts["revision"] = rev
	}
	if p.Spec.NodeName != "" {
		facts["node"] = p.Spec.NodeName
	}
	for k, v := range sg.Facts {
		facts[k] = v
	}
	key := ""
	if sg.Key != "" {
		key = "/" + sg.Key
	}
	return store.Event{ID: "pod." + sg.Type + "/" + string(p.UID) + key, Type: "pod." + sg.Type, Namespace: p.Namespace, Kind: "Pod", ResourceUID: string(p.UID),
		ResourceName: p.Name, WorkloadID: wl.ID, OccurredAt: sg.At, ObservedAt: now, Severity: sg.Severity, Confidence: conf, Summary: wl.Name + ": " + sg.Summary, Facts: facts}
}

// workloadPodGone ends a Pod's record. How it ended is only what the informer last saw; a Pod
// that vanished while OpsTide was not running ends with confidence unknown.
func (c *Collector) workloadPodGone(ctx context.Context, it item) error {
	var last *corev1.Pod
	if v, ok := c.gone.Load(string(it.uid)); ok {
		last, _ = v.(*corev1.Pod)
	}
	now := c.now()
	err := c.db.Update(ctx, func(w *store.Writer) error {
		rec, had, err := w.Pod(ctx, string(it.uid))
		if err != nil || !had || !rec.TerminatedAt.IsZero() {
			return err
		}
		prev, err := release.DecodePod(rec.State)
		if err != nil {
			return err
		}
		wl, wok, err := c.db.WorkloadByID(ctx, rec.WorkloadID)
		if err != nil {
			return err
		}
		seen := last != nil && !it.tombstone
		var sigs []release.Signal
		if last != nil {
			prev, sigs = release.Observe(&prev, last, now, wok, true)
		}
		at := now
		if it.deletedAt > 0 {
			at = msTime(it.deletedAt)
		}
		next := release.Gone(prev, at)
		if !seen {
			next.TermReason = "unknown"
		}
		rec.TerminatedAt, rec.State = next.TermAt, release.EncodePod(next)
		if err := w.UpsertPod(ctx, rec); err != nil {
			return err
		}
		if !wok {
			return nil
		}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: rec.Name, Namespace: rec.Namespace, UID: types.UID(rec.UID), Labels: map[string]string{"controller-revision-hash": rec.Revision}}, Spec: corev1.PodSpec{NodeName: rec.Node}}
		for _, sg := range sigs {
			if added, err := c.addEvent(ctx, w, c.workloadPodEvent(wl, pod, sg, now, store.Observed), now); err != nil {
				return err
			} else if added {
				c.wm.podEvent(sg.Type)
			}
		}
		sg := release.Signal{Type: "terminated", Severity: "info", Summary: fmt.Sprintf("Pod %s terminated (deleted, replaced or scaled down)", rec.Name)}
		conf := store.Observed
		if !seen {
			sg.Summary, conf = fmt.Sprintf("Pod %s was removed while OpsTide was not watching it; how it ended is not known", rec.Name), store.Unknown
		}
		if it.deletedAt > 0 {
			sg.At = at
		}
		if added, err := c.addEvent(ctx, w, c.workloadPodEvent(wl, pod, sg, now, conf), now); err != nil {
			return err
		} else if added {
			c.wm.podEvent("terminated")
		}
		return nil
	})
	if err == nil {
		c.gone.Delete(string(it.uid))
	}
	return err
}

// reconcileAbsentWorkloadPods ends the records of StatefulSet and DaemonSet Pods that vanished while OpsTide was not running.
func (c *Collector) reconcileAbsentWorkloadPods(ctx context.Context) error {
	live, err := c.db.LiveWorkloadPods(ctx)
	if err != nil {
		return err
	}
	for _, rec := range live {
		s := c.scopeFor(rec.Namespace)
		if s == nil {
			continue
		}
		p, err := get[*corev1.Pod](s.workloadPods, rec.Namespace, rec.Name, types.UID(rec.UID))
		if err != nil {
			return err
		}
		if p == nil {
			c.queue.Add(item{kind: WorkloadPods, namespace: rec.Namespace, name: rec.Name, uid: types.UID(rec.UID), tombstone: true})
		}
	}
	return nil
}

// reconcileAbsentWorkloads marks StatefulSets and DaemonSets deleted while OpsTide was not running.
func (c *Collector) reconcileAbsentWorkloads(ctx context.Context) error {
	live, err := c.db.LiveWorkloads(ctx)
	if err != nil {
		return err
	}
	for _, wl := range live {
		s := c.scopeFor(wl.Namespace)
		if s == nil {
			continue
		}
		switch wl.Kind {
		case store.KindStatefulSet:
			if o, err := get[*appsv1.StatefulSet](s.statefulSets, wl.Namespace, wl.Name, types.UID(wl.ID)); err != nil {
				return err
			} else if o == nil {
				c.queue.Add(item{kind: StatefulSets, namespace: wl.Namespace, name: wl.Name, uid: types.UID(wl.ID), tombstone: true})
			}
		case store.KindDaemonSet:
			if o, err := get[*appsv1.DaemonSet](s.daemonSets, wl.Namespace, wl.Name, types.UID(wl.ID)); err != nil {
				return err
			} else if o == nil {
				c.queue.Add(item{kind: DaemonSets, namespace: wl.Namespace, name: wl.Name, uid: types.UID(wl.ID), tombstone: true})
			}
		}
	}
	return nil
}

// saveDeploymentState records a Deployment's current observed state beside the
// StatefulSets' and DaemonSets', so one list can show all three. The rollout state is the one
// the Deployment's current release already carries.
func (c *Collector) saveDeploymentState(ctx context.Context, w *store.Writer, d *appsv1.Deployment, rev string, want int32, cur *store.Release, now time.Time) error {
	st := d.Status
	strategy := string(d.Spec.Strategy.Type)
	if strategy == "" {
		strategy = string(appsv1.RollingUpdateDeploymentStrategyType)
	}
	ws := store.WorkloadState{WorkloadID: string(d.UID), Kind: store.KindDeployment, Generation: d.Generation, ObservedGeneration: st.ObservedGeneration, Desired: int(want),
		Ready: int(st.ReadyReplicas), Available: int(st.AvailableReplicas), Updated: int(st.UpdatedReplicas), Current: int(st.Replicas), Revision: rev, UpdateRevision: rev,
		Strategy: strategy, MinReadySeconds: int(d.Spec.MinReadySeconds)}
	if ru := d.Spec.Strategy.RollingUpdate; ru != nil {
		if ru.MaxUnavailable != nil {
			ws.MaxUnavailable = ru.MaxUnavailable.String()
		}
		if ru.MaxSurge != nil {
			ws.MaxSurge = ru.MaxSurge.String()
		}
	}
	if cur != nil {
		ws.Rollout, ws.RolloutNote = cur.State, cur.StateReason
	}
	var conds []wlCondition
	for _, cd := range st.Conditions {
		conds = append(conds, wlCondition{Type: string(cd.Type), Status: string(cd.Status), Reason: cut(cd.Reason, 128), Message: cut(cd.Message, 512), Transition: mt(&cd.LastTransitionTime)})
	}
	ws.Conditions = encodeJSON(conds)
	o := obs{state: ws, conditions: conds}
	ws.StateHash = stateHash(o)
	prev, had, err := w.WorkloadState(ctx, ws.WorkloadID)
	if err != nil {
		return err
	}
	if had && prev.StateHash == ws.StateHash {
		return nil
	}
	ws.LastChangeAt = now
	return w.SaveWorkloadState(ctx, ws)
}
