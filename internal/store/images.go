package store

import (
	"context"
	"database/sql"
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
}

// TemplateContainer is a container of a template as seen now.
type TemplateContainer struct {
	Name  string
	Init  bool
	Image string
}

// SetTemplateImages records the images a CronJob's job template names now. A container whose image changed, or that
// is gone, has its row ended at at; a new image opens a row from at. origin applies to rows opened now.
func (w *Writer) SetTemplateImages(ctx context.Context, workloadID, kind, ns, name string, cs []TemplateContainer, origin string, at time.Time) error {
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
		if img, ok := want[k]; ok && img == o.image {
			continue
		}
		if _, err := w.tx.ExecContext(ctx, `UPDATE template_images SET ended_at = ? WHERE workload_id = ? AND init = ? AND container = ? AND first_seen_at = ?`,
			now, workloadID, k.init, k.name, o.first); err != nil {
			return err
		}
	}
	for k, img := range want {
		if o, ok := have[k]; ok && o.image == img {
			continue
		}
		if _, err := w.tx.ExecContext(ctx, `INSERT OR REPLACE INTO template_images (workload_id, workload_kind, namespace, workload_name, container, init, image, origin, first_seen_at, ended_at)
VALUES (?,?,?,?,?,?,?,?,?,NULL)`, workloadID, kind, ns, name, k.name, k.init, img, origin, now); err != nil {
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
	_, err := w.tx.ExecContext(ctx, `UPDATE template_images SET ended_at = ? WHERE workload_id = ? AND ended_at IS NULL`, ms(at), workloadID)
	return err
}

// TemplateImages lists every recorded template image, in a namespace when one is given.
func (s *Store) TemplateImages(ctx context.Context, namespace string) ([]TemplateImage, error) {
	q := `SELECT workload_id, workload_kind, namespace, workload_name, container, init, image, origin, first_seen_at, ended_at FROM template_images`
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
		if err := rows.Scan(&t.WorkloadID, &t.WorkloadKind, &t.Namespace, &t.WorkloadName, &t.Container, &t.Init, &t.Image, &t.Origin, &first, &ended); err != nil {
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
	Snapshot                              string
	ImageIDs                              string // a release's container -> image ID, from a Pod's status; "" for a ControllerRevision
}

// ImageRevisions lists every held revision of every workload (in one namespace when given), the snapshot included.
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
	r.started_at, r.observed_at, r.updated_at, r.superseded_at, r.origin, r.state, r.snapshot, r.image_ids
FROM releases r LEFT JOIN workloads w ON w.id = r.workload_id` + where + `
UNION ALL
SELECT v.workload_id, v.kind, w.namespace, w.name, w.deleted_at, v.id, v.name, v.revision_num,
	v.created_at, v.observed_at, v.updated_at, NULL, v.origin, '', v.snapshot, ''
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
			&created, &observed, &updated, &superseded, &r.Origin, &r.State, &r.Snapshot, &r.ImageIDs); err != nil {
			return nil, err
		}
		r.WorkloadDeletedAt, r.CreatedAt, r.SupersededAt = fromMS(deleted), fromMS(created), fromMS(superseded)
		r.ObservedAt, r.UpdatedAt = time.UnixMilli(observed).UTC(), time.UnixMilli(updated).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}
