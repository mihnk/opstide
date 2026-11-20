package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mihnk/opstide/internal/api"
	"github.com/mihnk/opstide/internal/digest"
	"github.com/mihnk/opstide/internal/metrics"
	"github.com/mihnk/opstide/internal/store"
)

// Builder makes a summary from the record (api.Server.Morning).
type Builder func(ctx context.Context, o api.MorningOptions) (digest.Morning, error)

const (
	stateKey = "notify_state" // in the store's meta table: when the summary last went out, and each delivery
	// catchUp is how late a summary is still sent when OpsTide was not running at its time; later than that it would
	// arrive in the middle of the day, about a night long past.
	catchUp      = 3 * time.Hour
	attempts     = 3
	timeout      = 15 * time.Second
	testInterval = 30 * time.Second
)

// Notifier sends the morning summary on its schedule. It implements api.Notifier.
type Notifier struct {
	cfg     *Config
	build   Builder
	db      *store.Store
	log     *slog.Logger
	version string
	client  *http.Client
	now     func() time.Time
	backoff []time.Duration

	mu       sync.Mutex
	st       state
	lastTest map[string]time.Time
	ok, bad  map[string]uint64
}

type state struct {
	LastFire     time.Time            `json:"lastFire"` // the scheduled time of the latest summary sent
	Destinations map[string]destState `json:"destinations"`
}

type destState struct {
	Last   *api.Delivery `json:"last,omitempty"`
	LastOK time.Time     `json:"lastOk,omitzero"`
}

// New makes a Notifier for a validated configuration.
func New(cfg *Config, build Builder, db *store.Store, log *slog.Logger, version string) *Notifier {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone() // honours HTTPS_PROXY and NO_PROXY
	return &Notifier{cfg: cfg, build: build, db: db, log: log, version: version, now: time.Now,
		client:  &http.Client{Transport: tr, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		backoff: []time.Duration{5 * time.Second, 20 * time.Second},
		st:      state{Destinations: map[string]destState{}}, lastTest: map[string]time.Time{}, ok: map[string]uint64{}, bad: map[string]uint64{}}
}

// Run sends summaries until ctx ends. A summary whose time passed while OpsTide was not running is sent at start, if
// its time was less than catchUp ago, and says it is late.
func (n *Notifier) Run(ctx context.Context) {
	n.load(ctx)
	now := n.now()
	if prev := n.previous(now); !prev.IsZero() && n.lastFire().Before(prev) {
		n.fire(ctx, prev, true)
	}
	for {
		next := n.cfg.Schedule.Next(n.now())
		if next.IsZero() {
			return
		}
		// One clock for both: when the summary is due and how long to wait for it.
		t := time.NewTimer(next.Sub(n.now()))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		if !next.After(n.lastFire()) {
			continue // already sent for this time
		}
		n.fire(ctx, next, false)
	}
}

// previous is the latest scheduled time in the last catchUp, or zero.
func (n *Notifier) previous(now time.Time) time.Time {
	times, _ := n.cfg.Schedule.Between(now.Add(-catchUp), now)
	if len(times) == 0 {
		return time.Time{}
	}
	return times[len(times)-1]
}

func (n *Notifier) lastFire() time.Time {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.st.LastFire
}

// fire builds the summary due at `at` and sends it to every destination.
func (n *Notifier) fire(ctx context.Context, at time.Time, late bool) {
	m, err := n.summary(ctx, false, late)
	if err != nil {
		n.log.Error("morning summary: cannot build it", "err", err)
		return
	}
	for _, d := range n.cfg.Destinations {
		n.deliver(ctx, d, m, false)
	}
	n.mu.Lock()
	n.st.LastFire = at
	n.mu.Unlock()
	n.save(ctx)
}

func (n *Notifier) summary(ctx context.Context, test, late bool) (digest.Morning, error) {
	s := n.cfg.Summary
	now := n.now().UTC()
	return n.build(ctx, api.MorningOptions{From: digest.Since(s.Covers, now, n.cfg.Location), To: now, TimeZone: s.TimeZone,
		Language: s.Language, Include: s.Include, Test: test, Late: late})
}

// deliver sends m to d, trying again after a network error or a 429 or 5xx answer, and records how it went.
func (n *Notifier) deliver(ctx context.Context, d DestinationSpec, m digest.Morning, test bool) api.Delivery {
	res := api.Delivery{At: n.now().UTC(), Test: test}
	body, err := payload(d.Type, m)
	if err != nil {
		res.Error = "cannot build the message: " + err.Error()
	} else {
		tries := attempts
		if test {
			tries = 1 // someone is waiting for the answer
		}
		for i := 0; i < tries; i++ {
			if i > 0 {
				select {
				case <-ctx.Done():
				case <-time.After(n.backoff[min(i-1, len(n.backoff)-1)]):
				}
			}
			res.Status, err = n.post(ctx, d, body)
			res.OK = err == nil
			res.Error = ""
			if err != nil {
				res.Error = err.Error()
			}
			if res.OK || (res.Status >= 400 && res.Status < 500 && res.Status != http.StatusTooManyRequests) || ctx.Err() != nil {
				break
			}
		}
	}
	n.mu.Lock()
	ds := n.st.Destinations[d.Name]
	r := res
	ds.Last = &r
	if res.OK {
		ds.LastOK = res.At
		n.ok[d.Name]++
	} else {
		n.bad[d.Name]++
	}
	n.st.Destinations[d.Name] = ds
	n.mu.Unlock()
	if res.OK {
		n.log.Info("morning summary sent", "destination", d.Name, "test", test, "status", res.Status)
	} else {
		n.log.Warn("morning summary not delivered", "destination", d.Name, "test", test, "status", res.Status, "err", res.Error)
	}
	return res
}

// post sends one request. Its error never contains the URL, which is a secret.
func (n *Notifier) post(ctx context.Context, d DestinationSpec, body []byte) (int, error) {
	raw, err := readURL(d)
	if err != nil {
		return 0, err
	}
	secret := func(err error) error {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		msg := strings.ReplaceAll(err.Error(), raw, "<url>")
		if u, perr := url.Parse(raw); perr == nil {
			msg = strings.ReplaceAll(msg, u.Host, "<host>")
		}
		return errors.New(msg)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, raw, bytes.NewReader(body))
	if err != nil {
		return 0, secret(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "OpsTide/"+n.version)
	resp, err := n.client.Do(req)
	if err != nil {
		return 0, secret(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("the destination answered %s", resp.Status)
	}
	return resp.StatusCode, nil
}

// Test sends a test message, the summary as it would be now, to one configured destination.
func (n *Notifier) Test(ctx context.Context, name string) (api.Delivery, error) {
	var dest *DestinationSpec
	for i := range n.cfg.Destinations {
		if n.cfg.Destinations[i].Name == name {
			dest = &n.cfg.Destinations[i]
		}
	}
	if dest == nil {
		return api.Delivery{}, api.ErrNoDestination
	}
	n.mu.Lock()
	if t, ok := n.lastTest[name]; ok && n.now().Sub(t) < testInterval {
		n.mu.Unlock()
		return api.Delivery{}, api.ErrTooSoon
	}
	n.lastTest[name] = n.now()
	n.mu.Unlock()
	m, err := n.summary(ctx, true, false)
	if err != nil {
		return api.Delivery{}, err
	}
	res := n.deliver(ctx, *dest, m, true)
	n.save(ctx)
	return res, nil
}

// Status is how the summary is configured and how its deliveries went, for the Integrations page.
func (n *Notifier) Status() api.NotifyStatus {
	s := n.cfg.Summary
	out := api.NotifyStatus{Configured: true, Schedule: s.Schedule, TimeZone: s.TimeZone, Covers: s.Covers, Language: s.Language,
		Include: s.Include, Next: n.cfg.Schedule.Next(n.now())}
	n.mu.Lock()
	defer n.mu.Unlock()
	out.LastSent = n.st.LastFire
	for _, d := range n.cfg.Destinations {
		ds := n.st.Destinations[d.Name]
		out.Destinations = append(out.Destinations, api.DestinationStatus{Name: d.Name, Type: d.Type, Source: d.Source, Last: ds.Last, LastOK: ds.LastOK})
	}
	return out
}

// Metrics describes the deliveries for /metrics.
func (n *Notifier) Metrics() metrics.Source {
	return func(_ context.Context, w *metrics.Writer) {
		n.mu.Lock()
		defer n.mu.Unlock()
		if next := n.cfg.Schedule.Next(n.now()); !next.IsZero() {
			w.Gauge("opstide_notify_next_timestamp_seconds", "When the next morning summary is due, Unix time.", float64(next.Unix()))
		}
		for _, d := range n.cfg.Destinations {
			w.Counter("opstide_notify_deliveries_total", "Morning summaries sent to a destination since start, by result (tests included).", float64(n.ok[d.Name]), "destination", d.Name, "result", "ok")
			w.Counter("opstide_notify_deliveries_total", "Morning summaries sent to a destination since start, by result (tests included).", float64(n.bad[d.Name]), "destination", d.Name, "result", "failed")
			if t := n.st.Destinations[d.Name].LastOK; !t.IsZero() {
				w.Gauge("opstide_notify_last_success_timestamp_seconds", "When a message last reached the destination, Unix time.", float64(t.Unix()), "destination", d.Name)
			}
		}
	}
}

func (n *Notifier) load(ctx context.Context) {
	v, ok, err := n.db.Meta(ctx, stateKey)
	if err != nil || !ok {
		return
	}
	var st state
	if err := json.Unmarshal([]byte(v), &st); err != nil {
		return
	}
	if st.Destinations == nil {
		st.Destinations = map[string]destState{}
	}
	n.mu.Lock()
	n.st = st
	n.mu.Unlock()
}

func (n *Notifier) save(ctx context.Context) {
	n.mu.Lock()
	b, err := json.Marshal(n.st)
	n.mu.Unlock()
	if err != nil {
		return
	}
	if err := n.db.Update(ctx, func(w *store.Writer) error { return w.SetMeta(ctx, stateKey, string(b)) }); err != nil {
		n.log.Warn("morning summary: cannot record how it went", "err", err)
	}
}
