package claude

// Smoke test of the engine's real invocation against the real CLI and login:
//
//	ZORO_SMOKE=1 go test ./internal/claude -run TestSmoke -v
//
// One new session per mode (connectors off, on, bare), "say ok", tokens logged.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"zoro/internal/uid"
)

func TestSmoke(t *testing.T) {
	if os.Getenv("ZORO_SMOKE") == "" {
		t.Skip("set ZORO_SMOKE=1 to call the real CLI")
	}
	bin := os.Getenv("CLAUDE_BIN")
	if bin == "" {
		bin = "/home/rafael/.local/bin/claude"
	}
	soul, _ := os.ReadFile("/home/rafael/.zoro/soul.md")
	d := New(Config{Bin: bin, Model: "opus", WorkDir: "/home/rafael", DangerSkip: true})
	for _, c := range []struct {
		name string
		o    RunOpts
	}{
		{"connectors off", RunOpts{Effort: "low", SystemPrompt: string(soul), NoConnectors: true}},
		{"connectors on", RunOpts{Effort: "low", SystemPrompt: string(soul)}},
		{"bare", RunOpts{Model: "sonnet", Effort: "low", SystemPrompt: "Reply with one word.", Bare: true}},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		res, err := d.Run(ctx, uid.New(), true, "say ok", c.o)
		cancel()
		if err != nil || !strings.Contains(strings.ToLower(res.Text), "ok") {
			t.Fatalf("%s: err=%v text=%q diag=%q", c.name, err, res.Text, res.Diagnostic())
		}
		t.Logf("%-15s session %s · input %d tokens · %q", c.name, res.SessionID, res.Usage.Input(), strings.TrimSpace(res.Text))
	}
}
