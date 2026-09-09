package bot

// /coche — the car card: plate, tire pressures, hologram/engomado, gas, VIN.
// Content lives in ~/.zoro/coche.md so the agent can update it (e.g. when the
// hologram is renewed) without touching Go; this command just sends it verbatim.

import (
	"os"
	"path/filepath"
	"strings"
)

const cochePath = ".zoro/coche.md"

func cocheText() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/home/rafael"
	}
	raw, err := os.ReadFile(filepath.Join(home, cochePath))
	if err != nil {
		return "⚠️ No encontré ~/.zoro/coche.md — pídele a zoro que lo regenere."
	}
	return strings.TrimSpace(string(raw))
}
