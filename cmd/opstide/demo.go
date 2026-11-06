package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mihnk/opstide/internal/api"
	"github.com/mihnk/opstide/internal/auth"
	"github.com/mihnk/opstide/internal/config"
	"github.com/mihnk/opstide/internal/demo"
	"github.com/mihnk/opstide/internal/health"
	"github.com/mihnk/opstide/internal/store"
	"github.com/mihnk/opstide/internal/webui"
)

// runDemo serves a made-up history from a temporary database: no cluster, kubeconfig or Docker, and nothing collected.
// The real store, API and UI run; only the content is invented, and the UI says so on every page.
func runDemo(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	dir, err := os.MkdirTemp("", "opstide-demo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db, err := store.Open(ctx, filepath.Join(dir, "opstide.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	if err := demo.Seed(ctx, db, time.Now()); err != nil {
		return err
	}
	// Keeps the "last write" heartbeat fresh so the header reads as alive; the history itself is a snapshot.
	maintCtx, stopMaint := context.WithCancel(ctx)
	var maint sync.WaitGroup
	maint.Go(func() { db.Maintain(maintCtx, store.Retention{Days: cfg.RetentionDays, MaxEvents: cfg.MaxEvents}, log) })
	defer maint.Wait()
	defer stopMaint()

	listen := cfg.Listen
	if listen == ":8080" { // a demo is for this computer unless asked otherwise
		listen = "127.0.0.1:8080"
	}
	probes := health.New()
	probes.Add("storage", db.Writable)
	mux := http.NewServeMux()
	probes.Register(mux)
	auth.New("").Register(mux) // no login: the session endpoint still answers
	api.New(db, demo.Watcher{}, api.Config{Version: version + " (demo)", Cluster: "demo", StartedAt: time.Now(), Demo: true, ReleaseWindow: 10 * time.Minute,
		Retention: store.Retention{Days: cfg.RetentionDays, MaxEvents: cfg.MaxEvents}}, log).Register(mux)
	mux.Handle("/", webui.Handler())
	log.Info("demo: made-up data, no cluster; open the address below", "url", "http://"+listen)
	return serve(ctx, listen, secure(mux), log)
}
