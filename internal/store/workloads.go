package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// WorkloadState is what Kubernetes last reported about a workload, as OpsTide
// recorded it. Counts are the workload's own status fields; Rollout is the
// documented state internal/workload derives from them.
type WorkloadState struct {
	WorkloadID, Kind                   string
	Generation, ObservedGeneration     int64
	Desired, Ready, Available, Updated int
	Current, Misscheduled              int
	Revision, UpdateRevision           string
	Strategy                           string
	Partition                          *int
	MaxUnavailable, MaxSurge           string
	PodManagement                      string
	MinReadySeconds                    int
	Rollout, RolloutNote               string
	Conditions                         string // JSON: allow-listed conditions as reported
	StateHash                          string
	LastChangeAt, UpdatedAt            time.Time
}

const stateCols = `workload_id, kind, generation, observed_generation, desired, ready, available, updated, current, misscheduled,
	revision, update_revision, strategy, partition_, max_unavailable, max_surge, pod_management, min_ready_seconds, rollout, rollout_note,
	conditions, state_hash, last_change_at, updated_at`

func scanState(sc interface{ Scan(...any) error }) (WorkloadState, error) {
	var s WorkloadState
	var part sql.NullInt64
	var change, upd int64
	err := sc.Scan(&s.WorkloadID, &s.Kind, &s.Generation, &s.ObservedGeneration, &s.Desired, &s.Ready, &s.Available, &s.Updated, &s.Current, &s.Misscheduled,
		&s.Revision, &s.UpdateRevision, &s.Strategy, &part, &s.MaxUnavailable, &s.MaxSurge, &s.PodManagement, &s.MinReadySeconds, &s.Rollout, &s.RolloutNote,
		&s.Conditions, &s.StateHash, &change, &upd)
	if err != nil {
		return s, err
	}
	if part.Valid {
		n := int(part.Int64)
		s.Partition = &n
	}
	s.LastChangeAt, s.UpdatedAt = time.UnixMilli(change).UTC(), time.UnixMilli(upd).UTC()
	return s, nil
}

// WorkloadState returns the last recorded state of a workload.
func (w *Writer) WorkloadState(ctx context.Context, id string) (WorkloadState, bool, error) {
	s, err := scanState(w.tx.QueryRowContext(ctx, `SELECT `+stateCols+` FROM workload_state WHERE workload_id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return WorkloadState{}, false, nil
	}
	return s, err == nil, err
}

// SaveWorkloadState inserts or replaces a workload's state.
func (w *Writer) SaveWorkloadState(ctx context.Context, s WorkloadState) error {
	var part sql.NullInt64
	if s.Partition != nil {
		part = sql.NullInt64{Int64: int64(*s.Partition), Valid: true}
	}
	_, err := w.tx.ExecContext(ctx, `INSERT OR REPLACE INTO workload_state (`+stateCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		s.WorkloadID, s.Kind, s.Generation, s.ObservedGeneration, s.Desired, s.Ready, s.Available, s.Updated, s.Current, s.Misscheduled,
		s.Revision, s.UpdateRevision, s.Strategy, part, s.MaxUnavailable, s.MaxSurge, s.PodManagement, s.MinReadySeconds, s.Rollout, s.RolloutNote,
		s.Conditions, s.StateHash, ms(s.LastChangeAt).Int64, ms(w.now()).Int64)
	return err
}

// WorkloadRevision is one ControllerRevision of a StatefulSet or DaemonSet: its
// identity and the allow-listed snapshot of its Pod template.
type WorkloadRevision struct {
	ID, WorkloadID, Kind, Name, Hash string
	Number                           int64
	ControllerRevisionUID            string
	CreatedAt, ObservedAt            time.Time
	Origin                           string
	Fingerprint, Snapshot            string
}

const revCols = `id, workload_id, kind, name, hash, revision_num, controller_revision_uid, created_at, observed_at, origin, fingerprint, snapshot`

func scanRevision(sc interface{ Scan(...any) error }) (WorkloadRevision, error) {
	var r WorkloadRevision
	var created sql.NullInt64
	var observed int64
	if err := sc.Scan(&r.ID, &r.WorkloadID, &r.Kind, &r.Name, &r.Hash, &r.Number, &r.ControllerRevisionUID, &created, &observed, &r.Origin, &r.Fingerprint, &r.Snapshot); err != nil {
		return r, err
	}
	r.CreatedAt, r.ObservedAt = fromMS(created), time.UnixMilli(observed).UTC()
	return r, nil
}

// AddRevision records a ControllerRevision once. Its data is immutable, but a rollback gives an
// existing ControllerRevision a new, higher revision number, so that is kept current.
func (w *Writer) AddRevision(ctx context.Context, r WorkloadRevision) (bool, error) {
	res, err := w.tx.ExecContext(ctx, `INSERT INTO workload_revisions (`+revCols+`, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET revision_num = excluded.revision_num, updated_at = excluded.updated_at WHERE workload_revisions.revision_num <> excluded.revision_num`,
		r.ID, r.WorkloadID, r.Kind, r.Name, r.Hash, r.Number, r.ControllerRevisionUID, ms(r.CreatedAt), ms(r.ObservedAt).Int64, r.Origin, r.Fingerprint, r.Snapshot, ms(w.now()).Int64)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// Revisions lists a workload's recorded revisions, oldest first.
func (s *Store) Revisions(ctx context.Context, workloadID string) ([]WorkloadRevision, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+revCols+` FROM workload_revisions WHERE workload_id = ? ORDER BY revision_num, id`, workloadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkloadRevision
	for rows.Next() {
		r, err := scanRevision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// WorkloadRow is a workload with its current state, for lists.
type WorkloadRow struct {
	Workload
	State               WorkloadState
	HasState            bool
	Warnings24h         int    // Warning Events recorded about it in the last day
	LatestWarningReason string // the reason of the newest of them, as Kubernetes wrote it
	LatestWarningAt     time.Time
}

// WorkloadQuery selects workloads. Zero fields do not filter.
type WorkloadQuery struct {
	Kind, Namespace, Text string
	Status                string    // live | deleted
	ChangedSince          time.Time // by last change
	Now                   time.Time // for the 24 hour evidence count
	Cursor                string
	Limit                 int // default 100, at most 500
}

// WorkloadPage is a page of workloads, most recently changed first.
type WorkloadPage struct {
	Rows       []WorkloadRow
	NextCursor string
}

// Workloads lists workloads by last meaningful change, by keyset pagination.
func (s *Store) Workloads(ctx context.Context, q WorkloadQuery) (WorkloadPage, error) {
	limit := min(max(q.Limit, 1), 500)
	if q.Limit <= 0 {
		limit = 100
	}
	now := q.Now
	if now.IsZero() {
		now = s.now()
	}
	// The sort key is the state's last change, else the workload's last sighting.
	key := `coalesce(st.last_change_at, w.last_seen_at)`
	var where []string
	var args []any
	add := func(c string, a ...any) { where, args = append(where, c), append(args, a...) }
	if q.Kind != "" {
		add("w.kind = ?", q.Kind)
	}
	if q.Namespace != "" {
		add("w.namespace = ?", q.Namespace)
	}
	if q.Text != "" {
		add(`w.name LIKE ? ESCAPE '\'`, "%"+escapeLike(q.Text)+"%")
	}
	switch q.Status {
	case "live":
		add("w.deleted_at IS NULL")
	case "deleted":
		add("w.deleted_at IS NOT NULL")
	case "":
	default:
		return WorkloadPage{}, fmt.Errorf("bad status %q", q.Status)
	}
	if !q.ChangedSince.IsZero() {
		add(key+" >= ?", q.ChangedSince.UnixMilli())
	}
	if q.Cursor != "" {
		at, id, err := parseEvidenceCursor(q.Cursor)
		if err != nil {
			return WorkloadPage{}, err
		}
		add("("+key+" < ? OR ("+key+" = ? AND w.id < ?))", at, at, id)
	}
	sqlq := `SELECT w.id, w.kind, w.origin, w.namespace, w.name, w.first_seen_at, w.last_seen_at, w.deleted_at, ` + key + `,
	(SELECT count(*) FROM evidence e WHERE e.workload_id = w.id AND e.event_type = 'Warning' AND e.at >= ?),
	coalesce((SELECT e.reason FROM evidence e WHERE e.workload_id = w.id AND e.event_type = 'Warning' ORDER BY e.at DESC LIMIT 1), ''),
	(SELECT max(e.at) FROM evidence e WHERE e.workload_id = w.id AND e.event_type = 'Warning')
FROM workloads w LEFT JOIN workload_state st ON st.workload_id = w.id`
	if len(where) > 0 {
		// Only fixed clauses are joined; every value is a placeholder.
		sqlq += " WHERE " + strings.Join(where, " AND ") //nolint:gosec // see above
	}
	sqlq += " ORDER BY " + key + " DESC, w.id DESC LIMIT ?"
	args = append([]any{now.Add(-24 * time.Hour).UnixMilli()}, append(args, limit+1)...)
	rows, err := s.db.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return WorkloadPage{}, err
	}
	var page WorkloadPage
	var keys []int64
	for rows.Next() {
		var r WorkloadRow
		var first, last, sortKey int64
		var deleted, warnAt sql.NullInt64
		if err := rows.Scan(&r.ID, &r.Kind, &r.Origin, &r.Namespace, &r.Name, &first, &last, &deleted, &sortKey, &r.Warnings24h, &r.LatestWarningReason, &warnAt); err != nil {
			rows.Close()
			return WorkloadPage{}, err
		}
		r.FirstSeen, r.LastSeen, r.DeletedAt = time.UnixMilli(first).UTC(), time.UnixMilli(last).UTC(), fromMS(deleted)
		r.LatestWarningAt = fromMS(warnAt)
		page.Rows = append(page.Rows, r)
		keys = append(keys, sortKey)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return WorkloadPage{}, err
	}
	rows.Close()
	if len(page.Rows) > limit {
		page.Rows, keys = page.Rows[:limit], keys[:limit]
		page.NextCursor = fmt.Sprintf("%d.%s", keys[limit-1], page.Rows[limit-1].ID)
	}
	if len(page.Rows) == 0 {
		return page, nil
	}
	ids := make([]any, len(page.Rows))
	index := map[string]int{}
	for i, r := range page.Rows {
		ids[i] = r.ID
		index[r.ID] = i
	}
	// Only placeholders are joined into the statement; every value is bound.
	srows, err := s.db.QueryContext(ctx, `SELECT `+stateCols+` FROM workload_state WHERE workload_id IN (`+marks(len(ids))+`)`, ids...) //nolint:gosec // see above
	if err != nil {
		return WorkloadPage{}, err
	}
	defer srows.Close()
	for srows.Next() {
		st, err := scanState(srows)
		if err != nil {
			return WorkloadPage{}, err
		}
		i := index[st.WorkloadID]
		page.Rows[i].State, page.Rows[i].HasState = st, true
	}
	return page, srows.Err()
}

// WorkloadByID returns one workload with its state.
func (s *Store) WorkloadByID(ctx context.Context, id string) (WorkloadRow, bool, error) {
	var out WorkloadRow
	var first, last int64
	var deleted sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id, kind, origin, namespace, name, first_seen_at, last_seen_at, deleted_at FROM workloads WHERE id = ?`, id).
		Scan(&out.ID, &out.Kind, &out.Origin, &out.Namespace, &out.Name, &first, &last, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkloadRow{}, false, nil
	}
	if err != nil {
		return WorkloadRow{}, false, err
	}
	out.FirstSeen, out.LastSeen, out.DeletedAt = time.UnixMilli(first).UTC(), time.UnixMilli(last).UTC(), fromMS(deleted)
	st, err := scanState(s.db.QueryRowContext(ctx, `SELECT `+stateCols+` FROM workload_state WHERE workload_id = ?`, id))
	if err == nil {
		out.State, out.HasState = st, true
	} else if !errors.Is(err, sql.ErrNoRows) {
		return WorkloadRow{}, false, err
	}
	return out, true, nil
}

// PodsOfWorkload lists the Pods recorded for a workload, newest first. Placement
// and revision are what was observed; a Pod OpsTide did not see is not listed.
func (s *Store) PodsOfWorkload(ctx context.Context, workloadID string, limit int) ([]PodRecord, error) {
	return queryPods(ctx, s.db, `SELECT `+podCols+` FROM release_pods WHERE workload_id = ? ORDER BY created_at DESC, uid LIMIT ?`, workloadID, min(max(limit, 1), 1000))
}

// PodsOnNode lists the Pods observed running on a Node (by the Node name the Pod
// reported), newest first. It is placement context, not ownership.
func (s *Store) PodsOnNode(ctx context.Context, node string, limit int) ([]PodRecord, error) {
	return queryPods(ctx, s.db, `SELECT `+podCols+` FROM release_pods WHERE node = ? AND node <> '' ORDER BY created_at DESC, uid LIMIT ?`, node, min(max(limit, 1), 500))
}

// LivePodsOfWorkload lists the Pods last recorded as not ended.
func (s *Store) LivePodsOfWorkload(ctx context.Context, workloadID string) ([]PodRecord, error) {
	return queryPods(ctx, s.db, `SELECT `+podCols+` FROM release_pods WHERE workload_id = ? AND terminated_at IS NULL ORDER BY node, name`, workloadID)
}

// ---- Nodes ----

// Node is a Node as last observed. Conditions is compact JSON of the allow-listed
// conditions with the source's own status, reason, message and transition time.
type Node struct {
	UID, Name                                     string
	FirstSeenAt, LastSeenAt, CreatedAt, DeletedAt time.Time
	Origin                                        string
	Unschedulable                                 bool
	Conditions, StateHash                         string
	UpdatedAt                                     time.Time
}

const nodeCols = `uid, name, first_seen_at, last_seen_at, created_at, deleted_at, origin, unschedulable, conditions, state_hash, updated_at`

func scanNode(sc interface{ Scan(...any) error }) (Node, error) {
	var n Node
	var first, last, upd int64
	var created, deleted sql.NullInt64
	var unsched int
	if err := sc.Scan(&n.UID, &n.Name, &first, &last, &created, &deleted, &n.Origin, &unsched, &n.Conditions, &n.StateHash, &upd); err != nil {
		return n, err
	}
	n.FirstSeenAt, n.LastSeenAt, n.UpdatedAt = time.UnixMilli(first).UTC(), time.UnixMilli(last).UTC(), time.UnixMilli(upd).UTC()
	n.CreatedAt, n.DeletedAt, n.Unschedulable = fromMS(created), fromMS(deleted), unsched != 0
	return n, nil
}

// NodeByUID returns the Node with this UID.
func (w *Writer) NodeByUID(ctx context.Context, uid string) (Node, bool, error) {
	n, err := scanNode(w.tx.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE uid = ?`, uid))
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, false, nil
	}
	return n, err == nil, err
}

// SaveNode inserts or replaces a Node's observed state.
func (w *Writer) SaveNode(ctx context.Context, n Node) error {
	u := 0
	if n.Unschedulable {
		u = 1
	}
	_, err := w.tx.ExecContext(ctx, `INSERT OR REPLACE INTO nodes (`+nodeCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		n.UID, n.Name, ms(n.FirstSeenAt).Int64, ms(n.LastSeenAt).Int64, ms(n.CreatedAt), ms(n.DeletedAt), n.Origin, u, n.Conditions, n.StateHash, ms(w.now()).Int64)
	return err
}

// MarkNodeDeleted notes that the Node is gone; its record stays.
func (w *Writer) MarkNodeDeleted(ctx context.Context, uid string, at time.Time) error {
	_, err := w.tx.ExecContext(ctx, `UPDATE nodes SET deleted_at = ?, updated_at = ? WHERE uid = ? AND deleted_at IS NULL`, ms(at).Int64, ms(w.now()).Int64, uid)
	return err
}

// NodeIDByName finds the UID of the live Node with this exact name, "" when
// there is none or when more than one record claims it live.
func (w *Writer) NodeIDByName(ctx context.Context, name string) (string, error) {
	rows, err := w.tx.QueryContext(ctx, `SELECT uid FROM nodes WHERE name = ? AND deleted_at IS NULL`, name)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		ids = append(ids, id)
	}
	if len(ids) != 1 {
		return "", rows.Err()
	}
	return ids[0], rows.Err()
}

// LiveNodes lists the Nodes last recorded as existing.
func (s *Store) LiveNodes(ctx context.Context) ([]Node, error) {
	return s.queryNodes(ctx, `SELECT `+nodeCols+` FROM nodes WHERE deleted_at IS NULL ORDER BY name`)
}

// NodeQuery selects Nodes.
type NodeQuery struct {
	Text   string
	Status string // live | deleted
	Limit  int
}

// Nodes lists Nodes by name, bounded.
func (s *Store) Nodes(ctx context.Context, q NodeQuery) ([]Node, error) {
	sqlq := `SELECT ` + nodeCols + ` FROM nodes`
	var where []string
	var args []any
	if q.Text != "" {
		where, args = append(where, `name LIKE ? ESCAPE '\'`), append(args, "%"+escapeLike(q.Text)+"%")
	}
	switch q.Status {
	case "live":
		where = append(where, "deleted_at IS NULL")
	case "deleted":
		where = append(where, "deleted_at IS NOT NULL")
	case "":
	default:
		return nil, fmt.Errorf("bad status %q", q.Status)
	}
	if len(where) > 0 {
		sqlq += " WHERE " + strings.Join(where, " AND ") //nolint:gosec // fixed clauses, placeholder values
	}
	sqlq += " ORDER BY name, first_seen_at LIMIT ?"
	return s.queryNodes(ctx, sqlq, append(args, min(max(q.Limit, 1), 500))...)
}

// NodeByUID returns one Node for readers.
func (s *Store) NodeByUID(ctx context.Context, uid string) (Node, bool, error) {
	n, err := scanNode(s.db.QueryRowContext(ctx, `SELECT `+nodeCols+` FROM nodes WHERE uid = ?`, uid))
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, false, nil
	}
	return n, err == nil, err
}

func (s *Store) queryNodes(ctx context.Context, sqlq string, args ...any) ([]Node, error) {
	rows, err := s.db.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ---- Job Pods: late binding of evidence ----

// PodOwner is the compact, lasting record of which Job a Pod belonged to.
type PodOwner struct {
	PodUID, Namespace, Name string
	RunID, OperationID      string
	Node                    string
	FirstSeenAt             time.Time
}

// SavePodOwner records a Job Pod's ownership once (the node is filled when learned).
func (w *Writer) SavePodOwner(ctx context.Context, p PodOwner) error {
	_, err := w.tx.ExecContext(ctx, `INSERT INTO pod_owners (pod_uid, namespace, name, run_id, operation_id, node, first_seen_at) VALUES (?,?,?,?,?,?,?)
ON CONFLICT(pod_uid) DO UPDATE SET operation_id = coalesce(nullif(excluded.operation_id, ''), pod_owners.operation_id),
	node = coalesce(nullif(excluded.node, ''), pod_owners.node)`, p.PodUID, p.Namespace, p.Name, p.RunID, p.OperationID, p.Node, ms(p.FirstSeenAt).Int64)
	return err
}

// PodOwnerByUID returns what is recorded of a Job Pod's ownership.
func (w *Writer) PodOwnerByUID(ctx context.Context, uid string) (PodOwner, bool, error) {
	var p PodOwner
	var first int64
	err := w.tx.QueryRowContext(ctx, `SELECT pod_uid, namespace, name, run_id, operation_id, node, first_seen_at FROM pod_owners WHERE pod_uid = ?`, uid).
		Scan(&p.PodUID, &p.Namespace, &p.Name, &p.RunID, &p.OperationID, &p.Node, &first)
	if errors.Is(err, sql.ErrNoRows) {
		return PodOwner{}, false, nil
	}
	p.FirstSeenAt = time.UnixMilli(first).UTC()
	return p, err == nil, err
}

// RelinkEvidenceOfJobPod links the Events recorded about a Job's Pod, before its
// Job was known, to the run (and its CronJob). Only rows still without a run
// are touched, and the Event's identity and times are left as they are.
func (w *Writer) RelinkEvidenceOfJobPod(ctx context.Context, podUID, runID, operationID string) error {
	_, err := w.tx.ExecContext(ctx, `UPDATE evidence SET run_id = ?, operation_id = ?, link_via = 'Pod ownerReference to Job (recorded)', updated_at = ?
WHERE regarding_uid = ? AND regarding_uid <> '' AND regarding_kind = 'Pod' AND run_id = ''`, runID, operationID, ms(w.now()).Int64, podUID)
	return err
}

// RelinkEvidenceOfRun gives the CronJob to evidence that names a run, once the run's
// CronJob is known.
func (w *Writer) RelinkEvidenceOfRun(ctx context.Context, runID, operationID string) error {
	if operationID == "" {
		return nil
	}
	_, err := w.tx.ExecContext(ctx, `UPDATE evidence SET operation_id = ?, updated_at = ? WHERE run_id = ? AND run_id <> '' AND operation_id = ''`,
		operationID, ms(w.now()).Int64, runID)
	return err
}

// RelinkEvidenceOfNode links Events that named a Node before it was recorded. Only Events that
// state this Node's UID, or its name as the UID (as kubelet does), and that are not older than
// the Node's record are touched: an Event about an earlier Node of the same name is not this one's.
func (w *Writer) RelinkEvidenceOfNode(ctx context.Context, nodeID, name string, since time.Time) error {
	_, err := w.tx.ExecContext(ctx, `UPDATE evidence SET node_id = ?, link_via = 'regarding names the Node (exact name; kubelet reports the name as the UID)', updated_at = ?
WHERE regarding_kind = 'Node' AND regarding_name = ? AND regarding_uid IN (?, ?) AND node_id = '' AND at >= ?`,
		nodeID, ms(w.now()).Int64, name, name, nodeID, since.Add(-time.Minute).UnixMilli())
	return err
}

// WorkloadEvidenceStatus counts the Warning Events recorded about a workload in the last day and
// gives the newest one's reason, as Kubernetes wrote it.
func (s *Store) WorkloadEvidenceStatus(ctx context.Context, id string, now time.Time) (warnings int, reason string, at time.Time, err error) {
	var latest sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM evidence WHERE workload_id = ? AND event_type = 'Warning' AND at >= ?),
	coalesce((SELECT reason FROM evidence WHERE workload_id = ? AND event_type = 'Warning' ORDER BY at DESC LIMIT 1), ''),
	(SELECT max(at) FROM evidence WHERE workload_id = ? AND event_type = 'Warning')`, id, now.Add(-24*time.Hour).UnixMilli(), id, id).Scan(&warnings, &reason, &latest)
	return warnings, reason, fromMS(latest), err
}

// WorkloadCounts counts workloads by kind (live ones), for metrics.
func (s *Store) WorkloadCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, count(*) FROM workloads WHERE deleted_at IS NULL GROUP BY kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}
