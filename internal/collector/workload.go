package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"

	"github.com/mihnk/opstide/internal/release"
	"github.com/mihnk/opstide/internal/store"
)

// This file turns a Deployment's ReplicaSets and Pods into releases and
// Pod lifecycle records. Handlers only queue keys, as everywhere else in the
// collector; one worker reads the caches and writes one transaction per object.

// rsOwnerIndex indexes ReplicaSets by the UID of the Deployment that controls them.
const rsOwnerIndex = "deployment"

// rsPodIndex indexes Deployment Pods by the UID of the ReplicaSet that controls them.
const rsPodIndex = "replicaset"

func podRSUID(obj any) ([]string, error) {
	o, ok := obj.(metav1.Object)
	if !ok {
		return nil, nil
	}
	if ref := metav1.GetControllerOf(o); ref != nil && ref.Kind == "ReplicaSet" {
		return []string{string(ref.UID)}, nil
	}
	return nil, nil
}

func rsOwnerUID(obj any) ([]string, error) {
	o, ok := obj.(metav1.Object)
	if !ok {
		return nil, nil
	}
	if ref := metav1.GetControllerOf(o); ref != nil && ref.Kind == "Deployment" {
		return []string{string(ref.UID)}, nil
	}
	return nil, nil
}

// trimPod reduces a cached Pod to what OpsTide reads. The informer holds every
// Deployment Pod of the cluster, and a spec is most of a Pod's size.
func trimPod(obj any) (any, error) {
	p, ok := obj.(*corev1.Pod)
	if !ok {
		return obj, nil // a tombstone passes through
	}
	out := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: p.Name, Namespace: p.Namespace, UID: p.UID, ResourceVersion: p.ResourceVersion,
			CreationTimestamp: p.CreationTimestamp, DeletionTimestamp: p.DeletionTimestamp, OwnerReferences: p.OwnerReferences,
		},
		Spec:   corev1.PodSpec{NodeName: p.Spec.NodeName},
		Status: p.Status,
	}
	return out, nil
}

// trimReplicaSet drops what is never read: managed fields, most annotations,
// and from the Pod template everything the snapshot does not take (literal
// environment values, commands, arguments, mounts, ports). The snapshot is
// built from what is left.
func trimReplicaSet(obj any) (any, error) {
	rs, ok := obj.(*appsv1.ReplicaSet)
	if !ok {
		return obj, nil
	}
	out := rs.DeepCopy()
	out.ManagedFields = nil
	keep := map[string]string{}
	if v, ok := rs.Annotations[release.AnnRevision]; ok {
		keep[release.AnnRevision] = v
	}
	out.Annotations = keep
	strip := func(cs []corev1.Container) {
		for i := range cs {
			c := &cs[i]
			for j := range c.Env {
				c.Env[j].Value = ""
			}
			c.Command, c.Args, c.VolumeMounts, c.VolumeDevices, c.Ports, c.Lifecycle = nil, nil, nil, nil, nil, nil
		}
	}
	strip(out.Spec.Template.Spec.Containers)
	strip(out.Spec.Template.Spec.InitContainers)
	return out, nil
}

// handleDeployPods queues Deployment Pod changes. A delete also stashes the
// Pod's last state, which the cache no longer has once the worker runs.
func (c *Collector) handleDeployPods(inf cache.SharedIndexInformer) {
	enqueue := func(obj any, deleted bool) {
		tombstone := false
		if t, ok := obj.(cache.DeletedFinalStateUnknown); ok {
			obj, deleted, tombstone = t.Obj, true, true
		}
		p, ok := obj.(*corev1.Pod)
		if !ok {
			return
		}
		it := item{kind: DeployPods, namespace: p.Namespace, name: p.Name}
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

// workloadMetrics are bounded counters for /metrics: no Deployment name, Pod
// or release ever becomes a label.
type workloadMetrics struct {
	releases atomic.Uint64
	health   [3]atomic.Uint64 // healthy, degraded, insufficient
	pod      [8]atomic.Uint64 // by podTypes
	last     atomic.Int64     // Unix ms of the last workload entry
}

var podTypes = []string{"created", "ready", "notready", "restarted", "oomkilled", "waiting", "evicted", "terminated"}

// WorkloadStats is a snapshot of those counters.
type WorkloadStats struct {
	Releases                        uint64
	Healthy, Degraded, Insufficient uint64
	PodEvents                       map[string]uint64
	LastEvent                       time.Time
}

func (m *workloadMetrics) stats() WorkloadStats {
	s := WorkloadStats{Releases: m.releases.Load(), Healthy: m.health[0].Load(), Degraded: m.health[1].Load(), Insufficient: m.health[2].Load(), PodEvents: map[string]uint64{}}
	for i, t := range podTypes {
		s.PodEvents[t] = m.pod[i].Load()
	}
	if ms := m.last.Load(); ms > 0 {
		s.LastEvent = time.UnixMilli(ms).UTC()
	}
	return s
}

func (m *workloadMetrics) podEvent(t string) {
	for i, n := range podTypes {
		if n == t {
			m.pod[i].Add(1)
		}
	}
}

// noteEvent records that a workload entry was written, for System.
func (c *Collector) noteEvent(ctx context.Context, w *store.Writer, now time.Time) {
	c.wm.last.Store(now.UnixMilli())
	_ = w.SetMeta(ctx, "last_workload_event", strconv.FormatInt(now.UnixMilli(), 10))
}

// addEvent writes an entry and reports whether it was new: a repeat of an
// entry already written is not counted anywhere.
func (c *Collector) addEvent(ctx context.Context, w *store.Writer, e store.Event, now time.Time) (bool, error) {
	if e.WorkloadID == "" && e.Kind == "Deployment" {
		e.WorkloadID = e.ResourceUID // an entry about a Deployment names its workload
	}
	added, err := w.AddEvent(ctx, e)
	if err == nil && added {
		c.noteEvent(ctx, w, now)
	}
	return added, err
}

// scopeNamesFor are the coverage scopes release observation depends on.
func (c *Collector) scopeNamesFor(ns string) []string { return release.ScopeNames(ns, c.clusterWide) }

// watched reports whether Deployments, ReplicaSets and Pods were all watched
// without a gap over [from, to].
func (c *Collector) watched(ctx context.Context, w *store.Writer, ns string, from, to time.Time) bool {
	for _, scope := range c.scopeNamesFor(ns) {
		ok, err := w.WatchedThroughout(ctx, scope, from, to)
		if err != nil || !ok {
			return false
		}
	}
	return true
}

// ---- releases ----

type candidate struct {
	rs  *appsv1.ReplicaSet
	rev string
	num int64
	id  string
}

// settled says a release's verdict can no longer change, so it is not judged
// again: not on every Deployment update, not on every Pod change, not years
// later. An adopted release is never assessed. Once the window has closed (or
// a newer revision took over) the verdict stands; until then a degraded one can
// still gain reasons, and one that is still observing is judged as things happen.
func settled(r store.Release, now time.Time) bool {
	switch {
	case r.Origin == store.OriginAdopted:
		return true
	case r.Health == store.HealthObserving:
		return false
	case r.Health == store.HealthDegraded:
		if !r.SupersededAt.IsZero() {
			return true
		}
		return !r.ObserveUntil.IsZero() && !now.Before(r.ObserveUntil)
	}
	return true // healthy and insufficient are only ever reached once the window closed
}

// reconcileReleases brings a Deployment's releases up to date from its
// ReplicaSets and its own status, inside the caller's transaction. It returns
// the release of the Deployment's current revision, or nil when there is none.
//
// A ReplicaSet becomes a release when it carries a revision number no newer
// than the Deployment's current one. (The controller creates a ReplicaSet
// before it updates the Deployment's revision, and the two arrive on separate
// watches; a ReplicaSet ahead of the Deployment waits until the Deployment
// catches up, it is not "already replaced".) It is "observed" when OpsTide was
// watching when the revision appeared, and "adopted" when it was already
// there: its rollout was not seen, and OpsTide says so instead of assessing it.
// Of the older ReplicaSets Kubernetes keeps, only the newest is adopted, so a
// release can be compared with its predecessor; the rest are ignored.
func (c *Collector) reconcileReleases(ctx context.Context, w *store.Writer, s *scope, d *appsv1.Deployment, now time.Time) (*store.Release, error) {
	_, curNum, ok := release.Revision(d.Annotations)
	if !ok {
		return nil, nil
	}
	dUID := string(d.UID)
	lastSeen, known, err := w.WorkloadLastSeen(ctx, dUID)
	if err != nil {
		return nil, err
	}
	if err := w.UpsertWorkload(ctx, store.Workload{ID: dUID, Namespace: d.Namespace, Name: d.Name, FirstSeen: now, LastSeen: now}); err != nil {
		return nil, err
	}
	objs, err := s.replicaSets.GetIndexer().ByIndex(rsOwnerIndex, dUID)
	if err != nil {
		return nil, err
	}
	var cands []candidate
	for _, o := range objs {
		rs, ok := o.(*appsv1.ReplicaSet)
		if !ok {
			continue
		}
		if rev, num, ok := release.Revision(rs.Annotations); ok && num <= curNum {
			cands = append(cands, candidate{rs: rs, rev: rev, num: num, id: release.ID(dUID, string(rs.UID), rev)})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].num > cands[j].num })

	existing, err := w.ReleasesOfWorkload(ctx, dUID)
	if err != nil {
		return nil, err
	}
	rels := map[string]*store.Release{}
	orig := map[string]store.Release{}
	for i := range existing {
		rels[existing[i].ID] = &existing[i]
		orig[existing[i].ID] = existing[i]
	}
	var curCand *candidate
	for i := range cands {
		if cands[i].num == curNum {
			curCand = &cands[i]
			break
		}
	}

	// Was the Deployment watched without a gap since it was last reconciled? Then a
	// revision that is new now appeared while OpsTide watched, even when its
	// ReplicaSet is an old one a rollback brought back.
	continuous := known && c.watchedSince(ctx, w, d.Namespace, lastSeen, now)

	rollout := release.ClassifyRollout(d)
	adoptedPrev := false
	var fresh []string
	for i := range cands {
		cd := &cands[i]
		if rels[cd.id] != nil {
			continue
		}
		isCur := cd.num == curNum
		live := c.createdWhileWatching(ctx, w, d.Namespace, cd.rs.CreationTimestamp.Time) || (isCur && continuous)
		var origin string
		switch {
		case live:
			origin = store.OriginObserved
		case isCur:
			origin = store.OriginAdopted
		case !known && !adoptedPrev:
			origin, adoptedPrev = store.OriginAdopted, true
		default:
			continue
		}
		rel := c.newRelease(d, cd, origin, isCur, now)
		rels[cd.id] = &rel
		fresh = append(fresh, cd.id)
	}

	// Chain each release to the one before it, once.
	ordered := make([]*store.Release, 0, len(rels))
	for _, r := range rels {
		ordered = append(ordered, r)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].RevisionNum < ordered[j].RevisionNum })
	for i, r := range ordered {
		if r.PrevID == "" && i > 0 {
			r.PrevID = ordered[i-1].ID
		}
	}

	var current *store.Release
	for _, r := range ordered {
		switch {
		case curCand != nil && r.ReplicaSetUID == string(curCand.rs.UID) && r.RevisionNum == curNum:
			current = r
			if err := c.applyRollout(ctx, w, r, rollout, now); err != nil {
				return nil, err
			}
		case r.RevisionNum < curNum:
			if err := c.retire(ctx, w, r, curCand, s, now); err != nil {
				return nil, err
			}
		}
	}
	for _, r := range ordered {
		nr, _, err := c.reassess(ctx, w, *r, now)
		if err != nil {
			return nil, err
		}
		*r = nr
		if o, had := orig[r.ID]; !had || !reflect.DeepEqual(o, *r) {
			if err := w.UpsertRelease(ctx, *r); err != nil {
				return nil, err
			}
		}
	}
	for _, id := range fresh {
		r := rels[id]
		if r.Origin == store.OriginObserved {
			if err := c.releaseObservedEvent(ctx, w, *r, ordered, now); err != nil {
				return nil, err
			}
			c.wm.releases.Add(1)
		}
		// Pods of this ReplicaSet that arrived before the release did (the
		// ReplicaSet and the Deployment are separate watches) are read again.
		pods, _ := s.deployPods.GetIndexer().ByIndex(rsPodIndex, r.ReplicaSetUID)
		for _, o := range pods {
			if p, ok := o.(*corev1.Pod); ok {
				c.queue.Add(item{kind: DeployPods, namespace: p.Namespace, name: p.Name})
			}
		}
	}
	return current, nil
}

// createdWhileWatching says whether OpsTide was watching Deployments and
// ReplicaSets when a ReplicaSet was created. Kubernetes timestamps have a
// resolution of one second, so the check is made at the end of that second:
// a ReplicaSet created just after a watch began is not mistaken for one that
// was already there (the opposite mistake, one created in the last fraction of
// a second before it began, is possible and accepted).
func (c *Collector) createdWhileWatching(ctx context.Context, w *store.Writer, ns string, at time.Time) bool {
	at = at.Add(time.Second - time.Millisecond)
	for _, scope := range c.scopeNamesFor(ns)[:2] { // deployments, replicasets
		ok, err := w.WatchedThroughout(ctx, scope, at, at)
		if err != nil || !ok {
			return false
		}
	}
	return true
}

// watchedSince says whether Deployments and ReplicaSets were watched without a
// gap from `from` until `to`.
func (c *Collector) watchedSince(ctx context.Context, w *store.Writer, ns string, from, to time.Time) bool {
	for _, scope := range c.scopeNamesFor(ns)[:2] {
		ok, err := w.WatchedThroughout(ctx, scope, from, to)
		if err != nil || !ok {
			return false
		}
	}
	return true
}

func (c *Collector) newRelease(d *appsv1.Deployment, cd *candidate, origin string, isCur bool, now time.Time) store.Release {
	var dep *appsv1.Deployment
	if origin == store.OriginObserved && isCur {
		// The Deployment's own settings are the ones of this revision only while
		// it is being observed as it becomes current; later they say nothing about it.
		dep = d
	}
	snap := release.FromTemplate(cd.rs.Spec.Template, dep)
	rel := store.Release{
		ID: cd.id, WorkloadID: string(d.UID), Namespace: d.Namespace, Workload: d.Name,
		Revision: cd.rev, RevisionNum: cd.num, ReplicaSetUID: string(cd.rs.UID), ReplicaSetName: cd.rs.Name,
		TemplateHash: cd.rs.Labels["pod-template-hash"], Fingerprint: snap.Fingerprint(), Origin: origin,
		ObservedAt: now, StartedAt: mt(&cd.rs.CreationTimestamp),
		State: store.ReleaseObserved, StateAt: now, Health: store.HealthObserving, WindowSeconds: int(c.window / time.Second),
		Episodes: "[]", Trouble: "[]", ImageIDs: "{}", HealthReasons: "[]", Snapshot: snap.Encode(), AssessmentVersion: release.AssessmentVersion,
	}
	if origin == store.OriginAdopted {
		rel.Health = store.HealthInsufficient
		rel.HealthReasons = release.EncodeReasons([]string{"The release existed before OpsTide started watching, so its rollout and its first minutes were not observed."})
		rel.HealthAt = now
	}
	return rel
}

// releaseObservedEvent writes "revision N observed". It says "a rollback" only
// when the revision reuses a ReplicaSet an earlier release already had: the same
// Pod-template fingerprint is not enough, since the fingerprint leaves out
// environment values and commands (a `kubectl rollout restart` has the same one).
func (c *Collector) releaseObservedEvent(ctx context.Context, w *store.Writer, r store.Release, all []*store.Release, now time.Time) error {
	snap, _ := release.DecodeSnapshot(r.Snapshot)
	var images []string
	for _, ct := range snap.Containers {
		images = append(images, ct.Image)
	}
	facts := map[string]string{"workload": r.Workload, "revision": r.Revision, "replicaSet": r.ReplicaSetName, "images": strings.Join(images, " ")}
	summary := fmt.Sprintf("%s revision %s observed", r.Workload, r.Revision)
	for _, o := range all {
		if o.ID != r.ID && o.RevisionNum < r.RevisionNum && o.ReplicaSetUID == r.ReplicaSetUID {
			summary += fmt.Sprintf(" (rolled back to the ReplicaSet of revision %s)", o.Revision)
			facts["rollbackOf"] = o.Revision
			break
		}
	}
	_, err := c.addEvent(ctx, w, store.Event{
		ID: "release.observed/" + r.ID, Type: "release.observed", Namespace: r.Namespace, Kind: "Deployment", ResourceUID: r.WorkloadID,
		ResourceName: r.Workload, ReleaseID: r.ID, OccurredAt: r.StartedAt, ObservedAt: now, Severity: "info", Confidence: store.Observed,
		Summary: summary, Facts: facts,
	}, now)
	return err
}

// applyRollout copies what the Deployment's status says onto the release of
// its current revision, and writes the entries for what changed.
func (c *Collector) applyRollout(ctx context.Context, w *store.Writer, r *store.Release, ro release.Rollout, now time.Time) error {
	prevDesired := r.Desired
	r.Desired, r.Updated, r.Ready, r.Available = ro.Desired, ro.Updated, ro.Ready, ro.Available
	at := ro.At
	if at.IsZero() {
		at = now
	}
	// A superseded release that becomes current again (a rollback to it, under a
	// new revision, is a new release; this is the same revision seen again).
	r.SupersededAt = time.Time{}
	// Once completed a release stays completed: later availability trouble is
	// tracked as episodes and judged by the assessment, it does not undo the rollout.
	if r.CompletedAt.IsZero() {
		if ro.State != r.State || ro.Reason != r.StateReason {
			r.State, r.StateReason, r.StateAt = ro.State, ro.Reason, at
			switch ro.State {
			case release.StateStalled, release.StateFailed:
				// Kept for good: a rollout that was given up on and later recovered is not a clean one.
				tr := release.DecodeTrouble(r.Trouble)
				tr = append(tr, release.Trouble{State: ro.State, Reason: ro.Reason, At: at})
				r.Trouble = release.EncodeTrouble(tr)
				if _, err := c.addEvent(ctx, w, store.Event{
					ID: "release." + ro.State + "/" + r.ID + "/" + strconv.FormatInt(at.UnixMilli(), 10), Type: "release." + ro.State,
					Namespace: r.Namespace, Kind: "Deployment", ResourceUID: r.WorkloadID, ResourceName: r.Workload, ReleaseID: r.ID,
					OccurredAt: ro.At, ObservedAt: now, Severity: "error", Confidence: store.Derived,
					Summary: fmt.Sprintf("%s revision %s rollout %s: %s", r.Workload, r.Revision, ro.State, ro.Reason),
					Facts:   map[string]string{"workload": r.Workload, "revision": r.Revision, "reason": ro.Reason},
				}, now); err != nil {
					return err
				}
			}
		}
		if ro.State == release.StateCompleted {
			r.State, r.StateReason, r.CompletedAt, r.StateAt = release.StateCompleted, "", at, at
		}
	}
	if ro.Paused != r.Paused {
		r.Paused = ro.Paused
		typ, word := "release.resumed", "resumed"
		if ro.Paused {
			typ, word = "release.paused", "paused"
		}
		if _, err := c.addEvent(ctx, w, store.Event{
			ID: typ + "/" + r.ID + "/" + strconv.FormatInt(now.UnixMilli(), 10), Type: typ, Namespace: r.Namespace, Kind: "Deployment",
			ResourceUID: r.WorkloadID, ResourceName: r.Workload, ReleaseID: r.ID, ObservedAt: now, Severity: "info", Confidence: store.Observed,
			Summary: fmt.Sprintf("%s rollout %s", r.Workload, word), Facts: map[string]string{"workload": r.Workload, "revision": r.Revision},
		}, now); err != nil {
			return err
		}
	}
	if !r.CompletedAt.IsZero() {
		r.ObserveUntil = r.CompletedAt.Add(time.Duration(r.WindowSeconds) * time.Second)
		eps := release.DecodeEpisodes(r.Episodes)
		open := len(eps) > 0 && eps[len(eps)-1].Until.IsZero()
		switch {
		case ro.Available < ro.Desired && !open:
			ep := release.Episode{Since: now}
			// The episode began with a raised replica target while at least the earlier
			// target was available: record that, so the assessment can tell catching up
			// with a new target from losing what was serving.
			if prevDesired > 0 && ro.Desired > prevDesired && ro.Available >= prevDesired {
				ep.Scaling = &release.ScaleOpen{From: prevDesired, To: ro.Desired, At: now, MinAvailable: ro.Available, Last: ro.Available}
			}
			eps = append(eps, ep)
			r.Episodes = release.EncodeEpisodes(eps)
		case ro.Available >= ro.Desired && open:
			eps[len(eps)-1].Until = now
			r.Episodes = release.EncodeEpisodes(eps)
		case open:
			if sc := eps[len(eps)-1].Scaling; sc != nil {
				changed := false
				if ro.Desired > sc.To {
					sc.To, changed = ro.Desired, true
				}
				if ro.Available < sc.Last && ro.Desired >= sc.Last && !sc.Lost {
					sc.Lost, sc.LostFrom, sc.LostTo, changed = true, sc.Last, ro.Available, true
				}
				if ro.Available < sc.MinAvailable {
					sc.MinAvailable, changed = ro.Available, true
				}
				if ro.Available != sc.Last {
					sc.Last, changed = ro.Available, true
				}
				if changed {
					r.Episodes = release.EncodeEpisodes(eps)
				}
			}
		}
	}
	return nil
}

// retire records that a release is no longer the Deployment's current one,
// and when its ReplicaSet was scaled down or removed.
func (c *Collector) retire(ctx context.Context, w *store.Writer, r *store.Release, cur *candidate, s *scope, now time.Time) error {
	if r.SupersededAt.IsZero() && (r.Origin != store.OriginAdopted || r.State != store.ReleaseSuperseded) {
		// Superseded when the next revision appeared. The next revision's ReplicaSet
		// creation time says so, unless that ReplicaSet is an old one a rollback
		// brought back, in which case the change was seen now.
		at := now
		if cur != nil {
			if t := mt(&cur.rs.CreationTimestamp); t.After(r.ObservedAt) && t.Before(now) {
				at = t
			}
		}
		r.SupersededAt = at
		if r.CompletedAt.IsZero() {
			r.State, r.StateReason, r.StateAt = store.ReleaseSuperseded, "replaced by a newer revision", now
		}
		if r.Origin == store.OriginObserved {
			if _, err := c.addEvent(ctx, w, store.Event{
				ID: "release.superseded/" + r.ID, Type: "release.superseded", Namespace: r.Namespace, Kind: "Deployment", ResourceUID: r.WorkloadID,
				ResourceName: r.Workload, ReleaseID: r.ID, OccurredAt: at, ObservedAt: now, Severity: "info", Confidence: store.Observed,
				Summary: fmt.Sprintf("%s revision %s superseded by a newer revision", r.Workload, r.Revision),
				Facts:   map[string]string{"workload": r.Workload, "revision": r.Revision},
			}, now); err != nil {
				return err
			}
		}
	}
	// Close the availability episode of a release that is no longer current.
	if eps := release.DecodeEpisodes(r.Episodes); len(eps) > 0 && eps[len(eps)-1].Until.IsZero() && !r.SupersededAt.IsZero() {
		eps[len(eps)-1].Until = r.SupersededAt
		r.Episodes = release.EncodeEpisodes(eps)
	}
	if r.ScaledDownAt.IsZero() && !r.SupersededAt.IsZero() {
		// Looked up by name; a ReplicaSet Kubernetes has already removed is as scaled down as it gets.
		obj, exists, err := s.replicaSets.GetIndexer().GetByKey(r.Namespace + "/" + r.ReplicaSetName)
		if err != nil {
			return err
		}
		gone := !exists
		if exists {
			if rs, ok := obj.(*appsv1.ReplicaSet); ok {
				gone = string(rs.UID) != r.ReplicaSetUID || (rs.Spec.Replicas != nil && *rs.Spec.Replicas == 0 && rs.Status.Replicas == 0)
			}
		}
		if gone {
			r.ScaledDownAt = now
			if r.Origin == store.OriginObserved {
				_, err := c.addEvent(ctx, w, store.Event{
					ID: "release.scaled_down/" + r.ID, Type: "release.scaled_down", Namespace: r.Namespace, Kind: "ReplicaSet",
					ResourceUID: r.ReplicaSetUID, ResourceName: r.ReplicaSetName, ReleaseID: r.ID, ObservedAt: now, Severity: "info",
					Confidence: store.Observed, Summary: fmt.Sprintf("%s revision %s scaled down after replacement", r.Workload, r.Revision),
					Facts: map[string]string{"workload": r.Workload, "revision": r.Revision},
				}, now)
				return err
			}
		}
	}
	return nil
}

// reassess judges a release again from what is stored, unless its verdict is
// settled, and writes the entry for a verdict reached for the first time. It
// returns the updated release and whether anything changed; the caller stores it.
func (c *Collector) reassess(ctx context.Context, w *store.Writer, r store.Release, now time.Time) (store.Release, bool, error) {
	if settled(r, now) {
		return r, false, nil
	}
	pods, err := w.ReleasePods(ctx, r)
	if err != nil {
		return r, false, err
	}
	rollback, err := w.IsRollback(ctx, r)
	if err != nil {
		return r, false, err
	}
	in := release.InputFor(r, pods, rollback, now, 0, func(from, to time.Time) bool { return c.watched(ctx, w, r.Namespace, from, to) })
	v := release.Assess(in)
	reasons := release.EncodeReasons(v.Reasons)
	if v.Health == r.Health && reasons == r.HealthReasons {
		return r, false, nil
	}
	was := r.Health
	r.Health, r.HealthReasons, r.HealthAt = v.Health, reasons, now
	// A shortfall in availability that outlasted its grace is written once.
	for _, f := range v.Findings {
		if f.Kind != "unavailable" {
			continue
		}
		if _, err := c.addEvent(ctx, w, store.Event{
			ID: "release.unavailable/" + r.ID + "/" + strconv.FormatInt(f.At.UnixMilli(), 10), Type: "release.unavailable", Namespace: r.Namespace,
			Kind: "Deployment", ResourceUID: r.WorkloadID, ResourceName: r.Workload, ReleaseID: r.ID, OccurredAt: f.At, ObservedAt: now,
			Severity: "warning", Confidence: store.Derived, Summary: fmt.Sprintf("%s revision %s: %s", r.Workload, r.Revision, f.Summary),
			Facts: map[string]string{"workload": r.Workload, "revision": r.Revision},
		}, now); err != nil {
			return r, true, err
		}
	}
	if v.Health != was && v.Health != store.HealthObserving && r.Origin == store.OriginObserved {
		word, sev, idx := "healthy", "info", 0
		switch v.Health {
		case store.HealthDegraded:
			word, sev, idx = "degraded", "error", 1
		case store.HealthInsufficient:
			word, sev, idx = "insufficient", "info", 2
		}
		summary := fmt.Sprintf("%s revision %s: rule result for the observation window: %s", r.Workload, r.Revision, strings.ReplaceAll(v.Health, "_", " "))
		switch v.Health {
		case store.HealthDegraded:
			summary = fmt.Sprintf("%s revision %s had rule findings during observation", r.Workload, r.Revision)
		case store.HealthHealthy:
			summary = fmt.Sprintf("%s revision %s: no rule findings in the observation window", r.Workload, r.Revision)
		}
		if len(v.Reasons) > 0 {
			summary += ": " + v.Reasons[0]
		}
		added, err := c.addEvent(ctx, w, store.Event{
			ID: "release.health." + word + "/" + r.ID, Type: "release.health." + word, Namespace: r.Namespace, Kind: "Deployment",
			ResourceUID: r.WorkloadID, ResourceName: r.Workload, ReleaseID: r.ID, ObservedAt: now, Severity: sev, Confidence: store.Derived,
			Summary: summary, Facts: map[string]string{"workload": r.Workload, "revision": r.Revision, "window": strconv.Itoa(r.WindowSeconds/60) + " min"},
		}, now)
		if err != nil {
			return r, true, err
		}
		if added {
			c.wm.health[idx].Add(1)
		}
	}
	return r, true, nil
}

// ---- pods ----

func (c *Collector) podEvent(r store.Release, p *corev1.Pod, sg release.Signal, now time.Time, conf store.Confidence) store.Event {
	facts := map[string]string{"workload": r.Workload, "revision": r.Revision}
	if sg.Container != "" {
		facts["container"] = sg.Container
	}
	for k, v := range sg.Facts {
		facts[k] = v
	}
	key := ""
	if sg.Key != "" {
		key = "/" + sg.Key
	}
	return store.Event{
		ID: "pod." + sg.Type + "/" + string(p.UID) + key, Type: "pod." + sg.Type, Namespace: p.Namespace, Kind: "Pod", ResourceUID: string(p.UID),
		ResourceName: p.Name, ReleaseID: r.ID, WorkloadID: r.WorkloadID, OccurredAt: sg.At, ObservedAt: now, Severity: sg.Severity, Confidence: conf,
		Summary: r.Workload + ": " + sg.Summary, Facts: facts,
	}
}

// degrading are the Pod signals that can change a release's verdict. The
// routine ones (created, ready, terminated) are written but do not make the
// release be judged again; the sweep and the Deployment's own updates do that.
func degrading(t string) bool {
	switch t {
	case "restarted", "oomkilled", "waiting", "notready", "evicted":
		return true
	}
	return false
}

func (c *Collector) writePodSignals(ctx context.Context, w *store.Writer, rel store.Release, pod *corev1.Pod, sigs []release.Signal, now time.Time, conf store.Confidence) (bool, error) {
	judge := false
	for _, sg := range sigs {
		added, err := c.addEvent(ctx, w, c.podEvent(rel, pod, sg, now, conf), now)
		if err != nil {
			return false, err
		}
		if added {
			c.wm.podEvent(sg.Type)
		}
		judge = judge || degrading(sg.Type)
	}
	return judge, nil
}

// syncDeployPod records a Deployment-owned Pod's lifecycle. The Pod's release
// is found through its ReplicaSet's UID, never by name.
func (c *Collector) syncDeployPod(ctx context.Context, it item) error {
	s := c.scopeFor(it.namespace)
	if s == nil {
		return nil
	}
	pod, err := get[*corev1.Pod](s.deployPods, it.namespace, it.name, it.uid)
	if err != nil {
		return err
	}
	if pod == nil {
		if it.uid == "" {
			return nil
		}
		return c.podGone(ctx, it)
	}
	ref := metav1.GetControllerOf(pod)
	if ref == nil || ref.Kind != "ReplicaSet" {
		return nil
	}
	rs, err := get[*appsv1.ReplicaSet](s.replicaSets, pod.Namespace, ref.Name, ref.UID)
	if err != nil {
		return err
	}
	if rs == nil {
		if pod.DeletionTimestamp != nil {
			return nil // an orphan on its way out
		}
		return fmt.Errorf("ReplicaSet %s of Pod %s is not in the cache yet", ref.Name, pod.Name)
	}
	dref := metav1.GetControllerOf(rs)
	if dref == nil || dref.Kind != "Deployment" {
		return nil
	}
	_, rsNum, rsHasRev := release.Revision(rs.Annotations)
	now := c.now()
	return c.db.Update(ctx, func(w *store.Writer) error {
		rel, ok, err := w.LatestReleaseOfReplicaSet(ctx, string(rs.UID))
		if err != nil {
			return err
		}
		// A newer revision of this ReplicaSet than the latest release (a rollback
		// reusing it) needs the Deployment read again first.
		if !ok || (rsHasRev && rsNum > rel.RevisionNum) {
			d, err := get[*appsv1.Deployment](s.deployments, pod.Namespace, dref.Name, dref.UID)
			if err != nil {
				return err
			}
			if d == nil {
				return fmt.Errorf("the Deployment %s of Pod %s is not in the cache yet", dref.Name, pod.Name)
			}
			if _, err := c.reconcileReleases(ctx, w, s, d, now); err != nil {
				return err
			}
			if rel, ok, err = w.LatestReleaseOfReplicaSet(ctx, string(rs.UID)); err != nil {
				return err
			}
			// The Deployment has not caught up with this ReplicaSet's revision yet
			// (the controller creates the ReplicaSet first): try again shortly. The
			// Deployment's own sync re-queues this Pod as well. This holds for a
			// rollback too, whose Pods must not be written to the old release.
			if _, dNum, dHas := release.Revision(d.Annotations); rsHasRev && (!ok || rsNum > rel.RevisionNum) && (!dHas || dNum < rsNum) {
				return fmt.Errorf("revision %d of the Deployment %s is not current yet", rsNum, dref.Name)
			}
			if !ok {
				return nil // a ReplicaSet outside any revision history: not tracked
			}
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
		// A Pod already on record is observed as it changes. One first seen in an
		// adopted release, and created before OpsTide began, only sets a baseline.
		// (Both timestamps' second resolution is allowed for.)
		emit := had || rel.Origin == store.OriginObserved || !pod.CreationTimestamp.Add(time.Second).Before(rel.ObservedAt)
		// Its whole life was watched only if it was created while Pods were being watched.
		history := false
		if prev == nil {
			ok, err := w.WatchedThroughout(ctx, c.scopeNamesFor(pod.Namespace)[2], pod.CreationTimestamp.Add(time.Second-time.Millisecond), pod.CreationTimestamp.Add(time.Second-time.Millisecond))
			history = err == nil && ok
		}
		next, sigs := release.Observe(prev, pod, now, emit, history)
		enc := release.EncodePod(next)
		if had && rec.State == enc {
			return nil
		}
		firstRelease := rel.ID
		if had && rec.ReleaseID != "" {
			firstRelease = rec.ReleaseID
		}
		if err := w.UpsertPod(ctx, store.PodRecord{UID: string(pod.UID), ReleaseID: firstRelease, Namespace: pod.Namespace, Name: pod.Name,
			ReplicaSetUID: string(rs.UID), WorkloadID: string(dref.UID), CreatedAt: mt(&pod.CreationTimestamp), TerminatedAt: next.TermAt, State: enc,
			Kind: store.KindDeployment, Revision: rel.Revision, Node: pod.Spec.NodeName}); err != nil {
			return err
		}
		if !had {
			if err := w.RelinkEvidenceOfPod(ctx, string(pod.UID), string(rs.UID), string(dref.UID), "Pod ownerReference to ReplicaSet to Deployment (recorded)"); err != nil {
				return err
			}
		}
		judge, err := c.writePodSignals(ctx, w, rel, pod, sigs, now, store.Observed)
		if err != nil {
			return err
		}
		// Image IDs (digests) come from the first Pod that reports them.
		if ids := imageIDs(pod); ids != "" && (rel.ImageIDs == "{}" || rel.ImageIDs == "") {
			rel.ImageIDs = ids
			if err := w.UpsertRelease(ctx, rel); err != nil {
				return err
			}
		}
		if judge {
			nr, changed, err := c.reassess(ctx, w, rel, now)
			if err != nil {
				return err
			}
			if changed {
				return w.UpsertRelease(ctx, nr)
			}
		}
		return nil
	})
}

func imageIDs(p *corev1.Pod) string {
	m := map[string]string{}
	for _, cs := range p.Status.ContainerStatuses {
		if cs.ImageID != "" {
			m[cs.Name] = cs.ImageID
		}
	}
	if len(m) == 0 {
		return ""
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// podGone ends a Pod's record. The Pod's final state is the one the informer
// saw when it was deleted (kept until this has been written, so a retry still
// has it); none when the Pod vanished while OpsTide was not running, or when a
// relist only noticed it was gone. The entry says which, and says "unknown"
// when how it ended was not seen.
func (c *Collector) podGone(ctx context.Context, it item) error {
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
		rel, ok, err := w.Release(ctx, rec.ReleaseID)
		if err != nil {
			return err
		}
		// The Pod's latest release is the one that was current when it ended.
		if latest, lok, lerr := w.LatestReleaseOfReplicaSet(ctx, rec.ReplicaSetUID); lerr == nil && lok {
			rel, ok = latest, true
		}
		seen := last != nil && !it.tombstone // a final state the informer really saw
		var sigs []release.Signal
		if last != nil {
			prev, sigs = release.Observe(&prev, last, now, ok, true)
		}
		at := now
		if it.deletedAt > 0 {
			at = time.UnixMilli(it.deletedAt).UTC()
		}
		next := release.Gone(prev, at)
		if !seen {
			next.TermReason = "unknown"
		}
		if err := w.UpsertPod(ctx, store.PodRecord{UID: rec.UID, ReleaseID: rec.ReleaseID, Namespace: rec.Namespace, Name: rec.Name,
			ReplicaSetUID: rec.ReplicaSetUID, WorkloadID: rec.WorkloadID, CreatedAt: rec.CreatedAt, TerminatedAt: next.TermAt, State: release.EncodePod(next),
			Kind: rec.Kind, Revision: rec.Revision, Node: rec.Node, Ordinal: rec.Ordinal}); err != nil {
			return err
		}
		if !ok {
			return nil
		}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: rec.Name, Namespace: rec.Namespace, UID: types.UID(rec.UID)}}
		judge, err := c.writePodSignals(ctx, w, rel, pod, sigs, now, store.Observed)
		if err != nil {
			return err
		}
		expected := !rel.SupersededAt.IsZero() || !rel.ScaledDownAt.IsZero()
		sg := release.Signal{Type: "terminated", Severity: "info"}
		if it.deletedAt > 0 {
			sg.At = at
		}
		conf := store.Observed
		switch {
		case !seen:
			sg.Summary, conf = fmt.Sprintf("Pod %s was removed while OpsTide was not watching it; how it ended is not known", rec.Name), store.Unknown
		case expected:
			sg.Summary = fmt.Sprintf("Pod %s terminated (its ReplicaSet was replaced by a newer revision)", rec.Name)
		case prev.TermReason == "Failed" && !prev.Evicted:
			sg.Summary, sg.Severity = fmt.Sprintf("Pod %s ended in phase Failed and was removed", rec.Name), "warning"
		default:
			sg.Summary = fmt.Sprintf("Pod %s terminated (deleted or scaled down)", rec.Name)
		}
		if added, err := c.addEvent(ctx, w, c.podEvent(rel, pod, sg, now, conf), now); err != nil {
			return err
		} else if added {
			c.wm.podEvent("terminated")
		}
		if judge {
			nr, changed, err := c.reassess(ctx, w, rel, now)
			if err != nil {
				return err
			}
			if changed {
				return w.UpsertRelease(ctx, nr)
			}
		}
		return nil
	})
	if err == nil {
		c.gone.Delete(string(it.uid)) // only now: a retry above still had it
	}
	return err
}

// reconcileAbsentPods ends the records of Pods that vanished while OpsTide was
// not running. It cannot know how they ended, and the entry says so.
func (c *Collector) reconcileAbsentPods(ctx context.Context) error {
	live, err := c.db.LivePods(ctx)
	if err != nil {
		return err
	}
	for _, rec := range live {
		s := c.scopeFor(rec.Namespace)
		if s == nil {
			continue
		}
		p, err := get[*corev1.Pod](s.deployPods, rec.Namespace, rec.Name, types.UID(rec.UID))
		if err != nil {
			return err
		}
		if p == nil {
			c.queue.Add(item{kind: DeployPods, namespace: rec.Namespace, name: rec.Name, uid: types.UID(rec.UID), tombstone: true})
		}
	}
	return nil
}

// reconcileAbsentDeployments ends the observation of Deployments that were
// deleted while OpsTide was not running. When that happened is not known, so
// the deletion is dated by the time it was noticed.
func (c *Collector) reconcileAbsentDeployments(ctx context.Context) error {
	live, err := c.db.LiveWorkloads(ctx)
	if err != nil {
		return err
	}
	for _, wl := range live {
		if wl.Kind != store.KindDeployment {
			continue // StatefulSets and DaemonSets are reconciled with their own watches
		}
		s := c.scopeFor(wl.Namespace)
		if s == nil {
			continue
		}
		d, err := get[*appsv1.Deployment](s.deployments, wl.Namespace, wl.Name, types.UID(wl.ID))
		if err != nil {
			return err
		}
		if d == nil {
			c.queue.Add(item{kind: Deployments, namespace: wl.Namespace, name: wl.Name, uid: types.UID(wl.ID), tombstone: true})
		}
	}
	return nil
}

// retireDeleted ends the observation of a Deployment's releases once the
// Deployment is gone: nothing more will be learned about them.
func (c *Collector) retireDeleted(ctx context.Context, w *store.Writer, dUID string, at, now time.Time) error {
	rels, err := w.ReleasesOfWorkload(ctx, dUID)
	if err != nil {
		return err
	}
	for _, r := range rels {
		if r.SupersededAt.IsZero() && (r.Origin != store.OriginAdopted || r.State != store.ReleaseSuperseded) {
			r.SupersededAt = at
			if r.CompletedAt.IsZero() {
				r.State, r.StateReason, r.StateAt = store.ReleaseSuperseded, "the Deployment was deleted", now
			}
		}
		nr, _, err := c.reassess(ctx, w, r, now)
		if err != nil {
			return err
		}
		if err := w.UpsertRelease(ctx, nr); err != nil {
			return err
		}
	}
	return nil
}

// sweepReleases closes observation windows. It is the only timer release
// observation has: every 30 seconds it judges the few releases still being
// observed, so a window that ends or an availability loss that outlasts its
// grace is noticed even when nothing else changes.
func (c *Collector) sweepReleases(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		open, err := c.db.OpenReleases(ctx)
		if err != nil {
			c.log.Warn("listing releases under observation", "err", err)
			continue
		}
		for _, r := range open {
			now := c.now()
			err := c.db.Update(ctx, func(w *store.Writer) error {
				fresh, ok, err := w.Release(ctx, r.ID)
				if err != nil || !ok {
					return err
				}
				nr, changed, err := c.reassess(ctx, w, fresh, now)
				if err != nil || !changed {
					return err
				}
				return w.UpsertRelease(ctx, nr)
			})
			if err != nil && ctx.Err() == nil {
				c.log.Warn("assessing a release", "release", r.ID, "err", err)
			}
		}
	}
}
