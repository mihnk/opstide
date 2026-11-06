package workload

import (
	"strings"
	"testing"
)

func TestStatefulSetRolloutStatesFollowTheReportedStatus(t *testing.T) {
	base := StatefulSetInput{Generation: 3, ObservedGeneration: 3, Replicas: 5, CurrentRevision: "web-a", UpdateRevision: "web-b", Strategy: RollingUpdate}
	for name, tc := range map[string]struct {
		mutate func(*StatefulSetInput)
		want   string
		note   string
	}{
		"status not yet reported":     {func(i *StatefulSetInput) { i.ObservedGeneration = 2 }, Unknown, "generation 3 (observed generation 2)"},
		"no update revision yet":      {func(i *StatefulSetInput) { i.UpdateRevision = "" }, Unknown, ""},
		"same revision":               {func(i *StatefulSetInput) { i.UpdateRevision = "web-a"; i.Updated = 5 }, Completed, "equals"},
		"rolling, in progress":        {func(i *StatefulSetInput) { i.Updated = 2 }, Updating, "2 of 5"},
		"partition not reached":       {func(i *StatefulSetInput) { i.Partition = 2; i.Updated = 1 }, Updating, "1 of 5"},
		"partition boundary":          {func(i *StatefulSetInput) { i.Partition = 2; i.Updated = 3 }, Partitioned, "partition 2"},
		"partition covers all pods":   {func(i *StatefulSetInput) { i.Partition = 5; i.Updated = 0 }, Partitioned, "partition 5"},
		"OnDelete, template changed":  {func(i *StatefulSetInput) { i.Strategy = OnDelete; i.Updated = 0 }, AwaitingPodDeletion, "only when it is deleted"},
		"OnDelete, all Pods replaced": {func(i *StatefulSetInput) { i.Strategy = OnDelete; i.Updated = 5 }, Completed, "all 5 of 5"},
		"OnDelete, some replaced":     {func(i *StatefulSetInput) { i.Strategy = OnDelete; i.Updated = 3 }, AwaitingPodDeletion, "3 of 5"},
	} {
		in := base
		tc.mutate(&in)
		got := ClassifyStatefulSet(in)
		if got.State != tc.want || (tc.note != "" && !strings.Contains(got.Note, tc.note)) {
			t.Errorf("%s: %+v, want %s containing %q", name, got, tc.want, tc.note)
		}
		for _, banned := range []string{"fail", "stuck", "blocked", "because", "cause"} {
			if strings.Contains(strings.ToLower(got.Note), banned) {
				t.Errorf("%s: the note says %q: %q", name, banned, got.Note)
			}
		}
	}
}

func TestDaemonSetRolloutIsUpdatedAgainstDesiredAndNeverAFailure(t *testing.T) {
	base := DaemonSetInput{Generation: 2, ObservedGeneration: 2, Desired: 6, Updated: 6, Strategy: RollingUpdate}
	if got := ClassifyDaemonSet(base); got.State != Completed {
		t.Fatalf("%+v", got)
	}
	in := base
	in.Updated = 2
	if got := ClassifyDaemonSet(in); got.State != Updating || !strings.Contains(got.Note, "2 of 6") {
		t.Fatalf("%+v", got)
	}
	in.Strategy = OnDelete
	if got := ClassifyDaemonSet(in); got.State != AwaitingPodDeletion {
		t.Fatalf("a template change under OnDelete is not a completed rollout: %+v", got)
	}
	in.ObservedGeneration = 1
	if got := ClassifyDaemonSet(in); got.State != Unknown {
		t.Fatalf("%+v", got)
	}
	// Fewer nodes is a different desired count, not an update.
	if got := ClassifyDaemonSet(DaemonSetInput{Generation: 2, ObservedGeneration: 2, Desired: 0, Updated: 0}); got.State != Completed {
		t.Fatalf("%+v", got)
	}
}

func TestEntriesAreWrittenForChangesAndNeverForABaseline(t *testing.T) {
	r := func(s string) Rollout { return Rollout{State: s} }
	for name, tc := range map[string]struct {
		prev, next Rollout
		rev        bool
		want       string
	}{
		"first sight":                   {Rollout{}, r(Updating), false, ""},
		"status not reported before":    {r(Unknown), r(Updating), true, ""},
		"completed to updating":         {r(Completed), r(Updating), true, "update_started"},
		"still updating, same revision": {r(Updating), r(Updating), false, ""},
		"newer revision while updating": {r(Updating), r(Updating), true, "update_started"},
		"updating to completed":         {r(Updating), r(Completed), false, "update_completed"},
		"completed stays":               {r(Completed), r(Completed), false, ""},
		"updating to partitioned":       {r(Updating), r(Partitioned), false, "update_partitioned"},
		"completed to partitioned":      {r(Completed), r(Partitioned), true, "update_started,update_partitioned"},
		"completed to OnDelete":         {r(Completed), r(AwaitingPodDeletion), true, "update_started,update_awaiting_pod_deletion"},
		"partitioned to completed":      {r(Partitioned), r(Completed), false, "update_completed"},
	} {
		if got := strings.Join(Entries(tc.prev, tc.next, tc.rev), ","); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

func TestCountChangesAreListedInAFixedOrder(t *testing.T) {
	got := Describe(Changes(Counts{Desired: 3, Ready: 3, Available: 3, Updated: 3}, Counts{Desired: 3, Ready: 2, Available: 2, Updated: 3, Misscheduled: 1}))
	if got != "ready 3 to 2, available 3 to 2" {
		t.Fatal(got)
	}
	got = Describe(Changes(Counts{Misscheduled: 0, Current: 4}, Counts{Misscheduled: 1, Current: 5}, "current", "misscheduled"))
	if got != "current 4 to 5, misscheduled 0 to 1" {
		t.Fatal(got)
	}
}

func TestPodRevisionAndOrdinalAreReadOnlyWhereKubernetesStatesThem(t *testing.T) {
	if !PodRevisionMatches("web-7d9c", "web-7d9c", "7d9c") || !PodRevisionMatches("7d9c", "web-7d9c", "7d9c") || PodRevisionMatches("", "web-7d9c", "7d9c") || PodRevisionMatches("other", "web-7d9c", "7d9c") {
		t.Fatal("revision label")
	}
	for name, tc := range map[string]struct {
		pod, sts, label string
		n               int
		ok              bool
	}{
		"label wins":      {"web-9", "web", "2", 2, true},
		"name":            {"web-3", "web", "", 3, true},
		"another sts":     {"webx-3", "web", "", 0, false},
		"no index":        {"web-abc", "web", "", 0, false},
		"leading zero":    {"web-03", "web", "", 0, false},
		"signed":          {"web--1", "web", "", 0, false},
		"sts name itself": {"web", "web", "", 0, false},
	} {
		n, ok := Ordinal(tc.pod, tc.sts, tc.label)
		if n != tc.n || ok != tc.ok {
			t.Errorf("%s: %d %v", name, n, ok)
		}
	}
}
