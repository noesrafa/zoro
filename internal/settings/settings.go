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
	// Idioma overrides the soul's output-language rule: "es" forces Spanish,
	// "" or "en" leaves the soul's default (English) in charge.
	Idioma string `json:"idioma,omitempty"`
	// Auth picks the backend: "" = Claude subscription, "mimo" = MiMo Token Plan.
	// Switching never rotates the session (cross-resume works both ways).
	Auth string `json:"auth,omitempty"`
	// Gate turns on the English gate (/gate on): a Spanish message from the owner
	// gets its English version back instead of an answer. Off by default.
	Gate bool `json:"gate,omitempty"`
	// GateSpanishOff: with the gate on, a Spanish message gets no "Say it in
	// English" quiz (only English with mistakes does). Off = Spanish quizzes ON.
	GateSpanishOff bool `json:"gate_spanish_off,omitempty"`
	// Connectors (/connectors on|off): "on" loads the claude.ai connectors, the
	// MCP servers and their skills in the agent's sessions, "off" leaves them
	// out; "" = the agent's default (off on Zoro, on on the sub-agents).
	Connectors string `json:"connectors,omitempty"`
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

// SetIdioma persists the language override ("es" or "en").
func (s *Store) SetIdioma(v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.s.Idioma = v
	return s.save()
}

// SetGate persists the English gate switch.
func (s *Store) SetGate(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.s.Gate = on
	return s.save()
}

// SetGateSpanishOff persists the Spanish-quiz switch of the gate.
func (s *Store) SetGateSpanishOff(off bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.s.GateSpanishOff = off
	return s.save()
}

// SetConnectors persists the connectors switch ("on", "off").
func (s *Store) SetConnectors(v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.s.Connectors = v
	return s.save()
}

// SetAuth persists the backend ("" = subscription, "mimo").
func (s *Store) SetAuth(v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.s.Auth = v
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
