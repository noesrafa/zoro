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
	Token   string
	OwnerID int64

	ClaudeBin   string
	ClaudeModel string
	Effort      string
	WorkDir     string
	DangerSkip  bool

	// Agent content lives OUTSIDE the engine, in ~/.zoro (its own git repo).
	ZoroHome string // ~/.zoro
	SoulFile string // ~/.zoro/soul.md — always injected, read fresh each turn

	StateDir  string
	InboxDir  string
	OutboxDir string

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
	}
	c.SoulFile = getenv("ZORO_SOUL_FILE", filepath.Join(c.ZoroHome, "soul.md"))

	if id := getenv("TELEGRAM_OWNER_ID", ""); id != "" {
		v, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return c, fmt.Errorf("invalid TELEGRAM_OWNER_ID %q: %w", id, err)
		}
		c.OwnerID = v
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
