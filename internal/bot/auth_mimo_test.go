package bot

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"zoro/internal/claude"
	"zoro/internal/settings"
)

func TestAuthSwitchPersistsAndKeepsTheSession(t *testing.T) {
	tb := newTestBot(t, "zoro")
	if err := os.WriteFile(tb.cfg.MiMoKeyFile, []byte("tp-x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = tb.store.MarkCreated()
	sid := tb.store.Current().SessionID
	ctx := context.Background()

	tb.handleAuth(ctx, 1, "mimo")
	if tb.sentWith("🔀 Now on MiMo (mimo-v2.6-pro). /auth sub to go back.") != 1 {
		t.Fatalf("sent %q", tb.sent)
	}
	// Persisted like /model: a fresh store (= a restart) still says mimo.
	again, _ := settings.Open(tb.cfg.StateDir, settings.Settings{Model: "opus", Effort: "high"})
	if again.Get().Auth != claude.AuthMiMo {
		t.Fatal("/auth mimo must persist in settings.json")
	}
	// Next turn goes to MiMo, on the SAME session.
	var got claude.RunOpts
	var gotSID string
	tb.run = func(_ context.Context, s string, _ bool, _ string, o claude.RunOpts) (claude.Result, error) {
		got, gotSID = o, s
		return claude.Result{Text: "hi", SessionID: s}, nil
	}
	tb.process(ctx, ownerJob("hola"))
	if got.Auth != claude.AuthMiMo || gotSID != sid {
		t.Fatalf("turn ran with auth %q on %s, want mimo on %s", got.Auth, gotSID, sid)
	}
	if !strings.Contains(tb.backendLine(), "mimo") {
		t.Fatalf("backendLine = %q", tb.backendLine())
	}

	tb.handleAuth(ctx, 1, "sub")
	tb.process(ctx, ownerJob("hola"))
	if got.Auth != "" || gotSID != sid {
		t.Fatalf("after /auth sub: auth %q on %s", got.Auth, gotSID)
	}
}

func TestAuthMiMoRefusesWithoutKey(t *testing.T) {
	tb := newTestBot(t, "zoro")
	tb.handleAuth(context.Background(), 1, "mimo")
	if tb.set.Get().Auth != "" || tb.sentWith("MiMo key file missing") != 1 {
		t.Fatalf("no key file: must stay on sub, sent %q", tb.sent)
	}
}

// A MiMo error is ONE short line: no raw JSON, no pause, session kept.
func TestMiMoFailureIsOneLineAndKeepsSession(t *testing.T) {
	tb := newTestBot(t, "zoro")
	_ = tb.set.SetAuth(claude.AuthMiMo)
	_ = tb.store.MarkCreated()
	sid := tb.store.Current().SessionID
	raw := `API Error: 429 {"error":{"type":"rate_limit_error","message":"quota exceeded for tp-SECRET"}}`
	tb.reply = func(string) (claude.Result, error) {
		// Even the subscription banner on MiMo must not pause everyone.
		return claude.Result{Text: raw + "\n" + banner, IsError: true, APIError: "rate_limit"}, errors.New("claude exited: exit status 1: ")
	}
	tb.process(context.Background(), ownerJob("hola"))
	if tb.paused() {
		t.Fatal("MiMo errors must not pause")
	}
	if st := tb.store.Current(); st.SessionID != sid || tb.nCalls() != 1 {
		t.Fatalf("session must be kept without retries: %+v, %d calls", st, tb.nCalls())
	}
	if len(tb.sent) != 1 || !strings.HasPrefix(tb.sent[0], "🚦 MiMo quota") || strings.Contains(tb.sent[0], "{") {
		t.Fatalf("want one short line, sent %q", tb.sent)
	}
}
