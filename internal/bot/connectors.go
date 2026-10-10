package bot

// /connectors on|off — rafiña's ask, 9-oct-2026 ("que Zoro gaste menos
// tokens"): the claude.ai connectors (Higgsfield's 121 tools, Drive, Gmail,
// Calendar, Claude Docs) and lazyweb were loaded into every session unused,
// and re-announced their tool lists each time they reconnected. Off, every
// turn runs with no MCP server and without the lazyweb skills
// (claude.RunOpts.NoConnectors): ~8.5K fewer input tokens per call. On brings
// them back from the next turn. Off by default on Zoro only: the sub-agents
// keep their invocation as it was until their owner says otherwise.

import (
	"context"

	"zoro/internal/tg"
)

// connectorsOn: the switch, or the agent's default when it was never set.
func (b *Bot) connectorsOn() bool {
	switch b.set.Get().Connectors {
	case "on":
		return true
	case "off":
		return false
	}
	return !b.isZoro()
}

func (b *Bot) handleConnectors(ctx context.Context, m *tg.Message, arg string) {
	state := func() string {
		if b.connectorsOn() {
			return "🔌 connectors: on — claude.ai connectors, lazyweb and their skills load in my sessions."
		}
		return "🔌 connectors: off — no claude.ai connectors, no lazyweb (~8.5K fewer tokens per call). /connectors on to load them."
	}
	if arg == "" {
		b.send(ctx, m.Chat.ID, state())
		return
	}
	if m.From == nil || m.From.ID != b.cfg.OwnerID {
		b.send(ctx, m.Chat.ID, "⚠️ Only "+b.cfg.OwnerName+" can switch the connectors.")
		return
	}
	var v string
	switch arg {
	case "on", "si", "sí", "yes":
		v = "on"
	case "off", "no":
		v = "off"
	default:
		b.send(ctx, m.Chat.ID, "Usage: /connectors on|off")
		return
	}
	if err := b.set.SetConnectors(v); err != nil {
		b.send(ctx, m.Chat.ID, "⚠️ "+err.Error())
		return
	}
	b.send(ctx, m.Chat.ID, "✅ "+state()+" (from the next turn; persists)")
}
