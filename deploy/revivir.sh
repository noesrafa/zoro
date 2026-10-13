#!/usr/bin/env bash
# /revivir — re-login de Claude DESDE EL CEL para un agente (zoro=rafael, sky, tequila; dominio apagado 25-sep).
# Cada login dura ~30 días; al vencer, el agente muere y sólo revive con un login de navegador
# (23-sep-2026: Sky murió así). El engine de Zoro llama esto SIN pasar por Claude, así que sirve
# aunque el muerto sea el propio Zoro.
#   revivir.sh inicio <agente>          → abre `claude` → /login en tmux; imprime  URL=<link>
#   revivir.sh codigo <agente> <código> → pega el código; imprime  OK <resumen>  |  ERROR <motivo>
#   revivir.sh estado                   → días que le quedan al login de cada agente + el token global
#   revivir.sh token-inicio             → `claude setup-token` en tmux (como rafael); imprime  URL=<link>
#   revivir.sh token-codigo <código>    → pega el código, guarda el token de 1 año en ~/.secrets/claude.token
#                                          (600) SÓLO si una llamada real con él responde; imprime OK|ERROR
# Desde el 11-oct-2026 (un solo usuario) los sub-agentes en ~/agentes/<n> usan el token global;
# el login por agente queda para Zoro y para los que todavía no se mudan.
set -uo pipefail

TOKEN=${REVIVIR_TOKEN_FILE:-/home/rafael/.secrets/claude.token}     # (env sólo para pruebas)

usuario() { case "$1" in zoro) echo rafael;; sky) echo sky;; tequila) echo tequila;; rinzler) echo rinzler;; dominio) echo dominio;; *) return 1;; esac; }
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
import json,time,datetime,os
T='/home/rafael/.secrets/claude.token'
if os.path.exists(T) and os.path.getsize(T)>0:
    m=os.path.getmtime(T); d=(m+365*86400-time.time())/86400
    print(f"🔑 token global: creado {datetime.datetime.fromtimestamp(m).strftime('%d-%b-%Y')} · vence en ~{d:.0f} días")
else:
    print("⚪ token global: no existe todavía (/revivir lo crea)")
for ag in ['zoro','sky','tequila','rinzler']:
    nuevo=f'/home/rafael/agentes/{ag}'
    if ag!='zoro' and os.path.exists(nuevo+'/agente.env'):
        cred=nuevo+'/claude/.credentials.json'
        print(f"🔑 {ag}: usa el token global"+(" (le queda un login viejo en su claude/)" if os.path.exists(cred) else ""))
        continue
    u={'zoro':'rafael'}.get(ag,ag)
    try:
        o=json.load(open(f'/home/{u}/.claude/.credentials.json')).get('claudeAiOauth',{})
        r=o.get('refreshTokenExpiresAt',0)/1000
        if not r or not o.get('refreshToken'):
            print(f"🔴 {ag}: sin login (muerto)"); continue
        d=(r-time.time())/86400
        icono='🔴' if d<=0 else ('⚠️' if d<5 else '✅')
        print(f"{icono} {ag}: {d:.0f} días (vence {datetime.datetime.fromtimestamp(r).strftime('%d-%b')})")
    except FileNotFoundError:
        if ag!='zoro' and not os.path.isdir(f'/home/{u}'):
            continue   # usuario ya borrado
        print(f"❓ {ag}: no pude leer su login (FileNotFoundError)")
    except Exception as e:
        print(f"❓ {ag}: no pude leer su login ({e.__class__.__name__})")
PY
}

# --- token global (claude setup-token) — corre como rafael, sin sudo -------------------------------
TS=${REVIVIR_TS:-revivir-token}
TCFG=${REVIVIR_TCFG:-/home/rafael/.cache/zoro-setup-token}     # config dir aparte: setup-token no toca ~/.claude de Zoro
TCLAUDE=${TCLAUDE:-/home/rafael/.local/bin/claude}
tpantalla() { tmux capture-pane -t "$TS" -p -J 2>/dev/null; }
tesperar() { local fin=$((SECONDS + $2)); while [ $SECONDS -lt $fin ]; do tpantalla | grep -qE "$1" && return 0; sleep 1; done; return 1; }
tcola() { tpantalla | grep -v '^\s*$' | grep -v 'sk-ant-' | tail -6 | cut -c1-160; }
token_inicio() {
  tmux kill-session -t "$TS" 2>/dev/null
  mkdir -p "$TCFG" && chmod 700 "$TCFG"
  [ -f "$TCFG/.claude.json" ] || echo '{"hasCompletedOnboarding": true}' > "$TCFG/.claude.json"
  tmux new-session -d -s "$TS" -x 250 -y 60 \
    "env -u ANTHROPIC_API_KEY -u CLAUDE_CODE_OAUTH_TOKEN CLAUDE_CONFIG_DIR=$TCFG $TCLAUDE setup-token" \
    || { echo "ERROR no pude abrir tmux"; exit 1; }
  # 13-oct: además de leer la pantalla, se guarda la salida CRUDA (por si la pantalla recorta/redibuja el token) y se deja
  # que «c to copy» (OSC 52) llegue al portapapeles de tmux. Todo en $TCFG (700) y se borra al terminar.
  : > "$TCFG/crudo.log"; chmod 600 "$TCFG/crudo.log"
  tmux pipe-pane -t "$TS" -o "cat >> '$TCFG/crudo.log'" 2>/dev/null
  tmux set -s set-clipboard on 2>/dev/null; tmux set -g allow-passthrough on 2>/dev/null
  tesperar 'oauth/authorize' 30 || { echo "ERROR no salió el link: $(tcola)"; tmux kill-session -t "$TS"; exit 1; }
  sleep 1
  url=$(tpantalla | tr -d '\n' | grep -oE 'https://claude\.com/cai/oauth/authorize[^ ]*state=[A-Za-z0-9_-]+' | head -1)
  [ -n "$url" ] || { echo "ERROR no pude leer el link"; tmux kill-session -t "$TS"; exit 1; }
  echo "URL=$url"
}
# probar_token <archivo> → 0 si una llamada real con ese token contesta «ok» (config dir vacío: sin login)
probar_token() {
  local cfg r
  cfg=$(mktemp -d /tmp/zoro-tok-XXXX)
  echo '{"hasCompletedOnboarding": true}' > "$cfg/.claude.json"
  r=$(cd /tmp && env -u ANTHROPIC_API_KEY CLAUDE_CONFIG_DIR="$cfg" CLAUDE_CODE_OAUTH_TOKEN="$(cat "$1")" \
    timeout 90 "$TCLAUDE" -p "Responde exactamente: ok" --strict-mcp-config 2>&1 | tail -1)
  rm -rf "$cfg"
  [ "$(echo "$r" | tr -d '[:space:][:punct:]' | tr 'A-Z' 'a-z')" = "ok" ] || { echo "$r" | grep -v 'sk-ant-' | cut -c1-200; return 1; }
}
# extraer_token: lee la pantalla por stdin e imprime el token (pegando las líneas que lo continúan si se partió)
extraer_token() {
  # Lee texto por stdin (pantalla, bitácora cruda o portapapeles) y imprime el token más largo que encuentre.
  # Quita escapes ANSI/OSC y bordes de caja; pega continuaciones si el token se partió en varias líneas.
  python3 -c '
import re,sys
x=sys.stdin.buffer.read().decode("utf-8","replace")
x=re.sub(r"\x1b\][^\x07\x1b]*(\x07|\x1b\\\\)","\n",x)        # OSC (títulos, hipervínculos, portapapeles)
x=re.sub(r"\x1b\[[0-9;]*[CG]"," ",x)                                  # saltos de cursor (Ink los usa en vez de espacios) = espacio
x=re.sub(r"\x1b\[[0-9;?]*[ -/]*[@-~]","",x)                          # resto de CSI (colores, borrar línea…)
x=re.sub(r"\x1b[@-_]","",x); x=re.sub(r"\r+\n","\n",x).replace("\r","\n")
CAJA=re.compile(r"[\s\u2500-\u257f|]")
ls=x.splitlines(); cands=set()
for i,l in enumerate(ls):
    for m in re.finditer(r"sk-ant-oat\d+-[A-Za-z0-9_-]+",l):
        t=m.group(0); j=i+1; cands.add(t)
        if CAJA.sub("",l[m.end():])=="":
            while j<len(ls):
                c=re.sub(r"^[\s\u2500-\u257f|]+|[\s\u2500-\u257f|]+$","",ls[j])   # sólo bordes/espacios de las orillas
                if not re.fullmatch(r"[A-Za-z0-9_-]+",c or "."): break           # texto con espacios internos = ya no es token
                t+=c; j+=1; cands.add(t)
        cands.add(m.group(0))
for t in sorted(cands,key=len,reverse=True):
    if len(t)>=80: print(t)'
}
# diagnostico: guarda la FORMA de la pantalla con todo lo que parezca secreto tachado (para depurar sin ver el token)
diagnostico() {
  local f=/home/rafael/.cache/zoro-revivir-diag.txt
  { echo "== pantalla $(date)"; tpantalla; echo "== cola cruda (sin escapes)"; tail -c 6000 "$TCFG/crudo.log" 2>/dev/null | python3 -c '
import re,sys; x=sys.stdin.buffer.read().decode("utf-8","replace"); print(re.sub(r"\x1b","<ESC>",x))'; } \
    | python3 -c 'import re,sys; print(re.sub(r"[A-Za-z0-9_-]{20,}", lambda m: "<X%d>"%len(m.group(0)), sys.stdin.read()))' > "$f"
  chmod 600 "$f"
}
token_codigo() {
  local code=$1 tok err
  [[ "$code" =~ ^[A-Za-z0-9_-]+#[A-Za-z0-9_-]+$ ]] || { echo "ERROR eso no parece el código (debe verse como xxxx#yyyy)"; exit 2; }
  tmux has-session -t "$TS" 2>/dev/null || { echo "ERROR no hay un setup-token esperando; manda /revivir otra vez"; exit 1; }
  tmux send-keys -t "$TS" -l "$code"; sleep 0.5; tmux send-keys -t "$TS" Enter
  tesperar 'sk-ant-oat[0-9]+-' 45 || { echo "ERROR no salió el token: $(tcola)"; tmux kill-session -t "$TS"; exit 1; }
  sleep 2
  C="$TCFG/candidatos"; ( umask 077; : > "$C" )
  tpantalla | extraer_token >> "$C"; extraer_token < "$TCFG/crudo.log" >> "$C"
  if [ ! -s "$C" ]; then
    tmux send-keys -t "$TS" c; sleep 1.5                          # «c to copy» → portapapeles de tmux (OSC 52)
    tmux show-buffer 2>/dev/null | extraer_token >> "$C"; tmux delete-buffer 2>/dev/null
  fi
  [ -s "$C" ] || diagnostico
  tmux kill-session -t "$TS" 2>/dev/null     # se va el scrollback con el token
  rm -f "$TCFG/crudo.log"
  [ -s "$C" ] || { echo "ERROR no pude leer el token de la pantalla (dejé un diagnóstico sin secretos para Zoro)"; exit 1; }
  mkdir -p "$(dirname "$TOKEN")" && chmod 700 "$(dirname "$TOKEN")"
  ok=""; err=""
  while IFS= read -r tok; do                 # del más largo al más corto, sin repetir; el primero que Claude acepte
    ( umask 077; printf '%s\n' "$tok" > "$TOKEN.new" )
    if err=$(probar_token "$TOKEN.new"); then ok=1; break; fi
  done < <(awk '!v[$0]++' "$C" | awk '{print length($0), $0}' | sort -rn | cut -d' ' -f2-)
  tok=""; rm -f "$C"
  [ -n "$ok" ] || { rm -f "$TOKEN.new"; echo "ERROR el token se leyó pero ninguna lectura pasó la prueba: $err"; exit 1; }
  [ -s "$TOKEN" ] && cp -p "$TOKEN" "$TOKEN.bak-$(date +%Y%m%d-%H%M%S)"
  # en el MISMO inodo (nada de mv): las unidades zoro@ lo ven por un bind de sólo lectura y un archivo nuevo no les llegaría
  ( umask 077; cat "$TOKEN.new" > "$TOKEN" ) && chmod 600 "$TOKEN" && rm -f "$TOKEN.new"
  echo "OK guardado en ~/.secrets/claude.token (…$(tail -c 5 "$TOKEN" | tr -d '\n')) · prueba: respondió «ok» · lo toman todos en su siguiente turno"
}

accion=${1:-estado}
[ "$accion" = "estado" ] && { estado; exit 0; }
[ "$accion" = "token-inicio" ] && { token_inicio; exit 0; }
[ "$accion" = "token-codigo" ] && { token_codigo "${2:-}"; exit 0; }
[ "$accion" = "token-cancelar" ] && { tmux kill-session -t "$TS" 2>/dev/null; echo "OK cancelado"; exit 0; }
AG=${2:-}
U=$(usuario "$AG") || { echo "ERROR agente desconocido: '$AG' (usa zoro, sky, tequila o rinzler)"; exit 2; }
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
