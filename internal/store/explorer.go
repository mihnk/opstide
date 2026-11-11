package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The Explorer reads the Log (events) and the Kubernetes Event record (evidence) together. It does not
// copy either into a third table: each source is queried on its own index, bounded by the time range and
// the page size, and the few rows that come back are merged. A Kubernetes Warning Event is both an
// `evidence` row and an `evidence.*` Log entry; the Explorer shows the row only, once.
//
// Order: every record has one record time that never changes after it is written, so a cursor
// can neither skip nor repeat a record. It is the time the source reported the occurrence when it gave
// one (for a Kubernetes Event, its first occurrence), otherwise when OpsTide first observed it. It is
// never invented: ObservedAt is always kept beside it. Ties are broken by the record id.

// Explorer record sources.
const (
	ExplorerEvent    = "event"    // a Log entry (state transition, run, rollout, Pod, Node, scaling, coverage)
	ExplorerEvidence = "evidence" // a Kubernetes Event
)

// Explorer groups: which kind of record. A Log entry belongs to the first group whose prefix its type has.
var explorerGroupPrefixes = []struct {
	Key      string
	Label    string
	Prefixes []string
}{
	{"operations", "Scheduled operations", []string{"cronjob.", "job."}},
	{"releases", "Deployment releases", []string{"release.", "deployment."}},
	{"workloads", "StatefulSets and DaemonSets", []string{"statefulset.", "daemonset."}},
	{"pods", "Pod lifecycle", []string{"pod."}},
	{"nodes", "Node conditions", []string{"node."}},
	{"scaling", "Scaling", []string{"hpa.", "workload."}},
	{"config", "ConfigMaps and Secrets", []string{"configmap.", "secret."}},
	{"observation", "Observation", []string{"collection."}},
}

// ExplorerGroupEvents is the group of Kubernetes Events.
const ExplorerGroupEvents = "events"

// ExplorerGroups lists the groups in display order, with their labels.
func ExplorerGroups() [][2]string {
	out := make([][2]string, 0, len(explorerGroupPrefixes)+1)
	for _, g := range explorerGroupPrefixes {
		out = append(out, [2]string{g.Key, g.Label})
	}
	return append(out, [2]string{ExplorerGroupEvents, "Kubernetes Events"})
}

// GroupOfType is the group of a Log entry type, "" when it has none.
func GroupOfType(t string) string {
	for _, g := range explorerGroupPrefixes {
		for _, p := range g.Prefixes {
			if strings.HasPrefix(t, p) {
				return g.Key
			}
		}
	}
	return ""
}

// Routine entries are the ordinary life of Pods, and Kubernetes Events of type Normal. Views may leave them
// out by default, say how many they left out, and show them on request.
// Reported replica counts and Job creation say little on their own; they are routine too.
// Helm's release records (ConfigMaps and Secrets named sh.helm.release.v1.*, labelled owner=helm) are routine as well:
// each upgrade makes one and relabels the one before, and the release itself is recorded elsewhere.
var routineTypes = append([]string{"pod.created", "pod.ready", "pod.terminated", "job.created", "statefulset.counts_changed", "daemonset.counts_changed"}, HelmReleaseTypes...)

// HelmReleaseTypes are the entries about Helm's release records (see routineTypes).
var HelmReleaseTypes = []string{"configmap.helm_release_created", "configmap.helm_release_written", "configmap.helm_release_deleted",
	"secret.helm_release_created", "secret.helm_release_written", "secret.helm_release_deleted"}

// ConfigRoutineTypes are the ConfigMap and Secret entries counted against the routine-volume retention budget: Helm's
// release records, and the folded writes of an object written again within five minutes of its previous write entry.
// Folded writes stay visible by default; they are only budgeted with the routine volume.
var ConfigRoutineTypes = append(append([]string{}, HelmReleaseTypes...), "configmap.writes_folded", "secret.writes_folded")

// IsRoutineType says whether a Log entry type is routine.
func IsRoutineType(t string) bool {
	for _, r := range routineTypes {
		if r == t {
			return true
		}
	}
	return false
}

// Record sources as the Explorer names them. They say where a fact comes from, not how much to trust it.
const (
	SourceKubernetesAPI = "kubernetes.api"   // an object's state, read from the Kubernetes API by an informer
	SourceOpsTideRule   = "opstide.rule"     // a documented, deterministic result of OpsTide's own rules
	SourceCoverage      = "opstide.coverage" // OpsTide's own account of what it could and could not watch
	EvidenceTypeName    = "kubernetes.event" // the type of a Kubernetes Event record
	evidenceSourcePrefx = "kubernetes."      // + "events.k8s.io/v1" or "core/v1"
)

// SourceOfEvent is the source of a Log entry.
func SourceOfEvent(e Event) string {
	switch {
	case strings.HasPrefix(e.Type, "collection."):
		return SourceCoverage
	case e.Confidence == Derived:
		return SourceOpsTideRule
	}
	return SourceKubernetesAPI
}

// ExplorerQuery selects records. Zero fields do not filter. From and To are required: a range is bounded.
type ExplorerQuery struct {
	From, To                       time.Time // by record time, [From, To)
	Namespace                      string
	Kind, Name, UID                string // the resource a record is about
	WorkloadID, RunID, OperationID string
	// ReleaseID selects Log entries tied to the release, and Kubernetes Events about its ReplicaSet, the Pods that own,
	// or its Deployment while it was current (the same rule as the release's evidence): fill the Release fields below.
	ReleaseID                          string
	ReleaseReplicaSet, ReleaseWorkload string
	ReleaseFrom, ReleaseTo             time.Time
	NodeID                             string
	Groups                             []string
	Types                              []string // exact Log entry types; EvidenceTypeName selects Kubernetes Events
	Reason                             string
	// Severities keeps Log entries of these severities (info, warning, error); a Kubernetes Event is "warning" when
	// Kubernetes typed it Warning, else "info".
	Severities []string
	// Owner keeps records of operations whose owner annotation is exactly this.
	Owner      string
	Sources    []string
	Provenance []string // observed | derived | unknown
	Text       string   // names, reasons and summaries only; never Event messages
	Routine    bool     // include routine records
	// Exclude leaves out records whose record time falls in one of these stretches, in one of its namespaces (all
	// namespaces, cluster-scoped records included, when it names none): "outside maintenance windows only".
	Exclude []ExcludeSpan
	Cursor  string
	Limit   int  // default 100, at most 500
	Asc     bool // oldest first (exports)

	routineOnly bool // counting the routine records a query leaves out
	insideOnly  bool // counting the records Exclude leaves out
}

// ExcludeSpan is a stretch of record time, [From, To), optionally limited to some namespaces.
type ExcludeSpan struct {
	From, To   time.Time
	Namespaces []string
}

// ClusterScopedKinds are the built-in kinds that live outside any namespace. A Kubernetes Event about one of them is
// stored in a namespace (normally default), but the record is about a cluster-scoped object: for maintenance windows
// it is in no namespace, so only a cluster-wide window covers it.
var ClusterScopedKinds = []string{"APIService", "CSIDriver", "CSINode", "CertificateSigningRequest", "ClusterRole", "ClusterRoleBinding",
	"CustomResourceDefinition", "IngressClass", "MutatingWebhookConfiguration", "Namespace", "Node", "PersistentVolume", "PriorityClass",
	"RuntimeClass", "StorageClass", "ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding", "ValidatingWebhookConfiguration", "VolumeAttachment"}

// IsClusterScoped says whether kind is one of ClusterScopedKinds.
func IsClusterScoped(kind string) bool {
	for _, k := range ClusterScopedKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// evidenceNamespaceExpr is a Kubernetes Event's namespace for maintenance matching: "" when it is about a
// cluster-scoped object.
var evidenceNamespaceExpr = func() string {
	q := make([]string, len(ClusterScopedKinds))
	for i, k := range ClusterScopedKinds {
		q[i] = "'" + k + "'"
	}
	return "(CASE WHEN regarding_kind IN (" + strings.Join(q, ", ") + ") THEN '' ELSE namespace END)"
}()

// insideSpans is (inside any of the spans), on the given time column and namespace expression.
func insideSpans(col, nsExpr string, spans []ExcludeSpan) (string, []any) {
	var ors []string
	var args []any
	for _, sp := range spans {
		part := "(" + col + " >= ? AND " + col + " < ?"
		args = append(args, sp.From.UnixMilli(), sp.To.UnixMilli())
		if len(sp.Namespaces) > 0 {
			cond, a := in(nsExpr, sp.Namespaces)
			part += " AND " + cond
			args = append(args, a...)
		}
		ors = append(ors, part+")")
	}
	return "(" + strings.Join(ors, " OR ") + ")", args
}

// excludeClause is NOT (inside any of the spans), on the given time column; when counting what Exclude leaves out
// (insideOnly), it is the opposite.
func excludeClause(c *clause, col, nsExpr string, spans []ExcludeSpan, insideOnly bool) {
	if len(spans) == 0 {
		return
	}
	cond, args := insideSpans(col, nsExpr, spans)
	if insideOnly {
		c.add(cond, args...)
		return
	}
	c.add("NOT "+cond, args...)
}

// ExplorerRow is a record as stored. Exactly one of Event and Evidence is set.
type ExplorerRow struct {
	ID       string // stable: "e.<seq>" or "k.<evidence id>"
	Key      time.Time
	Event    *Event
	Evidence *Evidence
}

// ExplorerPage is one page.
type ExplorerPage struct {
	Rows       []ExplorerRow
	NextCursor string
	// SuppressedRoutine counts routine records the query left out (zero when Routine is set).
	SuppressedRoutine int
	// SuppressedExcluded counts the records Exclude left out (with every other filter applied).
	SuppressedExcluded int
}

func eventRID(seq int64) string { return fmt.Sprintf("e.%012d", seq) }

// EventRecordID is the Explorer id of a Log entry.
func EventRecordID(seq int64) string { return eventRID(seq) }

// EvidenceRecordID is the Explorer id of a Kubernetes Event record.
func EvidenceRecordID(id string) string { return "k." + id }

// evidenceKey is a Kubernetes Event's record time: the first occurrence the source gave when OpsTide first recorded it, else
// when OpsTide first saw it. It is stored once and never rewritten (see SaveEvidence).
const evidenceKey = "record_key"

// explorerWhere builds one branch's conditions. A branch that cannot match gives ok=false.
type clause struct {
	sql  []string
	args []any
}

func (c *clause) add(s string, a ...any) { c.sql, c.args = append(c.sql, s), append(c.args, a...) }

func in(col string, vals []string) (string, []any) {
	return col + " IN (" + marks(len(vals)) + ")", anys(vals)
}

func has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func (q ExplorerQuery) useEvents() bool {
	if len(q.Groups) > 0 {
		any := false
		for _, g := range q.Groups {
			any = any || g != ExplorerGroupEvents
		}
		if !any {
			return false
		}
	}
	if len(q.Types) > 0 {
		any := false
		for _, t := range q.Types {
			any = any || t != EvidenceTypeName
		}
		if !any {
			return false
		}
	}
	if len(q.Sources) > 0 && !(has(q.Sources, SourceKubernetesAPI) || has(q.Sources, SourceOpsTideRule) || has(q.Sources, SourceCoverage)) {
		return false
	}
	return true
}

func (q ExplorerQuery) useEvidence() bool {
	if len(q.Groups) > 0 && !has(q.Groups, ExplorerGroupEvents) {
		return false
	}
	if len(q.Types) > 0 && !has(q.Types, EvidenceTypeName) {
		return false
	}
	if len(q.Provenance) > 0 && !has(q.Provenance, string(Observed)) {
		return false // a Kubernetes Event is always an observed fact
	}
	if len(q.Sources) > 0 {
		ok := false
		for _, s := range q.Sources {
			ok = ok || strings.HasPrefix(s, evidenceSourcePrefx+"events") || strings.HasPrefix(s, evidenceSourcePrefx+"core")
		}
		if !ok {
			return false
		}
	}
	return true
}

func (q ExplorerQuery) eventsClause(cursor *explorerCursor) clause {
	var c clause
	c.add("substr(type, 1, 9) <> 'evidence.'") // those entries are the Kubernetes Event records, shown from evidence
	c.add("at >= ? AND at < ?", q.From.UnixMilli(), q.To.UnixMilli())
	if q.Namespace != "" {
		c.add("namespace = ?", q.Namespace)
	}
	if q.Kind != "" {
		c.add("kind = ?", q.Kind)
	}
	if q.Name != "" {
		c.add("resource_name = ?", q.Name)
	}
	if q.UID != "" {
		c.add("resource_uid = ? AND resource_uid <> ''", q.UID)
	}
	if q.WorkloadID != "" {
		c.add("workload_id = ? AND workload_id <> ''", q.WorkloadID)
	}
	if q.ReleaseID != "" {
		c.add("release_id = ? AND release_id <> ''", q.ReleaseID)
	}
	if q.RunID != "" {
		c.add("run_id = ? AND run_id <> ''", q.RunID)
	}
	if q.OperationID != "" {
		c.add("operation_id = ? AND operation_id <> ''", q.OperationID)
	}
	if q.NodeID != "" {
		c.add("kind = 'Node' AND resource_uid = ?", q.NodeID)
	}
	if q.Reason != "" {
		c.add("reason = ? AND reason <> ''", q.Reason)
	}
	if q.Owner != "" {
		c.add("operation_id IN (SELECT id FROM operations WHERE owner = ?)", q.Owner)
	}
	if len(q.Severities) > 0 {
		s, a := in("severity", q.Severities)
		c.add(s, a...)
	}
	if len(q.Provenance) > 0 {
		s, a := in("confidence", q.Provenance)
		c.add(s, a...)
	}
	if len(q.Groups) > 0 {
		var ors []string
		var args []any
		for _, g := range q.Groups {
			for _, grp := range explorerGroupPrefixes {
				if grp.Key != g {
					continue
				}
				for _, p := range grp.Prefixes {
					ors, args = append(ors, "substr(type, 1, ?) = ?"), append(args, len(p), p)
				}
			}
		}
		if len(ors) == 0 {
			c.add("0")
		} else {
			c.add("("+strings.Join(ors, " OR ")+")", args...)
		}
	}
	if len(q.Types) > 0 {
		var t []string
		for _, x := range q.Types {
			if x != EvidenceTypeName {
				t = append(t, x)
			}
		}
		if len(t) > 0 {
			s, a := in("type", t)
			c.add(s, a...)
		}
	}
	if len(q.Sources) > 0 {
		var ors []string
		for _, s := range q.Sources {
			switch s {
			case SourceCoverage:
				ors = append(ors, "substr(type, 1, 11) = 'collection.'")
			case SourceOpsTideRule:
				ors = append(ors, "(confidence = 'derived' AND substr(type, 1, 11) <> 'collection.')")
			case SourceKubernetesAPI:
				ors = append(ors, "(confidence <> 'derived' AND substr(type, 1, 11) <> 'collection.')")
			}
		}
		if len(ors) > 0 {
			c.add("(" + strings.Join(ors, " OR ") + ")")
		}
	}
	if q.Text != "" {
		like := "%" + escapeLike(q.Text) + "%"
		c.add(`(summary LIKE ? ESCAPE '\' OR resource_name LIKE ? ESCAPE '\' OR reason LIKE ? ESCAPE '\')`, like, like, like)
	}
	if !q.Routine {
		s, a := in("type", routineTypes)
		c.add("NOT ("+s+")", a...)
	}
	excludeClause(&c, "at", "namespace", q.Exclude, q.insideOnly)
	if cursor != nil {
		c.cursor("at", "printf('e.%012d', seq)", *cursor, q.Asc)
	}
	return c
}

func (q ExplorerQuery) evidenceClause(cursor *explorerCursor) clause {
	var c clause
	c.add(evidenceKey+" >= ? AND "+evidenceKey+" < ?", q.From.UnixMilli(), q.To.UnixMilli())
	if q.Namespace != "" {
		c.add("namespace = ?", q.Namespace)
	}
	if q.Kind != "" {
		c.add("regarding_kind = ?", q.Kind)
	}
	if q.Name != "" {
		c.add("regarding_name = ?", q.Name)
	}
	if q.UID != "" {
		c.add("regarding_uid = ? AND regarding_uid <> ''", q.UID)
	}
	if q.WorkloadID != "" {
		c.add("workload_id = ? AND workload_id <> ''", q.WorkloadID)
	}
	if q.RunID != "" {
		c.add("run_id = ? AND run_id <> ''", q.RunID)
	}
	if q.OperationID != "" {
		c.add("operation_id = ? AND operation_id <> ''", q.OperationID)
	}
	if q.NodeID != "" {
		c.add("node_id = ? AND node_id <> ''", q.NodeID)
	}
	if q.ReleaseID != "" {
		// Events about the release's ReplicaSet or its Pods, and about its Deployment or an autoscaler targeting it,
		// while it was current: the rule the release's own Evidence tab uses.
		c.add(`((rs_uid = ? AND rs_uid <> '') OR (workload_id = ? AND workload_id <> '' AND rs_uid = '' AND regarding_kind IN ('Deployment', 'HorizontalPodAutoscaler')))
AND `+evidenceKey+` >= ?`, q.ReleaseReplicaSet, q.ReleaseWorkload, q.ReleaseFrom.UnixMilli())
		if !q.ReleaseTo.IsZero() {
			c.add("coalesce(first_at, observed_at) < ?", q.ReleaseTo.UnixMilli())
		}
	}
	if q.Reason != "" {
		c.add("reason = ?", q.Reason)
	}
	if q.Owner != "" {
		c.add("operation_id IN (SELECT id FROM operations WHERE owner = ?)", q.Owner)
	}
	if len(q.Severities) > 0 {
		switch {
		case has(q.Severities, "warning") && has(q.Severities, "info"):
		case has(q.Severities, "warning"):
			c.add("event_type = 'Warning'")
		case has(q.Severities, "info"):
			c.add("event_type <> 'Warning'")
		default:
			c.add("0") // Kubernetes Events are never an error
		}
	}
	if len(q.Sources) > 0 {
		var srcs []string
		for _, s := range q.Sources {
			if t, ok := strings.CutPrefix(s, evidenceSourcePrefx); ok && (strings.HasPrefix(t, "events") || strings.HasPrefix(t, "core")) {
				srcs = append(srcs, t)
			}
		}
		if len(srcs) > 0 {
			s, a := in("source", srcs)
			c.add(s, a...)
		}
	}
	if q.Text != "" {
		like := "%" + escapeLike(q.Text) + "%"
		c.add(`(reason LIKE ? ESCAPE '\' OR regarding_name LIKE ? ESCAPE '\' OR regarding_kind LIKE ? ESCAPE '\')`, like, like, like)
	}
	if !q.Routine {
		c.add("event_type <> 'Normal'")
	}
	excludeClause(&c, evidenceKey, evidenceNamespaceExpr, q.Exclude, q.insideOnly)
	if cursor != nil {
		c.cursor(evidenceKey, "('k.' || id)", *cursor, q.Asc)
	}
	return c
}

type explorerCursor struct {
	Key int64
	RID string
}

// cursor continues strictly after (Key, RID) in the query's direction.
func (c *clause) cursor(keyExpr, ridExpr string, cur explorerCursor, asc bool) {
	op := "<"
	if asc {
		op = ">"
	}
	c.add("("+keyExpr+" "+op+" ? OR ("+keyExpr+" = ? AND "+ridExpr+" "+op+" ?))", cur.Key, cur.Key, cur.RID)
}

func parseExplorerCursor(s string) (*explorerCursor, error) {
	if s == "" {
		return nil, nil
	}
	k, rid, ok := strings.Cut(s, ".")
	if !ok || rid == "" {
		return nil, errors.New("bad cursor")
	}
	n, err := strconv.ParseInt(k, 10, 64)
	if err != nil || (!strings.HasPrefix(rid, "e.") && !strings.HasPrefix(rid, "k.")) {
		return nil, errors.New("bad cursor")
	}
	return &explorerCursor{n, rid}, nil
}

func where(c clause) string { return " WHERE " + strings.Join(c.sql, " AND ") }

func (q ExplorerQuery) check() error {
	if q.From.IsZero() || q.To.IsZero() || !q.To.After(q.From) {
		return errors.New("a time range is required")
	}
	return nil
}

const eventCols = `seq, id, type, namespace, kind, resource_uid, resource_name, operation_id, run_id, occurred_at, observed_at, recorded_at, severity, confidence, summary, facts, release_id, reason, workload_id`

// Explorer returns one page of records, merged from the Log and the Kubernetes Event record, in record time order.
// Each source is read with its own index and limit, so the work does not grow with the size of the history.
func (s *Store) Explorer(ctx context.Context, q ExplorerQuery) (ExplorerPage, error) {
	if err := q.check(); err != nil {
		return ExplorerPage{}, err
	}
	limit := min(max(q.Limit, 1), 500)
	if q.Limit <= 0 {
		limit = 100
	}
	cur, err := parseExplorerCursor(q.Cursor)
	if err != nil {
		return ExplorerPage{}, err
	}
	order := " DESC"
	if q.Asc {
		order = " ASC"
	}
	var rows []ExplorerRow
	if q.useEvents() {
		c := q.eventsClause(cur)
		rs, err := s.db.QueryContext(ctx, `SELECT `+eventCols+`, at FROM events`+where(c)+" ORDER BY at"+order+", seq"+order+" LIMIT ?", append(c.args, limit+1)...) //nolint:gosec // fixed clauses, bound values
		if err != nil {
			return ExplorerPage{}, err
		}
		for rs.Next() {
			row, err := scanEventRow(rs)
			if err != nil {
				rs.Close()
				return ExplorerPage{}, err
			}
			rows = append(rows, row)
		}
		err = rs.Err()
		rs.Close()
		if err != nil {
			return ExplorerPage{}, err
		}
	}
	if q.useEvidence() {
		c := q.evidenceClause(cur)
		rs, err := s.db.QueryContext(ctx, `SELECT `+evidenceCols+`, `+evidenceKey+` FROM evidence`+where(c)+" ORDER BY "+evidenceKey+order+", id"+order+" LIMIT ?", append(c.args, limit+1)...) //nolint:gosec // fixed clauses, bound values
		if err != nil {
			return ExplorerPage{}, err
		}
		for rs.Next() {
			// evidenceCols' scan followed by the key.
			e, key, err := scanEvidenceKeyed(rs)
			if err != nil {
				rs.Close()
				return ExplorerPage{}, err
			}
			ev := e
			rows = append(rows, ExplorerRow{ID: EvidenceRecordID(e.ID), Key: time.UnixMilli(key).UTC(), Evidence: &ev})
		}
		err = rs.Err()
		rs.Close()
		if err != nil {
			return ExplorerPage{}, err
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].Key.Equal(rows[j].Key) {
			return rows[i].Key.After(rows[j].Key) != q.Asc
		}
		return (rows[i].ID > rows[j].ID) != q.Asc
	})
	page := ExplorerPage{}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		page.NextCursor = fmt.Sprintf("%d.%s", last.Key.UnixMilli(), last.ID)
	}
	page.Rows = rows
	if !q.Routine {
		n, err := s.explorerCount(ctx, q.withRoutineOnly())
		if err != nil {
			return ExplorerPage{}, err
		}
		page.SuppressedRoutine = n
	}
	if len(q.Exclude) > 0 {
		n, err := s.explorerCount(ctx, q.withInsideOnly())
		if err != nil {
			return ExplorerPage{}, err
		}
		page.SuppressedExcluded = n
	}
	return page, nil
}

// scanEventRow scans eventCols followed by the record time.
func scanEventRow(rs *sql.Rows) (ExplorerRow, error) {
	var at int64
	var e Event
	var occurred sql.NullInt64
	var observed, recorded int64
	var conf, facts string
	if err := rs.Scan(&e.Seq, &e.ID, &e.Type, &e.Namespace, &e.Kind, &e.ResourceUID, &e.ResourceName, &e.OperationID, &e.RunID, &occurred, &observed, &recorded,
		&e.Severity, &conf, &e.Summary, &facts, &e.ReleaseID, &e.Reason, &e.WorkloadID, &at); err != nil {
		return ExplorerRow{}, err
	}
	e.OccurredAt, e.ObservedAt, e.RecordedAt = fromMS(occurred), time.UnixMilli(observed).UTC(), time.UnixMilli(recorded).UTC()
	e.Confidence = Confidence(conf)
	_ = json.Unmarshal([]byte(facts), &e.Facts)
	return ExplorerRow{ID: eventRID(e.Seq), Key: time.UnixMilli(at).UTC(), Event: &e}, nil
}

// ResourceName names a resource by namespace, kind and name.
type ResourceName struct{ Namespace, Kind, Name string }

// ChangesQuery selects the Log entries of some types recorded in [From, To) about any of a set of resources: by
// workload, by CronJob (operation), by resource UID, or by namespace, kind and name.
type ChangesQuery struct {
	From, To                        time.Time
	Types                           []string // required
	WorkloadIDs, OperationIDs, UIDs []string
	Names                           []ResourceName
	Limit                           int // default 500, at most 2000
}

// Changes reads, in one statement, the entries of q, newest first (record time, then id, as the Explorer orders them).
// Each kind of key is its own branch on its own index, bounded by the time range, so the work grows with what is
// related, not with the history. more says entries beyond the limit were left out.
func (s *Store) Changes(ctx context.Context, q ChangesQuery) (rows []ExplorerRow, more bool, err error) {
	if !q.To.After(q.From) || len(q.Types) == 0 {
		return nil, false, errors.New("a time range and types are required")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 500
	}
	limit = min(limit, 2000)
	ty, tyArgs := in("type", q.Types)
	bound := " AND at >= ? AND at < ? AND " + ty
	var branches []string
	var args []any
	branch := func(cond string, a ...any) {
		branches = append(branches, "SELECT seq FROM events WHERE "+cond+bound)
		args = append(append(append(args, a...), q.From.UnixMilli(), q.To.UnixMilli()), tyArgs...)
	}
	if len(q.WorkloadIDs) > 0 {
		c, a := in("workload_id", q.WorkloadIDs)
		branch(c+" AND workload_id <> ''", a...)
	}
	if len(q.OperationIDs) > 0 {
		c, a := in("operation_id", q.OperationIDs)
		branch(c+" AND operation_id <> ''", a...)
	}
	if len(q.UIDs) > 0 {
		c, a := in("resource_uid", q.UIDs)
		branch(c+" AND resource_uid <> ''", a...)
	}
	byNK := map[[2]string][]string{}
	var nks [][2]string
	for _, n := range q.Names {
		if n.Name == "" {
			continue
		}
		k := [2]string{n.Namespace, n.Kind}
		if _, ok := byNK[k]; !ok {
			nks = append(nks, k)
		}
		byNK[k] = append(byNK[k], n.Name)
	}
	for _, k := range nks {
		c, a := in("resource_name", byNK[k])
		branch("namespace = ? AND kind = ? AND "+c+" AND resource_name <> ''", append([]any{k[0], k[1]}, a...)...)
	}
	if len(branches) == 0 {
		return nil, false, nil
	}
	rs, err := s.db.QueryContext(ctx, `SELECT `+eventCols+`, at FROM events WHERE seq IN (`+strings.Join(branches, " UNION ")+`) ORDER BY at DESC, seq DESC LIMIT ?`, append(args, limit+1)...) //nolint:gosec // fixed clauses, bound values
	if err != nil {
		return nil, false, err
	}
	defer rs.Close()
	for rs.Next() {
		row, err := scanEventRow(rs)
		if err != nil {
			return nil, false, err
		}
		rows = append(rows, row)
	}
	if err := rs.Err(); err != nil {
		return nil, false, err
	}
	if len(rows) > limit {
		return rows[:limit], true, nil
	}
	return rows, false, nil
}

// scanEvidenceKeyed scans an evidence row followed by its record-time key.
func scanEvidenceKeyed(rs *sql.Rows) (Evidence, int64, error) {
	var e Evidence
	var first, last, deleted, loggedAt sql.NullInt64
	var observed, at, upd, key int64
	var trunc int
	err := rs.Scan(&e.ID, &e.Source, &e.SourceUID, &e.Namespace, &e.Name, &e.Reason, &e.EventType, &e.Message, &trunc, &e.Action, &e.Reporter,
		&e.Regarding.Kind, &e.Regarding.Name, &e.Regarding.UID, &e.Related.Kind, &e.Related.Name, &e.Related.UID, &first, &last, &e.Count, &e.CountBasis,
		&observed, &e.Origin, &at, &upd, &deleted, &e.CountLogged, &loggedAt, &e.ReplicaSetUID, &e.WorkloadID, &e.RunID, &e.OperationID, &e.HPAUID, &e.LinkVia, &e.NodeID, &key)
	if err != nil {
		return e, 0, err
	}
	e.MessageTruncated = trunc != 0
	e.FirstAt, e.LastAt, e.SourceDeletedAt, e.CountLoggedAt = fromMS(first), fromMS(last), fromMS(deleted), fromMS(loggedAt)
	e.ObservedAt, e.At, e.UpdatedAt = time.UnixMilli(observed).UTC(), time.UnixMilli(at).UTC(), time.UnixMilli(upd).UTC()
	return e, key, nil
}

// withRoutineOnly narrows the query to the routine records it would otherwise leave out.
func (q ExplorerQuery) withRoutineOnly() ExplorerQuery {
	q.Cursor, q.Routine = "", true
	// The routine set is added by the count itself; a type filter still applies, so a filter that names no routine type counts none.
	q.routineOnly = true
	return q
}

func (q ExplorerQuery) withInsideOnly() ExplorerQuery {
	q.Cursor, q.insideOnly = "", true
	return q
}

// explorerCount counts the records matching q in each source.
func (s *Store) explorerCount(ctx context.Context, q ExplorerQuery) (int, error) {
	total := 0
	if q.useEvents() {
		c := q.eventsClause(nil)
		if q.routineOnly {
			ss, a := in("type", routineTypes)
			c.add(ss, a...)
		}
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM events`+where(c), c.args...).Scan(&n); err != nil { //nolint:gosec // fixed clauses
			return 0, err
		}
		total += n
	}
	if q.useEvidence() {
		c := q.evidenceClause(nil)
		if q.routineOnly {
			c.add("event_type = 'Normal'")
		}
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM evidence`+where(c), c.args...).Scan(&n); err != nil { //nolint:gosec // fixed clauses
			return 0, err
		}
		total += n
	}
	return total, nil
}

// ExplorerFacets counts the records of the query by group, resource kind, provenance and reason, and lists the
// namespaces. Each count leaves out its own filter, so the other values can be chosen. It reads the same indexes as
// the query and is bounded by its time range.
type ExplorerFacets struct {
	Groups     map[string]int
	Kinds      map[string]int
	Provenance map[string]int
	Reasons    []NamedCount
	Namespaces []NamedCount
	Total      int
}

// NamedCount is a value and how many records carry it.
type NamedCount struct {
	Name  string
	Count int
}

func (s *Store) groupCount(ctx context.Context, table string, c clause, col string) (map[string]int, error) {
	rs, err := s.db.QueryContext(ctx, `SELECT `+col+`, count(*) FROM `+table+where(c)+` GROUP BY 1`, c.args...) //nolint:gosec // fixed clauses, bound values
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := map[string]int{}
	for rs.Next() {
		var k sql.NullString
		var n int
		if err := rs.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k.String] += n
	}
	return out, rs.Err()
}

// Facets computes the counts. limit bounds each list of names.
func (s *Store) ExplorerFacets(ctx context.Context, q ExplorerQuery, limit int) (ExplorerFacets, error) {
	if err := q.check(); err != nil {
		return ExplorerFacets{}, err
	}
	f := ExplorerFacets{Groups: map[string]int{}, Kinds: map[string]int{}, Provenance: map[string]int{}}
	// Groups: leave out the group filter (and the type filter, which narrows groups too).
	g := q
	g.Groups, g.Types = nil, nil
	if g.useEvents() {
		ts, err := s.groupCount(ctx, "events", g.eventsClause(nil), "type")
		if err != nil {
			return f, err
		}
		for t, n := range ts {
			if key := GroupOfType(t); key != "" {
				f.Groups[key] += n
			}
		}
	}
	if g.useEvidence() {
		n, err := s.groupCount(ctx, "evidence", g.evidenceClause(nil), "'x'")
		if err != nil {
			return f, err
		}
		f.Groups[ExplorerGroupEvents] += n["x"]
	}
	// Kinds.
	k := q
	k.Kind = ""
	if k.useEvents() {
		m, err := s.groupCount(ctx, "events", k.eventsClause(nil), "kind")
		if err != nil {
			return f, err
		}
		for name, n := range m {
			if name != "" {
				f.Kinds[name] += n
			}
		}
	}
	if k.useEvidence() {
		m, err := s.groupCount(ctx, "evidence", k.evidenceClause(nil), "regarding_kind")
		if err != nil {
			return f, err
		}
		for name, n := range m {
			if name != "" {
				f.Kinds[name] += n
			}
		}
	}
	// Provenance.
	p := q
	p.Provenance = nil
	if p.useEvents() {
		m, err := s.groupCount(ctx, "events", p.eventsClause(nil), "confidence")
		if err != nil {
			return f, err
		}
		for name, n := range m {
			f.Provenance[name] += n
		}
	}
	if p.useEvidence() {
		n, err := s.groupCount(ctx, "evidence", p.evidenceClause(nil), "'observed'")
		if err != nil {
			return f, err
		}
		f.Provenance["observed"] += n["observed"]
	}
	// Reasons and namespaces.
	r := q
	r.Reason = ""
	reasons := map[string]int{}
	if r.useEvents() {
		m, err := s.groupCount(ctx, "events", r.eventsClause(nil), "reason")
		if err != nil {
			return f, err
		}
		for name, n := range m {
			if name != "" {
				reasons[name] += n
			}
		}
	}
	if r.useEvidence() {
		m, err := s.groupCount(ctx, "evidence", r.evidenceClause(nil), "reason")
		if err != nil {
			return f, err
		}
		for name, n := range m {
			if name != "" {
				reasons[name] += n
			}
		}
	}
	f.Reasons = topCounts(reasons, limit)
	ns := map[string]int{}
	n0 := q
	n0.Namespace = ""
	if n0.useEvents() {
		m, err := s.groupCount(ctx, "events", n0.eventsClause(nil), "namespace")
		if err != nil {
			return f, err
		}
		for name, n := range m {
			if name != "" {
				ns[name] += n
			}
		}
	}
	if n0.useEvidence() {
		m, err := s.groupCount(ctx, "evidence", n0.evidenceClause(nil), "namespace")
		if err != nil {
			return f, err
		}
		for name, n := range m {
			if name != "" {
				ns[name] += n
			}
		}
	}
	f.Namespaces = topCounts(ns, limit)
	for _, n := range f.Provenance {
		f.Total += n
	}
	return f, nil
}

func topCounts(m map[string]int, limit int) []NamedCount {
	out := make([]NamedCount, 0, len(m))
	for k, v := range m {
		out = append(out, NamedCount{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ExplorerRowsByID returns the records with these ids, in the order given; ids that no longer exist are
// returned in missing (expired by retention, or never recorded).
func (s *Store) ExplorerRowsByID(ctx context.Context, ids []string) (rows []ExplorerRow, missing []string, err error) {
	if len(ids) > 1000 {
		return nil, nil, errors.New("too many ids")
	}
	var seqs, kids []string
	seqOf := map[string]int64{}
	for _, id := range ids {
		switch {
		case strings.HasPrefix(id, "e."):
			n, perr := strconv.ParseInt(strings.TrimPrefix(id, "e."), 10, 64)
			if perr != nil || eventRID(n) != id {
				continue
			}
			seqs = append(seqs, strconv.FormatInt(n, 10))
			seqOf[id] = n
		case strings.HasPrefix(id, "k."):
			kids = append(kids, strings.TrimPrefix(id, "k."))
		}
	}
	found := map[string]ExplorerRow{}
	if len(seqs) > 0 {
		args := make([]any, len(seqs))
		for i, v := range seqs {
			args[i] = v
		}
		rs, qerr := s.db.QueryContext(ctx, `SELECT `+eventCols+`, at FROM events WHERE seq IN (`+marks(len(seqs))+`)`, args...) //nolint:gosec // placeholders only
		if qerr != nil {
			return nil, nil, qerr
		}
		for rs.Next() {
			var at int64
			var e Event
			var occurred sql.NullInt64
			var observed, recorded int64
			var conf, facts string
			if err := rs.Scan(&e.Seq, &e.ID, &e.Type, &e.Namespace, &e.Kind, &e.ResourceUID, &e.ResourceName, &e.OperationID, &e.RunID, &occurred, &observed, &recorded,
				&e.Severity, &conf, &e.Summary, &facts, &e.ReleaseID, &e.Reason, &e.WorkloadID, &at); err != nil {
				rs.Close()
				return nil, nil, err
			}
			e.OccurredAt, e.ObservedAt, e.RecordedAt = fromMS(occurred), time.UnixMilli(observed).UTC(), time.UnixMilli(recorded).UTC()
			e.Confidence = Confidence(conf)
			_ = json.Unmarshal([]byte(facts), &e.Facts)
			ev := e
			found[eventRID(e.Seq)] = ExplorerRow{ID: eventRID(e.Seq), Key: time.UnixMilli(at).UTC(), Event: &ev}
		}
		err = rs.Err()
		rs.Close()
		if err != nil {
			return nil, nil, err
		}
	}
	if len(kids) > 0 {
		rs, qerr := s.db.QueryContext(ctx, `SELECT `+evidenceCols+`, `+evidenceKey+` FROM evidence WHERE id IN (`+marks(len(kids))+`)`, anys(kids)...) //nolint:gosec // placeholders only
		if qerr != nil {
			return nil, nil, qerr
		}
		for rs.Next() {
			e, key, serr := scanEvidenceKeyed(rs)
			if serr != nil {
				rs.Close()
				return nil, nil, serr
			}
			ev := e
			found[EvidenceRecordID(e.ID)] = ExplorerRow{ID: EvidenceRecordID(e.ID), Key: time.UnixMilli(key).UTC(), Evidence: &ev}
		}
		err = rs.Err()
		rs.Close()
		if err != nil {
			return nil, nil, err
		}
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if r, ok := found[id]; ok {
			rows = append(rows, r)
		} else {
			missing = append(missing, id)
		}
	}
	return rows, missing, nil
}

// OldestRecordTime is the record time of the oldest Log entry and Kubernetes Event record held, zero when empty:
// the start of the history OpsTide still has.
func (s *Store) OldestRecordTime(ctx context.Context) (time.Time, error) {
	var a, b sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT (SELECT min(at) FROM events), (SELECT min(`+evidenceKey+`) FROM evidence)`).Scan(&a, &b); err != nil {
		return time.Time{}, err
	}
	switch {
	case a.Valid && b.Valid:
		return time.UnixMilli(min(a.Int64, b.Int64)).UTC(), nil
	case a.Valid:
		return time.UnixMilli(a.Int64).UTC(), nil
	case b.Valid:
		return time.UnixMilli(b.Int64).UTC(), nil
	}
	return time.Time{}, nil
}

// ReleaseAssessmentVersions returns the rule-set version each of these releases was assessed with (those that exist).
func (s *Store) ReleaseAssessmentVersions(ctx context.Context, ids []string) (map[string]int, error) {
	out := map[string]int{}
	for len(ids) > 0 {
		n := min(len(ids), 500)
		rs, err := s.db.QueryContext(ctx, `SELECT id, assessment_version FROM releases WHERE id IN (`+marks(n)+`)`, anys(ids[:n])...) //nolint:gosec // placeholders only
		if err != nil {
			return nil, err
		}
		for rs.Next() {
			var id string
			var v int
			if err := rs.Scan(&id, &v); err != nil {
				rs.Close()
				return nil, err
			}
			out[id] = v
		}
		err = rs.Err()
		rs.Close()
		if err != nil {
			return nil, err
		}
		ids = ids[n:]
	}
	return out, nil
}

// FirstWatchedAll is when each coverage scope ("deployments", "events/shop", ...) was first watched, from the whole record.
func (s *Store) FirstWatchedAll(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT scope, min(started_at) FROM coverage GROUP BY scope`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var scope string
		var ms int64
		if err := rows.Scan(&scope, &ms); err != nil {
			return nil, err
		}
		out[scope] = time.UnixMilli(ms).UTC()
	}
	return out, rows.Err()
}

// ExplorerCount counts the records q matches (the query's range, filters and routine setting apply; paging does not).
func (s *Store) ExplorerCount(ctx context.Context, q ExplorerQuery) (int, error) {
	if err := q.check(); err != nil {
		return 0, err
	}
	q.Cursor = ""
	return s.explorerCount(ctx, q)
}
