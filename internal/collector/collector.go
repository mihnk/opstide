// Package collector watches CronJobs, Jobs, their Pods and Deployments and
// records what it sees in the store.
//
// Informer handlers only queue keys; one worker reads the current object
// from the informer cache and writes runs, operations and log entries in
// one transaction each. Entries have deterministic IDs, so a relist or a
// restart that shows the same object again adds nothing.
package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync/atomic"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/mihnk/opstide/internal/store"
)

// Kinds OpsTide watches, as used in coverage scopes.
const (
	CronJobs    = "cronjobs"
	Jobs        = "jobs"
	Pods        = "pods"
	Deployments = "deployments"
)

// Options configures a Collector.
type Options struct {
	// Namespaces to watch; empty watches the whole cluster.
	Namespaces []string
	Log        *slog.Logger
	// Now is the clock; time.Now when nil.
	Now func() time.Time
}

// Collector watches the cluster and writes to the store.
type Collector struct {
	client kubernetes.Interface
	db     *store.Store
	log    *slog.Logger
	now    func() time.Time
	cov    *tracker
	queue  workqueue.TypedRateLimitingInterface[item]

	clusterWide bool
	scopes      map[string]*scope // by namespace; "" when cluster-wide
	informers   []cache.SharedIndexInformer
	synced      atomic.Bool
}

type scope struct {
	ns                                string
	cronJobs, jobs, pods, deployments cache.SharedIndexInformer
}

type item struct {
	kind      string
	namespace string
	name      string
	uid       types.UID // set when the handler saw a delete
	// tombstone: the delete was inferred by a relist, not seen happen.
	tombstone bool
	deletedAt int64 // the object's deletionTimestamp, Unix ms, when it had one
}

// jobIndex indexes pods by the UID of the Job that controls them.
const jobIndex = "job"

// New sets up informers; nothing runs until Run.
func New(client kubernetes.Interface, db *store.Store, opt Options) *Collector {
	c := &Collector{
		client: client, db: db, log: opt.Log, now: opt.Now,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[item](),
			workqueue.TypedRateLimitingQueueConfig[item]{Name: "opstide"}),
		scopes: map[string]*scope{},
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	if c.now == nil {
		c.now = time.Now
	}
	c.cov = newTracker(db, c.log, c.now)
	namespaces := opt.Namespaces
	if len(namespaces) == 0 {
		c.clusterWide = true
		namespaces = []string{metav1.NamespaceAll}
	}
	for _, ns := range namespaces {
		c.scopes[ns] = c.newScope(ns)
	}
	return c
}

func (c *Collector) newScope(ns string) *scope {
	s := &scope{ns: ns}
	batch, core, apps := c.client.BatchV1(), c.client.CoreV1(), c.client.AppsV1()

	s.cronJobs = c.informer(ns, CronJobs, &batchv1.CronJob{}, nil,
		func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return batch.CronJobs(ns).List(ctx, o)
		},
		func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			return batch.CronJobs(ns).Watch(ctx, o)
		})
	s.jobs = c.informer(ns, Jobs, &batchv1.Job{}, nil,
		func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return batch.Jobs(ns).List(ctx, o)
		},
		func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			return batch.Jobs(ns).Watch(ctx, o)
		})
	// Only pods a Job created: the Job controller labels them job-name.
	jobPods := func(o *metav1.ListOptions) { o.LabelSelector = "job-name" }
	s.pods = c.informer(ns, Pods, &corev1.Pod{}, cache.Indexers{jobIndex: podJobUID},
		func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			jobPods(&o)
			return core.Pods(ns).List(ctx, o)
		},
		func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			jobPods(&o)
			return core.Pods(ns).Watch(ctx, o)
		})
	s.deployments = c.informer(ns, Deployments, &appsv1.Deployment{}, nil,
		func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return apps.Deployments(ns).List(ctx, o)
		},
		func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			return apps.Deployments(ns).Watch(ctx, o)
		})

	self := func(kind string) func(metav1.Object) (item, bool) {
		return func(o metav1.Object) (item, bool) {
			return item{kind: kind, namespace: o.GetNamespace(), name: o.GetName()}, true
		}
	}
	c.handle(s.cronJobs, true, self(CronJobs))
	c.handle(s.jobs, true, self(Jobs))
	c.handle(s.deployments, true, self(Deployments))
	// A pod change is a change to its Job's run.
	c.handle(s.pods, false, func(o metav1.Object) (item, bool) {
		ref := metav1.GetControllerOf(o)
		if ref == nil || ref.Kind != "Job" {
			return item{}, false
		}
		return item{kind: Jobs, namespace: o.GetNamespace(), name: ref.Name}, true
	})
	return s
}

func scopeName(kind, ns string) string {
	if ns == "" {
		return kind
	}
	return kind + "/" + ns
}

func (c *Collector) informer(ns, kind string, example runtime.Object, idx cache.Indexers,
	list func(context.Context, metav1.ListOptions) (runtime.Object, error),
	watchFn func(context.Context, metav1.ListOptions) (watch.Interface, error)) cache.SharedIndexInformer {
	scope := scopeName(kind, ns)
	lw := &trackedLW{scope: scope, t: c.cov, list: list, watch: watchFn}
	inf := cache.NewSharedIndexInformerWithOptions(lw, example, cache.SharedIndexInformerOptions{Indexers: idx, ObjectDescription: scope})
	_ = inf.SetWatchErrorHandlerWithContext(func(ctx context.Context, r *cache.Reflector, err error) {
		c.cov.failed(scope, err)
		cache.DefaultWatchErrorHandler(ctx, r, err)
	})
	c.informers = append(c.informers, inf)
	return inf
}

// handle queues key(obj) for every change. subject says the key names the
// object itself, so a delete carries its UID; for pods the key is their Job.
func (c *Collector) handle(inf cache.SharedIndexInformer, subject bool, key func(metav1.Object) (item, bool)) {
	enqueue := func(obj any, deleted bool) {
		tombstone := false
		if t, ok := obj.(cache.DeletedFinalStateUnknown); ok {
			obj, deleted, tombstone = t.Obj, true, true
		}
		o, ok := obj.(metav1.Object)
		if !ok {
			return
		}
		it, ok := key(o)
		if !ok {
			return
		}
		if deleted && subject {
			it.uid, it.tombstone = o.GetUID(), tombstone
			if at := mt(o.GetDeletionTimestamp()); !at.IsZero() {
				it.deletedAt = at.UnixMilli()
			}
		}
		c.queue.Add(it)
	}
	_, _ = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { enqueue(obj, false) },
		UpdateFunc: func(_, obj any) { enqueue(obj, false) },
		DeleteFunc: func(obj any) { enqueue(obj, true) },
	})
}

func podJobUID(obj any) ([]string, error) {
	o, ok := obj.(metav1.Object)
	if !ok {
		return nil, nil
	}
	if ref := metav1.GetControllerOf(o); ref != nil && ref.Kind == "Job" {
		return []string{string(ref.UID)}, nil
	}
	return nil, nil
}

// Synced is the readiness check: nil once every watch has listed once and
// the collector has reconciled what changed while it was not running.
func (c *Collector) Synced(context.Context) error {
	if c.synced.Load() {
		return nil
	}
	return errors.New("watches not synced yet")
}

// Problems lists the scopes that cannot be watched right now, with why.
func (c *Collector) Problems() []Problem { return c.cov.problems() }

// Run watches until ctx ends.
func (c *Collector) Run(ctx context.Context) error {
	defer c.queue.ShutDown()
	for _, inf := range c.informers {
		go inf.RunWithContext(ctx)
	}
	synced := make([]cache.InformerSynced, len(c.informers))
	for i, inf := range c.informers {
		synced[i] = inf.HasSynced
	}
	if !cache.WaitForCacheSync(ctx.Done(), synced...) {
		return ctx.Err()
	}
	if err := c.reconcileAbsent(ctx); err != nil {
		c.log.Warn("reconciling objects removed while not watching", "err", err)
	}
	c.synced.Store(true)
	c.log.Info("watches synced", "informers", len(c.informers))

	go func() {
		<-ctx.Done()
		c.queue.ShutDown()
	}()
	for c.next(ctx) {
	}
	return nil
}

func (c *Collector) next(ctx context.Context) bool {
	it, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(it)
	var err error
	switch it.kind {
	case CronJobs:
		err = c.syncCronJob(ctx, it)
	case Jobs:
		err = c.syncJob(ctx, it)
	case Deployments:
		err = c.syncDeployment(ctx, it)
	}
	switch {
	case err == nil:
		c.queue.Forget(it)
	case ctx.Err() != nil:
	case c.queue.NumRequeues(it) < 8:
		c.queue.AddRateLimited(it)
	default:
		c.log.Error("giving up on an object", "kind", it.kind, "namespace", it.namespace, "name", it.name, "err", err)
		c.queue.Forget(it)
	}
	return true
}

func (c *Collector) scopeFor(ns string) *scope {
	if c.clusterWide {
		return c.scopes[metav1.NamespaceAll]
	}
	return c.scopes[ns]
}

// get returns the cached object for key, or nil when it is gone or has
// been replaced by a new object with the same name.
func get[T metav1.Object](inf cache.SharedIndexInformer, ns, name string, uid types.UID) (T, error) {
	var zero T
	obj, exists, err := inf.GetIndexer().GetByKey(ns + "/" + name)
	if err != nil || !exists {
		return zero, err
	}
	o, ok := obj.(T)
	if !ok {
		return zero, fmt.Errorf("unexpected %T in cache", obj)
	}
	if uid != "" && o.GetUID() != uid {
		return zero, nil
	}
	return o, nil
}

// reconcileAbsent handles what disappeared while OpsTide was not running:
// CronJobs it knew that no longer exist, and runs it last saw active whose
// Job is gone. It cannot know when or how, and says so.
func (c *Collector) reconcileAbsent(ctx context.Context) error {
	now := c.now()
	ops, err := c.db.Operations(ctx, false)
	if err != nil {
		return err
	}
	for _, op := range ops {
		s := c.scopeFor(op.Namespace)
		if s == nil {
			continue
		}
		cj, err := get[*batchv1.CronJob](s.cronJobs, op.Namespace, op.Name, types.UID(op.ID))
		if err != nil {
			return err
		}
		if cj == nil {
			if err := c.cronJobGone(ctx, op, now, time.Time{}, false); err != nil {
				return err
			}
		}
	}
	runs, err := c.db.ActiveRuns(ctx)
	if err != nil {
		return err
	}
	for _, r := range runs {
		s := c.scopeFor(r.Namespace)
		if s == nil {
			continue
		}
		job, err := get[*batchv1.Job](s.jobs, r.Namespace, r.JobName, types.UID(r.ID))
		if err != nil {
			return err
		}
		if job == nil {
			if err := c.runLost(ctx, r, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func mt(t *metav1.Time) time.Time {
	if t == nil || t.IsZero() {
		return time.Time{}
	}
	return t.UTC().Truncate(time.Millisecond)
}

func images(spec corev1.PodSpec) []string {
	var out []string
	for _, ct := range spec.Containers {
		out = append(out, ct.Image)
	}
	return slices.Compact(out)
}
