// Package api serves OpsTide's read-only HTTP API under /api/v1.
//
// Every response is JSON with a schema number. Times are RFC 3339 in UTC;
// the browser shows them in the reader's zone and sends that zone (tz) on
// requests that depend on where a day starts. Nothing here writes to the
// record or to the cluster.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/mihnk/opstide/internal/collector"
	"github.com/mihnk/opstide/internal/maintenance"
	"github.com/mihnk/opstide/internal/store"
)

// Schema is the response schema version; it changes when a field changes
// meaning or goes away.
const Schema = 1

// Watcher is what the API needs from the collector.
type Watcher interface {
	Synced(ctx context.Context) error
	Problems() []collector.Problem
}

// Config describes the running collector for the System screen.
type Config struct {
	Version    string
	Cluster    string
	Namespaces []string // empty: the whole cluster
	Retention  store.Retention
	StartedAt  time.Time
	// ControllerZone is the zone the CronJob controller evaluates
	// schedules in when a CronJob sets no spec.timeZone (normally UTC).
	ControllerZone *time.Location
	// ReleaseWindow is the observation length after a rollout completes
	// (default 10 minutes).
	ReleaseWindow time.Duration
	// ObserveNodes says whether Nodes are watched (they need cluster RBAC and are off by default).
	ObserveNodes bool
	// WatchConfigMaps and WatchSecretMetadata say whether ConfigMaps, and Secrets' metadata, are watched.
	WatchConfigMaps, WatchSecretMetadata bool
	// ClustrailURL is a link template for "Open in Clustrail"; {cluster},
	// {namespace} and {name} are replaced, URL-escaped. Empty: no link.
	ClustrailURL string
	// PublicURL is where readers open OpsTide (no trailing slash), for links handed to other tools; empty gives paths.
	PublicURL string
	// Maintenance is the configured maintenance windows (nil: none). Records inside one are marked, never hidden.
	Maintenance *maintenance.Set
	// Demo says the data is made up (opstide -demo); the UI shows it on every page.
	Demo bool
}

// Server answers API requests.
type Server struct {
	db  *store.Store
	w   Watcher
	cfg Config
	log *slog.Logger
	now func() time.Time

	streams chan struct{} // bounds concurrent event streams

	retMu  sync.Mutex // guards the cached retention sources
	retAt  time.Time
	retVal []RetentionSourceView
}

// New returns an API server.
func New(db *store.Store, w Watcher, cfg Config, log *slog.Logger) *Server {
	if cfg.ControllerZone == nil {
		cfg.ControllerZone = time.UTC
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Server{db: db, w: w, cfg: cfg, log: log, now: time.Now, streams: make(chan struct{}, 64)}
}

// Register adds the API routes to mux.
func (s *Server) Register(mux *http.ServeMux) {
	heavy := newCache(s.db.Version).wrap // the endpoints that read a long stretch of the record
	mux.HandleFunc("GET /api/v1/range", heavy(s.rangeHandler))
	mux.HandleFunc("GET /api/v1/day", heavy(s.rangeHandler)) // the same, kept for date= links
	mux.HandleFunc("GET /api/v1/overview", heavy(s.overview))
	mux.HandleFunc("GET /api/v1/events", s.events)
	mux.HandleFunc("GET /api/v1/operations", heavy(s.operations))
	mux.HandleFunc("GET /api/v1/owners", s.owners)
	mux.HandleFunc("GET /api/v1/releases", s.releases)
	mux.HandleFunc("GET /api/v1/releases/{id}", s.release)
	mux.HandleFunc("GET /api/v1/releases/{id}/events", s.releaseEvents)
	mux.HandleFunc("GET /api/v1/releases/{id}/pods", s.releasePods)
	mux.HandleFunc("GET /api/v1/releases/{id}/changes", s.releaseChanges)
	mux.HandleFunc("GET /api/v1/releases/{id}/evidence", s.releaseEvidence)
	mux.HandleFunc("GET /api/v1/evidence", s.evidenceList)
	mux.HandleFunc("GET /api/v1/evidence/{id}", s.evidenceOne)
	mux.HandleFunc("GET /api/v1/workloads/{id}/scaling", s.workloadScaling)
	mux.HandleFunc("GET /api/v1/workloads", s.workloadList)
	mux.HandleFunc("GET /api/v1/workloads/{id}", s.workloadOne)
	mux.HandleFunc("GET /api/v1/workloads/{id}/history", s.workloadHistory)
	mux.HandleFunc("GET /api/v1/workloads/{id}/revisions", s.workloadRevisions)
	mux.HandleFunc("GET /api/v1/workloads/{id}/pods", s.workloadPods)
	mux.HandleFunc("GET /api/v1/workloads/{id}/evidence", s.workloadEvidence)
	mux.HandleFunc("GET /api/v1/images", heavy(s.images))         // every revision's images, over the whole record
	mux.HandleFunc("GET /api/v1/images/{ref}", heavy(s.imageOne)) // one image's history; ref is base64url, see ImageRefEncoding
	mux.HandleFunc("GET /api/v1/nodes", s.nodeList)
	mux.HandleFunc("GET /api/v1/nodes/{id}", s.nodeOne)
	mux.HandleFunc("GET /api/v1/nodes/{id}/history", s.nodeHistory)
	mux.HandleFunc("GET /api/v1/runs/{id}/evidence", s.runEvidence)
	mux.HandleFunc("GET /api/v1/operations/{id}", s.operation)
	mux.HandleFunc("GET /api/v1/runs/{id}", s.run)
	mux.HandleFunc("GET /api/v1/system", s.system)
	mux.HandleFunc("GET /api/v1/explorer", s.explorer)
	mux.HandleFunc("GET /api/v1/explorer/filters", heavy(s.explorerFilters)) // counts over the whole range
	mux.HandleFunc("GET /api/v1/explorer/context", s.explorerContext)
	mux.HandleFunc("GET /api/v1/explorer/summary", heavy(s.explorerSummary))
	mux.HandleFunc("GET /api/v1/explorer/state", s.explorerState)
	mux.HandleFunc("GET /api/v1/explorer/records/{id}", s.explorerRecordOne)
	mux.HandleFunc("GET /api/v1/explorer/records/{id}/before", heavy(s.explorerBefore)) // a window of history over many related resources
	mux.HandleFunc("GET /api/v1/explorer/export", s.explorerExport)
	mux.HandleFunc("POST /api/v1/explorer/export", s.explorerExportSelected)
	mux.HandleFunc("POST /api/v1/explorer/lookup", s.explorerLookup)
	mux.HandleFunc("GET /api/v1/annotations", heavy(s.annotations)) // Grafana pulls these over a long range
	mux.HandleFunc("GET /api/v1/stream", s.stream)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, http.StatusNotFound, "no such API endpoint")
	})
}

type errBadRequest struct{ msg string }

func (e errBadRequest) Error() string { return e.msg }

func badRequest(msg string) error { return errBadRequest{msg} }

func reply(w http.ResponseWriter, r *http.Request, log *slog.Logger, v any, err error) {
	var bad errBadRequest
	switch {
	case errors.As(err, &bad):
		fail(w, http.StatusBadRequest, bad.msg)
	case errors.Is(err, errNotFound):
		fail(w, http.StatusNotFound, "not found")
	case err != nil:
		if r.Context().Err() == nil {
			log.Error("api", "path", r.URL.Path, "err", err)
		}
		fail(w, http.StatusInternalServerError, "internal error; see the collector's log")
	default:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(v)
	}
}

var errNotFound = errors.New("not found")

func fail(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"schema": Schema, "error": msg})
}

// zone reads the tz parameter; UTC when absent.
func zone(r *http.Request) (*time.Location, error) {
	tz := r.URL.Query().Get("tz")
	if tz == "" {
		return time.UTC, nil
	}
	if len(tz) > 64 {
		return nil, badRequest("tz is too long")
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, badRequest("unknown time zone " + strconv.Quote(tz))
	}
	return loc, nil
}

// dayBounds is the calendar day containing t in loc: midnight to the next
// midnight, 23 or 25 hours long on daylight-saving days.
func dayBounds(t time.Time, loc *time.Location) (time.Time, time.Time) {
	t = t.In(loc)
	from := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	return from, from.AddDate(0, 0, 1)
}

func dateParam(r *http.Request, loc *time.Location, now time.Time) (time.Time, error) {
	d := r.URL.Query().Get("date")
	if d == "" {
		return now, nil
	}
	t, err := time.ParseInLocation("2006-01-02", d, loc)
	if err != nil {
		return time.Time{}, badRequest("date must be YYYY-MM-DD")
	}
	return t, nil
}

func intParam(r *http.Request, name string, def, max int) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, badRequest(name + " must be a positive number")
	}
	return min(n, max), nil
}

func timeParam(r *http.Request, name string) (time.Time, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, badRequest(name + " must be an RFC 3339 time")
	}
	return t, nil
}
