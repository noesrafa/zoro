package session

import "testing"

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
