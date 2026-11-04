package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func get(t *testing.T, mux *http.ServeMux, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code, rec.Body.String()
}

func TestReadinessFollowsChecks(t *testing.T) {
	p := New()
	mux := http.NewServeMux()
	p.Register(mux)

	if code, _ := get(t, mux, "/readyz"); code != http.StatusOK {
		t.Fatalf("no checks: %d", code)
	}
	synced := errors.New("jobs not synced")
	p.Add("watch", func(context.Context) error { return synced })
	p.Add("storage", func(context.Context) error { return nil })
	code, body := get(t, mux, "/readyz")
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "watch: jobs not synced") || strings.Contains(body, "storage") {
		t.Fatalf("failing check: %d %q", code, body)
	}
	synced = nil
	p.Add("watch", func(context.Context) error { return synced })
	if code, _ := get(t, mux, "/readyz"); code != http.StatusOK {
		t.Fatalf("after sync: %d", code)
	}
	if code, body := get(t, mux, "/healthz"); code != http.StatusOK || body != "ok\n" {
		t.Fatalf("healthz: %d %q", code, body)
	}
}
