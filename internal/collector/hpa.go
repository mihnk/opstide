package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mihnk/opstide/internal/store"
)

// HPACondition and HPAMetric are the allow-listed parts of an autoscaler's status.
type HPACondition struct {
	Type       string    `json:"type"`
	Status     string    `json:"status"`
	Reason     string    `json:"reason,omitempty"`
	Message    string    `json:"message,omitempty"`
	Transition time.Time `json:"transition,omitzero"`
}

// HPAMetric is one metric the autoscaler reports: the current value Kubernetes
// gave and the target in its spec, as text (units differ by metric type).
type HPAMetric struct {
	Type    string `json:"type"`
	Name    string `json:"name,omitempty"`
	Current string `json:"current,omitempty"`
	Target  string `json:"target,omitempty"`
}

const maxHPAMetrics = 8

func hpaConditions(h *autoscalingv2.HorizontalPodAutoscaler) []HPACondition {
	var out []HPACondition
	for _, c := range h.Status.Conditions {
		out = append(out, HPACondition{Type: string(c.Type), Status: string(c.Status), Reason: cut(c.Reason, 128), Message: cut(c.Message, 512), Transition: mt(&c.LastTransitionTime)})
	}
	return out
}

func quantityOf(t autoscalingv2.MetricTarget) string {
	switch {
	case t.AverageUtilization != nil:
		return strconv.Itoa(int(*t.AverageUtilization)) + "%"
	case t.AverageValue != nil:
		return t.AverageValue.String()
	case t.Value != nil:
		return t.Value.String()
	}
	return ""
}

func currentOf(v autoscalingv2.MetricValueStatus) string {
	switch {
	case v.AverageUtilization != nil:
		return strconv.Itoa(int(*v.AverageUtilization)) + "%"
	case v.AverageValue != nil:
		return v.AverageValue.String()
	case v.Value != nil:
		return v.Value.String()
	}
	return ""
}

// hpaMetrics pairs each reported current value with the target in the spec for
// the same metric. Only what Kubernetes reported is listed.
func hpaMetrics(h *autoscalingv2.HorizontalPodAutoscaler) []HPAMetric {
	var out []HPAMetric
	target := map[string]string{}
	key := func(t, n string) string { return t + "/" + n }
	for _, m := range h.Spec.Metrics {
		switch m.Type {
		case autoscalingv2.ResourceMetricSourceType:
			if m.Resource != nil {
				target[key("Resource", string(m.Resource.Name))] = quantityOf(m.Resource.Target)
			}
		case autoscalingv2.ContainerResourceMetricSourceType:
			if m.ContainerResource != nil {
				target[key("ContainerResource", string(m.ContainerResource.Name))] = quantityOf(m.ContainerResource.Target)
			}
		case autoscalingv2.PodsMetricSourceType:
			if m.Pods != nil {
				target[key("Pods", m.Pods.Metric.Name)] = quantityOf(m.Pods.Target)
			}
		case autoscalingv2.ObjectMetricSourceType:
			if m.Object != nil {
				target[key("Object", m.Object.Metric.Name)] = quantityOf(m.Object.Target)
			}
		case autoscalingv2.ExternalMetricSourceType:
			if m.External != nil {
				target[key("External", m.External.Metric.Name)] = quantityOf(m.External.Target)
			}
		}
	}
	add := func(typ, name, cur string) {
		if len(out) < maxHPAMetrics {
			out = append(out, HPAMetric{Type: typ, Name: cut(name, 128), Current: cut(cur, 64), Target: target[key(typ, name)]})
		}
	}
	for _, m := range h.Status.CurrentMetrics {
		switch m.Type {
		case autoscalingv2.ResourceMetricSourceType:
			if m.Resource != nil {
				add("Resource", string(m.Resource.Name), currentOf(m.Resource.Current))
			}
		case autoscalingv2.ContainerResourceMetricSourceType:
			if m.ContainerResource != nil {
				add("ContainerResource", string(m.ContainerResource.Name), currentOf(m.ContainerResource.Current))
			}
		case autoscalingv2.PodsMetricSourceType:
			if m.Pods != nil {
				add("Pods", m.Pods.Metric.Name, currentOf(m.Pods.Current))
			}
		case autoscalingv2.ObjectMetricSourceType:
			if m.Object != nil {
				add("Object", m.Object.Metric.Name, currentOf(m.Object.Current))
			}
		case autoscalingv2.ExternalMetricSourceType:
			if m.External != nil {
				add("External", m.External.Metric.Name, currentOf(m.External.Current))
			}
		}
	}
	return out
}

func decodeJSON[T any](s string) []T {
	var out []T
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

func encodeJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return "[]"
	}
	return string(b)
}

// syncHPA records an autoscaler's observed state and the transitions between
// two observations of it. The first sight of an autoscaler sets a baseline and
// writes no transition. The Deployment it targets is found by the name in its
// scaleTargetRef, in its own namespace: an explicit reference, not a guess, and
// recorded as that and nothing stronger.
func (c *Collector) syncHPA(ctx context.Context, it item) error {
	s := c.scopeFor(it.namespace)
	if s == nil {
		return nil
	}
	h, err := get[*autoscalingv2.HorizontalPodAutoscaler](s.hpas, it.namespace, it.name, it.uid)
	if err != nil {
		return err
	}
	now := c.now()
	if h == nil {
		if it.uid == "" {
			return nil
		}
		return c.db.Update(ctx, func(w *store.Writer) error { return w.MarkHPADeleted(ctx, string(it.uid), now) })
	}
	ref := h.Spec.ScaleTargetRef
	next := store.HPA{
		UID: string(h.UID), Namespace: h.Namespace, Name: h.Name, TargetAPI: cut(ref.APIVersion, 64), TargetKind: cut(ref.Kind, 64), TargetName: cut(ref.Name, maxField),
		MinReplicas: h.Spec.MinReplicas, MaxReplicas: h.Spec.MaxReplicas, CurrentReplicas: h.Status.CurrentReplicas, DesiredReplicas: h.Status.DesiredReplicas,
		LastScaleAt: mt(h.Status.LastScaleTime), Conditions: encodeJSON(hpaConditions(h)), Metrics: encodeJSON(hpaMetrics(h)),
	}
	if ref.Kind == "Deployment" {
		if d, err := get[*appsv1.Deployment](s.deployments, h.Namespace, ref.Name, ""); err != nil {
			return err
		} else if d != nil {
			next.TargetUID, next.WorkloadID = string(d.UID), string(d.UID)
		}
	}
	return c.db.Update(ctx, func(w *store.Writer) error {
		prev, had, err := w.HPAByUID(ctx, next.UID)
		if err != nil {
			return err
		}
		next.FirstSeenAt = now
		if had {
			next.FirstSeenAt = prev.FirstSeenAt
			if next.WorkloadID == "" && prev.TargetKind == next.TargetKind && prev.TargetName == next.TargetName {
				next.TargetUID, next.WorkloadID = prev.TargetUID, prev.WorkloadID // the Deployment was seen before
			}
			if sameHPA(prev, next) {
				return nil
			}
		}
		if err := w.SaveHPA(ctx, next); err != nil {
			return err
		}
		c.em.last.Store(now.UnixMilli())
		if !had {
			return w.RelinkHPAs(ctx, next.Namespace, next.TargetName, next.WorkloadID)
		}
		return c.hpaTransitions(ctx, w, prev, next, now)
	})
}

func sameHPA(a, b store.HPA) bool {
	return a.TargetKind == b.TargetKind && a.TargetName == b.TargetName && a.TargetUID == b.TargetUID && a.WorkloadID == b.WorkloadID &&
		a.CurrentReplicas == b.CurrentReplicas && a.DesiredReplicas == b.DesiredReplicas && a.MaxReplicas == b.MaxReplicas &&
		ptrEq(a.MinReplicas, b.MinReplicas) && a.LastScaleAt.Equal(b.LastScaleAt) && a.Conditions == b.Conditions && a.Metrics == b.Metrics && a.DeletedAt.IsZero()
}

func ptrEq(a, b *int32) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }

// hpaTransitions writes the entries for what changed between two observations.
// They say what Kubernetes reported changed; none says why.
func (c *Collector) hpaTransitions(ctx context.Context, w *store.Writer, prev, next store.HPA, now time.Time) error {
	base := func(typ, id string, sev, summary string, facts map[string]string, occurred time.Time) store.Event {
		facts["hpa"] = next.Name
		if next.TargetName != "" {
			facts["target"] = next.TargetKind + "/" + next.TargetName
		}
		if next.WorkloadID != "" {
			facts["targetRelationship"] = "SAME_RESOURCE_CONTEXT"
			facts["targetVia"] = "spec.scaleTargetRef name in the same namespace"
		}
		return store.Event{ID: id, Type: typ, Namespace: next.Namespace, Kind: "HorizontalPodAutoscaler", ResourceUID: next.UID, ResourceName: next.Name,
			WorkloadID: next.WorkloadID, OccurredAt: occurred, ObservedAt: now, Severity: sev, Confidence: store.Observed, Summary: summary, Facts: facts}
	}
	stamp := strconv.FormatInt(now.UnixMilli(), 10)
	// An autoscaler is created before its controller has written any status. Desired
	// replicas of 0 and no conditions mean "not reported yet", so the first status is a
	// baseline, not a change.
	reported := prev.DesiredReplicas != 0 || len(decodeJSON[HPACondition](prev.Conditions)) > 0
	if reported && prev.DesiredReplicas != 0 && prev.DesiredReplicas != next.DesiredReplicas {
		facts := map[string]string{"from": strconv.Itoa(int(prev.DesiredReplicas)), "to": strconv.Itoa(int(next.DesiredReplicas)), "currentReplicas": strconv.Itoa(int(next.CurrentReplicas))}
		if !next.LastScaleAt.IsZero() {
			facts["lastScaleTime"] = next.LastScaleAt.Format(time.RFC3339)
		}
		e := base("hpa.desired_replicas_changed", "hpa.desired_replicas_changed/"+next.UID+"/"+stamp+"/"+facts["to"], "info",
			fmt.Sprintf("HPA %s reported desired replicas changed from %d to %d", next.Name, prev.DesiredReplicas, next.DesiredReplicas), facts, time.Time{})
		if added, err := c.addEvent(ctx, w, e, now); err != nil {
			return err
		} else if added {
			c.em.hpa[0].Add(1)
		}
	}
	old := map[string]HPACondition{}
	for _, cd := range decodeJSON[HPACondition](prev.Conditions) {
		old[cd.Type] = cd
	}
	for _, cd := range decodeJSON[HPACondition](next.Conditions) {
		if len(old) == 0 {
			break // the first conditions the autoscaler reports
		}
		o, seen := old[cd.Type]
		if seen && o.Status == cd.Status && o.Reason == cd.Reason {
			continue
		}
		from := "not reported"
		if seen {
			from = o.Status
		}
		sev := "info"
		if (cd.Type == "ScalingLimited" && cd.Status == "True") || ((cd.Type == "AbleToScale" || cd.Type == "ScalingActive") && cd.Status == "False") {
			sev = "warning"
		}
		facts := map[string]string{"condition": cd.Type, "from": from, "to": cd.Status, "reason": cd.Reason, "message": cut(cd.Message, 512)}
		// The condition's own transition time belongs to a change of status. A change of
		// reason alone keeps the status's older time, so the entry carries none and sorts
		// by when OpsTide observed it.
		occurred := time.Time{}
		if from != cd.Status {
			occurred = cd.Transition
		}
		e := base("hpa.condition_changed", "hpa.condition_changed/"+next.UID+"/"+cd.Type+"/"+stamp, sev,
			fmt.Sprintf("HPA %s reported condition %s %s (status %s to %s)", next.Name, cd.Type, cd.Reason, from, cd.Status), facts, occurred)
		e.Reason = cd.Reason
		if added, err := c.addEvent(ctx, w, e, now); err != nil {
			return err
		} else if added {
			c.em.hpa[1].Add(1)
		}
	}
	return nil
}

// replicaTarget notices when a Deployment's replica target changed between two
// observations of it. It is a fact about the Deployment; the entry does not say
// what changed it, and does not assume an autoscaler did.
func (c *Collector) replicaTarget(ctx context.Context, w *store.Writer, d *appsv1.Deployment, releaseID string, want int32, now time.Time) error {
	id := string(d.UID)
	prev, known, err := w.WorkloadSpecReplicas(ctx, id)
	if err != nil {
		return err
	}
	if known && prev == want {
		return nil
	}
	if err := w.SetWorkloadSpecReplicas(ctx, id, want); err != nil {
		return err
	}
	if !known {
		return nil // a baseline
	}
	facts := map[string]string{"from": strconv.Itoa(int(prev)), "to": strconv.Itoa(int(want)), "deployment": d.Name}
	if hs, err := c.db.HPAsOfWorkload(ctx, id); err == nil {
		var names []string
		for _, h := range hs {
			if h.DeletedAt.IsZero() {
				names = append(names, h.Name)
			}
		}
		if len(names) > 0 {
			facts["hpaTargetingThisDeployment"] = strings.Join(names, ",") // listed as context; it is not claimed as the cause
		}
	}
	added, err := c.addEvent(ctx, w, store.Event{
		ID: "workload.replica_target_changed/" + id + "/" + strconv.FormatInt(now.UnixMilli(), 10) + "/" + strconv.Itoa(int(want)), Type: "workload.replica_target_changed",
		Namespace: d.Namespace, Kind: "Deployment", ResourceUID: id, ResourceName: d.Name, ReleaseID: releaseID, WorkloadID: id, ObservedAt: now,
		Severity: "info", Confidence: store.Observed,
		Summary: fmt.Sprintf("%s replica target changed from %d to %d", d.Name, prev, want), Facts: facts,
	}, now)
	if added {
		c.em.hpa[2].Add(1)
	}
	return err
}

// reconcileAbsentHPAs marks autoscalers deleted while OpsTide was not running.
func (c *Collector) reconcileAbsentHPAs(ctx context.Context) error {
	live, err := c.db.LiveHPAs(ctx)
	if err != nil {
		return err
	}
	for _, h := range live {
		s := c.scopeFor(h.Namespace)
		if s == nil {
			continue
		}
		cur, err := get[*autoscalingv2.HorizontalPodAutoscaler](s.hpas, h.Namespace, h.Name, types.UID(h.UID))
		if err != nil {
			return err
		}
		if cur == nil {
			c.queue.Add(item{kind: HPAs, namespace: h.Namespace, name: h.Name, uid: types.UID(h.UID), tombstone: true})
		}
	}
	return nil
}
