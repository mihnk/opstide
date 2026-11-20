package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mihnk/opstide/internal/digest"
	"github.com/mihnk/opstide/internal/schedule"
	"github.com/mihnk/opstide/internal/store"
)

// troubleTypes are the records the morning summary lists as something going wrong in a workload. A failed run is
// listed on its own, and a Warning Event is counted, not listed: there are too many of them.
var troubleTypes = []string{
	"pod.restarted", "pod.oomkilled", "pod.evicted",
	"release.stalled", "release.failed", "release.unavailable", "release.health.degraded",
}

// MorningOptions says which summary to build: the stretch, and how it is written.
type MorningOptions struct {
	From, To   time.Time
	TimeZone   string
	Language   string // en or tr
	Include    []string
	Test, Late bool
}

// Morning builds the morning summary of [From, To) from the record.
func (s *Server) Morning(ctx context.Context, o MorningOptions) (digest.Morning, error) {
	m := digest.Morning{Schema: digest.Schema, Cluster: s.cfg.Cluster, From: o.From.UTC(), To: o.To.UTC(), TimeZone: o.TimeZone,
		Language: o.Language, Include: o.Include, Test: o.Test, Late: o.Late, Demo: s.cfg.Demo,
		Failed: []digest.Run{}, Overdue: []digest.Overdue{}, Troubles: []digest.Trouble{}, NotWatched: []string{}}
	if m.Include == nil {
		m.Include = digest.DefaultIncludes
	}
	if s.cfg.PublicURL != "" {
		m.Link = s.publicLink("/")
	}
	loc, err := time.LoadLocation(o.TimeZone)
	if err != nil {
		return m, err
	}
	ops, err := s.db.Operations(ctx, false)
	if err != nil {
		return m, err
	}
	byID := map[string]store.Operation{}
	for _, op := range ops {
		byID[op.ID] = op
	}

	// Runs that started or ended in the stretch, and the failed ones by when they ended.
	runs, err := s.db.RunsBetween(ctx, o.From, o.To)
	if err != nil {
		return m, err
	}
	var failed []digest.Run
	for _, r := range runs {
		m.Runs.Total++
		switch r.Status {
		case store.RunSucceeded:
			m.Runs.Succeeded++
		case store.RunFailed:
			m.Runs.Failed++
			at := r.FinishedAt
			if at.IsZero() {
				at = r.CreatedAt
			}
			op := byID[r.OperationID]
			f := digest.Run{Name: displayName(op), Namespace: r.Namespace, CronJob: op.Name, At: at}
			if op.ID == "" {
				f.Name, f.CronJob = r.JobName, r.JobName
			}
			if mk := s.maintenanceAt(at, r.Namespace); mk != nil {
				f.Maintenance = mk.Name
			}
			if s.cfg.PublicURL != "" && op.ID != "" {
				f.Link = s.publicLink("/operations/" + url.PathEscape(op.ID) + "?run=" + url.QueryEscape(r.ID))
			}
			failed = append(failed, f)
		case store.RunUnknown:
			m.Runs.Unknown++
		default:
			m.Runs.Running++
		}
	}
	sort.Slice(failed, func(i, j int) bool { return failed[i].At.After(failed[j].At) })
	m.Failed, m.FailedMore = cut(failed, digest.MaxListed)

	// CronJobs overdue for a success, now.
	cov, err := loadCoverage(ctx, s.db, o.To.Add(-longestWithin(ops)), o.To, o.To)
	if err != nil {
		return m, err
	}
	last, err := s.db.LastSuccesses(ctx)
	if err != nil {
		return m, err
	}
	var overdue []digest.Overdue
	for _, op := range ops {
		d := s.deadline(op, last[op.ID], cov, o.To)
		if d == nil || d.State != "overdue" {
			continue
		}
		ov := digest.Overdue{Name: displayName(op), Namespace: op.Namespace, CronJob: op.Name, WithinSeconds: d.WithinSeconds, LastSuccess: d.LastSuccess}
		if s.cfg.PublicURL != "" {
			ov.Link = s.publicLink("/operations/" + url.PathEscape(op.ID))
		}
		overdue = append(overdue, ov)
	}
	m.Overdue, m.OverdueMore = cut(overdue, digest.MaxListed)

	// Counts and watches, as the Overview has them.
	sum, err := s.summarize(ctx, o.From, o.To)
	if err != nil {
		return m, err
	}
	for _, it := range sum.Items {
		switch it.Key {
		case "changes":
			m.Changes = it.Count
		case "warnings":
			m.Warnings = it.Count
		}
	}
	for _, nw := range sum.NotWhole {
		m.NotWatched = append(m.NotWatched, nw.Label)
	}

	// Troubles, newest first, each with the change recorded nearest before it.
	page, err := s.db.Explorer(ctx, store.ExplorerQuery{From: o.From, To: o.To, Types: troubleTypes, Limit: 100})
	if err != nil {
		return m, err
	}
	for i, row := range page.Rows {
		if i == digest.MaxListed {
			m.TroubleMore = len(page.Rows) - i
			break
		}
		b, err := s.before(ctx, row, "1h")
		if err != nil {
			return m, err
		}
		t := digest.Trouble{Type: b.Record.Type, Summary: b.Record.Summary, At: b.Record.At, ChangesBefore: len(b.Changes)}
		if len(b.Changes) > 0 {
			c := b.Changes[0]
			t.Nearest = &digest.Change{Summary: c.Record.Summary, At: c.Record.At, Writer: c.Writer, SecondsBefore: c.SecondsBefore}
		}
		if s.cfg.PublicURL != "" {
			t.Link = s.publicLink(explorerPath(t.At.Add(-time.Hour), t.At.Add(time.Hour), "", "", b.Record.ID))
		}
		m.Troubles = append(m.Troubles, t)
	}

	if slices.Contains(m.Include, digest.IncludeChanges) {
		cp, err := s.db.Explorer(ctx, store.ExplorerQuery{From: o.From, To: o.To, Types: changeTypes, Limit: digest.MaxListed})
		if err != nil {
			return m, err
		}
		for _, row := range cp.Rows {
			rec := recordOfRow(row, false)
			c := digest.Change{Summary: rec.Summary, At: rec.At}
			if s.cfg.PublicURL != "" {
				c.Link = s.publicLink(explorerPath(rec.At.Add(-time.Hour), rec.At.Add(time.Hour), "", "", rec.ID))
			}
			m.ChangeList = append(m.ChangeList, c)
		}
		if m.Changes > len(m.ChangeList) {
			m.ChangesMore = m.Changes - len(m.ChangeList)
		}
	}

	// What is still due today, after the summary's time.
	y, mo, d := o.To.In(loc).Date()
	dayEnd := time.Date(y, mo, d+1, 0, 0, 0, 0, loc)
	for _, op := range ops {
		if op.Suspend {
			continue
		}
		sc, err := schedule.Parse(op.Schedule, op.TimeZone, s.cfg.ControllerZone)
		if err != nil {
			continue
		}
		times, _ := sc.Between(o.To, dayEnd)
		m.Due.Count += len(times)
		if len(times) > 0 && (m.Due.NextAt.IsZero() || times[0].Before(m.Due.NextAt)) {
			m.Due.NextAt, m.Due.NextName = times[0], displayName(op)
		}
	}
	return m, nil
}

func cut[T any](list []T, n int) ([]T, int) {
	if list == nil {
		return []T{}, 0
	}
	if len(list) <= n {
		return list, 0
	}
	return list[:n], len(list) - n
}

// MorningPreview is a summary as the API gives it: the data, and the lines people read.
type MorningPreview struct {
	Morning digest.Morning `json:"morning"`
	Lines   []digest.Line  `json:"lines"`
}

// morning answers GET /api/v1/morning?tz=&covers=&lang=&include=: the summary that would be sent now. Without
// parameters it is the configured one (or the defaults when none is configured).
func (s *Server) morning(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	st := s.notifyStatus()
	tz, covers, lang, include := st.TimeZone, st.Covers, st.Language, st.Include
	if v := q.Get("tz"); v != "" {
		tz = v
	}
	if tz == "" {
		tz = "UTC"
	}
	if v := q.Get("covers"); v != "" {
		covers = v
	}
	if covers == "" {
		covers = digest.CoversSince18
	}
	if v := q.Get("lang"); v != "" {
		lang = v
	}
	if lang == "" {
		lang = "en"
	}
	if q.Has("include") {
		include = []string{}
		for _, p := range strings.Split(q.Get("include"), ",") {
			if p != "" {
				include = append(include, p)
			}
		}
	}
	if include == nil {
		include = digest.DefaultIncludes
	}
	var err error
	for k := range q {
		if !slices.Contains([]string{"tz", "covers", "lang", "include"}, k) {
			err = badRequest("unknown parameter " + k + "; this takes tz, covers, lang and include")
		}
	}
	loc, lerr := time.LoadLocation(tz)
	switch {
	case err != nil:
	case len(tz) > 64 || lerr != nil:
		err = badRequest("unknown time zone")
	case !digest.ValidCovers(covers):
		err = badRequest("covers takes since-18h, 12h or 24h")
	case lang != "en" && lang != "tr":
		err = badRequest("lang takes en or tr")
	}
	for _, p := range include {
		if err == nil && !slices.Contains(digest.AllIncludes, p) {
			err = badRequest("include takes " + strings.Join(digest.AllIncludes, ", "))
		}
	}
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	now := s.now().UTC()
	m, err := s.Morning(r.Context(), MorningOptions{From: digest.Since(covers, now, loc), To: now, TimeZone: tz, Language: lang, Include: include})
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	reply(w, r, s.log, MorningPreview{Morning: m, Lines: m.Lines()}, nil)
}

// Notifier sends the morning summary (internal/notify). The API reads where it stands and asks it for test messages;
// it never changes what is configured: that is the chart's.
type Notifier interface {
	Status() NotifyStatus
	Test(ctx context.Context, destination string) (Delivery, error)
}

// NotifyStatus is how the morning summary is configured, and how its last deliveries went.
type NotifyStatus struct {
	Configured   bool                `json:"configured"`
	Demo         bool                `json:"demo,omitempty"` // the demo's made-up destination: nothing is sent
	Schedule     string              `json:"schedule,omitempty"`
	TimeZone     string              `json:"timeZone,omitempty"`
	Covers       string              `json:"covers,omitempty"`
	Language     string              `json:"language,omitempty"`
	Include      []string            `json:"include,omitempty"`
	Next         time.Time           `json:"next,omitzero"`
	LastSent     time.Time           `json:"lastSent,omitzero"` // when the latest scheduled summary went out
	Destinations []DestinationStatus `json:"destinations"`
}

// DestinationStatus is one place the summary goes. Its URL is never shown: Source says where it is kept.
type DestinationStatus struct {
	Name   string `json:"name"`
	Type   string `json:"type"`   // slack, teams or webhook
	Source string `json:"source"` // e.g. "Secret opstide-notify, key slack"
	// Last is the latest attempt to deliver to it, scheduled or a test.
	Last *Delivery `json:"last,omitempty"`
	// LastOK is when a delivery last succeeded.
	LastOK time.Time `json:"lastOk,omitzero"`
}

// Delivery is one attempt to deliver a message.
type Delivery struct {
	At     time.Time `json:"at"`
	OK     bool      `json:"ok"`
	Test   bool      `json:"test,omitempty"`
	Status int       `json:"status,omitempty"` // the HTTP status the destination answered with
	Error  string    `json:"error,omitempty"`  // what went wrong, never the URL
}

// SetNotifier connects the notifier, which needs the server to build summaries and so is made after it.
func (s *Server) SetNotifier(n Notifier) { s.notifier = n }

func (s *Server) notifyStatus() NotifyStatus {
	if s.notifier == nil {
		return NotifyStatus{Destinations: []DestinationStatus{}}
	}
	st := s.notifier.Status()
	if st.Destinations == nil {
		st.Destinations = []DestinationStatus{}
	}
	return st
}

func (s *Server) notifyGet(w http.ResponseWriter, r *http.Request) {
	reply(w, r, s.log, s.notifyStatus(), nil)
}

// notifyTest sends a test message to one configured destination: POST {"destination": name}. Only a destination the
// chart configured can be named; a URL never comes from a request.
func (s *Server) notifyTest(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		reply(w, r, s.log, nil, badRequest("send JSON: {\"destination\": name}"))
		return
	}
	var body struct {
		Destination string `json:"destination"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Destination == "" {
		reply(w, r, s.log, nil, badRequest("send JSON: {\"destination\": name}"))
		return
	}
	if s.notifier == nil {
		reply(w, r, s.log, nil, errNotFound)
		return
	}
	d, err := s.notifier.Test(r.Context(), body.Destination)
	switch {
	case errors.Is(err, ErrNoDestination):
		err = errNotFound
	case errors.Is(err, ErrTooSoon):
		err = badRequest("a test was sent to it less than 30 seconds ago")
	}
	reply(w, r, s.log, d, err)
}

// Errors a Notifier's Test returns.
var (
	ErrNoDestination = errors.New("no such destination")
	ErrTooSoon       = errors.New("tested too recently")
)
