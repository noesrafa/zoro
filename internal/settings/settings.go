// Package settings persists runtime-tunable knobs (model, effort) to disk,
// separately from the conversation session so they survive /newsession and restarts.
package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Settings are the persisted, user-tunable options.
type Settings struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

// Store guards settings.json.
type Store struct {
	path string
	mu   sync.Mutex
	s    Settings
}

// Open loads settings.json under dir, falling back to def for missing fields.
func Open(dir string, def Settings) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	st := &Store{path: filepath.Join(dir, "settings.json"), s: def}
	if b, err := os.ReadFile(st.path); err == nil {
		_ = json.Unmarshal(b, &st.s)
	}
	if st.s.Model == "" {
		st.s.Model = def.Model
	}
	if st.s.Effort == "" {
		st.s.Effort = def.Effort
	}
	return st, st.save()
}

// Get returns a copy of the current settings.
func (s *Store) Get() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.s
}

// SetModel persists a new model.
func (s *Store) SetModel(m string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.s.Model = m
	return s.save()
}

// SetEffort persists a new effort level.
func (s *Store) SetEffort(e string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.s.Effort = e
	return s.save()
}

func (s *Store) save() error {
	b, _ := json.MarshalIndent(s.s, "", "  ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
