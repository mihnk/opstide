package api

import (
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
	order := map[string]int{"cronjobs": 0, "jobs": 1, "pods": 2, "deployments": 3}
	sort.Slice(out.Scopes, func(i, j int) bool {
		a, b := out.Scopes[i], out.Scopes[j]
		if a.Kind != b.Kind {
			return order[a.Kind] < order[b.Kind]
		}
		return a.Namespace < b.Namespace
	})

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
