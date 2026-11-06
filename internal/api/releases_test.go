package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/mihnk/opstide/internal/release"
	"github.com/mihnk/opstide/internal/store"
)

func snap(image, mem string, replicas int32) string {
	c := corev1.Container{Name: "api", Image: image}
	if mem != "" {
		c.Resources.Limits = corev1.ResourceList{corev1.ResourceMemory: mustQty(mem)}
	}
	var d *appsv1.Deployment
	if replicas > 0 {
		d = &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: &replicas}}
	}
	return release.FromTemplate(corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{c}}}, d).Encode()
}

func relRow(id, ws, rev string, num int64, at time.Time, state, health, origin, prev, sn string) store.Release {
	fp := ""
	if s, err := release.DecodeSnapshot(sn); err == nil {
		fp = s.Fingerprint()
	}
	return store.Release{ID: id, WorkloadID: "d-" + ws, Namespace: "shop", Workload: ws, Revision: rev, RevisionNum: num, ReplicaSetUID: "rs-" + id, ReplicaSetName: ws + "-" + rev,
		Fingerprint: fp, PrevID: prev, Origin: origin, ObservedAt: at, StartedAt: at, State: state, StateAt: at, Health: health, HealthReasons: "[]", WindowSeconds: 600,
		Episodes: "[]", Snapshot: sn, ImageIDs: `{"api":"sha256:abc"}`, Desired: 3, Available: 3, Updated: 3, Ready: 3}
}

func releaseServer(t *testing.T) *httptest.Server {
	t.Helper()
	later := base.Add(-time.Hour)
	// payments-api: revision 41 adopted (its rollout not seen), 42 observed live and completed, 43 observed and degraded by an OOMKill.
	r41 := relRow("r41", "payments-api", "41", 41, later.Add(-48*time.Hour), "superseded", "insufficient_evidence", "adopted", "", snap("api:1.8.3", "256Mi", 0))
	r42 := relRow("r42", "payments-api", "42", 42, later.Add(-30*time.Minute), "completed", "healthy", "observed", "r41", snap("api:1.9.0", "384Mi", 3))
	r42.CompletedAt, r42.SupersededAt = later.Add(-25*time.Minute), later
	r43 := relRow("r43", "payments-api", "43", 43, later, "completed", "still_observing", "observed", "r42", snap("api:1.9.0", "384Mi", 4))
	r43.CompletedAt, r43.ObserveUntil = later.Add(5*time.Minute), base.Add(10*time.Minute)
	// ledger: one stalled release, no previous.
	rl := relRow("rl1", "ledger", "7", 7, later.Add(-10*time.Minute), "stalled", "degraded", "observed", "", snap("ledger:3", "", 2))
	rl.StateReason = "ProgressDeadlineExceeded: timed out"
	return setup(t, base, nil, func(ctx context.Context, w *store.Writer) error {
		for _, r := range []store.Release{r41, r42, r43, rl} {
			if err := w.UpsertRelease(ctx, r); err != nil {
				return err
			}
		}
		oomAt := later.Add(7 * time.Minute)
		ps := release.PodState{Ready: true, ReadyAt: later.Add(time.Minute), Transitions: []release.Transition{{At: later.Add(time.Minute), Ready: true}},
			Containers: map[string]*release.ContainerObs{"api": {Restarts: 1, Restarted: []release.Restart{{Observed: oomAt, Count: 1, Delta: 1, Reason: "OOMKilled"}},
				OOM: []release.OOMObs{{At: oomAt, Observed: oomAt}}}}}
		if err := w.UpsertPod(ctx, store.PodRecord{UID: "p1", ReleaseID: "r43", Namespace: "shop", Name: "payments-api-43-a", ReplicaSetUID: "rs-r43", WorkloadID: "d-payments-api", CreatedAt: later, State: release.EncodePod(ps)}); err != nil {
			return err
		}
		add := func(id, typ, relID, sev string, at time.Time) error {
			_, err := w.AddEvent(ctx, store.Event{ID: id, Type: typ, ReleaseID: relID, Namespace: "shop", ObservedAt: at, OccurredAt: at, Severity: sev, Confidence: store.Observed, Summary: id})
			return err
		}
		for _, e := range []struct {
			id, typ, rel, sev string
			at                time.Time
		}{
			{"e1", "release.observed", "r43", "info", later},
			{"e2", "pod.created", "r43", "info", later},
			{"e3", "pod.oomkilled", "r43", "error", later.Add(7 * time.Minute)},
			{"e4", "release.health.degraded", "r43", "error", later.Add(8 * time.Minute)},
			{"e5", "release.observed", "r42", "info", later.Add(-30 * time.Minute)},
		} {
			if err := add(e.id, e.typ, e.rel, e.sev, e.at); err != nil {
				return err
			}
		}
		// Coverage: Deployments watched with a gap; ReplicaSets and Pods without.
		for _, sc := range []string{"deployments", "replicasets", "deploypods"} {
			if err := w.OpenInterval(ctx, sc, store.Healthy, base.Add(-72*time.Hour)); err != nil {
				return err
			}
		}
		if err := w.CloseInterval(ctx, "deploypods", base.Add(-40*time.Minute), "watch could not resume; relisted"); err != nil {
			return err
		}
		return w.OpenInterval(ctx, "deploypods", store.Healthy, base.Add(-35*time.Minute))
	})
}

func TestReleasesListFiltersPagesAndWarns(t *testing.T) {
	srvURL := releaseServer(t)
	var rs Releases
	if code := get(t, srvURL, "/api/v1/releases?within=30d", &rs); code != 200 {
		t.Fatal(code)
	}
	var ids []string
	for _, r := range rs.Releases {
		ids = append(ids, r.ID)
	}
	// Newest first; the adopted release already replaced when OpsTide arrived is hidden by default.
	if strings.Join(ids, ",") != "r43,rl1,r42" {
		t.Fatalf("%v", ids)
	}
	if rs.WindowMinutes != 10 || rs.Releases[0].Health != "still_observing" || rs.Releases[0].PrevID != "r42" {
		t.Fatalf("%+v", rs.Releases[0])
	}
	if recent := rs.Releases[0].Recent; len(recent) != 2 || recent[0].Type != "release.health.degraded" || recent[1].Type != "pod.oomkilled" {
		t.Fatalf("recent entries must be the significant ones, newest first: %+v", recent)
	}
	if len(rs.Warnings) == 0 || !strings.Contains(rs.Warnings[0], "Pods") || !strings.Contains(rs.Warnings[0], "adopted") {
		t.Fatalf("a Pod watch gap was not warned about: %v", rs.Warnings)
	}
	var hist Releases
	get(t, srvURL, "/api/v1/releases?within=30d&history=1", &hist)
	if len(hist.Releases) != 4 {
		t.Fatalf("history: %d", len(hist.Releases))
	}
	for qs, want := range map[string]string{
		"&state=stalled": "rl1", "&health=degraded": "rl1", "&health=healthy,still_observing": "r43,r42",
		"&workload=LEDG": "rl1", "&namespace=other": "", "&state=completed&health=healthy": "r42",
	} {
		var f Releases
		get(t, srvURL, "/api/v1/releases?within=30d"+qs, &f)
		var got []string
		for _, r := range f.Releases {
			got = append(got, r.ID)
		}
		if strings.Join(got, ",") != want {
			t.Errorf("%s: got %v want %q", qs, got, want)
		}
	}
	var p1, p2 Releases
	get(t, srvURL, "/api/v1/releases?within=30d&limit=2", &p1)
	get(t, srvURL, "/api/v1/releases?within=30d&limit=2&cursor="+p1.NextCursor, &p2)
	if len(p1.Releases) != 2 || p1.NextCursor == "" || len(p2.Releases) != 1 || p2.Releases[0].ID != "r42" || p2.NextCursor != "" {
		t.Fatalf("paging: %+v %+v", p1, p2)
	}
	for _, bad := range []string{"?state=done", "?health=fine", "?limit=0", "?within=forever", "?cursor=x", "?from=yesterday"} {
		if code := get(t, srvURL, "/api/v1/releases"+bad, nil); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
}

func TestReleaseDetailAssessmentAndWindows(t *testing.T) {
	srvURL := releaseServer(t)
	var d ReleaseDetail
	if code := get(t, srvURL, "/api/v1/releases/r43", &d); code != 200 {
		t.Fatal(code)
	}
	if d.Assessment.Health != "degraded" || !d.Assessment.Stored || d.Assessment.WindowMinutes != 10 || len(d.Assessment.Windows) != 4 {
		t.Fatalf("%+v", d.Assessment)
	}
	if reasons := strings.Join(d.Assessment.Reasons, " "); !strings.Contains(reasons, "OOMKilled") || !strings.Contains(reasons, "payments-api-43-a") {
		t.Fatalf("the verdict does not say why: %q", reasons)
	}
	if d.Previous == nil || d.Previous.Revision != "42" || d.Workload.Name != "payments-api" || d.ReplicaSet.UID != "rs-r43" || d.ImageIDs["api"] != "sha256:abc" {
		t.Fatalf("%+v", d)
	}
	if len(d.Snapshot.Containers) != 1 || d.Snapshot.Containers[0].Image != "api:1.9.0" {
		t.Fatalf("snapshot %+v", d.Snapshot)
	}
	// The OOMKill came 7 minutes in; a 5 minute window closed at 5 after completion (that is 5+5=10 after observation).
	var short ReleaseDetail
	get(t, srvURL, "/api/v1/releases/r43?window=5", &short)
	if short.Assessment.Stored || short.Assessment.WindowMinutes != 5 || short.Assessment.Health != "degraded" {
		t.Fatalf("5 min: %+v", short.Assessment)
	}
	// Adopted release: insufficient evidence, no guess.
	var ad ReleaseDetail
	get(t, srvURL, "/api/v1/releases/r41", &ad)
	if ad.Assessment.Health != "insufficient_evidence" || !strings.Contains(strings.Join(ad.Assessment.Reasons, " "), "before OpsTide") {
		t.Fatalf("%+v", ad.Assessment)
	}
	for path, want := range map[string]int{"/api/v1/releases/nope": 404, "/api/v1/releases/r43?window=7": 400, "/api/v1/releases/r43?window=abc": 400, "/api/v1/releases/nope/pods": 404, "/api/v1/releases/nope/changes": 404, "/api/v1/releases/nope/events": 404} {
		if code := get(t, srvURL, path, nil); code != want {
			t.Errorf("%s: %d want %d", path, code, want)
		}
	}
}

func TestReleaseEventsPodsAndChanges(t *testing.T) {
	srvURL := releaseServer(t)
	var ev Entries
	get(t, srvURL, "/api/v1/releases/r43/events", &ev)
	if len(ev.Entries) != 4 || ev.Entries[0].Type != "release.health.degraded" || ev.Entries[3].Type != "release.observed" || ev.Entries[0].ReleaseID != "r43" {
		t.Fatalf("the release timeline must hold only its own entries, newest first: %+v", ev.Entries)
	}
	var pods ReleasePods
	get(t, srvURL, "/api/v1/releases/r43/pods", &pods)
	if len(pods.Pods) != 1 || pods.ReplicaSet.Name != "payments-api-43" {
		t.Fatalf("%+v", pods)
	}
	p := pods.Pods[0]
	if p.State != "ready" || p.Restarts != 1 || len(p.Containers) != 1 || p.Containers[0].OOMKilled != 1 || p.Containers[0].Waiting == nil || len(p.Readiness) != 1 {
		t.Fatalf("%+v", p)
	}

	var ch ReleaseChanges
	get(t, srvURL, "/api/v1/releases/r43/changes", &ch)
	if !ch.Available || ch.Previous == nil || ch.Previous.Revision != "42" || ch.SameTemplate || len(ch.Changes) != 1 || ch.Changes[0].Field != "replicas" || ch.Changes[0].Before != "3" || ch.Changes[0].After != "4" || !ch.DeploymentCompared {
		t.Fatalf("%+v", ch)
	}
	var ch42 ReleaseChanges
	get(t, srvURL, "/api/v1/releases/r42/changes", &ch42)
	fields := map[string]bool{}
	for _, c := range ch42.Changes {
		fields[c.Field] = true
		if c.Summary == "" {
			t.Fatal("a change without a summary")
		}
	}
	if !ch42.Available || !fields["image"] || !fields["memory limit"] || ch42.DeploymentCompared || !strings.Contains(ch42.Reason, "existed before OpsTide") || !strings.Contains(ch42.Reason, "not compared") {
		t.Fatalf("%+v", ch42)
	}
	// No preceding revision: unavailable, and it says it will not be reconstructed.
	var none ReleaseChanges
	get(t, srvURL, "/api/v1/releases/rl1/changes", &none)
	if none.Available || len(none.Changes) != 0 || !strings.Contains(none.Reason, "does not reconstruct") {
		t.Fatalf("%+v", none)
	}
}

func TestEntriesCanBeFilteredByReleaseAndByTheNewGroups(t *testing.T) {
	srvURL := releaseServer(t)
	types := func(qs string) string {
		var e Entries
		if code := get(t, srvURL, "/api/v1/events?within=30d"+qs, &e); code != 200 {
			t.Fatalf("%s: %d", qs, code)
		}
		var out []string
		for _, x := range e.Entries {
			out = append(out, x.Type)
		}
		return strings.Join(out, ",")
	}
	for qs, want := range map[string]string{
		"&show=health":                  "release.health.degraded,pod.oomkilled",
		"&show=pods":                    "pod.created",
		"&show=releases":                "release.observed,release.observed",
		"&show=releases&release=r42":    "release.observed",
		"&release=r43&show=pods,health": "release.health.degraded,pod.oomkilled,pod.created",
	} {
		if got := types(qs); got != want {
			t.Errorf("%s: got %q want %q", qs, got, want)
		}
	}
}

func TestSystemReportsReleaseObservation(t *testing.T) {
	srvURL := releaseServer(t)
	var sys System
	if code := get(t, srvURL, "/api/v1/system", &sys); code != 200 {
		t.Fatal(code)
	}
	kinds := map[string]bool{}
	for _, sc := range sys.Scopes {
		kinds[sc.Kind] = true
	}
	if !kinds["replicasets"] || !kinds["deploypods"] || !kinds["deployments"] {
		t.Fatalf("scopes %v", kinds)
	}
	if sys.Workload.WindowMinutes != 10 || sys.Workload.ActiveWindows != 1 || sys.Workload.Degraded == nil {
		t.Fatalf("%+v", sys.Workload)
	}
}

func mustQty(s string) resource.Quantity { return resource.MustParse(s) }

// Same Pod template means the same ReplicaSet (a rollback to it) or the same
// pod-template-hash; equal allow-listed settings are not enough, since the
// allow-list leaves out environment values and commands.
func TestChangesSayRollbackOnlyForTheSameReplicaSetAndNoCapturedDifferencesOtherwise(t *testing.T) {
	at := base.Add(-time.Hour)
	sn := snap("api:1", "256Mi", 0)
	a := relRow("ra", "api", "1", 1, at.Add(-3*time.Hour), "completed", "healthy", "observed", "", sn)
	b := relRow("rb", "api", "2", 2, at.Add(-2*time.Hour), "completed", "healthy", "observed", "ra", sn) // rollout restart: new ReplicaSet, same settings
	b.TemplateHash = "hash-b"
	c := relRow("rc", "api", "3", 3, at.Add(-time.Hour), "completed", "healthy", "observed", "rb", sn) // rollback to the first ReplicaSet
	c.ReplicaSetUID = a.ReplicaSetUID
	srv := setup(t, base, nil, func(ctx context.Context, w *store.Writer) error {
		for _, r := range []store.Release{a, b, c} {
			if err := w.UpsertRelease(ctx, r); err != nil {
				return err
			}
		}
		return nil
	})
	var restart, rollback ReleaseChanges
	get(t, srv, "/api/v1/releases/rb/changes", &restart)
	get(t, srv, "/api/v1/releases/rc/changes", &rollback)
	if !restart.Available || restart.SameTemplate || len(restart.Changes) != 0 || !restart.NoCapturedDifferences {
		t.Fatalf("a restart with only uncaptured changes must say so: %+v", restart)
	}
	if rollback.SameTemplate != false { // its predecessor (rb) is another ReplicaSet
		t.Fatalf("%+v", rollback)
	}
	var back ReleaseChanges
	c2 := c
	c2.PrevID = "ra"
	c2.ID = "rd"
	srv2 := setup(t, base, nil, func(ctx context.Context, w *store.Writer) error {
		for _, r := range []store.Release{a, c2} {
			if err := w.UpsertRelease(ctx, r); err != nil {
				return err
			}
		}
		return nil
	})
	get(t, srv2, "/api/v1/releases/rd/changes", &back)
	if !back.SameTemplate || back.NoCapturedDifferences {
		t.Fatalf("a release on the same ReplicaSet is the same template: %+v", back)
	}
}

// A verdict that was reached and closed is shown as it was reached, however the Pod records
// look now; only a window chosen with ?window= is judged again, and says so.
func TestAFinalVerdictIsShownAsStored(t *testing.T) {
	later := base.Add(-3 * time.Hour)
	r := relRow("rf1", "frozen", "5", 5, later, "completed", "healthy", "observed", "", snap("api:1", "", 1))
	r.CompletedAt = later.Add(time.Minute)
	r.ObserveUntil = later.Add(11 * time.Minute)
	r.HealthReasons = `["The rollout completed and nothing was found."]`
	srv := setup(t, base, nil, func(ctx context.Context, w *store.Writer) error {
		if err := w.UpsertRelease(ctx, r); err != nil {
			return err
		}
		oomAt := later.Add(5 * time.Minute)
		ps := release.PodState{Containers: map[string]*release.ContainerObs{"api": {OOM: []release.OOMObs{{At: oomAt, Observed: oomAt}}}}}
		if err := w.UpsertPod(ctx, store.PodRecord{UID: "pf", ReleaseID: "rf1", Namespace: "shop", Name: "frozen-5-a", ReplicaSetUID: "rs-rf1", WorkloadID: "d-frozen", CreatedAt: later, State: release.EncodePod(ps)}); err != nil {
			return err
		}
		for _, sc := range []string{"deployments", "replicasets", "deploypods"} {
			if err := w.OpenInterval(ctx, sc, store.Healthy, base.Add(-72*time.Hour)); err != nil {
				return err
			}
		}
		return nil
	})
	var d ReleaseDetail
	if code := get(t, srv, "/api/v1/releases/rf1", &d); code != 200 {
		t.Fatal(code)
	}
	if d.Assessment.Health != "healthy" || !d.Assessment.Stored || len(d.Assessment.Reasons) != 1 || !strings.Contains(d.Assessment.Reasons[0], "nothing was found") {
		t.Fatalf("a closed verdict must be shown as it was reached: %+v", d.Assessment)
	}
	var asked ReleaseDetail
	get(t, srv, "/api/v1/releases/rf1?window=15", &asked)
	if asked.Assessment.Stored || asked.Assessment.Health != "degraded" {
		t.Fatalf("a window chosen now is judged now: %+v", asked.Assessment)
	}
}
