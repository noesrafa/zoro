package bot

// One real cron in a side session, with a test engine (temp state, fake
// Telegram) and the real CLI, soul, context.md and brief — never the service:
//
//	ZORO_SIDE_LIVE=gastos-hoy go test ./internal/bot -run TestSideCronLive -v
//
// Logs the input tokens the side session read (the main session re-read ~200K).

import (
	"context"
	"os"
	"strings"
	"testing"

	"zoro/internal/claude"
	"zoro/internal/cron"
)

func TestSideCronLive(t *testing.T) {
	id := os.Getenv("ZORO_SIDE_LIVE")
	if id == "" {
		t.Skip("set ZORO_SIDE_LIVE=<cron id> to run one real cron in a side session")
	}
	f, err := cron.Load("/home/rafael/.zoro/crons.json")
	if err != nil {
		t.Fatal(err)
	}
	var j cron.Job
	for _, c := range f.Crons {
		if c.ID == id {
			j = c
		}
	}
	if j.ID == "" || j.Rollover {
		t.Fatalf("no side cron %q", id)
	}
	tb := newTestBot(t, "zoro")
	tb.cfg.SoulFile = "/home/rafael/.zoro/soul.md"
	tb.cfg.ContextFile = "/home/rafael/.zoro/context.md"
	tb.cfg.BriefFile = "/home/rafael/zoro/state/brief.md"
	if err := tb.set.SetModel(os.Getenv("ZORO_SIDE_MODEL")); err != nil {
		t.Fatal(err)
	}
	if e := os.Getenv("ZORO_SIDE_EFFORT"); e != "" {
		_ = tb.set.SetEffort(e)
	}
	bin := os.Getenv("CLAUDE_BIN")
	if bin == "" {
		bin = "/home/rafael/.local/bin/claude"
	}
	driver := claude.New(claude.Config{Bin: bin, Model: "opus", WorkDir: "/home/rafael", DangerSkip: true})
	var res claude.Result
	tb.run = func(ctx context.Context, sid string, create bool, prompt string, o claude.RunOpts) (claude.Result, error) {
		r, err := driver.Run(ctx, sid, create, prompt, o)
		res = r
		return r, err
	}
	tb.fireCron(j)
	tb.runJob(tb.takeJob(t))
	t.Logf("session %s · input %d tokens (fresh %d, cache write %d, cache read %d) · output %d",
		res.SessionID, res.Usage.Input(), res.Usage.InputTokens, res.Usage.CacheCreationTokens, res.Usage.CacheReadTokens, res.Usage.OutputTokens)
	t.Logf("reply: %s", strings.TrimSpace(res.Text))
	t.Logf("digest: %+v", readDigest(tb.digestFile()))
	t.Logf("sent: %q", tb.sent)
}
