// Command opstide is the in-cluster collector and web server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/mihnk/opstide/internal/collector"
	"github.com/mihnk/opstide/internal/config"
	"github.com/mihnk/opstide/internal/health"
	"github.com/mihnk/opstide/internal/store"
)

var version = "dev"

func main() {
	cfg, err := config.Load(os.Args[1:], os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "opstide:", err)
		os.Exit(2)
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, log); err != nil {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	log.Info("starting", "version", version, "cluster", cfg.Cluster, "namespaces", cfg.Namespaces)

	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return err
	}
	db, err := store.Open(ctx, filepath.Join(cfg.DataDir, "opstide.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	// Whatever the previous process was watching ended at its last
	// heartbeat; from then until the watches sync again is a gap.
	if err := db.Update(ctx, func(w *store.Writer) error {
		n, err := w.CloseAfterRestart(ctx)
		if n > 0 {
			log.Info("closed coverage left open by the previous run", "intervals", n)
		}
		return err
	}); err != nil {
		return err
	}
	go db.Maintain(ctx, store.Retention{Days: cfg.RetentionDays, MaxEvents: cfg.MaxEvents}, log)

	kube, err := kubeConfig(cfg.Kubeconfig)
	if err != nil {
		return err
	}
	kube.UserAgent = "opstide/" + version
	kube.QPS, kube.Burst = 20, 40
	client, err := kubernetes.NewForConfig(kube)
	if err != nil {
		return err
	}
	col := collector.New(client, db, collector.Options{Namespaces: cfg.Namespaces, Log: log})
	colErr := make(chan error, 1)
	go func() { colErr <- col.Run(ctx) }()

	probes := health.New()
	probes.Add("storage", db.Writable)
	probes.Add("watches", col.Synced)
	mux := http.NewServeMux()
	probes.Register(mux)
	if err := serve(ctx, cfg.Listen, mux, log); err != nil {
		return err
	}
	if err := <-colErr; err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// kubeConfig uses the in-cluster ServiceAccount, or path when set.
func kubeConfig(path string) (*rest.Config, error) {
	if path == "" {
		return rest.InClusterConfig()
	}
	return clientcmd.BuildConfigFromFlags("", path)
}

// serve runs the HTTP server until ctx ends, then drains it for up to 10 s.
func serve(ctx context.Context, addr string, h http.Handler, log *slog.Logger) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("listening", "addr", addr)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
