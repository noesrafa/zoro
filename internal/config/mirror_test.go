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
