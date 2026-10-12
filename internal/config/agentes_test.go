package config

import (
	"os"
	"path/filepath"
	"testing"
)

// One-user layout (11-oct-2026): agents are the folders in ZORO_AGENTS_DIR with an
// agente.env; a folder replaces the ZORO_AGENTS entry with the same name.
func TestAllAgentsDiscoversFolders(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"sky", "tequila", "sin-env"} {
		if err := os.MkdirAll(filepath.Join(dir, n, "state"), 0o755); err != nil {
			t.Fatal(err)
		}
		if n != "sin-env" {
			if err := os.WriteFile(filepath.Join(dir, n, "agente.env"), []byte("x=1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, "zoro")); err != nil {
		t.Fatal(err)
	}
	c := Config{
		AgentName: "zoro",
		AgentsDir: dir,
		Agents:    parseAgents("sky:/home/sky/engine/state,rinzler:/home/rinzler/engine/state"),
	}
	got := map[string]Agent{}
	for _, a := range c.AllAgents() {
		got[a.Name] = a
	}
	if len(got) != 3 {
		t.Fatalf("want sky, tequila, rinzler — got %+v", got)
	}
	if got["sky"].StateDir != filepath.Join(dir, "sky", "state") || got["sky"].Unit != "zoro@sky" {
		t.Fatalf("the folder must win over ZORO_AGENTS: %+v", got["sky"])
	}
	if got["rinzler"].Unit != "rinzler" || got["rinzler"].StateDir != "/home/rinzler/engine/state" {
		t.Fatalf("not-yet-moved agent keeps its old entry: %+v", got["rinzler"])
	}
	// without ZORO_AGENTS_DIR nothing changes (backward compatible)
	c.AgentsDir = ""
	if n := len(c.AllAgents()); n != 2 {
		t.Fatalf("without AgentsDir only ZORO_AGENTS: %d", n)
	}
}

func TestBotTokenFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "bot.token")
	if err := os.WriteFile(f, []byte("123:abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_BOT_TOKEN_FILE", f)
	t.Setenv("TELEGRAM_OWNER_ID", "42")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Token != "123:abc" {
		t.Fatalf("token from file: %q", c.Token)
	}
	t.Setenv("TELEGRAM_BOT_TOKEN_FILE", filepath.Join(t.TempDir(), "missing"))
	if _, err := Load(); err == nil {
		t.Fatal("a missing token file must fail loudly")
	}
}
