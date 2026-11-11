package api

import (
	"math"
	"sort"
	"time"

	"github.com/mihnk/opstide/internal/store"
)

// A CronJob's duration trend compares how long its recent runs took with how long they took before. It is a pair of
// numbers per period, never a verdict: OpsTide does not say a job got worse, only that its recent median is longer
// than its baseline median by so much, and only when both medians rest on enough runs.
//
//	recent:   the last 7 calendar days in the reader's zone (today and the 6 days before it), up to now;
//	baseline: the 23 calendar days before that, cut at the retention boundary when the record is shorter.
//
// Only succeeded runs with a recorded start and end count; a run belongs to the period its start falls in. Failed
// runs, runs whose outcome is unknown and runs still going are left out. p50 and p95 are nearest-rank percentiles: the
// value of the run at rank ceil(p × n) of the sorted durations, so each is a duration some run actually took.

const (
	trendRecentDays   = 7
	trendBaselineDays = 23
	// trendMinRuns is how many runs each period needs before the two medians are compared.
	trendMinRuns = 5
	// trendLongerAt is the ratio of recent to baseline median from which the trend is marked as longer.
	trendLongerAt = 1.3
	// trendPoints is how many of the latest runs the detail's per-run durations carry.
	trendPoints = 60
)

// trendBasis says, in the answer, how the numbers were made.
const trendBasis = "Succeeded runs with a recorded start and end, by when they started. Recent is the last 7 days in the requested time zone, " +
	"baseline the 23 days before that (shortened where the kept history is shorter). p50 and p95 are nearest-rank percentiles of the run durations. " +
	"The ratio is recent p50 over baseline p50, given only when both periods have at least 5 runs; it is marked longer from 1.3. " +
	"Runs OpsTide did not see while it was not watching are not counted; such a period is marked not whole."

// DurationPeriod is one period of a duration trend.
type DurationPeriod struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	Runs int       `json:"runs"` // succeeded runs with a known start and end that started in the period
	// P50Seconds and P95Seconds are null when no run counts; rounded to whole seconds. P50Ms and P95Ms are the same
	// percentiles in milliseconds, for runs that take less than a few seconds (the ratio is of these exact values).
	P50Seconds *int64 `json:"p50Seconds"`
	P95Seconds *int64 `json:"p95Seconds"`
	P50Ms      *int64 `json:"p50Ms"`
	P95Ms      *int64 `json:"p95Ms"`
	// Whole says OpsTide watched Jobs in the CronJob's namespace throughout the period (from when the CronJob existed).
	// When it is false, runs from the stretches not watched may be missing from the numbers; they are not counted as absent.
	Whole             bool  `json:"whole"`
	NotWatchedSeconds int64 `json:"notWatchedSeconds"`
	// ShortenedByRetention: the period would reach further back than the history OpsTide keeps.
	ShortenedByRetention bool `json:"shortenedByRetention,omitempty"`
}

// DurationPoint is one succeeded run's duration.
type DurationPoint struct {
	RunID     string    `json:"runId"`
	StartedAt time.Time `json:"startedAt"`
	Seconds   int64     `json:"seconds"`
	Ms        int64     `json:"ms"`
	Recent    bool      `json:"recent"` // in the recent period, else in the baseline
}

// DurationTrend compares a CronJob's recent run durations with its baseline.
type DurationTrend struct {
	Provenance string         `json:"provenance"` // DERIVED: computed from recorded runs by the rule in Basis
	Recent     DurationPeriod `json:"recent"`
	Baseline   DurationPeriod `json:"baseline"`
	MinRuns    int            `json:"minRuns"`
	// Enough says both periods have at least MinRuns runs; without it there is no ratio and no marker.
	Enough bool `json:"enough"`
	// Ratio is recent p50 / baseline p50 (1.42 is 42% longer), rounded to two decimals; null unless Enough.
	Ratio    *float64 `json:"ratio"`
	LongerAt float64  `json:"longerAt"`
	// Longer marks a ratio of at least LongerAt backed by Enough runs. It is a marker, not a judgement.
	Longer bool `json:"longer"`
	// Basis and Points are on the detail only: how the numbers are made, and the latest runs' durations, oldest first.
	Basis  string          `json:"basis,omitempty"`
	Points []DurationPoint `json:"points,omitempty"`
}

// trendPeriods are the recent and baseline bounds for now in loc, with the baseline cut at the retention boundary.
func (s *Server) trendPeriods(now time.Time, loc *time.Location) (recent, baseline DurationPeriod) {
	todayFrom, _ := dayBounds(now, loc)
	rf := todayFrom.AddDate(0, 0, -(trendRecentDays - 1))
	bf := rf.AddDate(0, 0, -trendBaselineDays)
	recent = DurationPeriod{From: rf.UTC(), To: now.UTC()}
	baseline = DurationPeriod{From: bf.UTC(), To: rf.UTC()}
	if d := s.cfg.Retention.Days; d > 0 {
		// The retention boundary is compared in the reader's zone, like the period bounds: d calendar days back at the
		// same wall time. In UTC it can sit an hour either side of a local midnight across a DST change, which made
		// "shortened" flip with the hour of the day. The cut itself is the later of the two, so From never reaches
		// into history that is not held.
		if b := now.In(loc).AddDate(0, 0, -d); b.After(bf) {
			if bu := now.UTC().AddDate(0, 0, -d); bu.After(b) {
				b = bu
			}
			baseline.From, baseline.ShortenedByRetention = b.UTC(), true
			if baseline.From.After(baseline.To) {
				baseline.From = baseline.To
			}
		}
	}
	return recent, baseline
}

// nearestRank is the p-th percentile (0 < p ≤ 1) of sorted by the nearest-rank method.
func nearestRank(sorted []time.Duration, p float64) time.Duration {
	i := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[min(max(i, 0), len(sorted)-1)]
}

func secondsOf(d time.Duration) *int64 {
	v := int64(math.Round(d.Seconds()))
	return &v
}

func msOf(d time.Duration) *int64 {
	v := d.Milliseconds()
	return &v
}

// fill sets the period's counts and percentiles from the durations of the runs in it.
func (p *DurationPeriod) fill(ds []time.Duration) {
	p.Runs = len(ds)
	if len(ds) == 0 {
		return
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	p50, p95 := nearestRank(ds, 0.5), nearestRank(ds, 0.95)
	p.P50Seconds, p.P95Seconds = secondsOf(p50), secondsOf(p95)
	p.P50Ms, p.P95Ms = msOf(p50), msOf(p95)
}

// cover says whether Jobs in the operation's namespace were watched throughout the period, from when the CronJob
// existed until it was deleted. Time before OpsTide first watched counts as not watched: runs then were not seen.
func (p *DurationPeriod) cover(cov *coverage, op store.Operation) {
	from, to := p.From, p.To
	if op.CreatedAt.After(from) {
		from = op.CreatedAt
	}
	if !op.DeletedAt.IsZero() && op.DeletedAt.Before(to) {
		to = op.DeletedAt
	}
	var missing time.Duration
	if from.Before(to) {
		for _, g := range gaps(cov.jobs(op.Namespace), from, to) {
			missing += g.To.Sub(g.From)
		}
	}
	if missing < time.Second {
		missing = 0
	}
	p.NotWatchedSeconds = int64(missing / time.Second)
	p.Whole = missing == 0 && !p.ShortenedByRetention
}

// durationTrend computes the trend of one operation from the durations of its succeeded runs in the recent period
// and in the baseline (each in any order; they are sorted here). points, when not nil, are the runs' durations for
// the detail, oldest first; the latest trendPoints are kept.
func durationTrend(op store.Operation, recentDurs, baselineDurs []time.Duration, recent, baseline DurationPeriod, cov *coverage, points []DurationPoint) DurationTrend {
	t := DurationTrend{Provenance: ProvDerived, Recent: recent, Baseline: baseline, MinRuns: trendMinRuns, LongerAt: trendLongerAt}
	t.Recent.fill(recentDurs)
	t.Baseline.fill(baselineDurs)
	t.Recent.cover(cov, op)
	t.Baseline.cover(cov, op)
	t.Enough = t.Recent.Runs >= trendMinRuns && t.Baseline.Runs >= trendMinRuns
	if t.Enough { // fill sorted both
		// The ratio of the exact medians, not of the rounded seconds.
		r := float64(nearestRank(recentDurs, 0.5)) / float64(nearestRank(baselineDurs, 0.5))
		r = math.Round(r*100) / 100
		t.Ratio = &r
		t.Longer = r >= trendLongerAt
	}
	if points != nil {
		t.Basis = trendBasis
		if len(points) > trendPoints {
			points = points[len(points)-trendPoints:]
		}
		t.Points = points
	}
	return t
}

// splitRuns divides one operation's runs (oldest first) into the recent period and the baseline by their start, with
// the per-run points of both.
func splitRuns(runs []store.RunDuration, recent, baseline DurationPeriod) (rd, bd []time.Duration, pts []DurationPoint) {
	pts = []DurationPoint{}
	for _, r := range runs {
		in := func(p DurationPeriod) bool { return !r.StartedAt.Before(p.From) && r.StartedAt.Before(p.To) }
		switch {
		case in(recent):
			rd = append(rd, r.Duration)
		case in(baseline):
			bd = append(bd, r.Duration)
		default:
			continue
		}
		pts = append(pts, DurationPoint{RunID: r.RunID, StartedAt: r.StartedAt, Seconds: *secondsOf(r.Duration), Ms: r.Duration.Milliseconds(), Recent: in(recent)})
	}
	return rd, bd, pts
}
