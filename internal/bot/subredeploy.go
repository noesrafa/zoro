package bot

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// subRedeploy is /redeploy (and /update) on a sub-agent (sky, tequila, rinzler…):
// they can't build Zoro's repo (not theirs — the build failed with "error
// obtaining VCS status", 10-oct-2026), so they install Zoro's latest build over
// their own binary and restart — the same thing actualizar-agentes does at 01:40,
// on demand. The old binary stays next to it as <exe>.bak.
func (b *Bot) subRedeploy(chatID int64) {
	ctx := context.Background()
	msg, restart := installUpstream(b.cfg.UpstreamBin)
	b.send(ctx, chatID, msg)
	if !restart {
		return
	}
	time.Sleep(500 * time.Millisecond)
	_ = os.Remove(b.aliveFile) // a planned restart, not a crash
	os.Exit(0)                 // systemd (Restart=always) starts the new binary
}

// installUpstream copies src over the running executable (write beside it, then
// rename: safe while it runs). It returns the message for the owner and whether
// a restart should follow.
func installUpstream(src string) (string, bool) {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return "❌ No sé dónde está mi binario: " + err.Error(), false
	}
	return installBinary(src, exe)
}

func installBinary(src, dst string) (string, bool) {
	nb, err := os.ReadFile(src)
	if err != nil || len(nb) == 0 {
		return fmt.Sprintf("❌ No pude leer el engine nuevo (%s): %v", src, err), false
	}
	if cur, err := os.ReadFile(dst); err == nil && bytes.Equal(cur, nb) {
		return "✅ Ya tengo el engine más nuevo. Reinicio igual para aplicarlo…", true
	}
	if err := copyFile(dst, dst+".bak"); err != nil {
		return "❌ No pude respaldar mi binario, no cambio nada: " + err.Error(), false
	}
	tmp := dst + ".new"
	if err := os.WriteFile(tmp, nb, 0o755); err != nil {
		return "❌ No pude escribir el engine nuevo: " + err.Error(), false
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return "❌ No pude instalar el engine nuevo: " + err.Error(), false
	}
	return "✅ Engine actualizado al de Zoro — reiniciando…", true
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
