package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/mihnk/opstide/internal/store"
)

// The export is a versioned, self-describing JSON document for a person or a tool to read elsewhere. It is made only
// when someone asks (a download), never sent anywhere by OpsTide, bounded, and says so when it is cut.
const (
	ExportSchemaName = "opstide.explorer.export"
	ExportVersion    = "1.0"
)

// ExportRule is the rule behind a derived record.
type ExportRule struct {
	ID      string `json:"id"`
	Set     string `json:"set"`
	Version string `json:"version"`
}

// ExportRecord is one record in an export. Times are RFC 3339 UTC.
type ExportRecord struct {
	ID            string            `json:"id"`
	Type          string            `json:"type"`
	Group         string            `json:"group"`
	Source        string            `json:"source"`
	Provenance    string            `json:"provenance"`
	Resource      ResourceRef       `json:"resource"`
	SourceUID     string            `json:"sourceUid,omitempty"` // a Kubernetes Event's own UID
	RecordTime    time.Time         `json:"recordTime"`
	OccurredAt    *time.Time        `json:"occurredAt"`
	LastOccurred  *time.Time        `json:"lastOccurredAt,omitempty"`
	ObservedAt    time.Time         `json:"observedAt"`
	Severity      string            `json:"severity,omitempty"`
	Reason        string            `json:"reason,omitempty"`
	EventType     string            `json:"eventType,omitempty"`
	Summary       string            `json:"summary"`
	Count         *Count            `json:"count,omitempty"`
	SourceFields  map[string]string `json:"sourceFields,omitempty"`
	Rule          *ExportRule       `json:"rule,omitempty"`
	Relationships []Relationship    `json:"relationships"`
	Message       *string           `json:"message,omitempty"`
}

// ExportTruncation says a filtered export stopped at its limit, and how to continue.
type ExportTruncation struct {
	Limit      int    `json:"limit"`
	NextCursor string `json:"nextCursor"`
	Note       string `json:"note"`
}

// ExportDoc is the export document.
type ExportDoc struct {
	Schema      string    `json:"schema"`
	Version     string    `json:"version"`
	GeneratedAt time.Time `json:"generatedAt"`
	Mode        string    `json:"mode"` // filtered | selected
	Cluster     string    `json:"cluster"`
	Scope       struct {
		WatchedNamespaces []string `json:"watchedNamespaces"` // empty: the whole cluster
	} `json:"scope"`
	Range   ExportRange      `json:"range"`
	Filters *explorerFilters `json:"filters,omitempty"`
	Options struct {
		IncludeMessages bool `json:"includeMessages"`
	} `json:"options"`
	Order       string            `json:"order"`
	Records     []ExportRecord    `json:"records"`
	Counts      ExportCounts      `json:"counts"`
	Truncation  *ExportTruncation `json:"truncation"`
	Missing     []string          `json:"missing,omitempty"` // selected ids no longer recorded
	Coverage    *ExplorerContext  `json:"coverage"`
	Notes       []string          `json:"notes"`
	Limitations []string          `json:"limitations"`
}

// ExportRange is the time range an export covers.
type ExportRange struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// ExportCounts counts what is in the document.
type ExportCounts struct {
	Records           int `json:"records"`
	SuppressedRoutine int `json:"suppressedRoutine"`
}

// messageKeys are Log fact keys that carry text Kubernetes wrote. They are left out unless messages are asked for.
var messageKeys = map[string]bool{"message": true, "note": true}

func exportRecord(row store.ExplorerRow, messages bool, versions map[string]int) ExportRecord {
	rec := recordOfRow(row, messages)
	x := ExportRecord{ID: rec.ID, Type: rec.Type, Group: rec.Group, Source: rec.Source, Provenance: rec.Provenance, Resource: rec.Resource, RecordTime: rec.At,
		OccurredAt: rec.OccurredAt, LastOccurred: rec.LastOccurredAt, ObservedAt: rec.ObservedAt, Severity: rec.Severity, Reason: rec.Reason, EventType: rec.EventType,
		Summary: rec.Summary, Count: rec.Count, Relationships: relationshipsOf(row), Message: rec.Message}
	if ev := row.Evidence; ev != nil {
		x.SourceUID = ev.SourceUID
		x.SourceFields = map[string]string{"action": ev.Action, "reporter": ev.Reporter, "origin": ev.Origin, "eventsApi": ev.Source, "countBasis": ev.CountBasis,
			"relatedKind": ev.Related.Kind, "relatedName": ev.Related.Name, "relatedUid": ev.Related.UID}
		if ev.MessageTruncated {
			x.SourceFields["messageTruncated"] = "true"
		}
		for k, v := range x.SourceFields {
			if v == "" {
				delete(x.SourceFields, k)
			}
		}
		return x
	}
	e := row.Event
	if len(e.Facts) > 0 {
		x.SourceFields = map[string]string{}
		for k, v := range e.Facts {
			if messageKeys[k] && !messages {
				continue
			}
			x.SourceFields[k] = v
		}
	}
	if e.Confidence == store.Derived {
		x.Rule = &ExportRule{ID: e.Type, Set: "opstide.rule", Version: "unversioned"}
		if e.ReleaseID != "" {
			x.Rule.Set = "release.assessment"
			if v, ok := versions[e.ReleaseID]; ok {
				x.Rule.Version = itoa(v)
			}
		}
	}
	return x
}

func (s *Server) exportDoc(r *http.Request, mode string, rows []store.ExplorerRow, from, to time.Time, messages bool) (*ExportDoc, error) {
	ctx := r.Context()
	doc := &ExportDoc{Schema: ExportSchemaName, Version: ExportVersion, GeneratedAt: s.now().UTC().Truncate(time.Second), Mode: mode, Cluster: s.cfg.Cluster,
		Range: ExportRange{from, to}, Order: "record time ascending, then record id", Records: make([]ExportRecord, 0, len(rows)), Notes: []string{}}
	doc.Scope.WatchedNamespaces = append([]string{}, s.cfg.Namespaces...)
	doc.Options.IncludeMessages = messages
	var releases []string
	seen := map[string]bool{}
	for _, row := range rows {
		if e := row.Event; e != nil && e.Confidence == store.Derived && e.ReleaseID != "" && !seen[e.ReleaseID] {
			seen[e.ReleaseID] = true
			releases = append(releases, e.ReleaseID)
		}
	}
	sort.Strings(releases)
	versions, err := s.db.ReleaseAssessmentVersions(ctx, releases)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		doc.Records = append(doc.Records, exportRecord(row, messages, versions))
	}
	doc.Counts.Records = len(doc.Records)
	if to.Sub(from) <= maxExplorerSpan && to.After(from) {
		if doc.Coverage, err = s.buildContext(ctx, from, to, ""); err != nil {
			return nil, err
		}
	} else if len(rows) == 0 {
		doc.Notes = append(doc.Notes, "No record is held, so no coverage is given for a range.")
	} else {
		doc.Notes = append(doc.Notes, "The records span more than 400 days, so no coverage is given for the range.")
	}
	doc.Limitations = []string{
		"OpsTide records evidence; this document does not interpret it. A relationship classification says how two records are known to connect, never that one caused another.",
		"Records from times OpsTide was not watching (see coverage) may be missing. A missing record is not proof that nothing happened.",
		"Routine records (Pod created/ready/terminated, Normal Kubernetes Events) are included only when the filters ask for them.",
	}
	if !messages {
		doc.Notes = append(doc.Notes, "Kubernetes Event messages and quoted condition messages are left out. They can contain names, addresses or other sensitive text; export again with messages=1 to include them.")
	}
	return doc, nil
}

func (s *Server) sendExport(w http.ResponseWriter, doc *ExportDoc) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="opstide-explorer-%s-%s.json"`, doc.Mode, doc.GeneratedAt.Format("20060102T150405Z")))
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(doc)
}

// explorerExport exports what the filters match: oldest first, at most maxExportRows, and says if it stopped there.
func (s *Server) explorerExport(w http.ResponseWriter, r *http.Request) {
	q, f, err := s.parseExplorer(r)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	messages := r.URL.Query().Get("messages") == "1"
	q.Asc, q.Limit = true, 500
	var rows []store.ExplorerRow
	var next string
	var suppressed int
	for {
		pg, err := s.db.Explorer(r.Context(), q)
		if err != nil {
			reply(w, r, s.log, nil, err)
			return
		}
		rows = append(rows, pg.Rows...)
		if suppressed == 0 {
			suppressed = pg.SuppressedRoutine
		}
		next, q.Cursor = pg.NextCursor, pg.NextCursor
		if next == "" || len(rows) >= maxExportRows {
			break
		}
	}
	doc, err := s.exportDoc(r, "filtered", rows, q.From, q.To, messages)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	f.Routine = q.Routine
	doc.Filters, doc.Counts.SuppressedRoutine = &f, suppressed
	if next != "" {
		doc.Truncation = &ExportTruncation{Limit: maxExportRows, NextCursor: next,
			Note: "The export stopped at its limit; more records match. Request again with cursor set to nextCursor to continue from here, or narrow the range or filters."}
	}
	s.sendExport(w, doc)
}

// readJSON reads a small JSON request body. A form from another site cannot send application/json without a preflight.
func readJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return badRequest("send application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return badRequest("the body is not the JSON this endpoint takes")
	}
	return nil
}

// idList validates, de-duplicates and bounds a list of record ids.
func idList(ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, badRequest("ids is empty")
	}
	if len(ids) > maxSelectedIDs {
		return nil, badRequest("at most 1000 ids at a time")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !idRe.MatchString(id) {
			return nil, badRequest("an id is not a record id")
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

type selectedBody struct {
	IDs             []string `json:"ids"`
	IncludeMessages bool     `json:"includeMessages"`
}

// explorerExportSelected exports the records a person selected, by id, oldest first.
func (s *Server) explorerExportSelected(w http.ResponseWriter, r *http.Request) {
	var body selectedBody
	err := readJSON(w, r, &body)
	var ids []string
	if err == nil {
		ids, err = idList(body.IDs)
	}
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	rows, missing, err := s.db.ExplorerRowsByID(r.Context(), ids)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].Key.Equal(rows[j].Key) {
			return rows[i].Key.Before(rows[j].Key)
		}
		return rows[i].ID < rows[j].ID
	})
	from, to := s.now().UTC(), s.now().UTC()
	if len(rows) > 0 {
		from, to = rows[0].Key, rows[len(rows)-1].Key.Add(time.Millisecond)
	}
	doc, err := s.exportDoc(r, "selected", rows, from, to, body.IncludeMessages)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	doc.Missing = missing
	if len(missing) > 0 {
		doc.Notes = append(doc.Notes, fmt.Sprintf("%d selected record(s) are no longer recorded (removed by retention) and are not in this document.", len(missing)))
	}
	s.sendExport(w, doc)
}

// ExplorerLookup returns records by id, for a client that kept only ids (the Investigation Workspace).
type ExplorerLookup struct {
	Schema  int              `json:"schema"`
	Records []ExplorerRecord `json:"records"`
	Missing []string         `json:"missing"`
}

func (s *Server) explorerLookup(w http.ResponseWriter, r *http.Request) {
	var body selectedBody
	err := readJSON(w, r, &body)
	var ids []string
	if err == nil {
		ids, err = idList(body.IDs)
	}
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	rows, missing, err := s.db.ExplorerRowsByID(r.Context(), ids)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	out := ExplorerLookup{Schema: Schema, Records: make([]ExplorerRecord, 0, len(rows)), Missing: append([]string{}, missing...)}
	for _, row := range rows {
		out.Records = append(out.Records, recordOfRow(row, false))
	}
	sortRecords(out.Records, true)
	reply(w, r, s.log, out, nil)
}
