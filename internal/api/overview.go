package api

import (
	"net/http"
	"time"
)

// Overview is the strip above the Overview screen: how much ran, and how
// much of it failed, over the last days, with the times OpsTide was not
// watching. It is one aggregate query, so a month stays cheap.
type Overview struct {
	Schema int       `json:"schema"`
	Span   string    `json:"span"`
	From   time.Time `json:"from"`
	To     time.Time `json:"to"` // the end of today in the reader's zone
	Now    time.Time `json:"now"`
	// WatchedSince is when OpsTide first watched Jobs; zero when it never has.
	// Before it nothing is known.
	WatchedSince  time.Time        `json:"watchedSince,omitzero"`
	Gaps          []Span           `json:"gaps"`
	BucketSeconds int              `json:"bucketSeconds"`
	Buckets       []OverviewBucket `json:"buckets"`
	// RetentionDays is how far back the record goes at most.
	RetentionDays int `json:"retentionDays"`
}

// OverviewBucket counts runs created in one stretch (BucketSeconds).
type OverviewBucket struct {
	Start  time.Time `json:"start"`
	Runs   int       `json:"runs"`
	Failed int       `json:"failed,omitempty"`
}

var overviewSpans = map[string]int{"24h": 1, "5d": 5, "7d": 7, "30d": 30}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	v, err := s.buildOverview(r)
	reply(w, r, s.log, v, err)
}

func (s *Server) buildOverview(r *http.Request) (Overview, error) {
	ctx := r.Context()
	loc, err := zone(r)
	if err != nil {
		return Overview{}, err
	}
	span := r.URL.Query().Get("span")
	if span == "" {
		span = "5d"
	}
	days, ok := overviewSpans[span]
	if !ok {
		return Overview{}, badRequest("span is 24h, 5d, 7d or 30d")
	}
	now := s.now().UTC()
	_, endOfToday := dayBounds(now, loc)
	to := endOfToday
	from := to.AddDate(0, 0, -days)
	bucket := time.Hour
	if days > 7 {
		bucket = 6 * time.Hour
	}
	o := Overview{Schema: Schema, Span: span, From: from.UTC(), To: to.UTC(), Now: now, Gaps: []Span{},
		BucketSeconds: int(bucket / time.Second), Buckets: []OverviewBucket{}, RetentionDays: s.cfg.Retention.Days}
	if o.WatchedSince, err = s.db.FirstWatched(ctx); err != nil {
		return o, err
	}
	_, off := from.In(loc).Zone()
	hist, err := s.db.RunHistogram(ctx, from, to, bucket, time.Duration(off)*time.Second)
	if err != nil {
		return o, err
	}
	for _, b := range hist {
		o.Buckets = append(o.Buckets, OverviewBucket{Start: b.Start, Runs: b.Runs, Failed: b.Failed})
	}

	ops, err := s.db.Operations(ctx, true)
	if err != nil {
		return o, err
	}
	seen := map[string]bool{}
	var namespaces []string
	for _, op := range ops {
		if !seen[op.Namespace] {
			seen[op.Namespace] = true
			namespaces = append(namespaces, op.Namespace)
		}
	}
	if len(namespaces) == 0 {
		namespaces = append(namespaces, s.cfg.Namespaces...)
		if len(namespaces) == 0 {
			namespaces = []string{""}
		}
	}
	cov, err := loadCoverage(ctx, s.db, from, now, now)
	if err != nil {
		return o, err
	}
	end := now
	if to.Before(end) {
		end = to
	}
	o.Gaps = append(o.Gaps, cov.jobGaps(namespaces, from, end)...)
	return o, nil
}
