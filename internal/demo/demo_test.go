package demo

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mihnk/opstide/internal/store"
)

// The made-up history must be valid against the real store and tell every part of the story the demo is for.
func TestSeedTellsTheWholeStory(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "opstide.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	if err := Seed(ctx, db, now); err != nil {
		t.Fatal(err)
	}
	pg, err := db.Explorer(ctx, store.ExplorerQuery{From: now.Add(-7 * 24 * time.Hour), To: now.Add(time.Hour), Routine: true, Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]int{}
	ids := map[string]bool{}
	derived := 0
	for _, r := range pg.Rows {
		if ids[r.ID] {
			t.Fatalf("a record appears twice: %s", r.ID)
		}
		ids[r.ID] = true
		if r.Event != nil {
			types[r.Event.Type]++
			if r.Event.Confidence == store.Derived {
				derived++
			}
		} else {
			types["kubernetes.event"]++
		}
	}
	for _, want := range []string{"job.succeeded", "job.failed", "collection.gap", "release.observed", "release.health.degraded", "statefulset.update_partitioned", "daemonset.update_completed", "node.condition_changed", "node.deleted", "node.observed", "pod.restarted", "kubernetes.event", "deployment.spec_written", "statefulset.spec_written",
		"configmap.data_written", "configmap.created", "configmap.deleted", "secret.written"} {
		if types[want] == 0 {
			t.Errorf("the story has no %s", want)
		}
	}
	if derived == 0 {
		t.Error("nothing is derived")
	}
	// The api's ConfigMap is named by the api and by web.
	if refs, err := db.ConfigReferrers(ctx, "shop", store.KindConfigMap, "api-config", time.Time{}); err != nil || len(refs) != 2 {
		t.Errorf("%+v %v", refs, err)
	}
	// A gave-up gap names its object.
	var named bool
	for _, r := range pg.Rows {
		if r.Event != nil && r.Event.Type == "collection.gap" && r.Event.Facts["object"] != "" {
			named = true
		}
	}
	if !named {
		t.Error("the dropped object's gap is missing")
	}
	// The Node Events follow the lifetime of the Node that had the name; the early one is unresolved.
	links := map[string]string{}
	ev, _ := db.Evidence(ctx, store.EvidenceQuery{Limit: 50})
	for _, e := range ev.Records {
		if e.Regarding.Kind == "Node" {
			links[e.Reason+"/"+e.ID] = e.NodeID
		}
	}
	if links["NodeNotReady/ev-node-old"] != "n-a-old" || links["NodeReady/ev-node-new"] != "n-a-new" || links["NodeNotReady/ev-node-early"] != "" {
		t.Errorf("%v", links)
	}
	if n, err := db.Nodes(ctx, store.NodeQuery{Limit: 10}); err != nil || len(n) != 3 {
		t.Errorf("%d %v", len(n), err)
	}
	// The nightly backup has become slower in the last week; the weekly archive has too few runs to compare.
	durs, err := db.DurationsByOperation(ctx, now.AddDate(0, 0, -30), now.AddDate(0, 0, -7), now)
	if err != nil {
		t.Fatal(err)
	}
	median := func(ds []time.Duration) time.Duration {
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		return ds[(len(ds)-1)/2]
	}
	if b := durs["cj-backup"]; len(b.After) < 5 || len(b.Before) < 5 || median(b.After) < median(b.Before)*13/10 {
		t.Errorf("backup %d %d", len(b.After), len(b.Before))
	}
	if a := durs["cj-archive"]; len(a.After) == 0 || len(a.After) >= 5 {
		t.Errorf("archive %d", len(a.After))
	}
	wls, err := db.Workloads(ctx, store.WorkloadQuery{Limit: 20})
	if err != nil || len(wls.Rows) != 4 {
		t.Fatalf("%v %+v", err, wls)
	}
}

// The demo's maintenance window covers a failure and a restart of the story, and its images show a replacement and an
// image shared by two workloads.
func TestDemoMaintenanceAndImages(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "opstide.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 11, 6, 12, 7, 0, 0, time.UTC)
	if err := Seed(ctx, db, now); err != nil {
		t.Fatal(err)
	}
	m := Maintenance(now)
	if _, ok := m.At(now.Add(-29*time.Hour), "shop"); !ok {
		t.Error("yesterday's occurrence covers the api Pod's restart")
	}
	runs, err := db.RunsBetween(ctx, now.Add(-8*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	covered := false
	for _, r := range runs {
		if r.Status == store.RunFailed && r.OperationID == "cj-ledger" {
			_, covered = m.At(r.FinishedAt, "payments")
		}
	}
	if !covered {
		t.Error("today's occurrence covers the failed ledger run")
	}
	revs, err := db.ImageRevisions(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	shipper := map[string]bool{}
	for _, r := range revs {
		if strings.Contains(r.Containers+r.InitContainers, "shop/log-shipper:1.2") {
			shipper[r.WorkloadID] = true
		}
	}
	if len(shipper) != 2 {
		t.Errorf("the log shipper runs in two workloads: %v", shipper)
	}
	ti, err := db.TemplateImages(ctx, "")
	if err != nil || len(ti) != 5 {
		t.Errorf("CronJob images: %d %v", len(ti), err)
	}
}
