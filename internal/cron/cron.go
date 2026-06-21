// Package cron is a tiny, file-driven scheduler. It reads ~/.zoro/crons.json,
// and once a minute fires every enabled entry whose schedule fell due — in a
// fixed timezone (default America/Mexico_City). Firing just hands the entry's
// prompt to a callback; the bot runs it as a normal turn and replies to the owner.
//
// Design notes:
//   - The JSON is re-read every tick, so editing it takes effect within a minute
//     (hot-reload) — no restart needed.
//   - We use robfig/cron only as a battle-tested PARSER (standard 5-field specs);
//     the loop itself is a plain per-minute tick, which keeps reload trivial.
package cron

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	cronparser "github.com/robfig/cron/v3"
)

// DefaultTimezone — rafiña vive en CDMX; todos los crons corren aquí salvo override.
const DefaultTimezone = "America/Mexico_City"

// Job is one scheduled message. Schedule is a standard 5-field cron spec
// (min hour dom mon dow), e.g. "0 9 * * *" = 9:00 every day.
type Job struct {
	ID       string `json:"id"`
	Schedule string `json:"schedule"`
	Prompt   string `json:"prompt"`
	Enabled  bool   `json:"enabled"`
}

// File is the on-disk shape of crons.json.
type File struct {
	Timezone string `json:"timezone"`
	Crons    []Job  `json:"crons"`
}

// 5-field standard parser (no seconds), matching crontab syntax.
var parser = cronparser.NewParser(
	cronparser.Minute | cronparser.Hour | cronparser.Dom | cronparser.Month | cronparser.Dow,
)

// Load reads and parses crons.json. A missing file is not an error — it returns
// an empty File so the scheduler simply has nothing to do until one appears.
func Load(path string) (File, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return File{Timezone: DefaultTimezone}, nil
	}
	if err != nil {
		return File{}, err
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return File{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if f.Timezone == "" {
		f.Timezone = DefaultTimezone
	}
	return f, nil
}

// Location resolves the file's timezone, falling back to CDMX then UTC.
func (f File) Location() *time.Location {
	if loc, err := time.LoadLocation(f.Timezone); err == nil && loc != nil {
		return loc
	}
	if loc, err := time.LoadLocation(DefaultTimezone); err == nil && loc != nil {
		return loc
	}
	return time.UTC
}

// NextRuns returns the next n fire times for a job, in loc. Used by `cron list`.
func NextRuns(j Job, loc *time.Location, n int) ([]time.Time, error) {
	sched, err := parser.Parse(j.Schedule)
	if err != nil {
		return nil, err
	}
	out := make([]time.Time, 0, n)
	t := time.Now().In(loc)
	for i := 0; i < n; i++ {
		t = sched.Next(t)
		out = append(out, t)
	}
	return out, nil
}

// due reports whether j should fire in the window (last, now] given its schedule.
func due(j Job, loc *time.Location, last, now time.Time) (bool, error) {
	sched, err := parser.Parse(j.Schedule)
	if err != nil {
		return false, err
	}
	next := sched.Next(last.In(loc))
	return !next.After(now.In(loc)), nil
}

// Logger is the minimal logging surface the scheduler needs.
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}

// Run drives the scheduler until ctx is cancelled. Every minute it reloads
// `path` and calls fire(j) for each enabled, due job. fire must not block (the
// bot's fireCron just enqueues a job).
func Run(ctx context.Context, path string, fire func(Job), log Logger) {
	// Align the first tick to the next minute boundary so fires land near :00.
	now := time.Now()
	first := now.Truncate(time.Minute).Add(time.Minute)
	timer := time.NewTimer(time.Until(first))
	defer timer.Stop()

	last := now
	for {
		select {
		case <-ctx.Done():
			return
		case tick := <-timer.C:
			timer.Reset(time.Until(tick.Truncate(time.Minute).Add(time.Minute)))
			f, err := Load(path)
			if err != nil {
				log.Warn("cron: load failed", "err", err)
				last = tick
				continue
			}
			loc := f.Location()
			for _, j := range f.Crons {
				if !j.Enabled {
					continue
				}
				ok, derr := due(j, loc, last, tick)
				if derr != nil {
					log.Warn("cron: bad schedule", "id", j.ID, "schedule", j.Schedule, "err", derr)
					continue
				}
				if ok {
					log.Info("cron: firing", "id", j.ID)
					fire(j)
				}
			}
			last = tick
		}
	}
}
