package api

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mihnk/opstide/internal/collector"
)

// Coverage is what lets a reader tell "nothing happened" from "OpsTide was not looking". Every state here is a
// statement about OpsTide's own watching, never about the cluster.

// Coverage states of a scope.
const (
	CovWatching     = "watching"                 // watched throughout the part of the range it could be
	CovGap          = "gap"                      // watched, but not throughout: records from the gaps may be missing
	CovUnavailable  = "unavailable"              // cannot be read right now (permissions or API)
	CovDisabled     = "disabled"                 // observation is turned off
	CovBeforeFirst  = "before_first_observation" // the range ends before OpsTide first watched it
	CovOutsideRetn  = "outside_retention"        // the range is older than the history OpsTide keeps
	CovNoMatches    = "no_matches"
	CovSourceUnavil = "source_unavailable"
)

// Band is a stretch of the range classified as a whole.
type Band struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	Kind string    `json:"kind"` // outside_retention | before_first_observation | observable
}

// ExplorerScope is one watched kind (or kind in a namespace) over the range.
type ExplorerScope struct {
	Scope     string `json:"scope"`
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Namespace string `json:"namespace,omitempty"`
	State     string `json:"state"`
	Watching  []Span `json:"watching"`
	Gaps      []Span `json:"gaps"`
	// FirstWatchedAt is when OpsTide first watched it, from the whole record; null if never.
	FirstWatchedAt *time.Time `json:"firstWatchedAt"`
	// LastObservedAt is when it was last watched (for a scope that is not watched now).
	LastObservedAt *time.Time `json:"lastObservedAt"`
	Problem        string     `json:"problem,omitempty"`
	Note           string     `json:"note,omitempty"`
}

// ExplorerSource is a source of records and whether it can be read.
type ExplorerSource struct {
	Name  string `json:"name"`
	State string `json:"state"` // available | unavailable | disabled | not_in_use
	Note  string `json:"note,omitempty"`
}

// ExplorerRetention is how much history is kept.
type ExplorerRetention struct {
	Days       int        `json:"days"`
	MaxEntries int        `json:"maxEntries"`
	BoundaryAt *time.Time `json:"boundaryAt"` // records before this are pruned; null when no age limit is set
	// Sources says, per source, how far back its records go and whether a size budget (not age) cut it short.
	Sources []RetentionSourceView `json:"sources"`
}

// RetentionSourceView is one source's retained history.
type RetentionSourceView struct {
	Name          string     `json:"name"`
	Label         string     `json:"label"`
	RetainedSince *time.Time `json:"retainedSince"` // the oldest record held; null when none
	CappedAt      *time.Time `json:"cappedAt"`      // when a budget last removed records; null if never
	CappedTotal   int64      `json:"cappedTotal"`
}

var retentionLabels = map[string]string{"log": "Log entries", "routine": "Pod lifecycle, Event, autoscaler and count entries", "evidence": "Kubernetes Events"}

// retentionSources is cached for a minute: finding each source's oldest record walks the log, and it changes slowly.
func (s *Server) retentionSources(ctx context.Context) []RetentionSourceView {
	s.retMu.Lock()
	defer s.retMu.Unlock()
	if s.retVal != nil && s.now().Sub(s.retAt) < time.Minute {
		return s.retVal
	}
	out := []RetentionSourceView{}
	rs, err := s.db.RetentionSources(ctx)
	if err != nil {
		return out
	}
	defer func() { s.retVal, s.retAt = out, s.now() }()
	for _, r := range rs {
		out = append(out, RetentionSourceView{Name: r.Name, Label: retentionLabels[r.Name], RetainedSince: tp(r.RetainedSince), CappedAt: tp(r.CappedAt), CappedTotal: r.CappedTotal})
	}
	return out
}

// ExplorerContext is the coverage of a range.
type ExplorerContext struct {
	Schema          int               `json:"schema"`
	From            time.Time         `json:"from"`
	To              time.Time         `json:"to"`
	Now             time.Time         `json:"now"`
	FirstObservedAt *time.Time        `json:"firstObservedAt"`
	OldestRecordAt  *time.Time        `json:"oldestRecordAt"`
	LastWriteAt     *time.Time        `json:"lastWriteAt"`
	Retention       ExplorerRetention `json:"retention"`
	Bands           []Band            `json:"bands"`
	Scopes          []ExplorerScope   `json:"scopes"`
	Sources         []ExplorerSource  `json:"sources"`
	NodesObserved   bool              `json:"nodesObserved"`
}

// scopeKindsOf are the watched kinds a group of records depends on.
var scopeKindsOf = map[string][]string{
	"operations": {"cronjobs", "jobs"},
	"releases":   {"deployments", "replicasets", "deploypods"},
	"workloads":  {"statefulsets", "daemonsets", "controllerrevisions", "workloadpods"},
	"pods":       {"pods", "deploypods", "workloadpods"},
	"nodes":      {"nodes"},
	"scaling":    {"hpas", "deployments"},
	"events":     {"events"},
}

var scopeLabel = map[string]string{"cronjobs": "CronJobs", "jobs": "Jobs", "pods": "Job Pods", "deployments": "Deployments", "replicasets": "ReplicaSets",
	"deploypods": "Deployment Pods", "events": "Kubernetes Events", "hpas": "Autoscalers", "statefulsets": "StatefulSets", "daemonsets": "DaemonSets",
	"controllerrevisions": "ControllerRevisions", "workloadpods": "StatefulSet and DaemonSet Pods", "nodes": "Nodes"}

func labelOf(kind string) string {
	if l, ok := scopeLabel[kind]; ok {
		return l
	}
	return kind
}

func clip(sp Span, from, to time.Time) (Span, bool) {
	if sp.From.Before(from) {
		sp.From = from
	}
	if sp.To.After(to) {
		sp.To = to
	}
	return sp, sp.To.After(sp.From)
}

// gapsOf is the part of [from, to) not inside the (merged, sorted) spans; shorter than a second is not a gap.
func gapsOf(spans []Span, from, to time.Time) []Span {
	out := []Span{}
	at := from
	for _, sp := range spans {
		if sp.From.After(at) && sp.From.Sub(at) >= time.Second {
			out = append(out, Span{at, sp.From})
		}
		if sp.To.After(at) {
			at = sp.To
		}
	}
	if to.After(at) && to.Sub(at) >= time.Second {
		out = append(out, Span{at, to})
	}
	return out
}

func tpp(t time.Time) *time.Time { return tp(t) }

// buildContext reads the coverage of [from, to). namespace, when set, keeps cluster-wide scopes and that namespace's.
func (s *Server) buildContext(ctx context.Context, from, to time.Time, namespace string) (*ExplorerContext, error) {
	now := s.now().UTC()
	cov, err := loadCoverage(ctx, s.db, from, to, now)
	if err != nil {
		return nil, err
	}
	out := &ExplorerContext{Schema: Schema, From: from, To: to, Now: now, Bands: []Band{}, Scopes: []ExplorerScope{}, Sources: []ExplorerSource{}, NodesObserved: s.cfg.ObserveNodes,
		Retention: ExplorerRetention{Days: s.cfg.Retention.Days, MaxEntries: s.cfg.Retention.MaxEvents, Sources: s.retentionSources(ctx)}}
	if lw, err := s.db.LastWrite(ctx); err == nil {
		out.LastWriteAt = tpp(lw)
	}
	if old, err := s.db.OldestRecordTime(ctx); err == nil {
		out.OldestRecordAt = tpp(old)
	}
	problems := map[string]string{}
	for _, p := range s.w.Problems() {
		problems[p.Scope] = p.Err
	}
	firstBy, err := s.db.FirstWatchedAll(ctx)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	var firstAll time.Time
	for sc, t := range firstBy {
		names[sc] = true
		if firstAll.IsZero() || t.Before(firstAll) {
			firstAll = t
		}
	}
	for sc := range problems {
		names[sc] = true
	}
	if !s.cfg.ObserveNodes {
		names[collector.Nodes] = true
	}
	out.FirstObservedAt = tpp(firstAll)
	list := make([]string, 0, len(names))
	for sc := range names {
		list = append(list, sc)
	}
	sort.Strings(list)
	for _, sc := range list {
		kind, ns, _ := strings.Cut(sc, "/")
		if namespace != "" && ns != "" && ns != namespace {
			continue
		}
		v := ExplorerScope{Scope: sc, Kind: kind, Label: labelOf(kind), Namespace: ns, Watching: []Span{}, Gaps: []Span{}, Problem: problems[sc]}
		first, watched := firstBy[sc]
		if watched {
			v.FirstWatchedAt = tpp(first)
		}
		if last, ok, err := s.db.ScopeLastWatched(ctx, sc, now); err == nil && ok {
			v.LastObservedAt = tpp(last)
		}
		for _, sp := range cov.byScope[sc] {
			if c, ok := clip(sp, from, to); ok {
				v.Watching = append(v.Watching, c)
			}
		}
		effFrom, effTo := from, to
		if effTo.After(now) {
			effTo = now
		}
		switch {
		case kind == collector.Nodes && !s.cfg.ObserveNodes:
			v.State = CovDisabled
			v.Note = "Node observation is off. Nodes are not shown as live; the last time they were observed is given."
		case v.Problem != "":
			v.State = CovUnavailable
		case !watched:
			v.State = CovBeforeFirst
			v.Note = "OpsTide has not watched this yet."
		case first.After(effTo):
			v.State = CovBeforeFirst
			v.Note = "OpsTide first watched this after the range."
		default:
			if first.After(effFrom) {
				effFrom = first
			}
			v.Gaps = gapsOf(v.Watching, effFrom, effTo)
			if first.After(from) { // the stretch before this was first watched is not covered either
				v.Gaps = append([]Span{{from, first}}, v.Gaps...)
				v.Note = "First watched " + first.UTC().Format(time.RFC3339) + "; nothing before that is covered."
			}
			v.State = CovWatching
			if len(v.Gaps) > 0 {
				v.State = CovGap
			}
		}
		out.Scopes = append(out.Scopes, v)
	}
	// Bands: the part of the range older than the history kept, the part before the first observation, the rest.
	at := from
	if out.Retention.Days > 0 {
		b := now.AddDate(0, 0, -out.Retention.Days)
		out.Retention.BoundaryAt = &b
		if b.After(at) {
			end := b
			if to.Before(end) {
				end = to
			}
			out.Bands = append(out.Bands, Band{at, end, CovOutsideRetn})
			at = end
		}
	}
	if at.Before(to) {
		first := firstAll
		if first.IsZero() || first.After(to) {
			first = to
		}
		if first.After(at) {
			out.Bands = append(out.Bands, Band{at, first, CovBeforeFirst})
			at = first
		}
	}
	if at.Before(to) {
		out.Bands = append(out.Bands, Band{at, to, "observable"})
	}
	out.Sources = s.explorerSources(out, problems)
	return out, nil
}

func (s *Server) explorerSources(c *ExplorerContext, problems map[string]string) []ExplorerSource {
	api := s.eventsSource()
	out := []ExplorerSource{
		{Name: "kubernetes.api", State: "available", Note: "object state read from the Kubernetes API"},
		{Name: "opstide.rule", State: "available", Note: "documented rules applied to recorded observations"},
		{Name: "opstide.coverage", State: "available", Note: "OpsTide's own account of what it could watch"},
	}
	ev := func(name, a string) ExplorerSource {
		st := ExplorerSource{Name: name, State: "available"}
		if api != "" && api != a {
			st.State, st.Note = "not_in_use", "Kubernetes Events are read through "+api
		}
		for sc, p := range problems {
			if strings.HasPrefix(sc, "events") && st.State == "available" {
				st.State, st.Note = "unavailable", "Events cannot be read ("+p+")"
			}
		}
		return st
	}
	out = append(out, ev("kubernetes.events.k8s.io/v1", "events.k8s.io/v1"), ev("kubernetes.core/v1", "core/v1"))
	for i := range out {
		if out[i].Name == "kubernetes.api" {
			for _, p := range problems {
				out[i].State, out[i].Note = "partial", "some resources cannot be read ("+p+")"
				break
			}
		}
	}
	return out
}

// relevant lists the scopes that records of these groups depend on (all when none are named).
func (c *ExplorerContext) relevant(groups []string) []ExplorerScope {
	if len(groups) == 0 {
		return c.Scopes
	}
	kinds := map[string]bool{}
	for _, g := range groups {
		for _, k := range scopeKindsOf[g] {
			kinds[k] = true
		}
	}
	var out []ExplorerScope
	for _, sc := range c.Scopes {
		if kinds[sc.Kind] {
			out = append(out, sc)
		}
	}
	return out
}

// Notices are short statements about what the range does not cover, for any page of the timeline.
func (c *ExplorerContext) Notices(groups []string) []string {
	out := []string{}
	for _, b := range c.Bands {
		switch b.Kind {
		case CovOutsideRetn:
			out = append(out, "Part of this range is older than the history OpsTide keeps ("+itoa(c.Retention.Days)+" days); records older than "+c.Retention.BoundaryAt.UTC().Format(time.RFC3339)+" are not kept (an Event that keeps recurring is kept while it does).")
		case CovBeforeFirst:
			out = append(out, "Part of this range is before OpsTide first observed this cluster; what happened then is not known.")
		}
	}
	var gaps []string
	seen := map[string]bool{}
	for _, sc := range c.relevant(groups) {
		switch sc.State {
		case CovDisabled:
			out = append(out, sc.Label+" observation is off.")
		case CovUnavailable:
			out = append(out, sc.Label+" cannot be read right now ("+sc.Problem+"); records of them may be missing.")
		case CovGap:
			if !seen[sc.Label] {
				seen[sc.Label] = true
				gaps = append(gaps, sc.Label)
			}
		}
	}
	if len(gaps) > 0 {
		list := strings.Join(gaps, ", ")
		if len(gaps) > 3 {
			list = strings.Join(gaps[:3], ", ") + " and " + itoa(len(gaps)-3) + " more"
		}
		out = append(out, "Not watched for part of this range: "+list+". Records from those stretches may be missing; see Coverage by watch.")
	}
	return out
}

func itoa(n int) string { return strconv.Itoa(n) }

// ExplorerEmpty says why a timeline is empty. The kinds are different statements and are never merged.
type ExplorerEmpty struct {
	Kind    string   `json:"kind"` // no_matches | source_unavailable | outside_retention | before_first_observation | collection_gap
	Message string   `json:"message"`
	Detail  []string `json:"detail"`
}

// EmptyState classifies an empty first page.
func (c *ExplorerContext) EmptyState(groups []string, suppressed int) *ExplorerEmpty {
	// 1. the whole range is older than anything kept
	if c.Retention.BoundaryAt != nil && !c.To.After(*c.Retention.BoundaryAt) {
		return &ExplorerEmpty{CovOutsideRetn, "This range is older than the history OpsTide keeps.", []string{"Records are kept for " + itoa(c.Retention.Days) + " days; nothing before " + c.Retention.BoundaryAt.UTC().Format(time.RFC3339) + " is held, so nothing can be shown."}}
	}
	// 2. the whole range is before OpsTide first watched
	if c.FirstObservedAt == nil || !c.To.After(*c.FirstObservedAt) {
		d := "OpsTide has not observed this cluster yet."
		if c.FirstObservedAt != nil {
			d = "OpsTide first observed this cluster at " + c.FirstObservedAt.UTC().Format(time.RFC3339) + "."
		}
		return &ExplorerEmpty{CovBeforeFirst, "This range is before OpsTide first observed the cluster.", []string{d, "Not enough recorded evidence to determine what happened."}}
	}
	// 2b. count-based pruning: the range ends before the oldest record still held, though OpsTide was already watching
	if c.OldestRecordAt != nil && !c.To.After(*c.OldestRecordAt) && c.FirstObservedAt.Before(*c.OldestRecordAt) {
		return &ExplorerEmpty{CovOutsideRetn, "This range is older than the oldest record OpsTide still holds.", []string{"The oldest record held is from " + c.OldestRecordAt.UTC().Format(time.RFC3339) + "; older ones were removed to stay within the kept history (age or entry limit)."}}
	}
	// 2c. part of the range is not covered at all: an empty result says nothing about that part
	for _, b := range c.Bands {
		if b.Kind == CovOutsideRetn || b.Kind == CovBeforeFirst {
			msg := "Part of this range is before OpsTide first observed the cluster, so an empty result there is not an answer."
			if b.Kind == CovOutsideRetn {
				msg = "Part of this range is older than the history OpsTide keeps, so an empty result there is not an answer."
			}
			return &ExplorerEmpty{b.Kind, "Nothing recorded matches in the part of the range OpsTide covers.", []string{msg, "Not enough recorded evidence to determine what happened in the rest."}}
		}
	}
	// 3. everything these records depend on is off or cannot be read
	rel := c.relevant(groups)
	var off, gaps []string
	for _, sc := range rel {
		switch sc.State {
		case CovDisabled, CovUnavailable:
			off = append(off, sc.Label+" — "+sc.State+": "+firstNonEmpty(sc.Problem, sc.Note))
		case CovGap, CovBeforeFirst:
			gaps = append(gaps, sc.Label)
		}
	}
	if len(rel) > 0 && len(off) == len(rel) {
		return &ExplorerEmpty{CovSourceUnavil, "The source of these records is not being read.", off}
	}
	// 4. watched, but not throughout
	if len(gaps) > 0 {
		d := []string{"OpsTide was not watching these for the whole range: " + strings.Join(gaps, ", ") + ". Records from the gaps may be missing; an empty result here is not proof that nothing happened."}
		return &ExplorerEmpty{CovGap, "No records match, and there are gaps in what OpsTide watched.", append(d, off...)}
	}
	d := []string{"OpsTide was watching throughout this range, and nothing recorded matches the filters."}
	if len(rel) == 0 {
		d = []string{"Nothing recorded matches the filters. These records are OpsTide's own account of its watching, not a watch of the cluster."}
	}
	if suppressed > 0 {
		d = append(d, itoa(suppressed)+" routine record(s) match but are hidden; show routine records to see them.")
	}
	return &ExplorerEmpty{CovNoMatches, "No records match.", append(d, off...)}
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

func (s *Server) explorerContext(w http.ResponseWriter, r *http.Request) {
	from, to, _, err := s.explorerRange(r)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	ns := r.URL.Query().Get("namespace")
	if err := tooLong("namespace", ns, 253); err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	c, err := s.buildContext(r.Context(), from, to, ns)
	reply(w, r, s.log, c, err)
}
