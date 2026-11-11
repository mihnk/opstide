package api

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mihnk/opstide/internal/collector"
	"github.com/mihnk/opstide/internal/store"
)

// Relationship classes (the same four the evidence views use).
// REFERENCE is added for a workload whose Pod template names a ConfigMap or Secret.
var _ = []string{Direct, Ownership, SameResourceContext, Unresolved, Reference}

// Relationship says how a record is known to connect to another record or resource. It is a classification of the
// link, never a statement that one caused the other.
type Relationship struct {
	Kind           string `json:"kind"` // Workload | Release | Run | Operation | Node | ReplicaSet | HorizontalPodAutoscaler
	UID            string `json:"uid,omitempty"`
	Name           string `json:"name,omitempty"`
	Classification string `json:"classification"`
	Via            string `json:"via,omitempty"`
	// Resolved says whether OpsTide holds a record of the target. Only the drilldown looks it up.
	Resolved *bool  `json:"resolved,omitempty"`
	Path     string `json:"path,omitempty"` // where the UI shows the target
	Note     string `json:"note,omitempty"`
}

// relationshipsOf lists what a stored record names, from the stored fields alone. Nothing is looked up or inferred.
func relationshipsOf(row store.ExplorerRow) []Relationship {
	if ev := row.Evidence; ev != nil {
		return evidenceRelationships(*ev)
	}
	return eventRelationships(*row.Event)
}

func evidenceRelationships(e store.Evidence) []Relationship {
	rec := evidenceRecord(e)
	out := []Relationship{}
	for _, l := range rec.Links {
		if e.Regarding.Kind == "Node" {
			continue // handled below: a Node is linked by UID, by exact name while it existed, or not at all
		}
		out = append(out, Relationship{Kind: l.Kind, UID: l.UID, Classification: l.Relationship, Via: l.Via})
	}
	if e.Regarding.Kind == "Node" {
		switch {
		case e.NodeID != "" && e.LinkVia == "regarding.uid":
			out = append(out, Relationship{Kind: "Node", UID: e.NodeID, Name: e.Regarding.Name, Classification: Direct, Via: e.LinkVia})
		case e.NodeID != "":
			out = append(out, Relationship{Kind: "Node", UID: e.NodeID, Name: e.Regarding.Name, Classification: SameResourceContext, Via: e.LinkVia,
				Note: "Kubernetes reports a Node's name where the UID would be; this Node had exactly that name when the Event happened."})
		default:
			out = append(out, Relationship{Kind: "Node", Name: e.Regarding.Name, Classification: Unresolved,
				Via: "No Node OpsTide recorded matches this Event by UID, or by exact name while a Node of that name existed (Node observation may be off)."})
		}
	}
	return out
}

func eventRelationships(e store.Event) []Relationship {
	out := []Relationship{}
	self := func(uid string) bool { return e.ResourceUID == uid }
	if e.WorkloadID != "" {
		switch {
		case self(e.WorkloadID):
			out = append(out, Relationship{Kind: "Workload", UID: e.WorkloadID, Name: e.ResourceName, Classification: Direct, Via: "this record is about the workload"})
		case e.Kind == "Pod":
			out = append(out, Relationship{Kind: "Workload", UID: e.WorkloadID, Classification: Ownership, Via: "Pod ownerReference chain, as recorded"})
		default:
			out = append(out, Relationship{Kind: "Workload", UID: e.WorkloadID, Classification: SameResourceContext, Via: "recorded against the same workload"})
		}
	}
	if e.ReleaseID != "" {
		c, via := SameResourceContext, "recorded within this release's window"
		if strings.HasPrefix(e.Type, "release.") {
			c, via = Direct, "this record is about the release"
		}
		out = append(out, Relationship{Kind: "Release", UID: e.ReleaseID, Classification: c, Via: via})
	}
	if e.RunID != "" && !self(e.RunID) {
		c, via := SameResourceContext, "recorded against the same run"
		if e.Kind == "Pod" {
			c, via = Ownership, "Pod ownerReference to Job"
		}
		out = append(out, Relationship{Kind: "Run", UID: e.RunID, Classification: c, Via: via})
	}
	if e.OperationID != "" && !self(e.OperationID) {
		c, via := SameResourceContext, "recorded against the same scheduled operation"
		if e.Kind == "Job" || e.Kind == "Pod" {
			c, via = Ownership, "Job ownerReference to CronJob"
		}
		out = append(out, Relationship{Kind: "Operation", UID: e.OperationID, Classification: c, Via: via})
	}
	if e.Kind == "Pod" && e.WorkloadID == "" && e.RunID == "" {
		out = append(out, Relationship{Kind: "Workload", Classification: Unresolved, Via: "OpsTide recorded no owner for this Pod"})
	}
	return out
}

// ---- the drilldown ----

// ExplorerObservation separates when something happened from when OpsTide saw it.
type ExplorerObservation struct {
	RecordTime     time.Time  `json:"recordTime"`
	TimeBasis      string     `json:"timeBasis"` // occurredAt | firstOccurrence | observedAt
	Explanation    string     `json:"explanation"`
	OccurredAt     *time.Time `json:"occurredAt"`
	LastOccurredAt *time.Time `json:"lastOccurredAt,omitempty"`
	ObservedAt     time.Time  `json:"observedAt"`
	RecordedAt     *time.Time `json:"recordedAt,omitempty"`
	UpdatedAt      *time.Time `json:"updatedAt,omitempty"`
	Origin         string     `json:"origin,omitempty"` // observed | adopted, for Kubernetes Events
}

// ExplorerProvenance says what kind of fact a record is.
type ExplorerProvenance struct {
	Class       string `json:"class"`
	Source      string `json:"source"`
	Explanation string `json:"explanation"`
}

// EvidenceRef names a record a derived fact was computed from.
type EvidenceRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Path string `json:"path,omitempty"`
}

// ExplorerDerivation is how a derived fact was computed: its rule, the version, and the inputs.
type ExplorerDerivation struct {
	RuleID       string            `json:"ruleId"`
	RuleSet      string            `json:"ruleSet"`
	RuleVersion  string            `json:"ruleVersion"`
	Inputs       map[string]string `json:"inputs"`
	EvidenceRefs []EvidenceRef     `json:"evidenceRefs"`
	Note         string            `json:"note,omitempty"`
}

// ExplorerOriginal is the record as stored, before any of the views above.
type ExplorerOriginal struct {
	Kind  string          `json:"kind"` // log_entry | kubernetes_event
	Entry *Entry          `json:"entry,omitempty"`
	Event *EvidenceRecord `json:"event,omitempty"`
}

// ExplorerRecordCoverage says whether OpsTide was watching what the record concerns at the record's time.
type ExplorerRecordCoverage struct {
	State  string   `json:"state"` // watching | gap | before_first_observation | disabled | not_applicable
	Scopes []string `json:"scopes"`
	Note   string   `json:"note"`
}

// ExplorerCurrent is a link to the live view of the resource. It is current state, never the state at the record's time.
type ExplorerCurrent struct {
	Label string `json:"label"`
	Path  string `json:"path"`
}

// ExplorerDetail is the drilldown of one record.
type ExplorerDetail struct {
	Schema        int                    `json:"schema"`
	Record        ExplorerRecord         `json:"record"`
	Observation   ExplorerObservation    `json:"observation"`
	Provenance    ExplorerProvenance     `json:"provenance"`
	Derivation    *ExplorerDerivation    `json:"derivation"`
	Relationships []Relationship         `json:"relationships"`
	Original      ExplorerOriginal       `json:"original"`
	Coverage      ExplorerRecordCoverage `json:"coverage"`
	Current       *ExplorerCurrent       `json:"current"`
	Limitations   []string               `json:"limitations"`
}

func (s *Server) explorerRecordOne(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !idRe.MatchString(id) {
		reply(w, r, s.log, nil, badRequest("that is not a record id"))
		return
	}
	rows, _, err := s.db.ExplorerRowsByID(r.Context(), []string{id})
	if err == nil && len(rows) == 0 {
		err = errNotFound
	}
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	d, err := s.detailOf(r.Context(), rows[0])
	reply(w, r, s.log, d, err)
}

func (s *Server) detailOf(ctx context.Context, row store.ExplorerRow) (ExplorerDetail, error) {
	rec := recordOfRow(row, true)
	d := ExplorerDetail{Schema: Schema, Record: rec, Relationships: relationshipsOf(row), Limitations: []string{}}
	if err := s.resolve(ctx, d.Relationships); err != nil {
		return d, err
	}
	extra, err := s.podRelationships(ctx, rec, d.Relationships)
	if err != nil {
		return d, err
	}
	d.Relationships = append(d.Relationships, extra...)
	refs, err := s.configReferrers(ctx, rec)
	if err != nil {
		return d, err
	}
	d.Relationships = append(d.Relationships, refs...)
	d.Observation = ExplorerObservation{RecordTime: rec.At, ObservedAt: rec.ObservedAt, OccurredAt: rec.OccurredAt, LastOccurredAt: rec.LastOccurredAt}
	switch {
	case row.Evidence != nil:
		e := *row.Evidence
		er := evidenceRecord(e)
		d.Original = ExplorerOriginal{Kind: "kubernetes_event", Event: &er}
		d.Observation.UpdatedAt, d.Observation.Origin = tp(e.UpdatedAt), e.Origin
		d.Observation.TimeBasis = "firstOccurrence"
		d.Observation.Explanation = "The time Kubernetes reported for the Event's first occurrence. OpsTide first saw it at the observed time."
		if e.FirstAt.IsZero() {
			d.Observation.TimeBasis = "observedAt"
			d.Observation.Explanation = "Kubernetes gave this Event no time, so the time shown is when OpsTide first saw it."
			d.Limitations = append(d.Limitations, "The source gave no time for this Event; its position on the timeline is when OpsTide first saw it, not when it happened.")
		}
		d.Provenance = ExplorerProvenance{ProvObserved, rec.Source, "Reported by Kubernetes and stored as reported. OpsTide does not know why the Event was emitted."}
		if e.Origin == store.OriginAdopted {
			d.Limitations = append(d.Limitations, "This Event was already present when OpsTide began watching; when it was first emitted may be earlier than when OpsTide saw it.")
		}
		if e.MessageTruncated {
			d.Limitations = append(d.Limitations, "The message was longer than OpsTide stores and was cut.")
		}
		if !e.SourceDeletedAt.IsZero() {
			d.Limitations = append(d.Limitations, "Kubernetes has since dropped this Event; OpsTide's copy is what remains.")
		}
		d.Limitations = append(d.Limitations, "A Kubernetes Event is the cluster's own report. Other records near it in time are not thereby related to it.")
	default:
		e := *row.Event
		en := entry(e)
		d.Original = ExplorerOriginal{Kind: "log_entry", Entry: &en}
		d.Observation.RecordedAt = tp(e.RecordedAt)
		d.Observation.TimeBasis = "occurredAt"
		d.Observation.Explanation = "The time the source gives for it."
		if e.OccurredAt.IsZero() {
			d.Observation.TimeBasis = "observedAt"
			d.Observation.Explanation = "The source gave no time, so the time shown is when OpsTide first observed it."
			d.Limitations = append(d.Limitations, "The source gave no time for this; its position on the timeline is when OpsTide observed it, not necessarily when it happened.")
		}
		switch e.Confidence {
		case store.Derived:
			d.Provenance = ExplorerProvenance{ProvDerived, rec.Source, "A documented rule of OpsTide applied to recorded observations. It is a result, not an observation."}
			dv, err := s.derivationOf(ctx, e)
			if err != nil {
				return d, err
			}
			d.Derivation = &dv
		case store.Unknown:
			d.Provenance = ExplorerProvenance{ProvUnknown, rec.Source, "OpsTide records that it does not know this."}
		default:
			d.Provenance = ExplorerProvenance{ProvObserved, rec.Source, "Read from Kubernetes or from OpsTide's own watching, and recorded as seen."}
		}
	}
	if d.Coverage, err = s.coverageAtRecord(ctx, rec); err != nil {
		return d, err
	}
	if d.Coverage.State == CovGap || d.Coverage.State == CovBeforeFirst {
		d.Limitations = append(d.Limitations, d.Coverage.Note)
	}
	d.Current = s.currentLink(rec)
	return d, nil
}

// podRelationships adds, for a record about a Pod OpsTide recorded, the revision it belongs to: the release of its
// ReplicaSet for a Deployment's Pod, the ControllerRevision its label names for a StatefulSet's or DaemonSet's. These
// are what the Pod record itself says; nothing is inferred. Relationships the record already has are not repeated.
func (s *Server) podRelationships(ctx context.Context, rec ExplorerRecord, have []Relationship) ([]Relationship, error) {
	if rec.Resource.Kind != "Pod" || rec.Resource.UID == "" {
		return nil, nil
	}
	p, ok, err := s.db.PodByUID(ctx, rec.Resource.UID)
	if err != nil || !ok {
		return nil, err
	}
	var out []Relationship
	has := func(kind, uid string) bool {
		for _, r := range have {
			if r.Kind == kind && r.UID == uid {
				return true
			}
		}
		return false
	}
	switch {
	case p.ReleaseID != "" && (p.Kind == "" || p.Kind == store.KindDeployment):
		if has("Release", p.ReleaseID) {
			return nil, nil
		}
		rel, found, err := s.db.Release(ctx, p.ReleaseID)
		if err != nil {
			return nil, err
		}
		r := Relationship{Kind: "Release", UID: p.ReleaseID, Classification: Ownership, Via: "the Pod's ownerReference names a ReplicaSet, which is this release's, as OpsTide recorded it"}
		f := found
		r.Resolved = &f
		if found {
			r.Path, r.Name = "/releases/"+pathValue(p.ReleaseID), rel.Workload+" "+rel.Revision
		}
		out = append(out, r)
	case p.Revision != "" && p.WorkloadID != "":
		revs, err := s.db.Revisions(ctx, p.WorkloadID)
		if err != nil {
			return nil, err
		}
		r := Relationship{Kind: "Revision", Name: p.Revision, Classification: Direct, Via: "the Pod's controller-revision-hash label names this revision, as recorded"}
		found := false
		for _, rv := range revs {
			if rv.Name == p.Revision || (rv.Hash != "" && rv.Hash == p.Revision) {
				found, r.Name = true, rv.Name
			}
		}
		r.Resolved = &found
		if found {
			r.Path = "/workloads/" + pathValue(p.WorkloadID) + "?tab=revisions"
		} else {
			r.Note = "OpsTide holds no record of this revision (it may have been removed, or never observed)."
		}
		out = append(out, r)
	}
	return out, nil
}

// derivationOf names the rule behind a derived Log entry, its version where one is recorded, and its inputs.
func (s *Server) derivationOf(ctx context.Context, e store.Event) (ExplorerDerivation, error) {
	dv := ExplorerDerivation{RuleID: e.Type, RuleSet: "opstide.rule", RuleVersion: "unversioned", Inputs: map[string]string{}, EvidenceRefs: []EvidenceRef{}}
	for k, v := range e.Facts {
		dv.Inputs[k] = v
	}
	if e.ReleaseID != "" {
		dv.RuleSet = "release.assessment"
		vs, err := s.db.ReleaseAssessmentVersions(ctx, []string{e.ReleaseID})
		if err != nil {
			return dv, err
		}
		if v, ok := vs[e.ReleaseID]; ok {
			dv.RuleVersion = itoa(v)
			dv.Note = "The rule-set version is the one stored with the release when it was assessed."
		}
		dv.EvidenceRefs = append(dv.EvidenceRefs, EvidenceRef{Kind: "Release", ID: e.ReleaseID, Path: "/releases/" + pathValue(e.ReleaseID)})
	}
	if dv.RuleVersion == "unversioned" {
		dv.Note = "This rule is part of OpsTide " + s.cfg.Version + " and is not versioned separately; the inputs below are what it was given."
	}
	if e.WorkloadID != "" {
		dv.EvidenceRefs = append(dv.EvidenceRefs, EvidenceRef{Kind: "Workload", ID: e.WorkloadID, Path: "/workloads/" + pathValue(e.WorkloadID)})
	}
	if e.RunID != "" {
		dv.EvidenceRefs = append(dv.EvidenceRefs, EvidenceRef{Kind: "Run", ID: e.RunID})
	}
	if e.ResourceUID != "" {
		dv.EvidenceRefs = append(dv.EvidenceRefs, EvidenceRef{Kind: e.Kind, ID: e.ResourceUID})
	}
	return dv, nil
}

// resolve says, for each relationship, whether OpsTide holds the target, and where the UI shows it.
func (s *Server) resolve(ctx context.Context, rels []Relationship) error {
	set := func(r *Relationship, ok bool, path, name string) {
		r.Resolved = &ok
		if ok {
			r.Path = path
			if r.Name == "" {
				r.Name = name
			}
		} else if r.UID != "" {
			r.Note = firstNonEmpty(r.Note, "OpsTide holds no record of this (it may have been removed by retention, or never observed).")
		}
	}
	for i := range rels {
		r := &rels[i]
		if r.UID == "" {
			continue
		}
		switch r.Kind {
		case "Workload":
			w, ok, err := s.db.WorkloadByID(ctx, r.UID)
			if err != nil {
				return err
			}
			set(r, ok, "/workloads/"+pathValue(r.UID), w.Name)
		case "Release":
			rel, ok, err := s.db.Release(ctx, r.UID)
			if err != nil {
				return err
			}
			set(r, ok, "/releases/"+pathValue(r.UID), rel.Workload+" "+rel.Revision)
		case "Operation":
			op, ok, err := s.db.Operation(ctx, r.UID)
			if err != nil {
				return err
			}
			set(r, ok, "/operations/"+pathValue(r.UID), op.Name)
		case "Run":
			run, ok, err := s.db.Run(ctx, r.UID)
			if err != nil {
				return err
			}
			path := ""
			if run.OperationID != "" {
				path = "/operations/" + pathValue(run.OperationID)
			}
			set(r, ok, path, run.JobName)
			if ok && path == "" {
				r.Note = "A Job no CronJob owns has no page of its own."
			}
		case "Node":
			n, ok, err := s.db.NodeByUID(ctx, r.UID)
			if err != nil {
				return err
			}
			set(r, ok, "/nodes/"+pathValue(r.UID), n.Name)
		case "HorizontalPodAutoscaler", "ReplicaSet":
			r.Note = firstNonEmpty(r.Note, "This kind has no page of its own; see the workload.")
		}
	}
	return nil
}

func (s *Server) currentLink(rec ExplorerRecord) *ExplorerCurrent {
	var path string
	switch rec.Resource.Kind {
	case "Deployment", "StatefulSet", "DaemonSet":
		path = "/workloads/" + pathValue(rec.Resource.UID)
	case "Node":
		// Kubernetes reports a Node's name where the UID would be; only a Node OpsTide recorded has a page.
		if rec.Refs.NodeID != "" {
			path = "/nodes/" + pathValue(rec.Refs.NodeID)
		} else if rec.Type != store.EvidenceTypeName {
			path = "/nodes/" + pathValue(rec.Resource.UID)
		}
	case "CronJob":
		path = "/operations/" + pathValue(rec.Resource.UID)
	default:
		switch {
		case rec.Refs.WorkloadID != "":
			path = "/workloads/" + pathValue(rec.Refs.WorkloadID)
		case rec.Refs.OperationID != "":
			path = "/operations/" + pathValue(rec.Refs.OperationID)
		}
	}
	if path == "" || rec.Resource.UID == "" && rec.Refs.WorkloadID == "" && rec.Refs.OperationID == "" {
		return nil
	}
	return &ExplorerCurrent{Label: "Current state — what OpsTide knows now, not the state at this record's time", Path: path}
}

// coverageAtRecord says whether OpsTide was watching what the record concerns at the record's time.
func (s *Server) coverageAtRecord(ctx context.Context, rec ExplorerRecord) (ExplorerRecordCoverage, error) {
	kinds := scopeKindsOf[rec.Group]
	if k := kindScopes[rec.Resource.Kind]; rec.Group == "config" && len(k) > 0 {
		kinds = k // a ConfigMap's record depends on the ConfigMap watch only, a Secret's on the Secret watch only
	}
	if len(kinds) == 0 {
		return ExplorerRecordCoverage{State: "not_applicable", Scopes: []string{}, Note: "This record is OpsTide's own account of its watching."}, nil
	}
	return s.coverageAt(ctx, kinds, rec.Resource.Namespace, rec.At)
}

// coverageAt says whether the scopes of these kinds were watched at the time. Cluster-wide and per-namespace scopes both count.
func (s *Server) coverageAt(ctx context.Context, kinds []string, ns string, at time.Time) (ExplorerRecordCoverage, error) {
	out := ExplorerRecordCoverage{Scopes: []string{}}
	first, err := s.db.FirstWatchedAll(ctx)
	if err != nil {
		return out, err
	}
	var watching, total int
	var earliest time.Time
	for _, k := range kinds {
		if off, note := s.configOff(k); off {
			out.State, out.Note = CovDisabled, note
			return out, nil
		}
		if k == collector.Nodes && !s.cfg.ObserveNodes {
			out.State, out.Note = CovDisabled, "Node observation is off, so nothing says what the Nodes were doing then."
			if last, ok, err := s.db.ScopeLastWatched(ctx, k, s.now().UTC()); err == nil && ok {
				out.Note += " Last observed: " + last.UTC().Format(time.RFC3339) + "."
			}
			return out, nil
		}
		total++
		out.Scopes = append(out.Scopes, k)
		ok := false
		for _, sc := range []string{k, k + "/" + ns} {
			if t, seen := first[sc]; seen && (earliest.IsZero() || t.Before(earliest)) {
				earliest = t
			}
			if sc == k+"/" && ns == "" {
				continue
			}
			w, err := s.db.WatchedThroughout(ctx, sc, at, at)
			if err != nil {
				return out, err
			}
			ok = ok || w
		}
		if ok {
			watching++
		}
	}
	switch {
	case watching == total:
		out.State, out.Note = CovWatching, "OpsTide was watching what this concerns at this time."
	case earliest.IsZero() || earliest.After(at):
		out.State, out.Note = CovBeforeFirst, "OpsTide had not yet begun watching what this concerns at this time."
	default:
		out.State, out.Note = CovGap, "OpsTide was not watching all of what this concerns at this time; related records may be missing."
	}
	return out, nil
}

// ---- state at a time ----

// State bases, from most to least certain.
const (
	StateObservedAt = "OBSERVED_AT" // OpsTide recorded a state change at exactly this time
	StateTransition = "TRANSITION"  // the latest recorded state change before this time
	StateLatest     = "LATEST_RECORD"
	StateUnknown    = "UNKNOWN"
)

// ExplorerState is what OpsTide can say about a resource at a past time. It never reconstructs a state it did not record.
type ExplorerState struct {
	Schema    int       `json:"schema"`
	UID       string    `json:"uid"`
	Kind      string    `json:"kind,omitempty"`
	At        time.Time `json:"at"`
	Basis     string    `json:"basis"`
	Statement string    `json:"statement"`
	// Transition is the latest state change recorded at or before At.
	Transition *ExplorerRecord `json:"transition"`
	// LatestRecord is the latest record of any kind about the resource at or before At.
	LatestRecord *ExplorerRecord        `json:"latestRecord"`
	Recent       []ExplorerRecord       `json:"recent"`
	Coverage     ExplorerRecordCoverage `json:"coverage"`
	// Current labels the live view as what it is: now, not then.
	Current *ExplorerCurrent `json:"current"`
}

var kindScopes = map[string][]string{
	"Deployment": {"deployments", "replicasets"}, "StatefulSet": {"statefulsets"}, "DaemonSet": {"daemonsets"}, "Node": {"nodes"},
	"HorizontalPodAutoscaler": {"hpas"}, "Pod": {"pods", "deploypods", "workloadpods"}, "Job": {"jobs"}, "CronJob": {"cronjobs"}, "ReplicaSet": {"replicasets"},
	"ConfigMap": {"configmaps"}, "Secret": {"secrets"},
}

var uidRe = regexp.MustCompile(`^[A-Za-z0-9._:/=-]{1,128}$`)

func (s *Server) explorerState(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	uid, kind, ns := q.Get("uid"), q.Get("kind"), q.Get("namespace")
	at, err := timeParam(r, "at")
	switch {
	case err != nil:
	case !uidRe.MatchString(uid):
		err = badRequest("uid is required")
	case at.IsZero():
		err = badRequest("at is required (RFC 3339)")
	case len(kind) > 64 || len(ns) > 253:
		err = badRequest("kind or namespace is too long")
	case at.After(s.now().Add(time.Minute)):
		err = badRequest("at is in the future")
	}
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	at = at.UTC()
	out := ExplorerState{Schema: Schema, UID: uid, Kind: kind, At: at, Recent: []ExplorerRecord{}}
	pg, err := s.db.Explorer(r.Context(), store.ExplorerQuery{From: at.Add(-maxExplorerSpan), To: at.Add(time.Millisecond), UID: uid, Routine: true, Limit: 100})
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	for i, row := range pg.Rows {
		rec := recordOfRow(row, false)
		if i < 10 {
			out.Recent = append(out.Recent, rec)
		}
		if out.LatestRecord == nil {
			c := rec
			out.LatestRecord = &c
		}
		if row.Event != nil && !strings.HasPrefix(row.Event.Type, "collection.") {
			// An observed change is preferred over a derived result; a derived one is used only when no observed one exists.
			if out.Transition == nil || (out.Transition.Provenance == ProvDerived && rec.Provenance != ProvDerived) {
				c := rec
				out.Transition = &c
			}
		}
		if kind == "" {
			kind = rec.Resource.Kind
			out.Kind = kind
		}
	}
	kinds := kindScopes[kind]
	if len(kinds) == 0 {
		out.Coverage = ExplorerRecordCoverage{State: "not_applicable", Scopes: []string{}, Note: "OpsTide cannot say what was being watched for a resource of this kind."}
	} else if out.Coverage, err = s.coverageAt(r.Context(), kinds, ns, at); err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	when := at.Format(time.RFC3339)
	const unknown = "Not enough recorded evidence to determine this state."
	switch {
	case out.Coverage.State == CovBeforeFirst:
		out.Basis, out.Statement = StateUnknown, unknown+" This is before OpsTide first watched it."
	case out.Coverage.State == CovDisabled:
		out.Basis, out.Statement = StateUnknown, unknown+" "+out.Coverage.Note
	case out.Transition != nil && out.Transition.At.Equal(at):
		out.Basis, out.Statement = StateObservedAt, "OpsTide recorded this change at exactly "+when+": "+out.Transition.Summary
	case out.Transition != nil:
		tail := " OpsTide recorded no later change up to " + when + "."
		if out.Coverage.State == CovGap {
			tail = " OpsTide was not watching all of this at " + when + ", so a later change may not have been recorded."
		}
		out.Basis, out.Statement = StateTransition, "The latest known change before "+when+" was recorded ("+strings.ToLower(out.Transition.Provenance)+") at "+out.Transition.At.Format(time.RFC3339)+": "+out.Transition.Summary+"."+tail
	case out.LatestRecord != nil:
		out.Basis, out.Statement = StateLatest, unknown+" The latest record before "+when+" is a Kubernetes Event, not a state change: "+out.LatestRecord.Summary+"."
	default:
		out.Basis, out.Statement = StateUnknown, unknown
	}
	if out.LatestRecord != nil {
		out.Current = s.currentLink(*out.LatestRecord)
	}
	reply(w, r, s.log, out, nil)
}

// sortRecords orders records by record time then id, the same order the store pages in.
func sortRecords(rs []ExplorerRecord, asc bool) {
	sort.SliceStable(rs, func(i, j int) bool {
		if !rs[i].At.Equal(rs[j].At) {
			return rs[i].At.Before(rs[j].At) == asc
		}
		return (rs[i].ID < rs[j].ID) == asc
	})
}

// configOff says whether a ConfigMap or Secret watch is turned off, and what that means for a record.
func (s *Server) configOff(kind string) (bool, string) {
	switch {
	case kind == collector.Secrets && !s.cfg.WatchSecretMetadata:
		return true, "Secret metadata is not watched (it is off unless asked for), so nothing says what happened to Secrets then."
	case kind == collector.ConfigMaps && !s.cfg.WatchConfigMaps:
		return true, "ConfigMaps are not watched (turned off), so nothing says what happened to them then."
	}
	return false, ""
}

// configReferrers lists, for a record about a ConfigMap or Secret, the workloads whose Pod templates name it, as OpsTide
// recorded them: a reference by name, never a claim that the change reached or affected the workload.
func (s *Server) configReferrers(ctx context.Context, rec ExplorerRecord) ([]Relationship, error) {
	k := rec.Resource.Kind
	if (k != store.KindConfigMap && k != store.KindSecret) || rec.Resource.Name == "" || rec.Type == store.EvidenceTypeName {
		return nil, nil
	}
	refs, err := s.db.ConfigReferrers(ctx, rec.Resource.Namespace, k, rec.Resource.Name, rec.At)
	if err != nil {
		return nil, err
	}
	out := []Relationship{}
	for _, x := range refs {
		how := "a volume, envFrom or env valueFrom"
		if k == store.KindSecret {
			how = "a volume, envFrom, env valueFrom or an image pull Secret"
		}
		r := Relationship{Kind: x.WorkloadKind, UID: x.WorkloadID, Name: x.WorkloadName, Classification: Reference,
			Via: "the " + x.WorkloadKind + "'s Pod template names this " + k + " (" + how + "), as OpsTide last saw it"}
		ok := true
		r.Resolved = &ok
		if x.WorkloadKind == "CronJob" {
			r.Path = "/operations/" + pathValue(x.WorkloadID)
		} else {
			r.Path = "/workloads/" + pathValue(x.WorkloadID)
		}
		notes := []string{"Named by name, so it applies to whichever " + k + " has that name. A reference does not say the change reached the workload or affected it."}
		if x.FirstSeenAt.After(rec.At) {
			notes = append(notes, "OpsTide first saw this reference at "+x.FirstSeenAt.UTC().Format(time.RFC3339)+", after this record; whether it existed then is not known.")
		}
		if !x.EndedAt.IsZero() {
			notes = append(notes, "The template stopped naming it (or the workload went) at "+x.EndedAt.UTC().Format(time.RFC3339)+".")
		}
		r.Note = strings.Join(notes, " ")
		out = append(out, r)
	}
	if len(out) == 0 {
		out = append(out, Relationship{Kind: "Workload", Classification: Unresolved,
			Via: "No Deployment, StatefulSet, DaemonSet or CronJob OpsTide recorded names this " + k + " in its Pod template (it may be read another way, or by something OpsTide does not watch)."})
	}
	return out, nil
}
