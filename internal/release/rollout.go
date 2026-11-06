package release

import (
	"fmt"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// States of a rollout, and the Kubernetes annotation that numbers revisions.
const (
	StateObserved    = "observed"
	StateProgressing = "progressing"
	StateCompleted   = "completed"
	StateStalled     = "stalled"
	StateFailed      = "failed"
	StateSuperseded  = "superseded"
	StateUnknown     = "unknown"

	AnnRevision = "deployment.kubernetes.io/revision"
)

// Revision is a ReplicaSet's or Deployment's revision number; ok is false
// when there is none (not every ReplicaSet belongs to a rollout history).
func Revision(annotations map[string]string) (string, int64, bool) {
	v := annotations[AnnRevision]
	n, err := strconv.ParseInt(v, 10, 64)
	if v == "" || err != nil || n < 0 {
		return "", 0, false
	}
	return v, n, true
}

// Rollout is what the Deployment's own status says about the revision that
// is current. It describes Kubernetes' rollout, nothing about health.
type Rollout struct {
	State       string
	Reason      string
	CompletedAt time.Time // when the controller reported the rollout complete
	At          time.Time // when the controller last updated the condition behind State
	Paused      bool
	Desired     int
	Updated     int
	Ready       int
	Available   int
}

// ClassifyRollout reads the current revision's rollout from the Deployment.
//
// Completed needs every replica updated and available and the controller
// reporting NewReplicaSetAvailable, as before. Stalled is the controller
// giving up on progress (ProgressDeadlineExceeded). Failed is a ReplicaFailure
// the controller reports (quota, admission). Anything else with work left is
// progressing. A Deployment the controller has not yet observed is "observed".
func ClassifyRollout(d *appsv1.Deployment) Rollout {
	want := int32(1)
	if d.Spec.Replicas != nil {
		want = *d.Spec.Replicas
	}
	st := d.Status
	r := Rollout{Paused: d.Spec.Paused, Desired: int(want), Updated: int(st.UpdatedReplicas), Ready: int(st.ReadyReplicas), Available: int(st.AvailableReplicas)}
	var progressing, failure *appsv1.DeploymentCondition
	for i := range d.Status.Conditions {
		switch c := &d.Status.Conditions[i]; c.Type {
		case appsv1.DeploymentProgressing:
			progressing = c
		case appsv1.DeploymentReplicaFailure:
			failure = c
		}
	}
	switch {
	case failure != nil && failure.Status == corev1.ConditionTrue:
		r.State, r.Reason, r.At = StateFailed, cond(failure), failure.LastUpdateTime.Time
	case progressing != nil && progressing.Status == corev1.ConditionFalse && progressing.Reason == "ProgressDeadlineExceeded":
		r.State, r.Reason, r.At = StateStalled, cond(progressing), progressing.LastUpdateTime.Time
	case progressing != nil && progressing.Status == corev1.ConditionTrue && progressing.Reason == "NewReplicaSetAvailable" &&
		st.ObservedGeneration >= d.Generation && st.Replicas == want && st.UpdatedReplicas == want && st.AvailableReplicas == want:
		r.State, r.Reason, r.CompletedAt, r.At = StateCompleted, "", progressing.LastUpdateTime.Time, progressing.LastUpdateTime.Time
	case st.ObservedGeneration < d.Generation:
		r.State, r.Reason = StateObserved, "the Deployment controller has not yet seen the latest spec"
	default:
		r.State = StateProgressing
		r.Reason = fmt.Sprintf("%d of %d replicas updated, %d available", st.UpdatedReplicas, want, st.AvailableReplicas)
	}
	return r
}

func cond(c *appsv1.DeploymentCondition) string {
	if c.Message != "" {
		return c.Reason + ": " + c.Message
	}
	return c.Reason
}
