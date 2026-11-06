package main

import (
	"context"
	"sync"
	"time"

	"github.com/mihnk/opstide/internal/collector"
	"github.com/mihnk/opstide/internal/metrics"
	"github.com/mihnk/opstide/internal/store"
)

// ownMetrics describes OpsTide itself: is it ready, how fresh is its
// record, which watches fail, how big is the database. Reading the event
// count walks a table, so the storage figures are reused for 30 s.
func ownMetrics(db *store.Store, col *collector.Collector, version, cluster string, started time.Time) metrics.Source {
	var (
		mu     sync.Mutex
		stats  store.Stats
		read   time.Time
		failed bool
	)
	storage := func(ctx context.Context) (store.Stats, bool) {
		mu.Lock()
		defer mu.Unlock()
		if read.IsZero() || time.Since(read) > 30*time.Second {
			st, err := db.Stats(ctx)
			stats, failed, read = st, err != nil, time.Now()
		}
		return stats, !failed
	}
	return func(ctx context.Context, w *metrics.Writer) {
		b2f := func(b bool) float64 {
			if b {
				return 1
			}
			return 0
		}
		cs := col.Stats()
		w.Gauge("opstide_build_info", "Always 1; the version and cluster name are labels.", 1, "version", version, "cluster", cluster)
		w.Gauge("opstide_ready", "1 when every watch has synced and storage is writable.", b2f(cs.Synced && db.Writable(ctx) == nil))
		w.Gauge("opstide_start_time_seconds", "When this process started, Unix time.", float64(started.Unix()))
		if t, err := db.LastWrite(ctx); err == nil && !t.IsZero() {
			w.Gauge("opstide_last_heartbeat_timestamp_seconds", "When the collector last recorded itself alive, Unix time. Far behind now means it is stuck.", float64(t.Unix()))
		}
		w.Gauge("opstide_watches_failing", "Scopes that cannot be watched right now, usually for lack of permission.", float64(cs.Failing))
		w.Counter("opstide_collector_items_total", "Objects written to the record.", float64(cs.Processed))
		w.Counter("opstide_collector_failures_total", "Tries to write an object that failed.", float64(cs.Failed))
		w.Counter("opstide_collector_dropped_total", "Objects OpsTide gave up recording after repeated failures; each is a recorded gap in coverage.", float64(cs.Dropped))
		w.Gauge("opstide_collector_queue_length", "Objects waiting to be written.", float64(cs.Queued))
		wl := cs.Workload
		w.Counter("opstide_releases_observed_total", "Releases (Deployment revisions) OpsTide saw appear.", float64(wl.Releases))
		w.Counter("opstide_release_assessments_total", "Post-deployment verdicts reached, by verdict.", float64(wl.Healthy), "verdict", "healthy")
		w.Counter("opstide_release_assessments_total", "Post-deployment verdicts reached, by verdict.", float64(wl.Degraded), "verdict", "degraded")
		w.Counter("opstide_release_assessments_total", "Post-deployment verdicts reached, by verdict.", float64(wl.Insufficient), "verdict", "insufficient_evidence")
		for _, t := range []string{"created", "ready", "notready", "restarted", "oomkilled", "waiting", "evicted", "terminated"} {
			w.Counter("opstide_pod_entries_total", "Deployment Pod lifecycle entries written, by type.", float64(wl.PodEvents[t]), "type", t)
		}
		if !wl.LastEvent.IsZero() {
			w.Gauge("opstide_workload_last_entry_timestamp_seconds", "When a release or Pod entry was last written, Unix time.", float64(wl.LastEvent.Unix()))
		}
		if n, err := db.ActiveWindows(ctx, time.Now()); err == nil {
			w.Gauge("opstide_release_windows_active", "Releases being observed after their rollout.", float64(n))
		}
		ev := cs.Evidence
		w.Counter("opstide_evidence_events_total", "Kubernetes Events first recorded, by origin (observed while watching, or adopted when already there).", float64(ev.Observed), "origin", "observed")
		w.Counter("opstide_evidence_events_total", "Kubernetes Events first recorded, by origin (observed while watching, or adopted when already there).", float64(ev.Adopted), "origin", "adopted")
		w.Counter("opstide_evidence_updates_total", "Writes that changed an Event already recorded (a count that grew, a changed message).", float64(ev.Updated))
		w.Counter("opstide_scaling_entries_total", "Scaling entries written, by type.", float64(ev.HPADesired), "type", "hpa_desired_replicas_changed")
		w.Counter("opstide_scaling_entries_total", "Scaling entries written, by type.", float64(ev.HPACondition), "type", "hpa_condition_changed")
		w.Counter("opstide_scaling_entries_total", "Scaling entries written, by type.", float64(ev.HPATarget), "type", "workload_replica_target_changed")
		if wc, err := db.WorkloadCounts(ctx); err == nil {
			for _, k := range []string{"Deployment", "StatefulSet", "DaemonSet"} {
				w.Gauge("opstide_workloads", "Live workloads recorded, by kind.", float64(wc[k]), "kind", k)
			}
		}
		if nodes, err := db.LiveNodes(ctx); err == nil {
			w.Gauge("opstide_nodes", "Live Nodes recorded (0 unless Node observation is on).", float64(len(nodes)))
		}
		if es, err := db.EvidenceStats(ctx); err == nil {
			w.Gauge("opstide_evidence_records", "Kubernetes Event records kept.", float64(es.Records))
		}
		if st, ok := storage(ctx); ok {
			w.Gauge("opstide_store_events", "Log entries kept.", float64(st.Events))
			w.Gauge("opstide_store_bytes", "Size of the database file.", float64(st.Bytes))
			w.Gauge("opstide_store_wal_bytes", "Size of the write-ahead log next to it.", float64(st.WALBytes))
			if !st.OldestEvent.IsZero() {
				w.Gauge("opstide_store_oldest_event_timestamp_seconds", "Time of the oldest log entry kept, Unix time.", float64(st.OldestEvent.Unix()))
			}
		}
	}
}
