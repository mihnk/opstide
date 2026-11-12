package api

import (
	"testing"
	"time"

	"github.com/mihnk/opstide/internal/store"
)

func TestExplorerHistogramCountsWhatTheListShowsPerStretch(t *testing.T) {
	srv := explorerServer(t, nil)
	var h ExplorerHistogram
	if code := get(t, srv, "/api/v1/explorer/histogram?range=24h", &h); code != 200 {
		t.Fatal(code)
	}
	if h.BucketSeconds != 3600 || len(h.Buckets) < 24 || len(h.Buckets) > 25 {
		t.Fatalf("24 hourly buckets: %d × %ds", len(h.Buckets), h.BucketSeconds)
	}
	var l ExplorerList
	get(t, srv, "/api/v1/explorer?range=24h&limit=500", &l)
	if h.Total != len(l.Records) {
		t.Fatalf("the chart counts %d, the list shows %d", h.Total, len(l.Records))
	}
	sum := map[string]int{}
	for i, b := range h.Buckets {
		if i > 0 && b.Start.Sub(h.Buckets[i-1].Start) != time.Hour {
			t.Fatalf("buckets are contiguous: %v", h.Buckets)
		}
		sum["warning"] += b.Warning
		sum["error"] += b.Error
	}
	// The two Warning Events; the Normal one (Pulled) and the Pod creation are routine and left out as in the list.
	if sum["warning"] != 2 || sum["error"] != 0 {
		t.Fatalf("%v", sum)
	}
	if h.Buckets[0].Start.After(h.From) || !h.Buckets[len(h.Buckets)-1].Start.Before(h.To) {
		t.Fatalf("the buckets cover the range: %v .. %v for %v .. %v", h.Buckets[0].Start, h.Buckets[len(h.Buckets)-1].Start, h.From, h.To)
	}
	// Routine records and other filters follow the list.
	var all ExplorerHistogram
	get(t, srv, "/api/v1/explorer/histogram?range=24h&routine=1", &all)
	if all.Total != h.Total+2 {
		t.Fatalf("routine=1 adds the two routine records: %d vs %d", all.Total, h.Total)
	}
	var ev ExplorerHistogram
	get(t, srv, "/api/v1/explorer/histogram?range=24h&group=events", &ev)
	if ev.Total != 2 {
		t.Fatalf("group=events: %d", ev.Total)
	}
	// The reader's clock: in a +05:30 zone an hour starts at :30 UTC.
	var in ExplorerHistogram
	get(t, srv, "/api/v1/explorer/histogram?range=24h&tz=Asia/Kolkata", &in)
	if m := in.Buckets[1].Start.Minute(); m != 30 {
		t.Fatalf("buckets on the reader's hour: %v", in.Buckets[1].Start)
	}
	// Short ranges get short buckets; never more than 40.
	for rg, want := range map[string]int{"15m": 60, "1h": 120, "6h": 600, "7d": 6 * 3600} {
		var x ExplorerHistogram
		get(t, srv, "/api/v1/explorer/histogram?range="+rg, &x)
		if x.BucketSeconds != want || len(x.Buckets) > 41 {
			t.Fatalf("%s: %ds × %d", rg, x.BucketSeconds, len(x.Buckets))
		}
	}
	if code := get(t, srv, "/api/v1/explorer/histogram?range=24h&tz=Nowhere/Else", &in); code != 400 {
		t.Fatalf("an unknown zone is refused: %d", code)
	}
	if code := get(t, srv, "/api/v1/explorer/histogram?range=24h&bogus=1", &in); code != 400 {
		t.Fatalf("an unknown parameter is refused: %d", code)
	}
}

func TestExplorerFilterChoicesCountSeverityAndOutcomes(t *testing.T) {
	srv := explorerServer(t, nil)
	var f ExplorerFilters
	if code := get(t, srv, "/api/v1/explorer/filters?range=24h&severity=warning", &f); code != 200 {
		t.Fatal(code)
	}
	sev := map[string]int{}
	for _, c := range f.Severity {
		sev[c.Value] = c.Count
	}
	// Severity leaves out its own filter: every record of the range counts under one.
	if sev["warning"] != 2 || sev["info"] != 5 || sev["error"] != 0 {
		t.Fatalf("%v", sev)
	}
	if len(f.Outcomes) != 3 || f.Outcomes[0].Value != "failed" || len(f.Owners) != 0 {
		t.Fatalf("%+v %+v", f.Outcomes, f.Owners)
	}
}

func TestExplorerGapRecordCarriesItsStretch(t *testing.T) {
	from := base.Add(-3 * time.Hour)
	to := from.Add(20 * time.Minute)
	e := store.Event{ID: "g1", Type: "collection.gap", Kind: "events", ResourceName: "events", OccurredAt: to, ObservedAt: to, Severity: "warning", Confidence: store.Observed,
		Summary: "Watching events again", Facts: map[string]string{"from": from.Format(time.RFC3339), "to": to.Format(time.RFC3339), "scope": "events"}}
	g := gapOf(e)
	if g == nil || !g.From.Equal(from) || !g.To.Equal(to) || g.Scope != "events" {
		t.Fatalf("%+v", g)
	}
	e.Facts["to"] = "not a time"
	if gapOf(e) != nil {
		t.Fatal("a stretch the facts do not give is not made up")
	}
	e.Type = "pod.created"
	if gapOf(e) != nil {
		t.Fatal("only gap records carry one")
	}
}
