package main

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/mihnk/opstide/internal/collector"
	"github.com/mihnk/opstide/internal/maintenance"
	"github.com/mihnk/opstide/internal/metrics"
	"github.com/mihnk/opstide/internal/store"
)

// maxOperationSeries bounds the per-CronJob series so a cluster with thousands of CronJobs cannot flood a scrape.
const maxOperationSeries = 1000

// ownMetrics describes OpsTide itself: is it ready, how fresh is its
// record, which watches fail, how big is the database. Reading the event
// count walks a table, so the storage figures are reused for 30 s.
func ownMetrics(db *store.Store, col *collector.Collector, version, cluster string, started time.Time, maint *maintenance.Set) metrics.Source {
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
	var (
		opMu    sync.Mutex
		opRead  time.Time
		opLast  []store.OperationLast
		opTotal int
	)
	// The per-CronJob times are reused for 30 s: finding them reads each CronJob's runs.
	operations := func(ctx context.Context) ([]store.OperationLast, int, error) {
		opMu.Lock()
		defer opMu.Unlock()
		if opRead.IsZero() || time.Since(opRead) > 30*time.Second {
			ops, err := db.OperationsLast(ctx, maxOperationSeries)
			if err != nil {
				return nil, 0, err
			}
			n, err := db.LiveOperationCount(ctx)
			if err != nil {
				return nil, 0, err
			}
			opLast, opTotal, opRead = ops, n, time.Now()
		}
		return opLast, opTotal, nil
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
		// One series per live CronJob (bounded), so an alert can say "the backup has not succeeded for 26 hours". OpsTide only
		// reports when it last saw a run end; the threshold, and whether OpsTide was watching (opstide_watches_failing, the
		// coverage in the UI), are the alert's to weigh.
		if ops, total, err := operations(ctx); err == nil {
			w.Gauge("opstide_operations", "Live CronJobs recorded (series below are limited to the first "+strconv.Itoa(maxOperationSeries)+").", float64(total))
			for _, o := range ops {
				// cronjob_namespace and cronjob, not namespace: a scrape through the Prometheus Operator renames a
				// "namespace" label to exported_namespace, which would break an alert written from these names.
				l := []string{"cronjob_namespace", o.Namespace, "cronjob", o.Name}
				w.Gauge("opstide_operation_suspended", "1 when the CronJob is suspended.", b2f(o.Suspended), l...)
				if !o.LastSuccessAt.IsZero() {
					w.Gauge("opstide_operation_last_success_timestamp_seconds", "When OpsTide last saw a run of the CronJob succeed, Unix time. Absent if it has seen none.", float64(o.LastSuccessAt.Unix()), l...)
				}
				if !o.LastFailureAt.IsZero() {
					w.Gauge("opstide_operation_last_failure_timestamp_seconds", "When OpsTide last saw a run of the CronJob fail, Unix time. Absent if it has seen none.", float64(o.LastFailureAt.Unix()), l...)
				}
				if !o.LastRunAt.IsZero() {
					w.Gauge("opstide_operation_last_run_timestamp_seconds", "When OpsTide last saw a Job of the CronJob start, Unix time. Absent if it has seen none.", float64(o.LastRunAt.Unix()), l...)
				}
			}
		}
		ev := cs.Evidence
		w.Counter("opstide_evidence_events_total", "Kubernetes Events first recorded, by origin (observed while watching, or adopted when already there).", float64(ev.Observed), "origin", "observed")
		w.Counter("opstide_evidence_events_total", "Kubernetes Events first recorded, by origin (observed while watching, or adopted when already there).", float64(ev.Adopted), "origin", "adopted")
		w.Counter("opstide_evidence_updates_total", "Writes that changed an Event already recorded (a count that grew, a changed message).", float64(ev.Updated))
		w.Counter("opstide_scaling_entries_total", "Scaling entries written, by type.", float64(ev.HPADesired), "type", "hpa_desired_replicas_changed")
		w.Counter("opstide_scaling_entries_total", "Scaling entries written, by type.", float64(ev.HPACondition), "type", "hpa_condition_changed")
		w.Counter("opstide_scaling_entries_total", "Scaling entries written, by type.", float64(ev.HPATarget), "type", "workload_replica_target_changed")
		// One series per configured maintenance window (at most 100), 1 while it is open, so an alert rule can hold back
		// during planned work: `unless on() opstide_maintenance_active{name="..."} == 1`. Declared, not observed.
		active := maint.Active(time.Now())
		for _, mw := range maint.Windows() {
			w.Gauge("opstide_maintenance_active", "1 while a maintenance window declared in OpsTide's configuration is open, else 0; the window's name is a label.", b2f(active[mw.Name]), "name", mw.Name)
		}
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
