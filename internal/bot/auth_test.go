package bot

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// refreshTokenAlive decides whether rafiña gets told to run /login. Getting it wrong
// in either direction costs him: a false "alive" hides a real dead login, and a false
// "dead" sends him to do a pointless chore. So test both, plus the garbage cases.
func TestRefreshTokenAlive(t *testing.T) {
	futuro := time.Now().Add(20 * 24 * time.Hour).UnixMilli()
	pasado := time.Now().Add(-2 * time.Hour).UnixMilli()

	casos := []struct {
		nombre    string
		contenido string
		quiero    bool
	}{
		{"refresh token vigente", `{"claudeAiOauth":{"refreshTokenExpiresAt":` + itoa(futuro) + `}}`, true},
		{"refresh token vencido", `{"claudeAiOauth":{"refreshTokenExpiresAt":` + itoa(pasado) + `}}`, false},
		{"campo ausente", `{"claudeAiOauth":{"accessToken":"x"}}`, false},
		{"expiresAt en cero", `{"claudeAiOauth":{"refreshTokenExpiresAt":0}}`, false},
		{"JSON vacio", `{}`, false},
		{"JSON truncado a media escritura", `{"claudeAiOauth":{"refreshToken`, false},
		{"archivo vacio", ``, false},
	}

	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("HOME", dir)
			if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".claude", ".credentials.json"), []byte(c.contenido), 0o600); err != nil {
				t.Fatal(err)
			}
			got, _ := refreshTokenAlive()
			if got != c.quiero {
				t.Fatalf("%s: quiero %v, obtuve %v", c.nombre, c.quiero, got)
			}
		})
	}
}

// Sin archivo no se puede afirmar que el login está sano: debe decir "no vivo",
// para que el mensaje caiga del lado seguro (mandarlo a revisar) y no del lado
// que le esconde un login muerto.
func TestRefreshTokenAliveSinArchivo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if alive, _ := refreshTokenAlive(); alive {
		t.Fatal("sin archivo debe reportar NO vivo")
	}
}

func itoa(v int64) string {
	s := ""
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		s = string(rune('0'+v%10)) + s
		v /= 10
	}
	if s == "" {
		s = "0"
	}
	if neg {
		s = "-" + s
	}
	return s
}
