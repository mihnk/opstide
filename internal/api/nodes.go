package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/mihnk/opstide/internal/collector"
	"github.com/mihnk/opstide/internal/store"
)

// NodeCondition is a Node condition as Kubernetes reported it, with when OpsTide first saw its status.
type NodeCondition struct {
	Type       string     `json:"type"`
	Status     string     `json:"status"`
	Reason     string     `json:"reason,omitempty"`
	Message    string     `json:"message,omitempty"`
	Transition *time.Time `json:"lastTransitionTime"` // the source's time; null when it gave none
	Observed   *time.Time `json:"observedAt"`         // when OpsTide first saw this status
}

// NodeView is a Node. Only its identity, spec.unschedulable and five conditions are recorded.
type NodeView struct {
	ID            string          `json:"id"` // the Node's UID
	Name          string          `json:"name"`
	Status        string          `json:"status"` // live | removed
	Origin        string          `json:"origin"` // observed | adopted
	CreatedAt     *time.Time      `json:"createdAt"`
	FirstSeenAt   time.Time       `json:"firstSeenAt"`
	DeletedAt     *time.Time      `json:"deletedAt"`
	Unschedulable bool            `json:"unschedulable"`
	Conditions    []NodeCondition `json:"conditions"`
}

func nodeView(n store.Node) NodeView {
	v := NodeView{ID: n.UID, Name: n.Name, Status: "live", Origin: n.Origin, CreatedAt: tp(n.CreatedAt), FirstSeenAt: n.FirstSeenAt, DeletedAt: tp(n.DeletedAt),
		Unschedulable: n.Unschedulable, Conditions: []NodeCondition{}}
	if !n.DeletedAt.IsZero() {
		v.Status = "removed"
	}
	var cs []collector.NodeCondition
	_ = json.Unmarshal([]byte(n.Conditions), &cs)
	for _, c := range cs {
		v.Conditions = append(v.Conditions, NodeCondition{Type: c.Type, Status: c.Status, Reason: c.Reason, Message: c.Message, Transition: tp(c.Transition), Observed: tp(c.Observed)})
	}
	return v
}

// NodesList is the Nodes list. Enabled says whether Nodes are observed at all.
type NodesList struct {
	Schema  int        `json:"schema"`
	Enabled bool       `json:"enabled"`
	Nodes   []NodeView `json:"nodes"`
	Notes   []string   `json:"notes"`
}

func (s *Server) nodeNotes() []string {
	if !s.cfg.ObserveNodes {
		return []string{"Node observation is off. It needs list and watch on nodes cluster-wide; turn it on with nodes.observe (OPSTIDE_OBSERVE_NODES)."}
	}
	return s.unreadableNodes()
}

func (s *Server) unreadableNodes() []string {
	var out []string
	for _, p := range s.w.Problems() {
		if kind, _, _ := strings.Cut(p.Scope, "/"); kind == "nodes" {
			out = append(out, "Nodes cannot be read ("+p.Err+"): what is shown may be incomplete.")
		}
	}
	return out
}

func (s *Server) nodeList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	nq := store.NodeQuery{Text: q.Get("q"), Status: q.Get("status")}
	var err error
	if nq.Status != "" && nq.Status != "live" && nq.Status != "removed" {
		err = badRequest("status takes live or removed")
	}
	if nq.Status == "removed" {
		nq.Status = "deleted"
	}
	if len(nq.Text) > 200 {
		err = badRequest("q is too long")
	}
	if err == nil {
		nq.Limit, err = intParam(r, "limit", 200, 500)
	}
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	nodes, err := s.db.Nodes(r.Context(), nq)
	out := NodesList{Schema: Schema, Enabled: s.cfg.ObserveNodes, Nodes: []NodeView{}, Notes: s.nodeNotes()}
	for _, n := range nodes {
		out.Nodes = append(out.Nodes, nodeView(n))
	}
	reply(w, r, s.log, out, err)
}

// NodeDetail is one Node with the Pods observed on it. The Pods are placement context: where each Pod
// reported it ran, not what it belongs to or what happened because of it.
type NodeDetail struct {
	Schema int       `json:"schema"`
	Node   NodeView  `json:"node"`
	Pods   []NodePod `json:"pods"`
	Notes  []string  `json:"notes"`
	// PodsTruncated is set when more Pods were observed than are listed.
	PodsTruncated bool `json:"podsTruncated,omitempty"`
}

// NodePod is a Pod observed on a Node.
type NodePod struct {
	UID          string     `json:"uid"`
	Name         string     `json:"name"`
	Namespace    string     `json:"namespace"`
	WorkloadID   string     `json:"workloadId,omitempty"`
	WorkloadKind string     `json:"workloadKind,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	TerminatedAt *time.Time `json:"terminatedAt"`
}

func (s *Server) loadNode(r *http.Request) (store.Node, error) {
	n, ok, err := s.db.NodeByUID(r.Context(), r.PathValue("id"))
	if err == nil && !ok {
		err = errNotFound
	}
	return n, err
}

func (s *Server) nodeOne(w http.ResponseWriter, r *http.Request) {
	n, err := s.loadNode(r)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	pods, err := s.db.PodsOnNode(r.Context(), n.Name, 201)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	out := NodeDetail{Schema: Schema, Node: nodeView(n), Pods: []NodePod{}, Notes: s.nodeNotes()}
	// Only Pods that were observed on a Node of this name while this Node existed: a Node recreated
	// under the same name is another Node.
	from := n.CreatedAt
	if from.IsZero() {
		from = n.FirstSeenAt
	}
	for _, p := range pods {
		if p.CreatedAt.Before(from.Add(-time.Second)) && !n.CreatedAt.IsZero() {
			continue
		}
		if !n.DeletedAt.IsZero() && p.CreatedAt.After(n.DeletedAt) {
			continue
		}
		if len(out.Pods) == 200 {
			out.PodsTruncated = true
			break
		}
		out.Pods = append(out.Pods, NodePod{UID: p.UID, Name: p.Name, Namespace: p.Namespace, WorkloadID: p.WorkloadID, WorkloadKind: p.Kind, CreatedAt: p.CreatedAt, TerminatedAt: tp(p.TerminatedAt)})
	}
	if len(out.Pods) == 0 {
		out.Notes = append(out.Notes, "No Pod OpsTide observed has reported running on this Node. Pods it did not observe are not listed, and Job Pods are not placed here.")
	}
	reply(w, r, s.log, out, nil)
}

// NodeHistory is the Node's recorded transitions and the Kubernetes Events that name it.
type NodeHistory struct {
	Schema     int              `json:"schema"`
	Entries    []Entry          `json:"entries"`
	NextCursor string           `json:"nextCursor,omitempty"`
	Records    []EvidenceRecord `json:"records"`
}

func (s *Server) nodeHistory(w http.ResponseWriter, r *http.Request) {
	n, err := s.loadNode(r)
	var eq store.EventQuery
	if err == nil {
		eq.Limit, err = intParam(r, "limit", 100, 500)
	}
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	eq.Kind, eq.ResourceUID, eq.Cursor = "Node", n.UID, r.URL.Query().Get("cursor")
	page, err := s.db.Events(r.Context(), eq)
	out := NodeHistory{Schema: Schema, Entries: []Entry{}, NextCursor: page.NextCursor, Records: []EvidenceRecord{}}
	for _, e := range page.Events {
		out.Entries = append(out.Entries, entry(e))
	}
	if err == nil {
		ev, eerr := s.db.Evidence(r.Context(), store.EvidenceQuery{NodeID: n.UID, Limit: 50})
		err = eerr
		for _, e := range ev.Records {
			out.Records = append(out.Records, evidenceRecord(e))
		}
	}
	if err != nil && strings.HasPrefix(err.Error(), "bad cursor") {
		err = badRequest(err.Error())
	}
	reply(w, r, s.log, out, err)
}

// RunEvidence is the Kubernetes Events linked to a run, by its Job's UID or its Pods' ownerReferences.
type RunEvidence struct {
	Schema  int              `json:"schema"`
	Records []EvidenceRecord `json:"records"`
	// Notes are the limits of what can be shown: a run's Events exist only if OpsTide was watching when Kubernetes had them.
	Notes      []string   `json:"notes"`
	Provenance Provenance `json:"provenance"`
}

func (s *Server) runEvidence(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	run, ok, err := s.db.Run(ctx, r.PathValue("id"))
	if err == nil && !ok {
		err = errNotFound
	}
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	page, err := s.db.Evidence(ctx, store.EvidenceQuery{RunID: run.ID, Limit: 100})
	out := RunEvidence{Schema: Schema, Records: []EvidenceRecord{}, Notes: []string{}, Provenance: Provenance{EventsAPI: s.eventsSource(), Notes: []string{}}}
	for _, e := range page.Records {
		out.Records = append(out.Records, evidenceRecord(e))
	}
	out.Notes = append(out.Notes, "Events Kubernetes had already dropped when OpsTide began watching are not recorded, and an Event is linked to this run only through the UID of its Job or the ownerReference of its Pod.")
	if len(out.Records) == 0 {
		out.Notes = append(out.Notes, "No Kubernetes Event is recorded for this run.")
	}
	if page.NextCursor != "" {
		out.Notes = append(out.Notes, "More Events are recorded than are shown.")
	}
	if !s.eventsReadable() {
		out.Provenance.Notes = append(out.Provenance.Notes, "OpsTide cannot read Kubernetes Events right now: evidence from now on may be missing.")
	}
	reply(w, r, s.log, out, err)
}
