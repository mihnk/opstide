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
	// 4: Kubernetes evidence and scaling. Events are kept as an allow-listed,
	// size-bounded record keyed by the Event's own UID, so a count that grows is
	// one row that changes and not many rows. The links (rs_uid, workload_id, ...)
	// are written only from UIDs and ownerReferences, never from names.
	`
CREATE TABLE evidence (
	id                TEXT PRIMARY KEY,        -- hash of the Event's UID
	source            TEXT NOT NULL,           -- events.k8s.io/v1 | core/v1
	source_uid        TEXT NOT NULL,
	namespace         TEXT NOT NULL,
	name              TEXT NOT NULL,           -- the Event object's name
	reason            TEXT NOT NULL DEFAULT '',
	event_type        TEXT NOT NULL DEFAULT '',-- Normal | Warning, as reported
	message           TEXT NOT NULL DEFAULT '',
	message_truncated INTEGER NOT NULL DEFAULT 0,
	action            TEXT NOT NULL DEFAULT '',
	reporter          TEXT NOT NULL DEFAULT '',
	regarding_kind    TEXT NOT NULL DEFAULT '',
	regarding_name    TEXT NOT NULL DEFAULT '',
	regarding_uid     TEXT NOT NULL DEFAULT '',
	related_kind      TEXT NOT NULL DEFAULT '',
	related_name      TEXT NOT NULL DEFAULT '',
	related_uid       TEXT NOT NULL DEFAULT '',
	first_at          INTEGER,                 -- NULL: the source gave none
	last_at           INTEGER,
	count             INTEGER NOT NULL DEFAULT 0,
	count_basis       TEXT NOT NULL DEFAULT '',-- series | deprecatedCount | count | ''
	observed_at       INTEGER NOT NULL,        -- when OpsTide first saw it
	origin            TEXT NOT NULL,           -- observed | adopted
	at                INTEGER NOT NULL,        -- sort time: last_at, else first_at, else observed_at
	updated_at        INTEGER NOT NULL,
	source_deleted_at INTEGER,
	count_logged      INTEGER NOT NULL DEFAULT 0,
	count_logged_at   INTEGER,
	rs_uid            TEXT NOT NULL DEFAULT '',
	workload_id       TEXT NOT NULL DEFAULT '',
	run_id            TEXT NOT NULL DEFAULT '',
	operation_id      TEXT NOT NULL DEFAULT '',
	hpa_uid           TEXT NOT NULL DEFAULT '',
	link_via          TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX evidence_source_uid ON evidence(source_uid);
CREATE INDEX evidence_at ON evidence(at, id);
CREATE INDEX evidence_regarding ON evidence(regarding_uid, at) WHERE regarding_uid <> '';
CREATE INDEX evidence_rs ON evidence(rs_uid, at) WHERE rs_uid <> '';
CREATE INDEX evidence_workload ON evidence(workload_id, at) WHERE workload_id <> '';
CREATE INDEX evidence_run ON evidence(run_id, at) WHERE run_id <> '';
CREATE INDEX evidence_hpa ON evidence(hpa_uid, at) WHERE hpa_uid <> '';
CREATE INDEX evidence_ns ON evidence(namespace, at);
CREATE INDEX evidence_reason ON evidence(reason, at);
CREATE INDEX evidence_updated ON evidence(updated_at);
CREATE INDEX evidence_live ON evidence(at) WHERE source_deleted_at IS NULL;

-- Scaling entries and Event entries in the Log are filtered by what they are about.
ALTER TABLE events ADD COLUMN reason TEXT NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN workload_id TEXT NOT NULL DEFAULT '';
CREATE INDEX events_workload ON events(workload_id, at) WHERE workload_id <> '';
CREATE INDEX events_reason ON events(reason, at) WHERE reason <> '';

-- A Deployment's replica target as last seen, to notice when it changes.
ALTER TABLE workloads ADD COLUMN spec_replicas INTEGER;

-- Which rules judged a release. 1 is the M2.5 contract, kept for releases
-- observed before scaling evidence existed; 2 sets scaling convergence apart.
ALTER TABLE releases ADD COLUMN assessment_version INTEGER NOT NULL DEFAULT 1;

CREATE TABLE hpas (
	uid               TEXT PRIMARY KEY,
	namespace         TEXT NOT NULL,
	name              TEXT NOT NULL,
	target_api        TEXT NOT NULL DEFAULT '',
	target_kind       TEXT NOT NULL DEFAULT '',
	target_name       TEXT NOT NULL DEFAULT '',
	target_uid        TEXT NOT NULL DEFAULT '', -- the Deployment of that name, when OpsTide has seen it
	workload_id       TEXT NOT NULL DEFAULT '',
	min_replicas      INTEGER,
	max_replicas      INTEGER NOT NULL DEFAULT 0,
	current_replicas  INTEGER NOT NULL DEFAULT 0,
	desired_replicas  INTEGER NOT NULL DEFAULT 0,
	last_scale_at     INTEGER,
	conditions        TEXT NOT NULL DEFAULT '[]',
	metrics           TEXT NOT NULL DEFAULT '[]',
	first_seen_at     INTEGER NOT NULL,
	updated_at        INTEGER NOT NULL,
	deleted_at        INTEGER
);
CREATE INDEX hpas_workload ON hpas(workload_id) WHERE workload_id <> '';
CREATE INDEX hpas_target ON hpas(namespace, target_kind, target_name);
CREATE INDEX hpas_updated ON hpas(updated_at);
`,
	// 5: more workload kinds and Nodes. One workloads table now holds Deployments,
	// StatefulSets and DaemonSets, each keyed by its own UID; workload_state is the
	// current observed state and workload_revisions the ControllerRevision history of
	// the kinds that have one (Deployments keep theirs in releases). Nothing here
	// holds a whole object: snapshots are the allow-list of internal/release.
	`
ALTER TABLE workloads ADD COLUMN kind TEXT NOT NULL DEFAULT 'Deployment';
ALTER TABLE workloads ADD COLUMN origin TEXT NOT NULL DEFAULT '';
CREATE INDEX workloads_kind ON workloads(kind, namespace, name);

CREATE TABLE workload_state (
	workload_id         TEXT PRIMARY KEY,
	kind                TEXT NOT NULL,
	generation          INTEGER NOT NULL DEFAULT 0,
	observed_generation INTEGER NOT NULL DEFAULT 0,
	desired             INTEGER NOT NULL DEFAULT 0,
	ready               INTEGER NOT NULL DEFAULT 0,
	available           INTEGER NOT NULL DEFAULT 0,
	updated             INTEGER NOT NULL DEFAULT 0,
	current             INTEGER NOT NULL DEFAULT 0,  -- StatefulSet currentReplicas, DaemonSet currentNumberScheduled
	misscheduled        INTEGER NOT NULL DEFAULT 0,  -- DaemonSet numberMisscheduled
	revision            TEXT NOT NULL DEFAULT '',    -- Deployment revision; StatefulSet currentRevision; DaemonSet newest ControllerRevision
	update_revision     TEXT NOT NULL DEFAULT '',
	strategy            TEXT NOT NULL DEFAULT '',
	partition_          INTEGER,
	max_unavailable     TEXT NOT NULL DEFAULT '',
	max_surge           TEXT NOT NULL DEFAULT '',
	pod_management      TEXT NOT NULL DEFAULT '',
	min_ready_seconds   INTEGER NOT NULL DEFAULT 0,
	rollout             TEXT NOT NULL DEFAULT '',
	rollout_note        TEXT NOT NULL DEFAULT '',
	conditions          TEXT NOT NULL DEFAULT '[]',
	state_hash          TEXT NOT NULL DEFAULT '',
	last_change_at      INTEGER NOT NULL,
	updated_at          INTEGER NOT NULL
);
CREATE INDEX workload_state_change ON workload_state(last_change_at, workload_id);
CREATE INDEX workload_state_updated ON workload_state(updated_at);

CREATE TABLE workload_revisions (
	id                      TEXT PRIMARY KEY,   -- hash of the workload UID and the ControllerRevision UID
	workload_id             TEXT NOT NULL,
	kind                    TEXT NOT NULL,
	name                    TEXT NOT NULL,      -- the ControllerRevision's name
	hash                    TEXT NOT NULL DEFAULT '',
	revision_num            INTEGER NOT NULL,   -- ControllerRevision.revision, meaningful within this workload only
	controller_revision_uid TEXT NOT NULL,
	created_at              INTEGER,
	observed_at             INTEGER NOT NULL,
	origin                  TEXT NOT NULL,      -- observed | adopted
	fingerprint             TEXT NOT NULL,
	snapshot                TEXT NOT NULL,
	updated_at              INTEGER NOT NULL
);
CREATE INDEX workload_revisions_workload ON workload_revisions(workload_id, revision_num);
CREATE INDEX workload_revisions_observed ON workload_revisions(observed_at);

-- Pods of StatefulSets and DaemonSets live in release_pods too, with their placement.
ALTER TABLE release_pods ADD COLUMN kind TEXT NOT NULL DEFAULT 'Deployment';
ALTER TABLE release_pods ADD COLUMN revision TEXT NOT NULL DEFAULT '';
ALTER TABLE release_pods ADD COLUMN node TEXT NOT NULL DEFAULT '';
ALTER TABLE release_pods ADD COLUMN ordinal INTEGER;
CREATE INDEX release_pods_workload ON release_pods(workload_id, created_at);
CREATE INDEX release_pods_node ON release_pods(node, created_at) WHERE node <> '';

CREATE TABLE nodes (
	uid           TEXT PRIMARY KEY,
	name          TEXT NOT NULL,
	first_seen_at INTEGER NOT NULL,
	last_seen_at  INTEGER NOT NULL,
	created_at    INTEGER,
	deleted_at    INTEGER,
	origin        TEXT NOT NULL,           -- observed | adopted
	unschedulable INTEGER NOT NULL DEFAULT 0,
	conditions    TEXT NOT NULL DEFAULT '[]',
	state_hash    TEXT NOT NULL DEFAULT '',
	updated_at    INTEGER NOT NULL
);
CREATE INDEX nodes_name ON nodes(name, first_seen_at);
CREATE INDEX nodes_updated ON nodes(updated_at);
CREATE INDEX events_node ON events(resource_uid, at) WHERE kind = 'Node';

-- Which Job a Pod belonged to, kept after the Pod and Job are garbage-collected,
-- so an Event about the Pod can be linked to its run whenever either is learned.
CREATE TABLE pod_owners (
	pod_uid       TEXT PRIMARY KEY,
	namespace     TEXT NOT NULL,
	name          TEXT NOT NULL,
	run_id        TEXT NOT NULL,            -- the Job's UID
	operation_id  TEXT NOT NULL DEFAULT '', -- the CronJob's UID, when the Job's controller was one
	node          TEXT NOT NULL DEFAULT '',
	first_seen_at INTEGER NOT NULL
);
CREATE INDEX pod_owners_run ON pod_owners(run_id);
CREATE INDEX pod_owners_seen ON pod_owners(first_seen_at);

ALTER TABLE evidence ADD COLUMN node_id TEXT NOT NULL DEFAULT '';
CREATE INDEX evidence_node ON evidence(node_id, at) WHERE node_id <> '';

-- Deployment entries now name their workload, like the others.
UPDATE events SET workload_id = resource_uid WHERE workload_id = '' AND kind = 'Deployment' AND resource_uid <> '' AND (type LIKE 'release.%' OR type LIKE 'deployment.%');
`,
	// 6: the Explorer reads events and evidence together. A Kubernetes Event is ordered by its
	// first occurrence (else when it was first seen), which never changes once written, so a
	// cursor never skips or repeats a record; the expression index serves that order.
	`
CREATE INDEX IF NOT EXISTS evidence_explorer ON evidence(coalesce(first_at, observed_at), id);
-- The Explorer's drilldown reads everything recorded about one resource or one run.
CREATE INDEX IF NOT EXISTS events_resource ON events(resource_uid, at) WHERE resource_uid <> '';
CREATE INDEX IF NOT EXISTS events_run ON events(run_id, at) WHERE run_id <> '';

-- A Deployment's "last change" was set when its state was first recorded (migration 5), which made every
-- Deployment look as if it had just changed. It is the time of its newest recorded release instead.
UPDATE workload_state SET last_change_at = coalesce(
	(SELECT max(CASE WHEN r.origin = 'adopted' THEN coalesce(r.started_at, r.observed_at) ELSE r.observed_at END) FROM releases r WHERE r.workload_id = workload_state.workload_id),
	(SELECT first_seen_at FROM workloads w WHERE w.id = workload_state.workload_id),
	last_change_at)
WHERE kind = 'Deployment';
-- A Node link made by exact name under the earlier, time-blind rule is kept only where that Node existed when the Event began.
UPDATE evidence SET node_id = '', link_via = '' WHERE node_id <> '' AND link_via LIKE 'regarding names the Node%'
	AND NOT EXISTS (SELECT 1 FROM nodes n WHERE n.uid = evidence.node_id
		AND coalesce(n.created_at, n.first_seen_at) <= coalesce(evidence.first_at, evidence.observed_at) + 1000
		AND (n.deleted_at IS NULL OR n.deleted_at >= coalesce(evidence.first_at, evidence.observed_at)));
-- Whether a workload's last change was seen, or is the best baseline there is for something already there.
ALTER TABLE workload_state ADD COLUMN change_basis TEXT NOT NULL DEFAULT 'observed';
UPDATE workload_state SET change_basis = 'baseline' WHERE kind = 'Deployment';
`,
	// 7: a Kubernetes Event's place on the Explorer timeline is fixed when it is first recorded. It was computed from
	// first_at, which Kubernetes may fill in later; stored, it can never move, so a cursor never skips or repeats it.
	`
ALTER TABLE evidence ADD COLUMN record_key INTEGER;
UPDATE evidence SET record_key = coalesce(first_at, observed_at);
DROP INDEX IF EXISTS evidence_explorer;
CREATE INDEX evidence_record_key ON evidence(record_key, id);
`,
	// 8: ConfigMaps and Secrets. Only identity, when each was seen, and for a ConfigMap a hash of its content and the
	// names of its keys: never a value, and for a Secret nothing beyond its metadata (OpsTide never requests its data).
	// config_refs is which workloads' Pod templates name which ConfigMap or Secret, by name, as OpsTide last saw them.
	`
CREATE TABLE config_objects (
	uid           TEXT PRIMARY KEY,
	kind          TEXT NOT NULL,            -- ConfigMap | Secret
	namespace     TEXT NOT NULL,
	name          TEXT NOT NULL,
	created_at    INTEGER,
	first_seen_at INTEGER NOT NULL,
	last_seen_at  INTEGER NOT NULL,
	deleted_at    INTEGER,
	origin        TEXT NOT NULL,            -- observed | adopted
	content_hash  TEXT NOT NULL DEFAULT '', -- ConfigMap only: sha256 of the sorted keys and values; '' for a Secret
	keys          TEXT NOT NULL DEFAULT '[]', -- ConfigMap only: key names, never values
	updated_at    INTEGER NOT NULL
);
CREATE INDEX config_objects_name ON config_objects(namespace, kind, name);

CREATE TABLE config_refs (
	workload_id   TEXT NOT NULL,            -- the workload's UID (a CronJob's UID for a CronJob)
	workload_kind TEXT NOT NULL,            -- Deployment | StatefulSet | DaemonSet | CronJob
	namespace     TEXT NOT NULL,
	workload_name TEXT NOT NULL,
	ref_kind      TEXT NOT NULL,            -- ConfigMap | Secret
	ref_name      TEXT NOT NULL,
	first_seen_at INTEGER NOT NULL,
	last_seen_at  INTEGER NOT NULL,
	ended_at      INTEGER,                  -- when OpsTide saw the template stop naming it, or the workload go
	PRIMARY KEY (workload_id, ref_kind, ref_name)
);
CREATE INDEX config_refs_target ON config_refs(namespace, ref_kind, ref_name);
`,
	// 9: a ConfigMap's content hash is an HMAC under a key made for this install (kept in meta, never served), so a small
	// value cannot be recovered by hashing guesses. Hashes made before (plain SHA-256) are dropped, not compared, and are
	// kept out of record IDs. key_hashes holds the per-key HMACs, so the changed keys can be named after a restart;
	// writes_seen holds, per field manager, the time of the newest write already handled, so a restart or a relist does
	// not handle a write twice; helm_release marks Helm's release records. A Secret's write is one neutral type: managedFields cannot say its data changed.
	`
UPDATE config_objects SET content_hash = '';
ALTER TABLE config_objects ADD COLUMN key_hashes TEXT NOT NULL DEFAULT '{}';
ALTER TABLE config_objects ADD COLUMN writes_seen TEXT NOT NULL DEFAULT '{}';
-- 1 for Helm's release records (named sh.helm.release.v1.*, labelled owner=helm): routine volume, not configuration.
ALTER TABLE config_objects ADD COLUMN helm_release INTEGER NOT NULL DEFAULT 0;
UPDATE events SET id = 'configmap.data_changed/' || resource_uid || '/seq' || seq WHERE type = 'configmap.data_changed';
UPDATE events SET type = 'secret.written' WHERE type IN ('secret.data_written', 'secret.metadata_written');
CREATE INDEX config_objects_deleted ON config_objects(deleted_at) WHERE deleted_at IS NOT NULL;
CREATE INDEX config_refs_ended ON config_refs(ended_at) WHERE ended_at IS NOT NULL;
`,
	// 10: the changes on a resource named by namespace, kind and name (a ConfigMap or Secret a Pod template names, the
	// Explorer's name filter) are read on their own index, bounded by time, instead of scanning every entry of the types.
	`
CREATE INDEX IF NOT EXISTS events_name ON events(namespace, kind, resource_name, at) WHERE resource_name <> '';
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
