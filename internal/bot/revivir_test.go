package bot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEsComandoRevivir(t *testing.T) {
	casos := []struct {
		text, ag string
		ok       bool
	}{
		{"/revivir", "", true},
		{"/revivir sky", "sky", true},
		{"/revivir_sky", "sky", true},
		{"/revivir_Zoro", "zoro", true},
		{"/revivir@zorobot dominio", "dominio", true},
		{"/revivir estado", "estado", true},
		{"/revivir token", "token", true},
		{"/revive", "", false},
		{"/redeploy", "", false},
	}
	for _, c := range casos {
		fields := strings.Fields(c.text)
		cmd := strings.SplitN(fields[0], "@", 2)[0]
		ag, ok := esComandoRevivir(cmd, c.text, fields)
		if ok != c.ok || ag != c.ag {
			t.Errorf("%q → (%q,%v), quería (%q,%v)", c.text, ag, ok, c.ag, c.ok)
		}
	}
}

func TestCodigoLogin(t *testing.T) {
	// el código real que pegó el 23-sep (ya usado) y cosas que NO deben tragarse
	bien := "DFKre0sWkN5M1sAJM7L7p0qWoAuxeGPtJAegn5Dh5V144AfW#l45zkcBnjqYok26wlctXyli7YxWAsuMmcs8tQ3lfuW0"
	if !reCodigoLogin.MatchString(bien) {
		t.Error("no reconoció un código real")
	}
	for _, mal := range []string{"hola #sky", "abc#def", "Oye sky murió? #ayuda", "https://x.com/a#b", bien + " gracias"} {
		if reCodigoLogin.MatchString(mal) {
			t.Errorf("se tragó %q como código", mal)
		}
	}
}

func TestEsperaLogin(t *testing.T) {
	var e esperaLogin
	if _, ok := e.tomar(); ok {
		t.Fatal("vacío no debe dar agente")
	}
	e.poner("sky", time.Minute)
	if !e.vigente() {
		t.Fatal("recién puesto debe estar vigente")
	}
	if ag, ok := e.tomar(); !ok || ag != "sky" {
		t.Fatalf("tomar → %q %v", ag, ok)
	}
	if _, ok := e.tomar(); ok {
		t.Fatal("un código se usa una sola vez")
	}
	e.poner("zoro", -time.Second)
	if _, ok := e.tomar(); ok {
		t.Fatal("vencido no debe servir")
	}
}

func TestUsaTokenGlobal(t *testing.T) {
	dir := t.TempDir()
	b := &Bot{}
	if b.usaTokenGlobal("sky") {
		t.Fatal("sin ZORO_AGENTS_DIR nadie usa el token global (compatibilidad)")
	}
	b.cfg.AgentsDir = dir
	if b.usaTokenGlobal("sky") {
		t.Fatal("sky todavía no se mudó: su login viejo")
	}
	if err := os.MkdirAll(filepath.Join(dir, "sky"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sky", "agente.env"), []byte("ZORO_AGENT_NAME=sky\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !b.usaTokenGlobal("sky") {
		t.Fatal("sky ya vive en ~/agentes: token global")
	}
	if b.usaTokenGlobal("zoro") {
		t.Fatal("zoro conserva su propio login")
	}
}
