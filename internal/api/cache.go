package api

import (
	"bytes"
	"context"
	"net/http"
	"slices"
	"sync"
	"time"
)

// cache keeps the answers of the heavy read endpoints for a moment. The Log
// and the Overview refetch whenever the record changes, and several tabs ask
// for the same thing; without this each of them would recompute a
// second-long answer. An answer is reused for three times what it took to
// make (at least a quarter of a second, at most 20), but only while the
// record has not changed: the key carries the record's version, so a request
// made after a change never gets, or joins, an answer computed before it.
// Identical requests that arrive while one is being computed share its result.
type cache struct {
	mu      sync.Mutex
	entries map[string]*cached
	bytes   int
	now     func() time.Time
	// version names the state of the record; nil, or an error, means the
	// answer is not tied to one (tests; a database that cannot answer).
	version func(context.Context) (string, error)
}

type cached struct {
	done    chan struct{} // closed when the answer is ready
	status  int
	header  http.Header
	body    []byte
	expires time.Time
}

const (
	cacheMin = 250 * time.Millisecond
	cacheMax = 20 * time.Second
	// Memory is bounded by bytes, not by entries: an answer is up to a few MB.
	cacheBudget  = 32 << 20
	cacheMaxBody = 4 << 20
	// How long one computation may take. It outlives its first caller (others
	// may be waiting) but not a shutdown's drain.
	cacheCompute = 15 * time.Second
)

func newCache(version func(context.Context) (string, error)) *cache {
	return &cache{entries: map[string]*cached{}, now: time.Now, version: version}
}

// capture records a response instead of sending it.
type capture struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (c *capture) Header() http.Header         { return c.header }
func (c *capture) Write(b []byte) (int, error) { return c.body.Write(b) }
func (c *capture) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
}

// key is the request's address with its parameters in a fixed order, behind
// the record's version.
func (c *cache) key(r *http.Request) string {
	v := ""
	if c.version != nil {
		v, _ = c.version(r.Context())
	}
	return v + "|" + r.URL.Path + "?" + r.URL.Query().Encode()
}

// wrap serves h through the cache.
func (c *cache) wrap(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := c.key(r)
		c.mu.Lock()
		e := c.entries[key]
		if e != nil {
			select {
			case <-e.done:
				if !c.now().Before(e.expires) {
					c.drop(key, e)
					e = nil
				}
			default: // being computed: wait for it below
			}
		}
		compute := false
		if e == nil {
			e = &cached{done: make(chan struct{})}
			c.entries[key] = e
			c.sweep()
			compute = true
		}
		c.mu.Unlock()

		if compute {
			c.compute(key, e, h, r)
		} else {
			select {
			case <-e.done:
			case <-r.Context().Done():
				fail(w, http.StatusServiceUnavailable, "the request was cancelled")
				return
			}
		}
		for k, v := range e.header {
			w.Header()[k] = slices.Clone(v)
		}
		w.WriteHeader(e.status)
		_, _ = w.Write(e.body)
	}
}

// compute runs h for e. Whatever happens, e is finished and the waiters
// are released: a handler that panics must not leave its address waiting
// forever.
func (c *cache) compute(key string, e *cached, h http.HandlerFunc, r *http.Request) {
	finished := false
	defer func() {
		if finished {
			return
		}
		c.mu.Lock()
		e.status, e.header, e.body = http.StatusInternalServerError, http.Header{"Content-Type": {"application/json"}}, []byte(`{"schema":1,"error":"internal error; see the collector's log"}`+"\n")
		if c.entries[key] == e { // never counted in c.bytes
			delete(c.entries, key)
		}
		c.mu.Unlock()
		close(e.done)
		// The panic goes on to net/http, which logs it and closes the connection.
	}()
	// The caller that happens to be first may leave; the others still want the answer.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), cacheCompute)
	defer cancel()
	rec := &capture{header: http.Header{}}
	start := c.now()
	h(rec, r.Clone(ctx))
	took := c.now().Sub(start)

	c.mu.Lock()
	e.status, e.header, e.body = rec.status, rec.header, rec.body.Bytes()
	if e.status == 0 {
		e.status = http.StatusOK
	}
	if e.status == http.StatusOK && len(e.body) <= cacheMaxBody {
		e.expires = c.now().Add(min(max(3*took, cacheMin), cacheMax))
		c.bytes += len(e.body)
	} else {
		// Errors, and answers too big to keep, are shared with whoever is already
		// waiting but not served again.
		e.expires = c.now()
		delete(c.entries, key)
	}
	c.sweep()
	c.mu.Unlock()
	finished = true
	close(e.done)
}

// drop removes e if it is still the entry for key. The lock is held.
func (c *cache) drop(key string, e *cached) {
	if c.entries[key] == e {
		delete(c.entries, key)
		c.bytes -= len(e.body)
	}
}

// sweep forgets expired answers, then the soonest to expire until the
// budget holds. The lock is held.
func (c *cache) sweep() {
	ready := func(e *cached) bool {
		select {
		case <-e.done:
			return true
		default:
			return false // being computed
		}
	}
	for k, e := range c.entries {
		if ready(e) && !c.now().Before(e.expires) {
			c.drop(k, e)
		}
	}
	for c.bytes > cacheBudget {
		var oldestKey string
		var oldest *cached
		for k, e := range c.entries {
			if ready(e) && (oldest == nil || e.expires.Before(oldest.expires)) {
				oldestKey, oldest = k, e
			}
		}
		if oldest == nil {
			return
		}
		c.drop(oldestKey, oldest)
	}
}
