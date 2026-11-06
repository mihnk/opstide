package release

import (
	"encoding/json"
	"time"

	"github.com/mihnk/opstide/internal/store"
)

// ScopeNames are the coverage scopes release observation depends on for a
// namespace: Deployments, their ReplicaSets and their Pods. clusterWide says
// whether OpsTide watches the whole cluster (one scope per kind) or per
// namespace.
func ScopeNames(namespace string, clusterWide bool) []string {
	kinds := []string{"deployments", "replicasets", "deploypods"}
	if clusterWide {
		return kinds
	}
	out := make([]string, len(kinds))
	for i, k := range kinds {
		out[i] = k + "/" + namespace
	}
	return out
}

// DecodePod reads a stored Pod's lifecycle record.
func DecodePod(raw string) (PodState, error) {
	var s PodState
	err := json.Unmarshal([]byte(raw), &s)
	return s, err
}

// EncodePod is the stored form of a Pod's lifecycle record.
func EncodePod(s PodState) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// DecodeEpisodes reads a release's availability episodes.
func DecodeEpisodes(raw string) []Episode {
	var e []Episode
	_ = json.Unmarshal([]byte(raw), &e)
	return e
}

// EncodeEpisodes is the stored form of availability episodes.
func EncodeEpisodes(e []Episode) string {
	if len(e) > maxKept {
		e = e[len(e)-maxKept:]
	}
	b, _ := json.Marshal(e)
	if string(b) == "null" {
		return "[]"
	}
	return string(b)
}

// DecodeTrouble reads a release's stalled or failed rollout history.
func DecodeTrouble(raw string) []Trouble {
	var t []Trouble
	_ = json.Unmarshal([]byte(raw), &t)
	return t
}

// EncodeTrouble is the stored form of that history.
func EncodeTrouble(t []Trouble) string {
	if len(t) > maxKept {
		t = t[len(t)-maxKept:]
	}
	b, _ := json.Marshal(t)
	if string(b) == "null" {
		return "[]"
	}
	return string(b)
}

// DecodeReasons reads a stored list of reasons.
func DecodeReasons(raw string) []string {
	var r []string
	_ = json.Unmarshal([]byte(raw), &r)
	return r
}

// EncodeReasons is the stored form of a list of reasons.
func EncodeReasons(r []string) string {
	b, _ := json.Marshal(r)
	if string(b) == "null" {
		return "[]"
	}
	return string(b)
}

// InputFor builds the assessment input for a stored release and its stored
// Pods. window is the observation length to judge it by; zero uses the one the
// release was observed with. watched may be nil (coverage unknown).
func InputFor(rel store.Release, pods []store.PodRecord, rollback bool, now time.Time, window time.Duration, watched func(from, to time.Time) bool) Input {
	if window <= 0 {
		window = time.Duration(rel.WindowSeconds) * time.Second
	}
	in := Input{
		Origin: rel.Origin, Rollout: rel.State, RolloutNote: rel.StateReason,
		ObservedAt: rel.ObservedAt, CompletedAt: rel.CompletedAt, SupersededAt: rel.SupersededAt,
		Window: window, Desired: rel.Desired, Episodes: DecodeEpisodes(rel.Episodes), Trouble: DecodeTrouble(rel.Trouble), Now: now, Watched: watched, Rollback: rollback, Version: rel.AssessmentVersion,
	}
	for _, p := range pods {
		st, err := DecodePod(p.State)
		if err != nil {
			continue
		}
		in.Pods = append(in.Pods, PodInput{Name: p.Name, State: st})
	}
	return in
}
