package bot

import (
	"context"
	"os"
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
//	/revivir              → TOKEN GLOBAL (11-oct-2026, un solo usuario): `claude setup-token` en tmux,
//	                        manda el link; el código pegado da un token de 1 año que se guarda en
//	                        ~/.secrets/claude.token (600) y lo usan todos los agentes y los trabajos
//	/revivir estado       → días que le quedan a cada login + el token global
//	/revivir_zoro         → abre `claude` → /login de Zoro en tmux y manda el link (como antes)
//	/revivir_sky          → si sky ya se mudó a ~/agentes, es el token global; si no, su login viejo
//	(mensaje xxxx#yyyy)   → si hay un login esperando, se pega ahí (no llega a Claude)

var reCodigoLogin = regexp.MustCompile(`^[A-Za-z0-9_-]{16,}#[A-Za-z0-9_-]{16,}$`)

// tokenGlobal es el "agente" del login que espera código cuando es el token de todos.
const tokenGlobal = "token"

var agentesRevivir = map[string]bool{"zoro": true, "sky": true, "tequila": true, "rinzler": true, "dominio": true} // dominio: apagado 25-sep, se deja por si revive

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

// usaTokenGlobal: el sub-agente ya vive en ~/agentes/<ag> (un solo usuario), así que su
// "login" es el token de todos. Antes de la mudanza sigue su login viejo.
func (b *Bot) usaTokenGlobal(ag string) bool {
	if ag == "zoro" || b.cfg.AgentsDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(b.cfg.AgentsDir, ag, "agente.env"))
	return err == nil
}

func (b *Bot) revivir(chatID int64, ag string) {
	ctx := context.Background()
	links := "/revivir — token nuevo para todos · /revivir_zoro — login de Zoro · /revivir estado"
	switch {
	case ag == "estado" || ag == "status":
		b.send(ctx, chatID, "🔑 Claude:\n"+b.scriptRevivir(ctx, 30*time.Second, "estado")+"\n\n"+links)
		return
	case ag == "" || ag == tokenGlobal || b.usaTokenGlobal(ag):
		b.revivirToken(ctx, chatID)
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

// revivirToken abre `claude setup-token` (deploy/revivir.sh token-inicio) y manda el link.
func (b *Bot) revivirToken(ctx context.Context, chatID int64) {
	b.send(ctx, chatID, "🔑 Abriendo el token de Claude para todos los agentes… (~15 s)")
	out := b.scriptRevivir(ctx, 2*time.Minute, "token-inicio")
	if !strings.HasPrefix(out, "URL=") {
		b.send(ctx, chatID, "❌ No pude abrir claude setup-token:\n"+out)
		return
	}
	b.revive.poner(tokenGlobal, 10*time.Minute)
	b.send(ctx, chatID, "1) Abre este link con tu cuenta de Claude (noesrafa):\n\n"+strings.TrimPrefix(out, "URL=")+
		"\n\n2) Dale Authorize y copia el código.\n3) Pégamelo aquí tal cual (xxxx#yyyy). Tienes 10 min. "+
		"El código y el token se quedan en la VPS: no pasan por Claude.")
}

func (b *Bot) revivirCodigo(chatID int64, ag, code string) {
	ctx := context.Background()
	if ag == tokenGlobal {
		b.send(ctx, chatID, "⏳ Creando el token y probándolo… (~30 s)")
		out := b.scriptRevivir(ctx, 3*time.Minute, "token-codigo", code)
		if strings.HasPrefix(out, "OK") {
			b.send(ctx, chatID, "✅ Token de Claude listo para todos — "+strings.TrimSpace(strings.TrimPrefix(out, "OK")))
			return
		}
		b.send(ctx, chatID, "❌ "+out+"\n\nReintenta con /revivir")
		return
	}
	b.send(ctx, chatID, "⏳ Pegando el código en el login de "+ag+" y probándolo… (~30 s)")
	out := b.scriptRevivir(ctx, 3*time.Minute, "codigo", ag, code)
	if strings.HasPrefix(out, "OK") {
		b.send(ctx, chatID, "✅ "+ag+" revivido — "+strings.TrimSpace(strings.TrimPrefix(out, "OK")))
		return
	}
	b.send(ctx, chatID, "❌ "+out+"\n\nReintenta con /revivir_"+ag)
}
