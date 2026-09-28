package bot

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"zoro/internal/claude"
	"zoro/internal/config"
	"zoro/internal/cron"
	"zoro/internal/media"
	"zoro/internal/session"
	"zoro/internal/settings"
	"zoro/internal/tg"
)

const banner = "You've hit your weekly limit · resets 11pm (America/Mexico_City)"

// testBot is a Bot wired to temp dirs, a fake Telegram (records every
// sendMessage) and a stand-in Claude (records every call).
type testBot struct {
	*Bot
	mu    sync.Mutex
	sent  []string
	calls []string // prompts Claude got
	reply func(prompt string) (claude.Result, error)
	sudos []string
}

func newTestBot(t *testing.T, name string) *testBot {
	t.Helper()
	dir := t.TempDir()
	tb := &testBot{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			_ = r.ParseForm()
			tb.mu.Lock()
			tb.sent = append(tb.sent, r.Form.Get("text"))
			tb.mu.Unlock()
		}
		io.WriteString(w, `{"ok":true,"result":true}`)
	}))
	t.Cleanup(srv.Close)

	cfg := config.Config{
		OwnerID: 1, OwnerIDs: map[int64]bool{1: true}, OwnerName: "rafiña", AgentName: name,
		StateDir: filepath.Join(dir, "state"), InboxDir: filepath.Join(dir, "inbox"), OutboxDir: filepath.Join(dir, "outbox"),
		BriefFile: filepath.Join(dir, "brief.md"), CronFile: filepath.Join(dir, "crons.json"),
		MiMoKeyFile: filepath.Join(dir, "mimo.key"), MiMoModel: "mimo-v2.6-pro",
	}
	for _, d := range []string{cfg.StateDir, cfg.InboxDir, cfg.OutboxDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	store, err := session.Open(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	set, err := settings.Open(cfg.StateDir, settings.Settings{Model: "opus", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	tb.Bot = &Bot{
		cfg: cfg, log: slog.New(slog.NewTextHandler(io.Discard, nil)), tg: tg.NewAt("T", srv.URL),
		store: store, set: set, mediaC: media.Config{InboxDir: cfg.InboxDir, OutboxDir: cfg.OutboxDir},
		jobs: make(chan job, 16), quit: make(chan struct{}),
	}
	tb.run = func(_ context.Context, sid string, _ bool, prompt string, _ claude.RunOpts) (claude.Result, error) {
		tb.mu.Lock()
		tb.calls = append(tb.calls, prompt)
		tb.mu.Unlock()
		if tb.reply == nil {
			return claude.Result{Text: "ok", SessionID: sid}, nil
		}
		r, err := tb.reply(prompt)
		if r.SessionID == "" {
			r.SessionID = sid
		}
		return r, err
	}
	// sudo stand-in: record the call, then run it as the test user without the
	// chown flags (install -o/-g needs root).
	tb.sudo = func(ctx context.Context, args ...string) ([]byte, error) {
		tb.mu.Lock()
		tb.sudos = append(tb.sudos, strings.Join(args, " "))
		tb.mu.Unlock()
		var clean []string
		for i := 0; i < len(args); i++ {
			if args[i] == "-o" || args[i] == "-g" {
				i++
				continue
			}
			clean = append(clean, args[i])
		}
		return exec.CommandContext(ctx, clean[0], clean[1:]...).CombinedOutput()
	}
	return tb
}

func (tb *testBot) sentWith(sub string) int {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	n := 0
	for _, s := range tb.sent {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

func (tb *testBot) nCalls() int {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return len(tb.calls)
}

func ownerJob(text string) job {
	return job{chatID: 1, msgs: []*tg.Message{{Text: text, From: &tg.User{ID: 1, FirstName: "Rafa"}, Chat: tg.Chat{ID: 1}}}}
}

func limitReply(string) (claude.Result, error) {
	return claude.Result{Text: banner, IsError: true, APIError: "rate_limit"}, errors.New("claude exited: exit status 1: ")
}

func pause(t *testing.T, tb *testBot) {
	t.Helper()
	if err := tb.writeOwnPause(tb.newPause(reasonStop, "", "test")); err != nil {
		t.Fatal(err)
	}
}

func TestPausedCronIsSkippedWithoutClaude(t *testing.T) {
	tb := newTestBot(t, "sky")
	pause(t, tb)
	tb.fireCron(cron.Job{ID: "gastos", Prompt: "p"})
	tb.fireCron(cron.Job{ID: "cierre-del-dia", Prompt: "p", Rollover: true})
	if len(tb.jobs) != 0 {
		t.Fatalf("a paused cron must not be enqueued, got %d jobs", len(tb.jobs))
	}
	// Even a cron that was queued before the pause makes no call.
	tb.process(context.Background(), job{chatID: 1, prompt: "p", label: "⏰ cron"})
	if tb.nCalls() != 0 {
		t.Fatalf("paused: Claude was called %d times", tb.nCalls())
	}
	if msgs, _ := readPending(tb.pendingFile()); len(msgs) != 0 {
		t.Fatalf("crons must not be queued, got %d", len(msgs))
	}
}

func TestPausedOwnerMessageIsSavedAndAckedAtMostEvery30Min(t *testing.T) {
	tb := newTestBot(t, "sky")
	tb.cfg.PauseReply = "ando en pausa"
	pause(t, tb)
	tb.process(context.Background(), ownerJob("hola"))
	tb.process(context.Background(), ownerJob("¿sigues ahí?"))
	if tb.nCalls() != 0 {
		t.Fatalf("paused: Claude was called %d times", tb.nCalls())
	}
	msgs, _ := readPending(tb.pendingFile())
	if len(msgs) != 2 || msgs[0].Text != "hola" || msgs[1].Text != "¿sigues ahí?" || msgs[0].From != "rafiña" || msgs[0].At == "" {
		t.Fatalf("queue = %+v", msgs)
	}
	if n := tb.sentWith("ando en pausa"); n != 1 {
		t.Fatalf("two messages in a row must get ONE ack, got %d", n)
	}
	// 31 minutes later the next message gets acked again.
	tb.ackMu.Lock()
	tb.acked[1] = time.Now().Add(-31 * time.Minute)
	tb.ackMu.Unlock()
	tb.process(context.Background(), ownerJob("ya?"))
	if n := tb.sentWith("ando en pausa"); n != 2 {
		t.Fatalf("after 30 min the ack comes back, got %d", n)
	}
}

func TestPausedWithoutReplyStaysSilent(t *testing.T) {
	tb := newTestBot(t, "tequila")
	pause(t, tb)
	tb.process(context.Background(), ownerJob("hola"))
	if len(tb.sent) != 0 {
		t.Fatalf("empty ZORO_PAUSE_REPLY must send nothing, sent %q", tb.sent)
	}
}

func TestResumeRunsTheQueueOnce(t *testing.T) {
	tb := newTestBot(t, "sky")
	for _, txt := range []string{"uno", "dos"} {
		if err := appendPending(tb.pendingFile(), pendingMsg{ChatID: 1, From: "Lú", At: "2026-09-27 10:00", Text: txt}); err != nil {
			t.Fatal(err)
		}
	}
	tb.maybeResume()
	tb.maybeResume() // a second tick while the first is queued must not add another
	if len(tb.jobs) != 1 {
		t.Fatalf("want exactly 1 resume job, got %d", len(tb.jobs))
	}
	tb.process(context.Background(), <-tb.jobs)
	if tb.nCalls() != 1 {
		t.Fatalf("want one bundled turn, got %d calls", tb.nCalls())
	}
	p := tb.calls[0]
	for _, want := range []string{"While you were paused", "Lú: uno", "Lú: dos", "Pick them up"} {
		if !strings.Contains(p, want) {
			t.Errorf("bundle missing %q:\n%s", want, p)
		}
	}
	if _, err := os.Stat(tb.pendingFile()); !os.IsNotExist(err) {
		t.Fatal("queue must be cleared after a successful resume turn")
	}
	tb.maybeResume()
	if len(tb.jobs) != 0 {
		t.Fatal("empty queue must not produce another turn")
	}
}

func TestResumeHittingTheLimitAgainKeepsTheQueue(t *testing.T) {
	tb := newTestBot(t, "sky")
	_ = appendPending(tb.pendingFile(), pendingMsg{ChatID: 1, From: "Lú", Text: "uno"})
	tb.reply = limitReply
	tb.maybeResume()
	tb.process(context.Background(), <-tb.jobs)
	if !tb.paused() {
		t.Fatal("limit on the resume turn must pause again")
	}
	if msgs, _ := readPending(tb.pendingFile()); len(msgs) != 1 {
		t.Fatalf("queue must be kept, got %d", len(msgs))
	}
	if tb.resumeQueued.Load() {
		t.Fatal("resumeQueued must be released")
	}
}

func TestLimitKeepsTheSessionAndForwardsNothing(t *testing.T) {
	for _, created := range []bool{true, false} {
		tb := newTestBot(t, "sky")
		tb.cfg.PauseReply = "ando en pausa"
		sid := tb.store.Current().SessionID
		if created {
			_ = tb.store.MarkCreated()
		}
		tb.reply = limitReply
		tb.process(context.Background(), ownerJob("hola"))

		st := tb.store.Current()
		if st.SessionID != sid || !st.Created {
			t.Fatalf("created=%v: session must be kept (and adopted), got %+v want id %s", created, st, sid)
		}
		if tb.nCalls() != 1 {
			t.Fatalf("created=%v: no retry on a new session allowed, got %d calls", created, tb.nCalls())
		}
		f, ok := readPause(tb.pauseFile())
		if !ok || f.Reason != reasonLimit || f.Resets != "11pm (America/Mexico_City)" || f.Agent != "sky" {
			t.Fatalf("flag = %+v ok=%v", f, ok)
		}
		if msgs, _ := readPending(tb.pendingFile()); len(msgs) != 1 || msgs[0].Text != "hola" {
			t.Fatalf("the failed message must be queued, got %+v", msgs)
		}
		if tb.sentWith("weekly limit") != 0 || tb.sentWith("New session") != 0 || tb.sentWith("NEW one") != 0 {
			t.Fatalf("raw error / rotation notice leaked to Telegram: %q", tb.sent)
		}
		if tb.sentWith("ando en pausa") != 1 {
			t.Fatalf("the owner gets the canned ack, sent %q", tb.sent)
		}
	}
}

func TestNormalReplyAboutTheLimitDoesNotPause(t *testing.T) {
	tb := newTestBot(t, "zoro")
	tb.reply = func(string) (claude.Result, error) {
		return claude.Result{Text: banner + " — that's what you all got on Saturday."}, nil
	}
	tb.process(context.Background(), ownerJob("what happened saturday?"))
	if tb.paused() {
		t.Fatal("a normal reply must never pause")
	}
	if tb.sentWith("Saturday") != 1 {
		t.Fatalf("the reply must be delivered, sent %q", tb.sent)
	}
}

func agentDirs(t *testing.T, tb *testBot) {
	t.Helper()
	for _, n := range []string{"sky", "tequila"} {
		d := filepath.Join(t.TempDir(), n, "state")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		tb.cfg.Agents = append(tb.cfg.Agents, config.Agent{Name: n, StateDir: d})
	}
}

func TestStopAndStartFlipEveryFlag(t *testing.T) {
	tb := newTestBot(t, "zoro")
	agentDirs(t, tb)
	ctx := context.Background()

	tb.stopAll(ctx, 1)
	for _, p := range []string{tb.pauseFile(), filepath.Join(tb.cfg.Agents[0].StateDir, "paused"), filepath.Join(tb.cfg.Agents[1].StateDir, "paused")} {
		f, ok := readPause(p)
		if !ok || f.Reason != reasonStop {
			t.Fatalf("%s: want a stop flag, got %+v ok=%v", p, f, ok)
		}
	}
	if tb.sentWith("⏸️ Paused: zoro, sky, tequila. Messages get saved; /start brings everyone back.") != 1 {
		t.Fatalf("stop reply, sent %q", tb.sent)
	}
	var installs int
	for _, c := range tb.sudos {
		if strings.HasPrefix(c, "install -o sky -g sky -m 644 ") || strings.HasPrefix(c, "install -o tequila -g tequila -m 644 ") {
			installs++
		}
	}
	if installs != 2 {
		t.Fatalf("other agents' flags must go through sudo install with their owner, calls: %q", tb.sudos)
	}

	// Saved messages across agents are counted by /start.
	_ = appendPending(tb.pendingFile(), pendingMsg{ChatID: 1, Text: "a"})
	_ = appendPending(filepath.Join(tb.cfg.Agents[0].StateDir, "pending.jsonl"), pendingMsg{ChatID: 5, Text: "b"})
	_ = appendPending(filepath.Join(tb.cfg.Agents[0].StateDir, "pending.jsonl"), pendingMsg{ChatID: 5, Text: "c"})

	tb.startAll(ctx, 1)
	for _, p := range []string{tb.pauseFile(), filepath.Join(tb.cfg.Agents[0].StateDir, "paused"), filepath.Join(tb.cfg.Agents[1].StateDir, "paused")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s must be removed by /start", p)
		}
	}
	if tb.sentWith("Back, bro! Zoro, Sky and Tequila are awake again — picking up the 3 message(s)") != 1 {
		t.Fatalf("start reply, sent %q", tb.sent)
	}
	if len(tb.jobs) != 1 {
		t.Fatal("/start must queue Zoro's own saved message right away")
	}

	tb.startAll(ctx, 1)
	if tb.sentWith("Nobody was paused") != 1 {
		t.Fatalf("second /start must say nothing was paused, sent %q", tb.sent)
	}
}

func TestLimitAnywherePausesEveryoneAndTellsRafinaOnce(t *testing.T) {
	tb := newTestBot(t, "zoro")
	agentDirs(t, tb)
	sky := tb.cfg.Agents[0]
	// Sky hit the limit and paused itself.
	raw := `{"since":"2026-09-26T04:32:00Z","reason":"limit","resets":"11pm (America/Mexico_City)","detail":"` + banner + `","agent":"sky"}`
	if err := os.WriteFile(filepath.Join(sky.StateDir, "paused"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	tb.watchAgents(context.Background())
	tb.watchAgents(context.Background())

	if !tb.paused() {
		t.Fatal("zoro must pause itself")
	}
	if _, ok := readPause(filepath.Join(tb.cfg.Agents[1].StateDir, "paused")); !ok {
		t.Fatal("tequila must be paused too")
	}
	if f, _ := readPause(filepath.Join(sky.StateDir, "paused")); f.Agent != "sky" {
		t.Fatal("sky's own flag must be left as it is")
	}
	want := "⏸️ Weekly limit hit (resets 11pm (America/Mexico_City)). Paused zoro, sky and tequila so nobody spams errors."
	if tb.sentWith(want) != 1 || len(tb.sent) != 1 {
		t.Fatalf("want exactly one notice %q, sent %q", want, tb.sent)
	}
	if tb.sentWith("/auth mimo and /start") != 1 {
		t.Fatal("the notice must offer MiMo")
	}
}

func TestZoroOwnLimitPausesEveryone(t *testing.T) {
	tb := newTestBot(t, "zoro")
	agentDirs(t, tb)
	tb.reply = limitReply
	tb.process(context.Background(), ownerJob("hola"))
	for _, a := range tb.cfg.Agents {
		if _, ok := readPause(filepath.Join(a.StateDir, "paused")); !ok {
			t.Fatalf("%s must be paused", a.Name)
		}
	}
	if tb.sentWith("Weekly limit hit") != 1 {
		t.Fatalf("one notice, sent %q", tb.sent)
	}
}

func TestSubAgentLimitDoesNotTouchOthers(t *testing.T) {
	tb := newTestBot(t, "sky")
	tb.reply = limitReply
	tb.process(context.Background(), job{chatID: 1, prompt: "p", label: "⏰ cron"})
	if !tb.paused() {
		t.Fatal("sky pauses itself")
	}
	if len(tb.sent) != 0 || len(tb.sudos) != 0 {
		t.Fatalf("a sub-agent's cron hitting the limit sends nothing, touches no one: sent %q sudo %q", tb.sent, tb.sudos)
	}
	if msgs, _ := readPending(tb.pendingFile()); len(msgs) != 0 {
		t.Fatal("a failed cron is dropped, not queued")
	}
}

func TestPauseFlagSurvivesARestart(t *testing.T) {
	tb := newTestBot(t, "sky")
	pause(t, tb)
	again := &Bot{cfg: tb.cfg}
	if !again.paused() {
		t.Fatal("a new engine on the same state dir must still be paused")
	}
}

// 28-sep-2026: when the pause ends, whoever was told "I'm paused" (or left a saved
// message) gets ZORO_RESUME_REPLY ONCE, before the saved messages are answered.
func TestResumeGreetsWhoNoticedThePauseOnce(t *testing.T) {
	tb := newTestBot(t, "sky")
	tb.cfg.PauseReply = "ando en pausa"
	tb.cfg.ResumeReply = "¡Ya regresé!"
	pause(t, tb)
	tb.maybeResume() // a tick during the pause greets nobody
	tb.process(context.Background(), ownerJob("hola"))
	if n := tb.sentWith("¡Ya regresé!"); n != 0 {
		t.Fatalf("no greeting while still paused, got %d", n)
	}
	if err := os.Remove(tb.pauseFile()); err != nil { // /start
		t.Fatal(err)
	}
	tb.maybeResume()
	tb.resumeQueued.Store(false) // the queued turn hasn't run yet: the next tick must not greet again
	tb.maybeResume()
	if n := tb.sentWith("¡Ya regresé!"); n != 1 {
		t.Fatalf("the chat that noticed the pause gets exactly ONE greeting, got %d", n)
	}
	if len(tb.jobs) == 0 {
		t.Fatal("the saved message must still be picked up after the greeting")
	}
	// A second pause earns a second greeting.
	pause(t, tb)
	tb.maybeResume()
	tb.process(context.Background(), ownerJob("otra vez"))
	_ = os.Remove(tb.pauseFile())
	tb.resumeQueued.Store(false)
	tb.maybeResume()
	if n := tb.sentWith("¡Ya regresé!"); n != 2 {
		t.Fatalf("a new pause greets again, got %d", n)
	}
}

func TestResumeWithoutReplyOrWithoutNoticeStaysSilent(t *testing.T) {
	tb := newTestBot(t, "tequila")
	tb.cfg.ResumeReply = "de vuelta"
	pause(t, tb)
	_ = os.Remove(tb.pauseFile())
	tb.maybeResume()
	if len(tb.sent) != 0 {
		t.Fatalf("nobody noticed the pause: nobody gets greeted, sent %q", tb.sent)
	}
	tb2 := newTestBot(t, "sky")
	pause(t, tb2)
	tb2.process(context.Background(), ownerJob("hola"))
	_ = os.Remove(tb2.pauseFile())
	tb2.maybeResume()
	if len(tb2.sent) != 0 {
		t.Fatalf("empty ZORO_RESUME_REPLY must send nothing, sent %q", tb2.sent)
	}
}

func TestHumanList(t *testing.T) {
	for in, want := range map[string]string{"zoro": "Zoro", "zoro,sky": "Zoro and Sky", "zoro,sky,tequila": "Zoro, Sky and Tequila"} {
		if got := humanList(strings.Split(in, ",")); got != want {
			t.Errorf("humanList(%q) = %q, want %q", in, got, want)
		}
	}
}
