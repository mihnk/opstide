// Package metrics serves OpsTide's own health in the Prometheus text format
// at /metrics: whether it is ready, how fresh its record is, which watches
// fail, how big the database is, and how often it is asked. It says nothing
// about the cluster it watches; that is what the record is for.
package metrics

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Writer writes metric families; HELP and TYPE are written once per name,
// so series of one family must be written one after another.
type Writer struct {
	w    io.Writer
	seen map[string]bool
}

func (w *Writer) series(kind, name, help string, v float64, labels []string) {
	if !w.seen[name] {
		w.seen[name] = true
		fmt.Fprintf(w.w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
	}
	fmt.Fprintf(w.w, "%s%s %s\n", name, labelText(labels), strconv.FormatFloat(v, 'g', -1, 64))
}

// Gauge writes a value that can go up and down. labels are name, value pairs.
func (w *Writer) Gauge(name, help string, v float64, labels ...string) {
	w.series("gauge", name, help, v, labels)
}

// Counter writes a value that only goes up.
func (w *Writer) Counter(name, help string, v float64, labels ...string) {
	w.series("counter", name, help, v, labels)
}

func labelText(kv []string) string {
	if len(kv) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i+1 < len(kv); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		v := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(kv[i+1])
		fmt.Fprintf(&b, `%s="%s"`, kv[i], v)
	}
	b.WriteByte('}')
	return b.String()
}

// Source writes some metrics when /metrics is scraped.
type Source func(ctx context.Context, w *Writer)

// Registry holds the sources and counts the HTTP requests.
type Registry struct {
	mu      sync.Mutex
	sources []Source

	requests sync.Map // "class\x00code" -> *atomic.Uint64
}

// Add registers a source.
func (r *Registry) Add(s Source) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sources = append(r.sources, s)
}

// Handler serves the metrics.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		mw := &Writer{w: w, seen: map[string]bool{}}
		r.mu.Lock()
		sources := append([]Source(nil), r.sources...)
		r.mu.Unlock()
		for _, s := range sources {
			s(req.Context(), mw)
		}
		r.writeRequests(mw)
	})
}

func (r *Registry) writeRequests(w *Writer) {
	type row struct {
		class, code string
		n           uint64
	}
	var rows []row
	r.requests.Range(func(k, v any) bool {
		class, code, _ := strings.Cut(k.(string), "\x00")
		rows = append(rows, row{class, code, v.(*atomic.Uint64).Load()})
		return true
	})
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].class != rows[j].class {
			return rows[i].class < rows[j].class
		}
		return rows[i].code < rows[j].code
	})
	for _, x := range rows {
		w.Counter("opstide_http_requests_total", "HTTP requests served, by kind of request and status code.", float64(x.n), "class", x.class, "code", x.code)
	}
}

// class names a request without letting the path make up new label values.
func class(path string) string {
	switch {
	case strings.HasPrefix(path, "/api/"):
		return "api"
	case path == "/metrics":
		return "metrics"
	case path == "/healthz" || path == "/readyz":
		return "probe"
	}
	return "ui"
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.code == 0 {
		s.code = code
	}
	s.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the real writer (event streams flush through it).
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Instrument counts every request by class and status code.
func (r *Registry) Instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, req)
		code := sw.code
		if code == 0 {
			code = http.StatusOK
		}
		key := class(req.URL.Path) + "\x00" + strconv.Itoa(code)
		c, _ := r.requests.LoadOrStore(key, new(atomic.Uint64))
		c.(*atomic.Uint64).Add(1)
	})
}
