package collector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"
	"unicode/utf8"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mihnk/opstide/internal/store"
)

// Kubernetes Events are evidence. An Event is one object with one UID, served
// by two APIs: events.k8s.io/v1 (read by default) and the older core/v1, which
// is read only when a cluster does not serve the first. The two are never read
// together, and the UID is the identity either way, so a cluster cannot make
// the same Event appear twice.
//
// What is kept is an allow-list: reason, type, message (cut to maxMessage
// bytes), the resource it is about and the one it is related to, who reported
// it, the times and the count Kubernetes gave. Nothing else of the Event is read.

const (
	// Events and HPAs are the coverage scopes of this evidence.
	Events = "events"
	HPAs   = "hpas"

	maxMessage = 1024
	maxField   = 253

	// countLogEvery limits how often a growing count writes a Log entry for the
	// same Event. The record itself always holds the latest count.
	countLogEvery = 5 * time.Minute
)

// evidenceKinds are the resources whose Events are kept: the ones OpsTide
// follows. Events about Nodes, Services, volumes and the rest are not read into the record.
var evidenceKinds = map[string]bool{
	"Pod": true, "ReplicaSet": true, "Deployment": true, "Job": true, "CronJob": true, "HorizontalPodAutoscaler": true,
	"StatefulSet": true, "DaemonSet": true, "Node": true,
}

type evidenceMetrics struct {
	observed, adopted, updated atomic.Uint64
	hpa                        [3]atomic.Uint64 // desired, condition, replica target
	last                       atomic.Int64
}

// EvidenceStats is a snapshot of evidence collection counters.
type EvidenceStats struct {
	Source                              string
	Observed, Adopted, Updated          uint64
	HPADesired, HPACondition, HPATarget uint64
	Last                                time.Time
}

func (m *evidenceMetrics) stats(source string) EvidenceStats {
	st := EvidenceStats{Source: source, Observed: m.observed.Load(), Adopted: m.adopted.Load(), Updated: m.updated.Load(),
		HPADesired: m.hpa[0].Load(), HPACondition: m.hpa[1].Load(), HPATarget: m.hpa[2].Load()}
	if ms := m.last.Load(); ms > 0 {
		st.Last = time.UnixMilli(ms).UTC()
	}
	return st
}

// pickEventsSource chooses the one API Events are read through.
func (c *Collector) pickEventsSource() string {
	switch c.eventsSourceOpt {
	case store.SourceCoreV1, store.SourceEventsV1:
		return c.eventsSourceOpt
	}
	list, err := c.client.Discovery().ServerResourcesForGroupVersion("events.k8s.io/v1")
	if err != nil {
		if apierrors.IsNotFound(err) {
			return store.SourceCoreV1
		}
		return store.SourceEventsV1 // cannot tell: the API a current cluster serves
	}
	for _, r := range list.APIResources {
		if r.Name == "events" {
			return store.SourceEventsV1
		}
	}
	return store.SourceCoreV1
}

// trimEvent keeps in memory only what is read, and cuts the text. An Event
// about a kind that is not followed is reduced to its identity.
func trimEvent(obj any) (any, error) {
	switch e := obj.(type) {
	case *eventsv1.Event:
		out := &eventsv1.Event{ObjectMeta: slimMeta(e.ObjectMeta), Regarding: refOnly(e.Regarding), Reason: cut(e.Reason, 128), Type: e.Type}
		if !evidenceKinds[e.Regarding.Kind] {
			return out, nil
		}
		out.EventTime, out.Series, out.Action = e.EventTime, e.Series, cut(e.Action, 128)
		out.ReportingController, out.ReportingInstance = cut(e.ReportingController, maxField), cut(e.ReportingInstance, maxField)
		out.Note = cutMessage(e.Note)
		if e.Related != nil {
			r := refOnly(*e.Related)
			out.Related = &r
		}
		out.DeprecatedFirstTimestamp, out.DeprecatedLastTimestamp, out.DeprecatedCount = e.DeprecatedFirstTimestamp, e.DeprecatedLastTimestamp, e.DeprecatedCount
		out.DeprecatedSource.Component = cut(e.DeprecatedSource.Component, maxField)
		return out, nil
	case *corev1.Event:
		out := &corev1.Event{ObjectMeta: slimMeta(e.ObjectMeta), InvolvedObject: refOnly(e.InvolvedObject), Reason: cut(e.Reason, 128), Type: e.Type}
		if !evidenceKinds[e.InvolvedObject.Kind] {
			return out, nil
		}
		out.Message = cutMessage(e.Message)
		out.FirstTimestamp, out.LastTimestamp, out.Count, out.EventTime, out.Series = e.FirstTimestamp, e.LastTimestamp, e.Count, e.EventTime, e.Series
		out.Action = cut(e.Action, 128)
		out.ReportingController, out.ReportingInstance = cut(e.ReportingController, maxField), cut(e.ReportingInstance, maxField)
		out.Source.Component = cut(e.Source.Component, maxField)
		if e.Related != nil {
			r := refOnly(*e.Related)
			out.Related = &r
		}
		return out, nil
	}
	return obj, nil
}

func slimMeta(m metav1.ObjectMeta) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: m.Name, Namespace: m.Namespace, UID: m.UID, ResourceVersion: m.ResourceVersion, CreationTimestamp: m.CreationTimestamp}
}

func refOnly(r corev1.ObjectReference) corev1.ObjectReference {
	return corev1.ObjectReference{Kind: cut(r.Kind, 64), Namespace: r.Namespace, Name: cut(r.Name, maxField), UID: r.UID}
}

// cut shortens s to at most n bytes on a character boundary.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func cutMessage(s string) string { return cut(s, maxMessage+utf8.UTFMax) } // a character over, to know it was cut

// normalizeEventsV1 and normalizeCore turn an Event into an evidence record.
// They do not pretend the two APIs mean the same thing by their times and
// counts: the record says which field each came from.
func normalizeEventsV1(e *eventsv1.Event) store.Evidence {
	ev := baseEvidence(store.SourceEventsV1, e.ObjectMeta, e.Reason, e.Type, e.Note, e.Action, e.Regarding, e.Related)
	ev.Reporter = reporter(e.ReportingController, e.ReportingInstance, e.DeprecatedSource.Component)
	// An event created through the older API and shown through this one carries
	// the older timestamps; a native one has eventTime, and series for repeats.
	ev.FirstAt = firstTime(mt(&e.DeprecatedFirstTimestamp), microTime(e.EventTime))
	switch {
	case e.Series != nil:
		ev.LastAt, ev.Count, ev.CountBasis = microTime(e.Series.LastObservedTime), int(e.Series.Count), "series"
	case !e.DeprecatedLastTimestamp.IsZero():
		ev.LastAt = mt(&e.DeprecatedLastTimestamp)
	default:
		ev.LastAt = microTime(e.EventTime)
	}
	if ev.CountBasis == "" && e.DeprecatedCount > 0 {
		ev.Count, ev.CountBasis = int(e.DeprecatedCount), "deprecatedCount"
	}
	return ev
}

func normalizeCore(e *corev1.Event) store.Evidence {
	ev := baseEvidence(store.SourceCoreV1, e.ObjectMeta, e.Reason, e.Type, e.Message, e.Action, e.InvolvedObject, e.Related)
	ev.Reporter = reporter(e.ReportingController, e.ReportingInstance, e.Source.Component)
	ev.FirstAt = firstTime(mt(&e.FirstTimestamp), microTime(e.EventTime))
	switch {
	case e.Series != nil:
		ev.LastAt, ev.Count, ev.CountBasis = microTime(e.Series.LastObservedTime), int(e.Series.Count), "series"
	case !e.LastTimestamp.IsZero():
		ev.LastAt = mt(&e.LastTimestamp)
	default:
		ev.LastAt = microTime(e.EventTime)
	}
	if ev.CountBasis == "" && e.Count > 0 {
		ev.Count, ev.CountBasis = int(e.Count), "count"
	}
	return ev
}

func baseEvidence(source string, m metav1.ObjectMeta, reason, typ, message, action string, regarding corev1.ObjectReference, related *corev1.ObjectReference) store.Evidence {
	ev := store.Evidence{
		ID: EvidenceID(string(m.UID)), Source: source, SourceUID: string(m.UID), Namespace: m.Namespace, Name: m.Name,
		Reason: cut(reason, 128), EventType: cut(typ, 32), Action: cut(action, 128),
		Regarding: store.Ref{Kind: regarding.Kind, Name: regarding.Name, UID: string(regarding.UID)},
	}
	ev.Message = message
	if len(message) > maxMessage {
		ev.Message, ev.MessageTruncated = cut(message, maxMessage), true
	}
	if related != nil {
		ev.Related = store.Ref{Kind: related.Kind, Name: related.Name, UID: string(related.UID)}
	}
	return ev
}

// EvidenceID is the identity of the evidence record for an Event: derived from
// the Event's own UID, so any API and any restart gives the same one.
func EvidenceID(sourceUID string) string { return "ev" + hashID("kubernetes.event|"+sourceUID) }

func reporter(controller, instance, component string) string {
	switch {
	case controller != "" && instance != "":
		return controller + "/" + instance
	case controller != "":
		return controller
	}
	return component
}

func microTime(t metav1.MicroTime) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return t.UTC().Truncate(time.Millisecond)
}

func firstTime(ts ...time.Time) time.Time {
	for _, t := range ts {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

// syncEvent records one Event. Nothing is written when nothing it reports changed.
func (c *Collector) syncEvent(ctx context.Context, it item) error {
	s := c.scopeFor(it.namespace)
	if s == nil {
		return nil
	}
	inf := s.eventsV1
	if c.eventsSource() == store.SourceCoreV1 {
		inf = s.eventsCore
	}
	obj, exists, err := inf.GetIndexer().GetByKey(it.namespace + "/" + it.name)
	if err != nil {
		return err
	}
	now := c.now()
	var ev store.Evidence
	if exists {
		switch e := obj.(type) {
		case *eventsv1.Event:
			ev = normalizeEventsV1(e)
		case *corev1.Event:
			ev = normalizeCore(e)
		default:
			return fmt.Errorf("unexpected %T in cache", obj)
		}
		if it.uid != "" && string(types.UID(ev.SourceUID)) != string(it.uid) {
			exists = false // replaced by an Event of the same name
		}
	}
	if !exists {
		if it.uid == "" {
			return nil
		}
		// Kubernetes dropped the Event (it expired, or was deleted). The evidence stays.
		return c.db.Update(ctx, func(w *store.Writer) error { return w.MarkEvidenceSourceGone(ctx, string(it.uid), now) })
	}
	if ev.SourceUID == "" || !evidenceKinds[ev.Regarding.Kind] {
		return nil
	}
	created := time.Time{}
	if o, ok := obj.(metav1.Object); ok {
		created = mt(ptr(o.GetCreationTimestamp()))
	}
	return c.db.Update(ctx, func(w *store.Writer) error {
		prev, had, err := w.EvidenceByUID(ctx, ev.SourceUID)
		if err != nil {
			return err
		}
		if err := c.linkEvidence(ctx, w, s, &ev); err != nil {
			return err
		}
		if had {
			ev.ObservedAt, ev.Origin = prev.ObservedAt, prev.Origin
			ev.CountLogged, ev.CountLoggedAt = prev.CountLogged, prev.CountLoggedAt
			keepLinks(&ev, prev)
			if sameEvidence(prev, ev) {
				return nil
			}
		} else {
			ev.ObservedAt = now
			// An Event that existed before OpsTide began watching is adopted: the record
			// says when OpsTide first saw it, never that it began then.
			ev.Origin = store.OriginAdoptedEvidence
			if !created.IsZero() {
				end := created.Truncate(time.Second).Add(time.Second - time.Millisecond)
				if ok, err := w.WatchedThroughout(ctx, scopeName(Events, c.nsOf(it.namespace)), end, end); err == nil && ok {
					ev.Origin = store.OriginObservedEvidence
				}
			}
		}
		warn := ev.EventType == "Warning"
		switch {
		case !had && warn:
			ev.CountLogged, ev.CountLoggedAt = ev.Count, now
		case had && warn && ev.Count > prev.CountLogged && (prev.CountLoggedAt.IsZero() || now.Sub(prev.CountLoggedAt) >= countLogEvery):
			ev.CountLogged, ev.CountLoggedAt = ev.Count, now
		}
		if err := w.SaveEvidence(ctx, ev); err != nil {
			return err
		}
		c.em.last.Store(now.UnixMilli())
		if !had {
			if ev.Origin == store.OriginAdoptedEvidence {
				c.em.adopted.Add(1)
			} else {
				c.em.observed.Add(1)
			}
		} else {
			c.em.updated.Add(1)
		}
		if !warn {
			return nil
		}
		switch {
		case !had:
			_, err = w.AddEvent(ctx, c.evidenceEntry("evidence.event", "evidence.event/"+ev.SourceUID, ev, now, 0))
		case ev.CountLogged != prev.CountLogged:
			_, err = w.AddEvent(ctx, c.evidenceEntry("evidence.count_changed", "evidence.count/"+ev.SourceUID+"/"+strconv.Itoa(ev.Count), ev, now, prev.CountLogged))
		}
		return err
	})
}

func ptr[T any](v T) *T { return &v }

// keepLinks keeps what was linked before when the caches can no longer say: a Job or
// Pod that was garbage-collected is gone from them, but the link was made from its
// UID and ownerReferences while it existed, and the Event's identity has not changed.
func keepLinks(ev *store.Evidence, prev store.Evidence) {
	if ev.ReplicaSetUID == "" {
		ev.ReplicaSetUID = prev.ReplicaSetUID
	}
	if ev.WorkloadID == "" {
		ev.WorkloadID = prev.WorkloadID
	}
	if ev.RunID == "" {
		ev.RunID = prev.RunID
	}
	if ev.OperationID == "" {
		ev.OperationID = prev.OperationID
	}
	if ev.HPAUID == "" {
		ev.HPAUID = prev.HPAUID
	}
	if ev.NodeID == "" {
		ev.NodeID = prev.NodeID
	}
	if ev.LinkVia == "" {
		ev.LinkVia = prev.LinkVia
	}
}

// sameEvidence compares what an Event reports and where OpsTide linked it.
func sameEvidence(a, b store.Evidence) bool {
	return a.Reason == b.Reason && a.EventType == b.EventType && a.Message == b.Message && a.Count == b.Count && a.CountBasis == b.CountBasis &&
		a.FirstAt.Equal(b.FirstAt) && a.LastAt.Equal(b.LastAt) && a.Regarding == b.Regarding && a.Related == b.Related && a.Reporter == b.Reporter &&
		a.ReplicaSetUID == b.ReplicaSetUID && a.WorkloadID == b.WorkloadID && a.RunID == b.RunID && a.OperationID == b.OperationID &&
		a.HPAUID == b.HPAUID && a.NodeID == b.NodeID && a.LinkVia == b.LinkVia && a.SourceDeletedAt.IsZero()
}

// evidenceEntry is the Log entry for a Warning Event: what Kubernetes reported,
// stated as reported. Normal Events are kept as evidence but do not fill the Log.
func (c *Collector) evidenceEntry(typ, id string, ev store.Evidence, now time.Time, fromCount int) store.Event {
	who := fmt.Sprintf("%s %s", ev.Regarding.Kind, ev.Regarding.Name)
	summary := fmt.Sprintf("Kubernetes reported %s for %s", ev.Reason, who)
	facts := map[string]string{"reason": ev.Reason, "eventType": ev.EventType, "source": ev.Source, "origin": ev.Origin, "evidenceId": ev.ID,
		"message": cut(ev.Message, 512)}
	if ev.Count > 0 {
		facts["count"] = strconv.Itoa(ev.Count)
		facts["countBasis"] = ev.CountBasis
	}
	if typ == "evidence.count_changed" {
		summary = fmt.Sprintf("Kubernetes reported %s for %s again: count %d to %d", ev.Reason, who, fromCount, ev.Count)
		facts["countFrom"] = strconv.Itoa(fromCount)
	}
	if ev.Origin == store.OriginAdoptedEvidence {
		summary += " (already present when OpsTide began watching)"
	}
	conf := store.Observed
	return store.Event{
		ID: id, Type: typ, Namespace: ev.Namespace, Kind: ev.Regarding.Kind, ResourceUID: ev.Regarding.UID, ResourceName: ev.Regarding.Name,
		RunID: ev.RunID, OperationID: ev.OperationID, Reason: ev.Reason, WorkloadID: ev.WorkloadID,
		OccurredAt: firstTime(ev.LastAt, ev.FirstAt), ObservedAt: now, Severity: "warning", Confidence: conf, Summary: summary, Facts: facts,
	}
}

// reconcileAbsentEvidence marks Events that Kubernetes dropped while OpsTide was not
// running. When that happened is not known, so it is dated by the time it was noticed.
// It runs only once the Event watch has listed, so an unreadable API marks nothing.
func (c *Collector) reconcileAbsentEvidence(ctx context.Context) error {
	live, err := c.db.LiveEvidence(ctx)
	if err != nil {
		return err
	}
	now := c.now()
	var gone []string
	for _, e := range live {
		s := c.scopeFor(e.Namespace)
		if s == nil {
			continue
		}
		inf := s.eventsV1
		if c.eventsSource() == store.SourceCoreV1 {
			inf = s.eventsCore
		}
		obj, exists, err := inf.GetIndexer().GetByKey(e.Namespace + "/" + e.Name)
		if err != nil {
			return err
		}
		if exists {
			if o, ok := obj.(metav1.Object); ok && string(o.GetUID()) == e.SourceUID {
				continue
			}
		}
		gone = append(gone, e.SourceUID)
	}
	for len(gone) > 0 {
		n := min(len(gone), 500)
		batch := gone[:n]
		gone = gone[n:]
		if err := c.db.Update(ctx, func(w *store.Writer) error {
			for _, uid := range batch {
				if err := w.MarkEvidenceSourceGone(ctx, uid, now); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// nsOf maps an object's namespace to the namespace part of its scope name.
func (c *Collector) nsOf(ns string) string {
	if c.clusterWide {
		return ""
	}
	return ns
}

// linkEvidence resolves what an Event is about, using only the UID it states and
// ownerReferences: recorded ones first (they survive garbage collection), then the
// informer caches. An Event with no UID, or about something OpsTide never saw,
// stays unlinked.
func (c *Collector) linkEvidence(ctx context.Context, w *store.Writer, s *scope, ev *store.Evidence) error {
	ev.ReplicaSetUID, ev.WorkloadID, ev.RunID, ev.OperationID, ev.HPAUID, ev.NodeID, ev.LinkVia = "", "", "", "", "", "", ""
	uid, ns, name := ev.Regarding.UID, ev.Namespace, ev.Regarding.Name
	if uid == "" {
		return nil
	}
	switch ev.Regarding.Kind {
	case "Pod":
		if rec, ok, err := w.Pod(ctx, uid); err != nil {
			return err
		} else if ok {
			ev.ReplicaSetUID, ev.WorkloadID = rec.ReplicaSetUID, rec.WorkloadID
			ev.LinkVia = "Pod ownerReference to ReplicaSet to Deployment (recorded)"
			if rec.Kind != store.KindDeployment {
				ev.LinkVia = "Pod ownerReference to " + rec.Kind + " (recorded)"
			}
			return nil
		}
		// A Job's Pod, kept after the Pod and its Job are gone.
		if po, ok, err := w.PodOwnerByUID(ctx, uid); err != nil {
			return err
		} else if ok {
			ev.RunID, ev.OperationID, ev.LinkVia = po.RunID, po.OperationID, "Pod ownerReference to Job (recorded)"
			return nil
		}
		if p, err := get[*corev1.Pod](s.workloadPods, ns, name, types.UID(uid)); err != nil {
			return err
		} else if p != nil {
			if ref := metav1.GetControllerOf(p); ownedByApps(ref, "StatefulSet", "DaemonSet") {
				ev.WorkloadID, ev.LinkVia = string(ref.UID), "Pod ownerReference to "+ref.Kind
			}
			return nil
		}
		if p, err := get[*corev1.Pod](s.deployPods, ns, name, types.UID(uid)); err != nil {
			return err
		} else if p != nil {
			if ref := metav1.GetControllerOf(p); ref != nil && ref.Kind == "ReplicaSet" {
				ev.ReplicaSetUID, ev.LinkVia = string(ref.UID), "Pod ownerReference to ReplicaSet"
				ev.WorkloadID = c.workloadOfReplicaSet(ctx, w, s, ns, ref.Name, ref.UID)
				if ev.WorkloadID != "" {
					ev.LinkVia += " to Deployment"
				}
			}
			return nil
		}
		if p, err := get[*corev1.Pod](s.pods, ns, name, types.UID(uid)); err != nil {
			return err
		} else if p != nil {
			if ref := metav1.GetControllerOf(p); ref != nil && ref.Kind == "Job" {
				ev.RunID, ev.LinkVia = string(ref.UID), "Pod ownerReference to Job"
				ev.OperationID = c.operationOfJob(s, ns, ref.Name, ref.UID)
			}
		}
	case "ReplicaSet":
		ev.ReplicaSetUID, ev.LinkVia = uid, "regarding.uid"
		ev.WorkloadID = c.workloadOfReplicaSet(ctx, w, s, ns, name, types.UID(uid))
	case "Deployment", "StatefulSet", "DaemonSet":
		ev.WorkloadID, ev.LinkVia = uid, "regarding.uid"
	case "Node":
		// kubelet reports a Node's name as the UID of its Events; the Node controller reports the
		// real one. A UID OpsTide recorded links directly; otherwise the exact name of the one live
		// Node of that name is an explicit reference (Nodes are cluster-scoped), recorded as such.
		if _, ok, err := w.NodeByUID(ctx, uid); err != nil {
			return err
		} else if ok {
			ev.NodeID, ev.LinkVia = uid, "regarding.uid"
		} else if c.nodes != nil {
			// Only while Nodes are observed (records of Nodes no longer watched say nothing about the Node of that
			// name now), and only to the Node of that name that existed when the Event happened.
			at := firstTime(ev.FirstAt, ev.LastAt)
			if at.IsZero() {
				at = c.now()
			}
			if id, err := w.NodeIDByNameAt(ctx, name, at); err != nil {
				return err
			} else if id != "" {
				ev.NodeID, ev.LinkVia = id, "regarding names the Node (exact name; kubelet reports the name as the UID)"
			}
		}
	case "Job":
		ev.RunID, ev.LinkVia = uid, "regarding.uid"
		ev.OperationID = c.operationOfJob(s, ns, name, types.UID(uid))
	case "CronJob":
		ev.OperationID, ev.LinkVia = uid, "regarding.uid"
	case "HorizontalPodAutoscaler":
		ev.HPAUID, ev.LinkVia = uid, "regarding.uid"
		if h, ok, err := w.HPAByUID(ctx, uid); err != nil {
			return err
		} else if ok {
			ev.WorkloadID = h.WorkloadID
		}
	}
	return nil
}

// workloadOfReplicaSet finds the Deployment of a ReplicaSet by ownerReference,
// from a release OpsTide recorded or from the cache.
func (c *Collector) workloadOfReplicaSet(ctx context.Context, w *store.Writer, s *scope, ns, name string, uid types.UID) string {
	if rel, ok, err := w.LatestReleaseOfReplicaSet(ctx, string(uid)); err == nil && ok {
		return rel.WorkloadID
	}
	if rs, err := get[*appsv1.ReplicaSet](s.replicaSets, ns, name, uid); err == nil && rs != nil {
		if ref := metav1.GetControllerOf(rs); ref != nil && ref.Kind == "Deployment" {
			return string(ref.UID)
		}
	}
	return ""
}

// operationOfJob finds the CronJob of a Job by ownerReference.
func (c *Collector) operationOfJob(s *scope, ns, name string, uid types.UID) string {
	if j, err := get[*batchv1.Job](s.jobs, ns, name, uid); err == nil && j != nil {
		if ref := metav1.GetControllerOf(j); ref != nil && ref.Kind == "CronJob" {
			return string(ref.UID)
		}
	}
	return ""
}

func hashID(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:20]
}
