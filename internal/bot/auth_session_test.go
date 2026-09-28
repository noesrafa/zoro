package bot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"zoro/internal/claude"
	"zoro/internal/session"
	"zoro/internal/settings"
)

// call is one Claude run as the stand-in saw it.
type call struct {
	sid    string
	create bool
	auth   string
	prompt string
}

// sessionBot is a Zoro test bot with a MiMo key, a context.md to prime new
// sessions with, and a Claude stand-in that records every call.
func sessionBot(t *testing.T) (*testBot, *[]call) {
	t.Helper()
	tb := newTestBot(t, "zoro")
	dir := filepath.Dir(tb.cfg.StateDir)
	if err := os.WriteFile(tb.cfg.MiMoKeyFile, []byte("tp-x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tb.cfg.ContextFile = filepath.Join(dir, "context.md")
	if err := os.WriteFile(tb.cfg.ContextFile, []byte("CONTEXTO-DURABLE"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := &[]call{}
	tb.run = func(_ context.Context, sid string, create bool, prompt string, o claude.RunOpts) (claude.Result, error) {
		*calls = append(*calls, call{sid, create, o.Auth, prompt})
		return claude.Result{Text: "ok", SessionID: sid}, nil
	}
	return tb, calls
}

func last(c *[]call) call { return (*c)[len(*c)-1] }

// The measured bug (28-sep): /auth mimo resumed the 270k Opus session on MiMo.
// Now sub → mimo → sub comes back to the SAME subscription session, and no
// MiMo turn ever sees its id.
func TestAuthOneSessionPerBackend(t *testing.T) {
	tb, calls := sessionBot(t)
	ctx := context.Background()

	tb.process(ctx, ownerJob("hola opus"))
	sub := last(calls)
	if sub.auth != "" || !sub.create || !strings.Contains(sub.prompt, "CONTEXTO-DURABLE") {
		t.Fatalf("first sub turn: %+v", sub)
	}
	tb.process(ctx, ownerJob("sigo en opus"))
	if c := last(calls); c.sid != sub.sid || c.create {
		t.Fatalf("second sub turn must resume: %+v", c)
	}

	tb.handleAuth(ctx, 1, "mimo")
	if tb.sentWith("sesión nueva en MiMo") != 1 {
		t.Fatalf("sent %q", tb.sent)
	}
	tb.process(ctx, ownerJob("hola mimo"))
	m := last(calls)
	if m.auth != claude.AuthMiMo || m.sid == sub.sid || !m.create {
		t.Fatalf("MiMo must CREATE its own session, got %+v (sub %s)", m, sub.sid)
	}
	// Primed like any new session: soul (system prompt) + context, nothing of Opus.
	if !strings.Contains(m.prompt, "CONTEXTO-DURABLE") || strings.Contains(m.prompt, "hola opus") {
		t.Fatalf("MiMo prime: %q", m.prompt)
	}
	tb.process(ctx, ownerJob("sigo en mimo"))
	if c := last(calls); c.sid != m.sid || c.create {
		t.Fatalf("second MiMo turn must resume MiMo's session: %+v", c)
	}

	tb.handleAuth(ctx, 1, "sub")
	if tb.sentWith("de vuelta a tu sesión de Opus") != 1 {
		t.Fatalf("sent %q", tb.sent)
	}
	tb.process(ctx, ownerJob("de vuelta"))
	if c := last(calls); c.auth != "" || c.sid != sub.sid || c.create || strings.Contains(c.prompt, "CONTEXTO-DURABLE") {
		t.Fatalf("/auth sub must resume the SAME sub session, unprimed: %+v (want %s)", c, sub.sid)
	}

	tb.handleAuth(ctx, 1, "mimo")
	if tb.sentWith("de vuelta a tu sesión de MiMo") != 1 {
		t.Fatalf("sent %q", tb.sent)
	}
	tb.process(ctx, ownerJob("otra vez mimo"))
	if c := last(calls); c.sid != m.sid || c.create {
		t.Fatalf("second /auth mimo must resume MiMo's session: %+v", c)
	}

	for _, c := range *calls {
		if c.auth == claude.AuthMiMo && c.sid == sub.sid {
			t.Fatalf("MiMo got the subscription session id: %+v", c)
		}
		if c.auth == "" && c.sid != sub.sid {
			t.Fatalf("the subscription ran on a foreign session: %+v", c)
		}
	}
}

// A restart in the middle (redeploy while on MiMo) keeps both sessions.
func TestAuthSessionsSurviveRestart(t *testing.T) {
	tb, calls := sessionBot(t)
	ctx := context.Background()
	tb.process(ctx, ownerJob("hola opus"))
	sub := last(calls).sid
	tb.handleAuth(ctx, 1, "mimo")
	tb.process(ctx, ownerJob("hola mimo"))
	mimo := last(calls).sid

	// restart: fresh stores over the same state dir
	var err error
	if tb.store, err = session.Open(tb.cfg.StateDir); err != nil {
		t.Fatal(err)
	}
	if tb.set, err = settings.Open(tb.cfg.StateDir, settings.Settings{Model: "opus", Effort: "high"}); err != nil {
		t.Fatal(err)
	}

	tb.process(ctx, ownerJob("sigo en mimo"))
	if c := last(calls); c.auth != claude.AuthMiMo || c.sid != mimo || c.create {
		t.Fatalf("after restart MiMo must resume its session: %+v (want %s)", c, mimo)
	}
	tb.handleAuth(ctx, 1, "sub")
	tb.process(ctx, ownerJob("de vuelta"))
	if c := last(calls); c.auth != "" || c.sid != sub || c.create {
		t.Fatalf("after restart /auth sub must resume the same sub session: %+v (want %s)", c, sub)
	}
}

// Nightly rollover while on MiMo: fresh MiMo session and the parked sub one is
// gone — /auth sub the next morning starts clean (with the brief) instead of
// resuming yesterday's transcript.
func TestRolloverDropsTheOtherBackendSession(t *testing.T) {
	tb, calls := sessionBot(t)
	ctx := context.Background()
	tb.process(ctx, ownerJob("hola opus"))
	sub := last(calls).sid
	tb.handleAuth(ctx, 1, "mimo")
	tb.process(ctx, ownerJob("hola mimo"))
	mimo := last(calls).sid

	tb.finishRollover(ctx, job{chatID: 1}, "brief del día")
	if st := tb.store.Current(); st.SessionID == mimo || st.Created || st.Backend != claude.AuthMiMo {
		t.Fatalf("rollover must rotate MiMo's session: %+v", st)
	}
	if _, ok := tb.store.Parked(""); ok {
		t.Fatal("rollover must drop the parked sub session")
	}
	tb.handleAuth(ctx, 1, "sub")
	tb.process(ctx, ownerJob("buenos días"))
	c := last(calls)
	if c.sid == sub || !c.create || !strings.Contains(c.prompt, "brief del día") {
		t.Fatalf("after rollover /auth sub must start fresh with the brief: %+v", c)
	}
}

// The backend moved without /auth (settings.json edited by hand, a crash
// between the two writes): the turn still never resumes the other backend's
// session.
func TestTurnNeverResumesTheOtherBackendsSession(t *testing.T) {
	tb, calls := sessionBot(t)
	ctx := context.Background()
	tb.process(ctx, ownerJob("hola opus"))
	sub := last(calls).sid

	_ = tb.set.SetAuth(claude.AuthMiMo) // behind /auth's back
	tb.process(ctx, ownerJob("hola"))
	if c := last(calls); c.auth != claude.AuthMiMo || c.sid == sub || !c.create {
		t.Fatalf("MiMo turn on the sub session: %+v", c)
	}
	_ = tb.set.SetAuth("")
	tb.process(ctx, ownerJob("hola"))
	if c := last(calls); c.sid != sub || c.create {
		t.Fatalf("sub session not restored: %+v (want %s)", c, sub)
	}
}

// /auth never swaps sessions under a running turn.
func TestAuthRefusedWhileATurnRuns(t *testing.T) {
	tb, _ := sessionBot(t)
	sid := tb.store.Current().SessionID
	tb.busy.Store(true)
	tb.handleAuth(context.Background(), 1, "mimo")
	tb.busy.Store(false)
	if tb.set.Get().Auth != "" || tb.store.Current().SessionID != sid || tb.sentWith("Hay un turno corriendo") != 1 {
		t.Fatalf("switched under a running turn: auth %q, sent %q", tb.set.Get().Auth, tb.sent)
	}
	tb.turnMu.Lock()
	tb.handleAuth(context.Background(), 1, "mimo")
	tb.turnMu.Unlock()
	if tb.set.Get().Auth != "" || tb.sentWith("Hay un turno corriendo") != 2 {
		t.Fatalf("switched while the turn lock was held: auth %q", tb.set.Get().Auth)
	}
}
