package bot

// /auth — switch Zoro between the Claude subscription and rafiña's Xiaomi MiMo
// Token Plan (pedido 28-sep-2026, so a spent weekly limit doesn't mean no Zoro).
// Persisted in settings.json like /model, applied on the next turn, no restart.
// The session is NEVER rotated: a transcript born on one backend resumes on the
// other (measured both ways). Sub-agents stay on the subscription for now.

import (
	"context"
	"os"
	"strings"

	"zoro/internal/claude"
)

func (b *Bot) onMiMo() bool { return b.set.Get().Auth == claude.AuthMiMo }

// backendLine names the active backend for /auth, /uso and /status.
func (b *Bot) backendLine() string {
	if b.onMiMo() {
		return "backend: mimo — MiMo Token Plan (" + b.cfg.MiMoModel + ")"
	}
	return "backend: sub — Claude subscription (" + b.set.Get().Model + ")"
}

func (b *Bot) handleAuth(ctx context.Context, chatID int64, arg string) {
	switch arg {
	case "":
		b.send(ctx, chatID, "🔑 "+b.backendLine()+"\nUsage: /auth sub | /auth mimo")
	case "mimo":
		if _, err := os.Stat(b.cfg.MiMoKeyFile); err != nil {
			b.send(ctx, chatID, "⚠️ MiMo key file missing: "+b.cfg.MiMoKeyFile+" — staying on the subscription.")
			return
		}
		if err := b.set.SetAuth(claude.AuthMiMo); err != nil {
			b.send(ctx, chatID, "⚠️ "+err.Error())
			return
		}
		b.send(ctx, chatID, "🔀 Now on MiMo ("+b.cfg.MiMoModel+"). /auth sub to go back.")
	case "sub", "claude", "subscription":
		if err := b.set.SetAuth(""); err != nil {
			b.send(ctx, chatID, "⚠️ "+err.Error())
			return
		}
		b.send(ctx, chatID, "🔀 Back on the Claude subscription ("+b.set.Get().Model+"). /auth mimo to switch.")
	default:
		b.send(ctx, chatID, "⚠️ Usage: /auth sub | /auth mimo")
	}
}

// mimoFailText is the ONE short line a failed MiMo turn gets. The CLI's raw
// output (JSON bodies and all) goes to the log only.
func mimoFailText(res claude.Result, err error) string {
	if strings.HasPrefix(err.Error(), "mimo key file") {
		return "⚠️ " + err.Error() // our own error: names the file, never the key
	}
	d := strings.ToLower(res.Diagnostic() + " " + err.Error())
	switch {
	case strings.Contains(d, "401"), strings.Contains(d, "403"),
		strings.Contains(d, "authenticat"), strings.Contains(d, "invalid api key"):
		return "🔑 MiMo rejected the key. Session kept — /auth sub to go back."
	case strings.Contains(d, "429"), strings.Contains(d, "quota"), strings.Contains(d, "limit"),
		strings.Contains(d, "insufficient"), strings.Contains(d, "balance"):
		return "🚦 MiMo quota / rate limit hit. Session kept — retry later or /auth sub to go back."
	}
	return "⚠️ MiMo turn failed. Session kept — try again, or /auth sub to go back."
}

// usoHeader goes on top of /uso: which backend answers, and whether we're paused.
func (b *Bot) usoHeader() string {
	h := "🔑 " + b.backendLine() + "\n"
	if p := b.pauseLine(); p != "" {
		h += p + "\n"
	}
	return h + "\n"
}
