package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mihnk/opstide/internal/store"
)

// TestPerfLargeCluster measures the read API on a big record. It is off
// unless OPSTIDE_PERF is set (it writes about a million rows):
//
//	OPSTIDE_PERF=1 go test -run PerfLargeCluster -v -timeout 30m ./internal/api
//
// OPSTIDE_PERF_OPS and OPSTIDE_PERF_DAYS scale it (default 500 CronJobs, 30 days).
func TestPerfLargeCluster(t *testing.T) {
	if os.Getenv("OPSTIDE_PERF") == "" {
		t.Skip("set OPSTIDE_PERF=1 to measure")
	}
	nOps, days := envInt("OPSTIDE_PERF_OPS", 500), envInt("OPSTIDE_PERF_DAYS", 30)
	ctx := context.Background()
	dir := os.Getenv("OPSTIDE_PERF_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	db, err := store.Open(ctx, filepath.Join(dir, "opstide.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().UTC().Truncate(time.Minute)
	start := now.AddDate(0, 0, -days)
	// 10% every 5 minutes, 30% hourly, 60% daily: 18k runs a day at 500 CronJobs.
	type spec struct {
		cron  string
		every time.Duration
	}
	specs := []spec{{"*/5 * * * *", 5 * time.Minute}, {"7 * * * *", time.Hour}, {"30 3 * * *", 24 * time.Hour}}
	var ops []store.Operation
	for i := range nOps {
		sp := specs[2]
		switch {
		case i%10 == 0:
			sp = specs[0]
		case i%10 < 4:
			sp = specs[1]
		}
		ops = append(ops, store.Operation{ID: "op" + strconv.Itoa(i), Namespace: "ns" + strconv.Itoa(i%20), Name: "job-" + strconv.Itoa(i),
			Owner: "team-" + strconv.Itoa(i%12), Criticality: []string{"low", "medium", "high", "critical"}[i%4],
			Schedule: sp.cron, ConcurrencyPolicy: "Forbid", CreatedAt: start, FirstSeenAt: start, LastSeenAt: now})
	}
	loadStart := time.Now()
	var runs, events int
	// OPSTIDE_PERF_DIR keeps the record between runs: a loaded one is reused.
	if have, _ := db.Operations(ctx, false); len(have) >= nOps {
		ops = nil
	}
	if err := db.Update(ctx, func(w *store.Writer) error {
		for _, op := range ops {
			if err := w.UpsertOperation(ctx, op); err != nil {
				return err
			}
		}
		return w.OpenInterval(ctx, "jobs", "watching", start)
	}); err != nil {
		t.Fatal(err)
	}
	for _, chunk := range chunks(ops, 25) {
		if len(chunk) == 0 {
			break
		}
		if err := db.Update(ctx, func(w *store.Writer) error {
			for _, op := range chunk {
				n, _ := strconv.Atoi(op.ID[2:])
				every := specs[2].every
				switch {
				case n%10 == 0:
					every = specs[0].every
				case n%10 < 4:
					every = specs[1].every
				}
				for at := firstSlot(start, every); at.Before(now); at = at.Add(every) {
					failed := (at.Unix()/60+int64(n))%37 == 0
					st, typ, sev := store.RunSucceeded, "job.succeeded", "info"
					if failed {
						st, typ, sev = store.RunFailed, "job.failed", "error"
					}
					id := fmt.Sprintf("%s-%d", op.ID, at.Unix())
					fin := at.Add(40 * time.Second)
					code := int32(0)
					if err := w.UpsertRun(ctx, store.Run{ID: id, OperationID: op.ID, Namespace: op.Namespace, JobName: op.Name + "-" + strconv.FormatInt(at.Unix()/60, 10),
						ScheduledAt: at, ScheduledBasis: "annotation", CreatedAt: at, StartedAt: at.Add(2 * time.Second), FinishedAt: fin, Status: st, Attempts: 1,
						Pods:       []store.Pod{{Name: op.Name + "-abcde", UID: id + "-pod", Phase: "Succeeded", ExitCode: &code}},
						ObservedAt: at}); err != nil {
						return err
					}
					runs++
					for _, e := range []store.Event{
						{ID: "job.created/" + id, Type: "job.created", OccurredAt: at, Severity: "info", Summary: "started"},
						{ID: typ + "/" + id, Type: typ, OccurredAt: fin, Severity: sev, Summary: "finished"},
					} {
						e.Namespace, e.ResourceName, e.OperationID, e.RunID = op.Namespace, op.Name, op.ID, id
						e.ObservedAt, e.Confidence = e.OccurredAt, store.Derived
						if _, err := w.AddEvent(ctx, e); err != nil {
							return err
						}
						events++
					}
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("loaded %d CronJobs, %d runs, %d events in %s", nOps, runs, events, time.Since(loadStart).Round(time.Second))
	_ = db.Update(ctx, func(w *store.Writer) error { return w.Heartbeat(ctx, now) })
	// What Maintain does at start and every hour.
	an := time.Now()
	if err := db.Analyze(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("analyze took %s", time.Since(an).Round(time.Millisecond))

	s := New(db, watcher{}, Config{Version: "perf", Cluster: "big", Retention: store.Retention{Days: days, MaxEvents: 2_000_000}, StartedAt: start}, nil)
	mux := http.NewServeMux()
	s.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var base runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&base)
	// Each try asks for a URL of its own, so the answer cache cannot help: this
	// is what computing the answer costs. The "again" column is what a second
	// tab, or a live refresh, pays: the same URL right after.
	measure := func(name, path string) {
		var d []time.Duration
		var size int
		sep := "&"
		if !strings.Contains(path, "?") {
			sep = "?"
		}
		for i := range 5 {
			t0 := time.Now()
			resp, err := http.Get(srv.URL + path + sep + "try=" + strconv.Itoa(i))
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			buf := make([]byte, 64<<10)
			for {
				k, err := resp.Body.Read(buf)
				n += k
				if err != nil {
					break
				}
			}
			resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatalf("%s: %d", path, resp.StatusCode)
			}
			d = append(d, time.Since(t0))
			size = n
		}
		first := d[0]
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		t0 := time.Now()
		if resp, err := http.Get(srv.URL + path + sep + "try=4"); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		again := time.Since(t0)
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		t.Logf("%-34s first %7s  median %7s  again %6s  %6d KB   (heap in use %d MB)", name, first.Round(time.Millisecond), d[2].Round(time.Millisecond), again.Round(time.Millisecond), size/1024, m.HeapInuse>>20)
	}
	only := os.Getenv("OPSTIDE_PERF_ONLY")
	for _, c := range [][2]string{
		{"range within=3h", "/api/v1/range?within=3h&tz=UTC"},
		{"range today", "/api/v1/range?tz=UTC"},
		{"range within=24h", "/api/v1/range?within=24h&tz=UTC"},
		{"range within=5d", "/api/v1/range?within=5d&tz=UTC"},
		{"range within=7d", "/api/v1/range?within=7d&tz=UTC"},
		{"overview span=24h", "/api/v1/overview?span=24h&tz=UTC"},
		{"overview span=7d", "/api/v1/overview?span=7d&tz=UTC"},
		{"overview span=30d", "/api/v1/overview?span=30d&tz=UTC"},
		{"operations", "/api/v1/operations?tz=UTC"},
		{"operation detail", "/api/v1/operations/op0?tz=UTC"},
		{"events default", "/api/v1/events?within=48h&limit=100"},
		{"events 30d errors", "/api/v1/events?within=720h&show=runs&severity=error&limit=100"},
		{"events owner+outcome", "/api/v1/events?within=720h&show=runs&outcome=failed&owner=team-3&limit=100"},
		{"events text search 30d", "/api/v1/events?within=720h&q=job-4&limit=100"},
		{"owners", "/api/v1/owners"},
		{"system", "/api/v1/system?tz=UTC"},
	} {
		if only == "" || strings.Contains(c[0], only) {
			measure(c[0], c[1])
		}
	}
	st, _ := db.Stats(ctx)
	t.Logf("database %d MB, wal %d MB", st.Bytes>>20, st.WALBytes>>20)
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return def
}

func chunks[T any](s []T, n int) [][]T {
	var out [][]T
	for len(s) > n {
		out, s = append(out, s[:n]), s[n:]
	}
	return append(out, s)
}

// firstSlot is the first time after start the schedules above fire at:
// every 5 minutes on the 5s, hourly at :07, daily at 03:30 UTC.
func firstSlot(start time.Time, every time.Duration) time.Time {
	switch every {
	case time.Hour:
		at := start.Truncate(time.Hour).Add(7 * time.Minute)
		if !at.After(start) {
			at = at.Add(time.Hour)
		}
		return at
	case 24 * time.Hour:
		at := start.Truncate(24 * time.Hour).Add(3*time.Hour + 30*time.Minute)
		if !at.After(start) {
			at = at.Add(24 * time.Hour)
		}
		return at
	}
	return start.Truncate(every).Add(every)
}
