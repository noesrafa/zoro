package bot

import (
	"testing"
	"time"
)

func TestRetryAt(t *testing.T) {
	mx, _ := time.LoadLocation("America/Mexico_City")
	since := time.Date(2026, 10, 11, 23, 30, 0, 0, mx)
	f := func(resets string) pauseFlag {
		return pauseFlag{Since: since.UTC().Format(time.RFC3339), Reason: reasonLimit, Resets: resets}
	}
	cases := []struct {
		resets string
		want   time.Time
	}{
		{"11pm (America/Mexico_City)", time.Date(2026, 10, 12, 23, 2, 0, 0, mx)},      // already past today → tomorrow
		{"3am (America/Mexico_City)", time.Date(2026, 10, 12, 3, 2, 0, 0, mx)},        // later tonight
		{"11:59pm (America/Mexico_City)", time.Date(2026, 10, 12, 0, 1, 0, 0, mx)},    // minutes
		{"Oct 13, 5pm (America/Mexico_City)", time.Date(2026, 10, 13, 17, 2, 0, 0, mx)}, // dated
		{"12am (America/Mexico_City)", time.Date(2026, 10, 12, 0, 2, 0, 0, mx)},       // midnight
		{"", since.Add(autoRetryEvery)},
		{"soon", since.Add(autoRetryEvery)},
	}
	for _, c := range cases {
		if got := retryAt(f(c.resets)); !got.Equal(c.want) {
			t.Errorf("retryAt(%q) = %v, want %v", c.resets, got.In(mx), c.want)
		}
	}
}
