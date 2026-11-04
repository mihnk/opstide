package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func serve(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestServesTheAppForEveryRoute(t *testing.T) {
	h := handler(fstest.MapFS{
		"dist/index.html":         {Data: []byte("<!doctype html><div id=root></div>")},
		"dist/assets/index-ab.js": {Data: []byte("console.log(1)")},
	})
	for _, p := range []string{"/", "/log", "/operations/abc", "/system?tz=UTC"} {
		rec := serve(t, h, http.MethodGet, p)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "id=root") || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'self'") {
			t.Errorf("%s: %d %q", p, rec.Code, rec.Body.String())
		}
	}
	rec := serve(t, h, http.MethodGet, "/assets/index-ab.js")
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset: %d %v", rec.Code, rec.Header())
	}
	if rec := serve(t, h, http.MethodGet, "/assets/missing.js"); rec.Code != 404 {
		t.Fatalf("missing asset: %d", rec.Code)
	}
	if rec := serve(t, h, http.MethodPost, "/"); rec.Code != 405 {
		t.Fatalf("post: %d", rec.Code)
	}
}

func TestSaysWhenTheUIIsNotBuilt(t *testing.T) {
	h := handler(fstest.MapFS{"dist/.gitkeep": {}})
	rec := serve(t, h, http.MethodGet, "/")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "make web") {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
}
