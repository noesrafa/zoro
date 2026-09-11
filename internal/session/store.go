// Package session persists the single, always-resumed Claude session id to disk.
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
	Created   bool   `json:"created"` // whether the id has been used to create a session
	WorkDir   string `json:"work_dir,omitempty"` // /focus: cwd of this session ("" = home); constant per session
	UpdatedAt string `json:"updated_at"`
}

// Store guards access to state.json.
type Store struct {
	path string
	mu   sync.Mutex
	st   State
}

// Open loads (or initializes) the store under dir.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "state.json")}
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &s.st)
	}
	// A focused session does NOT survive a restart (rafiña's call, 10-sep-2026):
	// rotate it away at boot. Resuming it from home would also fork the
	// transcript — the CLI keys conversations by cwd.
	if s.st.SessionID == "" || s.st.WorkDir != "" {
		s.st = State{SessionID: uid.New(), Created: false, UpdatedAt: now()}
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

// MarkCreated flags the current session id as created (so future turns resume it).
func (s *Store) MarkCreated() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Created = true
	s.st.UpdatedAt = now()
	return s.save()
}

// New rotates to a brand-new, uncreated session id (home, no focus).
func (s *Store) New() (State, error) { return s.NewIn("") }

// NewIn rotates to a brand-new session that will live inside dir (/focus).
// The cwd is decided at rotation and never changes for the session's lifetime.
func (s *Store) NewIn(dir string) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st = State{SessionID: uid.New(), Created: false, WorkDir: dir, UpdatedAt: now()}
	return s.st, s.save()
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

func (s *Store) save() error {
	b, _ := json.MarshalIndent(s.st, "", "  ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }
