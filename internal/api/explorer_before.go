package api

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/mihnk/opstide/internal/collector"
	"github.com/mihnk/opstide/internal/store"
)

// "What changed before this?" lists, for one record, the changes OpsTide recorded in a window before it, on the
// resource the record is about and on the resources related to it by the relationships the drilldown already uses
// (the record's own, its Pod's revision, the Node the Pod reported, and the ConfigMaps and Secrets the workload's Pod
// template names). It is an ordered list of earlier records and the coverage of the window; it never says that any
// of them caused the record.

// beforeWindows are the windows offered; 24 hours is the most.
var beforeWindows = []string{"15m", "1h", "6h", "24h"}

// beforeTypes are what counts as a change here: the Explorer's "Changes only" set, plus Node condition changes.
var beforeTypes = append(append([]string{}, changeTypes...), "node.condition_changed")

// failureTypes are records that mark something going wrong; a Warning Kubernetes Event is one too. The UI offers the
// list for these; the API answers for any record.
var failureTypes = map[string]bool{
	"job.failed": true, "pod.restarted": true, "pod.oomkilled": true, "pod.evicted": true, "pod.waiting": true,
	"release.stalled": true, "release.failed": true, "release.unavailable": true, "release.health.degraded": true,
	"node.condition_changed": true,
}

func failureLike(rec ExplorerRecord) bool {
	return failureTypes[rec.Type] || (rec.Type == store.EvidenceTypeName && rec.EventType == "Warning")
}

const (
	maxBeforeChanges  = 200
	maxBeforeSubjects = 100 // related resources looked at; beyond this the answer says it was cut short
	beforeOrderBasis  = "Newest first: the change closest to the record comes first. Record time, then record id, as on the timeline."
	beforeDisclaimer  = "These changes were recorded before this record, on it or on resources related to it. Being earlier does not make them a cause: OpsTide does not say whether any of them had anything to do with it."
	beforeNoteSelf    = "the resource this record is about"
	beforeNoteNodePod = "the Node this Pod reported running on: the one Node of that name that existed at the record's time"
)

// Coverage states and empty kinds of this answer beyond the Explorer's own.
const (
	// CovUnresolved: a related resource could not be resolved (or was not looked at), so its changes were not looked
	// for; the window cannot read as watched whatever the watches did.
	CovUnresolved = "unresolved"
)

// Reasons a "before" answer gives in coverage.unresolved.
const (
	unresolvedSubject = "unresolved_subject" // a related resource OpsTide could not resolve (an unrecorded owner, a Node it does not hold)
	unresolvedNothing = "nothing_watched"    // nothing related names a watch the changes could come from
	unresolvedCapped  = "subjects_capped"    // more related resources than are looked at
)

// BeforeSubject is a resource whose changes were looked for, and how it is related to the record.
type BeforeSubject struct {
	Kind           string `json:"kind"`
	Namespace      string `json:"namespace,omitempty"`
	Name           string `json:"name,omitempty"`
	UID            string `json:"uid,omitempty"`
	Classification string `json:"classification"`
	Via            string `json:"via"`
}

// BeforeChange is one change recorded before the record.
type BeforeChange struct {
	Record        ExplorerRecord `json:"record"`
	SecondsBefore int64          `json:"secondsBefore"`
	// Writer is the field manager that wrote it, when the record names one: a client name, not a person.
	Writer         string      `json:"writer,omitempty"`
	Classification string      `json:"classification"`
	Via            string      `json:"via"`
	Subject        ResourceRef `json:"subject"` // the related resource that brought it in
}

// BeforeScope is one watch the changes depend on, over the window.
type BeforeScope struct {
	Scope     string `json:"scope"`
	Label     string `json:"label"`
	Namespace string `json:"namespace,omitempty"`
	State     string `json:"state"` // watching | gap | unavailable | disabled | before_first_observation
	Gaps      []Span `json:"gaps"`
	Note      string `json:"note,omitempty"`
}

// BeforeCoverage says whether the window was watched, so "no changes recorded" is never read as "nothing changed".
type BeforeCoverage struct {
	// State is watching only when every related resource was resolved and looked at, every watch they depend on was
	// whole, and every record of the window is still held.
	State           string     `json:"state"` // watching | gap | unresolved | outside_retention | before_first_observation
	FirstObservedAt *time.Time `json:"firstObservedAt"`
	// HeldFrom: records from before this are no longer held (the age limit, or a size budget that removed the oldest),
	// so the window up to it is not covered. Null when the whole window is held.
	HeldFrom *time.Time `json:"heldFrom"`
	HeldNote string     `json:"heldNote,omitempty"`
	// Unresolved gives why some related resources' changes were not looked for: unresolved_subject | nothing_watched |
	// subjects_capped. Empty when all were.
	Unresolved []string      `json:"unresolved"`
	Scopes     []BeforeScope `json:"scopes"`
}

// ExplorerBefore is the answer of /explorer/records/{id}/before.
type ExplorerBefore struct {
	Schema      int             `json:"schema"`
	Record      ExplorerRecord  `json:"record"`
	FailureLike bool            `json:"failureLike"`
	Window      string          `json:"window"`
	From        time.Time       `json:"from"`
	To          time.Time       `json:"to"` // the record's time; the window is [from, to], and at to only what the timeline puts before the record
	Order       string          `json:"order"`
	OrderBasis  string          `json:"orderBasis"`
	ChangeTypes []string        `json:"changeTypes"`
	Related     []BeforeSubject `json:"related"`
	// SubjectsCapped says more resources were related than are looked at (maxBeforeSubjects); the rest were not.
	SubjectsCapped bool           `json:"subjectsCapped"`
	Changes        []BeforeChange `json:"changes"`
	Truncated      bool           `json:"truncated"`
	Coverage       BeforeCoverage `json:"coverage"`
	// Empty: no_changes_recorded | no_changes_recorded_with_gaps | unresolved | outside_retention | before_first_observation
	Empty *ExplorerEmpty `json:"empty,omitempty"`
	Note  string         `json:"note"`
}

// beforeSubject is a related resource and how its changes are found: by workload, by CronJob, by UID or by name. A
// subject with none of them is listed only.
type beforeSubject struct {
	BeforeSubject
	wid, oid, uid string
	name          *store.ResourceName
	scopes        []string
	covNS         string // the namespace whose watches it depends on ("" for a cluster-scoped resource)
	refs          bool   // a workload or CronJob: the ConfigMaps and Secrets its Pod template names are related too
	// spans, for a reference only, are when a template named it (To zero: it still does); nil is the whole window.
	spans []Span
}

func (b beforeSubject) queried() bool {
	return b.wid != "" || b.oid != "" || b.uid != "" || b.name != nil
}

// changeScopes are the watches the changes of a kind come from.
var changeScopes = map[string][]string{
	store.KindDeployment: {"deployments", "replicasets", "hpas"}, store.KindStatefulSet: {"statefulsets", "controllerrevisions"},
	store.KindDaemonSet: {"daemonsets", "controllerrevisions"}, "CronJob": {"cronjobs"}, "Node": {"nodes"}, "HorizontalPodAutoscaler": {"hpas"},
	store.KindConfigMap: {"configmaps"}, store.KindSecret: {"secrets"},
}

// anyWorkloadScopes are the watches a workload's changes may come from when its kind is not known.
var anyWorkloadScopes = []string{"controllerrevisions", "daemonsets", "deployments", "hpas", "replicasets", "statefulsets"}

// classRank orders relationship classes from the closest connection; a resource reached twice keeps the closer one.
var classRank = map[string]int{Direct: 0, Ownership: 1, Reference: 2, SameResourceContext: 3, Unresolved: 4}

func (s *Server) explorerBefore(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := r.URL.Query()
	win := q.Get("window")
	if win == "" {
		win = "1h"
	}
	var err error
	for k := range q {
		if k != "window" {
			err = badRequest("unknown parameter; this takes window")
		}
	}
	if err == nil && !has(beforeWindows, win) {
		err = badRequest("window takes 15m, 1h, 6h or 24h (at most 24 hours)")
	}
	if err == nil && !idRe.MatchString(id) {
		err = badRequest("that is not a record id")
	}
	var rows []store.ExplorerRow
	if err == nil {
		rows, _, err = s.db.ExplorerRowsByID(r.Context(), []string{id})
		if err == nil && len(rows) == 0 {
			err = errNotFound
		}
	}
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	out, err := s.before(r.Context(), rows[0], win)
	reply(w, r, s.log, out, err)
}

// inSpans says whether t lies in one of the spans, ends included (an open span has no end).
func inSpans(spans []Span, t time.Time) bool {
	for _, sp := range spans {
		if !t.Before(sp.From) && (sp.To.IsZero() || !t.After(sp.To)) {
			return true
		}
	}
	return false
}

func (s *Server) before(ctx context.Context, row store.ExplorerRow, win string) (ExplorerBefore, error) {
	rec := recordOfRow(row, false)
	d, _ := parseSpan(win)
	to := rec.At
	from := to.Add(-d)
	out := ExplorerBefore{Schema: Schema, Record: rec, FailureLike: failureLike(rec), Window: win, From: from, To: to, Order: "newest_first", OrderBasis: beforeOrderBasis,
		ChangeTypes: beforeTypes, Related: []BeforeSubject{}, Changes: []BeforeChange{}, Note: beforeDisclaimer}
	subs, capped, err := s.beforeSubjects(ctx, row, rec, from, to)
	if err != nil {
		return out, err
	}
	out.SubjectsCapped = capped
	// One query for every subject: each kind of key on its own index. The window runs to the record's millisecond
	// included; at that millisecond only what the timeline orders before the record (a smaller id) is kept.
	q := store.ChangesQuery{From: from, To: to.Add(time.Millisecond), Types: beforeTypes, Limit: 500}
	byW, byO, byU, byN := map[string][]int{}, map[string][]int{}, map[string][]int{}, map[store.ResourceName][]int{}
	for i, sub := range subs {
		out.Related = append(out.Related, sub.BeforeSubject)
		switch {
		case sub.wid != "":
			if len(byW[sub.wid]) == 0 {
				q.WorkloadIDs = append(q.WorkloadIDs, sub.wid)
			}
			byW[sub.wid] = append(byW[sub.wid], i)
		case sub.oid != "":
			if len(byO[sub.oid]) == 0 {
				q.OperationIDs = append(q.OperationIDs, sub.oid)
			}
			byO[sub.oid] = append(byO[sub.oid], i)
		case sub.uid != "":
			if len(byU[sub.uid]) == 0 {
				q.UIDs = append(q.UIDs, sub.uid)
			}
			byU[sub.uid] = append(byU[sub.uid], i)
		case sub.name != nil:
			if len(byN[*sub.name]) == 0 {
				q.Names = append(q.Names, *sub.name)
			}
			byN[*sub.name] = append(byN[*sub.name], i)
		}
	}
	rows, more, err := s.db.Changes(ctx, q)
	if err != nil {
		return out, err
	}
	out.Truncated = more
	for _, cr := range rows {
		if cr.ID == rec.ID || cr.Event == nil || cr.Key.After(to) || (cr.Key.Equal(to) && cr.ID >= rec.ID) {
			continue
		}
		e := cr.Event
		var cands []int
		cands = append(cands, byW[e.WorkloadID]...)
		cands = append(cands, byO[e.OperationID]...)
		cands = append(cands, byU[e.ResourceUID]...)
		cands = append(cands, byN[store.ResourceName{Namespace: e.Namespace, Kind: e.Kind, Name: e.ResourceName}]...)
		var best *BeforeChange
		for _, i := range cands {
			sub := subs[i]
			if sub.spans != nil && !inSpans(sub.spans, cr.Key) {
				continue // a reference counts only while the template named it
			}
			c := BeforeChange{Record: recordOfRow(cr, false), Classification: sub.Classification, Via: sub.Via,
				Subject: ResourceRef{Kind: sub.Kind, Namespace: sub.Namespace, Name: sub.Name, UID: sub.UID}}
			if (sub.wid != "" || sub.oid != "") && e.ResourceUID != "" && e.ResourceUID != sub.UID {
				// Recorded against the workload or CronJob, but about another resource (an autoscaler): that resource
				// is the subject, in the same resource context, not the workload's own class.
				c.Classification = SameResourceContext
				c.Via = "recorded against the " + kindName(sub.Kind, sub.Name) + ", but about this " + e.Kind + ", not the " + sub.Kind + " itself"
				c.Subject = ResourceRef{Kind: e.Kind, Namespace: e.Namespace, Name: e.ResourceName, UID: e.ResourceUID}
			}
			if best == nil || classRank[c.Classification] < classRank[best.Classification] {
				cc := c
				best = &cc
			}
		}
		if best == nil {
			continue
		}
		best.SecondsBefore = int64(to.Sub(best.Record.At) / time.Second)
		best.Writer = e.Facts["fieldManager"]
		out.Changes = append(out.Changes, *best)
	}
	cs := out.Changes
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i].Record, cs[j].Record
		if !a.At.Equal(b.At) {
			return a.At.After(b.At)
		}
		return a.ID > b.ID
	})
	if len(cs) > maxBeforeChanges {
		out.Changes, out.Truncated = cs[:maxBeforeChanges], true
	}
	if out.Coverage, err = s.beforeCoverage(ctx, subs, capped, from, to); err != nil {
		return out, err
	}
	if len(out.Changes) == 0 {
		out.Empty = beforeEmpty(out.Coverage)
	}
	return out, nil
}

func kindName(kind, name string) string {
	if name == "" {
		return kind
	}
	return kind + " " + name
}

// beforeEmpty says why the list is empty. Every statement that applies is in the detail; the kind is the widest.
func beforeEmpty(cov BeforeCoverage) *ExplorerEmpty {
	var detail []string
	if cov.HeldFrom != nil {
		detail = append(detail, "Records from before "+cov.HeldFrom.UTC().Format(time.RFC3339)+" are no longer held ("+cov.HeldNote+"); a change then may have been removed.")
	}
	if cov.State == CovBeforeFirst {
		detail = append(detail, "What happened before OpsTide began watching is not known.")
	}
	for _, u := range cov.Unresolved {
		switch u {
		case unresolvedSubject:
			detail = append(detail, "Some related resources could not be resolved (see related), so their changes were not looked for.")
		case unresolvedNothing:
			detail = append(detail, "Nothing related to this record names a watch its changes could come from, so nothing was looked at.")
		case unresolvedCapped:
			detail = append(detail, "Only the first "+itoa(maxBeforeSubjects)+" related resources were looked at.")
		}
	}
	var gaps []string
	for _, sc := range cov.Scopes {
		if sc.State != CovWatching {
			l := sc.Label
			if sc.Namespace != "" {
				l += " in " + sc.Namespace
			}
			gaps = append(gaps, l+" ("+sc.State+")")
		}
	}
	if len(gaps) > 0 {
		detail = append(detail, "Not watched throughout: "+strings.Join(gaps, ", ")+". A change there may not have been recorded; this is not proof that nothing changed.")
	}
	switch cov.State {
	case CovOutsideRetn:
		return &ExplorerEmpty{CovOutsideRetn, "No changes recorded, but part or all of this window is older than the records OpsTide still holds.", detail}
	case CovBeforeFirst:
		return &ExplorerEmpty{CovBeforeFirst, "No changes recorded, and part or all of this window is before OpsTide first observed the cluster.", detail}
	case CovUnresolved:
		return &ExplorerEmpty{CovUnresolved, "No changes recorded, but OpsTide could not look at everything related to this record.", detail}
	case CovGap:
		return &ExplorerEmpty{"no_changes_recorded_with_gaps", "No changes recorded, but some of the watches these changes come from were not whole in this window.", detail}
	}
	return &ExplorerEmpty{"no_changes_recorded", "No changes recorded in this window, and OpsTide was watching all of what they come from throughout.",
		[]string{"Only what OpsTide watches can appear here (no Secret values, nothing outside Kubernetes)."}}
}

// beforeSubjects lists the resources whose changes are looked for: the record's own resource, then what the drilldown's
// relationships name, the Node a Pod reported, and the ConfigMaps and Secrets the workloads' Pod templates name. At
// most maxBeforeSubjects; capped says there were more.
func (s *Server) beforeSubjects(ctx context.Context, row store.ExplorerRow, rec ExplorerRecord, from, to time.Time) ([]beforeSubject, bool, error) {
	var out []beforeSubject
	capped := false
	seen := map[string]int{}
	recNS := rec.Resource.Namespace
	add := func(key string, sub beforeSubject) {
		if sub.covNS == "" && sub.Kind != "Node" {
			sub.covNS = firstNonEmpty(sub.Namespace, recNS) // owners and autoscalers are in the namespace of what they own
		}
		if i, ok := seen[key]; ok {
			o := &out[i]
			if classRank[sub.Classification] < classRank[o.Classification] {
				o.Classification, o.Via = sub.Classification, sub.Via
			}
			if o.spans == nil || sub.spans == nil {
				o.spans = nil // reached for the whole window
			} else {
				o.spans = append(o.spans, sub.spans...)
			}
			return
		}
		if len(out) >= maxBeforeSubjects {
			capped = true
			return
		}
		seen[key] = len(out)
		out = append(out, sub)
	}
	workload := func(id, class, via string) error {
		w, ok, err := s.db.WorkloadByID(ctx, id)
		if err != nil {
			return err
		}
		// Not known: any workload kind's watches, so an unknown kind never reads as depending on none.
		sub := beforeSubject{BeforeSubject: BeforeSubject{Kind: "Workload", UID: id, Classification: class, Via: via}, wid: id, scopes: anyWorkloadScopes, refs: true}
		if ok {
			sub.Kind, sub.Namespace, sub.Name = w.Kind, w.Namespace, w.Name
			if sc, known := changeScopes[w.Kind]; known {
				sub.scopes = sc
			}
		}
		add("w:"+id, sub)
		return nil
	}
	operation := func(id, class, via string) error {
		op, ok, err := s.db.Operation(ctx, id)
		if err != nil {
			return err
		}
		sub := beforeSubject{BeforeSubject: BeforeSubject{Kind: "CronJob", UID: id, Classification: class, Via: via}, oid: id, scopes: changeScopes["CronJob"], refs: true}
		if ok {
			sub.Namespace, sub.Name = op.Namespace, op.Name
		}
		add("o:"+id, sub)
		return nil
	}
	node := func(uid, class, via string) error {
		n, ok, err := s.db.NodeByUID(ctx, uid)
		if err != nil {
			return err
		}
		sub := beforeSubject{BeforeSubject: BeforeSubject{Kind: "Node", UID: uid, Classification: class, Via: via}, uid: uid, scopes: changeScopes["Node"]}
		if ok {
			sub.Name = n.Name
		}
		add("n:"+uid, sub)
		return nil
	}
	res := rec.Resource
	var err error
	// The record's own resource.
	switch res.Kind {
	case store.KindDeployment, store.KindStatefulSet, store.KindDaemonSet:
		if res.UID != "" {
			err = workload(res.UID, Direct, beforeNoteSelf)
		}
	case "CronJob":
		if res.UID != "" {
			err = operation(res.UID, Direct, beforeNoteSelf)
		}
	case "Node":
		if res.UID != "" && rec.Type != store.EvidenceTypeName { // an Event names a Node by name; its relationship below says which
			err = node(res.UID, Direct, beforeNoteSelf)
		}
	case "HorizontalPodAutoscaler":
		if res.UID != "" {
			add("u:"+res.UID, beforeSubject{BeforeSubject: BeforeSubject{Kind: res.Kind, Namespace: res.Namespace, Name: res.Name, UID: res.UID, Classification: Direct, Via: beforeNoteSelf},
				uid: res.UID, scopes: changeScopes[res.Kind]})
		}
	case store.KindConfigMap, store.KindSecret:
		if res.Name != "" {
			add("c:"+res.Namespace+"/"+res.Kind+"/"+res.Name, beforeSubject{BeforeSubject: BeforeSubject{Kind: res.Kind, Namespace: res.Namespace, Name: res.Name, UID: res.UID, Classification: Direct, Via: beforeNoteSelf},
				name: &store.ResourceName{Namespace: res.Namespace, Kind: res.Kind, Name: res.Name}, scopes: changeScopes[res.Kind]})
		}
	}
	if err != nil {
		return nil, false, err
	}
	// What the record names, as the drilldown classifies it.
	rels := relationshipsOf(row)
	extra, err := s.podRelationships(ctx, rec, rels)
	if err != nil {
		return nil, false, err
	}
	for _, rl := range append(rels, extra...) {
		switch {
		case rl.Classification == Unresolved:
			// Its watches still count: an unresolved Node depends on the Node watch, and the answer cannot read as
			// watched while it is unresolved.
			add("x:"+rl.Kind+rl.Via, beforeSubject{BeforeSubject: BeforeSubject{Kind: rl.Kind, Name: rl.Name, Classification: Unresolved, Via: rl.Via}, scopes: changeScopes[rl.Kind]})
		case rl.UID == "":
		case rl.Kind == "Workload":
			err = workload(rl.UID, rl.Classification, rl.Via)
		case rl.Kind == "Operation":
			err = operation(rl.UID, rl.Classification, rl.Via)
		case rl.Kind == "Node":
			err = node(rl.UID, rl.Classification, rl.Via)
		case rl.Kind == "HorizontalPodAutoscaler":
			add("u:"+rl.UID, beforeSubject{BeforeSubject: BeforeSubject{Kind: rl.Kind, UID: rl.UID, Classification: rl.Classification, Via: rl.Via},
				uid: rl.UID, scopes: changeScopes[rl.Kind]})
		case rl.Kind == "Release":
			r, ok, rerr := s.db.Release(ctx, rl.UID)
			if rerr != nil {
				return nil, false, rerr
			}
			if ok && r.WorkloadID != "" {
				err = workload(r.WorkloadID, rl.Classification, "the Deployment of the release: "+rl.Via)
			}
		}
		if err != nil {
			return nil, false, err
		}
	}
	// The Node a Pod reported running on: the Node of that exact name that existed at the record's time, never a later
	// or earlier Node that had the same name.
	if res.Kind == "Pod" && res.UID != "" {
		p, ok, err := s.db.PodByUID(ctx, res.UID)
		if err != nil {
			return nil, false, err
		}
		if ok && p.Node != "" {
			uid, err := s.db.NodeIDByNameAt(ctx, p.Node, rec.At)
			if err != nil {
				return nil, false, err
			}
			if uid != "" {
				if err := node(uid, SameResourceContext, beforeNoteNodePod); err != nil {
					return nil, false, err
				}
			} else {
				add("x:Node", beforeSubject{BeforeSubject: BeforeSubject{Kind: "Node", Name: p.Node, Classification: Unresolved,
					Via: "the Pod reported this Node, but OpsTide holds no single Node of that name at the record's time (Node observation may be off)"}, scopes: changeScopes["Node"]})
			}
		}
	}
	// The ConfigMaps and Secrets the workloads' Pod templates named during the window, each only while it was named.
	var owners []string
	for _, sub := range out {
		if sub.refs && sub.UID != "" {
			owners = append(owners, sub.UID)
		}
	}
	refs, err := s.db.ConfigRefsOf(ctx, owners)
	if err != nil {
		return nil, false, err
	}
	for _, x := range refs {
		if (!x.EndedAt.IsZero() && x.EndedAt.Before(from)) || !x.FirstSeenAt.Before(to) {
			continue // named only before the window, or only from the record's time on
		}
		add("c:"+x.Namespace+"/"+x.RefKind+"/"+x.RefName, beforeSubject{
			BeforeSubject: BeforeSubject{Kind: x.RefKind, Namespace: x.Namespace, Name: x.RefName, Classification: Reference,
				Via: "the " + x.WorkloadKind + " " + x.WorkloadName + "'s Pod template names this " + x.RefKind + ", as OpsTide saw it; a reference, not a sign the change reached it"},
			name: &store.ResourceName{Namespace: x.Namespace, Kind: x.RefKind, Name: x.RefName}, scopes: changeScopes[x.RefKind], spans: []Span{{x.FirstSeenAt, x.EndedAt}}})
	}
	return out, capped, nil
}

// beforeRetentionSources are the retention sources (log, routine) the change types are kept under.
var beforeRetentionSources = func() map[string]bool {
	out := map[string]bool{}
	for _, t := range beforeTypes {
		out[store.RetentionSourceOf(t)] = true
	}
	return out
}()

// beforeCoverage reads the coverage of the window for the watches the related resources' changes come from, what of
// the window is still held, and whether every related resource was looked at.
func (s *Server) beforeCoverage(ctx context.Context, subs []beforeSubject, capped bool, from, to time.Time) (BeforeCoverage, error) {
	out := BeforeCoverage{State: CovWatching, Scopes: []BeforeScope{}, Unresolved: []string{}}
	c, err := s.buildContext(ctx, from, to, "")
	if err != nil {
		return out, err
	}
	out.FirstObservedAt = c.FirstObservedAt
	// The watches needed, by scope name as the collector names them: kind, or kind/namespace when only some namespaces
	// are watched (Nodes are cluster-wide).
	type need struct{ kind, ns string }
	needs := map[string]need{}
	addNeed := func(kind, ns string) {
		if len(s.cfg.Namespaces) == 0 || kind == "nodes" {
			needs[kind] = need{kind, ""}
			return
		}
		if ns == "" { // not known which namespace: every one watched
			for _, n := range s.cfg.Namespaces {
				needs[kind+"/"+n] = need{kind, n}
			}
			return
		}
		needs[kind+"/"+ns] = need{kind, ns}
	}
	unresolved, anyScopes := false, false
	for _, sub := range subs {
		for _, k := range sub.scopes {
			addNeed(k, sub.covNS)
			anyScopes = true
		}
		unresolved = unresolved || sub.Classification == Unresolved || (!sub.queried() && len(sub.scopes) > 0)
	}
	if unresolved {
		out.Unresolved = append(out.Unresolved, unresolvedSubject)
	}
	if !anyScopes {
		out.Unresolved = append(out.Unresolved, unresolvedNothing)
	}
	if capped {
		out.Unresolved = append(out.Unresolved, unresolvedCapped)
	}
	byScope := map[string]ExplorerScope{}
	for _, sc := range c.Scopes {
		byScope[sc.Scope] = sc
	}
	keys := make([]string, 0, len(needs))
	for k := range needs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	whole := []Span{{from, to}}
	for _, k := range keys {
		n := needs[k]
		v := BeforeScope{Scope: k, Label: labelOf(n.kind), Namespace: n.ns, Gaps: whole}
		sc, found := byScope[k]
		switch off, note := s.watchOff(n.kind); {
		case off:
			v.State, v.Note = CovDisabled, note
		case found:
			v.State, v.Gaps, v.Note = sc.State, sc.Gaps, firstNonEmpty(sc.Problem, sc.Note)
		case n.ns != "" && !has(s.cfg.Namespaces, n.ns):
			v.State, v.Note = CovDisabled, "OpsTide does not watch this namespace."
		default:
			v.State, v.Note = CovBeforeFirst, "OpsTide has not watched this."
		}
		out.Scopes = append(out.Scopes, v)
		if v.State != CovWatching {
			out.State = CovGap
		}
	}
	if len(out.Unresolved) > 0 {
		out.State = CovUnresolved
	}
	if c.FirstObservedAt == nil || c.FirstObservedAt.After(from) {
		out.State = CovBeforeFirst
	}
	// What of the window is still held: nothing before the age limit, and, for a source a size budget has cut, nothing
	// before the oldest record it still holds (budgets remove the oldest first). Coverage intervals outlive both.
	var held time.Time
	if b := c.Retention.BoundaryAt; b != nil && b.After(from) {
		held, out.HeldNote = *b, "the age limit of "+itoa(c.Retention.Days)+" days"
	}
	for _, rs := range c.Retention.Sources {
		if !beforeRetentionSources[rs.Name] || rs.CappedAt == nil {
			continue
		}
		since := to // a cut source holding nothing holds nothing of the window
		if rs.RetainedSince != nil && rs.RetainedSince.Before(to) {
			since = *rs.RetainedSince
		}
		if since.After(from) && since.After(held) {
			held, out.HeldNote = since, "a size budget removed the oldest records of this kind: "+rs.Label
		}
	}
	if !held.IsZero() {
		if held.After(to) {
			held = to
		}
		out.HeldFrom = &held
		out.State = CovOutsideRetn
	}
	return out, nil
}

// watchOff says whether a watch is turned off in the configuration, and what that means.
func (s *Server) watchOff(kind string) (bool, string) {
	if kind == collector.Nodes && !s.cfg.ObserveNodes {
		return true, "Node observation is off, so nothing says what happened to Nodes then."
	}
	return s.configOff(kind)
}

// troubleRecord is the newest record that says a release stalled, failed, became unavailable or had rule findings,
// so the release page can open "What changed before this" on it, with its record time so the Explorer's range holds
// it. "" when there is none, or when it cannot be read (logged).
func (s *Server) troubleRecord(ctx context.Context, rel store.Release) (string, time.Time) {
	from := rel.ObservedAt
	if from.IsZero() || (!rel.StartedAt.IsZero() && rel.StartedAt.Before(from)) {
		from = rel.StartedAt
	}
	if from.IsZero() {
		return "", time.Time{}
	}
	pg, err := s.db.Explorer(ctx, store.ExplorerQuery{From: from.Add(-time.Minute), To: s.now().Add(time.Minute), ReleaseID: rel.ID, ReleaseWorkload: rel.WorkloadID,
		ReleaseReplicaSet: rel.ReplicaSetUID, ReleaseFrom: from, Types: []string{"release.stalled", "release.failed", "release.unavailable", "release.health.degraded"}, Routine: true, Limit: 1})
	if err != nil {
		s.log.Warn("could not find the release's trouble record", "release", rel.ID, "err", err)
		return "", time.Time{}
	}
	if len(pg.Rows) == 0 {
		return "", time.Time{}
	}
	return pg.Rows[0].ID, pg.Rows[0].Key
}
