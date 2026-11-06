package evidence

import (
	"strings"
	"testing"
	"time"

	"github.com/mihnk/opstide/internal/release"
	"github.com/mihnk/opstide/internal/store"
)

var t0 = time.Date(2026, 11, 5, 14, 2, 0, 0, time.UTC)

func TestSummaryIsFactsOnlyAndSaysRootCauseIsNotEstablished(t *testing.T) {
	r := store.Release{ID: "r43", Revision: "43", Origin: store.OriginObserved, ObservedAt: t0, ReplicaSetName: "api-7d9",
		Trouble: release.EncodeTrouble([]release.Trouble{{State: "stalled", Reason: "ProgressDeadlineExceeded: timed out", At: t0.Add(5 * time.Minute)}})}
	in := Input{Release: r, Pods: []string{"a", "b", "c"}, Complete: true,
		Records: []store.Evidence{
			{ID: "ev1", Reason: "Unhealthy", EventType: "Warning", Count: 3, LastAt: t0.Add(3 * time.Minute), Regarding: store.Ref{Kind: "Pod", Name: "a"}},
			{ID: "ev2", Reason: "Pulled", EventType: "Normal", Regarding: store.Ref{Kind: "Pod", Name: "a"}, LastAt: t0.Add(time.Minute)},
		},
		Scaling: []store.Event{{ID: "s1", Type: "hpa.desired_replicas_changed", ObservedAt: t0.Add(7 * time.Minute), Facts: map[string]string{"hpa": "api", "from": "3", "to": "5"}}},
	}
	s := Build(in)
	if s.RootCause != "Not established by OpsTide." || !s.Complete {
		t.Fatalf("%+v", s)
	}
	var text []string
	for _, f := range s.Facts {
		text = append(text, f.Text)
		if f.Rule == "" {
			t.Errorf("a line without a rule: %+v", f)
		}
	}
	all := strings.Join(text, "\n")
	for _, want := range []string{
		"Revision 43 was observed at 14:02 UTC.",
		"3 Pods were observed under ReplicaSet api-7d9.",
		"Kubernetes reported Unhealthy for Pod a at 14:05 UTC (count reported by Kubernetes: 3).",
		"Kubernetes reported the rollout stalled at 14:07 UTC: ProgressDeadlineExceeded: timed out.",
		"HPA api reported desired replicas changed from 3 to 5 (observed at 14:09 UTC).",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	if strings.Contains(all, "Pulled") {
		t.Error("a Normal Event was summarised as a fact of the release")
	}
	low := strings.ToLower(all)
	for _, banned := range []string{"because", "caused", "probably", "likely", "root cause is", "due to", "traffic"} {
		if strings.Contains(low, banned) {
			t.Errorf("the summary uses %q", banned)
		}
	}
	// Chronological, with the evidence behind each line.
	if !strings.HasPrefix(text[0], "Revision 43") || s.Facts[len(s.Facts)-1].Rule != "scaling.hpa_desired" {
		t.Errorf("order: %v", text)
	}
	if got := s.Facts[2].Refs; len(got) != 1 || got[0] != "ev1" {
		t.Errorf("refs: %v", got)
	}
}

func TestSummaryIsDeterministicAndAdmitsWhenItIsIncomplete(t *testing.T) {
	in := Input{Release: store.Release{ID: "r", Revision: "9", Origin: store.OriginAdopted, ObservedAt: t0}, Complete: false}
	a, b := Build(in), Build(in)
	if len(a.Facts) != len(b.Facts) || a.Facts[0].Text != b.Facts[0].Text {
		t.Fatal("not deterministic")
	}
	if !strings.Contains(a.Facts[0].Text, "already present") || a.Note == "" || a.Complete {
		t.Fatalf("%+v", a)
	}
}
