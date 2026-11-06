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
	// 3: workload releases. A release is one revision of a Deployment as
	// OpsTide saw it; its Pods are kept as compact lifecycle records, not as
	// Kubernetes objects. Nothing here holds a Secret, an environment value
	// or a whole spec: the snapshot is an allow-list built by internal/release.
	`
ALTER TABLE events ADD COLUMN release_id TEXT NOT NULL DEFAULT '';
CREATE INDEX events_release ON events(release_id, at) WHERE release_id <> '';

CREATE TABLE workloads (
	id            TEXT PRIMARY KEY,         -- the Deployment's UID
	namespace     TEXT NOT NULL,
	name          TEXT NOT NULL,
	first_seen_at INTEGER NOT NULL,
	last_seen_at  INTEGER NOT NULL,
	deleted_at    INTEGER
);
CREATE INDEX workloads_ns_name ON workloads(namespace, name);

CREATE TABLE releases (
	id              TEXT PRIMARY KEY,       -- hash of Deployment UID, ReplicaSet UID and revision
	workload_id     TEXT NOT NULL,
	namespace       TEXT NOT NULL,
	workload        TEXT NOT NULL,
	revision        TEXT NOT NULL,
	revision_num    INTEGER NOT NULL,
	replicaset_uid  TEXT NOT NULL,
	replicaset_name TEXT NOT NULL,
	template_hash   TEXT NOT NULL DEFAULT '',
	fingerprint     TEXT NOT NULL,
	prev_id         TEXT NOT NULL DEFAULT '',
	origin          TEXT NOT NULL,          -- observed | adopted
	observed_at     INTEGER NOT NULL,
	started_at      INTEGER,                -- the ReplicaSet's creation time
	completed_at    INTEGER,
	superseded_at   INTEGER,
	scaled_down_at  INTEGER,
	state           TEXT NOT NULL,
	state_reason    TEXT NOT NULL DEFAULT '',
	state_at        INTEGER NOT NULL,
	desired         INTEGER NOT NULL DEFAULT 0,
	updated         INTEGER NOT NULL DEFAULT 0,
	ready           INTEGER NOT NULL DEFAULT 0,
	available       INTEGER NOT NULL DEFAULT 0,
	paused          INTEGER NOT NULL DEFAULT 0,
	health          TEXT NOT NULL,
	health_reasons  TEXT NOT NULL DEFAULT '[]',
	health_at       INTEGER,
	window_seconds  INTEGER NOT NULL,
	observe_until   INTEGER,
	episodes        TEXT NOT NULL DEFAULT '[]',
	trouble         TEXT NOT NULL DEFAULT '[]',     -- times the rollout was stalled or failed
	snapshot        TEXT NOT NULL,
	image_ids       TEXT NOT NULL DEFAULT '{}',
	updated_at      INTEGER NOT NULL
);
CREATE INDEX releases_workload ON releases(workload_id, revision_num);
CREATE INDEX releases_observed ON releases(observed_at, id);
CREATE INDEX releases_rs ON releases(replicaset_uid, observed_at);
CREATE INDEX releases_open ON releases(observe_until) WHERE health IN ('still_observing', 'degraded');
CREATE INDEX releases_updated ON releases(updated_at);

CREATE TABLE release_pods (
	uid            TEXT PRIMARY KEY,
	release_id     TEXT NOT NULL DEFAULT '',
	namespace      TEXT NOT NULL,
	name           TEXT NOT NULL,
	replicaset_uid TEXT NOT NULL,
	workload_id    TEXT NOT NULL,
	created_at     INTEGER NOT NULL,
	terminated_at  INTEGER,
	state          TEXT NOT NULL,           -- internal/release.PodState, compact JSON
	updated_at     INTEGER NOT NULL
);
CREATE INDEX release_pods_release ON release_pods(release_id, created_at);
-- A release's Pods are the Pods of its ReplicaSet that lived during it.
CREATE INDEX release_pods_rs ON release_pods(replicaset_uid, created_at);
CREATE INDEX release_pods_live ON release_pods(created_at) WHERE terminated_at IS NULL;
CREATE INDEX release_pods_updated ON release_pods(updated_at);
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
