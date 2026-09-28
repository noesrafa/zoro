package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A focused session must NOT survive a restart: Open rotates it away (resuming
// it from another cwd would fork the transcript — the CLI keys sessions by cwd).
func TestOpenRotatesFocusedSession(t *testing.T) {
	dir := t.TempDir()

	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.NewIn("/tmp/proyecto")
	if err != nil {
		t.Fatal(err)
	}
	if st.WorkDir != "/tmp/proyecto" {
		t.Fatalf("NewIn did not set WorkDir: %+v", st)
	}

	// simulate restart
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := s2.Current()
	if got.WorkDir != "" {
		t.Fatalf("focus survived restart: %+v", got)
	}
	if got.SessionID == st.SessionID {
		t.Fatalf("focused session was not rotated at boot: %+v", got)
	}
}

// An unfocused session DOES survive a restart — rotating it would wipe the
// conversation on every redeploy.
func TestOpenKeepsHomeSession(t *testing.T) {
	dir := t.TempDir()

	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.New()
	if err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Current(); got.SessionID != st.SessionID {
		t.Fatalf("home session did not survive restart: got %q want %q", got.SessionID, st.SessionID)
	}
}

// /auth: one session per backend. sub → mimo → sub brings back the SAME
// subscription session (id and created flag), and MiMo never gets it.
func TestUseParksAndResumesTheExactSession(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = s.MarkCreated()
	sub := s.Current()

	m, sw, err := s.Use("mimo")
	if err != nil {
		t.Fatal(err)
	}
	if !sw.Changed || sw.Resumed || m.SessionID == sub.SessionID || m.Created || m.Backend != "mimo" {
		t.Fatalf("mimo must start a brand-new session: %+v %+v", m, sw)
	}
	if p, ok := s.Parked(""); !ok || p.SessionID != sub.SessionID || !p.Created {
		t.Fatalf("sub session not parked intact: %+v %v", p, ok)
	}
	_ = s.Set("mimo-real-id", true) // the CLI may hand back its own id
	mimo := s.Current()

	back, sw, _ := s.Use("")
	if !sw.Resumed || back.SessionID != sub.SessionID || !back.Created || back.Backend != "" {
		t.Fatalf("sub session not resumed exactly: %+v, want %+v", back, sub)
	}
	again, sw, _ := s.Use("mimo")
	if !sw.Resumed || again.SessionID != mimo.SessionID || !again.Created {
		t.Fatalf("mimo session not resumed: %+v, want %+v", again, mimo)
	}
	// Same backend: nothing moves.
	if st, sw, _ := s.Use("mimo"); sw.Changed || st.SessionID != mimo.SessionID {
		t.Fatalf("Use on the active backend must be a no-op: %+v %+v", st, sw)
	}
}

func TestParkedSessionSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	_ = s.MarkCreated()
	sub := s.Current()
	_, _, _ = s.Use("mimo")
	_ = s.MarkCreated()
	mimo := s.Current()

	s2, err := Open(dir) // restart while on MiMo
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Current(); got.SessionID != mimo.SessionID || got.Backend != "mimo" || !got.Created {
		t.Fatalf("active mimo session lost on restart: %+v", got)
	}
	back, sw, _ := s2.Use("")
	if !sw.Resumed || back.SessionID != sub.SessionID || !back.Created {
		t.Fatalf("parked sub session lost on restart: %+v", back)
	}
}

// The nightly cut drops the parked session too: the brief is the continuity.
func TestRolloverDropsTheParkedSession(t *testing.T) {
	s, _ := Open(t.TempDir())
	_ = s.MarkCreated()
	sub := s.Current()
	_, _, _ = s.Use("mimo")
	st, err := s.Rollover()
	if err != nil {
		t.Fatal(err)
	}
	if st.Backend != "mimo" || st.Created {
		t.Fatalf("rollover must rotate on the active backend: %+v", st)
	}
	if _, ok := s.Parked(""); ok {
		t.Fatal("rollover must drop the parked sub session")
	}
	back, sw, _ := s.Use("")
	if sw.Resumed || back.SessionID == sub.SessionID {
		t.Fatalf("yesterday's sub session came back after the rollover: %+v", back)
	}
}

// /newsession and /focus rotate only the active backend's session.
func TestNewKeepsBackendAndParkedSession(t *testing.T) {
	s, _ := Open(t.TempDir())
	_ = s.MarkCreated()
	sub := s.Current()
	_, _, _ = s.Use("mimo")
	for _, rotate := range []func() (State, error){s.New, func() (State, error) { return s.NewIn("/tmp/p") }} {
		st, _ := rotate()
		if st.Backend != "mimo" {
			t.Fatalf("rotation changed backend: %+v", st)
		}
		if p, ok := s.Parked(""); !ok || p.SessionID != sub.SessionID {
			t.Fatalf("rotation touched the parked session: %+v %v", p, ok)
		}
	}
}

// A focused session parked by /auth does not survive a restart either.
func TestFocusedParkedSessionDroppedAtBoot(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	_, _ = s.NewIn("/tmp/p")
	_ = s.MarkCreated()
	_, _, _ = s.Use("mimo")
	s2, _ := Open(dir)
	if p, ok := s2.Parked(""); ok {
		t.Fatalf("focused parked session survived the restart: %+v", p)
	}
}

// A state.json from before /auth loads as "sub session, nothing parked", and a
// sub-agent (never switches) never gets it rewritten.
func TestLegacyStateIsTheSubSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	old := []byte(`{
  "session_id": "82602e2d-6443-444c-bc16-cefbfffe8e17",
  "created": true,
  "updated_at": "2026-09-28T07:09:00Z"
}`)
	if err := os.WriteFile(path, old, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st, sw, _ := s.Use(""); sw.Changed || st.SessionID != "82602e2d-6443-444c-bc16-cefbfffe8e17" || !st.Created {
		t.Fatalf("legacy session must be the sub session: %+v %+v", st, sw)
	}
	if b, _ := os.ReadFile(path); string(b) != string(old) {
		t.Fatalf("state.json rewritten without a switch:\n%s", b)
	}
	if _, sw, _ := s.Use("mimo"); !sw.Changed {
		t.Fatal("switch to mimo did not move")
	}
	if p, ok := s.Parked(""); !ok || p.SessionID != "82602e2d-6443-444c-bc16-cefbfffe8e17" {
		t.Fatalf("legacy session not parked as sub: %+v", p)
	}
}

// Writes go through temp file + rename and leave valid JSON behind.
func TestSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	_, _, _ = s.Use("mimo")
	if _, err := os.Stat(filepath.Join(dir, "state.json.tmp")); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "state.json"))
	var d disk
	if err := json.Unmarshal(b, &d); err != nil || d.Backend != "mimo" || d.Parked["sub"].SessionID == "" {
		t.Fatalf("state.json = %s (%v)", b, err)
	}
}
