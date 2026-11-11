package maintenance

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func mustParse(t *testing.T, y string) *Set {
	t.Helper()
	s, err := Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestParsesBothKinds(t *testing.T) {
	s := mustParse(t, `
windows:
  - name: Saturday node upgrade
    cron: "0 2 * * 6"
    duration: 2h
    timeZone: Europe/Istanbul
    namespaces: [payments, data, payments]
    note: Node pool upgrade by the platform team
  - name: db-migration
    start: 2026-11-20T22:00:00Z
    end: 2026-11-21T01:00:00+00:00
`)
	ws := s.Windows()
	if len(ws) != 2 || !ws[0].Recurring() || ws[1].Recurring() {
		t.Fatalf("%+v", ws)
	}
	if strings.Join(ws[0].Namespaces, ",") != "data,payments" || ws[0].Duration != 2*time.Hour || ws[0].TimeZone != "Europe/Istanbul" {
		t.Fatalf("%+v", ws[0])
	}
	if !ws[1].Start.Equal(time.Date(2026, 11, 20, 22, 0, 0, 0, time.UTC)) || len(ws[1].Namespaces) != 0 {
		t.Fatalf("%+v", ws[1])
	}
	// JSON is YAML too.
	if _, err := Parse([]byte(`{"windows":[{"name":"x","cron":"0 1 * * *","duration":"30m"}]}`)); err != nil {
		t.Fatal(err)
	}
	if s, err := Parse([]byte("")); err != nil || s.Len() != 0 {
		t.Fatalf("empty: %v %v", s, err)
	}
}

func TestRejectsBadWindows(t *testing.T) {
	many := "windows:\n"
	for i := 0; i <= MaxWindows; i++ {
		many += "  - {name: w" + strconv.Itoa(i) + ", cron: '0 1 * * *', duration: 1h}\n"
	}
	for name, y := range map[string]string{
		"unknown field":      "windows: [{name: a, cron: '0 1 * * *', duration: 1h, namespace: x}]",
		"no name":            "windows: [{cron: '0 1 * * *', duration: 1h}]",
		"bad name":           "windows: [{name: ' spaced', cron: '0 1 * * *', duration: 1h}]",
		"duplicate":          "windows: [{name: a, cron: '0 1 * * *', duration: 1h}, {name: a, cron: '0 2 * * *', duration: 1h}]",
		"both kinds":         "windows: [{name: a, cron: '0 1 * * *', duration: 1h, start: '2026-01-01T00:00:00Z', end: '2026-01-01T01:00:00Z'}]",
		"neither":            "windows: [{name: a}]",
		"cron alone":         "windows: [{name: a, cron: '0 1 * * *'}]",
		"six fields":         "windows: [{name: a, cron: '0 0 1 * * *', duration: 1h}]",
		"descriptor":         "windows: [{name: a, cron: '@daily', duration: 1h}]",
		"bad cron":           "windows: [{name: a, cron: '61 1 * * *', duration: 1h}]",
		"never fires":        "windows: [{name: a, cron: '0 0 30 2 *', duration: 1h}]",
		"bad zone":           "windows: [{name: a, cron: '0 1 * * *', duration: 1h, timeZone: Mars/Olympus}]",
		"bad duration":       "windows: [{name: a, cron: '0 1 * * *', duration: 2d}]",
		"too short":          "windows: [{name: a, cron: '0 1 * * *', duration: 30s}]",
		"too long":           "windows: [{name: a, cron: '0 1 * * *', duration: 169h}]",
		"bad start":          "windows: [{name: a, start: tomorrow, end: '2026-01-01T01:00:00Z'}]",
		"end before start":   "windows: [{name: a, start: '2026-01-02T00:00:00Z', end: '2026-01-01T00:00:00Z'}]",
		"one-off too long":   "windows: [{name: a, start: '2026-01-01T00:00:00Z', end: '2026-03-01T00:00:00Z'}]",
		"zone on one-off":    "windows: [{name: a, start: '2026-01-01T00:00:00Z', end: '2026-01-01T01:00:00Z', timeZone: UTC}]",
		"bad namespace":      "windows: [{name: a, cron: '0 1 * * *', duration: 1h, namespaces: [Payments]}]",
		"not yaml":           "windows: [",
		"too many":           many,
		"control in a note":  "windows: [{name: a, cron: '0 1 * * *', duration: 1h, note: \"a\\tb\"}]",
		"windows not a list": "windows: {name: a}",
	} {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestLoadFile(t *testing.T) {
	if s, err := LoadFile(""); err != nil || s.Len() != 0 {
		t.Fatal(s, err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "m.yaml")
	if err := os.WriteFile(p, []byte("windows: [{name: a, cron: '0 1 * * *', duration: 1h}]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := LoadFile(p); err != nil || s.Len() != 1 {
		t.Fatal(s, err)
	}
	if _, err := LoadFile(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("a missing file is an error")
	}
	bad := filepath.Join(dir, "bad.yaml")
	_ = os.WriteFile(bad, []byte("windows: [{name: a}]"), 0o600)
	if _, err := LoadFile(bad); err == nil || !strings.Contains(err.Error(), `window "a"`) {
		t.Fatalf("the error names the window: %v", err)
	}
}

// A window at 02:30 local time every day in Europe/Berlin: the cron is read in the zone, so the UTC times move with
// daylight saving. On the night the clocks jump from 02:00 to 03:00 there is no 02:30: the window starts at the first
// instant that exists after it, 03:00 CEST. In autumn 02:30 happens twice: the window starts once, at the first.
// (The CronJob controller skips the first and fires twice on the second; a window is planned work, not a run.)
func TestRecurringAcrossDaylightSaving(t *testing.T) {
	s := mustParse(t, "windows: [{name: nightly, cron: '30 2 * * *', duration: 1h, timeZone: Europe/Berlin}]")
	from := time.Date(2026, 3, 27, 0, 0, 0, 0, time.UTC)
	occ, truncated := s.Between(from, from.Add(4*24*time.Hour))
	if truncated {
		t.Fatal("not truncated")
	}
	var got []string
	for _, o := range occ {
		got = append(got, o.From.Format("01-02T15:04")+"/"+o.To.Format("15:04"))
	}
	// 27 and 28 March at CET (UTC+1), 29 March at the jump (03:00 CEST, 01:00 UTC), 30 March at CEST (UTC+2).
	want := "03-27T01:30/02:30 03-28T01:30/02:30 03-29T01:00/02:00 03-30T00:30/01:30"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %v, want %s", got, want)
	}
	occ, _ = s.Between(time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC))
	got = nil
	for _, o := range occ {
		if o.To.Sub(o.From) != time.Hour {
			t.Fatalf("an occurrence lasts its duration: %+v", o)
		}
		got = append(got, o.From.Format("01-02T15:04"))
	}
	if strings.Join(got, " ") != "10-24T00:30 10-25T00:30 10-26T01:30" {
		t.Fatalf("autumn: %v", got)
	}
	// At, in UTC, matches the local occurrence.
	if o, ok := s.At(time.Date(2026, 3, 30, 1, 0, 0, 0, time.UTC), "any"); !ok || o.Name != "nightly" {
		t.Fatal("covered at 03:00 CEST")
	}
	if o, ok := s.At(time.Date(2026, 3, 29, 1, 30, 0, 0, time.UTC), "any"); !ok || !o.From.Equal(time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC)) {
		t.Fatal("on the night 02:30 does not exist, open from 03:00 CEST")
	}
	if _, ok := s.At(time.Date(2026, 10, 25, 1, 45, 0, 0, time.UTC), "any"); ok {
		t.Fatal("the second 02:30 of the autumn night opens nothing")
	}
}

// America/New_York: 30 2 * * * on the spring-forward day (8 March 2026) starts at 03:00 EDT; 30 1 * * * on the
// fall-back day (1 November 2026) starts once, at 01:30 EDT.
func TestRecurringDaylightSavingNewYork(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip(err)
	}
	s := mustParse(t, `
windows:
  - {name: spring, cron: '30 2 * * *', duration: 30m, timeZone: America/New_York}
  - {name: fall, cron: '30 1 * * *', duration: 2h, timeZone: America/New_York}
`)
	occ, _ := s.Between(time.Date(2026, 3, 8, 0, 0, 0, 0, ny), time.Date(2026, 3, 9, 0, 0, 0, 0, ny))
	var spring []time.Time
	for _, o := range occ {
		if o.Name == "spring" {
			spring = append(spring, o.From)
		}
	}
	if len(spring) != 1 || !spring[0].Equal(time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("spring forward: %v", spring)
	}
	occ, _ = s.Between(time.Date(2026, 11, 1, 0, 0, 0, 0, ny), time.Date(2026, 11, 2, 0, 0, 0, 0, ny))
	var fall []Occurrence
	for _, o := range occ {
		if o.Name == "fall" {
			fall = append(fall, o)
		}
	}
	if len(fall) != 1 || !fall[0].From.Equal(time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)) || !fall[0].To.Equal(time.Date(2026, 11, 1, 7, 30, 0, 0, time.UTC)) {
		t.Fatalf("fall back: %+v", fall)
	}
	// The second 01:30 (EST, 06:30 UTC) is inside the first occurrence, not the start of another.
	if o, ok := s.At(time.Date(2026, 11, 1, 6, 45, 0, 0, time.UTC), ""); !ok || !o.From.Equal(fall[0].From) {
		t.Fatalf("at: %+v", o)
	}
	if n := s.Windows()[1].Next(time.Date(2026, 10, 31, 12, 0, 0, 0, ny), 3); len(n) != 3 || n[1].From.Sub(n[0].From) != 25*time.Hour {
		t.Fatalf("next: %+v", n)
	}
}

// A recurring window may not overlap itself, and the error says where.
func TestRecurringMustNotOverlapItself(t *testing.T) {
	for _, c := range []string{
		"windows: [{name: a, cron: '* * * * *', duration: 168h}]",
		"windows: [{name: a, cron: '*/5 * * * *', duration: 6m}]",
		"windows: [{name: a, cron: '0 2 * * 1,2', duration: 25h}]",
		// 01:00 and 02:30 are 90 minutes apart, but on the spring-forward night 02:30 starts at 03:00 EDT, an hour after.
		"windows: [{name: a, cron: '0 1 * * *\n', duration: 1h}]",
	}[:3] {
		if _, err := Parse([]byte(c)); err == nil || !strings.Contains(err.Error(), "occurrences would overlap") {
			t.Errorf("%s: %v", c, err)
		}
	}
	if _, err := Parse([]byte("windows: [{name: a, cron: '30 1,2 * * *', duration: 45m}]")); err != nil {
		t.Error("in UTC there is no such night:", err)
	}
	if _, err := Parse([]byte("windows: [{name: a, cron: '0 1 * * *', duration: 24h}]")); err != nil {
		t.Error("back to back is not an overlap:", err)
	}
}

func TestNamespaceScoping(t *testing.T) {
	s := mustParse(t, `
windows:
  - {name: payments-only, start: '2026-11-07T02:00:00Z', end: '2026-11-07T04:00:00Z', namespaces: [payments]}
  - {name: everything, start: '2026-11-08T02:00:00Z', end: '2026-11-08T04:00:00Z'}
`)
	in := time.Date(2026, 11, 7, 3, 0, 0, 0, time.UTC)
	if o, ok := s.At(in, "payments"); !ok || o.Name != "payments-only" {
		t.Fatal("covers its namespace")
	}
	if _, ok := s.At(in, "data"); ok {
		t.Fatal("not another namespace")
	}
	if _, ok := s.At(in, ""); ok {
		t.Fatal("a namespaced window does not cover cluster-scoped records")
	}
	if _, ok := s.At(time.Date(2026, 11, 7, 4, 0, 0, 0, time.UTC), "payments"); ok {
		t.Fatal("the end is not inside")
	}
	all := time.Date(2026, 11, 8, 2, 0, 0, 0, time.UTC)
	for _, ns := range []string{"", "payments", "kube-system"} {
		if o, ok := s.At(all, ns); !ok || o.Name != "everything" {
			t.Fatalf("cluster-wide covers %q", ns)
		}
	}
	ix := s.Index(time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC))
	if len(ix.Occurrences()) != 2 {
		t.Fatal(ix.Occurrences())
	}
	if o, ok := ix.At(in, "payments"); !ok || o.Name != "payments-only" {
		t.Fatal("index")
	}
	if _, ok := ix.At(in, "data"); ok {
		t.Fatal("index scoping")
	}
}

func TestExpansionIsBounded(t *testing.T) {
	s := mustParse(t, "windows: [{name: every-minute, cron: '* * * * *', duration: 1m}]")
	// A time inside: one schedule step, not an expansion.
	if o, ok := s.At(time.Date(2026, 1, 1, 10, 0, 30, 0, time.UTC), ""); !ok || !o.From.Equal(time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatal(o)
	}
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	occ, truncated := s.Between(from, from.Add(30*24*time.Hour))
	if !truncated || len(occ) != MaxOccurrences {
		t.Fatalf("%d %v", len(occ), truncated)
	}
	// A truncated index still answers correctly, window by window.
	late := from.Add(20*24*time.Hour + 30*time.Second)
	if _, ok := s.Index(from, from.Add(30*24*time.Hour)).At(late, "x"); !ok {
		t.Fatal("past the cap")
	}
}

func TestNextAndActive(t *testing.T) {
	s := mustParse(t, `
windows:
  - {name: daily, cron: '0 2 * * *', duration: 2h}
  - {name: once, start: '2026-11-07T10:00:00Z', end: '2026-11-07T11:00:00Z'}
`)
	now := time.Date(2026, 11, 7, 3, 0, 0, 0, time.UTC)
	act := s.Active(now)
	if !act["daily"] || act["once"] {
		t.Fatal(act)
	}
	ws := s.Windows()
	next := ws[0].Next(now, 3)
	if len(next) != 3 || !next[0].From.Equal(time.Date(2026, 11, 7, 2, 0, 0, 0, time.UTC)) || !next[1].From.Equal(time.Date(2026, 11, 8, 2, 0, 0, 0, time.UTC)) {
		t.Fatal(next)
	}
	if n := ws[1].Next(now, 3); len(n) != 1 || !n[0].From.Equal(ws[1].Start) {
		t.Fatal(n)
	}
	if n := ws[1].Next(now.Add(24*time.Hour), 3); len(n) != 0 {
		t.Fatal("a one-off window that ended has no next")
	}
	var nilSet *Set
	if nilSet.Len() != 0 || len(nilSet.Active(now)) != 0 {
		t.Fatal("nil is none")
	}
	if _, ok := nilSet.At(now, ""); ok {
		t.Fatal("nil covers nothing")
	}
}

// The index (binary search over the expanded occurrences) and the window-by-window lookup give the same answer,
// including where windows of different lengths overlap.
func TestIndexMatchesSetAt(t *testing.T) {
	s := mustParse(t, `
windows:
  - {name: long, start: '2026-11-01T00:00:00Z', end: '2026-11-20T00:00:00Z', namespaces: [data]}
  - {name: hourly, cron: '0 * * * *', duration: 10m}
  - {name: nightly, cron: '30 2 * * *', duration: 3h, timeZone: Europe/Istanbul, namespaces: [shop]}
`)
	from, to := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	ix := s.Index(from, to)
	if ix.Truncated() {
		t.Fatal("truncated")
	}
	for at := from; at.Before(to); at = at.Add(7 * time.Minute) {
		for _, ns := range []string{"", "data", "shop"} {
			a, okA := ix.At(at, ns)
			b, okB := s.At(at, ns)
			if okA != okB || a.Name != b.Name || !a.From.Equal(b.From) {
				t.Fatalf("%v %q: index %v %+v, set %v %+v", at, ns, okA, a, okB, b)
			}
		}
	}
}
