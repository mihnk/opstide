// Package config reads OpsTide's settings from flags, falling back to
// OPSTIDE_* environment variables, then to defaults.
package config

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// Config is the collector's runtime configuration.
type Config struct {
	// Listen is the HTTP listen address for the UI, API and probes.
	Listen string
	// DataDir holds the SQLite database. It must be on a volume only this
	// process writes to.
	DataDir string
	// Namespaces limits collection to these namespaces. Empty means the
	// whole cluster, which needs cluster-wide RBAC.
	Namespaces []string
	// Cluster is the name shown in the UI.
	Cluster string
	// RetentionDays and MaxEvents bound what is kept; whichever is reached
	// first prunes the oldest entries.
	RetentionDays int
	MaxEvents     int
	// Kubeconfig is for running outside the cluster during development.
	// Empty means the in-cluster ServiceAccount.
	Kubeconfig string
	LogLevel   slog.Level
}

// Load parses args, using getenv for defaults (os.Getenv in main).
func Load(args []string, getenv func(string) string) (Config, error) {
	env := func(key, def string) string {
		if v := getenv("OPSTIDE_" + key); v != "" {
			return v
		}
		return def
	}
	envInt := func(key string, def int) (int, error) {
		v := getenv("OPSTIDE_" + key)
		if v == "" {
			return def, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("OPSTIDE_%s: %q is not a number", key, v)
		}
		return n, nil
	}

	retention, err := envInt("RETENTION_DAYS", 30)
	if err != nil {
		return Config{}, err
	}
	maxEvents, err := envInt("MAX_EVENTS", 2_000_000)
	if err != nil {
		return Config{}, err
	}

	var c Config
	var namespaces, level string
	fs := flag.NewFlagSet("opstide", flag.ContinueOnError)
	fs.StringVar(&c.Listen, "listen", env("LISTEN", ":8080"), "HTTP listen address (OPSTIDE_LISTEN)")
	fs.StringVar(&c.DataDir, "data-dir", env("DATA_DIR", "/data"), "directory for the database (OPSTIDE_DATA_DIR)")
	fs.StringVar(&namespaces, "namespaces", env("NAMESPACES", ""), "comma-separated namespaces to watch; empty watches all (OPSTIDE_NAMESPACES)")
	fs.StringVar(&c.Cluster, "cluster", env("CLUSTER", "cluster"), "cluster name shown in the UI (OPSTIDE_CLUSTER)")
	fs.IntVar(&c.RetentionDays, "retention-days", retention, "days of history to keep (OPSTIDE_RETENTION_DAYS)")
	fs.IntVar(&c.MaxEvents, "max-events", maxEvents, "most log entries to keep (OPSTIDE_MAX_EVENTS)")
	fs.StringVar(&c.Kubeconfig, "kubeconfig", env("KUBECONFIG", ""), "kubeconfig for running outside the cluster (OPSTIDE_KUBECONFIG)")
	fs.StringVar(&level, "log-level", env("LOG_LEVEL", "info"), "debug, info, warn or error (OPSTIDE_LOG_LEVEL)")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	for _, ns := range strings.Split(namespaces, ",") {
		if ns = strings.TrimSpace(ns); ns != "" {
			c.Namespaces = append(c.Namespaces, ns)
		}
	}
	if err := c.LogLevel.UnmarshalText([]byte(level)); err != nil {
		return Config{}, fmt.Errorf("log level %q: use debug, info, warn or error", level)
	}
	switch {
	case c.DataDir == "":
		return Config{}, errors.New("data dir is empty")
	case c.RetentionDays < 1:
		return Config{}, errors.New("retention must be at least 1 day")
	case c.MaxEvents < 1000:
		return Config{}, errors.New("max events must be at least 1000")
	}
	return c, nil
}
