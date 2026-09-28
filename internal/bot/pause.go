package bot

// Pause / resume (pedido de rafiña, 28-sep-2026). Every agent on the VPS logs in
// to the SAME Claude account, so they share one weekly limit. When it ran out
// (26-sep 22:32 → 27-sep 23:00 CDMX) every cron and message forwarded "You've hit
// your weekly limit" to Telegram — Sky forwarded it to his mom — and each failure
// threw the session away. Now:
//
//   - <state>/paused (JSON) means this engine makes NO Claude call at all: crons are
//     skipped, owner messages go to <state>/pending.jsonl and get a canned ack
//     (ZORO_PAUSE_REPLY, at most once per 30 min per chat).
//   - A limit error writes that flag itself, keeps the session, forwards nothing.
//   - Flag gone + queue not empty → ONE turn that bundles the queued messages; the
//     queue is cleared only when that turn succeeds.
//   - Zoro only: /stop and /start flip the flag of every agent in ZORO_AGENTS (their
//     dirs belong to other users → sudo), and a limit seen anywhere pauses everyone
//     with ONE message to rafiña.
//
// The flag is a plain file, so it survives restarts (the nightly update restarts
// every agent) — nothing but /start ever removes it.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"zoro/internal/claude"
	"zoro/internal/config"
)

const (
	pauseFileName   = "paused"
	pendingFileName = "pending.jsonl"

	reasonLimit = "limit" // the Claude subscription limit paused us
	reasonStop  = "stop"  // rafiña sent /stop

	ackEvery    = 30 * time.Minute
	resumeEvery = 30 * time.Second
	watchEvery  = 2 // resume ticks between two looks at the other agents' flags (~60 s)
)

// pauseFlag is <state>/paused.
type pauseFlag struct {
	Since  string `json:"since"`            // RFC3339, UTC
	Reason string `json:"reason"`           // reasonLimit | reasonStop
	Resets string `json:"resets,omitempty"` // as the CLI said it: "11pm (America/Mexico_City)"
	Detail string `json:"detail,omitempty"` // the CLI's own words / who sent /stop
	Agent  string `json:"agent"`            // which engine wrote it
}

// pendingMsg is one line of <state>/pending.jsonl: an owner message that arrived
// while paused (or died on the limit), kept for the resume turn.
type pendingMsg struct {
	ChatID      int64    `json:"chat_id"`
	From        string   `json:"from"`
	At          string   `json:"at"` // CDMX, "2006-01-02 15:04"
	Text        string   `json:"text,omitempty"`
	Transcripts []string `json:"transcripts,omitempty"`
	Files       []string `json:"files,omitempty"`
}

var cdmx = func() *time.Location {
	if loc, err := time.LoadLocation("America/Mexico_City"); err == nil {
		return loc
	}
	return time.FixedZone("CDMX", -6*3600)
}()

func (b *Bot) isZoro() bool        { return b.cfg.AgentName == "zoro" }
func (b *Bot) pauseFile() string   { return filepath.Join(b.cfg.StateDir, pauseFileName) }
func (b *Bot) pendingFile() string { return filepath.Join(b.cfg.StateDir, pendingFileName) }

// paused is checked before every Claude call; a stat is cheap enough for that.
func (b *Bot) paused() bool {
	_, err := os.Stat(b.pauseFile())
	return err == nil
}

func readPause(path string) (pauseFlag, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return pauseFlag{}, false
	}
	return parsePause(raw), true
}

// parsePause never fails: a flag that exists but can't be read still means paused.
func parsePause(raw []byte) pauseFlag {
	var f pauseFlag
	if json.Unmarshal(raw, &f) != nil || f.Reason == "" {
		f.Reason = reasonStop
	}
	return f
}

func (b *Bot) newPause(reason, resets, detail string) pauseFlag {
	return pauseFlag{Since: time.Now().UTC().Format(time.RFC3339), Reason: reason, Resets: resets, Detail: detail, Agent: b.cfg.AgentName}
}

func (b *Bot) writeOwnPause(f pauseFlag) error {
	raw, _ := json.MarshalIndent(f, "", "  ")
	tmp := b.pauseFile() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, b.pauseFile())
}

func appendPending(path string, p pendingMsg) error {
	raw, _ := json.Marshal(p)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// readPending returns the queue in arrival order; a missing file is an empty queue.
func readPending(path string) ([]pendingMsg, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []pendingMsg
	for _, line := range strings.Split(string(raw), "\n") {
		var p pendingMsg
		if strings.TrimSpace(line) == "" || json.Unmarshal([]byte(line), &p) != nil {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// consumePending drops the first n messages — the ones the resume turn just
// delivered. Anything queued after them stays for the next round.
func (b *Bot) consumePending(n int) error {
	msgs, err := readPending(b.pendingFile())
	if err != nil {
		return err
	}
	if n >= len(msgs) {
		return os.Remove(b.pendingFile())
	}
	var sb strings.Builder
	for _, p := range msgs[n:] {
		raw, _ := json.Marshal(p)
		sb.Write(raw)
		sb.WriteByte('\n')
	}
	tmp := b.pendingFile() + ".tmp"
	if err := os.WriteFile(tmp, []byte(sb.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, b.pendingFile())
}

// heldMsg is what an owner job leaves in the queue. ok=false for machine jobs
// (crons, rollover, /compact, the resume bundle itself): those are just dropped.
func (b *Bot) heldMsg(j job, texts, transcripts, files []string) (pendingMsg, bool) {
	if j.label != "" || j.isCompact || j.isPending {
		return pendingMsg{}, false
	}
	from := b.cfg.OwnerName
	if len(j.msgs) > 0 {
		from = b.senderName(j)
	}
	return pendingMsg{
		ChatID:      j.chatID,
		From:        from,
		At:          time.Now().In(cdmx).Format("2006-01-02 15:04"),
		Text:        strings.Join(texts, "\n\n"),
		Transcripts: transcripts,
		Files:       files,
	}, true
}

// hold saves an owner message for later and acks it.
func (b *Bot) hold(ctx context.Context, p pendingMsg) {
	if err := appendPending(b.pendingFile(), p); err != nil {
		b.log.Error("paused: could not save message", "err", err)
		return
	}
	b.log.Info("paused: message saved for later", "chat", p.ChatID)
	b.ackPaused(ctx, p.ChatID)
}

// ackPaused sends ZORO_PAUSE_REPLY, at most once per ackEvery per chat, so a burst
// of messages during a long pause doesn't get a burst of identical answers.
func (b *Bot) ackPaused(ctx context.Context, chatID int64) {
	if b.cfg.PauseReply == "" {
		return
	}
	b.ackMu.Lock()
	if b.acked == nil {
		b.acked = map[int64]time.Time{}
	}
	if time.Since(b.acked[chatID]) < ackEvery {
		b.ackMu.Unlock()
		return
	}
	b.acked[chatID] = time.Now()
	b.ackMu.Unlock()
	b.send(ctx, chatID, b.cfg.PauseReply)
}

// greetBack (28-sep-2026, pedido de rafiña: "al start debería mandar mensaje que
// revivió, en plan amigable"): once the pause is over, every chat we told "I'm
// paused" — or that left a saved message — gets ZORO_RESUME_REPLY ONCE, before the
// saved messages are answered. Nobody who didn't notice the pause gets pinged.
func (b *Bot) greetBack(pending []pendingMsg) {
	b.ackMu.Lock()
	chats := make([]int64, 0, len(b.acked)+len(pending))
	seen := map[int64]bool{}
	for id := range b.acked {
		if !seen[id] {
			seen[id] = true
			chats = append(chats, id)
		}
	}
	for _, m := range pending {
		if m.ChatID != 0 && !seen[m.ChatID] && !b.greeted[m.ChatID] {
			seen[m.ChatID] = true
			chats = append(chats, m.ChatID)
		}
	}
	b.acked = nil
	if b.greeted == nil {
		b.greeted = map[int64]bool{}
	}
	for _, id := range chats {
		b.greeted[id] = true // pending lines stay until their turn succeeds: greet once
	}
	b.ackMu.Unlock()
	if b.cfg.ResumeReply == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, id := range chats {
		b.send(ctx, id, b.cfg.ResumeReply)
	}
}

// pendingPrompt bundles the queue into one turn.
func pendingPrompt(msgs []pendingMsg) string {
	var sb strings.Builder
	sb.WriteString("[While you were paused these messages arrived (CDMX times):\n")
	for _, m := range msgs {
		fmt.Fprintf(&sb, "\n• %s — %s:", m.At, m.From)
		if m.Text != "" {
			sb.WriteString(" " + m.Text)
		}
		for _, t := range m.Transcripts {
			sb.WriteString("\n  [Voice note transcript]: " + t)
		}
		if len(m.Files) > 0 {
			sb.WriteString("\n  [Attached file(s) — use the Read tool: " + strings.Join(m.Files, ", ") + "]")
		}
	}
	sb.WriteString("\n\nPick them up.]")
	return sb.String()
}

// onLimit handles a turn that died on the subscription limit: pause (keeping the
// session — the caller already adopted it), save the owner's message, forward
// nothing raw. Zoro also pauses everyone else and tells rafiña once.
func (b *Bot) onLimit(ctx context.Context, j job, res claude.Result, held *pendingMsg) {
	was := b.paused()
	f := b.newPause(reasonLimit, res.LimitResets(), truncate(res.Diagnostic(), 300))
	if err := b.writeOwnPause(f); err != nil {
		b.log.Error("limit hit but could not write the pause flag", "err", err)
	}
	b.log.Warn("claude usage limit hit — paused, session kept", "resets", f.Resets, "job", j.label)
	switch {
	case held != nil:
		b.hold(ctx, *held)
	case j.isPending:
		b.log.Info("limit hit on the resume turn — queue kept for the next /start")
	default:
		b.log.Info("limit hit on a machine turn — dropped", "job", j.label)
	}
	if b.isZoro() && !was {
		b.pauseEveryoneForLimit(ctx, f)
	}
}

var limitKindRe = regexp.MustCompile(`(?i)hit your ([^\n·]{0,40}?limit)`)

// pauseEveryoneForLimit (Zoro) pauses the other agents and sends rafiña the ONE
// message of this pause. Callers guarantee Zoro wasn't paused before, which is
// what keeps it from repeating.
func (b *Bot) pauseEveryoneForLimit(ctx context.Context, f pauseFlag) {
	names := []string{b.cfg.AgentName}
	ok, fails := b.pauseAgents(ctx, f)
	names = append(names, ok...)

	kind := "Claude usage limit"
	if m := limitKindRe.FindStringSubmatch(f.Detail); m != nil {
		kind = strings.ToUpper(m[1][:1]) + m[1][1:]
	}
	when := ""
	if f.Resets != "" {
		when = " (resets " + f.Resets + ")"
	}
	msg := "⏸️ " + kind + " hit" + when + ". Paused " + joinAnd(names) +
		" so nobody spams errors. Messages are being saved. Send /start when you want us back" +
		" — or /auth mimo and /start to keep going on MiMo."
	if len(fails) > 0 {
		msg += "\n⚠️ Couldn't pause: " + strings.Join(fails, "; ")
	}
	b.send(ctx, b.cfg.OwnerID, msg)
}

// watchAgents (Zoro, ~every 60 s): a sub-agent that hits the limit only pauses
// itself; noticing its flag here is what pauses the rest and tells rafiña.
func (b *Bot) watchAgents(ctx context.Context) {
	if b.paused() {
		return // already paused: the message went out when that happened
	}
	for _, a := range b.cfg.Agents {
		f, ok := b.agentFlag(ctx, a)
		if !ok || f.Reason != reasonLimit {
			continue
		}
		b.log.Warn("agent hit the usage limit — pausing everyone", "agent", a.Name)
		if err := b.writeOwnPause(f); err != nil {
			b.log.Error("could not write own pause flag", "err", err)
		}
		b.pauseEveryoneForLimit(ctx, f)
		return
	}
}

// pauseLoop resumes (and, on Zoro, watches the others) until ctx ends.
func (b *Bot) pauseLoop(ctx context.Context) {
	t := time.NewTicker(resumeEvery)
	defer t.Stop()
	for n := 1; ; n++ {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		b.maybeResume()
		if b.isZoro() && n%watchEvery == 0 {
			b.watchAgents(ctx)
		}
	}
}

// maybeResume enqueues the bundle turn once the flag is gone. resumeQueued keeps a
// slow turn from getting a second copy queued behind it.
func (b *Bot) maybeResume() {
	if b.paused() {
		b.ackMu.Lock()
		b.greeted = nil // a pause is on: whoever notices it gets greeted when it ends
		b.ackMu.Unlock()
		return
	}
	if b.resumeQueued.Load() {
		return
	}
	msgs, err := readPending(b.pendingFile())
	b.greetBack(msgs)
	if err != nil || len(msgs) == 0 {
		return
	}
	chat := msgs[0].ChatID
	for _, m := range msgs {
		if m.ChatID != chat {
			chat = b.cfg.OwnerID // several chats wrote: answer the primary owner
			break
		}
	}
	b.resumeQueued.Store(true)
	select {
	case b.jobs <- job{chatID: chat, isPending: true, label: "⏯️ messages saved during the pause"}:
		b.log.Info("resuming: picking up saved messages", "n", len(msgs))
	default:
		b.resumeQueued.Store(false) // queue full; next tick
	}
}

// --- Zoro: /stop and /start for every agent -------------------------------------

func defaultSudo(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "sudo", append([]string{"-n"}, args...)...).CombinedOutput()
}

// readAgentFile reads a file in another agent's state dir. Those dirs are world-
// readable, so the ~60 s watcher reads them directly (sudo twice a minute would
// flood the auth log); sudo is only the fallback when a read is denied.
func (b *Bot) readAgentFile(ctx context.Context, path string) ([]byte, bool) {
	raw, err := os.ReadFile(path)
	if err == nil {
		return raw, true
	}
	if os.IsNotExist(err) {
		return nil, false
	}
	out, err := b.sudo(ctx, "cat", path)
	return out, err == nil
}

// agentFlag reads another agent's flag (its dir belongs to its own unix user).
func (b *Bot) agentFlag(ctx context.Context, a config.Agent) (pauseFlag, bool) {
	raw, ok := b.readAgentFile(ctx, filepath.Join(a.StateDir, pauseFileName))
	if !ok {
		return pauseFlag{}, false
	}
	return parsePause(raw), true
}

func (b *Bot) writeAgentFlag(ctx context.Context, a config.Agent, f pauseFlag) error {
	raw, _ := json.MarshalIndent(f, "", "  ")
	tmp, err := os.CreateTemp("", "zoro-paused-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	out, err := b.sudo(ctx, "install", "-o", a.Name, "-g", a.Name, "-m", "644", tmp.Name(), filepath.Join(a.StateDir, pauseFileName))
	if err != nil {
		return fmt.Errorf("%v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (b *Bot) agentPending(ctx context.Context, a config.Agent) int {
	out, ok := b.readAgentFile(ctx, filepath.Join(a.StateDir, pendingFileName))
	if !ok {
		return 0
	}
	n := 0
	for _, l := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

// pauseAgents writes f into every agent that isn't paused yet (an existing flag —
// e.g. the agent's own limit flag — is left as it is).
func (b *Bot) pauseAgents(ctx context.Context, f pauseFlag) (ok, fails []string) {
	for _, a := range b.cfg.Agents {
		if _, already := b.agentFlag(ctx, a); already {
			ok = append(ok, a.Name)
			continue
		}
		if err := b.writeAgentFlag(ctx, a, f); err != nil {
			b.log.Warn("could not pause agent", "agent", a.Name, "err", err)
			fails = append(fails, a.Name+": "+truncate(err.Error(), 120))
			continue
		}
		ok = append(ok, a.Name)
	}
	return ok, fails
}

// stopAll is /stop (Zoro).
func (b *Bot) stopAll(ctx context.Context, chatID int64) {
	f := b.newPause(reasonStop, "", "/stop from Telegram")
	var names, fails []string
	if b.paused() {
		names = append(names, b.cfg.AgentName)
	} else if err := b.writeOwnPause(f); err != nil {
		fails = append(fails, b.cfg.AgentName+": "+err.Error())
	} else {
		names = append(names, b.cfg.AgentName)
	}
	ok, afails := b.pauseAgents(ctx, f)
	names, fails = append(names, ok...), append(fails, afails...)
	msg := "⏸️ Paused: " + strings.Join(names, ", ") + ". Messages get saved; /start brings everyone back."
	if len(fails) > 0 {
		msg += "\n⚠️ Couldn't pause: " + strings.Join(fails, "; ")
	}
	b.log.Info("/stop", "paused", names, "failed", fails)
	b.send(ctx, chatID, msg)
}

// startAll is /start (Zoro): remove every flag and report what is waiting.
func (b *Bot) startAll(ctx context.Context, chatID int64) {
	var back, fails []string
	pending := 0
	if b.paused() {
		if err := os.Remove(b.pauseFile()); err != nil {
			fails = append(fails, b.cfg.AgentName+": "+err.Error())
		} else {
			back = append(back, b.cfg.AgentName)
		}
	}
	if msgs, _ := readPending(b.pendingFile()); len(msgs) > 0 {
		pending += len(msgs)
	}
	for _, a := range b.cfg.Agents {
		pending += b.agentPending(ctx, a)
		if _, ok := b.agentFlag(ctx, a); !ok {
			continue
		}
		if out, err := b.sudo(ctx, "rm", "-f", filepath.Join(a.StateDir, pauseFileName)); err != nil {
			fails = append(fails, a.Name+": "+truncate(strings.TrimSpace(err.Error()+" "+string(out)), 120))
			continue
		}
		back = append(back, a.Name)
	}
	var msg string
	switch {
	case len(back) == 0 && pending == 0:
		msg = "⚔️ We were never down, bro — everyone's already running."
	case len(back) == 0:
		msg = fmt.Sprintf("⚔️ Nobody was paused — the %d saved message(s) get picked up in the next ~30 s.", pending)
	case pending == 0:
		msg = "⚔️ Back, bro! " + humanList(back) + " " + areAwake(len(back)) + " — nothing was waiting. Let's keep going."
	default:
		msg = fmt.Sprintf("⚔️ Back, bro! %s %s — picking up the %d message(s) that came in meanwhile. Let's keep going.", humanList(back), areAwake(len(back)), pending)
	}
	if len(fails) > 0 {
		msg += "\n⚠️ Couldn't resume: " + strings.Join(fails, "; ")
	}
	b.log.Info("/start", "resumed", back, "pending", pending, "failed", fails)
	b.send(ctx, chatID, msg)
	b.maybeResume() // Zoro's own queue goes now; the others' within ~30 s
}

// humanList turns ["zoro","sky","tequila"] into "Zoro, Sky and Tequila".
func humanList(names []string) string {
	c := make([]string, len(names))
	for i, n := range names {
		if n != "" {
			c[i] = strings.ToUpper(n[:1]) + n[1:]
		}
	}
	switch len(c) {
	case 0:
		return ""
	case 1:
		return c[0]
	}
	return strings.Join(c[:len(c)-1], ", ") + " and " + c[len(c)-1]
}

func areAwake(n int) string {
	if n == 1 {
		return "is awake again"
	}
	return "are awake again"
}

// pauseLine is the one-line pause state for /uso and /status ("" when running).
func (b *Bot) pauseLine() string {
	f, ok := readPause(b.pauseFile())
	if !ok {
		return ""
	}
	s := "⏸️ paused (" + f.Reason
	if f.Resets != "" {
		s += ", resets " + f.Resets
	}
	if t, err := time.Parse(time.RFC3339, f.Since); err == nil {
		s += ", since " + t.In(cdmx).Format("Jan 2 15:04") + " CDMX"
	}
	s += ")"
	if msgs, _ := readPending(b.pendingFile()); len(msgs) > 0 {
		s += fmt.Sprintf(" · %d saved message(s)", len(msgs))
	}
	if !b.isZoro() {
		return s + " — Zoro's /start resumes"
	}
	return s + " — /start to resume"
}

// joinAnd renders ["zoro","sky","tequila"] as "zoro, sky and tequila".
func joinAnd(xs []string) string {
	if len(xs) <= 1 {
		return strings.Join(xs, "")
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}
