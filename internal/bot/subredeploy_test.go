package bot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallBinary_ReplacesKeepsBackupAndDetectsSame(t *testing.T) {
	d := t.TempDir()
	src, dst := filepath.Join(d, "upstream"), filepath.Join(d, "zoro")
	os.WriteFile(src, []byte("new"), 0o755)
	os.WriteFile(dst, []byte("old"), 0o755)
	if _, restart := installBinary(src, dst); !restart {
		t.Fatal("a different upstream must install and restart")
	}
	if b, _ := os.ReadFile(dst); string(b) != "new" {
		t.Errorf("dst = %q, want new", b)
	}
	if b, _ := os.ReadFile(dst + ".bak"); string(b) != "old" {
		t.Errorf("backup = %q, want old", b)
	}
	if fi, _ := os.Stat(dst); fi.Mode().Perm()&0o100 == 0 {
		t.Error("installed binary must stay executable")
	}
	if msg, restart := installBinary(src, dst); !restart || msg == "" {
		t.Error("same binary: still restart, with a message")
	}
}

func TestInstallBinary_UnreadableSourceChangesNothing(t *testing.T) {
	d := t.TempDir()
	dst := filepath.Join(d, "zoro")
	os.WriteFile(dst, []byte("old"), 0o755)
	if _, restart := installBinary(filepath.Join(d, "missing"), dst); restart {
		t.Fatal("no source must not restart")
	}
	if b, _ := os.ReadFile(dst); string(b) != "old" {
		t.Errorf("dst touched: %q", b)
	}
}
