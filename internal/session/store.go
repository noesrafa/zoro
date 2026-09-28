// Package session persists the always-resumed Claude session id to disk — one
// per backend (/auth): the active one, plus at most one parked session per
// backend that is not in use right now.
package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"zoro/internal/uid"
)

// State is the persisted session metadata.
type State struct {
	SessionID string `json:"session_id"`
	Created   bool   `json:"created"`            // whether the id has been used to create a session
	WorkDir   string `json:"work_dir,omitempty"` // /focus: cwd of this session ("" = home); constant per session
	// Backend the session was born on (/auth): "" = Claude subscription, "mimo"…
	// A session only ever runs on its own backend. Empty on every state.json
	// written before /auth existed — those were all subscription sessions.
	Backend   string `json:"backend,omitempty"`
	UpdatedAt string `json:"updated_at"`
}

// disk is state.json: the active session flat at the top (the pre-/auth shape,
// so an old file loads as "active session, nothing parked"), plus the sessions
// of the other backends, keyed by parkKey(backend).
type disk struct {
	State
	Parked map[string]State `json:"parked,omitempty"`
}

// parkKey names a backend in the parked map ("" can't be a readable key).
func parkKey(backend string) string {
	if backend == "" {
		return "sub"
	}
	return backend
}

// Store guards access to state.json.
type Store struct {
	path   string
	mu     sync.Mutex
	st     State
	parked map[string]State
}

// Open loads (or initializes) the store under dir.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "state.json")}
	var d disk
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &d)
	}
	s.st, s.parked = d.State, map[string]State{}
	dirty := false
	for k, p := range d.Parked {
		// Keep only sane entries: a real id, filed under its own backend, never
		// the active backend's (that one is s.st), and not focused — focus does
		// not survive a restart, parked or not.
		if p.SessionID == "" || parkKey(p.Backend) != k || p.Backend == s.st.Backend || p.WorkDir != "" {
			dirty = true
			continue
		}
		s.parked[k] = p
	}
	// A focused session does NOT survive a restart (rafiña's call, 10-sep-2026):
	// rotate it away at boot. Resuming it from home would also fork the
	// transcript — the CLI keys conversations by cwd. The backend stays.
	if s.st.SessionID == "" || s.st.WorkDir != "" {
		s.st = State{SessionID: uid.New(), Created: false, Backend: s.st.Backend, UpdatedAt: now()}
		dirty = true
	}
	if dirty {
		if err := s.save(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Current returns the active state.
func (s *Store) Current() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st
}

// Parked returns the session saved for backend, if any.
func (s *Store) Parked(backend string) (State, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.parked[parkKey(backend)]
	return p, ok
}

// MarkCreated flags the current session id as created (so future turns resume it).
func (s *Store) MarkCreated() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Created = true
	s.st.UpdatedAt = now()
	return s.save()
}

// New rotates to a brand-new, uncreated session id (home, no focus) on the same
// backend. The other backends' parked sessions are untouched.
func (s *Store) New() (State, error) { return s.NewIn("") }

// NewIn rotates to a brand-new session that will live inside dir (/focus), on
// the same backend. The cwd is decided at rotation and never changes for the
// session's lifetime.
func (s *Store) NewIn(dir string) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st = State{SessionID: uid.New(), Created: false, WorkDir: dir, Backend: s.st.Backend, UpdatedAt: now()}
	return s.st, s.save()
}

// Rollover is the nightly cut: a brand-new home session on the active backend
// AND every parked session dropped. The brief carries the day's thread into the
// new session; a parked one would resume yesterday's transcript behind its back.
func (s *Store) Rollover() (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st = State{SessionID: uid.New(), Created: false, Backend: s.st.Backend, UpdatedAt: now()}
	s.parked = map[string]State{}
	return s.st, s.save()
}

// Switch reports what Use did.
type Switch struct {
	Changed bool  // the active session was swapped
	Resumed bool  // … for one that was parked (false = a brand-new session)
	From    State // the session that was active before (parked now, if Changed)
}

// Use makes the active session one that belongs to backend: a no-op when it
// already does; otherwise the active session is parked under its own backend
// and backend's parked session comes back EXACTLY as it was (same id, created,
// cwd) — or, if there is none, a brand-new uncreated one starts. A session is
// never carried from one backend to another.
func (s *Store) Use(backend string) (State, Switch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sw := Switch{From: s.st}
	if s.st.Backend == backend {
		return s.st, sw, nil
	}
	sw.Changed = true
	if s.parked == nil {
		s.parked = map[string]State{}
	}
	s.parked[parkKey(s.st.Backend)] = s.st
	if p, ok := s.parked[parkKey(backend)]; ok {
		delete(s.parked, parkKey(backend))
		s.st, sw.Resumed = p, true
	} else {
		s.st = State{SessionID: uid.New(), Created: false, Backend: backend}
	}
	s.st.UpdatedAt = now()
	return s.st, sw, s.save()
}

// Set overwrites the current session id and created flag.
func (s *Store) Set(id string, created bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.SessionID = id
	s.st.Created = created
	s.st.UpdatedAt = now()
	return s.save()
}

// save writes state.json atomically (temp file + rename): a crash mid-write
// leaves the previous file, never half of one.
func (s *Store) save() error {
	d := disk{State: s.st}
	if len(s.parked) > 0 {
		d.Parked = s.parked
	}
	b, _ := json.MarshalIndent(d, "", "  ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }
