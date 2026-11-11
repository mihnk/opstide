package api

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

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
	beforeOrderBasis  = "Newest first: the change closest to the record comes first. Record time, then record id, as on the timeline."
	beforeDisclaimer  = "These changes were recorded before this record, on it or on resources related to it. Being earlier does not make them a cause: OpsTide does not say whether any of them had anything to do with it."
	beforeNoteSelf    = "the resource this record is about"
	beforeNoteNodePod = "the Node this Pod reported running on: the one Node of that name that existed at the record's time"
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
	Scope string `json:"scope"`
	Label string `json:"label"`
	State string `json:"state"` // watching | gap | unavailable | disabled | before_first_observation
	Gaps  []Span `json:"gaps"`
	Note  string `json:"note,omitempty"`
}

// BeforeCoverage says whether the window was watched, so "no changes recorded" is never read as "nothing changed".
type BeforeCoverage struct {
	State           string        `json:"state"` // watching | gap | before_first_observation
	FirstObservedAt *time.Time    `json:"firstObservedAt"`
	Scopes          []BeforeScope `json:"scopes"`
}

// ExplorerBefore is the answer of /explorer/records/{id}/before.
type ExplorerBefore struct {
	Schema      int             `json:"schema"`
	Record      ExplorerRecord  `json:"record"`
	FailureLike bool            `json:"failureLike"`
	Window      string          `json:"window"`
	From        time.Time       `json:"from"`
	To          time.Time       `json:"to"` // the record's time; the window is [from, to)
	Order       string          `json:"order"`
	OrderBasis  string          `json:"orderBasis"`
	ChangeTypes []string        `json:"changeTypes"`
	Related     []BeforeSubject `json:"related"`
	Changes     []BeforeChange  `json:"changes"`
	Truncated   bool            `json:"truncated"`
	Coverage    BeforeCoverage  `json:"coverage"`
	Empty       *ExplorerEmpty  `json:"empty,omitempty"` // no_changes_recorded | no_changes_recorded_with_gaps | before_first_observation
	Note        string          `json:"note"`
}

// beforeSubject is a related resource and the query that finds its changes. A subject without a query is listed only.
type beforeSubject struct {
	BeforeSubject
	q      *store.ExplorerQuery
	scopes []string
	refs   bool // a workload or CronJob: the ConfigMaps and Secrets its Pod template names are related too
}

// changeScopes are the watches the changes of a kind come from.
var changeScopes = map[string][]string{
	store.KindDeployment: {"deployments", "replicasets", "hpas"}, store.KindStatefulSet: {"statefulsets", "controllerrevisions"},
	store.KindDaemonSet: {"daemonsets", "controllerrevisions"}, "CronJob": {"cronjobs"}, "Node": {"nodes"}, "HorizontalPodAutoscaler": {"hpas"},
	store.KindConfigMap: {"configmaps"}, store.KindSecret: {"secrets"},
}

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

func (s *Server) before(ctx context.Context, row store.ExplorerRow, win string) (ExplorerBefore, error) {
	rec := recordOfRow(row, false)
	d, _ := parseSpan(win)
	to := rec.At
	from := to.Add(-d)
	out := ExplorerBefore{Schema: Schema, Record: rec, FailureLike: failureLike(rec), Window: win, From: from, To: to, Order: "newest_first", OrderBasis: beforeOrderBasis,
		ChangeTypes: beforeTypes, Related: []BeforeSubject{}, Changes: []BeforeChange{}, Note: beforeDisclaimer}
	subs, err := s.beforeSubjects(ctx, row, rec, from)
	if err != nil {
		return out, err
	}
	byID := map[string]int{}
	for _, sub := range subs {
		out.Related = append(out.Related, sub.BeforeSubject)
		if sub.q == nil {
			continue
		}
		q := *sub.q
		q.From, q.To, q.Types, q.Routine, q.Limit = from, to, beforeTypes, true, 500
		pg, err := s.db.Explorer(ctx, q)
		if err != nil {
			return out, err
		}
		if pg.NextCursor != "" {
			out.Truncated = true
		}
		for _, cr := range pg.Rows {
			if cr.ID == rec.ID {
				continue
			}
			c := BeforeChange{Record: recordOfRow(cr, false), Classification: sub.Classification, Via: sub.Via,
				Subject: ResourceRef{Kind: sub.Kind, Namespace: sub.Namespace, Name: sub.Name, UID: sub.UID}}
			c.SecondsBefore = int64(to.Sub(c.Record.At) / time.Second)
			if cr.Event != nil {
				c.Writer = cr.Event.Facts["fieldManager"]
			}
			if i, ok := byID[cr.ID]; ok {
				if classRank[c.Classification] < classRank[out.Changes[i].Classification] {
					out.Changes[i] = c
				}
				continue
			}
			byID[cr.ID] = len(out.Changes)
			out.Changes = append(out.Changes, c)
		}
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
	if out.Coverage, err = s.beforeCoverage(ctx, subs, from, to); err != nil {
		return out, err
	}
	if len(out.Changes) == 0 {
		switch out.Coverage.State {
		case CovBeforeFirst:
			out.Empty = &ExplorerEmpty{CovBeforeFirst, "No changes recorded, and part or all of this window is before OpsTide first observed the cluster.",
				[]string{"What happened before OpsTide began watching is not known."}}
		case CovGap:
			gaps := []string{}
			for _, sc := range out.Coverage.Scopes {
				if sc.State != CovWatching {
					gaps = append(gaps, sc.Label+" ("+sc.State+")")
				}
			}
			out.Empty = &ExplorerEmpty{"no_changes_recorded_with_gaps", "No changes recorded, but some of the watches these changes come from were not whole in this window.",
				[]string{"Not watched throughout: " + strings.Join(gaps, ", ") + ". A change there may not have been recorded; this is not proof that nothing changed."}}
		default:
			out.Empty = &ExplorerEmpty{"no_changes_recorded", "No changes recorded in this window, and OpsTide was watching all of what they come from throughout.",
				[]string{"Only what OpsTide watches can appear here (no Secret values, nothing outside Kubernetes)."}}
		}
	}
	return out, nil
}

// beforeSubjects lists the resources whose changes are looked for: the record's own resource, then what the drilldown's
// relationships name, the Node a Pod reported, and the ConfigMaps and Secrets the workloads' Pod templates name.
func (s *Server) beforeSubjects(ctx context.Context, row store.ExplorerRow, rec ExplorerRecord, from time.Time) ([]beforeSubject, error) {
	var out []beforeSubject
	seen := map[string]int{}
	add := func(key string, sub beforeSubject) {
		if i, ok := seen[key]; ok {
			if classRank[sub.Classification] < classRank[out[i].Classification] {
				out[i].Classification, out[i].Via = sub.Classification, sub.Via
			}
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
		sub := beforeSubject{BeforeSubject: BeforeSubject{Kind: "Workload", UID: id, Classification: class, Via: via}, q: &store.ExplorerQuery{WorkloadID: id}, refs: true}
		if ok {
			sub.Kind, sub.Namespace, sub.Name = w.Kind, w.Namespace, w.Name
			sub.scopes = changeScopes[w.Kind]
		}
		add("w:"+id, sub)
		return nil
	}
	operation := func(id, class, via string) error {
		op, ok, err := s.db.Operation(ctx, id)
		if err != nil {
			return err
		}
		sub := beforeSubject{BeforeSubject: BeforeSubject{Kind: "CronJob", UID: id, Classification: class, Via: via}, q: &store.ExplorerQuery{OperationID: id}, scopes: changeScopes["CronJob"], refs: true}
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
		sub := beforeSubject{BeforeSubject: BeforeSubject{Kind: "Node", UID: uid, Classification: class, Via: via}, q: &store.ExplorerQuery{Kind: "Node", UID: uid}, scopes: changeScopes["Node"]}
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
				q: &store.ExplorerQuery{UID: res.UID}, scopes: changeScopes[res.Kind]})
		}
	case store.KindConfigMap, store.KindSecret:
		if res.Name != "" {
			add("c:"+res.Namespace+"/"+res.Kind+"/"+res.Name, beforeSubject{BeforeSubject: BeforeSubject{Kind: res.Kind, Namespace: res.Namespace, Name: res.Name, UID: res.UID, Classification: Direct, Via: beforeNoteSelf},
				q: &store.ExplorerQuery{Namespace: res.Namespace, Kind: res.Kind, Name: res.Name}, scopes: changeScopes[res.Kind]})
		}
	}
	if err != nil {
		return nil, err
	}
	// What the record names, as the drilldown classifies it.
	rels := relationshipsOf(row)
	extra, err := s.podRelationships(ctx, rec, rels)
	if err != nil {
		return nil, err
	}
	for _, rl := range append(rels, extra...) {
		switch {
		case rl.Classification == Unresolved:
			add("x:"+rl.Kind+rl.Via, beforeSubject{BeforeSubject: BeforeSubject{Kind: rl.Kind, Name: rl.Name, Classification: Unresolved, Via: rl.Via}})
		case rl.UID == "":
		case rl.Kind == "Workload":
			err = workload(rl.UID, rl.Classification, rl.Via)
		case rl.Kind == "Operation":
			err = operation(rl.UID, rl.Classification, rl.Via)
		case rl.Kind == "Node":
			err = node(rl.UID, rl.Classification, rl.Via)
		case rl.Kind == "HorizontalPodAutoscaler":
			add("u:"+rl.UID, beforeSubject{BeforeSubject: BeforeSubject{Kind: rl.Kind, UID: rl.UID, Classification: rl.Classification, Via: rl.Via},
				q: &store.ExplorerQuery{UID: rl.UID}, scopes: changeScopes[rl.Kind]})
		case rl.Kind == "Release":
			r, ok, rerr := s.db.Release(ctx, rl.UID)
			if rerr != nil {
				return nil, rerr
			}
			if ok && r.WorkloadID != "" {
				err = workload(r.WorkloadID, rl.Classification, "the Deployment of the release: "+rl.Via)
			}
		}
		if err != nil {
			return nil, err
		}
	}
	// The Node a Pod reported running on: the Node of that exact name that existed at the record's time, never a later
	// or earlier Node that had the same name.
	if res.Kind == "Pod" && res.UID != "" {
		p, ok, err := s.db.PodByUID(ctx, res.UID)
		if err != nil {
			return nil, err
		}
		if ok && p.Node != "" {
			uid, err := s.db.NodeIDByNameAt(ctx, p.Node, rec.At)
			if err != nil {
				return nil, err
			}
			if uid != "" {
				if err := node(uid, SameResourceContext, beforeNoteNodePod); err != nil {
					return nil, err
				}
			} else {
				add("x:Node", beforeSubject{BeforeSubject: BeforeSubject{Kind: "Node", Name: p.Node, Classification: Unresolved,
					Via: "the Pod reported this Node, but OpsTide holds no single Node of that name at the record's time (Node observation may be off)"}})
			}
		}
	}
	// The ConfigMaps and Secrets the workloads' Pod templates named during the window.
	for i := 0; i < len(out); i++ {
		sub := out[i]
		if !sub.refs || sub.UID == "" {
			continue
		}
		refs, err := s.db.WorkloadConfigRefs(ctx, sub.UID)
		if err != nil {
			return nil, err
		}
		for _, x := range refs {
			if !x.EndedAt.IsZero() && x.EndedAt.Before(from) {
				continue
			}
			add("c:"+x.Namespace+"/"+x.RefKind+"/"+x.RefName, beforeSubject{
				BeforeSubject: BeforeSubject{Kind: x.RefKind, Namespace: x.Namespace, Name: x.RefName, Classification: Reference,
					Via: "the " + x.WorkloadKind + " " + x.WorkloadName + "'s Pod template names this " + x.RefKind + ", as OpsTide saw it; a reference, not a sign the change reached it"},
				q: &store.ExplorerQuery{Namespace: x.Namespace, Kind: x.RefKind, Name: x.RefName}, scopes: changeScopes[x.RefKind]})
		}
	}
	return out, nil
}

// beforeCoverage reads the coverage of the window for the watches the related resources' changes come from.
func (s *Server) beforeCoverage(ctx context.Context, subs []beforeSubject, from, to time.Time) (BeforeCoverage, error) {
	out := BeforeCoverage{State: CovWatching, Scopes: []BeforeScope{}}
	c, err := s.buildContext(ctx, from, to, "")
	if err != nil {
		return out, err
	}
	out.FirstObservedAt = c.FirstObservedAt
	kinds, nss := map[string]bool{}, map[string]bool{}
	for _, sub := range subs {
		for _, k := range sub.scopes {
			kinds[k] = true
		}
		if sub.Namespace != "" {
			nss[sub.Namespace] = true
		}
	}
	found := map[string]bool{}
	for _, sc := range c.Scopes {
		if !kinds[sc.Kind] || (sc.Namespace != "" && !nss[sc.Namespace]) {
			continue
		}
		found[sc.Kind] = true
		out.Scopes = append(out.Scopes, BeforeScope{Scope: sc.Scope, Label: sc.Label, State: sc.State, Gaps: sc.Gaps, Note: firstNonEmpty(sc.Problem, sc.Note)})
		if sc.State != CovWatching {
			out.State = CovGap
		}
	}
	names := make([]string, 0, len(kinds))
	for k := range kinds {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if !found[k] {
			out.Scopes = append(out.Scopes, BeforeScope{Scope: k, Label: labelOf(k), State: CovBeforeFirst, Gaps: []Span{{from, to}}, Note: "OpsTide has not watched this."})
			out.State = CovGap
		}
	}
	if c.FirstObservedAt == nil || c.FirstObservedAt.After(from) {
		out.State = CovBeforeFirst
	}
	return out, nil
}

// troubleRecordID is the newest record that says a release stalled, failed, became unavailable or had rule findings,
// so the release page can open "What changed before this" on it. "" when there is none.
func (s *Server) troubleRecordID(ctx context.Context, rel store.Release) string {
	from := rel.ObservedAt
	if from.IsZero() || (!rel.StartedAt.IsZero() && rel.StartedAt.Before(from)) {
		from = rel.StartedAt
	}
	if from.IsZero() {
		return ""
	}
	pg, err := s.db.Explorer(ctx, store.ExplorerQuery{From: from.Add(-time.Minute), To: s.now().Add(time.Minute), ReleaseID: rel.ID, ReleaseWorkload: rel.WorkloadID,
		ReleaseReplicaSet: rel.ReplicaSetUID, ReleaseFrom: from, Types: []string{"release.stalled", "release.failed", "release.unavailable", "release.health.degraded"}, Routine: true, Limit: 1})
	if err != nil || len(pg.Rows) == 0 {
		return ""
	}
	return pg.Rows[0].ID
}
