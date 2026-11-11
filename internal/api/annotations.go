package api

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mihnk/opstide/internal/store"
)

// Grafana annotations: OpsTide's changes, failures and the stretches it was not watching, as a flat JSON array that
// Grafana pulls with the Infinity data source (or any JSON data source) and draws on a dashboard. OpsTide sends nothing
// anywhere; the request carries the same token as the rest of the API. An annotation marks a record at its time. It
// does not say that the record explains anything on the graph.

const (
	maxAnnotationSpan   = 31 * 24 * time.Hour
	defAnnotationLimit  = 500
	maxAnnotationLimit  = 1000
	annotationPageLimit = 500 // the Explorer's page size; longer answers are read page by page
	// maxGapRegions is how many not-watched regions one answer draws; past it a notice region covers the rest.
	maxGapRegions = 500
)

// Annotation kinds, in the order the kinds parameter names them.
const (
	annoChange  = "change"
	annoFailure = "failure"
	annoGap     = "gap"
	annoNotice  = "notice" // the answer was cut at the limit: a region over the part whose records are not in it
	// annoMaintenance is a configured maintenance window, a region: planned work, declared in configuration.
	annoMaintenance = "maintenance"
)

// Annotation is one mark for Grafana. time and timeEnd are epoch milliseconds; timeEnd is set on a region (a gap, or
// the part of the range a cut answer leaves out).
type Annotation struct {
	Time    int64    `json:"time"`
	TimeEnd int64    `json:"timeEnd,omitempty"`
	Title   string   `json:"title"`
	Text    string   `json:"text"` // short, HTML-escaped; a link back to OpsTide when a public URL is set
	Tags    []string `json:"tags"`
	// TagList is Tags joined with commas, for JSON data sources that hand Grafana an array as text (Infinity does).
	TagList    string `json:"tagList"`
	Kind       string `json:"kind"`                 // change | failure | gap | notice | maintenance
	RecordID   string `json:"recordId,omitempty"`   // the Explorer record (changes and failures)
	Provenance string `json:"provenance,omitempty"` // OBSERVED | DERIVED | UNKNOWN, for records
	Namespace  string `json:"namespace,omitempty"`
	URL        string `json:"url"` // the record (or the stretch) in the OpsTide Explorer
}

// annotationTime reads epoch milliseconds (Grafana's ${__from}) or RFC 3339.
func annotationTime(r *http.Request, name string) (time.Time, error) {
	v := strings.TrimSpace(r.URL.Query().Get(name))
	if v == "" {
		return time.Time{}, nil
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n < 0 || n > 1<<45 {
			return time.Time{}, badRequest(name + " is out of range")
		}
		if n < 1e11 { // before 1973 in milliseconds, but a time of this century in seconds
			return time.Time{}, badRequest(name + " looks like epoch seconds; send milliseconds")
		}
		return time.UnixMilli(n).UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, badRequest(name + " must be epoch milliseconds or an RFC 3339 time")
	}
	return t.UTC(), nil
}

type annotationQuery struct {
	from, to                time.Time
	changes, failures, gaps bool
	maintenance             bool
	namespace, workload     string
	limit                   int
}

func (s *Server) parseAnnotations(r *http.Request) (annotationQuery, error) {
	var q annotationQuery
	v := r.URL.Query()
	known := map[string]bool{"from": true, "to": true, "kinds": true, "namespace": true, "workload": true, "limit": true}
	for k := range v {
		if !known[k] {
			return q, badRequest("unknown parameter " + fmt.Sprintf("%.40q", k))
		}
	}
	var err error
	if q.from, err = annotationTime(r, "from"); err != nil {
		return q, err
	}
	if q.to, err = annotationTime(r, "to"); err != nil {
		return q, err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	switch {
	case q.from.IsZero() && q.to.IsZero():
		q.from, q.to = now.Add(-24*time.Hour), now
	case q.from.IsZero():
		return q, badRequest("to needs from")
	case q.to.IsZero():
		q.to = now
	}
	if !q.to.After(q.from) {
		return q, badRequest("to must be after from")
	}
	if q.to.Sub(q.from) > maxAnnotationSpan {
		return q, badRequest("the range is longer than 31 days")
	}
	kinds := splitList(v.Get("kinds"))
	if len(kinds) == 0 {
		kinds = []string{"changes", "failures", "gaps", "maintenance"}
	}
	for _, k := range kinds {
		switch k {
		case "changes":
			q.changes = true
		case "failures":
			q.failures = true
		case "gaps":
			q.gaps = true
		case "maintenance":
			q.maintenance = true
		default:
			return q, badRequest("kinds takes changes, failures, gaps and maintenance")
		}
	}
	q.namespace, q.workload = strings.TrimSpace(v.Get("namespace")), strings.TrimSpace(v.Get("workload"))
	if err := tooLong("namespace", q.namespace, 253); err != nil {
		return q, err
	}
	if err := tooLong("workload", q.workload, 128); err != nil {
		return q, err
	}
	if v.Has("limit") {
		n, err := strconv.Atoi(v.Get("limit"))
		if err != nil || n < 1 || n > maxAnnotationLimit {
			return q, badRequest("limit must be between 1 and " + itoa(maxAnnotationLimit))
		}
		q.limit = n
	} else {
		q.limit = defAnnotationLimit
	}
	return q, nil
}

// annotationTypes are the record types the kinds ask for: the Explorer's "Changes only" set for changes; for failures
// the failure-like records "What changed before this" is offered on, with Warning Kubernetes Events (only Warning ones
// are read: Normal Events are routine and left out).
func (q annotationQuery) types() []string {
	var out []string
	if q.changes {
		out = append(out, changeTypes...)
	}
	if q.failures {
		for t := range failureTypes {
			if !has(out, t) {
				out = append(out, t)
			}
		}
		out = append(out, store.EvidenceTypeName)
	}
	sort.Strings(out)
	return out
}

func (s *Server) annotations(w http.ResponseWriter, r *http.Request) {
	v, err := s.buildAnnotations(r)
	reply(w, r, s.log, v, err)
}

func (s *Server) buildAnnotations(r *http.Request) ([]Annotation, error) {
	q, err := s.parseAnnotations(r)
	if err != nil {
		return nil, err
	}
	out := []Annotation{}
	var groups []string // the record groups drawn, for which watches' gaps matter
	if q.changes || q.failures {
		eq := store.ExplorerQuery{From: q.from, To: q.to, Namespace: q.namespace, WorkloadID: q.workload, Types: q.types(), Limit: annotationPageLimit}
		groups = groupsOfFilters(explorerFilters{Types: eq.Types})
		// The newest records first, page by page, up to the limit; one more says whether there are older ones.
		var recs []ExplorerRecord
		for len(recs) <= q.limit {
			eq.Limit = min(annotationPageLimit, q.limit+1-len(recs))
			pg, err := s.db.Explorer(r.Context(), eq)
			if err != nil {
				return nil, err
			}
			for _, row := range pg.Rows {
				recs = append(recs, recordOfRow(row, false))
			}
			if pg.NextCursor == "" || len(pg.Rows) == 0 {
				break
			}
			eq.Cursor = pg.NextCursor
		}
		cut := len(recs) > q.limit
		if cut {
			recs = recs[:q.limit]
		}
		s.markRecords(recs)
		for i := len(recs) - 1; i >= 0; i-- { // oldest first
			out = append(out, s.recordAnnotation(recs[i]))
		}
		if cut && len(recs) > 0 {
			// The oldest record kept is recs[len-1]: the records before it in the range are not in this answer.
			oldest := recs[len(recs)-1].At
			out = append(out, Annotation{Time: q.from.UnixMilli(), TimeEnd: oldest.UnixMilli(), Kind: annoNotice, Tags: []string{"opstide", "notice"},
				Title: "More records than the limit: only the newest " + itoa(q.limit) + " are drawn",
				Text:  html.EscapeString("Changes and failures recorded before this point of the range are not drawn. Narrow the range or the filters, or raise limit (at most " + itoa(maxAnnotationLimit) + ")."),
				URL:   s.publicLink(explorerPath(q.from, oldest, q.namespace, q.workload, ""))})
		}
	}
	if q.gaps {
		gs, err := s.gapAnnotations(r, q, groups)
		if err != nil {
			return nil, err
		}
		out = append(out, gs...)
	}
	if q.maintenance {
		out = append(out, s.maintenanceAnnotations(q)...)
	}
	for i := range out {
		out[i].TagList = strings.Join(out[i].Tags, ",")
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Time != b.Time {
			return a.Time < b.Time
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.RecordID != b.RecordID {
			return a.RecordID < b.RecordID
		}
		return a.TimeEnd < b.TimeEnd
	})
	return out, nil
}

func (s *Server) recordAnnotation(rec ExplorerRecord) Annotation {
	kind := annoFailure
	if has(changeTypes, rec.Type) {
		kind = annoChange
	}
	res := strings.TrimSpace(rec.Resource.Kind + " " + rec.Resource.Name)
	if rec.Resource.Namespace != "" {
		res = strings.TrimSpace(rec.Resource.Kind + " " + rec.Resource.Namespace + "/" + rec.Resource.Name)
	}
	tags := []string{"opstide", kind, rec.Type}
	if rec.Resource.Namespace != "" {
		tags = append(tags, "namespace:"+rec.Resource.Namespace)
	}
	if rec.Resource.Kind != "" {
		tags = append(tags, "kind:"+rec.Resource.Kind)
	}
	if rec.Reason != "" {
		tags = append(tags, "reason:"+rec.Reason)
	}
	tags = append(tags, strings.ToLower(rec.Provenance))
	if rec.Maintenance != nil {
		tags = append(tags, "maintenance:"+rec.Maintenance.Name)
	}
	link := s.publicLink(explorerPath(rec.At.Add(-time.Hour), rec.At.Add(time.Hour), "", "", rec.ID))
	text := html.EscapeString(res + " · " + rec.Provenance + " · " + rec.Source + ". Recorded by OpsTide; an annotation marks when, not why.")
	if s.cfg.PublicURL != "" {
		text += ` <a href="` + html.EscapeString(link) + `">Open in OpsTide</a>`
	}
	// The summary carries cluster-controlled strings (field managers, Event reasons, object names): escaped like the text.
	return Annotation{Time: rec.At.UnixMilli(), Title: html.EscapeString(rec.Summary), Text: text, Tags: tags, Kind: kind, RecordID: rec.ID, Provenance: rec.Provenance,
		Namespace: rec.Resource.Namespace, URL: link}
}

// gapAnnotations are the stretches of the range OpsTide was not watching what the drawn records come from (every
// watch when only gaps are asked for), merged across watches into regions, with the watches each one concerns; and the
// parts of the range before OpsTide first watched or older than the history it keeps. A watch that is turned off is
// not drawn as a gap: it is a setting, shown on System.
func (s *Server) gapAnnotations(r *http.Request, q annotationQuery, groups []string) ([]Annotation, error) {
	c, err := s.buildContext(r.Context(), q.from, q.to, q.namespace)
	if err != nil {
		return nil, err
	}
	type piece struct {
		sp    Span
		label string
	}
	var pieces []piece
	for _, sc := range c.relevant(groups) {
		var gs []Span
		switch sc.State {
		case CovGap:
			gs = sc.Gaps
		case CovBeforeFirst:
			// This watch began after the range (or has not begun) while others were on: the whole range is not
			// watched for it, so a quiet graph is not read as a calm one. A watch turned off (CovDisabled) is a setting.
			end := q.to
			if c.Now.Before(end) {
				end = c.Now
			}
			if end.After(q.from) {
				gs = []Span{{q.from, end}}
			}
		case CovUnavailable:
			end := q.to
			if c.Now.Before(end) {
				end = c.Now
			}
			start := q.from
			if sc.FirstWatchedAt != nil && sc.FirstWatchedAt.After(start) {
				start = *sc.FirstWatchedAt
			}
			gs = gapsOf(sc.Watching, start, end)
		}
		label := sc.Label
		if sc.Namespace != "" {
			label += " in " + sc.Namespace
		}
		for _, g := range gs {
			pieces = append(pieces, piece{g, label})
		}
	}
	// The parts of the range before OpsTide first watched, or older than it keeps, are regions of their own (below);
	// the watches' gaps are drawn only in the rest.
	var obs *Band
	for i := range c.Bands {
		if c.Bands[i].Kind == "observable" {
			obs = &c.Bands[i]
		}
	}
	kept := pieces[:0]
	for _, p := range pieces {
		if obs == nil {
			break
		}
		if sp, ok := clip(p.sp, obs.From, obs.To); ok {
			kept = append(kept, piece{sp, p.label})
		}
	}
	pieces = kept
	sort.SliceStable(pieces, func(i, j int) bool { return pieces[i].sp.From.Before(pieces[j].sp.From) })
	var out []Annotation
	type region struct {
		sp     Span
		labels map[string]bool
	}
	var regions []region
	for _, p := range pieces {
		if n := len(regions); n > 0 && !p.sp.From.After(regions[n-1].sp.To) {
			if p.sp.To.After(regions[n-1].sp.To) {
				regions[n-1].sp.To = p.sp.To
			}
			regions[n-1].labels[p.label] = true
			continue
		}
		regions = append(regions, region{p.sp, map[string]bool{p.label: true}})
	}
	// A range with very many short stretches is not drawn region by region: past maxGapRegions one notice covers the
	// rest of the range, so nothing there reads as watched.
	var capped *Span
	if len(regions) > maxGapRegions {
		capped = &Span{regions[maxGapRegions-1].sp.From, q.to}
		if c.Now.Before(capped.To) && c.Now.After(capped.From) {
			capped.To = c.Now
		}
		regions = regions[:maxGapRegions-1]
	}
	for _, rg := range regions {
		labels := make([]string, 0, len(rg.labels))
		for l := range rg.labels {
			labels = append(labels, l)
		}
		sort.Strings(labels)
		list := strings.Join(labels, ", ")
		if len(labels) > 6 {
			list = strings.Join(labels[:6], ", ") + " and " + itoa(len(labels)-6) + " more"
		}
		out = append(out, s.gapAnnotation(rg.sp, q, "OpsTide was not watching",
			"Not watched: "+list+". Records from this stretch may be missing; a quiet graph here is not proof that nothing happened."))
	}
	if capped != nil {
		a := s.gapAnnotation(*capped, q, "More stretches not watched than can be drawn",
			"From here on OpsTide was not watching in more than "+itoa(maxGapRegions-1)+" separate stretches; they are not drawn one by one. Records from this part of the range may be missing. Narrow the range to see each stretch.")
		a.Kind = annoNotice
		a.Tags = append(a.Tags, annoNotice)
		out = append(out, a)
	}
	for _, b := range c.Bands {
		switch b.Kind {
		case CovOutsideRetn:
			out = append(out, s.gapAnnotation(Span{b.From, b.To}, q, "Older than the history OpsTide keeps",
				"OpsTide keeps "+itoa(c.Retention.Days)+" days; nothing older is held, so nothing is drawn here."))
		case CovBeforeFirst:
			out = append(out, s.gapAnnotation(Span{b.From, b.To}, q, "Before OpsTide first observed the cluster",
				"OpsTide was not running here yet; what happened is not known."))
		}
	}
	return out, nil
}

func (s *Server) gapAnnotation(sp Span, q annotationQuery, title, text string) Annotation {
	link := s.publicLink(explorerPath(sp.From, sp.To, q.namespace, "", "") + "&group=observation")
	t := html.EscapeString(text)
	if s.cfg.PublicURL != "" {
		t += ` <a href="` + html.EscapeString(link) + `">Open in OpsTide</a>`
	}
	tags := []string{"opstide", annoGap}
	if q.namespace != "" {
		tags = append(tags, "namespace:"+q.namespace)
	}
	return Annotation{Time: sp.From.UnixMilli(), TimeEnd: sp.To.UnixMilli(), Title: title, Text: t, Tags: tags, Kind: annoGap, Namespace: q.namespace, URL: link}
}

// maintenanceAnnotations are the configured maintenance windows open in the range, one region per occurrence (for a
// namespace, the windows that cover it). They say work was planned then, as declared; not what it did.
func (s *Server) maintenanceAnnotations(q annotationQuery) []Annotation {
	var out []Annotation
	spans, truncated := s.maintenanceSpans(q.from, q.to, q.namespace)
	if truncated {
		// Some occurrences are not drawn: a notice over the range says so.
		link := s.publicLink(explorerPath(q.from, q.to, q.namespace, "", ""))
		out = append(out, Annotation{Time: q.from.UnixMilli(), TimeEnd: q.to.UnixMilli(), Kind: annoNotice, Tags: []string{"opstide", annoNotice, annoMaintenance},
			Title: "More maintenance window occurrences than are drawn", Text: html.EscapeString(maintenanceTruncatedNote), Namespace: q.namespace, URL: link})
	}
	for _, sp := range spans {
		where := "the whole cluster"
		if len(sp.Namespaces) > 0 {
			where = "namespaces " + strings.Join(sp.Namespaces, ", ")
		}
		text := "Maintenance window " + sp.Name + ", declared in OpsTide's configuration, for " + where + "."
		if sp.Note != "" {
			text += " " + sp.Note
		}
		text += " Records inside it are marked, not hidden."
		link := s.publicLink(explorerPath(sp.From, sp.To, q.namespace, "", ""))
		t := html.EscapeString(text)
		if s.cfg.PublicURL != "" {
			t += ` <a href="` + html.EscapeString(link) + `">Open in OpsTide</a>`
		}
		tags := []string{"opstide", annoMaintenance, "maintenance:" + sp.Name}
		for _, ns := range sp.Namespaces {
			tags = append(tags, "namespace:"+ns)
		}
		out = append(out, Annotation{Time: sp.From.UnixMilli(), TimeEnd: sp.To.UnixMilli(), Title: html.EscapeString("Maintenance: " + sp.Name), Text: t, Tags: tags,
			Kind: annoMaintenance, Provenance: ProvDerived, Namespace: q.namespace, URL: link})
	}
	return out
}

// explorerPath is the Explorer over [from, to), with a record selected when rec is set.
func explorerPath(from, to time.Time, namespace, workload, rec string) string {
	p := url.Values{}
	p.Set("from", from.UTC().Format(time.RFC3339))
	p.Set("to", to.UTC().Format(time.RFC3339))
	if namespace != "" {
		p.Set("namespace", namespace)
	}
	if workload != "" {
		p.Set("workload", workload)
	}
	if rec != "" {
		p.Set("rec", rec)
	}
	return "/explorer?" + p.Encode()
}

// publicLink puts the configured public address in front of a path; with none it stays a path.
func (s *Server) publicLink(path string) string {
	return s.cfg.PublicURL + path
}
