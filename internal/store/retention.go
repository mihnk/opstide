package store

import (
	"context"
	"log/slog"
	"strconv"
	"time"
)

// PruneResult counts what a prune removed.
type PruneResult struct {
	Events, Runs, Operations, Intervals int64
}

// Prune removes what is older than before, then the oldest entries beyond
// maxEvents. Operations go only once their CronJob has been deleted before
// before; a live CronJob keeps its description however old it is.
func (s *Store) Prune(ctx context.Context, before time.Time, maxEvents int) (PruneResult, error) {
	var r PruneResult
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
		if err := exec(&r.Events, `DELETE FROM events WHERE seq IN (
	SELECT seq FROM events ORDER BY at DESC, seq DESC LIMIT -1 OFFSET ?)`, maxEvents); err != nil {
			return err
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
		removed := r.Events + r.Runs + r.Operations + r.Intervals
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

// Retention is what Maintain keeps.
type Retention struct {
	Days      int
	MaxEvents int
}

// Maintain writes the heartbeat every 15 s, checkpoints every 5 min and
// prunes every hour (and once at start) until ctx ends.
func (s *Store) Maintain(ctx context.Context, keep Retention, log *slog.Logger) {
	beat := time.NewTicker(15 * time.Second)
	checkpoint := time.NewTicker(5 * time.Minute)
	prune := time.NewTicker(time.Hour)
	defer beat.Stop()
	defer checkpoint.Stop()
	defer prune.Stop()

	doPrune := func() {
		r, err := s.Prune(ctx, s.now().AddDate(0, 0, -keep.Days), keep.MaxEvents)
		if err != nil {
			log.Warn("prune failed", "err", err)
			return
		}
		log.Info("pruned", "events", r.Events, "runs", r.Runs, "operations", r.Operations, "intervals", r.Intervals)
	}
	doPrune()
	for {
		select {
		case <-ctx.Done():
			return
		case <-beat.C:
			if err := s.Update(ctx, func(w *Writer) error { return w.Heartbeat(ctx, s.now()) }); err != nil {
				log.Warn("heartbeat failed", "err", err)
			}
		case <-checkpoint.C:
			if err := s.Checkpoint(ctx); err != nil {
				log.Warn("checkpoint failed", "err", err)
			}
		case <-prune.C:
			doPrune()
		}
	}
}
