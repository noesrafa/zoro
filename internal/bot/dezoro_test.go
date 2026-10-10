package bot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTakeDeZoro_InOrderOnceAndSkipsTmp(t *testing.T) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "2.txt"), []byte("second"), 0o644)
	os.WriteFile(filepath.Join(d, "1.txt"), []byte(" first \n"), 0o644)
	os.WriteFile(filepath.Join(d, "3.tmp"), []byte("half-written"), 0o644)
	os.WriteFile(filepath.Join(d, "4.txt"), []byte("   "), 0o644)
	got := takeDeZoro(d)
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("got %q", got)
	}
	if again := takeDeZoro(d); len(again) != 0 {
		t.Errorf("delivered twice: %q", again)
	}
	if _, err := os.Stat(filepath.Join(d, "3.tmp")); err != nil {
		t.Error("a .tmp being written must be left alone")
	}
	if got := takeDeZoro(filepath.Join(d, "missing")); len(got) != 0 {
		t.Error("missing dir = nothing")
	}
}
