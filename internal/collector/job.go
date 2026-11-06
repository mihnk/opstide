package collector

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mihnk/opstide/internal/schedule"
	"github.com/mihnk/opstide/internal/store"
)

const (
	// Set by the CronJob controller on Jobs it creates (Kubernetes 1.32+).
	annScheduledTimestamp = "batch.kubernetes.io/cronjob-scheduled-timestamp"
	// Set by kubectl create job --from=cronjob/...
	annInstantiate = "cronjob.kubernetes.io/instantiate"
)

// scheduledAt works out which expected time a Job was created for.
func scheduledAt(job *batchv1.Job, cronJob string) (time.Time, string) {
	if v := job.Annotations[annScheduledTimestamp]; v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t.UTC(), "annotation"
		}
	}
	if job.Annotations[annInstantiate] == "manual" {
		return time.Time{}, "manual"
	}
	if cronJob != "" {
		if t, ok := schedule.JobNameTime(cronJob, job.Name); ok {
			return t, "name"
		}
	}
	return time.Time{}, ""
}

// jobOutcome reads a Job's status. Only the terminal conditions Complete
// and Failed end a run.
func jobOutcome(job *batchv1.Job) (status store.RunStatus, reason string, finished time.Time) {
	for _, cond := range job.Status.Conditions {
		if cond.Status != corev1.ConditionTrue {
			continue
		}
		switch cond.Type {
		case batchv1.JobComplete:
			finished = mt(job.Status.CompletionTime)
			if finished.IsZero() {
				finished = mt(&cond.LastTransitionTime)
			}
			return store.RunSucceeded, "", finished
		case batchv1.JobFailed:
			return store.RunFailed, cond.Reason, mt(&cond.LastTransitionTime)
		}
	}
	switch {
	case job.Spec.Suspend != nil && *job.Spec.Suspend:
		return store.RunPending, "Suspended", time.Time{}
	case job.Status.Active > 0 || job.Status.StartTime != nil:
		return store.RunRunning, "", time.Time{}
	}
	return store.RunPending, "", time.Time{}
}

func podFrom(p *corev1.Pod) store.Pod {
	sp := store.Pod{Name: p.Name, UID: string(p.UID), Phase: string(p.Status.Phase), Reason: p.Status.Reason, StartedAt: mt(p.Status.StartTime)}
	for _, cs := range p.Status.ContainerStatuses {
		t := cs.State.Terminated
		if t == nil {
			continue
		}
		// Report the first container that failed, else the first that ended.
		if sp.ExitCode == nil || *sp.ExitCode == 0 && t.ExitCode != 0 {
			code := t.ExitCode
			sp.ExitCode = &code
			if sp.Reason == "" || t.ExitCode != 0 {
				sp.Reason = t.Reason
			}
		}
		if f := mt(&t.FinishedAt); f.After(sp.FinishedAt) {
			sp.FinishedAt = f
		}
	}
	return sp
}

func (c *Collector) syncJob(ctx context.Context, it item) error {
	s := c.scopeFor(it.namespace)
	if s == nil {
		return nil
	}
	job, err := get[*batchv1.Job](s.jobs, it.namespace, it.name, it.uid)
	if err != nil {
		return err
	}
	if job == nil {
		if it.uid == "" {
			return nil
		}
		r, ok, err := c.db.Run(ctx, string(it.uid))
		if err != nil || !ok || (r.Status != store.RunPending && r.Status != store.RunRunning) {
			return err
		}
		return c.runLost(ctx, r, c.now())
	}

	now := c.now()
	run := store.Run{ID: string(job.UID), Namespace: job.Namespace, JobName: job.Name, CreatedAt: mt(&job.CreationTimestamp),
		StartedAt: mt(job.Status.StartTime), ObservedAt: now}
	cronJob := ""
	if ref := metav1.GetControllerOf(job); ref != nil && ref.Kind == "CronJob" {
		run.OperationID, cronJob = string(ref.UID), ref.Name
	}
	run.ScheduledAt, run.ScheduledBasis = scheduledAt(job, cronJob)
	if run.OperationID != "" {
		// Record the CronJob first, so the run's entries carry its name.
		if _, ok, err := c.db.Operation(ctx, run.OperationID); err != nil {
			return err
		} else if !ok {
			if err := c.syncCronJob(ctx, item{kind: CronJobs, namespace: job.Namespace, name: cronJob}); err != nil {
				return err
			}
		}
	}
	run.Status, run.StatusReason, run.FinishedAt = jobOutcome(job)
	run.Attempts = int(job.Status.Failed + job.Status.Succeeded + job.Status.Active)

	old, had, err := c.db.Run(ctx, run.ID)
	if err != nil {
		return err
	}
	// Keep pods already recorded: TTL cleanup removes them, the record stays.
	pods := map[string]store.Pod{}
	if had {
		run.ObservedAt = old.ObservedAt
		for _, p := range old.Pods {
			pods[p.UID] = p
		}
	}
	objs, err := s.pods.GetIndexer().ByIndex(jobIndex, run.ID)
	if err != nil {
		return err
	}
	nodeOf := map[string]string{}
	for _, o := range objs {
		if p, ok := o.(*corev1.Pod); ok {
			pods[string(p.UID)] = podFrom(p)
			nodeOf[string(p.UID)] = p.Spec.NodeName
		}
	}
	for _, p := range pods {
		run.Pods = append(run.Pods, p)
	}
	sort.Slice(run.Pods, func(i, j int) bool {
		a, b := run.Pods[i], run.Pods[j]
		if !a.StartedAt.Equal(b.StartedAt) {
			return a.StartedAt.Before(b.StartedAt)
		}
		return a.Name < b.Name
	})

	if had {
		cmp := old
		cmp.UpdatedAt = time.Time{}
		if reflect.DeepEqual(cmp, run) {
			return nil
		}
	}
	name := c.runName(ctx, run)
	base := store.Event{
		Namespace: run.Namespace, Kind: "Job", ResourceUID: run.ID, ResourceName: run.JobName,
		OperationID: run.OperationID, RunID: run.ID, ObservedAt: now, Severity: "info",
	}
	return c.db.Update(ctx, func(w *store.Writer) error {
		if err := w.UpsertRun(ctx, run); err != nil {
			return err
		}
		// Which Job each Pod belonged to is kept apart from the run, so an Event about a
		// Pod can be linked to its run after the Pod and the Job are garbage-collected, and
		// whichever of the Event and the Pod is learned first. Only ownerReferences are used.
		for _, p := range run.Pods {
			if err := w.SavePodOwner(ctx, store.PodOwner{PodUID: p.UID, Namespace: run.Namespace, Name: p.Name, RunID: run.ID, OperationID: run.OperationID, Node: nodeOf[p.UID], FirstSeenAt: now}); err != nil {
				return err
			}
			if err := w.RelinkEvidenceOfJobPod(ctx, p.UID, run.ID, run.OperationID); err != nil {
				return err
			}
		}
		if err := w.RelinkEvidenceOfRun(ctx, run.ID, run.OperationID); err != nil {
			return err
		}
		created := base
		created.ID, created.Type, created.OccurredAt, created.Confidence = "job.created/"+run.ID, "job.created", run.CreatedAt, store.Observed
		created.Summary, created.Facts = name+" run created", runFacts(run)
		if _, err := w.AddEvent(ctx, created); err != nil {
			return err
		}
		if run.Status != store.RunSucceeded && run.Status != store.RunFailed {
			return nil
		}
		done := base
		done.Type, done.OccurredAt, done.Confidence = "job."+string(run.Status), run.FinishedAt, store.Derived
		done.ID = done.Type + "/" + run.ID
		done.Facts = runFacts(run)
		done.Facts["attempts"] = strconv.Itoa(run.Attempts)
		if !run.StartedAt.IsZero() && !run.FinishedAt.IsZero() {
			done.Facts["seconds"] = strconv.FormatInt(int64(run.FinishedAt.Sub(run.StartedAt)/time.Second), 10)
		}
		if run.Status == store.RunSucceeded {
			done.Summary = name + " succeeded"
			done.Facts["basis"] = "Complete=True"
		} else {
			done.Severity = "error"
			done.Summary = fmt.Sprintf("%s failed after %d attempts", name, run.Attempts)
			done.Facts["basis"] = "Failed=True"
			done.Facts["reason"] = run.StatusReason
		}
		_, err := w.AddEvent(ctx, done)
		return err
	})
}

// runName is how a run reads in the log: its operation's name, or the
// Job's. The scheduled time goes in the entry's facts, for the UI to show
// in the reader's time zone.
func (c *Collector) runName(ctx context.Context, r store.Run) string {
	if r.OperationID != "" {
		if op, ok, err := c.db.Operation(ctx, r.OperationID); err == nil && ok {
			return label(op)
		}
	}
	return "Job " + r.Namespace + "/" + r.JobName
}

func runFacts(r store.Run) map[string]string {
	f := map[string]string{}
	if !r.ScheduledAt.IsZero() {
		f["scheduledAt"] = r.ScheduledAt.Format(time.RFC3339)
	}
	if r.ScheduledBasis != "" {
		f["scheduledBasis"] = r.ScheduledBasis
	}
	return f
}

// runLost ends a run whose Job disappeared before OpsTide saw it finish.
func (c *Collector) runLost(ctx context.Context, r store.Run, now time.Time) error {
	r.Status, r.StatusReason = store.RunUnknown, "Job removed before OpsTide saw it finish"
	name := c.runName(ctx, r)
	return c.db.Update(ctx, func(w *store.Writer) error {
		if err := w.UpsertRun(ctx, r); err != nil {
			return err
		}
		_, err := w.AddEvent(ctx, store.Event{
			ID: "job.lost/" + r.ID, Type: "job.lost", Namespace: r.Namespace, Kind: "Job", ResourceUID: r.ID,
			ResourceName: r.JobName, OperationID: r.OperationID, RunID: r.ID, ObservedAt: now,
			Severity: "warning", Confidence: store.Unknown, Facts: runFacts(r),
			Summary: name + ": the Job was removed before it finished; its outcome is unknown",
		})
		return err
	})
}
