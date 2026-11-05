package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/mihnk/opstide/internal/store"
)

// Entry is one log entry.
type Entry struct {
	ID           string            `json:"id"`
	Type         string            `json:"type"`
	Namespace    string            `json:"namespace,omitempty"`
	Kind         string            `json:"kind,omitempty"`
	ResourceName string            `json:"resourceName,omitempty"`
	ResourceUID  string            `json:"resourceUid,omitempty"`
	OperationID  string            `json:"operationId,omitempty"`
	RunID        string            `json:"runId,omitempty"`
	At           time.Time         `json:"at"`
	OccurredAt   time.Time         `json:"occurredAt,omitzero"`
	ObservedAt   time.Time         `json:"observedAt"`
	RecordedAt   time.Time         `json:"recordedAt"`
	Severity     string            `json:"severity"`
	Confidence   string            `json:"confidence"`
	Summary      string            `json:"summary"`
	Facts        map[string]string `json:"facts,omitempty"`
}

func entry(e store.Event) Entry {
	return Entry{ID: e.ID, Type: e.Type, Namespace: e.Namespace, Kind: e.Kind, ResourceName: e.ResourceName,
		ResourceUID: e.ResourceUID, OperationID: e.OperationID, RunID: e.RunID, At: e.At(), OccurredAt: e.OccurredAt,
		ObservedAt: e.ObservedAt, RecordedAt: e.RecordedAt, Severity: e.Severity, Confidence: string(e.Confidence),
		Summary: e.Summary, Facts: e.Facts}
}

// Entries is a page of the Log screen.
type Entries struct {
	Schema     int     `json:"schema"`
	Entries    []Entry `json:"entries"`
	NextCursor string  `json:"nextCursor,omitempty"`
}

// Log filters, by the groups the Log screen offers.
var typeGroups = map[string][]string{
	"runs":       {"job.succeeded", "job.failed", "job.lost"},
	"starts":     {"job.created"},
	"schedules":  {"cronjob."},
	"rollouts":   {"deployment."},
	"collection": {"collection."},
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var eq store.EventQuery
	var err error
	if eq.From, err = timeParam(r, "from"); err == nil {
		eq.To, err = timeParam(r, "to")
	}
	if err == nil {
		eq.Limit, err = intParam(r, "limit", 100, 500)
	}
	// within=48h: from that long before now, so a page's URL stays the same
	// while it is open.
	if w := q.Get("within"); err == nil && w != "" {
		d, perr := parseSpan(w)
		if perr != nil || d > 400*24*time.Hour {
			err = badRequest("within must be a duration such as 48h or 5d")
		} else if eq.From.IsZero() {
			eq.From = s.now().Add(-d)
		}
	}
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	eq.Namespace, eq.OperationID, eq.RunID, eq.Cursor = q.Get("namespace"), q.Get("operation"), q.Get("run"), q.Get("cursor")
	if t := q.Get("q"); len(t) > 200 {
		reply(w, r, s.log, nil, badRequest("q is too long"))
		return
	} else {
		eq.Text = t
	}
	if show := q.Get("show"); show != "" {
		for _, g := range strings.Split(show, ",") {
			types, ok := typeGroups[g]
			if !ok {
				reply(w, r, s.log, nil, badRequest("show takes runs, starts, schedules, rollouts or collection"))
				return
			}
			eq.Types = append(eq.Types, types...)
		}
	}
	page, err := s.db.Events(r.Context(), eq)
	out := Entries{Schema: Schema, Entries: []Entry{}, NextCursor: page.NextCursor}
	for _, e := range page.Events {
		out.Entries = append(out.Entries, entry(e))
	}
	if err != nil && strings.HasPrefix(err.Error(), "bad cursor") {
		err = badRequest(err.Error())
	}
	reply(w, r, s.log, out, err)
}

// RunDetail is one run with its log entries.
type RunDetail struct {
	Schema    int        `json:"schema"`
	Run       Run        `json:"run"`
	Operation *Operation `json:"operation,omitempty"`
	Entries   []Entry    `json:"entries"`
}

func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	run, ok, err := s.db.Run(ctx, r.PathValue("id"))
	if err == nil && !ok {
		err = errNotFound
	}
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	out := RunDetail{Schema: Schema, Entries: []Entry{}}
	var expected *time.Duration
	if run.OperationID != "" {
		op, ok, err := s.db.Operation(ctx, run.OperationID)
		if err != nil {
			reply(w, r, s.log, nil, err)
			return
		}
		if ok {
			v, err := s.view(ctx, op, nil)
			if err != nil {
				reply(w, r, s.log, nil, err)
				return
			}
			o := v.operation()
			out.Operation, expected = &o, op.ExpectedDuration
		}
	}
	out.Run = runView(run, expected, s.now().UTC())
	out.Run.Pods = run.Pods
	page, err := s.db.Events(ctx, store.EventQuery{RunID: run.ID, Limit: 200})
	for _, e := range page.Events {
		out.Entries = append(out.Entries, entry(e))
	}
	reply(w, r, s.log, out, err)
}
