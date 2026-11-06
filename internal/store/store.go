// Package store keeps OpsTide's record in SQLite.
//
// One process writes (the collector); the API reads. The database runs in
// WAL mode so readers never wait for the writer, and write transactions
// take the lock when they begin so they queue behind busy_timeout instead
// of failing halfway. Times are stored as UTC Unix milliseconds; an absent
// time is NULL, never zero.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"time"

	_ "modernc.org/sqlite" // database/sql driver "sqlite"
)

// Store is the SQLite-backed record.
type Store struct {
	db   *sql.DB
	path string
	now  func() time.Time
}

// Open opens (creating if needed) the database at path and migrates it to
// the current schema.
func Open(ctx context.Context, path string) (*Store, error) {
	q := url.Values{}
	for _, p := range []string{"journal_mode(WAL)", "busy_timeout(10000)", "foreign_keys(1)", "synchronous(NORMAL)"} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	s := &Store{db: db, path: path, now: time.Now}
	s.backupBeforeMigrating(ctx)
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return s, nil
}

// backupBeforeMigrating keeps a consistent copy of a database that is about to be migrated, as <path>.pre-v<N> (N is the
// schema it had). Migrations only go forward, so this is the way back to the earlier OpsTide. It is written once per
// version and never overwritten, and a failure (a full disk) is logged and does not stop OpsTide starting.
func (s *Store) backupBeforeMigrating(ctx context.Context) {
	v, err := s.SchemaVersion(ctx)
	if err != nil || v == 0 || v >= len(migrations) || s.path == "" {
		return
	}
	dst := fmt.Sprintf("%s.pre-v%d", s.path, v)
	if _, err := os.Stat(dst); err == nil {
		return
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", dst); err != nil {
		slog.Warn("could not back up the database before migrating it", "to", dst, "err", err)
		_ = os.Remove(dst)
		return
	}
	slog.Info("backed up the database before migrating it", "to", dst, "from schema", v, "to schema", len(migrations))
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Writable reports whether a write transaction can be started now. It is
// the storage readiness check.
func (s *Store) Writable(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	return tx.Rollback()
}

// SchemaVersion is the migration level of the open database.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}

// Update runs fn in one write transaction: everything it writes lands
// together or not at all.
func (s *Store) Update(ctx context.Context, fn func(w *Writer) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(&Writer{tx: tx, now: s.now}); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Writer writes inside a transaction started by Update.
type Writer struct {
	tx  *sql.Tx
	now func() time.Time
}

func ms(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.UnixMilli(), Valid: true}
}

func fromMS(n sql.NullInt64) time.Time {
	if !n.Valid {
		return time.Time{}
	}
	return time.UnixMilli(n.Int64).UTC()
}

func durS(d *time.Duration) sql.NullInt64 {
	if d == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*d / time.Second), Valid: true}
}

func fromDurS(n sql.NullInt64) *time.Duration {
	if !n.Valid {
		return nil
	}
	d := time.Duration(n.Int64) * time.Second
	return &d
}

// fileSize returns the size of path, or 0 when it does not exist.
func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) || err != nil {
		return 0
	}
	return fi.Size()
}
