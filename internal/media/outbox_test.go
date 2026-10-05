package media

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// put writes name into dir with modtime mod.
func put(t *testing.T, dir, name, body string, mod time.Time) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func names(fs []OutboxFile) []string {
	var out []string
	for _, f := range fs {
		out = append(out, filepath.Base(f.Path))
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestLedgerSeedsWithOldFiles(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	old := time.Now().Add(-time.Hour)
	put(t, dir, "viejo.png", "a", old)
	put(t, dir, "viejo.pdf", "b", old)

	l, seeded, err := OpenLedger(filepath.Join(state, "ledger.json"), dir)
	if err != nil || !seeded {
		t.Fatalf("seeded=%v err=%v", seeded, err)
	}
	if ready, wait := l.Pending(dir, time.Now(), 5*time.Second); len(ready) != 0 || wait != 0 {
		t.Fatalf("old files must not be re-sent: %v wait=%v", names(ready), wait)
	}
	// Reopening reads the saved ledger instead of seeding again.
	put(t, dir, "nuevo.gif", "c", old)
	l2, seeded, err := OpenLedger(filepath.Join(state, "ledger.json"), dir)
	if err != nil || seeded {
		t.Fatalf("reopen seeded=%v err=%v", seeded, err)
	}
	if ready, _ := l2.Pending(dir, time.Now(), 5*time.Second); !eq(names(ready), []string{"nuevo.gif"}) {
		t.Fatalf("pending = %v, want the file added after the seed", names(ready))
	}
}

func TestLedgerDeliversNewAndModifiedUntilMarked(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	path := filepath.Join(state, "ledger.json")
	l, _, _ := OpenLedger(path, dir) // empty outbox: nothing seeded
	t0 := time.Now().Add(-time.Minute)
	put(t, dir, "b.png", "1", t0)
	put(t, dir, "a.pdf", "1", t0)
	put(t, dir, ".tmp-rsync", "1", t0) // dotfiles are temp files, never sent

	ready, _ := l.Pending(dir, time.Now(), 5*time.Second)
	if !eq(names(ready), []string{"a.pdf", "b.png"}) {
		t.Fatalf("pending = %v", names(ready))
	}
	// Only a.pdf went out (b.png's send failed): b.png stays pending.
	if err := l.Mark(ready[0]); err != nil {
		t.Fatal(err)
	}
	if ready, _ := l.Pending(dir, time.Now(), 5*time.Second); !eq(names(ready), []string{"b.png"}) {
		t.Fatalf("unmarked file must stay pending, got %v", names(ready))
	}
	// Persisted across restarts.
	l2, _, _ := OpenLedger(path, dir)
	if ready, _ := l2.Pending(dir, time.Now(), 5*time.Second); !eq(names(ready), []string{"b.png"}) {
		t.Fatalf("after reopen pending = %v", names(ready))
	}
	// Rewriting a delivered file makes it new again (newer modtime or other size).
	put(t, dir, "a.pdf", "1", t0.Add(time.Second))
	if ready, _ := l2.Pending(dir, time.Now(), 5*time.Second); !eq(names(ready), []string{"a.pdf", "b.png"}) {
		t.Fatalf("modified file must be pending again, got %v", names(ready))
	}
	l2.Mark(ready[0])
	put(t, dir, "a.pdf", "1234", t0.Add(time.Second))
	if ready, _ := l2.Pending(dir, time.Now(), 5*time.Second); !eq(names(ready), []string{"a.pdf", "b.png"}) {
		t.Fatalf("same modtime but new size must be pending, got %v", names(ready))
	}
}

func TestLedgerWaitsForFilesBeingWritten(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	l, _, _ := OpenLedger(filepath.Join(state, "ledger.json"), dir)
	now := time.Now()
	put(t, dir, "listo.gif", "x", now.Add(-10*time.Second))
	put(t, dir, "copiando.gif", "x", now.Add(-2*time.Second))

	ready, wait := l.Pending(dir, now, 5*time.Second)
	if !eq(names(ready), []string{"listo.gif"}) {
		t.Fatalf("ready = %v, the file touched 2 s ago must wait", names(ready))
	}
	if wait != 3*time.Second {
		t.Fatalf("wait = %v, want 3s", wait)
	}
	if ready, wait := l.Pending(dir, now.Add(5*time.Second), 5*time.Second); len(ready) != 2 || wait != 0 {
		t.Fatalf("once settled both go: %v wait=%v", names(ready), wait)
	}
}

func TestLedgerForgetsDeletedFiles(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	old := time.Now().Add(-time.Hour)
	put(t, dir, "logo.png", "a", old)
	l, _, _ := OpenLedger(filepath.Join(state, "ledger.json"), dir)
	os.Remove(filepath.Join(dir, "logo.png"))
	l.Pending(dir, time.Now(), 5*time.Second)
	// The same name (even the same stamp) coming back later is a new deliverable.
	put(t, dir, "logo.png", "a", old)
	if ready, _ := l.Pending(dir, time.Now(), 5*time.Second); !eq(names(ready), []string{"logo.png"}) {
		t.Fatalf("recreated file must be pending, got %v", names(ready))
	}
}

func TestLedgerCorruptReseeds(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	path := filepath.Join(state, "ledger.json")
	os.WriteFile(path, []byte("{nope"), 0o644)
	put(t, dir, "viejo.png", "a", time.Now().Add(-time.Hour))
	l, seeded, err := OpenLedger(path, dir)
	if err != nil || !seeded {
		t.Fatalf("seeded=%v err=%v", seeded, err)
	}
	if ready, _ := l.Pending(dir, time.Now(), 5*time.Second); len(ready) != 0 {
		t.Fatalf("a broken ledger must not re-send the whole outbox: %v", names(ready))
	}
}
