package bot

// Crons in a side session. rafiña's ask, 9-oct-2026 ("que Zoro gaste menos
// tokens"): the 9 am payments reminder ran inside the main conversation and
// re-read ~200K tokens of the day to say "the phone bill is due today".
//
//   - Every cron except the nightly rollover and the ones marked "main": true in
//     crons.json (despertares included) runs in a FRESH throwaway session: same
//     soul as system prompt, same context.md + brief primed in, none of the
//     day's transcript. The session store is never touched.
//   - What it sent the owner is kept in <state>/cron-digest.jsonl (last
//     digestMax entries, each cut to digestMaxText runes) and rides as a short
//     preface on the NEXT main turn (an owner message, a main cron, the
//     rollover…), so the main Zoro knows what its crons said. Cleared once that
//     turn went through; a failed turn keeps it for the next one.
//   - Pause, limit and quiz rules are the same as for any machine turn.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"zoro/internal/claude"
	"zoro/internal/settings"
	"zoro/internal/uid"
)

const (
	digestName    = "cron-digest.jsonl"
	digestMax     = 5
	digestMaxText = 800
)

type digestEntry struct {
	At   string `json:"at"` // RFC3339
	Cron string `json:"cron"`
	Text string `json:"text"`
}

func (b *Bot) digestFile() string { return filepath.Join(b.cfg.StateDir, digestName) }

func readDigest(path string) []digestEntry {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []digestEntry
	for _, line := range strings.Split(string(raw), "\n") {
		var e digestEntry
		if strings.TrimSpace(line) == "" || json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		out = append(out, e)
	}
	return out
}

func writeDigest(path string, es []digestEntry) error {
	if len(es) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	var sb strings.Builder
	for _, e := range es {
		raw, _ := json.Marshal(e)
		sb.Write(raw)
		sb.WriteByte('\n')
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(sb.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// addDigest records what a side cron told the owner; only the last digestMax stay.
func (b *Bot) addDigest(cronID, text string) error {
	es := append(readDigest(b.digestFile()), digestEntry{
		At:   time.Now().Format(time.RFC3339),
		Cron: cronID,
		Text: truncate(strings.TrimSpace(text), digestMaxText),
	})
	if len(es) > digestMax {
		es = es[len(es)-digestMax:]
	}
	return writeDigest(b.digestFile(), es)
}

// cronDigest is the preface for the next main turn and how many entries it
// carries ("" and 0 when there is nothing).
func (b *Bot) cronDigest() (string, int) {
	es := readDigest(b.digestFile())
	if len(es) == 0 {
		return "", 0
	}
	var sb strings.Builder
	sb.WriteString("[🗂️ Mientras tanto, tus crons (corren en una sesión aparte) le mandaron esto a " + b.cfg.OwnerName + ". Es contexto, NO un mensaje suyo:]\n")
	for _, e := range es {
		at := e.At
		if t, err := time.Parse(time.RFC3339, e.At); err == nil {
			at = t.Format("02-Jan 15:04")
		}
		sb.WriteString("• " + at + " ⏰ " + e.Cron + ": " + strings.ReplaceAll(e.Text, "\n", " ⏎ ") + "\n")
	}
	sb.WriteString("[fin del resumen de crons]\n\n")
	return sb.String(), len(es)
}

// clearDigest drops the first n entries — the ones a main turn just carried.
func (b *Bot) clearDigest(n int) error {
	es := readDigest(b.digestFile())
	if n >= len(es) {
		es = nil
	} else {
		es = es[n:]
	}
	return writeDigest(b.digestFile(), es)
}

// processSide runs a cron in its own throwaway session and delivers the reply
// like any turn. Called by process under turnMu (outbox deliveries need it).
func (b *Bot) processSide(ctx, parent context.Context, j job, prompt string, cur settings.Settings, stopTyping func()) {
	opts := claude.RunOpts{Model: cur.Model, Effort: cur.Effort, SystemPrompt: b.systemPrompt(), Auth: cur.Auth}
	turn := b.primeWithContext(prompt)
	res, err := b.run(ctx, uid.New(), true, turn, opts)
	if err != nil && ctx.Err() != nil {
		return
	}
	if err != nil && cur.Auth != claude.AuthMiMo && res.HitLimit() {
		b.onLimit(parent, j, res, nil)
		return
	}
	if err != nil && cur.Auth == claude.AuthMiMo {
		b.log.Warn("mimo side cron failed", "detail", truncate(res.Diagnostic(), 500), "err", truncate(err.Error(), 500))
		b.reportFailure(parent, j, prompt, mimoFailText(res, err))
		return
	}
	// The shared login lost a refresh race (see process): heal it and retry once
	// on another fresh session — there is no transcript worth keeping here.
	if err != nil && res.Failure() == claude.FailAuth && b.healCredentials(ctx) {
		res, err = b.run(ctx, uid.New(), true, turn, opts)
	}
	if err != nil {
		if ctx.Err() == nil {
			b.reportFailure(parent, j, prompt, b.failureText(res, err))
		}
		return
	}
	b.log.Info("cron: side session done", "cron", j.cronID, "session", res.SessionID,
		"input_tokens", res.Usage.Input(), "output_tokens", res.Usage.OutputTokens)
	stopTyping()
	reply := strings.TrimSpace(res.Text)
	b.mirror(parent, j, prompt, reply)
	if reply != "" {
		b.reply(parent, j.chatID, reply)
		if err := b.addDigest(j.cronID, reply); err != nil {
			b.log.Warn("cron digest: could not save it", "err", err)
		}
	}
	b.sendOutbox(parent, j.chatID, true)
}
