package bot

// /coche — the car card: plate, tire pressures, hologram/engomado, gas, VIN.
// Content lives in ~/.zoro/coche.md so the agent can update it (e.g. when the
// hologram is renewed) without touching Go; this command just sends it verbatim.

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	cochePath    = ".zoro/coche.md"
	percancePath = ".zoro/percance.md"
)

func cocheText() string {
	return zoroCard(cochePath)
}

// /percance — the roadside emergency card: insurer phones, policy data for the
// call, coverage cheat-sheet. Same contract as /coche: content lives in
// ~/.zoro/percance.md and is sent verbatim, so it updates without a rebuild.
func percanceText() string {
	return zoroCard(percancePath)
}

func zoroCard(rel string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/home/rafael"
	}
	raw, err := os.ReadFile(filepath.Join(home, rel))
	if err != nil {
		return "⚠️ No encontré ~/" + rel + " — pídele a zoro que lo regenere."
	}
	return strings.TrimSpace(string(raw))
}
