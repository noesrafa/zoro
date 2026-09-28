#!/usr/bin/env bash
# actualizar-agentes.sh — actualización + reinicio nocturno de TODOS los agentes (pedido de rafiña,
# 28-sep-2026: "cada día, en la limpieza, reinicia a todos y actualízalos para que siempre corran el
# último Zoro"). Lo corre zoro-actualizar.timer como root a las 01:40 CDMX — después de los cierres
# del día (cierre-del-dia de zoro y sky, 01:00 ±5 min); tequila no tiene.
#
#   1. Preflight como rafael: go test ./... en ~/zoro y bin/zoro existe y es ejecutable.
#      Si algo falla: no copia nada, no reinicia nada, lo anota y despierta a Zoro.
#   2. sky / tequila: si su engine/zoro difiere de ~/zoro/bin/zoro → respaldo engine/zoro.bak-YYYYMMDD
#      (se quedan los 3 más nuevos) + install -o <user> -g <user> -m 755.
#   3. Reinicia sólo las units HABILITADAS (dominio está deshabilitado y así se queda).
#   4. Salud: systemctl is-active + "zoro online" en el journal desde el reinicio (20 s, reintenta
#      hasta 60 s). Si un sub-agente no levanta: restaura su respaldo, reinicia, despierta a Zoro.
#   5. Una línea por paso en state/actualizar.log.
#
# Las banderas de pausa (<state>/paused) NO se tocan: sobreviven el reinicio y el engine arranca pausado.
#
#   actualizar-agentes.sh            → de verdad (root)
#   actualizar-agentes.sh --dry-run  → imprime lo que copiaría/reiniciaría; no toca nada
set -uo pipefail

# ACTUALIZAR_CLI=0  # NO actualizar el Claude CLI aquí, a propósito: todos los agentes comparten el mismo
#                   # CLI, así que una versión mala los tumbaría a todos a la vez a las 01:40 sin nadie
#                   # despierto. Si algún día se quiere, que sea un paso aparte con su propio health check.

ENGINE=/home/rafael/zoro
BIN=$ENGINE/bin/zoro
LOG=$ENGINE/state/actualizar.log
GO=/usr/local/go/bin/go
AGENTES="sky tequila"            # unix user == unit == /home/<user>/engine/zoro
HOY=$(date +%Y%m%d)
DESPERTAR=/home/rafael/.zoro/despertar.sh

DRY=0
[ "${1:-}" = "--dry-run" ] && DRY=1
if [ $DRY = 0 ] && [ "$(id -u)" != 0 ]; then
  echo "correr como root (o --dry-run)" >&2
  exit 1
fi

log() {
  if [ $DRY = 1 ]; then echo "$*"; return; fi
  echo "$*" >>"$LOG"
  chown rafael:rafael "$LOG" 2>/dev/null
}
hacer() {  # ejecuta, o en dry-run sólo lo describe
  if [ $DRY = 1 ]; then echo "  [dry-run] $*"; return 0; fi
  "$@"
}
como_rafael() {
  if [ "$(id -u)" = 0 ]; then runuser -u rafael -- env HOME=/home/rafael "$@"; else "$@"; fi
}
despertar() {  # $1 = qué falló
  log "  → despierto a Zoro: $1"
  hacer sudo -u rafael "$DESPERTAR" "Actualización nocturna de agentes: $1 — log: $LOG" 6 >/dev/null
}
banderas() {
  local out="" u d
  for u in zoro $AGENTES; do
    d=/home/$u/engine/state; [ $u = zoro ] && d=$ENGINE/state
    [ -e "$d/paused" ] && out="$out $u"
  done
  out=${out# }
  echo "${out:-ninguna}"
}

log "== $(TZ=America/Mexico_City date '+%Y-%m-%d %H:%M') CDMX$([ $DRY = 1 ] && echo ' (dry-run)') =="

# 1. Preflight
if ! salida=$(como_rafael bash -c "cd $ENGINE && $GO test -count=1 ./... 2>&1"); then
  log "preflight: go test FALLÓ — no copio ni reinicio nada"
  log "$(echo "$salida" | grep -E '^(FAIL|---)' | head -5)"
  despertar "go test falló en ~/zoro, no se actualizó ni reinició nadie"
  exit 1
fi
if [ ! -x "$BIN" ]; then
  log "preflight: $BIN no existe o no es ejecutable — no copio ni reinicio nada"
  despertar "falta bin/zoro"
  exit 1
fi
aviso=""
[ "$(stat -c %Y "$BIN")" -lt "$(git -C "$ENGINE" log -1 --format=%ct 2>/dev/null || echo 0)" ] &&
  aviso=" · ⚠️ bin/zoro es más viejo que el último commit (falta /redeploy)"
log "preflight: go test ok · bin/zoro $(date -r "$BIN" '+%Y-%m-%d %H:%M')$aviso"
log "pausa antes: $(banderas)"

# 2. Copiar el binario a los sub-agentes que lo tengan distinto
declare -A RESPALDO=()
for u in $AGENTES; do
  dst=/home/$u/engine/zoro
  if ! systemctl is-enabled --quiet "$u" 2>/dev/null; then
    log "$u: unit no habilitada — la dejo como está"
    continue
  fi
  if cmp -s "$BIN" "$dst"; then
    log "$u: ya corre el último binario"
    continue
  fi
  bak=$dst.bak-$HOY
  hacer cp -p "$dst" "$bak" &&
    hacer install -o "$u" -g "$u" -m 755 "$BIN" "$dst" || {
      log "$u: no pude copiar el binario"
      despertar "no pude copiar el binario a $u"
      continue
    }
  RESPALDO[$u]=$bak
  log "$u: $([ $DRY = 1 ] && echo actualizaría || echo actualizado) (respaldo $(basename "$bak"))"
  # quedan los 3 respaldos más nuevos
  for viejo in $(ls -1 "$dst".bak-* 2>/dev/null | sort -r | tail -n +4); do
    hacer rm -f "$viejo"
  done
done

# 3. Reiniciar las units habilitadas (sub-agentes primero, Zoro al final)
UNITS=()
for u in $AGENTES zoro; do
  systemctl is-enabled --quiet "$u" 2>/dev/null && UNITS+=("$u")
done
# Transición (28-sep-2026): un engine de ANTES de la pausa manda "💤 deteniéndose" a su DUEÑO al
# apagarse — a Lú o a Ángel a las 01:40. El de ahora lo manda al espejo (rafiña). Si el proceso que
# corre es de antes (su binario no conoce ZORO_PAUSE_REPLY), se espera a que esté ocioso (el único
# proceso del cgroup es el engine: ningún claude/whisper en curso) y se apaga con SIGKILL, sin
# despedida; se borra su state/alive para que el nuevo no avise "revivió". Tras la primera noche ya
# no aplica: todos corren el binario nuevo.
viejo_ocioso() {  # $1 unit → 0 si corre un binario viejo y está ocioso (espera hasta 10 min)
  local pid i
  pid=$(systemctl show -p MainPID --value "$1")
  [ "${pid:-0}" != 0 ] || return 1
  grep -qa ZORO_PAUSE_REPLY "/proc/$pid/exe" 2>/dev/null && return 1
  for i in $(seq 60); do
    [ "$(wc -l <"/sys/fs/cgroup/system.slice/$1.service/cgroup.procs")" = 1 ] && return 0
    [ $DRY = 1 ] && return 1
    sleep 10
  done
  return 1
}
desde=$(date '+%Y-%m-%d %H:%M:%S')
for u in "${UNITS[@]}"; do
  if [ "$u" != zoro ] && viejo_ocioso "$u"; then
    log "$u: engine viejo y ocioso — lo apago sin despedida (no le escribe al dueño)"
    hacer systemctl kill -s KILL "$u"
    hacer rm -f "/home/$u/engine/state/alive"
  fi
  # reinicio planeado: el engine se salta los avisos "deteniéndose"/"online" (lo borra al arrancar)
  d=/home/$u/engine/state; [ "$u" = zoro ] && d=$ENGINE/state
  hacer install -o "$(stat -c %U "$d")" -m 644 /dev/null "$d/reinicio-nocturno"
  hacer systemctl restart "$u" || log "$u: systemctl restart falló"
done
if [ $DRY = 1 ]; then
  log "reiniciaría: ${UNITS[*]} · salud: is-active + \"zoro online\" en el journal"
  log "pausa (se conserva): $(banderas)"
  exit 0
fi

# 4. Salud
sano() {  # $1 unit, $2 desde
  systemctl is-active --quiet "$1" && journalctl -u "$1" --since "$2" --no-pager -q 2>/dev/null | grep -q "zoro online"
}
esperar_sano() {
  sleep 20
  local i
  for i in 1 2 3 4 5 6 7 8 9; do
    sano "$1" "$2" && return 0
    sleep 5
  done
  return 1
}
estado=""
for u in "${UNITS[@]}"; do
  if esperar_sano "$u" "$desde"; then
    estado="$estado $u ok ·"
    continue
  fi
  if [ -n "${RESPALDO[$u]:-}" ]; then
    log "$u: no levantó con el binario nuevo — restauro $(basename "${RESPALDO[$u]}")"
    install -o "$u" -g "$u" -m 755 "${RESPALDO[$u]}" "/home/$u/engine/zoro"
    otra=$(date '+%Y-%m-%d %H:%M:%S')
    systemctl restart "$u"
    if esperar_sano "$u" "$otra"; then
      estado="$estado $u RESTAURADO ·"
      despertar "$u no levantó con el binario nuevo; se restauró el respaldo y ya corre"
    else
      estado="$estado $u CAÍDO ·"
      despertar "$u no levanta ni con el respaldo"
    fi
  else
    estado="$estado $u CAÍDO ·"
    despertar "$u no levantó después del reinicio"
  fi
done
log "reinicio:${estado%·}"
log "pausa después: $(banderas)"
