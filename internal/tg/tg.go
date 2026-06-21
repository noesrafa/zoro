// Package tg is a tiny, dependency-free Telegram Bot API client covering only
// what zoro needs: long polling, sending text/media, and downloading files.
package tg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Chat actions.
const (
	ActionTyping      = "typing"
	ActionRecordVoice = "record_voice"
	ActionUploadPhoto = "upload_photo"
)

// Client talks to the Telegram Bot API.
type Client struct {
	token   string
	api     string
	fileAPI string
	hc      *http.Client
}

// New builds a client for the given bot token. It forces IPv4 because the IPv6
// route to api.telegram.org is unreliable from this VPS (intermittent resets/timeouts
// that stall long-poll). IPv4 is consistently fast here.
func New(token string) *Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if network == "tcp" || network == "tcp6" {
				network = "tcp4"
			}
			return dialer.DialContext(ctx, network, addr)
		},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &Client{
		token:   token,
		api:     "https://api.telegram.org/bot" + token,
		fileAPI: "https://api.telegram.org/file/bot" + token,
		hc:      &http.Client{Timeout: 70 * time.Second, Transport: tr},
	}
}

// --- API object model (only the fields we use) ---

type Update struct {
	UpdateID      int      `json:"update_id"`
	Message       *Message `json:"message"`
	EditedMessage *Message `json:"edited_message"`
}

type Message struct {
	MessageID int         `json:"message_id"`
	From      *User       `json:"from"`
	Chat      Chat        `json:"chat"`
	Date      int64       `json:"date"`
	Text      string      `json:"text"`
	Caption   string      `json:"caption"`
	Photo     []PhotoSize `json:"photo"`
	Document  *Document   `json:"document"`
	Voice     *Voice      `json:"voice"`
	Audio     *Audio      `json:"audio"`
}

type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type Chat struct {
	ID int64 `json:"id"`
}

type PhotoSize struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int64  `json:"file_size"`
}

type Document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

type Voice struct {
	FileID   string `json:"file_id"`
	Duration int    `json:"duration"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

type Audio struct {
	FileID   string `json:"file_id"`
	Duration int    `json:"duration"`
	MimeType string `json:"mime_type"`
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
}

type File struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
	FilePath string `json:"file_path"`
}

type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

type apiResp struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
}

func (c *Client) call(ctx context.Context, method string, params url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.api+"/"+method, strings.NewReader(params.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var r apiResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return err
	}
	if !r.OK {
		return fmt.Errorf("telegram %s: %s (code %d)", method, r.Description, r.ErrorCode)
	}
	if out != nil && len(r.Result) > 0 {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

// GetUpdates long-polls for new updates starting at offset.
func (c *Client) GetUpdates(ctx context.Context, offset, timeoutSec int) ([]Update, error) {
	v := url.Values{}
	v.Set("offset", strconv.Itoa(offset))
	v.Set("timeout", strconv.Itoa(timeoutSec))
	v.Set("allowed_updates", `["message","edited_message"]`)
	var ups []Update
	err := c.call(ctx, "getUpdates", v, &ups)
	return ups, err
}

// SendMessage sends text. parseMode may be "" (plain), "HTML", or "MarkdownV2".
func (c *Client) SendMessage(ctx context.Context, chatID int64, text, parseMode string) error {
	v := url.Values{}
	v.Set("chat_id", strconv.FormatInt(chatID, 10))
	v.Set("text", text)
	if parseMode != "" {
		v.Set("parse_mode", parseMode)
		v.Set("link_preview_options", `{"is_disabled":true}`)
	}
	return c.call(ctx, "sendMessage", v, nil)
}

// SendChatAction shows a "typing…"/"recording…" indicator.
func (c *Client) SendChatAction(ctx context.Context, chatID int64, action string) error {
	v := url.Values{}
	v.Set("chat_id", strconv.FormatInt(chatID, 10))
	v.Set("action", action)
	return c.call(ctx, "sendChatAction", v, nil)
}

// SetMyCommands publishes the bot's slash-command menu.
func (c *Client) SetMyCommands(ctx context.Context, cmds []BotCommand) error {
	b, _ := json.Marshal(cmds)
	v := url.Values{}
	v.Set("commands", string(b))
	return c.call(ctx, "setMyCommands", v, nil)
}

// GetFile resolves a file_id to a downloadable file_path.
func (c *Client) GetFile(ctx context.Context, fileID string) (File, error) {
	v := url.Values{}
	v.Set("file_id", fileID)
	var f File
	err := c.call(ctx, "getFile", v, &f)
	return f, err
}

// DownloadFile downloads filePath (from GetFile) to dest on disk.
func (c *Client) DownloadFile(ctx context.Context, filePath, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.fileAPI+"/"+filePath, nil)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: status %d", filePath, resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

// SendDocument uploads a file as a document.
func (c *Client) SendDocument(ctx context.Context, chatID int64, path, caption string) error {
	return c.sendFile(ctx, "sendDocument", "document", chatID, path, caption)
}

// SendPhoto uploads a file as a photo.
func (c *Client) SendPhoto(ctx context.Context, chatID int64, path, caption string) error {
	return c.sendFile(ctx, "sendPhoto", "photo", chatID, path, caption)
}

// SendVoice uploads an Ogg/Opus file as a voice note.
func (c *Client) SendVoice(ctx context.Context, chatID int64, path, caption string) error {
	return c.sendFile(ctx, "sendVoice", "voice", chatID, path, caption)
}

func (c *Client) sendFile(ctx context.Context, method, field string, chatID int64, path, caption string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	if caption != "" {
		_ = w.WriteField("caption", caption)
	}
	fw, err := w.CreateFormFile(field, filepath.Base(path))
	if err != nil {
		return err
	}
	if _, err := io.Copy(fw, f); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.api+"/"+method, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var r apiResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return err
	}
	if !r.OK {
		return fmt.Errorf("telegram %s: %s (code %d)", method, r.Description, r.ErrorCode)
	}
	return nil
}
