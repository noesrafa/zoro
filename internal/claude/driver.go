// Package claude drives the `claude` CLI as a subprocess in headless stream-json
// mode, maintaining one conversation via create-once / resume-forever.
package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type Config struct {
	Bin        string
	Model      string
	WorkDir    string // cmd.Dir — MUST be constant across calls or --resume forks a new session
	DangerSkip bool
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
}

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
	case strings.Contains(d, "usage limit"),
		strings.Contains(d, "rate limit"),
		strings.Contains(d, "quota"):
		return FailLimit
	case strings.Contains(d, "no conversation found with session id"):
		return FailNoSession
	}
	return FailUnknown
}

// RunOpts overrides per-turn knobs (model, effort, system prompt).
type RunOpts struct {
	Model        string
	Effort       string
	SystemPrompt string // overrides cfg.SystemPrompt; read fresh each turn for hot-reload
}

type Driver struct{ cfg Config }

func New(cfg Config) *Driver { return &Driver{cfg: cfg} }

func (d *Driver) args(sessionID string, create bool, prompt string, o RunOpts) []string {
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
	if o.SystemPrompt != "" {
		a = append(a, "--append-system-prompt", o.SystemPrompt)
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
	Message      json.RawMessage `json:"message"`
}

// Run executes one turn and returns the assistant's final text.
func (d *Driver) Run(ctx context.Context, sessionID string, create bool, prompt string, o RunOpts) (Result, error) {
	cmd := exec.CommandContext(ctx, d.cfg.Bin, d.args(sessionID, create, prompt, o)...)
	cmd.Dir = d.cfg.WorkDir
	cmd.Env = scrubEnv(os.Environ())

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

// scrubEnv removes API-key env vars so the CLI uses its own OAuth (subscription)
// credentials instead of switching to per-token API billing.
func scrubEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, "ANTHROPIC_API_KEY=") || strings.HasPrefix(e, "ANTHROPIC_AUTH_TOKEN=") {
			continue
		}
		out = append(out, e)
	}
	return out
}
