// Package collector coalesces bursts of incoming Telegram messages into single
// batches, so the bot runs one coherent turn instead of several fragmented ones.
//
// It solves two problems with the same mechanism — a short per-chat debounce:
//
//   - Albums: Telegram delivers a multi-photo album as several separate
//     messages (sharing a media_group_id) that arrive within milliseconds.
//     Buffering briefly lets the turn see all photos at once.
//   - Typed-in-pieces: when the user sends a thought across a few quick bubbles,
//     they collapse into one turn instead of N.
//
// A lone message still waits only one debounce window (a fraction of a second,
// with "typing" already shown), so latency is negligible.
package collector

import (
	"sync"
	"time"

	"zoro/internal/tg"
)

// Collector batches messages per chat and emits each batch after a quiet window.
// It is safe for concurrent use.
type Collector struct {
	window time.Duration
	emit   func(chatID int64, msgs []*tg.Message)

	mu      sync.Mutex
	buckets map[int64]*bucket
	stopped bool
}

type bucket struct {
	msgs  []*tg.Message
	timer *time.Timer
}

// New returns a Collector that flushes a chat's buffered messages to emit once
// no new message has arrived for window. emit is called from a timer goroutine,
// never while holding the Collector's lock.
func New(window time.Duration, emit func(chatID int64, msgs []*tg.Message)) *Collector {
	return &Collector{
		window:  window,
		emit:    emit,
		buckets: make(map[int64]*bucket),
	}
}

// Add buffers m for its chat and (re)arms that chat's flush timer. Every new
// message extends the window, so a burst flushes only after it settles.
func (c *Collector) Add(m *tg.Message) {
	chatID := m.Chat.ID
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return
	}
	b := c.buckets[chatID]
	if b == nil {
		b = &bucket{}
		b.timer = time.AfterFunc(c.window, func() { c.flush(chatID) })
		c.buckets[chatID] = b
	} else {
		b.timer.Reset(c.window)
	}
	b.msgs = append(b.msgs, m)
}

// flush emits and clears a chat's bucket. Called from the timer goroutine.
func (c *Collector) flush(chatID int64) {
	c.mu.Lock()
	b := c.buckets[chatID]
	if b == nil {
		c.mu.Unlock()
		return
	}
	delete(c.buckets, chatID)
	msgs := b.msgs
	c.mu.Unlock()

	if len(msgs) > 0 {
		c.emit(chatID, msgs)
	}
}

// Drop discards any buffered (not-yet-emitted) messages for a chat and returns
// how many were dropped. Used by /cancel so pending input doesn't run after the
// user aborts.
func (c *Collector) Drop(chatID int64) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.buckets[chatID]
	if b == nil {
		return 0
	}
	b.timer.Stop()
	delete(c.buckets, chatID)
	return len(b.msgs)
}

// Stop halts all pending timers and prevents further buffering. In-flight emits
// already scheduled may still fire; callers drain those through the normal path.
func (c *Collector) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopped = true
	for id, b := range c.buckets {
		b.timer.Stop()
		delete(c.buckets, id)
	}
}
