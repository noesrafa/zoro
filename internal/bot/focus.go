package bot

// /focus <project> — fresh session standing INSIDE a project folder, so Claude
// Code picks up that project's CLAUDE.md and every command runs from there.
// The name→folder map lives in ~/.zoro/proyectos.json and is read on every use
// (hot-reload: adding a project never needs a rebuild). Focus is session
// metadata: it ends with the session (/focus off, /newsession, the nightly
// rollover) and does NOT survive a restart/redeploy (rafiña's call,
// 10-sep-2026) — session.Open rotates a focused session away at boot.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const proyectosPath = "proyectos.json" // under cfg.ZoroHome

type proyectosFile struct {
	Nota      string            `json:"nota,omitempty"`
	Proyectos map[string]string `json:"proyectos"`
}

// proyectos reads the project map fresh from disk. Names come back sorted for
// stable listings.
func (b *Bot) proyectos() (map[string]string, []string, error) {
	raw, err := os.ReadFile(filepath.Join(b.cfg.ZoroHome, proyectosPath))
	if err != nil {
		return nil, nil, err
	}
	var f proyectosFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, nil, err
	}
	names := make([]string, 0, len(f.Proyectos))
	for n := range f.Proyectos {
		names = append(names, n)
	}
	sort.Strings(names)
	return f.Proyectos, names, nil
}

func (b *Bot) handleFocus(ctx context.Context, chatID int64, arg string) {
	mapa, names, err := b.proyectos()
	if err != nil {
		b.send(ctx, chatID, "⚠️ Can't read "+proyectosPath+" in "+b.cfg.ZoroHome+": "+err.Error())
		return
	}
	lista := strings.Join(names, ", ")

	if arg == "" {
		cur := b.store.Current().WorkDir
		estado := "focus: — (home, " + b.cfg.WorkDir + ")"
		if cur != "" {
			estado = "focus: " + cur
		}
		b.send(ctx, chatID, estado+"\nProjects: "+lista+"\nUsage: /focus <name> · /focus off")
		return
	}

	if b.busy.Load() {
		b.send(ctx, chatID, "⏳ Hay un turno corriendo. Usa /cancel o espera a que termine, luego /focus.")
		return
	}

	if arg == "off" {
		if b.store.Current().WorkDir == "" {
			b.send(ctx, chatID, "Already home ("+b.cfg.WorkDir+") — nothing to unfocus.")
			return
		}
		st, err := b.store.New()
		if err != nil {
			b.send(ctx, chatID, "⚠️ "+err.Error())
			return
		}
		b.send(ctx, chatID, "🏠 Focus off — fresh session back home ("+b.cfg.WorkDir+").\n"+st.SessionID)
		return
	}

	dir, ok := mapa[arg]
	if !ok {
		b.send(ctx, chatID, "⚠️ Unknown project «"+arg+"». Available: "+lista)
		return
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		b.send(ctx, chatID, "⚠️ The folder for «"+arg+"» doesn't exist: "+dir)
		return
	}
	st, err := b.store.NewIn(dir)
	if err != nil {
		b.send(ctx, chatID, "⚠️ "+err.Error())
		return
	}
	b.send(ctx, chatID, "🎯 Focus: "+arg+" — fresh session in "+dir+".\nEnds with /focus off, /newsession or a restart.\n"+st.SessionID)
}
