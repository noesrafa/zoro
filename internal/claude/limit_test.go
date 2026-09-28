package claude

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The banner is VERBATIM from the transcript of 27-sep-2026 (synthetic assistant
// message, error:"rate_limit", apiErrorStatus 429), when the shared weekly limit
// ran out for every agent at once.
const weeklyBanner = "You've hit your weekly limit · resets 11pm (America/Mexico_City)"

// HitLimit pauses EVERY agent, so both directions matter: a real limit must be
// caught, and a normal reply that talks about the limit must never pause anyone.
func TestHitLimit(t *testing.T) {
	cases := []struct {
		name string
		res  Result
		want bool
	}{
		{"real weekly limit: error tag + is_error", Result{Text: weeklyBanner, IsError: true, APIError: "rate_limit"}, true},
		{"only the error tag", Result{Text: weeklyBanner, APIError: "rate_limit"}, true},
		{"only is_error", Result{Text: weeklyBanner, IsError: true}, true},
		{"session limit", Result{Text: "You've hit your session limit · resets 3am (America/Mexico_City)", IsError: true}, true},
		{"curly apostrophe", Result{Text: "You’ve hit your Opus limit", IsError: true}, true},
		{"limit mid-turn after some real text", Result{Text: "Checked the repo.\n\n" + weeklyBanner, IsError: true}, true},
		{"limit reported in the errors array", Result{Errors: []string{weeklyBanner}, IsError: true}, true},

		{"normal reply quoting the banner word for word", Result{Text: weeklyBanner}, false},
		{"normal reply explaining the limit", Result{Text: "Yesterday you hit your weekly limit; it resets 11pm on Sunday."}, false},
		{"normal reply starting a line with the banner", Result{Text: "Here is what they saw:\nYou've hit your weekly limit · resets 11pm"}, false},
		{"transient API rate limit is not the subscription limit", Result{Text: "API Error: Rate limit reached", IsError: true, APIError: "rate_limit"}, false},
		{"auth failure", Result{Text: "Failed to authenticate: OAuth session expired", IsError: true}, false},
		{"error that mentions the limit mid-sentence", Result{Text: "Tool failed: grep 'You've hit your weekly limit' found 3 lines", IsError: true}, false},
		{"empty", Result{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.res.HitLimit(); got != c.want {
				t.Errorf("HitLimit() = %v, want %v (%+v)", got, c.want, c.res)
			}
		})
	}
}

func TestLimitResets(t *testing.T) {
	r := Result{Text: weeklyBanner, IsError: true}
	if got := r.LimitResets(); got != "11pm (America/Mexico_City)" {
		t.Errorf("LimitResets() = %q", got)
	}
	if got := (Result{Text: "You've hit your weekly limit", IsError: true}).LimitResets(); got != "" {
		t.Errorf("no resets part should give \"\", got %q", got)
	}
}

// fakeCLI writes a stand-in for the claude binary: it dumps its environment to
// envOut, prints the given stream-json lines and exits with code.
func fakeCLI(t *testing.T, lines []string, code int) (bin, envOut string) {
	t.Helper()
	dir := t.TempDir()
	envOut = filepath.Join(dir, "env.txt")
	var sb strings.Builder
	sb.WriteString("#!/bin/sh\nenv > '" + envOut + "'\ncat <<'JSON'\n")
	for _, l := range lines {
		sb.WriteString(l + "\n")
	}
	sb.WriteString("JSON\nexit " + string(rune('0'+code)) + "\n")
	bin = filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(sb.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, envOut
}

// End to end through the stream-json parser: the event shapes are the ones the
// CLI 2.1.281 emits for the limit (assistant with top-level error tag, then an
// is_error result, exit 1) and for a normal answer.
func TestRunDetectsLimitFromStream(t *testing.T) {
	limit := []string{
		`{"type":"system","subtype":"init","session_id":"s-1"}`,
		`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"` + weeklyBanner + `"}]},"session_id":"s-1","error":"rate_limit"}`,
		`{"type":"result","subtype":"success","is_error":true,"result":"` + weeklyBanner + `","session_id":"s-1"}`,
	}
	bin, _ := fakeCLI(t, limit, 1)
	res, err := New(Config{Bin: bin, WorkDir: t.TempDir()}).Run(context.Background(), "s-1", false, "hola", RunOpts{})
	if err == nil {
		t.Fatal("exit 1 must be an error")
	}
	if res.APIError != "rate_limit" || !res.HitLimit() || res.SessionID != "s-1" {
		t.Fatalf("limit not detected: APIError=%q HitLimit=%v session=%q", res.APIError, res.HitLimit(), res.SessionID)
	}

	normal := []string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"` + weeklyBanner + ` — that's what the agents saw on Saturday."}]},"session_id":"s-1"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"s-1","error":{"weird":"object"}}`,
	}
	bin, _ = fakeCLI(t, normal, 0)
	res, err = New(Config{Bin: bin, WorkDir: t.TempDir()}).Run(context.Background(), "s-1", false, "hola", RunOpts{})
	if err != nil || res.HitLimit() || res.APIError != "" {
		t.Fatalf("normal reply must not be a limit: err=%v HitLimit=%v APIError=%q", err, res.HitLimit(), res.APIError)
	}
	if res.NumTurns != 0 || !strings.Contains(res.Text, "Saturday") {
		t.Fatalf("an object-typed \"error\" on another event must not break parsing, got %+v", res)
	}
}

func envMap(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, l := range strings.Split(string(raw), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			m[k] = v
		}
	}
	return m
}

const secretKey = "tp-SECRET-do-not-leak-123"

// Spelled out here, NOT taken from backendVars: a test that reuses the list under
// test can't notice an entry missing from it.
var mimoVars = []string{
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
}

func TestBackendEnv(t *testing.T) {
	// A parent env polluted with every backend var: sub mode must drop them all.
	for _, k := range mimoVars {
		t.Setenv(k, "from-parent")
	}
	keyFile := filepath.Join(t.TempDir(), "mimo.key")
	if err := os.WriteFile(keyFile, []byte(secretKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mimo := MiMo{KeyFile: keyFile, Model: "mimo-v2.6-pro", BaseURL: "https://token-plan-sgp.xiaomimimo.com/anthropic"}
	ok := []string{`{"type":"result","subtype":"success","is_error":false,"result":"hi","session_id":"s"}`}

	t.Run("sub mode leaks nothing", func(t *testing.T) {
		bin, envOut := fakeCLI(t, ok, 0)
		if _, err := New(Config{Bin: bin, WorkDir: t.TempDir(), MiMo: mimo}).Run(context.Background(), "s", true, "hi", RunOpts{}); err != nil {
			t.Fatal(err)
		}
		env := envMap(t, envOut)
		for _, k := range mimoVars {
			if v, ok := env[k]; ok {
				t.Errorf("sub mode leaked %s=%s", k, v)
			}
		}
	})

	t.Run("mimo mode sets exactly its vars", func(t *testing.T) {
		bin, envOut := fakeCLI(t, ok, 0)
		if _, err := New(Config{Bin: bin, WorkDir: t.TempDir(), MiMo: mimo}).Run(context.Background(), "s", true, "hi", RunOpts{Auth: AuthMiMo, Model: "opus", Effort: "high"}); err != nil {
			t.Fatal(err)
		}
		env := envMap(t, envOut)
		want := map[string]string{
			"ANTHROPIC_BASE_URL":                       mimo.BaseURL,
			"ANTHROPIC_AUTH_TOKEN":                     secretKey,
			"ANTHROPIC_MODEL":                          "mimo-v2.6-pro",
			"ANTHROPIC_DEFAULT_OPUS_MODEL":             "mimo-v2.6-pro",
			"ANTHROPIC_DEFAULT_SONNET_MODEL":           "mimo-v2.6-pro",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL":            "mimo-v2.6-pro",
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
		}
		for k, v := range want {
			if env[k] != v {
				t.Errorf("%s = %q, want %q", k, env[k], v)
			}
		}
		if _, ok := env["ANTHROPIC_API_KEY"]; ok {
			t.Error("ANTHROPIC_API_KEY must be unset in mimo mode")
		}
	})

	t.Run("mimo failure never echoes the key", func(t *testing.T) {
		bin, _ := fakeCLI(t, []string{`{"type":"result","subtype":"success","is_error":true,"result":"API Error: 401 invalid token","session_id":"s"}`}, 1)
		res, err := New(Config{Bin: bin, WorkDir: t.TempDir(), MiMo: mimo}).Run(context.Background(), "s", true, "hi", RunOpts{Auth: AuthMiMo})
		if err == nil {
			t.Fatal("want error")
		}
		if strings.Contains(err.Error(), secretKey) || strings.Contains(res.Diagnostic(), secretKey) {
			t.Fatal("key leaked into the error")
		}
	})

	t.Run("missing key file is a clear error, no CLI run", func(t *testing.T) {
		bin, envOut := fakeCLI(t, ok, 0)
		m := mimo
		m.KeyFile = filepath.Join(t.TempDir(), "nope.key")
		_, err := New(Config{Bin: bin, WorkDir: t.TempDir(), MiMo: m}).Run(context.Background(), "s", true, "hi", RunOpts{Auth: AuthMiMo})
		if err == nil || !strings.Contains(err.Error(), "mimo key file") || !strings.Contains(err.Error(), m.KeyFile) {
			t.Fatalf("want a clear key-file error, got %v", err)
		}
		if _, statErr := os.Stat(envOut); statErr == nil {
			t.Fatal("the CLI must not run without a key")
		}
	})
}
