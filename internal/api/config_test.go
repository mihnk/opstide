package api

import (
	"context"
	"testing"
	"time"

	"github.com/mihnk/opstide/internal/store"
)

// A ConfigMap write is a change: it is in the change ledger and the summary's changes, its record lists the workloads
// that name it (a reference, never ownership or a cause), and the workload's history shows it.
func TestConfigChangesAreChangesAndListTheWorkloadsThatReferenceThem(t *testing.T) {
	at := base.Add(-2 * time.Hour)
	srv := setupWith(t, base, nil, func(ctx context.Context, w *store.Writer) error {
		for _, sc := range []string{"configmaps", "deployments"} {
			if err := w.OpenInterval(ctx, sc, store.Healthy, base.Add(-30*time.Hour)); err != nil {
				return err
			}
		}
		if err := w.UpsertWorkload(ctx, store.Workload{ID: "d-1", Kind: store.KindDeployment, Origin: store.OriginObserved, Namespace: "shop", Name: "api", FirstSeen: at.Add(-time.Hour), LastSeen: base}); err != nil {
			return err
		}
		if err := w.SaveConfigObject(ctx, store.ConfigObject{UID: "cm-1", Kind: store.KindConfigMap, Namespace: "shop", Name: "api-config", FirstSeenAt: at.Add(-time.Hour), LastSeenAt: base, Origin: store.OriginAdopted, ContentHash: "h", Keys: []string{"LOG_LEVEL"}}); err != nil {
			return err
		}
		if err := w.SetConfigRefs(ctx, "d-1", "Deployment", "shop", "api", []store.ConfigName{{Kind: store.KindConfigMap, Name: "api-config"}, {Kind: store.KindSecret, Name: "api-db"}}, at.Add(-time.Hour)); err != nil {
			return err
		}
		for _, e := range []store.Event{
			{ID: "c1", Type: "configmap.data_written", Namespace: "shop", Kind: "ConfigMap", ResourceUID: "cm-1", ResourceName: "api-config", OccurredAt: at, ObservedAt: at, Severity: "info", Confidence: store.Observed,
				Summary: "ConfigMap shop/api-config: data written by argocd-controller (Apply)", Facts: map[string]string{"fieldManager": "argocd-controller", "operation": "Apply", "keysChanged": "LOG_LEVEL"}},
			// Before the Deployment's template named it: not part of the Deployment's history.
			{ID: "c0", Type: "configmap.metadata_written", Namespace: "shop", Kind: "ConfigMap", ResourceUID: "cm-1", ResourceName: "api-config", OccurredAt: at.Add(-90 * time.Minute), ObservedAt: at, Severity: "info", Confidence: store.Observed,
				Summary: "ConfigMap shop/api-config: written by kubectl-label (Update)", Facts: map[string]string{"fieldManager": "kubectl-label", "operation": "Update"}},
			{ID: "c2", Type: "configmap.created", Namespace: "shop", Kind: "ConfigMap", ResourceUID: "cm-2", ResourceName: "unused", OccurredAt: at, ObservedAt: at, Severity: "info", Confidence: store.Observed, Summary: "ConfigMap shop/unused created"},
			{ID: "r1", Type: "release.observed", Namespace: "shop", Kind: "Deployment", ResourceUID: "d-1", ResourceName: "api", WorkloadID: "d-1", OccurredAt: at.Add(time.Minute), ObservedAt: at.Add(time.Minute), Severity: "info", Confidence: store.Observed, Summary: "api revision 8"},
		} {
			if _, err := w.AddEvent(ctx, e); err != nil {
				return err
			}
		}
		return nil
	}, func(c *Config) { c.WatchConfigMaps = true })

	var l ExplorerList
	if code := get(t, srv, "/api/v1/explorer?range=24h&preset=changes", &l); code != 200 || len(l.Records) != 4 {
		t.Fatalf("%d %+v", code, l.Records)
	}
	var cw ExplorerRecord
	for _, r := range l.Records {
		if r.Type == "configmap.data_written" {
			cw = r
		}
	}
	if cw.Group != "config" || cw.Provenance != ProvObserved {
		t.Fatalf("%+v", cw)
	}
	var g ExplorerList
	if code := get(t, srv, "/api/v1/explorer?range=24h&group=config", &g); code != 200 || len(g.Records) != 3 {
		t.Fatalf("the config group: %d %+v", code, g.Records)
	}

	var d ExplorerDetail
	if code := get(t, srv, "/api/v1/explorer/records/"+cw.ID, &d); code != 200 {
		t.Fatal(code)
	}
	var ref *Relationship
	for i, r := range d.Relationships {
		if r.Classification == Ownership || r.Classification == Direct {
			t.Fatalf("a reference is neither ownership nor a direct link: %+v", r)
		}
		if r.Classification == Reference {
			ref = &d.Relationships[i]
		}
	}
	if ref == nil || ref.Kind != "Deployment" || ref.UID != "d-1" || ref.Name != "api" || ref.Path != "/workloads/d-1" {
		t.Fatalf("%+v", d.Relationships)
	}
	if d.Coverage.State != CovWatching || len(d.Coverage.Scopes) != 1 || d.Coverage.Scopes[0] != "configmaps" {
		t.Fatalf("a ConfigMap's record depends on the ConfigMap watch: %+v", d.Coverage)
	}
	// A ConfigMap nothing names says so.
	for _, r := range l.Records {
		if r.Type == "configmap.created" {
			var u ExplorerDetail
			get(t, srv, "/api/v1/explorer/records/"+r.ID, &u)
			if len(u.Relationships) != 1 || u.Relationships[0].Classification != Unresolved {
				t.Fatalf("%+v", u.Relationships)
			}
		}
	}

	// The workload's history has the write to the ConfigMap it names, and says what it names.
	var h WorkloadHistory
	if code := get(t, srv, "/api/v1/workloads/d-1/history", &h); code != 200 {
		t.Fatal(code)
	}
	types := map[string]bool{}
	for _, e := range h.Entries.Entries {
		types[e.Type] = true
	}
	if !types["configmap.data_written"] || !types["release.observed"] || types["configmap.created"] || types["configmap.metadata_written"] || len(h.References) != 2 {
		t.Fatalf("%v %+v", types, h.References)
	}
	var plain WorkloadHistory
	get(t, srv, "/api/v1/workloads/d-1/history?config=none", &plain)
	if len(plain.Entries.Entries) != 1 {
		t.Fatalf("config=none leaves them out: %+v", plain.Entries.Entries)
	}

	// The summary counts config changes among the changes.
	var sm ExplorerSummary
	get(t, srv, "/api/v1/explorer/summary?range=24h", &sm)
	for _, it := range sm.Items {
		if it.Key == "changes" && it.Count != 4 {
			t.Fatalf("%+v", it)
		}
	}

	// System says ConfigMaps are watched and Secret metadata is not; the Secret watch is shown as off, never as calm.
	var sys System
	get(t, srv, "/api/v1/system", &sys)
	if !sys.Config.ConfigMaps || sys.Config.SecretMetadata || sys.Config.ConfigMapCount != 1 || sys.Config.SecretNote == "" {
		t.Fatalf("%+v", sys.Config)
	}
	var cx ExplorerContext
	get(t, srv, "/api/v1/explorer/context?range=24h", &cx)
	off := false
	for _, sc := range cx.Scopes {
		off = off || (sc.Kind == "secrets" && sc.State == CovDisabled)
	}
	if !off {
		t.Fatalf("%+v", cx.Scopes)
	}
}
