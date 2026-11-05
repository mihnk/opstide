package schedule

import (
	"math/rand/v2"
	"sort"
	"testing"
	"time"
)

var day = time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)

func at(h, m int) time.Time {
	return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
}

func always(time.Time, time.Time) bool { return true }

// gap returns coverage with nothing watched in [from, to).
func gap(from, to time.Time) Coverage {
	return func(a, b time.Time) bool { return !a.Before(to) || !b.After(from) }
}

func states(slots []Slot) []State {
	out := make([]State, len(slots))
	for i, s := range slots {
		out[i] = s.State
	}
	return out
}

func eq(t *testing.T, got []Slot, want ...State) {
	t.Helper()
	g := states(got)
	if len(g) != len(want) {
		t.Fatalf("got %v, want %v", g, want)
	}
	for i := range g {
		if g[i] != want[i] {
			t.Fatalf("slot %d (%s): got %v, want %v", i, got[i].At.Format("15:04"), g, want)
		}
	}
}

func TestSlotsByState(t *testing.T) {
	s := mustParse(t, "0 * * * *", "")
	op := Operation{CreatedAt: day.AddDate(0, -1, 0), ConcurrencyPolicy: "Allow"}
	runs := []Run{
		{ID: "r0", ScheduledAt: at(0, 0), CreatedAt: at(0, 0), FinishedAt: at(0, 3)},
		{ID: "r2", ScheduledAt: at(2, 0), CreatedAt: at(2, 0), FinishedAt: at(2, 3)},
		{ID: "manual", CreatedAt: at(1, 10), FinishedAt: at(1, 12)}, // kubectl create job --from: no slot
	}
	now := at(5, 1)
	got, _ := Slots(s, op, runs, gap(at(3, 0), at(3, 1)), day, at(7, 0), now)
	// 00 ran · 01 missed · 02 ran · 03 not confirmed (gap) · 04 missed · 05 due (inside grace) · 06 expected
	eq(t, got, Ran, Missed, Ran, Unconfirmed, Missed, Due, Expected)
	if got[0].RunID != "r0" || got[1].Why == "" || got[3].Why == "" {
		t.Fatalf("%+v", got)
	}
}

func TestStartingDeadlineWidensTheWindow(t *testing.T) {
	s := mustParse(t, "0 * * * *", "")
	deadline := 10 * time.Minute
	op := Operation{CreatedAt: day.AddDate(0, -1, 0), StartingDeadline: &deadline}
	got, _ := Slots(s, op, nil, always, at(5, 0), at(6, 0), at(5, 5))
	eq(t, got, Due)
	got, _ = Slots(s, op, nil, always, at(5, 0), at(6, 0), at(5, 11))
	eq(t, got, Missed)
	// A gap anywhere in the start window leaves it unconfirmed.
	got, _ = Slots(s, op, nil, gap(at(5, 8), at(5, 9)), at(5, 0), at(6, 0), at(5, 11))
	eq(t, got, Unconfirmed)
}

func TestForbidSkipsWhileThePreviousRunIsActive(t *testing.T) {
	s := mustParse(t, "*/30 * * * *", "")
	op := Operation{CreatedAt: day.AddDate(0, -1, 0), ConcurrencyPolicy: "Forbid"}
	runs := []Run{
		{ID: "long", ScheduledAt: at(8, 0), CreatedAt: at(8, 0), FinishedAt: at(8, 45)},
		{ID: "next", ScheduledAt: at(9, 0), CreatedAt: at(9, 0)}, // still running
	}
	got, _ := Slots(s, op, runs, always, at(8, 0), at(10, 0), at(9, 40))
	eq(t, got, Ran, Skipped, Ran, Skipped)

	op.ConcurrencyPolicy = "Allow"
	got, _ = Slots(s, op, runs, always, at(8, 0), at(10, 0), at(9, 40))
	eq(t, got, Ran, Missed, Ran, Missed)
}

func TestSuspensionAndLifetime(t *testing.T) {
	s := mustParse(t, "0 * * * *", "")
	op := Operation{
		CreatedAt:   at(1, 30),
		DeletedAt:   at(9, 30),
		Suspensions: []Window{{Start: at(3, 30), End: at(5, 30)}, {Start: at(8, 30)}},
	}
	got, _ := Slots(s, op, nil, always, day, day.Add(24*time.Hour), at(7, 10))
	// 02 missed · 03 missed · 04 05 suspended · 06 missed · 07 due · 08 expected · 09 suspended (open window); nothing after deletion
	eq(t, got, Missed, Missed, Suspended, Suspended, Missed, Missed, Expected, Suspended)
	if got[0].At != at(2, 0) || got[len(got)-1].At != at(9, 0) {
		t.Fatalf("bounds %v .. %v", got[0].At, got[len(got)-1].At)
	}
	if got, _ := Slots(s, op, nil, always, at(10, 0), at(12, 0), at(11, 0)); len(got) != 0 {
		t.Fatalf("after deletion: %v", got)
	}
}

// activeAt was a scan of every run; the index must give the same answer.
func TestActiveIndexMatchesAScan(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	t0 := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	scan := func(runs []Run, at time.Time) bool {
		for _, r := range runs {
			if r.CreatedAt.Before(at) && (r.FinishedAt.IsZero() || r.FinishedAt.After(at)) {
				return true
			}
		}
		return false
	}
	for range 300 {
		var runs []Run
		for range rng.IntN(25) {
			c := t0.Add(time.Duration(rng.IntN(600)) * time.Minute)
			r := Run{CreatedAt: c}
			if rng.IntN(4) > 0 { // some never finish
				r.FinishedAt = c.Add(time.Duration(rng.IntN(120)) * time.Minute)
			}
			runs = append(runs, r)
		}
		sort.Slice(runs, func(i, j int) bool { return runs[i].CreatedAt.Before(runs[j].CreatedAt) })
		ix := newActiveIndex(runs)
		for m := -5; m < 760; m++ {
			at := t0.Add(time.Duration(m) * time.Minute)
			if got, want := ix.at(at), scan(runs, at); got != want {
				t.Fatalf("at +%dm with %+v: index %v, scan %v", m, runs, got, want)
			}
		}
	}
	if newActiveIndex(nil).at(t0) {
		t.Fatal("no runs, yet active")
	}
}
