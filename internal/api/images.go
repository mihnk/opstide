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
	"github.com/mihnk/opstide/internal/release"
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
	// current) or first_recorded (it was already in use when OpsTide first recorded it).
	Since      time.Time `json:"since"`
	SinceBasis string    `json:"sinceBasis"`
	// Until is when a newer revision replaced it (null: still the newest revision recorded); UntilBasis says how:
	// replaced, container_removed or workload_deleted.
	Until        *time.Time `json:"until"`
	UntilBasis   string     `json:"untilBasis,omitempty"`
	FromRevision string     `json:"fromRevision"`
	ToRevision   string     `json:"toRevision"`
	ReleaseID    string     `json:"releaseId,omitempty"`       // the release of fromRevision (Deployments)
	LatestRelID  string     `json:"latestReleaseId,omitempty"` // the release of toRevision (Deployments)
	// Provenance is OBSERVED (seen in a live object: a ReplicaSet's or CronJob's template), DERIVED (inferred from a
	// ControllerRevision's template) or, in an answer about a time OpsTide was not watching, UNKNOWN.
	Provenance      string `json:"provenance"`
	ReportedImageID string `json:"reportedImageId,omitempty"` // the image ID a Pod of the revision reported
	Previous        string `json:"previous,omitempty"`
	ReplacedBy      string `json:"replacedBy,omitempty"`
	Adopted         bool   `json:"adopted"`
	Note            string `json:"note,omitempty"`
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
	Uses      []ImageUse `json:"uses"`
	Current   int        `json:"current"`
	Workloads int        `json:"workloads"`
	OtherTags []ImageTag `json:"otherTags"` // other images of the same repository
	Encoding  string     `json:"refEncoding"`
	Notes     []string   `json:"notes"`
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

// imageUses reads every recorded use, in a namespace when one is given.
func (s *Server) imageUses(ctx context.Context, namespace string) ([]inventory.Use, error) {
	revs, err := s.db.ImageRevisions(ctx, namespace)
	if err != nil {
		return nil, err
	}
	var in []inventory.Revision
	for _, r := range revs {
		sn, err := release.DecodeSnapshot(r.Snapshot)
		if err != nil {
			continue // a snapshot that cannot be read names no image; nothing is made up for it
		}
		ir := inventory.Revision{WorkloadID: r.WorkloadID, Kind: r.Kind, Namespace: r.Namespace, Workload: r.Workload, WorkloadDeletedAt: r.WorkloadDeletedAt,
			ID: r.ID, Revision: r.Revision, Number: r.Number, CreatedAt: r.CreatedAt, ObservedAt: r.ObservedAt, Adopted: r.Origin == store.OriginAdopted, State: r.State,
			Provenance: inventory.Derived, BecameNewestAt: r.UpdatedAt}
		if r.Kind == store.KindDeployment {
			ir.ReleaseID, ir.Provenance, ir.BecameNewestAt = r.ID, inventory.Observed, r.ObservedAt
			if r.ImageIDs != "" {
				_ = json.Unmarshal([]byte(r.ImageIDs), &ir.ReportedIDs)
			}
		}
		for _, c := range sn.InitContainers {
			ir.Containers = append(ir.Containers, inventory.Container{Name: c.Name, Image: c.Image, Init: true})
		}
		for _, c := range sn.Containers {
			ir.Containers = append(ir.Containers, inventory.Container{Name: c.Name, Image: c.Image})
		}
		in = append(in, ir)
	}
	uses := inventory.FromRevisions(in)
	tis, err := s.db.TemplateImages(ctx, namespace)
	if err != nil {
		return nil, err
	}
	var ts []inventory.Template
	for _, t := range tis {
		ts = append(ts, inventory.Template{WorkloadID: t.WorkloadID, Kind: t.WorkloadKind, Namespace: t.Namespace, Workload: t.WorkloadName, Container: t.Container,
			Init: t.Init, Image: t.Image, Adopted: t.Origin == store.OriginAdopted, FirstSeenAt: t.FirstSeenAt, EndedAt: t.EndedAt})
	}
	return append(uses, inventory.FromTemplates(ts)...), nil
}

func useView(u inventory.Use) ImageUse {
	v := ImageUse{WorkloadID: u.WorkloadID, Kind: u.Kind, Namespace: u.Namespace, Workload: u.Workload, Container: u.Container, Init: u.Init,
		Since: u.Since, SinceBasis: u.SinceBasis, Until: tp(u.Until), UntilBasis: u.UntilBasis, FromRevision: u.FromRevision, ToRevision: u.ToRevision,
		ReleaseID: u.ReleaseID, LatestRelID: u.LatestReleaseID, Provenance: u.Provenance, ReportedImageID: u.ReportedImageID, Previous: u.Previous,
		ReplacedBy: u.ReplacedBy, Adopted: u.Adopted, Note: u.Note, Path: "/workloads/" + pathValue(u.WorkloadID)}
	if u.Kind == "CronJob" {
		v.Path = "/operations/" + pathValue(u.WorkloadID)
	}
	return v
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
		return a.Since.Before(b.Since)
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
	out := ImageList{Schema: Schema, At: at, Now: now, Current: !now.After(at.Add(time.Minute)), Namespace: ns, Query: text, Images: []ImageGroup{}, Encoding: ImageRefEncoding, Notes: []string{}}
	if out.Coverage, err = s.imageCoverage(r.Context(), at, ns); err != nil {
		return out, err
	}
	uses, err := s.imageUses(r.Context(), ns)
	if err != nil {
		return out, err
	}
	needle := strings.ToLower(text)
	groups := map[string]*ImageGroup{}
	for _, u := range uses {
		if !u.ActiveAt(at) {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(u.Image), needle) && !strings.Contains(strings.ToLower(u.Namespace+"/"+u.Workload), needle) {
			continue
		}
		v := useView(u)
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
	if out.Coverage.State != CovWatching {
		out.Notes = append(out.Notes, out.Coverage.Note)
	}
	if since := out.Coverage.CronJobImagesSince; since != nil && at.Before(*since) {
		out.Notes = append(out.Notes, "OpsTide records CronJob images since "+since.Format(time.RFC3339)+"; none is held from before, so CronJobs are missing here.")
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
		Uses: []ImageUse{}, OtherTags: []ImageTag{}, Encoding: ImageRefEncoding, Notes: []string{}}
	ns := r.URL.Query().Get("namespace")
	if out.Coverage, err = s.imageCoverage(r.Context(), now, ns); err != nil {
		return out, err
	}
	uses, err := s.imageUses(r.Context(), ns)
	if err != nil {
		return out, err
	}
	workloads := map[string]bool{}
	tags := map[string]*ImageTag{}
	for _, u := range uses {
		if u.Image == image {
			out.Uses = append(out.Uses, useView(u))
			workloads[u.WorkloadID] = true
			if u.Until.IsZero() {
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
		if u.Until.IsZero() {
			tg.Current++
		}
	}
	out.Workloads = len(workloads)
	sort.SliceStable(out.Uses, func(i, j int) bool {
		if !out.Uses[i].Since.Equal(out.Uses[j].Since) {
			return out.Uses[i].Since.After(out.Uses[j].Since)
		}
		return out.Uses[i].Workload < out.Uses[j].Workload
	})
	for _, tg := range tags {
		out.OtherTags = append(out.OtherTags, *tg)
	}
	sort.Slice(out.OtherTags, func(i, j int) bool { return out.OtherTags[i].Image < out.OtherTags[j].Image })
	if len(out.Uses) == 0 {
		out.Notes = append(out.Notes, "OpsTide holds no record of this image in a workload it watches"+
			" (within the "+itoa(s.cfg.Retention.Days)+" days it keeps). That says what was recorded, not that the image never ran.")
	}
	out.Notes = append(out.Notes, "Times are those of the workloads' revisions: when a revision became current, not when each Pod pulled the image.")
	return out, nil
}
