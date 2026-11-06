package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/mihnk/opstide/internal/evidence"
	"github.com/mihnk/opstide/internal/release"
	"github.com/mihnk/opstide/internal/store"
)

// Relationship says how two records are known to be connected. It is a
// classification of the link, never a statement that one caused the other.
const (
	// Direct: the Event names the resource by UID.
	Direct = "DIRECT"
	// Ownership: the resource is linked to another through ownerReferences.
	Ownership = "OWNERSHIP"
	// SameResourceContext: both concern the same resource or workload; nothing is asserted between them.
	SameResourceContext = "SAME_RESOURCE_CONTEXT"
	// Unresolved: there is not enough evidence to link them.
	Unresolved = "UNRESOLVED"
)

// ResourceRef is a resource as an Event names it.
type ResourceRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
	UID       string `json:"uid,omitempty"`
}

// EvidenceLink connects a record to a resource OpsTide has seen.
type EvidenceLink struct {
	Kind         string `json:"kind"` // ReplicaSet | Workload | Run | Operation | HorizontalPodAutoscaler
	UID          string `json:"uid,omitempty"`
	Relationship string `json:"relationship"`
	Via          string `json:"via,omitempty"`
}

// Count is the occurrence count Kubernetes reported, and which field it came from.
type Count struct {
	Value int    `json:"value"`
	Basis string `json:"basis"` // series | deprecatedCount | count
}

// Times separates the source's own times from OpsTide's: a missing one is null.
type Times struct {
	First *time.Time `json:"first"`
	Last  *time.Time `json:"last"`
}

// EvidenceRecord is one Kubernetes Event as recorded. Reason and Message are
// Kubernetes' words about what it observed, not OpsTide's conclusion.
type EvidenceRecord struct {
	ID               string         `json:"id"`
	Source           string         `json:"source"` // events.k8s.io/v1 | core/v1
	Kind             string         `json:"kind"`   // KubernetesEvent
	SourceUID        string         `json:"sourceUid"`
	Namespace        string         `json:"namespace"`
	Name             string         `json:"name"`
	Reason           string         `json:"reason"`
	EventType        string         `json:"eventType"`
	Message          string         `json:"message"`
	MessageTruncated bool           `json:"messageTruncated,omitempty"`
	Action           string         `json:"action,omitempty"`
	Reporter         string         `json:"reporter,omitempty"`
	OccurredAt       Times          `json:"occurredAt"`
	ObservedAt       time.Time      `json:"observedAt"` // when OpsTide first saw it
	UpdatedAt        time.Time      `json:"updatedAt"`
	Count            *Count         `json:"count"`
	Resource         ResourceRef    `json:"resource"`
	Related          *ResourceRef   `json:"related,omitempty"`
	Relationship     string         `json:"relationship"` // of the record to the resource it names
	Links            []EvidenceLink `json:"links"`
	Origin           string         `json:"origin"` // observed | adopted
	SourceDeletedAt  *time.Time     `json:"sourceDeletedAt"`
	Certainty        string         `json:"certainty"` // OBSERVED
}

func tp(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// evidenceRecord builds the API view. Links come from what the collector stored
// from UIDs and ownerReferences; nothing is added here.
func evidenceRecord(e store.Evidence) EvidenceRecord {
	rec := EvidenceRecord{
		ID: e.ID, Source: e.Source, Kind: "KubernetesEvent", SourceUID: e.SourceUID, Namespace: e.Namespace, Name: e.Name, Reason: e.Reason,
		EventType: e.EventType, Message: e.Message, MessageTruncated: e.MessageTruncated, Action: e.Action, Reporter: e.Reporter,
		OccurredAt: Times{First: tp(e.FirstAt), Last: tp(e.LastAt)}, ObservedAt: e.ObservedAt, UpdatedAt: e.UpdatedAt,
		Resource: ResourceRef{Kind: e.Regarding.Kind, Namespace: e.Namespace, Name: e.Regarding.Name, UID: e.Regarding.UID},
		Origin:   e.Origin, SourceDeletedAt: tp(e.SourceDeletedAt), Certainty: "OBSERVED", Links: []EvidenceLink{},
	}
	if e.Count > 0 {
		rec.Count = &Count{Value: e.Count, Basis: e.CountBasis}
	}
	if e.Related.Kind != "" || e.Related.UID != "" {
		rec.Related = &ResourceRef{Kind: e.Related.Kind, Namespace: e.Namespace, Name: e.Related.Name, UID: e.Related.UID}
	}
	rec.Relationship = Direct
	if e.Regarding.UID == "" {
		rec.Relationship = Unresolved
		rec.Links = append(rec.Links, EvidenceLink{Kind: e.Regarding.Kind, Relationship: Unresolved, Via: "the Event names no UID, and OpsTide does not match by name"})
		return rec
	}
	owned := func(kind string) string { // how a link relates to the resource the Event names
		if e.Regarding.Kind == kind {
			return Direct
		}
		return Ownership
	}
	if e.ReplicaSetUID != "" {
		rec.Links = append(rec.Links, EvidenceLink{Kind: "ReplicaSet", UID: e.ReplicaSetUID, Relationship: owned("ReplicaSet"), Via: e.LinkVia})
	}
	if e.WorkloadID != "" {
		rel, via := owned("Deployment"), e.LinkVia
		if e.Regarding.Kind == "HorizontalPodAutoscaler" {
			// The autoscaler names the Deployment by name in its scaleTargetRef, not by UID.
			rel, via = SameResourceContext, "the autoscaler's spec.scaleTargetRef names the Deployment (by name, same namespace)"
		}
		rec.Links = append(rec.Links, EvidenceLink{Kind: "Workload", UID: e.WorkloadID, Relationship: rel, Via: via})
	}
	if e.RunID != "" {
		rec.Links = append(rec.Links, EvidenceLink{Kind: "Run", UID: e.RunID, Relationship: owned("Job"), Via: e.LinkVia})
	}
	if e.OperationID != "" {
		rec.Links = append(rec.Links, EvidenceLink{Kind: "Operation", UID: e.OperationID, Relationship: owned("CronJob"), Via: e.LinkVia})
	}
	if e.HPAUID != "" {
		rec.Links = append(rec.Links, EvidenceLink{Kind: "HorizontalPodAutoscaler", UID: e.HPAUID, Relationship: Direct, Via: "regarding.uid"})
	}
	if len(rec.Links) == 0 {
		rec.Links = append(rec.Links, EvidenceLink{Kind: e.Regarding.Kind, UID: e.Regarding.UID, Relationship: Unresolved, Via: "OpsTide has not recorded this resource, so nothing it owns or is owned by is known"})
	}
	return rec
}

// EvidenceList is a page of evidence.
type EvidenceList struct {
	Schema     int              `json:"schema"`
	Records    []EvidenceRecord `json:"records"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

func (s *Server) evidenceList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var eq store.EvidenceQuery
	var err error
	if eq.From, err = timeParam(r, "from"); err == nil {
		eq.To, err = timeParam(r, "to")
	}
	if err == nil {
		eq.Limit, err = intParam(r, "limit", 100, 500)
	}
	if v := q.Get("within"); err == nil && v != "" {
		d, perr := parseSpan(v)
		if perr != nil || d > 400*24*time.Hour {
			err = badRequest("within must be a duration such as 48h or 5d")
		} else if eq.From.IsZero() {
			eq.From = s.now().Add(-d)
		}
	}
	if err == nil {
		eq.Reasons = splitList(q.Get("reason"))
		eq.EventTypes = splitList(q.Get("type"))
		for _, t := range eq.EventTypes {
			if t != "Normal" && t != "Warning" {
				err = badRequest("type takes Normal or Warning")
			}
		}
		if len(eq.Reasons) > 20 || len(q.Get("q")) > 200 {
			err = badRequest("too many reasons, or q is too long")
		}
	}
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	eq.Namespace, eq.Kind, eq.RegardingUID, eq.WorkloadID = q.Get("namespace"), q.Get("kind"), q.Get("uid"), q.Get("workload")
	eq.ReplicaSet, eq.RunID, eq.HPAUID, eq.Text, eq.Cursor = q.Get("replicaset"), q.Get("run"), q.Get("hpa"), q.Get("q"), q.Get("cursor")
	page, err := s.db.Evidence(r.Context(), eq)
	out := EvidenceList{Schema: Schema, Records: []EvidenceRecord{}, NextCursor: page.NextCursor}
	for _, e := range page.Records {
		out.Records = append(out.Records, evidenceRecord(e))
	}
	if err != nil && strings.HasPrefix(err.Error(), "bad cursor") {
		err = badRequest(err.Error())
	}
	reply(w, r, s.log, out, err)
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// EvidenceOne is one record on its own.
type EvidenceOne struct {
	Schema int            `json:"schema"`
	Record EvidenceRecord `json:"record"`
}

func (s *Server) evidenceOne(w http.ResponseWriter, r *http.Request) {
	e, ok, err := s.db.EvidenceByID(r.Context(), r.PathValue("id"))
	if err == nil && !ok {
		err = errNotFound
	}
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	reply(w, r, s.log, EvidenceOne{Schema: Schema, Record: evidenceRecord(e)}, nil)
}

// ReleaseRecord is a record in a release's evidence with how it relates to the
// release, which is not the same as how it relates to the resource it names.
type ReleaseRecord struct {
	EvidenceRecord
	// ReleaseRelationship: DIRECT (the Event names the release's ReplicaSet),
	// OWNERSHIP (it names a Pod that ReplicaSet owns), SAME_RESOURCE_CONTEXT (it
	// names the Deployment or an autoscaler targeting it, during the release).
	ReleaseRelationship string `json:"releaseRelationship"`
	ReleaseVia          string `json:"releaseVia"`
	Scope               string `json:"scope"` // release | workload
}

// ReleaseEvidence is the Evidence tab of a release.
type ReleaseEvidence struct {
	Schema  int             `json:"schema"`
	Release string          `json:"release"`
	From    time.Time       `json:"from"`
	To      time.Time       `json:"to,omitzero"` // zero: still current
	Records []ReleaseRecord `json:"records"`
	// Scaling are the autoscaler and replica-target entries for the Deployment during the release.
	Scaling []Entry `json:"scaling"`
	// Summary is built only from the records and entries here and from documented rules.
	Summary evidence.Summary `json:"summary"`
	// Context are observations set aside from the release's verdict, with the rule.
	Context   []release.Note `json:"context"`
	Truncated bool           `json:"truncated"`
	Counts    map[string]int `json:"counts"`
	// Provenance says where the evidence comes from and whether that source is readable now.
	Provenance Provenance `json:"provenance"`
}

// Provenance says which API the Events come from and what could not be read.
type Provenance struct {
	EventsAPI string   `json:"eventsApi"`
	Notes     []string `json:"notes"`
}

// releaseWindow is the span whose evidence belongs to a release: from its
// ReplicaSet's creation (observation for a rollback, whose ReplicaSet is older)
// until a newer revision took over.
func (s *Server) releaseWindow(r *http.Request, rel store.Release) (time.Time, time.Time, error) {
	from := rel.ObservedAt.Add(-time.Minute)
	if !rel.StartedAt.IsZero() && rel.StartedAt.Before(rel.ObservedAt) {
		from = rel.StartedAt
	}
	rb, err := s.db.IsRollback(r.Context(), rel)
	if err != nil {
		return from, time.Time{}, err
	}
	if rb {
		from = rel.ObservedAt
	}
	return from, rel.SupersededAt, nil
}

func (s *Server) releaseEvidence(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rel, err := s.loadRelease(r)
	var limit int
	if err == nil {
		limit, err = intParam(r, "limit", 200, 500)
	}
	var from, to time.Time
	if err == nil {
		from, to, err = s.releaseWindow(r, rel)
	}
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	page, err := s.db.ReleaseEvidence(ctx, store.ReleaseEvidenceQuery{ReplicaSetUID: rel.ReplicaSetUID, WorkloadID: rel.WorkloadID, From: from, To: to, Limit: limit})
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	out := ReleaseEvidence{Schema: Schema, Release: rel.ID, From: from, To: to, Records: []ReleaseRecord{}, Scaling: []Entry{}, Context: []release.Note{},
		Truncated: page.NextCursor != "", Counts: map[string]int{Direct: 0, Ownership: 0, SameResourceContext: 0, "warnings": 0},
		Provenance: Provenance{EventsAPI: s.eventsSource(), Notes: []string{}}}
	for _, e := range page.Records {
		rr := ReleaseRecord{EvidenceRecord: evidenceRecord(e)}
		switch {
		case e.ReplicaSetUID == rel.ReplicaSetUID && e.Regarding.Kind == "ReplicaSet":
			rr.ReleaseRelationship, rr.ReleaseVia, rr.Scope = Direct, "the Event names this release's ReplicaSet by UID", "release"
		case e.ReplicaSetUID == rel.ReplicaSetUID:
			rr.ReleaseRelationship, rr.ReleaseVia, rr.Scope = Ownership, "the Event names a "+e.Regarding.Kind+" owned by this release's ReplicaSet (ownerReference)", "release"
		default:
			rr.ReleaseRelationship, rr.ReleaseVia, rr.Scope = SameResourceContext, "the Event concerns the "+kindWord(e.Regarding.Kind)+" of the same Deployment while this release was current; no link to the release itself is asserted", "workload"
		}
		out.Counts[rr.ReleaseRelationship]++
		if e.EventType == "Warning" {
			out.Counts["warnings"]++
		}
		out.Records = append(out.Records, rr)
	}
	sp, err := s.db.Events(ctx, store.EventQuery{WorkloadID: rel.WorkloadID, Types: []string{"hpa.", "workload.replica_target_changed"}, From: from, To: to, Limit: 100})
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	for _, e := range sp.Events {
		out.Scaling = append(out.Scaling, entry(e))
	}
	// Context notes come from the stored verdict's rule set.
	pods, err := s.db.ReleasePods(ctx, rel)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	rb, err := s.db.IsRollback(ctx, rel)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	now := s.now().UTC()
	v := release.Assess(release.InputFor(rel, pods, rb, now, 0, s.watchedFn(ctx, rel.Namespace)))
	out.Context = v.Context
	var names []string
	for _, p := range pods {
		names = append(names, p.Name)
	}
	recs := page.Records
	out.Summary = evidence.Build(evidence.Input{Release: rel, Pods: names, Records: recs, Scaling: sp.Events, Context: v.Context, Complete: !out.Truncated && sp.NextCursor == ""})
	if !s.eventsReadable() {
		out.Provenance.Notes = append(out.Provenance.Notes, "OpsTide cannot read Kubernetes Events right now: evidence from now on may be missing.")
	}
	if rel.Origin == store.OriginAdopted {
		out.Provenance.Notes = append(out.Provenance.Notes, "This release existed before OpsTide began watching: Events from before then are recorded only if Kubernetes still held them at that time.")
	}
	reply(w, r, s.log, out, nil)
}

func kindWord(k string) string {
	if k == "HorizontalPodAutoscaler" {
		return "autoscaler"
	}
	return k
}

// eventsSource is the API Events are read through, when the collector says.
func (s *Server) eventsSource() string {
	if src, ok := s.w.(interface{ EvidenceSource() string }); ok {
		return src.EvidenceSource()
	}
	return ""
}

// eventsReadable is false while the Event watch is failing.
func (s *Server) eventsReadable() bool {
	for _, p := range s.w.Problems() {
		if strings.HasPrefix(p.Scope, "events") {
			return false
		}
	}
	return true
}

// HPAView is an autoscaler as last observed.
type HPAView struct {
	UID             string          `json:"uid"`
	Namespace       string          `json:"namespace"`
	Name            string          `json:"name"`
	Target          string          `json:"target"`
	TargetRelation  string          `json:"targetRelationship"`
	TargetVia       string          `json:"targetVia"`
	Min             *int32          `json:"minReplicas"`
	Max             int32           `json:"maxReplicas"`
	CurrentReplicas int32           `json:"currentReplicas"`
	DesiredReplicas int32           `json:"desiredReplicas"`
	LastScaleAt     *time.Time      `json:"lastScaleTime"`
	Conditions      json.RawMessage `json:"conditions"`
	Metrics         json.RawMessage `json:"metrics"`
	ObservedAt      time.Time       `json:"updatedAt"`
	DeletedAt       *time.Time      `json:"deletedAt"`
}

// WorkloadScaling is the scaling context of a Deployment.
type WorkloadScaling struct {
	Schema   int         `json:"schema"`
	Workload WorkloadRef `json:"workload"`
	// ReplicaTarget is the Deployment's replica target as last recorded; null when never recorded.
	ReplicaTarget *int32           `json:"replicaTarget"`
	HPAs          []HPAView        `json:"hpas"`
	Transitions   []Entry          `json:"transitions"`
	Records       []EvidenceRecord `json:"records"` // Events about the autoscalers
}

func (s *Server) workloadScaling(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	wl, ok, err := s.db.Workload(ctx, id)
	if err == nil && !ok {
		err = errNotFound
	}
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	out := WorkloadScaling{Schema: Schema, Workload: WorkloadRef{UID: wl.ID, Namespace: wl.Namespace, Name: wl.Name}, HPAs: []HPAView{}, Transitions: []Entry{}, Records: []EvidenceRecord{}}
	if n, known, err := s.db.SpecReplicas(ctx, id); err != nil {
		reply(w, r, s.log, nil, err)
		return
	} else if known {
		out.ReplicaTarget = &n
	}
	hpas, err := s.db.HPAsOfWorkload(ctx, id)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	for _, h := range hpas {
		out.HPAs = append(out.HPAs, HPAView{UID: h.UID, Namespace: h.Namespace, Name: h.Name, Target: h.TargetKind + "/" + h.TargetName,
			TargetRelation: SameResourceContext, TargetVia: "spec.scaleTargetRef names the Deployment; resolved by name in the same namespace",
			Min: h.MinReplicas, Max: h.MaxReplicas, CurrentReplicas: h.CurrentReplicas, DesiredReplicas: h.DesiredReplicas, LastScaleAt: tp(h.LastScaleAt),
			Conditions: rawJSON(h.Conditions), Metrics: rawJSON(h.Metrics), ObservedAt: h.UpdatedAt, DeletedAt: tp(h.DeletedAt)})
	}
	lim, err := intParam(r, "limit", 50, 200)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	pg, err := s.db.Events(ctx, store.EventQuery{WorkloadID: id, Types: []string{"hpa.", "workload.replica_target_changed"}, Limit: lim})
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	for _, e := range pg.Events {
		out.Transitions = append(out.Transitions, entry(e))
	}
	for _, h := range hpas {
		ev, err := s.db.Evidence(ctx, store.EvidenceQuery{HPAUID: h.UID, Limit: 20})
		if err != nil {
			reply(w, r, s.log, nil, err)
			return
		}
		for _, e := range ev.Records {
			out.Records = append(out.Records, evidenceRecord(e))
		}
	}
	reply(w, r, s.log, out, nil)
}

// rawJSON passes stored compact JSON through, or an empty list if it is not valid.
func rawJSON(s string) json.RawMessage {
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	return json.RawMessage("[]")
}
