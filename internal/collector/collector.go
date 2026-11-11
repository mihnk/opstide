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
	"sync"
	"sync/atomic"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	"github.com/mihnk/opstide/internal/release"
	"github.com/mihnk/opstide/internal/store"
)

// Kinds OpsTide watches, as used in coverage scopes.
const (
	CronJobs    = "cronjobs"
	Jobs        = "jobs"
	Pods        = "pods"
	Deployments = "deployments"
	// ReplicaSets and DeployPods are the watches behind releases. DeployPods
	// are the Pods a Deployment's ReplicaSets own, found by the
	// pod-template-hash label the Deployment controller puts on them, so the
	// API server filters out every other Pod before OpsTide sees it.
	ReplicaSets = "replicasets"
	DeployPods  = "deploypods"
)

// Options configures a Collector.
type Options struct {
	// Namespaces to watch; empty watches the whole cluster.
	Namespaces []string
	Log        *slog.Logger
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// ReleaseWindow is how long after a rollout completes a release is
	// observed before it can be called healthy; 10 minutes when zero.
	ReleaseWindow time.Duration
	// EventsSource forces the API Kubernetes Events are read through
	// ("events.k8s.io/v1" or "core/v1"); empty chooses by discovery.
	EventsSource string
	// ObserveNodes watches Nodes. They are cluster-scoped, so it needs cluster RBAC
	// and is off unless asked for, whatever the namespaces being watched.
	ObserveNodes bool
	// WatchConfigMaps records ConfigMaps being created, deleted and written (by which field manager), keeping a hash of
	// their content (HMAC) and the names of their keys, never a value.
	WatchConfigMaps bool
	// WatchSecretMetadata records Secrets being created, deleted and written, through Metadata only: their data is never
	// requested. Off unless asked for, and ignored without Metadata.
	WatchSecretMetadata bool
	// Metadata is the metadata-only client Secrets are read through (PartialObjectMetadata).
	Metadata metadata.Interface
	// RetentionDays is how long the record keeps history: a write older than that, found in managedFields, is not
	// recorded (it would only be pruned again). 0 sets no limit.
	RetentionDays int
	// configWriteEvery overrides the least time between two write entries of one ConfigMap or Secret (tests).
	configWriteEvery time.Duration
}

// Collector watches the cluster and writes to the store.
type Collector struct {
	client kubernetes.Interface
	md     metadata.Interface // metadata-only reads (Secrets); nil when not asked for
	db     *store.Store
	log    *slog.Logger
	now    func() time.Time
	cov    *tracker
	queue  workqueue.TypedRateLimitingInterface[item]
	// lowQueue holds Kubernetes Events and autoscalers, which come in bursts (a node upgrade, a crash loop) and must not
	// delay a short Job or a Pod being recorded: it has its own worker.
	lowQueue workqueue.TypedRateLimitingInterface[item]
	// cfgQueue holds ConfigMaps and Secrets: a relist of tens of thousands of them must delay neither Jobs nor Events.
	cfgQueue workqueue.TypedRateLimitingInterface[item]
	// firstFail is when each object's current run of failed tries began (read and written by its worker only, per queue).
	firstFail map[item]time.Time
	// written remembers the spec writes already put in the change ledger; started is when this process began.
	written writtenSeen
	started time.Time
	// cfg is what ConfigMap and Secret observation remembers between two looks; the watches are on when asked for.
	cfg                           configState
	watchConfigMaps, watchSecrets bool
	cfgEvery                      time.Duration
	retentionDays                 int
	// trim reduces ConfigMaps and Secrets' metadata as they arrive; cfgGone holds the last state of one whose delete
	// was seen until the worker has recorded it.
	trim    *configTrim
	cfgGone sync.Map
	failMu  sync.Mutex
	// dropped counts objects given up on; droppedAt is the last time.
	dropped   atomic.Uint64
	droppedAt atomic.Int64

	clusterWide bool
	scopes      map[string]*scope // by namespace; "" when cluster-wide
	informers   []cache.SharedIndexInformer
	synced      atomic.Bool

	processed, failed atomic.Uint64

	window time.Duration
	// Evidence: Events and autoscalers are optional watches. A cluster that
	// does not allow reading them must not stop releases and runs from being
	// recorded, so they are started but not waited for.
	opt             []optInf
	observeNodes    bool
	nodes           cache.SharedIndexInformer // cluster-scoped; nil unless Nodes are observed
	eventsSourceOpt string
	evSource        atomic.Value // string, the API in use
	em              evidenceMetrics
	// gone holds the last state of a Deployment Pod whose delete was seen, by
	// UID, until the worker has recorded it: the informer's cache no longer has it.
	gone sync.Map
	wm   workloadMetrics
}

type scope struct {
	ns                                string
	cronJobs, jobs, pods, deployments cache.SharedIndexInformer
	replicaSets, deployPods           cache.SharedIndexInformer
	eventsV1, eventsCore, hpas        cache.SharedIndexInformer
	statefulSets, daemonSets          cache.SharedIndexInformer
	revisions, workloadPods           cache.SharedIndexInformer
	configMaps, secrets               cache.SharedIndexInformer // nil unless watched
}

// optInf is an optional watch and the group it starts with.
type optInf struct {
	inf   cache.SharedIndexInformer
	group string
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
		lowQueue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[item](),
			workqueue.TypedRateLimitingQueueConfig[item]{Name: "opstide-events"}),
		cfgQueue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[item](),
			workqueue.TypedRateLimitingQueueConfig[item]{Name: "opstide-config"}),
		trim:      newConfigTrim(),
		firstFail: map[item]time.Time{},
		scopes:    map[string]*scope{},
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	if c.now == nil {
		c.now = time.Now
	}
	c.started = c.now()
	c.eventsSourceOpt = opt.EventsSource
	c.observeNodes = opt.ObserveNodes
	c.md = opt.Metadata
	c.cfgEvery = opt.configWriteEvery
	if c.cfgEvery <= 0 {
		c.cfgEvery = configWriteEvery
	}
	c.watchConfigMaps, c.watchSecrets = opt.WatchConfigMaps, opt.WatchSecretMetadata && opt.Metadata != nil
	c.retentionDays = opt.RetentionDays
	c.window = opt.ReleaseWindow
	if c.window <= 0 {
		c.window = release.DefaultWindow
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
	if c.observeNodes {
		c.addNodeInformer()
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

	s.replicaSets = c.informer(ns, ReplicaSets, &appsv1.ReplicaSet{}, cache.Indexers{rsOwnerIndex: rsOwnerUID},
		func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return apps.ReplicaSets(ns).List(ctx, o)
		},
		func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			return apps.ReplicaSets(ns).Watch(ctx, o)
		})
	// Deployment-owned Pods: the Deployment controller labels them pod-template-hash.
	deployPodSel := func(o *metav1.ListOptions) { o.LabelSelector = "pod-template-hash" }
	s.deployPods = c.informer(ns, DeployPods, &corev1.Pod{}, cache.Indexers{rsPodIndex: podRSUID},
		func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			deployPodSel(&o)
			return core.Pods(ns).List(ctx, o)
		},
		func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			deployPodSel(&o)
			return core.Pods(ns).Watch(ctx, o)
		})
	// Keep in memory only what is read: the cache holds every such Pod of the
	// cluster, and a Pod's spec is most of its size.
	_ = s.deployPods.SetTransform(trimPod)
	_ = s.replicaSets.SetTransform(trimReplicaSet)

	// Evidence. Only one of the two Event informers is ever started.
	events := c.client.EventsV1()
	s.eventsV1 = c.optionalInformer(ns, Events, &eventsv1.Event{}, nil,
		func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return events.Events(ns).List(ctx, o)
		},
		func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			return events.Events(ns).Watch(ctx, o)
		}, "events-v1")
	s.eventsCore = c.optionalInformer(ns, Events, &corev1.Event{}, nil,
		func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return core.Events(ns).List(ctx, o)
		},
		func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			return core.Events(ns).Watch(ctx, o)
		}, "events-core")
	c.addWorkloadInformers(s, ns)
	c.addConfigInformers(s, ns)
	_ = s.eventsV1.SetTransform(trimEvent)
	_ = s.eventsCore.SetTransform(trimEvent)
	autoscaling := c.client.AutoscalingV2()
	s.hpas = c.optionalInformer(ns, HPAs, &autoscalingv2.HorizontalPodAutoscaler{}, nil,
		func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			return autoscaling.HorizontalPodAutoscalers(ns).List(ctx, o)
		},
		func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			return autoscaling.HorizontalPodAutoscalers(ns).Watch(ctx, o)
		}, "hpa")

	self := func(kind string) func(metav1.Object) (item, bool) {
		return func(o metav1.Object) (item, bool) {
			return item{kind: kind, namespace: o.GetNamespace(), name: o.GetName()}, true
		}
	}
	c.handle(s.cronJobs, true, self(CronJobs))
	c.handle(s.jobs, true, self(Jobs))
	c.handle(s.deployments, true, self(Deployments))
	// A ReplicaSet change is a change to its Deployment's releases.
	c.handle(s.replicaSets, false, func(o metav1.Object) (item, bool) {
		ref := metav1.GetControllerOf(o)
		if ref == nil || ref.Kind != "Deployment" {
			return item{}, false
		}
		return item{kind: Deployments, namespace: o.GetNamespace(), name: ref.Name}, true
	})
	c.handleDeployPods(s.deployPods)
	c.handle(s.eventsV1, true, c.eventItem)
	c.handle(s.eventsCore, true, c.eventItem)
	c.handle(s.hpas, true, self(HPAs))
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

// eventItem queues an Event unless it is about a kind OpsTide does not follow.
func (c *Collector) eventItem(o metav1.Object) (item, bool) {
	kind := ""
	switch e := o.(type) {
	case *eventsv1.Event:
		kind = e.Regarding.Kind
	case *corev1.Event:
		kind = e.InvolvedObject.Kind
	}
	if !evidenceKinds[kind] {
		return item{}, false
	}
	return item{kind: Events, namespace: o.GetNamespace(), name: o.GetName()}, true
}

// optionalInformer is informer for a watch the collector starts but does not wait for.
func (c *Collector) optionalInformer(ns, kind string, example runtime.Object, idx cache.Indexers,
	list func(context.Context, metav1.ListOptions) (runtime.Object, error),
	watchFn func(context.Context, metav1.ListOptions) (watch.Interface, error), group string) cache.SharedIndexInformer {
	n := len(c.informers)
	inf := c.informer(ns, kind, example, idx, list, watchFn)
	c.informers = c.informers[:n] // not one the collector waits for
	c.opt = append(c.opt, optInf{inf, group})
	return inf
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

// add queues an object on the queue its kind belongs to.
func (c *Collector) add(it item) { c.queueOf(it.kind).Add(it) }

func (c *Collector) queueOf(kind string) workqueue.TypedRateLimitingInterface[item] {
	if kind == ConfigMaps || kind == Secrets {
		return c.cfgQueue
	}
	if kind == Events || kind == HPAs {
		return c.lowQueue
	}
	return c.queue
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
		c.add(it)
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

// Stats is a snapshot of the collector's own work.
type Stats struct {
	Synced bool
	// Processed counts objects written to the record; Failed counts tries
	// that failed (an object retried three times counts three).
	Processed, Failed uint64
	// Queued is how many objects wait to be written.
	Queued int
	// Failing is how many scopes cannot be watched right now.
	Failing int
	// Dropped counts objects OpsTide gave up recording after repeated failures; each one is a recorded gap.
	Dropped uint64
	// Workload counts release observation, with bounded labels only.
	Workload WorkloadStats
	// Evidence counts Event and autoscaler observation.
	Evidence EvidenceStats
}

// Stats returns the collector's counters.
func (c *Collector) Stats() Stats {
	return Stats{
		Synced: c.synced.Load(), Processed: c.processed.Load(), Failed: c.failed.Load(),
		Queued: c.queue.Len() + c.lowQueue.Len() + c.cfgQueue.Len(), Dropped: c.dropped.Load(), Failing: len(c.cov.problems()), Workload: c.wm.stats(), Evidence: c.em.stats(c.eventsSource()),
	}
}

// Problems lists the scopes that cannot be watched right now, with why.
func (c *Collector) Problems() []Problem { return c.cov.problems() }

// Run watches until ctx ends.
func (c *Collector) Run(ctx context.Context) error {
	defer c.queue.ShutDown()
	defer c.lowQueue.ShutDown()
	defer c.cfgQueue.ShutDown()
	if c.watchConfigMaps {
		// The key ConfigMap content is hashed under; the transforms need it before the first object arrives.
		key, err := c.db.ConfigHashKey(ctx)
		if err != nil {
			return fmt.Errorf("the ConfigMap hash key: %w", err)
		}
		c.trim.key = key
	}
	for _, inf := range c.informers {
		go inf.RunWithContext(ctx)
	}
	// Events (through one API) and autoscalers: started, not waited for.
	src := c.pickEventsSource()
	c.evSource.Store(src)
	optional := c.optionalInformers(src)
	for _, inf := range optional {
		go inf.RunWithContext(ctx)
	}
	// Each group is reconciled once its own watches have listed: a group that cannot be
	// read (no permission) must not hold back the others, and an unlisted cache must
	// never be taken to mean that objects are gone.
	c.whenSynced(ctx, []string{"events-v1", "events-core", "hpa"}, src, func() {
		if err := c.reconcileAbsentHPAs(ctx); err != nil {
			c.log.Warn("reconciling autoscalers removed while not watching", "err", err)
		}
		if err := c.reconcileAbsentEvidence(ctx); err != nil {
			c.log.Warn("reconciling Events dropped while not watching", "err", err)
		}
	})
	c.whenSynced(ctx, []string{StatefulSets, DaemonSets, WorkloadPods}, src, func() {
		if err := c.reconcileAbsentWorkloads(ctx); err != nil {
			c.log.Warn("reconciling workloads removed while not watching", "err", err)
		}
		if err := c.reconcileAbsentWorkloadPods(ctx); err != nil {
			c.log.Warn("reconciling Pods removed while not watching", "err", err)
		}
		if err := c.reconcileConfigRefs(ctx, store.KindStatefulSet, store.KindDaemonSet); err != nil {
			c.log.Warn("ending references of workloads removed while not watching", "err", err)
		}
	})
	for _, kind := range []string{ConfigMaps, Secrets} { // each on its own: one that cannot be read must not hold back the other
		c.whenSynced(ctx, []string{kind}, src, func() {
			if err := c.reconcileAbsentConfig(ctx, kind); err != nil {
				c.log.Warn("reconciling ConfigMaps or Secrets removed while not watching", "kind", kind, "err", err)
			}
		})
	}
	c.whenSynced(ctx, []string{Nodes}, src, func() {
		if err := c.reconcileAbsentNodes(ctx); err != nil {
			c.log.Warn("reconciling Nodes removed while not watching", "err", err)
		}
	})
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
	if err := c.reconcileAbsentPods(ctx); err != nil {
		c.log.Warn("reconciling Pods removed while not watching", "err", err)
	}
	if err := c.reconcileAbsentDeployments(ctx); err != nil {
		c.log.Warn("reconciling Deployments removed while not watching", "err", err)
	}
	if err := c.reconcileConfigRefs(ctx, store.KindDeployment, "CronJob"); err != nil {
		c.log.Warn("ending references of workloads removed while not watching", "err", err)
	}
	go c.sweepReleases(ctx)
	c.synced.Store(true)
	c.log.Info("watches synced", "informers", len(c.informers))

	go func() {
		<-ctx.Done()
		c.queue.ShutDown()
		c.lowQueue.ShutDown()
		c.cfgQueue.ShutDown()
	}()
	go func() {
		for c.next(ctx, c.lowQueue) {
		}
	}()
	go func() {
		for c.next(ctx, c.cfgQueue) {
		}
	}()
	for c.next(ctx, c.queue) {
	}
	return nil
}

func (c *Collector) next(ctx context.Context, q workqueue.TypedRateLimitingInterface[item]) bool {
	it, quit := q.Get()
	if quit {
		return false
	}
	defer q.Done(it)
	var err error
	switch it.kind {
	case CronJobs:
		err = c.syncCronJob(ctx, it)
	case Jobs:
		err = c.syncJob(ctx, it)
	case Deployments:
		err = c.syncDeployment(ctx, it)
	case DeployPods:
		err = c.syncDeployPod(ctx, it)
	case StatefulSets:
		err = c.syncStatefulSet(ctx, it)
	case DaemonSets:
		err = c.syncDaemonSet(ctx, it)
	case WorkloadPods:
		err = c.syncWorkloadPod(ctx, it)
	case Nodes:
		err = c.syncNode(ctx, it)
	case Events:
		err = c.syncEvent(ctx, it)
	case HPAs:
		err = c.syncHPA(ctx, it)
	case ConfigMaps, Secrets:
		err = c.syncConfig(ctx, it)
	}
	switch {
	case err == nil:
		c.processed.Add(1)
		c.clearFail(it)
		q.Forget(it)
	case ctx.Err() != nil:
	case q.NumRequeues(it) < 8:
		c.failed.Add(1)
		c.markFail(it)
		q.AddRateLimited(it)
	default:
		c.failed.Add(1)
		c.giveUp(it, c.clearFail(it), err)
		q.Forget(it)
	}
	return true
}

// markFail notes when an object's run of failed tries began.
func (c *Collector) markFail(it item) {
	c.failMu.Lock()
	defer c.failMu.Unlock()
	if _, ok := c.firstFail[it]; !ok {
		c.firstFail[it] = c.now()
	}
}

// clearFail forgets an object's failures and returns when they began (now when none were noted).
func (c *Collector) clearFail(it item) time.Time {
	c.failMu.Lock()
	defer c.failMu.Unlock()
	t, ok := c.firstFail[it]
	delete(c.firstFail, it)
	if !ok {
		return c.now()
	}
	return t
}

// EvidenceSource is the API Events are read through: events.k8s.io/v1, or core/v1
// on a cluster that does not serve the first. Empty before Run has chosen.
func (c *Collector) EvidenceSource() string {
	v, _ := c.evSource.Load().(string)
	return v
}

// evidenceSynced reports whether the Event and autoscaler watches have listed
// at least once. Readiness does not wait for it (a cluster that forbids reading
// Events must still record runs and releases); tests and System use it.
func (c *Collector) evidenceSynced() bool {
	if _, ok := c.evSource.Load().(string); !ok {
		return false
	}
	for _, inf := range c.optionalInformers(c.eventsSource()) {
		if !inf.HasSynced() {
			return false
		}
	}
	return true
}

// whenSynced runs fn once every running optional watch of these groups has listed.
func (c *Collector) whenSynced(ctx context.Context, groups []string, eventsSource string, fn func()) {
	var syncd []cache.InformerSynced
	for _, o := range c.opt {
		for _, g := range groups {
			if o.group != g || (g == "events-v1" && eventsSource != store.SourceEventsV1) || (g == "events-core" && eventsSource != store.SourceCoreV1) {
				continue
			}
			syncd = append(syncd, o.inf.HasSynced)
		}
	}
	if len(syncd) == 0 {
		return
	}
	go func() {
		if cache.WaitForCacheSync(ctx.Done(), syncd...) {
			fn()
		}
	}()
}

// optionalInformers are the optional watches that run, given which Events API is read.
func (c *Collector) optionalInformers(eventsSource string) []cache.SharedIndexInformer {
	var out []cache.SharedIndexInformer
	for _, o := range c.opt {
		if (o.group == "events-v1" && eventsSource != store.SourceEventsV1) || (o.group == "events-core" && eventsSource != store.SourceCoreV1) {
			continue
		}
		out = append(out, o.inf)
	}
	return out
}

// eventsSource is the API Events are read through, once Run has chosen it.
func (c *Collector) eventsSource() string {
	if v, ok := c.evSource.Load().(string); ok {
		return v
	}
	return c.pickEventsSource()
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
