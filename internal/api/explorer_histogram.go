package api

import (
	"net/http"
	"time"

	"github.com/mihnk/opstide/internal/store"
)

// The Explorer's volume chart: how many records the filters match per stretch of the range, by class. It counts what
// was recorded; a stretch with no records is not called quiet here, the coverage answer says whether it was watched.

// histogramSteps are the bucket lengths the chart uses: the shortest that gives at most maxHistogramBuckets.
var histogramSteps = []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute, 30 * time.Minute,
	time.Hour, 2 * time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour}

const maxHistogramBuckets = 40

// ExplorerHistogram is the answer of GET /api/v1/explorer/histogram.
type ExplorerHistogram struct {
	Schema        int       `json:"schema"`
	From          time.Time `json:"from"`
	To            time.Time `json:"to"`
	BucketSeconds int       `json:"bucketSeconds"`
	// Buckets cover [From, To) in order, empty ones included. The first may start before From and the last end after
	// To: a bucket is a whole stretch of the reader's clock, and only records inside the range are counted.
	Buckets []ExplorerBucket `json:"buckets"`
	Total   int              `json:"total"`
	Counted string           `json:"counted"`
	Classes string           `json:"classes"`
}

// ExplorerBucket counts one stretch by class.
type ExplorerBucket struct {
	Start   time.Time `json:"start"`
	Error   int       `json:"error"`
	Warning int       `json:"warning"`
	Change  int       `json:"change"`
	Other   int       `json:"other"`
}

func histogramStep(span time.Duration) time.Duration {
	for _, s := range histogramSteps {
		if span/s <= maxHistogramBuckets {
			return s
		}
	}
	return histogramSteps[len(histogramSteps)-1] * time.Duration(span/(histogramSteps[len(histogramSteps)-1]*maxHistogramBuckets)+1)
}

func (s *Server) explorerHistogram(w http.ResponseWriter, r *http.Request) {
	q, _, err := s.parseExplorer(r)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	var offset time.Duration
	if tz := r.URL.Query().Get("tz"); tz != "" {
		loc, err := time.LoadLocation(tz)
		if err != nil || len(tz) > 64 {
			reply(w, r, s.log, nil, badRequest("tz is not a time zone this server knows"))
			return
		}
		_, sec := q.To.In(loc).Zone()
		offset = time.Duration(sec) * time.Second
	}
	step := histogramStep(q.To.Sub(q.From))
	bs, err := s.db.ExplorerHistogram(r.Context(), q, step, offset, changeTypes)
	if err != nil {
		reply(w, r, s.log, nil, err)
		return
	}
	out := ExplorerHistogram{Schema: Schema, From: q.From, To: q.To, BucketSeconds: int(step / time.Second), Buckets: []ExplorerBucket{},
		Counted: "records in the range matching every filter, as the list shows them (routine records only with routine=1)",
		Classes: "error: severity error; warning: severity warning, or a Kubernetes Event typed Warning; change: a record that says something was changed; other: the rest. Each record is in one class."}
	by := map[int64]store.HistogramBucket{}
	for _, b := range bs {
		by[b.Start.UnixMilli()] = b
	}
	off := offset.Milliseconds()
	ms := step.Milliseconds()
	start := ((q.From.UnixMilli()+off)/ms)*ms - off
	for t := start; t < q.To.UnixMilli(); t += ms {
		b := by[t]
		out.Buckets = append(out.Buckets, ExplorerBucket{Start: time.UnixMilli(t).UTC(), Error: b.Error, Warning: b.Warning, Change: b.Change, Other: b.Other})
		out.Total += b.Error + b.Warning + b.Change + b.Other
	}
	reply(w, r, s.log, out, nil)
}
