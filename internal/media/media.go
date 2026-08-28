// Package media bridges Telegram attachments and Claude: it ingests incoming
// photos/PDFs/voice (downloading + transcribing) and detects outgoing files
// the agent writes to the outbox so they can be delivered to the chat.
package media

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"zoro/internal/stt"
	"zoro/internal/tg"
	"zoro/internal/uid"
)

type Config struct {
	InboxDir     string
	OutboxDir    string
	MaxFileBytes int64
	STT          stt.Config
}

// Incoming is the result of ingesting one message's attachments.
type Incoming struct {
	Files      []string // absolute paths to images/docs for the agent to Read
	Transcript string   // transcribed voice/audio text
	HadVoice   bool
	Notes      []string // user-facing notes (skipped files, transcription errors…)
}

// Ingest downloads and processes any attachments on m.
func Ingest(ctx context.Context, c Config, client *tg.Client, m *tg.Message) (Incoming, error) {
	var in Incoming
	if err := os.MkdirAll(c.InboxDir, 0o755); err != nil {
		return in, err
	}

	if len(m.Photo) > 0 {
		p := m.Photo[len(m.Photo)-1] // largest size
		path, note, err := c.download(ctx, client, p.FileID, p.FileSize, "jpg")
		if err != nil {
			return in, err
		}
		if note != "" {
			in.Notes = append(in.Notes, note)
		} else {
			in.Files = append(in.Files, path)
		}
	}

	if m.Document != nil {
		ext := strings.TrimPrefix(filepath.Ext(m.Document.FileName), ".")
		path, note, err := c.download(ctx, client, m.Document.FileID, m.Document.FileSize, ext)
		if err != nil {
			return in, err
		}
		if note != "" {
			in.Notes = append(in.Notes, note)
		} else {
			in.Files = append(in.Files, path)
		}
	}

	var audioPath string
	switch {
	case m.Voice != nil:
		path, note, err := c.download(ctx, client, m.Voice.FileID, m.Voice.FileSize, "oga")
		if err != nil {
			return in, err
		}
		if note != "" {
			in.Notes = append(in.Notes, note)
		} else {
			audioPath, in.HadVoice = path, true
		}
	case m.Audio != nil:
		ext := strings.TrimPrefix(filepath.Ext(m.Audio.FileName), ".")
		if ext == "" {
			ext = "mp3"
		}
		path, note, err := c.download(ctx, client, m.Audio.FileID, m.Audio.FileSize, ext)
		if err != nil {
			return in, err
		}
		if note != "" {
			in.Notes = append(in.Notes, note)
		} else {
			audioPath, in.HadVoice = path, true
		}
	}

	if audioPath != "" {
		if c.STT.Available() {
			txt, err := stt.Transcribe(ctx, c.STT, audioPath)
			if err != nil {
				in.Notes = append(in.Notes, "⚠️ couldn't transcribe audio: "+err.Error())
				in.HadVoice = false
			} else {
				in.Transcript = txt
			}
		} else {
			in.Notes = append(in.Notes, "⚠️ voice received, but speech-to-text isn't installed yet.")
			in.HadVoice = false
		}
	}

	return in, nil
}

func (c Config) download(ctx context.Context, client *tg.Client, fileID string, size int64, ext string) (path, note string, err error) {
	if c.MaxFileBytes > 0 && size > c.MaxFileBytes {
		return "", fmt.Sprintf("⚠️ skipped a file (%.1f MB) — over the %d MB Telegram limit.",
			float64(size)/1e6, c.MaxFileBytes/(1024*1024)), nil
	}
	f, err := client.GetFile(ctx, fileID)
	if err != nil {
		return "", "", err
	}
	if ext == "" {
		ext = strings.TrimPrefix(filepath.Ext(f.FilePath), ".")
	}
	if ext == "" {
		ext = "bin"
	}
	dest := filepath.Join(c.InboxDir, fmt.Sprintf("%s-%s.%s",
		time.Now().Format("20060102-150405"), uid.New()[:8], ext))
	if err := client.DownloadFile(ctx, f.FilePath, dest); err != nil {
		return "", "", err
	}
	return dest, "", nil
}

// SnapshotOutbox records the files currently in dir (name → modtime).
func SnapshotOutbox(dir string) map[string]time.Time {
	out := map[string]time.Time{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if info, err := e.Info(); err == nil {
			out[e.Name()] = info.ModTime()
		}
	}
	return out
}

// NewOutboxFiles returns absolute paths of files added or modified since before, sorted by name.
func NewOutboxFiles(dir string, before map[string]time.Time) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if prev, ok := before[e.Name()]; !ok || info.ModTime().After(prev) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	paths := make([]string, 0, len(names))
	for _, n := range names {
		paths = append(paths, filepath.Join(dir, n))
	}
	return paths
}
