package collector

import (
	"testing"
	"time"

	"zoro/internal/tg"
)

const testWindow = 30 * time.Millisecond

type batch struct {
	chatID int64
	msgs   []*tg.Message
}

// newTestCollector returns a collector and a channel that receives every batch.
func newTestCollector() (*Collector, chan batch) {
	ch := make(chan batch, 16)
	c := New(testWindow, func(chatID int64, msgs []*tg.Message) {
		ch <- batch{chatID, msgs}
	})
	return c, ch
}

func msg(chatID int64, text string) *tg.Message {
	return &tg.Message{Text: text, Chat: tg.Chat{ID: chatID}}
}

// waitBatch returns the next batch or fails if none arrives in time.
func waitBatch(t *testing.T, ch chan batch) batch {
	t.Helper()
	select {
	case b := <-ch:
		return b
	case <-time.After(testWindow * 10):
		t.Fatal("timed out waiting for a batch")
		return batch{}
	}
}

// expectNoBatch fails if any batch arrives within a few windows.
func expectNoBatch(t *testing.T, ch chan batch) {
	t.Helper()
	select {
	case b := <-ch:
		t.Fatalf("expected no batch, got one with %d msg(s)", len(b.msgs))
	case <-time.After(testWindow * 4):
	}
}

func TestBurstCollapsesToOneBatch(t *testing.T) {
	c, ch := newTestCollector()
	for _, txt := range []string{"foto1", "foto2", "foto3"} {
		c.Add(msg(42, txt))
	}
	b := waitBatch(t, ch)
	if b.chatID != 42 {
		t.Fatalf("chatID = %d, want 42", b.chatID)
	}
	if len(b.msgs) != 3 {
		t.Fatalf("batch had %d msgs, want 3", len(b.msgs))
	}
	expectNoBatch(t, ch) // nothing left over
}

func TestSpacedMessagesAreSeparateBatches(t *testing.T) {
	c, ch := newTestCollector()

	c.Add(msg(1, "uno"))
	first := waitBatch(t, ch) // waiting for it guarantees the window elapsed

	c.Add(msg(1, "dos"))
	second := waitBatch(t, ch)

	if len(first.msgs) != 1 || len(second.msgs) != 1 {
		t.Fatalf("expected two singleton batches, got %d and %d", len(first.msgs), len(second.msgs))
	}
	if first.msgs[0].Text != "uno" || second.msgs[0].Text != "dos" {
		t.Fatalf("order/content wrong: %q then %q", first.msgs[0].Text, second.msgs[0].Text)
	}
}

func TestChatsDoNotMix(t *testing.T) {
	c, ch := newTestCollector()
	c.Add(msg(1, "a1"))
	c.Add(msg(2, "b1"))
	c.Add(msg(1, "a2"))

	got := map[int64]int{}
	for i := 0; i < 2; i++ {
		b := waitBatch(t, ch)
		got[b.chatID] = len(b.msgs)
	}
	if got[1] != 2 {
		t.Fatalf("chat 1 batch = %d msgs, want 2", got[1])
	}
	if got[2] != 1 {
		t.Fatalf("chat 2 batch = %d msgs, want 1", got[2])
	}
}

func TestDropDiscardsPending(t *testing.T) {
	c, ch := newTestCollector()
	c.Add(msg(7, "x"))
	c.Add(msg(7, "y"))

	if n := c.Drop(7); n != 2 {
		t.Fatalf("Drop returned %d, want 2", n)
	}
	expectNoBatch(t, ch)

	if n := c.Drop(7); n != 0 {
		t.Fatalf("second Drop returned %d, want 0", n)
	}
}

func TestStopPreventsBuffering(t *testing.T) {
	c, ch := newTestCollector()
	c.Stop()
	c.Add(msg(1, "ignored"))
	expectNoBatch(t, ch)
}
