package release

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// PodState is the compact lifecycle record kept for a Deployment-owned Pod.
// It holds what was observed, with the time of each observation; where
// Kubernetes gave no time it stays zero rather than being guessed.
type PodState struct {
	Phase       string                   `json:"phase,omitempty"`
	Node        string                   `json:"node,omitempty"`
	ScheduledAt time.Time                `json:"scheduledAt,omitzero"`
	ReadyAt     time.Time                `json:"readyAt,omitzero"` // the first time it was Ready
	Ready       bool                     `json:"ready"`
	Transitions []Transition             `json:"transitions,omitempty"` // Ready / NotReady changes seen, newest last
	Containers  map[string]*ContainerObs `json:"containers,omitempty"`
	Deleting    bool                     `json:"deleting,omitempty"`
	DeletedAt   time.Time                `json:"deletedAt,omitzero"` // deletionTimestamp, when Kubernetes set one
	Evicted     bool                     `json:"evicted,omitempty"`
	EvictedAt   time.Time                `json:"evictedAt,omitzero"` // when it was seen evicted
	Terminated  bool                     `json:"terminated,omitempty"`
	TermAt      time.Time                `json:"termAt,omitzero"`
	TermSeen    time.Time                `json:"termSeen,omitzero"` // when the end was observed
	TermReason  string                   `json:"termReason,omitempty"`
	// Quiet is set for a Pod first seen in an adopted release: its past was not
	// observed, so no events were made from it and none are replayed later.
	Quiet bool `json:"quiet,omitempty"`
}

// Transition is a readiness change.
type Transition struct {
	At    time.Time `json:"at"`
	Ready bool      `json:"ready"`
}

// ContainerObs is what was seen of one container.
type ContainerObs struct {
	Restarts   int32     `json:"restarts"`
	Restarted  []Restart `json:"restarted,omitempty"`
	OOM        []OOMObs  `json:"oom,omitempty"`
	Waiting    []WaitObs `json:"waiting,omitempty"`
	LastReason string    `json:"lastReason,omitempty"`
	LastExit   *int32    `json:"lastExit,omitempty"`
	Init       bool      `json:"init,omitempty"`
}

// Restart is an observed increase of a container's restart count.
type Restart struct {
	// At is when the previous run ended, when Kubernetes said so (lastState
	// of the container); zero when it did not. It is never the observation time.
	At       time.Time `json:"at,omitzero"`
	Observed time.Time `json:"observed"`
	Count    int32     `json:"count"`
	Delta    int32     `json:"delta"`
	Reason   string    `json:"reason,omitempty"`
	Exit     *int32    `json:"exit,omitempty"`
}

// OOMObs is an out-of-memory kill, identified by when the container ended.
type OOMObs struct {
	At       time.Time `json:"at"`
	Observed time.Time `json:"observed"`
}

// WaitObs is a waiting reason worth recording, once per reason.
type WaitObs struct {
	Reason   string    `json:"reason"`
	Observed time.Time `json:"observed"`
}

const (
	maxKept = 40 // newest observations kept per list
)

// WaitReasons are container waiting reasons that mean something is wrong.
// ContainerCreating and PodInitializing are normal and ignored. CrashLoopBackOff
// is a waiting reason, not a Pod phase.
var WaitReasons = map[string]bool{
	"CrashLoopBackOff": true, "ImagePullBackOff": true, "ErrImagePull": true, "CreateContainerConfigError": true,
	"CreateContainerError": true, "InvalidImageName": true, "RunContainerError": true,
}

// Signal is something worth a timeline entry that Observe found.
type Signal struct {
	Type      string // created, ready, notready, restarted, oomkilled, waiting, evicted, terminated
	Key       string // makes the entry's ID unique and stable across repeats
	At        time.Time
	Container string
	Severity  string // info, warning, error
	Summary   string
	Facts     map[string]string
}

// Observe folds a Pod's current state into its record and returns the signals
// that are new. prev is nil the first time the Pod is seen. emit is false for
// a Pod of an adopted release: its state is kept but nothing is replayed.
// history says the Pod's whole life was watched (it was created while its
// Pods were being watched); when it is false, what a Pod already did before
// it was first seen (restarts, kills, waiting reasons) is only a baseline.
func Observe(prev *PodState, p *corev1.Pod, now time.Time, emit, history bool) (PodState, []Signal) {
	var next PodState
	if prev != nil {
		next = *prev
		next.Transitions = append([]Transition(nil), prev.Transitions...)
		next.Containers = map[string]*ContainerObs{}
		for k, v := range prev.Containers {
			c := *v
			next.Containers[k] = &c
		}
	} else {
		next.Containers = map[string]*ContainerObs{}
		next.Quiet = !emit
	}
	// A Pod first seen in an adopted release only sets a baseline: what it had
	// already done is marked as predating the observation (a zero Observed),
	// so it is neither replayed as an event nor counted against the release.
	// What it does afterwards is observed like anything else.
	baseline := prev == nil && (!emit || !history)
	var sigs []Signal
	add := func(s Signal) {
		if emit {
			sigs = append(sigs, s)
		}
	}
	name := p.Name

	next.Phase, next.Node = string(p.Status.Phase), p.Spec.NodeName
	if prev == nil {
		add(Signal{Type: "created", Key: "", At: p.CreationTimestamp.Time, Severity: "info", Summary: fmt.Sprintf("Pod %s created", name)})
	}
	for _, c := range p.Status.Conditions {
		switch c.Type {
		case corev1.PodScheduled:
			if c.Status == corev1.ConditionTrue && next.ScheduledAt.IsZero() {
				next.ScheduledAt = c.LastTransitionTime.Time
			}
		case corev1.PodReady:
			ready := c.Status == corev1.ConditionTrue
			switch {
			case ready && !next.Ready:
				next.Ready = true
				at := c.LastTransitionTime.Time
				if next.ReadyAt.IsZero() {
					next.ReadyAt = at
				}
				next.Transitions = keepLast(append(next.Transitions, Transition{At: at, Ready: true}))
				add(Signal{Type: "ready", Key: fmt.Sprint(at.UnixMilli()), At: at, Severity: "info", Summary: fmt.Sprintf("Pod %s became Ready", name)})
			case !ready && next.Ready:
				next.Ready = false
				at := c.LastTransitionTime.Time
				next.Transitions = keepLast(append(next.Transitions, Transition{At: at, Ready: false}))
				sev, why := "warning", "Pod "+name+" became NotReady"
				if p.DeletionTimestamp != nil || next.Deleting {
					sev, why = "info", "Pod "+name+" became NotReady while terminating"
				}
				add(Signal{Type: "notready", Key: fmt.Sprint(at.UnixMilli()), At: at, Severity: sev, Summary: why, Facts: map[string]string{"reason": c.Reason}})
			}
		}
	}
	if p.DeletionTimestamp != nil {
		next.Deleting = true
		next.DeletedAt = p.DeletionTimestamp.Time
	}

	observeContainers := func(list []corev1.ContainerStatus, init bool) {
		for _, cs := range list {
			o := next.Containers[cs.Name]
			if o == nil {
				o = &ContainerObs{Init: init}
				next.Containers[cs.Name] = o
			}
			// Restarts: a count that rose is an observed restart. When it jumped
			// by more than one, the ones in between were not seen; the entry says
			// so through its delta instead of inventing each one.
			if baseline {
				o.Restarts = cs.RestartCount
				for _, t := range []*corev1.ContainerStateTerminated{cs.LastTerminationState.Terminated, cs.State.Terminated} {
					if t != nil && t.Reason == "OOMKilled" && !hasOOM(o, t.FinishedAt.Time) {
						o.OOM = append(o.OOM, OOMObs{At: t.FinishedAt.Time})
					}
				}
				if w := cs.State.Waiting; w != nil && WaitReasons[w.Reason] {
					o.Waiting = append(o.Waiting, WaitObs{Reason: w.Reason})
				}
				continue
			}
			if cs.RestartCount > o.Restarts {
				delta := cs.RestartCount - o.Restarts
				r := Restart{Observed: now, Count: cs.RestartCount, Delta: delta}
				if t := cs.LastTerminationState.Terminated; t != nil {
					r.Reason, r.Exit = t.Reason, &t.ExitCode
					// The previous run's end time is exact only for the latest restart.
					if delta == 1 && !t.FinishedAt.IsZero() {
						r.At = t.FinishedAt.Time
					}
					o.LastReason, o.LastExit = t.Reason, &t.ExitCode
				}
				o.Restarted = keepLastR(append(o.Restarted, r))
				facts := map[string]string{"restarts": fmt.Sprint(cs.RestartCount), "delta": fmt.Sprint(delta), "reason": r.Reason}
				if r.Exit != nil {
					facts["exitCode"] = fmt.Sprint(*r.Exit)
				}
				sum := fmt.Sprintf("Container %s restarted in pod %s", cs.Name, name)
				if r.Reason != "" {
					sum += " (" + r.Reason + ")"
				}
				if delta > 1 {
					sum += fmt.Sprintf(", %d restarts since last seen", delta)
				}
				add(Signal{Type: "restarted", Key: cs.Name + "/" + fmt.Sprint(cs.RestartCount), At: r.At, Container: cs.Name, Severity: "warning", Summary: sum, Facts: facts})
			}
			if cs.RestartCount > o.Restarts {
				o.Restarts = cs.RestartCount
			}
			// Out of memory: from the last or the current termination.
			for _, t := range []*corev1.ContainerStateTerminated{cs.LastTerminationState.Terminated, cs.State.Terminated} {
				if t == nil || t.Reason != "OOMKilled" {
					continue
				}
				at := t.FinishedAt.Time
				if hasOOM(o, at) {
					continue
				}
				o.OOM = keepLastO(append(o.OOM, OOMObs{At: at, Observed: now}))
				o.LastReason, o.LastExit = t.Reason, &t.ExitCode
				add(Signal{Type: "oomkilled", Key: cs.Name + "/" + fmt.Sprint(at.UnixMilli()), At: at, Container: cs.Name, Severity: "error",
					Summary: fmt.Sprintf("Container %s in pod %s was OOMKilled", cs.Name, name),
					Facts:   map[string]string{"exitCode": fmt.Sprint(t.ExitCode)}})
			}
			if w := cs.State.Waiting; w != nil && WaitReasons[w.Reason] && !hasWait(o, w.Reason) {
				o.Waiting = keepLastW(append(o.Waiting, WaitObs{Reason: w.Reason, Observed: now}))
				add(Signal{Type: "waiting", Key: cs.Name + "/" + w.Reason, Container: cs.Name, Severity: "warning",
					Summary: fmt.Sprintf("Container %s in pod %s is waiting: %s", cs.Name, name, w.Reason),
					Facts:   map[string]string{"reason": w.Reason, "message": clipMsg(w.Message)}})
			}
		}
	}
	observeContainers(p.Status.InitContainerStatuses, true)
	observeContainers(p.Status.ContainerStatuses, false)

	if p.Status.Reason == "Evicted" && !next.Evicted {
		next.Evicted, next.EvictedAt = true, now
		add(Signal{Type: "evicted", Severity: "warning", Summary: fmt.Sprintf("Pod %s was evicted", name),
			Facts: map[string]string{"message": clipMsg(p.Status.Message)}})
	}
	// A Pod that ran to a terminal phase has ended even though its object stays.
	if (p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed) && !next.Terminated {
		next.Terminated, next.TermReason, next.TermSeen = true, string(p.Status.Phase), now
		if t := lastFinish(p); !t.IsZero() {
			next.TermAt = t
		}
	}
	return next, sigs
}

// Gone ends a Pod's record because its object was removed. at is the
// deletionTimestamp when known, else the observation time; known says which.
// The final summary and its severity are decided by the caller, who knows
// whether the ReplicaSet was being replaced.
func Gone(prev PodState, at time.Time) PodState {
	prev.Terminated = true
	if prev.TermSeen.IsZero() {
		prev.TermSeen = at
	}
	if prev.TermAt.IsZero() {
		prev.TermAt = at
	}
	if prev.TermReason == "" {
		prev.TermReason = "deleted"
	}
	return prev
}

func lastFinish(p *corev1.Pod) time.Time {
	var t time.Time
	for _, cs := range p.Status.ContainerStatuses {
		if x := cs.State.Terminated; x != nil && x.FinishedAt.After(t) {
			t = x.FinishedAt.Time
		}
	}
	return t
}

func hasOOM(o *ContainerObs, at time.Time) bool {
	for _, x := range o.OOM {
		if x.At.Equal(at) {
			return true
		}
	}
	return false
}

func hasWait(o *ContainerObs, reason string) bool {
	for _, x := range o.Waiting {
		if x.Reason == reason {
			return true
		}
	}
	return false
}

func clipMsg(s string) string {
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

func keepLast(s []Transition) []Transition {
	if len(s) > maxKept {
		return s[len(s)-maxKept:]
	}
	return s
}
func keepLastR(s []Restart) []Restart {
	if len(s) > maxKept {
		return s[len(s)-maxKept:]
	}
	return s
}
func keepLastO(s []OOMObs) []OOMObs {
	if len(s) > maxKept {
		return s[len(s)-maxKept:]
	}
	return s
}
func keepLastW(s []WaitObs) []WaitObs {
	if len(s) > maxKept {
		return s[len(s)-maxKept:]
	}
	return s
}
