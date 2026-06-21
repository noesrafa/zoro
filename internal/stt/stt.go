// Package stt transcribes audio to text using whisper.cpp (+ ffmpeg for decode).
package stt

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Config struct {
	WhisperBin   string
	WhisperModel string
	Lang         string
	FFmpegBin    string
}

// Available reports whether STT is configured (binaries set).
func (c Config) Available() bool { return c.WhisperBin != "" && c.WhisperModel != "" }

// Transcribe converts an audio file (e.g. Telegram .oga/opus) into text.
func Transcribe(ctx context.Context, c Config, audioPath string) (string, error) {
	if !c.Available() {
		return "", fmt.Errorf("stt not configured")
	}

	wav := strings.TrimSuffix(audioPath, filepath.Ext(audioPath)) + ".stt.wav"
	ff := exec.CommandContext(ctx, c.FFmpegBin, "-y", "-i", audioPath,
		"-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", wav)
	if out, err := ff.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ffmpeg decode: %v: %s", err, lastLine(out))
	}
	defer os.Remove(wav)

	lang := c.Lang
	if lang == "" {
		lang = "auto"
	}
	// whisper.cpp: -nt no timestamps, -np no progress prints → clean text on stdout.
	cmd := exec.CommandContext(ctx, c.WhisperBin,
		"-m", c.WhisperModel, "-l", lang, "-nt", "-np", "-f", wav)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("whisper: %v: %s", err, lastLine(stderr.Bytes()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func lastLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(s)
}
