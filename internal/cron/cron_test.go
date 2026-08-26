package cron

import (
	"os"
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

// firesIn walks every minute of [from, to) exactly as Run does — contiguous
// one-minute windows — and returns when the job actually fired. This is the only
// honest way to test a jittered schedule: the fire minute moved on purpose, so
// asserting a fixed minute would just re-assert the old behaviour.
func firesIn(j Job, loc *time.Location, from, to time.Time) []time.Time {
	var out []time.Time
	for last := from; last.Before(to); last = last.Add(time.Minute) {
		now := last.Add(time.Minute)
		if ok, err := due(j, loc, last, now); err == nil && ok {
			out = append(out, now)
		}
	}
	return out
}

func TestDue_FiresExactlyOncePerOccurrence(t *testing.T) {
	loc := cdmx(t)
	j := Job{ID: "gastos", Schedule: "0 9 * * *", Enabled: true}

	// Three whole days, minute by minute: three fires, no more, no less. A drift
	// that let an occurrence slip between two windows, or be caught by both, shows
	// up here as 2 or 4.
	from := time.Date(2026, 6, 22, 0, 0, 0, 0, loc)
	got := firesIn(j, loc, from, from.AddDate(0, 0, 3))
	if len(got) != 3 {
		t.Fatalf("expected exactly 3 fires in 3 days, got %d: %v", len(got), got)
	}
	for _, f := range got {
		scheduled := time.Date(f.Year(), f.Month(), f.Day(), 9, 0, 0, 0, loc)
		if d := f.Sub(scheduled); d < -jitterWindow || d > jitterWindow {
			t.Fatalf("fire at %s drifted %v from 9:00, outside ±%v", f, d, jitterWindow)
		}
	}
}

func TestDue_WeekdaysOnly(t *testing.T) {
	loc := cdmx(t)
	j := Job{ID: "no-circula", Schedule: "30 6 * * 1-5", Enabled: true} // 6:30 Mon–Fri

	// 2026-06-22 is a Monday → exactly one fire that day.
	mon := time.Date(2026, 6, 22, 0, 0, 0, 0, loc)
	if got := firesIn(j, loc, mon, mon.AddDate(0, 0, 1)); len(got) != 1 {
		t.Fatalf("expected 1 fire Monday, got %d: %v", len(got), got)
	}
	// 2026-06-21 is a Sunday → none. Drift must not leak a fire across the day
	// boundary into a day the schedule excludes.
	sun := time.Date(2026, 6, 21, 0, 0, 0, 0, loc)
	if got := firesIn(j, loc, sun, sun.AddDate(0, 0, 1)); len(got) != 0 {
		t.Fatalf("expected no fire Sunday, got %d: %v", len(got), got)
	}
}

func TestDrift_StaysInsideWindow(t *testing.T) {
	loc := cdmx(t)
	sched, err := parser.Parse("0 1 * * *")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for i := 0; i < 400; i++ {
		occ := time.Date(2026, 1, 1, 1, 0, 0, 0, loc).AddDate(0, 0, i)
		d := drift("cierre-del-dia", sched, occ)
		if d < -jitterWindow || d > jitterWindow {
			t.Fatalf("drift %v on %s is outside ±%v", d, occ, jitterWindow)
		}
		if d%time.Minute != 0 {
			t.Fatalf("drift %v is not a whole number of minutes", d)
		}
	}
}

func TestDrift_IsDeterministic(t *testing.T) {
	loc := cdmx(t)
	sched, _ := parser.Parse("0 3 * * *")
	occ := time.Date(2026, 8, 24, 3, 0, 0, 0, loc)
	first := drift("backup-diario", sched, occ)
	for i := 0; i < 50; i++ {
		if again := drift("backup-diario", sched, occ); again != first {
			t.Fatalf("drift is not stable: %v then %v", first, again)
		}
	}
}

// The collision this whole change exists to break: zoro, sky and expxxi each run a
// job called "cierre-del-dia" on "0 1 * * *". Same ID, same occurrence — only the
// per-agent salt can pull them apart, so this is the test that would have caught
// shipping a jitter that changed nothing.
func TestDrift_DiffersBetweenAgentsSharingAJobID(t *testing.T) {
	loc := cdmx(t)
	sched, _ := parser.Parse("0 1 * * *")
	saved := instanceSalt
	defer func() { instanceSalt = saved }()

	collisions := 0
	days := 60
	for i := 0; i < days; i++ {
		occ := time.Date(2026, 8, 24, 1, 0, 0, 0, loc).AddDate(0, 0, i)
		seen := map[time.Duration]bool{}
		for _, uid := range []string{"1000", "1001", "1002"} { // rafael, expxxi, sky
			instanceSalt = uid
			d := drift("cierre-del-dia", sched, occ)
			if seen[d] {
				collisions++
			}
			seen[d] = true
		}
	}
	// With 11 slots and 3 agents a few same-minute draws are expected; what must not
	// happen is the agents moving in lockstep, which is what an unsalted hash gives.
	if collisions > days/2 {
		t.Fatalf("agents collide on %d of %d days — salt is not separating them", collisions, days)
	}
}

func TestDrift_SkippedForFrequentSchedules(t *testing.T) {
	loc := cdmx(t)
	sched, err := parser.Parse("*/5 * * * *")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Occurrences 5 minutes apart cannot absorb a ±5 minute drift without
	// overlapping or swapping order, so they must be left exactly on their marks.
	for i := 0; i < 200; i++ {
		occ := time.Date(2026, 8, 24, 0, 0, 0, 0, loc).Add(time.Duration(i) * 5 * time.Minute)
		if d := drift("frequent", sched, occ); d != 0 {
			t.Fatalf("expected no drift on a */5 schedule, got %v at %s", d, occ)
		}
	}
	// And such a job still fires on every one of its occurrences.
	j := Job{ID: "frequent", Schedule: "*/5 * * * *", Enabled: true}
	from := time.Date(2026, 8, 24, 0, 0, 0, 0, loc)
	if got := firesIn(j, loc, from, from.Add(time.Hour)); len(got) != 12 {
		t.Fatalf("expected 12 fires in an hour, got %d", len(got))
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
	// NextRuns reports the DRIFTED time, because that is when the job really
	// fires — so assert the band around 9:00, not the exact minute.
	for _, r := range runs {
		scheduled := time.Date(r.Year(), r.Month(), r.Day(), 9, 0, 0, 0, loc)
		if d := r.Sub(scheduled); d < -jitterWindow || d > jitterWindow {
			t.Fatalf("expected a run within ±%v of 9:00, got %s", jitterWindow, r.Format("15:04"))
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

// A rollover job MUST be recognised as such: an old binary (or a parsing slip)
// treats it as an ordinary cron and texts the internal close-of-day prompt's
// output to the owner at 1am instead of rotating the session.
func TestLoadParsesRolloverFlag(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/crons.json"
	body := `{"timezone":"America/Mexico_City","crons":[
	  {"id":"gastos-hoy","schedule":"0 9 * * *","enabled":true,"prompt":"x"},
	  {"id":"cierre-del-dia","schedule":"0 1 * * *","enabled":true,"rollover":true,"prompt":"y"}
	]}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Crons) != 2 {
		t.Fatalf("want 2 crons, got %d", len(f.Crons))
	}
	if f.Crons[0].Rollover {
		t.Error("an ordinary cron must not be flagged as rollover")
	}
	if !f.Crons[1].Rollover {
		t.Error("cierre-del-dia should be flagged as rollover")
	}
}
