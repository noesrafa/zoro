package bot

import (
	"testing"

	"zoro/internal/tg"
)

func TestBuildPrompt(t *testing.T) {
	cases := []struct {
		name        string
		texts       []string
		transcripts []string
		files       []string
		want        string
	}{
		{
			name:  "multiple texts joined",
			texts: []string{"hola", "mundo"},
			want:  "hola\n\nmundo",
		},
		{
			name:  "files only lists all of them",
			files: []string{"/a.png", "/b.png", "/c.png"},
			want:  "[The user attached 3 file(s). Use the Read tool to view them: /a.png, /b.png, /c.png]",
		},
		{
			name:        "text + transcript + file in order",
			texts:       []string{"mira esto"},
			transcripts: []string{"hola que tal"},
			files:       []string{"/x.jpg"},
			want:        "mira esto\n\n[Voice note transcript]: hola que tal\n\n[The user attached 1 file(s). Use the Read tool to view them: /x.jpg]",
		},
		{
			name: "empty yields empty",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildPrompt(tc.texts, tc.transcripts, tc.files); got != tc.want {
				t.Errorf("buildPrompt()\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

func TestMsgText(t *testing.T) {
	cases := []struct {
		name string
		msg  *tg.Message
		want string
	}{
		{"text wins", &tg.Message{Text: "  hi  ", Caption: "cap"}, "hi"},
		{"caption fallback", &tg.Message{Caption: "  cap  "}, "cap"},
		{"both empty", &tg.Message{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := msgText(tc.msg); got != tc.want {
				t.Errorf("msgText() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDrainJobs(t *testing.T) {
	b := &Bot{jobs: make(chan job, 16)}
	for i := 0; i < 3; i++ {
		b.jobs <- job{chatID: int64(i)}
	}
	if n := b.drainJobs(); n != 3 {
		t.Fatalf("drainJobs() = %d, want 3", n)
	}
	if n := b.drainJobs(); n != 0 {
		t.Fatalf("drainJobs() on empty = %d, want 0", n)
	}
}
