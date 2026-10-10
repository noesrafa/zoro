// Package claude drives the `claude` CLI as a subprocess in headless stream-json
// mode, maintaining one conversation via create-once / resume-forever.
package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type Config struct {
	Bin        string
	Model      string
	WorkDir    string // cmd.Dir — MUST be constant across calls or --resume forks a new session
	DangerSkip bool
	MiMo       MiMo // the alternate backend behind /auth mimo
}

// AuthMiMo is the RunOpts.Auth value that routes a turn to Xiaomi's MiMo Token
// Plan (Anthropic-compatible endpoint) instead of the Claude subscription.
const AuthMiMo = "mimo"

// MiMo is where the MiMo backend lives. The key is read from KeyFile on EVERY
// turn (never cached, never logged) so rotating it needs no restart.
type MiMo struct {
	KeyFile string
	Model   string
	BaseURL string
}

// Result is the outcome of one turn.
type Result struct {
	Text      string
	SessionID string
	CostUSD   float64
	NumTurns  int
	IsError   bool
	Subtype   string
	Errors    []string // "errors" array of the result event (e.g. resume of a missing session)
	// APIError is the "error" tag the CLI puts on a synthetic assistant message
	// when the API call itself failed ("rate_limit", "authentication_failed"…).
	// A real model reply never carries it.
	APIError string
	Usage    Usage // what the turn read and wrote, summed over its API calls (from the result event)
}

// Usage is the token count the CLI reports for a turn. Input is everything the
// model read: fresh + cache writes + cache reads.
type Usage struct {
	InputTokens         int `json:"input_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
	OutputTokens        int `json:"output_tokens"`
}

// Input is every input token of the turn, cached or not.
func (u Usage) Input() int { return u.InputTokens + u.CacheCreationTokens + u.CacheReadTokens }

// FailureKind classifies WHY a turn failed, read from the CLI's own output.
// Needed because the most important failure — the OAuth session dying — exits 1
// with a COMPLETELY EMPTY stderr, so the caller would otherwise only ever see a
// bare "exit status 1". The real message arrives on stdout, as a <synthetic>
// assistant text ("Failed to authenticate: OAuth session expired and could not
// be refreshed"), which Run already accumulates into Result.Text.
type FailureKind int

const (
	FailUnknown FailureKind = iota
	FailAuth                // login died: expired/unrefreshable OAuth, invalid key
	FailLimit               // subscription usage limit hit
	FailNoSession           // --resume pointed at a transcript that doesn't exist
)

// Diagnostic is everything the CLI told us about the turn, in one string.
func (r Result) Diagnostic() string {
	parts := make([]string, 0, 1+len(r.Errors))
	if t := strings.TrimSpace(r.Text); t != "" {
		parts = append(parts, t)
	}
	parts = append(parts, r.Errors...)
	return strings.Join(parts, "\n")
}

// Failure classifies a failed turn. Only meaningful when Run returned an error
// (or Result.IsError is set).
func (r Result) Failure() FailureKind {
	d := strings.ToLower(r.Diagnostic())
	switch {
	case strings.Contains(d, "failed to authenticate"),
		strings.Contains(d, "oauth session expired"),
		strings.Contains(d, "could not be refreshed"),
		strings.Contains(d, "invalid api key"),
		strings.Contains(d, "please run /login"),
		strings.Contains(d, "authentication_error"):
		return FailAuth
	case r.HitLimit():
		return FailLimit
	case strings.Contains(d, "no conversation found with session id"):
		return FailNoSession
	}
	return FailUnknown
}

// limitRe is the CLI's own subscription-limit banner, anchored to the start of a
// line: "You've hit your weekly limit · resets 11pm (America/Mexico_City)" (also
// session / Opus limits and the monthly spend cap), plus the older
// "Claude AI usage limit reached|<epoch>".
var limitRe = regexp.MustCompile(`(?im)^\s*(?:you(?:'|’)ve hit your [^\n·]{0,40}?limit|claude ai usage limit reached)`)

var resetsRe = regexp.MustCompile(`(?i)\bresets\s+([^\n·]+)`)

// HitLimit reports whether the turn died on the Claude subscription's usage limit.
// It demands BOTH signals: the turn must have failed as an API error (the CLI
// tags the synthetic message error:"rate_limit" and the result is_error), AND the
// text must be the CLI's limit banner. A normal reply that merely talks about the
// weekly limit — even quoting the banner word for word — has neither error flag,
// so it can never pause the agents.
func (r Result) HitLimit() bool {
	if !r.IsError && r.APIError == "" {
		return false
	}
	return limitRe.MatchString(r.Diagnostic())
}

// LimitResets extracts the "resets …" part of the limit banner ("11pm
// (America/Mexico_City)"), or "" when the CLI didn't say.
func (r Result) LimitResets() string {
	if m := resetsRe.FindStringSubmatch(r.Diagnostic()); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// RunOpts overrides per-turn knobs (model, effort, system prompt).
type RunOpts struct {
	Model        string
	Effort       string
	SystemPrompt string // overrides cfg.SystemPrompt; read fresh each turn for hot-reload
	WorkDir      string // overrides cfg.WorkDir (/focus) — MUST stay constant within a session
	Auth         string // "" = Claude subscription (OAuth), AuthMiMo = MiMo Token Plan
	// Bare is a one-shot text job (the English quiz): SystemPrompt REPLACES
	// Claude Code's own, and no tools, MCP servers, skills, settings files
	// (CLAUDE.md, hooks) or transcript. ~0.9K tokens of input instead of ~26K.
	Bare bool
	// NoConnectors leaves out the claude.ai connectors (Higgsfield, Drive,
	// Gmail…), every other MCP server (lazyweb) and the skills and plugins that
	// only go with them: ~8.5K fewer input tokens on EVERY call of a session, and
	// no mid-session re-announcements of their tool lists. /connectors on.
	NoConnectors bool
}

// offSkillPrefixes are skills that only drive an MCP server NoConnectors drops
// (matched by name prefix in the CLI's skills dir, so new ones are caught too);
// offPlugins are plugins nobody uses here. The higgsfield-* skills stay: they
// run the higgsfield CLI, not the connector.
var (
	offSkillPrefixes = []string{"lazyweb"}
	offPlugins       = []string{"cowork-plugin-management@synced"}
)

// leanSettings is the --settings JSON of a NoConnectors turn: skillOverrides
// "off" hides a skill from the model and the slash menu without deleting it.
func leanSettings(env []string) string {
	dir := ""
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "CLAUDE_CONFIG_DIR="); ok {
			dir = v
		}
	}
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	skills := map[string]string{}
	ents, _ := os.ReadDir(filepath.Join(dir, "skills"))
	for _, e := range ents {
		for _, p := range offSkillPrefixes {
			if strings.HasPrefix(e.Name(), p) {
				skills[e.Name()] = "off"
			}
		}
	}
	plugins := map[string]bool{}
	for _, p := range offPlugins {
		plugins[p] = false
	}
	raw, _ := json.Marshal(map[string]any{"skillOverrides": skills, "enabledPlugins": plugins})
	return string(raw)
}

type Driver struct{ cfg Config }

func New(cfg Config) *Driver { return &Driver{cfg: cfg} }

func (d *Driver) args(sessionID string, create bool, prompt string, o RunOpts) []string {
	return d.argsEnv(sessionID, create, prompt, o, os.Environ())
}

func (d *Driver) argsEnv(sessionID string, create bool, prompt string, o RunOpts, env []string) []string {
	model := o.Model
	if model == "" {
		model = d.cfg.Model
	}
	a := []string{"-p", "--output-format", "stream-json", "--verbose"}
	if model != "" {
		a = append(a, "--model", model)
	}
	if o.Effort != "" {
		a = append(a, "--effort", o.Effort)
	}
	switch {
	case o.Bare:
		a = append(a, "--system-prompt", o.SystemPrompt, "--tools", "", "--strict-mcp-config",
			"--disable-slash-commands", "--setting-sources", "", "--no-session-persistence")
	case o.SystemPrompt != "":
		a = append(a, "--append-system-prompt", o.SystemPrompt)
	}
	if o.NoConnectors && !o.Bare {
		// --strict-mcp-config with no --mcp-config = no MCP server at all.
		a = append(a, "--strict-mcp-config", "--settings", leanSettings(env))
	}
	if d.cfg.DangerSkip {
		a = append(a, "--dangerously-skip-permissions")
	}
	if create {
		a = append(a, "--session-id", sessionID)
	} else {
		a = append(a, "--resume", sessionID)
	}
	a = append(a, prompt) // positional user message, last
	return a
}

type event struct {
	Type         string          `json:"type"`
	Subtype      string          `json:"subtype"`
	SessionID    string          `json:"session_id"`
	Result       string          `json:"result"`
	TotalCostUSD float64         `json:"total_cost_usd"`
	NumTurns     int             `json:"num_turns"`
	IsError      bool            `json:"is_error"`
	Errors       []string        `json:"errors"`
	Usage        *Usage          `json:"usage"`
	Message      json.RawMessage `json:"message"`
	// Raw on purpose: only assistant events carry it as a string, and a typed
	// field would make any other event with an object "error" fail to decode.
	Error json.RawMessage `json:"error"`
}

// Run executes one turn and returns the assistant's final text.
func (d *Driver) Run(ctx context.Context, sessionID string, create bool, prompt string, o RunOpts) (Result, error) {
	env, err := buildEnv(os.Environ(), o.Auth, d.cfg.MiMo)
	if err != nil {
		return Result{SessionID: sessionID}, err
	}
	cmd := exec.CommandContext(ctx, d.cfg.Bin, d.argsEnv(sessionID, create, prompt, o, env)...)
	cmd.Dir = d.cfg.WorkDir
	if o.WorkDir != "" {
		cmd.Dir = o.WorkDir
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return Result{}, err
	}

	res := Result{SessionID: sessionID}
	var assistant strings.Builder

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // allow large NDJSON lines
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev event
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "system":
			if ev.SessionID != "" {
				res.SessionID = ev.SessionID
			}
		case "assistant":
			var tag string
			if json.Unmarshal(ev.Error, &tag) == nil && tag != "" {
				res.APIError = tag
			}
			// Acumulamos CADA bloque de texto del turno, separando con doble
			// salto los mensajes distintos (los que van entre tool calls).
			if t := strings.TrimSpace(extractText(ev.Message)); t != "" {
				if assistant.Len() > 0 {
					assistant.WriteString("\n\n")
				}
				assistant.WriteString(t)
			}
		case "result":
			res.Subtype = ev.Subtype
			res.IsError = ev.IsError
			res.CostUSD = ev.TotalCostUSD
			res.NumTurns = ev.NumTurns
			res.Errors = ev.Errors
			if ev.Usage != nil {
				res.Usage = *ev.Usage
			}
			if ev.SessionID != "" {
				res.SessionID = ev.SessionID
			}
			if ev.Result != "" {
				res.Text = ev.Result
			}
		}
	}

	waitErr := cmd.Wait()
	if scErr := sc.Err(); scErr != nil && waitErr == nil {
		waitErr = scErr
	}
	// A Telegram debe ir TODO lo que dije en el turno — cada bloque de texto,
	// incluidos los emitidos ANTES de un tool call. El evento "result" del CLI
	// solo trae el ÚLTIMO bloque, así que preferirlo tiraba los mensajes
	// intermedios (ese era el bug: respuestas que "no llegaban" a Telegram).
	// Usamos el acumulado completo; result queda solo como fallback.
	if full := strings.TrimSpace(assistant.String()); full != "" {
		res.Text = full
	}
	if waitErr != nil {
		return res, fmt.Errorf("claude exited: %v: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	return res, nil
}

func extractText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	var b strings.Builder
	for _, c := range m.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

// backendVars are every env var that can point the CLI at another backend or
// credential. All of them are stripped from the parent env on every turn, so the
// subscription mode can never inherit a stray MiMo (or API-key) setting, and the
// MiMo mode sets exactly its own.
var backendVars = []string{
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL",
	"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
}

// scrubEnv removes backend/API-key env vars so the CLI uses its own OAuth
// (subscription) credentials instead of switching to per-token API billing.
func scrubEnv(env []string) []string {
	out := make([]string, 0, len(env))
next:
	for _, e := range env {
		for _, k := range backendVars {
			if strings.HasPrefix(e, k+"=") {
				continue next
			}
		}
		out = append(out, e)
	}
	return out
}

// buildEnv is the environment of one turn. MiMo mode reads the key file fresh;
// errors name the file but never contain the key.
func buildEnv(parent []string, auth string, m MiMo) ([]string, error) {
	env := scrubEnv(parent)
	if auth != AuthMiMo {
		return env, nil
	}
	raw, err := os.ReadFile(m.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("mimo key file %s not readable (/auth sub to go back): %w", m.KeyFile, errors.Unwrap(err))
	}
	key := strings.TrimSpace(string(raw))
	if key == "" {
		return nil, fmt.Errorf("mimo key file %s is empty (/auth sub to go back)", m.KeyFile)
	}
	return append(env,
		"ANTHROPIC_BASE_URL="+m.BaseURL,
		"ANTHROPIC_AUTH_TOKEN="+key,
		"ANTHROPIC_MODEL="+m.Model,
		"ANTHROPIC_DEFAULT_OPUS_MODEL="+m.Model,
		"ANTHROPIC_DEFAULT_SONNET_MODEL="+m.Model,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL="+m.Model,
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	), nil
}
