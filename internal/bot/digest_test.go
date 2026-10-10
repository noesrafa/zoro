package bot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"zoro/internal/claude"
	"zoro/internal/cron"
)

type sideCall struct {
	sid    string
	create bool
	prompt string
	opts   claude.RunOpts
}

// sideBot records every Claude call with its session, and has a soul.
func sideBot(t *testing.T) (*testBot, func() []sideCall) {
	t.Helper()
	tb := newTestBot(t, "zoro")
	tb.cfg.SoulFile = filepath.Join(t.TempDir(), "soul.md")
	if err := os.WriteFile(tb.cfg.SoulFile, []byte("I am Zoro."), 0o644); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var calls []sideCall
	tb.run = func(_ context.Context, sid string, create bool, prompt string, o claude.RunOpts) (claude.Result, error) {
		mu.Lock()
		calls = append(calls, sideCall{sid, create, prompt, o})
		mu.Unlock()
		tb.mu.Lock()
		tb.calls = append(tb.calls, prompt)
		tb.mu.Unlock()
		if tb.reply != nil {
			r, err := tb.reply(prompt)
			if r.SessionID == "" {
				r.SessionID = sid
			}
			return r, err
		}
		if strings.Contains(prompt, "Cron automático") {
			return claude.Result{Text: "Today the phone bill is due.", SessionID: sid}, nil
		}
		return claude.Result{Text: "ok", SessionID: sid}, nil
	}
	return tb, func() []sideCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]sideCall(nil), calls...)
	}
}

func (tb *testBot) fire(t *testing.T, j cron.Job) job {
	t.Helper()
	tb.fireCron(j)
	q := tb.takeJob(t)
	tb.runJob(q)
	return q
}

// A normal cron runs on a fresh session with the soul, never touches the main
// session, reaches the owner, and lands in the digest.
func TestSideCronRunsInThrowawaySession(t *testing.T) {
	tb, calls := sideBot(t)
	before := tb.store.Current()
	q := tb.fire(t, cron.Job{ID: "gastos-hoy", Prompt: "check payments"})
	if !q.isSide {
		t.Fatal("a cron without main:true must be a side job")
	}
	c := calls()
	if len(c) != 1 {
		t.Fatalf("one call, got %d", len(c))
	}
	if c[0].sid == before.SessionID || !c[0].create {
		t.Fatalf("side cron must create its own session, got sid=%s create=%v (main %s)", c[0].sid, c[0].create, before.SessionID)
	}
	if !strings.Contains(c[0].opts.SystemPrompt, "I am Zoro.") || c[0].opts.Bare {
		t.Fatalf("side cron keeps the soul and the full agent: %+v", c[0].opts)
	}
	if after := tb.store.Current(); after != before {
		t.Fatalf("the main session must not move: %+v → %+v", before, after)
	}
	if tb.sentWith("phone bill is due") != 1 {
		t.Fatalf("the reply must reach the owner: %q", tb.sent)
	}
	es := readDigest(tb.digestFile())
	if len(es) != 1 || es[0].Cron != "gastos-hoy" || !strings.Contains(es[0].Text, "phone bill") {
		t.Fatalf("digest: %+v", es)
	}
}

// The digest rides on the next main turn, once, and is gone after it.
func TestDigestRidesNextMainTurnAndClears(t *testing.T) {
	tb, calls := sideBot(t)
	tb.fire(t, cron.Job{ID: "gastos-hoy", Prompt: "check payments"})
	tb.runJob(ownerJob("and what about the gym today"))
	tb.runJob(ownerJob("thanks"))
	c := calls()
	if len(c) != 3 {
		t.Fatalf("3 calls, got %d", len(c))
	}
	main := tb.store.Current().SessionID
	if c[1].sid != main || c[2].sid != main {
		t.Fatal("owner turns run on the main session")
	}
	if !strings.Contains(c[1].prompt, "gastos-hoy: Today the phone bill is due.") ||
		!strings.Contains(c[1].prompt, "and what about the gym today") ||
		strings.Index(c[1].prompt, "gastos-hoy") > strings.Index(c[1].prompt, "and what about the gym") {
		t.Fatalf("the digest goes BEFORE the owner's message: %q", c[1].prompt)
	}
	if strings.Contains(c[2].prompt, "gastos-hoy") {
		t.Fatalf("the digest is delivered once: %q", c[2].prompt)
	}
	if _, err := os.Stat(tb.digestFile()); !os.IsNotExist(err) {
		t.Fatal("the digest file must be gone")
	}
	if strings.Contains(tb.sent[len(tb.sent)-1], "gastos-hoy") {
		t.Fatal("the digest is for the model, never sent to the chat")
	}
}

// The rollover stays in the main session (it must close THAT day) and carries the digest.
func TestRolloverStaysMainWithDigest(t *testing.T) {
	tb, calls := sideBot(t)
	tb.fire(t, cron.Job{ID: "gastos-hoy", Prompt: "check payments"})
	main := tb.store.Current().SessionID
	q := tb.fire(t, cron.Job{ID: "cierre-del-dia", Prompt: "close the day", Rollover: true})
	if q.isSide {
		t.Fatal("the rollover is never a side job")
	}
	c := calls()
	if c[1].sid != main || !strings.Contains(c[1].prompt, "gastos-hoy") {
		t.Fatalf("rollover: sid=%s (main %s) prompt=%q", c[1].sid, main, c[1].prompt)
	}
	if len(readDigest(tb.digestFile())) != 0 {
		t.Fatal("the rollover consumed the digest")
	}
}

// "main": true keeps a cron in the main conversation, as before; no digest.
func TestMainCronStaysMain(t *testing.T) {
	tb, calls := sideBot(t)
	main := tb.store.Current().SessionID
	q := tb.fire(t, cron.Job{ID: "revision-semanal", Prompt: "weekly review", Main: true})
	if q.isSide || calls()[0].sid != main {
		t.Fatal("main:true runs on the main session")
	}
	if len(readDigest(tb.digestFile())) != 0 {
		t.Fatal("a main cron needs no digest: it IS in the main session")
	}
}

// Despertares (one-shot wake-ups from detached jobs) also go to a side session.
func TestDespertarIsSide(t *testing.T) {
	tb, calls := sideBot(t)
	q := tb.fire(t, cron.Job{ID: "despertar-1760000000", Prompt: "job done"})
	if !q.isSide || calls()[0].sid == tb.store.Current().SessionID {
		t.Fatal("a despertar runs on a side session")
	}
}

// At most digestMax entries, each cut to digestMaxText runes.
func TestDigestCapped(t *testing.T) {
	tb, _ := sideBot(t)
	long := strings.Repeat("ñ", 2000)
	tb.reply = func(string) (claude.Result, error) { return claude.Result{Text: long}, nil }
	for i := 0; i < 7; i++ {
		tb.fire(t, cron.Job{ID: "c" + string(rune('0'+i)), Prompt: "p"})
	}
	es := readDigest(tb.digestFile())
	if len(es) != digestMax || es[0].Cron != "c2" || es[digestMax-1].Cron != "c6" {
		t.Fatalf("keeps the last %d: %d entries, first %q", digestMax, len(es), es[0].Cron)
	}
	if n := len([]rune(es[0].Text)); n > digestMaxText+1 {
		t.Fatalf("entry not cut: %d runes", n)
	}
}

// A failed main turn keeps the digest; a slash command never carries it.
func TestDigestKeptOnFailureAndSlash(t *testing.T) {
	tb, calls := sideBot(t)
	tb.fire(t, cron.Job{ID: "gastos-hoy", Prompt: "check payments"})
	tb.runJob(ownerJob("/deep-research gyms near me"))
	if c := calls(); strings.Contains(c[1].prompt, "gastos-hoy") || !strings.HasPrefix(c[1].prompt, "/deep-research") {
		t.Fatalf("a slash command must stay first and bare: %q", c[1].prompt)
	}
	tb.reply = func(string) (claude.Result, error) { return claude.Result{}, errors.New("boom") }
	tb.runJob(ownerJob("what is due this week"))
	if len(readDigest(tb.digestFile())) != 1 {
		t.Fatal("a failed turn must keep the digest for the next one")
	}
	tb.reply = nil
	tb.runJob(ownerJob("what is due this week"))
	if c := calls(); !strings.Contains(c[len(c)-1].prompt, "gastos-hoy") || len(readDigest(tb.digestFile())) != 0 {
		t.Fatal("the next good turn carries and clears it")
	}
}

// Paused: a side cron makes no call and adds nothing to the digest.
func TestPausedSideCronDropped(t *testing.T) {
	tb, calls := sideBot(t)
	pause(t, tb)
	tb.process(context.Background(), job{chatID: 1, prompt: "p", label: "⏰ cron", isSide: true, cronID: "x"})
	if len(calls()) != 0 || len(readDigest(tb.digestFile())) != 0 {
		t.Fatal("paused: no call, no digest")
	}
}

// A side cron that hits the limit pauses like any machine turn and is dropped.
func TestSideCronLimitPauses(t *testing.T) {
	tb, _ := sideBot(t)
	tb.reply = func(string) (claude.Result, error) {
		return claude.Result{Text: banner, IsError: true, APIError: "rate_limit"}, errors.New("exit status 1")
	}
	before := tb.store.Current()
	tb.fire(t, cron.Job{ID: "gastos-hoy", Prompt: "p"})
	if !tb.paused() {
		t.Fatal("the limit must pause")
	}
	if tb.store.Current() != before || len(readDigest(tb.digestFile())) != 0 {
		t.Fatal("no session change, no digest")
	}
}
