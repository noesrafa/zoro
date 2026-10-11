package bot

import (
	"context"
	"encoding/json"
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

// withExtras is goodQuiz with the optional fields the page uses for clarity:
// the rule's ❌/✅ example and each puzzle's Spanish goal and first-miss tip.
func withExtras() quiz {
	q := goodQuiz()
	q.Rule.Bad, q.Rule.Good = "Why Tequila takes long?", "Why does Tequila take long?"
	q.Puzzles[0].Es, q.Puzzles[0].Tip = "¿Qué tenemos en R2?", "Question word, then do"
	q.Puzzles[1].Es, q.Puzzles[1].Tip = "¿Por qué Tequila tarda tanto en contestar?", "Tequila = he/she/it"
	q.Puzzles[2].Es, q.Puzzles[2].Tip = "¿Dónde guarda Ángel las llaves de Meta?", "Ángel = he → does"
	return q
}

// The new fields are optional both ways: a quiz without them (every quiz made
// before 10-oct) is valid, and one with them is valid too.
func TestValidateQuizExtras(t *testing.T) {
	if err := validateQuiz(withExtras()); err != nil {
		t.Fatalf("a quiz with es, tip and the rule example must pass: %v", err)
	}
	q := withExtras()
	q.Puzzles[1].Es, q.Puzzles[2].Tip = "", ""
	if err := validateQuiz(q); err != nil {
		t.Fatalf("some extras missing is fine: %v", err)
	}
	for name, mutate := range map[string]func(q *quiz){
		"bad without good":  func(q *quiz) { q.Rule.Good = "" },
		"good without bad":  func(q *quiz) { q.Rule.Bad = " " },
		"example not fixed": func(q *quiz) { q.Rule.Good = "why tequila takes long" },
		"example too long":  func(q *quiz) { q.Rule.Bad = strings.Repeat("why Tequila takes long ", 4) },
		"title too long":    func(q *quiz) { q.Rule.Title = "Questions: do, does and did go first, always" },
		"rule too long": func(q *quiz) {
			q.Rule.Text = "In a question, do/does/did goes before the subject, and the verb stays in its base form: What do we have? Why does it fail?"
		},
		"es is english":    func(q *quiz) { q.Puzzles[0].Es = "what do we have in r2" },
		"es too long":      func(q *quiz) { q.Puzzles[2].Es = strings.Repeat("¿Dónde guarda Ángel las llaves? ", 6) },
		"tip too long":     func(q *quiz) { q.Puzzles[1].Tip = "Tequila is one person so use the form with s here" },
		"tip gives it all": func(q *quiz) { q.Puzzles[2].Tip = "Where does Ángel keep the Meta keys?" },
	} {
		q := withExtras()
		mutate(&q)
		if err := validateQuiz(q); err == nil {
			t.Errorf("%s: must be rejected", name)
		}
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
	q, _, clean, err := buildQuiz(modelQuizJSON, "what we have in r2?")
	if err != nil || clean {
		t.Fatalf("fenced model JSON must build: clean=%v err=%v", clean, err)
	}
	if !quizIDRe.MatchString(q.ID) || q.Source != "what we have in r2?" || len(q.Puzzles[0].Words) != 6 {
		t.Fatalf("engine fields not filled: %+v", q)
	}
	praise := ""
	if _, praise, clean, err = buildQuiz(`{"clean": true, "praise": "✅ \"did you send\" — did first."}`, "x"); err != nil || !clean || praise != `✅ "did you send" — did first.` {
		t.Fatalf("clean verdict and its praise: clean=%v praise=%q err=%v", clean, praise, err)
	}
	if _, _, clean, err = buildQuiz("{\"clean\": true}\n\nWait, actually: "+modelQuizJSON, "x"); err != nil || !clean {
		t.Fatalf("the first verdict stands when the model second-guesses itself: clean=%v err=%v", clean, err)
	}
	if _, _, _, err = buildQuiz("sorry, I can't", "x"); err == nil {
		t.Fatal("garbage must fail")
	}
}

// The maker's new fields survive the build and the file the page reads.
func TestBuildQuizExtras(t *testing.T) {
	model := strings.NewReplacer(
		`"text": "In a question, do/does/did goes before the subject."}`, `"text": "In a question, do/does/did goes before the subject.", "bad": "Why Tequila takes long?", "good": "Why does Tequila take long?"}`,
		`"accept": []},`, `"accept": [], "es": "¿Qué tenemos en R2?", "tip": "Question word, then do"},`,
		`"hint": "Tequila = he/she/it"}`, `"hint": "Tequila = he/she/it", "es": "¿Por qué Tequila tarda tanto?", "tip": "Tequila = he/she/it"}`,
	).Replace(modelQuizJSON)
	q, _, clean, err := buildQuiz(model, "what we have in r2?")
	if err != nil || clean {
		t.Fatalf("model JSON with extras must build: clean=%v err=%v", clean, err)
	}
	path := filepath.Join(t.TempDir(), "q", q.ID+".json")
	if err := writeQuiz(path, q); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	for _, want := range []string{`"bad": "Why Tequila takes long?"`, `"es": "¿Qué tenemos en R2?"`, `"tip": "Question word, then do"`, `"es": "¿Por qué Tequila tarda tanto?"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the quiz file lost %s:\n%s", want, raw)
		}
	}
	if strings.Contains(string(raw), `"es": ""`) || strings.Contains(string(raw), `"tip": ""`) {
		t.Errorf("absent extras are left out of the file:\n%s", raw)
	}
	// Extras the validator would reject are dropped at build time, not retried.
	long := strings.Replace(model, `"tip": "Question word, then do"`, `"tip": "Start with the question word and then put do"`, 1)
	long = strings.Replace(long, `"bad": "Why Tequila takes long?", `, ``, 1)
	if q, _, _, err := buildQuiz(long, "what we have in r2?"); err != nil || q.Puzzles[0].Tip != "" || q.Puzzles[1].Tip == "" || q.Rule.Good != "" {
		t.Fatalf("a long tip and half an example go, the quiz stays: err=%v %+v", err, q)
	}
	if _, _, _, err := buildQuiz(modelQuizJSON, "what we have in r2?"); err != nil {
		t.Fatalf("model JSON without extras still builds: %v", err)
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
	gen4 := func() (quiz, bool, string, error) { return tb.generateQuiz(ctx, "what we have in r2?", false) }
	gen := func() (quiz, bool, error) {
		q, ok, _, err := tb.generateQuiz(ctx, "what we have in r2?", false)
		return q, ok, err
	}

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
	makes, checks = []string{`{"clean": true, "praise": "🎯 \"how can we add\" — can before we."}`}, nil
	if _, ok, praise, err := tb.generateQuiz(ctx, "how can we add the new v4 to stg?", false); err != nil || ok || tb.nCalls() != n+1 || !strings.Contains(praise, "can before we") {
		t.Fatalf("clean = no quiz, no checker, a praise line: ok=%v praise=%q err=%v", ok, praise, err)
	}
	// A mistake first and "clean" only on the retry: no quiz and no praise.
	makes, checks = []string{broken, `{"clean": true, "praise": "✅ nice"}`}, nil
	if _, ok, praise, err := gen4(); !errors.Is(err, errQuizUnsure) || ok || praise != "" {
		t.Fatalf("clean only on the retry must not be praised: ok=%v praise=%q err=%v", ok, praise, err)
	}
	makes, checks = []string{broken, broken}, nil
	if _, _, _, err := gen4(); !errors.Is(err, errQuizRejected) {
		t.Fatalf("two rejections are errQuizRejected (the streak breaks): %v", err)
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
	// A wrong es, tip or example goes; the quiz stands (the page has fallbacks).
	q = withExtras()
	c = quizCheck{RuleOK: true, ChooseCorrect: []string{"does"}, WriteOK: true, EsWrong: []int{2, 9}, TipWrong: []int{3}, ExampleWrong: true}
	if err := applyCheck(&q, c); err != nil {
		t.Fatalf("wrong extras are dropped, not rejected: %v", err)
	}
	if q.Puzzles[1].Es != "" || q.Puzzles[0].Es == "" || q.Puzzles[2].Es == "" {
		t.Fatalf("only puzzle 2's es goes: %+v", q.Puzzles)
	}
	if q.Puzzles[2].Tip != "" || q.Puzzles[0].Tip == "" || q.Rule.Bad != "" || q.Rule.Good != "" {
		t.Fatalf("puzzle 3's tip and the example go: %+v %+v", q.Rule, q.Puzzles)
	}
	if p := quizCheckPrompt(withExtras()); !strings.Contains(p, "es: ¿Qué tenemos en R2?") || !strings.Contains(p, "❌ Why Tequila takes long?") {
		t.Fatalf("the checker sees the extras:\n%s", p)
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

// --- the hub --------------------------------------------------------------

type quizStub struct {
	mu     sync.Mutex
	texts  []string
	err    error
	praise string // the clean verdict's line
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
	tb.makeQuiz = func(_ context.Context, text string, spanish bool) (quiz, bool, string, error) {
		st.mu.Lock()
		st.texts = append(st.texts, text)
		err, praise := st.err, st.praise
		st.mu.Unlock()
		if err != nil {
			return quiz{}, false, "", err
		}
		if !strings.Contains(text, "we have") && !spanish {
			return quiz{}, false, praise, nil
		}
		q := goodQuiz()
		q.ID, q.Source, q.Created = newQuizID(), text, time.Now().UTC().Format(time.RFC3339)
		if spanish {
			q.Kind = quizSpanish
		}
		return q, true, "", nil
	}
	return tb, st
}

// run processes a job and waits for the quiz it may have started.
func (tb *testBot) runJob(j job) {
	tb.process(context.Background(), j)
	tb.quizWG.Wait()
}

// hub reads hub.json as the page does.
func (tb *testBot) hub(t *testing.T) []hubEntry {
	t.Helper()
	raw, err := os.ReadFile(tb.hubFile())
	if err != nil {
		t.Fatalf("hub.json: %v", err)
	}
	var hub []hubEntry
	if err := json.Unmarshal(raw, &hub); err != nil {
		t.Fatalf("hub.json: %v", err)
	}
	return hub
}

// solve does what the page's PUT does.
func (tb *testBot) solve(t *testing.T, id string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(tb.solvedFile(id)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tb.solvedFile(id), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// takeJob pops the job quizTick queued (the old wall's release turn).
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

const hubNotice = "🧩 New quiz: https://coach.example/?q="

// noWallTexts: what the wall used to say. None of it may ever be sent.
func noWallTexts(t *testing.T, tb *testBot) {
	t.Helper()
	for _, bad := range []string{"before my next answer", "Solve the quiz first", "start with ! to skip", "held"} {
		if tb.sentWith(bad) != 0 {
			t.Fatalf("the wall's text %q went out: %q", bad, tb.sent)
		}
	}
}

// Nothing ever waits: every message is answered right away, the quiz is
// published with "hub": true and goes into hub.json, and there is no wall file.
func TestQuizNeverHolds(t *testing.T) {
	tb, st := quizBot(t)
	tb.runJob(ownerJob("what we have in r2?"))
	if tb.nCalls() != 1 || st.n() != 1 {
		t.Fatalf("the message must be answered AND judged: calls=%d quiz=%d", tb.nCalls(), st.n())
	}
	if tb.sentWith(hubNotice) != 1 {
		t.Fatalf("expected the hub line, sent=%q", tb.sent)
	}
	hub := tb.hub(t)
	if len(hub) != 1 || hub[0].Title != "Questions: do/does/did first" || hub[0].Kind != quizGrammar || hub[0].Created == "" {
		t.Fatalf("the quiz must be in the hub: %+v", hub)
	}
	raw, err := os.ReadFile(tb.quizFile(hub[0].ID))
	if err != nil || !strings.Contains(string(raw), `"hub": true`) {
		t.Fatalf("quiz JSON must be published with \"hub\": true: %v %s", err, raw)
	}

	// The next messages — short, long, another mistake — are answered at once.
	tb.runJob(ownerJob("ok dale"))
	tb.runJob(ownerJob("and how is tequila doing"))
	if tb.nCalls() != 3 {
		t.Fatalf("no message may wait for the quiz: calls=%d", tb.nCalls())
	}
	tb.quizTick()
	if len(tb.jobs) != 0 {
		t.Fatal("nothing is queued: nothing waited")
	}
	for _, f := range []string{tb.quizWallFile(), tb.wallQueueFile()} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Fatalf("no wall files: %s", f)
		}
	}
	noWallTexts(t, tb)
}

// Three quizzes in a row: all published, all in the hub (newest first), and each
// one announced with its own link (10-oct: he wants to see every quiz link).
func TestQuizHubStacksAndTellsOnce(t *testing.T) {
	tb, st := quizBot(t)
	for _, m := range []string{"what we have in r2?", "and what we have in the mac?", "so what we have for today?"} {
		tb.runJob(ownerJob(m))
	}
	if st.n() != 3 || tb.nCalls() != 3 {
		t.Fatalf("3 judged, 3 answered: quiz=%d calls=%d", st.n(), tb.nCalls())
	}
	hub := tb.hub(t)
	if len(hub) != 3 {
		t.Fatalf("3 quizzes in the hub: %+v", hub)
	}
	for _, e := range hub {
		if _, err := os.Stat(tb.quizFile(e.ID)); err != nil {
			t.Fatalf("each quiz is published: %v", err)
		}
	}
	if q := readQuizFile(t, tb, hub[0].ID); q.Source != "so what we have for today?" {
		t.Fatalf("newest first: %+v", hub)
	}
	if q := readQuizFile(t, tb, hub[2].ID); q.Source != "what we have in r2?" {
		t.Fatalf("oldest last: %+v", hub)
	}
	if tb.sentWith("🧩") != 3 || tb.sentWith(hubNotice) != 3 {
		t.Fatalf("each one is announced with its link: %q", tb.sent)
	}
	for _, e := range hub {
		if tb.sentWith("?q="+e.ID) != 1 {
			t.Fatalf("quiz %s announced with its own link: %q", e.ID, tb.sent)
		}
	}
	if g := tb.gateText(); !strings.Contains(g, "🧩 Quiz hub: 3 to do — https://coach.example/") {
		t.Fatalf("/gate shows the count and the link: %q", g)
	}

	// With some solved and some to do, every new one still gets its line.
	tb.solve(t, hub[0].ID)
	tb.runJob(ownerJob("where we have the keys?"))
	if tb.sentWith(hubNotice) != 4 || len(tb.hub(t)) != 4 {
		t.Fatalf("a 4th quiz, a 4th line: %q", tb.sent)
	}
	noWallTexts(t, tb)
}

func readQuizFile(t *testing.T, tb *testBot, id string) quiz {
	t.Helper()
	raw, err := os.ReadFile(tb.quizFile(id))
	if err != nil {
		t.Fatal(err)
	}
	var q quiz
	if err := json.Unmarshal(raw, &q); err != nil {
		t.Fatal(err)
	}
	return q
}

func writeTestQuiz(t *testing.T, tb *testBot, id, created, title string, extra string) {
	t.Helper()
	q := goodQuiz()
	q.ID, q.Created, q.Rule.Title = id, created, title
	raw, _ := json.Marshal(q)
	if extra != "" {
		raw = []byte(strings.TrimSuffix(string(raw), "}") + "," + extra + "}")
	}
	if err := os.MkdirAll(filepath.Dir(tb.quizFile(id)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tb.quizFile(id), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// No hub.json yet: it is built from the quizzes already in q/ (newest first,
// the practice quiz marked), and the solved ones don't count as to do. An
// existing hub.json keeps its entries and gets the q/ quizzes it lacks.
func TestQuizHubBackfill(t *testing.T) {
	tb, _ := quizBot(t)
	writeTestQuiz(t, tb, "aaaaaaaaaa01", "2026-10-09T19:00:00Z", "Old one", "")
	writeTestQuiz(t, tb, "aaaaaaaaaa02", "2026-10-10T12:00:00Z", "New one", "")
	writeTestQuiz(t, tb, quizPracticeID, "2026-10-09T22:00:00Z", "Practice", `"practice":true`)
	writeTestQuiz(t, tb, "aaaaaaaaaa03", "2026-10-09T23:00:00Z", "Hand practice", `"practice":true`)
	_ = os.WriteFile(filepath.Join(tb.cfg.QuizDir, "q", "broken0000xx.json"), []byte("{"), 0o644)
	tb.solve(t, "aaaaaaaaaa01")

	tb.hubInit()
	hub := tb.hub(t)
	var ids []string
	for _, e := range hub {
		ids = append(ids, e.ID)
	}
	if strings.Join(ids, ",") != "aaaaaaaaaa02,aaaaaaaaaa03,"+quizPracticeID+",aaaaaaaaaa01" {
		t.Fatalf("all valid quizzes, newest first: %v", ids)
	}
	if !hub[1].Practice || !hub[2].Practice || hub[0].Practice || hub[0].Title != "New one" || hub[0].Kind != quizGrammar {
		t.Fatalf("practice marked, titles and kinds kept: %+v", hub)
	}
	if n, _ := tb.hubToDo(); n != 1 {
		t.Fatalf("to do = not solved, not practice: %d", n)
	}

	// Already there: kept as it is, only the q/ quizzes it doesn't list are
	// added (the old engine published without adding them).
	if err := os.WriteFile(tb.hubFile(), []byte(`[{"id":"aaaaaaaaaa02","title":"Kept","kind":"grammar","created":"2026-10-10T12:00:00Z"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTestQuiz(t, tb, "aaaaaaaaaa04", "2026-10-10T14:00:00Z", "Made by the old engine", "")
	tb.hubInit()
	hub = tb.hub(t)
	if len(hub) != 5 || hub[0].ID != "aaaaaaaaaa04" || hub[1].Title != "Kept" {
		t.Fatalf("an existing hub.json is kept and completed: %+v", hub)
	}
	tb.hubInit()
	if len(tb.hub(t)) != 5 {
		t.Fatal("a second start adds nothing")
	}

	// The first quiz the engine makes on a site with no hub.json yet: the
	// hub is built from q/ (it already holds the new quiz) and it's still told.
	tb2, _ := quizBot(t)
	tb2.runJob(ownerJob("what we have in r2?"))
	if len(tb2.hub(t)) != 1 || tb2.sentWith(hubNotice) != 1 {
		t.Fatalf("first quiz on a fresh site: hub=%+v sent=%q", tb2.hub(t), tb2.sent)
	}
}

// writeOldWall leaves what the old engine left: its pending quiz (published)
// in <state>/quiz.json and two messages it kept in quiz-held.jsonl.
func writeOldWall(t *testing.T, tb *testBot) string {
	t.Helper()
	id := "oldwall00001"
	writeTestQuiz(t, tb, id, "2026-10-10T13:00:00Z", "Questions: do/does/did first", "")
	w, _ := json.Marshal(map[string]any{"id": id, "url": "https://coach.example/?q=" + id, "title": "Questions: do/does/did first", "created": "2026-10-10T13:00:00Z", "chat_id": 1, "nudged": true})
	if err := os.WriteFile(tb.quizWallFile(), w, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"are you there?", "and how is tequila doing"} {
		if err := appendPending(tb.wallQueueFile(), pendingMsg{ChatID: 1, From: "Rafa", At: "2026-10-10 13:05", Text: m}); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// The old wall at start: its quiz goes into the hub, the wall file is deleted
// and the messages it kept are answered as one turn, in order — none is lost.
func TestQuizMigratesOldWall(t *testing.T) {
	tb, st := quizBot(t)
	id := writeOldWall(t, tb)
	if err := os.WriteFile(tb.hubFile(), []byte(`[]`), 0o644); err != nil { // a hub that doesn't list it yet
		t.Fatal(err)
	}
	tb.hubInit()
	if _, err := os.Stat(tb.quizWallFile()); !os.IsNotExist(err) {
		t.Fatal("the old wall must be deleted")
	}
	hub := tb.hub(t)
	if len(hub) != 1 || hub[0].ID != id || hub[0].Title != "Questions: do/does/did first" {
		t.Fatalf("the old wall's quiz goes into the hub: %+v", hub)
	}
	tb.runJob(tb.takeJob(t))
	if tb.nCalls() != 1 {
		t.Fatalf("the release turn must run: calls=%d", tb.nCalls())
	}
	got := tb.calls[0]
	if i, k := strings.Index(got, "are you there?"), strings.Index(got, "and how is tequila doing"); i < 0 || k < i {
		t.Fatalf("the old wall's messages arrive in order: %q", got)
	}
	if _, err := os.Stat(tb.wallQueueFile()); !os.IsNotExist(err) {
		t.Fatal("the old queue must be cleared after the turn")
	}
	if st.n() != 1 || st.texts[0] != "are you there?\nand how is tequila doing" {
		t.Fatalf("those messages get judged too: %q", st.texts)
	}
	tb.quizTick()
	if len(tb.jobs) != 0 {
		t.Fatal("no second release")
	}
	tb.hubInit() // a second start: nothing to migrate
	if len(tb.hub(t)) != 1 || len(tb.jobs) != 0 {
		t.Fatal("a second start changes nothing")
	}
	noWallTexts(t, tb)
}

// A paused engine keeps the old wall's messages until /start.
func TestQuizMigratePaused(t *testing.T) {
	tb, _ := quizBot(t)
	writeOldWall(t, tb)
	pause(t, tb)
	tb.hubInit()
	tb.quizTick()
	if len(tb.jobs) != 0 {
		t.Fatal("paused: nothing released")
	}
	if _, err := os.Stat(tb.quizWallFile()); !os.IsNotExist(err) {
		t.Fatal("the wall goes even while paused (it holds nothing new)")
	}
	if err := os.Remove(tb.pauseFile()); err != nil {
		t.Fatal(err)
	}
	tb.quizTick()
	tb.runJob(tb.takeJob(t))
	if tb.nCalls() != 1 || !strings.Contains(tb.calls[0], "are you there?") {
		t.Fatalf("after the pause they are answered: %q", tb.calls)
	}
}

// !, /commands and crons are answered and never judged.
func TestQuizSkips(t *testing.T) {
	tb, st := quizBot(t)
	tb.runJob(ownerJob("! urgent: what we have in the backups?"))
	tb.runJob(ownerJob("/deep-research what we have in r2"))
	tb.runJob(job{chatID: 1, prompt: "cron prompt", label: "⏰ gastos"})
	tb.fireCron(cron.Job{ID: "x", Prompt: "p"})
	tb.runJob(tb.takeJob(t))
	if tb.nCalls() != 4 || st.n() != 0 || tb.sentWith("🧩") != 0 {
		t.Fatalf("!, /commands and crons: answered, no quiz: calls=%d quiz=%d sent=%q", tb.nCalls(), st.n(), tb.sent)
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
	if wallQueueQuizText([]pendingMsg{{Text: "what we have", Transcripts: []string{"we have"}}}) != "" {
		t.Fatal("voice notes make no quiz")
	}
	if quizCandidate(nil) != "" {
		t.Fatal("a voice note has no typed text: no quiz")
	}
}

// A quiz that can't be made (rejected twice, timeout…) = nothing in the hub,
// no line.
func TestQuizBrokenGoesNowhere(t *testing.T) {
	tb, st := quizBot(t)
	st.err = errors.New("rejected twice")
	tb.runJob(ownerJob("what we have in r2?"))
	tb.runJob(ownerJob("and what we have in the mac?"))
	if tb.nCalls() != 2 || tb.sentWith("🧩") != 0 {
		t.Fatalf("no quiz, both answered: calls=%d sent=%q", tb.nCalls(), tb.sent)
	}
	if n, _ := tb.hubToDo(); n != 0 || len(tb.hub(t)) != 0 {
		t.Fatal("nothing in the hub")
	}
}

// /gate off: no new quizzes, the hub stays; the texts don't promise a wall.
func TestQuizGateOffKeepsHub(t *testing.T) {
	tb, _ := quizBot(t)
	tb.handleGate(context.Background(), 1, []string{"/gate", "on"})
	tb.runJob(ownerJob("what we have in r2?"))
	tb.handleGate(context.Background(), 1, []string{"/gate", "off"})
	if tb.set.Get().Gate || len(tb.hub(t)) != 1 {
		t.Fatal("/gate off: gate off, hub kept")
	}
	tb.runJob(ownerJob("what we have in the mac?"))
	if tb.nCalls() != 2 || len(tb.hub(t)) != 1 || tb.sentWith("New quiz") != 1 {
		t.Fatal("gate off: answered, no quiz")
	}
	if tb.sentWith("lands in your hub: https://coach.example/") != 1 || tb.sentWith("Your hub stays: https://coach.example/") != 1 {
		t.Fatalf("/gate on|off texts: %q", tb.sent)
	}
	noWallTexts(t, tb)
}

// Spanish gets a quiz unless gate_spanish_off; sub-agents (no quiz site) never
// quiz, gate on or not; gate off = no quiz.
func TestQuizSwitches(t *testing.T) {
	tb, st := quizBot(t)
	tb.runJob(ownerJob("oye wey puedes revisar el disco de la vps porfa"))
	if hub := tb.hub(t); len(hub) != 1 || hub[0].Kind != quizSpanish || st.n() != 1 {
		t.Fatalf("Spanish must get a quiz: %+v", hub)
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
	sky.hubInit()
	if st3.n() != 0 || sky.nCalls() != 2 {
		t.Fatalf("no quiz site = no quiz: quiz=%d calls=%d", st3.n(), sky.nCalls())
	}
	if _, err := os.Stat("hub.json"); !os.IsNotExist(err) {
		t.Fatal("no quiz site: no hub.json anywhere")
	}

	off, st4 := quizBot(t)
	_ = off.set.SetGate(false)
	off.runJob(ownerJob("what we have in r2?"))
	if st4.n() != 0 || off.nCalls() != 1 {
		t.Fatal("gate off: no quiz")
	}
}

// One quiz made at a time: a second message while the first quiz is being made
// is answered but doesn't start another one.
func TestQuizOneAtATime(t *testing.T) {
	old := praiseWait
	praiseWait = 50 * time.Millisecond // the 1st answer waits for a verdict that is kept back
	t.Cleanup(func() { praiseWait = old })
	tb, st := quizBot(t)
	release := make(chan struct{})
	inner := tb.makeQuiz
	tb.makeQuiz = func(ctx context.Context, text string, spanish bool) (quiz, bool, string, error) {
		<-release
		return inner(ctx, text, spanish)
	}
	tb.process(context.Background(), ownerJob("what we have in r2?"))
	tb.process(context.Background(), ownerJob("and what we have in the mac?"))
	close(release)
	tb.quizWG.Wait()
	if st.n() != 1 || tb.sentWith("🧩 New quiz: ") != 1 || tb.nCalls() != 2 || len(tb.hub(t)) != 1 {
		t.Fatalf("one quiz at a time, both answered: quiz=%d sent=%q calls=%d", st.n(), tb.sent, tb.nCalls())
	}
}

// A release turn (the old wall's queue) that fails keeps the messages and is
// not re-queued every tick; after the wait it runs again and clears the queue.
func TestQuizReleaseFailureBacksOff(t *testing.T) {
	tb, _ := quizBot(t)
	writeOldWall(t, tb)
	tb.reply = func(string) (claude.Result, error) { return claude.Result{}, errors.New("boom") }
	tb.hubInit()
	tb.runJob(tb.takeJob(t))
	if msgs, _ := readPending(tb.wallQueueFile()); len(msgs) != 2 {
		t.Fatalf("a failed release keeps the messages, got %d", len(msgs))
	}
	tb.quizTick()
	if len(tb.jobs) != 0 {
		t.Fatal("no new release right after a failure")
	}
	tb.reply = nil
	tb.quizRetryAt.Store(time.Now().Add(-time.Second).UnixNano())
	tb.quizTick()
	tb.runJob(tb.takeJob(t))
	if _, err := os.Stat(tb.wallQueueFile()); !os.IsNotExist(err) || tb.quizRetryAt.Load() != 0 {
		t.Fatal("after the wait the release runs and clears the queue")
	}
}

// Both quiz calls run bare (their own system prompt, no tools/MCP/skills): the
// job needs ~2K tokens, Claude Code's defaults cost ~28K per call.
func TestQuizCallsAreBare(t *testing.T) {
	tb, _ := quizBot(t)
	var opts []claude.RunOpts
	tb.run = func(_ context.Context, sid string, _ bool, p string, o claude.RunOpts) (claude.Result, error) {
		opts = append(opts, o)
		if strings.HasPrefix(p, "Quiz to check") {
			return claude.Result{Text: checkOK}, nil
		}
		return claude.Result{Text: modelQuizJSON}, nil
	}
	if _, ok, _, err := tb.generateQuiz(context.Background(), "what we have in r2?", false); err != nil || !ok {
		t.Fatalf("quiz: ok=%v err=%v", ok, err)
	}
	if len(opts) != 2 {
		t.Fatalf("maker + checker = 2 calls, got %d", len(opts))
	}
	for i, o := range opts {
		if !o.Bare || o.SystemPrompt == "" {
			t.Errorf("call %d must be bare with its own system prompt: %+v", i, o)
		}
	}
}
