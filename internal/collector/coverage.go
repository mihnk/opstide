package collector

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"

	"github.com/mihnk/opstide/internal/store"
)

// tracker turns what the informers' list and watch calls do into coverage
// intervals: the stretches in which a scope was watched without a break.
//
// A watch that drops and resumes from its resourceVersion has lost
// nothing, so its interval is reopened. A relist means the watch could not
// resume: whatever happened between the last moment the watch was known
// to be live and the relist may be missing, and that span is recorded as
// a gap. The informers are told not to use streaming lists so every
// relist goes through List and is seen here.
type tracker struct {
	db  *store.Store
	log *slog.Logger
	now func() time.Time

	mu     sync.Mutex
	scopes map[string]*scopeState
}

type scopeState struct {
	listed    bool      // a list has completed in this process
	open      bool      // an interval is open in the store
	resumable bool      // the last close was a watch error; a resumed watch reopens it
	alive     time.Time // last moment the watch was known to be delivering
	listStart time.Time
	err       error // last list or watch error, nil once listing works
}

func newTracker(db *store.Store, log *slog.Logger, now func() time.Time) *tracker {
	return &tracker{db: db, log: log, now: now, scopes: map[string]*scopeState{}}
}

func (t *tracker) state(scope string) *scopeState {
	st := t.scopes[scope]
	if st == nil {
		st = &scopeState{}
		t.scopes[scope] = st
	}
	return st
}

// write records coverage with its own context: it must be written even
// while the list or watch that reported it is being cancelled.
func (t *tracker) write(fn func(w *store.Writer) error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := t.db.Update(ctx, fn); err != nil {
		t.log.Error("recording coverage failed", "err", err)
	}
}

func (t *tracker) listStarted(scope string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state(scope).listStart = t.now()
}

// listDone opens an interval at the moment the completed list started,
// recording the gap since the scope's previous interval, if any.
func (t *tracker) listDone(scope string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.state(scope)
	start := st.listStart
	t.write(func(w *store.Writer) error {
		ctx := context.Background()
		if st.open {
			if err := w.CloseInterval(ctx, scope, st.alive, "watch could not resume; relisted"); err != nil {
				return err
			}
		}
		prev, err := w.LastIntervalEnd(ctx, scope)
		if err != nil {
			return err
		}
		if !prev.IsZero() && prev.Before(start) {
			kind, ns := splitScope(scope)
			if _, err := w.AddEvent(ctx, store.Event{
				ID:   fmt.Sprintf("collection.gap/%s/%d", scope, prev.UnixMilli()),
				Type: "collection.gap", Kind: kind, Namespace: ns,
				ObservedAt: start, Severity: "warning", Confidence: store.Observed,
				Summary: fmt.Sprintf("Watching %s again; entries between %s and %s may be missing", scope, prev.Format(time.RFC3339), start.Format(time.RFC3339)),
				Facts:   map[string]string{"scope": scope, "from": prev.Format(time.RFC3339), "to": start.Format(time.RFC3339), "seconds": strconv.FormatInt(int64(start.Sub(prev)/time.Second), 10)},
			}); err != nil {
				return err
			}
		}
		return w.OpenInterval(ctx, scope, store.Healthy, start)
	})
	st.listed, st.open, st.resumable, st.alive, st.err = true, true, false, t.now(), nil
}

// dropped records that OpsTide gave up writing an object: whatever happened to it from since until now may be missing from
// the record, so the watch is not whole over that stretch. The interval is closed at since and reopened now, and a Log
// entry says which object and why; the record never claims continuous watching across it.
func (t *tracker) dropped(scope string, since time.Time, what string, cause error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.state(scope)
	now := t.now()
	if since.After(now) {
		since = now
	}
	t.write(func(w *store.Writer) error {
		ctx := context.Background()
		kind, ns := splitScope(scope)
		if _, err := w.AddEvent(ctx, store.Event{
			ID:   fmt.Sprintf("collection.gap/%s/dropped/%d", scope, now.UnixMilli()),
			Type: "collection.gap", Kind: kind, Namespace: ns,
			ObservedAt: now, Severity: "warning", Confidence: store.Observed,
			Summary: fmt.Sprintf("Gave up recording %s after repeated failures; entries about %s between %s and %s may be missing", what, scope, since.Format(time.RFC3339), now.Format(time.RFC3339)),
			Facts:   map[string]string{"scope": scope, "from": since.Format(time.RFC3339), "to": now.Format(time.RFC3339), "seconds": strconv.FormatInt(int64(now.Sub(since)/time.Second), 10), "object": what, "error": cutErr(cause)},
		}); err != nil {
			return err
		}
		if !st.open {
			return nil // not being watched right now: the gap is already recorded
		}
		if err := w.CloseInterval(ctx, scope, since, "gave up recording "+what); err != nil {
			return err
		}
		return w.OpenInterval(ctx, scope, store.Healthy, now)
	})
}

func cutErr(err error) string {
	if err == nil {
		return ""
	}
	return cut(err.Error(), 200)
}

// failed closes the scope's interval at the last moment it was known live.
func (t *tracker) failed(scope string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.state(scope)
	st.err = err
	if !st.open {
		return
	}
	t.write(func(w *store.Writer) error {
		return w.CloseInterval(context.Background(), scope, st.alive, err.Error())
	})
	st.open, st.resumable = false, true
}

// watchStarted reopens an interval closed by a watch error: the watch
// resumed from its resourceVersion, so nothing was missed.
func (t *tracker) watchStarted(scope string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.state(scope)
	st.alive, st.err = t.now(), nil
	if st.open || !st.resumable || !st.listed {
		return
	}
	t.write(func(w *store.Writer) error {
		_, err := w.ReopenInterval(context.Background(), scope)
		return err
	})
	st.open, st.resumable = true, false
}

func (t *tracker) touch(scope string) {
	t.mu.Lock()
	t.state(scope).alive = t.now()
	t.mu.Unlock()
}

// Problem is a scope that cannot be watched right now.
type Problem struct {
	Scope string
	Err   string
}

func (t *tracker) problems() []Problem {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Problem
	for scope, st := range t.scopes {
		if st.err != nil {
			out = append(out, Problem{Scope: scope, Err: st.err.Error()})
		}
	}
	return out
}

func splitScope(scope string) (kind, ns string) {
	for i := range len(scope) {
		if scope[i] == '/' {
			return scope[:i], scope[i+1:]
		}
	}
	return scope, ""
}

// trackedLW is a ListerWatcher that reports to the tracker.
type trackedLW struct {
	scope string
	t     *tracker
	list  func(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error)
	watch func(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error)
}

var (
	_ cache.ListerWatcher            = (*trackedLW)(nil)
	_ cache.ListerWatcherWithContext = (*trackedLW)(nil)
)

// IsWatchListSemanticsUnSupported keeps the reflector on plain List and
// Watch, so relists are visible to the tracker.
func (lw *trackedLW) IsWatchListSemanticsUnSupported() bool { return true }

func (lw *trackedLW) ListWithContext(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
	if opts.Continue == "" {
		lw.t.listStarted(lw.scope)
	}
	obj, err := lw.list(ctx, opts)
	if err != nil {
		lw.t.failed(lw.scope, err)
		return nil, err
	}
	if l, err := meta.ListAccessor(obj); err == nil && l.GetContinue() == "" {
		lw.t.listDone(lw.scope)
	}
	return obj, nil
}

func (lw *trackedLW) WatchWithContext(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	w, err := lw.watch(ctx, opts)
	if err != nil {
		lw.t.failed(lw.scope, err)
		return nil, err
	}
	lw.t.watchStarted(lw.scope)
	return newAliveWatch(w, func() { lw.t.touch(lw.scope) }), nil
}

func (lw *trackedLW) List(opts metav1.ListOptions) (runtime.Object, error) {
	return lw.ListWithContext(context.Background(), opts)
}

func (lw *trackedLW) Watch(opts metav1.ListOptions) (watch.Interface, error) {
	return lw.WatchWithContext(context.Background(), opts)
}

// aliveWatch passes a watch's events through, noting each one (bookmarks
// included) as proof the watch is live.
type aliveWatch struct {
	inner watch.Interface
	out   chan watch.Event
	done  chan struct{}
	once  sync.Once
}

func newAliveWatch(inner watch.Interface, touch func()) *aliveWatch {
	a := &aliveWatch{inner: inner, out: make(chan watch.Event), done: make(chan struct{})}
	go func() {
		defer close(a.out)
		for ev := range inner.ResultChan() {
			if ev.Type != watch.Error {
				touch()
			}
			select {
			case a.out <- ev:
			case <-a.done:
				return
			}
		}
	}()
	return a
}

func (a *aliveWatch) ResultChan() <-chan watch.Event { return a.out }

func (a *aliveWatch) Stop() {
	a.once.Do(func() { close(a.done) })
	a.inner.Stop()
}

// giveUp is what OpsTide does when it stops trying to write an object: it counts it, says so in the log, and records
// a gap in the scope's coverage over the stretch the object's changes may have been lost.
func (c *Collector) giveUp(it item, since time.Time, err error) {
	c.dropped.Add(1)
	c.droppedAt.Store(c.now().UnixMilli())
	what := it.kind + " " + it.name
	if it.namespace != "" {
		what = it.kind + " " + it.namespace + "/" + it.name
	}
	c.log.Error("giving up on an object", "kind", it.kind, "namespace", it.namespace, "name", it.name, "err", err)
	c.cov.dropped(scopeName(it.kind, c.nsOf(it.namespace)), since, what, err)
}

// DroppedObjects says how many objects OpsTide gave up recording, and when it last did (zero if never).
func (c *Collector) DroppedObjects() (uint64, time.Time) {
	n := c.dropped.Load()
	if n == 0 {
		return 0, time.Time{}
	}
	return n, time.UnixMilli(c.droppedAt.Load()).UTC()
}
