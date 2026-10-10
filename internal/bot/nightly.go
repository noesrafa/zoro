package bot

import (
	"fmt"

	"zoro/internal/config"
	"zoro/internal/cron"
)

// NightlyFor is the engine's built-in close-the-day for this agent (cron.Nightly):
// it runs at cfg.NightlyAt with the agent's cierre.md, or a generic close that
// knows the agent's own memory files, so no agent can be set up without one.
func NightlyFor(cfg config.Config) cron.Nightly {
	return cron.Nightly{
		At:         cfg.NightlyAt,
		PromptFile: cfg.NightlyPromptFile,
		Default: fmt.Sprintf(`Cierre técnico del día. Complétalo sin preguntar nada: %[1]s está dormido.

0. Relee la conversación de hoy.
1. Guarda lo durable en TU memoria, en %[2]s: lo que aprendiste de %[1]s, decisiones, estado de cada proyecto y pendientes,
   en la nota o archivo que le toca (actualiza en vez de duplicar, fechas absolutas). %[3]s es el contexto que lees al
   arrancar: mantenlo como índice corto. %[4]s son tus reglas: sólo si hoy cambió una regla. Si %[2]s es un repo git, haz commit.
2. Tu respuesta final ES el brief de mañana: en qué quedó cada tema abierto, pendientes con dueño, decisiones de hoy y lo
   primero que toca hacer. Concreto, máximo 40 líneas. Esta sesión se cierra: mañana arrancas sólo con ese brief, tus
   reglas y tu contexto.
3. No le escribas a nadie ni cambies nada fuera de tu memoria.`, cfg.OwnerName, cfg.ZoroHome, cfg.ContextFile, cfg.SoulFile),
	}
}

// loadCrons is crons.json plus the built-in nightly close: what the scheduler,
// /crons and the CLI all see.
func (b *Bot) loadCrons() (cron.File, error) {
	return cron.LoadWithNightly(b.cfg.CronFile, NightlyFor(b.cfg))
}
