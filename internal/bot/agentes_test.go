package bot

import (
	"strings"
	"testing"
	"time"
)

func TestFormatAgentLine(t *testing.T) {
	now := time.Date(2026, 10, 11, 22, 0, 0, 0, time.UTC)
	l := agentLine{Name: "sky", Unit: "zoro@sky", Active: "active", Model: "claude-sonnet-5-5", Effort: "medium",
		Pending: 2, LastTurn: now.Add(-3 * time.Hour)}
	got := formatAgentLine(l, now)
	for _, want := range []string{"🟢 sky", "zoro@sky active", "sonnet-5-5/medium", "2 mensaje(s)", "hace 3 h"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q missing %q", got, want)
		}
	}
	l.Paused = &pauseFlag{Reason: reasonStop}
	if got := formatAgentLine(l, now); !strings.HasPrefix(got, "⏸️") || !strings.Contains(got, "en pausa (stop)") {
		t.Fatalf("paused: %q", got)
	}
	l.Active = "failed"
	if got := formatAgentLine(l, now); !strings.HasPrefix(got, "🔴") {
		t.Fatalf("down wins over paused: %q", got)
	}
}
