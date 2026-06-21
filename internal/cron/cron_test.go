package cron

import (
	"testing"
	"time"
)

func cdmx(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(DefaultTimezone)
	if err != nil {
		t.Fatalf("load CDMX tz: %v", err)
	}
	return loc
}

func TestDue_FiresExactlyOnceAtTheMinute(t *testing.T) {
	loc := cdmx(t)
	j := Job{ID: "gastos", Schedule: "0 9 * * *", Enabled: true}

	// 9:00 sharp: window (8:59, 9:00] contains the 9:00 fire → due.
	now := time.Date(2026, 6, 22, 9, 0, 0, 0, loc)
	last := now.Add(-time.Minute)
	if ok, err := due(j, loc, last, now); err != nil || !ok {
		t.Fatalf("expected due at 9:00, got ok=%v err=%v", ok, err)
	}

	// 9:01: window (9:00, 9:01] — already fired last tick → not due.
	if ok, _ := due(j, loc, now, now.Add(time.Minute)); ok {
		t.Fatalf("did not expect a second fire at 9:01")
	}

	// 8:30: not the scheduled minute → not due.
	at830 := time.Date(2026, 6, 22, 8, 30, 0, 0, loc)
	if ok, _ := due(j, loc, at830.Add(-time.Minute), at830); ok {
		t.Fatalf("did not expect a fire at 8:30")
	}
}

func TestDue_WeekdaysOnly(t *testing.T) {
	loc := cdmx(t)
	j := Job{ID: "no-circula", Schedule: "30 6 * * 1-5", Enabled: true} // 6:30 Mon–Fri

	// 2026-06-22 is a Monday → due at 6:30.
	mon := time.Date(2026, 6, 22, 6, 30, 0, 0, loc)
	if ok, _ := due(j, loc, mon.Add(-time.Minute), mon); !ok {
		t.Fatalf("expected fire Monday 6:30")
	}
	// 2026-06-21 is a Sunday → not due.
	sun := time.Date(2026, 6, 21, 6, 30, 0, 0, loc)
	if ok, _ := due(j, loc, sun.Add(-time.Minute), sun); ok {
		t.Fatalf("did not expect fire Sunday 6:30")
	}
}

func TestDue_BadScheduleErrors(t *testing.T) {
	loc := cdmx(t)
	if _, err := due(Job{Schedule: "not a cron"}, loc, time.Now(), time.Now()); err == nil {
		t.Fatal("expected error for invalid schedule")
	}
}

func TestNextRuns(t *testing.T) {
	loc := cdmx(t)
	runs, err := NextRuns(Job{Schedule: "0 9 * * *"}, loc, 3)
	if err != nil {
		t.Fatalf("NextRuns: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("want 3 runs, got %d", len(runs))
	}
	for _, r := range runs {
		if r.Hour() != 9 || r.Minute() != 0 {
			t.Fatalf("expected 9:00 runs, got %s", r.Format("15:04"))
		}
		if r.Location().String() != DefaultTimezone {
			t.Fatalf("expected CDMX location, got %s", r.Location())
		}
	}
}

func TestLoad_MissingFileIsEmpty(t *testing.T) {
	f, err := Load("/nonexistent/crons.json")
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if len(f.Crons) != 0 || f.Timezone != DefaultTimezone {
		t.Fatalf("expected empty file with default tz, got %+v", f)
	}
}
