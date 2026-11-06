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

// Sources an Event can be read from. They are the same Kubernetes objects
// (one UID) served by two APIs, so a cluster is read through one of them.
const (
	SourceEventsV1 = "events.k8s.io/v1"
	SourceCoreV1   = "core/v1"
)

// Origins of an evidence record.
const (
	OriginObservedEvidence = "observed" // first seen while OpsTide was watching
	OriginAdoptedEvidence  = "adopted"  // already there when OpsTide began watching
)

// Evidence is one Kubernetes Event as OpsTide recorded it: an allow-listed,
// size-bounded copy of what Kubernetes reported, plus links to resources
// OpsTide had seen, made only from UIDs and ownerReferences.
//
// Reason, Message and EventType are Kubernetes' words, not OpsTide's. A zero
// FirstAt or LastAt means the source gave none.
type Evidence struct {
	ID        string
	Source    string
	SourceUID string
	Namespace string
	Name      string

	Reason           string
	EventType        string
	Message          string
	MessageTruncated bool
	Action           string
	Reporter         string

	Regarding Ref
	Related   Ref

	FirstAt    time.Time
	LastAt     time.Time
	Count      int
	CountBasis string // series | deprecatedCount | count | "" when the source gave none

	ObservedAt      time.Time // first seen by OpsTide
	Origin          string
	At              time.Time // the time it sorts by
	UpdatedAt       time.Time
	SourceDeletedAt time.Time // when OpsTide saw Kubernetes drop the Event; zero while it exists

	// CountLogged is the count the Log last showed, and when.
	CountLogged   int
	CountLoggedAt time.Time

	// Links, by UID. LinkVia says how, in words: "regarding.uid", or
	// "Pod ownerReference to ReplicaSet to Deployment" and the like.
	ReplicaSetUID string
	WorkloadID    string
	RunID         string
	OperationID   string
	HPAUID        string
	LinkVia       string
}

// Ref names a resource an Event refers to, as the Event states it.
type Ref struct {
	Kind, Name, UID string
}

// SortTime is the time an Event is ordered by: when Kubernetes says it last
// happened, else when it first did, else when OpsTide saw it.
func (e Evidence) SortTime() time.Time {
	switch {
	case !e.LastAt.IsZero():
		return e.LastAt
	case !e.FirstAt.IsZero():
		return e.FirstAt
	}
	return e.ObservedAt
}

const evidenceCols = `id, source, source_uid, namespace, name, reason, event_type, message, message_truncated, action, reporter,
	regarding_kind, regarding_name, regarding_uid, related_kind, related_name, related_uid, first_at, last_at, count, count_basis,
	observed_at, origin, at, updated_at, source_deleted_at, count_logged, count_logged_at, rs_uid, workload_id, run_id, operation_id, hpa_uid, link_via`

func scanEvidence(sc interface{ Scan(...any) error }) (Evidence, error) {
	var e Evidence
	var first, last, deleted, loggedAt sql.NullInt64
	var observed, at, upd int64
	var trunc int
	err := sc.Scan(&e.ID, &e.Source, &e.SourceUID, &e.Namespace, &e.Name, &e.Reason, &e.EventType, &e.Message, &trunc, &e.Action, &e.Reporter,
		&e.Regarding.Kind, &e.Regarding.Name, &e.Regarding.UID, &e.Related.Kind, &e.Related.Name, &e.Related.UID, &first, &last, &e.Count, &e.CountBasis,
		&observed, &e.Origin, &at, &upd, &deleted, &e.CountLogged, &loggedAt, &e.ReplicaSetUID, &e.WorkloadID, &e.RunID, &e.OperationID, &e.HPAUID, &e.LinkVia)
	if err != nil {
		return e, err
	}
	e.MessageTruncated = trunc != 0
	e.FirstAt, e.LastAt, e.SourceDeletedAt, e.CountLoggedAt = fromMS(first), fromMS(last), fromMS(deleted), fromMS(loggedAt)
	e.ObservedAt, e.At, e.UpdatedAt = time.UnixMilli(observed).UTC(), time.UnixMilli(at).UTC(), time.UnixMilli(upd).UTC()
	return e, nil
}

// EvidenceByUID returns the record of the Event with this UID.
func (w *Writer) EvidenceByUID(ctx context.Context, uid string) (Evidence, bool, error) {
	e, err := scanEvidence(w.tx.QueryRowContext(ctx, `SELECT `+evidenceCols+` FROM evidence WHERE source_uid = ?`, uid))
	if errors.Is(err, sql.ErrNoRows) {
		return Evidence{}, false, nil
	}
	return e, err == nil, err
}

// SaveEvidence inserts or replaces the record. The caller decides whether
// anything changed; this always writes.
func (w *Writer) SaveEvidence(ctx context.Context, e Evidence) error {
	trunc := 0
	if e.MessageTruncated {
		trunc = 1
	}
	_, err := w.tx.ExecContext(ctx, `INSERT OR REPLACE INTO evidence (`+evidenceCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.Source, e.SourceUID, e.Namespace, e.Name, e.Reason, e.EventType, e.Message, trunc, e.Action, e.Reporter,
		e.Regarding.Kind, e.Regarding.Name, e.Regarding.UID, e.Related.Kind, e.Related.Name, e.Related.UID, ms(e.FirstAt), ms(e.LastAt), e.Count, e.CountBasis,
		ms(e.ObservedAt).Int64, e.Origin, ms(e.SortTime()).Int64, ms(w.now()).Int64, ms(e.SourceDeletedAt), e.CountLogged, ms(e.CountLoggedAt),
		e.ReplicaSetUID, e.WorkloadID, e.RunID, e.OperationID, e.HPAUID, e.LinkVia)
	return err
}

// MarkEvidenceSourceGone notes that Kubernetes dropped the Event. The record stays.
func (w *Writer) MarkEvidenceSourceGone(ctx context.Context, uid string, at time.Time) error {
	_, err := w.tx.ExecContext(ctx, `UPDATE evidence SET source_deleted_at = ?, updated_at = ? WHERE source_uid = ? AND source_deleted_at IS NULL`,
		ms(at).Int64, ms(w.now()).Int64, uid)
	return err
}

// LiveEvidence lists the Events last recorded as still existing in Kubernetes, for the
// startup check of Events that were dropped while OpsTide was not running.
func (s *Store) LiveEvidence(ctx context.Context) ([]Evidence, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+evidenceCols+` FROM evidence WHERE source_deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Evidence
	for rows.Next() {
		e, err := scanEvidence(rows)
		if err != nil {
			return nil, err
		}
		e.Message = "" // not needed, and not held in memory
		out = append(out, e)
	}
	return out, rows.Err()
}

// RelinkEvidenceOfPod links the Events recorded about a Pod before OpsTide
// knew which ReplicaSet owned it.
func (w *Writer) RelinkEvidenceOfPod(ctx context.Context, podUID, rsUID, workloadID string) error {
	_, err := w.tx.ExecContext(ctx, `UPDATE evidence SET rs_uid = ?, workload_id = ?, link_via = 'Pod ownerReference to ReplicaSet to Deployment', updated_at = ?
WHERE regarding_uid = ? AND regarding_uid <> '' AND regarding_kind = 'Pod' AND rs_uid = ''`, rsUID, workloadID, ms(w.now()).Int64, podUID)
	return err
}

// RelinkEvidenceOfReplicaSet links the Events about a ReplicaSet, and about
// its Pods, to the Deployment that owns it, once that is known.
func (w *Writer) RelinkEvidenceOfReplicaSet(ctx context.Context, rsUID, workloadID string) error {
	_, err := w.tx.ExecContext(ctx, `UPDATE evidence SET workload_id = ?, updated_at = ? WHERE rs_uid = ? AND rs_uid <> '' AND workload_id = ''`,
		workloadID, ms(w.now()).Int64, rsUID)
	return err
}

// RelinkEvidenceOfWorkload links the Events about a Deployment (by UID) and
// about the HorizontalPodAutoscalers that target it.
func (w *Writer) RelinkEvidenceOfWorkload(ctx context.Context, workloadID string) error {
	_, err := w.tx.ExecContext(ctx, `UPDATE evidence SET workload_id = ?, updated_at = ? WHERE regarding_uid = ? AND regarding_uid <> '' AND regarding_kind = 'Deployment' AND workload_id = ''`,
		workloadID, ms(w.now()).Int64, workloadID)
	return err
}

// EvidenceQuery selects evidence records. Zero fields do not filter.
type EvidenceQuery struct {
	From, To     time.Time // by At, [From, To)
	Namespace    string
	Reasons      []string
	EventTypes   []string // Normal, Warning
	RegardingUID string
	Kind         string // of the resource the Event is about
	WorkloadID   string
	ReplicaSet   string // a ReplicaSet UID
	RunID        string
	HPAUID       string
	Text         string
	Cursor       string
	Limit        int // default 100, at most 500
}

// EvidencePage is a page of evidence, newest first.
type EvidencePage struct {
	Records    []Evidence
	NextCursor string
}

// Evidence lists records, newest first, by keyset pagination.
func (s *Store) Evidence(ctx context.Context, q EvidenceQuery) (EvidencePage, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 500)
	var where []string
	var args []any
	add := func(clause string, a ...any) { where, args = append(where, clause), append(args, a...) }
	if !q.From.IsZero() {
		add("at >= ?", q.From.UnixMilli())
	}
	if !q.To.IsZero() {
		add("at < ?", q.To.UnixMilli())
	}
	if q.Namespace != "" {
		add("namespace = ?", q.Namespace)
	}
	if len(q.Reasons) > 0 {
		add("reason IN ("+marks(len(q.Reasons))+")", anys(q.Reasons)...)
	}
	if len(q.EventTypes) > 0 {
		add("event_type IN ("+marks(len(q.EventTypes))+")", anys(q.EventTypes)...)
	}
	if q.RegardingUID != "" {
		add("regarding_uid = ? AND regarding_uid <> ''", q.RegardingUID)
	}
	if q.Kind != "" {
		add("regarding_kind = ?", q.Kind)
	}
	if q.WorkloadID != "" {
		add("workload_id = ? AND workload_id <> ''", q.WorkloadID)
	}
	if q.ReplicaSet != "" {
		add("rs_uid = ? AND rs_uid <> ''", q.ReplicaSet)
	}
	if q.RunID != "" {
		add("run_id = ? AND run_id <> ''", q.RunID)
	}
	if q.HPAUID != "" {
		add("hpa_uid = ? AND hpa_uid <> ''", q.HPAUID)
	}
	if q.Text != "" {
		like := "%" + escapeLike(q.Text) + "%"
		add(`(message LIKE ? ESCAPE '\' OR regarding_name LIKE ? ESCAPE '\')`, like, like)
	}
	if q.Cursor != "" {
		at, id, err := parseEvidenceCursor(q.Cursor)
		if err != nil {
			return EvidencePage{}, err
		}
		add("(at < ? OR (at = ? AND id < ?))", at, at, id)
	}
	return s.queryEvidence(ctx, where, args, limit)
}

func (s *Store) queryEvidence(ctx context.Context, where []string, args []any, limit int) (EvidencePage, error) {
	sqlq := `SELECT ` + evidenceCols + ` FROM evidence`
	if len(where) > 0 {
		// Only fixed clauses are joined; every value is a placeholder.
		sqlq += " WHERE " + strings.Join(where, " AND ") //nolint:gosec // see above
	}
	sqlq += " ORDER BY at DESC, id DESC LIMIT ?"
	rows, err := s.db.QueryContext(ctx, sqlq, append(args, limit+1)...)
	if err != nil {
		return EvidencePage{}, err
	}
	defer rows.Close()
	var page EvidencePage
	for rows.Next() {
		e, err := scanEvidence(rows)
		if err != nil {
			return EvidencePage{}, err
		}
		page.Records = append(page.Records, e)
	}
	if err := rows.Err(); err != nil {
		return EvidencePage{}, err
	}
	if len(page.Records) > limit {
		page.Records = page.Records[:limit]
		last := page.Records[limit-1]
		page.NextCursor = fmt.Sprintf("%d.%s", last.At.UnixMilli(), last.ID)
	}
	return page, nil
}

// EvidenceByID returns one record.
func (s *Store) EvidenceByID(ctx context.Context, id string) (Evidence, bool, error) {
	e, err := scanEvidence(s.db.QueryRowContext(ctx, `SELECT `+evidenceCols+` FROM evidence WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Evidence{}, false, nil
	}
	return e, err == nil, err
}

// ReleaseEvidenceQuery selects what is recorded for a release: Events about its
// ReplicaSet or the Pods that ReplicaSet owns, and Events about the Deployment
// or an autoscaler targeting it, within [From, To).
type ReleaseEvidenceQuery struct {
	ReplicaSetUID string
	WorkloadID    string
	From, To      time.Time // To zero: open ended
	Limit         int
}

// ReleaseEvidence returns the records for a release, newest first.
func (s *Store) ReleaseEvidence(ctx context.Context, q ReleaseEvidenceQuery) (EvidencePage, error) {
	// An Event overlaps the window when it began before the window ended and was last
	// seen after it began: one that kept repeating after a newer revision took over still
	// belongs to the release it began under.
	where := []string{`((rs_uid = ? AND rs_uid <> '') OR (workload_id = ? AND workload_id <> '' AND rs_uid = '' AND regarding_kind IN ('Deployment', 'HorizontalPodAutoscaler')))`, "at >= ?"}
	args := []any{q.ReplicaSetUID, q.WorkloadID, q.From.UnixMilli()}
	if !q.To.IsZero() {
		where, args = append(where, "coalesce(first_at, at) < ?"), append(args, q.To.UnixMilli())
	}
	return s.queryEvidence(ctx, where, args, min(max(q.Limit, 1), 500))
}

// EvidenceStats describes the evidence record for the System screen.
type EvidenceStats struct {
	Records, Warnings int64
	Latest            time.Time // when OpsTide last first-saw an Event
	HPAs              int64
}

// EvidenceStats counts what is recorded.
func (s *Store) EvidenceStats(ctx context.Context) (EvidenceStats, error) {
	var st EvidenceStats
	var latest sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(event_type = 'Warning'), 0), max(observed_at) FROM evidence`).Scan(&st.Records, &st.Warnings, &latest)
	if err != nil {
		return st, err
	}
	st.Latest = fromMS(latest)
	err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM hpas WHERE deleted_at IS NULL`).Scan(&st.HPAs)
	return st, err
}

func parseEvidenceCursor(c string) (int64, string, error) {
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

func marks(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func anys(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

// ---- HorizontalPodAutoscalers ----

// HPA is a HorizontalPodAutoscaler as last observed. Conditions and Metrics are
// compact JSON built by the collector from an allow-list.
type HPA struct {
	UID, Namespace, Name              string
	TargetAPI, TargetKind, TargetName string
	TargetUID, WorkloadID             string
	MinReplicas                       *int32
	MaxReplicas                       int32
	CurrentReplicas, DesiredReplicas  int32
	LastScaleAt                       time.Time
	Conditions, Metrics               string
	FirstSeenAt, UpdatedAt, DeletedAt time.Time
}

const hpaCols = `uid, namespace, name, target_api, target_kind, target_name, target_uid, workload_id, min_replicas, max_replicas,
	current_replicas, desired_replicas, last_scale_at, conditions, metrics, first_seen_at, updated_at, deleted_at`

func scanHPA(sc interface{ Scan(...any) error }) (HPA, error) {
	var h HPA
	var minR, last, deleted sql.NullInt64
	var first, upd int64
	err := sc.Scan(&h.UID, &h.Namespace, &h.Name, &h.TargetAPI, &h.TargetKind, &h.TargetName, &h.TargetUID, &h.WorkloadID, &minR, &h.MaxReplicas,
		&h.CurrentReplicas, &h.DesiredReplicas, &last, &h.Conditions, &h.Metrics, &first, &upd, &deleted)
	if err != nil {
		return h, err
	}
	if minR.Valid {
		v := int32(minR.Int64)
		h.MinReplicas = &v
	}
	h.LastScaleAt, h.DeletedAt = fromMS(last), fromMS(deleted)
	h.FirstSeenAt, h.UpdatedAt = time.UnixMilli(first).UTC(), time.UnixMilli(upd).UTC()
	return h, nil
}

// HPAByUID returns the autoscaler with this UID.
func (w *Writer) HPAByUID(ctx context.Context, uid string) (HPA, bool, error) {
	h, err := scanHPA(w.tx.QueryRowContext(ctx, `SELECT `+hpaCols+` FROM hpas WHERE uid = ?`, uid))
	if errors.Is(err, sql.ErrNoRows) {
		return HPA{}, false, nil
	}
	return h, err == nil, err
}

// SaveHPA inserts or replaces an autoscaler's observed state.
func (w *Writer) SaveHPA(ctx context.Context, h HPA) error {
	var minR sql.NullInt64
	if h.MinReplicas != nil {
		minR = sql.NullInt64{Int64: int64(*h.MinReplicas), Valid: true}
	}
	_, err := w.tx.ExecContext(ctx, `INSERT OR REPLACE INTO hpas (`+hpaCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		h.UID, h.Namespace, h.Name, h.TargetAPI, h.TargetKind, h.TargetName, h.TargetUID, h.WorkloadID, minR, h.MaxReplicas,
		h.CurrentReplicas, h.DesiredReplicas, ms(h.LastScaleAt), h.Conditions, h.Metrics, ms(h.FirstSeenAt).Int64, ms(w.now()).Int64, ms(h.DeletedAt))
	return err
}

// MarkHPADeleted notes that the autoscaler is gone; its record stays.
func (w *Writer) MarkHPADeleted(ctx context.Context, uid string, at time.Time) error {
	_, err := w.tx.ExecContext(ctx, `UPDATE hpas SET deleted_at = ?, updated_at = ? WHERE uid = ? AND deleted_at IS NULL`, ms(at).Int64, ms(w.now()).Int64, uid)
	return err
}

// RelinkHPAs links autoscalers that target a Deployment by name to its UID, and
// the Events recorded about them.
// A Deployment deleted and created again under the same name has a new UID: autoscalers
// that named it are moved to it, so its scaling context is not left empty.
func (w *Writer) RelinkHPAs(ctx context.Context, namespace, name, workloadID string) error {
	if workloadID == "" {
		return nil
	}
	if _, err := w.tx.ExecContext(ctx, `UPDATE hpas SET target_uid = ?, workload_id = ?, updated_at = ?
WHERE namespace = ? AND target_kind = 'Deployment' AND target_name = ? AND workload_id <> ?`, workloadID, workloadID, ms(w.now()).Int64, namespace, name, workloadID); err != nil {
		return err
	}
	_, err := w.tx.ExecContext(ctx, `UPDATE evidence SET workload_id = ?, updated_at = ? WHERE workload_id <> ? AND hpa_uid <> ''
AND hpa_uid IN (SELECT uid FROM hpas WHERE workload_id = ?)`, workloadID, ms(w.now()).Int64, workloadID, workloadID)
	return err
}

// HPAsOfWorkload lists the autoscalers whose target is the Deployment with this UID.
func (s *Store) HPAsOfWorkload(ctx context.Context, workloadID string) ([]HPA, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+hpaCols+` FROM hpas WHERE workload_id = ? AND workload_id <> '' ORDER BY namespace, name`, workloadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HPA
	for rows.Next() {
		h, err := scanHPA(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// LiveHPAs lists autoscalers last recorded as existing.
func (s *Store) LiveHPAs(ctx context.Context) ([]HPA, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+hpaCols+` FROM hpas WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HPA
	for rows.Next() {
		h, err := scanHPA(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// WorkloadSpecReplicas is the Deployment's replica target as last recorded.
func (w *Writer) WorkloadSpecReplicas(ctx context.Context, id string) (n int32, known bool, err error) {
	var v sql.NullInt64
	err = w.tx.QueryRowContext(ctx, `SELECT spec_replicas FROM workloads WHERE id = ?`, id).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return int32(v.Int64), v.Valid, err
}

// SetWorkloadSpecReplicas records the Deployment's replica target.
func (w *Writer) SetWorkloadSpecReplicas(ctx context.Context, id string, n int32) error {
	_, err := w.tx.ExecContext(ctx, `UPDATE workloads SET spec_replicas = ? WHERE id = ?`, n, id)
	return err
}

// Workload returns a Deployment as recorded.
func (s *Store) Workload(ctx context.Context, id string) (Workload, bool, error) {
	var wl Workload
	var first, last int64
	err := s.db.QueryRowContext(ctx, `SELECT id, namespace, name, first_seen_at, last_seen_at FROM workloads WHERE id = ?`, id).Scan(&wl.ID, &wl.Namespace, &wl.Name, &first, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return Workload{}, false, nil
	}
	wl.FirstSeen, wl.LastSeen = time.UnixMilli(first).UTC(), time.UnixMilli(last).UTC()
	return wl, err == nil, err
}

// SpecReplicas is the Deployment's replica target as last recorded; ok is false when unknown.
func (s *Store) SpecReplicas(ctx context.Context, id string) (int32, bool, error) {
	var v sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT spec_replicas FROM workloads WHERE id = ?`, id).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return int32(v.Int64), v.Valid, err
}
