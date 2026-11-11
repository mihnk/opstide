package inventory

import (
	"sort"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func rev(n int64, at time.Time, cs ...Container) Revision {
	return Revision{WorkloadID: "d-api", Kind: "Deployment", Namespace: "shop", Workload: "api", ID: "r" + string(rune('0'+n)), ReleaseID: "rel-" + string(rune('0'+n)),
		Revision: string(rune('0' + n)), Number: n, CreatedAt: at, ObservedAt: at, Containers: cs}
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
	if len(cur) != 1 || !cur[0].Until.IsZero() || cur[0].Previous != "shop/api:1.5.0" || cur[0].Provenance != Derived || cur[0].TemplateProvenance != Observed || !cur[0].Current() {
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
		Containers: []Container{{Name: "db", Image: "postgres:16.4"}}}
	us := FromRevisions([]Revision{r})
	if len(us) != 1 || us[0].Provenance != Derived || us[0].TemplateProvenance != Observed || !us[0].Adopted || !hasNote(us[0], NoteAdoptedCreated) || !hasNote(us[0], NoteControllerRevision) {
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
	// Rows ended before the reason was recorded: replaced when the next row starts there, else not known.
	if !us[2].Init || us[2].UntilBasis != UntilUnknown || us[2].Current() {
		t.Fatalf("%+v", us[2])
	}
	ts[2].EndReason = UntilContainerRemoved
	ts = append(ts, Template{WorkloadID: "cj2", Kind: "CronJob", Container: "c", Image: "a:1", FirstSeenAt: t0, EndedAt: t0.Add(time.Hour), EndReason: UntilWorkloadDeleted},
		Template{WorkloadID: "cj3", Kind: "CronJob", Container: "c", Image: "b:1", FirstSeenAt: t0, EndedAt: t0.Add(time.Hour), EndReason: UntilUnknown},
		Template{WorkloadID: "cj3", Kind: "CronJob", Container: "c", Image: "b:2", Adopted: true, FirstSeenAt: t0.Add(time.Hour)})
	us = FromTemplates(ts)
	if us[2].UntilBasis != UntilContainerRemoved || us[3].UntilBasis != UntilWorkloadDeleted || us[4].UntilBasis != UntilUnknown || us[4].ReplacedBy != "b:2" ||
		!hasNote(us[4], NoteChangedUnwatched) || us[5].SinceBasis != SinceFirstRecorded {
		t.Fatalf("%+v", us[2:])
	}
	// At a time inside the stretch whose end is not known: it may have been current, not certain.
	if in, certain := us[4].At(t0.Add(30 * time.Minute)); !in || certain {
		t.Fatal("uncertain")
	}
}

func hasNote(u Use, code string) bool {
	for _, n := range u.Notes {
		if n.Code == code {
			return true
		}
	}
	return false
}

// A StatefulSet rolled back to an earlier ControllerRevision renumbers it in place: its earlier stretch (from its
// creation until the next revision) is put back, so the history reads 1 -> 2 -> 1, not 2 -> 1.
func TestRenumberedControllerRevisionKeepsItsEarlierStretch(t *testing.T) {
	cr := func(id string, n int64, created time.Time, img string) Revision {
		return Revision{WorkloadID: "sts", Kind: "StatefulSet", Namespace: "data", Workload: "db", ID: id, Revision: id, Number: n, CreatedAt: created, ObservedAt: created,
			RenumberedInPlace: true, Containers: []Container{{Name: "db", Image: img}}}
	}
	b := cr("db-b", 2, t0.Add(24*time.Hour), "postgres:16.5")
	a := cr("db-a", 3, t0, "postgres:16.4") // renumbered from 1 by a rollback seen 50 h in
	a.BecameNewestAt = t0.Add(50 * time.Hour)
	us := FromRevisions([]Revision{a, b})
	old := find(us, "postgres:16.4", "db")
	if len(old) != 2 {
		t.Fatalf("%+v", us)
	}
	sort.Slice(old, func(i, j int) bool { return old[i].Since.Before(old[j].Since) })
	if !old[0].Since.Equal(t0) || old[0].SinceBasis != SinceCreated || !old[0].Until.Equal(t0.Add(24*time.Hour)) || old[0].UntilBasis != UntilReplaced || !hasNote(old[0], NoteEarlierPeriod) {
		t.Fatalf("earlier %+v", old[0])
	}
	if !old[1].Since.Equal(t0.Add(50*time.Hour)) || old[1].SinceBasis != SinceObserved || !old[1].Current() {
		t.Fatalf("after the rollback %+v", old[1])
	}
	if nw := find(us, "postgres:16.5", "db"); len(nw) != 1 || !nw[0].Until.Equal(t0.Add(50*time.Hour)) || nw[0].Previous != "postgres:16.4" {
		t.Fatalf("between %+v", nw)
	}
}

// A Deployment rollback done before OpsTide watched: since is when OpsTide found it (first_recorded) and the use before
// it ended by then, when exactly not known. With no usable time at all, since is not known: never a copy of the
// previous since (no zero-length use).
func TestRollbacksWithoutAUsableTime(t *testing.T) {
	a := rev(1, t0, Container{Name: "app", Image: "x:1"})
	b := rev(2, t0.Add(24*time.Hour), Container{Name: "app", Image: "x:2"})
	c := rev(3, t0, Container{Name: "app", Image: "x:1"})
	c.Adopted, c.RollbackUnseen, c.BecameNewestAt = true, true, t0.Add(72*time.Hour)
	us := FromRevisions([]Revision{a, b, c})
	two := find(us, "x:2", "app")
	if len(two) != 1 || two[0].UntilBasis != UntilUnknown || !two[0].Until.Equal(t0.Add(72*time.Hour)) || !hasNote(two[0], NoteUntilUnknown) {
		t.Fatalf("before the rollback %+v", two)
	}
	var back Use
	for _, u := range find(us, "x:1", "app") {
		if u.FromRevision == "3" {
			back = u
		}
	}
	if back.SinceBasis != SinceFirstRecorded || !back.Since.Equal(t0.Add(72*time.Hour)) || !hasNote(back, NoteRollbackFound) {
		t.Fatalf("rollback %+v", back)
	}

	// No usable time: BecameNewestAt is not after the revision before.
	c.RollbackUnseen, c.BecameNewestAt = false, t0.Add(time.Hour)
	d := rev(4, t0.Add(96*time.Hour), Container{Name: "app", Image: "x:4"})
	us = FromRevisions([]Revision{a, b, c, d})
	for _, u := range us {
		if u.FromRevision == "3" {
			if !u.Since.IsZero() || u.SinceBasis != SinceUnknown || !u.SinceAfter.Equal(t0.Add(24*time.Hour)) || !u.Until.Equal(t0.Add(96*time.Hour)) {
				t.Fatalf("unknown since %+v", u)
			}
			if in, certain := u.At(t0.Add(48 * time.Hour)); !in || certain {
				t.Fatal("it may have been current after the revision before it")
			}
		}
		if u.Image == "x:2" && (u.UntilBasis != UntilUnknown || !u.Until.Equal(t0.Add(96*time.Hour))) {
			t.Fatalf("ended by the next known time %+v", u)
		}
		if !u.Since.IsZero() && !u.Until.IsZero() && !u.Until.After(u.Since) {
			t.Fatalf("zero-length use %+v", u)
		}
	}
}

// A failed rollout's stretch is UNKNOWN (its Pods may never have run it); a stalled one stays DERIVED, with a note.
func TestFailedRolloutsAreUnknown(t *testing.T) {
	a := rev(1, t0, Container{Name: "app", Image: "x:1"})
	a.State = "failed"
	b := rev(1, t0, Container{Name: "app", Image: "x:1"})
	b.WorkloadID, b.State = "other", "stalled"
	us := FromRevisions([]Revision{a, b})
	if us[0].Provenance != Unknown || !hasNote(us[0], NoteRollout) || us[1].Provenance != Derived || !hasNote(us[1], NoteRollout) {
		t.Fatalf("%+v", us)
	}
}

func TestHeld(t *testing.T) {
	h := HeldOf([]Revision{rev(2, t0.Add(time.Hour)), rev(1, t0)})
	if len(h) != 1 || !h[0].Since.Equal(t0) || !h[0].NothingBefore {
		t.Fatalf("%+v", h)
	}
	h = HeldOf([]Revision{rev(5, t0)})
	if h[0].NothingBefore {
		t.Fatal("revision 5 had revisions before it")
	}
	ht := HeldOfTemplates([]Template{{WorkloadID: "cj", FirstSeenAt: t0, Adopted: true}})
	if ht[0].NothingBefore {
		t.Fatal("adopted: what it named before is not known")
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
