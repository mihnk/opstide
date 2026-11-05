package store

import (
	"context"
	"time"
)

// RunBucket counts the runs created in one stretch of time.
type RunBucket struct {
	Start  time.Time
	Runs   int
	Failed int
}

// RunHistogram counts runs created in [from, to) per bucket of the given
// length, aligned to whole buckets of a clock that is offset from UTC by
// offset (so hours start on the reader's hour, also in a zone at +05:30).
// Empty buckets are left out. It is a single aggregate query, so a month of
// history stays cheap to draw.
func (s *Store) RunHistogram(ctx context.Context, from, to time.Time, bucket, offset time.Duration) ([]RunBucket, error) {
	ms, off := bucket.Milliseconds(), offset.Milliseconds()
	rows, err := s.db.QueryContext(ctx, `
SELECT ((created_at + ?) / ?) * ? - ?, count(*), sum(CASE WHEN status = 'failed' THEN 1 ELSE 0 END)
FROM runs WHERE created_at >= ? AND created_at < ? GROUP BY 1 ORDER BY 1`,
		off, ms, ms, off, from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunBucket
	for rows.Next() {
		var start int64
		var b RunBucket
		if err := rows.Scan(&start, &b.Runs, &b.Failed); err != nil {
			return nil, err
		}
		b.Start = time.UnixMilli(start).UTC()
		out = append(out, b)
	}
	return out, rows.Err()
}

// FirstWatchedByScope is when each Jobs scope ("jobs", or "jobs/<namespace>")
// was first watched, from the whole record, not only a recent stretch of it.
func (s *Store) FirstWatchedByScope(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT scope, min(started_at) FROM coverage WHERE scope = 'jobs' OR scope LIKE 'jobs/%' GROUP BY scope`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var scope string
		var ms int64
		if err := rows.Scan(&scope, &ms); err != nil {
			return nil, err
		}
		out[scope] = time.UnixMilli(ms).UTC()
	}
	return out, rows.Err()
}

// FirstWatched is when Jobs were first watched, in any scope; zero when
// nothing has been watched yet. Nothing before it can be said about Jobs.
func (s *Store) FirstWatched(ctx context.Context) (time.Time, error) {
	var first *int64
	err := s.db.QueryRowContext(ctx, `SELECT min(started_at) FROM coverage WHERE scope = 'jobs' OR scope LIKE 'jobs/%'`).Scan(&first)
	if err != nil || first == nil {
		return time.Time{}, err
	}
	return time.UnixMilli(*first).UTC(), nil
}
