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
