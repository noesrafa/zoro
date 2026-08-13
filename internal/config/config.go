// Package config loads zoro's runtime configuration from the environment
// (systemd EnvironmentFile in prod, or a local .env for `make run`).
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Token    string
	OwnerID  int64          // primario (recibe crons/notify): el primero de la lista
	OwnerIDs map[int64]bool // todos los IDs autorizados a hablarle al bot

	ClaudeBin   string
	ClaudeModel string
	Effort      string
	WorkDir     string
	EngineDir   string // where zoro's own Go source lives (for /redeploy build)
	DangerSkip  bool

	// Agent content lives OUTSIDE the engine, in ~/.zoro (its own git repo).
	ZoroHome    string // ~/.zoro
	SoulFile    string // ~/.zoro/soul.md — always injected (every turn), read fresh
	ContextFile string // ~/.zoro/context.md — injected once at the start of each session
	CronFile    string // ~/.zoro/crons.json — scheduled messages (hot-reloaded)

	StateDir  string
	InboxDir  string
	OutboxDir string

	// Mirror: when set, every turn (who wrote + what the agent answered) is echoed
	// to this chat via this same bot. Used so rafiña can watch the sub-agents
	// (sky, experienciaXXI) talk to his mom/dad without being in their chats.
	MirrorChatID int64
	AgentName    string // label shown in mirrored messages, e.g. "sky"

	FFmpegBin string

	WhisperBin   string
	WhisperModel string
	WhisperLang  string

	PiperBin   string
	PiperVoice string

	MaxFileBytes int64
}

// Load reads configuration, applying defaults. Token and OwnerID are required.
func Load() (Config, error) {
	loadDotEnv(".env")

	c := Config{
		Token:        getenv("TELEGRAM_BOT_TOKEN", ""),
		ClaudeBin:    getenv("CLAUDE_BIN", "/home/rafael/.local/bin/claude"),
		ClaudeModel:  getenv("CLAUDE_MODEL", "opus"),
		Effort:       getenv("ZORO_EFFORT", "high"),
		WorkDir:      getenv("ZORO_WORK_DIR", "/home/rafael"),
		EngineDir:    getenv("ZORO_ENGINE_DIR", "/home/rafael/zoro"),
		DangerSkip:   getbool("ZORO_DANGER_SKIP", true),
		ZoroHome:     getenv("ZORO_HOME", "/home/rafael/.zoro"),
		StateDir:     getenv("ZORO_STATE_DIR", "/home/rafael/zoro/state"),
		InboxDir:     getenv("ZORO_INBOX_DIR", "/home/rafael/zoro/inbox"),
		OutboxDir:    getenv("ZORO_OUTBOX_DIR", "/home/rafael/zoro/outbox"),
		FFmpegBin:    getenv("FFMPEG_BIN", "ffmpeg"),
		WhisperBin:   getenv("WHISPER_BIN", ""),
		WhisperModel: getenv("WHISPER_MODEL", ""),
		WhisperLang:  getenv("WHISPER_LANG", "es"),
		PiperBin:     getenv("PIPER_BIN", ""),
		PiperVoice:   getenv("PIPER_VOICE", ""),
		MaxFileBytes: getint64("ZORO_MAX_FILE_BYTES", 20*1024*1024),
		MirrorChatID: getint64("ZORO_MIRROR_CHAT_ID", 0),
		AgentName:    getenv("ZORO_AGENT_NAME", "zoro"),
	}
	c.SoulFile = getenv("ZORO_SOUL_FILE", filepath.Join(c.ZoroHome, "soul.md"))
	c.ContextFile = getenv("ZORO_CONTEXT_FILE", filepath.Join(c.ZoroHome, "context.md"))
	c.CronFile = getenv("ZORO_CRON_FILE", filepath.Join(c.ZoroHome, "crons.json"))

	// TELEGRAM_OWNER_ID admite varios IDs separados por coma. El primero es el
	// "primario" (recibe crons/notify); TODOS pueden hablarle al bot.
	c.OwnerIDs = map[int64]bool{}
	if ids := getenv("TELEGRAM_OWNER_ID", ""); ids != "" {
		for _, part := range strings.Split(ids, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			v, err := strconv.ParseInt(part, 10, 64)
			if err != nil {
				return c, fmt.Errorf("invalid TELEGRAM_OWNER_ID %q: %w", part, err)
			}
			if c.OwnerID == 0 {
				c.OwnerID = v
			}
			c.OwnerIDs[v] = true
		}
	}

	if c.Token == "" {
		return c, errors.New("TELEGRAM_BOT_TOKEN is required")
	}
	if c.OwnerID == 0 {
		return c, errors.New("TELEGRAM_OWNER_ID is required")
	}
	return c, nil
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getbool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	}
	return def
}

func getint64(key string, def int64) int64 {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return def
	}
	return n
}

// loadDotEnv best-effort loads KEY=VALUE lines from a file into the environment,
// without overriding variables that are already set.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		if k == "" {
			continue
		}
		if _, exists := os.LookupEnv(k); !exists {
			_ = os.Setenv(k, v)
		}
	}
}
