package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

func (w *Writer) setMeta(ctx context.Context, key, value string) error {
	_, err := w.tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func metaTime(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, key string) (time.Time, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(n).UTC(), nil
}

// Heartbeat records that the collector was alive and watching at at.
func (w *Writer) Heartbeat(ctx context.Context, at time.Time) error {
	return w.setMeta(ctx, "heartbeat", strconv.FormatInt(at.UnixMilli(), 10))
}

// LastHeartbeat is the last time the collector recorded itself alive.
func (w *Writer) LastHeartbeat(ctx context.Context) (time.Time, error) {
	return metaTime(ctx, w.tx, "heartbeat")
}

// LastWrite is the most recent heartbeat, as readers see it.
func (s *Store) LastWrite(ctx context.Context) (time.Time, error) {
	return metaTime(ctx, s.db, "heartbeat")
}

// Version changes whenever the record does: a new log entry, a run or an
// operation written. Live views compare it to know when to refetch.
func (s *Store) Version(ctx context.Context) (string, error) {
	var seq, runs, ops, rel, pods, ev, hpa, wl, nodes, revs, refs int64
	err := s.db.QueryRowContext(ctx, `SELECT
	(SELECT coalesce(max(seq), 0) FROM events),
	(SELECT coalesce(max(updated_at), 0) FROM runs),
	(SELECT coalesce(max(last_seen_at), 0) + coalesce(max(deleted_at), 0) FROM operations),
	(SELECT coalesce(max(updated_at), 0) FROM releases),
	(SELECT coalesce(max(updated_at), 0) FROM release_pods),
	(SELECT coalesce(max(updated_at), 0) FROM evidence),
	(SELECT coalesce(max(updated_at), 0) FROM hpas),
	(SELECT coalesce(max(updated_at), 0) FROM workload_state),
	(SELECT coalesce(max(updated_at), 0) FROM nodes),
	(SELECT coalesce(max(updated_at), 0) FROM workload_revisions),
	(SELECT count(*) + coalesce(max(first_seen_at), 0) + coalesce(max(ended_at), 0) FROM config_refs)`).Scan(&seq, &runs, &ops, &rel, &pods, &ev, &hpa, &wl, &nodes, &revs, &refs)
	f := func(n int64) string { return strconv.FormatInt(n, 10) }
	return f(seq) + "." + f(runs) + "." + f(ops) + "." + f(rel) + "." + f(pods) + "." + f(ev) + "." + f(hpa) + "." + f(wl) + "." + f(nodes) + "." + f(revs) + "." + f(refs), err
}

// SetMeta stores a small value under key.
func (w *Writer) SetMeta(ctx context.Context, key, value string) error {
	return w.setMeta(ctx, key, value)
}

// LastWorkloadEvent is when the collector last wrote a release or Pod entry.
func (s *Store) LastWorkloadEvent(ctx context.Context) (time.Time, error) {
	return metaTime(ctx, s.db, "last_workload_event")
}
