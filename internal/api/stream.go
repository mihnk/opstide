package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// stream sends server-sent events: "change" whenever the record changes
// (the page refetches what it shows) and "ping" every 15 s with the last
// write, so the page's freshness line stays true. It polls the record's
// version every 2 s; the API works without it.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	select {
	case s.streams <- struct{}{}:
		defer func() { <-s.streams }()
	default:
		fail(w, http.StatusServiceUnavailable, "too many live views open")
		return
	}
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	ctx := r.Context()
	send := func(event string, v any) bool {
		b, _ := json.Marshal(v)
		_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	ping := func() bool {
		last, err := s.db.LastWrite(ctx)
		if err != nil {
			return ctx.Err() == nil
		}
		return send("ping", map[string]any{"lastWrite": last, "synced": s.w.Synced(ctx) == nil})
	}

	version, _ := s.db.Version(ctx)
	if !send("hello", map[string]any{"schema": Schema, "version": version}) || !ping() {
		return
	}
	poll := time.NewTicker(2 * time.Second)
	beat := time.NewTicker(15 * time.Second)
	defer poll.Stop()
	defer beat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			v, err := s.db.Version(ctx)
			if err != nil || v == version {
				continue
			}
			version = v
			if !send("change", map[string]any{"version": v}) {
				return
			}
		case <-beat.C:
			if !ping() {
				return
			}
		}
	}
}
