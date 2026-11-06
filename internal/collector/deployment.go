package collector

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/mihnk/opstide/internal/store"
)

const annRevision = "deployment.kubernetes.io/revision"

// syncDeployment keeps a Deployment's releases up to date and records a
// rollout once it has finished: every replica updated and available, and the
// controller reporting the new ReplicaSet available. It says nothing about
// who rolled it out; that needs audit logs or GitOps history OpsTide does not
// read. Health after the rollout is the release's own assessment, not this.
func (c *Collector) syncDeployment(ctx context.Context, it item) error {
	s := c.scopeFor(it.namespace)
	if s == nil {
		return nil
	}
	d, err := get[*appsv1.Deployment](s.deployments, it.namespace, it.name, it.uid)
	if err != nil {
		return err
	}
	if d == nil {
		if it.uid == "" {
			return nil
		}
		at := c.now()
		if it.deletedAt > 0 {
			at = msTime(it.deletedAt)
		}
		return c.db.Update(ctx, func(w *store.Writer) error {
			if err := w.MarkWorkloadDeleted(ctx, string(it.uid), at); err != nil {
				return err
			}
			return c.retireDeleted(ctx, w, string(it.uid), at, c.now())
		})
	}
	rev := d.Annotations[annRevision]
	if rev == "" {
		return nil
	}
	var progressing *appsv1.DeploymentCondition
	for i := range d.Status.Conditions {
		if cond := &d.Status.Conditions[i]; cond.Type == appsv1.DeploymentProgressing {
			progressing = cond
		}
	}
	want := int32(1)
	if d.Spec.Replicas != nil {
		want = *d.Spec.Replicas
	}
	st := d.Status
	done := progressing != nil && progressing.Status == corev1.ConditionTrue && progressing.Reason == "NewReplicaSetAvailable" &&
		st.ObservedGeneration >= d.Generation && st.Replicas == want && st.UpdatedReplicas == want && st.AvailableReplicas == want
	now := c.now()
	return c.db.Update(ctx, func(w *store.Writer) error {
		cur, err := c.reconcileReleases(ctx, w, s, d, now)
		if err != nil {
			return err
		}
		if !done {
			return nil
		}
		imgs := images(d.Spec.Template.Spec)
		e := store.Event{
			ID:   fmt.Sprintf("deployment.rollout/%s/%s", d.UID, rev),
			Type: "deployment.rollout", Namespace: d.Namespace, Kind: "Deployment", ResourceUID: string(d.UID), ResourceName: d.Name,
			OccurredAt: mt(&progressing.LastUpdateTime), ObservedAt: now, Severity: "info", Confidence: store.Derived,
			Summary: fmt.Sprintf("%s rolled out revision %s, %d/%d ready", d.Name, rev, st.AvailableReplicas, want),
			Facts:   map[string]string{"revision": rev, "replicas": strconv.Itoa(int(want)), "images": strings.Join(imgs, " ")},
		}
		if cur != nil {
			e.ReleaseID = cur.ID
		}
		_, err = c.addEvent(ctx, w, e, now)
		return err
	})
}
