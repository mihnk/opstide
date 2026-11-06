package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/mihnk/opstide/internal/release"
	"github.com/mihnk/opstide/internal/store"
	"github.com/mihnk/opstide/internal/workload"
)

// WorkloadState is what Kubernetes last reported about a workload, as recorded. Revision,
// UpdateRevision and the counts are Kubernetes' own fields; Rollout is the result of a
// documented rule (internal/workload) and RolloutNote quotes the values it used.
type WorkloadState struct {
	Generation         int64           `json:"generation"`
	ObservedGeneration int64           `json:"observedGeneration"`
	Desired            int             `json:"desired"`
	Ready              int             `json:"ready"`
	Available          int             `json:"available"`
	Updated            int             `json:"updated"`
	Current            int             `json:"current"`
	Misscheduled       int             `json:"misscheduled,omitempty"`
	Revision           string          `json:"revision"`
	UpdateRevision     string          `json:"updateRevision"`
	Strategy           string          `json:"strategy"`
	Partition          *int            `json:"partition"`
	MaxUnavailable     string          `json:"maxUnavailable,omitempty"`
	MaxSurge           string          `json:"maxSurge,omitempty"`
	PodManagement      string          `json:"podManagement,omitempty"`
	MinReadySeconds    int             `json:"minReadySeconds"`
	Rollout            string          `json:"rollout"`
	RolloutNote        string          `json:"rolloutNote"`
	Conditions         json.RawMessage `json:"conditions"`
	LastChangeAt       time.Time       `json:"lastChangeAt"`
}

// WorkloadEvidenceStatus is the recent Kubernetes Warning Events about a workload. It is a count
// and the newest reason as Kubernetes wrote it: not a score and not a ranking.
type WorkloadEvidenceStatus struct {
	Warnings24h         int       `json:"warnings24h"`
	LatestWarningReason string    `json:"latestWarningReason,omitempty"`
	LatestWarningAt     time.Time `json:"latestWarningAt,omitzero"`
}

// WorkloadView is a workload of any kind. State is null until Kubernetes' status has been recorded.
type WorkloadView struct {
	ID        string                 `json:"id"` // the workload's own UID
	Kind      string                 `json:"kind"`
	Namespace string                 `json:"namespace"`
	Name      string                 `json:"name"`
	Origin    string                 `json:"origin,omitempty"` // observed | adopted
	Status    string                 `json:"status"`           // live | deleted
	FirstSeen time.Time              `json:"firstSeenAt"`
	LastSeen  time.Time              `json:"lastSeenAt"`
	DeletedAt *time.Time             `json:"deletedAt"`
	State     *WorkloadState         `json:"state"`
	Evidence  WorkloadEvidenceStatus `json:"evidence"`
	// Sections are the parts of the detail this kind has, so nothing is shown empty for lack of support.
	Sections []string `json:"sections,omitempty"`
}

func workloadView(r store.WorkloadRow) WorkloadView {
	v := WorkloadView{ID: r.ID, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name, Origin: r.Origin, Status: "live", FirstSeen: r.FirstSeen, LastSeen: r.LastSeen, DeletedAt: tp(r.DeletedAt),
		Evidence: WorkloadEvidenceStatus{Warnings24h: r.Warnings24h, LatestWarningReason: r.LatestWarningReason, LatestWarningAt: r.LatestWarningAt}}
	if !r.DeletedAt.IsZero() {
		v.Status = "deleted"
	}
	if r.HasState {
		st := r.State
		v.State = &WorkloadState{Generation: st.Generation, ObservedGeneration: st.ObservedGeneration, Desired: st.Desired, Ready: st.Ready, Available: st.Available, Updated: st.Updated,
			Current: st.Current, Misscheduled: st.Misscheduled, Revision: st.Revision, UpdateRevision: st.UpdateRevision, Strategy: st.Strategy, Partition: st.Partition,
			MaxUnavailable: st.MaxUnavailable, MaxSurge: st.MaxSurge, PodManagement: st.PodManagement, MinReadySeconds: st.MinReadySeconds, Rollout: st.Rollout, RolloutNote: st.RolloutNote,
			Conditions: rawJSON(st.Conditions), LastChangeAt: st.LastChangeAt}
	}
	return v
}

// sectionsOf are the sections a workload's detail has. Every kind has the same five today; they stay a list
// so a kind that lacked one would show nothing in its place.
func sectionsOf(string) []string {
	return []string{"overview", "history", "revisions", "pods", "evidence"}
}

// WorkloadList is a page of the Workloads view.
type WorkloadList struct {
	Schema     int            `json:"schema"`
	Workloads  []WorkloadView `json:"workloads"`
	NextCursor string         `json:"nextCursor,omitempty"`
	// Notes say what is not being observed right now, so a short list is not read as a complete one.
	Notes []string `json:"notes"`
}

var workloadScopes = map[string]string{"deployments": "Deployments", "statefulsets": "StatefulSets", "daemonsets": "DaemonSets", "controllerrevisions": "ControllerRevisions (revision history)", "workloadpods": "StatefulSet and DaemonSet Pods"}

// unreadable lists the watches of a workload kind that cannot be read right now.
func (s *Server) unreadable(prefixes ...string) []string {
	var out []string
	for _, p := range s.w.Problems() {
		kind, _, _ := strings.Cut(p.Scope, "/")
		for _, want := range prefixes {
			if kind == want {
				out = append(out, workloadScopes[kind]+" cannot be read ("+p.Err+"): what is shown may be incomplete.")
			}
		}
	}
	return out
}

func (s *Server) workloadList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	wq := store.WorkloadQuery{Kind: q.Get("kind"), Namespace: q.Get("namespace"), Text: q.Get("q"), Status: q.Get("status"), Cursor: q.Get("cursor"), Now: s.now()}
	var err error
	if wq.Kind != "" && wq.Kind != store.KindDeployment && wq.Kind != store.KindStatefulSet && wq.Kind != store.KindDaemonSet {
		err = badRequest("kind takes Deployment, StatefulSet or DaemonSet")
	}
	if wq.Status != "" && wq.Status != "live" && wq.Status != "deleted" {
		err = badRequest("status takes live or deleted")
	}
	if len(wq.Text) > 200 {
		err = badRequest("q is too long")
	}
	if err == nil {
		wq.Limit, err = intParam(r, "limit", 100, 500)
	}
	if v := q.Get("within"); err == nil && v != "" {
		d, perr := parseSpan(v)
		if perr != nil || d > 400*24*time.Hour {
			err = badRequest("within must be a duration such as 48h or 5d")
		} else {
			wq.ChangedSince = s.now().Add(-d)
		}
	}
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	page, err := s.db.Workloads(r.Context(), wq)
	out := WorkloadList{Schema: Schema, Workloads: []WorkloadView{}, NextCursor: page.NextCursor, Notes: []string{}}
	for _, row := range page.Rows {
		out.Workloads = append(out.Workloads, workloadView(row))
	}
	out.Notes = append(out.Notes, s.unreadable("deployments", "statefulsets", "daemonsets")...)
	if err != nil && strings.HasPrefix(err.Error(), "bad cursor") {
		err = badRequest(err.Error())
	}
	reply(w, r, s.log, out, err)
}

// WorkloadDetail is one workload with its identity and observed state.
type WorkloadDetail struct {
	Schema   int          `json:"schema"`
	Workload WorkloadView `json:"workload"`
	// Release is the Deployment's current release, for the existing Releases pages.
	Release *struct {
		ID       string `json:"id"`
		Revision string `json:"revision"`
	} `json:"release,omitempty"`
	Notes []string `json:"notes"`
}

func (s *Server) loadWorkload(r *http.Request) (store.WorkloadRow, error) {
	row, ok, err := s.db.WorkloadByID(r.Context(), r.PathValue("id"))
	if err == nil && !ok {
		err = errNotFound
	}
	return row, err
}

func (s *Server) workloadOne(w http.ResponseWriter, r *http.Request) {
	row, err := s.loadWorkload(r)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	// The recent evidence status, asked for this workload by its id.
	if n, reason, at, err := s.db.WorkloadEvidenceStatus(r.Context(), row.ID, s.now()); err == nil {
		row.Warnings24h, row.LatestWarningReason, row.LatestWarningAt = n, reason, at
	}
	v := workloadView(row)
	v.Sections = sectionsOf(row.Kind)
	out := WorkloadDetail{Schema: Schema, Workload: v, Notes: []string{}}
	if row.Kind == store.KindDeployment {
		if rels, err := s.db.ReleasesOfWorkloadID(r.Context(), row.ID, 1); err == nil && len(rels) > 0 {
			out.Release = &struct {
				ID       string `json:"id"`
				Revision string `json:"revision"`
			}{rels[0].ID, rels[0].Revision}
		}
	}
	if !row.HasState {
		out.Notes = append(out.Notes, "Kubernetes' status for this workload has not been recorded yet.")
	}
	out.Notes = append(out.Notes, s.unreadable(strings.ToLower(row.Kind)+"s", "controllerrevisions", "workloadpods")...)
	reply(w, r, s.log, out, nil)
}

// routinePodEntries are Pod entries that are the ordinary life of Pods; the workload history leaves them out unless asked.
var routinePodEntries = []string{"pod.created", "pod.ready", "pod.terminated"}

func (s *Server) workloadHistory(w http.ResponseWriter, r *http.Request) {
	row, err := s.loadWorkload(r)
	var eq store.EventQuery
	if err == nil {
		eq.Limit, err = intParam(r, "limit", 100, 500)
	}
	if err == nil {
		eq.From, err = timeParam(r, "from")
	}
	if err == nil {
		eq.To, err = timeParam(r, "to")
	}
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	eq.WorkloadID, eq.Cursor = row.ID, r.URL.Query().Get("cursor")
	if r.URL.Query().Get("pods") != "all" {
		eq.ExcludeTypes = routinePodEntries
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

// RevisionView is one revision of a workload. Changes compare it with the revision before it; when that is not
// possible the reason is stated and nothing is guessed.
type RevisionView struct {
	ID         string    `json:"id"`
	Source     string    `json:"source"` // ControllerRevision | Release
	Name       string    `json:"name"`
	Number     int64     `json:"number"` // meaningful within this workload only
	CreatedAt  time.Time `json:"createdAt,omitzero"`
	ObservedAt time.Time `json:"observedAt"`
	Origin     string    `json:"origin"`
	IsCurrent  bool      `json:"isCurrent"`
	IsUpdate   bool      `json:"isUpdate"`
	Images     []string  `json:"images"`
	// Fingerprint is a hash of the allow-listed template settings; equal fingerprints mean equal settings.
	Fingerprint string `json:"fingerprint"`
	// ReleaseID links a Deployment revision to its release page.
	ReleaseID  string        `json:"releaseId,omitempty"`
	Comparison RevComparison `json:"comparison"`
}

// RevComparison is the comparison with the previous revision.
type RevComparison struct {
	Available bool             `json:"available"`
	Reason    string           `json:"reason,omitempty"` // "Comparison unavailable" and why
	Changes   []release.Change `json:"changes"`
}

// WorkloadRevisions is the Revisions section.
type WorkloadRevisions struct {
	Schema    int            `json:"schema"`
	Kind      string         `json:"kind"`
	Revisions []RevisionView `json:"revisions"` // newest first
	// Strategy says what the update strategy means for reading these revisions.
	Notes []string `json:"notes"`
}

func imagesOf(sn release.Snapshot) []string {
	out := []string{}
	for _, c := range sn.Containers {
		out = append(out, c.Image)
	}
	return out
}

func (s *Server) workloadRevisions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	row, err := s.loadWorkload(r)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	out := WorkloadRevisions{Schema: Schema, Kind: row.Kind, Revisions: []RevisionView{}, Notes: []string{}}
	if row.Kind == store.KindDeployment {
		rels, err := s.db.ReleasesOfWorkloadID(ctx, row.ID, 100)
		if err != nil {
			reply(w, r, s.log, nil, err)
			return
		}
		for i, rel := range rels { // newest first
			sn, derr := release.DecodeSnapshot(rel.Snapshot)
			v := RevisionView{ID: rel.ID, Source: "Release", Name: "revision " + rel.Revision, Number: rel.RevisionNum, ObservedAt: rel.ObservedAt, Origin: rel.Origin,
				IsCurrent: rel.SupersededAt.IsZero(), IsUpdate: rel.SupersededAt.IsZero(), Images: imagesOf(sn), Fingerprint: rel.Fingerprint, ReleaseID: rel.ID, Comparison: RevComparison{Changes: []release.Change{}}}
			if !rel.StartedAt.IsZero() {
				v.CreatedAt = rel.StartedAt
			}
			switch {
			case derr != nil:
				v.Comparison.Reason = "Comparison unavailable: the stored snapshot cannot be read."
			case i+1 >= len(rels):
				v.Comparison.Reason = "Comparison unavailable: no earlier revision of this Deployment is recorded."
			default:
				if prev, perr := release.DecodeSnapshot(rels[i+1].Snapshot); perr != nil {
					v.Comparison.Reason = "Comparison unavailable: the earlier snapshot cannot be read."
				} else {
					v.Comparison.Available, v.Comparison.Changes = true, nonNilChanges(release.Diff(prev, sn))
				}
			}
			out.Revisions = append(out.Revisions, v)
		}
		out.Notes = append(out.Notes, "A Deployment's revisions are its releases (ReplicaSets); the release page has the rollout, health and evidence of each.")
		reply(w, r, s.log, out, nil)
		return
	}
	revs, err := s.db.Revisions(ctx, row.ID)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	cur, upd := "", ""
	if row.HasState {
		cur, upd = row.State.Revision, row.State.UpdateRevision
	}
	for i := len(revs) - 1; i >= 0; i-- { // newest first
		rv := revs[i]
		sn, derr := release.DecodeSnapshot(rv.Snapshot)
		v := RevisionView{ID: rv.ID, Source: "ControllerRevision", Name: rv.Name, Number: rv.Number, CreatedAt: rv.CreatedAt, ObservedAt: rv.ObservedAt, Origin: rv.Origin,
			IsCurrent: rv.Name == cur, IsUpdate: rv.Name == upd, Images: imagesOf(sn), Fingerprint: rv.Fingerprint, Comparison: RevComparison{Changes: []release.Change{}}}
		switch {
		case derr != nil || rv.Snapshot == "{}":
			v.Comparison.Reason = "Comparison unavailable: the ControllerRevision's Pod template could not be read."
		case i == 0:
			v.Comparison.Reason = "Comparison unavailable: no earlier revision is recorded (Kubernetes keeps a limited history, and OpsTide records only what it saw)."
		default:
			prev, perr := release.DecodeSnapshot(revs[i-1].Snapshot)
			if perr != nil || revs[i-1].Snapshot == "{}" {
				v.Comparison.Reason = "Comparison unavailable: the earlier ControllerRevision's Pod template could not be read."
			} else {
				v.Comparison.Available, v.Comparison.Changes = true, nonNilChanges(release.Diff(prev, sn))
			}
		}
		out.Revisions = append(out.Revisions, v)
	}
	if row.HasState {
		switch row.State.Rollout {
		case workload.Partitioned:
			out.Notes = append(out.Notes, "The rolling update stopped at its partition boundary: Pods below the partition stay on the previous revision by design.")
		case workload.AwaitingPodDeletion:
			out.Notes = append(out.Notes, "The update strategy is OnDelete: a changed template replaces no Pod until it is deleted, so the update revision is not yet what is running.")
		}
	}
	if row.Kind == store.KindDaemonSet {
		out.Notes = append(out.Notes, "A DaemonSet's status carries no revision: the newest ControllerRevision is shown as its template revision.")
	}
	reply(w, r, s.log, out, nil)
}

func nonNilChanges(c []release.Change) []release.Change {
	if c == nil {
		return []release.Change{}
	}
	return c
}

// WorkloadPodView is a Pod of a workload with its placement and revision as observed.
type WorkloadPodView struct {
	PodView
	Ordinal  *int   `json:"ordinal,omitempty"`
	Revision string `json:"revision,omitempty"`
	// RevisionRole says how the Pod's revision compares with the workload's: "update" (the update
	// revision), "current" (the current revision, when it differs), or "other"; "" when not known.
	RevisionRole string `json:"revisionRole,omitempty"`
	NodeID       string `json:"nodeId,omitempty"` // the Node record with that name, when OpsTide has one
}

// NodeGap is a Node on which no Pod of a DaemonSet is recorded. It says that, and nothing about why.
type NodeGap struct {
	NodeID string `json:"nodeId"`
	Name   string `json:"name"`
}

// WorkloadPods is the Pods section.
type WorkloadPods struct {
	Schema int               `json:"schema"`
	Kind   string            `json:"kind"`
	Pods   []WorkloadPodView `json:"pods"`
	// NodesWithoutPod lists observed Nodes with no live Pod of this DaemonSet recorded. Why is not
	// established: eligibility (selectors, taints, tolerations) is not read. Null when Nodes are not observed.
	NodesWithoutPod *[]NodeGap `json:"nodesWithoutPod"`
	Notes           []string   `json:"notes"`
	Truncated       bool       `json:"truncated,omitempty"`
}

func (s *Server) workloadPods(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	row, err := s.loadWorkload(r)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	pods, err := s.db.PodsOfWorkload(ctx, row.ID, maxPodsShown+1)
	var live []store.PodRecord
	if err == nil && row.Kind == store.KindDaemonSet {
		live, err = s.db.LivePodsOfWorkload(ctx, row.ID) // every live Pod, however many were recorded since
	}
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	out := WorkloadPods{Schema: Schema, Kind: row.Kind, Pods: []WorkloadPodView{}, Notes: []string{}}
	if len(pods) > maxPodsShown {
		pods, out.Truncated = pods[:maxPodsShown], true
	}
	nodes, nodeErr := s.db.LiveNodes(ctx)
	byName := map[string]string{}
	for _, n := range nodes {
		byName[n.Name] = n.UID
	}
	cur, upd := "", ""
	if row.HasState {
		cur, upd = row.State.Revision, row.State.UpdateRevision
	}
	revs, _ := s.db.Revisions(ctx, row.ID)
	hashOf := map[string]string{} // a Pod label (name or hash) -> the revision's name
	for _, rv := range revs {
		hashOf[rv.Name] = rv.Name
		if rv.Hash != "" {
			hashOf[rv.Hash] = rv.Name
		}
	}
	for _, p := range pods {
		st, derr := release.DecodePod(p.State)
		if derr != nil {
			continue
		}
		v := WorkloadPodView{PodView: podView(p, st), Ordinal: p.Ordinal, Revision: p.Revision}
		if v.Node == "" {
			v.Node = p.Node
		}
		v.NodeID = byName[v.Node]
		if name := hashOf[p.Revision]; name != "" && row.Kind != store.KindDeployment {
			switch name {
			case upd:
				v.RevisionRole = "update"
			case cur:
				v.RevisionRole = "current"
			default:
				v.RevisionRole = "other"
			}
		}
		out.Pods = append(out.Pods, v)
	}
	sort.SliceStable(out.Pods, func(i, j int) bool {
		a, b := out.Pods[i], out.Pods[j]
		if a.Ordinal != nil && b.Ordinal != nil && *a.Ordinal != *b.Ordinal {
			return *a.Ordinal < *b.Ordinal
		}
		return a.CreatedAt.After(b.CreatedAt)
	})
	if row.Kind == store.KindDaemonSet {
		switch {
		case !s.cfg.ObserveNodes || nodeErr != nil:
			out.Notes = append(out.Notes, "Nodes are not observed, so which Nodes have no Pod of this DaemonSet cannot be listed. Turn on Node observation (nodes.observe) to see it.")
		case len(s.unreadableNodes()) > 0 || len(nodes) == 0:
			out.Notes = append(out.Notes, "No Node is recorded (the Node watch is failing or has not listed yet), so which Nodes have no Pod of this DaemonSet is unknown.")
		default:
			has := map[string]bool{}
			for _, p := range live {
				if p.Node != "" {
					has[p.Node] = true
				}
			}
			gaps := []NodeGap{}
			for _, n := range nodes {
				if !has[n.Name] {
					gaps = append(gaps, NodeGap{NodeID: n.UID, Name: n.Name})
				}
			}
			out.NodesWithoutPod = &gaps
			if len(gaps) > 0 {
				out.Notes = append(out.Notes, "No Pod of this DaemonSet is recorded on these Nodes. Whether they are eligible for it (node selector, taints, tolerations, affinity) is not established by OpsTide, so no explanation is given.")
			}
		}
		if row.HasState {
			out.Notes = append(out.Notes, "Reported by Kubernetes: "+itoaN(row.State.Desired)+" desired, "+itoaN(row.State.Current)+" currently scheduled, "+itoaN(row.State.Misscheduled)+" misscheduled, "+itoaN(row.State.Available)+" available.")
		}
	}
	reply(w, r, s.log, out, nil)
}

func itoaN(n int) string { b, _ := json.Marshal(n); return string(b) }

// WorkloadEvidence is the Evidence section: the Kubernetes Events linked to the workload (directly, or through its
// Pods and ReplicaSets) and the conditions Kubernetes reports on it.
type WorkloadEvidence struct {
	Schema     int              `json:"schema"`
	Records    []EvidenceRecord `json:"records"`
	NextCursor string           `json:"nextCursor,omitempty"`
	Conditions json.RawMessage  `json:"conditions"`
	Provenance Provenance       `json:"provenance"`
}

func (s *Server) workloadEvidence(w http.ResponseWriter, r *http.Request) {
	row, err := s.loadWorkload(r)
	eq := store.EvidenceQuery{}
	if err == nil {
		eq.Limit, err = intParam(r, "limit", 100, 500)
	}
	if err == nil {
		eq.From, err = timeParam(r, "from")
	}
	if err == nil {
		eq.To, err = timeParam(r, "to")
	}
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	q := r.URL.Query()
	eq.WorkloadID, eq.Cursor = row.ID, q.Get("cursor")
	eq.Reasons, eq.EventTypes = splitList(q.Get("reason")), splitList(q.Get("type"))
	page, err := s.db.Evidence(r.Context(), eq)
	out := WorkloadEvidence{Schema: Schema, Records: []EvidenceRecord{}, NextCursor: page.NextCursor, Conditions: json.RawMessage("[]"), Provenance: Provenance{EventsAPI: s.eventsSource(), Notes: []string{}}}
	for _, e := range page.Records {
		out.Records = append(out.Records, evidenceRecord(e))
	}
	if row.HasState {
		out.Conditions = rawJSON(row.State.Conditions)
	}
	if !s.eventsReadable() {
		out.Provenance.Notes = append(out.Provenance.Notes, "OpsTide cannot read Kubernetes Events right now: evidence from now on may be missing.")
	}
	if err != nil && strings.HasPrefix(err.Error(), "bad cursor") {
		err = badRequest(err.Error())
	}
	reply(w, r, s.log, out, err)
}
