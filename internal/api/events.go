package api

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mihnk/opstide/internal/store"
)

// Entry is one log entry.
type Entry struct {
	ID           string            `json:"id"`
	RecordID     string            `json:"recordId"` // the id the Explorer and the Investigation Workspace use
	Type         string            `json:"type"`
	Namespace    string            `json:"namespace,omitempty"`
	Kind         string            `json:"kind,omitempty"`
	Reason       string            `json:"reason,omitempty"`
	WorkloadID   string            `json:"workloadId,omitempty"`
	ResourceName string            `json:"resourceName,omitempty"`
	ResourceUID  string            `json:"resourceUid,omitempty"`
	OperationID  string            `json:"operationId,omitempty"`
	RunID        string            `json:"runId,omitempty"`
	ReleaseID    string            `json:"releaseId,omitempty"`
	At           time.Time         `json:"at"`
	OccurredAt   time.Time         `json:"occurredAt,omitzero"`
	ObservedAt   time.Time         `json:"observedAt"`
	RecordedAt   time.Time         `json:"recordedAt"`
	Severity     string            `json:"severity"`
	Confidence   string            `json:"confidence"`
	Summary      string            `json:"summary"`
	Facts        map[string]string `json:"facts,omitempty"`
}

func entry(e store.Event) Entry {
	return Entry{ID: e.ID, RecordID: store.EventRecordID(e.Seq), Type: e.Type, Namespace: e.Namespace, Kind: e.Kind, ResourceName: e.ResourceName,
		ResourceUID: e.ResourceUID, Reason: e.Reason, WorkloadID: e.WorkloadID, OperationID: e.OperationID, RunID: e.RunID, ReleaseID: e.ReleaseID, At: e.At(), OccurredAt: e.OccurredAt,
		ObservedAt: e.ObservedAt, RecordedAt: e.RecordedAt, Severity: e.Severity, Confidence: string(e.Confidence),
		Summary: e.Summary, Facts: e.Facts}
}

// Entries is a page of the Log screen.
type Entries struct {
	Schema     int     `json:"schema"`
	Entries    []Entry `json:"entries"`
	NextCursor string  `json:"nextCursor,omitempty"`
	// Truncated is set on a merged timeline that was cut at its limit.
	Truncated bool `json:"truncated,omitempty"`
}

// Log filters, by the groups the Log screen offers.
var typeGroups = map[string][]string{
	"runs":       {"job.succeeded", "job.failed", "job.lost"},
	"starts":     {"job.created"},
	"schedules":  {"cronjob."},
	"rollouts":   {"deployment."},
	"collection": {"collection."},
	// Releases: a revision appearing and what became of its rollout.
	"releases": {"release.observed", "release.superseded", "release.scaled_down", "release.paused", "release.resumed", "release.stalled", "release.failed"},
	// Workload health: what went wrong after (or during) a rollout.
	"health": {"release.health.", "release.unavailable", "pod.notready", "pod.oomkilled", "pod.restarted", "pod.waiting", "pod.evicted"},
	// Pod lifecycle: the routine of Pods coming and going.
	"pods": {"pod.created", "pod.ready", "pod.terminated"},
	// Kubernetes Events: what Kubernetes reported (Warning Events only enter the
	// Log; every Event is kept as evidence).
	"kubernetes": {"evidence."},
	// Scaling: what autoscalers and Deployments reported about replica targets.
	"scaling": {"hpa.", "workload.replica_target_changed"},
	// StatefulSet and DaemonSet activity: first observed, revision and rollout changes, deletion.
	"statefulsets": {"statefulset.observed", "statefulset.revision_changed", "statefulset.current_revision_changed", "statefulset.partition_changed", "statefulset.strategy_changed", "statefulset.update_started", "statefulset.update_partitioned", "statefulset.update_awaiting_pod_deletion", "statefulset.update_completed", "statefulset.condition_changed", "statefulset.deleted"},
	"daemonsets":   {"daemonset.observed", "daemonset.revision_changed", "daemonset.current_revision_changed", "daemonset.partition_changed", "daemonset.strategy_changed", "daemonset.update_started", "daemonset.update_partitioned", "daemonset.update_awaiting_pod_deletion", "daemonset.update_completed", "daemonset.condition_changed", "daemonset.deleted"},
	// Node conditions and scheduling changes, as reported.
	"nodes": {"node."},
	// Reported replica and scheduled counts of StatefulSets and DaemonSets. Routine, so off by default.
	"counts": {"statefulset.counts_changed", "daemonset.counts_changed"},
}

// outcomeTypes maps the outcome filter to the entry types of finished runs.
var outcomeTypes = map[string]string{"failed": "job.failed", "succeeded": "job.succeeded", "unknown": "job.lost"}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var eq store.EventQuery
	var err error
	if eq.From, err = timeParam(r, "from"); err == nil {
		eq.To, err = timeParam(r, "to")
	}
	if err == nil {
		eq.Limit, err = intParam(r, "limit", 100, 500)
	}
	// within=48h: from that long before now, so a page's URL stays the same
	// while it is open.
	if w := q.Get("within"); err == nil && w != "" {
		d, perr := parseSpan(w)
		if perr != nil || d > 400*24*time.Hour {
			err = badRequest("within must be a duration such as 48h or 5d")
		} else if eq.From.IsZero() {
			eq.From = s.now().Add(-d)
		}
	}
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	eq.Namespace, eq.OperationID, eq.RunID, eq.Cursor = q.Get("namespace"), q.Get("operation"), q.Get("run"), q.Get("cursor")
	eq.ReleaseID = q.Get("release")
	eq.Kind, eq.Reason, eq.WorkloadID = q.Get("kind"), q.Get("reason"), q.Get("workload")
	if len(eq.Kind) > 64 || len(eq.Reason) > 128 || len(eq.WorkloadID) > 64 {
		reply(w, r, s.log, nil, badRequest("kind, reason or workload is too long"))
		return
	}
	if t := q.Get("q"); len(t) > 200 {
		reply(w, r, s.log, nil, badRequest("q is too long"))
		return
	} else {
		eq.Text = t
	}
	// outcome narrows the run outcomes the "runs" group shows.
	runTypes := typeGroups["runs"]
	if oc := q.Get("outcome"); oc != "" {
		runTypes = nil
		for _, o := range strings.Split(oc, ",") {
			t, ok := outcomeTypes[o]
			if !ok {
				reply(w, r, s.log, nil, badRequest("outcome takes failed, succeeded or unknown"))
				return
			}
			runTypes = append(runTypes, t)
		}
	}
	show := q.Get("show")
	if show == "" && q.Get("outcome") != "" {
		// An outcome with no group named narrows the run entries among all groups.
		show = "runs,starts,schedules,rollouts,collection"
	}
	if show != "" {
		hasRuns := false
		for _, g := range strings.Split(show, ",") {
			types, ok := typeGroups[g]
			if !ok {
				reply(w, r, s.log, nil, badRequest("show takes runs, starts, schedules, rollouts, releases, health, pods, kubernetes, scaling, statefulsets, daemonsets, nodes, counts or collection"))
				return
			}
			if g == "runs" {
				types, hasRuns = runTypes, true
			}
			eq.Types = append(eq.Types, types...)
		}
		if q.Get("outcome") != "" && !hasRuns {
			reply(w, r, s.log, nil, badRequest("outcome narrows run entries: add runs to show"))
			return
		}
	}
	// Kubernetes Event entries are asked for by name or by what they carry
	// (kind, reason, workload); an unfiltered Log does not include them.
	if show == "" && eq.Reason == "" && eq.Kind == "" && eq.WorkloadID == "" && eq.OperationID == "" && eq.RunID == "" && eq.ReleaseID == "" {
		// Kubernetes Event entries and reported counts are routine: asked for by name, not in the unfiltered Log.
		eq.ExcludeTypes = []string{"evidence.", "statefulset.counts_changed", "daemonset.counts_changed"}
	}
	if sv := q.Get("severity"); sv != "" {
		for _, v := range strings.Split(sv, ",") {
			if v != "info" && v != "warning" && v != "error" {
				reply(w, r, s.log, nil, badRequest("severity takes info, warning or error"))
				return
			}
			eq.Severities = append(eq.Severities, v)
		}
	}
	if eq.Owner = q.Get("owner"); utf8.RuneCountInString(eq.Owner) > 120 { // the collector keeps at most 120 characters
		reply(w, r, s.log, nil, badRequest("owner is too long"))
		return
	}
	page, err := s.db.Events(r.Context(), eq)
	out := Entries{Schema: Schema, Entries: []Entry{}, NextCursor: page.NextCursor}
	for _, e := range page.Events {
		out.Entries = append(out.Entries, entry(e))
	}
	if err != nil && strings.HasPrefix(err.Error(), "bad cursor") {
		err = badRequest(err.Error())
	}
	reply(w, r, s.log, out, err)
}

// RunDetail is one run with its log entries.
type RunDetail struct {
	Schema    int        `json:"schema"`
	Run       Run        `json:"run"`
	Operation *Operation `json:"operation,omitempty"`
	Entries   []Entry    `json:"entries"`
}

func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	run, ok, err := s.db.Run(ctx, r.PathValue("id"))
	if err == nil && !ok {
		err = errNotFound
	}
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	out := RunDetail{Schema: Schema, Entries: []Entry{}}
	var expected *time.Duration
	if run.OperationID != "" {
		op, ok, err := s.db.Operation(ctx, run.OperationID)
		if err != nil {
			reply(w, r, s.log, nil, err)
			return
		}
		if ok {
			v, err := s.view(ctx, op, nil, nil)
			if err != nil {
				reply(w, r, s.log, nil, err)
				return
			}
			o := v.operation()
			out.Operation, expected = &o, op.ExpectedDuration
		}
	}
	out.Run = runView(run, expected, s.now().UTC())
	out.Run.Pods = run.Pods
	page, err := s.db.Events(ctx, store.EventQuery{RunID: run.ID, Limit: 200})
	for _, e := range page.Events {
		out.Entries = append(out.Entries, entry(e))
	}
	reply(w, r, s.log, out, err)
}

// Owners is the list behind the Log's Owner filter.
type Owners struct {
	Schema int      `json:"schema"`
	Owners []string `json:"owners"`
}

func (s *Server) owners(w http.ResponseWriter, r *http.Request) {
	o, err := s.db.Owners(r.Context())
	reply(w, r, s.log, Owners{Schema: Schema, Owners: o}, err)
}
