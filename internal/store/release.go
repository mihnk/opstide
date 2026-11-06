package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Workload is a Deployment OpsTide has seen.
type Workload struct {
	ID        string // the workload's own UID
	Kind      string // Deployment | StatefulSet | DaemonSet; Deployment when empty
	Origin    string // observed | adopted; empty when not known
	Namespace string
	Name      string
	FirstSeen time.Time
	LastSeen  time.Time
	DeletedAt time.Time
}

// Workload kinds.
const (
	KindDeployment  = "Deployment"
	KindStatefulSet = "StatefulSet"
	KindDaemonSet   = "DaemonSet"
)

// Release states (the rollout) and health verdicts (what followed it). They
// are separate on purpose: a release can be completed and degraded.
const (
	ReleaseObserved    = "observed"
	ReleaseProgressing = "progressing"
	ReleaseCompleted   = "completed"
	ReleaseStalled     = "stalled"
	ReleaseFailed      = "failed"
	ReleaseSuperseded  = "superseded"
	ReleaseUnknown     = "unknown"

	HealthObserving    = "still_observing"
	HealthHealthy      = "healthy"
	HealthDegraded     = "degraded"
	HealthInsufficient = "insufficient_evidence"

	// OriginObserved: OpsTide was watching when the ReplicaSet appeared.
	// OriginAdopted: it already existed, so nothing about its rollout was seen.
	OriginObserved = "observed"
	OriginAdopted  = "adopted"
)

// Release is one revision of a Deployment as OpsTide saw it. JSON fields hold
// what internal/release defines; the store only keeps them.
type Release struct {
	ID          string
	WorkloadID  string
	Namespace   string
	Workload    string
	Revision    string
	RevisionNum int64

	ReplicaSetUID  string
	ReplicaSetName string
	TemplateHash   string
	Fingerprint    string
	PrevID         string
	Origin         string

	ObservedAt   time.Time
	StartedAt    time.Time
	CompletedAt  time.Time
	SupersededAt time.Time
	ScaledDownAt time.Time

	State       string
	StateReason string
	StateAt     time.Time

	Desired, Updated, Ready, Available int
	Paused                             bool

	Health        string
	HealthReasons string // JSON array of strings
	HealthAt      time.Time
	WindowSeconds int
	ObserveUntil  time.Time

	Episodes string // JSON: availability episodes after completion
	Trouble  string // JSON: times the rollout was stalled or failed
	Snapshot string // JSON: the allow-listed revision snapshot
	ImageIDs string // JSON: container -> image ID, from the first Pod that reported one

	// AssessmentVersion is the rule set that judges the release: 1 is the M2.5
	// contract, 2 sets scaling convergence apart from release findings.
	AssessmentVersion int

	UpdatedAt time.Time
}

// PodRecord is a Deployment-owned Pod's compact lifecycle record.
type PodRecord struct {
	// Kind is the kind of the workload that owns the Pod: Deployment (the default),
	// StatefulSet or DaemonSet. Revision is the controller-revision-hash label it
	// carries, Node where it was observed running, Ordinal its StatefulSet index.
	Kind          string
	Revision      string
	Node          string
	Ordinal       *int
	UID           string
	ReleaseID     string
	Namespace     string
	Name          string
	ReplicaSetUID string
	WorkloadID    string
	CreatedAt     time.Time
	TerminatedAt  time.Time
	State         string // JSON: internal/release.PodState
	UpdatedAt     time.Time
}

// queryer is what *sql.DB and *sql.Tx share, so reads work both from the API
// and inside a write transaction.
type queryer interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

const releaseCols = `id, workload_id, namespace, workload, revision, revision_num, replicaset_uid, replicaset_name, template_hash,
	fingerprint, prev_id, origin, observed_at, started_at, completed_at, superseded_at, scaled_down_at, state, state_reason,
	state_at, desired, updated, ready, available, paused, health, health_reasons, health_at, window_seconds, observe_until,
	episodes, trouble, snapshot, image_ids, assessment_version, updated_at`

func scanRelease(sc interface{ Scan(...any) error }) (Release, error) {
	var r Release
	var observed, state, upd int64
	var started, completed, superseded, scaled, healthAt, until sql.NullInt64
	var paused int
	err := sc.Scan(&r.ID, &r.WorkloadID, &r.Namespace, &r.Workload, &r.Revision, &r.RevisionNum, &r.ReplicaSetUID, &r.ReplicaSetName,
		&r.TemplateHash, &r.Fingerprint, &r.PrevID, &r.Origin, &observed, &started, &completed, &superseded, &scaled, &r.State,
		&r.StateReason, &state, &r.Desired, &r.Updated, &r.Ready, &r.Available, &paused, &r.Health, &r.HealthReasons, &healthAt,
		&r.WindowSeconds, &until, &r.Episodes, &r.Trouble, &r.Snapshot, &r.ImageIDs, &r.AssessmentVersion, &upd)
	if err != nil {
		return r, err
	}
	r.ObservedAt, r.StateAt, r.UpdatedAt = time.UnixMilli(observed).UTC(), time.UnixMilli(state).UTC(), time.UnixMilli(upd).UTC()
	r.StartedAt, r.CompletedAt, r.SupersededAt, r.ScaledDownAt = fromMS(started), fromMS(completed), fromMS(superseded), fromMS(scaled)
	r.HealthAt, r.ObserveUntil = fromMS(healthAt), fromMS(until)
	r.Paused = paused != 0
	return r, nil
}

func queryReleases(ctx context.Context, q queryer, sqlq string, args ...any) ([]Release, error) {
	rows, err := q.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Release
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func releaseByID(ctx context.Context, q queryer, id string) (Release, bool, error) {
	r, err := scanRelease(q.QueryRowContext(ctx, `SELECT `+releaseCols+` FROM releases WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Release{}, false, nil
	}
	return r, err == nil, err
}

// Release returns the release with this ID.
func (s *Store) Release(ctx context.Context, id string) (Release, bool, error) {
	return releaseByID(ctx, s.db, id)
}

// Release is Store.Release inside the writer's transaction.
func (w *Writer) Release(ctx context.Context, id string) (Release, bool, error) {
	return releaseByID(ctx, w.tx, id)
}

// ReleasesOfWorkload returns every release kept for a Deployment, newest
// revision first.
func (w *Writer) ReleasesOfWorkload(ctx context.Context, workloadID string) ([]Release, error) {
	return queryReleases(ctx, w.tx, `SELECT `+releaseCols+` FROM releases WHERE workload_id = ? ORDER BY revision_num DESC, observed_at DESC`, workloadID)
}

// LatestReleaseOfReplicaSet is the most recent release that ReplicaSet
// represented: a rollback gives an old ReplicaSet a new release.
func (w *Writer) LatestReleaseOfReplicaSet(ctx context.Context, rsUID string) (Release, bool, error) {
	rs, err := queryReleases(ctx, w.tx, `SELECT `+releaseCols+` FROM releases WHERE replicaset_uid = ? ORDER BY revision_num DESC LIMIT 1`, rsUID)
	if err != nil || len(rs) == 0 {
		return Release{}, false, err
	}
	return rs[0], true, nil
}

// UpsertRelease inserts r or updates what changes over a release's life. The
// identity, snapshot, fingerprint and first-observed time never change.
func (w *Writer) UpsertRelease(ctx context.Context, r Release) error {
	if r.ID == "" || r.WorkloadID == "" || r.Snapshot == "" {
		return fmt.Errorf("release needs an id, a workload and a snapshot: %+v", r.ID)
	}
	paused := 0
	if r.Paused {
		paused = 1
	}
	_, err := w.tx.ExecContext(ctx, `
INSERT INTO releases (`+releaseCols+`)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
	prev_id=excluded.prev_id, started_at=excluded.started_at, completed_at=excluded.completed_at,
	superseded_at=excluded.superseded_at, scaled_down_at=excluded.scaled_down_at, state=excluded.state,
	state_reason=excluded.state_reason, state_at=excluded.state_at, desired=excluded.desired, updated=excluded.updated,
	ready=excluded.ready, available=excluded.available, paused=excluded.paused, health=excluded.health,
	health_reasons=excluded.health_reasons, health_at=excluded.health_at, observe_until=excluded.observe_until,
	episodes=excluded.episodes, trouble=excluded.trouble, image_ids=excluded.image_ids, updated_at=excluded.updated_at`,
		r.ID, r.WorkloadID, r.Namespace, r.Workload, r.Revision, r.RevisionNum, r.ReplicaSetUID, r.ReplicaSetName, r.TemplateHash,
		r.Fingerprint, r.PrevID, r.Origin, ms(r.ObservedAt).Int64, ms(r.StartedAt), ms(r.CompletedAt), ms(r.SupersededAt),
		ms(r.ScaledDownAt), r.State, r.StateReason, ms(r.StateAt).Int64, r.Desired, r.Updated, r.Ready, r.Available, paused,
		r.Health, r.HealthReasons, ms(r.HealthAt), r.WindowSeconds, ms(r.ObserveUntil), r.Episodes, r.Trouble, r.Snapshot, r.ImageIDs,
		max(r.AssessmentVersion, 1), ms(w.now()).Int64)
	if err != nil {
		return err
	}
	// Events recorded about this ReplicaSet before its Deployment was known.
	return w.RelinkEvidenceOfReplicaSet(ctx, r.ReplicaSetUID, r.WorkloadID)
}

// UpsertWorkload records a Deployment, keeping the time it was first seen.
func (w *Writer) UpsertWorkload(ctx context.Context, wl Workload) error {
	_, err := w.tx.ExecContext(ctx, `
INSERT INTO workloads (id, namespace, name, first_seen_at, last_seen_at, deleted_at, kind, origin) VALUES (?,?,?,?,?,NULL,?,?)
ON CONFLICT(id) DO UPDATE SET namespace=excluded.namespace, name=excluded.name, last_seen_at=excluded.last_seen_at, deleted_at=NULL,
	origin=coalesce(nullif(workloads.origin, ''), excluded.origin)`,
		wl.ID, wl.Namespace, wl.Name, ms(wl.FirstSeen).Int64, ms(wl.LastSeen).Int64, kindOf(wl.Kind), wl.Origin)
	return err
}

// WorkloadLastSeen is when a Deployment was last reconciled; ok is false when
// it has never been recorded.
func (w *Writer) WorkloadLastSeen(ctx context.Context, id string) (time.Time, bool, error) {
	var last int64
	err := w.tx.QueryRowContext(ctx, `SELECT last_seen_at FROM workloads WHERE id = ?`, id).Scan(&last)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return time.UnixMilli(last).UTC(), true, nil
}

// WorkloadKnown reports whether a Deployment has been recorded before.
func (w *Writer) WorkloadKnown(ctx context.Context, id string) (bool, error) {
	var n int
	err := w.tx.QueryRowContext(ctx, `SELECT count(*) FROM workloads WHERE id = ?`, id).Scan(&n)
	return n > 0, err
}

// MarkWorkloadDeleted notes that the Deployment is gone; its releases stay.
func (w *Writer) MarkWorkloadDeleted(ctx context.Context, id string, at time.Time) error {
	_, err := w.tx.ExecContext(ctx, `UPDATE workloads SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`, ms(at).Int64, id)
	return err
}

// LiveWorkloads returns the Deployments last recorded as existing, for the
// startup reconciliation of Deployments deleted while OpsTide was not running.
func (s *Store) LiveWorkloads(ctx context.Context) ([]Workload, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, namespace, name, kind FROM workloads WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Workload
	for rows.Next() {
		var wl Workload
		if err := rows.Scan(&wl.ID, &wl.Namespace, &wl.Name, &wl.Kind); err != nil {
			return nil, err
		}
		out = append(out, wl)
	}
	return out, rows.Err()
}

// WorkloadDeleted tells whether a Deployment was seen deleted, and when.
func (s *Store) WorkloadDeleted(ctx context.Context, id string) (time.Time, error) {
	var d sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT deleted_at FROM workloads WHERE id = ?`, id).Scan(&d)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	return fromMS(d), err
}

// ReleaseQuery selects releases for the Releases page.
type ReleaseQuery struct {
	From, To  time.Time // by when the release was first observed, [From, To)
	Namespace string
	Workload  string // substring of the Deployment's name
	States    []string
	Health    []string
	// WithHistory includes adopted releases that were already replaced when
	// OpsTide arrived: kept only so a newer release can be compared with them.
	WithHistory bool
	Cursor      string
	Limit       int // default 50, at most 200
}

// ReleasePage is one page, newest first.
type ReleasePage struct {
	Releases   []Release
	NextCursor string
}

// Releases lists releases, newest first, by stable keyset cursor.
func (s *Store) Releases(ctx context.Context, q ReleaseQuery) (ReleasePage, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	var where []string
	var args []any
	if !q.From.IsZero() {
		where, args = append(where, "observed_at >= ?"), append(args, q.From.UnixMilli())
	}
	if !q.To.IsZero() {
		where, args = append(where, "observed_at < ?"), append(args, q.To.UnixMilli())
	}
	if q.Namespace != "" {
		where, args = append(where, "namespace = ?"), append(args, q.Namespace)
	}
	if q.Workload != "" {
		where, args = append(where, `workload LIKE ? ESCAPE '\'`), append(args, "%"+escapeLike(q.Workload)+"%")
	}
	in := func(col string, vals []string) {
		if len(vals) == 0 {
			return
		}
		where = append(where, col+" IN ("+strings.TrimSuffix(strings.Repeat("?,", len(vals)), ",")+")")
		for _, v := range vals {
			args = append(args, v)
		}
	}
	in("state", q.States)
	in("health", q.Health)
	if !q.WithHistory {
		where = append(where, "NOT (origin = 'adopted' AND state = 'superseded')")
	}
	if q.Cursor != "" {
		at, id, err := parseReleaseCursor(q.Cursor)
		if err != nil {
			return ReleasePage{}, err
		}
		where, args = append(where, "(observed_at < ? OR (observed_at = ? AND id < ?))"), append(args, at, at, id)
	}
	sqlq := `SELECT ` + releaseCols + ` FROM releases`
	if len(where) > 0 {
		// Only the fixed clauses above are joined; every value is a placeholder.
		sqlq += " WHERE " + strings.Join(where, " AND ") //nolint:gosec // see above
	}
	sqlq += " ORDER BY observed_at DESC, id DESC LIMIT ?"
	args = append(args, limit+1)
	rs, err := queryReleases(ctx, s.db, sqlq, args...)
	if err != nil {
		return ReleasePage{}, err
	}
	page := ReleasePage{Releases: rs}
	if len(rs) > limit {
		page.Releases = rs[:limit]
		last := page.Releases[limit-1]
		page.NextCursor = fmt.Sprintf("%d.%s", last.ObservedAt.UnixMilli(), last.ID)
	}
	return page, nil
}

func parseReleaseCursor(c string) (int64, string, error) {
	a, id, ok := strings.Cut(c, ".")
	if !ok || id == "" {
		return 0, "", errors.New("bad cursor")
	}
	at, err := strconv.ParseInt(a, 10, 64)
	if err != nil {
		return 0, "", errors.New("bad cursor")
	}
	return at, id, nil
}

// OpenReleases returns releases whose post-deployment window is open, for the
// sweep that closes the ones that ended. A release already degraded stays in
// while its window lasts: its verdict is final only once the window is over.
func (s *Store) OpenReleases(ctx context.Context) ([]Release, error) {
	return queryReleases(ctx, s.db, `SELECT `+releaseCols+` FROM releases WHERE health = 'still_observing'
	OR (health = 'degraded' AND superseded_at IS NULL AND (observe_until IS NULL OR observe_until > ?))`, s.now().UnixMilli())
}

// ReleasesOfWorkloadID lists a Deployment's releases for readers, newest revision first.
func (s *Store) ReleasesOfWorkloadID(ctx context.Context, id string, limit int) ([]Release, error) {
	return queryReleases(ctx, s.db, `SELECT `+releaseCols+` FROM releases WHERE workload_id = ? ORDER BY revision_num DESC, observed_at DESC LIMIT ?`, id, min(max(limit, 1), 200))
}

// ActiveWindows counts releases being observed now.
func (s *Store) ActiveWindows(ctx context.Context, now time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM releases WHERE health = 'still_observing' AND (observe_until IS NULL OR observe_until > ?)`, now.UnixMilli()).Scan(&n)
	return n, err
}

// UpsertPod records a Pod's lifecycle, keeping its first-seen identity.
func (w *Writer) UpsertPod(ctx context.Context, p PodRecord) error {
	_, err := w.tx.ExecContext(ctx, `
INSERT INTO release_pods (uid, release_id, namespace, name, replicaset_uid, workload_id, created_at, terminated_at, state, updated_at, kind, revision, node, ordinal)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(uid) DO UPDATE SET release_id=excluded.release_id, terminated_at=excluded.terminated_at,
	state=excluded.state, updated_at=excluded.updated_at,
	revision=coalesce(nullif(excluded.revision, ''), release_pods.revision), node=coalesce(nullif(excluded.node, ''), release_pods.node)`,
		p.UID, p.ReleaseID, p.Namespace, p.Name, p.ReplicaSetUID, p.WorkloadID, ms(p.CreatedAt).Int64, ms(p.TerminatedAt),
		p.State, ms(w.now()).Int64, kindOf(p.Kind), p.Revision, p.Node, p.Ordinal)
	return err
}

const podCols = `uid, release_id, namespace, name, replicaset_uid, workload_id, created_at, terminated_at, state, updated_at, kind, revision, node, ordinal`

func scanPod(sc interface{ Scan(...any) error }) (PodRecord, error) {
	var p PodRecord
	var created, upd int64
	var term, ord sql.NullInt64
	if err := sc.Scan(&p.UID, &p.ReleaseID, &p.Namespace, &p.Name, &p.ReplicaSetUID, &p.WorkloadID, &created, &term, &p.State, &upd, &p.Kind, &p.Revision, &p.Node, &ord); err != nil {
		return p, err
	}
	if ord.Valid {
		n := int(ord.Int64)
		p.Ordinal = &n
	}
	p.CreatedAt, p.TerminatedAt, p.UpdatedAt = time.UnixMilli(created).UTC(), fromMS(term), time.UnixMilli(upd).UTC()
	return p, nil
}

// Pod is the lifecycle record of the Pod with this UID.
func (w *Writer) Pod(ctx context.Context, uid string) (PodRecord, bool, error) {
	p, err := scanPod(w.tx.QueryRowContext(ctx, `SELECT `+podCols+` FROM release_pods WHERE uid = ?`, uid))
	if errors.Is(err, sql.ErrNoRows) {
		return PodRecord{}, false, nil
	}
	return p, err == nil, err
}

// PodByUID is Writer.Pod for readers.
func (s *Store) PodByUID(ctx context.Context, uid string) (PodRecord, bool, error) {
	p, err := scanPod(s.db.QueryRowContext(ctx, `SELECT `+podCols+` FROM release_pods WHERE uid = ?`, uid))
	if errors.Is(err, sql.ErrNoRows) {
		return PodRecord{}, false, nil
	}
	return p, err == nil, err
}

func queryPods(ctx context.Context, q queryer, sqlq string, args ...any) ([]PodRecord, error) {
	rows, err := q.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PodRecord
	for rows.Next() {
		p, err := scanPod(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// podsOfRelease selects a release's Pods: the Pods of its ReplicaSet that
// were alive at some time while it was the current release. Selecting by
// ReplicaSet and time, not by a stored release id, keeps both the old and the
// new release's Pods right when a rollback reuses a ReplicaSet whose Pods never stopped.
// A release first seen as its ReplicaSet came up allows a minute for the Pods
// that were created just before it was noticed; a rollback has no such slack,
// since Pods that ended before it began belong to the earlier release.
func podsOfRelease(ctx context.Context, q queryer, r Release) ([]PodRecord, error) {
	upper := int64(1<<62 - 1)
	if !r.SupersededAt.IsZero() {
		upper = r.SupersededAt.UnixMilli()
	}
	lower := r.ObservedAt.UnixMilli()
	reused, err := reusedReplicaSet(ctx, q, r)
	if err != nil {
		return nil, err
	}
	if !reused {
		lower -= int64(time.Minute.Milliseconds())
	}
	return queryPods(ctx, q, `SELECT `+podCols+` FROM release_pods
WHERE replicaset_uid = ? AND created_at <= ? AND (terminated_at IS NULL OR terminated_at >= ?)
ORDER BY created_at, uid`, r.ReplicaSetUID, upper, lower)
}

// reusedReplicaSet says whether an earlier release already used r's ReplicaSet:
// r is then a rollback to it.
func reusedReplicaSet(ctx context.Context, q queryer, r Release) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM releases WHERE replicaset_uid = ? AND revision_num < ?`, r.ReplicaSetUID, r.RevisionNum).Scan(&n)
	return n > 0, err
}

// IsRollback says whether r reuses a ReplicaSet of an earlier release.
func (s *Store) IsRollback(ctx context.Context, r Release) (bool, error) {
	return reusedReplicaSet(ctx, s.db, r)
}

// IsRollback is Store.IsRollback inside the writer's transaction.
func (w *Writer) IsRollback(ctx context.Context, r Release) (bool, error) {
	return reusedReplicaSet(ctx, w.tx, r)
}

// ReleasePods returns a release's Pods, oldest first.
func (s *Store) ReleasePods(ctx context.Context, r Release) ([]PodRecord, error) {
	return podsOfRelease(ctx, s.db, r)
}

// ReleasePods is Store.ReleasePods inside the writer's transaction.
func (w *Writer) ReleasePods(ctx context.Context, r Release) ([]PodRecord, error) {
	return podsOfRelease(ctx, w.tx, r)
}

// LivePods returns Pods last recorded as not terminated, for the startup
// reconciliation of Pods that vanished while OpsTide was not running.
func (s *Store) LivePods(ctx context.Context) ([]PodRecord, error) {
	return queryPods(ctx, s.db, `SELECT `+podCols+` FROM release_pods WHERE terminated_at IS NULL AND kind = 'Deployment'`)
}

// LiveWorkloadPods is LivePods for the Pods of StatefulSets and DaemonSets.
func (s *Store) LiveWorkloadPods(ctx context.Context) ([]PodRecord, error) {
	return queryPods(ctx, s.db, `SELECT `+podCols+` FROM release_pods WHERE terminated_at IS NULL AND kind <> 'Deployment'`)
}

func kindOf(k string) string {
	if k == "" {
		return KindDeployment
	}
	return k
}

// WatchedThroughout reports whether scope was watched without a gap over
// [from, to]: one interval, open or closed, that covers all of it.
func (s *Store) WatchedThroughout(ctx context.Context, scope string, from, to time.Time) (bool, error) {
	if to.Before(from) {
		to = from
	}
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM coverage WHERE scope = ? AND started_at <= ? AND (ended_at IS NULL OR ended_at >= ?)`,
		scope, from.UnixMilli(), to.UnixMilli()).Scan(&n)
	return n > 0, err
}

// WatchedThroughout is the same check inside the writer's transaction.
func (w *Writer) WatchedThroughout(ctx context.Context, scope string, from, to time.Time) (bool, error) {
	if to.Before(from) {
		to = from
	}
	var n int
	err := w.tx.QueryRowContext(ctx, `SELECT count(*) FROM coverage WHERE scope = ? AND started_at <= ? AND (ended_at IS NULL OR ended_at >= ?)`,
		scope, from.UnixMilli(), to.UnixMilli()).Scan(&n)
	return n > 0, err
}
