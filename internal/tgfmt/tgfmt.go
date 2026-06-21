// Package tgfmt converts Claude's GitHub-flavored markdown into Telegram-HTML
// message chunks. Telegram's HTML parse_mode supports a small tag subset
// (b/i/u/s/code/pre/a/blockquote/tg-spoiler) and counts the 4096-char limit
// AFTER entity parsing, so tags don't count toward length.
package tgfmt

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// DefaultLimit is a safe per-message character budget (source chars; the visible
// text after stripping markdown is always shorter).
const DefaultLimit = 3800

var (
	reFence    = regexp.MustCompile("(?s)```([a-zA-Z0-9_+-]*)\\n?(.*?)```")
	reInline   = regexp.MustCompile("`([^`\\n]+)`")
	reBold1    = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reBold2    = regexp.MustCompile(`__(.+?)__`)
	reItalic   = regexp.MustCompile(`\*([^*\n]+?)\*`)
	reStrike   = regexp.MustCompile(`~~(.+?)~~`)
	reLink     = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	reHeader   = regexp.MustCompile(`^\s{0,3}#{1,6}\s+(.*)$`)
	reBullet   = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	reHr       = regexp.MustCompile(`^\s*[-*_]([ \t]*[-*_]){2,}\s*$`)
	reQuote    = regexp.MustCompile(`^\s*&gt;\s?(.*)$`) // '>' is already HTML-escaped by this point
	reTrailWS  = regexp.MustCompile(`[ \t]+$`)
)

// Render converts md into one or more Telegram-HTML strings, each <= limit
// source chars and individually valid (balanced tags). Pass DefaultLimit for limit.
func Render(md string, limit int) []string {
	if limit <= 0 {
		limit = DefaultLimit
	}
	var out []string
	for _, chunk := range chunkMarkdown(md, limit) {
		h := toHTML(chunk)
		if strings.TrimSpace(h) != "" {
			out = append(out, h)
		}
	}
	return out
}

// toHTML converts a single markdown chunk to Telegram HTML.
func toHTML(md string) string {
	md = strings.ReplaceAll(md, "\r\n", "\n")

	var codes []string
	stash := func(html string) string {
		codes = append(codes, html)
		return fmt.Sprintf("\x00%d\x00", len(codes)-1)
	}

	// 1. Fenced code blocks -> <pre>[<code class=...>]...</pre>
	md = reFence.ReplaceAllStringFunc(md, func(m string) string {
		sm := reFence.FindStringSubmatch(m)
		lang, body := sm[1], sm[2]
		body = strings.TrimRight(body, "\n")
		if lang != "" {
			return stash("<pre><code class=\"language-" + lang + "\">" + esc(body) + "</code></pre>")
		}
		return stash("<pre>" + esc(body) + "</pre>")
	})

	// 2. Inline code -> <code>...</code>
	md = reInline.ReplaceAllStringFunc(md, func(m string) string {
		sm := reInline.FindStringSubmatch(m)
		return stash("<code>" + esc(sm[1]) + "</code>")
	})

	// 3. Escape the remaining text.
	md = esc(md)

	// 4. Block-level, line by line.
	lines := strings.Split(md, "\n")
	var b strings.Builder
	inQuote := false
	closeQuote := func() {
		if inQuote {
			b.WriteString("</blockquote>\n")
			inQuote = false
		}
	}
	for _, ln := range lines {
		ln = reTrailWS.ReplaceAllString(ln, "")
		switch {
		case reHr.MatchString(ln):
			closeQuote()
			b.WriteString("──────\n")
		case reHeader.MatchString(ln):
			closeQuote()
			b.WriteString("<b>" + strings.TrimSpace(reHeader.FindStringSubmatch(ln)[1]) + "</b>\n")
		case reQuote.MatchString(ln):
			if !inQuote {
				b.WriteString("<blockquote>")
				inQuote = true
			}
			b.WriteString(reQuote.FindStringSubmatch(ln)[1] + "\n")
		case reBullet.MatchString(ln):
			closeQuote()
			sm := reBullet.FindStringSubmatch(ln)
			b.WriteString(sm[1] + "• " + sm[2] + "\n")
		default:
			closeQuote()
			b.WriteString(ln + "\n")
		}
	}
	closeQuote()
	text := b.String()

	// 5. Inline emphasis + links (order matters: bold before italic).
	text = reBold1.ReplaceAllString(text, "<b>$1</b>")
	text = reBold2.ReplaceAllString(text, "<b>$1</b>")
	text = reStrike.ReplaceAllString(text, "<s>$1</s>")
	text = reItalic.ReplaceAllString(text, "<i>$1</i>")
	text = reLink.ReplaceAllStringFunc(text, func(m string) string {
		sm := reLink.FindStringSubmatch(m)
		href := strings.ReplaceAll(sm[2], `"`, "&quot;")
		return "<a href=\"" + href + "\">" + sm[1] + "</a>"
	})

	// 6. Restore code placeholders.
	for i, c := range codes {
		text = strings.ReplaceAll(text, fmt.Sprintf("\x00%d\x00", i), c)
	}
	return strings.TrimRight(text, "\n")
}

// esc escapes the three characters Telegram HTML requires.
func esc(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// chunkMarkdown splits md into pieces <= limit chars, keeping fenced code blocks
// atomic (splitting an oversized block across multiple fences) and breaking
// elsewhere on line boundaries.
func chunkMarkdown(md string, limit int) []string {
	md = strings.ReplaceAll(md, "\r\n", "\n")
	lines := strings.Split(md, "\n")

	var chunks []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			chunks = append(chunks, strings.TrimRight(cur.String(), "\n"))
			cur.Reset()
		}
	}
	add := func(s string) {
		if cur.Len()+len(s)+1 > limit {
			flush()
		}
		cur.WriteString(s)
		cur.WriteString("\n")
	}

	for i := 0; i < len(lines); {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
			start := i
			i++
			for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
				i++
			}
			if i < len(lines) {
				i++ // include closing fence
			}
			block := strings.Join(lines[start:i], "\n")
			if len(block) <= limit {
				add(block)
			} else {
				flush()
				for _, part := range splitCodeBlock(lines[start:i], limit) {
					chunks = append(chunks, part)
				}
			}
			continue
		}
		ln := lines[i]
		i++
		if len(ln) > limit {
			flush()
			for _, p := range hardSplit(ln, limit) {
				chunks = append(chunks, p)
			}
			continue
		}
		add(ln)
	}
	flush()
	if len(chunks) == 0 {
		return []string{""}
	}
	return chunks
}

// splitCodeBlock breaks an oversized fenced block (incl. its fences) into
// multiple self-closed fenced blocks, each <= limit.
func splitCodeBlock(blockLines []string, limit int) []string {
	fence := strings.TrimSpace(blockLines[0]) // ```lang
	inner := blockLines[1:]
	if len(inner) > 0 && strings.HasPrefix(strings.TrimSpace(inner[len(inner)-1]), "```") {
		inner = inner[:len(inner)-1]
	}
	overhead := len(fence) + len("\n```") + 2
	budget := limit - overhead
	if budget < 64 {
		budget = 64
	}

	var out []string
	var buf strings.Builder
	flush := func() {
		if buf.Len() > 0 {
			out = append(out, fence+"\n"+strings.TrimRight(buf.String(), "\n")+"\n```")
			buf.Reset()
		}
	}
	for _, ln := range inner {
		for len(ln) > budget { // a single very long line
			out = append(out, fence+"\n"+ln[:budget]+"\n```")
			ln = ln[budget:]
		}
		if buf.Len()+len(ln)+1 > budget {
			flush()
		}
		buf.WriteString(ln)
		buf.WriteString("\n")
	}
	flush()
	return out
}

// hardSplit breaks a long line at rune boundaries.
func hardSplit(s string, limit int) []string {
	var out []string
	for len(s) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if cut == 0 {
			cut = limit
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}
