package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Operation is a CronJob as OpsTide knows it: its schedule and controller
// settings, plus the description its owners put in annotations.
type Operation struct {
	ID        string // the CronJob's UID; a recreated CronJob is a new operation
	Namespace string
	Name      string

	DisplayName      string
	Description      string
	Owner            string
	Criticality      string
	RunbookURL       string
	ExpectedDuration *time.Duration
	// MetadataProblems names annotations that were present but unusable
	// (a runbook that is not https, a duration that does not parse).
	MetadataProblems []string

	Schedule          string
	TimeZone          string // spec.timeZone; "" means the controller's zone
	Suspend           bool
	ConcurrencyPolicy string
	StartingDeadline  *time.Duration
	SuccessfulHistory int
	FailedHistory     int

	CreatedAt   time.Time
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	DeletedAt   time.Time
}

// RunStatus is a run's outcome as far as it is known.
type RunStatus string

const (
	RunPending   RunStatus = "pending"
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
	RunUnknown   RunStatus = "unknown"
)

// Run is one observed Job. It is never inferred from a schedule.
type Run struct {
	ID          string // the Job's UID
	OperationID string // owning CronJob's UID; "" for a Job no CronJob owns
	Namespace   string
	JobName     string

	ScheduledAt time.Time
	// ScheduledBasis says where ScheduledAt came from: "annotation"
	// (batch.kubernetes.io/cronjob-scheduled-timestamp), "name" (the
	// controller's minute suffix) or "" when unknown.
	ScheduledBasis string

	CreatedAt    time.Time
	StartedAt    time.Time
	FinishedAt   time.Time
	Status       RunStatus
	StatusReason string
	Attempts     int
	Pods         []Pod

	ObservedAt time.Time // first seen by OpsTide
	UpdatedAt  time.Time
}

// Pod is what OpsTide keeps of a Job's pod: identity and how it ended.
type Pod struct {
	Name       string    `json:"name"`
	UID        string    `json:"uid"`
	Phase      string    `json:"phase"`
	Reason     string    `json:"reason,omitempty"`
	ExitCode   *int32    `json:"exitCode,omitempty"`
	StartedAt  time.Time `json:"startedAt,omitzero"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
}

// UpsertOperation inserts op or updates everything but FirstSeenAt.
func (w *Writer) UpsertOperation(ctx context.Context, op Operation) error {
	problems, _ := json.Marshal(nonNil(op.MetadataProblems))
	_, err := w.tx.ExecContext(ctx, `
INSERT INTO operations (id, namespace, name, display_name, description, owner, criticality, runbook_url,
	expected_duration_s, metadata_problems, schedule, time_zone, suspend, concurrency_policy, starting_deadline_s,
	successful_history, failed_history, created_at, first_seen_at, last_seen_at, deleted_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
	namespace=excluded.namespace, name=excluded.name, display_name=excluded.display_name,
	description=excluded.description, owner=excluded.owner, criticality=excluded.criticality,
	runbook_url=excluded.runbook_url, expected_duration_s=excluded.expected_duration_s,
	metadata_problems=excluded.metadata_problems, schedule=excluded.schedule, time_zone=excluded.time_zone,
	suspend=excluded.suspend, concurrency_policy=excluded.concurrency_policy,
	starting_deadline_s=excluded.starting_deadline_s, successful_history=excluded.successful_history,
	failed_history=excluded.failed_history, created_at=excluded.created_at,
	last_seen_at=excluded.last_seen_at, deleted_at=excluded.deleted_at`,
		op.ID, op.Namespace, op.Name, op.DisplayName, op.Description, op.Owner, op.Criticality, op.RunbookURL,
		durS(op.ExpectedDuration), string(problems), op.Schedule, op.TimeZone, op.Suspend, op.ConcurrencyPolicy,
		durS(op.StartingDeadline), op.SuccessfulHistory, op.FailedHistory, ms(op.CreatedAt).Int64,
		ms(op.FirstSeenAt).Int64, ms(op.LastSeenAt).Int64, ms(op.DeletedAt))
	return err
}

// MarkOperationDeleted records that the CronJob is gone. Its runs and
// history stay until retention removes them.
func (w *Writer) MarkOperationDeleted(ctx context.Context, id string, at time.Time) error {
	_, err := w.tx.ExecContext(ctx, `UPDATE operations SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`, ms(at), id)
	return err
}

const operationCols = `id, namespace, name, display_name, description, owner, criticality, runbook_url,
	expected_duration_s, metadata_problems, schedule, time_zone, suspend, concurrency_policy, starting_deadline_s,
	successful_history, failed_history, created_at, first_seen_at, last_seen_at, deleted_at`

func scanOperation(sc interface{ Scan(...any) error }) (Operation, error) {
	var op Operation
	var expected, deadline, deleted sql.NullInt64
	var created, first, last int64
	var problems string
	err := sc.Scan(&op.ID, &op.Namespace, &op.Name, &op.DisplayName, &op.Description, &op.Owner, &op.Criticality,
		&op.RunbookURL, &expected, &problems, &op.Schedule, &op.TimeZone, &op.Suspend, &op.ConcurrencyPolicy,
		&deadline, &op.SuccessfulHistory, &op.FailedHistory, &created, &first, &last, &deleted)
	if err != nil {
		return op, err
	}
	op.ExpectedDuration, op.StartingDeadline = fromDurS(expected), fromDurS(deadline)
	_ = json.Unmarshal([]byte(problems), &op.MetadataProblems)
	op.CreatedAt = time.UnixMilli(created).UTC()
	op.FirstSeenAt, op.LastSeenAt = time.UnixMilli(first).UTC(), time.UnixMilli(last).UTC()
	op.DeletedAt = fromMS(deleted)
	return op, nil
}

// Operation returns the operation with this ID; ok is false when unknown.
func (s *Store) Operation(ctx context.Context, id string) (op Operation, ok bool, err error) {
	op, err = scanOperation(s.db.QueryRowContext(ctx, `SELECT `+operationCols+` FROM operations WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, false, nil
	}
	return op, err == nil, err
}

// Operations lists operations by namespace and name; deleted ones only
// when includeDeleted.
func (s *Store) Operations(ctx context.Context, includeDeleted bool) ([]Operation, error) {
	q := `SELECT ` + operationCols + ` FROM operations`
	if !includeDeleted {
		q += ` WHERE deleted_at IS NULL`
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY namespace, name, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Operation
	for rows.Next() {
		op, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// UpsertRun inserts run or updates it, keeping the first ObservedAt. The run
// must come from Run or ActiveRuns, or be built from the Job itself: lists
// (RunsBetween and the like) leave the pods out, and writing such a run back
// would erase them.
func (w *Writer) UpsertRun(ctx context.Context, r Run) error {
	pods, _ := json.Marshal(nonNil(r.Pods))
	_, err := w.tx.ExecContext(ctx, `
INSERT INTO runs (id, operation_id, namespace, job_name, scheduled_at, scheduled_basis, created_at, started_at,
	finished_at, status, status_reason, attempts, pods, observed_at, updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
	operation_id=excluded.operation_id, scheduled_at=excluded.scheduled_at, scheduled_basis=excluded.scheduled_basis,
	started_at=excluded.started_at, finished_at=excluded.finished_at, status=excluded.status,
	status_reason=excluded.status_reason, attempts=excluded.attempts, pods=excluded.pods, updated_at=excluded.updated_at`,
		r.ID, r.OperationID, r.Namespace, r.JobName, ms(r.ScheduledAt), r.ScheduledBasis, ms(r.CreatedAt).Int64,
		ms(r.StartedAt), ms(r.FinishedAt), string(r.Status), r.StatusReason, r.Attempts, string(pods),
		ms(r.ObservedAt).Int64, ms(w.now()).Int64)
	return err
}

const runCols = `id, operation_id, namespace, job_name, scheduled_at, scheduled_basis, created_at, started_at,
	finished_at, status, status_reason, attempts, pods, observed_at, updated_at`

// runColsLite is runCols without the pods, which are the bulk of a row and
// only the run's own page shows. Lists of runs use it.
// Runs read with runColsLite have no pods: never write one back with
// UpsertRun, which would replace the stored pods with none.
const runColsLite = `id, operation_id, namespace, job_name, scheduled_at, scheduled_basis, created_at, started_at,
	finished_at, status, status_reason, attempts, '[]', observed_at, updated_at`

func scanRun(sc interface{ Scan(...any) error }) (Run, error) {
	var r Run
	var scheduled, started, finished sql.NullInt64
	var created, observed, updated int64
	var pods, status string
	err := sc.Scan(&r.ID, &r.OperationID, &r.Namespace, &r.JobName, &scheduled, &r.ScheduledBasis, &created,
		&started, &finished, &status, &r.StatusReason, &r.Attempts, &pods, &observed, &updated)
	if err != nil {
		return r, err
	}
	r.Status = RunStatus(status)
	r.ScheduledAt, r.StartedAt, r.FinishedAt = fromMS(scheduled), fromMS(started), fromMS(finished)
	r.CreatedAt = time.UnixMilli(created).UTC()
	r.ObservedAt, r.UpdatedAt = time.UnixMilli(observed).UTC(), time.UnixMilli(updated).UTC()
	if pods != "[]" {
		_ = json.Unmarshal([]byte(pods), &r.Pods)
	}
	return r, nil
}

// Run returns the run with this Job UID; ok is false when unknown.
func (s *Store) Run(ctx context.Context, id string) (r Run, ok bool, err error) {
	r, err = scanRun(s.db.QueryRowContext(ctx, `SELECT `+runCols+` FROM runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, false, nil
	}
	return r, err == nil, err
}

// overlapsFrom is true for a run created before the window that matters to it:
// still pending or running, finished after the window starts, or lost (its
// outcome unknown) after it starts. Two parameters, both the window's start.
// A run lost or pending long ago does not count: it would be loaded forever.
const overlapsFrom = `(status IN ('pending', 'running') OR finished_at >= ? OR (status = 'unknown' AND updated_at >= ?))`

// RunsForOperation returns the operation's runs created in [from, to),
// newest first, without their pods (Run has them). A run still active from before from is included, since
// it matters to whatever was due in the window.
func (s *Store) RunsForOperation(ctx context.Context, operationID string, from, to time.Time) ([]Run, error) {
	return s.queryRuns(ctx, `SELECT `+runColsLite+` FROM runs
WHERE operation_id = ? AND created_at < ? AND (created_at >= ? OR `+overlapsFrom+`)
ORDER BY created_at DESC`, operationID, ms(to).Int64, ms(from).Int64, ms(from).Int64, ms(from).Int64)
}

// RunsBetween is RunsForOperation for every run, CronJob-owned or not. The
// "created before the window but still matters" rule is an OR that SQLite
// answers by reading every older run, so it is written as one lookup per
// index, each leaving out what an earlier one returned:
//
//	created in the window;
//	created before it and still pending or running;
//	created before it and finished inside it;
//	created before it, lost (outcome unknown) inside it, and not finished inside it.
func (s *Store) RunsBetween(ctx context.Context, from, to time.Time) ([]Run, error) {
	if !from.Before(to) {
		return nil, nil
	}
	f, t := ms(from).Int64, ms(to).Int64
	return s.queryRuns(ctx, `
SELECT `+runColsLite+` FROM runs INDEXED BY runs_created WHERE created_at >= ? AND created_at < ?
UNION ALL
SELECT `+runColsLite+` FROM runs INDEXED BY runs_active WHERE created_at < ? AND status IN ('pending', 'running')
UNION ALL
SELECT `+runColsLite+` FROM runs INDEXED BY runs_finished WHERE finished_at >= ? AND created_at < ? AND status NOT IN ('pending', 'running')
UNION ALL
SELECT `+runColsLite+` FROM runs INDEXED BY runs_lost WHERE status = 'unknown' AND updated_at >= ? AND created_at < ?
	AND (finished_at IS NULL OR finished_at < ?)
ORDER BY created_at DESC`, f, t, f, f, f, f, f, f)
}

// RecentRuns returns the operation's latest runs, newest first, created
// before before (zero for no bound).
func (s *Store) RecentRuns(ctx context.Context, operationID string, before time.Time, limit int) ([]Run, error) {
	b := int64(1) << 62
	if !before.IsZero() {
		b = before.UnixMilli()
	}
	return s.queryRuns(ctx, `SELECT `+runColsLite+` FROM runs WHERE operation_id = ? AND created_at < ?
ORDER BY created_at DESC LIMIT ?`, operationID, b, limit)
}

func (s *Store) queryRuns(ctx context.Context, q string, args ...any) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// ActiveRuns returns the runs last recorded as pending or running.
func (s *Store) ActiveRuns(ctx context.Context) ([]Run, error) {
	return s.queryRuns(ctx, `SELECT `+runCols+` FROM runs WHERE status IN ('pending', 'running') ORDER BY created_at`)
}

// Owners lists the distinct owner annotations of every CronJob seen,
// deleted ones included, for the Log's Owner filter.
func (s *Store) Owners(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT owner FROM operations WHERE owner <> '' ORDER BY owner`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var o string
		if err := rows.Scan(&o); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// OperationLast is, for one live CronJob, when OpsTide last saw a run end well, end badly, and begin: what an alert on
// "the backup has not succeeded for 26 hours" is built from. A time is zero when OpsTide has seen no such run.
type OperationLast struct {
	ID, Namespace, Name          string
	Suspended                    bool
	LastSuccessAt, LastFailureAt time.Time
	LastRunAt                    time.Time
}

// OperationsLast lists the live CronJobs (at most limit, by namespace and name) with when their runs last ended.
func (s *Store) OperationsLast(ctx context.Context, limit int) ([]OperationLast, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT o.id, o.namespace, o.name, o.suspend,
	(SELECT max(finished_at) FROM runs r WHERE r.operation_id = o.id AND r.status = 'succeeded'),
	(SELECT max(finished_at) FROM runs r WHERE r.operation_id = o.id AND r.status = 'failed'),
	(SELECT max(created_at) FROM runs r WHERE r.operation_id = o.id)
FROM operations o WHERE o.deleted_at IS NULL ORDER BY o.namespace, o.name LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OperationLast
	for rows.Next() {
		var o OperationLast
		var susp int
		var ok, fail, last sql.NullInt64
		if err := rows.Scan(&o.ID, &o.Namespace, &o.Name, &susp, &ok, &fail, &last); err != nil {
			return nil, err
		}
		o.Suspended = susp != 0
		o.LastSuccessAt, o.LastFailureAt, o.LastRunAt = fromMS(ok), fromMS(fail), fromMS(last)
		out = append(out, o)
	}
	return out, rows.Err()
}

// LiveOperationCount counts the CronJobs not known to be deleted.
func (s *Store) LiveOperationCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM operations WHERE deleted_at IS NULL`).Scan(&n)
	return n, err
}
