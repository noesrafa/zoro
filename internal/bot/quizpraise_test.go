package bot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// sentIndex is the position of the first sent message containing sub (-1: none).
func (tb *testBot) sentIndex(sub string) int {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	for i, s := range tb.sent {
		if strings.Contains(s, sub) {
			return i
		}
	}
	return -1
}

func (tb *testBot) nSent() int { tb.mu.Lock(); defer tb.mu.Unlock(); return len(tb.sent) }

// Clean typed English: the praise goes out BEFORE the answer, the streak counts
// and shows from 3; a line that repeats a recent one is swapped for a generic.
// A mistake: the quiz, no praise, the streak back to 0.
func TestQuizPraiseCleanThenMistake(t *testing.T) {
	tb, st := quizBot(t)
	tb.reply = nil
	st.praise = `✅ "how is Tequila" — is before Tequila. Perfect.`
	tb.runJob(ownerJob("how is Tequila doing today?"))
	pi, ai := tb.sentIndex(`✅ "how is Tequila" — is before Tequila. Perfect.`), tb.sentIndex("ok")
	if pi < 0 || ai < 0 || pi > ai || tb.nCalls() != 1 {
		t.Fatalf("the praise must go out before the answer: praise=%d answer=%d sent=%q", pi, ai, tb.sent)
	}
	if tb.sentWith("🔥") != 0 || tb.loadStreak().Streak != 1 {
		t.Fatalf("streak 1, not shown yet: %+v sent=%q", tb.loadStreak(), tb.sent)
	}

	// Same line again: it's a repeat, so a generic one goes instead.
	tb.runJob(ownerJob("how is Tequila doing now?"))
	if tb.sentWith(`"how is Tequila"`) != 1 || tb.loadStreak().Streak != 2 {
		t.Fatalf("a repeated praise must be swapped: sent=%q", tb.sent)
	}
	var generic bool
	for _, g := range praiseGeneric {
		if tb.sentWith(g) == 1 {
			generic = true
		}
	}
	if !generic {
		t.Fatalf("expected a generic line, sent=%q", tb.sent)
	}

	st.praise = `🎯 "can you check" — can before you, nice.`
	tb.runJob(ownerJob("can you check the backups?"))
	if tb.sentWith("🎯 \"can you check\" — can before you, nice.\n🔥 3 clean in a row") != 1 {
		t.Fatalf("from 3 on the streak rides on the praise: sent=%q", tb.sent)
	}
	st.praise = `💪 "want to deploy" — to + verb, right.`
	tb.runJob(ownerJob("I want to deploy it tonight"))
	if tb.sentWith("🔥 4 clean in a row") != 1 {
		t.Fatalf("streak 4: sent=%q", tb.sent)
	}
	if g := tb.gateText(); !strings.Contains(g, "🔥 Clean streak: 4 in a row (best 4)") {
		t.Fatalf("/gate shows the streak: %q", g)
	}

	// A mistake: quiz, no praise, streak 0 (best kept).
	n := tb.nSent()
	tb.runJob(ownerJob("what we have in r2?"))
	if tb.sentWith("Quick quiz") != 1 || tb.nSent() != n+2 {
		t.Fatalf("a mistake gets the answer and the quiz only: sent=%q", tb.sent[n:])
	}
	for _, s := range tb.sent[n:] {
		if strings.Contains(s, "clean in a row") || strings.Contains(s, "want to deploy") {
			t.Fatalf("no praise for a mistake: %q", s)
		}
	}
	if s := tb.loadStreak(); s.Streak != 0 || s.Best != 4 {
		t.Fatalf("a quiz breaks the streak: %+v", s)
	}
	if g := tb.gateText(); !strings.Contains(g, "🔥 Clean streak: 0 in a row (best 4)") {
		t.Fatalf("/gate after the quiz: %q", g)
	}
	// The streak is on disk: a restart keeps it.
	tb2 := newTestBot(t, "zoro")
	tb2.cfg.StateDir = tb.cfg.StateDir
	if tb2.loadStreak().Best != 4 {
		t.Fatal("the streak must survive a restart")
	}
}

// No praise and no streak change for what the quiz doesn't judge — short,
// "!", commands, crons, voice notes — nor for Spanish, nor with the gate off.
func TestQuizPraiseNotFor(t *testing.T) {
	tb, st := quizBot(t)
	st.praise = "✅ nice"
	tb.updateStreak(func(s *quizStreak) { s.Streak = 2 })
	for _, j := range []job{
		ownerJob("ok"), ownerJob("Continue"), ownerJob("ok dale"),
		ownerJob("! how is Tequila doing today?"),
		ownerJob("/gate"),
		{chatID: 1, prompt: "how is Tequila doing today?", label: "⏰ cron"},
	} {
		tb.runJob(j)
	}
	if st.n() != 0 || tb.sentWith("✅ nice") != 0 || tb.loadStreak().Streak != 2 {
		t.Fatalf("nothing judged, nothing praised: judged=%q sent=%q streak=%+v", st.texts, tb.sent, tb.loadStreak())
	}
	if heldQuizText([]pendingMsg{{Transcripts: []string{"how is Tequila doing today"}}}) != "" {
		t.Fatal("voice notes are never judged, so never praised")
	}

	// Spanish called clean by the model: no praise, streak untouched.
	tb.makeQuiz = func(context.Context, string, bool) (quiz, bool, string, error) {
		return quiz{}, false, "✅ nice", nil
	}
	tb.runJob(ownerJob("oye ya está el respaldo de la Mac?"))
	if tb.sentWith("✅ nice") != 0 || tb.loadStreak().Streak != 2 {
		t.Fatalf("Spanish is never praised: sent=%q", tb.sent)
	}
	// Spanish quizzes off: not even judged.
	if err := tb.set.SetGateSpanishOff(true); err != nil {
		t.Fatal(err)
	}
	tb.runJob(ownerJob("oye ya está el respaldo de la Mac?"))
	if tb.sentWith("✅ nice") != 0 || tb.loadStreak().Streak != 2 {
		t.Fatalf("Spanish is never praised: sent=%q", tb.sent)
	}
	// Gate off: nothing at all.
	if _, err := tb.gateOff(); err != nil {
		t.Fatal(err)
	}
	called := false
	tb.makeQuiz = func(context.Context, string, bool) (quiz, bool, string, error) {
		called = true
		return quiz{}, false, "✅ nice", nil
	}
	tb.runJob(ownerJob("how is Tequila doing today?"))
	if called || tb.sentWith("✅ nice") != 0 || tb.loadStreak().Streak != 2 {
		t.Fatalf("gate off: no judging, no praise: sent=%q", tb.sent)
	}
}

// A quiz the checker threw away still breaks the streak; a failed call doesn't
// touch it; neither is praised.
func TestQuizPraiseFailures(t *testing.T) {
	tb, st := quizBot(t)
	st.praise = "✅ nice"
	tb.updateStreak(func(s *quizStreak) { s.Streak = 5 })
	st.err = errors.New("claude down")
	tb.runJob(ownerJob("how is Tequila doing today?"))
	if tb.loadStreak().Streak != 5 || tb.sentWith("✅ nice") != 0 || tb.nCalls() != 1 {
		t.Fatalf("a failed call: answer, no praise, streak kept: %+v sent=%q", tb.loadStreak(), tb.sent)
	}
	st.err = errQuizUnsure
	tb.runJob(ownerJob("how is Tequila doing today?"))
	if tb.loadStreak().Streak != 5 || tb.sentWith("✅ nice") != 0 {
		t.Fatalf("clean only on the retry: no praise, streak kept: %+v", tb.loadStreak())
	}
	st.err = errors.Join(errQuizRejected, errors.New("choose has two answers"))
	tb.runJob(ownerJob("how is Tequila doing today?"))
	if tb.loadStreak().Streak != 0 || tb.sentWith("✅ nice") != 0 || tb.sentWith("🧩") != 0 {
		t.Fatalf("a mistake whose quiz was thrown away breaks the streak: %+v sent=%q", tb.loadStreak(), tb.sent)
	}
}

// A verdict slower than the answer: the answer doesn't wait past praiseWait,
// and the late praise is dropped (it would read like a reply to the answer).
func TestQuizPraiseLateIsDropped(t *testing.T) {
	old := praiseWait
	praiseWait = 50 * time.Millisecond
	t.Cleanup(func() { praiseWait = old })
	tb, _ := quizBot(t)
	release := make(chan struct{})
	tb.makeQuiz = func(context.Context, string, bool) (quiz, bool, string, error) {
		<-release
		return quiz{}, false, `✅ "how is Tequila" — right.`, nil
	}
	t0 := time.Now()
	tb.process(context.Background(), ownerJob("how is Tequila doing today?"))
	if d := time.Since(t0); d > 2*time.Second || tb.sentWith("ok") != 1 {
		t.Fatalf("the answer must not wait for a slow verdict: %s sent=%q", d, tb.sent)
	}
	close(release)
	tb.quizWG.Wait()
	if tb.sentWith("how is Tequila") != 0 || tb.loadStreak().Streak != 1 {
		t.Fatalf("a late praise is dropped, the streak still counts: %+v sent=%q", tb.loadStreak(), tb.sent)
	}
}

func TestCleanPraise(t *testing.T) {
	msg := "Why didn't you send me the backup yesterday?"
	isGeneric := func(s string) bool {
		for _, g := range praiseGeneric {
			if s == g {
				return true
			}
		}
		return false
	}
	for _, c := range []struct{ raw, want string }{
		{`✅ "Why didn't you send…" — did before you. Perfect.`, `✅ "Why didn't you send…" — did before you. Perfect.`},
		{`“why didnt you send me” — did first.`, `✅ “why didnt you send me” — did first.`}, // no emoji: one added
		{"🎯 \"send me the backup\" — nice.\nAnd more text", `🎯 "send me the backup" — nice.`},
		{`🎯 "the backup yesterday?" — good.`, `🎯 "the backup yesterday?" — good.`},
	} {
		if got := cleanPraise(c.raw, msg, nil); got != c.want {
			t.Errorf("cleanPraise(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
	for _, raw := range []string{
		"",
		`✅ "Why did you send" — did before you.`, // not his words
		`✅ "remind me" — right verb.`,
		`✅ "end" — good.`, // part of a word ("send") is not his word
		"✅ " + strings.Repeat("very ", 40) + "good",
	} {
		if got := cleanPraise(raw, msg, nil); !isGeneric(got) {
			t.Errorf("cleanPraise(%q) = %q, want a generic line", raw, got)
		}
	}
	// A generic line is never one of the recent ones.
	recent := praiseGeneric[:len(praiseGeneric)-1]
	for i := 0; i < 20; i++ {
		if got := cleanPraise("", msg, recent); got != praiseGeneric[len(praiseGeneric)-1] {
			t.Fatalf("generic must avoid recent lines: %q", got)
		}
	}
	if got := cleanPraise(`✅ "send me the backup" — nice.`, msg, []string{`✅ "send me the backup" — nice.`}); !isGeneric(got) {
		t.Fatalf("a repeat of a recent line must be swapped: %q", got)
	}
	if !strings.Contains(quizPrompt("hi there man", false, []string{"✅ a", "🎯 b"}), "- ✅ a\n- 🎯 b") {
		t.Fatal("the maker is told the recent lines")
	}
	if strings.Contains(quizPrompt("hi there man", false, nil), "praise") {
		t.Fatal("no recent lines, no note")
	}
}
