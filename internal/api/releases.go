package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mihnk/opstide/internal/release"
	"github.com/mihnk/opstide/internal/store"
)

// ReleaseRow is one release on the Releases page.
type ReleaseRow struct {
	ID            string    `json:"id"`
	Namespace     string    `json:"namespace"`
	Workload      string    `json:"workload"`
	Revision      string    `json:"revision"`
	Origin        string    `json:"origin"` // observed | adopted
	ObservedAt    time.Time `json:"observedAt"`
	StartedAt     time.Time `json:"startedAt,omitzero"`
	CompletedAt   time.Time `json:"completedAt,omitzero"`
	SupersededAt  time.Time `json:"supersededAt,omitzero"`
	State         string    `json:"state"` // the rollout
	StateReason   string    `json:"stateReason,omitempty"`
	Health        string    `json:"health"` // what followed it
	HealthReasons []string  `json:"healthReasons"`
	Desired       int       `json:"desired"`
	Updated       int       `json:"updated"`
	Ready         int       `json:"ready"`
	Available     int       `json:"available"`
	Paused        bool      `json:"paused,omitempty"`
	ObserveUntil  time.Time `json:"observeUntil,omitzero"`
	WindowMinutes int       `json:"windowMinutes"`
	PrevID        string    `json:"prevId,omitempty"`
	// Recent are the latest significant entries of the release, newest first.
	Recent []Brief `json:"recent"`
}

// Brief is a one-line entry for lists.
type Brief struct {
	At       time.Time `json:"at"`
	Type     string    `json:"type"`
	Severity string    `json:"severity"`
	Summary  string    `json:"summary"`
}

// Releases is the Releases page.
type Releases struct {
	Schema        int          `json:"schema"`
	Now           time.Time    `json:"now"`
	Releases      []ReleaseRow `json:"releases"`
	NextCursor    string       `json:"nextCursor,omitempty"`
	WindowMinutes int          `json:"windowMinutes"` // the configured observation window
	// Warnings say where release observation had gaps in the range asked for.
	Warnings []string `json:"warnings"`
}

var (
	releaseStates = map[string]bool{"observed": true, "progressing": true, "completed": true, "stalled": true, "failed": true, "superseded": true, "unknown": true}
	releaseHealth = map[string]bool{"healthy": true, "degraded": true, "still_observing": true, "insufficient_evidence": true}
)

func (s *Server) windowMinutes() int {
	if s.cfg.ReleaseWindow > 0 {
		return int(s.cfg.ReleaseWindow / time.Minute)
	}
	return int(release.DefaultWindow / time.Minute)
}

func (s *Server) releaseRow(r store.Release) ReleaseRow {
	return ReleaseRow{
		ID: r.ID, Namespace: r.Namespace, Workload: r.Workload, Revision: r.Revision, Origin: r.Origin,
		ObservedAt: r.ObservedAt, StartedAt: r.StartedAt, CompletedAt: r.CompletedAt, SupersededAt: r.SupersededAt,
		State: r.State, StateReason: r.StateReason, Health: r.Health, HealthReasons: nonNil(release.DecodeReasons(r.HealthReasons)),
		Desired: r.Desired, Updated: r.Updated, Ready: r.Ready, Available: r.Available, Paused: r.Paused,
		ObserveUntil: r.ObserveUntil, WindowMinutes: r.WindowSeconds / 60, PrevID: r.PrevID, Recent: []Brief{},
	}
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func csvParam(r *http.Request, name string, allowed map[string]bool) ([]string, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return nil, nil
	}
	var out []string
	for _, x := range strings.Split(v, ",") {
		if !allowed[x] {
			return nil, badRequest(fmt.Sprintf("%s takes %s", name, keysOf(allowed)))
		}
		out = append(out, x)
	}
	return out, nil
}

func keysOf(m map[string]bool) string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, ", ")
}

// releases lists releases, newest first. The range is within= (default 7d),
// or from= and to=.
func (s *Server) releases(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	var rq store.ReleaseQuery
	var err error
	if rq.From, err = timeParam(r, "from"); err == nil {
		rq.To, err = timeParam(r, "to")
	}
	if err == nil {
		rq.Limit, err = intParam(r, "limit", 50, 200)
	}
	within := q.Get("within")
	if within == "" && rq.From.IsZero() {
		within = "168h"
	}
	if err == nil && within != "" && rq.From.IsZero() {
		d, perr := parseSpan(within)
		if perr != nil || d > 400*24*time.Hour {
			err = badRequest("within must be a duration such as 48h or 7d")
		} else {
			rq.From = s.now().Add(-d)
		}
	}
	if err == nil {
		rq.States, err = csvParam(r, "state", releaseStates)
	}
	if err == nil {
		rq.Health, err = csvParam(r, "health", releaseHealth)
	}
	if len(q.Get("workload")) > 200 {
		err = badRequest("workload is too long")
	}
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	rq.Namespace, rq.Workload, rq.Cursor, rq.WithHistory = q.Get("namespace"), q.Get("workload"), q.Get("cursor"), q.Get("history") == "1"
	page, err := s.db.Releases(ctx, rq)
	if err != nil && strings.HasPrefix(err.Error(), "bad cursor") {
		err = badRequest("bad cursor")
	}
	out := Releases{Schema: Schema, Now: s.now().UTC(), Releases: []ReleaseRow{}, NextCursor: page.NextCursor, WindowMinutes: s.windowMinutes(), Warnings: []string{}}
	if err == nil {
		for _, rel := range page.Releases {
			row := s.releaseRow(rel)
			if ev, err := s.db.Events(ctx, store.EventQuery{ReleaseID: rel.ID, Types: release.SignificantTypes, Limit: 2}); err == nil {
				for _, e := range ev.Events {
					row.Recent = append(row.Recent, Brief{At: e.At(), Type: e.Type, Severity: e.Severity, Summary: e.Summary})
				}
			}
			out.Releases = append(out.Releases, row)
		}
		from, to := rq.From, rq.To
		if to.IsZero() {
			to = out.Now
		}
		out.Warnings = s.releaseWarnings(ctx, from, to, out.Now, rq.Namespace)
	}
	reply(w, r, s.log, out, err)
}

// releaseWarnings names the stretches in [from, to] when Deployments,
// ReplicaSets or their Pods were not being watched, from the whole record. A
// release created in one is adopted, and one that overlapped it cannot be assessed.
func (s *Server) releaseWarnings(ctx context.Context, from, to, now time.Time, namespace string) []string {
	ivs, err := s.db.Coverage(ctx, from.Add(-24*time.Hour), to)
	if err != nil {
		return []string{}
	}
	byScope := map[string][]Span{}
	for _, iv := range ivs {
		kind, ns, _ := strings.Cut(iv.Scope, "/")
		if kind != "deployments" && kind != "replicasets" && kind != "deploypods" {
			continue
		}
		if namespace != "" && ns != "" && ns != namespace {
			continue
		}
		end := iv.EndedAt
		if end.IsZero() {
			end = now
		}
		byScope[iv.Scope] = append(byScope[iv.Scope], Span{iv.StartedAt, end})
	}
	type gap struct {
		kind     string
		from, to time.Time
	}
	seen := map[string]bool{}
	var gaps []gap
	for scope, spans := range byScope {
		kind, _, _ := strings.Cut(scope, "/")
		sort.Slice(spans, func(i, j int) bool { return spans[i].From.Before(spans[j].From) })
		for i := 1; i < len(spans); i++ {
			a, b := spans[i-1].To, spans[i].From
			if !b.After(a) || b.Before(from) || a.After(to) {
				continue
			}
			key := kind + a.Format(time.RFC3339) + b.Format(time.RFC3339)
			if !seen[key] {
				seen[key] = true
				gaps = append(gaps, gap{kind, a, b})
			}
		}
	}
	// A watch that failed and has not come back is a gap that has not ended.
	for scope, spans := range byScope {
		kind, _, _ := strings.Cut(scope, "/")
		sort.Slice(spans, func(i, j int) bool { return spans[i].From.Before(spans[j].From) })
		if last := spans[len(spans)-1]; last.To.Before(now.Add(-time.Minute)) && last.To.Before(to) && last.To.After(from) {
			key := kind + last.To.Format(time.RFC3339) + "now"
			if !seen[key] {
				seen[key] = true
				gaps = append(gaps, gap{kind, last.To, now})
			}
		}
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].from.After(gaps[j].from) })
	out := []string{}
	for i, g := range gaps {
		if i == 3 {
			out = append(out, fmt.Sprintf("%d more gaps in this range; see System.", len(gaps)-3))
			break
		}
		out = append(out, fmt.Sprintf("OpsTide was not watching %s from %s to %s UTC; releases created then are marked adopted and cannot be assessed.",
			map[string]string{"deployments": "Deployments", "replicasets": "ReplicaSets", "deploypods": "Pods"}[g.kind],
			g.from.UTC().Format("2006-01-02 15:04"), g.to.UTC().Format("15:04")))
	}
	return out
}

// Assessment is a release's health verdict with the reasons for it.
type Assessment struct {
	release.Verdict
	// WindowMinutes is the observation length it was judged by.
	WindowMinutes int `json:"windowMinutes"`
	// Windows are the lengths a reader may ask for with ?window=.
	Windows []int `json:"windows"`
	// Stored is false when the verdict was worked out now for a window other
	// than the one the release was observed with.
	Stored bool `json:"stored"`
}

// ReleaseDetail is the Overview of a release.
type ReleaseDetail struct {
	Schema     int               `json:"schema"`
	Now        time.Time         `json:"now"`
	Release    ReleaseRow        `json:"release"`
	Workload   WorkloadRef       `json:"workload"`
	ReplicaSet ReplicaSetRef     `json:"replicaSet"`
	Assessment Assessment        `json:"assessment"`
	Previous   *PrevRef          `json:"previous,omitempty"`
	Snapshot   release.Snapshot  `json:"snapshot"`
	ImageIDs   map[string]string `json:"imageIds"`
	Warnings   []string          `json:"warnings"`
}

// WorkloadRef identifies the Deployment.
type WorkloadRef struct {
	UID       string    `json:"uid"`
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	DeletedAt time.Time `json:"deletedAt,omitzero"`
}

// ReplicaSetRef identifies the ReplicaSet that represented the release.
type ReplicaSetRef struct {
	UID          string `json:"uid"`
	Name         string `json:"name"`
	TemplateHash string `json:"templateHash,omitempty"`
	Fingerprint  string `json:"fingerprint"`
}

// PrevRef points at the preceding release.
type PrevRef struct {
	ID         string    `json:"id"`
	Revision   string    `json:"revision"`
	Origin     string    `json:"origin"`
	ObservedAt time.Time `json:"observedAt"`
}

func (s *Server) loadRelease(r *http.Request) (store.Release, error) {
	rel, ok, err := s.db.Release(r.Context(), r.PathValue("id"))
	if err == nil && !ok {
		err = errNotFound
	}
	return rel, err
}

func (s *Server) watchedFn(ctx context.Context, ns string) func(from, to time.Time) bool {
	scopes := release.ScopeNames(ns, len(s.cfg.Namespaces) == 0)
	return func(from, to time.Time) bool {
		for _, sc := range scopes {
			ok, err := s.db.WatchedThroughout(ctx, sc, from, to)
			if err != nil || !ok {
				return false
			}
		}
		return true
	}
}

// release is one release with its assessment. ?window=5|10|15|30 judges it
// again, now, by that observation length, from what is stored.
func (s *Server) release(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rel, err := s.loadRelease(r)
	var window time.Duration
	chosen := false
	if err == nil {
		if v := r.URL.Query().Get("window"); v != "" {
			n := 0
			if _, perr := fmt.Sscanf(v, "%d", &n); perr != nil || !release.ValidWindow(n) {
				err = badRequest("window must be 5, 10, 15 or 30 (minutes)")
			} else {
				window, chosen = time.Duration(n)*time.Minute, n*60 != rel.WindowSeconds
			}
		}
	}
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	pods, err := s.db.ReleasePods(ctx, rel)
	var rollback bool
	if err == nil {
		rollback, err = s.db.IsRollback(ctx, rel)
	}
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	now := s.now().UTC()
	v := release.Assess(release.InputFor(rel, pods, rollback, now, window, s.watchedFn(ctx, rel.Namespace)))
	// A verdict that was reached and is final is shown as it was reached; the
	// findings and evidence are worked out again only to be displayed. A
	// different ?window= is a question asked now, so it is answered now.
	stored := !chosen
	if stored && rel.Health != release.HealthObserving && rel.Health != "" {
		v.Health = rel.Health
		if reasons := release.DecodeReasons(rel.HealthReasons); len(reasons) > 0 {
			v.Reasons = reasons
		}
	}
	out := ReleaseDetail{Schema: Schema, Now: now, Release: s.releaseRow(rel), Snapshot: release.Snapshot{}, ImageIDs: map[string]string{}, Warnings: []string{}}
	out.Release.Recent = []Brief{}
	if snap, err := release.DecodeSnapshot(rel.Snapshot); err == nil {
		out.Snapshot = snap
	}
	out.Snapshot.Containers = nonNil(out.Snapshot.Containers)
	_ = json.Unmarshal([]byte(rel.ImageIDs), &out.ImageIDs)
	wm := rel.WindowSeconds / 60
	if chosen {
		wm = int(window / time.Minute)
	}
	out.Assessment = Assessment{Verdict: v, WindowMinutes: wm, Windows: release.Windows, Stored: stored}
	out.Workload = WorkloadRef{UID: rel.WorkloadID, Namespace: rel.Namespace, Name: rel.Workload}
	if del, err := s.db.WorkloadDeleted(ctx, rel.WorkloadID); err == nil {
		out.Workload.DeletedAt = del
	}
	out.ReplicaSet = ReplicaSetRef{UID: rel.ReplicaSetUID, Name: rel.ReplicaSetName, TemplateHash: rel.TemplateHash, Fingerprint: rel.Fingerprint}
	if rel.PrevID != "" {
		if p, ok, err := s.db.Release(ctx, rel.PrevID); err == nil && ok {
			out.Previous = &PrevRef{ID: p.ID, Revision: p.Revision, Origin: p.Origin, ObservedAt: p.ObservedAt}
		}
	}
	end := now
	if !rel.ObserveUntil.IsZero() && rel.ObserveUntil.Before(end) {
		end = rel.ObserveUntil
	}
	if !rel.SupersededAt.IsZero() && rel.SupersededAt.Before(end) {
		end = rel.SupersededAt
	}
	out.Warnings = s.releaseWarnings(ctx, rel.ObservedAt.Add(-time.Hour), end, now, rel.Namespace)
	reply(w, r, s.log, out, nil)
}

// releaseEvents is the release's timeline: every entry tied to it, newest first.
func (s *Server) releaseEvents(w http.ResponseWriter, r *http.Request) {
	rel, err := s.loadRelease(r)
	var eq store.EventQuery
	if err == nil {
		eq.Limit, err = intParam(r, "limit", 100, 500)
	}
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	eq.ReleaseID, eq.Cursor = rel.ID, r.URL.Query().Get("cursor")
	page, err := s.db.Events(r.Context(), eq)
	out := Entries{Schema: Schema, Entries: []Entry{}, NextCursor: page.NextCursor}
	for _, e := range page.Events {
		out.Entries = append(out.Entries, entry(e))
	}
	// include=evidence,scaling merges what is recorded about the release's own
	// resources and Deployment into the timeline, each entry keeping its type so
	// the page can tell them apart. Each source is bounded by the limit on its own, they
	// are added to the first page only, and older pages continue the release's own entries.
	if inc := splitList(r.URL.Query().Get("include")); err == nil && len(inc) > 0 && eq.Cursor == "" {
		err = s.mergeTimeline(r, rel, inc, eq.Limit, &out)
	}
	if err != nil && strings.HasPrefix(err.Error(), "bad cursor") {
		err = badRequest(err.Error())
	}
	reply(w, r, s.log, out, err)
}

// PodView is one Pod of a release.
type PodView struct {
	UID          string          `json:"uid"`
	Name         string          `json:"name"`
	Node         string          `json:"node,omitempty"`
	State        string          `json:"state"` // pending | running | ready | not ready | terminating | terminated
	CreatedAt    time.Time       `json:"createdAt"`
	ScheduledAt  time.Time       `json:"scheduledAt,omitzero"`
	ReadyAt      time.Time       `json:"readyAt,omitzero"` // the first time it was Ready
	TerminatedAt time.Time       `json:"terminatedAt,omitzero"`
	TermReason   string          `json:"termReason,omitempty"`
	Evicted      bool            `json:"evicted,omitempty"`
	Restarts     int             `json:"restarts"`
	Containers   []ContainerView `json:"containers"`
	Readiness    []ReadyChange   `json:"readiness"`
	// Quiet marks a Pod that already existed when its release was adopted: its
	// earlier history was not observed.
	Quiet bool `json:"quiet,omitempty"`
}

// ContainerView is what was observed of one container.
type ContainerView struct {
	Name       string   `json:"name"`
	Init       bool     `json:"init,omitempty"`
	Restarts   int32    `json:"restarts"`
	LastReason string   `json:"lastReason,omitempty"`
	LastExit   *int32   `json:"lastExit,omitempty"`
	OOMKilled  int      `json:"oomKilled"`
	Waiting    []string `json:"waiting"`
}

// ReadyChange is a readiness change of a Pod.
type ReadyChange struct {
	At    time.Time `json:"at"`
	Ready bool      `json:"ready"`
}

// ReleasePods is the Pods tab: the ReplicaSet and its Pods.
type ReleasePods struct {
	Schema     int           `json:"schema"`
	ReplicaSet ReplicaSetRef `json:"replicaSet"`
	Pods       []PodView     `json:"pods"`
	// Truncated is set when more Pods exist than are shown (the newest are kept).
	Truncated bool `json:"truncated"`
}

const maxPodsShown = 500

func (s *Server) releasePods(w http.ResponseWriter, r *http.Request) {
	rel, err := s.loadRelease(r)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	pods, err := s.db.ReleasePods(r.Context(), rel)
	out := ReleasePods{Schema: Schema, ReplicaSet: ReplicaSetRef{UID: rel.ReplicaSetUID, Name: rel.ReplicaSetName, TemplateHash: rel.TemplateHash, Fingerprint: rel.Fingerprint}, Pods: []PodView{}}
	if err == nil {
		if len(pods) > maxPodsShown {
			pods, out.Truncated = pods[len(pods)-maxPodsShown:], true
		}
		for _, p := range pods {
			st, derr := release.DecodePod(p.State)
			if derr != nil {
				continue
			}
			out.Pods = append(out.Pods, podView(p, st))
		}
	}
	reply(w, r, s.log, out, err)
}

func podView(p store.PodRecord, st release.PodState) PodView {
	v := PodView{UID: p.UID, Name: p.Name, Node: st.Node, CreatedAt: p.CreatedAt, ScheduledAt: st.ScheduledAt, ReadyAt: st.ReadyAt,
		TerminatedAt: p.TerminatedAt, TermReason: st.TermReason, Evicted: st.Evicted, Containers: []ContainerView{}, Readiness: []ReadyChange{}, Quiet: st.Quiet}
	switch {
	case st.Terminated:
		v.State = "terminated"
	case st.Deleting:
		v.State = "terminating"
	case st.Ready:
		v.State = "ready"
	case len(st.Transitions) > 0:
		v.State = "not ready"
	case st.Phase == "Running":
		v.State = "running"
	default:
		v.State = "pending"
	}
	names := make([]string, 0, len(st.Containers))
	for n := range st.Containers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		c := st.Containers[n]
		cv := ContainerView{Name: n, Init: c.Init, Restarts: c.Restarts, LastReason: c.LastReason, LastExit: c.LastExit, Waiting: []string{}}
		for _, o := range c.OOM {
			if !o.Observed.IsZero() {
				cv.OOMKilled++
			}
		}
		for _, w := range c.Waiting {
			if !w.Observed.IsZero() {
				cv.Waiting = append(cv.Waiting, w.Reason)
			}
		}
		v.Restarts += int(c.Restarts)
		v.Containers = append(v.Containers, cv)
	}
	for _, t := range st.Transitions {
		v.Readiness = append(v.Readiness, ReadyChange{At: t.At, Ready: t.Ready})
	}
	return v
}

// ReleaseChanges is the Changes tab.
type ReleaseChanges struct {
	Schema    int          `json:"schema"`
	Available bool         `json:"available"`
	Reason    string       `json:"reason,omitempty"` // why a comparison is unavailable, or a note on it
	Previous  *PrevRef     `json:"previous,omitempty"`
	Changes   []ChangeView `json:"changes"`
	// SameTemplate: the two Pod templates have the same fingerprint.
	SameTemplate bool `json:"sameTemplate"`
	// NoCapturedDifferences: the templates differ (another ReplicaSet) but
	// nothing in the allow-list changed. Env values, commands and the like are
	// not captured, so something outside it may have.
	NoCapturedDifferences bool `json:"noCapturedDifferences"`
	// DeploymentCompared says whether Deployment-level settings (replicas,
	// strategy) were known for both revisions and compared.
	DeploymentCompared bool `json:"deploymentCompared"`
}

// ChangeView is one difference.
type ChangeView struct {
	release.Change
	Summary string `json:"summary"`
}

func (s *Server) releaseChanges(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rel, err := s.loadRelease(r)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	out := ReleaseChanges{Schema: Schema, Changes: []ChangeView{}}
	unavailable := "The preceding revision was not observed by OpsTide and no snapshot of it exists. OpsTide does not reconstruct it from the current Deployment."
	if rel.PrevID == "" {
		out.Reason = unavailable
		reply(w, r, s.log, out, nil)
		return
	}
	prev, ok, err := s.db.Release(ctx, rel.PrevID)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	if !ok {
		out.Reason = "The preceding revision's record is no longer kept (retention). " + unavailable
		reply(w, r, s.log, out, nil)
		return
	}
	a, aerr := release.DecodeSnapshot(prev.Snapshot)
	b, berr := release.DecodeSnapshot(rel.Snapshot)
	if aerr != nil || berr != nil {
		out.Reason = "A stored snapshot cannot be read."
		reply(w, r, s.log, out, nil)
		return
	}
	out.Available, out.Previous = true, &PrevRef{ID: prev.ID, Revision: prev.Revision, Origin: prev.Origin, ObservedAt: prev.ObservedAt}
	// The same Pod template means the same ReplicaSet (a rollback to it) or an equal
	// pod-template-hash. Equal allow-listed settings are not enough: the allow-list
	// leaves out environment values and commands, which `rollout restart` or an env
	// edit changes.
	out.SameTemplate = prev.ReplicaSetUID == rel.ReplicaSetUID || (rel.TemplateHash != "" && prev.TemplateHash == rel.TemplateHash)
	out.DeploymentCompared = release.DeploymentFieldsCompared(a, b)
	for _, c := range release.Diff(a, b) {
		out.Changes = append(out.Changes, ChangeView{Change: c, Summary: c.Summary()})
	}
	out.NoCapturedDifferences = len(out.Changes) == 0 && !out.SameTemplate
	var notes []string
	if prev.Origin == store.OriginAdopted {
		notes = append(notes, "The preceding revision existed before OpsTide started: its Pod template is the one Kubernetes kept for it, but its rollout was not observed.")
	}
	if !out.DeploymentCompared {
		notes = append(notes, "Replica count and rollout strategy were not compared: they are known only for revisions OpsTide saw roll out.")
	}
	out.Reason = strings.Join(notes, " ")
	reply(w, r, s.log, out, nil)
}

// mergeTimeline adds Kubernetes Event and scaling entries to a release timeline.
func (s *Server) mergeTimeline(r *http.Request, rel store.Release, include []string, limit int, out *Entries) error {
	ctx := r.Context()
	from, to, err := s.releaseWindow(r, rel)
	if err != nil {
		return err
	}
	for _, inc := range include {
		switch inc {
		case "evidence":
			pg, err := s.db.ReleaseEvidence(ctx, store.ReleaseEvidenceQuery{ReplicaSetUID: rel.ReplicaSetUID, WorkloadID: rel.WorkloadID, From: from, To: to, Limit: limit})
			if err != nil {
				return err
			}
			for _, e := range pg.Records {
				out.Entries = append(out.Entries, evidenceEntry(e, rel.ID))
			}
			out.Truncated = out.Truncated || pg.NextCursor != ""
		case "scaling":
			pg, err := s.db.Events(ctx, store.EventQuery{WorkloadID: rel.WorkloadID, Types: []string{"hpa.", "workload.replica_target_changed"}, From: from, To: to, Limit: limit})
			if err != nil {
				return err
			}
			for _, e := range pg.Events {
				out.Entries = append(out.Entries, entry(e))
			}
			out.Truncated = out.Truncated || pg.NextCursor != ""
		default:
			return badRequest("include takes evidence or scaling")
		}
	}
	// An entry can come from two sources (a replica-target change is tied to the
	// release current when it was seen and to its Deployment): keep it once.
	seen := map[string]bool{}
	uniq := out.Entries[:0]
	for _, e := range out.Entries {
		if !seen[e.ID] {
			seen[e.ID] = true
			uniq = append(uniq, e)
		}
	}
	out.Entries = uniq
	// Newest first.
	sort.SliceStable(out.Entries, func(i, j int) bool { return out.Entries[i].At.After(out.Entries[j].At) })
	return nil
}

// evidenceEntry presents an evidence record as a timeline entry, as what Kubernetes reported.
func evidenceEntry(e store.Evidence, releaseID string) Entry {
	sev := "info"
	if e.EventType == "Warning" {
		sev = "warning"
	}
	facts := map[string]string{"reason": e.Reason, "eventType": e.EventType, "message": e.Message, "source": e.Source, "origin": e.Origin, "evidenceId": e.ID}
	if e.Count > 0 {
		facts["count"] = strconv.Itoa(e.Count)
	}
	return Entry{ID: "evidence/" + e.ID, Type: "evidence.record", Namespace: e.Namespace, Kind: e.Regarding.Kind, ResourceName: e.Regarding.Name, ResourceUID: e.Regarding.UID,
		Reason: e.Reason, WorkloadID: e.WorkloadID, ReleaseID: releaseID, At: e.SortTime(), OccurredAt: firstNonZero(e.LastAt, e.FirstAt), ObservedAt: e.ObservedAt, RecordedAt: e.UpdatedAt,
		Severity: sev, Confidence: string(store.Observed), Summary: "Kubernetes reported " + e.Reason + " for " + e.Regarding.Kind + " " + e.Regarding.Name, Facts: facts}
}

func firstNonZero(ts ...time.Time) time.Time {
	for _, t := range ts {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}
