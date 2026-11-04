// Package health serves the liveness and readiness probes.
//
// /healthz answers while the process can serve HTTP. /readyz answers 200
// only when every registered check passes: in OpsTide that is every watch
// having synced once and the database accepting writes.
package health

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Check reports nil when the named dependency is ready.
type Check func(ctx context.Context) error

// Probes holds the readiness checks.
type Probes struct {
	mu     sync.RWMutex
	checks map[string]Check
}

// New returns probes with no readiness checks; with none, /readyz is ready.
func New() *Probes { return &Probes{checks: map[string]Check{}} }

// Add registers or replaces a readiness check.
func (p *Probes) Add(name string, c Check) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checks[name] = c
}

// Ready runs every check and returns the failures, sorted by name.
func (p *Probes) Ready(ctx context.Context) []string {
	p.mu.RLock()
	names := make([]string, 0, len(p.checks))
	for n := range p.checks {
		names = append(names, n)
	}
	checks := make(map[string]Check, len(p.checks))
	for n, c := range p.checks {
		checks[n] = c
	}
	p.mu.RUnlock()
	sort.Strings(names)

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var failed []string
	for _, n := range names {
		if err := checks[n](ctx); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", n, err))
		}
	}
	return failed
}

// Register adds /healthz and /readyz to mux.
func (p *Probes) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if failed := p.Ready(r.Context()); len(failed) > 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(strings.Join(failed, "\n") + "\n"))
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
}
