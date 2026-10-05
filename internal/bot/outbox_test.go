package bot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"zoro/internal/tg"
)

// fakeFiles stands in for Telegram's upload endpoints: it records every
// upload as "method chat file" and fails the chats listed in fail.
type fakeFiles struct {
	mu   sync.Mutex
	got  []string
	fail map[int64]bool
	text []string
}

func (f *fakeFiles) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.got
	f.got = nil
	return out
}

func outboxBot(t *testing.T) (*testBot, *fakeFiles) {
	t.Helper()
	tb := newTestBot(t, "zoro")
	ff := &fakeFiles{fail: map[int64]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if method == "sendMessage" {
			_ = r.ParseForm()
			ff.mu.Lock()
			ff.text = append(ff.text, r.Form.Get("text"))
			ff.mu.Unlock()
			io.WriteString(w, `{"ok":true,"result":true}`)
			return
		}
		if method != "sendDocument" && method != "sendPhoto" && method != "sendVoice" {
			io.WriteString(w, `{"ok":true,"result":true}`) // sendChatAction…
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("%s: %v", method, err)
		}
		chat, _ := strconv.ParseInt(r.FormValue("chat_id"), 10, 64)
		var file string
		for _, fhs := range r.MultipartForm.File {
			file = fhs[0].Filename
		}
		ff.mu.Lock()
		defer ff.mu.Unlock()
		if ff.fail[chat] {
			io.WriteString(w, `{"ok":false,"description":"Bad Request: nope"}`)
			return
		}
		ff.got = append(ff.got, method+" "+strconv.FormatInt(chat, 10)+" "+file)
		io.WriteString(w, `{"ok":true,"result":true}`)
	}))
	t.Cleanup(srv.Close)
	tb.tg = tg.NewAt("T", srv.URL)
	return tb, ff
}

// drop leaves a file in the outbox as a job would, written a while ago (settled).
func drop(t *testing.T, tb *testBot, name string) {
	t.Helper()
	p := filepath.Join(tb.cfg.OutboxDir, name)
	if err := os.WriteFile(p, []byte(name), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
}

func same(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%s:\n got %q\nwant %q", what, got, want)
	}
}

// The bug of 4-oct: a file a detached job left BETWEEN turns was swallowed by
// the next turn's snapshot and never arrived. Now the next turn delivers it.
func TestTurnDeliversFilesLeftBetweenTurns(t *testing.T) {
	tb, ff := outboxBot(t)
	drop(t, tb, "viejo.pdf")
	tb.openOutbox() // seeded: viejo.pdf counts as delivered
	drop(t, tb, "trabajo.gif")

	tb.process(context.Background(), ownerJob("hola"))
	same(t, "first turn", ff.sent(), []string{"sendPhoto 1 trabajo.gif"})

	tb.process(context.Background(), ownerJob("otra"))
	same(t, "second turn re-sends nothing", ff.sent(), nil)
}

func TestOutboxWatcherDeliversToOwnerOutsideTurns(t *testing.T) {
	tb, ff := outboxBot(t)
	tb.cfg.MirrorChatIDs = []int64{1, 2} // the owner and a watcher
	tb.openOutbox()
	drop(t, tb, "reporte.pdf")
	drop(t, tb, "nota.ogg")

	// A turn is running: the watcher never sends in the middle of it.
	tb.turnMu.Lock()
	tb.outboxTick(context.Background())
	tb.turnMu.Unlock()
	tb.busy.Store(true)
	tb.outboxTick(context.Background())
	tb.busy.Store(false)
	same(t, "during a turn", ff.sent(), nil)

	tb.outboxTick(context.Background())
	same(t, "between turns", ff.sent(), []string{
		"sendVoice 1 nota.ogg", "sendVoice 2 nota.ogg",
		"sendDocument 1 reporte.pdf", "sendDocument 2 reporte.pdf",
	})
	tb.outboxTick(context.Background())
	same(t, "already delivered", ff.sent(), nil)

	// No clear owner: the watcher does nothing.
	drop(t, tb, "otro.pdf")
	tb.cfg.OwnerID = 0
	tb.outboxTick(context.Background())
	same(t, "no owner", ff.sent(), nil)
}

func TestOutboxFailedSendIsRetriedThenGivenUp(t *testing.T) {
	tb, ff := outboxBot(t)
	tb.cfg.MirrorChatIDs = []int64{2}
	tb.openOutbox()
	drop(t, tb, "logo.png")

	ff.fail[1] = true
	tb.outboxTick(context.Background())
	same(t, "owner failed: the mirror doesn't get it either", ff.sent(), nil)

	ff.fail[1] = false
	tb.outboxTick(context.Background())
	same(t, "retried", ff.sent(), []string{"sendPhoto 1 logo.png", "sendPhoto 2 logo.png"})

	// A file Telegram always rejects is given up after outboxTries, with a note.
	drop(t, tb, "enorme.mp4")
	ff.fail[1] = true
	for i := 0; i < outboxTries+2; i++ {
		tb.outboxTick(context.Background())
	}
	ff.mu.Lock()
	notes := ff.text
	ff.mu.Unlock()
	if len(notes) != 1 || !strings.Contains(notes[0], "enorme.mp4") {
		t.Fatalf("want one give-up note, got %q", notes)
	}
	ff.fail[1] = false
	tb.outboxTick(context.Background())
	same(t, "given up stays given up", ff.sent(), nil)
}

func TestTurnWaitsForAFileBeingWritten(t *testing.T) {
	tb, ff := outboxBot(t)
	tb.openOutbox()
	if err := os.WriteFile(filepath.Join(tb.cfg.OutboxDir, "recien.gif"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The watcher leaves a file touched just now for later…
	tb.outboxTick(context.Background())
	same(t, "watcher, fresh file", ff.sent(), nil)
	// …while the end of a turn waits for it to settle and sends it.
	tb.turnMu.Lock()
	tb.sendOutbox(context.Background(), 1, true)
	tb.turnMu.Unlock()
	same(t, "turn end", ff.sent(), []string{"sendPhoto 1 recien.gif"})
}
