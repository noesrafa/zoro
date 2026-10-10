package bot

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Direct line from Zoro to a sub-agent (rafiña, 10-oct-2026: "send him now man,
// we need to improve the connection" — a one-shot cron waits ≥6 min because of
// the ±5 min jitter). Zoro drops a text file in <StateDir>/de-zoro/ (with
// ~/.zoro/mandar.sh); within seconds it becomes a turn in the agent's MAIN
// session, like a "main": true cron, and the file is removed. Files are taken in
// name order (mandar.sh names them by timestamp).
const deZoroEvery = 2 * time.Second

func (b *Bot) deZoroDir() string { return filepath.Join(b.cfg.StateDir, "de-zoro") }

func (b *Bot) deZoroLoop(ctx context.Context) {
	t := time.NewTicker(deZoroEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.deZoroTick()
		}
	}
}

func (b *Bot) deZoroTick() {
	for _, text := range takeDeZoro(b.deZoroDir()) {
		b.log.Info("de-zoro: message from Zoro", "chars", len(text))
		b.enqueue(job{
			chatID: b.cfg.OwnerID,
			prompt: "[📨 Mensaje directo de Zoro (el agente de rafiña). No es de tu dueña/dueño: atiéndelo y, si te pide avisarle algo, avísale en tu respuesta.]\n\n" + text,
			label:  "📨 de Zoro",
		})
	}
}

// takeDeZoro reads and removes every finished message (*.txt; mandar.sh writes
// .tmp then renames) in name order. A file it can't remove is skipped, so a
// message is never delivered twice.
func takeDeZoro(dir string) []string {
	names, _ := filepath.Glob(filepath.Join(dir, "*.txt"))
	sort.Strings(names)
	var out []string
	for _, n := range names {
		b, err := os.ReadFile(n)
		if err != nil || os.Remove(n) != nil {
			continue
		}
		if s := strings.TrimSpace(string(b)); s != "" {
			out = append(out, s)
		}
	}
	return out
}
