package bot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"zoro/internal/media"
)

const (
	// outboxSettle: a file modified more recently than this may still be being
	// written (jobs copy 40 MB GIFs in), so it waits instead of going out half-done.
	outboxSettle = 5 * time.Second
	// outboxEvery: how often the watcher looks for files left between turns.
	outboxEvery = 30 * time.Second
	// outboxTries: failed sends of one file version before giving up on it, so a
	// file Telegram always rejects (too big…) isn't re-uploaded every 30 s forever.
	outboxTries = 3
)

// openOutbox loads (or, on the first start, seeds) the delivered-files ledger.
func (b *Bot) openOutbox() {
	path := filepath.Join(b.cfg.StateDir, "outbox-delivered.json")
	l, seeded, err := media.OpenLedger(path, b.cfg.OutboxDir)
	if err != nil {
		b.log.Warn("outbox ledger: could not save", "file", path, "err", err)
	}
	if seeded {
		b.log.Info("outbox ledger seeded with the files already there", "file", path)
	}
	b.outbox = l
}

// outboxLoop is the watcher: every outboxEvery it delivers to the owner what
// detached jobs left in the outbox between turns (the turn-end delivery only
// runs when there IS a turn). Stops with ctx.
func (b *Bot) outboxLoop(ctx context.Context) {
	t := time.NewTicker(outboxEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.outboxTick(ctx)
		}
	}
}

// outboxTick delivers pending outbox files to the owner, but only when no turn
// is running: never in the middle of one (the turn delivers them itself at its end).
func (b *Bot) outboxTick(ctx context.Context) {
	if b.cfg.OwnerID == 0 || b.outbox == nil {
		return
	}
	if b.busy.Load() || !b.turnMu.TryLock() {
		return
	}
	defer b.turnMu.Unlock()
	b.sendOutbox(ctx, b.cfg.OwnerID, false)
}

// sendOutbox delivers every outbox file not in the ledger yet (new, or changed
// since it was sent) to chatID and its mirrors, and records each one that
// reached chatID. Caller holds turnMu. settle: at the end of a turn, wait for
// files still being written (up to outboxSettle) instead of leaving them to the
// watcher; anything still moving after that goes out on a later tick.
func (b *Bot) sendOutbox(ctx context.Context, chatID int64, settle bool) {
	if b.outbox == nil {
		return
	}
	ready, wait := b.outbox.Pending(b.cfg.OutboxDir, time.Now(), outboxSettle)
	b.deliverOutbox(ctx, chatID, ready)
	if !settle || wait == 0 {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(wait + 100*time.Millisecond):
	}
	ready, _ = b.outbox.Pending(b.cfg.OutboxDir, time.Now(), outboxSettle)
	b.deliverOutbox(ctx, chatID, ready)
}

func (b *Bot) deliverOutbox(ctx context.Context, chatID int64, files []media.OutboxFile) {
	if b.outFails == nil {
		b.outFails = map[string]int{}
	}
	for _, f := range files {
		err := b.sendOutboxFile(ctx, chatID, f.Path)
		if err != nil && ctx.Err() != nil {
			return // shutting down: not marked, goes out after the restart
		}
		key := f.Path + "|" + strconv.FormatInt(f.Mod.UnixNano(), 10) + "|" + strconv.FormatInt(f.Size, 10)
		if err != nil {
			b.log.Warn("send outbox file failed", "file", f.Path, "chat", chatID, "err", err)
			if b.outFails[key]++; b.outFails[key] < outboxTries {
				continue // not marked: retried on the next turn or tick
			}
			b.send(ctx, chatID, fmt.Sprintf("⚠️ couldn't deliver %s after %d tries: %s",
				filepath.Base(f.Path), outboxTries, truncate(err.Error(), 300)))
		}
		delete(b.outFails, key)
		if merr := b.outbox.Mark(f); merr != nil {
			b.log.Warn("outbox ledger: could not save", "err", merr)
		}
	}
}

// sendOutboxFile uploads one file to chatID, then echoes it to the mirror: the
// watcher must SEE what a sub-agent sends its owner (logos, PDFs, fotos), not
// just read the text around it (pedido por rafiña el 13-sep-2026). Only the
// owner's chat decides success; the mirror gets it only once that worked, so a
// retry never repeats it there.
func (b *Bot) sendOutboxFile(ctx context.Context, chatID int64, f string) error {
	var sendTo func(path string, chat int64) error
	path := f
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(f), ".")) {
	case "jpg", "jpeg", "png", "webp", "gif":
		// Shrink oversized images first, then route by size: Telegram's
		// sendPhoto silently rejects files over 10 MiB, so anything still
		// above that goes out as a document (preserved, up to 50 MiB).
		path = media.OptimizeImage(f)
		if path != f {
			defer os.Remove(path)
		}
		if fi, err := os.Stat(path); err == nil && fi.Size() > media.PhotoMaxBytes {
			sendTo = func(p string, d int64) error { return b.tg.SendDocument(ctx, d, p, "") }
		} else {
			sendTo = func(p string, d int64) error { return b.tg.SendPhoto(ctx, d, p, "") }
		}
	case "ogg", "oga":
		sendTo = func(p string, d int64) error { return b.tg.SendVoice(ctx, d, p, "") }
	default:
		sendTo = func(p string, d int64) error { return b.tg.SendDocument(ctx, d, p, "") }
	}
	if err := sendTo(path, chatID); err != nil {
		return err
	}
	for _, d := range b.mirrorTargets(chatID) {
		if err := sendTo(path, d); err != nil {
			b.log.Warn("send outbox file to mirror failed", "file", f, "chat", d, "err", err)
		}
	}
	return nil
}
