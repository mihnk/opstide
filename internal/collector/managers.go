package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mihnk/opstide/internal/store"
)

// The change ledger: which client last wrote an object whose spec it manages, and when. A manager's time moves when it
// writes any field it owns, so this says a manager of the spec wrote, not that the spec changed; the revision, replica
// and schedule entries say what changed. Kubernetes
// records, per field manager, when it last wrote and which fields (managedFields). OpsTide keeps only the manager's
// name, the kind of operation and the time of writes that touched spec, never the fields or any value. A field manager
// is the name a client gave itself ("argocd-controller", "kubectl-edit", "helm"): it says which software wrote, not
// which person; the API server's audit log holds who.

// specWrite is one manager's latest write that touched spec.
type specWrite struct {
	Manager, Operation, Subresource string
	At                              time.Time
}

// touchesSpec says whether a managedFields entry's field set has spec at its top level, without keeping the fields.
func touchesSpec(f *metav1.FieldsV1) bool {
	if f == nil || len(f.Raw) == 0 {
		return false
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(f.Raw, &top) != nil {
		return false
	}
	_, ok := top["f:spec"]
	return ok
}

// slimManagedFields keeps, from an object's managedFields, only the entries that touched spec, each with a marker in
// place of its field set, so the informer cache does not hold the (large) field sets.
func slimManagedFields(mf []metav1.ManagedFieldsEntry) []metav1.ManagedFieldsEntry {
	var out []metav1.ManagedFieldsEntry
	for _, e := range mf {
		if !touchesSpec(e.FieldsV1) {
			continue
		}
		e.FieldsV1 = &metav1.FieldsV1{Raw: []byte(`{"f:spec":{}}`)}
		out = append(out, e)
	}
	return out
}

// specWrites lists the latest spec write of each manager (and operation, and subresource), oldest first.
func specWrites(mf []metav1.ManagedFieldsEntry) []specWrite {
	latest := map[string]specWrite{}
	for _, e := range mf {
		// Some writes come with no time (the scale subresource's, for one). They are left out: a write with no time cannot be
		// placed on a timeline, and the replica change it made is recorded on its own.
		if e.Time == nil || e.Manager == "" || !touchesSpec(e.FieldsV1) {
			continue
		}
		w := specWrite{Manager: e.Manager, Operation: string(e.Operation), Subresource: e.Subresource, At: e.Time.UTC()}
		k := w.Manager + "|" + w.Operation + "|" + w.Subresource
		if old, ok := latest[k]; !ok || w.At.After(old.At) {
			latest[k] = w
		}
	}
	out := make([]specWrite, 0, len(latest))
	for _, w := range latest {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.Before(out[j].At)
		}
		return out[i].Manager < out[j].Manager
	})
	return out
}

// writtenSeen remembers the writes already recorded, so a sync that changes nothing writes nothing. A write is
// remembered only after it is committed: a failed write is tried again with the sync's retry.
type writtenSeen struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func (s *writtenSeen) has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.seen[id]
	return ok
}

func (s *writtenSeen) add(ids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil || len(s.seen) > 50_000 {
		s.seen = map[string]struct{}{}
	}
	for _, id := range ids {
		s.seen[id] = struct{}{}
	}
}

// recordSpecWrites writes a Log entry for each spec write of an object not yet recorded. word is the lowercase kind
// ("deployment"); the entry type is word + ".spec_written". Entries are deterministic by manager, operation and time, so
// seeing the same write again after a relist or a restart adds nothing. A write older than this process is marked as
// already there when OpsTide began watching.
func (c *Collector) recordSpecWrites(ctx context.Context, word, kind, ns, name, uid string, mf []metav1.ManagedFieldsEntry) error {
	var fresh []specWrite
	var keys []string
	for _, w := range specWrites(mf) {
		k := fmt.Sprintf("%s/%s/%s/%s/%s/%d", word, uid, w.Manager, w.Operation, w.Subresource, w.At.Unix())
		if !c.written.has(k) {
			fresh, keys = append(fresh, w), append(keys, k)
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	now := c.now()
	if err := c.db.Update(ctx, func(w *store.Writer) error {
		for _, sw := range fresh {
			how := sw.Operation
			if sw.Subresource != "" {
				how += ", " + sw.Subresource
			}
			seen := "recorded after OpsTide started watching"
			if sw.At.Add(time.Second).Before(c.started) { // managedFields times are whole seconds
				seen = "already there when OpsTide began watching"
			}
			facts := map[string]string{"fieldManager": sw.Manager, "operation": sw.Operation, "writtenAt": sw.At.Format(time.RFC3339), "seen": seen}
			if sw.Subresource != "" {
				facts["subresource"] = sw.Subresource
			}
			e := store.Event{
				ID:   fmt.Sprintf("%s.spec_written/%s/%s/%s/%s/%d", word, uid, sw.Manager, sw.Operation, sw.Subresource, sw.At.Unix()),
				Type: word + ".spec_written", Namespace: ns, Kind: kind, ResourceUID: uid, ResourceName: name,
				OccurredAt: sw.At, ObservedAt: now, Severity: "info", Confidence: store.Observed,
				Summary: fmt.Sprintf("%s %s/%s: written by %s (%s), a manager of its spec", kind, ns, name, sw.Manager, how), Facts: facts,
			}
			if kind == "CronJob" {
				e.OperationID = uid
			} else {
				e.WorkloadID = uid
			}
			if _, err := w.AddEvent(ctx, e); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	c.written.add(keys)
	return nil
}
