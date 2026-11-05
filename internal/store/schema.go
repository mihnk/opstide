package store

import (
	"context"
	"fmt"
)

// migrations[i] moves the schema from version i to i+1. Never edit one
// that has shipped; add a new one.
var migrations = []string{
	`
CREATE TABLE operations (
	id                  TEXT PRIMARY KEY,   -- the CronJob's UID
	namespace           TEXT NOT NULL,
	name                TEXT NOT NULL,
	display_name        TEXT NOT NULL DEFAULT '',
	description         TEXT NOT NULL DEFAULT '',
	owner               TEXT NOT NULL DEFAULT '',
	criticality         TEXT NOT NULL DEFAULT '',
	runbook_url         TEXT NOT NULL DEFAULT '',
	expected_duration_s INTEGER,
	metadata_problems   TEXT NOT NULL DEFAULT '[]',
	schedule            TEXT NOT NULL,
	time_zone           TEXT NOT NULL DEFAULT '',
	suspend             INTEGER NOT NULL DEFAULT 0,
	concurrency_policy  TEXT NOT NULL DEFAULT 'Allow',
	starting_deadline_s INTEGER,
	successful_history  INTEGER NOT NULL DEFAULT 3,
	failed_history      INTEGER NOT NULL DEFAULT 1,
	created_at          INTEGER NOT NULL,   -- the CronJob's creationTimestamp
	first_seen_at       INTEGER NOT NULL,
	last_seen_at        INTEGER NOT NULL,
	deleted_at          INTEGER
);
CREATE INDEX operations_ns_name ON operations(namespace, name);

CREATE TABLE runs (
	id              TEXT PRIMARY KEY,       -- the Job's UID
	operation_id    TEXT NOT NULL DEFAULT '',  -- owning CronJob's UID, '' when none
	namespace       TEXT NOT NULL,
	job_name        TEXT NOT NULL,
	scheduled_at    INTEGER,
	scheduled_basis TEXT NOT NULL DEFAULT '',
	created_at      INTEGER NOT NULL,
	started_at      INTEGER,
	finished_at     INTEGER,
	status          TEXT NOT NULL,
	status_reason   TEXT NOT NULL DEFAULT '',
	attempts        INTEGER NOT NULL DEFAULT 0,
	pods            TEXT NOT NULL DEFAULT '[]',
	observed_at     INTEGER NOT NULL,
	updated_at      INTEGER NOT NULL
);
CREATE INDEX runs_operation_time ON runs(operation_id, created_at);

CREATE TABLE events (
	seq           INTEGER PRIMARY KEY AUTOINCREMENT,
	id            TEXT NOT NULL UNIQUE,     -- deterministic from the source occurrence
	type          TEXT NOT NULL,
	namespace     TEXT NOT NULL DEFAULT '',
	kind          TEXT NOT NULL DEFAULT '',
	resource_uid  TEXT NOT NULL DEFAULT '',
	resource_name TEXT NOT NULL DEFAULT '',
	operation_id  TEXT NOT NULL DEFAULT '',
	run_id        TEXT NOT NULL DEFAULT '',
	occurred_at   INTEGER,                  -- the source's own time; NULL when it has none
	observed_at   INTEGER NOT NULL,
	recorded_at   INTEGER NOT NULL,
	at            INTEGER NOT NULL,         -- occurred_at, else observed_at: the sort key
	severity      TEXT NOT NULL,
	confidence    TEXT NOT NULL,
	summary       TEXT NOT NULL,
	facts         TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX events_at ON events(at, seq);
CREATE INDEX events_operation ON events(operation_id, at);

CREATE TABLE coverage (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	scope      TEXT NOT NULL,              -- kind, or kind/namespace
	state      TEXT NOT NULL,              -- healthy | partial
	started_at INTEGER NOT NULL,
	ended_at   INTEGER,
	end_reason TEXT NOT NULL DEFAULT ''
);
CREATE INDEX coverage_scope ON coverage(scope, started_at);

CREATE TABLE meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`,
	// 2: the Log filters by kind and severity, and every view asks for the
	// suspends and resumes of all CronJobs at once; without these each of
	// them walks the whole table.
	`
CREATE INDEX events_type_at ON events(type, at);
CREATE INDEX events_severity_at ON events(severity, at);
-- Runs in a window are found by when they were created; the few created
-- before it that still matter (active, finished inside it, lost) by these.
CREATE INDEX runs_created ON runs(created_at, status);
CREATE INDEX runs_finished ON runs(finished_at, created_at);
CREATE INDEX runs_active ON runs(created_at) WHERE status IN ('pending', 'running');
CREATE INDEX runs_lost ON runs(updated_at) WHERE status = 'unknown';
-- The live views ask for max(updated_at) every couple of seconds.
CREATE INDEX runs_updated ON runs(updated_at);
`,
}

// CurrentSchema is the version Open migrates to.
var CurrentSchema = len(migrations)

func (s *Store) migrate(ctx context.Context) error {
	v, err := s.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if v > len(migrations) {
		return fmt.Errorf("database schema %d is newer than this OpsTide (%d); use a newer release or restore a backup", v, len(migrations))
	}
	for ; v < len(migrations); v++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[v]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("to schema %d: %w", v+1, err)
		}
		// PRAGMA does not take parameters; v is an int from this loop.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
