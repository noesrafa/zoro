package bot

// Quiz format and validator (quiz.go makes them, coach.dominioartificial.com
// renders them). One file per quiz: <QuizDir>/q/<id>.json. The format is
// documented for humans in <QuizDir>/FORMATO.md; normQuiz must stay in sync with
// norm() in <QuizDir>/index.html, or the page and the validator disagree on what
// counts as the same sentence.

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"unicode"
)

type quiz struct {
	ID        string       `json:"id"`
	Created   string       `json:"created"` // RFC3339, UTC
	Source    string       `json:"source"`  // his message, as the quiz saw it
	Kind      string       `json:"kind"`    // quizGrammar | quizSpanish
	Rule      quizRule     `json:"rule"`
	Original  string       `json:"original"`
	Corrected string       `json:"corrected"`
	Puzzles   []quizPuzzle `json:"puzzles"`
}

type quizRule struct {
	Title string `json:"title"`
	Text  string `json:"text"`
	// A tiny example of the mistake and its fix (spanish: a Spanish phrase and
	// its English). Optional: both or neither; the page shows them as ❌/✅.
	Bad  string `json:"bad,omitempty"`
	Good string `json:"good,omitempty"`
}

// quizPuzzle is one of the three, in this order: reorder, choose, write.
type quizPuzzle struct {
	Type    string   `json:"type"`
	Words   []string `json:"words,omitempty"`   // reorder: the tiles, shuffled
	Text    string   `json:"text,omitempty"`    // choose: the sentence with one ___
	Options []string `json:"options,omitempty"` // choose: 2-4 options
	Prompt  string   `json:"prompt,omitempty"`  // write: the sentence to fix (or say in English)
	Answer  string   `json:"answer"`
	Accept  []string `json:"accept,omitempty"` // reorder/write: other right answers
	Hint    string   `json:"hint,omitempty"`   // shown after 2 misses
	Es      string   `json:"es,omitempty"`     // what the answer means, in Mexican Spanish (the puzzle's goal)
	Tip     string   `json:"tip,omitempty"`    // a short nudge shown after the first miss
}

const (
	quizGrammar = "grammar"
	quizSpanish = "spanish"

	quizMinTiles = 3
	quizMaxTiles = 14

	// A rule he reads at a glance on the phone (older quizzes had longer ones:
	// the page shows those too, the validator only judges new ones).
	quizMaxTitle   = 40
	quizMaxRule    = 120
	quizMaxExample = 60
	quizMaxEs      = 140
	quizMaxTipWord = 8
	quizMaxTip     = 60
)

var quizIDRe = regexp.MustCompile(`^[a-z0-9]{10,40}$`)

// newQuizID is 12 random [a-z0-9] characters: the link is the only key to a quiz.
func newQuizID() string {
	const abc = "abcdefghijklmnopqrstuvwxyz0123456789"
	var sb strings.Builder
	for i := 0; i < 12; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(abc))))
		if err != nil {
			panic(err)
		}
		sb.WriteByte(abc[n.Int64()])
	}
	return sb.String()
}

// contractions the comparison expands, so "doesn't" and "does not" both pass.
var quizContractions = map[string]string{
	"won't": "will not", "can't": "can not", "cannot": "can not", "shan't": "shall not",
	"i'm": "i am", "let's": "let us",
	"it's": "it is", "that's": "that is", "what's": "what is", "how's": "how is", "there's": "there is",
	"here's": "here is", "who's": "who is", "where's": "where is", "when's": "when is", "why's": "why is",
	"he's": "he is", "she's": "she is",
}

// normQuiz is how two answers are compared: lowercase, typographic quotes as
// straight ones, no punctuation, single spaces, common contractions expanded.
func normQuiz(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer("’", "'", "‘", "'", "´", "'", "`", "'").Replace(s)
	words := strings.FieldsFunc(s, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'')
	})
	var out []string
	for _, w := range words {
		w = strings.Trim(w, "'")
		switch {
		case w == "":
			continue
		case quizContractions[w] != "":
			w = quizContractions[w]
		case strings.HasSuffix(w, "n't"):
			w = strings.TrimSuffix(w, "n't") + " not"
		case strings.HasSuffix(w, "'re"):
			w = strings.TrimSuffix(w, "'re") + " are"
		case strings.HasSuffix(w, "'ve"):
			w = strings.TrimSuffix(w, "'ve") + " have"
		case strings.HasSuffix(w, "'ll"):
			w = strings.TrimSuffix(w, "'ll") + " will"
		case strings.HasSuffix(w, "'d"):
			w = strings.TrimSuffix(w, "'d") + " would"
		}
		out = append(out, w)
	}
	return strings.Join(out, " ")
}

// quizTiles cuts a sentence into reorder tiles: one per word, punctuation at
// the edges dropped (the "?" would give the last tile away), the first word in
// lowercase when it's a common word (a capital would give the first tile away).
func quizTiles(answer string) []string {
	var tiles []string
	for _, w := range strings.Fields(answer) {
		w = strings.TrimFunc(w, func(r rune) bool {
			return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' || r == '’')
		})
		if w != "" {
			tiles = append(tiles, w)
		}
	}
	if len(tiles) > 0 {
		if l := strings.ToLower(tiles[0]); quizCommonStart[strings.ReplaceAll(l, "’", "'")] {
			tiles[0] = l
		}
	}
	return tiles
}

// quizCommonStart: sentence openers that are not names, so their capital can go.
var quizCommonStart = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`what how why where when who which whose do does did is are was were am
		can could should would will shall may might must have has had the a an my our your his her their its
		we you it this that these those there they he she please let's let if so and but or ok yes no not
		don't doesn't didn't isn't aren't wasn't weren't can't won't shouldn't wouldn't couldn't haven't hasn't
		it's that's what's how's there's i'm we're you're they're he's she's today tomorrow yesterday now then
		also maybe right just still every all some any one two three first last next send show tell give
		make check add run try use go keep remember explain help`) {
		m[w] = true
	}
	return m
}()

// shuffleTiles returns the tiles in a random order different from the answer's
// (and from every accepted order), or nil when every order reads the same.
func shuffleTiles(tiles []string, right []string) []string {
	bad := map[string]bool{}
	for _, r := range right {
		bad[normQuiz(r)] = true
	}
	out := append([]string(nil), tiles...)
	for try := 0; try < 50; try++ {
		for i := len(out) - 1; i > 0; i-- {
			n, _ := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
			j := int(n.Int64())
			out[i], out[j] = out[j], out[i]
		}
		if !bad[normQuiz(strings.Join(out, " "))] {
			return out
		}
	}
	return nil
}

// sameWords: a and b are the same multiset of (normalized) words.
func sameWords(a, b string) bool {
	count := map[string]int{}
	for _, w := range strings.Fields(normQuiz(a)) {
		count[w]++
	}
	for _, w := range strings.Fields(normQuiz(b)) {
		count[w]--
	}
	for _, n := range count {
		if n != 0 {
			return false
		}
	}
	return true
}

// validateQuiz rejects a quiz the page can't run or that has no single right
// answer it can check. A rejected quiz is never shown: no quiz, no wall.
func validateQuiz(q quiz) error {
	var errs []string
	bad := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }
	empty := func(s string) bool { return strings.TrimSpace(s) == "" }

	if !quizIDRe.MatchString(q.ID) {
		bad("id must be 10-40 chars of [a-z0-9]")
	}
	if empty(q.Created) || empty(q.Source) {
		bad("created and source are required")
	}
	if q.Kind != quizGrammar && q.Kind != quizSpanish {
		bad("kind must be %q or %q", quizGrammar, quizSpanish)
	}
	if empty(q.Rule.Title) || empty(q.Rule.Text) {
		bad("rule.title and rule.text are required")
	}
	if n, m := runes(q.Rule.Title), runes(q.Rule.Text); n > quizMaxTitle || m > quizMaxRule {
		bad("the rule must be short: title ≤ %d chars (got %d), text ≤ %d chars (got %d)", quizMaxTitle, n, quizMaxRule, m)
	}
	if empty(q.Rule.Bad) != empty(q.Rule.Good) {
		bad("rule.bad and rule.good go together (both or neither)")
	} else if !empty(q.Rule.Bad) {
		if runes(q.Rule.Bad) > quizMaxExample || runes(q.Rule.Good) > quizMaxExample {
			bad("rule.bad and rule.good are tiny examples (≤ %d chars each)", quizMaxExample)
		}
		if normQuiz(q.Rule.Bad) == normQuiz(q.Rule.Good) {
			bad("rule.good must fix rule.bad")
		}
	}
	if empty(q.Original) || empty(q.Corrected) {
		bad("original and corrected are required")
	} else if normQuiz(q.Original) == normQuiz(q.Corrected) {
		bad("corrected is the same as original")
	}
	types := []string{"reorder", "choose", "write"}
	if len(q.Puzzles) != len(types) {
		bad("exactly 3 puzzles: reorder, choose, write")
	}
	for i, p := range q.Puzzles {
		if i >= len(types) {
			break
		}
		if p.Type != types[i] {
			bad("puzzle %d must be %q, got %q", i+1, types[i], p.Type)
			continue
		}
		if empty(p.Answer) {
			bad("%s: answer is empty", p.Type)
			continue
		}
		validateExtras(p, bad)
		switch p.Type {
		case "reorder":
			validateReorder(p, bad)
		case "choose":
			validateChoose(p, q, bad)
		case "write":
			validateWrite(p, q, bad)
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func runes(s string) int { return len([]rune(strings.TrimSpace(s))) }

// validateExtras checks the optional es and tip: a quiz without them is fine
// (the page has fallbacks), but when there they must be usable.
func validateExtras(p quizPuzzle, bad func(string, ...any)) {
	if es := strings.TrimSpace(p.Es); es != "" {
		if runes(es) > quizMaxEs {
			bad("%s: es is too long (≤ %d chars)", p.Type, quizMaxEs)
		}
		if normQuiz(es) == normQuiz(p.Answer) {
			bad("%s: es must be the Spanish meaning, not the English answer", p.Type)
		}
	}
	if tip := strings.TrimSpace(p.Tip); tip != "" {
		if len(strings.Fields(tip)) > quizMaxTipWord || runes(tip) > quizMaxTip {
			bad("%s: tip is at most %d words", p.Type, quizMaxTipWord)
		}
		if p.Type != "choose" && normQuiz(tip) == normQuiz(p.Answer) {
			bad("%s: the tip can't be the whole answer", p.Type)
		}
	}
}

func validateReorder(p quizPuzzle, bad func(string, ...any)) {
	if len(p.Words) < quizMinTiles || len(p.Words) > quizMaxTiles {
		bad("reorder: %d to %d words, got %d", quizMinTiles, quizMaxTiles, len(p.Words))
	}
	for _, w := range p.Words {
		if strings.TrimSpace(w) == "" || strings.ContainsAny(strings.TrimSpace(w), " \t") {
			bad("reorder: every tile is one non-empty word")
			return
		}
	}
	tiles := strings.Join(p.Words, " ")
	if !sameWords(tiles, p.Answer) {
		bad("reorder: words must be exactly the words of the answer")
		return
	}
	right := map[string]bool{normQuiz(p.Answer): true}
	for _, a := range p.Accept {
		if !sameWords(a, p.Answer) {
			bad("reorder: accept %q uses other words than the answer", a)
		}
		right[normQuiz(a)] = true
	}
	if right[normQuiz(tiles)] {
		bad("reorder: the tiles are already in a right order")
	}
}

func validateChoose(p quizPuzzle, q quiz, bad func(string, ...any)) {
	if strings.Count(p.Text, "___") != 1 {
		bad("choose: text needs exactly one ___")
	}
	if len(p.Options) < 2 || len(p.Options) > 4 {
		bad("choose: 2 to 4 options, got %d", len(p.Options))
	}
	seen := map[string]bool{}
	in := false
	for _, o := range p.Options {
		k := strings.ToLower(strings.TrimSpace(o))
		if k == "" {
			bad("choose: empty option")
			continue
		}
		if seen[k] {
			bad("choose: duplicate option %q", o)
		}
		seen[k] = true
		if k == strings.ToLower(strings.TrimSpace(p.Answer)) {
			in = true
		}
	}
	if !in {
		bad("choose: answer %q is not one of the options", p.Answer)
	}
	filled := normQuiz(strings.Replace(p.Text, "___", p.Answer, 1))
	if filled == normQuiz(q.Original) || filled == normQuiz(q.Corrected) {
		bad("choose: must be another sentence, not his")
	}
}

func validateWrite(p quizPuzzle, q quiz, bad func(string, ...any)) {
	if strings.TrimSpace(p.Prompt) == "" {
		bad("write: prompt is empty")
		return
	}
	if normQuiz(p.Prompt) == normQuiz(q.Original) || normQuiz(p.Answer) == normQuiz(q.Corrected) {
		bad("write: must be another sentence, not his")
	}
	if q.Kind == quizGrammar && normQuiz(p.Prompt) == normQuiz(p.Answer) {
		bad("write: the prompt is already right")
	}
	for _, a := range p.Accept {
		if strings.TrimSpace(a) == "" {
			bad("write: empty accept")
		} else if q.Kind == quizGrammar && normQuiz(a) == normQuiz(p.Prompt) {
			bad("write: accept %q is the wrong sentence itself", a)
		}
	}
}

// quizVerdict is what the model returns: clean, or a quiz without the fields
// the engine fills (id, created, source, the reorder tiles).
type quizVerdict struct {
	Clean  bool   `json:"clean"`
	Praise string `json:"praise"` // clean: one line congratulating him (quizpraise.go)
	quiz
}

// parseQuizVerdict reads the model's FIRST JSON object, tolerating ```json
// fences and chatter around it. First, not last: when the model second-guesses
// itself ({"clean": true} … "Wait, actually" {…quiz…}) the first call stands —
// a needless wall costs more than a missed quiz.
func parseQuizVerdict(content string) (quizVerdict, error) {
	var v quizVerdict
	i := strings.Index(content, "{")
	if i < 0 {
		return v, fmt.Errorf("no JSON in %.120q", content)
	}
	err := json.NewDecoder(strings.NewReader(content[i:])).Decode(&v)
	return v, err
}

// fillReorder makes the tiles from the answer (the model only writes the
// sentence: tiles it cut itself could drift from it).
func fillReorder(q *quiz) {
	for i := range q.Puzzles {
		p := &q.Puzzles[i]
		if p.Type != "reorder" {
			continue
		}
		// An accepted order with other words can't be built from the tiles anyway.
		var acc []string
		for _, a := range p.Accept {
			if sameWords(a, p.Answer) && normQuiz(a) != normQuiz(p.Answer) {
				acc = append(acc, a)
			}
		}
		p.Accept = acc
		p.Words = shuffleTiles(quizTiles(p.Answer), append([]string{p.Answer}, p.Accept...))
	}
}

// trimExtras drops the optional fields the validator would reject (a long tip,
// an English "es", half an example): they only add clarity, so they never cost
// a retry — the page falls back without them.
func trimExtras(q *quiz) {
	r := &q.Rule
	r.Bad, r.Good = strings.TrimSpace(r.Bad), strings.TrimSpace(r.Good)
	if r.Bad == "" || r.Good == "" || runes(r.Bad) > quizMaxExample || runes(r.Good) > quizMaxExample || normQuiz(r.Bad) == normQuiz(r.Good) {
		r.Bad, r.Good = "", ""
	}
	for i := range q.Puzzles {
		p := &q.Puzzles[i]
		p.Es, p.Tip = strings.TrimSpace(p.Es), strings.TrimSpace(p.Tip)
		if runes(p.Es) > quizMaxEs || (p.Es != "" && normQuiz(p.Es) == normQuiz(p.Answer)) {
			p.Es = ""
		}
		if len(strings.Fields(p.Tip)) > quizMaxTipWord || runes(p.Tip) > quizMaxTip || (p.Type != "choose" && p.Tip != "" && normQuiz(p.Tip) == normQuiz(p.Answer)) {
			p.Tip = ""
		}
	}
}
