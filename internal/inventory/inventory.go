// Package inventory builds the image inventory from what OpsTide already records: the containers of each Deployment
// release (the ReplicaSet's Pod template, seen live), of each StatefulSet and DaemonSet ControllerRevision (a revision's
// template), and of each CronJob's job template. It answers "which workloads run image X, since when, until when" and
// "what did each workload's then-current revision use at time T". It is plain functions over records; it never talks to
// Kubernetes or to the database, and it never guesses: a time it cannot place is not filled in.
package inventory

import (
	"slices"
	"sort"
	"strings"
	"time"
)

// Provenance classes, as the API uses them.
const (
	Observed = "OBSERVED" // seen in a live object (a ReplicaSet's, ControllerRevision's or CronJob's template, a Pod's status)
	Derived  = "DERIVED"  // inferred by a rule from what was seen (when a revision was current)
	Unknown  = "UNKNOWN"
)

// Bases of a Use's Since.
const (
	SinceCreated       = "created"        // the revision's creation time, as Kubernetes reported it
	SinceObserved      = "observed"       // when OpsTide saw the revision become the newest (a rollback)
	SinceFirstRecorded = "first_recorded" // when OpsTide first recorded it current; it was in use before that, since when is not known
	SinceUnknown       = "unknown"        // no usable time: Since is zero, SinceAfter is the latest time it is known to be after
)

// Bases of a Use's Until.
const (
	UntilReplaced         = "replaced"          // a newer revision (or template) gave the container another image
	UntilContainerRemoved = "container_removed" // a newer revision has no container of that name
	UntilWorkloadDeleted  = "workload_deleted"
	// UntilUnknown: it ended, but when is not known. Until, when set, is the time by which it had ended (when OpsTide
	// found the change); zero when not even that is known.
	UntilUnknown = "unknown"
)

// Note codes: what a client words (and translates) itself. Params carry the values.
const (
	NoteAdoptedCreated     = "adopted_created"         // created before OpsTide watched; the time is Kubernetes' creation time
	NoteRollout            = "rollout_state"           // params state: stalled | failed; its Pods may not all have run it
	NoteControllerRevision = "controller_revision"     // from the newest ControllerRevision's template; Pods can lag
	NoteCronJobTemplate    = "cronjob_template"        // a CronJob's template: its Jobs run it when the schedule fires
	NoteCronJobAdopted     = "cronjob_adopted"         // already named when OpsTide first recorded the template
	NoteRollbackFound      = "rollback_found"          // current again by a rollback OpsTide did not see; since is when it found it
	NoteSinceUnknown       = "since_unknown"           // params after: when it became current is not known
	NoteEarlierPeriod      = "earlier_period"          // the stretch before a rollback renumbered the revision
	NoteUntilUnknown       = "until_unknown"           // params by (may be empty): ended, when not known
	NoteChangedUnwatched   = "changed_while_unwatched" // a CronJob template change found after OpsTide was not watching
)

// Note is a coded remark on a use.
type Note struct {
	Code   string            `json:"code"`
	Params map[string]string `json:"params,omitempty"`
}

// Text is the note in English, for API readers that do not translate codes.
func (n Note) Text() string {
	switch n.Code {
	case NoteAdoptedCreated:
		return "Created before OpsTide watched: the time is Kubernetes' creation time of the revision."
	case NoteRollout:
		return "The rollout of this revision was recorded as " + n.Params["state"] + "; its Pods may not all have run it."
	case NoteControllerRevision:
		return "From the newest ControllerRevision's template: Pods held back by a partition, OnDelete or a slow rollout can still run an earlier revision."
	case NoteCronJobTemplate:
		return "A CronJob's template: its Jobs run this image when the schedule fires."
	case NoteCronJobAdopted:
		return "The CronJob already named this image when OpsTide first recorded its template; since when is not known."
	case NoteRollbackFound:
		return "Made current again by a rollback OpsTide did not see: since is when it found it current, not when the rollback happened."
	case NoteSinceUnknown:
		if a := n.Params["after"]; a != "" {
			return "When this became current is not known (some time after " + a + ")."
		}
		return "When this became current is not known."
	case NoteEarlierPeriod:
		return "Before a rollback made this revision current again, it was current from its creation until the next revision: the earlier stretch."
	case NoteUntilUnknown:
		if b := n.Params["by"]; b != "" {
			return "It stopped being current by " + b + "; exactly when is not known."
		}
		return "It stopped being current; when is not known."
	case NoteChangedUnwatched:
		return "The template was found changed after OpsTide was not watching: when the change happened is not known."
	}
	return n.Code
}

// Container is one container of a template.
type Container struct {
	Name, Image string
	Init        bool
}

// Revision is one revision of a workload with the containers of its template.
type Revision struct {
	WorkloadID, Kind, Namespace, Workload string
	WorkloadDeletedAt                     time.Time
	ID                                    string // release id or workload revision id
	ReleaseID                             string // for a Deployment
	Revision                              string
	Number                                int64
	CreatedAt                             time.Time // Kubernetes' creation time of the ReplicaSet or ControllerRevision
	// BecameNewestAt is when OpsTide saw an existing revision become the newest again (a rollback renumbers it): used
	// when CreatedAt is older than the revision before it.
	BecameNewestAt time.Time
	// RollbackUnseen says that, if this revision was made current again by a rollback, OpsTide did not see it happen:
	// it found it already current (BecameNewestAt is then when it found it).
	RollbackUnseen bool
	// RenumberedInPlace says a rollback renumbers this very record (a ControllerRevision), so its earlier stretch as
	// the current revision is not a record of its own.
	RenumberedInPlace bool
	ObservedAt        time.Time
	Adopted           bool // it existed before OpsTide watched
	State             string
	Containers        []Container
	ReportedIDs       map[string]string // container -> image ID a Pod reported
}

// Template is a CronJob template image row as recorded.
type Template struct {
	WorkloadID, Kind, Namespace, Workload string
	Container                             string
	Init                                  bool
	Image                                 string
	Adopted                               bool
	FirstSeenAt, EndedAt                  time.Time
	EndReason                             string // replaced | container_removed | workload_deleted | unknown | "" (not recorded)
}

// Use is one container of one workload running one image over a stretch of time: from the first revision that gave it
// the image to the first that did not.
type Use struct {
	Image                                 string
	WorkloadID, Kind, Namespace, Workload string
	Container                             string
	Init                                  bool
	Since                                 time.Time // zero when SinceBasis is unknown
	SinceBasis                            string
	SinceAfter                            time.Time // with SinceBasis unknown: it became current after this (zero: no bound)
	Until                                 time.Time // zero: still the image of the newest revision recorded (unless UntilBasis is unknown)
	UntilBasis                            string
	FromRevision, ToRevision              string
	ReleaseID                             string // the release of FromRevision (Deployments)
	LatestReleaseID                       string // the release of ToRevision (Deployments)
	// Provenance is of the stretch (since, until): DERIVED, a rule applied to what was seen; UNKNOWN when the
	// revision's rollout failed, so whether its Pods ran it is not known. TemplateProvenance is of "the template named
	// this image": OBSERVED, seen in a live object.
	Provenance         string
	TemplateProvenance string
	ReportedImageID    string // the image ID a Pod of the revision reported, if any
	Previous           string // the container's image before, when recorded
	ReplacedBy         string // and after
	Adopted            bool   // the first revision existed before OpsTide watched
	Notes              []Note
}

// Current says the use is the image of the newest revision recorded, as far as OpsTide knows.
func (u Use) Current() bool { return u.Until.IsZero() && u.UntilBasis == "" }

// At says whether the use may have been current at t and, if so, whether that is certain. A use whose since or until
// is not known is uncertain over the stretch it may cover.
func (u Use) At(t time.Time) (in, certain bool) {
	start, sure := u.Since, true
	if u.SinceBasis == SinceUnknown {
		start, sure = u.SinceAfter, false
	}
	if t.Before(start) {
		return false, false
	}
	if u.UntilBasis == UntilUnknown {
		if u.Until.IsZero() || t.Before(u.Until) {
			return true, false
		}
		return false, false
	}
	if !u.Until.IsZero() && !t.Before(u.Until) {
		return false, false
	}
	return true, sure
}

// ActiveAt says whether the use was (or may have been) current at t.
func (u Use) ActiveAt(t time.Time) bool {
	in, _ := u.At(t)
	return in
}

func (u *Use) note(code string, params map[string]string) {
	u.Notes = append(u.Notes, Note{Code: code, Params: params})
}

type ckey struct {
	init bool
	name string
}

// FromRevisions turns each workload's revisions into uses. A revision is current from its creation (or, when made
// current again by a rollback, from when OpsTide saw it become the newest) until the next revision's; the last until the
// workload was deleted, or still.
func FromRevisions(revs []Revision) []Use {
	byWL := map[string][]Revision{}
	var order []string
	for _, r := range revs {
		if _, ok := byWL[r.WorkloadID]; !ok {
			order = append(order, r.WorkloadID)
		}
		byWL[r.WorkloadID] = append(byWL[r.WorkloadID], r)
	}
	var out []Use
	for _, id := range order {
		out = append(out, workloadUses(byWL[id])...)
	}
	return out
}

// activation is a revision becoming current: at since (zero when not known, then after sinceAfter).
type activation struct {
	rev        int
	since      time.Time
	basis      string
	sinceAfter time.Time
	unseen     bool // a rollback OpsTide did not see: the previous use ended by since, when exactly is not known
	earlier    bool // the stretch of a renumbered revision before the rollback that renumbered it
}

// Held is what OpsTide holds of one workload's revisions: from when, and whether nothing ran before that.
type Held struct {
	WorkloadID, Kind, Namespace, Workload string
	Since                                 time.Time // the earliest known time a held revision became current
	// NothingBefore says the workload had no revision before Since: its first revision (number 1) was held and its
	// time is Kubernetes' creation time. Otherwise what it ran before Since is not known.
	NothingBefore bool
	DeletedAt     time.Time
}

// HeldOf says, per workload, from when its revisions are held.
func HeldOf(revs []Revision) []Held {
	byWL := map[string]*Held{}
	var order []string
	for _, r := range revs {
		h := byWL[r.WorkloadID]
		if h == nil {
			h = &Held{WorkloadID: r.WorkloadID, Kind: r.Kind, Namespace: r.Namespace, Workload: r.Workload, DeletedAt: r.WorkloadDeletedAt}
			byWL[r.WorkloadID] = h
			order = append(order, r.WorkloadID)
		}
		t := r.CreatedAt
		if t.IsZero() {
			t = r.ObservedAt
		}
		if h.Since.IsZero() || t.Before(h.Since) {
			h.Since, h.NothingBefore = t, r.Number <= 1 && !r.CreatedAt.IsZero()
		}
	}
	out := make([]Held, 0, len(order))
	for _, id := range order {
		out = append(out, *byWL[id])
	}
	return out
}

// HeldOfTemplates is HeldOf for CronJob template rows: nothing ran before the earliest row unless it was adopted (the
// template already named an image when OpsTide first recorded it).
func HeldOfTemplates(ts []Template) []Held {
	byWL := map[string]*Held{}
	var order []string
	for _, t := range ts {
		h := byWL[t.WorkloadID]
		if h == nil {
			h = &Held{WorkloadID: t.WorkloadID, Kind: t.Kind, Namespace: t.Namespace, Workload: t.Workload}
			byWL[t.WorkloadID] = h
			order = append(order, t.WorkloadID)
		}
		if h.Since.IsZero() || t.FirstSeenAt.Before(h.Since) {
			h.Since, h.NothingBefore = t.FirstSeenAt, !t.Adopted
		}
		if t.EndReason == "workload_deleted" && (h.DeletedAt.IsZero() || t.EndedAt.After(h.DeletedAt)) {
			h.DeletedAt = t.EndedAt
		}
	}
	out := make([]Held, 0, len(order))
	for _, id := range order {
		out = append(out, *byWL[id])
	}
	return out
}

func workloadUses(rs []Revision) []Use {
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].Number != rs[j].Number {
			return rs[i].Number < rs[j].Number
		}
		return rs[i].ID < rs[j].ID
	})
	// When each revision became current, in number order. A revision older than the one before it was made current
	// again by a rollback: from when OpsTide saw that, or found it (unseen); with no usable time, not known.
	acts := make([]activation, 0, len(rs))
	var lastKnown time.Time
	for i, r := range rs {
		a := activation{rev: i, since: r.CreatedAt, basis: SinceCreated}
		if a.since.IsZero() {
			a.since, a.basis = r.ObservedAt, SinceFirstRecorded
		}
		if i > 0 && !lastKnown.IsZero() && !a.since.After(lastKnown) {
			a.since, a.basis = r.BecameNewestAt, SinceObserved
			if r.RollbackUnseen {
				a.basis, a.unseen = SinceFirstRecorded, true
			}
			if a.since.IsZero() || !a.since.After(lastKnown) {
				a.since, a.basis, a.sinceAfter, a.unseen = time.Time{}, SinceUnknown, lastKnown, false
			}
		}
		if !a.since.IsZero() {
			lastKnown = a.since
		}
		acts = append(acts, a)
	}
	// A rollback that renumbered a revision in place erased its earlier stretch: it was current from its creation until
	// the next revision that became current after that. That stretch is put back, before the first activation later
	// than its creation.
	for i := 0; i < len(acts); i++ {
		a := acts[i]
		r := rs[a.rev]
		if a.earlier || !r.RenumberedInPlace || r.CreatedAt.IsZero() || a.basis == SinceCreated || a.basis == SinceFirstRecorded && !a.unseen {
			continue
		}
		at := -1
		for j := 0; j < i; j++ {
			if !acts[j].since.IsZero() && acts[j].since.After(r.CreatedAt) {
				at = j
				break
			}
		}
		if at < 0 {
			continue // nothing held became current after its creation: its earlier stretch is not placed
		}
		e := activation{rev: a.rev, since: r.CreatedAt, basis: SinceCreated, earlier: true}
		acts = append(acts[:at], append([]activation{e}, acts[at:]...)...)
		i++
	}
	// The latest known time after each activation, for the bound of an unknown end.
	nextKnown := make([]time.Time, len(acts))
	var nk time.Time
	for i := len(acts) - 1; i >= 0; i-- {
		nextKnown[i] = nk
		if !acts[i].since.IsZero() {
			nk = acts[i].since
		}
	}

	var out []Use
	open := map[ckey]*Use{}
	var keys []ckey
	closeUse := func(k ckey, a activation, i int, why, next string) {
		u := open[k]
		u.Until, u.UntilBasis, u.ReplacedBy = a.since, why, next
		switch {
		case a.since.IsZero(): // the next became current at a time not known: this ended by the next known time
			u.Until, u.UntilBasis = nextKnown[i], UntilUnknown
		case a.unseen: // a rollback OpsTide did not see: this ended by when it found it
			u.UntilBasis = UntilUnknown
		}
		if u.UntilBasis == UntilUnknown {
			p := map[string]string{}
			if !u.Until.IsZero() {
				p["by"] = u.Until.UTC().Format(time.RFC3339)
			}
			u.note(NoteUntilUnknown, p)
		}
		out = append(out, *u)
		delete(open, k)
	}
	for i, a := range acts {
		r := rs[a.rev]
		now := map[ckey]string{}
		for _, c := range r.Containers {
			now[ckey{c.Init, c.Name}] = c.Image
		}
		for _, k := range keys {
			u, ok := open[k]
			if !ok {
				continue
			}
			img, still := now[k]
			switch {
			case !still:
				closeUse(k, a, i, UntilContainerRemoved, "")
			case img != u.Image:
				closeUse(k, a, i, UntilReplaced, img)
			default:
				u.ToRevision, u.LatestReleaseID = r.Revision, r.ReleaseID
				if id := r.ReportedIDs[k.name]; id != "" && !k.init {
					u.ReportedImageID = id
				}
			}
		}
		for _, c := range r.Containers {
			k := ckey{c.Init, c.Name}
			if _, ok := open[k]; ok {
				continue
			}
			u := &Use{Image: c.Image, WorkloadID: r.WorkloadID, Kind: r.Kind, Namespace: r.Namespace, Workload: r.Workload, Container: c.Name, Init: c.Init,
				Since: a.since, SinceBasis: a.basis, SinceAfter: a.sinceAfter, FromRevision: r.Revision, ToRevision: r.Revision, ReleaseID: r.ReleaseID,
				LatestReleaseID: r.ReleaseID, Provenance: Derived, TemplateProvenance: Observed, Adopted: r.Adopted}
			if !c.Init {
				u.ReportedImageID = r.ReportedIDs[c.Name]
			}
			if i > 0 {
				for _, pc := range rs[acts[i-1].rev].Containers {
					if pc.Name == c.Name && pc.Init == c.Init {
						u.Previous = pc.Image
					}
				}
			}
			switch {
			case a.earlier:
				u.note(NoteEarlierPeriod, nil)
			case a.basis == SinceUnknown:
				p := map[string]string{}
				if !a.sinceAfter.IsZero() {
					p["after"] = a.sinceAfter.UTC().Format(time.RFC3339)
				}
				u.note(NoteSinceUnknown, p)
			case a.unseen:
				u.note(NoteRollbackFound, nil)
			case r.Adopted && a.basis == SinceCreated:
				u.note(NoteAdoptedCreated, nil)
			}
			switch r.State {
			case "stalled":
				u.note(NoteRollout, map[string]string{"state": r.State})
			case "failed":
				// Its Pods may never have run it: when it was the running image is not known.
				u.Provenance = Unknown
				u.note(NoteRollout, map[string]string{"state": r.State})
			}
			if r.Kind == "StatefulSet" || r.Kind == "DaemonSet" {
				u.note(NoteControllerRevision, nil)
			}
			open[k] = u
			if !slices.Contains(keys, k) {
				keys = append(keys, k)
			}
		}
	}
	last := rs[len(rs)-1]
	for _, k := range keys {
		if u, ok := open[k]; ok {
			if !last.WorkloadDeletedAt.IsZero() {
				u.Until, u.UntilBasis = last.WorkloadDeletedAt, UntilWorkloadDeleted
			}
			out = append(out, *u)
		}
	}
	return out
}

// FromTemplates turns CronJob template rows into uses; a row's end is the next row's start for the same container.
func FromTemplates(ts []Template) []Use {
	var out []Use
	for i, t := range ts {
		u := Use{Image: t.Image, WorkloadID: t.WorkloadID, Kind: t.Kind, Namespace: t.Namespace, Workload: t.Workload, Container: t.Container, Init: t.Init,
			Since: t.FirstSeenAt, SinceBasis: SinceObserved, Until: t.EndedAt, Provenance: Derived, TemplateProvenance: Observed, Adopted: t.Adopted}
		if t.Adopted {
			u.SinceBasis = SinceFirstRecorded
			u.note(NoteCronJobAdopted, nil)
		}
		if !t.EndedAt.IsZero() {
			var next string
			for _, n := range ts {
				if n.WorkloadID == t.WorkloadID && n.Container == t.Container && n.Init == t.Init && n.FirstSeenAt.Equal(t.EndedAt) {
					next = n.Image
				}
			}
			switch t.EndReason {
			case UntilReplaced, UntilContainerRemoved, UntilWorkloadDeleted:
				u.UntilBasis = t.EndReason
			case UntilUnknown:
				u.UntilBasis = UntilUnknown
				u.note(NoteChangedUnwatched, nil)
				u.note(NoteUntilUnknown, map[string]string{"by": t.EndedAt.UTC().Format(time.RFC3339)})
			default: // ended before the reason was recorded: replaced when the next row starts there, else not known
				u.UntilBasis = UntilUnknown
				if next != "" {
					u.UntilBasis = UntilReplaced
				}
			}
			if u.UntilBasis == UntilReplaced || u.UntilBasis == UntilUnknown {
				u.ReplacedBy = next
			}
		}
		for j, p := range ts {
			if j != i && p.WorkloadID == t.WorkloadID && p.Container == t.Container && p.Init == t.Init && p.EndedAt.Equal(t.FirstSeenAt) {
				u.Previous = p.Image
			}
		}
		u.note(NoteCronJobTemplate, nil)
		out = append(out, u)
	}
	return out
}

// Ref is an image reference split into its parts. Nothing is filled in: an image with no tag has an empty Tag (the
// container runtime would use "latest"; OpsTide does not say so on its behalf).
type Ref struct {
	Repository, Tag, Digest string
}

// ParseRef splits repository[:tag][@digest].
func ParseRef(image string) Ref {
	var r Ref
	name := image
	if i := strings.Index(name, "@"); i >= 0 {
		name, r.Digest = name[:i], name[i+1:]
	}
	slash := strings.LastIndex(name, "/")
	if i := strings.LastIndex(name, ":"); i > slash {
		name, r.Tag = name[:i], name[i+1:]
	}
	r.Repository = name
	return r
}
