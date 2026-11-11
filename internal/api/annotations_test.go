package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mihnk/opstide/internal/auth"
	"github.com/mihnk/opstide/internal/collector"
	"github.com/mihnk/opstide/internal/store"
)

// annotationServer is the api_test fixture's day (Istanbul, now 10:06, Jobs not watched 03:10–03:14, a failed run
// recorded 08:15) with a few changes and Kubernetes Events added. publicURL and token tune the server.
func annotationServer(t *testing.T, publicURL, token string) *httptest.Server {
	t.Helper()
	return annotationServerWith(t, publicURL, token, nil)
}

// annotationServerWith is annotationServer with more written to the record by extra.
func annotationServerWith(t *testing.T, publicURL, token string, extra func(w *store.Writer) error) *httptest.Server {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "opstide.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Update(ctx, func(w *store.Writer) error {
		for _, e := range []store.Event{
			{ID: "job.failed/r0800", Type: "job.failed", Namespace: "payments", Kind: "Job", ResourceName: "payment-reconciliation-x", OperationID: "recon", RunID: "r0800",
				OccurredAt: local(8, 15), ObservedAt: local(8, 15), Severity: "error", Confidence: store.Derived, Summary: "Payment reconciliation failed after 2 attempts"},
			{ID: "cronjob.schedule_changed/recon/1", Type: "cronjob.schedule_changed", Namespace: "payments", Kind: "CronJob", ResourceName: "payment-reconciliation", OperationID: "recon",
				OccurredAt: local(5, 0), ObservedAt: local(5, 0), Severity: "info", Confidence: store.Observed, Summary: "Schedule changed to */30 * * * *"},
			{ID: "deployment.spec_written/d-x/1", Type: "deployment.spec_written", Namespace: "shop", Kind: "Deployment", ResourceName: "api", WorkloadID: "d-x",
				OccurredAt: local(7, 0), ObservedAt: local(7, 0), Severity: "info", Confidence: store.Observed, Summary: "api spec written by helm"},
			{ID: "deployment.rollout/d-x/2", Type: "deployment.rollout", Namespace: "shop", Kind: "Deployment", ResourceName: "api", WorkloadID: "d-x",
				OccurredAt: local(7, 1), ObservedAt: local(7, 1), Severity: "info", Confidence: store.Derived, Summary: "not a change type: never drawn"},
			{ID: "pod.created/p1", Type: "pod.created", Namespace: "shop", Kind: "Pod", ResourceName: "api-1", WorkloadID: "d-x",
				OccurredAt: local(7, 2), ObservedAt: local(7, 2), Severity: "info", Confidence: store.Observed, Summary: "routine: never drawn"},
		} {
			if _, err := w.AddEvent(ctx, e); err != nil {
				return err
			}
		}
		warn := ev("w1", "uw1", "BackOff", "Warning", "Pod", "payment-reconciliation-x-abc", "pod-1", local(8, 10))
		warn.Namespace = "payments"
		normal := ev("n1", "un1", "Pulled", "Normal", "Pod", "payment-reconciliation-x-abc", "pod-1", local(8, 11))
		normal.Namespace = "payments"
		for _, e := range []store.Evidence{warn, normal} {
			if err := w.SaveEvidence(ctx, e); err != nil {
				return err
			}
		}
		if err := w.OpenInterval(ctx, "jobs", store.Healthy, local(0, 0).AddDate(0, 0, -8)); err != nil {
			return err
		}
		if err := w.CloseInterval(ctx, "jobs", local(3, 10), "watch expired"); err != nil {
			return err
		}
		if err := w.OpenInterval(ctx, "jobs", store.Healthy, local(3, 14)); err != nil {
			return err
		}
		if extra != nil {
			if err := extra(w); err != nil {
				return err
			}
		}
		return w.Heartbeat(ctx, local(10, 6))
	}); err != nil {
		t.Fatal(err)
	}
	s := New(db, watcher{problems: []collector.Problem{{Scope: "pods/kube-system", Err: "forbidden"}}},
		Config{Version: "test", Cluster: "c", Retention: store.Retention{Days: 30, MaxEvents: 2_000_000}, StartedAt: local(0, 0), PublicURL: publicURL, WatchConfigMaps: true}, nil)
	s.now = func() time.Time { return local(10, 6) }
	mux := http.NewServeMux()
	s.Register(mux)
	var h http.Handler = mux
	if token != "" {
		a := auth.New(token)
		a.Register(mux)
		h = a.Middleware(mux)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func msq(t time.Time) string { return strconv.FormatInt(t.UnixMilli(), 10) }

func annos(t *testing.T, srv *httptest.Server, q string) []Annotation {
	t.Helper()
	var out []Annotation
	if code := get(t, srv, "/api/v1/annotations?"+q, &out); code != 200 {
		t.Fatalf("%s: %d", q, code)
	}
	return out
}

func kindsOf(as []Annotation) []string {
	var out []string
	for _, a := range as {
		out = append(out, a.Kind+":"+a.Title)
	}
	return out
}

func TestAnnotationsKindsAndFilters(t *testing.T) {
	srv := annotationServer(t, "", "")
	day := "from=" + msq(local(0, 0)) + "&to=" + msq(local(10, 6))

	// Failures: the failed run and the Warning Event; not the Normal Event, not changes.
	fs := annos(t, srv, day+"&kinds=failures")
	if got := kindsOf(fs); !reflect.DeepEqual(got, []string{"failure:BackOff — Pod payment-reconciliation-x-abc", "failure:Payment reconciliation failed after 2 attempts"}) {
		t.Fatalf("failures %q", got)
	}
	f := fs[1]
	if f.Time != local(8, 15).UnixMilli() || f.TimeEnd != 0 || f.RecordID == "" || f.Provenance != ProvDerived || f.Namespace != "payments" ||
		!strings.HasPrefix(f.URL, "/explorer?") || !strings.Contains(f.URL, "rec="+f.RecordID) || !has(f.Tags, "opstide") || !has(f.Tags, "job.failed") || !has(f.Tags, "namespace:payments") || f.TagList != strings.Join(f.Tags, ",") {
		t.Fatalf("failed run %+v", f)
	}
	if strings.Contains(f.Text, "<a ") {
		t.Fatalf("no link in the text without a public URL: %s", f.Text)
	}

	// Changes: the schedule change and the spec write; a rollout entry and routine Pod entries are not changes.
	cs := annos(t, srv, day+"&kinds=changes")
	if got := kindsOf(cs); !reflect.DeepEqual(got, []string{"change:Schedule changed to */30 * * * *", "change:api spec written by helm"}) {
		t.Fatalf("changes %q", got)
	}

	// namespace and workload narrow the records.
	if got := kindsOf(annos(t, srv, day+"&kinds=changes,failures&namespace=shop")); !reflect.DeepEqual(got, []string{"change:api spec written by helm"}) {
		t.Fatalf("namespace %q", got)
	}
	if got := kindsOf(annos(t, srv, day+"&kinds=changes,failures&workload=d-x")); !reflect.DeepEqual(got, []string{"change:api spec written by helm"}) {
		t.Fatalf("workload %q", got)
	}

	// Everything, by time, and the same answer twice.
	all := annos(t, srv, day)
	for i := 1; i < len(all); i++ {
		if all[i].Time < all[i-1].Time {
			t.Fatalf("not by time: %q", kindsOf(all))
		}
	}
	if again := annos(t, srv, day+"&kinds=gaps,failures,changes"); !reflect.DeepEqual(all, again) {
		t.Fatalf("not deterministic:\n%q\n%q", kindsOf(all), kindsOf(again))
	}

	// RFC 3339 works as well as epoch milliseconds.
	rfc := annos(t, srv, "from="+local(0, 0).Format(time.RFC3339)+"&to="+local(10, 6).Format(time.RFC3339)+"&kinds=failures")
	if !reflect.DeepEqual(rfc, fs) {
		t.Fatalf("rfc3339 %q", kindsOf(rfc))
	}
}

func TestAnnotationsGapRegions(t *testing.T) {
	srv := annotationServer(t, "https://opstide.example.com", "")
	gs := annos(t, srv, "from="+msq(local(0, 0))+"&to="+msq(local(10, 6))+"&kinds=gaps&namespace=payments")
	if len(gs) != 1 {
		t.Fatalf("gaps %q", kindsOf(gs))
	}
	g := gs[0]
	if g.Kind != "gap" || g.Time != local(3, 10).UnixMilli() || g.TimeEnd != local(3, 14).UnixMilli() || g.Title != "OpsTide was not watching" ||
		!strings.Contains(g.Text, "Jobs") || !strings.Contains(g.Text, "not proof that nothing happened") || g.RecordID != "" {
		t.Fatalf("gap %+v", g)
	}
	if !strings.HasPrefix(g.URL, "https://opstide.example.com/explorer?") || !strings.Contains(g.URL, "group=observation") || !strings.Contains(g.Text, `<a href="https://opstide.example.com/explorer?`) {
		t.Fatalf("gap link %+v", g)
	}

	// A watch that cannot be read is not watched: the kube-system Pods are a region over the whole range up to now.
	all := annos(t, srv, "from="+msq(local(0, 0))+"&to="+msq(local(10, 6))+"&kinds=gaps")
	if len(all) != 1 || all[0].Time != local(0, 0).UnixMilli() || all[0].TimeEnd != local(10, 6).UnixMilli() ||
		!strings.Contains(all[0].Text, "Job Pods in kube-system") || !strings.Contains(all[0].Text, "Jobs") {
		t.Fatalf("all watches %+v", all)
	}

	// Before OpsTide first watched (8 days ago), the range is a region of its own.
	early := annos(t, srv, "from="+msq(local(0, 0).AddDate(0, 0, -10))+"&to="+msq(local(0, 0).AddDate(0, 0, -7))+"&kinds=gaps&namespace=payments")
	if len(early) != 1 || early[0].Title != "Before OpsTide first observed the cluster" || early[0].TimeEnd != local(0, 0).AddDate(0, 0, -8).UnixMilli() {
		t.Fatalf("before first %+v", early)
	}

	// The records link to the configured address too.
	fs := annos(t, srv, "from="+msq(local(0, 0))+"&to="+msq(local(10, 6))+"&kinds=failures")
	if !strings.HasPrefix(fs[0].URL, "https://opstide.example.com/explorer?") || !strings.Contains(fs[0].Text, "Open in OpsTide</a>") {
		t.Fatalf("record link %+v", fs[0])
	}
}

func TestAnnotationsLimitAndValidation(t *testing.T) {
	srv := annotationServer(t, "", "")
	day := "from=" + msq(local(0, 0)) + "&to=" + msq(local(10, 6))
	// The newest record is kept; a notice covers the part of the range whose records are left out.
	got := annos(t, srv, day+"&kinds=changes,failures&limit=1")
	if len(got) != 2 || got[0].Kind != "notice" || got[0].Time != local(0, 0).UnixMilli() || got[0].TimeEnd != local(8, 15).UnixMilli() ||
		got[1].Title != "Payment reconciliation failed after 2 attempts" {
		t.Fatalf("limit %q %+v", kindsOf(got), got)
	}
	if got := annos(t, srv, day+"&kinds=changes,failures&limit=4"); len(got) != 4 || got[0].Kind == "notice" {
		t.Fatalf("exactly at the limit there is no notice: %q", kindsOf(got))
	}
	for _, bad := range []string{
		"from=" + msq(local(10, 0)) + "&to=" + msq(local(9, 0)),
		"from=" + msq(local(0, 0).AddDate(0, 0, -32)) + "&to=" + msq(local(0, 0)),
		day + "&limit=0", day + "&limit=1001", day + "&limit=many",
		day + "&kinds=deploys", day + "&kind=changes", "to=" + msq(local(9, 0)), "from=yesterday",
		day + "&namespace=" + strings.Repeat("n", 254),
	} {
		if code := get(t, srv, "/api/v1/annotations?"+bad, nil); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	// 31 days is the most.
	if code := get(t, srv, "/api/v1/annotations?from="+msq(local(10, 6).AddDate(0, 0, -31))+"&to="+msq(local(10, 6)), nil); code != 200 {
		t.Errorf("31 days: %d", code)
	}
	// Without a range: the last 24 hours.
	if code := get(t, srv, "/api/v1/annotations", nil); code != 200 {
		t.Errorf("default range: %d", code)
	}
}

func TestAnnotationsNeedTheToken(t *testing.T) {
	const token = "0123456789abcdef0123"
	srv := annotationServer(t, "", token)
	resp, err := http.Get(srv.URL + "/api/v1/annotations")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without a token: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/annotations?kinds=failures&from="+msq(local(0, 0)), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out []Annotation
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != 200 || len(out) != 2 {
		t.Fatalf("with the token: %d %v %d", resp.StatusCode, err, len(out))
	}
}

// Summaries carry cluster-controlled strings; the title is escaped like the text.
func TestAnnotationsEscapeTitles(t *testing.T) {
	const evil = "<img src=x onerror=alert(1)>"
	ctx := context.Background()
	srv := annotationServerWith(t, "", "", func(w *store.Writer) error {
		if _, err := w.AddEvent(ctx, store.Event{ID: "deployment.spec_written/d-x/9", Type: "deployment.spec_written", Namespace: "shop", Kind: "Deployment", ResourceName: "api",
			WorkloadID: "d-x", OccurredAt: local(9, 0), ObservedAt: local(9, 0), Severity: "info", Confidence: store.Observed, Summary: "api spec written by " + evil}); err != nil {
			return err
		}
		e := ev("w9", "uw9", evil, "Warning", "Pod", "api-1", "pod-9", local(9, 1))
		e.Namespace = "shop"
		return w.SaveEvidence(ctx, e)
	})
	got := annos(t, srv, "from="+msq(local(8, 30))+"&to="+msq(local(9, 30))+"&kinds=changes,failures")
	if len(got) != 2 {
		t.Fatalf("records %q", kindsOf(got))
	}
	for _, a := range got {
		if strings.Contains(a.Title, "<img") || strings.Contains(a.Text, "<img") || !strings.Contains(a.Title, "&lt;img src=x onerror=alert(1)&gt;") {
			t.Fatalf("not escaped: %+v", a)
		}
	}
}

// A watch turned on after the range (ConfigMaps today, the range two days ago) is not watched over the whole range: a
// region, clipped to the range, so a quiet graph there is not read as calm. A watch turned off is not drawn.
func TestAnnotationsWatchNotYetOn(t *testing.T) {
	srv := annotationServerWith(t, "", "", func(w *store.Writer) error {
		return w.OpenInterval(context.Background(), "configmaps", store.Healthy, local(9, 0))
	})
	from, to := local(0, 0).AddDate(0, 0, -3), local(0, 0).AddDate(0, 0, -2)
	gs := annos(t, srv, "from="+msq(from)+"&to="+msq(to)+"&kinds=gaps&namespace=payments")
	if len(gs) != 1 || gs[0].Time != from.UnixMilli() || gs[0].TimeEnd != to.UnixMilli() || !strings.Contains(gs[0].Text, "ConfigMaps") ||
		strings.Contains(gs[0].Text, "Secrets") {
		t.Fatalf("not yet watched %+v", gs)
	}
	// Changes are drawn from ConfigMaps too: the region is there with them.
	if cs := annos(t, srv, "from="+msq(from)+"&to="+msq(to)+"&kinds=changes,gaps&namespace=payments"); len(cs) != 1 || cs[0].Kind != "gap" {
		t.Fatalf("with changes %+v", cs)
	}
}

func TestAnnotationsRejectEpochSeconds(t *testing.T) {
	srv := annotationServer(t, "", "")
	var body map[string]any
	if code := get(t, srv, "/api/v1/annotations?from=1762500000&to=1762586400", &body); code != 400 || !strings.Contains(fmt.Sprint(body), "looks like epoch seconds; send milliseconds") {
		t.Fatalf("seconds: %d %v", code, body)
	}
}

func TestAnnotationsCapGapRegions(t *testing.T) {
	ctx := context.Background()
	srv := annotationServerWith(t, "", "", func(w *store.Writer) error {
		// 600 one-minute stretches the CronJob watch was not on, the day before the fixture's.
		at := local(0, 0).AddDate(0, 0, -1)
		if err := w.OpenInterval(ctx, "cronjobs", store.Healthy, at); err != nil {
			return err
		}
		for i := 0; i < 600; i++ {
			if err := w.CloseInterval(ctx, "cronjobs", at.Add(time.Duration(2*i+1)*time.Minute), "flap"); err != nil {
				return err
			}
			if err := w.OpenInterval(ctx, "cronjobs", store.Healthy, at.Add(time.Duration(2*i+2)*time.Minute)); err != nil {
				return err
			}
		}
		return nil
	})
	from, to := local(0, 0).AddDate(0, 0, -1), local(0, 0)
	gs := annos(t, srv, "from="+msq(from)+"&to="+msq(to)+"&kinds=gaps&namespace=payments")
	if len(gs) != maxGapRegions || gs[len(gs)-1].Kind != "notice" || gs[len(gs)-1].TimeEnd != to.UnixMilli() {
		t.Fatalf("capped: %d %+v", len(gs), gs[len(gs)-1])
	}
}
