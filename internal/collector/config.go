package collector

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"

	"github.com/mihnk/opstide/internal/release"
	"github.com/mihnk/opstide/internal/store"
)

// ConfigMaps and Secrets: when one is created, deleted or written, and by which field manager, the same way the change
// ledger records spec writes (managers.go). What is kept is never a value:
//
//   - A ConfigMap is read through the ordinary API (its content is needed to tell a content change from a metadata
//     change). Every value is replaced by an HMAC (under a key made once per install and kept in the database, never
//     served) as the object arrives: in the list function, one page of at most configPage objects at a time, and in the
//     informer's transform for each watch event. The record keeps the HMAC of the whole content, one per key, and the
//     names of its keys.
//   - A Secret is read through the metadata-only API (PartialObjectMetadata): its data is never requested. Its metadata
//     still arrives with annotations (kubectl's last-applied-configuration holds the whole Secret, data included) and
//     managedFields (which name its keys); both are dropped as they arrive, by the same list function and transform,
//     and never cached or kept. What a Secret's write touched is told only as "its manager's fields include data or not".
//
// Both are optional watches with their own coverage scopes ("configmaps", "secrets") and their own queue and worker: a
// relist of thousands of ConfigMaps must not delay a Job, or a Kubernetes Event, being recorded.
const (
	ConfigMaps = "configmaps"
	Secrets    = "secrets"
)

// secretsGVR is the only resource the metadata client is ever asked for.
var secretsGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

const (
	// annContentHash, annLeader, annHelm and annTrimmed are set by the trimming on the cached copy only.
	annContentHash = "opstide.mihnk.org/content-hash"
	annLeader      = "opstide.mihnk.org/leader-election"
	annHelm        = "opstide.mihnk.org/helm-release"
	annTrimmed     = "opstide.mihnk.org/trimmed"
	// annConfigRefs carries, on a trimmed StatefulSet or DaemonSet, the ConfigMaps and Secrets its Pod template names.
	annConfigRefs = "opstide.mihnk.org/config-refs"
	// leaderAnnotation marks a ConfigMap used as a leader-election lock: its annotation is rewritten every few seconds.
	leaderAnnotation = "control-plane.alpha.kubernetes.io/leader"
	// helmReleasePrefix and the label owner=helm mark Helm's release records (one per revision of a release).
	helmReleasePrefix = "sh.helm.release.v1."
	// configWriteEvery is the least time between two write entries of one object. A ConfigMap some controller rewrites
	// every few seconds (an autoscaler's status) would otherwise fill the record; the writes in between are folded into
	// the next entry, which says how many observations it stands for and is counted with the routine volume.
	configWriteEvery = 5 * time.Minute
	// configPage is how many ConfigMaps or Secrets one list request asks for: what is held before trimming.
	configPage = 500
)

// configWord is the entry-type prefix of a config kind.
func configWord(kind string) (word, k8sKind string) {
	if kind == Secrets {
		return "secret", store.KindSecret
	}
	return "configmap", store.KindConfigMap
}

// classOfFields says whether a managedFields entry's field set includes data ("data"), only metadata ("metadata"), or
// neither (""). It says what a manager owns, not what its latest write changed.
func classOfFields(f *metav1.FieldsV1) string {
	if f == nil || len(f.Raw) == 0 {
		return ""
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(f.Raw, &top) != nil {
		return ""
	}
	for _, k := range []string{"f:data", "f:binaryData", "f:stringData", "f:immutable", "f:type"} {
		if _, ok := top[k]; ok {
			return "data"
		}
	}
	if _, ok := top["f:metadata"]; ok {
		return "metadata"
	}
	return ""
}

// slimConfigFields keeps, of managedFields, only a marker of what each entry owns, never the field set (which names a
// Secret's keys).
func slimConfigFields(mf []metav1.ManagedFieldsEntry) []metav1.ManagedFieldsEntry {
	var out []metav1.ManagedFieldsEntry
	for _, e := range mf {
		c := classOfFields(e.FieldsV1)
		if c == "" {
			continue
		}
		e.FieldsV1 = &metav1.FieldsV1{Raw: []byte(`{"f:` + c + `":{}}`)}
		out = append(out, e)
	}
	return out
}

// configTrim reduces ConfigMaps and Secrets' metadata to what OpsTide keeps. mark tells an object it already trimmed
// (a random value per process, so an annotation from the cluster can never pass for it).
type configTrim struct {
	key  []byte
	mark string
}

func newConfigTrim() *configTrim {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return &configTrim{mark: hex.EncodeToString(b)}
}

// meta is the identity a cached ConfigMap or Secret keeps. Labels, annotations (last-applied-configuration among them)
// and the field sets of managedFields are dropped.
func (t *configTrim) meta(m metav1.ObjectMeta) metav1.ObjectMeta {
	out := metav1.ObjectMeta{Name: m.Name, Namespace: m.Namespace, UID: m.UID, ResourceVersion: m.ResourceVersion,
		CreationTimestamp: m.CreationTimestamp, DeletionTimestamp: m.DeletionTimestamp, ManagedFields: slimConfigFields(m.ManagedFields),
		Annotations: map[string]string{annTrimmed: t.mark}}
	if _, ok := m.Annotations[leaderAnnotation]; ok {
		out.Annotations[annLeader] = "true"
	}
	if m.Labels["owner"] == "helm" && strings.HasPrefix(m.Name, helmReleasePrefix) {
		out.Annotations[annHelm] = "true"
	}
	return out
}

func (t *configTrim) done(m metav1.Object) bool { return m.GetAnnotations()[annTrimmed] == t.mark }

// mac is the HMAC-SHA256 of the parts, separated by NUL, under the install's key.
func (t *configTrim) mac(parts ...string) hashWriter {
	h := hashWriter{hmac.New(sha256.New, t.key)}
	for _, p := range parts {
		h.add(p)
	}
	return h
}

type hashWriter struct{ h hash.Hash }

func (h hashWriter) add(p string) {
	_, _ = h.h.Write([]byte(p))
	_, _ = h.h.Write([]byte{0})
}

func (h hashWriter) hex() string { return hex.EncodeToString(h.h.Sum(nil)) }

// configMap keeps a ConfigMap's identity and, in place of its values, one HMAC per key and an HMAC of the whole content
// (the sorted keys and values). The values are dropped here, before the object is cached.
func (t *configTrim) configMap(obj any) (any, error) {
	cm, ok := obj.(*corev1.ConfigMap)
	if !ok {
		return nil, fmt.Errorf("ConfigMap watch: unexpected %T", obj)
	}
	if t.done(cm) {
		return cm, nil
	}
	if len(t.key) == 0 {
		return nil, fmt.Errorf("ConfigMap watch: no hash key")
	}
	values := make(map[string]string, len(cm.Data)+len(cm.BinaryData))
	for k, v := range cm.Data {
		values[k] = v
	}
	for k, v := range cm.BinaryData {
		values[k] = string(v)
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	all := t.mac()
	perKey := make(map[string]string, len(keys))
	for _, k := range keys {
		all.add(k)
		all.add(values[k])
		perKey[k] = t.mac(k, values[k]).hex()[:32]
	}
	meta := t.meta(cm.ObjectMeta)
	meta.Annotations[annContentHash] = all.hex()
	return &corev1.ConfigMap{ObjectMeta: meta, Data: perKey}, nil
}

// secretMeta keeps a Secret's identity and the markers of its managedFields. It only ever sees metadata.
func (t *configTrim) secretMeta(obj any) (any, error) {
	p, ok := obj.(*metav1.PartialObjectMetadata)
	if !ok {
		return nil, fmt.Errorf("Secret metadata watch: unexpected %T", obj)
	}
	if t.done(p) {
		return p, nil
	}
	return &metav1.PartialObjectMetadata{TypeMeta: p.TypeMeta, ObjectMeta: t.meta(p.ObjectMeta)}, nil
}

// pageOptions makes a ConfigMap or Secret list come in pages of at most configPage, from etcd (ResourceVersion ""), so
// only one page of full objects is ever held before it is trimmed. The watch cache would answer the first list
// (ResourceVersion "0") in one piece whatever the limit, holding every value of the cluster at once. The cost is a
// quorum read on the API server at each (re)list instead of an answer from its cache.
func pageOptions(o *metav1.ListOptions) {
	if o.Continue == "" {
		o.ResourceVersion, o.ResourceVersionMatch = "", ""
	}
	if o.Limit <= 0 || o.Limit > configPage {
		o.Limit = configPage
	}
}

// addConfigInformers makes the ConfigMap and Secret watches of a scope, as asked for. Streaming lists (WatchListClient)
// are not used by any OpsTide watch (trackedLW), so a list is always the paged one above.
func (c *Collector) addConfigInformers(s *scope, ns string) {
	if c.watchConfigMaps {
		core := c.client.CoreV1()
		inf := c.optionalInformer(ns, ConfigMaps, &corev1.ConfigMap{}, nil,
			func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
				pageOptions(&o)
				l, err := core.ConfigMaps(ns).List(ctx, o)
				if err != nil {
					return nil, err
				}
				for i := range l.Items {
					t, err := c.trim.configMap(&l.Items[i])
					if err != nil {
						return nil, err
					}
					l.Items[i] = *t.(*corev1.ConfigMap)
				}
				return l, nil
			},
			func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
				return core.ConfigMaps(ns).Watch(ctx, o)
			}, ConfigMaps)
		if c.trimmedOrDropped(inf, ns, ConfigMaps, c.trim.configMap) {
			s.configMaps = inf
			c.handleConfig(inf, ConfigMaps)
		}
	}
	if c.watchSecrets && c.md != nil {
		// Metadata only: the request asks the API server for PartialObjectMetadata, so Secret data is never requested.
		// Annotations (last-applied-configuration can hold the data) and managedFields (which name the keys) still come
		// with it: the list function and the transform drop them before anything is cached.
		res := c.md.Resource(secretsGVR).Namespace(ns)
		inf := c.optionalInformer(ns, Secrets, &metav1.PartialObjectMetadata{}, nil,
			func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
				pageOptions(&o)
				l, err := res.List(ctx, o)
				if err != nil {
					return nil, err
				}
				for i := range l.Items {
					t, err := c.trim.secretMeta(&l.Items[i])
					if err != nil {
						return nil, err
					}
					l.Items[i] = *t.(*metav1.PartialObjectMetadata)
				}
				return l, nil
			},
			func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
				return res.Watch(ctx, o)
			}, Secrets)
		if c.trimmedOrDropped(inf, ns, Secrets, c.trim.secretMeta) {
			s.secrets = inf
			c.handleConfig(inf, Secrets)
		}
	}
}

// trimmedOrDropped sets the transform of a ConfigMap or Secret watch. A watch whose transform cannot be set would cache
// values (or annotations holding a Secret): it is not started at all, and its scope is shown as not watchable.
func (c *Collector) trimmedOrDropped(inf cache.SharedIndexInformer, ns, kind string, tf cache.TransformFunc) bool {
	err := inf.SetTransform(tf)
	if err == nil {
		return true
	}
	for i, o := range c.opt {
		if o.inf == inf {
			c.opt = append(c.opt[:i], c.opt[i+1:]...)
			break
		}
	}
	c.cov.failed(scopeName(kind, ns), fmt.Errorf("not watched: what arrives could not be reduced before it is kept (%w)", err))
	c.log.Error("a ConfigMap or Secret watch was not started: its transform could not be set", "kind", kind, "namespace", ns, "err", err)
	return false
}

// goneConfig is the last state (trimmed) of a ConfigMap or Secret whose delete the handler saw, kept until the worker
// has recorded it: the informer's cache no longer has it.
type goneConfig struct {
	obj       metav1.Object
	tombstone bool
	deletedAt int64
}

// handleConfig queues every change of a ConfigMap or Secret; a delete keeps the object's last state for the worker.
func (c *Collector) handleConfig(inf cache.SharedIndexInformer, kind string) {
	enqueue := func(obj any, deleted bool) {
		tombstone := false
		if t, ok := obj.(cache.DeletedFinalStateUnknown); ok {
			obj, deleted, tombstone = t.Obj, true, true
		}
		o, ok := obj.(metav1.Object)
		if !ok {
			return
		}
		it := item{kind: kind, namespace: o.GetNamespace(), name: o.GetName()}
		if deleted {
			it.uid, it.tombstone = o.GetUID(), tombstone
			if at := mt(o.GetDeletionTimestamp()); !at.IsZero() {
				it.deletedAt = at.UnixMilli()
			}
			c.cfgGone.Store(it.uid, goneConfig{obj: o, tombstone: tombstone, deletedAt: it.deletedAt})
		}
		c.add(it)
	}
	_, _ = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { enqueue(obj, false) },
		UpdateFunc: func(_, obj any) { enqueue(obj, false) },
		DeleteFunc: func(obj any) { enqueue(obj, true) },
	})
}

// configState is what the collector remembers between two observations of a ConfigMap or Secret: when it last wrote a
// write entry for one, and how many observations were folded since. Both are forgotten when the object goes; the
// writes already handled and the per-key hashes are in its database row, not here.
type configState struct {
	mu      sync.Mutex
	last    map[string]time.Time
	fold    map[string]int
	sweepAt int
	refs    map[string]string // workload UID -> fingerprint of the names last recorded
}

func (s *configState) forget(uid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.last, uid)
	delete(s.fold, uid)
}

func (s *configState) forgetRefs(uid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.refs, uid)
}

// wrote notes a write entry for uid at now. Entries older than every (with nothing folded) are swept now and then.
func (s *configState) wrote(uid string, now time.Time, every time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		s.last, s.fold = map[string]time.Time{}, map[string]int{}
	}
	s.last[uid] = now
	delete(s.fold, uid)
	if len(s.last) > max(s.sweepAt, 10_000) {
		for k, t := range s.last {
			if now.Sub(t) >= every && s.fold[k] == 0 {
				delete(s.last, k)
			}
		}
		s.sweepAt = 2 * len(s.last)
	}
}

// folding counts one more observation into uid's next write entry when its last one is within every, and says how long
// until that entry is due; ok is false when the observation is to be recorded now.
func (s *configState) folding(uid string, now time.Time, every time.Duration) (wait time.Duration, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	last := s.last[uid]
	if last.IsZero() || now.Sub(last) >= every {
		return 0, false
	}
	if s.fold == nil {
		s.fold = map[string]int{}
	}
	s.fold[uid]++
	return every - now.Sub(last), true
}

func (s *configState) folded(uid string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fold[uid]
}

// configWrite is one manager's latest write to a ConfigMap or Secret, and whether its fields include data.
type configWrite struct {
	specWrite
	Class string // data | metadata
}

func (w configWrite) key() string { return w.Manager + "|" + w.Operation + "|" + w.Subresource }

func configWrites(mf []metav1.ManagedFieldsEntry) []configWrite {
	latest := map[string]configWrite{}
	for _, e := range mf {
		class := classOfFields(e.FieldsV1)
		// A write with no time cannot be placed on a timeline, and is left out (as for the change ledger).
		if e.Time == nil || e.Manager == "" || class == "" {
			continue
		}
		w := configWrite{specWrite{Manager: e.Manager, Operation: string(e.Operation), Subresource: e.Subresource, At: e.Time.UTC()}, class}
		if old, ok := latest[w.key()]; !ok || w.At.After(old.At) {
			latest[w.key()] = w
		}
	}
	out := make([]configWrite, 0, len(latest))
	for _, w := range latest {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.Before(out[j].At)
		}
		return out[i].Manager+out[i].Class < out[j].Manager+out[j].Class
	})
	return out
}

// pendingWrites picks, of an object's managedFields writes, the ones to record now, and returns the writes handled so
// far (by manager) with them. A write is left out, but counted as handled, when it is already handled, when it is the
// creation itself, when it is older than cut (retention) or when it is dated before baseline (an object that was already
// there when OpsTide first saw it: what it shows of the past is its starting point, not news). The annotation writes of a
// leader-election lock are left out without being counted, so they never cause a database write.
func pendingWrites(writes []configWrite, seen map[string]int64, created, baseline, cut time.Time, leader bool) (fresh []configWrite, next map[string]int64, changed bool) {
	next = make(map[string]int64, len(seen)+len(writes))
	for k, v := range seen {
		next[k] = v
	}
	for _, w := range writes {
		k, at := w.key(), w.At.Unix()
		if seen[k] >= at {
			continue
		}
		if leader && w.Class == "metadata" {
			continue // a leader-election lock's annotation is rewritten every few seconds; it is not configuration
		}
		next[k], changed = at, true
		switch {
		case !created.IsZero() && !w.At.After(created.Add(2*time.Second)):
		case !cut.IsZero() && w.At.Before(cut):
		case !baseline.IsZero() && !w.At.Add(time.Second).After(baseline): // managedFields times are whole seconds
		default:
			fresh = append(fresh, w)
		}
	}
	return fresh, next, changed
}

func listNames(names []string, n int) string {
	if len(names) > n {
		return strings.Join(names[:n], ", ") + fmt.Sprintf(" and %d more", len(names)-n)
	}
	return strings.Join(names, ", ")
}

func (c *Collector) configInformer(kind, ns string) cache.SharedIndexInformer {
	s := c.scopeFor(ns)
	if s == nil {
		return nil
	}
	if kind == Secrets {
		return s.secrets
	}
	return s.configMaps
}

// retentionCut is the oldest time a write may have and still be recorded: older ones would only be pruned again.
func (c *Collector) retentionCut(now time.Time) time.Time {
	if c.retentionDays <= 0 {
		return time.Time{}
	}
	return now.AddDate(0, 0, -c.retentionDays)
}

// syncConfig records a ConfigMap's or Secret's creation, deletion and writes.
func (c *Collector) syncConfig(ctx context.Context, it item) error {
	inf := c.configInformer(it.kind, it.namespace)
	if inf == nil {
		return nil
	}
	obj, exists, err := inf.GetIndexer().GetByKey(it.namespace + "/" + it.name)
	if err != nil {
		return err
	}
	var meta metav1.Object
	if exists {
		meta, _ = obj.(metav1.Object)
	}
	if meta != nil && it.uid != "" && meta.GetUID() != it.uid {
		meta = nil // replaced by a new object of the same name; its own add is queued
	}
	if meta == nil {
		if it.uid == "" {
			return nil // gone; its delete is queued with its UID
		}
		return c.configGone(ctx, it)
	}
	return c.observeConfig(ctx, it.kind, meta, false)
}

// observeConfig records what is new about a ConfigMap or Secret: its creation, and the writes and content change since
// OpsTide's previous observation. final is set for the last state of an object that is gone: nothing is folded then.
// An observation with nothing to record and nothing to remember writes nothing.
func (c *Collector) observeConfig(ctx context.Context, kindKey string, meta metav1.Object, final bool) error {
	word, kind := configWord(kindKey)
	uid := string(meta.GetUID())
	now := c.now()
	created := mt(ptrTime(meta.GetCreationTimestamp()))
	ann := meta.GetAnnotations()
	_, leader := ann[annLeader]
	_, helm := ann[annHelm]
	hash := ann[annContentHash]
	var keyHashes map[string]string
	if cm, ok := meta.(*corev1.ConfigMap); ok && kind == store.KindConfigMap {
		keyHashes = cm.Data
	} else {
		hash = ""
	}
	keys := make([]string, 0, len(keyHashes))
	for k := range keyHashes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	writes := configWrites(meta.GetManagedFields())
	cut := c.retentionCut(now)

	// Most observations change nothing worth keeping (a relist, a leader lock renewed): answer them with a read only.
	prev, had, err := c.db.ConfigObjectByUID(ctx, uid)
	if err != nil {
		return err
	}
	if had {
		baseline := time.Time{}
		if prev.Origin == store.OriginAdopted {
			baseline = prev.FirstSeenAt
		}
		fresh, _, seenChanged := pendingWrites(writes, prev.WritesSeen, created, baseline, cut, leader)
		sameContent := prev.ContentHash == hash && sameHashes(prev.KeyHashes, keyHashes)
		if len(fresh) == 0 && !seenChanged && sameContent && prev.HelmRelease == helm {
			return nil
		}
		contentChanged := prev.ContentHash != "" && hash != "" && prev.ContentHash != hash
		if !final && (len(fresh) > 0 || contentChanged) {
			// Fold writes that come within cfgEvery of the previous write entry of this object into the next one.
			if wait, ok := c.cfg.folding(uid, now, c.cfgEvery); ok {
				c.queueOf(kindKey).AddAfter(item{kind: kindKey, namespace: meta.GetNamespace(), name: meta.GetName()}, wait)
				return nil
			}
		}
	}

	wrote := false
	if err := c.db.Update(ctx, func(w *store.Writer) error {
		prev, had, err := w.ConfigObject(ctx, uid)
		if err != nil {
			return err
		}
		rec := store.ConfigObject{UID: uid, Kind: kind, Namespace: meta.GetNamespace(), Name: meta.GetName(), CreatedAt: created,
			FirstSeenAt: now, LastSeenAt: now, Origin: store.OriginObserved, ContentHash: hash, Keys: keys, KeyHashes: keyHashes, HelmRelease: helm}
		label := fmt.Sprintf("%s %s/%s", kind, rec.Namespace, rec.Name)
		if helm {
			label = fmt.Sprintf("Helm release record (%s) %s/%s", kind, rec.Namespace, rec.Name)
		}
		base := store.Event{Namespace: rec.Namespace, Kind: kind, ResourceUID: rec.UID, ResourceName: rec.Name, ObservedAt: now, Severity: "info", Confidence: store.Observed}
		var entries []store.Event
		baseline := time.Time{}
		if had {
			rec.FirstSeenAt, rec.Origin, rec.DeletedAt = prev.FirstSeenAt, prev.Origin, prev.DeletedAt
			if prev.Origin == store.OriginAdopted {
				baseline = prev.FirstSeenAt
			}
		} else {
			scope := scopeName(kindKey, c.nsOf(rec.Namespace))
			at := created.Add(time.Second - time.Millisecond) // creation times are whole seconds
			if ok, err := w.WatchedThroughout(ctx, scope, at, at); err != nil {
				return err
			} else if !ok || created.IsZero() {
				// Already there when OpsTide began watching: a baseline, not news. What its managedFields show of the
				// past is part of that baseline.
				rec.Origin, baseline = store.OriginAdopted, now
			} else {
				entries = append(entries, c.createdEntry(base, meta, word, kind, label, helm, created, keys))
			}
		}
		fresh, seen, _ := pendingWrites(writes, prev.WritesSeen, created, baseline, cut, leader)
		rec.WritesSeen = seen
		contentChanged := had && prev.ContentHash != "" && hash != "" && prev.ContentHash != hash
		sameContent := had && prev.ContentHash != "" && prev.ContentHash == hash
		var diff map[string]string
		if contentChanged {
			diff = keyDiff(prev.Keys, keys, prev.KeyHashes, keyHashes)
		}
		writeEntries := c.writeEntries(base, word, kind, label, helm, fresh, c.cfg.folded(uid), contentChanged, sameContent, diff, meta.GetResourceVersion())
		entries = append(entries, writeEntries...)
		for _, e := range entries {
			if _, err := w.AddEvent(ctx, e); err != nil {
				return err
			}
		}
		wrote = len(writeEntries) > 0
		return w.SaveConfigObject(ctx, rec)
	}); err != nil {
		return err
	}
	if wrote {
		c.cfg.wrote(uid, now, c.cfgEvery)
	}
	return nil
}

func sameHashes(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// createdEntry is the creation of a ConfigMap or Secret OpsTide was watching for.
func (c *Collector) createdEntry(base store.Event, meta metav1.Object, word, kind, label string, helm bool, created time.Time, keys []string) store.Event {
	e := base
	e.Type = word + ".created"
	if helm {
		e.Type = word + ".helm_release_created"
	}
	e.ID, e.OccurredAt = e.Type+"/"+base.ResourceUID, created
	e.Summary = label + " created"
	e.Facts = map[string]string{}
	for _, mw := range meta.GetManagedFields() {
		if mw.Time != nil && mw.Manager != "" && !mw.Time.UTC().After(created.Add(2*time.Second)) {
			e.Facts["fieldManager"], e.Facts["operation"] = mw.Manager, string(mw.Operation)
			e.Summary = fmt.Sprintf("%s created by %s (%s)", label, mw.Manager, mw.Operation)
			break
		}
	}
	if kind == store.KindConfigMap {
		e.Facts["keyCount"] = strconv.Itoa(len(keys))
		if len(keys) > 0 {
			e.Facts["keys"] = listNames(keys, 20)
		}
	}
	return e
}

// writeEntries turns the writes to record into entries. A field manager's time moves on any write it makes, so a write
// says who wrote, never what changed; what changed is told only by comparing content with OpsTide's previous observation
// (ConfigMaps), never by the fields a manager owns. For a Secret OpsTide has no content to compare: its write is one
// neutral type, with whether the manager's fields include data.
func (c *Collector) writeEntries(base store.Event, word, kind, label string, helm bool, fresh []configWrite, folded int, contentChanged, sameContent bool, diff map[string]string, rv string) []store.Event {
	typeOf := func(t string) string {
		if helm {
			return word + ".helm_release_written"
		}
		return word + "." + t
	}
	contentFacts := func(f map[string]string) {
		if kind != store.KindConfigMap {
			return
		}
		switch {
		case contentChanged:
			f["contentChanged"] = "yes: keys or values differ from OpsTide's previous observation"
			for k, v := range diff {
				f[k] = v
			}
		case sameContent:
			f["contentChanged"] = "no: the same keys and values as OpsTide's previous observation"
		default:
			f["contentChanged"] = "not known: OpsTide has no earlier observation of its content to compare"
		}
	}
	if len(fresh) == 0 {
		if !contentChanged {
			return nil
		}
		// The content differs and no field manager's dated write explains it: say so; who wrote stays unknown.
		e := base
		e.Type = typeOf("data_changed")
		e.ID = fmt.Sprintf("%s/%s/rv%s", e.Type, base.ResourceUID, rv)
		e.Summary = label + ": content differs from OpsTide's previous observation; no dated write by a field manager was found"
		e.Facts = map[string]string{}
		contentFacts(e.Facts)
		if folded > 0 {
			e.Facts["updatesFolded"] = strconv.Itoa(folded)
		}
		return []store.Event{e}
	}
	entry := func(cw configWrite, typ, summary string) store.Event {
		e := base
		e.Type = typ
		e.ID = fmt.Sprintf("%s/%s/%s/%s/%s/%d", typ, base.ResourceUID, cw.Manager, cw.Operation, cw.Subresource, cw.At.Unix())
		e.OccurredAt = cw.At
		seen := "recorded after OpsTide started watching"
		if cw.At.Add(time.Second).Before(c.started) {
			seen = "made before this OpsTide process began watching; seen when it listed"
		}
		e.Facts = map[string]string{"fieldManager": cw.Manager, "operation": cw.Operation, "writtenAt": cw.At.Format(time.RFC3339), "seen": seen}
		if cw.Subresource != "" {
			e.Facts["subresource"] = cw.Subresource
		}
		e.Summary = summary
		return e
	}
	how := func(cw configWrite) string {
		if cw.Subresource != "" {
			return cw.Operation + ", " + cw.Subresource
		}
		return cw.Operation
	}
	// The write an entry is about when there is one entry for several: the newest one whose manager owns data, else the
	// newest.
	primary := len(fresh) - 1
	for i := len(fresh) - 1; i >= 0; i-- {
		if fresh[i].Class == "data" {
			primary = i
			break
		}
	}
	others := func() string {
		var names []string
		for i, cw := range fresh {
			if i != primary {
				names = append(names, fmt.Sprintf("%s (%s, %s)", cw.Manager, how(cw), cw.At.Format(time.RFC3339)))
			}
		}
		return listNames(names, 10)
	}
	managesData := func(f map[string]string, cw configWrite) {
		if kind == store.KindSecret {
			if cw.Class == "data" {
				f["managesData"] = "yes: this manager's fields include the Secret's data; whether this write changed the data is not known"
			} else {
				f["managesData"] = "no: this manager's fields are metadata only"
			}
		}
	}

	if folded > 0 {
		cw := fresh[primary]
		e := entry(cw, typeOf("writes_folded"), fmt.Sprintf("%s: written again within %s of its previous write entry (%d updates folded); the latest by %s (%s)",
			label, durWords(c.cfgEvery), folded, cw.Manager, how(cw)))
		e.Facts["updatesFolded"] = strconv.Itoa(folded)
		contentFacts(e.Facts)
		managesData(e.Facts, cw)
		if len(fresh) > 1 {
			e.Facts["alsoWrittenBy"] = others()
		}
		return []store.Event{e}
	}
	if kind == store.KindConfigMap && contentChanged {
		var out []store.Event
		for i, cw := range fresh {
			if i == primary {
				e := entry(cw, typeOf("data_written"), fmt.Sprintf("%s: written by %s (%s); keys or values differ from OpsTide's previous observation", label, cw.Manager, how(cw)))
				contentFacts(e.Facts)
				if len(fresh) > 1 {
					e.Facts["alsoWrittenBy"] = others()
				}
				out = append(out, e)
				continue
			}
			e := entry(cw, typeOf("written"), fmt.Sprintf("%s: written by %s (%s)", label, cw.Manager, how(cw)))
			e.Facts["contentChanged"] = "yes, between OpsTide's previous observation and this one; which of the writes in between changed it is not known"
			out = append(out, e)
		}
		return out
	}
	out := make([]store.Event, 0, len(fresh))
	for _, cw := range fresh {
		var e store.Event
		switch {
		case kind == store.KindSecret:
			what := "its manager's fields include data"
			if cw.Class != "data" {
				what = "its manager's fields are metadata only"
			}
			e = entry(cw, typeOf("written"), fmt.Sprintf("%s: written by %s (%s); %s", label, cw.Manager, how(cw), what))
			managesData(e.Facts, cw)
		case sameContent:
			e = entry(cw, typeOf("metadata_written"), fmt.Sprintf("%s: written by %s (%s); keys and values unchanged (labels, annotations or other metadata)", label, cw.Manager, how(cw)))
			contentFacts(e.Facts)
		default:
			e = entry(cw, typeOf("written"), fmt.Sprintf("%s: written by %s (%s)", label, cw.Manager, how(cw)))
			contentFacts(e.Facts)
		}
		out = append(out, e)
	}
	return out
}

func durWords(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%d minutes", int(d/time.Minute))
	}
	return d.String()
}

func ptrTime(t metav1.Time) *metav1.Time { return &t }

// keyDiff names the keys added, removed and (when OpsTide holds the per-key hashes of its previous observation)
// changed. It never holds a value.
func keyDiff(before, after []string, beforeHash, afterHash map[string]string) map[string]string {
	out := map[string]string{}
	in := func(list []string, k string) bool {
		for _, x := range list {
			if x == k {
				return true
			}
		}
		return false
	}
	var added, removed, changed []string
	for _, k := range after {
		if !in(before, k) {
			added = append(added, k)
		}
	}
	for _, k := range before {
		if !in(after, k) {
			removed = append(removed, k)
		}
	}
	if len(added) > 0 {
		out["keysAdded"] = listNames(added, 20)
	}
	if len(removed) > 0 {
		out["keysRemoved"] = listNames(removed, 20)
	}
	if len(beforeHash) == 0 && len(before) > 0 {
		out["keysChanged"] = "not known: OpsTide holds no per-key hashes from its previous observation, so only added and removed keys can be named"
		return out
	}
	for _, k := range after {
		if in(before, k) && beforeHash[k] != afterHash[k] {
			changed = append(changed, k)
		}
	}
	if len(changed) > 0 {
		out["keysChanged"] = listNames(changed, 20)
	}
	return out
}

// configGone records that a ConfigMap or Secret no longer exists. Its last state, when the delete was seen, is
// observed first, so writes waiting to be folded and an object created and deleted before OpsTide recorded it are not
// lost. A delete inferred by a relist, or by the reconciliation at start, is recorded as not seen happening.
func (c *Collector) configGone(ctx context.Context, it item) error {
	word, kind := configWord(it.kind)
	uid := string(it.uid)
	seen, deletedAt := !it.tombstone, it.deletedAt
	var last metav1.Object
	if v, ok := c.cfgGone.Load(it.uid); ok {
		g := v.(goneConfig)
		last, seen, deletedAt = g.obj, !g.tombstone, g.deletedAt
	}
	if last != nil && string(last.GetUID()) == uid {
		if err := c.observeConfig(ctx, it.kind, last, true); err != nil {
			return err
		}
	}
	now := c.now()
	if err := c.db.Update(ctx, func(w *store.Writer) error {
		o, ok, err := w.ConfigObject(ctx, uid)
		if err != nil || !ok || !o.DeletedAt.IsZero() {
			return err
		}
		at := now
		if deletedAt > 0 {
			at = msTime(deletedAt)
		}
		if err := w.MarkConfigDeleted(ctx, o.UID, at); err != nil {
			return err
		}
		return configDeletedEntry(ctx, w, o, word, kind, msTime(deletedAt), seen, now)
	}); err != nil {
		return err
	}
	c.cfgGone.Delete(it.uid)
	c.cfg.forget(uid)
	return nil
}

func configDeletedEntry(ctx context.Context, w *store.Writer, o store.ConfigObject, word, kind string, at time.Time, seen bool, now time.Time) error {
	typ, label := word+".deleted", fmt.Sprintf("%s %s/%s", kind, o.Namespace, o.Name)
	if o.HelmRelease {
		typ, label = word+".helm_release_deleted", fmt.Sprintf("Helm release record (%s) %s/%s", kind, o.Namespace, o.Name)
	}
	e := store.Event{ID: typ + "/" + o.UID, Type: typ, Namespace: o.Namespace, Kind: kind, ResourceUID: o.UID, ResourceName: o.Name,
		OccurredAt: at, ObservedAt: now, Severity: "info", Confidence: store.Observed, Summary: label + " deleted"}
	if !seen {
		e.Confidence, e.OccurredAt = store.Unknown, time.Time{}
		e.Summary = label + " is no longer in the cluster; it was removed while OpsTide was not watching"
	}
	_, err := w.AddEvent(ctx, e)
	return err
}

// reconcileAbsentConfig queues, as deletes not seen happening, the ConfigMaps and Secrets OpsTide knew that are not in
// the listed cache, and notes the others as seen now. The deletes go through the config worker, behind anything the
// watch queued before: a delete the watch saw (its last state is held in cfgGone) is recorded as seen, never as
// removed while OpsTide was not watching.
func (c *Collector) reconcileAbsentConfig(ctx context.Context, kinds ...string) error {
	now := c.now()
	for _, kind := range kinds {
		_, k8sKind := configWord(kind)
		live, err := c.db.LiveConfigObjects(ctx, k8sKind)
		if err != nil {
			return err
		}
		var present []string
		for _, o := range live {
			inf := c.configInformer(kind, o.Namespace)
			if inf == nil || !inf.HasSynced() {
				continue
			}
			obj, exists, err := inf.GetIndexer().GetByKey(o.Namespace + "/" + o.Name)
			if err != nil {
				return err
			}
			if m, ok := obj.(metav1.Object); exists && ok && string(m.GetUID()) == o.UID {
				present = append(present, o.UID)
				continue
			}
			if _, pending := c.cfgGone.Load(types.UID(o.UID)); pending {
				continue // its delete is queued already
			}
			c.add(item{kind: kind, namespace: o.Namespace, name: o.Name, uid: types.UID(o.UID), tombstone: true})
		}
		if err := c.db.TouchConfigObjects(ctx, present, now); err != nil {
			return err
		}
	}
	return nil
}

// ---- which workloads name which ConfigMaps and Secrets ----

// configRefsOf lists the ConfigMaps and Secrets a Pod template names: volumes (projected ones too), envFrom, env
// valueFrom and image pull Secrets. Names only, as the release snapshot already keeps them.
func configRefsOf(t corev1.PodTemplateSpec) []store.ConfigName {
	snap := release.FromTemplate(t, nil)
	var out []store.ConfigName
	for _, n := range snap.ConfigMaps {
		out = append(out, store.ConfigName{Kind: store.KindConfigMap, Name: n})
	}
	for _, n := range snap.Secrets {
		out = append(out, store.ConfigName{Kind: store.KindSecret, Name: n})
	}
	return out
}

// encodeRefs is the annotation a trimmed StatefulSet or DaemonSet carries in place of its Pod template.
func encodeRefs(t corev1.PodTemplateSpec) string {
	b, _ := json.Marshal(configRefsOf(t))
	return string(b)
}

func decodeRefs(ann map[string]string) []store.ConfigName {
	var out []store.ConfigName
	_ = json.Unmarshal([]byte(ann[annConfigRefs]), &out)
	return out
}

// recordConfigRefs saves which watched ConfigMaps and Secrets a workload names, when that changed since last saved.
func (c *Collector) recordConfigRefs(ctx context.Context, uid, kind, ns, name string, refs []store.ConfigName) error {
	if !c.watchConfigMaps && !c.watchSecrets {
		return nil
	}
	var keep []store.ConfigName
	for _, r := range refs {
		if (r.Kind == store.KindConfigMap && c.watchConfigMaps) || (r.Kind == store.KindSecret && c.watchSecrets) {
			keep = append(keep, r)
		}
	}
	b, _ := json.Marshal(keep)
	fp := name + "|" + string(b)
	c.cfg.mu.Lock()
	if c.cfg.refs == nil {
		c.cfg.refs = map[string]string{}
	}
	same := c.cfg.refs[uid] == fp
	c.cfg.mu.Unlock()
	if same {
		return nil
	}
	if err := c.db.Update(ctx, func(w *store.Writer) error {
		return w.SetConfigRefs(ctx, uid, kind, ns, name, keep, c.now())
	}); err != nil {
		return err
	}
	c.cfg.mu.Lock()
	c.cfg.refs[uid] = fp
	c.cfg.mu.Unlock()
	return nil
}

// endConfigRefs ends the references of a workload that is gone, whether or not OpsTide recorded the workload itself.
func (c *Collector) endConfigRefs(ctx context.Context, w *store.Writer, uid string, at time.Time) error {
	c.cfg.forgetRefs(uid)
	return w.EndConfigRefs(ctx, uid, at)
}

// reconcileConfigRefs ends the open references of workloads of these kinds that are no longer in the listed caches:
// deleted while OpsTide was not watching, or never recorded as workloads at all.
func (c *Collector) reconcileConfigRefs(ctx context.Context, kinds ...string) error {
	now := c.now()
	for _, kind := range kinds {
		open, err := c.db.OpenConfigRefWorkloads(ctx, kind)
		if err != nil {
			return err
		}
		for _, wl := range open {
			s := c.scopeFor(wl.Namespace)
			if s == nil {
				continue
			}
			var inf cache.SharedIndexInformer
			switch kind {
			case store.KindDeployment:
				inf = s.deployments
			case store.KindStatefulSet:
				inf = s.statefulSets
			case store.KindDaemonSet:
				inf = s.daemonSets
			case "CronJob":
				inf = s.cronJobs
			}
			if inf == nil || !inf.HasSynced() {
				continue
			}
			obj, exists, err := inf.GetIndexer().GetByKey(wl.Namespace + "/" + wl.Name)
			if err != nil {
				return err
			}
			if m, ok := obj.(metav1.Object); exists && ok && string(m.GetUID()) == wl.ID {
				continue
			}
			if err := c.db.Update(ctx, func(w *store.Writer) error { return c.endConfigRefs(ctx, w, wl.ID, now) }); err != nil {
				return err
			}
		}
	}
	return nil
}
