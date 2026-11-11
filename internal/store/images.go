package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// TemplateImage is one container of a CronJob's job template and the image it named, while it named it.
type TemplateImage struct {
	WorkloadID, WorkloadKind, Namespace, WorkloadName string
	Container                                         string
	Init                                              bool
	Image                                             string
	Origin                                            string // observed | adopted
	FirstSeenAt                                       time.Time
	EndedAt                                           time.Time // zero while the template still names it
	// EndReason is why the row ended: one of the TemplateEnd* values, or "" for a row ended before it was recorded.
	EndReason string
}

// Why a template image row ended.
const (
	TemplateEndReplaced         = "replaced"
	TemplateEndContainerRemoved = "container_removed"
	TemplateEndWorkloadDeleted  = "workload_deleted"
	TemplateEndUnknown          = "unknown" // found changed (or gone) after OpsTide was not watching: when is not known
)

// TemplateContainer is a container of a template as seen now.
type TemplateContainer struct {
	Name  string
	Init  bool
	Image string
}

// SetTemplateImages records the images a CronJob's job template names now. A container whose image changed, or that
// is gone, has its row ended at at; a new image opens a row from at. origin applies to rows opened now. afterGap says
// the change was found at the first sync after OpsTide was not watching: the old rows end with reason unknown (at is
// only when it was found), and the caller opens the new ones as adopted.
func (w *Writer) SetTemplateImages(ctx context.Context, workloadID, kind, ns, name string, cs []TemplateContainer, origin string, at time.Time, afterGap bool) error {
	now := ms(at).Int64
	type key struct {
		init bool
		name string
	}
	want := map[key]string{}
	for _, c := range cs {
		want[key{c.Init, c.Name}] = c.Image
	}
	rows, err := w.tx.QueryContext(ctx, `SELECT container, init, image, first_seen_at FROM template_images WHERE workload_id = ? AND ended_at IS NULL`, workloadID)
	if err != nil {
		return err
	}
	type open struct {
		image string
		first int64
	}
	have := map[key]open{}
	for rows.Next() {
		var c, img string
		var init bool
		var first int64
		if err := rows.Scan(&c, &init, &img, &first); err != nil {
			rows.Close()
			return err
		}
		have[key{init, c}] = open{img, first}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for k, o := range have {
		img, ok := want[k]
		if ok && img == o.image {
			continue
		}
		reason := TemplateEndContainerRemoved
		switch {
		case afterGap:
			reason = TemplateEndUnknown
		case ok:
			reason = TemplateEndReplaced
		}
		if _, err := w.tx.ExecContext(ctx, `UPDATE template_images SET ended_at = ?, end_reason = ? WHERE workload_id = ? AND init = ? AND container = ? AND first_seen_at = ?`,
			now, reason, workloadID, k.init, k.name, o.first); err != nil {
			return err
		}
	}
	for k, img := range want {
		if o, ok := have[k]; ok && o.image == img {
			continue
		}
		if _, err := w.tx.ExecContext(ctx, `INSERT OR REPLACE INTO template_images (workload_id, workload_kind, namespace, workload_name, container, init, image, origin, first_seen_at, ended_at, end_reason)
VALUES (?,?,?,?,?,?,?,?,?,NULL,'')`, workloadID, kind, ns, name, k.name, k.init, img, origin, now); err != nil {
			return err
		}
	}
	_, err = w.tx.ExecContext(ctx, `UPDATE template_images SET workload_name = ? WHERE workload_id = ? AND ended_at IS NULL AND workload_name <> ?`, name, workloadID, name)
	return err
}

// HasTemplateImages says whether any image was ever recorded for the workload.
func (w *Writer) HasTemplateImages(ctx context.Context, workloadID string) (bool, error) {
	var n int
	err := w.tx.QueryRowContext(ctx, `SELECT count(*) FROM template_images WHERE workload_id = ?`, workloadID).Scan(&n)
	return n > 0, err
}

// EndTemplateImages ends the open rows of a workload that is gone.
func (w *Writer) EndTemplateImages(ctx context.Context, workloadID string, at time.Time) error {
	return w.EndTemplateImagesWhy(ctx, workloadID, at, TemplateEndWorkloadDeleted)
}

// EndTemplateImagesWhy ends the open rows of a workload with a reason (TemplateEndUnknown when it went while OpsTide
// was not watching and at is only when that was found).
func (w *Writer) EndTemplateImagesWhy(ctx context.Context, workloadID string, at time.Time, reason string) error {
	_, err := w.tx.ExecContext(ctx, `UPDATE template_images SET ended_at = ?, end_reason = ? WHERE workload_id = ? AND ended_at IS NULL`, ms(at), reason, workloadID)
	return err
}

// MarkTemplateImagesEndUnknown changes the reason of rows of a workload ended at at to unknown: it went while OpsTide
// was not watching, and at is only when that was found.
func (w *Writer) MarkTemplateImagesEndUnknown(ctx context.Context, workloadID string, at time.Time) error {
	_, err := w.tx.ExecContext(ctx, `UPDATE template_images SET end_reason = ? WHERE workload_id = ? AND ended_at = ?`, TemplateEndUnknown, workloadID, ms(at))
	return err
}

// TemplateImages lists every recorded template image, in a namespace when one is given.
func (s *Store) TemplateImages(ctx context.Context, namespace string) ([]TemplateImage, error) {
	q := `SELECT workload_id, workload_kind, namespace, workload_name, container, init, image, origin, first_seen_at, ended_at, end_reason FROM template_images`
	var args []any
	if namespace != "" {
		q += ` WHERE namespace = ?`
		args = append(args, namespace)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY workload_id, init, container, first_seen_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TemplateImage
	for rows.Next() {
		var t TemplateImage
		var first int64
		var ended sql.NullInt64
		if err := rows.Scan(&t.WorkloadID, &t.WorkloadKind, &t.Namespace, &t.WorkloadName, &t.Container, &t.Init, &t.Image, &t.Origin, &first, &ended, &t.EndReason); err != nil {
			return nil, err
		}
		t.FirstSeenAt, t.EndedAt = time.UnixMilli(first).UTC(), fromMS(ended)
		out = append(out, t)
	}
	return out, rows.Err()
}

// TemplateImagesSince is when OpsTide began recording CronJob images (the upgrade that added them); zero if never.
func (s *Store) TemplateImagesSince(ctx context.Context) (time.Time, error) {
	return metaTime(ctx, s.db, "template_images_since")
}

// ImageRevision is a revision of a workload with its allow-listed snapshot: a Deployment's release, or a StatefulSet's
// or DaemonSet's ControllerRevision.
type ImageRevision struct {
	WorkloadID, Kind, Namespace, Workload string
	WorkloadDeletedAt                     time.Time
	ID                                    string // the release id, or the workload revision id
	Revision                              string // the Deployment revision, or the ControllerRevision's name
	Number                                int64
	CreatedAt                             time.Time // the ReplicaSet's, or the ControllerRevision's, creation time as Kubernetes reported it
	ObservedAt                            time.Time // when OpsTide first saw it
	UpdatedAt                             time.Time // when OpsTide last changed the row (a ControllerRevision renumbered by a rollback)
	SupersededAt                          time.Time
	Origin                                string // observed | adopted
	State                                 string // a release's rollout state; "" for a ControllerRevision
	// Containers and InitContainers are the snapshot's containers and initContainers arrays as JSON (only these are
	// read: the rest of the snapshot is not needed for images); "" when the snapshot has none or cannot be read.
	Containers, InitContainers string
	ImageIDs                   string // a release's container -> image ID, from a Pod's status; "" for a ControllerRevision
}

// ImageVersion changes whenever what ImageRevisions and TemplateImages answer can change: a revision or template row
// written, renumbered, ended or pruned, or a workload deleted.
func (s *Store) ImageVersion(ctx context.Context) (string, error) {
	var a, b, c, d, e, f, g int64
	err := s.db.QueryRowContext(ctx, `SELECT
	(SELECT count(*) FROM releases), (SELECT coalesce(max(updated_at), 0) FROM releases),
	(SELECT count(*) FROM workload_revisions), (SELECT coalesce(max(updated_at), 0) FROM workload_revisions),
	(SELECT count(*) FROM template_images), (SELECT coalesce(max(first_seen_at), 0) + coalesce(max(ended_at), 0) FROM template_images),
	(SELECT count(*) + coalesce(max(deleted_at), 0) + coalesce(sum(deleted_at IS NOT NULL), 0) FROM workloads)`).Scan(&a, &b, &c, &d, &e, &f, &g)
	return fmt.Sprint(a, ".", b, ".", c, ".", d, ".", e, ".", f, ".", g), err
}

// ImageRevisions lists every held revision of every workload (in one namespace when given), with its containers.
func (s *Store) ImageRevisions(ctx context.Context, namespace string) ([]ImageRevision, error) {
	where, args := "", []any{}
	if namespace != "" {
		where, args = " WHERE r.namespace = ?", []any{namespace, namespace}
	}
	wwhere := ""
	if namespace != "" {
		wwhere = " WHERE w.namespace = ?"
	}
	q := `SELECT r.workload_id, coalesce(w.kind, 'Deployment'), r.namespace, r.workload, w.deleted_at, r.id, r.revision, r.revision_num,
	r.started_at, r.observed_at, r.updated_at, r.superseded_at, r.origin, r.state,
	coalesce(json_extract(CASE WHEN json_valid(r.snapshot) THEN r.snapshot END, '$.containers'), ''),
	coalesce(json_extract(CASE WHEN json_valid(r.snapshot) THEN r.snapshot END, '$.initContainers'), ''), r.image_ids
FROM releases r LEFT JOIN workloads w ON w.id = r.workload_id` + where + `
UNION ALL
SELECT v.workload_id, v.kind, w.namespace, w.name, w.deleted_at, v.id, v.name, v.revision_num,
	v.created_at, v.observed_at, v.updated_at, NULL, v.origin, '',
	coalesce(json_extract(CASE WHEN json_valid(v.snapshot) THEN v.snapshot END, '$.containers'), ''),
	coalesce(json_extract(CASE WHEN json_valid(v.snapshot) THEN v.snapshot END, '$.initContainers'), ''), ''
FROM workload_revisions v JOIN workloads w ON w.id = v.workload_id` + wwhere
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImageRevision
	for rows.Next() {
		var r ImageRevision
		var deleted, created, superseded sql.NullInt64
		var observed, updated int64
		if err := rows.Scan(&r.WorkloadID, &r.Kind, &r.Namespace, &r.Workload, &deleted, &r.ID, &r.Revision, &r.Number,
			&created, &observed, &updated, &superseded, &r.Origin, &r.State, &r.Containers, &r.InitContainers, &r.ImageIDs); err != nil {
			return nil, err
		}
		r.WorkloadDeletedAt, r.CreatedAt, r.SupersededAt = fromMS(deleted), fromMS(created), fromMS(superseded)
		r.ObservedAt, r.UpdatedAt = time.UnixMilli(observed).UTC(), time.UnixMilli(updated).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}
