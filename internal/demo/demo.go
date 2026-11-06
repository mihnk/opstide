// Package demo fills a database with a made-up cluster history so OpsTide can be looked at without a cluster, Docker or
// kind. Everything is written through the same store functions the collector uses, so the schema, the API and the UI
// are the real ones; only the content is invented. The history is anchored on "now", so every Explorer range has
// something in it. Nothing here claims to be real data: the UI shows a banner saying so.
package demo

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/mihnk/opstide/internal/collector"
	"github.com/mihnk/opstide/internal/release"
	"github.com/mihnk/opstide/internal/store"
)

// Watcher stands in for the collector: it watches nothing, so it is always synced and has no problems.
type Watcher struct{}

// Synced says the (absent) watches are synced.
func (Watcher) Synced(context.Context) error { return nil }

// Problems lists no failing watches.
func (Watcher) Problems() []collector.Problem { return nil }

// scopes OpsTide "watches" in the demo story, all from the start of the history.
var scopes = []string{"cronjobs", "jobs", "pods", "deployments", "replicasets", "deploypods", "events", "hpas", "statefulsets", "daemonsets", "controllerrevisions", "workloadpods"}

func snap(image, mem string, replicas int32) string {
	c := corev1.Container{Name: "app", Image: image}
	if mem != "" {
		c.Resources.Limits = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(mem)}
	}
	var d *appsv1.Deployment
	if replicas > 0 {
		d = &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: &replicas}}
	}
	return release.FromTemplate(corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{c}}}, d).Encode()
}

func fingerprint(s string) string {
	if sn, err := release.DecodeSnapshot(s); err == nil {
		return sn.Fingerprint()
	}
	return ""
}

// Seed writes the made-up history, ending at now.
func Seed(ctx context.Context, db *store.Store, now time.Time) error {
	now = now.UTC().Truncate(time.Second)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	return db.Update(ctx, func(w *store.Writer) error {
		s := &seeder{ctx: ctx, w: w, now: now, ago: ago}
		for _, step := range []func(*seeder) error{coverage, cronJobs, deployment, statefulSets, nodes, events} {
			if err := step(s); err != nil {
				return err
			}
		}
		return w.Heartbeat(ctx, now)
	})
}

type seeder struct {
	ctx context.Context
	w   *store.Writer
	now time.Time
	ago func(time.Duration) time.Time
	n   int // entries written, for unique ids
}

// log writes one Log entry. at is when it happened.
func (s *seeder) log(typ, kind, uid, name, ns string, at time.Time, conf store.Confidence, sev, summary string, facts map[string]string, set func(*store.Event)) error {
	s.n++
	e := store.Event{ID: fmt.Sprintf("demo/%s/%d", typ, s.n), Type: typ, Namespace: ns, Kind: kind, ResourceUID: uid, ResourceName: name,
		OccurredAt: at, ObservedAt: at.Add(time.Second), Severity: sev, Confidence: conf, Summary: summary, Facts: facts}
	if set != nil {
		set(&e)
	}
	_, err := s.w.AddEvent(s.ctx, e)
	return err
}

func coverage(s *seeder) error {
	start := s.ago(4 * 24 * time.Hour)
	for _, sc := range scopes {
		if err := s.w.OpenInterval(s.ctx, sc, store.Healthy, start); err != nil {
			return err
		}
	}
	gap := func(scope string, from, to time.Time, why string) error {
		if err := s.w.CloseInterval(s.ctx, scope, from, why); err != nil {
			return err
		}
		if err := s.w.OpenInterval(s.ctx, scope, store.Healthy, to); err != nil {
			return err
		}
		return s.log("collection.gap", scope, "", scope, "", to, store.Observed, "warning",
			fmt.Sprintf("Watching %s again; entries between %s and %s may be missing", scope, from.Format(time.RFC3339), to.Format(time.RFC3339)),
			map[string]string{"scope": scope, "from": from.Format(time.RFC3339), "to": to.Format(time.RFC3339), "seconds": fmt.Sprint(int(to.Sub(from).Seconds()))}, nil)
	}
	// Jobs were not watched for an hour yesterday (OpsTide was restarted); Events for 20 minutes.
	if err := gap("jobs", s.ago(26*time.Hour), s.ago(25*time.Hour), "OpsTide was not running"); err != nil {
		return err
	}
	if err := gap("events", s.ago(10*time.Hour), s.ago(10*time.Hour-20*time.Minute), "watch could not resume; relisted"); err != nil {
		return err
	}
	// OpsTide gave up recording one ReplicaSet after repeated failures: a gap, with the object named.
	from, to := s.ago(3*time.Hour+30*time.Minute), s.ago(3*time.Hour)
	if err := s.w.CloseInterval(s.ctx, "replicasets", from, "gave up recording ReplicaSet shop/api-6c9d"); err != nil {
		return err
	}
	if err := s.w.OpenInterval(s.ctx, "replicasets", store.Healthy, to); err != nil {
		return err
	}
	if err := s.log("collection.gap", "replicasets", "", "replicasets", "shop", to, store.Observed, "warning",
		fmt.Sprintf("Gave up recording ReplicaSet shop/api-6c9d after repeated failures; entries about replicasets between %s and %s may be missing", from.Format(time.RFC3339), to.Format(time.RFC3339)),
		map[string]string{"scope": "replicasets", "from": from.Format(time.RFC3339), "to": to.Format(time.RFC3339), "seconds": "1800", "object": "replicasets shop/api-6c9d", "error": "database is locked"}, nil); err != nil {
		return err
	}
	// Node observation was on for three days and was switched off two hours ago.
	if err := s.w.OpenInterval(s.ctx, "nodes", store.Healthy, start); err != nil {
		return err
	}
	return s.w.CloseInterval(s.ctx, "nodes", s.ago(2*time.Hour), "observation switched off")
}

func cronJobs(s *seeder) error {
	type job struct {
		id, ns, name, display, owner, crit, desc string
		every                                    time.Duration
		offset                                   time.Duration
		schedule                                 string
		dur                                      time.Duration
		failAt                                   []time.Duration // how long ago a run failed
	}
	jobs := []job{
		{"cj-ledger", "payments", "reconcile-ledger", "Reconcile ledger", "payments-team", "high", "Matches settled payments with the bank's file.", 2 * time.Hour, 0, "0 */2 * * *", 4 * time.Minute, []time.Duration{5 * time.Hour}},
		{"cj-report", "data", "sales-report", "Sales report", "data-platform", "medium", "Builds the sales report.", 6 * time.Hour, 0, "0 */6 * * *", 90 * time.Second, nil},
		{"cj-backup", "data", "nightly-backup", "Nightly backup", "data-platform", "high", "Backs up the orders database.", 24 * time.Hour, 3*time.Hour + 15*time.Minute, "15 3 * * *", 22 * time.Minute, []time.Duration{20 * time.Hour}},
	}
	for _, j := range jobs {
		exp := j.dur + j.dur/2
		if err := s.w.UpsertOperation(s.ctx, store.Operation{ID: j.id, Namespace: j.ns, Name: j.name, DisplayName: j.display, Description: j.desc, Owner: j.owner, Criticality: j.crit,
			ExpectedDuration: &exp, Schedule: j.schedule, ConcurrencyPolicy: "Forbid", SuccessfulHistory: 3, FailedHistory: 1,
			CreatedAt: s.ago(60 * 24 * time.Hour), FirstSeenAt: s.ago(4 * 24 * time.Hour), LastSeenAt: s.now}); err != nil {
			return err
		}
		// Scheduled times: every `every`, aligned to the schedule, over the last three days, skipping the Jobs gap and the future.
		first := s.now.Add(-3 * 24 * time.Hour).Truncate(j.every).Add(j.offset)
		for at := first; at.Before(s.now.Add(-time.Minute)); at = at.Add(j.every) {
			if at.After(s.ago(26*time.Hour)) && at.Before(s.ago(25*time.Hour)) {
				continue // OpsTide was not watching Jobs: no run was seen, so none is invented
			}
			failed := false
			for _, f := range j.failAt {
				if d := s.now.Sub(at); d >= f-j.every/2 && d < f+j.every/2 {
					failed = true
				}
			}
			id := fmt.Sprintf("%s-%d", j.id, at.Unix())
			status, reason, finished := store.RunSucceeded, "", at.Add(j.dur)
			if failed {
				status, reason = store.RunFailed, "BackoffLimitExceeded"
			}
			if finished.After(s.now) {
				status, finished = store.RunRunning, time.Time{}
			}
			run := store.Run{ID: id, OperationID: j.id, Namespace: j.ns, JobName: fmt.Sprintf("%s-%d", j.name, at.Unix()/60), ScheduledAt: at, ScheduledBasis: "annotation",
				CreatedAt: at.Add(2 * time.Second), StartedAt: at.Add(5 * time.Second), FinishedAt: finished, Status: status, StatusReason: reason, Attempts: 1, ObservedAt: at.Add(3 * time.Second)}
			if failed {
				run.Attempts = 3
			}
			if err := s.w.UpsertRun(s.ctx, run); err != nil {
				return err
			}
			if status == store.RunSucceeded || status == store.RunFailed {
				typ, sev, word := "job.succeeded", "info", "succeeded"
				if failed {
					typ, sev, word = "job.failed", "error", "failed ("+reason+")"
				}
				if err := s.log(typ, "Job", id, run.JobName, j.ns, finished, store.Derived, sev,
					fmt.Sprintf("%s %s", j.display, word), map[string]string{"seconds": fmt.Sprint(int(j.dur.Seconds())), "scheduledAt": at.Format(time.RFC3339), "scheduledBasis": "annotation", "attempts": fmt.Sprint(run.Attempts)},
					func(e *store.Event) { e.RunID, e.OperationID = id, j.id }); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func deployment(s *seeder) error {
	const id = "d-api"
	if err := s.w.UpsertWorkload(s.ctx, store.Workload{ID: id, Kind: store.KindDeployment, Origin: store.OriginObserved, Namespace: "shop", Name: "api", FirstSeen: s.ago(4 * 24 * time.Hour), LastSeen: s.now}); err != nil {
		return err
	}
	if err := s.w.SetWorkloadSpecReplicas(s.ctx, id, 3); err != nil {
		return err
	}
	type rel struct {
		n             int64
		at            time.Time
		image, mem    string
		state, health string
		reasons       []string
		superseded    time.Time
		origin        string
	}
	rels := []rel{
		{5, s.ago(3 * 24 * time.Hour), "shop/api:1.4.0", "256Mi", store.ReleaseSuperseded, store.HealthHealthy, nil, s.ago(30 * time.Hour), store.OriginAdopted},
		{6, s.ago(30 * time.Hour), "shop/api:1.5.0", "256Mi", store.ReleaseSuperseded, store.HealthDegraded, []string{"3 Pod restarts in the window (OOMKilled twice)", "1 Pod was not ready for 4 minutes"}, s.ago(6 * time.Hour), store.OriginObserved},
		{7, s.ago(6 * time.Hour), "shop/api:1.5.1", "512Mi", store.ReleaseCompleted, store.HealthHealthy, []string{"No restarts and no unavailable Pods in the observation window"}, time.Time{}, store.OriginObserved},
	}
	prev := ""
	for _, r := range rels {
		sn := snap(r.image, r.mem, 3)
		rid := fmt.Sprintf("rel-api-%d", r.n)
		row := store.Release{ID: rid, WorkloadID: id, Namespace: "shop", Workload: "api", Revision: fmt.Sprint(r.n), RevisionNum: r.n, ReplicaSetUID: fmt.Sprintf("rs-api-%d", r.n),
			ReplicaSetName: fmt.Sprintf("api-%d", r.n), TemplateHash: fmt.Sprintf("6c9d%d", r.n), Fingerprint: fingerprint(sn), PrevID: prev, Origin: r.origin, ObservedAt: r.at, StartedAt: r.at,
			CompletedAt: r.at.Add(3 * time.Minute), SupersededAt: r.superseded, State: r.state, StateAt: r.at.Add(3 * time.Minute), Desired: 3, Updated: 3, Ready: 3, Available: 3,
			Health: r.health, HealthReasons: release.EncodeReasons(r.reasons), HealthAt: r.at.Add(13 * time.Minute), WindowSeconds: 600, ObserveUntil: r.at.Add(13 * time.Minute),
			Episodes: "[]", Trouble: "[]", Snapshot: sn, ImageIDs: `{"app":"sha256:9f2c"}`, AssessmentVersion: 2}
		if r.origin == store.OriginAdopted {
			row.Health, row.HealthReasons = store.HealthInsufficient, release.EncodeReasons([]string{"The rollout happened before OpsTide began watching, so it cannot be assessed"})
		}
		if err := s.w.UpsertRelease(s.ctx, row); err != nil {
			return err
		}
		prev = rid
		if r.origin == store.OriginObserved {
			if err := s.log("release.observed", "Deployment", id, "api", "shop", r.at, store.Observed, "info", fmt.Sprintf("api revision %d appeared (shop/api:%s)", r.n, r.image[len("shop/api:"):]),
				map[string]string{"revision": fmt.Sprint(r.n)}, func(e *store.Event) { e.ReleaseID, e.WorkloadID = rid, id }); err != nil {
				return err
			}
			if r.health == store.HealthDegraded {
				if err := s.log("release.health.degraded", "Deployment", id, "api", "shop", r.at.Add(9*time.Minute), store.Derived, "warning", fmt.Sprintf("api revision %d: %s", r.n, r.reasons[0]),
					map[string]string{"rule": "restarts-in-window", "restarts": "3", "oomkilled": "2", "revision": fmt.Sprint(r.n)}, func(e *store.Event) { e.ReleaseID, e.WorkloadID = rid, id }); err != nil {
					return err
				}
			}
		}
		if !r.superseded.IsZero() {
			if err := s.log("release.superseded", "Deployment", id, "api", "shop", r.superseded, store.Observed, "info", fmt.Sprintf("api revision %d was superseded by revision %d", r.n, r.n+1),
				map[string]string{"revision": fmt.Sprint(r.n)}, func(e *store.Event) { e.ReleaseID, e.WorkloadID = rid, id }); err != nil {
				return err
			}
		}
	}
	if err := s.w.SaveWorkloadState(s.ctx, store.WorkloadState{WorkloadID: id, Kind: store.KindDeployment, Desired: 3, Ready: 3, Available: 3, Updated: 3, Current: 3, Revision: "7", UpdateRevision: "7",
		Strategy: "RollingUpdate", Rollout: "completed", Conditions: "[]", LastChangeAt: s.ago(6 * time.Hour), ChangeBasis: "observed"}); err != nil {
		return err
	}
	// Pods of the current release, and one that restarted under the previous one.
	for _, p := range []struct{ name, uid, rel, node string }{{"api-7-a1", "p-api-a1", "rel-api-7", "node-a"}, {"api-7-b2", "p-api-b2", "rel-api-7", "node-b"}, {"api-6-c3", "p-api-c3", "rel-api-6", "node-b"}} {
		created := s.ago(6 * time.Hour)
		var ended time.Time
		if p.rel == "rel-api-6" {
			created, ended = s.ago(30*time.Hour), s.ago(6*time.Hour)
		}
		st := release.PodState{Node: p.node, Ready: ended.IsZero(), Phase: "Running"}
		if !ended.IsZero() {
			st.Terminated, st.TermAt = true, ended
		}
		if err := s.w.UpsertPod(s.ctx, store.PodRecord{UID: p.uid, ReleaseID: p.rel, Namespace: "shop", Name: p.name, Kind: store.KindDeployment, WorkloadID: id, ReplicaSetUID: "rs-" + p.rel[4:], Node: p.node,
			CreatedAt: created, TerminatedAt: ended, State: release.EncodePod(st)}); err != nil {
			return err
		}
	}
	// The change ledger: which client wrote what.
	for _, c := range []struct {
		typ, kind, uid, name, ns, mgr, op string
		at                                time.Duration
		set                               func(*store.Event)
	}{
		{"deployment.spec_written", "Deployment", id, "api", "shop", "argocd-controller", "Apply", 6*time.Hour + 4*time.Minute, func(e *store.Event) { e.WorkloadID = id }},
		{"statefulset.spec_written", "StatefulSet", "sts-web", "web", "shop", "kubectl-edit", "Update", 2*time.Hour + 2*time.Minute, func(e *store.Event) { e.WorkloadID = "sts-web" }},
		{"cronjob.spec_written", "CronJob", "cj-backup", "nightly-backup", "data", "helm", "Update", 3 * 24 * time.Hour, func(e *store.Event) { e.OperationID = "cj-backup" }},
	} {
		if err := s.log(c.typ, c.kind, c.uid, c.name, c.ns, s.ago(c.at), store.Observed, "info", fmt.Sprintf("%s %s/%s: written by %s (%s), a manager of its spec", c.kind, c.ns, c.name, c.mgr, c.op),
			map[string]string{"fieldManager": c.mgr, "operation": c.op, "writtenAt": s.ago(c.at).Format(time.RFC3339), "seen": "recorded after OpsTide started watching"}, c.set); err != nil {
			return err
		}
	}
	return s.log("pod.restarted", "Pod", "p-api-c3", "api-6-c3", "shop", s.ago(29*time.Hour), store.Observed, "warning", "Pod api-6-c3 restarted (OOMKilled)",
		map[string]string{"reason": "OOMKilled", "delta": "1"}, func(e *store.Event) { e.WorkloadID, e.ReleaseID = id, "rel-api-6" })
}

func statefulSets(s *seeder) error {
	snapA, snapB := snap("shop/web:2.0", "256Mi", 0), snap("shop/web:2.1", "512Mi", 0)
	part := 2
	for _, wl := range []store.Workload{
		{ID: "sts-web", Kind: store.KindStatefulSet, Origin: store.OriginObserved, Namespace: "shop", Name: "web", FirstSeen: s.ago(4 * 24 * time.Hour), LastSeen: s.now},
		{ID: "ds-agent", Kind: store.KindDaemonSet, Origin: store.OriginObserved, Namespace: "kube-system", Name: "node-agent", FirstSeen: s.ago(4 * 24 * time.Hour), LastSeen: s.now},
		{ID: "sts-db", Kind: store.KindStatefulSet, Origin: store.OriginAdopted, Namespace: "data", Name: "orders-db", FirstSeen: s.ago(4 * 24 * time.Hour), LastSeen: s.now},
	} {
		if err := s.w.UpsertWorkload(s.ctx, wl); err != nil {
			return err
		}
	}
	rev := func(id, wl, kind, name, hash string, n int64, sn string, at time.Time) store.WorkloadRevision {
		return store.WorkloadRevision{ID: id, WorkloadID: wl, Kind: kind, Name: name, Hash: hash, Number: n, ControllerRevisionUID: "u-" + id, CreatedAt: at, ObservedAt: at, Origin: store.OriginObserved, Fingerprint: fingerprint(sn), Snapshot: sn}
	}
	for _, r := range []store.WorkloadRevision{
		rev("wr-web-1", "sts-web", store.KindStatefulSet, "web-6b8f", "6b8f", 1, snapA, s.ago(4*24*time.Hour)),
		rev("wr-web-2", "sts-web", store.KindStatefulSet, "web-7d21", "7d21", 2, snapB, s.ago(2*time.Hour)),
		rev("wr-agent-1", "ds-agent", store.KindDaemonSet, "node-agent-5c", "5c", 1, snap("shop/agent:3.0", "", 0), s.ago(4*24*time.Hour)),
		rev("wr-agent-2", "ds-agent", store.KindDaemonSet, "node-agent-8e", "8e", 2, snap("shop/agent:3.1", "", 0), s.ago(20*time.Hour)),
	} {
		if _, err := s.w.AddRevision(s.ctx, r); err != nil {
			return err
		}
	}
	for _, st := range []store.WorkloadState{
		{WorkloadID: "sts-web", Kind: store.KindStatefulSet, Desired: 3, Ready: 3, Available: 3, Updated: 1, Current: 2, Revision: "web-6b8f", UpdateRevision: "web-7d21", Strategy: "RollingUpdate", Partition: &part,
			PodManagement: "OrderedReady", Rollout: "partitioned", RolloutNote: "updated 1 of 3 replicas; partition 2 holds the lower ordinals on the previous revision", Conditions: "[]", LastChangeAt: s.ago(2 * time.Hour), ChangeBasis: "observed"},
		{WorkloadID: "ds-agent", Kind: store.KindDaemonSet, Desired: 2, Ready: 2, Available: 2, Updated: 2, Current: 2, Revision: "node-agent-8e", UpdateRevision: "node-agent-8e", Strategy: "RollingUpdate", MaxUnavailable: "1",
			Rollout: "completed", Conditions: "[]", LastChangeAt: s.ago(20 * time.Hour), ChangeBasis: "observed"},
		// Already there when OpsTide began: its last change is a baseline from the source, not "just now".
		{WorkloadID: "sts-db", Kind: store.KindStatefulSet, Desired: 1, Ready: 1, Available: 1, Updated: 1, Current: 1, Revision: "orders-db-3a", UpdateRevision: "orders-db-3a", Strategy: "OnDelete",
			Rollout: "completed", Conditions: "[]", LastChangeAt: s.ago(19 * 24 * time.Hour), ChangeBasis: "baseline"},
	} {
		if err := s.w.SaveWorkloadState(s.ctx, st); err != nil {
			return err
		}
	}
	for _, p := range []struct {
		name, uid, wl, rev, node string
		ord                      int
	}{{"web-0", "p-web-0", "sts-web", "web-7d21", "node-a", 0}, {"web-1", "p-web-1", "sts-web", "web-6b8f", "node-b", 1}, {"web-2", "p-web-2", "sts-web", "web-6b8f", "node-b", 2}} {
		ord := p.ord
		if err := s.w.UpsertPod(s.ctx, store.PodRecord{UID: p.uid, Namespace: "shop", Name: p.name, Kind: store.KindStatefulSet, WorkloadID: p.wl, Revision: p.rev, Node: p.node, Ordinal: &ord,
			CreatedAt: s.ago(3 * time.Hour), State: release.EncodePod(release.PodState{Node: p.node, Ready: true, Phase: "Running"})}); err != nil {
			return err
		}
	}
	for _, e := range []struct {
		typ, kind, uid, name, ns, wl, summary string
		at                                    time.Time
		facts                                 map[string]string
	}{
		{"statefulset.observed", "StatefulSet", "sts-web", "web", "shop", "sts-web", "StatefulSet web first observed (created while OpsTide was watching)", s.ago(4 * 24 * time.Hour), nil},
		{"statefulset.revision_changed", "StatefulSet", "sts-web", "web", "shop", "sts-web", "StatefulSet web update revision changed from web-6b8f to web-7d21", s.ago(2 * time.Hour), map[string]string{"from": "web-6b8f", "to": "web-7d21"}},
		{"statefulset.update_partitioned", "StatefulSet", "sts-web", "web", "shop", "sts-web", "StatefulSet web update stopped at its partition (2): 1 of 3 replicas updated", s.ago(110 * time.Minute), map[string]string{"rollout": "partitioned", "note": "updated 1 of 3; partition 2"}},
		{"daemonset.revision_changed", "DaemonSet", "ds-agent", "node-agent", "kube-system", "ds-agent", "DaemonSet node-agent revision changed from node-agent-5c to node-agent-8e", s.ago(20 * time.Hour), map[string]string{"from": "node-agent-5c", "to": "node-agent-8e"}},
		{"daemonset.update_completed", "DaemonSet", "ds-agent", "node-agent", "kube-system", "ds-agent", "DaemonSet node-agent update completed on 2 of 2 nodes", s.ago(19 * time.Hour), map[string]string{"rollout": "completed"}},
		{"statefulset.observed", "StatefulSet", "sts-db", "orders-db", "data", "sts-db", "StatefulSet orders-db first observed (already present when OpsTide began watching)", s.ago(4 * 24 * time.Hour), nil},
	} {
		if err := s.log(e.typ, e.kind, e.uid, e.name, e.ns, e.at, store.Observed, "info", e.summary, e.facts, func(ev *store.Event) { ev.WorkloadID = e.wl }); err != nil {
			return err
		}
	}
	return nil
}

func nodes(s *seeder) error {
	cond := func(status, reason, msg string, transition, observed time.Time) string {
		return fmt.Sprintf(`[{"type":"Ready","status":%q,"reason":%q,"message":%q,"transition":%q,"observed":%q}]`, status, reason, msg, transition.Format(time.RFC3339), observed.Format(time.RFC3339))
	}
	list := []store.Node{
		{UID: "n-a-old", Name: "node-a", FirstSeenAt: s.ago(4 * 24 * time.Hour), LastSeenAt: s.ago(30 * time.Hour), CreatedAt: s.ago(40 * 24 * time.Hour), DeletedAt: s.ago(30 * time.Hour), Origin: store.OriginAdopted,
			Conditions: cond("Unknown", "NodeStatusUnknown", "Kubelet stopped posting node status.", s.ago(31*time.Hour), s.ago(31*time.Hour))},
		{UID: "n-a-new", Name: "node-a", FirstSeenAt: s.ago(29 * time.Hour), LastSeenAt: s.ago(2 * time.Hour), CreatedAt: s.ago(29 * time.Hour), Origin: store.OriginObserved,
			Conditions: cond("True", "KubeletReady", "kubelet is posting ready status", s.ago(29*time.Hour), s.ago(29*time.Hour))},
		{UID: "n-b", Name: "node-b", FirstSeenAt: s.ago(4 * 24 * time.Hour), LastSeenAt: s.ago(2 * time.Hour), CreatedAt: s.ago(40 * 24 * time.Hour), Origin: store.OriginAdopted, Unschedulable: false,
			Conditions: cond("True", "KubeletReady", "kubelet is posting ready status", s.ago(10*24*time.Hour), s.ago(4*24*time.Hour))},
	}
	for _, n := range list {
		if err := s.w.SaveNode(s.ctx, n); err != nil {
			return err
		}
	}
	for _, e := range []struct {
		typ, uid, summary string
		at                time.Time
		sev               string
		facts             map[string]string
	}{
		{"node.condition_changed", "n-a-old", "Node node-a condition Ready changed from True to Unknown (NodeStatusUnknown)", s.ago(31 * time.Hour), "warning",
			map[string]string{"condition": "Ready", "from": "True", "to": "Unknown", "reason": "NodeStatusUnknown", "message": "Kubelet stopped posting node status."}},
		{"node.deleted", "n-a-old", "Node node-a removed (when it was removed is not known; this is when OpsTide noticed)", s.ago(30 * time.Hour), "info", nil},
		{"node.observed", "n-a-new", "Node node-a created (observed joining the cluster)", s.ago(29 * time.Hour), "info", nil},
	} {
		if err := s.log(e.typ, "Node", e.uid, "node-a", "", e.at, store.Observed, e.sev, e.summary, e.facts, nil); err != nil {
			return err
		}
	}
	return nil
}

func events(s *seeder) error {
	ev := func(id, reason, typ, kind, name, ns, uid, msg string, at time.Time, count int, set func(*store.Evidence)) error {
		e := store.Evidence{ID: "ev-" + id, Source: store.SourceEventsV1, SourceUID: "su-" + id, Namespace: ns, Name: id, Reason: reason, EventType: typ, Message: msg, Action: "Demo",
			Reporter: "kubelet/node-b", Regarding: store.Ref{Kind: kind, Name: name, UID: uid}, FirstAt: at, LastAt: at.Add(time.Duration(count-1) * time.Minute), Count: count, CountBasis: "deprecatedCount",
			ObservedAt: at.Add(time.Second), Origin: store.OriginObservedEvidence}
		if set != nil {
			set(&e)
		}
		if err := s.w.SaveEvidence(s.ctx, e); err != nil {
			return err
		}
		if typ == "Warning" {
			return s.log("evidence.event", kind, uid, name, ns, at, store.Observed, "warning", fmt.Sprintf("Kubernetes reported %s for %s %s", reason, kind, name),
				map[string]string{"reason": reason, "eventType": typ, "source": store.SourceEventsV1, "origin": e.Origin, "evidenceId": e.ID, "message": msg, "count": fmt.Sprint(count)},
				func(le *store.Event) { le.Reason, le.WorkloadID = reason, e.WorkloadID })
		}
		return nil
	}
	link := func(wl, rs string) func(*store.Evidence) {
		return func(e *store.Evidence) {
			e.WorkloadID, e.ReplicaSetUID, e.LinkVia = wl, rs, "Pod ownerReference to ReplicaSet to Deployment"
		}
	}
	if err := ev("api6-oom", "BackOff", "Warning", "Pod", "api-6-c3", "shop", "p-api-c3", "Back-off restarting failed container app in pod api-6-c3", s.ago(29*time.Hour), 6, link("d-api", "rs-api-6")); err != nil {
		return err
	}
	if err := ev("api6-unhealthy", "Unhealthy", "Warning", "Pod", "api-6-c3", "shop", "p-api-c3", "Readiness probe failed: HTTP probe failed with statuscode: 503", s.ago(29*time.Hour+10*time.Minute), 3, link("d-api", "rs-api-6")); err != nil {
		return err
	}
	if err := ev("web0-unhealthy", "Unhealthy", "Warning", "Pod", "web-0", "shop", "p-web-0", "Startup probe failed: connection refused", s.ago(100*time.Minute), 2, func(e *store.Evidence) {
		e.WorkloadID, e.LinkVia = "sts-web", "Pod ownerReference to StatefulSet"
	}); err != nil {
		return err
	}
	for i, r := range []struct{ reason, msg string }{{"Scheduled", "Successfully assigned shop/api-7-a1 to node-a"}, {"Pulled", "Container image already present on machine"}, {"Started", "Started container app"}} {
		if err := ev(fmt.Sprintf("routine-%d", i), r.reason, "Normal", "Pod", "api-7-a1", "shop", "p-api-a1", r.msg, s.ago(6*time.Hour-time.Duration(i)*time.Minute), 1, link("d-api", "rs-api-7")); err != nil {
			return err
		}
	}
	// Node Events: one named the earlier node-a while it existed (linked by exact name, to that Node), one the recreated
	// node-a, and one from before any Node of that name was recorded (unresolved).
	node := func(id string, at time.Time, id2 string) func(*store.Evidence) {
		return func(e *store.Evidence) {
			if id2 != "" {
				e.NodeID, e.LinkVia = id2, "regarding names the Node (exact name; kubelet reports the name as the UID)"
			}
		}
	}
	if err := ev("node-old", "NodeNotReady", "Warning", "Node", "node-a", "default", "node-a", "Node node-a status is now: NodeNotReady", s.ago(31*time.Hour), 1, node("", time.Time{}, "n-a-old")); err != nil {
		return err
	}
	if err := ev("node-new", "NodeReady", "Normal", "Node", "node-a", "default", "node-a", "Node node-a status is now: NodeReady", s.ago(29*time.Hour), 1, node("", time.Time{}, "n-a-new")); err != nil {
		return err
	}
	return ev("node-early", "NodeNotReady", "Warning", "Node", "node-a", "default", "node-a", "Node node-a status is now: NodeNotReady", s.ago(3*24*time.Hour), 1, nil)
}
