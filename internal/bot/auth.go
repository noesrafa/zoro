package bot

// /auth — switch Zoro between the Claude subscription and rafiña's Xiaomi MiMo
// Token Plan (pedido 28-sep-2026, so a spent weekly limit doesn't mean no Zoro).
// Persisted in settings.json like /model, applied on the next turn, no restart.
// Sub-agents stay on the subscription for now.
//
// ONE SESSION PER BACKEND (28-sep-2026). Resuming the subscription's session on
// MiMo auto-compacted 270k tokens of Opus transcript down to 5k (3.5 min wait,
// the detail gone for good once back on Opus) and shipped the whole history —
// secrets included — to Xiaomi. So a switch parks the current session in
// state.json and brings back the target backend's own one:
//
//   - /auth mimo: the subscription session is parked intact (id + created); MiMo
//     resumes ITS parked session, or starts a fresh one primed like any new
//     session (soul + context.md + brief) — never anything of the Opus transcript.
//   - /auth sub: MiMo's session is parked and the subscription's comes back
//     EXACTLY where it was (same id, not compacted).
//   - Nightly rollover: fresh session on the active backend AND the parked one
//     is dropped — the brief carries the continuity (session.Store.Rollover).
//   - /newsession and /focus: rotate only the ACTIVE backend's session; the
//     parked one waits untouched for its /auth.
//   - Restart: both survive (state.json), except a focused one (focus never
//     survives a restart).
//   - Every turn re-checks that its session belongs to its backend
//     (session.Store.Use in process), so no path resumes the other backend's
//     session by accident.

import (
	"context"
	"os"
	"strings"

	"zoro/internal/claude"
	"zoro/internal/session"
)

func (b *Bot) onMiMo() bool { return b.set.Get().Auth == claude.AuthMiMo }

// backendLine names the active backend for /auth, /uso and /status.
func (b *Bot) backendLine() string {
	if b.onMiMo() {
		return "backend: mimo — MiMo Token Plan (" + b.cfg.MiMoModel + ")"
	}
	return "backend: sub — Claude subscription (" + b.set.Get().Model + ")"
}

// parkedNote is the /status line for the other backend's saved session ("" if none).
func (b *Bot) parkedNote() string {
	other, name := claude.AuthMiMo, "mimo"
	if b.onMiMo() {
		other, name = "", "sub"
	}
	p, ok := b.store.Parked(other)
	if !ok {
		return ""
	}
	return "\nparked: " + name + " session " + shortID(p.SessionID) + " (resumes on /auth " + name + ")"
}

// subName is how /auth calls the subscription session: "Opus" for the opus alias.
func (b *Bot) subName() string {
	m := b.set.Get().Model
	if m == "" || strings.ContainsAny(m, "-.[") {
		return "la suscripción (" + m + ")"
	}
	return strings.ToUpper(m[:1]) + m[1:]
}

func (b *Bot) handleAuth(ctx context.Context, chatID int64, arg string) {
	var target string
	switch arg {
	case "":
		b.send(ctx, chatID, "🔑 "+b.backendLine()+b.parkedNote()+"\nUsage: /auth sub | /auth mimo")
		return
	case "mimo":
		if _, err := os.Stat(b.cfg.MiMoKeyFile); err != nil {
			b.send(ctx, chatID, "⚠️ MiMo key file missing: "+b.cfg.MiMoKeyFile+" — staying on the subscription.")
			return
		}
		target = claude.AuthMiMo
	case "sub", "claude", "subscription":
		target = ""
	default:
		b.send(ctx, chatID, "⚠️ Usage: /auth sub | /auth mimo")
		return
	}

	// The switch swaps sessions: never under a running turn (see Bot.turnMu).
	if b.busy.Load() || !b.turnMu.TryLock() {
		b.send(ctx, chatID, "⏳ Hay un turno corriendo. Usa /cancel o espera a que termine, luego /auth.")
		return
	}
	defer b.turnMu.Unlock()

	prev := b.set.Get().Auth
	// Session first, then the backend: if the settings write fails the session
	// goes back, and if we die in between, the next turn's Use re-pairs them.
	st, sw, err := b.store.Use(target)
	if err != nil {
		b.send(ctx, chatID, "⚠️ "+err.Error())
		return
	}
	if err := b.set.SetAuth(target); err != nil {
		_, _, _ = b.store.Use(prev)
		b.send(ctx, chatID, "⚠️ "+err.Error())
		return
	}
	b.send(ctx, chatID, b.authSwitchText(target, st, sw))
}

// authSwitchText tells rafiña which session is live after /auth.
func (b *Bot) authSwitchText(target string, st session.State, sw session.Switch) string {
	id := " (" + shortID(st.SessionID) + ")"
	if target == claude.AuthMiMo {
		head := "🔀 MiMo (" + b.cfg.MiMoModel + ") — "
		switch {
		case !sw.Changed:
			return head + "ya estabas aquí, misma sesión" + id + "."
		case sw.Resumed:
			return head + "de vuelta a tu sesión de MiMo" + id + ". Tu sesión de " + b.subName() + " quedó guardada intacta: /auth sub para volver a ella."
		}
		return head + "sesión nueva en MiMo" + id + ", sin nada del historial de " + b.subName() + ". Esa quedó guardada intacta: /auth sub para volver a ella."
	}
	head := "🔀 Suscripción (" + b.set.Get().Model + ") — "
	switch {
	case !sw.Changed:
		return head + "ya estabas aquí, misma sesión" + id + "."
	case sw.Resumed:
		return head + "de vuelta a tu sesión de " + b.subName() + id + ", exactamente donde se quedó. La de MiMo quedó guardada: /auth mimo para volver a ella."
	}
	return head + "sesión nueva" + id + " (no había una de " + b.subName() + " guardada — el cierre nocturno la cierra). La de MiMo quedó guardada: /auth mimo para volver a ella."
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
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
