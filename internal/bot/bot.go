// Package bot wires Telegram <-> Claude: long-poll loop, single-user lock,
// slash commands, and a serialized worker that runs one Claude turn at a time.
package bot

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"zoro/internal/claude"
	"zoro/internal/collector"
	"zoro/internal/config"
	"zoro/internal/cron"
	"zoro/internal/media"
	"zoro/internal/session"
	"zoro/internal/settings"
	"zoro/internal/stt"
	"zoro/internal/tg"
	"zoro/internal/tgfmt"
	"zoro/internal/tts"
	"zoro/internal/uid"
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
/btw <question> — side-question in parallel on Opus (researches freely, won't touch this chat)
/crons — list scheduled messages (and next run)
/crons <id> — fire that scheduled message now
/status — session, model, effort, uptime
/uso — uso de Claude Code (límites 5h / 7d, snapshot oficial vía claude-hud)
/ls [path] — list VM files
/stats — VM + git repo status
/cancel — stop the current task
/redeploy — rebuild my code + restart (apply changes)
/update — pull latest code from GitHub + rebuild + restart
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
	mediaC    media.Config
	ttsDir    string
	aliveFile string // state/alive — present while running; removed on clean shutdown

	startedAt time.Time
	jobs      chan job
	quit      chan struct{}
	collector *collector.Collector

	mu        sync.Mutex
	cancelCur context.CancelFunc
	lastCost  float64
	busy      atomic.Bool // true while a turn is being processed
}

type job struct {
	chatID    int64
	msgs      []*tg.Message // content messages for a turn (album/burst already coalesced)
	prompt    string        // pre-built prompt for synthetic jobs (/compact, /voice)
	wantVoice bool
	isCompact bool

	// label replaces the raw prompt in the mirror. Machine-made prompts (crons,
	// the nightly rollover) are long internal instructions that are pure noise to
	// whoever is watching — they want to see WHAT fired, not its wording.
	label string
	// isRollover: close the day, then rotate to a fresh session (see cron.Job.Rollover).
	isRollover bool
}

// collectWindow is how long the collector waits for more messages before
// flushing a chat's buffer as one turn — long enough to gather an album or a
// quick multi-bubble thought, short enough to feel instant for a lone message.
const collectWindow = 700 * time.Millisecond

// New constructs a Bot.
func New(cfg config.Config, log *slog.Logger, store *session.Store, set *settings.Store) *Bot {
	b := &Bot{
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
		aliveFile: filepath.Join(cfg.StateDir, "alive"),
		startedAt: time.Now(),
		jobs:      make(chan job, 16),
		quit:      make(chan struct{}),
	}
	// Coalesce albums and quick bursts into a single turn (see internal/collector).
	b.collector = collector.New(collectWindow, func(chatID int64, msgs []*tg.Message) {
		b.enqueue(job{chatID: chatID, msgs: msgs})
	})
	return b
}

// Run starts the worker and the long-poll loop until ctx is cancelled, then drains
// queued + in-flight turns before returning, so a restart/SIGTERM never drops a
// message that was already fetched from Telegram.
func (b *Bot) Run(ctx context.Context) error {
	b.registerCommands(ctx)

	// Lifecycle: if the alive file lingered, the previous run crashed (didn't shut down
	// cleanly). Mark ourselves running now.
	crashed := false
	if _, err := os.Stat(b.aliveFile); err == nil {
		crashed = true
	}
	_ = os.WriteFile(b.aliveFile, []byte(time.Now().UTC().Format(time.RFC3339)), 0o644)

	workerDone := make(chan struct{})
	go func() {
		b.worker()
		close(workerDone)
	}()

	// Cron scheduler: once a minute, fires due prompts from ~/.zoro/crons.json
	// (CDMX timezone, hot-reloaded). Each fire enqueues a normal turn.
	go cron.Run(ctx, b.cfg.CronFile, b.fireCron, b.log)

	b.log.Info("zoro online",
		"owner", b.cfg.OwnerID, "workdir", b.cfg.WorkDir, "model", b.cfg.ClaudeModel,
		"stt", b.mediaC.STT.Available(), "tts", b.tts.Available())
	if crashed {
		b.notify("⚠️ zoro revivió — el run anterior se cayó. Ya regresé ⚔️")
	} else {
		b.notify("⚡ zoro online ⚔️")
	}

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

	// Graceful shutdown: notify, stop accepting new work, finish queued/in-flight, mark clean.
	b.log.Info("draining before shutdown")
	b.notify("💤 zoro deteniéndose… (si es un reinicio, vuelvo en unos segundos)")
	b.collector.Stop() // stop buffering new input; queued jobs still drain below
	close(b.quit)
	<-workerDone
	_ = os.Remove(b.aliveFile)
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
	if !b.cfg.OwnerIDs[m.From.ID] {
		b.log.Warn("ignored non-owner", "from", m.From.ID, "username", m.From.Username)
		return
	}

	text := strings.TrimSpace(m.Text)
	// Telegram puts the text of a message WITH an attachment in Caption, not Text.
	// Promote a slash-command caption so commands (/btw, /voice, …) work with media too.
	if text == "" && strings.HasPrefix(strings.TrimSpace(m.Caption), "/") {
		text = strings.TrimSpace(m.Caption)
	}
	if strings.HasPrefix(text, "/") {
		fields := strings.Fields(text)
		cmd := strings.SplitN(fields[0], "@", 2)[0] // strip @botname
		switch cmd {
		case "/start", "/help":
			b.send(ctx, m.Chat.ID, helpText)
		case "/status":
			b.send(ctx, m.Chat.ID, b.statusText())
		case "/uso", "/usage":
			b.send(ctx, m.Chat.ID, usoText())
		case "/ls":
			b.send(ctx, m.Chat.ID, b.listDir(firstArg(text, fields[0])))
		case "/stats":
			out := b.runCmd(ctx, "bash", "-c", statsScript)
			b.reply(ctx, m.Chat.ID, "```\n"+out+"\n```")
		case "/newsession":
			if b.busy.Load() {
				b.send(ctx, m.Chat.ID, "⏳ Hay un turno corriendo. Usa /cancel o espera a que termine, luego /newsession.")
				return
			}
			st, err := b.store.New()
			if err != nil {
				b.send(ctx, m.Chat.ID, "⚠️ "+err.Error())
				return
			}
			b.send(ctx, m.Chat.ID, "🔄 New session — fresh memory.\n"+st.SessionID)
		case "/cancel":
			discarded := b.collector.Drop(m.Chat.ID) + b.drainJobs()
			b.cancel()
			reply := "✋ Cancelled the current task (if any)."
			if discarded > 0 {
				reply += fmt.Sprintf(" Dropped %d queued message(s) too.", discarded)
			}
			b.send(ctx, m.Chat.ID, reply)
		case "/restart":
			if b.busy.Load() {
				b.send(ctx, m.Chat.ID, "⏳ Hay un turno corriendo. Usa /cancel primero o espera a que termine, luego /restart.")
				return
			}
			b.send(ctx, m.Chat.ID, "♻️ Reiniciando…")
			go func() {
				time.Sleep(500 * time.Millisecond)
				_ = os.Remove(b.aliveFile) // clean marker so next start isn't reported as a crash
				os.Exit(0)
			}()
		case "/redeploy":
			go b.redeploy(m.Chat.ID)
		case "/update":
			go b.update(m.Chat.ID)
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
		case "/btw":
			// Quick side-question: runs in parallel on a throwaway Sonnet session,
			// so it never touches or blocks the main conversation.
			rest := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
			if rest == "" {
				b.send(ctx, m.Chat.ID, "Uso: /btw <pregunta> — consulta rápida en paralelo (no toca tu hilo principal).")
				return
			}
			go b.handleBtw(m.Chat.ID, rest)
		case "/crons", "/cron":
			// /crons          → list scheduled messages + next run
			// /crons <id>     → fire that cron right now (at the moment)
			if len(fields) >= 2 {
				id := fields[1]
				if id == "test" && len(fields) >= 3 { // tolerate legacy "/cron test <id>"
					id = fields[2]
				}
				j, ok := b.findCron(id)
				if !ok {
					b.send(ctx, m.Chat.ID, "⚠️ No encontré el cron \""+id+"\". Manda /crons para ver la lista.")
					return
				}
				b.send(ctx, m.Chat.ID, "🧪 Disparando cron \""+j.ID+"\" ahora…")
				b.fireCron(j)
				return
			}
			b.reply(ctx, m.Chat.ID, b.cronList())
		default:
			// Unknown slash command → pass it through to Claude (its own skills,
			// e.g. /deep-research, /code-review, run inside the turn). Bypass the
			// collector so a command isn't merged with unrelated buffered media.
			b.enqueue(job{chatID: m.Chat.ID, msgs: []*tg.Message{m}})
		}
		return
	}

	// Normal message → show "typing…" right away (the collector adds a short
	// debounce before the turn starts, so without this the indicator would lag),
	// then buffer it. The collector coalesces albums and quick bursts into a
	// single turn before enqueuing (see internal/collector).
	go b.tg.SendChatAction(context.Background(), m.Chat.ID, tg.ActionTyping)
	b.collector.Add(m)
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
	for {
		select {
		case j := <-b.jobs:
			b.process(context.Background(), j)
		case <-b.quit:
			// Shutting down: finish whatever is already queued, then stop. An
			// in-flight turn runs on context.Background() and completes; new input
			// is no longer buffered because the collector was stopped first.
			for {
				select {
				case j := <-b.jobs:
					b.process(context.Background(), j)
				default:
					return
				}
			}
		}
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

	b.busy.Store(true)
	defer b.busy.Store(false)

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
	if len(j.msgs) > 0 {
		p, notes, err := b.ingestMessages(ctx, j.msgs)
		if err != nil {
			b.send(parent, j.chatID, "⚠️ media error: "+err.Error())
			return
		}
		for _, n := range notes {
			b.send(parent, j.chatID, n)
		}
		prompt = p
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
	// Create collided with an existing session id — this happens when state got
	// stuck at created:false while pointing at a live transcript. The session
	// really exists, so resume it instead of bricking the turn with the raw
	// "Session ID … is already in use" error.
	if err != nil && !st.Created && strings.Contains(strings.ToLower(err.Error()), "already in use") {
		b.log.Warn("session id already exists on create, resuming instead", "id", st.SessionID)
		_ = b.store.MarkCreated()
		st = b.store.Current()
		res, err = b.cd.Run(ctx, st.SessionID, false, prompt, opts)
	}
	// Login died. The CLI itself ran fine — it just couldn't authenticate — so the
	// session is INTACT and must not be burned. Falling through to the resume-failed
	// branch below was silently throwing away the whole conversation on every token
	// hiccup, and reporting a bare "exit status 1" because auth failures print
	// NOTHING to stderr. Report the real reason and stop.
	if err != nil && res.Failure() == claude.FailAuth {
		// The CLI writes the transcript before failing, so the session id now exists:
		// adopt it as created, or the next turn re-creates it and hits "already in use".
		if res.SessionID != "" {
			_ = b.store.Set(res.SessionID, true)
		}
		b.log.Error("claude auth failure", "detail", res.Diagnostic())
		msg := b.failureText(res, err)
		b.send(parent, j.chatID, msg)
		b.mirror(parent, j, prompt, msg)
		return
	}
	if err != nil && st.Created {
		// Resume failed. Per rafiña: no silent retry — surface the real error and
		// tell him the thread was reset, then answer on a fresh session so he
		// always knows WHY the context is gone.
		resumeErr := err
		b.log.Warn("resume failed, starting new session", "err", err)
		if ns, nerr := b.store.New(); nerr == nil {
			st = ns
			b.send(parent, j.chatID, "⚠️ Couldn't resume the previous conversation (resume failed) — started a NEW one, so the prior thread is gone. Error:\n"+truncate(resumeErr.Error(), 800))
			res, err = b.cd.Run(ctx, st.SessionID, true, b.primeWithContext(prompt), opts)
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		msg := b.failureText(res, err)
		b.send(parent, j.chatID, msg)
		b.mirror(parent, j, prompt, msg)
		return
	}

	// Persist session continuity: always adopt the id the CLI actually used and
	// mark it created, so the next turn resumes instead of re-creating (which is
	// what left state stuck at created:false and caused "already in use").
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

	// Echo the turn to the watcher before delivering it, so the mirror is complete
	// even on the voice path below (which returns early).
	b.mirror(parent, j, prompt, reply)

	// The nightly close answers to the machine, not to the chat: mom and dad must
	// not get a 1am message. finishRollover reports to the watcher instead.
	if j.isRollover {
		b.finishRollover(parent, j, reply)
		b.sendOutbox(parent, j.chatID, before)
		return
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

// failureText turns a failed turn into something a human can act on. The CLI's own
// words (stdout) beat the process error, because the failures that matter most —
// the OAuth session dying above all — exit 1 with a COMPLETELY EMPTY stderr, which
// is why this used to surface as a bare "claude exited: exit status 1:".
func (b *Bot) failureText(res claude.Result, err error) string {
	switch res.Failure() {
	case claude.FailAuth:
		return "🔒 I died on login — my Claude session expired and couldn't be refreshed.\n\n" +
			"Nothing is lost: the conversation is intact and I'll pick it up as soon as you log in again " +
			"(on the VPS: run `claude` → `/login`).\n\n" +
			"CLI said: " + truncate(res.Diagnostic(), 400)
	case claude.FailLimit:
		return "🚦 I hit the usage limit — can't answer this turn.\n\nCLI said: " + truncate(res.Diagnostic(), 400)
	}
	if d := strings.TrimSpace(res.Diagnostic()); d != "" {
		return "⚠️ " + truncate(d, 1500)
	}
	return "⚠️ " + truncate(err.Error(), 1500)
}

// mirror echoes a finished turn to ZORO_MIRROR_CHAT_ID through this same bot, so
// rafiña can watch a sub-agent (sky, experienciaXXI) work with his mom/dad: who
// wrote, what they said, and what the agent answered. Off when unset.
func (b *Bot) mirror(ctx context.Context, j job, userText, reply string) {
	id := b.cfg.MirrorChatID
	if id == 0 || id == j.chatID {
		return // mirroring off, or the watcher is the one talking
	}
	// A machine-made turn shows its label; only a human's turn shows the text itself.
	shown := strings.TrimSpace(userText)
	if j.label != "" {
		shown = j.label
	}
	var sb strings.Builder
	sb.WriteString("🪞 " + b.cfg.AgentName + " · " + b.senderName(j) + "\n\n")
	sb.WriteString("👤 " + truncate(shown, 1500))
	if r := strings.TrimSpace(reply); r != "" {
		sb.WriteString("\n\n🤖 " + truncate(r, 2500))
	} else {
		sb.WriteString("\n\n🤖 (no reply)")
	}
	b.send(ctx, id, sb.String())
}

// finishRollover closes the day: it saves the brief the agent just wrote and rotates
// to a FRESH session, so tomorrow starts on clean context instead of an ever-growing
// transcript. The brief is what carries continuity across the cut — it's injected
// into the first message of the new session by primeWithContext.
//
// It reports to the mirror when there is one (rafiña), NOT to the chat: the whole
// point is that his mom and dad never see a 1am message. Any failure leaves the
// current session untouched — losing the thread silently would be worse than a
// transcript that grew one more day.
func (b *Bot) finishRollover(ctx context.Context, j job, brief string) {
	dest := b.cfg.MirrorChatID
	if dest == 0 {
		dest = j.chatID
	}
	brief = strings.TrimSpace(brief)
	if brief == "" {
		b.log.Warn("rollover produced no brief, keeping session")
		b.send(ctx, dest, "🌙 "+b.cfg.AgentName+": the close-of-day produced no brief — keeping the current session.")
		return
	}
	if err := os.WriteFile(b.cfg.BriefFile, []byte(brief), 0o644); err != nil {
		b.log.Warn("rollover: could not save brief", "err", err)
		b.send(ctx, dest, "🌙 "+b.cfg.AgentName+": couldn't save the brief ("+err.Error()+") — keeping the current session.")
		return
	}
	st, err := b.store.New()
	if err != nil {
		b.log.Warn("rollover: could not rotate session", "err", err)
		b.send(ctx, dest, "🌙 "+b.cfg.AgentName+": brief saved, but the session did NOT rotate ("+err.Error()+").")
		return
	}
	b.log.Info("rollover done", "session", st.SessionID)
	b.send(ctx, dest, "🌙 Cierre del día — "+b.cfg.AgentName+"\n\n"+brief+
		"\n\n———\n🧹 Sesión nueva, contexto limpio. Este brief entra en el primer mensaje de mañana.")
}

// senderName labels a mirrored turn with whoever caused it. Jobs with no source
// message are scheduled ones.
//
// The primary owner is labelled with ZORO_OWNER_NAME instead of their Telegram
// first name: rafiña's dad is literally called "Rafa" there, so the mirror of
// experienciaXXI read as if rafiña had written the messages himself.
func (b *Bot) senderName(j job) string {
	for _, m := range j.msgs {
		if m.From == nil {
			continue
		}
		if m.From.ID == b.cfg.OwnerID {
			if n := strings.TrimSpace(b.cfg.OwnerName); n != "" {
				return n
			}
		}
		if n := strings.TrimSpace(m.From.FirstName); n != "" {
			return n
		}
		if n := strings.TrimSpace(m.From.Username); n != "" {
			return n
		}
	}
	return "⏰ cron"
}

// systemPrompt reads the agent's soul FRESH each turn from ~/.zoro/soul.md (identity +
// behavior). Editing it takes effect on the next message, no restart needed.
func (b *Bot) systemPrompt() string {
	return readFile(b.cfg.SoulFile)
}

// primeWithContext prepends the durable background (~/.zoro/context.md) to the first
// message of a new session, so it enters the conversation history once. Returns the
// prompt unchanged if there is no context file.
//
// The owner is named from ZORO_OWNER_NAME, NOT hardcoded: sub-agents serve someone
// else, and "rafiña" baked in here is why Sky greeted rafiña's mom by his nickname.
func (b *Bot) primeWithContext(userPrompt string) string {
	c := readFile(b.cfg.ContextFile)
	if c == "" {
		return userPrompt
	}
	who := b.cfg.OwnerName
	out := "[SESSION CONTEXT — durable background about " + who + ", loaded once at the start of this conversation. Your identity and behavior rules are in your system prompt.]\n\n" + c
	// The nightly rollover cuts the transcript; this brief is what carries the
	// thread across the cut, so a clean session doesn't mean a forgetful one.
	if brief := readFile(b.cfg.BriefFile); brief != "" {
		out += "\n\n---\n\n[BRIEF — cómo cerró el día anterior y en qué se quedó la conversación. Escrito por ti mismo en el cierre nocturno.]\n\n" + brief
	}
	return out + "\n\n---\n\n" + who + ": " + userPrompt
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
			// Shrink oversized images first, then route by size: Telegram's
			// sendPhoto silently rejects files over 10 MiB, so anything still
			// above that goes out as a document (preserved, up to 50 MiB).
			send := media.OptimizeImage(f)
			if fi, serr := os.Stat(send); serr == nil && fi.Size() > media.PhotoMaxBytes {
				err = b.tg.SendDocument(ctx, chatID, send, "")
			} else {
				err = b.tg.SendPhoto(ctx, chatID, send, "")
			}
			if send != f {
				os.Remove(send)
			}
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

// drainJobs removes every queued (not-yet-started) job without running it and
// returns how many were dropped. The in-flight turn, if any, is stopped
// separately via cancel.
func (b *Bot) drainJobs() int {
	n := 0
	for {
		select {
		case <-b.jobs:
			n++
		default:
			return n
		}
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
		{Command: "btw", Description: "Quick side-question in parallel: /btw <question>"},
		{Command: "crons", Description: "List scheduled messages and next run"},
		{Command: "status", Description: "Show session, model, effort, uptime"},
		{Command: "uso", Description: "Claude Code usage (5h / 7d rate limits)"},
		{Command: "ls", Description: "List VM files: /ls [path]"},
		{Command: "stats", Description: "VM + git repo status"},
		{Command: "cancel", Description: "Cancel the current task"},
		{Command: "redeploy", Description: "Rebuild engine + restart (apply code changes)"},
		{Command: "update", Description: "Pull latest code from GitHub + rebuild + restart"},
		{Command: "restart", Description: "Restart the daemon"},
		{Command: "help", Description: "Show help"},
	}
	if err := b.tg.SetMyCommands(ctx, cmds); err != nil {
		b.log.Warn("setMyCommands failed", "err", err)
	}
}

// ingestMessages downloads every message's attachments and assembles one turn
// prompt: concatenated texts/captions, then any voice transcripts, then a single
// line listing all attached files. Notes (skipped files, transcription errors)
// are returned for the caller to surface to the user.
func (b *Bot) ingestMessages(ctx context.Context, msgs []*tg.Message) (prompt string, notes []string, err error) {
	var texts, transcripts, files []string
	for _, m := range msgs {
		in, ierr := media.Ingest(ctx, b.mediaC, b.tg, m)
		if ierr != nil {
			return "", nil, ierr
		}
		if t := msgText(m); t != "" {
			texts = append(texts, t)
		}
		files = append(files, in.Files...)
		notes = append(notes, in.Notes...)
		if in.Transcript != "" {
			transcripts = append(transcripts, in.Transcript)
		}
	}
	return buildPrompt(texts, transcripts, files), notes, nil
}

// buildPrompt assembles the user-visible turn text from its collected parts.
func buildPrompt(texts, transcripts, files []string) string {
	var parts []string
	if t := strings.Join(texts, "\n\n"); t != "" {
		parts = append(parts, t)
	}
	for _, tr := range transcripts {
		parts = append(parts, "[Voice note transcript]: "+tr)
	}
	if len(files) > 0 {
		parts = append(parts, fmt.Sprintf(
			"[The user attached %d file(s). Use the Read tool to view them: %s]",
			len(files), strings.Join(files, ", ")))
	}
	return strings.Join(parts, "\n\n")
}

// msgText returns a message's text, falling back to its caption.
func msgText(m *tg.Message) string {
	if t := strings.TrimSpace(m.Text); t != "" {
		return t
	}
	return strings.TrimSpace(m.Caption)
}

// firstArg returns the first whitespace-separated argument after cmdToken in text.
func firstArg(text, cmdToken string) string {
	rest := strings.TrimSpace(strings.TrimPrefix(text, cmdToken))
	if rest == "" {
		return ""
	}
	return strings.Fields(rest)[0]
}

// notify sends a lifecycle message to the owner, out-of-band of any turn.
func (b *Bot) notify(text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := b.tg.SendMessage(ctx, b.cfg.OwnerID, text, ""); err != nil {
		b.log.Warn("notify failed", "err", err)
	}
}

// handleBtw answers a side-question in PARALLEL with the main worker: a throwaway
// Opus session (not stored, not primed with context.md) so it never pollutes or
// blocks the main conversation. It may research freely (launch tasks/workflows),
// so there is NO deadline — it runs on context.Background() like the main worker
// and always delivers, however long it takes. Runs in its own goroutine straight
// from dispatch — outside the serialized job queue.
func (b *Bot) handleBtw(chatID int64, question string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := b.typingPump(ctx, chatID, tg.ActionTyping)
	defer stop()

	opts := claude.RunOpts{Model: "opus", Effort: "medium", SystemPrompt: b.systemPrompt()}
	res, err := b.cd.Run(ctx, uid.New(), true, question, opts)
	stop()
	if err != nil {
		b.send(ctx, chatID, "💭 btw → ⚠️ "+truncate(err.Error(), 800))
		return
	}
	reply := strings.TrimSpace(res.Text)
	if reply == "" {
		reply = "(sin respuesta)"
	}
	b.reply(ctx, chatID, "💭 *btw* →\n\n"+reply)
}

// fireCron enqueues a scheduled job's prompt as a normal turn to the owner. The
// wrapper tells the model this is an automatic trigger so it replies concisely.
func (b *Bot) fireCron(j cron.Job) {
	if j.Rollover {
		// The rollover prompt is an instruction to the agent, not a reminder to the
		// owner, so it skips the "mándame el resultado como un aviso" framing.
		b.enqueue(job{
			chatID:     b.cfg.OwnerID,
			prompt:     "[🌙 Cierre del día \"" + j.ID + "\" — tarea interna, automática. No es un mensaje de tu usuario.]\n\n" + j.Prompt,
			label:      "🌙 cierre del día (" + j.ID + ")",
			isRollover: true,
		})
		return
	}
	prompt := "[⏰ Cron automático \"" + j.ID + "\" — disparado a su hora programada (CDMX). " +
		"Atiende esto y mándame el resultado de forma breve, como un recordatorio/aviso:]\n\n" + j.Prompt
	b.enqueue(job{chatID: b.cfg.OwnerID, prompt: prompt, label: "⏰ cron \"" + j.ID + "\""})
}

// findCron loads crons.json and returns the entry matching ref, which can be a
// 1-based list number (e.g. "2") or the slug id (e.g. "gastos-hoy").
func (b *Bot) findCron(ref string) (cron.Job, bool) {
	f, err := cron.Load(b.cfg.CronFile)
	if err != nil {
		return cron.Job{}, false
	}
	if n, err := strconv.Atoi(ref); err == nil { // numeric position
		if n >= 1 && n <= len(f.Crons) {
			return f.Crons[n-1], true
		}
		return cron.Job{}, false
	}
	for _, j := range f.Crons {
		if j.ID == ref {
			return j, true
		}
	}
	return cron.Job{}, false
}

// cronList renders the configured crons as a clean, numbered list (Markdown).
// The number is what /crons <n> fires, so it doubles as a quick-fire index.
func (b *Bot) cronList() string {
	f, err := cron.Load(b.cfg.CronFile)
	if err != nil {
		return "⚠️ crons.json: " + err.Error()
	}
	if len(f.Crons) == 0 {
		return "📭 No tienes crons todavía.\nSe definen en `" + b.cfg.CronFile + "`."
	}
	loc := f.Location()
	var sb strings.Builder
	sb.WriteString("⏰ *Tus crons* · horario CDMX\n")
	sb.WriteString("──────────────\n")
	for i, j := range f.Crons {
		dot := "🟢"
		if !j.Enabled {
			dot = "⚪️"
		}
		fmt.Fprintf(&sb, "\n*%d.* %s  *%s*\n", i+1, dot, j.ID)
		fmt.Fprintf(&sb, "🗓 %s", humanWhen(j.Schedule))
		if runs, rerr := cron.NextRuns(j, loc, 1); rerr == nil && len(runs) > 0 {
			fmt.Fprintf(&sb, "  ·  próxima: %s", humanNext(runs[0], loc))
		}
		fmt.Fprintf(&sb, "\n💬 %s\n", truncate(oneLine(j.Prompt), 90))
	}
	sb.WriteString("\n──────────────\n")
	fmt.Fprintf(&sb, "_Dispara uno ya:_ `/crons 1`  _o_  `/crons %s`", f.Crons[0].ID)
	return sb.String()
}

// humanWhen turns a 5-field cron spec into plain Spanish ("todos los días 8:30",
// "martes 6:30", "L–V 9:00"). Falls back to the raw spec for exotic schedules.
func humanWhen(schedule string) string {
	p := strings.Fields(schedule)
	if len(p) != 5 {
		return schedule
	}
	hm := schedule
	if h, e1 := strconv.Atoi(p[1]); e1 == nil {
		if m, e2 := strconv.Atoi(p[0]); e2 == nil {
			hm = fmt.Sprintf("%d:%02d", h, m)
		}
	}
	when := "todos los días"
	if p[2] == "*" && p[3] == "*" {
		switch p[4] {
		case "*":
			when = "todos los días"
		case "1-5":
			when = "L–V"
		case "6-0", "0,6", "6,0":
			when = "fines de semana"
		case "0", "7":
			when = "domingos"
		case "1":
			when = "lunes"
		case "2":
			when = "martes"
		case "3":
			when = "miércoles"
		case "4":
			when = "jueves"
		case "5":
			when = "viernes"
		case "6":
			when = "sábados"
		default:
			when = "días " + p[4]
		}
	}
	return when + " " + hm
}

// humanNext renders the next run relative to now ("hoy 22:30", "mañana 08:30").
func humanNext(t time.Time, loc *time.Location) string {
	now := time.Now().In(loc)
	y1, m1, d1 := t.Date()
	y2, m2, d2 := now.Date()
	y3, m3, d3 := now.AddDate(0, 0, 1).Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return "hoy " + t.Format("15:04")
	case y1 == y3 && m1 == m3 && d1 == d3:
		return "mañana " + t.Format("15:04")
	default:
		return t.Format("02 Jan 15:04")
	}
}

// oneLine collapses whitespace/newlines so a prompt fits on one summary line.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// goBin resolves the Go toolchain across platforms: $GO override, then PATH, then
// common install locations (Homebrew on macOS, /usr/local/go on Linux).
func goBin() string {
	if g := os.Getenv("GO"); g != "" {
		return g
	}
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	for _, p := range []string{"/opt/homebrew/bin/go", "/usr/local/go/bin/go"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "/usr/local/go/bin/go"
}

// restartDetached relaunches the daemon out-of-band so the in-flight reply lands
// first and this process can exit. Linux uses systemd (sudo); macOS uses the
// per-user launchd agent (no sudo). The child is detached so it outlives us.
func (b *Bot) restartDetached() {
	if runtime.GOOS == "darwin" {
		c := exec.Command("bash", "-c", "sleep 1; launchctl kickstart -k gui/$(id -u)/com.zoro.agent >/dev/null 2>&1")
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		_ = c.Start()
		return
	}
	// Linux: systemd brings up the new binary; lifecycle messages (💤 → ⚡) confirm it.
	_ = exec.Command("setsid", "bash", "-c", "sleep 1; sudo systemctl restart zoro >/dev/null 2>&1").Start()
}

// buildAndRestart rebuilds the engine and, on success, restarts the daemon. On
// build failure it reports the error and does NOT restart.
func (b *Bot) buildAndRestart(chatID int64) {
	b.send(context.Background(), chatID, "🔧 Rebuilding zoro…")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, goBin(), "build", "-o", "bin/zoro", "./cmd/zoro")
	cmd.Dir = b.cfg.EngineDir
	if out, err := cmd.CombinedOutput(); err != nil {
		b.send(context.Background(), chatID, "❌ Build falló — NO reinicio:\n"+truncate(strings.TrimSpace(string(out)), 1500))
		return
	}
	b.send(context.Background(), chatID, "✅ Build OK — aplicando (reinicio)…")
	b.restartDetached()
}

// redeploy rebuilds the current working tree and restarts (apply local changes).
func (b *Bot) redeploy(chatID int64) { b.buildAndRestart(chatID) }

// update fast-forwards the engine repo from GitHub, then rebuilds and restarts.
// Use it to roll out code pushed from elsewhere (e.g. another zoro instance).
func (b *Bot) update(chatID int64) {
	b.send(context.Background(), chatID, "⬇️ Pulling latest from GitHub…")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pull := exec.CommandContext(ctx, "git", "-C", b.cfg.EngineDir, "pull", "--ff-only")
	out, err := pull.CombinedOutput()
	res := strings.TrimSpace(string(out))
	if err != nil {
		b.send(context.Background(), chatID, "❌ git pull falló — NO rebuildeo:\n"+truncate(res, 1200))
		return
	}
	b.send(context.Background(), chatID, "📥 "+truncate(res, 800))
	b.buildAndRestart(chatID)
}

// listDir returns a clean, scannable listing: dirs first (📁), then files (📄), names only.
func (b *Bot) listDir(path string) string {
	if path == "" {
		path = b.cfg.WorkDir
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(b.cfg.WorkDir, path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return "⚠️ " + err.Error()
	}
	var dirs, files []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, "📁 "+e.Name()+"/")
		} else {
			files = append(files, "📄 "+e.Name())
		}
	}
	sort.Strings(dirs)
	sort.Strings(files)
	var sb strings.Builder
	fmt.Fprintf(&sb, "📂 %s  —  %d dirs · %d files\n\n", path, len(dirs), len(files))
	for _, d := range dirs {
		sb.WriteString(d + "\n")
	}
	if len(dirs) > 0 && len(files) > 0 {
		sb.WriteString("\n")
	}
	for _, f := range files {
		sb.WriteString(f + "\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// runCmd runs a command (cwd = workdir) with a 30s timeout and returns combined output.
func (b *Bot) runCmd(parent context.Context, name string, args ...string) string {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = b.cfg.WorkDir
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		if s != "" {
			s += "\n"
		}
		s += "⚠️ " + err.Error()
	}
	if s == "" {
		s = "(no output)"
	}
	return s
}

// statsScript gathers VM + git-repo status for /stats.
const statsScript = `
echo "🖥️  VM"
echo "uptime: $(uptime -p | sed 's/^up //')"
echo "load:   $(cut -d' ' -f1-3 /proc/loadavg)"
free -h | awk 'NR==1 || /^Mem:/ {print}'
df -h / | awk 'NR==1 || NR==2 {print}'
echo
echo "🔧 services"
for s in zoro daph; do printf "  %-7s %s\n" "$s" "$(systemctl is-active $s 2>/dev/null)"; done
echo
echo "📦 git repos under $HOME (dirty / unpushed)"
found=0
for d in $(find "$HOME" -maxdepth 3 -type d -name .git -not -path '*/node_modules/*' 2>/dev/null); do
  r=$(dirname "$d")
  dirty=$(git -C "$r" status --porcelain 2>/dev/null | wc -l | tr -d ' ')
  branch=$(git -C "$r" rev-parse --abbrev-ref HEAD 2>/dev/null)
  ahead=$(git -C "$r" rev-list --count '@{u}..HEAD' 2>/dev/null || echo '-')
  if [ "$dirty" != "0" ] || { [ "$ahead" != "0" ] && [ "$ahead" != "-" ]; }; then
    printf "  • %-22s [%s] uncommitted:%s unpushed:%s\n" "${r#$HOME/}" "$branch" "$dirty" "$ahead"
    found=1
  fi
done
if [ "$found" = "0" ]; then echo "  ✅ todo limpio y al día"; fi
`

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
