// Command zoro is a single-user Telegram agent backed by the Claude Code CLI.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"zoro/internal/bot"
	"zoro/internal/config"
	"zoro/internal/session"
	"zoro/internal/settings"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load()
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	for _, d := range []string{cfg.StateDir, cfg.InboxDir, cfg.OutboxDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			log.Error("mkdir", "dir", d, "err", err)
			os.Exit(1)
		}
	}
	if err := ensureSoul(cfg.ZoroHome, cfg.SoulFile); err != nil {
		log.Error("soul", "err", err)
		os.Exit(1)
	}

	store, err := session.Open(cfg.StateDir)
	if err != nil {
		log.Error("session store", "err", err)
		os.Exit(1)
	}

	set, err := settings.Open(cfg.StateDir, settings.Settings{Model: cfg.ClaudeModel, Effort: cfg.Effort})
	if err != nil {
		log.Error("settings store", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	b := bot.New(cfg, log, store, set)
	if err := b.Run(ctx); err != nil && ctx.Err() == nil {
		log.Error("run", "err", err)
		os.Exit(1)
	}
	log.Info("zoro stopped")
}

// ensureSoul makes sure ~/.zoro and soul.md exist, seeding a default soul on a
// fresh deploy. The living soul is maintained in ~/.zoro (its own git repo).
func ensureSoul(home, soulFile string) error {
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(soulFile); err == nil {
		return nil
	}
	return os.WriteFile(soulFile, []byte(defaultSoul), 0o644)
}
