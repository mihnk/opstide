package api

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/mihnk/opstide/internal/release"
	"github.com/mihnk/opstide/internal/store"
)

func podSnap(init []corev1.Container, cs ...corev1.Container) string {
	return release.FromTemplate(corev1.PodTemplateSpec{Spec: corev1.PodSpec{InitContainers: init, Containers: cs}}, nil).Encode()
}

// imageServer: watching since 10 days ago. shop/api ran 1.5.0 (with an init migrate container) from 3 days ago and
// 1.5.1 from 6 hours ago, both with the log-shipper sidecar the web StatefulSet runs too; the backup CronJob's template
// named postgres:16.4 until an hour ago, then postgres:16.5. Retention is 30 days.
func imageServer(t *testing.T) *httptest.Server {
	t.Helper()
	ship := corev1.Container{Name: "log", Image: "shop/log-shipper:1.2"}
	return setupWith(t, base, nil, func(ctx context.Context, w *store.Writer) error {
		for _, sc := range imageScopes {
			if err := w.OpenInterval(ctx, sc, store.Healthy, base.Add(-10*24*time.Hour)); err != nil {
				return err
			}
		}
		if err := w.UpsertWorkload(ctx, store.Workload{ID: "d-api", Kind: store.KindDeployment, Origin: store.OriginObserved, Namespace: "shop", Name: "api", FirstSeen: base.Add(-9 * 24 * time.Hour), LastSeen: base}); err != nil {
			return err
		}
		r1 := relRow("rel-5", "api", "5", 5, base.Add(-3*24*time.Hour), "superseded", "healthy", "observed", "",
			podSnap([]corev1.Container{{Name: "migrate", Image: "shop/migrate:5"}}, corev1.Container{Name: "app", Image: "shop/api:1.5.0"}, ship))
		r2 := relRow("rel-6", "api", "6", 6, base.Add(-6*time.Hour), "completed", "healthy", "observed", "rel-5",
			podSnap(nil, corev1.Container{Name: "app", Image: "shop/api:1.5.1"}, ship))
		r1.SupersededAt = base.Add(-6 * time.Hour)
		for _, r := range []store.Release{r1, r2} {
			r.ImageIDs = `{"app":"sha256:0101"}`
			if err := w.UpsertRelease(ctx, r); err != nil {
				return err
			}
		}
		if err := w.UpsertWorkload(ctx, store.Workload{ID: "sts-web", Kind: store.KindStatefulSet, Origin: store.OriginObserved, Namespace: "shop", Name: "web", FirstSeen: base.Add(-9 * 24 * time.Hour), LastSeen: base}); err != nil {
			return err
		}
		if _, err := w.AddRevision(ctx, store.WorkloadRevision{ID: "wr-1", WorkloadID: "sts-web", Kind: store.KindStatefulSet, Name: "web-1", Number: 1, ControllerRevisionUID: "cr-1",
			CreatedAt: base.Add(-5 * 24 * time.Hour), ObservedAt: base.Add(-5 * 24 * time.Hour), Origin: store.OriginObserved, Snapshot: podSnap(nil, corev1.Container{Name: "web", Image: "shop/web:2.0"}, ship)}); err != nil {
			return err
		}
		if err := w.SetTemplateImages(ctx, "cj-backup", "CronJob", "data", "backup", []store.TemplateContainer{{Name: "dump", Image: "postgres:16.4"}}, store.OriginAdopted, base.Add(-5*24*time.Hour)); err != nil {
			return err
		}
		return w.SetTemplateImages(ctx, "cj-backup", "CronJob", "data", "backup", []store.TemplateContainer{{Name: "dump", Image: "postgres:16.5"}}, store.OriginObserved, base.Add(-time.Hour))
	}, func(*Config) {})
}

func imageMap(l ImageList) map[string]ImageGroup {
	out := map[string]ImageGroup{}
	for _, g := range l.Images {
		out[g.Image] = g
	}
	return out
}

func TestImagesNowAndAtATime(t *testing.T) {
	srv := imageServer(t)
	var now ImageList
	if code := get(t, srv, "/api/v1/images", &now); code != 200 {
		t.Fatal(code)
	}
	m := imageMap(now)
	if !now.Current || now.Coverage.State != CovWatching || len(now.Images) != 4 {
		t.Fatalf("%+v", now)
	}
	if _, ok := m["shop/api:1.5.0"]; ok {
		t.Fatal("a replaced image is not current")
	}
	api := m["shop/api:1.5.1"]
	if api.Repository != "shop/api" || api.Tag != "1.5.1" || len(api.Uses) != 1 || api.Uses[0].Provenance != ProvObserved || api.Uses[0].Until != nil ||
		api.Uses[0].Previous != "shop/api:1.5.0" || api.Uses[0].ReleaseID != "rel-6" || api.Uses[0].Path != "/workloads/d-api" || api.Uses[0].ReportedImageID != "sha256:0101" {
		t.Fatalf("%+v", api)
	}
	ship := m["shop/log-shipper:1.2"]
	if len(ship.Uses) != 2 || ship.Uses[0].Workload != "api" || ship.Uses[1].Workload != "web" || ship.Uses[1].Provenance != ProvDerived {
		t.Fatalf("one image, two workloads: %+v", ship)
	}
	if pg := m["postgres:16.5"]; len(pg.Uses) != 1 || pg.Uses[0].Kind != "CronJob" || pg.Uses[0].Path != "/operations/cj-backup" || pg.Namespaces[0] != "data" {
		t.Fatalf("%+v", pg)
	}

	// Two days ago: api ran 1.5.0 with its init container, the backup postgres:16.4.
	var then ImageList
	if code := get(t, srv, "/api/v1/images?at="+url.QueryEscape(base.Add(-48*time.Hour).Format(time.RFC3339)), &then); code != 200 {
		t.Fatal(code)
	}
	m = imageMap(then)
	old, ok := m["shop/api:1.5.0"]
	if then.Current || !ok || old.Uses[0].Until == nil || !old.Uses[0].Until.Equal(base.Add(-6*time.Hour)) || old.Uses[0].ReplacedBy != "shop/api:1.5.1" || old.Uses[0].UntilBasis != "replaced" {
		t.Fatalf("%+v", then)
	}
	if mg, ok := m["shop/migrate:5"]; !ok || !mg.Uses[0].Init {
		t.Fatalf("the init container is labelled: %+v", mg)
	}
	if pg, ok := m["postgres:16.4"]; !ok || pg.Uses[0].SinceBasis != "first_recorded" || !pg.Uses[0].Adopted {
		t.Fatalf("%+v", pg)
	}
	if _, ok := m["shop/api:1.5.1"]; ok {
		t.Fatal("1.5.1 was not yet current")
	}

	// Filters.
	var f ImageList
	if code := get(t, srv, "/api/v1/images?q=POSTGRES", &f); code != 200 || len(f.Images) != 1 {
		t.Fatalf("q: %d %+v", code, f.Images)
	}
	if code := get(t, srv, "/api/v1/images?namespace=data", &f); code != 200 || len(f.Images) != 1 || f.Images[0].Image != "postgres:16.5" {
		t.Fatalf("namespace: %+v", f.Images)
	}
	if code := get(t, srv, "/api/v1/images?limit=1", &f); code != 200 || len(f.Images) != 1 || !f.Truncated || f.Total != 4 {
		t.Fatalf("limit: %+v", f)
	}
}

func TestImagesSayWhenOpsTideWasNotWatching(t *testing.T) {
	srv := imageServer(t)
	var before ImageList
	// Twelve days ago: before OpsTide first watched. Nothing was recorded as current then.
	if code := get(t, srv, "/api/v1/images?at="+url.QueryEscape(base.Add(-12*24*time.Hour).Format(time.RFC3339)), &before); code != 200 {
		t.Fatal(code)
	}
	if before.Coverage.State != CovBeforeFirst || len(before.Notes) == 0 || len(before.Images) != 0 {
		t.Fatalf("%+v", before)
	}
	var old ImageList
	if code := get(t, srv, "/api/v1/images?at="+url.QueryEscape(base.Add(-40*24*time.Hour).Format(time.RFC3339)), &old); code != 200 {
		t.Fatal(code)
	}
	if old.Coverage.State != CovOutsideRetn || old.Coverage.BoundaryAt == nil || !strings.Contains(old.Notes[0], "30 days") {
		t.Fatalf("%+v", old.Coverage)
	}
}

func TestImageHistoryAcrossWorkloadsAndValidation(t *testing.T) {
	srv := imageServer(t)
	var d ImageDetail
	if code := get(t, srv, "/api/v1/images/"+EncodeImageRef("shop/api:1.5.0"), &d); code != 200 {
		t.Fatal(code)
	}
	if d.Image != "shop/api:1.5.0" || len(d.Uses) != 1 || d.Current != 0 || d.Workloads != 1 || len(d.OtherTags) != 1 || d.OtherTags[0].Image != "shop/api:1.5.1" || d.OtherTags[0].Current != 1 {
		t.Fatalf("%+v", d)
	}
	if code := get(t, srv, "/api/v1/images/"+EncodeImageRef("shop/log-shipper:1.2"), &d); code != 200 || d.Workloads != 2 || d.Current != 2 {
		t.Fatalf("%d %+v", code, d)
	}
	var none ImageDetail
	if code := get(t, srv, "/api/v1/images/"+EncodeImageRef("never/seen:1"), &none); code != 200 || len(none.Uses) != 0 || len(none.Notes) < 2 {
		t.Fatalf("%d %+v", code, none)
	}
	for _, p := range []string{
		"/api/v1/images?at=yesterday",
		"/api/v1/images?at=" + url.QueryEscape(base.Add(time.Hour).Format(time.RFC3339)),
		"/api/v1/images?namespace=Shop",
		"/api/v1/images?limit=0",
		"/api/v1/images?image=x",
		"/api/v1/images?q=" + strings.Repeat("x", 201),
		"/api/v1/images/not*base64",
		"/api/v1/images/" + EncodeImageRef("has space"),
		"/api/v1/images/" + EncodeImageRef("x") + "?at=2026-01-01T00:00:00Z",
	} {
		if code := get(t, srv, p, nil); code != 400 {
			t.Errorf("%s: %d", p, code)
		}
	}
}
