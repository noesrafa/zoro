package bot

import (
	"context"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// /revivir — re-login de Claude DESDE EL CEL (pedido de rafiña, 23-sep-2026, el día que Sky
// murió porque su login de 30 días venció). Todo corre en Go + deploy/revivir.sh, SIN pasar
// por Claude: así sirve aunque el que esté muerto sea el propio Zoro.
//
//	/revivir              → días que le quedan al login de cada agente + links para revivir
//	/revivir_sky          → abre `claude` → /login como ese usuario en tmux y manda el link
//	(mensaje xxxx#yyyy)   → si hay un login esperando, se pega ahí (no llega a Claude)

var reCodigoLogin = regexp.MustCompile(`^[A-Za-z0-9_-]{16,}#[A-Za-z0-9_-]{16,}$`)

var agentesRevivir = map[string]bool{"zoro": true, "sky": true, "dominio": true}

// esperaLogin: el login que espera el código que rafiña va a pegar (uno a la vez).
type esperaLogin struct {
	mu     sync.Mutex
	agente string
	hasta  time.Time
}

func (e *esperaLogin) poner(ag string, d time.Duration) {
	e.mu.Lock()
	e.agente, e.hasta = ag, time.Now().Add(d)
	e.mu.Unlock()
}

// tomar devuelve el agente que espera código (y lo limpia) si sigue vigente.
func (e *esperaLogin) tomar() (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.agente == "" || time.Now().After(e.hasta) {
		return "", false
	}
	ag := e.agente
	e.agente = ""
	return ag, true
}

func (e *esperaLogin) vigente() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.agente != "" && time.Now().Before(e.hasta)
}

// esComandoRevivir reconoce /revivir, /revivir sky y /revivir_sky; devuelve el agente ("" = estado).
func esComandoRevivir(cmd, text string, fields []string) (string, bool) {
	if cmd != "/revivir" && !strings.HasPrefix(cmd, "/revivir_") {
		return "", false
	}
	ag := strings.TrimPrefix(strings.TrimPrefix(cmd, "/revivir"), "_")
	if ag == "" {
		ag = firstArg(text, fields[0])
	}
	return strings.ToLower(ag), true
}

func (b *Bot) scriptRevivir(ctx context.Context, d time.Duration, args ...string) string {
	c, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	out, err := exec.CommandContext(c, "bash", append([]string{filepath.Join(b.cfg.EngineDir, "deploy", "revivir.sh")}, args...)...).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if s == "" && err != nil {
		s = "ERROR " + err.Error()
	}
	return s
}

func (b *Bot) revivir(chatID int64, ag string) {
	ctx := context.Background()
	links := "Para revivir uno toca: /revivir_zoro · /revivir_sky · /revivir_dominio"
	if ag == "" {
		b.send(ctx, chatID, "🔑 Logins de Claude (duran ~30 días):\n"+b.scriptRevivir(ctx, 30*time.Second, "estado")+"\n\n"+links)
		return
	}
	if !agentesRevivir[ag] {
		b.send(ctx, chatID, "⚠️ No conozco a \""+ag+"\". "+links)
		return
	}
	b.send(ctx, chatID, "🔑 Abriendo el login de "+ag+"… (~15 s)")
	out := b.scriptRevivir(ctx, 2*time.Minute, "inicio", ag)
	if !strings.HasPrefix(out, "URL=") {
		b.send(ctx, chatID, "❌ No pude abrir el login de "+ag+":\n"+out)
		return
	}
	b.revive.poner(ag, 10*time.Minute)
	b.send(ctx, chatID, "1) Abre este link con tu cuenta de Claude:\n\n"+strings.TrimPrefix(out, "URL=")+
		"\n\n2) Dale Authorize y copia el código.\n3) Pégamelo aquí tal cual (se ve como xxxx#yyyy). Tienes 10 min.")
}

func (b *Bot) revivirCodigo(chatID int64, ag, code string) {
	ctx := context.Background()
	b.send(ctx, chatID, "⏳ Pegando el código en el login de "+ag+" y probándolo… (~30 s)")
	out := b.scriptRevivir(ctx, 3*time.Minute, "codigo", ag, code)
	if strings.HasPrefix(out, "OK") {
		b.send(ctx, chatID, "✅ "+ag+" revivido — "+strings.TrimSpace(strings.TrimPrefix(out, "OK")))
		return
	}
	b.send(ctx, chatID, "❌ "+out+"\n\nReintenta con /revivir_"+ag)
}
