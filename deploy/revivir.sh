#!/usr/bin/env bash
# /revivir — re-login de Claude DESDE EL CEL para un agente (zoro=rafael, sky, tequila; dominio apagado 25-sep).
# Cada login dura ~30 días; al vencer, el agente muere y sólo revive con un login de navegador
# (23-sep-2026: Sky murió así). El engine de Zoro llama esto SIN pasar por Claude, así que sirve
# aunque el muerto sea el propio Zoro.
#   revivir.sh inicio <agente>          → abre `claude` → /login en tmux; imprime  URL=<link>
#   revivir.sh codigo <agente> <código> → pega el código; imprime  OK <resumen>  |  ERROR <motivo>
#   revivir.sh estado                   → días que le quedan al login de cada agente
set -uo pipefail

usuario() { case "$1" in zoro) echo rafael;; sky) echo sky;; tequila) echo tequila;; dominio) echo dominio;; *) return 1;; esac; }
comoU() { sudo -n -u "$U" -i "$@"; }          # login shell de ese usuario (su PATH trae su claude)
pantalla() { comoU tmux capture-pane -t "$S" -p -J 2>/dev/null; }
esperar() {  # $1=regex  $2=segundos → 0 si apareció
  local fin=$((SECONDS + $2))
  while [ $SECONDS -lt $fin ]; do pantalla | grep -qE "$1" && return 0; sleep 1; done
  return 1
}
cola() { pantalla | grep -v '^\s*$' | tail -6 | cut -c1-160; }

estado() {
  sudo -n python3 - <<'PY'
import json,time,datetime
for ag,u in [('zoro','rafael'),('sky','sky'),('tequila','tequila')]:
    try:
        o=json.load(open(f'/home/{u}/.claude/.credentials.json')).get('claudeAiOauth',{})
        r=o.get('refreshTokenExpiresAt',0)/1000
        if not r or not o.get('refreshToken'):
            print(f"🔴 {ag}: sin login (muerto)"); continue
        d=(r-time.time())/86400
        icono='🔴' if d<=0 else ('⚠️' if d<5 else '✅')
        print(f"{icono} {ag}: {d:.0f} días (vence {datetime.datetime.fromtimestamp(r).strftime('%d-%b')})")
    except Exception as e:
        print(f"❓ {ag}: no pude leer su login ({e.__class__.__name__})")
PY
}

accion=${1:-estado}
[ "$accion" = "estado" ] && { estado; exit 0; }
AG=${2:-}
U=$(usuario "$AG") || { echo "ERROR agente desconocido: '$AG' (usa zoro, sky o tequila)"; exit 2; }
S="revivir-$AG"

case "$accion" in
  inicio)
    comoU tmux kill-session -t "$S" 2>/dev/null
    sudo -n cp -p "/home/$U/.claude/.credentials.json" "/tmp/credenciales-$U.bak-$(date +%Y%m%d-%H%M%S)" 2>/dev/null
    # Agente recién nacido: sin esto `claude` abre la bienvenida (tema, etc.) que el bucle de abajo no
    # sabe contestar y se rinde a los 45 s (Tequila, 25-sep). Se marca la bienvenida como vista.
    sudo -n python3 -c "import json,os; p='/home/$U/.claude.json'; d=json.load(open(p)) if os.path.exists(p) else {}; d.setdefault('hasCompletedOnboarding', True); json.dump(d, open(p,'w'), indent=2)" \
      && sudo -n chown "$U:$U" "/home/$U/.claude.json" && sudo -n chmod 600 "/home/$U/.claude.json"
    comoU tmux new-session -d -s "$S" -x 250 -y 60 claude || { echo "ERROR no pude abrir tmux como $U"; exit 1; }
    # Diálogos de arranque que pueden salir antes del prompt: confiar en la carpeta (el cursor puede
    # estar en "No, exit"), novedades tipo "Flicker-free output… Yes, try it / Not now", etc.
    # Se contesta cada uno hasta ver el prompt normal. Pausa antes de teclear: el diálogo se pinta
    # antes de aceptar teclas y sin ella las flechas se pierden (medido 23-sep).
    listo=0
    for _ in $(seq 1 45); do
      P=$(pantalla)
      if echo "$P" | grep -q 'Enter to confirm'; then
        sleep 2; P=$(pantalla)
        if echo "$P" | grep -q 'trust this folder'; then quiero='Yes'
        elif echo "$P" | grep -q 'Not now'; then quiero='Not now'
        else comoU tmux send-keys -t "$S" Escape; sleep 1; continue
        fi
        for _ in 1 2 3 4; do
          pantalla | grep -qE "❯ *([0-9]\. )?$quiero" && break
          comoU tmux send-keys -t "$S" Down; sleep 1
        done
        pantalla | grep -qE "❯ *([0-9]\. )?$quiero" || { echo "ERROR no pude contestar un diálogo: $(cola)"; comoU tmux kill-session -t "$S"; exit 1; }
        comoU tmux send-keys -t "$S" Enter; sleep 1.5; continue
      fi
      if echo "$P" | grep -qE 'for shortcuts|Try "|/effort'; then listo=1; break; fi
      sleep 1
    done
    [ "$listo" = 1 ] || { echo "ERROR claude no quedó listo como $U: $(cola)"; comoU tmux kill-session -t "$S"; exit 1; }
    sleep 1
    comoU tmux send-keys -t "$S" -l '/login'; sleep 0.5; comoU tmux send-keys -t "$S" Enter
    esperar 'Select login method' 20 || { echo "ERROR no salió el menú de /login: $(cola)"; comoU tmux kill-session -t "$S"; exit 1; }
    comoU tmux send-keys -t "$S" Enter   # opción 1: cuenta de Claude con suscripción (Max)
    esperar 'state=' 25 || { echo "ERROR no salió el link: $(cola)"; comoU tmux kill-session -t "$S"; exit 1; }
    url=$(pantalla | tr -d '\n' | grep -oE 'https://claude\.com/cai/oauth/authorize[^ ]*state=[A-Za-z0-9_-]+' | head -1)
    [ -n "$url" ] || { echo "ERROR no pude leer el link"; comoU tmux kill-session -t "$S"; exit 1; }
    echo "URL=$url"
    ;;
  codigo)
    code=${3:-}
    [[ "$code" =~ ^[A-Za-z0-9_-]+#[A-Za-z0-9_-]+$ ]] || { echo "ERROR eso no parece el código (debe verse como xxxx#yyyy)"; exit 2; }
    comoU tmux has-session -t "$S" 2>/dev/null || { echo "ERROR no hay un login de $AG esperando; manda /revivir_$AG otra vez"; exit 1; }
    comoU tmux send-keys -t "$S" -l "$code"; sleep 0.5; comoU tmux send-keys -t "$S" Enter
    if ! esperar 'Login successful|Logged in as' 30; then
      echo "ERROR el login no se completó: $(cola)"; comoU tmux kill-session -t "$S"; exit 1
    fi
    cuenta=$(pantalla | grep -oE 'Logged in as [^ ]+' | head -1)
    comoU tmux send-keys -t "$S" Enter; sleep 1.5
    comoU tmux send-keys -t "$S" -l '/exit'; sleep 0.5; comoU tmux send-keys -t "$S" Enter; sleep 2
    comoU tmux kill-session -t "$S" 2>/dev/null
    # la prueba de verdad: una llamada real con el login nuevo
    prueba=$(sudo -n -u "$U" -i bash -c 'cd /tmp && timeout 90 claude -p "Responde exactamente: ok" 2>&1 | tail -1')
    linea=$(estado | grep " $AG:")
    if [ "$(echo "$prueba" | tr -d '[:space:][:punct:]' | tr 'A-Z' 'a-z')" = "ok" ]; then
      echo "OK ${cuenta:-login hecho} · $linea · prueba: respondió «ok»"
    else
      echo "ERROR el login se guardó pero la prueba falló: $(echo "$prueba" | cut -c1-200) · $linea"; exit 1
    fi
    ;;
  cancelar)
    comoU tmux kill-session -t "$S" 2>/dev/null; echo "OK cancelado"
    ;;
  *) echo "ERROR acción desconocida: $accion"; exit 2;;
esac
