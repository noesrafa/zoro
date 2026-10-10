package bot

// English gate, praise (/gate on). rafiña's ask, 9-oct-2026: "In the new coach
// I want that he sends me congratulations messages each time he doesn't send me
// a quiz".
//
//   - No extra call: the quiz maker's clean verdict carries the line,
//     {"clean": true, "praise": "✅ \"Why didn't you send…\" — did before you."}.
//   - It goes out BEFORE Zoro's answer: the answer waits for the verdict at most
//     praiseWait; a verdict later than the answer gives no praise (one after the
//     answer would read like a reply to it). Only the praise is waited for — a
//     quiz still comes whenever it's ready.
//   - cleanPraise keeps the line honest: one line, an emoji, and every quoted
//     piece must really be in his message (a misquote would praise words he
//     never wrote); otherwise, or when it repeats a recent line, a short generic
//     one picked from praiseGeneric.
//   - The streak (<state>/quiz-streak.json) counts clean judged messages in a
//     row; a mistake (a quiz, or one the checker threw away) sets it to 0. From
//     3 on it rides on the praise ("🔥 4 clean in a row"); /gate shows it. The
//     last praiseRecent lines are kept too: the maker is told not to reuse them.
//   - Only what the quiz judges can be praised: typed English, ≥ 3 words, no "!",
//     no command, no code, not a cron, not a voice note. Spanish never is.
//
// The first praise (faa6504) rode on the Gemma bounce gate, removed in 3ad411b.

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

// praiseWait is the longest the answer waits for the verdict (a var for tests).
// A clean verdict takes ~4-5 s (one short call); a mistake's takes 10-20 s
// (the quiz and its checker) and its answer should not wait for it, so this is
// short: only an answer faster than it is held, and only up to it.
var praiseWait = 8 * time.Second

const (
	quizStreakName = "quiz-streak.json"

	praiseMaxRunes  = 140
	praiseRecent    = 6 // lines remembered so the next ones differ
	praiseStreakMin = 3 // the streak shows from this many clean in a row
)

// praiseGeneric: when the maker gave no usable line. One emoji, simple English.
var praiseGeneric = []string{
	"✅ Clean English, nothing to fix.",
	"👌 Nothing to fix — clear and correct.",
	"💯 All correct in that one.",
	"🎯 No mistakes this time. Nice.",
	"✨ Perfect English, well done.",
	"👏 Clean message, good job.",
}

// quizStreak is <state>/quiz-streak.json.
type quizStreak struct {
	Streak int      `json:"streak"`
	Best   int      `json:"best"`
	Recent []string `json:"recent,omitempty"` // the last praise lines sent, newest last
}

func (b *Bot) quizStreakFile() string { return filepath.Join(b.cfg.StateDir, quizStreakName) }

func (b *Bot) loadStreak() quizStreak {
	var s quizStreak
	if raw, err := os.ReadFile(b.quizStreakFile()); err == nil {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

// updateStreak applies f to the saved streak under streakMu and saves it.
func (b *Bot) updateStreak(f func(*quizStreak)) quizStreak {
	b.streakMu.Lock()
	defer b.streakMu.Unlock()
	s := b.loadStreak()
	f(&s)
	if s.Streak > s.Best {
		s.Best = s.Streak
	}
	raw, _ := json.MarshalIndent(s, "", "  ")
	tmp := b.quizStreakFile() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err == nil {
		err = os.Rename(tmp, b.quizStreakFile())
		if err != nil {
			b.log.Warn("quiz: could not save the streak", "err", err)
		}
	} else {
		b.log.Warn("quiz: could not save the streak", "err", err)
	}
	return s
}

func (b *Bot) streakBroken() {
	b.updateStreak(func(s *quizStreak) { s.Streak = 0 })
}

// praiseSlot orders the praise before the answer of the same turn.
type praiseSlot struct {
	done     chan struct{} // closed when the verdict was handled
	mu       sync.Mutex
	answered bool // the answer went out: too late to praise
}

func newPraiseSlot() *praiseSlot { return &praiseSlot{done: make(chan struct{})} }

// awaitPraise is called right before the answer goes out: it gives the verdict
// up to praiseWait to land (and its praise to be sent), then closes the slot.
func (b *Bot) awaitPraise(ctx context.Context, s *praiseSlot) {
	if s == nil {
		return
	}
	t := time.NewTimer(praiseWait)
	defer t.Stop()
	select {
	case <-s.done:
	case <-t.C:
		b.log.Info("quiz: verdict not in yet, answering without waiting for the praise")
	case <-ctx.Done():
	}
	s.close()
}

func (s *praiseSlot) close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.answered = true
	s.mu.Unlock()
}

// praiseClean counts a clean message and sends its praise, unless the answer
// already went out or the gate went off meanwhile.
func (b *Bot) praiseClean(ctx context.Context, chatID int64, text, raw string, slot *praiseSlot) {
	if !b.quizOn() {
		return
	}
	recent := b.loadStreak().Recent
	line := cleanPraise(raw, text, recent)
	s := b.updateStreak(func(s *quizStreak) {
		s.Streak++
		s.Recent = append(s.Recent, line)
		if len(s.Recent) > praiseRecent {
			s.Recent = s.Recent[len(s.Recent)-praiseRecent:]
		}
	})
	msg := line
	if s.Streak >= praiseStreakMin {
		msg += fmt.Sprintf("\n🔥 %d clean in a row", s.Streak)
	}
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if slot.answered {
		b.log.Info("quiz: clean, but the answer went first — no praise", "streak", s.Streak)
		return
	}
	b.log.Info("quiz: clean — praised", "streak", s.Streak, "praise", line)
	b.send(ctx, chatID, msg)
}

var praiseQuoteRe = regexp.MustCompile(`"([^"]+)"|“([^”]+)”`)

// cleanPraise turns the maker's line into what he gets: its first line, at most
// praiseMaxRunes, with an emoji, every quote really his, and not a repeat of a
// recent line — or else a generic line that isn't one of the recent ones.
func cleanPraise(raw, text string, recent []string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(raw), "\n")
	line = strings.TrimSpace(line)
	ok := line != "" && runes(line) <= praiseMaxRunes && quotesAreHis(line, text)
	for _, r := range recent {
		if normQuiz(r) == normQuiz(line) {
			ok = false
		}
	}
	if ok {
		if !hasEmoji(line) {
			line = "✅ " + line
		}
		return line
	}
	used := map[string]bool{}
	for _, r := range recent {
		used[r] = true
	}
	var fresh []string
	for _, g := range praiseGeneric {
		if !used[g] {
			fresh = append(fresh, g)
		}
	}
	if len(fresh) == 0 {
		fresh = praiseGeneric
	}
	return fresh[rand.IntN(len(fresh))]
}

// quotesAreHis: every "quoted" piece of the line (an ending … dropped) is a run
// of whole words of his message — case, punctuation and apostrophes aside.
func quotesAreHis(line, text string) bool {
	msg := " " + praiseNorm(text) + " "
	for _, m := range praiseQuoteRe.FindAllStringSubmatch(line, -1) {
		n := praiseNorm(m[1] + m[2])
		if n == "" || !strings.Contains(msg, " "+n+" ") {
			return false
		}
	}
	return true
}

// praiseNorm: lowercase words of letters and digits ("didn't" = "didnt").
func praiseNorm(s string) string {
	s = strings.NewReplacer("'", "", "’", "", "‘", "", "`", "").Replace(strings.ToLower(s))
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r))
	}), " ")
}

// hasEmoji: a pictograph or symbol from the emoji blocks (✅ ✨ 🎯 🔥 …).
func hasEmoji(s string) bool {
	for _, r := range s {
		if r >= 0x1F000 || (r >= 0x2100 && unicode.Is(unicode.So, r)) {
			return true
		}
	}
	return false
}

// streakText is the /gate line about the streak.
func (b *Bot) streakText() string {
	s := b.loadStreak()
	return fmt.Sprintf("🔥 Clean streak: %d in a row (best %d)", s.Streak, s.Best)
}
