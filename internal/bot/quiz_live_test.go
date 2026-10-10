package bot

// Full cycle against the LIVE quiz site, with a test engine (temp state, fake
// Telegram, stand-in Claude) — never the real service:
//
//	ZORO_QUIZ_LIVE=/home/rafael/trabajos/coach/navegador.js go test ./internal/bot -run TestQuizLiveCycle -v
//
// The engine publishes a quiz into the real site folder and holds a message; a
// headless browser (the script) solves it on https://coach.dominioartificial.com,
// whose PUT writes solved/<id>; quizTick must see it and release the message.

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestQuizLiveCycle(t *testing.T) {
	script := os.Getenv("ZORO_QUIZ_LIVE")
	if script == "" {
		t.Skip("set ZORO_QUIZ_LIVE=<navegador.js> to run the live PUT → engine cycle")
	}
	tb, _ := quizBot(t)
	tb.cfg.QuizDir = "/home/rafael/dominioartificial/coach"
	tb.cfg.QuizURL = "https://coach.dominioartificial.com"
	var id string
	t.Cleanup(func() {
		if id != "" {
			_ = os.Remove(tb.quizFile(id))
		}
	})
	tb.runJob(ownerJob("what we have in r2?"))
	p, ok := tb.pendingQuiz()
	if !ok {
		t.Fatal("no quiz posted")
	}
	id = p.ID
	tb.runJob(ownerJob("and the backups are fine?"))
	if tb.nCalls() != 1 {
		t.Fatal("the second message must be held")
	}
	tb.quizTick()
	if len(tb.jobs) != 0 {
		t.Fatal("not solved yet: nothing released")
	}

	out, err := exec.Command("node", script, id, t.TempDir()).CombinedOutput()
	t.Logf("browser:\n%s", out)
	if err != nil || strings.Contains(string(out), "FAIL") {
		t.Fatalf("browser run failed: %v", err)
	}
	if _, err := os.Stat(tb.solvedFile(id)); err != nil {
		t.Fatalf("the page's PUT must have written solved/%s: %v", id, err)
	}
	t0 := time.Now()
	tb.quizTick()
	if _, ok := tb.pendingQuiz(); ok {
		t.Fatal("the engine must see the solved file and take the wall down")
	}
	tb.process(context.Background(), tb.takeJob(t))
	t.Logf("solved file → release turn: %s", time.Since(t0).Round(time.Millisecond))
	if tb.nCalls() != 2 || !strings.Contains(tb.calls[1], "and the backups are fine?") {
		t.Fatalf("the held message must be answered: %q", tb.calls)
	}
	_ = os.Remove(tb.solvedFile(id)) // www-data's file in rafael's dir: removable
}
