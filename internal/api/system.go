package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// System is the System screen: what OpsTide can and cannot see.
type System struct {
	Schema     int       `json:"schema"`
	Version    string    `json:"version"`
	Cluster    string    `json:"cluster"`
	StartedAt  time.Time `json:"startedAt"`
	Now        time.Time `json:"now"`
	LastWrite  time.Time `json:"lastWrite,omitzero"`
	Synced     bool      `json:"synced"`
	Namespaces []string  `json:"namespaces"` // empty: the whole cluster
	TimeZone   string    `json:"timeZone"`
	From       time.Time `json:"from"` // start of today in TimeZone
	Scopes     []Scope   `json:"scopes"`
	Storage    Storage   `json:"storage"`
	// Integrations are optional connections. v0.1 has none configured;
	// each says so rather than implying a connection.
	Integrations []Integration `json:"integrations"`
	// Workload is the state of release observation.
	Workload WorkloadSystem `json:"workload"`
	// Evidence is the state of Kubernetes Event and autoscaler collection.
	Evidence EvidenceSystem `json:"evidence"`
}

// EvidenceSystem is the state of Kubernetes Event and autoscaler collection.
type EvidenceSystem struct {
	// Source is the API Events are read through (events.k8s.io/v1, or core/v1 on a
	// cluster that does not serve the first); empty until the collector has chosen.
	Source string `json:"source"`
	// Records and Warnings count the Events kept; LatestObserved is when OpsTide
	// last saw an Event for the first time.
	Records        int64     `json:"records"`
	Warnings       int64     `json:"warnings"`
	LatestObserved time.Time `json:"latestObserved,omitzero"`
	Autoscalers    int64     `json:"autoscalers"`
	// Degraded says what makes evidence collection incomplete right now.
	Degraded []string `json:"degraded"`
}

// WorkloadSystem is the state of release observation.
type WorkloadSystem struct {
	// WindowMinutes is the configured post-deployment observation length.
	WindowMinutes int `json:"windowMinutes"`
	// ActiveWindows counts releases being observed now.
	ActiveWindows int `json:"activeWindows"`
	// LastEvent is when a release or Pod entry was last written.
	LastEvent time.Time `json:"lastEvent,omitzero"`
	// Degraded says what makes release observation incomplete right now.
	Degraded []string `json:"degraded"`
}

// Scope is one watch (a kind, cluster-wide or in one namespace) and when
// it was watching today.
type Scope struct {
	Scope     string `json:"scope"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Watching  []Span `json:"watching"`
	Problem   string `json:"problem,omitempty"`
}

// Storage describes the database.
type Storage struct {
	SchemaVersion    int       `json:"schemaVersion"`
	Bytes            int64     `json:"bytes"`
	WALBytes         int64     `json:"walBytes"`
	Entries          int64     `json:"entries"`
	OldestEntry      time.Time `json:"oldestEntry,omitzero"`
	LastPrune        time.Time `json:"lastPrune,omitzero"`
	LastPruneRemoved int64     `json:"lastPruneRemoved"`
	LastCheckpoint   time.Time `json:"lastCheckpoint,omitzero"`
	RetentionDays    int       `json:"retentionDays"`
	MaxEntries       int       `json:"maxEntries"`
}

// Integration is an optional connection and whether it is set up.
type Integration struct {
	Name  string `json:"name"`
	State string `json:"state"` // not configured | ready | partial | unavailable
	Note  string `json:"note"`
}

func (s *Server) system(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	loc, err := zone(r)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	now := s.now().UTC()
	from, _ := dayBounds(now, loc)
	out := System{Schema: Schema, Version: s.cfg.Version, Cluster: s.cfg.Cluster, StartedAt: s.cfg.StartedAt, Now: now,
		Synced: s.w.Synced(ctx) == nil, Namespaces: append([]string{}, s.cfg.Namespaces...), TimeZone: loc.String(),
		From: from.UTC(), Scopes: []Scope{}}
	if out.LastWrite, err = s.db.LastWrite(ctx); err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	cov, err := loadCoverage(ctx, s.db, from, now, now)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	problems := map[string]string{}
	for _, p := range s.w.Problems() {
		problems[p.Scope] = p.Err
	}
	seen := map[string]bool{}
	for scope, spans := range cov.byScope {
		seen[scope] = true
		out.Scopes = append(out.Scopes, scopeView(scope, spans, problems[scope]))
	}
	for scope, p := range problems {
		if !seen[scope] {
			out.Scopes = append(out.Scopes, scopeView(scope, []Span{}, p))
		}
	}
	order := map[string]int{"cronjobs": 0, "jobs": 1, "pods": 2, "deployments": 3, "replicasets": 4, "deploypods": 5, "events": 6, "hpas": 7}
	sort.Slice(out.Scopes, func(i, j int) bool {
		a, b := out.Scopes[i], out.Scopes[j]
		if a.Kind != b.Kind {
			return order[a.Kind] < order[b.Kind]
		}
		return a.Namespace < b.Namespace
	})

	out.Workload = WorkloadSystem{WindowMinutes: s.windowMinutes(), Degraded: []string{}}
	if out.Workload.ActiveWindows, err = s.db.ActiveWindows(ctx, now); err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	if out.Workload.LastEvent, err = s.db.LastWorkloadEvent(ctx); err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	// Release observation needs Deployments, ReplicaSets and their Pods; say
	// which of them it cannot read right now.
	names := map[string]string{"deployments": "Deployments", "replicasets": "ReplicaSets", "deploypods": "Deployment Pods"}
	for _, sc := range out.Scopes {
		if n, ok := names[sc.Kind]; ok && sc.Problem != "" {
			out.Workload.Degraded = append(out.Workload.Degraded, fmt.Sprintf("%s cannot be watched (%s): releases from now on may be missing or unassessable.", n, sc.Problem))
		}
	}
	out.Evidence = EvidenceSystem{Source: s.eventsSource(), Degraded: []string{}}
	if es, err := s.db.EvidenceStats(ctx); err != nil {
		reply(w, r, s.log, nil, err)
		return
	} else {
		out.Evidence.Records, out.Evidence.Warnings, out.Evidence.LatestObserved, out.Evidence.Autoscalers = es.Records, es.Warnings, es.Latest, es.HPAs
	}
	for _, sc := range out.Scopes {
		switch {
		case sc.Kind == "events" && sc.Problem != "":
			out.Evidence.Degraded = append(out.Evidence.Degraded, fmt.Sprintf("Kubernetes Events cannot be read (%s): evidence from now on may be missing.", sc.Problem))
		case sc.Kind == "hpas" && sc.Problem != "":
			out.Evidence.Degraded = append(out.Evidence.Degraded, fmt.Sprintf("HorizontalPodAutoscalers cannot be read (%s): scaling context may be missing.", sc.Problem))
		}
	}
	st, err := s.db.Stats(ctx)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	out.Storage = Storage{SchemaVersion: st.SchemaVersion, Bytes: st.Bytes, WALBytes: st.WALBytes, Entries: st.Events,
		OldestEntry: st.OldestEvent, LastPrune: st.LastPrune, LastPruneRemoved: st.LastPruneRemoved,
		LastCheckpoint: st.LastCheckpoint, RetentionDays: s.cfg.Retention.Days, MaxEntries: s.cfg.Retention.MaxEvents}
	out.Integrations = []Integration{
		{Name: "Clustrail link", State: "not configured", Note: "Open an object in Clustrail from any entry."},
		{Name: "Investigation agent", State: "not configured", Note: "Not part of v0.1. Investigate stays off."},
		{Name: "Prometheus", State: "not configured", Note: "Optional, for resource use around a run."},
		{Name: "Notifications", State: "not configured", Note: "Webhook for failures and gaps. Off by default."},
	}
	reply(w, r, s.log, out, nil)
}

func scopeView(scope string, spans []Span, problem string) Scope {
	kind, ns, _ := strings.Cut(scope, "/")
	return Scope{Scope: scope, Kind: kind, Namespace: ns, Watching: spans, Problem: problem}
}
