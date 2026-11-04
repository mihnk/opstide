package schedule

import (
	"testing"
	"time"
)

func mustParse(t *testing.T, spec, tz string) *Schedule {
	t.Helper()
	s, err := Parse(spec, tz, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestParse(t *testing.T) {
	s := mustParse(t, "*/30 * * * *", "")
	if !s.ZoneAssumed || s.Location() != time.UTC {
		t.Fatal("no timeZone should assume the controller's zone, UTC by default")
	}
	ist, _ := time.LoadLocation("Europe/Istanbul")
	if s, _ := Parse("@daily", "", ist); s.Location() != ist {
		t.Fatal("controller zone ignored")
	}
	if s := mustParse(t, "@hourly", "Europe/Istanbul"); s.ZoneAssumed || s.Location().String() != "Europe/Istanbul" {
		t.Fatal("spec.timeZone ignored")
	}
	for _, bad := range [][2]string{{"61 * * * *", ""}, {"* * * *", ""}, {"@daily", "Mars/Olympus"}} {
		if _, err := Parse(bad[0], bad[1], nil); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestBetweenUsesTheTimeZone(t *testing.T) {
	s := mustParse(t, "12 3 * * *", "Europe/Istanbul") // 03:12 local = 00:12 UTC
	from := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	got, truncated := s.Between(from, from.Add(48*time.Hour))
	if truncated || len(got) != 2 || !got[0].Equal(time.Date(2026, 11, 3, 0, 12, 0, 0, time.UTC)) {
		t.Fatalf("got %v", got)
	}
	// from is inclusive, to exclusive.
	got, _ = s.Between(got[0], got[1])
	if len(got) != 1 {
		t.Fatalf("bounds: %v", got)
	}
	if n := s.Next(got[0]); !n.Equal(got[0].Add(24 * time.Hour)) {
		t.Fatalf("next %v", n)
	}
}

// The controller uses the same library; these pin its daylight-saving
// behaviour so a library upgrade that changes it is noticed.
func TestDaylightSavingMatchesTheController(t *testing.T) {
	s := mustParse(t, "30 2 * * *", "Europe/Berlin")
	berlin := s.Location()

	// 29 March 2026: 02:00 → 03:00. 02:30 does not exist and does not fire.
	got, _ := s.Between(time.Date(2026, 3, 28, 12, 0, 0, 0, berlin), time.Date(2026, 3, 30, 12, 0, 0, 0, berlin))
	if len(got) != 1 || got[0].In(berlin).Day() != 30 {
		t.Fatalf("spring: %v", got)
	}
	// 25 October 2026: 03:00 → 02:00. 02:30 happens twice and fires twice.
	got, _ = s.Between(time.Date(2026, 10, 25, 0, 0, 0, 0, berlin), time.Date(2026, 10, 25, 12, 0, 0, 0, berlin))
	if len(got) != 2 || got[1].Sub(got[0]) != time.Hour {
		t.Fatalf("autumn: %v", got)
	}
}

func TestBetweenIsBounded(t *testing.T) {
	s := mustParse(t, "* * * * *", "")
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got, truncated := s.Between(from, from.AddDate(0, 1, 0))
	if !truncated || len(got) != MaxSlots {
		t.Fatalf("%d slots, truncated %v", len(got), truncated)
	}
}

func TestJobNameTime(t *testing.T) {
	at := time.Date(2026, 11, 4, 5, 0, 0, 0, time.UTC)
	if got, ok := JobNameTime("payment-reconciliation", "payment-reconciliation-29896140"); !ok || !got.Equal(at) {
		t.Fatalf("got %v %v", got, ok)
	}
	for _, name := range []string{"payment-reconciliation", "payment-reconciliation-", "payment-reconciliation-manual-x7", "other-29896140", "payment-reconciliation-99999999999999999999"} {
		if _, ok := JobNameTime("payment-reconciliation", name); ok {
			t.Errorf("%q parsed", name)
		}
	}
}
