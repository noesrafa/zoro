package bot

import (
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"testing"
)

// The page and the validator must agree on what counts as the same sentence:
// norm() in the quiz page (index.html) vs normQuiz here. Needs node, so it
// only runs when asked:
//
//	ZORO_QUIZ_PAGE=/home/rafael/dominioartificial/coach/index.html go test ./internal/bot -run TestQuizPageNorm
func TestQuizPageNorm(t *testing.T) {
	page := os.Getenv("ZORO_QUIZ_PAGE")
	if page == "" {
		t.Skip("set ZORO_QUIZ_PAGE=<index.html> to compare the page's norm() with normQuiz")
	}
	raw, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	js := regexp.MustCompile(`(?s)const CONTRACTIONS = .*?\nfunction norm\(s\) \{.*?\n\}\n`).Find(raw)
	if js == nil {
		t.Fatal("norm() not found in the page")
	}
	cases := []string{
		"Tequila doesn’t answer me.", "  What's  going on?! ", "We won't use it", "“Explain to me”", "I can't, I'm busy",
		"How’s the Behance going?", "Ángel’s app — v4.2 isn't live", "they're / we've / you'll / I'd", "Don't ask Jev, ask the AI instead.",
		"'quoted' words", "R2 isn't 100% ready", "¿Qué tenemos en R2?", "Let's go", "It’s still working",
	}
	in, _ := json.Marshal(cases)
	script := string(js) + "\nconsole.log(JSON.stringify(" + string(in) + ".map(norm)))"
	out, err := exec.Command("node", "-e", script).Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	var got []string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("node output %q: %v", out, err)
	}
	for i, c := range cases {
		if want := normQuiz(c); got[i] != want {
			t.Errorf("%q: page %q, engine %q", c, got[i], want)
		}
	}
}
