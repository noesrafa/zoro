package media

// Outbox delivery ledger. The engine used to snapshot the outbox when a turn
// started and send only what changed during it, so a file a detached job left
// BETWEEN turns fell into the next snapshot and was never delivered. Now each
// bot keeps a persistent record of what it already delivered (name → modtime +
// size) and sends whatever is not in it, whoever wrote it and whenever.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// OutboxFile is one outbox file as seen when it was picked for delivery. Mark
// records exactly this version, so a file rewritten during its upload goes out
// again.
type OutboxFile struct {
	Path string
	Mod  time.Time
	Size int64
}

type stamp struct {
	Mod  int64 `json:"mod"` // UnixNano
	Size int64 `json:"size"`
}

// Ledger is the persistent set of outbox files already delivered.
type Ledger struct {
	path string
	mu   sync.Mutex
	seen map[string]stamp
}

// OpenLedger loads the ledger at path. When it doesn't exist yet (or is
// unreadable) it is seeded with everything outboxDir holds right now, so the
// first start never re-sends hundreds of old files; seeded reports that. The
// returned ledger is always usable: err only means it couldn't be saved.
func OpenLedger(path, outboxDir string) (l *Ledger, seeded bool, err error) {
	l = &Ledger{path: path, seen: map[string]stamp{}}
	if data, rerr := os.ReadFile(path); rerr == nil && json.Unmarshal(data, &l.seen) == nil && l.seen != nil {
		return l, false, nil
	}
	l.seen = map[string]stamp{}
	for _, f := range scanOutbox(outboxDir) {
		l.seen[filepath.Base(f.Path)] = stamp{f.Mod.UnixNano(), f.Size}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l, true, l.save()
}

// Pending lists the outbox files not delivered yet, sorted by name, split by
// stability: a file modified less than settle ago may still be being written
// (jobs copy 40 MB GIFs in), so it waits. wait is how long until the youngest
// of those settles (0 when none is waiting).
func (l *Ledger) Pending(dir string, now time.Time, settle time.Duration) (ready []OutboxFile, wait time.Duration) {
	files := scanOutbox(dir)
	l.mu.Lock()
	defer l.mu.Unlock()
	present := make(map[string]bool, len(files))
	for _, f := range files {
		name := filepath.Base(f.Path)
		present[name] = true
		if s, ok := l.seen[name]; ok && s.Mod == f.Mod.UnixNano() && s.Size == f.Size {
			continue
		}
		if age := now.Sub(f.Mod); age >= 0 && age < settle {
			wait = max(wait, settle-age)
			continue
		}
		ready = append(ready, f)
	}
	// Forget files that are gone, so the ledger doesn't grow forever and a file
	// recreated later under the same name counts as new.
	for name := range l.seen {
		if !present[name] {
			delete(l.seen, name)
		}
	}
	return ready, wait
}

// Mark records f as delivered and saves the ledger.
func (l *Ledger) Mark(f OutboxFile) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seen[filepath.Base(f.Path)] = stamp{f.Mod.UnixNano(), f.Size}
	return l.save()
}

// save writes the ledger atomically (tmp + rename). Caller holds l.mu.
func (l *Ledger) save() error {
	data, err := json.Marshal(l.seen)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}

// scanOutbox lists the regular files in dir, sorted by name. Dotfiles are
// skipped: they are temp files (rsync, editors), never deliverables.
func scanOutbox(dir string) []OutboxFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []OutboxFile
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := os.Stat(path) // follows symlinks, as the upload does
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		out = append(out, OutboxFile{Path: path, Mod: info.ModTime(), Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
