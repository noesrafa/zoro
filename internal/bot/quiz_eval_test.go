package bot

// Live check of the quiz maker against rafiña's real messages — calls Claude,
// so it only runs when asked:
//
//	ZORO_QUIZ_EVAL=cases.tsv ZORO_QUIZ_EVAL_OUT=out.jsonl go test ./internal/bot -run TestQuizEval -v -timeout 60m
//
// cases.tsv: "<label>\t<message>" per line (label: error | clean | spanish).
// The environment picks the Claude login (CLAUDE_CONFIG_DIR, …), as for the engine.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"zoro/internal/claude"
)

type quizEvalRow struct {
	Label   string  `json:"label"`
	Text    string  `json:"text"`
	Result  string  `json:"result"` // quiz | clean | rejected | error | skipped
	Err     string  `json:"err,omitempty"`
	Calls   int     `json:"calls"`
	Seconds float64 `json:"seconds"`
	Quiz    *quiz   `json:"quiz,omitempty"`
	Log     string  `json:"log,omitempty"` // the engine's log lines (rejections)
}

func TestQuizEval(t *testing.T) {
	path := os.Getenv("ZORO_QUIZ_EVAL")
	if path == "" {
		t.Skip("set ZORO_QUIZ_EVAL=cases.tsv to run the live quiz check")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []quizEvalRow
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		label, text, ok := strings.Cut(sc.Text(), "\t")
		if ok && strings.TrimSpace(text) != "" {
			rows = append(rows, quizEvalRow{Label: label, Text: text})
		}
	}
	f.Close()

	bin := os.Getenv("CLAUDE_BIN")
	if bin == "" {
		bin = "/home/rafael/.local/bin/claude"
	}
	driver := claude.New(claude.Config{Bin: bin, WorkDir: "/home/rafael", DangerSkip: true})
	workers := 3
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i := range rows {
		wg.Add(1)
		go func(r *quizEvalRow) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			tb := newTestBot(t, "zoro")
			var logBuf bytes.Buffer
			tb.Bot.log = slog.New(slog.NewTextHandler(&logBuf, nil))
			defer func() { r.Log = logBuf.String() }()
			var mu sync.Mutex
			tb.run = func(ctx context.Context, sid string, create bool, prompt string, o claude.RunOpts) (claude.Result, error) {
				mu.Lock()
				r.Calls++
				mu.Unlock()
				return driver.Run(ctx, sid, create, prompt, o)
			}
			text := quizCandidate([]string{r.Text})
			if text == "" {
				r.Result = "skipped"
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), quizTimeout)
			defer cancel()
			t0 := time.Now()
			q, ok, err := tb.generateQuiz(ctx, text, looksSpanish(text))
			r.Seconds = time.Since(t0).Seconds()
			switch {
			case err != nil && strings.Contains(err.Error(), "rejected twice"):
				r.Result, r.Err = "rejected", err.Error()
			case err != nil:
				r.Result, r.Err = "error", err.Error()
			case ok:
				r.Result, r.Quiz = "quiz", &q
			default:
				r.Result = "clean"
			}
			t.Logf("%-7s %-8s %5.1fs calls=%d  %.70s", r.Label, r.Result, r.Seconds, r.Calls, r.Text)
		}(&rows[i])
	}
	wg.Wait()

	if out := os.Getenv("ZORO_QUIZ_EVAL_OUT"); out != "" {
		w, err := os.Create(out)
		if err != nil {
			t.Fatal(err)
		}
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		for _, r := range rows {
			_ = enc.Encode(r)
		}
		w.Close()
	}
	var secs []float64
	n := map[string]int{}
	for _, r := range rows {
		n[r.Label+"/"+r.Result]++
		if r.Result != "skipped" {
			secs = append(secs, r.Seconds)
		}
	}
	sort.Float64s(secs)
	pct := func(p float64) float64 {
		if len(secs) == 0 {
			return 0
		}
		return secs[int(p*float64(len(secs)-1)+0.5)]
	}
	t.Logf("results: %v · latency p50 %.1fs p95 %.1fs", n, pct(0.5), pct(0.95))
}
