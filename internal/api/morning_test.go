package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mihnk/opstide/internal/store"
)

type fakeNotifier struct{ tested []string }

func (f *fakeNotifier) Status() NotifyStatus {
	return NotifyStatus{Configured: true, TimeZone: "Europe/Istanbul", Covers: "12h", Language: "tr", Include: []string{"failures"},
		Destinations: []DestinationStatus{{Name: "#ops", Type: "slack", Source: "Secret s, key k"}}}
}

func (f *fakeNotifier) Test(_ context.Context, name string) (Delivery, error) {
	if name != "#ops" {
		return Delivery{}, ErrNoDestination
	}
	f.tested = append(f.tested, name)
	return Delivery{OK: true, Test: true, Status: 200}, nil
}

func TestMorningSummaryAndNotifyEndpoints(t *testing.T) {
	now := base.Add(9 * time.Hour)
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "opstide.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	backup := operation("backup", "15 3 * * *")
	backup.DisplayName = "Nightly backup"
	if err := db.Update(ctx, func(w *store.Writer) error {
		if err := w.UpsertOperation(ctx, backup); err != nil {
			return err
		}
		if err := w.OpenInterval(ctx, "jobs", store.Healthy, base.AddDate(0, 0, -5)); err != nil {
			return err
		}
		at := now.Add(-3 * time.Hour)
		return w.UpsertRun(ctx, store.Run{ID: "r1", OperationID: "backup", Namespace: "ns", JobName: "backup-1", CreatedAt: at, StartedAt: at,
			FinishedAt: at.Add(time.Minute), Status: store.RunFailed, ObservedAt: at})
	}); err != nil {
		t.Fatal(err)
	}
	s := New(db, watcher{}, Config{Cluster: "prod", PublicURL: "https://opstide.example.com", Retention: store.Retention{Days: 30}}, nil)
	s.now = func() time.Time { return now }
	mux := http.NewServeMux()
	s.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var p MorningPreview
	if code := get(t, srv, "/api/v1/morning?tz=UTC&lang=en", &p); code != 200 {
		t.Fatal(code)
	}
	if p.Morning.Runs.Failed != 1 || len(p.Morning.Failed) != 1 || p.Morning.Failed[0].Name != "Nightly backup" ||
		!strings.HasPrefix(p.Morning.Failed[0].Link, "https://opstide.example.com/operations/backup?run=r1") || len(p.Lines) < 3 {
		t.Fatalf("%+v", p.Morning)
	}
	for _, bad := range []string{"?tz=Mars/Olympus", "?covers=48h", "?lang=de", "?include=everything", "?q=1"} {
		if code := get(t, srv, "/api/v1/morning"+bad, nil); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	var st NotifyStatus
	if code := get(t, srv, "/api/v1/notify", &st); code != 200 || st.Configured || st.Destinations == nil {
		t.Errorf("no notifier: %d %+v", code, st)
	}

	f := &fakeNotifier{}
	s.SetNotifier(f)
	// Without parameters the summary is the configured one: Turkish, failures only, the last 12 hours.
	if code := get(t, srv, "/api/v1/morning", &p); code != 200 || p.Morning.Language != "tr" || len(p.Morning.Include) != 1 ||
		!p.Morning.From.Equal(now.Add(-12*time.Hour)) || !strings.HasPrefix(p.Lines[0].Plain(), "Sabah özeti") {
		t.Errorf("configured defaults: %d %+v", code, p.Morning)
	}
	post := func(ct, body string) int {
		resp, err := http.Post(srv.URL+"/api/v1/notify/test", ct, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := post("application/json", `{"destination": "#ops"}`); c != 200 || len(f.tested) != 1 {
		t.Errorf("test: %d %v", c, f.tested)
	}
	if c := post("application/json", `{"destination": "elsewhere"}`); c != 404 {
		t.Errorf("unknown destination: %d", c)
	}
	if c := post("application/json", `{"url": "https://attacker.example"}`); c != 400 {
		t.Errorf("a URL never comes from a request: %d", c)
	}
	if c := post("application/x-www-form-urlencoded", `destination=%23ops`); c != 400 || len(f.tested) != 1 {
		t.Errorf("a form post is refused: %d", c)
	}
}
