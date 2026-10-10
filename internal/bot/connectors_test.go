package bot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"zoro/internal/cron"
	"zoro/internal/tg"
)

// Zoro runs without connectors by default — main turns, side crons — and
// /connectors on brings them back from the next turn, persisted.
func TestConnectorsDefaultOffOnZoro(t *testing.T) {
	tb, calls := sideBot(t)
	tb.runJob(ownerJob("hello"))
	tb.fire(t, cron.Job{ID: "gastos-hoy", Prompt: "p"})
	for i, c := range calls() {
		if !c.opts.NoConnectors {
			t.Fatalf("call %d: Zoro's default is no connectors", i)
		}
	}
	tb.handleConnectors(context.Background(), ownerJob("/connectors on").msgs[0], "on")
	tb.runJob(ownerJob("generate an image with the higgsfield mcp"))
	c := calls()
	if c[len(c)-1].opts.NoConnectors {
		t.Fatal("/connectors on: the next turn loads them")
	}
	raw, _ := os.ReadFile(filepath.Join(tb.cfg.StateDir, "settings.json"))
	var s struct{ Connectors string }
	if json.Unmarshal(raw, &s) != nil || s.Connectors != "on" {
		t.Fatalf("the switch persists: %s", raw)
	}
	tb.handleConnectors(context.Background(), ownerJob("/connectors off").msgs[0], "off")
	if tb.connectorsOn() {
		t.Fatal("/connectors off")
	}
}

// The sub-agents keep today's invocation (connectors on) unless switched.
func TestConnectorsDefaultOnOnSubAgents(t *testing.T) {
	tb := newTestBot(t, "sky")
	if !tb.connectorsOn() {
		t.Fatal("sky keeps its connectors by default")
	}
}

// Only the primary owner switches them.
func TestConnectorsOnlyPrimaryOwner(t *testing.T) {
	tb := newTestBot(t, "tequila")
	tb.cfg.OwnerIDs[2] = true
	m := &tg.Message{Text: "/connectors off", From: &tg.User{ID: 2, FirstName: "Ángel"}, Chat: tg.Chat{ID: 2}}
	tb.handleConnectors(context.Background(), m, "off")
	if !tb.connectorsOn() || tb.sentWith("Only rafiña") != 1 {
		t.Fatalf("a second owner can't switch them: on=%v sent=%q", tb.connectorsOn(), tb.sent)
	}
}
