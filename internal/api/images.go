package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mihnk/opstide/internal/inventory"
	"github.com/mihnk/opstide/internal/store"
)

// The image inventory: which container images the workloads OpsTide records run, where, since when and until when,
// built from the releases, ControllerRevisions and CronJob templates already recorded (no watch of its own). A time
// it cannot place is not filled in, and an answer about a past time says whether OpsTide was watching then.

const (
	defImageLimit = 200
	maxImageLimit = 1000
	maxImageRef   = 512
	// ImageRefEncoding says how an image reference is put in a URL path.
	ImageRefEncoding = "ref is the image reference, exactly as the Pod template names it, in unpadded base64url (RFC 4648 section 5)"
)

var nsNameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)

// imageScopes are the watches the inventory depends on.
var imageScopes = []string{"cronjobs", "controllerrevisions", "daemonsets", "deployments", "replicasets", "statefulsets"}

// ImageUse is one container of one workload running one image over a stretch of time.
type ImageUse struct {
	WorkloadID string `json:"workloadId"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Workload   string `json:"workload"`
	Container  string `json:"container"`
	Init       bool   `json:"init"`
	// Since is when the workload's revision that first gave the container this image became current; SinceBasis says
	// what that time is: created (Kubernetes' creation time of the revision), observed (when OpsTide saw it become
	// current), first_recorded (it was already in use when OpsTide first recorded it) or unknown (since is null;
	// sinceAfter, when set, is a time it became current after).
	Since      *time.Time `json:"since"`
	SinceBasis string     `json:"sinceBasis"`
	SinceAfter *time.Time `json:"sinceAfter,omitempty"`
	// Until is when a newer revision replaced it (null: still the newest revision recorded, unless untilBasis is
	// unknown); UntilBasis says how: replaced, container_removed, workload_deleted, or unknown (it ended, when is not
	// known; until, when set, is a time by which it had ended).
	Until        *time.Time `json:"until"`
	UntilBasis   string     `json:"untilBasis,omitempty"`
	Current      bool       `json:"current"` // the image of the newest revision recorded, as far as OpsTide knows
	FromRevision string     `json:"fromRevision"`
	ToRevision   string     `json:"toRevision"`
	ReleaseID    string     `json:"releaseId,omitempty"`       // the release of fromRevision (Deployments)
	LatestRelID  string     `json:"latestReleaseId,omitempty"` // the release of toRevision (Deployments)
	// Provenance is of the stretch (since and until): DERIVED, a rule applied to the revisions seen; UNKNOWN when the
	// revision's rollout failed (its Pods may never have run it), when a time is not known, or in an answer about a
	// time OpsTide was not watching. TemplateProvenance is of "the template named this image": OBSERVED, seen in a
	// live object (a ReplicaSet's, ControllerRevision's or CronJob's template).
	Provenance         string `json:"provenance"`
	TemplateProvenance string `json:"templateProvenance"`
	ReportedImageID    string `json:"reportedImageId,omitempty"` // the image ID a Pod of the revision reported
	Previous           string `json:"previous,omitempty"`
	ReplacedBy         string `json:"replacedBy,omitempty"`
	Adopted            bool   `json:"adopted"`
	// ImageTruncated: the reference may have been cut where OpsTide caps stored text; it is shown as stored.
	ImageTruncated bool `json:"imageTruncated,omitempty"`
	// Note is the notes in English; Notes are the same as codes with values, for clients that word them themselves.
	Note  string           `json:"note,omitempty"`
	Notes []inventory.Note `json:"notes"`
	// Path is the workload's page (a CronJob's is its Schedules page).
	Path string `json:"path"`
}

// ImageGroup is one image and its uses.
type ImageGroup struct {
	Image      string     `json:"image"`
	Ref        string     `json:"ref"` // the image, encoded for /api/v1/images/{ref} and /images/{ref}
	Repository string     `json:"repository"`
	Tag        string     `json:"tag,omitempty"`
	Digest     string     `json:"digest,omitempty"`
	Namespaces []string   `json:"namespaces"`
	Uses       []ImageUse `json:"uses"`
}

// ImageCoverage says whether OpsTide was watching what the answer depends on at the time asked about.
type ImageCoverage struct {
	State string   `json:"state"` // watching | gap | before_first_observation | outside_retention
	Note  string   `json:"note"`
	Scope []string `json:"scopes"`
	// CronJobImagesSince is when OpsTide began recording CronJob images; none is held from before.
	CronJobImagesSince *time.Time `json:"cronJobImagesSince"`
	RetentionDays      int        `json:"retentionDays"`
	BoundaryAt         *time.Time `json:"boundaryAt"` // revisions replaced before this are no longer held
}

// ImageList is GET /api/v1/images.
type ImageList struct {
	Schema    int           `json:"schema"`
	At        time.Time     `json:"at"`
	Now       time.Time     `json:"now"`
	Current   bool          `json:"current"` // at is now
	Namespace string        `json:"namespace,omitempty"`
	Query     string        `json:"q,omitempty"`
	Coverage  ImageCoverage `json:"coverage"`
	Images    []ImageGroup  `json:"images"`
	Total     int           `json:"total"`     // images matching, before the limit
	Truncated bool          `json:"truncated"` // more images matched than limit
	Encoding  string        `json:"refEncoding"`
	Notes     []string      `json:"notes"`
	// NoteCodes are Notes as codes with values (see the note codes in the README), for clients that word them.
	NoteCodes []inventory.Note `json:"noteCodes"`
	// Unknown lists, for an answer about a past time, the workloads whose held history starts after it while they
	// may have existed then: what they ran is not known. Never an empty answer for them.
	Unknown []UnknownWorkload `json:"unknown"`
}

// UnknownWorkload is a workload whose image at the time asked about is not known.
type UnknownWorkload struct {
	WorkloadID string    `json:"workloadId"`
	Kind       string    `json:"kind"`
	Namespace  string    `json:"namespace"`
	Workload   string    `json:"workload"`
	HeldSince  time.Time `json:"heldSince"`  // the earliest time a held revision of it became current
	Provenance string    `json:"provenance"` // UNKNOWN
	Path       string    `json:"path"`
}

// ImageTag is another image of the same repository.
type ImageTag struct {
	Image   string `json:"image"`
	Ref     string `json:"ref"`
	Current int    `json:"current"` // uses current now
	Uses    int    `json:"uses"`    // uses recorded
}

// ImageDetail is GET /api/v1/images/{ref}: one image's history across workloads.
type ImageDetail struct {
	Schema     int           `json:"schema"`
	Image      string        `json:"image"`
	Ref        string        `json:"ref"`
	Repository string        `json:"repository"`
	Tag        string        `json:"tag,omitempty"`
	Digest     string        `json:"digest,omitempty"`
	Now        time.Time     `json:"now"`
	Coverage   ImageCoverage `json:"coverage"`
	// Uses is every recorded use of the image, newest first; Current counts those still current.
	Uses      []ImageUse       `json:"uses"`
	Current   int              `json:"current"`
	Workloads int              `json:"workloads"`
	OtherTags []ImageTag       `json:"otherTags"` // other images of the same repository
	Encoding  string           `json:"refEncoding"`
	Notes     []string         `json:"notes"`
	NoteCodes []inventory.Note `json:"noteCodes"`
}

// EncodeImageRef is an image reference as it goes in a path.
func EncodeImageRef(image string) string { return base64.RawURLEncoding.EncodeToString([]byte(image)) }

func decodeImageRef(ref string) (string, error) {
	if ref == "" || len(ref) > 700 {
		return "", badRequest("ref must be an image reference in unpadded base64url")
	}
	b, err := base64.RawURLEncoding.DecodeString(ref)
	if err != nil || len(b) == 0 || len(b) > maxImageRef || !utf8.Valid(b) {
		return "", badRequest("ref must be an image reference in unpadded base64url")
	}
	for _, r := range string(b) {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", badRequest("an image reference has no spaces or control characters")
		}
	}
	return string(b), nil
}

// imageMemo is the decoded inventory of the whole record, for one state of the revisions and template rows.
type imageMemo struct {
	version string
	uses    []inventory.Use
	held    []inventory.Held
}

// snapshotContainer is the part of a stored snapshot's container the inventory reads.
type snapshotContainer struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

// Image references are capped where they are stored: 253 bytes in a release or ControllerRevision snapshot, 512 in a
// CronJob template row. One that long may have been cut.
const (
	snapshotTextCap  = 253
	templateImageCap = 512
)

// cleanImage makes a stored reference valid UTF-8 (one cut inside a character before cuts were made on character
// boundaries) and says whether it may have been cut.
func cleanImage(img string, limit int) (string, bool) {
	cut := len(img) >= limit-3
	if !utf8.ValidString(img) {
		img, cut = strings.ToValidUTF8(img, ""), true
	}
	return img, cut
}

// allImageUses decodes every recorded use, reusing the last decoding while the revisions and template rows are as
// they were. Only the containers' names and images are read from the stored snapshots.
func (s *Server) allImageUses(ctx context.Context) (*imageMemo, error) {
	ver, err := s.db.ImageVersion(ctx)
	if err != nil {
		return nil, err
	}
	s.imgMu.Lock()
	defer s.imgMu.Unlock()
	if s.img != nil && s.img.version == ver {
		return s.img, nil
	}
	revs, err := s.db.ImageRevisions(ctx, "")
	if err != nil {
		return nil, err
	}
	var in []inventory.Revision
	for _, r := range revs {
		ir := inventory.Revision{WorkloadID: r.WorkloadID, Kind: r.Kind, Namespace: r.Namespace, Workload: r.Workload, WorkloadDeletedAt: r.WorkloadDeletedAt,
			ID: r.ID, Revision: r.Revision, Number: r.Number, CreatedAt: r.CreatedAt, ObservedAt: r.ObservedAt, Adopted: r.Origin == store.OriginAdopted, State: r.State}
		if r.Kind == store.KindDeployment {
			// A rollback is a release of its own: when OpsTide saw it, or, adopted, when it found it current.
			ir.ReleaseID, ir.BecameNewestAt, ir.RollbackUnseen = r.ID, r.ObservedAt, r.Origin == store.OriginAdopted
			if r.ImageIDs != "" {
				_ = json.Unmarshal([]byte(r.ImageIDs), &ir.ReportedIDs)
			}
		} else {
			// A ControllerRevision made current again is renumbered in place (updated_at). Never renumbered after
			// OpsTide recorded it, a rollback to it happened before: it found it already current.
			ir.BecameNewestAt, ir.RenumberedInPlace = r.UpdatedAt, true
			ir.RollbackUnseen = r.UpdatedAt.Sub(r.ObservedAt) < 2*time.Second
		}
		var cs, ics []snapshotContainer
		if r.InitContainers != "" && json.Unmarshal([]byte(r.InitContainers), &ics) != nil {
			continue // a snapshot that cannot be read names no image; nothing is made up for it
		}
		if r.Containers != "" && json.Unmarshal([]byte(r.Containers), &cs) != nil {
			continue
		}
		for _, c := range ics {
			ir.Containers = append(ir.Containers, inventory.Container{Name: c.Name, Image: c.Image, Init: true})
		}
		for _, c := range cs {
			ir.Containers = append(ir.Containers, inventory.Container{Name: c.Name, Image: c.Image})
		}
		in = append(in, ir)
	}
	uses := inventory.FromRevisions(in)
	held := inventory.HeldOf(in)
	tis, err := s.db.TemplateImages(ctx, "")
	if err != nil {
		return nil, err
	}
	var ts []inventory.Template
	for _, t := range tis {
		ts = append(ts, inventory.Template{WorkloadID: t.WorkloadID, Kind: t.WorkloadKind, Namespace: t.Namespace, Workload: t.WorkloadName, Container: t.Container,
			Init: t.Init, Image: t.Image, Adopted: t.Origin == store.OriginAdopted, FirstSeenAt: t.FirstSeenAt, EndedAt: t.EndedAt, EndReason: t.EndReason})
	}
	uses = append(uses, inventory.FromTemplates(ts)...)
	held = append(held, inventory.HeldOfTemplates(ts)...)
	for i := range uses {
		uses[i].Image, _ = cleanImage(uses[i].Image, templateImageCap)
	}
	s.img = &imageMemo{version: ver, uses: uses, held: held}
	return s.img, nil
}

// imageUses lists every recorded use, in a namespace when one is given, with the workloads' held history.
func (s *Server) imageUses(ctx context.Context, namespace string) ([]inventory.Use, []inventory.Held, error) {
	m, err := s.allImageUses(ctx)
	if err != nil {
		return nil, nil, err
	}
	if namespace == "" {
		return m.uses, m.held, nil
	}
	var uses []inventory.Use
	for _, u := range m.uses {
		if u.Namespace == namespace {
			uses = append(uses, u)
		}
	}
	var held []inventory.Held
	for _, h := range m.held {
		if h.Namespace == namespace {
			held = append(held, h)
		}
	}
	return uses, held, nil
}

// watchedNamespace says whether OpsTide is set to watch the namespace now (every namespace when none is listed).
func (s *Server) watchedNamespace(ns string) bool {
	return len(s.cfg.Namespaces) == 0 || has(s.cfg.Namespaces, ns)
}

func (s *Server) useView(u inventory.Use) ImageUse {
	v := ImageUse{WorkloadID: u.WorkloadID, Kind: u.Kind, Namespace: u.Namespace, Workload: u.Workload, Container: u.Container, Init: u.Init,
		Since: tp(u.Since), SinceBasis: u.SinceBasis, SinceAfter: tp(u.SinceAfter), Until: tp(u.Until), UntilBasis: u.UntilBasis, Current: u.Current(),
		FromRevision: u.FromRevision, ToRevision: u.ToRevision, ReleaseID: u.ReleaseID, LatestRelID: u.LatestReleaseID, Provenance: u.Provenance,
		TemplateProvenance: u.TemplateProvenance, ReportedImageID: u.ReportedImageID, Previous: u.Previous, ReplacedBy: u.ReplacedBy, Adopted: u.Adopted,
		Notes: append([]inventory.Note{}, u.Notes...), Path: "/workloads/" + pathValue(u.WorkloadID)}
	if u.Kind == "CronJob" {
		v.Path = "/operations/" + pathValue(u.WorkloadID)
	}
	if v.SinceBasis == inventory.SinceUnknown || v.UntilBasis == inventory.UntilUnknown {
		v.Provenance = ProvUnknown
	}
	// Still open in a namespace OpsTide no longer watches: whether it is still current is not known.
	if v.Current && !s.watchedNamespace(u.Namespace) {
		v.Current, v.UntilBasis, v.Provenance = false, inventory.UntilUnknown, ProvUnknown
		v.Notes = append(v.Notes, inventory.Note{Code: noteNotWatchedNow})
	}
	limit := snapshotTextCap
	if u.Kind == "CronJob" {
		limit = templateImageCap
	}
	_, v.ImageTruncated = cleanImage(u.Image, limit)
	if v.ImageTruncated {
		v.Notes = append(v.Notes, inventory.Note{Code: noteImageTruncated, Params: map[string]string{"bytes": itoa(limit)}})
	}
	texts := make([]string, 0, len(v.Notes))
	for _, n := range v.Notes {
		texts = append(texts, noteText(n))
	}
	v.Note = strings.Join(texts, " ")
	return v
}

// Note codes the API adds to the inventory's own (inventory.Note*).
const (
	noteNotWatchedNow  = "namespace_not_watched"    // a use still open in a namespace OpsTide no longer watches
	noteImageTruncated = "image_truncated"          // params bytes: the reference may have been cut there
	noteCoverage       = "coverage"                 // params state: the coverage note for the time asked about
	noteCronJobsSince  = "cronjob_images_since"     // params since: no CronJob image held from before
	noteNoRecord       = "no_record"                // params days: no record of this image
	noteRevisionTimes  = "revision_times"           // times are revisions', not image pulls
	noteUncertainAt    = "uncertain_at"             // a use that may or may not have been current at the time asked about
	noteUnknownHistory = "workloads_history_starts" // params n: workloads whose held history starts after the time asked about
)

func noteText(n inventory.Note) string {
	switch n.Code {
	case noteNotWatchedNow:
		return "OpsTide no longer watches this namespace: whether this is still current is not known."
	case noteImageTruncated:
		return "The reference may have been cut at " + n.Params["bytes"] + " bytes where OpsTide stores it; it is shown as stored."
	case noteCronJobsSince:
		return "OpsTide records CronJob images since " + n.Params["since"] + "; none is held from before, so CronJobs are missing here."
	case noteNoRecord:
		return "OpsTide holds no record of this image in a workload it watches (within the " + n.Params["days"] + " days it keeps). That says what was recorded, not that the image never ran."
	case noteRevisionTimes:
		return "Times are those of the workloads' revisions: when a revision became current, not when each Pod pulled the image."
	case noteUncertainAt:
		return "Some uses may or may not have been current at this time (a time not known); they are marked UNKNOWN."
	case noteUnknownHistory:
		return n.Params["n"] + " workloads have no revision held from this time: what they ran then is not known (see unknown)."
	}
	return n.Text()
}

// sinceKey orders a use by its since, a not-known since by the time it is known to be after.
func sinceKey(u ImageUse) time.Time {
	if u.Since != nil {
		return *u.Since
	}
	if u.SinceAfter != nil {
		return *u.SinceAfter
	}
	return time.Time{}
}

func sortUses(us []ImageUse) {
	sort.SliceStable(us, func(i, j int) bool {
		a, b := us[i], us[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.Workload != b.Workload {
			return a.Workload < b.Workload
		}
		if a.Init != b.Init {
			return a.Init
		}
		if a.Container != b.Container {
			return a.Container < b.Container
		}
		return sinceKey(a).Before(sinceKey(b))
	})
}

// imageCoverage says what the answer about at can be trusted for.
func (s *Server) imageCoverage(ctx context.Context, at time.Time, namespace string) (ImageCoverage, error) {
	now := s.now().UTC()
	c := ImageCoverage{Scope: imageScopes, RetentionDays: s.cfg.Retention.Days}
	since, err := s.db.TemplateImagesSince(ctx)
	if err != nil {
		return c, err
	}
	c.CronJobImagesSince = tp(since)
	if s.cfg.Retention.Days > 0 {
		b := now.AddDate(0, 0, -s.cfg.Retention.Days)
		c.BoundaryAt = &b
		if at.Before(b) {
			c.State = CovOutsideRetn
			c.Note = "This is older than the " + itoa(s.cfg.Retention.Days) + " days of history OpsTide keeps. Revisions replaced since then are no longer held, so what ran then is not known; nothing is shown as if it were."
			return c, nil
		}
	}
	rc, err := s.coverageAt(ctx, imageScopes, namespace, at)
	if err != nil {
		return c, err
	}
	c.State, c.Note = rc.State, rc.Note
	switch rc.State {
	case CovWatching:
		c.Note = "OpsTide was watching the workloads and their revisions at this time."
	case CovBeforeFirst:
		c.Note = "This is before OpsTide first watched these workloads. A revision it found already there is shown with Kubernetes' creation time, but whether another was current then is not known."
	case CovGap:
		c.Note = "OpsTide was not watching all of these workloads at this time: a revision that came and went then may not have been recorded."
	}
	return c, nil
}

func (s *Server) imagesParams(r *http.Request, allowed ...string) error {
	known := map[string]bool{"namespace": true}
	for _, a := range allowed {
		known[a] = true
	}
	for k := range r.URL.Query() {
		if !known[k] {
			return badRequest("unknown parameter " + fmt.Sprintf("%.40q", k))
		}
	}
	ns := r.URL.Query().Get("namespace")
	if ns != "" && !nsNameRe.MatchString(ns) {
		return badRequest("namespace is not a namespace name")
	}
	return nil
}

func (s *Server) images(w http.ResponseWriter, r *http.Request) {
	v, err := s.buildImages(r)
	reply(w, r, s.log, v, err)
}

func (s *Server) buildImages(r *http.Request) (ImageList, error) {
	if err := s.imagesParams(r, "at", "q", "limit"); err != nil {
		return ImageList{}, err
	}
	q := r.URL.Query()
	now := s.now().UTC().Truncate(time.Millisecond)
	at, err := timeParam(r, "at")
	if err != nil {
		return ImageList{}, err
	}
	switch {
	case at.IsZero():
		at = now
	case at.After(now.Add(time.Minute)):
		return ImageList{}, badRequest("at is in the future")
	}
	at = at.UTC()
	text := strings.TrimSpace(q.Get("q"))
	if len(text) > 200 {
		return ImageList{}, badRequest("q is too long")
	}
	limit, err := intParam(r, "limit", defImageLimit, maxImageLimit)
	if err != nil {
		return ImageList{}, err
	}
	ns := q.Get("namespace")
	out := ImageList{Schema: Schema, At: at, Now: now, Current: !now.After(at.Add(time.Minute)), Namespace: ns, Query: text, Images: []ImageGroup{}, Encoding: ImageRefEncoding,
		Notes: []string{}, NoteCodes: []inventory.Note{}, Unknown: []UnknownWorkload{}}
	if out.Coverage, err = s.imageCoverage(r.Context(), at, ns); err != nil {
		return out, err
	}
	uses, held, err := s.imageUses(r.Context(), ns)
	if err != nil {
		return out, err
	}
	needle := strings.ToLower(text)
	matches := func(image, ns, workload string) bool {
		return needle == "" || strings.Contains(strings.ToLower(image), needle) || strings.Contains(strings.ToLower(ns+"/"+workload), needle)
	}
	groups := map[string]*ImageGroup{}
	uncertain := 0
	for _, u := range uses {
		in, certain := u.At(at)
		if !in || !matches(u.Image, u.Namespace, u.Workload) {
			continue
		}
		v := s.useView(u)
		if !certain {
			v.Provenance = ProvUnknown
			v.Notes = append(v.Notes, inventory.Note{Code: noteUncertainAt})
			uncertain++
		}
		if out.Coverage.State != CovWatching {
			v.Provenance = ProvUnknown // what OpsTide holds from before then; whether it was still current is not confirmed
		}
		g := groups[u.Image]
		if g == nil {
			ref := inventory.ParseRef(u.Image)
			g = &ImageGroup{Image: u.Image, Ref: EncodeImageRef(u.Image), Repository: ref.Repository, Tag: ref.Tag, Digest: ref.Digest, Namespaces: []string{}, Uses: []ImageUse{}}
			groups[u.Image] = g
		}
		g.Uses = append(g.Uses, v)
		if !has(g.Namespaces, u.Namespace) {
			g.Namespaces = append(g.Namespaces, u.Namespace)
		}
	}
	for _, g := range groups {
		sort.Strings(g.Namespaces)
		sortUses(g.Uses)
		out.Images = append(out.Images, *g)
	}
	sort.Slice(out.Images, func(i, j int) bool { return out.Images[i].Image < out.Images[j].Image })
	out.Total = len(out.Images)
	if len(out.Images) > limit {
		out.Images, out.Truncated = out.Images[:limit], true
	}
	// A workload whose held history starts after the time asked about, and that may have existed then: what it ran is
	// not known. It is listed as such, never left out as if it ran nothing.
	if !out.Current {
		for _, h := range held {
			if !at.Before(h.Since) || h.NothingBefore || (!h.DeletedAt.IsZero() && !at.Before(h.DeletedAt)) || !matches("", h.Namespace, h.Workload) {
				continue
			}
			p := "/workloads/" + pathValue(h.WorkloadID)
			if h.Kind == "CronJob" {
				p = "/operations/" + pathValue(h.WorkloadID)
			}
			out.Unknown = append(out.Unknown, UnknownWorkload{WorkloadID: h.WorkloadID, Kind: h.Kind, Namespace: h.Namespace, Workload: h.Workload, HeldSince: h.Since, Provenance: ProvUnknown, Path: p})
		}
		sort.Slice(out.Unknown, func(i, j int) bool {
			a, b := out.Unknown[i], out.Unknown[j]
			if a.Namespace != b.Namespace {
				return a.Namespace < b.Namespace
			}
			return a.Workload < b.Workload
		})
	}
	if out.Coverage.State != CovWatching {
		out.NoteCodes = append(out.NoteCodes, inventory.Note{Code: noteCoverage, Params: map[string]string{"state": out.Coverage.State}})
	}
	if since := out.Coverage.CronJobImagesSince; since != nil && at.Before(*since) {
		out.NoteCodes = append(out.NoteCodes, inventory.Note{Code: noteCronJobsSince, Params: map[string]string{"since": since.Format(time.RFC3339)}})
	}
	if uncertain > 0 {
		out.NoteCodes = append(out.NoteCodes, inventory.Note{Code: noteUncertainAt})
	}
	if n := len(out.Unknown); n > 0 {
		out.NoteCodes = append(out.NoteCodes, inventory.Note{Code: noteUnknownHistory, Params: map[string]string{"n": itoa(n)}})
	}
	for _, n := range out.NoteCodes {
		if n.Code == noteCoverage {
			out.Notes = append(out.Notes, out.Coverage.Note)
			continue
		}
		out.Notes = append(out.Notes, noteText(n))
	}
	return out, nil
}

func (s *Server) imageOne(w http.ResponseWriter, r *http.Request) {
	v, err := s.buildImage(r)
	reply(w, r, s.log, v, err)
}

func (s *Server) buildImage(r *http.Request) (ImageDetail, error) {
	if err := s.imagesParams(r); err != nil {
		return ImageDetail{}, err
	}
	image, err := decodeImageRef(r.PathValue("ref"))
	if err != nil {
		return ImageDetail{}, err
	}
	now := s.now().UTC().Truncate(time.Millisecond)
	ref := inventory.ParseRef(image)
	out := ImageDetail{Schema: Schema, Image: image, Ref: EncodeImageRef(image), Repository: ref.Repository, Tag: ref.Tag, Digest: ref.Digest, Now: now,
		Uses: []ImageUse{}, OtherTags: []ImageTag{}, Encoding: ImageRefEncoding, Notes: []string{}, NoteCodes: []inventory.Note{}}
	ns := r.URL.Query().Get("namespace")
	if out.Coverage, err = s.imageCoverage(r.Context(), now, ns); err != nil {
		return out, err
	}
	uses, _, err := s.imageUses(r.Context(), ns)
	if err != nil {
		return out, err
	}
	workloads := map[string]bool{}
	tags := map[string]*ImageTag{}
	for _, u := range uses {
		if u.Image == image {
			v := s.useView(u)
			out.Uses = append(out.Uses, v)
			workloads[u.WorkloadID] = true
			if v.Current {
				out.Current++
			}
			continue
		}
		if inventory.ParseRef(u.Image).Repository != ref.Repository {
			continue
		}
		tg := tags[u.Image]
		if tg == nil {
			tg = &ImageTag{Image: u.Image, Ref: EncodeImageRef(u.Image)}
			tags[u.Image] = tg
		}
		tg.Uses++
		if u.Current() && s.watchedNamespace(u.Namespace) {
			tg.Current++
		}
	}
	out.Workloads = len(workloads)
	sort.SliceStable(out.Uses, func(i, j int) bool {
		a, b := sinceKey(out.Uses[i]), sinceKey(out.Uses[j])
		if !a.Equal(b) {
			return a.After(b)
		}
		if out.Uses[i].Workload != out.Uses[j].Workload {
			return out.Uses[i].Workload < out.Uses[j].Workload
		}
		return out.Uses[i].Container < out.Uses[j].Container
	})
	for _, tg := range tags {
		out.OtherTags = append(out.OtherTags, *tg)
	}
	sort.Slice(out.OtherTags, func(i, j int) bool { return out.OtherTags[i].Image < out.OtherTags[j].Image })
	if len(out.Uses) == 0 {
		out.NoteCodes = append(out.NoteCodes, inventory.Note{Code: noteNoRecord, Params: map[string]string{"days": itoa(s.cfg.Retention.Days)}})
	}
	if out.Coverage.State != CovWatching {
		out.NoteCodes = append(out.NoteCodes, inventory.Note{Code: noteCoverage, Params: map[string]string{"state": out.Coverage.State}})
	}
	out.NoteCodes = append(out.NoteCodes, inventory.Note{Code: noteRevisionTimes})
	for _, n := range out.NoteCodes {
		if n.Code == noteCoverage {
			out.Notes = append(out.Notes, out.Coverage.Note)
			continue
		}
		out.Notes = append(out.Notes, noteText(n))
	}
	return out, nil
}
