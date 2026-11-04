package store

import (
	"context"
	"database/sql"
	"time"
)

// Interval is a stretch of time during which OpsTide was watching scope
// continuously. Time not inside an interval is a gap: entries from it may
// be missing.
type Interval struct {
	ID        int64
	Scope     string // "jobs", or "jobs/payments" when watched per namespace
	State     string // healthy | partial
	StartedAt time.Time
	EndedAt   time.Time // zero while still open
	EndReason string
}

// Coverage states.
const (
	Healthy = "healthy"
	// Partial: watching, but some of the scope cannot be read (a namespace
	// the ServiceAccount may not list).
	Partial = "partial"
)

// OpenInterval starts an interval for scope at at, closing any interval
// still open for it there first.
func (w *Writer) OpenInterval(ctx context.Context, scope, state string, at time.Time) error {
	if err := w.CloseInterval(ctx, scope, at, "superseded"); err != nil {
		return err
	}
	_, err := w.tx.ExecContext(ctx, `INSERT INTO coverage (scope, state, started_at) VALUES (?,?,?)`, scope, state, ms(at).Int64)
	return err
}

// CloseInterval ends scope's open interval at at, if there is one.
func (w *Writer) CloseInterval(ctx context.Context, scope string, at time.Time, reason string) error {
	_, err := w.tx.ExecContext(ctx, `UPDATE coverage SET ended_at = max(started_at, ?), end_reason = ?
WHERE scope = ? AND ended_at IS NULL`, ms(at).Int64, reason, scope)
	return err
}

// ReopenInterval undoes the last close of scope's interval: the watch
// resumed from where it stopped, so nothing was missed after all. It
// reports whether there was an interval to reopen.
func (w *Writer) ReopenInterval(ctx context.Context, scope string) (bool, error) {
	res, err := w.tx.ExecContext(ctx, `UPDATE coverage SET ended_at = NULL, end_reason = ''
WHERE id = (SELECT id FROM coverage WHERE scope = ? ORDER BY started_at DESC, id DESC LIMIT 1)
	AND ended_at IS NOT NULL
	AND NOT EXISTS (SELECT 1 FROM coverage WHERE scope = ? AND ended_at IS NULL)`, scope, scope)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// Coverage returns the intervals that overlap [from, to), by scope and
// start time.
func (s *Store) Coverage(ctx context.Context, from, to time.Time) ([]Interval, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, scope, state, started_at, ended_at, end_reason FROM coverage
WHERE started_at < ? AND (ended_at IS NULL OR ended_at > ?) ORDER BY scope, started_at`, ms(to).Int64, ms(from).Int64)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Interval
	for rows.Next() {
		var iv Interval
		var started int64
		var ended sql.NullInt64
		if err := rows.Scan(&iv.ID, &iv.Scope, &iv.State, &started, &ended, &iv.EndReason); err != nil {
			return nil, err
		}
		iv.StartedAt, iv.EndedAt = time.UnixMilli(started).UTC(), fromMS(ended)
		out = append(out, iv)
	}
	return out, rows.Err()
}

// CloseAfterRestart ends every interval left open by a previous process
// at its last heartbeat: OpsTide cannot know what happened after that.
// It returns how many it closed.
func (w *Writer) CloseAfterRestart(ctx context.Context) (int, error) {
	last, err := w.LastHeartbeat(ctx)
	if err != nil || last.IsZero() {
		return 0, err
	}
	res, err := w.tx.ExecContext(ctx, `UPDATE coverage SET ended_at = max(started_at, ?), end_reason = 'collector stopped'
WHERE ended_at IS NULL`, ms(last).Int64)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
