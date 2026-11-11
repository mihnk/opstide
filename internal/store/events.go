package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Confidence says how an entry is known.
type Confidence string

const (
	// Observed: a value read from the Kubernetes API, with its time.
	Observed Confidence = "observed"
	// Derived: a documented, deterministic reading of observed values,
	// such as a Job's Failed=True condition meaning the run failed.
	Derived Confidence = "derived"
	// Unknown: missing, stale or partial; shown as such, never guessed.
	Unknown Confidence = "unknown"
)

// Event is one entry in the log.
type Event struct {
	Seq int64
	// ID is deterministic from the source occurrence (for example
	// "job.failed/<job uid>"), so seeing the same thing again after a
	// relist or a restart does not add a second entry.
	ID           string
	Type         string
	Namespace    string
	Kind         string
	ResourceUID  string
	ResourceName string
	OperationID  string
	RunID        string
	// ReleaseID ties the entry to a workload release, "" for most.
	ReleaseID string
	// Reason is the reason Kubernetes reported, for entries that carry one
	// (Kubernetes Events); WorkloadID is the Deployment the entry is about,
	// when the link is by UID or ownerReference. Both are "" for most entries.
	Reason     string
	WorkloadID string
	// OccurredAt is the source's own time for the occurrence, zero when it
	// has none. ObservedAt is when OpsTide saw it; RecordedAt when it was
	// written.
	OccurredAt time.Time
	ObservedAt time.Time
	RecordedAt time.Time
	Severity   string // info | warning | error
	Confidence Confidence
	Summary    string
	Facts      map[string]string
}

// At is the time the entry sorts by: when it happened if known, else when
// it was seen.
func (e Event) At() time.Time {
	if !e.OccurredAt.IsZero() {
		return e.OccurredAt
	}
	return e.ObservedAt
}

// AddEvent records e unless an event with its ID exists. It reports
// whether it was added.
func (w *Writer) AddEvent(ctx context.Context, e Event) (bool, error) {
	if e.ID == "" || e.Type == "" || e.ObservedAt.IsZero() {
		return false, fmt.Errorf("event needs an id, a type and an observed time: %+v", e)
	}
	facts, _ := json.Marshal(e.Facts)
	if e.Facts == nil {
		facts = []byte("{}")
	}
	res, err := w.tx.ExecContext(ctx, `
INSERT INTO events (id, type, namespace, kind, resource_uid, resource_name, operation_id, run_id,
	occurred_at, observed_at, recorded_at, at, severity, confidence, summary, facts, release_id, reason, workload_id)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`,
		e.ID, e.Type, e.Namespace, e.Kind, e.ResourceUID, e.ResourceName, e.OperationID, e.RunID,
		ms(e.OccurredAt), ms(e.ObservedAt).Int64, ms(w.now()).Int64, ms(e.At()).Int64,
		e.Severity, string(e.Confidence), e.Summary, string(facts), e.ReleaseID, e.Reason, e.WorkloadID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// EventQuery selects log entries. Zero fields do not filter.
type EventQuery struct {
	From, To  time.Time // by At, [From, To)
	Namespace string
	Types     []string // exact types, or prefixes ending in "." ("job.")
	// ExcludeTypes leaves out types the same way Types selects them.
	ExcludeTypes []string
	OperationID  string
	RunID        string
	ReleaseID    string
	// Kind, Reason and WorkloadID narrow to the resource kind an entry is about,
	// the reason Kubernetes reported, and the Deployment (by UID) it concerns.
	Kind, Reason, WorkloadID string
	// ResourceUID narrows to the entries about one resource; with Kind it is served by an index.
	ResourceUID string
	// AlsoResourceUIDs widens WorkloadID: entries about these resources (the ConfigMaps and Secrets a workload's Pod
	// template names) are included as well. Ignored without WorkloadID.
	AlsoResourceUIDs []string
	Text             string   // substring of the summary or resource name
	Severities       []string // info, warning or error
	// Owner keeps entries of operations whose owner annotation is exactly this.
	Owner string
	// Cursor continues a previous page (its NextCursor).
	Cursor string
	Limit  int // default 100, at most 500
}

// EventPage is one page of entries, newest first.
type EventPage struct {
	Events     []Event
	NextCursor string // "" when this is the last page
}

// Events returns entries matching q, newest first, by keyset pagination:
// a cursor stays valid while new entries arrive.
func (s *Store) Events(ctx context.Context, q EventQuery) (EventPage, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 500)

	var where []string
	var args []any
	if !q.From.IsZero() {
		where, args = append(where, "at >= ?"), append(args, q.From.UnixMilli())
	}
	if !q.To.IsZero() {
		where, args = append(where, "at < ?"), append(args, q.To.UnixMilli())
	}
	if q.Namespace != "" {
		where, args = append(where, "namespace = ?"), append(args, q.Namespace)
	}
	if q.OperationID != "" {
		where, args = append(where, "operation_id = ?"), append(args, q.OperationID)
	}
	if q.ReleaseID != "" {
		// The second term is there so SQLite may use the partial index events_release,
		// which it does only when the query states what the index's WHERE says.
		where, args = append(where, "release_id = ? AND release_id <> ''"), append(args, q.ReleaseID)
	}
	if q.Kind != "" {
		where, args = append(where, "kind = ?"), append(args, q.Kind)
	}
	if q.ResourceUID != "" {
		where, args = append(where, "resource_uid = ? AND resource_uid <> ''"), append(args, q.ResourceUID)
	}
	if q.Reason != "" {
		where, args = append(where, "reason = ? AND reason <> ''"), append(args, q.Reason)
	}
	if q.WorkloadID != "" && len(q.AlsoResourceUIDs) > 0 {
		marks := strings.TrimSuffix(strings.Repeat("?,", len(q.AlsoResourceUIDs)), ",")
		where, args = append(where, "((workload_id = ? AND workload_id <> '') OR (resource_uid IN ("+marks+") AND resource_uid <> ''))"), append(args, q.WorkloadID)
		for _, u := range q.AlsoResourceUIDs {
			args = append(args, u)
		}
	} else if q.WorkloadID != "" {
		where, args = append(where, "workload_id = ? AND workload_id <> ''"), append(args, q.WorkloadID)
	}
	if q.RunID != "" {
		where, args = append(where, "run_id = ?"), append(args, q.RunID)
	}
	if len(q.Severities) > 0 {
		marks := strings.TrimSuffix(strings.Repeat("?,", len(q.Severities)), ",")
		where = append(where, "severity IN ("+marks+")")
		for _, v := range q.Severities {
			args = append(args, v)
		}
	}
	if q.Owner != "" {
		where, args = append(where, "operation_id IN (SELECT id FROM operations WHERE owner = ?)"), append(args, q.Owner)
	}
	if len(q.Types) > 0 {
		var ors []string
		for _, t := range q.Types {
			if strings.HasSuffix(t, ".") {
				ors, args = append(ors, "substr(type, 1, ?) = ?"), append(args, len(t), t)
			} else {
				ors, args = append(ors, "type = ?"), append(args, t)
			}
		}
		where = append(where, "("+strings.Join(ors, " OR ")+")")
	}
	for _, t := range q.ExcludeTypes {
		if strings.HasSuffix(t, ".") {
			where, args = append(where, "substr(type, 1, ?) <> ?"), append(args, len(t), t)
		} else {
			where, args = append(where, "type <> ?"), append(args, t)
		}
	}
	if q.Text != "" {
		like := "%" + escapeLike(q.Text) + "%"
		where, args = append(where, `(summary LIKE ? ESCAPE '\' OR resource_name LIKE ? ESCAPE '\')`), append(args, like, like)
	}
	if q.Cursor != "" {
		at, seq, err := parseCursor(q.Cursor)
		if err != nil {
			return EventPage{}, err
		}
		where, args = append(where, "(at < ? OR (at = ? AND seq < ?))"), append(args, at, at, seq)
	}
	sqlq := `SELECT seq, id, type, namespace, kind, resource_uid, resource_name, operation_id, run_id,
	occurred_at, observed_at, recorded_at, severity, confidence, summary, facts, release_id, reason, workload_id FROM events`
	if len(where) > 0 {
		// Only the fixed clauses above are joined; every value is a placeholder.
		sqlq += " WHERE " + strings.Join(where, " AND ") //nolint:gosec // see above
	}
	sqlq += " ORDER BY at DESC, seq DESC LIMIT ?"
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return EventPage{}, err
	}
	defer rows.Close()
	var page EventPage
	for rows.Next() {
		var e Event
		var occurred sql.NullInt64
		var observed, recorded int64
		var conf, facts string
		if err := rows.Scan(&e.Seq, &e.ID, &e.Type, &e.Namespace, &e.Kind, &e.ResourceUID, &e.ResourceName,
			&e.OperationID, &e.RunID, &occurred, &observed, &recorded, &e.Severity, &conf, &e.Summary, &facts, &e.ReleaseID, &e.Reason, &e.WorkloadID); err != nil {
			return EventPage{}, err
		}
		e.OccurredAt = fromMS(occurred)
		e.ObservedAt, e.RecordedAt = time.UnixMilli(observed).UTC(), time.UnixMilli(recorded).UTC()
		e.Confidence = Confidence(conf)
		_ = json.Unmarshal([]byte(facts), &e.Facts)
		page.Events = append(page.Events, e)
	}
	if err := rows.Err(); err != nil {
		return EventPage{}, err
	}
	if len(page.Events) > limit {
		page.Events = page.Events[:limit]
		last := page.Events[limit-1]
		page.NextCursor = fmt.Sprintf("%d.%d", last.At().UnixMilli(), last.Seq)
	}
	return page, nil
}

func parseCursor(c string) (at, seq int64, err error) {
	a, b, ok := strings.Cut(c, ".")
	if ok {
		if at, err = strconv.ParseInt(a, 10, 64); err == nil {
			seq, err = strconv.ParseInt(b, 10, 64)
		}
	}
	if !ok || err != nil {
		return 0, 0, fmt.Errorf("bad cursor %q", c)
	}
	return at, seq, nil
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// SuspensionEvents returns the suspend and resume entries of every CronJob
// (or of one, when operationID is set), grouped by operation, oldest first
// within each. Views build a CronJob's suspended periods from them.
func (s *Store) SuspensionEvents(ctx context.Context, operationID string) (map[string][]Event, error) {
	out := map[string][]Event{}
	q := EventQuery{OperationID: operationID, Types: []string{"cronjob.suspended", "cronjob.resumed"}, Limit: 500}
	for {
		page, err := s.Events(ctx, q)
		if err != nil {
			return nil, err
		}
		for _, e := range page.Events { // newest first
			out[e.OperationID] = append(out[e.OperationID], e)
		}
		if page.NextCursor == "" {
			for _, evs := range out {
				slices.Reverse(evs) // oldest first
			}
			return out, nil
		}
		q.Cursor = page.NextCursor
	}
}
