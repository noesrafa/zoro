package bot

// Auto-resume (rafiña, 11-oct-2026 23:3x: «I want to quit the auto stop… I don't want you
// stop»). A LIMIT pause no longer waits for /start: once the reset time the CLI announced
// has passed (+2 min), or every 30 min when it can't be parsed, the flag is removed and the
// next turn simply tries again. Still limited → onLimit pauses again, quietly (at most one
// message to rafiña every 12 h). Every agent runs this same loop on its own flag, so the
// ones Zoro paused come back by themselves too. A /stop pause (reasonStop) is never touched.

import (
	"context"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	autoRetryEvery = 30 * time.Minute
	limitNoticeGap = 12 * time.Hour
)

var (
	resetsClockRe = regexp.MustCompile(`(?i)(?:\b([a-z]{3})[a-z]*\.?\s+(\d{1,2}),?\s+(?:at\s+)?)?\b(\d{1,2})(?::(\d{2}))?\s*([ap])\.?m\b`)
	resetsTZRe    = regexp.MustCompile(`\(([A-Za-z_]+/[A-Za-z_/+-]+)\)`)
)

// retryAt is when a limit pause stops being worth keeping: the announced reset (+2 min)
// or, if the text can't be read, 30 min after the pause began.
func retryAt(f pauseFlag) time.Time {
	since, err := time.Parse(time.RFC3339, f.Since)
	if err != nil {
		since = time.Now()
	}
	fallback := since.Add(autoRetryEvery)
	m := resetsClockRe.FindStringSubmatch(f.Resets)
	if m == nil {
		return fallback
	}
	loc := time.Local
	if t := resetsTZRe.FindStringSubmatch(f.Resets); t != nil {
		if l, err := time.LoadLocation(t[1]); err == nil {
			loc = l
		}
	}
	h, _ := strconv.Atoi(m[3])
	mi := 0
	if m[4] != "" {
		mi, _ = strconv.Atoi(m[4])
	}
	if h < 1 || h > 12 || mi > 59 {
		return fallback
	}
	if h == 12 {
		h = 0
	}
	if strings.EqualFold(m[5], "p") {
		h += 12
	}
	s := since.In(loc)
	var at time.Time
	if m[1] != "" {
		mon, err := time.Parse("Jan", strings.ToUpper(m[1][:1])+strings.ToLower(m[1][1:]))
		day, _ := strconv.Atoi(m[2])
		if err != nil || day < 1 || day > 31 {
			return fallback
		}
		at = time.Date(s.Year(), mon.Month(), day, h, mi, 0, 0, loc)
		if at.Before(s.Add(-24 * time.Hour)) { // "Jan 2" announced in late December
			at = at.AddDate(1, 0, 0)
		}
	} else {
		at = time.Date(s.Year(), s.Month(), s.Day(), h, mi, 0, 0, loc)
		if !at.After(s) {
			at = at.Add(24 * time.Hour)
		}
	}
	at = at.Add(2 * time.Minute)
	if at.Sub(s) > 8*24*time.Hour {
		return fallback
	}
	return at
}

// maybeAutoUnpause lifts this engine's own LIMIT pause once its retry time has come.
func (b *Bot) maybeAutoUnpause(ctx context.Context) {
	f, ok := readPause(b.pauseFile())
	if !ok || f.Reason != reasonLimit || time.Now().Before(retryAt(f)) {
		return
	}
	if err := os.Remove(b.pauseFile()); err != nil && !os.IsNotExist(err) {
		b.log.Warn("could not lift the limit pause", "err", err)
		return
	}
	b.log.Info("limit pause expired — trying again on our own", "resets", f.Resets, "since", f.Since)
}

// limitNoticeDue says whether rafiña should hear about this limit (once per 12 h): the
// auto-retries of the same limit stay silent.
func (b *Bot) limitNoticeDue() bool {
	b.ackMu.Lock()
	defer b.ackMu.Unlock()
	if !b.lastLimitNotice.IsZero() && time.Since(b.lastLimitNotice) < limitNoticeGap {
		return false
	}
	b.lastLimitNotice = time.Now()
	return true
}
