package api

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mihnk/opstide/internal/store"
)

// The Explorer reads what OpsTide recorded, as one timeline. It records evidence; it does not interpret it: no record
// is called a cause of another, nothing is scored, and a relationship says only how two records are known to be
// connected (DIRECT, OWNERSHIP, SAME_RESOURCE_CONTEXT) or that they are not (UNRESOLVED).

// ExplorerOrderBasis says what the timeline is ordered by, so a reader (or an agent) does not have to guess.
const ExplorerOrderBasis = "Record time, then record id. Record time is when the source says the thing happened (for a Kubernetes Event, its first occurrence), " +
	"otherwise when OpsTide first observed it. It never changes once recorded, so paging neither skips nor repeats a record."

// Provenance classes of a record.
const (
	ProvObserved = "OBSERVED" // reported by Kubernetes and recorded as reported
	ProvDerived  = "DERIVED"  // a documented rule of OpsTide applied to recorded observations
	ProvUnknown  = "UNKNOWN"  // OpsTide records that it does not know
)

// ExplorerRefs are the identifiers a record carries for the records and resources around it. Only what the record
// itself names is here; nothing is inferred.
type ExplorerRefs struct {
	WorkloadID    string `json:"workloadId,omitempty"`
	ReleaseID     string `json:"releaseId,omitempty"`
	RunID         string `json:"runId,omitempty"`
	OperationID   string `json:"operationId,omitempty"`
	NodeID        string `json:"nodeId,omitempty"`
	ReplicaSetUID string `json:"replicaSetUid,omitempty"`
	HPAUID        string `json:"hpaUid,omitempty"`
}

// ExplorerRecord is one record on the timeline: a Log entry or a Kubernetes Event, as stored.
type ExplorerRecord struct {
	ID         string      `json:"id"`   // stable: e.<seq> (Log entry) or k.<id> (Kubernetes Event)
	Type       string      `json:"type"` // the Log entry type, or kubernetes.event
	Group      string      `json:"group"`
	Source     string      `json:"source"`
	Provenance string      `json:"provenance"`
	Routine    bool        `json:"routine,omitempty"`
	Severity   string      `json:"severity,omitempty"`
	Summary    string      `json:"summary"`
	Reason     string      `json:"reason,omitempty"`
	EventType  string      `json:"eventType,omitempty"` // Normal | Warning, for Kubernetes Events
	Resource   ResourceRef `json:"resource"`
	At         time.Time   `json:"at"` // the record time the timeline is ordered by
	// OccurredAt is the source's own time for it (a Kubernetes Event's first occurrence); null when the source gave none.
	OccurredAt *time.Time `json:"occurredAt"`
	// LastOccurredAt is when a repeating Kubernetes Event last occurred, per the source.
	LastOccurredAt *time.Time   `json:"lastOccurredAt,omitempty"`
	ObservedAt     time.Time    `json:"observedAt"` // when OpsTide first saw it
	Count          *Count       `json:"count,omitempty"`
	Refs           ExplorerRefs `json:"refs"`
	// Message is Kubernetes' own text of an Event. Lists and exports leave it out unless asked; the drilldown shows it.
	Message          *string `json:"message,omitempty"`
	MessageTruncated bool    `json:"messageTruncated,omitempty"`
}

func provenanceOf(c store.Confidence) string {
	switch c {
	case store.Derived:
		return ProvDerived
	case store.Unknown:
		return ProvUnknown
	}
	return ProvObserved
}

func groupOf(t string) string {
	if g := store.GroupOfType(t); g != "" {
		return g
	}
	return "other"
}

func recordOfEvent(e store.Event, key time.Time) ExplorerRecord {
	return ExplorerRecord{
		ID: store.EventRecordID(e.Seq), Type: e.Type, Group: groupOf(e.Type), Source: store.SourceOfEvent(e), Provenance: provenanceOf(e.Confidence),
		Routine: store.IsRoutineType(e.Type), Severity: e.Severity, Summary: e.Summary, Reason: e.Reason,
		Resource: ResourceRef{Kind: e.Kind, Namespace: e.Namespace, Name: e.ResourceName, UID: e.ResourceUID},
		At:       key, OccurredAt: tp(e.OccurredAt), ObservedAt: e.ObservedAt,
		Refs: ExplorerRefs{WorkloadID: e.WorkloadID, ReleaseID: e.ReleaseID, RunID: e.RunID, OperationID: e.OperationID},
	}
}

func recordOfEvidence(e store.Evidence, key time.Time, message bool) ExplorerRecord {
	r := ExplorerRecord{
		ID: store.EvidenceRecordID(e.ID), Type: store.EvidenceTypeName, Group: store.ExplorerGroupEvents, Source: "kubernetes." + e.Source, Provenance: ProvObserved,
		Routine: e.EventType == "Normal", Reason: e.Reason, EventType: e.EventType,
		Summary:  strings.TrimSpace(e.Reason + " — " + e.Regarding.Kind + " " + e.Regarding.Name),
		Resource: ResourceRef{Kind: e.Regarding.Kind, Namespace: e.Namespace, Name: e.Regarding.Name, UID: e.Regarding.UID},
		At:       key, OccurredAt: tp(e.FirstAt), LastOccurredAt: tp(e.LastAt), ObservedAt: e.ObservedAt,
		Refs: ExplorerRefs{WorkloadID: e.WorkloadID, RunID: e.RunID, OperationID: e.OperationID, NodeID: e.NodeID, ReplicaSetUID: e.ReplicaSetUID, HPAUID: e.HPAUID},
	}
	if e.EventType == "Warning" {
		r.Severity = "warning"
	}
	if e.Count > 0 {
		r.Count = &Count{Value: e.Count, Basis: e.CountBasis}
	}
	if message {
		m := e.Message
		r.Message, r.MessageTruncated = &m, e.MessageTruncated
	}
	return r
}

func recordOfRow(row store.ExplorerRow, message bool) ExplorerRecord {
	if row.Evidence != nil {
		return recordOfEvidence(*row.Evidence, row.Key, message)
	}
	return recordOfEvent(*row.Event, row.Key)
}

// ---- parameters ----

const (
	maxExplorerSpan = 400 * 24 * time.Hour
	maxExportRows   = 5000
	maxSelectedIDs  = 1000
)

// quickRanges are the ranges the Explorer offers by name.
var quickRanges = []string{"15m", "1h", "6h", "24h", "7d"}

var (
	typeRe   = regexp.MustCompile(`^[a-z0-9_.]{1,64}$`)
	cursorRe = regexp.MustCompile(`^[0-9]{1,16}\.[ek]\.[A-Za-z0-9._:/=-]{1,200}$`)
	idRe     = regexp.MustCompile(`^(e\.[0-9]{1,12}|k\.[A-Za-z0-9._:/=-]{1,200})$`)
)

var explorerSources = []string{store.SourceKubernetesAPI, store.SourceOpsTideRule, store.SourceCoverage, "kubernetes.events.k8s.io/v1", "kubernetes.core/v1"}

// explorerRange reads the time range: range=15m|1h|6h|24h|7d, or from and to; 24h when neither. Always bounded.
func (s *Server) explorerRange(r *http.Request) (from, to time.Time, label string, err error) {
	q := r.URL.Query()
	now := s.now().UTC().Truncate(time.Millisecond)
	if rg := q.Get("range"); rg != "" {
		ok := false
		for _, k := range quickRanges {
			ok = ok || k == rg
		}
		if !ok {
			return from, to, "", badRequest("range takes " + strings.Join(quickRanges, ", ") + "; or send from and to")
		}
		d, _ := parseSpan(rg)
		return now.Add(-d), now, rg, nil
	}
	if from, err = timeParam(r, "from"); err != nil {
		return
	}
	if to, err = timeParam(r, "to"); err != nil {
		return
	}
	switch {
	case from.IsZero() && to.IsZero():
		return now.Add(-24 * time.Hour), now, "24h", nil
	case from.IsZero():
		return from, to, "", badRequest("to needs from")
	case to.IsZero():
		to = now
	}
	from, to = from.UTC(), to.UTC()
	if !to.After(from) {
		return from, to, "", badRequest("to must be after from")
	}
	if to.Sub(from) > maxExplorerSpan {
		return from, to, "", badRequest("the range is longer than 400 days")
	}
	return from, to, "custom", nil
}

func tooLong(name, v string, n int) error {
	if len(v) > n {
		return badRequest(name + " is too long")
	}
	return nil
}

// explorerFilters is the validated query, kept beside the store query so it can be echoed in an export.
type explorerFilters struct {
	Range      string   `json:"range"`
	Namespace  string   `json:"namespace,omitempty"`
	Kind       string   `json:"kind,omitempty"`
	Name       string   `json:"name,omitempty"`
	UID        string   `json:"uid,omitempty"`
	Workload   string   `json:"workload,omitempty"`
	Release    string   `json:"release,omitempty"`
	Run        string   `json:"run,omitempty"`
	Operation  string   `json:"operation,omitempty"`
	Node       string   `json:"node,omitempty"`
	Groups     []string `json:"groups,omitempty"`
	Types      []string `json:"types,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	Sources    []string `json:"sources,omitempty"`
	Provenance []string `json:"provenance,omitempty"`
	Text       string   `json:"text,omitempty"`
	Routine    bool     `json:"includeRoutine"`
}

// parseExplorer reads and validates every filter. A name that is not understood is an error, not ignored.
func (s *Server) parseExplorer(r *http.Request) (store.ExplorerQuery, explorerFilters, error) {
	var q store.ExplorerQuery
	var f explorerFilters
	var err error
	if q.From, q.To, f.Range, err = s.explorerRange(r); err != nil {
		return q, f, err
	}
	v := r.URL.Query()
	known := map[string]bool{"range": true, "from": true, "to": true, "namespace": true, "kind": true, "name": true, "uid": true, "workload": true, "release": true, "run": true,
		"operation": true, "node": true, "group": true, "type": true, "reason": true, "source": true, "provenance": true, "q": true, "routine": true, "cursor": true, "limit": true,
		"order": true, "messages": true, "tz": true}
	for k := range v {
		if !known[k] {
			return q, f, badRequest("unknown parameter " + fmt.Sprintf("%.40q", k))
		}
	}
	for _, c := range []struct {
		name string
		dst  *string
		max  int
	}{{"namespace", &f.Namespace, 253}, {"kind", &f.Kind, 64}, {"name", &f.Name, 253}, {"uid", &f.UID, 128}, {"workload", &f.Workload, 128}, {"release", &f.Release, 128},
		{"run", &f.Run, 128}, {"operation", &f.Operation, 128}, {"node", &f.Node, 128}, {"reason", &f.Reason, 128}, {"q", &f.Text, 200}} {
		*c.dst = strings.TrimSpace(v.Get(c.name))
		if err = tooLong(c.name, *c.dst, c.max); err != nil {
			return q, f, err
		}
	}
	valid := map[string]bool{}
	for _, g := range store.ExplorerGroups() {
		valid[g[0]] = true
	}
	valid["other"] = false
	for _, g := range splitList(v.Get("group")) {
		if !valid[g] {
			return q, f, badRequest("unknown group " + fmt.Sprintf("%.40q", g))
		}
		f.Groups = append(f.Groups, g)
	}
	if ts := splitList(v.Get("type")); len(ts) > 25 {
		return q, f, badRequest("too many types")
	} else {
		for _, t := range ts {
			if !typeRe.MatchString(t) {
				return q, f, badRequest("a type is lower-case letters, digits, . and _")
			}
		}
		f.Types = ts
	}
	for _, src := range splitList(v.Get("source")) {
		ok := false
		for _, k := range explorerSources {
			ok = ok || k == src
		}
		if !ok {
			return q, f, badRequest("source takes " + strings.Join(explorerSources, ", "))
		}
		f.Sources = append(f.Sources, src)
	}
	for _, p := range splitList(strings.ToLower(v.Get("provenance"))) {
		if p != "observed" && p != "derived" && p != "unknown" {
			return q, f, badRequest("provenance takes observed, derived or unknown")
		}
		f.Provenance = append(f.Provenance, p)
	}
	f.Routine = v.Get("routine") == "1" || v.Get("routine") == "true"
	q.Namespace, q.Kind, q.Name, q.UID, q.WorkloadID, q.RunID, q.OperationID, q.NodeID = f.Namespace, f.Kind, f.Name, f.UID, f.Workload, f.Run, f.Operation, f.Node
	q.Groups, q.Types, q.Reason, q.Sources, q.Provenance, q.Text, q.Routine = f.Groups, f.Types, f.Reason, f.Sources, f.Provenance, f.Text, f.Routine
	if c := v.Get("cursor"); c != "" {
		if !cursorRe.MatchString(c) {
			return q, f, badRequest("cursor is not one this API gave")
		}
		q.Cursor = c
	}
	if q.Limit, err = intParam(r, "limit", 100, 500); err != nil {
		return q, f, err
	}
	switch v.Get("order") {
	case "", "desc":
	case "asc":
		q.Asc = true
	default:
		return q, f, badRequest("order takes asc or desc")
	}
	if f.Release != "" {
		rel, ok, err := s.db.Release(r.Context(), f.Release)
		if err != nil {
			return q, f, err
		}
		if !ok {
			return q, f, badRequest("that release is not recorded")
		}
		rf, rt, err := s.releaseWindow(r, rel)
		if err != nil {
			return q, f, err
		}
		q.ReleaseID, q.ReleaseReplicaSet, q.ReleaseWorkload, q.ReleaseFrom, q.ReleaseTo = rel.ID, rel.ReplicaSetUID, rel.WorkloadID, rf, rt
	}
	return q, f, nil
}

// ---- the timeline ----

// ExplorerList is a page of the timeline.
type ExplorerList struct {
	Schema     int              `json:"schema"`
	From       time.Time        `json:"from"`
	To         time.Time        `json:"to"`
	Range      string           `json:"range"`
	Order      string           `json:"order"`
	OrderBasis string           `json:"orderBasis"`
	Records    []ExplorerRecord `json:"records"`
	NextCursor string           `json:"nextCursor,omitempty"`
	// SuppressedRoutine counts routine records (Pod created/ready/terminated, Normal Kubernetes Events) left out;
	// routine=1 shows them. Nothing else is hidden.
	SuppressedRoutine int            `json:"suppressedRoutine"`
	Empty             *ExplorerEmpty `json:"empty,omitempty"`
	Notices           []string       `json:"notices"`
	Provenance        Provenance     `json:"provenance"`
}

func (s *Server) explorer(w http.ResponseWriter, r *http.Request) {
	q, f, err := s.parseExplorer(r)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	pg, err := s.db.Explorer(r.Context(), q)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	order := "desc"
	if q.Asc {
		order = "asc"
	}
	out := ExplorerList{Schema: Schema, From: q.From, To: q.To, Range: f.Range, Order: order, OrderBasis: ExplorerOrderBasis, Records: make([]ExplorerRecord, 0, len(pg.Rows)),
		NextCursor: pg.NextCursor, SuppressedRoutine: pg.SuppressedRoutine, Notices: []string{}, Provenance: Provenance{EventsAPI: s.eventsSource(), Notes: []string{}}}
	for _, row := range pg.Rows {
		out.Records = append(out.Records, recordOfRow(row, false))
	}
	if q.Cursor == "" {
		ctx, err := s.buildContext(r.Context(), q.From, q.To, q.Namespace)
		if err != nil {
			reply(w, r, s.log, nil, err)
			return
		}
		gs := groupsOfFilters(f)
		out.Notices = ctx.Notices(gs)
		if len(out.Records) == 0 {
			out.Empty = ctx.EmptyState(gs, pg.SuppressedRoutine)
		}
	}
	reply(w, r, s.log, out, nil)
}

// groupsOfFilters are the record groups the filters can match: the groups named, else those of the types and sources
// named, else none (all). It decides which watches an empty result depends on.
func groupsOfFilters(f explorerFilters) []string {
	if len(f.Groups) > 0 {
		return f.Groups
	}
	set := map[string]bool{}
	for _, t := range f.Types {
		if t == store.EvidenceTypeName {
			set[store.ExplorerGroupEvents] = true
		} else if g := store.GroupOfType(t); g != "" {
			set[g] = true
		}
	}
	for _, s := range f.Sources {
		if strings.HasPrefix(s, "kubernetes.events") || s == "kubernetes.core/v1" {
			set[store.ExplorerGroupEvents] = true
		}
	}
	out := make([]string, 0, len(set))
	for g := range set {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// ---- filters ----

// ExplorerFilters is what the filter bar can offer for a range, with how many records each choice would match.
type ExplorerFilters struct {
	Schema      int            `json:"schema"`
	From        time.Time      `json:"from"`
	To          time.Time      `json:"to"`
	QuickRanges []string       `json:"quickRanges"`
	Groups      []FilterChoice `json:"groups"`
	Kinds       []FilterChoice `json:"kinds"`
	Provenance  []FilterChoice `json:"provenance"`
	Sources     []string       `json:"sources"`
	Reasons     []FilterChoice `json:"reasons"`
	Namespaces  []FilterChoice `json:"namespaces"`
	Total       int            `json:"total"`
	// Counted says the numbers are for the range and the other filters, routine records included.
	Counted string `json:"counted"`
}

// FilterChoice is a value and the number of records that carry it.
type FilterChoice struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
	Count int    `json:"count"`
}

func (s *Server) explorerFilters(w http.ResponseWriter, r *http.Request) {
	q, _, err := s.parseExplorer(r)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	q.Routine, q.Cursor = true, ""
	fc, err := s.db.ExplorerFacets(r.Context(), q, 50)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	out := ExplorerFilters{Schema: Schema, From: q.From, To: q.To, QuickRanges: quickRanges, Sources: explorerSources, Total: fc.Total,
		Counted: "records in the range matching the other filters, routine records included", Groups: []FilterChoice{}, Kinds: []FilterChoice{}, Provenance: []FilterChoice{}, Reasons: []FilterChoice{}, Namespaces: []FilterChoice{}}
	for _, g := range store.ExplorerGroups() {
		out.Groups = append(out.Groups, FilterChoice{Value: g[0], Label: g[1], Count: fc.Groups[g[0]]})
	}
	named := func(m map[string]int, upper bool) []FilterChoice {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		res := []FilterChoice{}
		for _, k := range keys {
			v := k
			if upper {
				v = strings.ToLower(k)
			}
			res = append(res, FilterChoice{Value: v, Label: k, Count: m[k]})
		}
		return res
	}
	out.Kinds = named(fc.Kinds, false)
	out.Provenance = named(fc.Provenance, true)
	for _, c := range fc.Reasons {
		out.Reasons = append(out.Reasons, FilterChoice{Value: c.Name, Count: c.Count})
	}
	for _, c := range fc.Namespaces {
		out.Namespaces = append(out.Namespaces, FilterChoice{Value: c.Name, Count: c.Count})
	}
	reply(w, r, s.log, out, nil)
}

// pathValue is url.PathEscape for the single segments the UI routes use.
func pathValue(s string) string { return url.PathEscape(s) }
