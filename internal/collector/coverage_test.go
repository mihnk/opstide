package collector

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/mihnk/opstide/internal/store"
)

func TestTrackerRecordsGapsOnlyWhenTheWatchCouldNotResume(t *testing.T) {
	db := openStore(t)
	ctx := context.Background()
	now := t0
	tr := newTracker(db, slog.New(slog.DiscardHandler), func() time.Time { return now })
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	set := func(m int) { now = at(m) }

	set(0)
	tr.listStarted("jobs")
	tr.listDone("jobs")
	tr.watchStarted("jobs")
	set(5)
	tr.touch("jobs")
	// Connection drops at 6; the watch resumes from its resourceVersion at 7.
	set(6)
	tr.failed("jobs", errors.New("connection reset"))
	if len(tr.problems()) != 1 {
		t.Fatal("problem not reported")
	}
	set(7)
	tr.watchStarted("jobs")
	if len(tr.problems()) != 0 {
		t.Fatal("problem not cleared")
	}
	set(9)
	tr.touch("jobs")
	// 410 Gone at 12: the reflector relists, from 13.
	set(13)
	tr.listStarted("jobs")
	tr.listDone("jobs")

	ivs, err := db.Coverage(ctx, at(0), at(60))
	if err != nil {
		t.Fatal(err)
	}
	if len(ivs) != 2 || !ivs[0].StartedAt.Equal(at(0)) || !ivs[0].EndedAt.Equal(at(9)) || !ivs[1].StartedAt.Equal(at(13)) || !ivs[1].EndedAt.IsZero() {
		t.Fatalf("intervals %+v", ivs)
	}
	p, _ := db.Events(ctx, store.EventQuery{Types: []string{"collection.gap"}})
	if len(p.Events) != 1 || p.Events[0].Facts["seconds"] != "240" || p.Events[0].Kind != "jobs" || !p.Events[0].ObservedAt.Equal(at(13)) {
		t.Fatalf("gap entries %+v", p.Events)
	}

	// The process stops after a heartbeat at 20 and the next one lists at 25.
	if err := db.Update(ctx, func(w *store.Writer) error { return w.Heartbeat(ctx, at(20)) }); err != nil {
		t.Fatal(err)
	}
	if err := db.Update(ctx, func(w *store.Writer) error { _, err := w.CloseAfterRestart(ctx); return err }); err != nil {
		t.Fatal(err)
	}
	set(25)
	tr2 := newTracker(db, slog.New(slog.DiscardHandler), func() time.Time { return now })
	tr2.listStarted("jobs")
	tr2.listDone("jobs")
	p, _ = db.Events(ctx, store.EventQuery{Types: []string{"collection.gap"}})
	if len(p.Events) != 2 || p.Events[0].Facts["seconds"] != "300" {
		t.Fatalf("restart gap %+v", p.Events)
	}
}
