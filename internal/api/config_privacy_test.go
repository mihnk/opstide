package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	metafake "k8s.io/client-go/metadata/fake"

	"github.com/mihnk/opstide/internal/collector"
	"github.com/mihnk/opstide/internal/store"
)

// A Secret applied with kubectl carries its whole content in the last-applied-configuration annotation, and its
// managedFields name its keys; a ConfigMap's annotation can hold its values. Neither leaves a trace in what the API
// answers, nor anywhere in the database or its WAL, after a real collector has recorded them.
func TestConfigValuesAndSecretKeyNamesAreNotInAnyAnswer(t *testing.T) {
	const value, keyName = "s3cr3t-do-not-store", "zq-db-password-key"
	b64 := base64.StdEncoding.EncodeToString([]byte(value))
	dir := t.TempDir()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(dir, "opstide.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Now().UTC().Truncate(time.Second)
	if err := db.Update(ctx, func(w *store.Writer) error {
		// Watched since before the objects were made, so their creation is recorded.
		for _, sc := range []string{"configmaps", "secrets"} {
			if err := w.OpenInterval(ctx, sc, store.Healthy, now.Add(-time.Hour)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	mfe := func(manager, fields string, at time.Time) metav1.ManagedFieldsEntry {
		tm := metav1.NewTime(at)
		return metav1.ManagedFieldsEntry{Manager: manager, Operation: metav1.ManagedFieldsOperationUpdate, Time: &tm, FieldsType: "FieldsV1", FieldsV1: &metav1.FieldsV1{Raw: []byte(fields)}}
	}
	created := now.Add(-time.Minute)
	sec := &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}, ObjectMeta: metav1.ObjectMeta{
		Namespace: "shop", Name: "api-db", UID: "sec-1", ResourceVersion: "1", CreationTimestamp: metav1.NewTime(created),
		Annotations: map[string]string{"kubectl.kubernetes.io/last-applied-configuration": `{"apiVersion":"v1","data":{"` + keyName + `":"` + b64 + `"},"kind":"Secret"}`},
		ManagedFields: []metav1.ManagedFieldsEntry{
			mfe("kubectl-client-side-apply", `{"f:data":{".":{},"f:`+keyName+`":{}},"f:metadata":{"f:annotations":{"f:kubectl.kubernetes.io/last-applied-configuration":{}}}}`, created),
			mfe("rotator", `{"f:data":{"f:`+keyName+`":{}}}`, created.Add(20*time.Second)),
		}}}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-config", UID: "cm-1", ResourceVersion: "1", CreationTimestamp: metav1.NewTime(created),
		Annotations: map[string]string{"kubectl.kubernetes.io/last-applied-configuration": `{"data":{"LOG_LEVEL":"` + value + `"}}`}},
		Data: map[string]string{"LOG_LEVEL": value}}
	scheme := runtime.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	client := fake.NewClientset(cm)
	client.Resources = []*metav1.APIResourceList{{GroupVersion: "events.k8s.io/v1", APIResources: []metav1.APIResource{{Name: "events", Kind: "Event"}}}}
	col := collector.New(client, db, collector.Options{WatchConfigMaps: true, WatchSecretMetadata: true, Metadata: metafake.NewSimpleMetadataClient(scheme, sec), RetentionDays: 30})
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = col.Run(cctx) }()
	defer func() { cancel(); <-done }()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		pg, _ := db.Events(ctx, store.EventQuery{Types: []string{"secret.", "configmap."}, Limit: 50})
		if len(pg.Events) >= 3 { // two creations, the rotator's write
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not recorded: %+v", pg.Events)
		}
	}

	s := New(db, col, Config{Version: "test", Cluster: "c", Retention: store.Retention{Days: 30, MaxEvents: 1000}, WatchConfigMaps: true, WatchSecretMetadata: true}, nil)
	mux := http.NewServeMux()
	s.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	needles := []string{value, b64, keyName, "kubectl.kubernetes.io/last-applied"}
	check := func(path string) []byte {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, b)
		}
		for _, n := range needles {
			if bytes.Contains(b, []byte(n)) {
				t.Fatalf("%s answers with %q: %s", path, n, b)
			}
		}
		return b
	}
	var l ExplorerList
	if err := json.Unmarshal(check("/api/v1/explorer?range=24h&routine=1&group=config"), &l); err != nil || len(l.Records) < 3 {
		t.Fatalf("%v %+v", err, l.Records)
	}
	for _, r := range l.Records {
		check("/api/v1/explorer/records/" + r.ID)
		check("/api/v1/explorer/records/" + r.ID + "/before?window=24h")
	}
	check("/api/v1/explorer/export?range=24h&routine=1")
	check("/api/v1/events?within=48h")
	check("/api/v1/system")
	check("/api/v1/explorer/filters?range=24h")
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, n := range needles {
			if bytes.Contains(b, []byte(n)) {
				t.Fatalf("%q is in %s", n, f)
			}
		}
	}
}
