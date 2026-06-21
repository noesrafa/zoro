package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"zoro/internal/claude"
	"zoro/internal/config"
	"zoro/internal/cron"
	"zoro/internal/tg"
	"zoro/internal/uid"
)

// runCron handles `zoro cron …` — one-shot tools that run WITHOUT the daemon, so
// the scheduler can be tested without restarting the live bot:
//
//	zoro cron list        — print every cron + its next 3 runs (CDMX). Verifies
//	                        parsing + timezone without firing anything.
//	zoro cron fire <id>   — run a cron's prompt now through Claude and deliver the
//	                        result to the owner on Telegram (true end-to-end test).
func runCron(cfg config.Config, args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list":
		return cronList(cfg)
	case "fire":
		if len(args) < 2 {
			return fmt.Errorf("usage: zoro cron fire <id>")
		}
		return cronFire(cfg, args[1])
	default:
		return fmt.Errorf("unknown cron subcommand %q (use: list | fire <id>)", args[0])
	}
}

func cronList(cfg config.Config) error {
	f, err := cron.Load(cfg.CronFile)
	if err != nil {
		return err
	}
	if len(f.Crons) == 0 {
		fmt.Printf("no crons in %s\n", cfg.CronFile)
		return nil
	}
	loc := f.Location()
	fmt.Printf("crons (%s) — %s\n\n", f.Timezone, cfg.CronFile)
	for _, j := range f.Crons {
		state := "enabled"
		if !j.Enabled {
			state = "disabled"
		}
		fmt.Printf("• %-18s [%s]  %s\n", j.ID, j.Schedule, state)
		if runs, rerr := cron.NextRuns(j, loc, 3); rerr != nil {
			fmt.Printf("    invalid schedule: %v\n", rerr)
		} else {
			for _, r := range runs {
				fmt.Printf("    -> %s\n", r.Format("Mon 2006-01-02 15:04 MST"))
			}
		}
		fmt.Printf("    prompt: %s\n\n", j.Prompt)
	}
	return nil
}

func cronFire(cfg config.Config, id string) error {
	f, err := cron.Load(cfg.CronFile)
	if err != nil {
		return err
	}
	var job *cron.Job
	for i := range f.Crons {
		if f.Crons[i].ID == id {
			job = &f.Crons[i]
			break
		}
	}
	if job == nil {
		return fmt.Errorf("cron %q not found (try: zoro cron list)", id)
	}

	cd := claude.New(claude.Config{
		Bin: cfg.ClaudeBin, Model: cfg.ClaudeModel, WorkDir: cfg.WorkDir, DangerSkip: cfg.DangerSkip,
	})
	soul, _ := os.ReadFile(cfg.SoulFile)
	prompt := "[⏰ Cron \"" + job.ID + "\" — disparo manual de prueba. Atiende esto y responde breve, como aviso/recordatorio:]\n\n" + job.Prompt

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := cd.Run(ctx, uid.New(), true, prompt, claude.RunOpts{SystemPrompt: strings.TrimSpace(string(soul))})
	if err != nil {
		return fmt.Errorf("run prompt: %w", err)
	}
	text := strings.TrimSpace(res.Text)
	if text == "" {
		text = "(el cron produjo una respuesta vacía)"
	}
	if err := tg.New(cfg.Token).SendMessage(ctx, cfg.OwnerID, text, ""); err != nil {
		return fmt.Errorf("send to telegram: %w", err)
	}
	fmt.Printf("✓ cron %q disparado — %d chars entregados al owner\n", id, len(text))
	return nil
}
