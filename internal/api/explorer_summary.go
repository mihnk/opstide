package api

import (
	"net/http"
	"time"

	"github.com/mihnk/opstide/internal/store"
)

// SummaryItem is one line of the overview of the last day: how many records of a kind there are, and the Explorer query
// (parameters, without the range) that lists them. A count is a count of records; it is not a judgement.
type SummaryItem struct {
	Key    string `json:"key"`
	Label  string `json:"label"`  // "failed run"
	Plural string `json:"plural"` // "failed runs"
	Count  int    `json:"count"`
	Query  string `json:"query"` // Explorer parameters, e.g. "type=job.failed"
}

// SummaryNotWhole names a watch that was not whole in the range.
type SummaryNotWhole struct {
	Label string `json:"label"`
	State string `json:"state"`
}

// ExplorerSummary is the cross-kind overview of a range: what was recorded, and where OpsTide's watching was not whole.
type ExplorerSummary struct {
	Schema   int               `json:"schema"`
	From     time.Time         `json:"from"`
	To       time.Time         `json:"to"`
	Items    []SummaryItem     `json:"items"`
	NotWhole []SummaryNotWhole `json:"notWhole"`
	// Dropped counts objects OpsTide gave up recording since it started (not only in this range); each is a gap.
	Dropped uint64 `json:"dropped"`
}

func (s *Server) explorerSummary(w http.ResponseWriter, r *http.Request) {
	from, to, _, err := s.explorerRange(r)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	out := ExplorerSummary{Schema: Schema, From: from, To: to, Items: []SummaryItem{}, NotWhole: []SummaryNotWhole{}}
	for _, it := range []struct {
		key, label, plural, query string
		q                         store.ExplorerQuery
	}{
		{"changes", "recorded change (a spec or config written, a revision, a replica target, a schedule)", "recorded changes (a spec or config written, a revision, a replica target, a schedule)", "preset=changes", store.ExplorerQuery{Types: changeTypes}},
		{"runs_failed", "failed run", "failed runs", "type=job.failed", store.ExplorerQuery{Types: []string{"job.failed"}}},
		{"runs_unknown", "run with an unknown outcome", "runs with an unknown outcome", "type=job.lost", store.ExplorerQuery{Types: []string{"job.lost"}}},
		{"releases", "release appeared", "releases appeared", "type=release.observed", store.ExplorerQuery{Types: []string{"release.observed"}}},
		{"findings", "release with rule findings", "releases with rule findings", "type=release.health.degraded", store.ExplorerQuery{Types: []string{"release.health.degraded"}}},
		{"workloads", "StatefulSet or DaemonSet change", "StatefulSet or DaemonSet changes", "group=workloads", store.ExplorerQuery{Groups: []string{"workloads"}}},
		{"nodes", "Node change", "Node changes", "group=nodes", store.ExplorerQuery{Groups: []string{"nodes"}}},
		{"warnings", "Warning Event from Kubernetes", "Warning Events from Kubernetes", "type=kubernetes.event&severity=warning", store.ExplorerQuery{Types: []string{store.EvidenceTypeName}, Severities: []string{"warning"}}},
		{"gaps", "stretch OpsTide was not watching", "stretches OpsTide was not watching", "group=observation", store.ExplorerQuery{Groups: []string{"observation"}}},
	} {
		q := it.q
		q.From, q.To = from, to
		n, err := s.db.ExplorerCount(r.Context(), q)
		if err != nil {
			reply(w, r, s.log, nil, err)
			return
		}
		out.Items = append(out.Items, SummaryItem{Key: it.key, Label: it.label, Plural: it.plural, Count: n, Query: it.query})
	}
	ctx, err := s.buildContext(r.Context(), from, to, "")
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	// A watch turned off by configuration (Nodes, Secret metadata) is a choice, not a gap: System lists it, and the
	// Explorer's coverage still says so for records of that kind. Here it would only repeat on every install.
	for _, sc := range ctx.Scopes {
		if sc.State == CovGap || sc.State == CovUnavailable {
			out.NotWhole = append(out.NotWhole, SummaryNotWhole{Label: sc.Label, State: sc.State})
		}
	}
	if d, ok := s.w.(interface{ DroppedObjects() (uint64, time.Time) }); ok {
		out.Dropped, _ = d.DroppedObjects()
	}
	reply(w, r, s.log, out, nil)
}
