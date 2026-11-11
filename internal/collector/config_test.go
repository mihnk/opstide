package collector

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	metafake "k8s.io/client-go/metadata/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/mihnk/opstide/internal/store"
)

const secretValue = "s3cr3t-do-not-store" // appears in no object the collector is allowed to keep

func configMap(ns, name, uid string, created time.Time, data map[string]string, mfs ...metav1.ManagedFieldsEntry) *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID(uid), ResourceVersion: "1",
		CreationTimestamp: metav1.NewTime(created), ManagedFields: mfs}, Data: data}
}

func secretMeta(ns, name, uid string, created time.Time, mfs ...metav1.ManagedFieldsEntry) *metav1.PartialObjectMetadata {
	return &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID(uid), ResourceVersion: "1", CreationTimestamp: metav1.NewTime(created), ManagedFields: mfs}}
}

// configRun is a running collector whose ConfigMap (and, with a metadata client, Secret) watches are fake watchers, so
// updates are delivered as Kubernetes would send them.
type configRun struct {
	c        *Collector
	ctx      context.Context
	client   *fake.Clientset
	cm, sec  *watch.FakeWatcher
	md       *metafake.FakeMetadataClient
	cmLists  atomic.Int32
	cmListed atomic.Value // metav1.ListOptions of the first ConfigMap list
}

func runConfig(t *testing.T, db *store.Store, opt Options, secrets []runtime.Object, objs ...runtime.Object) *configRun {
	t.Helper()
	r := &configRun{client: fake.NewClientset(objs...), cm: watch.NewFakeWithChanSize(20, false), sec: watch.NewFakeWithChanSize(20, false)}
	r.client.Resources = []*metav1.APIResourceList{{GroupVersion: "events.k8s.io/v1", APIResources: []metav1.APIResource{{Name: "events", Kind: "Event"}}}}
	r.client.PrependWatchReactor("configmaps", func(k8stesting.Action) (bool, watch.Interface, error) { return true, r.cm, nil })
	r.client.PrependReactor("list", "configmaps", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if r.cmLists.Add(1) == 1 {
			r.cmListed.Store(a.(k8stesting.ListActionImpl).ListOptions)
		}
		return false, nil, nil
	})
	if opt.WatchSecretMetadata {
		scheme := runtime.NewScheme()
		if err := metav1.AddMetaToScheme(scheme); err != nil {
			t.Fatal(err)
		}
		r.md = metafake.NewSimpleMetadataClient(scheme, secrets...)
		r.md.PrependWatchReactor("secrets", func(k8stesting.Action) (bool, watch.Interface, error) { return true, r.sec, nil })
		opt.Metadata = r.md
	}
	r.c = New(r.client, db, opt)
	ctx, cancel := context.WithCancel(context.Background())
	r.ctx = ctx
	done := make(chan struct{})
	go func() { defer close(done); _ = r.c.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	eventually(t, "synced", func() bool { return r.c.Synced(ctx) == nil })
	eventually(t, "optional watches", r.c.evidenceSynced)
	return r
}

func configEntries(t *testing.T, db *store.Store, prefix string) []store.Event {
	t.Helper()
	pg, err := db.Events(context.Background(), store.EventQuery{Types: []string{prefix}, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	return pg.Events
}

func byType(es []store.Event, typ string) []store.Event {
	var out []store.Event
	for _, e := range es {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// noTrace fails if any of the needles is in an entry, or anywhere in the database's files (the WAL included).
func noTrace(t *testing.T, db *store.Store, dir string, needles ...string) {
	t.Helper()
	for _, e := range append(configEntries(t, db, "configmap."), configEntries(t, db, "secret.")...) {
		b, _ := json.Marshal(e)
		for _, n := range needles {
			if bytes.Contains(b, []byte(n)) {
				t.Fatalf("%q in an entry: %s", n, b)
			}
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, n := range needles {
			if bytes.Contains(b, []byte(n)) {
				t.Fatalf("%q is in %s", n, f)
			}
		}
	}
}

// noTraceInCache fails if any of the needles is in an object an informer holds.
func noTraceInCache(t *testing.T, objs []any, needles ...string) {
	t.Helper()
	for _, o := range objs {
		b, _ := json.Marshal(o)
		for _, n := range needles {
			if bytes.Contains(b, []byte(n)) {
				t.Fatalf("%q in the cache: %s", n, b)
			}
		}
	}
}

func dbIn(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(context.Background(), filepath.Join(dir, "opstide.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, dir
}

// plainHash is the unsalted content hash the earlier build kept: it must be nowhere now.
func plainHash(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k + "\x00" + values[k] + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// A ConfigMap's creation, writes (with the field manager) and deletion are recorded; the content is told apart by an
// HMAC, the keys that changed are named, and no value or plain hash is ever kept. What an object already showed when
// OpsTide first saw it is a baseline, not news.
func TestConfigMapChangesAreRecordedWithoutValues(t *testing.T) {
	db, dir := dbIn(t)
	old := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	small := map[string]string{"LOG_LEVEL": "info", "DB_PASSWORD": secretValue}
	adopted := configMap("shop", "api-config", "cm-1", old, small,
		mf("helm", metav1.ManagedFieldsOperationUpdate, "", `{"f:data":{"f:LOG_LEVEL":{}}}`, old),
		mf("argocd-controller", metav1.ManagedFieldsOperationApply, "", `{"f:data":{"f:LOG_LEVEL":{}}}`, old.Add(time.Hour)))
	r := runConfig(t, db, Options{WatchConfigMaps: true, RetentionDays: 30, configWriteEvery: time.Millisecond}, nil, adopted)
	eventually(t, "the baseline row", func() bool { _, ok, _ := db.ConfigObjectByUID(r.ctx, "cm-1"); return ok })
	if es := configEntries(t, db, "configmap."); len(es) != 0 {
		t.Fatalf("a ConfigMap already there is a baseline: neither its creation nor its earlier writes are news: %+v", es)
	}
	// The first list came in pages, from etcd: the watch cache would have answered with every value at once.
	if o, _ := r.cmListed.Load().(metav1.ListOptions); o.Limit != configPage || o.ResourceVersion != "" {
		t.Fatalf("%+v", o)
	}

	// The data is changed by kubectl-edit: one value changed, a key added.
	now := time.Now().UTC().Add(time.Second).Truncate(time.Second)
	changed := configMap("shop", "api-config", "cm-1", old, map[string]string{"LOG_LEVEL": "debug", "DB_PASSWORD": secretValue, "FEATURE_X": "on"},
		mf("helm", metav1.ManagedFieldsOperationUpdate, "", `{"f:data":{"f:LOG_LEVEL":{}}}`, old),
		mf("argocd-controller", metav1.ManagedFieldsOperationApply, "", `{"f:data":{"f:LOG_LEVEL":{}}}`, old.Add(time.Hour)),
		mf("kubectl-edit", metav1.ManagedFieldsOperationUpdate, "", `{"f:data":{"f:LOG_LEVEL":{},"f:FEATURE_X":{}}}`, now))
	changed.ResourceVersion = "2"
	r.cm.Modify(changed)
	eventually(t, "the kubectl write", func() bool { return len(byType(configEntries(t, db, "configmap."), "configmap.data_written")) == 1 })
	edit := byType(configEntries(t, db, "configmap."), "configmap.data_written")[0]
	if edit.Facts["fieldManager"] != "kubectl-edit" || !strings.HasPrefix(edit.Facts["contentChanged"], "yes") || edit.Facts["keysAdded"] != "FEATURE_X" ||
		edit.Facts["keysChanged"] != "LOG_LEVEL" || edit.Facts["seen"] != "recorded after OpsTide started watching" {
		t.Fatalf("the keys that changed are named (names, not values): %+v", edit.Facts)
	}
	if strings.Contains(edit.ID, plainHash(changed.Data)[:16]) {
		t.Fatalf("no hash in an ID: %s", edit.ID)
	}

	// Only its labels are written by kubectl-label; helm relabels it too (helm's time moves; its fields include data).
	// Neither changed the content, so neither is a data write.
	meta := changed.DeepCopy()
	meta.ResourceVersion, meta.Labels = "3", map[string]string{"team": "shop"}
	meta.ManagedFields = append(meta.ManagedFields, mf("kubectl-label", metav1.ManagedFieldsOperationUpdate, "", `{"f:metadata":{"f:labels":{"f:team":{}}}}`, now.Add(2*time.Second)))
	meta.ManagedFields[0] = mf("helm", metav1.ManagedFieldsOperationUpdate, "", `{"f:data":{"f:LOG_LEVEL":{}},"f:metadata":{"f:labels":{}}}`, now.Add(2*time.Second))
	r.cm.Modify(meta)
	eventually(t, "the label writes", func() bool {
		return len(byType(configEntries(t, db, "configmap."), "configmap.metadata_written")) == 2
	})
	if n := len(byType(configEntries(t, db, "configmap."), "configmap.data_written")); n != 1 {
		t.Fatalf("a write that left the content as it was is not a data write, whatever fields its manager owns: %d", n)
	}

	// A ConfigMap created while OpsTide watches is a creation, with who made it.
	created := time.Now().UTC().Truncate(time.Second).Add(time.Second)
	fresh := configMap("shop", "flags", "cm-2", created, map[string]string{"a": secretValue},
		mf("argocd-controller", metav1.ManagedFieldsOperationApply, "", `{"f:data":{"f:a":{}}}`, created))
	r.cm.Add(fresh)
	eventually(t, "the creation", func() bool { return len(byType(configEntries(t, db, "configmap."), "configmap.created")) == 1 })
	c := byType(configEntries(t, db, "configmap."), "configmap.created")[0]
	if c.Facts["fieldManager"] != "argocd-controller" || c.Facts["keys"] != "a" || !c.OccurredAt.Equal(created) {
		t.Fatalf("%+v", c)
	}

	// Deleted: the entry, and nothing kept of it in memory.
	gone := fresh.DeepCopy()
	gone.ResourceVersion = "9"
	r.cm.Delete(gone)
	eventually(t, "the deletion", func() bool { return len(byType(configEntries(t, db, "configmap."), "configmap.deleted")) == 1 })
	r.c.cfg.mu.Lock()
	_, inLast := r.c.cfg.last["cm-2"]
	r.c.cfg.mu.Unlock()
	if _, pending := r.c.cfgGone.Load(types.UID("cm-2")); inLast || pending {
		t.Fatal("a deleted object's state is forgotten")
	}
	noTrace(t, db, dir, secretValue, plainHash(small), plainHash(changed.Data))
	noTraceInCache(t, r.c.scopeFor("").configMaps.GetStore().List(), secretValue)

	// Every entry is an observed change in the change ledger's sense, and none is routine-volume.
	for _, e := range configEntries(t, db, "configmap.") {
		if store.IsRoutineType(e.Type) || store.GroupOfType(e.Type) != "config" {
			t.Fatalf("%s: routine %v, group %q", e.Type, store.IsRoutineType(e.Type), store.GroupOfType(e.Type))
		}
	}
}

// A ConfigMap a controller rewrites every few seconds does not fill the record: writes inside the interval are folded
// into the next entry, which says so and is counted with the routine volume. A ConfigMap used as a leader-election lock
// costs no write at all when it is renewed.
func TestFrequentConfigMapWritesAreFolded(t *testing.T) {
	db := openStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	cm := configMap("kube-system", "autoscaler-status", "cm-9", now.Add(-time.Hour), map[string]string{"status": "0"},
		mf("cluster-autoscaler", metav1.ManagedFieldsOperationUpdate, "", `{"f:data":{"f:status":{}}}`, now.Add(-time.Minute)))
	lock := configMap("kube-system", "lock", "cm-lock", now.Add(-time.Hour), nil,
		mf("controller", metav1.ManagedFieldsOperationUpdate, "", `{"f:metadata":{"f:annotations":{}}}`, now.Add(-time.Second)))
	lock.Annotations = map[string]string{leaderAnnotation: "{}"}
	r := runConfig(t, db, Options{WatchConfigMaps: true, configWriteEvery: 1500 * time.Millisecond}, nil, cm, lock)
	eventually(t, "the baseline", func() bool { _, ok, _ := db.ConfigObjectByUID(r.ctx, "cm-lock"); return ok })
	eventually(t, "drained", func() bool { return r.c.cfgQueue.Len() == 0 })
	time.Sleep(200 * time.Millisecond) // the reconciliation at start notes every listed object as seen
	before, _, _ := db.ConfigObjectByUID(r.ctx, "cm-lock")
	for i := 1; i <= 4; i++ {
		x := cm.DeepCopy()
		x.ResourceVersion, x.Data = "s"+string(rune('a'+i)), map[string]string{"status": string(rune('0' + i))}
		x.ManagedFields = []metav1.ManagedFieldsEntry{mf("cluster-autoscaler", metav1.ManagedFieldsOperationUpdate, "", `{"f:data":{"f:status":{}}}`, now.Add(time.Duration(i)*time.Second))}
		r.cm.Modify(x)
		l := lock.DeepCopy()
		l.ResourceVersion = "l" + string(rune('a'+i))
		l.ManagedFields = []metav1.ManagedFieldsEntry{mf("controller", metav1.ManagedFieldsOperationUpdate, "", `{"f:metadata":{"f:annotations":{}}}`, now.Add(time.Duration(i)*time.Second))}
		r.cm.Modify(l)
		if i == 1 {
			eventually(t, "the first write", func() bool { return len(configEntries(t, db, "configmap.data_written")) == 1 })
		}
		time.Sleep(100 * time.Millisecond)
	}
	eventually(t, "the folded write", func() bool { return len(configEntries(t, db, "configmap.writes_folded")) == 1 })
	time.Sleep(300 * time.Millisecond)
	es := configEntries(t, db, "configmap.writes_folded")
	if len(es) != 1 || es[0].Facts["updatesFolded"] != "3" || !es[0].OccurredAt.Equal(now.Add(4*time.Second)) || !strings.HasPrefix(es[0].Facts["contentChanged"], "yes") {
		t.Fatalf("one entry for the burst, the latest write, saying it folded others: %+v", es)
	}
	if !store.IsRoutineType("configmap.writes_folded") && !containsStr(store.ConfigRoutineTypes, "configmap.writes_folded") {
		t.Fatal("folded writes are budgeted as routine volume")
	}
	after, _, _ := db.ConfigObjectByUID(r.ctx, "cm-lock")
	if !after.LastSeenAt.Equal(before.LastSeenAt) || len(configEntries(t, db, "configmap.")) != 2 {
		t.Fatalf("a leader lock renewed is neither an entry nor a database write: %v %v", before.LastSeenAt, after.LastSeenAt)
	}
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// A write folded into the next entry is not lost when the object is deleted before that entry is due; an object
// created and deleted before OpsTide recorded it still leaves its creation and deletion.
func TestConfigDeleteKeepsFoldedWritesAndUnseenObjects(t *testing.T) {
	db := openStore(t)
	r := runConfig(t, db, Options{WatchConfigMaps: true, configWriteEvery: time.Hour}, nil)
	created := time.Now().UTC().Truncate(time.Second).Add(time.Second)
	cm := configMap("shop", "flags", "cm-f", created, map[string]string{"a": "1"},
		mf("kubectl-create", metav1.ManagedFieldsOperationUpdate, "", `{"f:data":{"f:a":{}}}`, created))
	r.cm.Add(cm)
	eventually(t, "created", func() bool { return len(configEntries(t, db, "configmap.created")) == 1 })
	w1 := cm.DeepCopy()
	w1.ResourceVersion, w1.Data = "2", map[string]string{"a": "2"}
	w1.ManagedFields = []metav1.ManagedFieldsEntry{mf("kubectl-edit", metav1.ManagedFieldsOperationUpdate, "", `{"f:data":{"f:a":{}}}`, created.Add(3*time.Second))}
	r.cm.Modify(w1)
	eventually(t, "the first write", func() bool { return len(configEntries(t, db, "configmap.data_written")) == 1 })
	w2 := w1.DeepCopy()
	w2.ResourceVersion, w2.Data = "3", map[string]string{"a": "3"}
	w2.ManagedFields = []metav1.ManagedFieldsEntry{mf("kubectl-edit", metav1.ManagedFieldsOperationUpdate, "", `{"f:data":{"f:a":{}}}`, created.Add(5*time.Second))}
	r.cm.Modify(w2)
	eventually(t, "folded", func() bool { return r.c.cfg.folded("cm-f") == 1 })
	r.cm.Delete(w2)
	eventually(t, "deleted", func() bool { return len(configEntries(t, db, "configmap.deleted")) == 1 })
	f := configEntries(t, db, "configmap.writes_folded")
	if len(f) != 1 || !f[0].OccurredAt.Equal(created.Add(5*time.Second)) {
		t.Fatalf("the folded write is recorded from the object's last state: %+v", configEntries(t, db, "configmap."))
	}

	// Created and deleted before the worker recorded it: the delete's last state is all there is.
	at := time.Now().UTC().Truncate(time.Second)
	brief := configMap("shop", "brief", "cm-b", at, map[string]string{"k": secretValue},
		mf("kubectl-create", metav1.ManagedFieldsOperationUpdate, "", `{"f:data":{"f:k":{}}}`, at))
	trimmed, err := r.c.trim.configMap(brief)
	if err != nil {
		t.Fatal(err)
	}
	r.c.cfgGone.Store(types.UID("cm-b"), goneConfig{obj: trimmed.(metav1.Object)})
	r.c.add(item{kind: ConfigMaps, namespace: "shop", name: "brief", uid: "cm-b"})
	eventually(t, "created and deleted", func() bool {
		var n int
		for _, e := range configEntries(t, db, "configmap.") {
			if e.ResourceUID == "cm-b" && (e.Type == "configmap.created" || e.Type == "configmap.deleted") && e.Confidence == store.Observed {
				n++
			}
		}
		return n == 2
	})
}

// Writes older than retention are never recorded, and on a restart nothing already handled comes back.
func TestHistoricConfigWritesAreNotReemitted(t *testing.T) {
	db := openStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	// Seen (not adopted) since 40 days ago: a write 35 days ago is older than the 30 days kept; one 2 hours ago is news.
	if err := db.Update(ctx, func(w *store.Writer) error {
		return w.SaveConfigObject(ctx, store.ConfigObject{UID: "cm-o", Kind: store.KindConfigMap, Namespace: "shop", Name: "old", FirstSeenAt: now.AddDate(0, 0, -40),
			LastSeenAt: now.AddDate(0, 0, -40), Origin: store.OriginObserved})
	}); err != nil {
		t.Fatal(err)
	}
	cm := configMap("shop", "old", "cm-o", now.AddDate(0, 0, -40), map[string]string{"a": "1"},
		mf("helm", metav1.ManagedFieldsOperationUpdate, "", `{"f:data":{"f:a":{}}}`, now.AddDate(0, 0, -35)),
		mf("kubectl-edit", metav1.ManagedFieldsOperationUpdate, "", `{"f:data":{"f:a":{}}}`, now.Add(-2*time.Hour)))
	adopted := configMap("shop", "adopted", "cm-a", now.Add(-24*time.Hour), map[string]string{"a": "1"},
		mf("argocd", metav1.ManagedFieldsOperationApply, "", `{"f:data":{"f:a":{}}}`, now.Add(-time.Hour)))
	for round := 0; round < 2; round++ {
		func() {
			cctx, cancel := context.WithCancel(ctx)
			defer cancel()
			client := fake.NewClientset(cm, adopted)
			client.Resources = []*metav1.APIResourceList{{GroupVersion: "events.k8s.io/v1", APIResources: []metav1.APIResource{{Name: "events", Kind: "Event"}}}}
			c := New(client, db, Options{WatchConfigMaps: true, RetentionDays: 30})
			done := make(chan struct{})
			go func() { defer close(done); _ = c.Run(cctx) }()
			defer func() { cancel(); <-done }()
			eventually(t, "both rows", func() bool {
				a, ok1, _ := db.ConfigObjectByUID(ctx, "cm-a")
				o, ok2, _ := db.ConfigObjectByUID(ctx, "cm-o")
				return ok1 && ok2 && len(a.WritesSeen) == 1 && len(o.WritesSeen) == 2
			})
			eventually(t, "queue drained", func() bool { return c.cfgQueue.Len() == 0 })
			time.Sleep(100 * time.Millisecond)
		}()
		es := configEntries(t, db, "configmap.")
		if len(es) != 1 || es[0].Facts["fieldManager"] != "kubectl-edit" || es[0].ResourceUID != "cm-o" {
			t.Fatalf("round %d: only the write inside retention, of the object OpsTide had seen; nothing again after a restart: %+v", round, es)
		}
	}
}

// Helm's release records are recorded as such: routine volume, and not in the change ledger.
func TestHelmReleaseRecordsAreRoutine(t *testing.T) {
	db := openStore(t)
	r := runConfig(t, db, Options{WatchConfigMaps: true, configWriteEvery: time.Millisecond}, nil)
	at := time.Now().UTC().Truncate(time.Second).Add(time.Second)
	rec := configMap("shop", "sh.helm.release.v1.api.v7", "cm-h", at, map[string]string{"release": secretValue},
		mf("helm", metav1.ManagedFieldsOperationUpdate, "", `{"f:data":{"f:release":{}},"f:metadata":{"f:labels":{}}}`, at))
	rec.Labels = map[string]string{"owner": "helm", "name": "api", "status": "deployed"}
	r.cm.Add(rec)
	eventually(t, "created", func() bool { return len(configEntries(t, db, "configmap.helm_release_created")) == 1 })
	up := rec.DeepCopy()
	up.ResourceVersion, up.Labels["status"] = "2", "superseded"
	up.ManagedFields = []metav1.ManagedFieldsEntry{mf("helm", metav1.ManagedFieldsOperationUpdate, "", `{"f:data":{"f:release":{}},"f:metadata":{"f:labels":{}}}`, at.Add(5*time.Second))}
	r.cm.Modify(up)
	eventually(t, "relabelled", func() bool { return len(configEntries(t, db, "configmap.helm_release_written")) == 1 })
	r.cm.Delete(up)
	eventually(t, "deleted", func() bool { return len(configEntries(t, db, "configmap.helm_release_deleted")) == 1 })
	for _, e := range configEntries(t, db, "configmap.") {
		if !store.IsRoutineType(e.Type) || !containsStr(store.ConfigRoutineTypes, e.Type) {
			t.Fatalf("%s is routine", e.Type)
		}
	}
	// A ConfigMap with the name but not the label is ordinary configuration.
	plain := configMap("shop", "sh.helm.release.v1.lookalike", "cm-l", at, map[string]string{"x": "1"})
	r.cm.Add(plain)
	eventually(t, "an ordinary creation", func() bool { return len(configEntries(t, db, "configmap.created")) == 1 })
}

// Secrets are read through the metadata-only API: the typed client is never asked for a Secret (so their data is never
// sent), and a write is one neutral type that says whether its manager's fields include data, never that data changed.
func TestSecretsAreReadThroughMetadataOnly(t *testing.T) {
	db := openStore(t)
	old := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	sec := secretMeta("shop", "api-db", "sec-1", old.Add(-time.Hour),
		mf("argocd-controller", metav1.ManagedFieldsOperationApply, "", `{"f:data":{"f:password":{}}}`, old),
		mf("kubectl-annotate", metav1.ManagedFieldsOperationUpdate, "", `{"f:metadata":{"f:annotations":{"f:note":{}}}}`, old.Add(time.Minute)))
	r := runConfig(t, db, Options{WatchConfigMaps: true, WatchSecretMetadata: true, configWriteEvery: time.Millisecond}, []runtime.Object{sec})
	eventually(t, "the baseline", func() bool { _, ok, _ := db.ConfigObjectByUID(r.ctx, "sec-1"); return ok })
	if es := configEntries(t, db, "secret."); len(es) != 0 {
		t.Fatalf("what an adopted Secret showed is a baseline: %+v", es)
	}
	now := time.Now().UTC().Truncate(time.Second).Add(time.Second)
	argo := mf("argocd-controller", metav1.ManagedFieldsOperationApply, "", `{"f:data":{"f:password":{}}}`, now)
	x := sec.DeepCopy()
	x.ResourceVersion, x.ManagedFields = "2", []metav1.ManagedFieldsEntry{argo, sec.ManagedFields[1]}
	r.sec.Modify(x)
	y := sec.DeepCopy()
	y.ResourceVersion, y.ManagedFields = "3", []metav1.ManagedFieldsEntry{argo, mf("kubectl-annotate", metav1.ManagedFieldsOperationUpdate, "", `{"f:metadata":{"f:annotations":{"f:note":{}}}}`, now.Add(time.Second))}
	r.sec.Modify(y)
	eventually(t, "the secret writes", func() bool { return len(configEntries(t, db, "secret.written")) == 2 })
	for _, e := range configEntries(t, db, "secret.written") {
		want := map[string]string{"argocd-controller": "yes", "kubectl-annotate": "no"}[e.Facts["fieldManager"]]
		if want == "" || !strings.HasPrefix(e.Facts["managesData"], want) || e.Kind != "Secret" || strings.Contains(e.Summary, "data written") {
			t.Fatalf("%+v", e)
		}
	}
	// The record of a Secret holds no keys and no hash: OpsTide never saw its data.
	o, ok, err := db.ConfigObjectByUID(r.ctx, "sec-1")
	if err != nil || !ok || o.ContentHash != "" || len(o.Keys) != 0 || len(o.KeyHashes) != 0 || o.Kind != "Secret" {
		t.Errorf("%+v %v %v", o, ok, err)
	}
	for _, a := range r.client.Actions() {
		if a.GetResource().Resource == "secrets" {
			t.Fatalf("the typed client was asked for Secrets: %v", a)
		}
	}
	for _, a := range r.md.Actions() {
		if a.GetResource().Resource != "secrets" || (a.GetVerb() != "list" && a.GetVerb() != "watch") {
			t.Fatalf("only list and watch on Secrets' metadata: %v", a)
		}
	}
}

// A Secret's metadata still carries annotations (kubectl's last-applied-configuration holds the whole Secret, its data
// in base64) and managedFields that name its keys. Both are dropped on arrival, in the first list and in every watch
// event: no trace of a value or a key name in the cache, in the database or its WAL.
func TestSecretAnnotationsAndKeyNamesLeaveNoTrace(t *testing.T) {
	db, dir := dbIn(t)
	const keyName = "zq-db-password-key"
	b64 := base64.StdEncoding.EncodeToString([]byte(secretValue))
	lastApplied := `{"apiVersion":"v1","data":{"` + keyName + `":"` + b64 + `"},"kind":"Secret","metadata":{"name":"api-db","namespace":"shop"}}`
	created := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	withSecrets := func(p *metav1.PartialObjectMetadata, at time.Time) *metav1.PartialObjectMetadata {
		p.Annotations = map[string]string{"kubectl.kubernetes.io/last-applied-configuration": lastApplied}
		p.ManagedFields = []metav1.ManagedFieldsEntry{mf("kubectl-client-side-apply", metav1.ManagedFieldsOperationUpdate, "",
			`{"f:data":{".":{},"f:`+keyName+`":{}},"f:metadata":{"f:annotations":{".":{},"f:kubectl.kubernetes.io/last-applied-configuration":{}}},"f:type":{}}`, at)}
		return p
	}
	listed := withSecrets(secretMeta("shop", "api-db", "sec-1", created), created)
	cmListed := configMap("shop", "app", "cm-1", created, map[string]string{"k": secretValue})
	cmListed.Annotations = map[string]string{"kubectl.kubernetes.io/last-applied-configuration": `{"data":{"k":"` + secretValue + `"}}`}
	r := runConfig(t, db, Options{WatchConfigMaps: true, WatchSecretMetadata: true, configWriteEvery: time.Millisecond}, []runtime.Object{listed}, cmListed)

	now := time.Now().UTC().Truncate(time.Second).Add(time.Second)
	added := withSecrets(secretMeta("shop", "api-db-2", "sec-2", now), now)
	r.sec.Add(added)
	upd := withSecrets(secretMeta("shop", "api-db", "sec-1", created), now.Add(time.Second))
	upd.ResourceVersion = "2"
	r.sec.Modify(upd)
	eventually(t, "recorded", func() bool {
		return len(configEntries(t, db, "secret.created")) == 1 && len(configEntries(t, db, "secret.written")) == 1
	})
	cacheObjs := append(r.c.scopeFor("").secrets.GetStore().List(), r.c.scopeFor("").configMaps.GetStore().List()...)
	if len(cacheObjs) != 3 {
		t.Fatalf("%d objects cached", len(cacheObjs))
	}
	needles := []string{secretValue, b64, keyName, "kubectl.kubernetes.io/last-applied"}
	noTraceInCache(t, cacheObjs, needles...)
	r.sec.Delete(upd)
	eventually(t, "deleted", func() bool { return len(configEntries(t, db, "secret.deleted")) == 1 })
	noTrace(t, db, dir, needles...)
}

// The ConfigMaps and Secrets a workload's Pod template names are recorded as references, by name, and end when the
// template stops naming them, or when the workload is gone, recorded as a workload or not.
func TestWorkloadsReferenceTheConfigTheyName(t *testing.T) {
	db := openStore(t)
	ctx := context.Background()
	// A Deployment OpsTide recorded as naming a ConfigMap, gone while OpsTide was not running and never in its workloads.
	if err := db.Update(ctx, func(w *store.Writer) error {
		return w.SetConfigRefs(ctx, "dep-gone", "Deployment", "shop", "gone", []store.ConfigName{{Kind: store.KindConfigMap, Name: "api-config"}}, time.Now().Add(-time.Hour))
	}); err != nil {
		t.Fatal(err)
	}
	d := deployment("1", 1, 1, 1)
	d.Spec.Template.Spec.Containers = []corev1.Container{{Name: "app", Image: "api:1",
		EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "api-config"}}}},
		Env:     []corev1.EnvVar{{Name: "DB", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "api-db"}, Key: "url"}}}}}}
	d.Spec.Template.Spec.Volumes = []corev1.Volume{{Name: "p", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{
		{ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "ca-bundle"}}}}}}}}
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "db", UID: "sts-1", ResourceVersion: "1"},
		Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "c",
			VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "api-config"}}}}}}}}}
	client, ctx := start(t, db, Options{WatchConfigMaps: true}, d, sts)
	eventually(t, "references", func() bool {
		r, _ := db.ConfigReferrers(ctx, "shop", store.KindConfigMap, "api-config", time.Time{})
		open := 0
		for _, x := range r {
			if x.EndedAt.IsZero() {
				open++
			}
		}
		return open == 2
	})
	gone, _ := db.WorkloadConfigRefs(ctx, "dep-gone")
	if len(gone) != 1 || gone[0].EndedAt.IsZero() {
		t.Fatalf("the reference of a workload no longer there ends after sync: %+v", gone)
	}
	r, _ := db.ConfigReferrers(ctx, "shop", store.KindConfigMap, "ca-bundle", time.Time{})
	if len(r) != 1 || r[0].WorkloadKind != "Deployment" || r[0].WorkloadName != d.Name {
		t.Fatalf("a projected volume is a reference: %+v", r)
	}
	// Secret metadata is not watched, so a Secret it names is not recorded as a reference.
	if r, _ := db.ConfigReferrers(ctx, "shop", store.KindSecret, "api-db", time.Time{}); len(r) != 0 {
		t.Fatalf("%+v", r)
	}
	// The template stops naming api-config: the reference ends, and stays in the record.
	d2 := d.DeepCopy()
	d2.ResourceVersion = "2"
	d2.Spec.Template.Spec.Containers[0].EnvFrom = nil
	if _, err := client.AppsV1().Deployments("shop").Update(ctx, d2, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the reference ends", func() bool {
		refs, _ := db.WorkloadConfigRefs(ctx, string(d.UID))
		for _, x := range refs {
			if x.RefName == "api-config" {
				return !x.EndedAt.IsZero()
			}
		}
		return false
	})
}

// A delete the watch saw, still queued when the reconciliation at start runs, is recorded as seen: never as removed
// while OpsTide was not watching.
func TestQueuedConfigDeleteIsNotTakenForAnUnseenOne(t *testing.T) {
	db := openStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if err := db.Update(ctx, func(w *store.Writer) error {
		return w.SaveConfigObject(ctx, store.ConfigObject{UID: "cm-z", Kind: store.KindConfigMap, Namespace: "shop", Name: "z", FirstSeenAt: at, LastSeenAt: at, Origin: store.OriginObserved})
	}); err != nil {
		t.Fatal(err)
	}
	client := fake.NewClientset()
	client.Resources = []*metav1.APIResourceList{{GroupVersion: "events.k8s.io/v1", APIResources: []metav1.APIResource{{Name: "events", Kind: "Event"}}}}
	c := New(client, db, Options{WatchConfigMaps: true})
	// What the handler keeps when it sees the delete; the reconciliation finds the object absent from the cache.
	c.cfgGone.Store(types.UID("cm-z"), goneConfig{obj: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "z", UID: "cm-z",
		Annotations: map[string]string{annTrimmed: c.trim.mark}}}, deletedAt: at.Add(30 * time.Minute).UnixMilli()})
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = c.Run(cctx) }()
	defer func() { cancel(); <-done }()
	if err := c.reconcileAbsentConfig(ctx, ConfigMaps); err != nil {
		t.Fatal(err)
	}
	c.add(item{kind: ConfigMaps, namespace: "shop", name: "z", uid: "cm-z"}) // the delete the handler queued
	eventually(t, "the deletion", func() bool { return len(configEntries(t, db, "configmap.deleted")) == 1 })
	time.Sleep(100 * time.Millisecond)
	es := configEntries(t, db, "configmap.deleted")
	if len(es) != 1 || es[0].Confidence != store.Observed || !es[0].OccurredAt.Equal(at.Add(30*time.Minute)) {
		t.Fatalf("%+v", es)
	}
}

// A watch whose transform cannot be set would cache what arrives in full: it is not started, and says so.
func TestConfigWatchWithoutTransformIsNotStarted(t *testing.T) {
	db := openStore(t)
	c := New(fake.NewClientset(), db, Options{WatchConfigMaps: true})
	inf := c.scopeFor("").configMaps
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go inf.RunWithContext(ctx)
	eventually(t, "started", func() bool { return inf.SetTransform(c.trim.configMap) != nil })
	n := len(c.opt)
	if c.trimmedOrDropped(inf, "", ConfigMaps, c.trim.configMap) || len(c.opt) != n-1 {
		t.Fatal("refused")
	}
	found := false
	for _, p := range c.Problems() {
		found = found || p.Scope == ConfigMaps
	}
	if !found {
		t.Fatalf("%+v", c.Problems())
	}
}

func TestKeyDiffNamesKeysOnly(t *testing.T) {
	got := keyDiff([]string{"a", "b"}, []string{"b", "c"}, map[string]string{"a": "1", "b": "2"}, map[string]string{"b": "3", "c": "4"})
	if got["keysAdded"] != "c" || got["keysRemoved"] != "a" || got["keysChanged"] != "b" {
		t.Fatalf("%v", got)
	}
	if got := keyDiff([]string{"a"}, []string{"a"}, nil, map[string]string{"a": "1"}); !strings.HasPrefix(got["keysChanged"], "not known") {
		t.Fatalf("without the earlier per-key hashes the changed keys are not known, and it says so: %v", got)
	}
	for raw, want := range map[string]string{`{"f:data":{}}`: "data", `{"f:stringData":{}}`: "data", `{"f:metadata":{"f:labels":{}}}`: "metadata", `{"f:status":{}}`: ""} {
		if got := classOfFields(&metav1.FieldsV1{Raw: []byte(raw)}); got != want {
			t.Fatalf("%s: %q", raw, got)
		}
	}
	// The transform keeps no value, and no plain hash: the same content under another install's key hashes differently.
	tr := newConfigTrim()
	tr.key = []byte("0123456789abcdef0123456789abcdef")
	in := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "x"}, Data: map[string]string{"k": secretValue}, BinaryData: map[string][]byte{"b": []byte(secretValue)}}
	out, err := tr.configMap(in)
	if err != nil {
		t.Fatal(err)
	}
	cm := out.(*corev1.ConfigMap)
	if len(cm.BinaryData) != 0 || strings.Contains(cm.Data["k"], secretValue) || cm.Annotations[annContentHash] == "" ||
		cm.Annotations[annContentHash] == plainHash(map[string]string{"k": secretValue, "b": secretValue}) {
		t.Fatalf("%+v", cm)
	}
	if again, _ := tr.configMap(cm); again != cm {
		t.Fatal("trimming twice changes nothing")
	}
	other := newConfigTrim()
	other.key = []byte("another-key-another-key-another-")
	o2, _ := other.configMap(in)
	if o2.(*corev1.ConfigMap).Annotations[annContentHash] == cm.Annotations[annContentHash] {
		t.Fatal("the hash depends on the install's key")
	}
	// An object carrying the mark annotation from the cluster is not taken as trimmed.
	forged := in.DeepCopy()
	forged.Annotations = map[string]string{annTrimmed: "guess"}
	if f, _ := tr.configMap(forged); strings.Contains(f.(*corev1.ConfigMap).Data["k"], secretValue) {
		t.Fatal("forged mark")
	}
	if _, err := tr.configMap(&corev1.Secret{}); err == nil {
		t.Fatal("anything but a ConfigMap is refused, never passed through")
	}
}
