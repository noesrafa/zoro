package bot

// Full cycle against the LIVE quiz site, with a test engine (temp state, fake
// Telegram, stand-in Claude) — never the real service:
//
//	ZORO_QUIZ_LIVE=/home/rafael/trabajos/coach/navegador.js go test ./internal/bot -run TestQuizLiveCycle -v
//
// The engine publishes a quiz into the real site folder and its hub.json and
// keeps answering; a headless browser (the script) solves it on
// https://coach.dominioartificial.com, whose PUT writes solved/<id>. The quiz,
// its solved file and its hub entry are removed at the end.

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestQuizLiveCycle(t *testing.T) {
	script := os.Getenv("ZORO_QUIZ_LIVE")
	if script == "" {
		t.Skip("set ZORO_QUIZ_LIVE=<navegador.js> to run the live PUT cycle")
	}
	tb, _ := quizBot(t)
	tb.cfg.QuizDir = "/home/rafael/dominioartificial/coach"
	tb.cfg.QuizURL = "https://coach.dominioartificial.com"
	before := tb.hub(t)
	var id string
	t.Cleanup(func() {
		if id == "" {
			return
		}
		_ = os.Remove(tb.quizFile(id))
		_ = os.Remove(tb.solvedFile(id)) // www-data's file in rafael's dir: removable
		tb.quizMu.Lock()
		defer tb.quizMu.Unlock()
		hub, err := tb.readHubLocked()
		if err != nil {
			t.Errorf("hub cleanup: %v", err)
			return
		}
		var keep []hubEntry
		for _, e := range hub {
			if e.ID != id {
				keep = append(keep, e)
			}
		}
		if err := tb.writeHubLocked(keep); err != nil {
			t.Errorf("hub cleanup: %v", err)
		}
	})
	tb.runJob(ownerJob("what we have in r2?"))
	hub := tb.hub(t)
	if len(hub) != len(before)+1 {
		t.Fatal("no quiz added to the hub")
	}
	id = hub[0].ID
	tb.runJob(ownerJob("and the backups are fine?"))
	if tb.nCalls() != 2 {
		t.Fatal("the second message must be answered right away")
	}
	raw, _ := json.Marshal(hub[0])
	t.Logf("hub entry: %s", raw)

	out, err := exec.Command("node", script, id, t.TempDir()).CombinedOutput()
	t.Logf("browser:\n%s", out)
	if err != nil || strings.Contains(string(out), "FAIL") {
		t.Fatalf("browser run failed: %v", err)
	}
	if _, err := os.Stat(tb.solvedFile(id)); err != nil {
		t.Fatalf("the page's PUT must have written solved/%s: %v", id, err)
	}
}
