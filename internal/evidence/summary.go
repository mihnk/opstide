// Package evidence builds the facts-only summary of a release from what was
// recorded. It has no model, no heuristics and no notion of cause: every line
// is one observed value or one documented rule applied to observed values,
// named as such, with the records it was built from.
package evidence

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mihnk/opstide/internal/release"
	"github.com/mihnk/opstide/internal/store"
)

// RootCause is shown with every summary. OpsTide records evidence; whoever reads
// it, person or agent, interprets it.
const RootCause = "Not established by OpsTide."

// Fact is one line of the summary.
type Fact struct {
	Text string    `json:"text"`
	Rule string    `json:"rule"` // the rule that produced the line
	At   time.Time `json:"at,omitzero"`
	// Refs are the records the line was built from: evidence ids, log entry ids,
	// or "release:<id>".
	Refs []string `json:"refs"`
}

// Summary is the compact "recorded evidence" list.
type Summary struct {
	Facts     []Fact `json:"facts"`
	RootCause string `json:"rootCause"`
	// Complete is false when the records it was built from were cut off, so the
	// list may be missing facts. The records themselves are always available.
	Complete bool   `json:"complete"`
	Note     string `json:"note,omitempty"`
}

// Input is everything the summary reads.
type Input struct {
	Release  store.Release
	Pods     []string // names of the Pods observed under the release's ReplicaSet
	Records  []store.Evidence
	Scaling  []store.Event // hpa.* and workload.replica_target_changed entries
	Context  []release.Note
	Complete bool
}

const maxEventFacts = 10

func hm(t time.Time) string { return t.UTC().Format("15:04 UTC") }

// Build constructs the summary. The same input gives the same lines.
func Build(in Input) Summary {
	r := in.Release
	s := Summary{Facts: []Fact{}, RootCause: RootCause, Complete: in.Complete}
	add := func(rule, text string, at time.Time, refs ...string) {
		s.Facts = append(s.Facts, Fact{Text: text, Rule: rule, At: at, Refs: refs})
	}
	rel := "release:" + r.ID

	if r.Origin == store.OriginAdopted {
		add("release.adopted", fmt.Sprintf("Revision %s was already present when OpsTide began watching; its rollout was not observed.", r.Revision), r.ObservedAt, rel)
	} else {
		add("release.observed", fmt.Sprintf("Revision %s was observed at %s.", r.Revision, hm(r.ObservedAt)), r.ObservedAt, rel)
	}
	if n := len(in.Pods); n > 0 {
		add("release.pods_observed", fmt.Sprintf("%d %s observed under ReplicaSet %s.", n, plural(n, "Pod was", "Pods were"), r.ReplicaSetName), time.Time{}, rel)
	}
	for _, t := range release.DecodeTrouble(r.Trouble) {
		add("rollout.reported", fmt.Sprintf("Kubernetes reported the rollout %s at %s: %s.", t.State, hm(t.At), strings.TrimSuffix(t.Reason, ".")), t.At, rel)
	}

	// Events, grouped by what they report and about which resource, newest group first.
	type key struct{ reason, kind, name, typ string }
	groups := map[key][]store.Evidence{}
	var order []key
	for _, e := range in.Records {
		k := key{e.Reason, e.Regarding.Kind, e.Regarding.Name, e.EventType}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], e)
	}
	sort.SliceStable(order, func(i, j int) bool {
		return latest(groups[order[i]]).After(latest(groups[order[j]]))
	})
	shown := 0
	for _, k := range order {
		if k.typ != "Warning" {
			continue // Normal Events are listed in the records; the summary keeps to what Kubernetes marked a warning
		}
		if shown == maxEventFacts {
			break
		}
		shown++
		recs := groups[k]
		var refs []string
		for _, e := range recs {
			refs = append(refs, e.ID)
		}
		text := fmt.Sprintf("Kubernetes reported %s for %s %s", k.reason, k.kind, k.name)
		if at := latest(recs); !at.IsZero() {
			text += " at " + hm(at)
		}
		switch {
		case len(recs) == 1 && recs[0].Count > 0:
			text += fmt.Sprintf(" (count reported by Kubernetes: %d)", recs[0].Count)
		case len(recs) > 1:
			text += fmt.Sprintf(" (%d separate Events are recorded)", len(recs))
		}
		add("event.reported", text+".", latest(recs), refs...)
	}
	if extra := warningGroups(groups) - shown; extra > 0 {
		add("event.more", fmt.Sprintf("%d more Warning %s are recorded; see the records below.", extra, plural(extra, "reason", "reasons")), time.Time{})
	}

	for _, e := range in.Scaling {
		switch e.Type {
		case "hpa.desired_replicas_changed":
			add("scaling.hpa_desired", fmt.Sprintf("HPA %s reported desired replicas changed from %s to %s (observed at %s).", e.Facts["hpa"], e.Facts["from"], e.Facts["to"], hm(e.ObservedAt)), e.ObservedAt, e.ID)
		case "workload.replica_target_changed":
			add("scaling.replica_target", fmt.Sprintf("The Deployment's replica target changed from %s to %s (observed at %s).", e.Facts["from"], e.Facts["to"], hm(e.ObservedAt)), e.ObservedAt, e.ID)
		}
	}
	for _, n := range in.Context {
		add(n.Rule, n.Summary+".", time.Time{}, rel)
	}

	// Chronological; a line with no time of its own sorts with the line before it.
	keys := make([]time.Time, len(s.Facts))
	for i, f := range s.Facts {
		keys[i] = f.At
		if f.At.IsZero() && i > 0 {
			keys[i] = keys[i-1]
		}
	}
	idx := make([]int, len(s.Facts))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return keys[idx[a]].Before(keys[idx[b]]) })
	sorted := make([]Fact, len(s.Facts))
	for i, k := range idx {
		sorted[i] = s.Facts[k]
	}
	s.Facts = sorted
	if !in.Complete {
		s.Note = "The records this list is built from were cut off at the limit, so it may be missing facts."
	}
	return s
}

func latest(recs []store.Evidence) time.Time {
	var t time.Time
	for _, e := range recs {
		if st := e.SortTime(); st.After(t) {
			t = st
		}
	}
	return t
}

func warningGroups[K comparable](g map[K][]store.Evidence) int {
	n := 0
	for _, recs := range g {
		if len(recs) > 0 && recs[0].EventType == "Warning" {
			n++
		}
	}
	return n
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
