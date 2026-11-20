package notify

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mihnk/opstide/internal/api"
	"github.com/mihnk/opstide/internal/digest"
	"github.com/mihnk/opstide/internal/store"
)

func urlFile(t *testing.T, u string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "url")
	if err := os.WriteFile(p, []byte(u+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseRefusesWhatCannotBeSent(t *testing.T) {
	good := urlFile(t, "https://hooks.slack.com/services/T0/B0/x")
	plain := urlFile(t, "http://bot.internal/hook")
	ok := `{"morningSummary": {"timeZone": "Europe/Istanbul"}, "destinations": [{"type": "slack", "name": "#ops", "urlFile": "` + good + `"}]}`
	c, err := Parse([]byte(ok))
	if err != nil {
		t.Fatal(err)
	}
	if c.Summary.Schedule != "30 8 * * 1-5" || c.Summary.Covers != digest.CoversSince18 || c.Summary.Language != "en" || len(c.Summary.Include) != 4 {
		t.Errorf("defaults: %+v", c.Summary)
	}
	if _, err := Parse([]byte(`{"destinations": [{"type": "webhook", "name": "bot", "urlFile": "` + plain + `"}]}`)); err != nil {
		t.Errorf("a webhook may be http: %v", err)
	}
	for name, bad := range map[string]string{
		"no destinations":   `{"morningSummary": {}}`,
		"unknown field":     `{"destinations": [{"type": "slack", "name": "a", "urlFile": "` + good + `", "url": "x"}]}`,
		"unknown type":      `{"destinations": [{"type": "email", "name": "a", "urlFile": "` + good + `"}]}`,
		"twice":             `{"destinations": [{"type": "slack", "name": "a", "urlFile": "` + good + `"}, {"type": "teams", "name": "a", "urlFile": "` + good + `"}]}`,
		"slack over http":   `{"destinations": [{"type": "slack", "name": "a", "urlFile": "` + plain + `"}]}`,
		"missing file":      `{"destinations": [{"type": "slack", "name": "a", "urlFile": "/nonexistent/url"}]}`,
		"bad cron":          `{"morningSummary": {"schedule": "every morning"}, "destinations": [{"type": "slack", "name": "a", "urlFile": "` + good + `"}]}`,
		"bad zone":          `{"morningSummary": {"timeZone": "Mars/Olympus"}, "destinations": [{"type": "slack", "name": "a", "urlFile": "` + good + `"}]}`,
		"bad covers":        `{"morningSummary": {"covers": "48h"}, "destinations": [{"type": "slack", "name": "a", "urlFile": "` + good + `"}]}`,
		"bad include":       `{"morningSummary": {"include": ["everything"]}, "destinations": [{"type": "slack", "name": "a", "urlFile": "` + good + `"}]}`,
		"bad language":      `{"morningSummary": {"language": "de"}, "destinations": [{"type": "slack", "name": "a", "urlFile": "` + good + `"}]}`,
		"no name":           `{"destinations": [{"type": "slack", "urlFile": "` + good + `"}]}`,
		"never fires":       `{"morningSummary": {"schedule": "0 0 30 2 *"}, "destinations": [{"type": "slack", "name": "a", "urlFile": "` + good + `"}]}`,
		"not a url in file": `{"destinations": [{"type": "webhook", "name": "a", "urlFile": "` + urlFile(t, "not a url") + `"}]}`,
	} {
		_, err := Parse([]byte(bad))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), "hooks.slack.com") || strings.Contains(err.Error(), "bot.internal") {
			t.Errorf("%s: the error shows the URL: %v", name, err)
		}
	}
}

func sample() digest.Morning {
	at := time.Date(2026, 11, 17, 5, 30, 0, 0, time.UTC)
	return digest.Morning{Schema: 1, Cluster: "prod", From: at.Add(-14 * time.Hour), To: at, TimeZone: "Europe/Istanbul", Language: "en",
		Include: digest.DefaultIncludes, Runs: digest.Runs{Total: 3, Succeeded: 2, Failed: 1},
		Failed:  []digest.Run{{Name: "Nightly <backup>", Namespace: "data", CronJob: "nightly-backup", At: at.Add(-time.Hour), Link: "https://opstide.example.com/operations/x"}},
		Overdue: []digest.Overdue{}, Troubles: []digest.Trouble{}, NotWatched: []string{}}
}

func TestSlackAndTeamsBodies(t *testing.T) {
	b, err := payload(Slack, sample())
	if err != nil {
		t.Fatal(err)
	}
	var sl slackMessage
	if err := json.Unmarshal(b, &sl); err != nil {
		t.Fatal(err)
	}
	all := string(b)
	if !strings.HasPrefix(sl.Text, "Morning summary · prod") || len(sl.Blocks) == 0 {
		t.Fatalf("slack: %s", all)
	}
	if !strings.Contains(all, "Nightly &lt;backup&gt;") || !strings.Contains(all, "<https://opstide.example.com/operations/x|open>") || !strings.Contains(all, "`data/nightly-backup`") {
		t.Errorf("slack escapes text and writes links and code: %s", all)
	}
	b, err = payload(Teams, sample())
	if err != nil {
		t.Fatal(err)
	}
	var tm teamsMessage
	if err := json.Unmarshal(b, &tm); err != nil {
		t.Fatal(err)
	}
	if tm.Type != "message" || len(tm.Attachments) != 1 || tm.Attachments[0].ContentType != "application/vnd.microsoft.card.adaptive" ||
		tm.Attachments[0].Content.Type != "AdaptiveCard" || !strings.Contains(string(b), "[open](https://opstide.example.com/operations/x)") {
		t.Errorf("teams: %s", b)
	}
	b, _ = payload(Webhook, sample())
	var wh webhookBody
	if err := json.Unmarshal(b, &wh); err != nil || wh.Kind != "opstide.morning_summary" || wh.Summary.Runs.Failed != 1 || !strings.Contains(wh.Text, "• Failed") {
		t.Errorf("webhook: %s", b)
	}
}

func newNotifier(t *testing.T, dests []DestinationSpec, schedule string) (*Notifier, *store.Store) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "opstide.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	var specs []string
	for _, d := range dests {
		b, _ := json.Marshal(d)
		specs = append(specs, string(b))
	}
	c, err := Parse([]byte(`{"morningSummary": {"schedule": "` + schedule + `"}, "destinations": [` + strings.Join(specs, ",") + `]}`))
	if err != nil {
		t.Fatal(err)
	}
	n := New(c, func(_ context.Context, o api.MorningOptions) (digest.Morning, error) {
		m := sample()
		m.Test, m.Late = o.Test, o.Late
		return m, nil
	}, db, nil, "test")
	n.backoff = []time.Duration{time.Millisecond}
	return n, db
}

func TestDeliveryRetriesWhatMayPassAndKeepsTheURLSecret(t *testing.T) {
	var calls atomic.Int32
	var answers atomic.Pointer[[]int]
	answers.Store(&[]int{500, 200})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := calls.Add(1) - 1
		if r.Header.Get("Content-Type") != "application/json" || !strings.HasPrefix(r.Header.Get("User-Agent"), "OpsTide/") {
			t.Errorf("headers %v", r.Header)
		}
		a := *answers.Load()
		w.WriteHeader(a[min(int(i), len(a)-1)])
	}))
	defer srv.Close()
	n, _ := newNotifier(t, []DestinationSpec{{Type: Webhook, Name: "bot", URLFile: urlFile(t, srv.URL+"/hook-secret")}}, "30 8 * * *")
	ctx := context.Background()
	if d := n.deliver(ctx, n.cfg.Destinations[0], sample(), false); !d.OK || d.Status != 200 || calls.Load() != 2 {
		t.Fatalf("a 500 is tried again: %+v after %d calls", d, calls.Load())
	}
	answers.Store(&[]int{400})
	calls.Store(0)
	if d := n.deliver(ctx, n.cfg.Destinations[0], sample(), false); d.OK || d.Status != 400 || calls.Load() != 1 {
		t.Fatalf("a 400 is not tried again: %+v after %d calls", d, calls.Load())
	}

	// Nothing listens: the error must not name the URL or its host.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := "http://" + l.Addr().String() + "/hook-secret-token"
	l.Close()
	n2, _ := newNotifier(t, []DestinationSpec{{Type: Webhook, Name: "gone", URLFile: urlFile(t, dead)}}, "30 8 * * *")
	d := n2.deliver(ctx, n2.cfg.Destinations[0], sample(), false)
	if d.OK || d.Error == "" || strings.Contains(d.Error, "hook-secret-token") || strings.Contains(d.Error, l.Addr().String()) {
		t.Fatalf("error %q", d.Error)
	}
	st := n2.Status()
	if len(st.Destinations) != 1 || st.Destinations[0].Last == nil || st.Destinations[0].Last.OK {
		t.Errorf("status: %+v", st)
	}
}

func TestTestMessagesGoOnlyToConfiguredDestinationsAndNotTooOften(t *testing.T) {
	var got atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body webhookBody
		_ = json.NewDecoder(r.Body).Decode(&body)
		got.Store(body)
	}))
	defer srv.Close()
	n, db := newNotifier(t, []DestinationSpec{{Type: Webhook, Name: "bot", URLFile: urlFile(t, srv.URL)}}, "30 8 * * *")
	ctx := context.Background()
	if _, err := n.Test(ctx, "elsewhere"); err != api.ErrNoDestination {
		t.Errorf("unknown destination: %v", err)
	}
	d, err := n.Test(ctx, "bot")
	if err != nil || !d.OK || !d.Test {
		t.Fatalf("%+v %v", d, err)
	}
	if b, _ := got.Load().(webhookBody); !b.Summary.Test || !strings.HasPrefix(b.Text, "Test message") {
		t.Errorf("a test says so: %+v", b)
	}
	if _, err := n.Test(ctx, "bot"); err != api.ErrTooSoon {
		t.Errorf("second test at once: %v", err)
	}
	if v, ok, _ := db.Meta(ctx, stateKey); !ok || !strings.Contains(v, `"test":true`) {
		t.Errorf("how it went is kept: %q", v)
	}
}

// OpsTide was not running at 08:30; it starts at 09:10 and sends the summary then, marked late, once.
func TestASummaryMissedWhileDownIsSentLateOnce(t *testing.T) {
	var got []webhookBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b webhookBody
		_ = json.NewDecoder(r.Body).Decode(&b)
		got = append(got, b)
	}))
	defer srv.Close()
	n, _ := newNotifier(t, []DestinationSpec{{Type: Webhook, Name: "bot", URLFile: urlFile(t, srv.URL)}}, "30 8 * * *")
	n.now = func() time.Time { return time.Date(2026, 11, 17, 9, 10, 0, 0, time.UTC) }
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond) // the catch-up, then stop waiting
	defer cancel()
	n.Run(ctx)
	if len(got) != 1 || !got[0].Summary.Late {
		t.Fatalf("sent %d, %+v", len(got), got)
	}
	if !n.lastFire().Equal(time.Date(2026, 11, 17, 8, 30, 0, 0, time.UTC)) {
		t.Errorf("last fire %v", n.lastFire())
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	n.Run(ctx2) // a restart: already sent
	if len(got) != 1 {
		t.Errorf("sent again after a restart: %d", len(got))
	}
	// Four hours late is too late: the morning has moved on.
	n2, _ := newNotifier(t, []DestinationSpec{{Type: Webhook, Name: "bot", URLFile: urlFile(t, srv.URL)}}, "30 8 * * *")
	n2.now = func() time.Time { return time.Date(2026, 11, 17, 12, 31, 0, 0, time.UTC) }
	ctx3, cancel3 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel3()
	n2.Run(ctx3)
	if len(got) != 1 {
		t.Errorf("sent %d", len(got))
	}
}
