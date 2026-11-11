package store

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// PruneResult counts what a prune removed.
type PruneResult struct {
	Events, Runs, Operations, Intervals, Releases int64
	// Capped counts what the size budgets (not age) removed, by source: "routine" Log entries, "log" entries and
	// "evidence" (Kubernetes Events). They are also in the totals above.
	Capped map[string]int64
}

// A Log entry is routine-volume when a burst of them (a crash loop, a node upgrade) should not be able to push out the
// rest: Pod created/ready/terminated (restarts, OOM kills and evictions are findings' evidence and stay with the rest), Kubernetes Event entries (the Events themselves are kept apart), autoscaler entries, reported
// counts, Job creation, Helm's release records (ConfigMaps and Secrets named sh.helm.release.v1.*, one set per
// upgrade) and the folded writes of a ConfigMap or Secret written again and again (a controller's status). They have
// their own budget; everything else shares the rest.
var routineEntries = `(substr(type, 1, 4) IN ('evid', 'hpa.') OR type IN ('` + strings.Join(routineBudgetTypes, "', '") + `'))`

// routineBudgetTypes are the exact types (besides the evid* and hpa.* prefixes) budgeted as routine volume.
var routineBudgetTypes = append([]string{"pod.created", "pod.ready", "pod.terminated", "job.created", "statefulset.counts_changed", "daemonset.counts_changed"}, ConfigRoutineTypes...)

// RetentionSourceOf names the retention source ("routine" or "log") whose budget a Log entry of this type counts
// against, the same rule as routineEntries: what a budget removed of that source says whether entries of this type
// may be missing.
func RetentionSourceOf(typ string) string {
	if strings.HasPrefix(typ, "evid") || strings.HasPrefix(typ, "hpa.") {
		return "routine"
	}
	for _, t := range routineBudgetTypes {
		if t == typ {
			return "routine"
		}
	}
	return "log"
}

// Prune removes what is older than before, then the oldest entries beyond
// maxEvents. Operations go only once their CronJob has been deleted before
// before; a live CronJob keeps its description however old it is.
func (s *Store) Prune(ctx context.Context, before time.Time, maxEvents int) (PruneResult, error) {
	return s.PruneBudgets(ctx, before, Retention{MaxEvents: maxEvents, MaxRoutine: maxEvents})
}

// PruneBudgets is Prune with a separate budget for routine-volume Log entries. When a budget (not age) removes
// something, the source and the count are kept, so the history OpsTide still holds can say it was cut short.
func (s *Store) PruneBudgets(ctx context.Context, before time.Time, keep Retention) (PruneResult, error) {
	r := PruneResult{Capped: map[string]int64{}}
	maxEvents := keep.MaxEvents
	cut := before.UnixMilli()
	err := s.Update(ctx, func(w *Writer) error {
		exec := func(n *int64, q string, args ...any) error {
			res, err := w.tx.ExecContext(ctx, q, args...)
			if err != nil {
				return err
			}
			c, err := res.RowsAffected()
			*n += c
			return err
		}
		if err := exec(&r.Events, `DELETE FROM events WHERE at < ?`, cut); err != nil {
			return err
		}
		if keep.MaxRoutine > 0 && keep.MaxRoutine < maxEvents {
			var n int64
			if err := exec(&n, `DELETE FROM events WHERE seq IN (
	SELECT seq FROM events WHERE `+routineEntries+` ORDER BY at DESC, seq DESC LIMIT -1 OFFSET ?)`, keep.MaxRoutine); err != nil { //nolint:gosec // constant clause
				return err
			}
			r.Events += n
			r.Capped["routine"] += n
		}
		// The overall budget can still reach routine entries (when they share it, or are newer than what is left of
		// the rest); each removed entry is credited to its own source, so System says which history was cut short. The
		// newest entry past the budget is found once; it and everything older go.
		var cutAt, cutSeq int64
		switch err := w.tx.QueryRowContext(ctx, `SELECT at, seq FROM events ORDER BY at DESC, seq DESC LIMIT 1 OFFSET ?`, maxEvents).Scan(&cutAt, &cutSeq); {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		default:
			const past = `(at < ? OR (at = ? AND seq <= ?))`
			var capRoutine int64
			if err := w.tx.QueryRowContext(ctx, `SELECT count(*) FROM events WHERE `+routineEntries+` AND `+past, cutAt, cutAt, cutSeq).Scan(&capRoutine); err != nil { //nolint:gosec // constant clauses
				return err
			}
			var capAll int64
			if err := exec(&capAll, `DELETE FROM events WHERE `+past, cutAt, cutAt, cutSeq); err != nil {
				return err
			}
			r.Events += capAll
			r.Capped["routine"] += capRoutine
			r.Capped["log"] += capAll - capRoutine
		}
		if err := exec(&r.Runs, `DELETE FROM runs WHERE coalesce(finished_at, created_at) < ? AND status IN ('succeeded', 'failed', 'unknown')`, cut); err != nil {
			return err
		}
		if err := exec(&r.Operations, `DELETE FROM operations WHERE deleted_at IS NOT NULL AND deleted_at < ?`, cut); err != nil {
			return err
		}
		if err := exec(&r.Intervals, `DELETE FROM coverage WHERE ended_at IS NOT NULL AND ended_at < ?`, cut); err != nil {
			return err
		}
		// Releases keep their history past Kubernetes garbage collection but not
		// past retention: the newest release of each workload always stays, so a
		// quiet Deployment is never left with nothing, and its Pods go with the
		// releases that are removed. A finished Pod record also ends with retention.
		if err := exec(&r.Releases, `DELETE FROM releases WHERE observed_at < ? AND id NOT IN (
	SELECT id FROM releases r2 WHERE r2.revision_num = (SELECT max(revision_num) FROM releases r3 WHERE r3.workload_id = r2.workload_id))`, cut); err != nil {
			return err
		}
		if err := exec(&r.Releases, `DELETE FROM release_pods WHERE (terminated_at IS NOT NULL AND terminated_at < ?)
	OR (kind = 'Deployment' AND replicaset_uid NOT IN (SELECT replicaset_uid FROM releases))
	OR (kind <> 'Deployment' AND workload_id NOT IN (SELECT id FROM workloads))`, cut); err != nil {
			return err
		}
		// ControllerRevision history of StatefulSets and DaemonSets: kept past
		// Kubernetes garbage collection, not past retention; the newest revision of a
		// workload that still exists stays. Revisions of a workload that was deleted
		// before the cut go with it, as do Job-Pod ownership and Nodes.
		if err := exec(&r.Releases, `DELETE FROM workload_revisions WHERE (observed_at < ? AND id NOT IN (
	SELECT id FROM workload_revisions v2 WHERE v2.revision_num = (SELECT max(revision_num) FROM workload_revisions v3 WHERE v3.workload_id = v2.workload_id)
	AND v2.workload_id IN (SELECT id FROM workloads WHERE deleted_at IS NULL)))
	OR workload_id IN (SELECT id FROM workloads WHERE deleted_at IS NOT NULL AND deleted_at < ?)`, cut, cut); err != nil {
			return err
		}
		if err := exec(&r.Releases, `DELETE FROM pod_owners WHERE first_seen_at < ?`, cut); err != nil {
			return err
		}
		if err := exec(&r.Releases, `DELETE FROM nodes WHERE deleted_at IS NOT NULL AND deleted_at < ?`, cut); err != nil {
			return err
		}
		// Evidence follows retention too, and is capped like the log: the oldest
		// go first. Autoscalers that were deleted go with it.
		if err := exec(&r.Events, `DELETE FROM evidence WHERE at < ?`, cut); err != nil {
			return err
		}
		var capEv int64
		if err := exec(&capEv, `DELETE FROM evidence WHERE id IN (
	SELECT id FROM evidence ORDER BY at DESC, id DESC LIMIT -1 OFFSET ?)`, maxEvents); err != nil {
			return err
		}
		r.Events += capEv
		r.Capped["evidence"] += capEv
		if err := exec(&r.Releases, `DELETE FROM hpas WHERE deleted_at IS NOT NULL AND deleted_at < ?`, cut); err != nil {
			return err
		}
		if err := exec(&r.Releases, `DELETE FROM workloads WHERE deleted_at IS NOT NULL AND deleted_at < ?
	AND id NOT IN (SELECT workload_id FROM releases) AND id NOT IN (SELECT workload_id FROM workload_revisions)`, cut); err != nil {
			return err
		}
		if err := exec(&r.Releases, `DELETE FROM workload_state WHERE workload_id NOT IN (SELECT id FROM workloads)`); err != nil {
			return err
		}
		// ConfigMaps and Secrets deleted before the cut, and references that ended before it, go with retention. A
		// reference of a workload whose own record is gone (pruned) and that OpsTide has not confirmed since the cut goes
		// too: nothing is left to show it against.
		if err := exec(&r.Releases, `DELETE FROM config_objects WHERE deleted_at IS NOT NULL AND deleted_at < ?`, cut); err != nil {
			return err
		}
		if err := exec(&r.Releases, `DELETE FROM config_refs WHERE (ended_at IS NOT NULL AND ended_at < ?)
	OR (last_seen_at < ? AND workload_id NOT IN (SELECT id FROM workloads) AND workload_id NOT IN (SELECT id FROM operations)
		AND (workload_kind <> 'Deployment' OR ended_at IS NOT NULL))`, cut, cut); err != nil {
			return err
		}
		for src, n := range r.Capped {
			if n == 0 {
				continue
			}
			if err := w.setMeta(ctx, "cap_pruned_at_"+src, strconv.FormatInt(s.now().UnixMilli(), 10)); err != nil {
				return err
			}
			var total int64
			var v string
			if err := w.tx.QueryRowContext(ctx, `SELECT coalesce((SELECT value FROM meta WHERE key = ?), '0')`, "cap_pruned_total_"+src).Scan(&v); err != nil {
				return err
			}
			total, _ = strconv.ParseInt(v, 10, 64)
			if err := w.setMeta(ctx, "cap_pruned_total_"+src, strconv.FormatInt(total+n, 10)); err != nil {
				return err
			}
		}
		removed := r.Events + r.Runs + r.Operations + r.Intervals + r.Releases
		if err := w.setMeta(ctx, "last_prune", strconv.FormatInt(s.now().UnixMilli(), 10)); err != nil {
			return err
		}
		return w.setMeta(ctx, "last_prune_removed", strconv.FormatInt(removed, 10))
	})
	return r, err
}

// Checkpoint copies the WAL into the database and truncates it, so the
// WAL does not grow without bound under constant reads.
func (s *Store) Checkpoint(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	if err == nil {
		err = s.Update(ctx, func(w *Writer) error {
			return w.setMeta(ctx, "last_checkpoint", strconv.FormatInt(s.now().UnixMilli(), 10))
		})
	}
	return err
}

// Stats describes the database for the System screen.
type Stats struct {
	SchemaVersion    int
	Bytes            int64 // database file
	WALBytes         int64
	Events           int64
	OldestEvent      time.Time
	LastPrune        time.Time
	LastPruneRemoved int64
	LastCheckpoint   time.Time
}

// Stats reads the database's size and contents.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	var err error
	if st.SchemaVersion, err = s.SchemaVersion(ctx); err != nil {
		return st, err
	}
	var oldest *int64
	if err := s.db.QueryRowContext(ctx, `SELECT count(*), min(at) FROM events`).Scan(&st.Events, &oldest); err != nil {
		return st, err
	}
	if oldest != nil {
		st.OldestEvent = time.UnixMilli(*oldest).UTC()
	}
	if st.LastPrune, err = metaTime(ctx, s.db, "last_prune"); err != nil {
		return st, err
	}
	if st.LastCheckpoint, err = metaTime(ctx, s.db, "last_checkpoint"); err != nil {
		return st, err
	}
	var removed string
	if err := s.db.QueryRowContext(ctx, `SELECT coalesce((SELECT value FROM meta WHERE key = 'last_prune_removed'), '0')`).Scan(&removed); err != nil {
		return st, err
	}
	st.LastPruneRemoved, _ = strconv.ParseInt(removed, 10, 64)
	st.Bytes, st.WALBytes = fileSize(s.path), fileSize(s.path+"-wal")
	return st, nil
}

// Analyze refreshes the statistics SQLite plans queries with. Without them it
// takes an index for a window that is most of the table and reads every row
// twice. A sample of each index is enough, so it takes well under a second
// on millions of rows.
func (s *Store) Analyze(ctx context.Context) error {
	conn, err := s.db.Conn(ctx) // the limit is per connection
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA analysis_limit = 2000`); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `ANALYZE`)
	return err
}

// Retention is what Maintain keeps.
type Retention struct {
	Days      int
	MaxEvents int
	// MaxRoutine is the budget of routine-volume Log entries (Pod lifecycle, Event entries, autoscaler entries, reported
	// counts, Job creation); 0 or MaxEvents means they share the one budget.
	MaxRoutine int
}

// RetentionSource is how much history one source still holds and whether a size budget has cut it short.
type RetentionSource struct {
	Name          string // routine | log | evidence
	RetainedSince time.Time
	CappedAt      time.Time // when a budget (not age) last removed something; zero if never
	CappedTotal   int64     // how many records budgets have removed since the database was made
}

// RetentionSources reports, per source, the oldest record held and what budgets have removed.
func (s *Store) RetentionSources(ctx context.Context) ([]RetentionSource, error) {
	oldest := func(q string) (time.Time, error) {
		var at *int64
		if err := s.db.QueryRowContext(ctx, q).Scan(&at); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return time.Time{}, nil // nothing of this source is held
			}
			return time.Time{}, err
		}
		if at == nil {
			return time.Time{}, nil
		}
		return time.UnixMilli(*at).UTC(), nil
	}
	var out []RetentionSource
	for _, src := range []struct{ name, q string }{
		{"log", `SELECT at FROM events WHERE NOT ` + routineEntries + ` ORDER BY at LIMIT 1`},
		{"routine", `SELECT at FROM events WHERE ` + routineEntries + ` ORDER BY at LIMIT 1`},
		{"evidence", `SELECT ` + evidenceKey + ` FROM evidence ORDER BY ` + evidenceKey + ` LIMIT 1`},
	} {
		rs := RetentionSource{Name: src.name}
		var err error
		if rs.RetainedSince, err = oldest(src.q); err != nil { //nolint:gosec // constant clauses
			return nil, err
		}
		if rs.CappedAt, err = metaTime(ctx, s.db, "cap_pruned_at_"+src.name); err != nil {
			return nil, err
		}
		var v string
		if err := s.db.QueryRowContext(ctx, `SELECT coalesce((SELECT value FROM meta WHERE key = ?), '0')`, "cap_pruned_total_"+src.name).Scan(&v); err != nil {
			return nil, err
		}
		rs.CappedTotal, _ = strconv.ParseInt(v, 10, 64)
		out = append(out, rs)
	}
	return out, nil
}

// Maintain writes the heartbeat at once and every 15 s, checkpoints every
// 5 min and prunes (then refreshes the query statistics) at once and every hour until ctx ends. A crash loses at
// most the last 15 s of coverage: it ends at the last heartbeat.
func (s *Store) Maintain(ctx context.Context, keep Retention, log *slog.Logger) {
	beat := time.NewTicker(15 * time.Second)
	checkpoint := time.NewTicker(5 * time.Minute)
	prune := time.NewTicker(time.Hour)
	defer beat.Stop()
	defer checkpoint.Stop()
	defer prune.Stop()

	doPrune := func() {
		r, err := s.PruneBudgets(ctx, s.now().AddDate(0, 0, -keep.Days), keep)
		switch {
		case err != nil && ctx.Err() == nil:
			log.Warn("prune failed", "err", err)
		case err == nil:
			log.Info("pruned", "events", r.Events, "runs", r.Runs, "operations", r.Operations, "intervals", r.Intervals)
			for src, n := range r.Capped {
				if n > 0 {
					log.Warn("a size budget, not age, removed records; the history for this source starts later than the retention days say", "source", src, "removed", n)
				}
			}
		}
		// Whether or not the prune worked: the planner needs fresh statistics.
		if err := s.Analyze(ctx); err != nil && ctx.Err() == nil {
			log.Warn("analyze failed", "err", err)
		}
	}
	heartbeat := func() {
		if err := s.Update(ctx, func(w *Writer) error { return w.Heartbeat(ctx, s.now()) }); err != nil && ctx.Err() == nil {
			log.Warn("heartbeat failed", "err", err)
		}
	}
	heartbeat()
	doPrune()
	for {
		select {
		case <-ctx.Done():
			return
		case <-beat.C:
			heartbeat()
		case <-checkpoint.C:
			if err := s.Checkpoint(ctx); err != nil && ctx.Err() == nil {
				log.Warn("checkpoint failed", "err", err)
			}
		case <-prune.C:
			doPrune()
		}
	}
}
