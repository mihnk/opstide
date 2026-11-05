package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func scrape(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain; version=0.0.4") {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	return rec.Body.String()
}

func TestWritesTheTextFormat(t *testing.T) {
	var r Registry
	r.Add(func(_ context.Context, w *Writer) {
		w.Gauge("opstide_ready", "1 when ready.", 1)
		w.Gauge("opstide_watch_failing", "A watch that fails.", 1, "scope", `pods/"kube-system"`+"\n")
		w.Gauge("opstide_watch_failing", "A watch that fails.", 1, "scope", "jobs")
		w.Counter("opstide_items_total", "Items.", 12)
	})
	got := scrape(t, r.Handler())
	want := []string{
		"# HELP opstide_ready 1 when ready.\n# TYPE opstide_ready gauge\nopstide_ready 1\n",
		`opstide_watch_failing{scope="pods/\"kube-system\"\n"} 1` + "\n",
		"opstide_watch_failing{scope=\"jobs\"} 1\n",
		"# TYPE opstide_items_total counter\nopstide_items_total 12\n",
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in\n%s", w, got)
		}
	}
	if strings.Count(got, "# TYPE opstide_watch_failing") != 1 {
		t.Errorf("TYPE written more than once:\n%s", got)
	}
}

func TestCountsRequestsByClassAndCode(t *testing.T) {
	var r Registry
	h := r.Instrument(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/v1/missing":
			http.NotFound(w, req)
		case "/healthz":
			// no explicit status: 200
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	for _, p := range []string{"/api/v1/day", "/api/v1/missing", "/api/v1/missing", "/healthz", "/anything/at/all/1", "/anything/at/all/2"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, p, nil))
	}
	got := scrape(t, r.Handler())
	for _, w := range []string{
		`opstide_http_requests_total{class="api",code="404"} 2`,
		`opstide_http_requests_total{class="api",code="418"} 1`,
		`opstide_http_requests_total{class="probe",code="200"} 1`,
		`opstide_http_requests_total{class="ui",code="418"} 2`,
	} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in\n%s", w, got)
		}
	}
	// Labels never carry the path, so a scanner cannot make up series.
	if strings.Contains(got, "anything") {
		t.Fatalf("path in labels:\n%s", got)
	}
}

func TestInstrumentKeepsFlushingForEventStreams(t *testing.T) {
	var r Registry
	var flushed bool
	h := r.Instrument(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flushed = http.NewResponseController(w).Flush() == nil
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/stream", nil))
	if !flushed {
		t.Fatal("the wrapper hides Flush")
	}
}

func TestOnlyGETIsServed(t *testing.T) {
	var r Registry
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/metrics", nil))
	if rec.Code != 405 {
		t.Fatal(rec.Code)
	}
}
