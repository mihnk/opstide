package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// clock is a settable time source for the cache.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func serveVia(h http.HandlerFunc, url string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, url, nil))
	return rec
}

func TestCacheReusesAnAnswerForAWhileProportionalToItsCost(t *testing.T) {
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	c := newCache(nil)
	c.now = clk.now
	var calls atomic.Int32
	cost := 2 * time.Second
	h := c.wrap(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		clk.add(cost) // the work takes this long
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"n":` + string(rune('0'+calls.Load())) + `}`))
	})

	first := serveVia(h, "/x?tz=UTC")
	if first.Body.String() != `{"n":1}` || first.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("first: %q %v", first.Body.String(), first.Header())
	}
	clk.add(5 * time.Second) // under 3 x 2 s
	if got := serveVia(h, "/x?tz=UTC").Body.String(); got != `{"n":1}` || calls.Load() != 1 {
		t.Fatalf("within the window it must be reused: %q, %d calls", got, calls.Load())
	}
	if serveVia(h, "/x?tz=Europe/Istanbul").Body.String() != `{"n":2}` {
		t.Fatal("a different query shares an answer")
	}
	clk.add(2 * time.Second) // now past 6 s since the first
	if got := serveVia(h, "/x?tz=UTC").Body.String(); got != `{"n":3}` {
		t.Fatalf("after the window it must be recomputed: %q", got)
	}
}

func TestCacheNeverHoldsCheapAnswersLongOrExpensiveOnesForever(t *testing.T) {
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	c := newCache(nil)
	c.now = clk.now
	var calls atomic.Int32
	cost := time.Millisecond
	h := c.wrap(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); clk.add(cost); _, _ = w.Write([]byte("x")) })
	serveVia(h, "/cheap")
	clk.add(cacheMin + time.Millisecond)
	serveVia(h, "/cheap")
	if calls.Load() != 2 {
		t.Fatalf("a cheap answer outlived the minimum: %d calls", calls.Load())
	}
	cost = time.Minute
	serveVia(h, "/slow")
	clk.add(cacheMax + time.Second)
	serveVia(h, "/slow")
	if calls.Load() != 4 {
		t.Fatalf("an expensive answer outlived the maximum: %d calls", calls.Load())
	}
}

func TestCacheDoesNotKeepFailures(t *testing.T) {
	c := newCache(nil)
	var calls atomic.Int32
	h := c.wrap(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			fail(w, http.StatusInternalServerError, "boom")
			return
		}
		_, _ = w.Write([]byte("fine"))
	})
	if rec := serveVia(h, "/e"); rec.Code != 500 {
		t.Fatal(rec.Code)
	}
	if rec := serveVia(h, "/e"); rec.Code != 200 || rec.Body.String() != "fine" {
		t.Fatalf("a failure was served again: %d %q", rec.Code, rec.Body.String())
	}
}

func TestCacheSharesOneComputationBetweenSimultaneousRequests(t *testing.T) {
	c := newCache(nil)
	var calls atomic.Int32
	release := make(chan struct{})
	h := c.wrap(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-release
		_, _ = w.Write([]byte("shared"))
	})
	var wg sync.WaitGroup
	out := make([]string, 8)
	for i := range out {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = serveVia(h, "/same").Body.String()
		}()
	}
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // let the others queue behind it
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("%d computations for 8 simultaneous requests", calls.Load())
	}
	for _, s := range out {
		if s != "shared" {
			t.Fatalf("a waiter got %q", s)
		}
	}
}

func TestCacheAnswerSurvivesTheFirstCallerLeaving(t *testing.T) {
	c := newCache(nil)
	h := c.wrap(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		if r.Context().Err() != nil {
			fail(w, http.StatusInternalServerError, "cancelled")
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	ctx, cancel := contextWithCancel()
	req := httptest.NewRequest(http.MethodGet, "/gone", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() { h(httptest.NewRecorder(), req); close(done) }()
	time.Sleep(10 * time.Millisecond)
	cancel() // the first client disconnects mid-computation
	<-done
	if rec := serveVia(h, "/gone"); rec.Code != 200 || rec.Body.String() != "ok" {
		t.Fatalf("the answer was ruined by the first client leaving: %d %q", rec.Code, rec.Body.String())
	}
}

func contextWithCancel() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

func TestCacheKeyCarriesTheRecordVersion(t *testing.T) {
	var version atomic.Int32
	c := newCache(func(context.Context) (string, error) { return string(rune('a' + version.Load())), nil })
	var calls atomic.Int32
	h := c.wrap(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte{byte('0' + calls.Add(1))}) })
	if serveVia(h, "/v").Body.String() != "1" || serveVia(h, "/v").Body.String() != "1" {
		t.Fatal("same version must reuse")
	}
	version.Store(1) // a run failed
	if got := serveVia(h, "/v").Body.String(); got != "2" {
		t.Fatalf("an answer from before the change was served after it: %q", got)
	}
}

func TestCacheDoesNotLetARequestJoinAComputationFromBeforeAChange(t *testing.T) {
	var version atomic.Int32
	c := newCache(func(context.Context) (string, error) { return string(rune('a' + version.Load())), nil })
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	h := c.wrap(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			close(started)
			<-release // a slow computation that began before the change
		}
		_, _ = w.Write([]byte{byte('0' + n)})
	})
	old := make(chan string)
	go func() { old <- serveVia(h, "/slow").Body.String() }()
	<-started
	version.Store(1)
	if got := serveVia(h, "/slow").Body.String(); got != "2" {
		t.Fatalf("joined the stale computation: %q", got)
	}
	close(release)
	if got := <-old; got != "1" {
		t.Fatal(got)
	}
}

func TestCacheSurvivesAPanickingHandler(t *testing.T) {
	c := newCache(nil)
	var calls atomic.Int32
	h := c.wrap(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			panic("boom")
		}
		_, _ = w.Write([]byte("fine"))
	})
	func() {
		defer func() { _ = recover() }() // net/http recovers it in production
		serveVia(h, "/p")
	}()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- serveVia(h, "/p") }()
	select {
	case rec := <-done:
		if rec.Code != 200 || rec.Body.String() != "fine" {
			t.Fatalf("after a panic: %d %q", rec.Code, rec.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a panic left the address waiting forever")
	}
	c.mu.Lock()
	b := c.bytes
	c.mu.Unlock()
	if b < 0 {
		t.Fatalf("byte count drifted to %d", b)
	}
}

func TestCacheIsBoundedByBytesAndSkipsHugeAnswers(t *testing.T) {
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	c := newCache(nil)
	c.now = clk.now
	big := make([]byte, 3<<20)
	h := c.wrap(func(w http.ResponseWriter, r *http.Request) {
		clk.add(10 * time.Second) // slow enough to be kept for 20 s
		if r.URL.Path == "/huge" {
			_, _ = w.Write(make([]byte, cacheMaxBody+1))
			return
		}
		_, _ = w.Write(big)
	})
	for i := range 40 {
		serveVia(h, "/b"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	c.mu.Lock()
	b, n := c.bytes, len(c.entries)
	c.mu.Unlock()
	if b > cacheBudget || n == 0 {
		t.Fatalf("%d bytes in %d entries, budget %d", b, n, cacheBudget)
	}
	serveVia(h, "/huge")
	c.mu.Lock()
	_, kept := c.entries["|/huge?"]
	c.mu.Unlock()
	if kept {
		t.Fatal("an answer over the size limit was kept")
	}
	// Expired answers are released, not left until the table fills.
	clk.add(time.Hour)
	serveVia(h, "/after")
	c.mu.Lock()
	b = c.bytes
	c.mu.Unlock()
	if b > 4<<20 {
		t.Fatalf("expired answers still held: %d bytes", b)
	}
}

func TestCacheKeyIgnoresParameterOrder(t *testing.T) {
	c := newCache(nil)
	var calls atomic.Int32
	h := c.wrap(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = w.Write([]byte("x")) })
	serveVia(h, "/k?a=1&b=2")
	serveVia(h, "/k?b=2&a=1")
	if calls.Load() != 1 {
		t.Fatalf("%d computations for one question asked in two orders", calls.Load())
	}
}

func TestCacheAnswersACancelledWaiterWith503(t *testing.T) {
	c := newCache(nil)
	release := make(chan struct{})
	h := c.wrap(func(w http.ResponseWriter, r *http.Request) { <-release; _, _ = w.Write([]byte("x")) })
	go serveVia(h, "/w")
	time.Sleep(30 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/w", nil).WithContext(ctx))
	close(release)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("a cancelled waiter was recorded as %d", rec.Code)
	}
}
