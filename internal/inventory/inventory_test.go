package inventory

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func rev(n int64, at time.Time, cs ...Container) Revision {
	return Revision{WorkloadID: "d-api", Kind: "Deployment", Namespace: "shop", Workload: "api", ID: "r" + string(rune('0'+n)), ReleaseID: "rel-" + string(rune('0'+n)),
		Revision: string(rune('0' + n)), Number: n, CreatedAt: at, ObservedAt: at, Provenance: Observed, Containers: cs}
}

func find(us []Use, image, container string) []Use {
	var out []Use
	for _, u := range us {
		if u.Image == image && u.Container == container {
			out = append(out, u)
		}
	}
	return out
}

// A replacement ends a row and opens the next; a container that keeps its image across revisions is one row; an init
// container is labelled; a sidecar's image shared by every revision runs from the first to now.
func TestReplacementEndsARowAndUnchangedImagesSpanRevisions(t *testing.T) {
	app := func(img string) Container { return Container{Name: "app", Image: img} }
	side := Container{Name: "log", Image: "shop/log-shipper:1.2"}
	mig := func(img string) Container { return Container{Name: "migrate", Image: img, Init: true} }
	r5 := rev(5, t0, app("shop/api:1.5.0"), side, mig("shop/migrate:1"))
	r5.ReportedIDs = map[string]string{"app": "sha256:aaa"}
	r6 := rev(6, t0.Add(24*time.Hour), app("shop/api:1.5.0"), side, mig("shop/migrate:2")) // env change only for app
	r7 := rev(7, t0.Add(48*time.Hour), app("shop/api:1.5.1"), side)                        // migrate removed
	us := FromRevisions([]Revision{r7, r5, r6})                                            // any order

	old := find(us, "shop/api:1.5.0", "app")
	if len(old) != 1 {
		t.Fatalf("%+v", us)
	}
	u := old[0]
	if !u.Since.Equal(t0) || !u.Until.Equal(t0.Add(48*time.Hour)) || u.UntilBasis != UntilReplaced || u.ReplacedBy != "shop/api:1.5.1" ||
		u.FromRevision != "5" || u.ToRevision != "6" || u.ReleaseID != "rel-5" || u.LatestReleaseID != "rel-6" || u.SinceBasis != SinceCreated || u.ReportedImageID != "sha256:aaa" {
		t.Fatalf("1.5.0 %+v", u)
	}
	cur := find(us, "shop/api:1.5.1", "app")
	if len(cur) != 1 || !cur[0].Until.IsZero() || cur[0].Previous != "shop/api:1.5.0" || cur[0].Provenance != Observed {
		t.Fatalf("1.5.1 %+v", cur)
	}
	if s := find(us, "shop/log-shipper:1.2", "log"); len(s) != 1 || !s[0].Since.Equal(t0) || !s[0].Until.IsZero() || s[0].ToRevision != "7" {
		t.Fatalf("sidecar %+v", s)
	}
	m1, m2 := find(us, "shop/migrate:1", "migrate"), find(us, "shop/migrate:2", "migrate")
	if len(m1) != 1 || !m1[0].Init || m1[0].ReplacedBy != "shop/migrate:2" || len(m2) != 1 || m2[0].UntilBasis != UntilContainerRemoved || !m2[0].Until.Equal(t0.Add(48*time.Hour)) {
		t.Fatalf("init %+v %+v", m1, m2)
	}
	if m1[0].ReportedImageID != "" {
		t.Fatal("a Pod's reported image ID is for the app container, not the init one")
	}
	if len(us) != 5 {
		t.Fatalf("%d uses: %+v", len(us), us)
	}

	// At a time: what each container ran then.
	at := func(t time.Time) map[string]string {
		out := map[string]string{}
		for _, u := range us {
			if u.ActiveAt(t) {
				out[u.Container] = u.Image
			}
		}
		return out
	}
	if a := at(t0.Add(30 * time.Hour)); a["app"] != "shop/api:1.5.0" || a["migrate"] != "shop/migrate:2" || a["log"] != "shop/log-shipper:1.2" {
		t.Fatal(a)
	}
	if a := at(t0.Add(48 * time.Hour)); a["app"] != "shop/api:1.5.1" || a["migrate"] != "" {
		t.Fatal(a)
	}
	if a := at(t0.Add(-time.Minute)); len(a) != 0 {
		t.Fatalf("before the first revision nothing is claimed: %v", a)
	}
}

func TestDeletedWorkloadsEndAndRollbacksUseWhenTheyBecameNewest(t *testing.T) {
	gone := t0.Add(72 * time.Hour)
	a := rev(1, t0, Container{Name: "app", Image: "x:1"})
	b := rev(2, t0.Add(24*time.Hour), Container{Name: "app", Image: "x:2"})
	// Revision 3 is revision 1's ReplicaSet made current again: its creation time is the old one.
	c := rev(3, t0, Container{Name: "app", Image: "x:1"})
	c.BecameNewestAt = t0.Add(50 * time.Hour)
	for _, r := range []*Revision{&a, &b, &c} {
		r.WorkloadDeletedAt = gone
	}
	us := FromRevisions([]Revision{a, b, c})
	back := find(us, "x:1", "app")
	if len(back) != 2 {
		t.Fatalf("%+v", us)
	}
	last := back[1]
	if back[0].Since.After(back[1].Since) {
		last = back[0]
	}
	if !last.Since.Equal(t0.Add(50*time.Hour)) || last.SinceBasis != SinceObserved || !last.Until.Equal(gone) || last.UntilBasis != UntilWorkloadDeleted {
		t.Fatalf("%+v", last)
	}
}

func TestStatefulSetRevisionsAreDerivedAndAdoptedOnesSaySo(t *testing.T) {
	r := Revision{WorkloadID: "sts", Kind: "StatefulSet", Namespace: "data", Workload: "db", ID: "v1", Revision: "db-1", Number: 1, CreatedAt: t0, Adopted: true,
		Provenance: Derived, Containers: []Container{{Name: "db", Image: "postgres:16.4"}}}
	us := FromRevisions([]Revision{r})
	if len(us) != 1 || us[0].Provenance != Derived || !us[0].Adopted || us[0].Note == "" {
		t.Fatalf("%+v", us)
	}
}

func TestTemplates(t *testing.T) {
	ts := []Template{
		{WorkloadID: "cj", Kind: "CronJob", Namespace: "data", Workload: "backup", Container: "dump", Image: "postgres:16.4", Adopted: true, FirstSeenAt: t0, EndedAt: t0.Add(time.Hour)},
		{WorkloadID: "cj", Kind: "CronJob", Namespace: "data", Workload: "backup", Container: "dump", Image: "postgres:16.5", FirstSeenAt: t0.Add(time.Hour)},
		{WorkloadID: "cj", Kind: "CronJob", Namespace: "data", Workload: "backup", Container: "wait", Init: true, Image: "busybox", FirstSeenAt: t0, EndedAt: t0.Add(2 * time.Hour)},
	}
	us := FromTemplates(ts)
	if us[0].ReplacedBy != "postgres:16.5" || us[0].UntilBasis != UntilReplaced || us[0].SinceBasis != SinceFirstRecorded {
		t.Fatalf("%+v", us[0])
	}
	if us[1].Previous != "postgres:16.4" || !us[1].Until.IsZero() || us[1].SinceBasis != SinceObserved {
		t.Fatalf("%+v", us[1])
	}
	if !us[2].Init || us[2].UntilBasis != UntilContainerRemoved {
		t.Fatalf("%+v", us[2])
	}
}

func TestParseRef(t *testing.T) {
	for in, want := range map[string]Ref{
		"shop/api:1.5.1":                       {"shop/api", "1.5.1", ""},
		"registry.example.com:5000/team/x:2.0": {"registry.example.com:5000/team/x", "2.0", ""},
		"registry.example.com:5000/team/x":     {"registry.example.com:5000/team/x", "", ""},
		"postgres":                             {"postgres", "", ""},
		"ghcr.io/a/b:1@sha256:abc":             {"ghcr.io/a/b", "1", "sha256:abc"},
		"ghcr.io/a/b@sha256:abc":               {"ghcr.io/a/b", "", "sha256:abc"},
	} {
		if got := ParseRef(in); got != want {
			t.Errorf("%s: %+v, want %+v", in, got, want)
		}
	}
}
