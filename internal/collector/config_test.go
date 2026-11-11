package collector

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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

// startWatching runs a collector on client whose ConfigMap watch is fw: the fake clientset's tracker rewrites
// managedFields on every update, so updates are delivered through the watch as Kubernetes would send them.
func startWatching(t *testing.T, db *store.Store, opt Options, fw *watch.FakeWatcher, objs ...runtime.Object) context.Context {
	t.Helper()
	client := fake.NewClientset(objs...)
	client.Resources = []*metav1.APIResourceList{{GroupVersion: "events.k8s.io/v1", APIResources: []metav1.APIResource{{Name: "events", Kind: "Event"}}}}
	client.PrependWatchReactor("configmaps", func(k8stesting.Action) (bool, watch.Interface, error) { return true, fw, nil })
	c := New(client, db, opt)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = c.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	eventually(t, "synced", func() bool { return c.Synced(ctx) == nil })
	eventually(t, "optional watches", c.evidenceSynced)
	return ctx
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

// No value of a ConfigMap reaches the record: not in an entry, not in the config table, not anywhere in the database file.
func assertNoValue(t *testing.T, db *store.Store, dir string) {
	t.Helper()
	for _, e := range configEntries(t, db, "configmap.") {
		if strings.Contains(e.Summary, secretValue) {
			t.Fatalf("a value in a summary: %+v", e)
		}
		for k, v := range e.Facts {
			if strings.Contains(v, secretValue) {
				t.Fatalf("a value in fact %s: %+v", k, e)
			}
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if bytes.Contains(b, []byte(secretValue)) {
			t.Fatalf("a value is in %s", f)
		}
	}
}

// A ConfigMap's creation, writes (with the field manager) and deletion are recorded; the content is told apart by a hash,
// the keys that changed are named, and no value is ever kept.
func TestConfigMapChangesAreRecordedWithoutValues(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(context.Background(), filepath.Join(dir, "opstide.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	old := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	// Already there: created two days ago by helm, its data later written by argocd. Its creation is a baseline, not news.
	adopted := configMap("shop", "api-config", "cm-1", old, map[string]string{"LOG_LEVEL": "info", "DB_PASSWORD": secretValue},
		mf("helm", metav1.ManagedFieldsOperationUpdate, "", `{"f:data":{"f:LOG_LEVEL":{}}}`, old),
		mf("argocd-controller", metav1.ManagedFieldsOperationApply, "", `{"f:data":{"f:LOG_LEVEL":{}}}`, old.Add(time.Hour)))
	fw := watch.NewFakeWithChanSize(10, false)
	startWatching(t, db, Options{WatchConfigMaps: true, configWriteEvery: time.Millisecond}, fw, adopted)

	eventually(t, "the argocd write", func() bool { return len(byType(configEntries(t, db, "configmap."), "configmap.data_written")) == 1 })
	es := configEntries(t, db, "configmap.")
	if len(byType(es, "configmap.created")) != 0 {
		t.Fatalf("a ConfigMap already there is a baseline, not a creation: %+v", es)
	}
	w := byType(es, "configmap.data_written")[0]
	if w.Facts["fieldManager"] != "argocd-controller" || w.Facts["seen"] != "already there when OpsTide began watching" || !w.OccurredAt.Equal(old.Add(time.Hour)) || w.ResourceName != "api-config" {
		t.Fatalf("%+v", w)
	}

	// The data is changed by kubectl-edit: one value changed, a key added. (Through the tracker: the typed client would
	// rewrite managedFields.)
	now := time.Now().UTC().Add(time.Second).Truncate(time.Second)
	changed := configMap("shop", "api-config", "cm-1", old, map[string]string{"LOG_LEVEL": "debug", "DB_PASSWORD": secretValue, "FEATURE_X": "on"},
		mf("helm", metav1.ManagedFieldsOperationUpdate, "", `{"f:data":{"f:LOG_LEVEL":{}}}`, old),
		mf("argocd-controller", metav1.ManagedFieldsOperationApply, "", `{"f:data":{"f:LOG_LEVEL":{}}}`, old.Add(time.Hour)),
		mf("kubectl-edit", metav1.ManagedFieldsOperationUpdate, "", `{"f:data":{"f:LOG_LEVEL":{},"f:FEATURE_X":{}}}`, now))
	changed.ResourceVersion = "2"
	fw.Modify(changed)
	var edit store.Event
	eventually(t, "the kubectl write", func() bool {
		for _, e := range byType(configEntries(t, db, "configmap."), "configmap.data_written") {
			if e.Facts["fieldManager"] == "kubectl-edit" {
				edit = e
				return true
			}
		}
		return false
	})
	if edit.Facts["contentChanged"] == "" || edit.Facts["keysAdded"] != "FEATURE_X" || edit.Facts["keysChanged"] != "LOG_LEVEL" || edit.Facts["seen"] != "recorded after OpsTide started watching" {
		t.Fatalf("the keys that changed are named (names, not values): %+v", edit.Facts)
	}

	// Only its labels are written: a metadata write, and the content is said to be unchanged.
	meta := changed.DeepCopy()
	meta.ResourceVersion, meta.Labels = "3", map[string]string{"team": "shop"}
	meta.ManagedFields = append(meta.ManagedFields, mf("kubectl-label", metav1.ManagedFieldsOperationUpdate, "", `{"f:metadata":{"f:labels":{"f:team":{}}}}`, now.Add(2*time.Second)))
	fw.Modify(meta)
	eventually(t, "the label write", func() bool {
		return len(byType(configEntries(t, db, "configmap."), "configmap.metadata_written")) == 1
	})

	// A ConfigMap created while OpsTide watches is a creation, with who made it.
	created := time.Now().UTC().Truncate(time.Second).Add(time.Second)
	fresh := configMap("shop", "flags", "cm-2", created, map[string]string{"a": secretValue},
		mf("argocd-controller", metav1.ManagedFieldsOperationApply, "", `{"f:data":{"f:a":{}}}`, created))
	fw.Add(fresh)
	eventually(t, "the creation", func() bool { return len(byType(configEntries(t, db, "configmap."), "configmap.created")) == 1 })
	c := byType(configEntries(t, db, "configmap."), "configmap.created")[0]
	if c.Facts["fieldManager"] != "argocd-controller" || c.Facts["keys"] != "a" || !c.OccurredAt.Equal(created) {
		t.Fatalf("%+v", c)
	}
	if n := len(byType(configEntries(t, db, "configmap."), "configmap.data_written")); n != 2 {
		t.Fatalf("the write that created it is the creation, not a second write: %d writes", n)
	}

	// Deleted.
	gone := fresh.DeepCopy()
	gone.ResourceVersion = "9"
	fw.Delete(gone)
	eventually(t, "the deletion", func() bool { return len(byType(configEntries(t, db, "configmap."), "configmap.deleted")) == 1 })
	assertNoValue(t, db, dir)

	// Every entry is an observed change in the change ledger's sense, and none is routine-volume.
	for _, e := range configEntries(t, db, "configmap.") {
		if store.IsRoutineType(e.Type) || store.GroupOfType(e.Type) != "config" {
			t.Fatalf("%s: routine %v, group %q", e.Type, store.IsRoutineType(e.Type), store.GroupOfType(e.Type))
		}
	}
}

// A ConfigMap a controller rewrites every few seconds does not fill the record: writes inside the interval are folded
// into the next entry, which says so.
func TestFrequentConfigMapWritesAreFolded(t *testing.T) {
	db := openStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	cm := configMap("kube-system", "autoscaler-status", "cm-9", now.Add(-time.Hour), map[string]string{"status": "1"},
		mf("cluster-autoscaler", metav1.ManagedFieldsOperationUpdate, "", `{"f:data":{"f:status":{}}}`, now.Add(-time.Minute)))
	fw := watch.NewFakeWithChanSize(10, false)
	startWatching(t, db, Options{WatchConfigMaps: true, configWriteEvery: 1500 * time.Millisecond}, fw, cm)
	eventually(t, "the first write", func() bool { return len(configEntries(t, db, "configmap.data_written")) == 1 })
	for i := 2; i <= 4; i++ {
		x := cm.DeepCopy()
		x.ResourceVersion, x.Data = time.Now().Format("150405.000")+string(rune('a'+i)), map[string]string{"status": string(rune('0' + i))}
		x.ManagedFields = []metav1.ManagedFieldsEntry{mf("cluster-autoscaler", metav1.ManagedFieldsOperationUpdate, "", `{"f:data":{"f:status":{}}}`, now.Add(time.Duration(i)*time.Second))}
		fw.Modify(x)
		time.Sleep(100 * time.Millisecond)
	}
	eventually(t, "the folded write", func() bool { return len(configEntries(t, db, "configmap.data_written")) == 2 })
	time.Sleep(300 * time.Millisecond)
	es := configEntries(t, db, "configmap.data_written")
	if len(es) != 2 || es[0].Facts["updatesFolded"] == "" || !es[0].OccurredAt.Equal(now.Add(4*time.Second)) {
		t.Fatalf("one entry for the burst, the latest write, saying it folded others: %+v", es)
	}
}

// Secrets are read through the metadata-only API: the typed client is never asked for a Secret (so their data is never
// sent), and a data write is told from a metadata write by managedFields alone.
func TestSecretsAreReadThroughMetadataOnly(t *testing.T) {
	db := openStore(t)
	client := fake.NewClientset()
	client.Resources = []*metav1.APIResourceList{{GroupVersion: "events.k8s.io/v1", APIResources: []metav1.APIResource{{Name: "events", Kind: "Event"}}}}
	var typed atomic.Int32
	client.PrependReactor("*", "secrets", func(a k8stesting.Action) (bool, runtime.Object, error) {
		typed.Add(1)
		return true, nil, nil
	})
	client.PrependWatchReactor("secrets", func(a k8stesting.Action) (bool, watch.Interface, error) {
		typed.Add(1)
		return true, nil, nil
	})
	scheme := runtime.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	md := metafake.NewSimpleMetadataClient(scheme, secretMeta("shop", "api-db", "sec-1", old.Add(-time.Hour),
		mf("argocd-controller", metav1.ManagedFieldsOperationApply, "", `{"f:data":{"f:password":{}}}`, old),
		mf("kubectl-annotate", metav1.ManagedFieldsOperationUpdate, "", `{"f:metadata":{"f:annotations":{"f:note":{}}}}`, old.Add(time.Minute))))
	c := New(client, db, Options{WatchConfigMaps: true, WatchSecretMetadata: true, Metadata: md, configWriteEvery: time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = c.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	eventually(t, "synced", func() bool { return c.Synced(ctx) == nil })
	eventually(t, "the secret writes", func() bool { return len(configEntries(t, db, "secret.")) == 2 })
	es := configEntries(t, db, "secret.")
	if d := byType(es, "secret.data_written"); len(d) != 1 || d[0].Facts["fieldManager"] != "argocd-controller" || d[0].Kind != "Secret" {
		t.Fatalf("%+v", es)
	}
	if m := byType(es, "secret.metadata_written"); len(m) != 1 || m[0].Facts["fieldManager"] != "kubectl-annotate" {
		t.Fatalf("%+v", es)
	}
	// The record of a Secret holds no keys and no hash: OpsTide never saw its data.
	if err := db.Update(ctx, func(w *store.Writer) error {
		o, ok, err := w.ConfigObject(ctx, "sec-1")
		if err != nil || !ok || o.ContentHash != "" || len(o.Keys) != 0 || o.Kind != "Secret" {
			t.Errorf("%+v %v %v", o, ok, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, a := range client.Actions() {
		if a.GetResource().Resource == "secrets" {
			t.Fatalf("the typed client was asked for Secrets: %v", a)
		}
	}
	if typed.Load() != 0 {
		t.Fatal("the typed client was asked for Secrets")
	}
	for _, a := range md.Actions() {
		if a.GetResource().Resource != "secrets" || (a.GetVerb() != "list" && a.GetVerb() != "watch") {
			t.Fatalf("only list and watch on Secrets' metadata: %v", a)
		}
	}
}

// With Secret metadata off (the default), nothing asks for Secrets at all.
func TestSecretsAreNotWatchedByDefault(t *testing.T) {
	db := openStore(t)
	client, _ := start(t, db, Options{WatchConfigMaps: true})
	time.Sleep(100 * time.Millisecond)
	for _, a := range client.Actions() {
		if a.GetResource().Resource == "secrets" {
			t.Fatalf("Secrets were requested: %v", a)
		}
	}
}

// The ConfigMaps and Secrets a workload's Pod template names are recorded as references, by name, and end when the
// template stops naming them.
func TestWorkloadsReferenceTheConfigTheyName(t *testing.T) {
	db := openStore(t)
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
		return len(r) == 2
	})
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

func TestKeyDiffNamesKeysOnly(t *testing.T) {
	got := keyDiff([]string{"a", "b"}, []string{"b", "c"}, map[string]string{"a": "1", "b": "2"}, map[string]string{"b": "3", "c": "4"})
	if got["keysAdded"] != "c" || got["keysRemoved"] != "a" || got["keysChanged"] != "b" {
		t.Fatalf("%v", got)
	}
	if got := keyDiff([]string{"a"}, []string{"a"}, nil, map[string]string{"a": "1"}); !strings.HasPrefix(got["keysChanged"], "not known") {
		t.Fatalf("after a restart the changed keys are not known, and it says so: %v", got)
	}
	for raw, want := range map[string]string{`{"f:data":{}}`: "data", `{"f:stringData":{}}`: "data", `{"f:metadata":{"f:labels":{}}}`: "metadata", `{"f:status":{}}`: ""} {
		if got := classOfFields(&metav1.FieldsV1{Raw: []byte(raw)}); got != want {
			t.Fatalf("%s: %q", raw, got)
		}
	}
	// The transform keeps no value.
	out, _ := trimConfigMap(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "x"}, Data: map[string]string{"k": secretValue}, BinaryData: map[string][]byte{"b": []byte(secretValue)}})
	cm := out.(*corev1.ConfigMap)
	if len(cm.BinaryData) != 0 || strings.Contains(cm.Data["k"], secretValue) || strings.Contains(cm.Data["b"], secretValue) || cm.Annotations[annContentHash] == "" {
		t.Fatalf("%+v", cm)
	}
}
