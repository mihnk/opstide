// Package config reads OpsTide's settings from flags, falling back to
// OPSTIDE_* environment variables, then to defaults.
package config

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
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
	// MaxRoutineEvents is the budget for routine-volume Log entries (Pod lifecycle, Event entries, autoscaler entries,
	// reported counts, Job creation), apart from MaxEvents, so a burst of them cannot push out the rest. 0 means half
	// of MaxEvents.
	MaxRoutineEvents int
	// Kubeconfig and Context are for running outside the cluster during
	// development. Kubeconfig may list several files separated like
	// KUBECONFIG; they are merged as kubectl merges them. Both empty means
	// the in-cluster ServiceAccount.
	Kubeconfig string
	Context    string
	LogLevel   slog.Level
	// TokenFile is a file holding the shared token (a mounted Secret); Token
	// is the same token from the environment. Both empty turns the login off.
	TokenFile string
	Token     string
	// ClustrailURL is a link template for opening a CronJob in Clustrail:
	// {cluster}, {namespace} and {name} are replaced. Empty shows no link.
	ClustrailURL string
	// PublicURL is the address readers open OpsTide at (http or https, no query), used for links OpsTide hands out to
	// other tools (Grafana annotations). Empty: those links are paths, relative to wherever OpsTide is reached.
	PublicURL string
	// ReleaseWindowMinutes is how long after a rollout completes a release is
	// observed before it can be called healthy: 5, 10, 15 or 30.
	ReleaseWindowMinutes int
	// ObserveNodes watches Nodes (cluster-scoped: it needs cluster RBAC, and is off unless asked for).
	ObserveNodes bool
	// WatchConfigMaps records ConfigMaps being created, deleted and written (on by default). Values are never kept.
	WatchConfigMaps bool
	// WatchSecretMetadata records Secrets being created, deleted and written, through the metadata-only API (off by
	// default). Their data is never requested.
	WatchSecretMetadata bool
	// Demo serves a made-up history from a temporary database, with no cluster, kubeconfig or Docker. Nothing is
	// collected, and the UI says so on every page.
	Demo bool
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

	routineEvents, err := envInt("MAX_ROUTINE_EVENTS", 0)
	if err != nil {
		return Config{}, err
	}
	releaseWindow, err := envInt("RELEASE_WINDOW", 10)
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
	fs.IntVar(&c.MaxRoutineEvents, "max-routine-events", routineEvents, "most routine-volume log entries (Pod lifecycle, Event entries, autoscaler entries) to keep; 0 means half of max-events (OPSTIDE_MAX_ROUTINE_EVENTS)")
	fs.StringVar(&c.Kubeconfig, "kubeconfig", env("KUBECONFIG", ""), "kubeconfig file(s), separated like KUBECONFIG, for running outside the cluster (OPSTIDE_KUBECONFIG)")
	fs.StringVar(&c.Context, "context", env("CONTEXT", ""), "kubeconfig context to use; empty uses its current context (OPSTIDE_CONTEXT)")
	fs.StringVar(&c.TokenFile, "token-file", env("TOKEN_FILE", ""), "file holding the token that protects the API; empty means no login (OPSTIDE_TOKEN_FILE)")
	fs.IntVar(&c.ReleaseWindowMinutes, "release-window", releaseWindow, "minutes a release is observed after its rollout completes: 5, 10, 15 or 30 (OPSTIDE_RELEASE_WINDOW)")
	fs.BoolVar(&c.ObserveNodes, "observe-nodes", env("OBSERVE_NODES", "") == "true", "record Node conditions and scheduling changes; needs list and watch on nodes cluster-wide (OPSTIDE_OBSERVE_NODES=true)")
	fs.BoolVar(&c.WatchConfigMaps, "watch-configmaps", env("WATCH_CONFIGMAPS", "true") != "false", "record ConfigMaps being created, deleted and written; values are never kept; needs list and watch on configmaps (OPSTIDE_WATCH_CONFIGMAPS=false to turn off)")
	fs.BoolVar(&c.WatchSecretMetadata, "watch-secret-metadata", env("WATCH_SECRET_METADATA", "") == "true", "record Secrets being created, deleted and written, reading metadata only (never their data); needs list and watch on secrets (OPSTIDE_WATCH_SECRET_METADATA=true)")
	fs.BoolVar(&c.Demo, "demo", false, "serve a made-up history from a temporary database, with no cluster needed; nothing is collected")
	fs.StringVar(&c.ClustrailURL, "clustrail-url", env("CLUSTRAIL_URL", ""), "link template for 'Open in Clustrail', with {cluster}, {namespace}, {name}; empty shows no link (OPSTIDE_CLUSTRAIL_URL)")
	fs.StringVar(&c.PublicURL, "public-url", env("PUBLIC_URL", ""), "the address readers open OpsTide at, e.g. https://opstide.example.com, for links in Grafana annotations; empty gives paths (OPSTIDE_PUBLIC_URL)")
	fs.StringVar(&level, "log-level", env("LOG_LEVEL", "info"), "debug, info, warn or error (OPSTIDE_LOG_LEVEL)")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	c.Token = getenv("OPSTIDE_TOKEN")
	for _, ns := range strings.Split(namespaces, ",") {
		if ns = strings.TrimSpace(ns); ns != "" {
			c.Namespaces = append(c.Namespaces, ns)
		}
	}
	if err := c.LogLevel.UnmarshalText([]byte(level)); err != nil {
		return Config{}, fmt.Errorf("log level %q: use debug, info, warn or error", level)
	}
	c.PublicURL = strings.TrimRight(strings.TrimSpace(c.PublicURL), "/")
	if c.PublicURL != "" {
		u, err := url.Parse(c.PublicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return Config{}, fmt.Errorf("public url %q must be an http:// or https:// address with a host and no query, fragment or user", c.PublicURL)
		}
	}
	switch {
	case c.ReleaseWindowMinutes != 5 && c.ReleaseWindowMinutes != 10 && c.ReleaseWindowMinutes != 15 && c.ReleaseWindowMinutes != 30:
		return Config{}, errors.New("release window must be 5, 10, 15 or 30 minutes")
	case c.DataDir == "":
		return Config{}, errors.New("data dir is empty")
	case c.RetentionDays < 1:
		return Config{}, errors.New("retention must be at least 1 day")
	case c.MaxEvents < 1000:
		return Config{}, errors.New("max events must be at least 1000")
	case c.MaxRoutineEvents < 0 || c.MaxRoutineEvents > c.MaxEvents:
		return Config{}, errors.New("max routine events must be between 0 and max events")
	}
	if c.MaxRoutineEvents == 0 {
		c.MaxRoutineEvents = c.MaxEvents / 2
	}
	return c, nil
}
