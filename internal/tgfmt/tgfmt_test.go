package tgfmt

import (
	"strings"
	"testing"
)

func TestToHTML(t *testing.T) {
	cases := []struct{ in, want string }{
		{"**bold**", "<b>bold</b>"},
		{"*italic*", "<i>italic</i>"},
		{"~~gone~~", "<s>gone</s>"},
		{"plain `code` here", "plain <code>code</code> here"},
		{"## Title", "<b>Title</b>"},
		{"### Deep header", "<b>Deep header</b>"},
		{"- one\n- two", "• one\n• two"},
		{"* star bullet", "• star bullet"},
		{"a < b & c > d", "a &lt; b &amp; c &gt; d"},
		{"[Anthropic](https://anthropic.com)", `<a href="https://anthropic.com">Anthropic</a>`},
	}
	for _, c := range cases {
		got := toHTML(c.in)
		if got != c.want {
			t.Errorf("toHTML(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBlockquote(t *testing.T) {
	got := toHTML("> quoted line")
	if !strings.Contains(got, "<blockquote>") || !strings.Contains(got, "quoted line") || !strings.Contains(got, "</blockquote>") {
		t.Errorf("blockquote not rendered: %q", got)
	}
}

func TestCodeBlockPreserved(t *testing.T) {
	in := "before\n```go\nx := a < b && c > d\n```\nafter"
	got := toHTML(in)
	if !strings.Contains(got, `<pre><code class="language-go">`) {
		t.Fatalf("missing pre/code language wrapper: %q", got)
	}
	if !strings.Contains(got, "x := a &lt; b &amp;&amp; c &gt; d") {
		t.Fatalf("code body not escaped correctly: %q", got)
	}
	// markdown inside an inline-code-like region must not be turned into tags
	if strings.Contains(got, "<b>") {
		t.Fatalf("unexpected emphasis applied inside code: %q", got)
	}
}

func TestNoRawMarkdownLeaks(t *testing.T) {
	got := toHTML("Here is **bold** and a `snippet` and # not-a-header inline")
	if strings.Contains(got, "**") {
		t.Errorf("raw ** leaked: %q", got)
	}
}

func TestChunkingSplitsLongText(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 500; i++ {
		sb.WriteString("line of some length here\n")
	}
	chunks := Render(sb.String(), 1000)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 1200 { // some HTML slack over the source limit
			t.Errorf("chunk %d too long: %d bytes", i, len(c))
		}
	}
}

func TestOversizedCodeBlockSplitsIntoValidFences(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("```python\n")
	for i := 0; i < 300; i++ {
		sb.WriteString("print('x')\n")
	}
	sb.WriteString("```\n")
	chunks := Render(sb.String(), 800)
	if len(chunks) < 2 {
		t.Fatalf("expected the big code block to split, got %d chunks", len(chunks))
	}
	for i, c := range chunks {
		// each chunk must be a balanced <pre> block
		if strings.Count(c, "<pre>") != strings.Count(c, "</pre>") {
			t.Errorf("chunk %d has unbalanced <pre>: %q", i, c)
		}
	}
}
