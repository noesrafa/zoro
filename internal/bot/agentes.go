package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"zoro/internal/config"
)

// /agentes (Zoro only, 11-oct-2026, the move to one user): one line per agent —
// unit up or down, model/effort, pause, saved messages, last activity. Everything
// is read from the agents' own state dirs and `systemctl is-active` (no sudo, no
// Claude call), so it answers even when the agents are dead.

type agentLine struct {
	Name, Unit, Active, Model, Effort string
	Paused                            *pauseFlag
	Pending                           int
	LastTurn                          time.Time
}

func (b *Bot) agentesText(ctx context.Context) string {
	agents := b.cfg.AllAgents()
	if len(agents) == 0 {
		return "🤖 No tengo sub-agentes configurados (ZORO_AGENTS / ZORO_AGENTS_DIR)."
	}
	var lines []string
	for _, a := range agents {
		lines = append(lines, formatAgentLine(b.agentLineFor(ctx, a), time.Now()))
	}
	return "🤖 Agentes\n" + strings.Join(lines, "\n") +
		"\n\n/stop y /start pausan a todos · zoro-agente estado <nombre> en la VPS"
}

func (b *Bot) agentLineFor(ctx context.Context, a config.Agent) agentLine {
	l := agentLine{Name: a.Name, Unit: a.Unit}
	if l.Unit == "" {
		l.Unit = a.Name
	}
	l.Active = unitActive(ctx, l.Unit)
	if raw, ok := b.readAgentFile(ctx, filepath.Join(a.StateDir, "settings.json")); ok {
		var s struct{ Model, Effort string }
		if json.Unmarshal(raw, &s) == nil {
			l.Model, l.Effort = s.Model, s.Effort
		}
	}
	if f, ok := b.agentFlag(ctx, a); ok {
		l.Paused = &f
	}
	l.Pending = b.agentPending(ctx, a)
	if raw, ok := b.readAgentFile(ctx, filepath.Join(a.StateDir, "state.json")); ok {
		var s struct {
			UpdatedAt time.Time `json:"updated_at"`
		}
		if json.Unmarshal(raw, &s) == nil {
			l.LastTurn = s.UpdatedAt
		}
	}
	return l
}

func unitActive(ctx context.Context, unit string) string {
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(c, "systemctl", "is-active", unit).Output()
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "unknown"
	}
	return s
}

func formatAgentLine(l agentLine, now time.Time) string {
	icon := "🟢"
	switch {
	case l.Active != "active":
		icon = "🔴"
	case l.Paused != nil:
		icon = "⏸️"
	}
	parts := []string{fmt.Sprintf("%s %s — %s %s", icon, l.Name, l.Unit, l.Active)}
	if l.Model != "" {
		m := strings.TrimPrefix(l.Model, "claude-")
		if l.Effort != "" {
			m += "/" + l.Effort
		}
		parts = append(parts, m)
	}
	if l.Paused != nil {
		p := "en pausa (" + l.Paused.Reason
		if l.Paused.Resets != "" {
			p += ", vuelve " + l.Paused.Resets
		}
		parts = append(parts, p+")")
	}
	if l.Pending > 0 {
		parts = append(parts, fmt.Sprintf("%d mensaje(s) guardado(s)", l.Pending))
	}
	if !l.LastTurn.IsZero() {
		parts = append(parts, "último turno hace "+ago(now.Sub(l.LastTurn)))
	}
	return strings.Join(parts, " · ")
}

func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1 min"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
	return fmt.Sprintf("%d días", int(d.Hours()/24))
}
