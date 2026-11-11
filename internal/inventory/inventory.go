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
	Observed = "OBSERVED" // seen in a live object (a ReplicaSet's or CronJob's template, a Pod's status)
	Derived  = "DERIVED"  // inferred from a revision's stored template (a ControllerRevision)
	Unknown  = "UNKNOWN"
)

// Bases of a Use's Since.
const (
	SinceCreated       = "created"        // the revision's creation time, as Kubernetes reported it
	SinceObserved      = "observed"       // when OpsTide saw the revision become the newest (a rollback)
	SinceFirstRecorded = "first_recorded" // when OpsTide first recorded it; it was in use before that, since when is not known
)

// Bases of a Use's Until.
const (
	UntilReplaced         = "replaced"          // a newer revision (or template) gave the container another image
	UntilContainerRemoved = "container_removed" // a newer revision has no container of that name
	UntilWorkloadDeleted  = "workload_deleted"
)

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
	ObservedAt     time.Time
	Adopted        bool // it existed before OpsTide watched
	State          string
	Provenance     string
	Containers     []Container
	ReportedIDs    map[string]string // container -> image ID a Pod reported
}

// Template is a CronJob template image row as recorded.
type Template struct {
	WorkloadID, Kind, Namespace, Workload string
	Container                             string
	Init                                  bool
	Image                                 string
	Adopted                               bool
	FirstSeenAt, EndedAt                  time.Time
}

// Use is one container of one workload running one image over a stretch of time: from the first revision that gave it
// the image to the first that did not.
type Use struct {
	Image                                 string
	WorkloadID, Kind, Namespace, Workload string
	Container                             string
	Init                                  bool
	Since                                 time.Time
	SinceBasis                            string
	Until                                 time.Time // zero: still the image of the newest revision recorded
	UntilBasis                            string
	FromRevision, ToRevision              string
	ReleaseID                             string // the release of FromRevision (Deployments)
	LatestReleaseID                       string // the release of ToRevision (Deployments)
	Provenance                            string
	ReportedImageID                       string // the image ID a Pod of the revision reported, if any
	Previous, ReplacedBy                  string // the container's image before and after, when recorded
	Adopted                               bool   // the first revision existed before OpsTide watched
	Note                                  string
}

// ActiveAt says whether the use was current at t.
func (u Use) ActiveAt(t time.Time) bool {
	return !t.Before(u.Since) && (u.Until.IsZero() || t.Before(u.Until))
}

type ckey struct {
	init bool
	name string
}

// FromRevisions turns each workload's revisions into uses. A revision is current from its creation (or, when renumbered
// by a rollback, from when OpsTide saw it become the newest) until the next revision's; the last until the workload was
// deleted, or still.
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

func workloadUses(rs []Revision) []Use {
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].Number != rs[j].Number {
			return rs[i].Number < rs[j].Number
		}
		return rs[i].ID < rs[j].ID
	})
	since := make([]time.Time, len(rs))
	basis := make([]string, len(rs))
	for i, r := range rs {
		since[i], basis[i] = r.CreatedAt, SinceCreated
		if since[i].IsZero() {
			since[i], basis[i] = r.ObservedAt, SinceFirstRecorded
		}
		if i > 0 && !since[i].After(since[i-1]) {
			// Older than the revision before it: an existing revision made current again by a rollback.
			since[i], basis[i] = r.BecameNewestAt, SinceObserved
			if !since[i].After(since[i-1]) {
				since[i], basis[i] = since[i-1], SinceObserved
			}
		}
	}
	var out []Use
	open := map[ckey]*Use{}
	var keys []ckey
	closeUse := func(k ckey, at time.Time, why, next string) {
		u := open[k]
		u.Until, u.UntilBasis, u.ReplacedBy = at, why, next
		out = append(out, *u)
		delete(open, k)
	}
	for i, r := range rs {
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
				closeUse(k, since[i], UntilContainerRemoved, "")
			case img != u.Image:
				closeUse(k, since[i], UntilReplaced, img)
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
				Since: since[i], SinceBasis: basis[i], FromRevision: r.Revision, ToRevision: r.Revision, ReleaseID: r.ReleaseID, LatestReleaseID: r.ReleaseID,
				Provenance: r.Provenance, Adopted: r.Adopted}
			if !c.Init {
				u.ReportedImageID = r.ReportedIDs[c.Name]
			}
			if i > 0 {
				for _, pc := range rs[i-1].Containers {
					if pc.Name == c.Name && pc.Init == c.Init {
						u.Previous = pc.Image
					}
				}
			}
			if r.Adopted && basis[i] == SinceCreated {
				u.Note = "Created before OpsTide watched: the time is Kubernetes' creation time of the revision."
			}
			if r.State == "stalled" || r.State == "failed" {
				u.Note = strings.TrimSpace(u.Note + " The rollout of this revision was recorded as " + r.State + "; its Pods may not all have run it.")
			}
			if r.Kind == "StatefulSet" || r.Kind == "DaemonSet" {
				u.Note = strings.TrimSpace(u.Note + " From the newest ControllerRevision's template: Pods held back by a partition, OnDelete or a slow rollout can still run an earlier revision.")
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
			Since: t.FirstSeenAt, SinceBasis: SinceObserved, Until: t.EndedAt, Provenance: Observed, Adopted: t.Adopted}
		if t.Adopted {
			u.SinceBasis = SinceFirstRecorded
			u.Note = "The CronJob already named this image when OpsTide first recorded its template; since when is not known."
		}
		if !t.EndedAt.IsZero() {
			u.UntilBasis = UntilContainerRemoved
			for _, n := range ts {
				if n.WorkloadID == t.WorkloadID && n.Container == t.Container && n.Init == t.Init && n.FirstSeenAt.Equal(t.EndedAt) {
					u.UntilBasis, u.ReplacedBy = UntilReplaced, n.Image
				}
			}
		}
		for j, p := range ts {
			if j != i && p.WorkloadID == t.WorkloadID && p.Container == t.Container && p.Init == t.Init && p.EndedAt.Equal(t.FirstSeenAt) {
				u.Previous = p.Image
			}
		}
		u.Note = strings.TrimSpace(u.Note + " A CronJob's template: its Jobs run this image when the schedule fires.")
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
