package api

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mihnk/opstide/internal/maintenance"
)

func windows(t *testing.T, specs ...maintenance.Spec) *maintenance.Set {
	t.Helper()
	s, err := maintenance.New(specs)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The history fixture with a one-off window over the payments streak (08:00 to 09:05): the streak is still an
// exception, marked and listed after the ones outside the window; the 10:30 failure is not marked; a window for
// another namespace marks nothing.
func TestRangeMarksExceptionsInsideMaintenanceAndListsThemLast(t *testing.T) {
	srv, s := history(t)
	s.cfg.Maintenance = windows(t,
		maintenance.Spec{Name: "ledger migration", Start: at(8, 0).Format(time.RFC3339), End: at(9, 5).Format(time.RFC3339), Namespaces: []string{"payments"}, Note: "planned"},
		maintenance.Spec{Name: "data only", Start: at(10, 0).Format(time.RFC3339), End: at(11, 0).Format(time.RFC3339), Namespaces: []string{"data"}},
	)
	var d Range
	if code := get(t, srv, "/api/v1/range?within=12h&tz=UTC", &d); code != 200 {
		t.Fatal(code)
	}
	if len(d.Maintenance) != 2 || d.Maintenance[0].Name != "ledger migration" || !d.Maintenance[0].From.Equal(at(8, 0)) {
		t.Fatalf("spans %+v", d.Maintenance)
	}
	var streak, single *Exception
	inside := -1
	for i, e := range d.Exceptions {
		if e.Maintenance != nil && inside < 0 {
			inside = i
		}
		if e.Maintenance == nil && inside >= 0 {
			t.Fatalf("an exception outside maintenance comes after one inside: %+v", d.Exceptions)
		}
		if e.OperationID == "pay" && e.Kind == "failing" {
			streak = &d.Exceptions[i]
		}
		if e.OperationID == "pay" && e.Kind == "failed" {
			single = &d.Exceptions[i]
		}
	}
	if streak == nil || streak.Maintenance == nil || streak.Maintenance.Name != "ledger migration" || streak.Maintenance.Provenance != ProvDerived ||
		streak.Maintenance.Statement != "inside maintenance window ledger migration" || streak.Namespace != "payments" {
		t.Fatalf("streak %+v", streak)
	}
	if single == nil || single.Maintenance != nil {
		t.Fatalf("the 10:30 failure is outside: %+v", single)
	}
	// The counts are not touched: nothing is hidden.
	if d.Counts.Failed == 0 {
		t.Fatal("failures still counted")
	}
	for _, l := range d.Lanes {
		if l.OperationID != "pay" {
			continue
		}
		for _, m := range l.Marks {
			want := ""
			if !m.Start.Before(at(8, 0)) && m.Start.Before(at(9, 5)) {
				want = "ledger migration"
			}
			if m.Maintenance != want {
				t.Fatalf("mark at %v: %q", m.Start, m.Maintenance)
			}
		}
	}
}

func TestSchedulesSayWhatHappenedDuringMaintenance(t *testing.T) {
	srv, s := history(t)
	s.cfg.Maintenance = windows(t, maintenance.Spec{Name: "all morning", Start: at(6, 0).Format(time.RFC3339), End: at(12, 0).Format(time.RFC3339), Namespaces: []string{"payments"}})
	var ops Operations
	if code := get(t, srv, "/api/v1/operations?tz=UTC", &ops); code != 200 {
		t.Fatal(code)
	}
	for _, o := range ops.Operations {
		if o.ID != "pay" {
			continue
		}
		joined := strings.Join(o.Attention, "|")
		if !strings.Contains(joined, "failed during maintenance in the last 24 h") || !strings.Contains(joined, "not confirmed during maintenance") {
			t.Fatalf("attention %q", joined)
		}
		if o.Maintenance == nil || o.Maintenance.Name != "all morning" {
			t.Fatalf("every reason was inside the window: %+v", o.Maintenance)
		}
		return
	}
	t.Fatal("no pay row")
}

// A window over the api Pod's restart (at+35m to at+45m in shop): the restart and the BackOff Event are marked, the
// rest are not; "outside maintenance only" leaves those two out of the list and nothing else; the drilldown, the
// "before" answer and Grafana annotations carry the window.
func TestExplorerMarksRecordsAndCanListOnlyThoseOutside(t *testing.T) {
	from := base.Add(-6*time.Hour + 35*time.Minute)
	set := windows(t, maintenance.Spec{Name: "api upgrade", Start: from.Format(time.RFC3339), End: from.Add(10 * time.Minute).Format(time.RFC3339), Namespaces: []string{"shop"}})
	srv := explorerServer(t, func(c *Config) { c.Maintenance = set })
	var l ExplorerList
	if code := get(t, srv, "/api/v1/explorer?range=24h&routine=1", &l); code != 200 {
		t.Fatal(code)
	}
	marked := map[string]bool{}
	var restartID string
	for _, r := range l.Records {
		if r.Maintenance != nil {
			marked[r.Summary] = true
			if r.Maintenance.Name != "api upgrade" || r.Maintenance.Provenance != ProvDerived {
				t.Fatalf("%+v", r.Maintenance)
			}
		}
		if r.Summary == "a4" {
			restartID = r.ID
		}
	}
	if len(marked) != 2 || !marked["a4"] || !marked["BackOff — Pod api-1"] {
		t.Fatalf("marked %v", marked)
	}
	var out ExplorerList
	if code := get(t, srv, "/api/v1/explorer?range=24h&routine=1&maintenance=outside", &out); code != 200 {
		t.Fatal(code)
	}
	if len(out.Records) != len(l.Records)-2 {
		t.Fatalf("outside only: %d of %d", len(out.Records), len(l.Records))
	}
	for _, r := range out.Records {
		if r.Maintenance != nil {
			t.Fatalf("%s is inside", r.ID)
		}
	}
	if code := get(t, srv, "/api/v1/explorer?range=24h&maintenance=inside", nil); code != 400 {
		t.Fatalf("maintenance takes outside: %d", code)
	}
	var d ExplorerDetail
	if code := get(t, srv, "/api/v1/explorer/records/"+url.PathEscape(restartID), &d); code != 200 || d.Record.Maintenance == nil {
		t.Fatalf("detail %d %+v", code, d.Record.Maintenance)
	}
	var b ExplorerBefore
	if code := get(t, srv, "/api/v1/explorer/records/"+url.PathEscape(restartID)+"/before?window=1h", &b); code != 200 {
		t.Fatal(code)
	}
	if b.Maintenance == nil || !strings.Contains(b.MaintenanceNote, "api upgrade") || len(b.MaintenanceWindows) != 1 {
		t.Fatalf("before %+v %q %+v", b.Maintenance, b.MaintenanceNote, b.MaintenanceWindows)
	}

	q := "from=" + msq(base.Add(-24*time.Hour)) + "&to=" + msq(base)
	only := annos(t, srv, q+"&kinds=maintenance")
	if len(only) != 1 || only[0].Kind != "maintenance" || only[0].Time != from.UnixMilli() || only[0].TimeEnd != from.Add(10*time.Minute).UnixMilli() ||
		!strings.Contains(only[0].TagList, "maintenance:api upgrade") || only[0].Title != "Maintenance: api upgrade" {
		t.Fatalf("%+v", only)
	}
	all := annos(t, srv, q)
	kinds := map[string]int{}
	tagged := false
	for _, a := range all {
		kinds[a.Kind]++
		if a.Kind == "failure" && strings.Contains(a.TagList, "maintenance:api upgrade") {
			tagged = true
		}
	}
	if kinds["maintenance"] != 1 || !tagged {
		t.Fatalf("default kinds include maintenance, and a failure inside is tagged: %v %v", kinds, tagged)
	}
	if code := get(t, srv, "/api/v1/annotations?kinds=windows", nil); code != 400 {
		t.Fatal("unknown kind")
	}
	// A namespace the window does not cover draws no region.
	if other := annos(t, srv, q+"&kinds=maintenance&namespace=data"); len(other) != 0 {
		t.Fatalf("%+v", other)
	}
}

func TestSystemListsWindowsAndTheirNextOccurrences(t *testing.T) {
	srv, s := history(t)
	s.cfg.Maintenance = windows(t,
		maintenance.Spec{Name: "nightly", Cron: "0 11 * * *", Duration: "2h", TimeZone: "UTC"},
		maintenance.Spec{Name: "once", Start: "2026-12-01T00:00:00Z", End: "2026-12-01T01:00:00Z", Namespaces: []string{"data"}, Note: "db"},
	)
	var sys System
	if code := get(t, srv, "/api/v1/system?tz=UTC", &sys); code != 200 {
		t.Fatal(code)
	}
	ws := sys.Maintenance.Windows
	if len(ws) != 2 || ws[0].Kind != "recurring" || !ws[0].Active || len(ws[0].Next) != 3 || !ws[0].Next[0].From.Equal(at(11, 0)) || ws[0].Duration != "2h0m0s" {
		t.Fatalf("%+v", ws)
	}
	if ws[1].Kind != "one-off" || ws[1].Active || len(ws[1].Next) != 1 || ws[1].Start == nil || ws[1].Namespaces[0] != "data" {
		t.Fatalf("%+v", ws[1])
	}
}
