// Package tts synthesizes speech with Piper and encodes Ogg/Opus for Telegram voice notes.
package tts

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"zoro/internal/uid"
)

type Config struct {
	PiperBin   string
	PiperVoice string
	FFmpegBin  string
}

// Available reports whether TTS is configured (binaries set).
func (c Config) Available() bool { return c.PiperBin != "" && c.PiperVoice != "" }

// Synthesize turns text into an Ogg/Opus file (suitable for sendVoice) in tmpDir.
// The caller owns the returned file and should delete it after sending.
func Synthesize(ctx context.Context, c Config, text, tmpDir string) (string, error) {
	if !c.Available() {
		return "", fmt.Errorf("tts not configured")
	}
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return "", err
	}

	base := filepath.Join(tmpDir, "tts-"+uid.New())
	wav := base + ".wav"
	ogg := base + ".ogg"

	p := exec.CommandContext(ctx, c.PiperBin, "--model", c.PiperVoice, "--output_file", wav)
	p.Stdin = strings.NewReader(text)
	if out, err := p.CombinedOutput(); err != nil {
		return "", fmt.Errorf("piper: %v: %s", err, lastLine(out))
	}
	defer os.Remove(wav)

	ff := exec.CommandContext(ctx, c.FFmpegBin, "-y", "-i", wav,
		"-c:a", "libopus", "-b:a", "48k", "-application", "voip", ogg)
	if out, err := ff.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ffmpeg encode: %v: %s", err, lastLine(out))
	}
	return ogg, nil
}

func lastLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(s)
}
