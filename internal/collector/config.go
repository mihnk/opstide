package collector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
//   - A ConfigMap is read through the ordinary API (its content is needed to tell a data change from a metadata change),
//     and the informer's transform replaces every value with a short hash the moment the object arrives, so no value is
//     held in memory past the decode. The record keeps a hash of the whole content and the names of its keys.
//   - A Secret is read through the metadata-only API (PartialObjectMetadata): its data is never requested, so it never
//     reaches OpsTide. A data write is told from a metadata write by the paths in managedFields (f:data, f:stringData),
//     never by reading the data.
//
// Both are optional watches with their own coverage scopes ("configmaps", "secrets"), and their items wait on the
// low-priority queue: a namespace with thousands of ConfigMaps must not delay a Job being recorded.
const (
	ConfigMaps = "configmaps"
	Secrets    = "secrets"
)

// secretsGVR is the only resource the metadata client is ever asked for.
var secretsGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

const (
	// annContentHash and annLeader are set by the transforms on the cached copy only.
	annContentHash = "opstide.mihnk.org/content-hash"
	annLeader      = "opstide.mihnk.org/leader-election"
	// annConfigRefs carries, on a trimmed StatefulSet or DaemonSet, the ConfigMaps and Secrets its Pod template names.
	annConfigRefs = "opstide.mihnk.org/config-refs"
	// leaderAnnotation marks a ConfigMap used as a leader-election lock: its annotation is rewritten every few seconds.
	leaderAnnotation = "control-plane.alpha.kubernetes.io/leader"
	// configWriteEvery is the least time between two write entries of one object. A ConfigMap some controller rewrites
	// every few seconds (an autoscaler's status) would otherwise fill the record; the writes in between are folded into
	// the next entry, which says how many observations it stands for.
	configWriteEvery = 5 * time.Minute
)

// configWord is the entry-type prefix of a config kind.
func configWord(kind string) (word, k8sKind string) {
	if kind == Secrets {
		return "secret", store.KindSecret
	}
	return "configmap", store.KindConfigMap
}

// classOfFields says whether a managedFields entry wrote data ("data"), only metadata ("metadata"), or neither ("").
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

// slimConfigFields keeps, of managedFields, only a marker of what each entry wrote, never the field set.
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

// configMeta is the identity a cached ConfigMap or Secret keeps.
func configMeta(m metav1.ObjectMeta) metav1.ObjectMeta {
	out := metav1.ObjectMeta{Name: m.Name, Namespace: m.Namespace, UID: m.UID, ResourceVersion: m.ResourceVersion,
		CreationTimestamp: m.CreationTimestamp, DeletionTimestamp: m.DeletionTimestamp, ManagedFields: slimConfigFields(m.ManagedFields)}
	if _, ok := m.Annotations[leaderAnnotation]; ok {
		out.Annotations = map[string]string{annLeader: "true"}
	}
	return out
}

// shortHash is the first 16 hex digits of sha256 over the parts, separated by NUL.
func shortHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// trimConfigMap keeps a ConfigMap's identity and, in place of its values, one hash per key and a hash of the whole
// content (sha256 of the sorted keys and values). The values themselves are dropped here, before the object is cached.
func trimConfigMap(obj any) (any, error) {
	cm, ok := obj.(*corev1.ConfigMap)
	if !ok {
		return obj, nil
	}
	values := map[string]string{}
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
	all := sha256.New()
	perKey := make(map[string]string, len(keys))
	for _, k := range keys {
		all.Write([]byte(k))
		all.Write([]byte{0})
		all.Write([]byte(values[k]))
		all.Write([]byte{0})
		perKey[k] = shortHash(k, values[k])
	}
	meta := configMeta(cm.ObjectMeta)
	if meta.Annotations == nil {
		meta.Annotations = map[string]string{}
	}
	meta.Annotations[annContentHash] = hex.EncodeToString(all.Sum(nil))
	return &corev1.ConfigMap{ObjectMeta: meta, Data: perKey}, nil
}

// trimSecretMeta keeps a Secret's identity and the markers of its managedFields. It only ever sees metadata.
func trimSecretMeta(obj any) (any, error) {
	p, ok := obj.(*metav1.PartialObjectMetadata)
	if !ok {
		return obj, nil
	}
	return &metav1.PartialObjectMetadata{TypeMeta: p.TypeMeta, ObjectMeta: configMeta(p.ObjectMeta)}, nil
}

// addConfigInformers makes the ConfigMap and Secret watches of a scope, as asked for.
func (c *Collector) addConfigInformers(s *scope, ns string) {
	if c.watchConfigMaps {
		core := c.client.CoreV1()
		s.configMaps = c.optionalInformer(ns, ConfigMaps, &corev1.ConfigMap{}, nil,
			func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
				return core.ConfigMaps(ns).List(ctx, o)
			},
			func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
				return core.ConfigMaps(ns).Watch(ctx, o)
			}, ConfigMaps)
		_ = s.configMaps.SetTransform(trimConfigMap)
		c.handle(s.configMaps, true, func(o metav1.Object) (item, bool) {
			return item{kind: ConfigMaps, namespace: o.GetNamespace(), name: o.GetName()}, true
		})
	}
	if c.watchSecrets && c.md != nil {
		// Metadata only: the request asks the API server for PartialObjectMetadata, so a Secret's data is never sent.
		res := c.md.Resource(secretsGVR).Namespace(ns)
		s.secrets = c.optionalInformer(ns, Secrets, &metav1.PartialObjectMetadata{}, nil,
			func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
				return res.List(ctx, o)
			},
			func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
				return res.Watch(ctx, o)
			}, Secrets)
		_ = s.secrets.SetTransform(trimSecretMeta)
		c.handle(s.secrets, true, func(o metav1.Object) (item, bool) {
			return item{kind: Secrets, namespace: o.GetNamespace(), name: o.GetName()}, true
		})
	}
}

// configState is what the collector remembers between two observations of a ConfigMap or Secret: when it last wrote a
// write entry for it, how many observations were folded since, and (ConfigMaps) the per-key hashes it last saw.
type configState struct {
	mu   sync.Mutex
	last map[types.UID]time.Time
	fold map[types.UID]int
	keys map[types.UID]map[string]string
	refs map[string]string // workload UID -> fingerprint of the names last recorded
}

func (s *configState) reset() {
	if s.last == nil || len(s.last) > 50_000 {
		s.last, s.fold, s.keys = map[types.UID]time.Time{}, map[types.UID]int{}, map[types.UID]map[string]string{}
	}
}

// configWrite is one manager's latest write to a ConfigMap or Secret: of its data, or of its metadata only.
type configWrite struct {
	specWrite
	Class string // data | metadata
}

func configWrites(mf []metav1.ManagedFieldsEntry) []configWrite {
	latest := map[string]configWrite{}
	for _, e := range mf {
		class := classOfFields(e.FieldsV1)
		// A write with no time cannot be placed on a timeline, and is left out (as for the change ledger).
		if e.Time == nil || e.Manager == "" || class == "" {
			continue
		}
		w := configWrite{specWrite{Manager: e.Manager, Operation: string(e.Operation), Subresource: e.Subresource, At: e.Time.UTC()}, class}
		k := w.Manager + "|" + w.Operation + "|" + w.Subresource + "|" + class
		if old, ok := latest[k]; !ok || w.At.After(old.At) {
			latest[k] = w
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

func listNames(names []string, n int) string {
	if len(names) > n {
		return strings.Join(names[:n], ", ") + fmt.Sprintf(" and %d more", len(names)-n)
	}
	return strings.Join(names, ", ")
}

// syncConfig records a ConfigMap's or Secret's creation, deletion and writes.
func (c *Collector) syncConfig(ctx context.Context, it item) error {
	s := c.scopeFor(it.namespace)
	if s == nil {
		return nil
	}
	inf := s.configMaps
	if it.kind == Secrets {
		inf = s.secrets
	}
	if inf == nil {
		return nil
	}
	word, kind := configWord(it.kind)
	obj, exists, err := inf.GetIndexer().GetByKey(it.namespace + "/" + it.name)
	if err != nil {
		return err
	}
	var meta metav1.Object
	var perKey map[string]string
	if exists {
		switch o := obj.(type) {
		case *corev1.ConfigMap:
			meta, perKey = o, o.Data
		case *metav1.PartialObjectMetadata:
			meta = o
		}
	}
	if meta != nil && it.uid != "" && meta.GetUID() != it.uid {
		meta = nil // replaced by a new object of the same name; its own add is queued
	}
	if meta == nil {
		if it.uid == "" {
			return nil
		}
		return c.configGone(ctx, it, word, kind)
	}
	uid := meta.GetUID()
	now := c.now()
	created := mt(ptrTime(meta.GetCreationTimestamp()))
	_, leader := meta.GetAnnotations()[annLeader]
	hash := meta.GetAnnotations()[annContentHash]
	keys := make([]string, 0, len(perKey))
	for k := range perKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Writes not yet recorded. A write at the object's creation is the creation itself, not a later write.
	var fresh []configWrite
	var seenKeys []string
	for _, w := range configWrites(meta.GetManagedFields()) {
		k := fmt.Sprintf("%s/%s/%s/%s/%s/%s/%d", word, uid, w.Class, w.Manager, w.Operation, w.Subresource, w.At.Unix())
		if c.written.has(k) {
			continue
		}
		seenKeys = append(seenKeys, k)
		if !created.IsZero() && !w.At.After(created.Add(2*time.Second)) {
			continue
		}
		if w.Class == "metadata" && leader {
			continue // a leader-election lock's annotation is rewritten every few seconds; it is not configuration
		}
		fresh = append(fresh, w)
	}

	// Fold writes that come within configWriteEvery of the previous write entry of this object into the next one.
	c.cfg.mu.Lock()
	c.cfg.reset()
	last, folded := c.cfg.last[uid], c.cfg.fold[uid]
	prevKeys := c.cfg.keys[uid]
	c.cfg.mu.Unlock()
	if len(fresh) > 0 && !last.IsZero() && now.Sub(last) < c.cfgEvery {
		c.cfg.mu.Lock()
		c.cfg.fold[uid]++
		c.cfg.mu.Unlock()
		c.queueOf(it.kind).AddAfter(item{kind: it.kind, namespace: it.namespace, name: it.name}, c.cfgEvery-now.Sub(last))
		return nil
	}

	wrote := false
	if err := c.db.Update(ctx, func(w *store.Writer) error {
		prev, had, err := w.ConfigObject(ctx, string(uid))
		if err != nil {
			return err
		}
		rec := store.ConfigObject{UID: string(uid), Kind: kind, Namespace: meta.GetNamespace(), Name: meta.GetName(), CreatedAt: created,
			FirstSeenAt: now, LastSeenAt: now, Origin: store.OriginObserved, ContentHash: hash, Keys: keys}
		if kind == store.KindSecret {
			rec.Keys = nil // a Secret's keys are in its data, which OpsTide does not read
		}
		label := fmt.Sprintf("%s %s/%s", kind, rec.Namespace, rec.Name)
		base := store.Event{Namespace: rec.Namespace, Kind: kind, ResourceUID: rec.UID, ResourceName: rec.Name, ObservedAt: now, Severity: "info", Confidence: store.Observed}
		var entries []store.Event
		if had {
			rec.FirstSeenAt, rec.Origin = prev.FirstSeenAt, prev.Origin
		} else {
			scope := scopeName(it.kind, c.nsOf(rec.Namespace))
			at := created.Add(time.Second - time.Millisecond) // creation times are whole seconds
			if ok, err := w.WatchedThroughout(ctx, scope, at, at); err != nil {
				return err
			} else if !ok || created.IsZero() {
				rec.Origin = store.OriginAdopted // already there when OpsTide began watching: a baseline, not news
			} else {
				e := base
				e.ID, e.Type, e.OccurredAt = word+".created/"+rec.UID, word+".created", created
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
				entries = append(entries, e)
			}
		}

		// What changed in a ConfigMap's content since OpsTide last saw it (names of keys only).
		var diff map[string]string
		contentChanged := had && kind == store.KindConfigMap && prev.ContentHash != "" && hash != "" && prev.ContentHash != hash
		if contentChanged {
			diff = keyDiff(prev.Keys, keys, prevKeys, perKey)
		}
		newestData := -1
		for _, cw := range fresh {
			how := cw.Operation
			if cw.Subresource != "" {
				how += ", " + cw.Subresource
			}
			seen := "recorded after OpsTide started watching"
			if cw.At.Add(time.Second).Before(c.started) {
				seen = "already there when OpsTide began watching"
			}
			e := base
			e.Type = word + "." + cw.Class + "_written"
			e.ID = fmt.Sprintf("%s/%s/%s/%s/%s/%d", e.Type, rec.UID, cw.Manager, cw.Operation, cw.Subresource, cw.At.Unix())
			e.OccurredAt = cw.At
			e.Facts = map[string]string{"fieldManager": cw.Manager, "operation": cw.Operation, "writtenAt": cw.At.Format(time.RFC3339), "seen": seen}
			if cw.Subresource != "" {
				e.Facts["subresource"] = cw.Subresource
			}
			if folded > 0 {
				e.Facts["updatesFolded"] = strconv.Itoa(folded)
			}
			if cw.Class == "data" {
				e.Summary = fmt.Sprintf("%s: data written by %s (%s)", label, cw.Manager, how)
				newestData = len(entries)
			} else {
				e.Summary = fmt.Sprintf("%s: labels or annotations written by %s (%s)", label, cw.Manager, how)
			}
			entries = append(entries, e)
		}
		if kind == store.KindConfigMap && had && newestData >= 0 {
			e := &entries[newestData]
			if contentChanged {
				e.Facts["contentChanged"] = "yes, since OpsTide's previous observation"
				for k, v := range diff {
					e.Facts[k] = v
				}
			} else if prev.ContentHash != "" && prev.ContentHash == hash {
				e.Facts["contentChanged"] = "no: the same keys and values as OpsTide's previous observation"
			}
		}
		if contentChanged && newestData < 0 {
			// The content differs and no field manager's dated write explains it: say so, and who wrote stays unknown.
			e := base
			e.Type, e.ID = word+".data_changed", fmt.Sprintf("%s.data_changed/%s/%s", word, rec.UID, hash[:16])
			e.Summary = label + ": content differs from OpsTide's previous observation; no dated write by a field manager was found"
			e.Facts = map[string]string{"contentChanged": "yes, since OpsTide's previous observation"}
			for k, v := range diff {
				e.Facts[k] = v
			}
			entries = append(entries, e)
		}
		for _, e := range entries {
			if _, err := w.AddEvent(ctx, e); err != nil {
				return err
			}
			if strings.HasSuffix(e.Type, "_written") || strings.HasSuffix(e.Type, "data_changed") {
				wrote = true
			}
		}
		return w.SaveConfigObject(ctx, rec)
	}); err != nil {
		return err
	}
	c.written.add(seenKeys)
	c.cfg.mu.Lock()
	if wrote {
		c.cfg.last[uid] = now
		delete(c.cfg.fold, uid)
	}
	if perKey != nil {
		c.cfg.keys[uid] = perKey
	}
	c.cfg.mu.Unlock()
	return nil
}

func ptrTime(t metav1.Time) *metav1.Time { return &t }

// keyDiff names the keys added, removed and (when OpsTide holds the per-key hashes of its previous observation in this
// process) changed. It never holds a value.
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
	if beforeHash == nil {
		out["keysChanged"] = "not known: OpsTide restarted since its previous observation, so only added and removed keys can be named"
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

// configGone records that a ConfigMap or Secret no longer exists. A delete inferred by a relist is recorded as not
// seen happening.
func (c *Collector) configGone(ctx context.Context, it item, word, kind string) error {
	now := c.now()
	return c.db.Update(ctx, func(w *store.Writer) error {
		o, ok, err := w.ConfigObject(ctx, string(it.uid))
		if err != nil || !ok || !o.DeletedAt.IsZero() {
			return err
		}
		at := now
		if it.deletedAt > 0 {
			at = msTime(it.deletedAt)
		}
		if err := w.MarkConfigDeleted(ctx, o.UID, at); err != nil {
			return err
		}
		return c.configDeletedEntry(ctx, w, o, word, kind, msTime(it.deletedAt), !it.tombstone, now)
	})
}

func (c *Collector) configDeletedEntry(ctx context.Context, w *store.Writer, o store.ConfigObject, word, kind string, at time.Time, seen bool, now time.Time) error {
	e := store.Event{ID: word + ".deleted/" + o.UID, Type: word + ".deleted", Namespace: o.Namespace, Kind: kind, ResourceUID: o.UID, ResourceName: o.Name,
		OccurredAt: at, ObservedAt: now, Severity: "info", Confidence: store.Observed, Summary: fmt.Sprintf("%s %s/%s deleted", kind, o.Namespace, o.Name)}
	if !seen {
		e.Confidence = store.Unknown
		e.Summary = fmt.Sprintf("%s %s/%s is no longer in the cluster; it was removed while OpsTide was not watching", kind, o.Namespace, o.Name)
	}
	_, err := w.AddEvent(ctx, e)
	return err
}

// reconcileAbsentConfig records the ConfigMaps and Secrets OpsTide knew that went while it was not watching.
func (c *Collector) reconcileAbsentConfig(ctx context.Context, kinds ...string) error {
	now := c.now()
	for _, kind := range kinds {
		word, k8sKind := configWord(kind)
		live, err := c.db.LiveConfigObjects(ctx, k8sKind)
		if err != nil {
			return err
		}
		for _, o := range live {
			s := c.scopeFor(o.Namespace)
			if s == nil {
				continue
			}
			inf := s.configMaps
			if kind == Secrets {
				inf = s.secrets
			}
			if inf == nil || !inf.HasSynced() {
				continue
			}
			obj, exists, err := inf.GetIndexer().GetByKey(o.Namespace + "/" + o.Name)
			if err != nil {
				return err
			}
			if m, ok := obj.(metav1.Object); exists && ok && string(m.GetUID()) == o.UID {
				continue
			}
			if err := c.db.Update(ctx, func(w *store.Writer) error {
				if err := w.MarkConfigDeleted(ctx, o.UID, now); err != nil {
					return err
				}
				return c.configDeletedEntry(ctx, w, o, word, k8sKind, time.Time{}, false, now)
			}); err != nil {
				return err
			}
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
	if c.cfg.refs == nil || len(c.cfg.refs) > 50_000 {
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

var (
	_ cache.TransformFunc = trimConfigMap
	_ cache.TransformFunc = trimSecretMeta
)
