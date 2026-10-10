package bot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"zoro/internal/claude"
	"zoro/internal/cron"
)

// goodQuiz is a valid quiz like the ones the model makes for "what we have in r2?".
func goodQuiz() quiz {
	q := quiz{
		ID: "abc123def456", Created: "2026-10-09T20:00:00Z", Source: "what we have in r2?", Kind: quizGrammar,
		Rule:     quizRule{Title: "Questions: do/does/did first", Text: "In a question, do/does/did goes before the subject: What do we have?"},
		Original: "what we have in r2?", Corrected: "What do we have in R2?",
		Puzzles: []quizPuzzle{
			{Type: "reorder", Answer: "What do we have in R2?"},
			{Type: "choose", Text: "Why ___ Tequila take so long to answer?", Options: []string{"do", "does", "is"}, Answer: "does"},
			{Type: "write", Prompt: "Where Ángel keeps the Meta keys?", Answer: "Where does Ángel keep the Meta keys?"},
		},
	}
	fillReorder(&q)
	return q
}

func TestValidateQuizAcceptsGood(t *testing.T) {
	q := goodQuiz()
	if err := validateQuiz(q); err != nil {
		t.Fatalf("a good quiz must pass: %v", err)
	}
	if strings.Join(q.Puzzles[0].Words, " ") == "what do we have in R2" {
		t.Fatal("tiles must be shuffled")
	}
	es := goodQuiz()
	es.Kind, es.Rule.Title = quizSpanish, "Say it in English"
	es.Original, es.Corrected = "qué tenemos en r2?", "What do we have in R2?"
	es.Puzzles[0] = quizPuzzle{Type: "reorder", Answer: "What do we have in R2?"}
	es.Puzzles[2] = quizPuzzle{Type: "write", Prompt: "¿Qué hace Tequila ahorita?", Answer: "What is Tequila doing right now?", Accept: []string{"What's Tequila doing now?"}}
	fillReorder(&es)
	if err := validateQuiz(es); err != nil {
		t.Fatalf("a good Spanish quiz must pass: %v", err)
	}
}

func TestValidateQuizRejectsBroken(t *testing.T) {
	cases := map[string]func(q *quiz){
		"bad id":             func(q *quiz) { q.ID = "short" },
		"bad kind":           func(q *quiz) { q.Kind = "vocab" },
		"no rule":            func(q *quiz) { q.Rule.Text = " " },
		"no title":           func(q *quiz) { q.Rule.Title = "" },
		"same fix":           func(q *quiz) { q.Corrected = "What we have in R2" },
		"no original":        func(q *quiz) { q.Original = "" },
		"two puzzles":        func(q *quiz) { q.Puzzles = q.Puzzles[:2] },
		"wrong order":        func(q *quiz) { q.Puzzles[0], q.Puzzles[1] = q.Puzzles[1], q.Puzzles[0] },
		"tile missing":       func(q *quiz) { q.Puzzles[0].Words = q.Puzzles[0].Words[1:] },
		"tile extra":         func(q *quiz) { q.Puzzles[0].Words = append(q.Puzzles[0].Words, "please") },
		"tile changed":       func(q *quiz) { q.Puzzles[0].Words[0] = "xyz" },
		"two words one tile": func(q *quiz) { q.Puzzles[0].Words = []string{"we have", "in", "R2", "what", "do"} },
		"tiles in order":     func(q *quiz) { q.Puzzles[0].Words = []string{"What", "do", "we", "have", "in", "R2"} },
		"accept other words": func(q *quiz) { q.Puzzles[0].Accept = []string{"What does we have in R2"} },
		"reorder no answer":  func(q *quiz) { q.Puzzles[0].Answer = "" },
		"answer not option":  func(q *quiz) { q.Puzzles[1].Answer = "did" },
		"duplicate option":   func(q *quiz) { q.Puzzles[1].Options = []string{"do", "does", "Does"} },
		"one option":         func(q *quiz) { q.Puzzles[1].Options = []string{"does"} },
		"five options":       func(q *quiz) { q.Puzzles[1].Options = []string{"do", "does", "did", "is", "are"} },
		"no blank":           func(q *quiz) { q.Puzzles[1].Text = "Why does Tequila take so long?" },
		"two blanks":         func(q *quiz) { q.Puzzles[1].Text = "Why ___ Tequila ___ so long?" },
		"choose is his": func(q *quiz) {
			q.Puzzles[1].Text = "What ___ we have in R2?"
			q.Puzzles[1].Answer = "do"
			q.Puzzles[1].Options = []string{"do", "does"}
		},
		"write empty":         func(q *quiz) { q.Puzzles[2].Answer = "  " },
		"write no prompt":     func(q *quiz) { q.Puzzles[2].Prompt = "" },
		"write already right": func(q *quiz) { q.Puzzles[2].Prompt = q.Puzzles[2].Answer },
		"write is his":        func(q *quiz) { q.Puzzles[2].Prompt = "what we have in r2?" },
		"write accept wrong":  func(q *quiz) { q.Puzzles[2].Accept = []string{"Where Ángel keeps the Meta keys"} },
		"write accept empty":  func(q *quiz) { q.Puzzles[2].Accept = []string{""} },
	}
	for name, mutate := range cases {
		q := goodQuiz()
		mutate(&q)
		if err := validateQuiz(q); err == nil {
			t.Errorf("%s: a broken quiz must be rejected", name)
		}
	}
}

func TestNormQuizAndTiles(t *testing.T) {
	same := [][2]string{
		{"Tequila doesn’t answer me.", "tequila does not answer me"},
		{"  What's  going on?! ", "what is going on"},
		{"We won't use it", "we will not use it"},
		{"“Explain to me”", "explain to me"},
		{"I can't, I'm busy", "i can not i am busy"},
	}
	for _, p := range same {
		if normQuiz(p[0]) != normQuiz(p[1]) {
			t.Errorf("%q and %q must compare equal (%q vs %q)", p[0], p[1], normQuiz(p[0]), normQuiz(p[1]))
		}
	}
	if normQuiz("What do we have?") == normQuiz("What we have?") {
		t.Error("a missing word must not compare equal")
	}
	if got := strings.Join(quizTiles("Why is Tequila taking so long?"), "|"); got != "why|is|Tequila|taking|so|long" {
		t.Errorf("tiles: %s", got)
	}
	if got := strings.Join(quizTiles("Tequila doesn't answer me."), "|"); got != "Tequila|doesn't|answer|me" {
		t.Errorf("a name keeps its capital: %s", got)
	}
	if got := quizTiles("I want to upload one per day."); got[0] != "I" {
		t.Errorf("I stays I: %v", got)
	}
	if shuffleTiles([]string{"no", "no", "no"}, []string{"no no no"}) != nil {
		t.Error("tiles with a single order can't be shuffled")
	}
}

const modelQuizJSON = "```json\n" + `{"clean": false, "kind": "grammar",
 "rule": {"title": "Questions: do/does/did first", "text": "In a question, do/does/did goes before the subject."},
 "original": "what we have in r2?", "corrected": "What do we have in R2?",
 "puzzles": [
  {"type": "reorder", "answer": "What do we have in R2?", "accept": []},
  {"type": "choose", "text": "Why ___ Tequila take so long?", "options": ["do", "does", "is"], "answer": "does", "hint": "Tequila = he/she/it"},
  {"type": "write", "prompt": "Where Ángel keeps the keys?", "answer": "Where does Ángel keep the keys?", "accept": [], "hint": "Where does…"}
 ]}` + "\n```"

func TestBuildQuiz(t *testing.T) {
	q, clean, err := buildQuiz(modelQuizJSON, "what we have in r2?")
	if err != nil || clean {
		t.Fatalf("fenced model JSON must build: clean=%v err=%v", clean, err)
	}
	if !quizIDRe.MatchString(q.ID) || q.Source != "what we have in r2?" || len(q.Puzzles[0].Words) != 6 {
		t.Fatalf("engine fields not filled: %+v", q)
	}
	if _, clean, err = buildQuiz(`{"clean": true}`, "x"); err != nil || !clean {
		t.Fatalf("clean verdict: clean=%v err=%v", clean, err)
	}
	if _, clean, err = buildQuiz("{\"clean\": true}\n\nWait, actually: "+modelQuizJSON, "x"); err != nil || !clean {
		t.Fatalf("the first verdict stands when the model second-guesses itself: clean=%v err=%v", clean, err)
	}
	if _, _, err = buildQuiz("sorry, I can't", "x"); err == nil {
		t.Fatal("garbage must fail")
	}
}

const checkOK = `{"rule_ok": true, "reorder_other_orders": [], "choose_correct": ["does"], "write_answer_ok": true, "write_bad_accept": [], "note": ""}`

// The real generator: maker + checker. A quiz the validator or the checker
// rejects gets one retry that is told why; two rejections mean no quiz; a
// clean verdict means no quiz, no checker and no error; a failing checker
// leaves the validated quiz alone.
func TestGenerateQuizRetry(t *testing.T) {
	tb := newTestBot(t, "zoro")
	broken := strings.Replace(modelQuizJSON, `"answer": "does"`, `"answer": "did"`, 1)
	var makes, checks []string
	tb.reply = func(p string) (claude.Result, error) {
		q := &makes
		if strings.HasPrefix(p, "Quiz to check") {
			q = &checks
		}
		a := (*q)[0]
		*q = (*q)[1:]
		if a == "ERR" {
			return claude.Result{}, errors.New("checker down")
		}
		return claude.Result{Text: a}, nil
	}
	ctx := context.Background()
	gen := func() (quiz, bool, error) { return tb.generateQuiz(ctx, "what we have in r2?", false) }

	makes, checks = []string{broken, modelQuizJSON}, []string{checkOK}
	if q, ok, err := gen(); err != nil || !ok || q.Rule.Title == "" || !strings.Contains(tb.calls[1], "REJECTED") {
		t.Fatalf("an invalid quiz gets one retry: ok=%v err=%v calls=%q", ok, err, tb.calls)
	}
	makes, checks = []string{modelQuizJSON, modelQuizJSON}, []string{strings.Replace(checkOK, `["does"]`, `["does", "did"]`, 1), checkOK}
	if _, ok, err := gen(); err != nil || !ok || !strings.Contains(tb.calls[len(tb.calls)-2], `the options that fit are ["does" "did"]`) {
		t.Fatalf("a choose with two right options gets a retry: ok=%v err=%v", ok, err)
	}
	makes, checks = []string{broken, broken}, nil
	if _, ok, err := gen(); err == nil || ok {
		t.Fatalf("two rejections = no quiz: ok=%v err=%v", ok, err)
	}
	makes, checks = []string{modelQuizJSON, modelQuizJSON}, []string{`{"rule_ok": false, "note": "too many is fine here"}`, `{"rule_ok": false}`}
	if _, ok, err := gen(); err == nil || ok || !strings.Contains(err.Error(), "the rule or the correction is wrong") ||
		!strings.Contains(tb.calls[len(tb.calls)-2], "(too many is fine here)") {
		t.Fatalf("a wrong rule twice = no quiz: ok=%v err=%v", ok, err)
	}
	makes, checks = []string{modelQuizJSON}, []string{"ERR"}
	if _, ok, err := gen(); err != nil || !ok {
		t.Fatalf("a failing checker keeps the validated quiz: ok=%v err=%v", ok, err)
	}
	n := tb.nCalls()
	makes, checks = []string{`{"clean": true}`}, nil
	if _, ok, err := tb.generateQuiz(ctx, "how can we add the new v4 to stg?", false); err != nil || ok || tb.nCalls() != n+1 {
		t.Fatalf("clean = no quiz, no checker: ok=%v err=%v", ok, err)
	}
}

func TestApplyCheck(t *testing.T) {
	q := goodQuiz()
	q.Puzzles[0] = quizPuzzle{Type: "reorder", Answer: "Please check the VPS now."}
	q.Puzzles[2].Accept = []string{"Where does Angel keep the Meta keys?", "Where Ángel does keep the Meta keys?"}
	fillReorder(&q)
	c := quizCheck{RuleOK: true, ChooseCorrect: []string{"Does"}, WriteOK: true,
		ReorderOthers: []string{"Now please check the VPS.", "Please check the server now."},
		WriteBad:      []string{"Where Ángel does keep the Meta keys"}}
	if err := applyCheck(&q, c); err != nil {
		t.Fatalf("fixable: %v", err)
	}
	if got := q.Puzzles[0].Accept; len(got) != 1 || got[0] != "Now please check the VPS." {
		t.Fatalf("other right orders of the same tiles become accepted (other words don't): %q", got)
	}
	if got := q.Puzzles[2].Accept; len(got) != 1 || got[0] != "Where does Angel keep the Meta keys?" {
		t.Fatalf("wrong accepts go: %q", got)
	}
	for name, c := range map[string]quizCheck{
		"wrong rule":      {RuleOK: false, ChooseCorrect: []string{"does"}, WriteOK: true},
		"two fit":         {RuleOK: true, ChooseCorrect: []string{"does", "is"}, WriteOK: true},
		"none fit":        {RuleOK: true, WriteOK: true},
		"other fits":      {RuleOK: true, ChooseCorrect: []string{"do"}, WriteOK: true},
		"write answer ko": {RuleOK: true, ChooseCorrect: []string{"does"}, WriteOK: false},
	} {
		q := goodQuiz()
		if err := applyCheck(&q, c); err == nil {
			t.Errorf("%s: must be rejected", name)
		}
	}
}

// --- the wall -------------------------------------------------------------

type quizStub struct {
	mu    sync.Mutex
	texts []string
	err   error
}

func (s *quizStub) n() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.texts) }

// quizBot: gate on, a quiz site in a temp dir, and a stand-in quiz maker that
// flags "we have" (missing do) and Spanish, and passes anything else.
func quizBot(t *testing.T) (*testBot, *quizStub) {
	t.Helper()
	tb := newTestBot(t, "zoro")
	tb.cfg.QuizDir = filepath.Join(t.TempDir(), "coach")
	tb.cfg.QuizURL = "https://coach.example"
	if err := tb.set.SetGate(true); err != nil {
		t.Fatal(err)
	}
	st := &quizStub{}
	tb.makeQuiz = func(_ context.Context, text string, spanish bool) (quiz, bool, error) {
		st.mu.Lock()
		st.texts = append(st.texts, text)
		err := st.err
		st.mu.Unlock()
		if err != nil {
			return quiz{}, false, err
		}
		if !strings.Contains(text, "we have") && !spanish {
			return quiz{}, false, nil
		}
		q := goodQuiz()
		q.ID, q.Source = newQuizID(), text
		if spanish {
			q.Kind = quizSpanish
		}
		return q, true, nil
	}
	return tb, st
}

// run processes a job and waits for the quiz it may have started.
func (tb *testBot) runJob(j job) {
	tb.process(context.Background(), j)
	tb.quizWG.Wait()
}

// solve does what the page's PUT does.
func (tb *testBot) solve(t *testing.T) {
	t.Helper()
	p, ok := tb.pendingQuiz()
	if !ok {
		t.Fatal("no pending quiz to solve")
	}
	if err := os.MkdirAll(filepath.Dir(tb.solvedFile(p.ID)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tb.solvedFile(p.ID), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// takeJob pops the job quizTick queued (the release turn).
func (tb *testBot) takeJob(t *testing.T) job {
	t.Helper()
	select {
	case j := <-tb.jobs:
		return j
	case <-time.After(time.Second):
		t.Fatal("no job queued")
	}
	return job{}
}

func TestQuizWallHoldsAndReleasesInOrder(t *testing.T) {
	tb, st := quizBot(t)
	tb.runJob(ownerJob("what we have in r2?"))
	if tb.nCalls() != 1 || st.n() != 1 {
		t.Fatalf("the message must be answered AND judged: calls=%d quiz=%d", tb.nCalls(), st.n())
	}
	p, ok := tb.pendingQuiz()
	if !ok || tb.sentWith("🧩 Quick quiz before my next answer: https://coach.example/?q="+p.ID) != 1 || tb.sentWith("Questions: do/does/did first") != 1 {
		t.Fatalf("expected the quiz link, sent=%q", tb.sent)
	}
	if _, err := os.Stat(tb.quizFile(p.ID)); err != nil {
		t.Fatalf("quiz JSON must be published: %v", err)
	}

	// Wall up: held, ONE nudge, no Claude call — even for "ok".
	tb.runJob(ownerJob("ok dale"))
	tb.runJob(ownerJob("and how is tequila doing"))
	if tb.nCalls() != 1 || tb.sentWith("🧩 Solve the quiz first: "+p.URL+" (start with ! to skip)") != 1 {
		t.Fatalf("held messages must not reach Claude and nudge once: calls=%d sent=%q", tb.nCalls(), tb.sent)
	}
	if msgs, _ := readPending(tb.quizHeldFile()); len(msgs) != 2 {
		t.Fatalf("both messages must be held, got %d", len(msgs))
	}
	tb.quizTick()
	if len(tb.jobs) != 0 {
		t.Fatal("nothing is released before the quiz is solved")
	}

	// Solved → wall down → one turn with both, in order.
	tb.solve(t)
	tb.quizTick()
	if _, ok := tb.pendingQuiz(); ok {
		t.Fatal("the wall must come down once solved")
	}
	tb.runJob(tb.takeJob(t))
	if tb.nCalls() != 2 {
		t.Fatalf("the release turn must run: calls=%d", tb.nCalls())
	}
	got := tb.calls[1]
	if i, k := strings.Index(got, "ok dale"), strings.Index(got, "and how is tequila doing"); i < 0 || k < i {
		t.Fatalf("held messages must arrive in order: %q", got)
	}
	if _, err := os.Stat(tb.quizHeldFile()); !os.IsNotExist(err) {
		t.Fatal("the held queue must be cleared after the turn")
	}
	if st.n() != 2 || st.texts[1] != "and how is tequila doing" {
		t.Fatalf("the released messages get judged too (short ones skipped): %q", st.texts)
	}
	tb.quizTick()
	if len(tb.jobs) != 0 {
		t.Fatal("no second release")
	}
	tb.runJob(ownerJob("thanks man"))
	if tb.nCalls() != 3 {
		t.Fatal("after the release, messages are answered again")
	}
}

func TestQuizWallLetsThrough(t *testing.T) {
	tb, st := quizBot(t)
	tb.runJob(ownerJob("what we have in r2?"))
	if _, ok := tb.pendingQuiz(); !ok {
		t.Fatal("no wall")
	}
	tb.runJob(ownerJob("! urgent: what we have in the backups?"))
	tb.runJob(ownerJob("/deep-research what we have in r2"))
	tb.runJob(job{chatID: 1, prompt: "cron prompt", label: "⏰ gastos"})
	tb.fireCron(cron.Job{ID: "x", Prompt: "p"})
	tb.runJob(tb.takeJob(t))
	if tb.nCalls() != 5 {
		t.Fatalf("!, /commands and crons must pass the wall: calls=%d", tb.nCalls())
	}
	if st.n() != 1 || tb.sentWith("Solve the quiz first") != 0 {
		t.Fatalf("they make no quiz and get no nudge: quiz=%d sent=%q", st.n(), tb.sent)
	}
	if _, ok := tb.pendingQuiz(); !ok {
		t.Fatal("the quiz stays pending after a !")
	}
}

func TestQuizNoQuizFor(t *testing.T) {
	tb, st := quizBot(t)
	for _, j := range []job{
		ownerJob("ok dale"),                  // short
		ownerJob("```go\nwe have := 1\n```"), // code
		ownerJob("new key apikey_2476bad77beb3d34f79ab8a17fe23f9ce8c for we have"), // credential
		ownerJob("https://x.com/a/status/1 we"),                                    // a link and one word
	} {
		tb.runJob(j)
	}
	if st.n() != 0 {
		t.Fatalf("short, code, credentials and bare links make no quiz: %q", st.texts)
	}
	if got := quizCandidate([]string{"check https://x.com/a/b?s=46 this have utility in our workflow?"}); got != "check  this have utility in our workflow?" {
		t.Fatalf("links are cut out: %q", got)
	}
	if heldQuizText([]pendingMsg{{Text: "what we have", Transcripts: []string{"we have"}}}) != "" {
		t.Fatal("voice notes make no quiz")
	}
	if quizCandidate(nil) != "" {
		t.Fatal("a voice note has no typed text: no quiz")
	}
}

// A quiz that can't be made (rejected twice, timeout…) = no wall: the next
// message is answered.
func TestQuizBrokenNeverBlocks(t *testing.T) {
	tb, st := quizBot(t)
	st.err = errors.New("rejected twice")
	tb.runJob(ownerJob("what we have in r2?"))
	tb.runJob(ownerJob("and what we have in the mac?"))
	if tb.nCalls() != 2 || tb.sentWith("🧩") != 0 {
		t.Fatalf("no quiz = no wall: calls=%d sent=%q", tb.nCalls(), tb.sent)
	}
	if _, ok := tb.pendingQuiz(); ok {
		t.Fatal("no wall")
	}
}

// The wall survives a restart (the state is on disk), and /gate off takes it
// down and releases the queue.
func TestQuizRestartAndGateOff(t *testing.T) {
	tb, _ := quizBot(t)
	tb.runJob(ownerJob("what we have in r2?"))
	p, _ := tb.pendingQuiz()

	// "restart": a bot that has to load everything from disk.
	tb.quizCur, tb.quizLoaded = nil, false
	tb.quizHeld.Store(false)
	tb.runJob(ownerJob("are you there?"))
	if tb.nCalls() != 1 || tb.sentWith("Solve the quiz first: "+p.URL) != 1 {
		t.Fatalf("after a restart the wall must still hold: calls=%d sent=%q", tb.nCalls(), tb.sent)
	}
	tb.quizCur, tb.quizLoaded = nil, false
	tb.quizHeld.Store(false)
	tb.runJob(ownerJob("hello again man"))
	if tb.sentWith("Solve the quiz first") != 1 {
		t.Fatal("the nudge is once per quiz, restart or not")
	}

	n, err := tb.gateOff()
	if err != nil || n != 2 {
		t.Fatalf("/gate off: n=%d err=%v", n, err)
	}
	if _, ok := tb.pendingQuiz(); ok || tb.set.Get().Gate {
		t.Fatal("/gate off takes the wall down")
	}
	tb.runJob(tb.takeJob(t))
	if tb.nCalls() != 2 || !strings.Contains(tb.calls[1], "are you there?") || !strings.Contains(tb.calls[1], "hello again man") {
		t.Fatalf("/gate off must release the held messages: %q", tb.calls)
	}
	tb.runJob(ownerJob("what we have in the mac?"))
	if tb.nCalls() != 3 || tb.sentWith("Quick quiz") != 1 {
		t.Fatal("gate off: answered, no quiz")
	}
}

// A paused engine keeps the held messages until /start; a quiz whose page is
// gone can't be solved, so it doesn't keep the wall up.
func TestQuizPausedAndMissingPage(t *testing.T) {
	tb, _ := quizBot(t)
	tb.runJob(ownerJob("what we have in r2?"))
	tb.runJob(ownerJob("are you there?"))
	pause(t, tb)
	tb.solve(t)
	tb.quizTick()
	if len(tb.jobs) != 0 {
		t.Fatal("paused: nothing released")
	}
	if err := os.Remove(tb.pauseFile()); err != nil {
		t.Fatal(err)
	}
	tb.quizTick()
	tb.runJob(tb.takeJob(t))
	if tb.nCalls() != 2 {
		t.Fatalf("after the pause the held message is answered: calls=%d", tb.nCalls())
	}

	tb.runJob(ownerJob("and what we have in the mac?"))
	p, ok := tb.pendingQuiz()
	if !ok {
		t.Fatal("expected a second quiz")
	}
	_ = os.Remove(tb.quizFile(p.ID))
	tb.quizTick()
	if _, ok := tb.pendingQuiz(); ok {
		t.Fatal("a quiz with no page must not keep the wall")
	}
}

// Spanish gets a quiz unless gate_spanish_off; sub-agents (no quiz site) never
// quiz, gate on or not; gate off = no quiz.
func TestQuizSwitches(t *testing.T) {
	tb, st := quizBot(t)
	tb.runJob(ownerJob("oye wey puedes revisar el disco de la vps porfa"))
	if p, ok := tb.pendingQuiz(); !ok || st.n() != 1 {
		t.Fatalf("Spanish must get a quiz: %+v", p)
	}

	tb2, st2 := quizBot(t)
	_ = tb2.set.SetGateSpanishOff(true)
	tb2.runJob(ownerJob("oye wey puedes revisar el disco de la vps porfa"))
	tb2.runJob(ownerJob("what we have in r2?"))
	if st2.n() != 1 || st2.texts[0] != "what we have in r2?" {
		t.Fatalf("gate_spanish_off: no Spanish quiz call, English still judged: %q", st2.texts)
	}

	sky, st3 := quizBot(t)
	sky.cfg.AgentName, sky.cfg.QuizDir = "sky", ""
	sky.runJob(ownerJob("what we have in r2?"))
	sky.runJob(ownerJob("what we have in the mac?"))
	if st3.n() != 0 || sky.nCalls() != 2 {
		t.Fatalf("no quiz site = no quiz, no wall: quiz=%d calls=%d", st3.n(), sky.nCalls())
	}

	off, st4 := quizBot(t)
	_ = off.set.SetGate(false)
	off.runJob(ownerJob("what we have in r2?"))
	if st4.n() != 0 || off.nCalls() != 1 {
		t.Fatal("gate off: no quiz")
	}
}

// One quiz at a time: a second message while the first quiz is being made
// doesn't start another one.
func TestQuizOneAtATime(t *testing.T) {
	tb, st := quizBot(t)
	release := make(chan struct{})
	inner := tb.makeQuiz
	tb.makeQuiz = func(ctx context.Context, text string, spanish bool) (quiz, bool, error) {
		<-release
		return inner(ctx, text, spanish)
	}
	tb.process(context.Background(), ownerJob("what we have in r2?"))
	tb.process(context.Background(), ownerJob("and what we have in the mac?"))
	close(release)
	tb.quizWG.Wait()
	if st.n() != 1 || tb.sentWith("Quick quiz") != 1 || tb.nCalls() != 2 {
		t.Fatalf("one quiz at a time, both answered: quiz=%d sent=%q calls=%d", st.n(), tb.sent, tb.nCalls())
	}
}

// A release turn that fails keeps the held messages and is not re-queued every
// tick (no error every 3 s); after the wait it runs again and clears the queue.
func TestQuizReleaseFailureBacksOff(t *testing.T) {
	tb, _ := quizBot(t)
	tb.runJob(ownerJob("what we have in r2?"))
	tb.runJob(ownerJob("are you there?"))
	tb.solve(t)
	tb.reply = func(string) (claude.Result, error) { return claude.Result{}, errors.New("boom") }
	tb.quizTick()
	tb.runJob(tb.takeJob(t))
	if msgs, _ := readPending(tb.quizHeldFile()); len(msgs) != 1 {
		t.Fatalf("a failed release keeps the held message, got %d", len(msgs))
	}
	tb.quizTick()
	if len(tb.jobs) != 0 {
		t.Fatal("no new release right after a failure")
	}
	tb.reply = nil
	tb.quizRetryAt.Store(time.Now().Add(-time.Second).UnixNano())
	tb.quizTick()
	tb.runJob(tb.takeJob(t))
	if _, err := os.Stat(tb.quizHeldFile()); !os.IsNotExist(err) || tb.quizRetryAt.Load() != 0 {
		t.Fatal("after the wait the release runs and clears the queue")
	}
}
