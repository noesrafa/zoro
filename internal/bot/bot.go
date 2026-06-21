// Package bot wires Telegram <-> Claude: long-poll loop, single-user lock,
// slash commands, and a serialized worker that runs one Claude turn at a time.
package bot

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"zoro/internal/claude"
	"zoro/internal/config"
	"zoro/internal/media"
	"zoro/internal/session"
	"zoro/internal/settings"
	"zoro/internal/stt"
	"zoro/internal/tg"
	"zoro/internal/tgfmt"
	"zoro/internal/tts"
)

var validEfforts = map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}

func validModel(m string) bool {
	switch m {
	case "opus", "sonnet", "haiku", "fable":
		return true
	}
	return strings.HasPrefix(m, "claude-")
}

const helpText = `⚔️ zoro — your agent on the VPS.

Just talk to me: text, photos, PDFs or voice notes. I work inside /home/rafael with the full toolset, in one persistent conversation.

Commands:
/newsession — start a fresh conversation
/compact — compress context, keep memory
/model <opus|sonnet|haiku|fable|claude-…> — switch model (persists)
/effort <low|medium|high|xhigh|max> — set reasoning effort (persists)
/voice <msg> — reply with a voice note
/status — session, model, effort, uptime
/cancel — stop the current task
/restart — restart me
/help — this

Any other /command (e.g. /deep-research) is passed straight to Claude.
I drop deliverables in my outbox and they arrive here automatically.`

// Bot is the running daemon.
type Bot struct {
	cfg    config.Config
	log    *slog.Logger
	tg     *tg.Client
	cd     *claude.Driver
	store  *session.Store
	set    *settings.Store
	tts    tts.Config
	mediaC media.Config
	ttsDir string

	startedAt time.Time
	jobs      chan job

	mu        sync.Mutex
	cancelCur context.CancelFunc
	lastCost  float64
}

type job struct {
	chatID    int64
	msg       *tg.Message // set for normal messages (attachments ingested in worker)
	prompt    string      // pre-built prompt for synthetic jobs (/compact, /voice)
	wantVoice bool
	isCompact bool
}

// New constructs a Bot.
func New(cfg config.Config, log *slog.Logger, store *session.Store, set *settings.Store) *Bot {
	return &Bot{
		cfg:   cfg,
		log:   log,
		tg:    tg.New(cfg.Token),
		store: store,
		set:   set,
		cd: claude.New(claude.Config{
			Bin:        cfg.ClaudeBin,
			Model:      cfg.ClaudeModel,
			WorkDir:    cfg.WorkDir,
			DangerSkip: cfg.DangerSkip,
		}),
		tts: tts.Config{PiperBin: cfg.PiperBin, PiperVoice: cfg.PiperVoice, FFmpegBin: cfg.FFmpegBin},
		mediaC: media.Config{
			InboxDir:     cfg.InboxDir,
			OutboxDir:    cfg.OutboxDir,
			MaxFileBytes: cfg.MaxFileBytes,
			STT:          stt.Config{WhisperBin: cfg.WhisperBin, WhisperModel: cfg.WhisperModel, Lang: cfg.WhisperLang, FFmpegBin: cfg.FFmpegBin},
		},
		ttsDir:    filepath.Join(cfg.StateDir, "tts"),
		startedAt: time.Now(),
		jobs:      make(chan job, 16),
	}
}

// Run starts the worker and the long-poll loop until ctx is cancelled, then drains
// queued + in-flight turns before returning, so a restart/SIGTERM never drops a
// message that was already fetched from Telegram.
func (b *Bot) Run(ctx context.Context) error {
	b.registerCommands(ctx)

	workerDone := make(chan struct{})
	go func() {
		b.worker()
		close(workerDone)
	}()

	b.log.Info("zoro online",
		"owner", b.cfg.OwnerID, "workdir", b.cfg.WorkDir, "model", b.cfg.ClaudeModel,
		"stt", b.mediaC.STT.Available(), "tts", b.tts.Available())

	offset := 0
	for ctx.Err() == nil {
		ups, err := b.tg.GetUpdates(ctx, offset, 50)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			b.log.Warn("getUpdates failed", "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
			}
			continue
		}
		for _, u := range ups {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			b.dispatch(ctx, u)
		}
	}

	// Graceful shutdown: stop accepting new work, finish what's queued / in-flight.
	b.log.Info("draining before shutdown")
	close(b.jobs)
	<-workerDone
	return ctx.Err()
}

func (b *Bot) dispatch(ctx context.Context, u tg.Update) {
	m := u.Message
	if m == nil {
		m = u.EditedMessage
	}
	if m == nil || m.From == nil {
		return
	}
	if m.From.ID != b.cfg.OwnerID {
		b.log.Warn("ignored non-owner", "from", m.From.ID, "username", m.From.Username)
		return
	}

	text := strings.TrimSpace(m.Text)
	if strings.HasPrefix(text, "/") {
		fields := strings.Fields(text)
		cmd := strings.SplitN(fields[0], "@", 2)[0] // strip @botname
		switch cmd {
		case "/start", "/help":
			b.send(ctx, m.Chat.ID, helpText)
		case "/status":
			b.send(ctx, m.Chat.ID, b.statusText())
		case "/newsession":
			st, err := b.store.New()
			if err != nil {
				b.send(ctx, m.Chat.ID, "⚠️ "+err.Error())
				return
			}
			b.send(ctx, m.Chat.ID, "🔄 New session — fresh memory.\n"+st.SessionID)
		case "/cancel":
			b.cancel()
			b.send(ctx, m.Chat.ID, "✋ Cancelled the current task (if any).")
		case "/restart":
			b.send(ctx, m.Chat.ID, "♻️ Restarting…")
			go func() { time.Sleep(500 * time.Millisecond); os.Exit(0) }()
		case "/compact":
			b.enqueue(job{chatID: m.Chat.ID, prompt: "/compact", isCompact: true})
		case "/model":
			arg := firstArg(text, fields[0])
			if arg == "" {
				b.send(ctx, m.Chat.ID, "model: "+b.set.Get().Model+"\nUsage: /model <opus|sonnet|haiku|fable|claude-…>")
				return
			}
			if !validModel(arg) {
				b.send(ctx, m.Chat.ID, "⚠️ Unknown model. Use an alias (opus/sonnet/haiku/fable) or a claude-… id.")
				return
			}
			if err := b.set.SetModel(arg); err != nil {
				b.send(ctx, m.Chat.ID, "⚠️ "+err.Error())
				return
			}
			b.send(ctx, m.Chat.ID, "✅ model → "+arg+" (persists across sessions)")
		case "/effort":
			arg := strings.ToLower(firstArg(text, fields[0]))
			if arg == "" {
				b.send(ctx, m.Chat.ID, "effort: "+b.set.Get().Effort+"\nUsage: /effort <low|medium|high|xhigh|max>")
				return
			}
			if !validEfforts[arg] {
				b.send(ctx, m.Chat.ID, "⚠️ Effort must be: low, medium, high, xhigh, or max.")
				return
			}
			if err := b.set.SetEffort(arg); err != nil {
				b.send(ctx, m.Chat.ID, "⚠️ "+err.Error())
				return
			}
			b.send(ctx, m.Chat.ID, "✅ effort → "+arg+" (persists across sessions)")
		case "/voice":
			rest := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
			if rest == "" {
				b.send(ctx, m.Chat.ID, "Usage: /voice <message> — I'll reply with audio.")
				return
			}
			b.enqueue(job{chatID: m.Chat.ID, prompt: rest, wantVoice: true})
		default:
			// Unknown slash command → pass it through to Claude (its own skills,
			// e.g. /deep-research, /code-review, run inside the turn).
			b.enqueue(job{chatID: m.Chat.ID, msg: m})
		}
		return
	}

	// Normal message → run as a turn (attachments ingested in the worker).
	b.enqueue(job{chatID: m.Chat.ID, msg: m})
}

func (b *Bot) enqueue(j job) {
	// Show "typing…" immediately on receipt, even if a previous turn is still
	// running and this job has to wait in the queue. The worker's pump continues it.
	go b.tg.SendChatAction(context.Background(), j.chatID, tg.ActionTyping)
	select {
	case b.jobs <- j:
	default:
		go b.send(context.Background(), j.chatID, "⏳ Still finishing the previous task — one sec.")
	}
}

// worker runs jobs one at a time on a background context so an in-flight turn
// survives SIGTERM and completes during the graceful drain in Run.
func (b *Bot) worker() {
	for j := range b.jobs {
		b.process(context.Background(), j)
	}
}

func (b *Bot) process(parent context.Context, j job) {
	ctx, cancel := context.WithCancel(parent)
	b.mu.Lock()
	b.cancelCur = cancel
	b.mu.Unlock()
	defer func() {
		cancel()
		b.mu.Lock()
		b.cancelCur = nil
		b.mu.Unlock()
	}()

	wantVoice := j.wantVoice

	// Start the typing/recording indicator IMMEDIATELY so it also covers media
	// download + voice transcription (whisper model load can take a few seconds).
	action := tg.ActionTyping
	if wantVoice && b.tts.Available() {
		action = tg.ActionRecordVoice
	}
	stopTyping := b.typingPump(ctx, j.chatID, action)
	defer stopTyping()

	prompt := j.prompt
	if j.msg != nil {
		in, err := media.Ingest(ctx, b.mediaC, b.tg, j.msg)
		if err != nil {
			b.send(parent, j.chatID, "⚠️ media error: "+err.Error())
			return
		}
		for _, n := range in.Notes {
			b.send(parent, j.chatID, n)
		}
		prompt = buildPrompt(j.msg, in)
		// Text by default: understanding a voice note does NOT force a voice reply.
		// Audio replies happen only on explicit request (/voice or the send-audio skill).
	}
	if strings.TrimSpace(prompt) == "" {
		return
	}

	before := media.SnapshotOutbox(b.cfg.OutboxDir)

	cur := b.set.Get()
	opts := claude.RunOpts{Model: cur.Model, Effort: cur.Effort, SystemPrompt: b.systemPrompt()}

	st := b.store.Current()
	// On a NEW session, prime the conversation once with the heavy context.md
	// (durable background about rafiña). On resumes it's already in history.
	turnPrompt := prompt
	if !st.Created {
		turnPrompt = b.primeWithContext(prompt)
	}
	res, err := b.cd.Run(ctx, st.SessionID, !st.Created, turnPrompt, opts)
	if err != nil && ctx.Err() != nil {
		return // cancelled / shutting down
	}
	if err != nil && st.Created {
		// Resume failed (e.g. transcript gone) — rotate to a fresh session and retry once,
		// re-priming the new session with context.
		b.log.Warn("resume failed, rotating session", "err", err)
		if ns, nerr := b.store.New(); nerr == nil {
			st = ns
			res, err = b.cd.Run(ctx, st.SessionID, true, b.primeWithContext(prompt), opts)
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		b.send(parent, j.chatID, "⚠️ "+truncate(err.Error(), 1500))
		return
	}

	// Persist session continuity.
	if res.SessionID != "" && res.SessionID != st.SessionID {
		_ = b.store.Set(res.SessionID, true)
	} else if !st.Created {
		_ = b.store.MarkCreated()
	}
	b.mu.Lock()
	b.lastCost = res.CostUSD
	b.mu.Unlock()

	stopTyping()

	reply := strings.TrimSpace(res.Text)
	if j.isCompact {
		if reply == "" {
			reply = "🧹 Compacted."
		} else {
			reply = "🧹 " + reply
		}
	}

	// Voice reply (mirror modality): synthesize into a temp dir (not the outbox).
	if wantVoice && b.tts.Available() && reply != "" {
		if ogg, terr := tts.Synthesize(ctx, b.tts, reply, b.ttsDir); terr == nil {
			if serr := b.tg.SendVoice(parent, j.chatID, ogg, ""); serr == nil {
				_ = os.Remove(ogg)
				b.sendOutbox(parent, j.chatID, before)
				return
			} else {
				b.log.Warn("send voice failed", "err", serr)
			}
		} else {
			b.log.Warn("tts failed", "err", terr)
		}
		// fall through to text on any failure
	}

	if reply != "" {
		b.reply(parent, j.chatID, reply)
	}
	b.sendOutbox(parent, j.chatID, before)
}

// systemPrompt reads the agent's soul FRESH each turn from ~/.zoro/soul.md (identity +
// behavior). Editing it takes effect on the next message, no restart needed.
func (b *Bot) systemPrompt() string {
	return readFile(b.cfg.SoulFile)
}

// primeWithContext prepends the durable background (~/.zoro/context.md) to the first
// message of a new session, so it enters the conversation history once. Returns the
// prompt unchanged if there is no context file.
func (b *Bot) primeWithContext(userPrompt string) string {
	c := readFile(b.cfg.ContextFile)
	if c == "" {
		return userPrompt
	}
	return "[SESSION CONTEXT — durable background about rafiña, loaded once at the start of this conversation. Your identity and behavior rules are in your system prompt.]\n\n" +
		c + "\n\n---\n\nrafiña: " + userPrompt
}

func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (b *Bot) sendOutbox(ctx context.Context, chatID int64, before map[string]time.Time) {
	for _, f := range media.NewOutboxFiles(b.cfg.OutboxDir, before) {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(f), "."))
		var err error
		switch ext {
		case "jpg", "jpeg", "png", "webp", "gif":
			err = b.tg.SendPhoto(ctx, chatID, f, "")
		case "ogg", "oga":
			err = b.tg.SendVoice(ctx, chatID, f, "")
		default:
			err = b.tg.SendDocument(ctx, chatID, f, "")
		}
		if err != nil {
			b.log.Warn("send outbox file failed", "file", f, "err", err)
		}
	}
}

func (b *Bot) typingPump(parent context.Context, chatID int64, action string) func() {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		_ = b.tg.SendChatAction(ctx, chatID, action)
		t := time.NewTicker(4 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = b.tg.SendChatAction(ctx, chatID, action)
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(cancel) }
}

func (b *Bot) cancel() {
	b.mu.Lock()
	c := b.cancelCur
	b.mu.Unlock()
	if c != nil {
		c()
	}
}

// send delivers plain text (status, help, errors, notes), chunked.
func (b *Bot) send(ctx context.Context, chatID int64, text string) {
	for _, chunk := range chunkText(text, 4000) {
		if err := b.tg.SendMessage(ctx, chatID, chunk, ""); err != nil {
			b.log.Warn("send failed", "err", err)
			return
		}
	}
}

// reply renders Claude's markdown to Telegram HTML and sends it in chunks,
// falling back to plain text for any chunk Telegram rejects (e.g. a parse error).
func (b *Bot) reply(ctx context.Context, chatID int64, md string) {
	htmlChunks := tgfmt.Render(md, tgfmt.DefaultLimit)
	plainChunks := chunkText(md, 4000)
	for i, h := range htmlChunks {
		if err := b.tg.SendMessage(ctx, chatID, h, "HTML"); err != nil {
			b.log.Warn("html send failed, falling back to plain", "err", err)
			fallback := h
			if i < len(plainChunks) {
				fallback = plainChunks[i]
			}
			if err2 := b.tg.SendMessage(ctx, chatID, fallback, ""); err2 != nil {
				b.log.Warn("plain fallback failed", "err", err2)
				return
			}
		}
	}
}

func (b *Bot) statusText() string {
	st := b.store.Current()
	b.mu.Lock()
	cost := b.lastCost
	b.mu.Unlock()
	sid := st.SessionID
	if len(sid) > 8 {
		sid = sid[:8]
	}
	state := "fresh"
	if st.Created {
		state = "active"
	}
	cur := b.set.Get()
	return strings.Join([]string{
		"⚔️ zoro status",
		"session: " + sid + " (" + state + ")",
		"model: " + cur.Model,
		"effort: " + cur.Effort,
		"workdir: " + b.cfg.WorkDir,
		fmt.Sprintf("last turn cost: $%.4f", cost),
		"voice in (stt): " + onoff(b.mediaC.STT.Available()),
		"voice out (tts): " + onoff(b.tts.Available()),
		"uptime: " + time.Since(b.startedAt).Round(time.Second).String(),
	}, "\n")
}

func (b *Bot) registerCommands(ctx context.Context) {
	cmds := []tg.BotCommand{
		{Command: "newsession", Description: "Start a fresh conversation"},
		{Command: "compact", Description: "Compact the conversation to free context"},
		{Command: "model", Description: "Switch model (opus/sonnet/haiku/fable/claude-…)"},
		{Command: "effort", Description: "Set reasoning effort (low/medium/high/xhigh/max)"},
		{Command: "voice", Description: "Reply with a voice note: /voice <message>"},
		{Command: "status", Description: "Show session, model, effort, uptime"},
		{Command: "cancel", Description: "Cancel the current task"},
		{Command: "restart", Description: "Restart the daemon"},
		{Command: "help", Description: "Show help"},
	}
	if err := b.tg.SetMyCommands(ctx, cmds); err != nil {
		b.log.Warn("setMyCommands failed", "err", err)
	}
}

func buildPrompt(m *tg.Message, in media.Incoming) string {
	var parts []string
	txt := strings.TrimSpace(m.Text)
	if txt == "" {
		txt = strings.TrimSpace(m.Caption)
	}
	if in.Transcript != "" {
		if txt != "" {
			parts = append(parts, txt)
		}
		parts = append(parts, "[Voice note transcript]: "+in.Transcript)
	} else if txt != "" {
		parts = append(parts, txt)
	}
	if len(in.Files) > 0 {
		parts = append(parts, fmt.Sprintf(
			"[The user attached %d file(s). Use the Read tool to view them: %s]",
			len(in.Files), strings.Join(in.Files, ", ")))
	}
	return strings.Join(parts, "\n\n")
}

// firstArg returns the first whitespace-separated argument after cmdToken in text.
func firstArg(text, cmdToken string) string {
	rest := strings.TrimSpace(strings.TrimPrefix(text, cmdToken))
	if rest == "" {
		return ""
	}
	return strings.Fields(rest)[0]
}

func onoff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// chunkText splits s into <=max-byte chunks on rune/newline boundaries.
func chunkText(s string, max int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	for len(s) > max {
		cut := strings.LastIndexByte(s[:max], '\n')
		if cut < max/2 {
			cut = max
		}
		for cut > 0 && cut < len(s) && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if cut <= 0 {
			cut = max
		}
		out = append(out, strings.TrimRight(s[:cut], "\n"))
		s = strings.TrimLeft(s[cut:], "\n")
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}
