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
	"sync"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/mihnk/opstide/internal/api"
	"github.com/mihnk/opstide/internal/auth"
	"github.com/mihnk/opstide/internal/collector"
	"github.com/mihnk/opstide/internal/config"
	"github.com/mihnk/opstide/internal/health"
	"github.com/mihnk/opstide/internal/metrics"
	"github.com/mihnk/opstide/internal/store"
	"github.com/mihnk/opstide/internal/webui"
)

var version = "dev"

func main() {
	cfg, err := config.Load(os.Args[1:], os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "opstide:", err)
		os.Exit(2)
	}
	if err := api.ValidClustrailURL(cfg.ClustrailURL); err != nil {
		fmt.Fprintln(os.Stderr, "opstide:", err)
		os.Exit(2)
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if cfg.Demo {
		if err := runDemo(ctx, cfg, log); err != nil {
			log.Error("stopped", "err", err)
			os.Exit(1)
		}
		return
	}
	if err := run(ctx, cfg, log); err != nil {
		log.Error("stopped", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	started := time.Now()
	log.Info("starting", "version", version, "cluster", cfg.Cluster, "namespaces", cfg.Namespaces)

	kube, where, err := kubeConfig(cfg.Kubeconfig, cfg.Context)
	if err != nil {
		return err
	}
	log.Info("kubernetes", "context", where, "server", kube.Host)
	kube.UserAgent = "opstide/" + version
	kube.QPS, kube.Burst = 20, 40
	client, err := kubernetes.NewForConfig(kube)
	if err != nil {
		return err
	}
	// Secrets are read through the metadata-only API: the server sends PartialObjectMetadata, never a Secret's data.
	var md metadata.Interface
	if cfg.WatchSecretMetadata {
		if md, err = metadata.NewForConfig(kube); err != nil {
			return err
		}
	}

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
	// Maintenance stops, and is waited for, before the store closes.
	maintCtx, stopMaint := context.WithCancel(ctx)
	var maint sync.WaitGroup
	maint.Go(func() {
		db.Maintain(maintCtx, store.Retention{Days: cfg.RetentionDays, MaxEvents: cfg.MaxEvents, MaxRoutine: cfg.MaxRoutineEvents}, log)
	})
	defer maint.Wait()
	defer stopMaint()

	col := collector.New(client, db, collector.Options{Namespaces: cfg.Namespaces, Log: log, ReleaseWindow: time.Duration(cfg.ReleaseWindowMinutes) * time.Minute, ObserveNodes: cfg.ObserveNodes,
		WatchConfigMaps: cfg.WatchConfigMaps, WatchSecretMetadata: cfg.WatchSecretMetadata, Metadata: md, RetentionDays: cfg.RetentionDays})
	colErr := make(chan error, 1)
	go func() { colErr <- col.Run(ctx) }()

	probes := health.New()
	probes.Add("storage", db.Writable)
	probes.Add("watches", col.Synced)
	token, err := auth.LoadToken(cfg.TokenFile, cfg.Token)
	if err != nil {
		return err
	}
	guard := auth.New(token)
	if !guard.Enabled() && !loopback(cfg.Listen) {
		log.Warn("no token is set and the address is not loopback: anyone who can reach it can read the record; set -token-file or put an authenticating proxy in front", "listen", cfg.Listen)
	}
	reg := &metrics.Registry{}
	reg.Add(ownMetrics(db, col, version, cfg.Cluster, started))
	mux := http.NewServeMux()
	probes.Register(mux)
	guard.Register(mux)
	mux.Handle("/metrics", reg.Handler())
	api.New(db, col, api.Config{
		Version: version, Cluster: cfg.Cluster, Namespaces: cfg.Namespaces, StartedAt: started, ClustrailURL: cfg.ClustrailURL, ReleaseWindow: time.Duration(cfg.ReleaseWindowMinutes) * time.Minute, ObserveNodes: cfg.ObserveNodes,
		WatchConfigMaps: cfg.WatchConfigMaps, WatchSecretMetadata: cfg.WatchSecretMetadata,
		Retention: store.Retention{Days: cfg.RetentionDays, MaxEvents: cfg.MaxEvents, MaxRoutine: cfg.MaxRoutineEvents},
	}, log).Register(mux)
	mux.Handle("/", webui.Handler())
	if err := serve(ctx, cfg.Listen, secure(reg.Instrument(guard.Middleware(mux))), log); err != nil {
		return err
	}
	if err := <-colErr; err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// kubeConfig returns the API client configuration and a name for where it
// came from. With neither paths nor context it uses the in-cluster
// ServiceAccount. Otherwise it loads kubeconfig files as kubectl does:
// paths is a list separated like KUBECONFIG (missing files are skipped),
// falling back to $KUBECONFIG and ~/.kube/config when empty; context
// overrides the current context.
func kubeConfig(paths, context string) (*rest.Config, string, error) {
	if paths == "" && context == "" {
		cfg, err := rest.InClusterConfig()
		if err != nil {
			return nil, "", fmt.Errorf("not running in a cluster (%w); pass -kubeconfig or -context to run outside one", err)
		}
		return cfg, "in-cluster", nil
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if paths != "" {
		rules.Precedence = filepath.SplitList(paths)
	}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: context})
	raw, err := cc.RawConfig()
	if err != nil {
		return nil, "", err
	}
	name := context
	if name == "" {
		name = raw.CurrentContext
	}
	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, "", err
	}
	return cfg, name, nil
}

// loopback reports whether addr only accepts connections from this machine.
func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// secure adds headers every response carries: no sniffing, no framing,
// no referrer to links followed out of OpsTide.
func secure(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Referrer-Policy", "no-referrer")
		h.ServeHTTP(w, r)
	})
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
