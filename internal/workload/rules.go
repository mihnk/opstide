// Package workload holds the rules that turn what Kubernetes reports about a
// StatefulSet or a DaemonSet into a documented rollout state. They are pure: the
// same status gives the same state, and every note quotes the values it used.
//
// There is no "failed" state. Neither kind has a failure condition that Kubernetes
// reports for a rollout, so OpsTide does not make one up; a condition a controller
// does report is recorded as reported (see the collector).
package workload

import (
	"fmt"
	"strconv"
	"strings"
)

// Rollout states. They describe the revision update, not the workload's health.
const (
	// Unknown: Kubernetes has not reported status for the current generation.
	Unknown = "unknown"
	// Completed: nothing is left to update under the rule for the kind.
	Completed = "completed"
	// Updating: a newer revision is being rolled out.
	Updating = "updating"
	// Partitioned: a StatefulSet rolling update reached its partition boundary. The
	// Pods below the partition stay on the previous revision by design: not a failure.
	Partitioned = "partitioned"
	// AwaitingPodDeletion: the strategy is OnDelete, so a changed template replaces
	// no Pod until it is deleted. The template change is not a completed rollout.
	AwaitingPodDeletion = "awaiting_pod_deletion"
)

// Update strategies as Kubernetes names them.
const (
	RollingUpdate = "RollingUpdate"
	OnDelete      = "OnDelete"
)

// Rollout is a state and the observed values behind it.
type Rollout struct {
	State string
	Note  string
}

// StatefulSetInput is the part of a StatefulSet's spec and status the rule reads.
type StatefulSetInput struct {
	Generation, ObservedGeneration  int64
	Replicas, Updated               int
	CurrentRevision, UpdateRevision string
	Strategy                        string
	Partition                       int
}

// ClassifyStatefulSet applies the rule. It follows what `kubectl rollout status`
// reads: the update revision against the current one, and for a partitioned
// rolling update the number of Pods updated against replicas minus the partition.
func ClassifyStatefulSet(in StatefulSetInput) Rollout {
	if in.ObservedGeneration < in.Generation || in.UpdateRevision == "" {
		return Rollout{Unknown, fmt.Sprintf("Kubernetes has not yet reported status for generation %d (observed generation %d).", in.Generation, in.ObservedGeneration)}
	}
	if in.CurrentRevision == in.UpdateRevision {
		return Rollout{Completed, "The current revision equals the update revision."}
	}
	switch {
	case in.Strategy == OnDelete && in.Updated >= in.Replicas:
		// Under OnDelete the controller does not advance currentRevision, so the revisions stay
		// different; every Pod being on the update revision is what completes it.
		return Rollout{Completed, fmt.Sprintf("The update strategy is OnDelete: all %d of %d Pods are on the update revision (Kubernetes does not advance currentRevision under OnDelete).", in.Updated, in.Replicas)}
	case in.Strategy == OnDelete:
		return Rollout{AwaitingPodDeletion, fmt.Sprintf("The update strategy is OnDelete: a Pod is replaced only when it is deleted. %d of %d Pods are on the update revision.", in.Updated, in.Replicas)}
	case in.Partition > 0 && in.Updated >= in.Replicas-in.Partition:
		return Rollout{Partitioned, fmt.Sprintf("The rolling update reached its partition boundary (partition %d): %d of %d Pods are on the update revision, and Pods with a lower ordinal stay on the previous revision by design.", in.Partition, in.Updated, in.Replicas)}
	}
	return Rollout{Updating, fmt.Sprintf("The update revision differs from the current revision: %d of %d Pods are on the update revision.", in.Updated, in.Replicas)}
}

// DaemonSetInput is the part of a DaemonSet's spec and status the rule reads.
type DaemonSetInput struct {
	Generation, ObservedGeneration int64
	Desired, Updated               int
	Strategy                       string
}

// ClassifyDaemonSet applies the rule. A DaemonSet's status has no revision, so
// "completed" means every Pod Kubernetes wants scheduled runs the update revision
// (updatedNumberScheduled covers desiredNumberScheduled). Availability is reported
// beside it as counts and is not part of the state.
func ClassifyDaemonSet(in DaemonSetInput) Rollout {
	if in.ObservedGeneration < in.Generation {
		return Rollout{Unknown, fmt.Sprintf("Kubernetes has not yet reported status for generation %d (observed generation %d).", in.Generation, in.ObservedGeneration)}
	}
	if in.Updated >= in.Desired {
		return Rollout{Completed, fmt.Sprintf("%d of %d scheduled Pods run the update revision.", in.Updated, in.Desired)}
	}
	if in.Strategy == OnDelete {
		return Rollout{AwaitingPodDeletion, fmt.Sprintf("The update strategy is OnDelete: a Pod is replaced only when it is deleted. %d of %d scheduled Pods run the update revision.", in.Updated, in.Desired)}
	}
	return Rollout{Updating, fmt.Sprintf("%d of %d scheduled Pods run the update revision.", in.Updated, in.Desired)}
}

// Entries are the log entries a change of rollout produces, as short names
// (update_started, update_partitioned, update_awaiting_pod_deletion,
// update_completed). A first sight, or a status Kubernetes had not yet reported, is
// a baseline and produces none. A new update revision while an update is going on
// starts another.
func Entries(prev, next Rollout, revisionChanged bool) []string {
	if prev.State == "" || prev.State == Unknown || next.State == Unknown {
		return nil
	}
	var out []string
	switch next.State {
	case Completed:
		if prev.State != Completed {
			out = append(out, "update_completed")
		}
	case Updating:
		if prev.State == Completed || revisionChanged {
			out = append(out, "update_started")
		}
	case Partitioned:
		if prev.State == Completed || revisionChanged {
			out = append(out, "update_started")
		}
		if prev.State != Partitioned || revisionChanged {
			out = append(out, "update_partitioned")
		}
	case AwaitingPodDeletion:
		if prev.State == Completed || revisionChanged {
			out = append(out, "update_started")
		}
		if prev.State != AwaitingPodDeletion || revisionChanged {
			out = append(out, "update_awaiting_pod_deletion")
		}
	}
	return out
}

// Counts are the status counts a workload reports.
type Counts struct {
	Desired, Ready, Available, Updated, Current, Misscheduled int
}

// CountChange is one count that differs between two observations.
type CountChange struct {
	Field    string
	From, To int
}

// Changes lists the counts that differ, in a fixed order. current and
// misscheduled are only listed when the kind reports them (report is a set of field names).
func Changes(prev, next Counts, report ...string) []CountChange {
	want := map[string]bool{"desired": true, "ready": true, "available": true, "updated": true}
	for _, r := range report {
		want[r] = true
	}
	var out []CountChange
	add := func(f string, a, b int) {
		if want[f] && a != b {
			out = append(out, CountChange{f, a, b})
		}
	}
	add("desired", prev.Desired, next.Desired)
	add("ready", prev.Ready, next.Ready)
	add("available", prev.Available, next.Available)
	add("updated", prev.Updated, next.Updated)
	add("current", prev.Current, next.Current)
	add("misscheduled", prev.Misscheduled, next.Misscheduled)
	return out
}

// Describe writes changes as "ready 3 to 2, available 3 to 2".
func Describe(cs []CountChange) string {
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = fmt.Sprintf("%s %d to %d", c.Field, c.From, c.To)
	}
	return strings.Join(parts, ", ")
}

// PodRevisionMatches says whether a Pod's controller-revision-hash label names a
// ControllerRevision. StatefulSet Pods carry the revision's name, DaemonSet Pods its
// hash (the controller.kubernetes.io/hash label).
func PodRevisionMatches(podLabel, revisionName, revisionHash string) bool {
	return podLabel != "" && (podLabel == revisionName || (revisionHash != "" && podLabel == revisionHash))
}

// Ordinal is a StatefulSet Pod's index. The apps.kubernetes.io/pod-index label
// is read when present; otherwise the name is read the way the StatefulSet
// controller builds it: "<statefulset name>-<index>", exactly. A name that does not
// have that shape has no ordinal.
func Ordinal(podName, statefulSet, indexLabel string) (int, bool) {
	if indexLabel != "" {
		if n, err := strconv.Atoi(indexLabel); err == nil && n >= 0 {
			return n, true
		}
	}
	rest, ok := strings.CutPrefix(podName, statefulSet+"-")
	if !ok || rest == "" || strings.HasPrefix(rest, "+") || strings.HasPrefix(rest, "-") {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 0 || strconv.Itoa(n) != rest {
		return 0, false
	}
	return n, true
}
