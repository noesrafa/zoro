package config

import "testing"

// The quiz site is rafiña's: only Zoro gets it by default. Sky and Tequila run
// the same binary (nightly copy) and must keep no quiz site, so /gate on does
// nothing there.
func TestQuizDirOnlyForZoro(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "x")
	t.Setenv("TELEGRAM_OWNER_ID", "1")
	t.Setenv("ZORO_QUIZ_DIR", "")
	for name, want := range map[string]string{"zoro": "/home/rafael/dominioartificial/coach", "sky": "", "tequila": ""} {
		t.Setenv("ZORO_AGENT_NAME", name)
		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if c.QuizDir != want {
			t.Errorf("%s: QuizDir %q, want %q", name, c.QuizDir, want)
		}
	}
}
