package bot

// /uso — surfaces the OFFICIAL Claude Code rate-limit usage (5h / 7d windows).
//
// We do NOT compute or approximate anything: the numbers come straight from the
// claude-hud statusline plugin, which reads Claude Code's native `rate_limits`
// stdin payload and atomically writes a snapshot to ~/.claude/zoro-usage.json
// (configured via display.externalUsageWritePath). This command just reads and
// formats that snapshot. It is therefore only as fresh as the last interactive
// Claude Code render on the VPS (e.g. a code-server session) — the footer shows
// the snapshot age so staleness is always visible.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const usageSnapshotPath = ".claude/zoro-usage.json"

type usageWindow struct {
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       *string  `json:"resets_at"`
}

type usageSnapshot struct {
	UpdatedAt    string      `json:"updated_at"`
	FiveHour     usageWindow `json:"five_hour"`
	SevenDay     usageWindow `json:"seven_day"`
	BalanceLabel string      `json:"balance_label"`
}

// usoText reads the claude-hud snapshot and renders it for Telegram.
func usoText() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/home/rafael"
	}
	path := filepath.Join(home, usageSnapshotPath)

	raw, err := os.ReadFile(path)
	if err != nil {
		return "⚪ Aún no hay snapshot de uso.\n" +
			"Abre Claude Code en code-server (code.soyrafa.dev) una vez — el HUD genera " +
			"`~/.claude/zoro-usage.json` y a partir de ahí /uso lo lee."
	}

	var s usageSnapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return "⚠️ No pude leer el snapshot de uso: " + err.Error()
	}

	now := time.Now()
	lines := []string{
		"⚔️ Uso de Claude Code",
		"",
		renderUsageWindow("5h", s.FiveHour, now),
		renderUsageWindow("7d", s.SevenDay, now),
	}
	if s.BalanceLabel != "" {
		lines = append(lines, "", "balance: "+s.BalanceLabel)
	}

	foot := "vía claude-hud"
	if t, err := time.Parse(time.RFC3339, s.UpdatedAt); err == nil {
		age := now.Sub(t)
		if age < time.Minute {
			foot = "actualizado hace un momento · " + foot
		} else {
			foot = "actualizado hace " + humanizeDur(age) + " · " + foot
		}
		if age > time.Hour {
			lines = append(lines, "",
				"⚠️ snapshot viejo — corre Claude Code en la VPS para refrescar el número "+
					"(los límites son de tu cuenta completa, pero este número solo se actualiza "+
					"cuando codeas en la VPS).")
		}
	}
	lines = append(lines, "", foot)
	return strings.Join(lines, "\n")
}

func renderUsageWindow(label string, w usageWindow, now time.Time) string {
	if w.UsedPercentage == nil {
		return "⚪ " + label + ": s/d (Claude Code no mandó rate_limits)"
	}
	p := *w.UsedPercentage
	line := fmt.Sprintf("%s %s: %3.0f%%", usageEmoji(p), label, p)
	if w.ResetsAt != nil {
		if t, err := time.Parse(time.RFC3339, *w.ResetsAt); err == nil {
			line += " · resets en " + humanizeDur(t.Sub(now))
		}
	}
	return line
}

func usageEmoji(p float64) string {
	switch {
	case p >= 90:
		return "🔴"
	case p >= 70:
		return "🟡"
	default:
		return "🟢"
	}
}

// humanizeDur formats a positive-ish duration compactly: "4d 18h", "3h 34m", "12m".
func humanizeDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd %dh", int(d/(24*time.Hour)), int((d%(24*time.Hour))/time.Hour))
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d/time.Hour), int((d%time.Hour)/time.Minute))
	default:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
}
