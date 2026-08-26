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
//   - Every occurrence is drifted by a few deterministic minutes (see jitterWindow).
//     Several agents share one upstream account, and crons written by hand cluster on
//     round times, so without this they all wake on the same instant of the same
//     minute and hammer it together.
package cron

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"strconv"
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

	// Rollover marks the nightly close-the-day job: the bot runs Prompt as a normal
	// turn (the agent files away what matters and writes a brief), then saves that
	// brief and rotates to a FRESH session, so the next day starts with clean
	// context instead of an ever-growing transcript.
	Rollover bool `json:"rollover"`
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
	// Report the drifted times — these are when the job actually fires, and `cron
	// list` showing a clean "03:00" for something that runs at 02:57 is a lie that
	// costs an hour the next time a fire looks late.
	out := make([]time.Time, 0, n)
	t := time.Now().In(loc)
	for i := 0; i < n; i++ {
		t = sched.Next(t)
		out = append(out, t.Add(drift(j.ID, sched, t)))
	}
	return out, nil
}

// jitterWindow is how far, in each direction, an occurrence may drift from its
// scheduled minute. "0 1 * * *" on three agents means three simultaneous calls on
// one shared login; spreading them over an 11-minute band costs nothing and stops
// them colliding.
const jitterWindow = 5 * time.Minute

// minSpacing is the tightest schedule we are willing to drift. Occurrences closer
// together than this could, once drifted independently, overlap or swap order — so
// frequent schedules ("*/5 * * * *") are left exactly on their marks.
const minSpacing = 15 * time.Minute

// instanceSalt keeps the drift different between agents running this same engine.
// zoro, sky and expxxi each have a job whose ID is "cierre-del-dia" on "0 1 * * *";
// hashing only the ID and the occurrence would hand all three the SAME drift, so
// they would still fire on the same instant — the exact collision the jitter exists
// to break. The uid is stable per agent and needs no plumbing to reach here.
var instanceSalt = strconv.Itoa(os.Getuid())

// drift returns the offset applied to one occurrence of one job: a whole number of
// minutes in [-jitterWindow, +jitterWindow]. It is derived from the job ID and the
// occurrence itself, never from the current time, which is what makes it safe — the
// same occurrence always draws the same offset, so it lands in exactly one tick
// window and fires exactly once, and a restart cannot shift a pending fire.
func drift(id string, sched cronparser.Schedule, occ time.Time) time.Duration {
	if sched.Next(occ).Sub(occ) < minSpacing {
		return 0
	}
	h := fnv.New64a()
	io.WriteString(h, instanceSalt)
	io.WriteString(h, "/")
	io.WriteString(h, id)
	io.WriteString(h, "@")
	io.WriteString(h, occ.UTC().Format(time.RFC3339))
	steps := int64(jitterWindow/time.Minute)*2 + 1 // -5..+5 inclusive
	return time.Duration(int64(h.Sum64()%uint64(steps))-int64(jitterWindow/time.Minute)) * time.Minute
}

// due reports whether j should fire in the window (last, now] given its schedule.
func due(j Job, loc *time.Location, last, now time.Time) (bool, error) {
	sched, err := parser.Parse(j.Schedule)
	if err != nil {
		return false, err
	}
	// Scan every occurrence that could land in this window once drifted: a negative
	// drift pulls a later occurrence in, a positive one pushes an earlier occurrence
	// forward, so widen the search by jitterWindow on both sides. The windows the
	// caller passes tile the timeline without gaps or overlaps, and each occurrence
	// has one fixed drifted time, so this still fires exactly once per occurrence.
	lo, hi := last.In(loc), now.In(loc)
	for t := lo.Add(-jitterWindow); ; {
		occ := sched.Next(t)
		if occ.After(hi.Add(jitterWindow)) {
			return false, nil
		}
		if at := occ.Add(drift(j.ID, sched, occ)); at.After(lo) && !at.After(hi) {
			return true, nil
		}
		t = occ
	}
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
