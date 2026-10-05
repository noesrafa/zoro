package bot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestParseCoach(t *testing.T) {
	v, err := parseCoach("```json\n{\"major\": true, \"corrected\": \"How does it work?\", \"tips\": [\"how it works? → how does it work?\"]}\n```")
	if err != nil || !v.Major || v.Corrected != "How does it work?" || len(v.Tips) != 1 {
		t.Fatalf("fenced: %+v %v", v, err)
	}
	if v, err = parseCoach(`{"major": false, "corrected": "", "tips": []}`); err != nil || v.Major {
		t.Fatalf("plain: %+v %v", v, err)
	}
	if _, err = parseCoach("sorry, I can't"); err == nil {
		t.Fatal("garbage must fail")
	}
}

// gateBot: a test bot with the gate on and a stand-in coach that flags any
// message containing "don't will".
func gateBot(t *testing.T, coachErr error) (*testBot, *int) {
	t.Helper()
	tb := newTestBot(t, "zoro")
	if err := tb.set.SetGate(true); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	n := 0
	tb.coach = func(_ context.Context, text string) (coachVerdict, error) {
		mu.Lock()
		n++
		mu.Unlock()
		if coachErr != nil {
			return coachVerdict{}, coachErr
		}
		if strings.Contains(text, "don't will") {
			return coachVerdict{Major: true, Corrected: strings.ReplaceAll(text, "don't will", "won't"),
				Tips: []string{"don't will → won't (future negative)"}}, nil
		}
		if strings.Contains(text, "no praise") {
			return coachVerdict{}, nil
		}
		return coachVerdict{Praise: "Nice question with the auxiliary."}, nil
	}
	return tb, &n
}

// A bad sentence bounces with the fix and NO answer; the rewrite always goes
// through (one bounce per request) — checked only for praise.
func TestGrammarGateBounceThenAnswer(t *testing.T) {
	tb, n := gateBot(t, nil)
	ctx := context.Background()
	tb.process(ctx, ownerJob("I think we don't will use local models"))
	if tb.sentWith("Almost! Say it like this") != 1 || tb.sentWith("we won't use local models") != 1 {
		t.Fatalf("expected the bounce with the fix, sent=%q", tb.sent)
	}
	if tb.nCalls() != 0 {
		t.Fatalf("bounced message must not reach Claude, calls=%d", tb.nCalls())
	}
	tb.process(ctx, ownerJob("I think we don't will use local models either way"))
	if tb.nCalls() != 1 || *n != 2 || tb.sentWith("✅") != 0 {
		t.Fatalf("a still-wrong rewrite must reach Claude with no praise: calls=%d coach=%d sent=%q", tb.nCalls(), *n, tb.sent)
	}
	// The free pass is used up: the next bad sentence is bounced again.
	tb.process(ctx, ownerJob("and we don't will use the Mac"))
	if *n != 3 || tb.nCalls() != 1 || tb.sentWith("Almost!") != 2 {
		t.Fatalf("the grace must be single-use: calls=%d coach=%d", tb.nCalls(), *n)
	}
}

// Good English, short messages, "!" and a failing coach all go straight through.
func TestGrammarGatePassThrough(t *testing.T) {
	tb, n := gateBot(t, nil)
	ctx := context.Background()
	tb.process(ctx, ownerJob("can u check the vps its slow today"))
	tb.process(ctx, ownerJob("ok dale"))
	tb.process(ctx, ownerJob("! we don't will use it, just do it"))
	if tb.nCalls() != 3 || tb.sentWith("Almost!") != 0 {
		t.Fatalf("all three must be answered: calls=%d sent=%q", tb.nCalls(), tb.sent)
	}
	if *n != 1 {
		t.Fatalf("coach must run only on the 1st (short and ! skip it), ran %d", *n)
	}
	if tb.sentWith("✅ Nice question with the auxiliary.") != 1 {
		t.Fatalf("clean English must be praised once, sent=%q", tb.sent)
	}

	tb2, _ := gateBot(t, errors.New("timeout"))
	tb2.process(ctx, ownerJob("I think we don't will use local models"))
	if tb2.nCalls() != 1 || tb2.sentWith("Almost!") != 0 || tb2.sentWith("✅") != 0 {
		t.Fatalf("a failing coach must never swallow the message nor praise it: calls=%d sent=%q", tb2.nCalls(), tb2.sent)
	}
}

// A clean rewrite after a bounce is praised; a verdict with no line gets the
// plain fallback.
func TestGrammarGatePraise(t *testing.T) {
	tb, _ := gateBot(t, nil)
	ctx := context.Background()
	tb.process(ctx, ownerJob("I think we don't will use local models"))
	tb.process(ctx, ownerJob("I think we won't use local models"))
	if tb.nCalls() != 1 || tb.sentWith("✅ Nice question") != 1 {
		t.Fatalf("a clean rewrite must be answered and praised: calls=%d sent=%q", tb.nCalls(), tb.sent)
	}
	tb.process(ctx, ownerJob("this one gets no praise line"))
	if tb.nCalls() != 2 || tb.sentWith("✅ Clean English, nothing to fix.") != 1 {
		t.Fatalf("an empty praise line must use the fallback: calls=%d sent=%q", tb.nCalls(), tb.sent)
	}
}

// Gate off: the coach never runs.
func TestGrammarGateOff(t *testing.T) {
	tb, n := gateBot(t, nil)
	if err := tb.set.SetGate(false); err != nil {
		t.Fatal(err)
	}
	tb.process(context.Background(), ownerJob("I think we don't will use local models"))
	if *n != 0 || tb.nCalls() != 1 {
		t.Fatalf("gate off: coach=%d calls=%d", *n, tb.nCalls())
	}
}
