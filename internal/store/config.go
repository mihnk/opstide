package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

// ConfigMaps and Secrets. OpsTide keeps who they are and when it saw them; for a ConfigMap also an HMAC of its content and
// of each key's value under a key made for this install, and the names of its keys, so a data change can be told from a
// metadata change. It never keeps a value, and it never asks for a Secret's data at all (its watch is metadata-only).

// Kinds of configuration objects.
const (
	KindConfigMap = "ConfigMap"
	KindSecret    = "Secret"
)

// ConfigObject is a ConfigMap or Secret as OpsTide last saw it.
type ConfigObject struct {
	UID, Kind, Namespace, Name string
	CreatedAt                  time.Time
	FirstSeenAt, LastSeenAt    time.Time
	DeletedAt                  time.Time
	Origin                     string // observed | adopted
	ContentHash                string // ConfigMap only: HMAC of the sorted keys and values; '' when not known
	Keys                       []string
	KeyHashes                  map[string]string // ConfigMap only: key name -> HMAC of its value
	// WritesSeen is, per field manager (manager|operation|subresource), the Unix second of the newest write already
	// handled: recorded, or deliberately left out (a baseline, older than retention).
	WritesSeen map[string]int64
	// HelmRelease marks one of Helm's release records (named sh.helm.release.v1.*, labelled owner=helm).
	HelmRelease bool
}

const configCols = `uid, kind, namespace, name, created_at, first_seen_at, last_seen_at, deleted_at, origin, content_hash, keys, key_hashes, writes_seen, helm_release`

func scanConfig(r interface{ Scan(...any) error }) (ConfigObject, error) {
	var o ConfigObject
	var created, deleted sql.NullInt64
	var first, last int64
	var keys, keyHashes, writes string
	if err := r.Scan(&o.UID, &o.Kind, &o.Namespace, &o.Name, &created, &first, &last, &deleted, &o.Origin, &o.ContentHash, &keys, &keyHashes, &writes, &o.HelmRelease); err != nil {
		return o, err
	}
	o.CreatedAt, o.DeletedAt = fromMS(created), fromMS(deleted)
	o.FirstSeenAt, o.LastSeenAt = time.UnixMilli(first).UTC(), time.UnixMilli(last).UTC()
	_ = json.Unmarshal([]byte(keys), &o.Keys)
	_ = json.Unmarshal([]byte(keyHashes), &o.KeyHashes)
	_ = json.Unmarshal([]byte(writes), &o.WritesSeen)
	return o, nil
}

// ConfigObject returns the ConfigMap or Secret with this UID.
func (w *Writer) ConfigObject(ctx context.Context, uid string) (ConfigObject, bool, error) {
	o, err := scanConfig(w.tx.QueryRowContext(ctx, `SELECT `+configCols+` FROM config_objects WHERE uid = ?`, uid))
	if errors.Is(err, sql.ErrNoRows) {
		return ConfigObject{}, false, nil
	}
	return o, err == nil, err
}

// ConfigObjectByUID is ConfigObject outside a write transaction: a read that takes no write lock.
func (s *Store) ConfigObjectByUID(ctx context.Context, uid string) (ConfigObject, bool, error) {
	o, err := scanConfig(s.db.QueryRowContext(ctx, `SELECT `+configCols+` FROM config_objects WHERE uid = ?`, uid))
	if errors.Is(err, sql.ErrNoRows) {
		return ConfigObject{}, false, nil
	}
	return o, err == nil, err
}

// SaveConfigObject inserts or replaces a ConfigMap's or Secret's record.
func (w *Writer) SaveConfigObject(ctx context.Context, o ConfigObject) error {
	keys := o.Keys
	if keys == nil {
		keys = []string{}
	}
	kj, _ := json.Marshal(keys)
	kh := []byte("{}")
	if len(o.KeyHashes) > 0 {
		kh, _ = json.Marshal(o.KeyHashes)
	}
	ws := []byte("{}")
	if len(o.WritesSeen) > 0 {
		ws, _ = json.Marshal(o.WritesSeen)
	}
	_, err := w.tx.ExecContext(ctx, `INSERT OR REPLACE INTO config_objects (`+configCols+`, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		o.UID, o.Kind, o.Namespace, o.Name, ms(o.CreatedAt), ms(o.FirstSeenAt).Int64, ms(o.LastSeenAt).Int64, ms(o.DeletedAt), o.Origin, o.ContentHash,
		string(kj), string(kh), string(ws), o.HelmRelease, ms(w.now()).Int64)
	return err
}

// MarkConfigDeleted notes that a ConfigMap or Secret is gone; its record stays (until retention).
func (w *Writer) MarkConfigDeleted(ctx context.Context, uid string, at time.Time) error {
	_, err := w.tx.ExecContext(ctx, `UPDATE config_objects SET deleted_at = ?, updated_at = ? WHERE uid = ? AND deleted_at IS NULL`, ms(at).Int64, ms(w.now()).Int64, uid)
	return err
}

// TouchConfigObjects sets last_seen_at of these live ConfigMaps or Secrets, in one statement per 500.
func (s *Store) TouchConfigObjects(ctx context.Context, uids []string, at time.Time) error {
	for len(uids) > 0 {
		n := min(len(uids), 500)
		part := uids[:n]
		uids = uids[n:]
		args := []any{ms(at).Int64}
		for _, u := range part {
			args = append(args, u)
		}
		marks := strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")
		if err := s.Update(ctx, func(w *Writer) error {
			_, err := w.tx.ExecContext(ctx, `UPDATE config_objects SET last_seen_at = max(last_seen_at, ?) WHERE deleted_at IS NULL AND uid IN (`+marks+`)`, args...) //nolint:gosec // placeholders only
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}

// ConfigHashKey is the key ConfigMap content is hashed under: 32 random bytes made once per database and kept in meta.
// It is never served by the API or exported; without it a hash cannot be checked against a guessed value.
func (s *Store) ConfigHashKey(ctx context.Context) ([]byte, error) {
	var out []byte
	err := s.Update(ctx, func(w *Writer) error {
		var v string
		err := w.tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'config_hash_key'`).Scan(&v)
		if err == nil {
			out, err = hex.DecodeString(v)
			if err == nil && len(out) == 32 {
				return nil
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		out = make([]byte, 32)
		if _, err := rand.Read(out); err != nil {
			return err
		}
		return w.setMeta(ctx, "config_hash_key", hex.EncodeToString(out))
	})
	return out, err
}

// LiveConfigObjects lists the ConfigMaps or Secrets of a kind recorded as existing.
func (s *Store) LiveConfigObjects(ctx context.Context, kind string) ([]ConfigObject, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+configCols+` FROM config_objects WHERE kind = ? AND deleted_at IS NULL ORDER BY namespace, name`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConfigObject
	for rows.Next() {
		o, err := scanConfig(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ConfigCounts counts the live ConfigMaps and Secrets recorded, by kind.
func (s *Store) ConfigCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, count(*) FROM config_objects WHERE deleted_at IS NULL GROUP BY kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// ConfigWindow is a ConfigMap or Secret a workload's Pod template named, and when it named it: from when OpsTide first
// saw the reference to when it ended (zero To: it still does).
type ConfigWindow struct {
	UID      string
	From, To time.Time
}

// ConfigUIDs lists, for each reference of a workload, every ConfigMap or Secret recorded under that name (an object
// deleted and created again under the same name has a new UID; all of them are named), with the reference's window.
func (s *Store) ConfigUIDs(ctx context.Context, workloadID string) ([]ConfigWindow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT o.uid, r.first_seen_at, r.ended_at FROM config_refs r
	JOIN config_objects o ON o.namespace = r.namespace AND o.kind = r.ref_kind AND o.name = r.ref_name
	WHERE r.workload_id = ? ORDER BY o.uid LIMIT 500`, workloadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConfigWindow
	for rows.Next() {
		var x ConfigWindow
		var from int64
		var to sql.NullInt64
		if err := rows.Scan(&x.UID, &from, &to); err != nil {
			return nil, err
		}
		x.From, x.To = time.UnixMilli(from).UTC(), fromMS(to)
		out = append(out, x)
	}
	return out, rows.Err()
}

// OpenConfigRefWorkloads lists the workloads of a kind that have a reference OpsTide has not ended.
func (s *Store) OpenConfigRefWorkloads(ctx context.Context, workloadKind string) ([]Workload, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT workload_id, namespace, workload_name FROM config_refs WHERE workload_kind = ? AND ended_at IS NULL`, workloadKind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Workload
	for rows.Next() {
		x := Workload{Kind: workloadKind}
		if err := rows.Scan(&x.ID, &x.Namespace, &x.Name); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ConfigName names a ConfigMap or Secret within a namespace.
type ConfigName struct {
	Kind, Name string
}

// ConfigRef is one workload's Pod template naming a ConfigMap or Secret, as OpsTide saw it.
type ConfigRef struct {
	WorkloadID, WorkloadKind, Namespace, WorkloadName string
	RefKind, RefName                                  string
	FirstSeenAt, LastSeenAt                           time.Time
	EndedAt                                           time.Time // zero while the template still names it
}

const refCols = `workload_id, workload_kind, namespace, workload_name, ref_kind, ref_name, first_seen_at, last_seen_at, ended_at`

func scanRef(r interface{ Scan(...any) error }) (ConfigRef, error) {
	var x ConfigRef
	var first, last int64
	var ended sql.NullInt64
	err := r.Scan(&x.WorkloadID, &x.WorkloadKind, &x.Namespace, &x.WorkloadName, &x.RefKind, &x.RefName, &first, &last, &ended)
	x.FirstSeenAt, x.LastSeenAt, x.EndedAt = time.UnixMilli(first).UTC(), time.UnixMilli(last).UTC(), fromMS(ended)
	return x, err
}

// SetConfigRefs records which ConfigMaps and Secrets a workload's Pod template names now. A name it no longer names is
// ended (kept, with when); a name it names again is reopened.
func (w *Writer) SetConfigRefs(ctx context.Context, workloadID, workloadKind, ns, workloadName string, names []ConfigName, at time.Time) error {
	now := ms(at).Int64
	want := map[ConfigName]bool{}
	for _, n := range names {
		want[n] = true
	}
	rows, err := w.tx.QueryContext(ctx, `SELECT ref_kind, ref_name, ended_at FROM config_refs WHERE workload_id = ?`, workloadID)
	if err != nil {
		return err
	}
	have := map[ConfigName]bool{} // value: still open
	for rows.Next() {
		var k, n string
		var ended sql.NullInt64
		if err := rows.Scan(&k, &n, &ended); err != nil {
			rows.Close()
			return err
		}
		have[ConfigName{k, n}] = !ended.Valid
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for n := range want {
		open, known := have[n]
		switch {
		case !known:
			_, err = w.tx.ExecContext(ctx, `INSERT INTO config_refs (`+refCols+`) VALUES (?,?,?,?,?,?,?,?,NULL)`, workloadID, workloadKind, ns, workloadName, n.Kind, n.Name, now, now)
		case !open:
			_, err = w.tx.ExecContext(ctx, `UPDATE config_refs SET ended_at = NULL, first_seen_at = ?, last_seen_at = ?, workload_name = ? WHERE workload_id = ? AND ref_kind = ? AND ref_name = ?`, now, now, workloadName, workloadID, n.Kind, n.Name)
		default:
			_, err = w.tx.ExecContext(ctx, `UPDATE config_refs SET last_seen_at = ? WHERE workload_id = ? AND ref_kind = ? AND ref_name = ?`, now, workloadID, n.Kind, n.Name)
		}
		if err != nil {
			return err
		}
	}
	for n, open := range have {
		if open && !want[n] {
			if _, err := w.tx.ExecContext(ctx, `UPDATE config_refs SET ended_at = ? WHERE workload_id = ? AND ref_kind = ? AND ref_name = ?`, now, workloadID, n.Kind, n.Name); err != nil {
				return err
			}
		}
	}
	return nil
}

// EndConfigRefs ends every reference of a workload that is gone.
func (w *Writer) EndConfigRefs(ctx context.Context, workloadID string, at time.Time) error {
	_, err := w.tx.ExecContext(ctx, `UPDATE config_refs SET ended_at = ? WHERE workload_id = ? AND ended_at IS NULL`, ms(at).Int64, workloadID)
	return err
}

// ConfigReferrers lists the workloads whose Pod templates name this ConfigMap or Secret, or did at or after `at` (zero:
// any time), oldest reference first.
func (s *Store) ConfigReferrers(ctx context.Context, ns, kind, name string, at time.Time) ([]ConfigRef, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+refCols+` FROM config_refs WHERE namespace = ? AND ref_kind = ? AND ref_name = ? AND (ended_at IS NULL OR ended_at >= ?)
		ORDER BY first_seen_at, workload_kind, workload_name LIMIT 200`, ns, kind, name, ms(at).Int64)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConfigRef
	for rows.Next() {
		x, err := scanRef(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// WorkloadConfigRefs lists what a workload's Pod template names or named, open references first, then by kind and name.
func (s *Store) WorkloadConfigRefs(ctx context.Context, workloadID string) ([]ConfigRef, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+refCols+` FROM config_refs WHERE workload_id = ? LIMIT 500`, workloadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConfigRef
	for rows.Next() {
		x, err := scanRef(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.EndedAt.IsZero() != b.EndedAt.IsZero() {
			return a.EndedAt.IsZero()
		}
		if a.RefKind != b.RefKind {
			return a.RefKind < b.RefKind
		}
		return a.RefName < b.RefName
	})
	return out, rows.Err()
}
