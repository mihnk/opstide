package api

import (
	"time"

	"github.com/mihnk/opstide/internal/maintenance"
	"github.com/mihnk/opstide/internal/store"
)

// Maintenance windows are declared in configuration (Helm values), never in the UI or the API. A record whose time
// falls inside a window that covers its namespace gets the window as context: a DERIVED statement, "inside maintenance
// window <name>", added to the answer. Nothing is hidden, removed or rewritten: the stored record stays as it was, it
// is still listed and counted, and only the order of the Overview's exceptions and Schedules' "Needs a look" puts it
// after the others.

// maintenanceStatement is the words every mark carries.
const maintenanceStatement = "inside maintenance window "

// MaintenanceMark is the window a record falls in.
type MaintenanceMark struct {
	Name       string    `json:"name"`
	Note       string    `json:"note,omitempty"`
	From       time.Time `json:"from"` // the occurrence the record falls in, [from, to)
	To         time.Time `json:"to"`
	Provenance string    `json:"provenance"` // DERIVED: a rule applied to the configured windows
	Statement  string    `json:"statement"`  // "inside maintenance window <name>"
}

// MaintenanceSpan is one occurrence of a window, for timelines.
type MaintenanceSpan struct {
	Name       string    `json:"name"`
	Note       string    `json:"note,omitempty"`
	From       time.Time `json:"from"`
	To         time.Time `json:"to"`
	Namespaces []string  `json:"namespaces"` // empty: the whole cluster
	Recurring  bool      `json:"recurring"`
}

func markOf(o maintenance.Occurrence) *MaintenanceMark {
	return &MaintenanceMark{Name: o.Name, Note: o.Note, From: o.From, To: o.To, Provenance: ProvDerived, Statement: maintenanceStatement + o.Name}
}

func spanOf(o maintenance.Occurrence) MaintenanceSpan {
	ns := o.Namespaces
	if ns == nil {
		ns = []string{}
	}
	return MaintenanceSpan{Name: o.Name, Note: o.Note, From: o.From, To: o.To, Namespaces: ns, Recurring: o.Recurring}
}

// maintenanceAt is the mark for a time in a namespace, or nil.
func (s *Server) maintenanceAt(t time.Time, ns string) *MaintenanceMark {
	if o, ok := s.cfg.Maintenance.At(t, ns); ok {
		return markOf(o)
	}
	return nil
}

// maintenanceSpans lists the occurrences overlapping [from, to), clipped to it. A namespace narrows to the windows
// that cover it; "" lists every window. truncated says the expansion was cut (see maintenance.MaxOccurrences): the list
// is then incomplete, and the answer must say so.
func (s *Server) maintenanceSpans(from, to time.Time, namespace string) ([]MaintenanceSpan, bool) {
	return s.spansWhere(from, to, func(o maintenance.Occurrence) bool {
		return namespace == "" || len(o.Namespaces) == 0 || has(o.Namespaces, namespace)
	})
}

// recordSpans lists the occurrences overlapping [from, to) that cover a record in namespace ns ("" is a cluster-scoped
// record, which only a cluster-wide window covers).
func (s *Server) recordSpans(from, to time.Time, ns string) ([]MaintenanceSpan, bool) {
	return s.spansWhere(from, to, func(o maintenance.Occurrence) bool { return len(o.Namespaces) == 0 || has(o.Namespaces, ns) })
}

func (s *Server) spansWhere(from, to time.Time, keep func(maintenance.Occurrence) bool) ([]MaintenanceSpan, bool) {
	out := []MaintenanceSpan{}
	occ, truncated := s.cfg.Maintenance.Between(from, to)
	for _, o := range occ {
		if !keep(o) {
			continue
		}
		sp := spanOf(o)
		if sp.From.Before(from) {
			sp.From = from
		}
		if sp.To.After(to) {
			sp.To = to
		}
		out = append(out, sp)
	}
	return out, truncated
}

// maintenanceTruncatedNote is what an answer whose maintenance windows were not all expanded says.
const maintenanceTruncatedNote = "More maintenance window occurrences fall in this range than OpsTide expands for one question; only the first are listed. Records are still marked correctly. Narrow the range to see every occurrence."

// maintenanceNamespace is the namespace a record is matched to windows by: "" (cluster-scoped, which only a
// cluster-wide window covers) for a record about a cluster-scoped object, such as a Kubernetes Event about a Node,
// which Kubernetes stores in default.
func maintenanceNamespace(rec ExplorerRecord) string {
	if store.IsClusterScoped(rec.Resource.Kind) {
		return ""
	}
	return rec.Resource.Namespace
}

// markRecords adds the maintenance window each record falls in, by its record time and namespace.
func (s *Server) markRecords(recs []ExplorerRecord) {
	if s.cfg.Maintenance.Len() == 0 || len(recs) == 0 {
		return
	}
	lo, hi := recs[0].At, recs[0].At
	for _, r := range recs {
		if r.At.Before(lo) {
			lo = r.At
		}
		if r.At.After(hi) {
			hi = r.At
		}
	}
	ix := s.cfg.Maintenance.Index(lo, hi.Add(time.Millisecond))
	for i := range recs {
		if o, ok := ix.At(recs[i].At, maintenanceNamespace(recs[i])); ok {
			recs[i].Maintenance = markOf(o)
		}
	}
}

// MaintenanceWindowView is one configured window, for System.
type MaintenanceWindowView struct {
	Name       string            `json:"name"`
	Kind       string            `json:"kind"` // recurring | one-off
	Cron       string            `json:"cron,omitempty"`
	Duration   string            `json:"duration,omitempty"`
	TimeZone   string            `json:"timeZone,omitempty"`
	Start      *time.Time        `json:"start,omitempty"`
	End        *time.Time        `json:"end,omitempty"`
	Namespaces []string          `json:"namespaces"` // empty: the whole cluster
	Note       string            `json:"note,omitempty"`
	Active     bool              `json:"active"`
	Next       []MaintenanceSpan `json:"next"` // the open occurrence, if any, then the next ones (up to 3 in all)
	// UnwatchedNamespaces are namespaces the window names that OpsTide is not set to watch: nothing there is recorded,
	// so the window marks nothing in them (likely a typo or a namespace left out of the watch).
	UnwatchedNamespaces []string `json:"unwatchedNamespaces,omitempty"`
}

// MaintenanceSystem is what System says about maintenance windows.
type MaintenanceSystem struct {
	Windows []MaintenanceWindowView `json:"windows"`
	Note    string                  `json:"note"`
}

func (s *Server) maintenanceSystem(now time.Time) MaintenanceSystem {
	out := MaintenanceSystem{Windows: []MaintenanceWindowView{},
		Note: "Declared in configuration (Helm values maintenance.windows). A record inside a window is marked, never hidden: it is still listed and counted. " +
			"A window that names no namespace (namespaces: [] or none) covers the whole cluster, cluster-scoped records included."}
	for _, w := range s.cfg.Maintenance.Windows() {
		v := MaintenanceWindowView{Name: w.Name, Kind: "one-off", Namespaces: w.Namespaces, Note: w.Note, Next: []MaintenanceSpan{}}
		if v.Namespaces == nil {
			v.Namespaces = []string{}
		}
		if len(s.cfg.Namespaces) > 0 { // empty: every namespace is watched
			for _, ns := range v.Namespaces {
				if !has(s.cfg.Namespaces, ns) {
					v.UnwatchedNamespaces = append(v.UnwatchedNamespaces, ns)
				}
			}
		}
		if w.Recurring() {
			v.Kind, v.Cron, v.Duration, v.TimeZone = "recurring", w.Cron, w.Duration.String(), w.TimeZone
		} else {
			v.Start, v.End = tp(w.Start), tp(w.End)
		}
		for _, o := range w.Next(now, 3) {
			v.Next = append(v.Next, spanOf(o))
			if !now.Before(o.From) && now.Before(o.To) {
				v.Active = true
			}
		}
		out.Windows = append(out.Windows, v)
	}
	return out
}
