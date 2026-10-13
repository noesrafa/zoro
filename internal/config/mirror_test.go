package config

import (
	"fmt"
	"testing"
)

func TestGetint64List(t *testing.T) {
	cases := map[string][]int64{
		"7197485955, 7395326131": {7197485955, 7395326131},
		"7395326131":             {7395326131},
		"":                       nil,
		"abc, 5,,0":              {5},
	}
	for in, want := range cases {
		t.Setenv("ZORO_MIRROR_CHAT_ID", in)
		if got := getint64list("ZORO_MIRROR_CHAT_ID"); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("getint64list(%q) = %v, want %v", in, got, want)
		}
	}
}

// Mirror is on by default for sub-agents (13-oct-2026): every owner sees the
// others' turns. Zoro never mirrors by default, "off" disables it, and an
// explicit list still wins.
func TestMirrorDefaults(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "x")
	t.Setenv("TELEGRAM_OWNER_ID", "8968836790, 7395326131")
	cases := []struct {
		agent, env string
		want       []int64
	}{
		{"rinzler", "", []int64{8968836790, 7395326131}},
		{"zoro", "", nil},
		{"rinzler", "off", nil},
		{"rinzler", "OFF ", nil},
		{"sky", "7395326131", []int64{7395326131}},
	}
	for _, tc := range cases {
		t.Setenv("ZORO_AGENT_NAME", tc.agent)
		t.Setenv("ZORO_MIRROR_CHAT_ID", tc.env)
		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(c.MirrorChatIDs) != fmt.Sprint(tc.want) {
			t.Errorf("%s/%q: MirrorChatIDs %v, want %v", tc.agent, tc.env, c.MirrorChatIDs, tc.want)
		}
		var first int64
		if len(tc.want) > 0 {
			first = tc.want[0]
		}
		if c.MirrorChatID != first {
			t.Errorf("%s/%q: MirrorChatID %d, want %d", tc.agent, tc.env, c.MirrorChatID, first)
		}
	}
}
