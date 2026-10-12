#!/usr/bin/env python3
"""guardia — hook PreToolUse de los sub-agentes (un solo usuario Linux, 11-oct-2026).

Todos los agentes corren como rafael, así que lo que separa a Sky de los secretos de rafiña,
de Zoro y de los otros agentes es esto (más el aislamiento de su unidad systemd). Se instala en
el settings.json del CLAUDE_CONFIG_DIR de cada agente (~/agentes/<n>/claude/settings.json):

  {"hooks": {"PreToolUse": [{"matcher": "*", "hooks": [{"type": "command",
     "command": "python3 /home/rafael/agentes/bin/guardia.py"}]}]}}

Regla: un agente sólo toca SU carpeta (~/agentes/<n>, que es su HOME) y lo que está fuera de
/home (el sistema). Nada de ~/.secrets, ~/.zoro, ~/.claude de Zoro, ~/trabajos, ~/zoro, ~/heru,
~/respaldos ni ~/agentes/<otros>; sudo sólo en la lista de su agente (guardia.json). Bloquea con
exit 2 + motivo en stderr (funciona aun con --dangerously-skip-permissions; medido 11-oct).

Es una barda contra accidentes e inyecciones ingenuas, no una caja fuerte: un comando muy
torcido puede esquivar un chequeo de texto. Por eso la unidad zoro@.service también esconde
esas rutas con InaccessiblePaths (kernel). Sin dependencias: sólo la stdlib de python3.
"""
import json
import os
import re
import shlex
import sys

BASE = "/home/rafael"
AGENTES = BASE + "/agentes"
CONFIG = os.environ.get("GUARDIA_CONFIG", AGENTES + "/bin/guardia.json")

# Fuera de /home también hay cosas que no son de un agente.
PROHIBIDO_FUERA = (
    "/root", "/etc/sudoers", "/etc/shadow", "/etc/gshadow", "/var/lib/claudeshare",
    "/etc/claude-code", "/etc/systemd", "/etc/nginx", "/var/lib/grug",
)
# /proc/<pid>/{root,cwd,environ,mem,fd,...} de OTRO proceso de rafael (Zoro) salta el aislamiento.
PROC_RE = re.compile(r"/proc/(?:[0-9]+|self|thread-self)/(?:root|cwd|environ|mem|fd|map_files|exe)\b|/proc/[0-9]+/")

# Programas que sacan al agente de su unidad (corren fuera de su aislamiento, como rafael).
ESCAPES = {
    "su", "pkexec", "doas", "systemd-run", "nsenter", "crontab", "at", "batch", "atq",
    "tmux", "screen", "machinectl", "runuser", "setpriv", "chroot", "busctl", "gdbus", "dbus-send",
}
# systemctl sin sudo: sólo verbos de lectura (con sudo, lo decide la lista del agente).
SYSTEMCTL_LECTURA = {"status", "is-active", "is-enabled", "is-failed", "show", "cat", "list-units", "list-timers"}
SEPARADORES = re.compile(r"\|\||&&|(?<![<>])&(?!>)|[;|\n()`]|\$\(")   # 2>&1 y &> no parten


def bloquear(motivo):
    sys.stderr.write("⛔ guardia (" + AGENTE + "): " + motivo +
                     ". Sólo puedes trabajar dentro de tu carpeta " + MIO +
                     "; si de verdad lo necesitas, pídeselo a rafiña.\n")
    sys.exit(2)


def agente_actual():
    n = os.environ.get("ZORO_AGENT_NAME", "").strip()
    if n and n != "zoro":
        return n
    cfg = os.path.realpath(os.environ.get("CLAUDE_CONFIG_DIR", ""))
    m = re.match(re.escape(AGENTES) + r"/([^/]+)/claude$", cfg)
    return m.group(1) if m else ""


def cargar_config():
    try:
        with open(CONFIG) as f:
            return json.load(f)
    except Exception:
        return {}


def normal(p, cwd):
    p = os.path.expanduser(p)            # HOME = la carpeta del agente: ~ es suya
    if not os.path.isabs(p):
        p = os.path.join(cwd, p)
    p = os.path.normpath(p)
    try:
        r = os.path.realpath(p)          # symlinks que apuntan afuera también cuentan
    except Exception:
        r = p
    return p, r


def dentro(p, raiz):
    return p == raiz or p.startswith(raiz.rstrip("/") + "/")


# Dentro de su carpeta hay archivos que mandan sobre él: no los edita él mismo.
def protegido_escritura(p):
    return (re.search(r"/\.?claude/settings(\.local)?\.json$", p) is not None
            or re.search(r"/\.?claude/hooks(/|$)", p) is not None
            or p == MIO + "/agente.env")


def alias_mio(q):
    # /home/<agente> queda como symlink a su carpeta tras la mudanza (rutas viejas en sus scripts)
    viejo = "/home/" + AGENTE
    return MIO + q[len(viejo):] if dentro(q, viejo) else q


def revisar_ruta(p, cwd, escribe=False, origen="esa ruta"):
    if not p:
        return
    if p == "~" + AGENTE or p.startswith("~" + AGENTE + "/"):
        p = MIO + p[len(AGENTE) + 1:]
    for q in map(alias_mio, normal(p, cwd)):
        if PROC_RE.search(q + "/"):
            bloquear(origen + " (" + q + ") es de otro proceso")
        if any(dentro(q, x) for x in PROHIBIDO_FUERA):
            bloquear(origen + " (" + q + ") es del sistema / de rafiña")
        if TOKEN_RE.search(q):
            bloquear("los tokens (Claude / bot) son del engine: no se leen")
        if q.endswith("/.credentials.json"):
            bloquear("los logins de Claude no se leen ni se tocan")
        if dentro(q, MIO):
            if escribe and protegido_escritura(q):
                bloquear(q + " es tu configuración de seguridad: no la cambias tú")
            continue
        if dentro(q, AGENTES + "/bin") or dentro(q, AGENTES + "/plantillas"):
            if escribe:
                bloquear(q + " es de Zoro (sólo lectura)")
            continue
        if q == "/home" or dentro(q, "/home"):
            if q in ("/home", BASE, AGENTES):
                if escribe:
                    bloquear(q + " no es tuya")
                continue            # listar el pasillo no revela nada
            bloquear(origen + " (" + q + ") está fuera de tu carpeta")


# El token de Claude (de todos) y el del bot: el engine los usa, el agente no los lee ni los imprime.
TOKEN_RE = re.compile(r"CLAUDE_CODE_OAUTH_TOKEN|TELEGRAM_BOT_TOKEN|zoro-claude\.token|/run/credentials|claude\.token|/environ\b")


def revisar_texto_peligroso(texto):
    if TOKEN_RE.search(texto):
        bloquear("los tokens (Claude / bot) son del engine: no se leen ni se imprimen")
    if "disableAllHooks" in texto or "allowManagedHooksOnly" in texto:
        bloquear("no se apagan los hooks")
    if re.search(r"(^|[\s'\"=:])ZORO_AGENT_NAME=|(^|[\s'\"])CLAUDE_CONFIG_DIR=|GUARDIA_CONFIG=", texto):
        bloquear("no se cambia la identidad del agente")


def revisar_sudo(trozo, permitidos):
    t = " ".join(trozo.split())
    t = re.sub(r"(\s+\d?>>?\s*&?\S+)+$", "", t)      # 2>&1, >/dev/null al final no cambian nada
    for patron in permitidos:
        if re.fullmatch(patron, t):
            return
    bloquear("sudo no permitido: «" + t[:120] + "» (tu lista: " + (", ".join(permitidos) or "ninguno") + ")")


RUTA_HOME = re.compile(r"""(?:^|(?<=[\s'"=:(`<>,]))(~[A-Za-z_][A-Za-z0-9_-]*|/?home/[^\s'"`;|&)<>,]*|/proc/[^\s'"`;|&)<>,]*|/root\b[^\s'"`;|&)<>]*|/etc/(?:sudoers|shadow|gshadow|claude-code|systemd|nginx)[^\s'"`;|&)<>]*|/var/lib/(?:claudeshare|grug)[^\s'"`;|&)<>]*)""")


def revisar_bash(cmd, cwd, permitidos):
    revisar_texto_peligroso(cmd)
    if re.search(r"\.?claude/settings(\.local)?\.json|\.?claude/hooks\b", cmd):
        bloquear("tu configuración de Claude (settings/hooks) no se toca desde Bash")
    # rutas escritas en el comando
    for m in RUTA_HOME.finditer(cmd):
        tok = m.group(1)
        if tok.startswith("~") and tok != "~" + AGENTE:
            bloquear("«" + tok + "» es la carpeta de otro usuario")
        if tok.startswith("~"):
            continue
        if tok.startswith("home/"):
            tok = "/" + tok
        revisar_ruta(tok, cwd, escribe=False, origen="el comando")
    # programa por programa, siguiendo los cd (cd .. && cd .. && cat .secrets/x)
    vcwd = cwd
    for trozo in SEPARADORES.split(cmd):
        try:
            toks = shlex.split(trozo, comments=False, posix=True)
        except ValueError:
            toks = trozo.split()
        if toks and toks[0] in ("cd", "pushd"):
            destino = next((t for t in toks[1:] if not t.startswith("-")), "~")
            nuevo = alias_mio(normal(destino, vcwd)[1])
            if dentro(nuevo, "/home") and not dentro(nuevo, MIO):
                bloquear("cd a " + nuevo + " te saca de tu carpeta")
            vcwd = nuevo
            continue
        for tok in toks:          # rutas relativas que suben (../..) desde donde estás
            for parte in re.split(r"[=:,]", tok):
                if ".." in parte and "/" in parte or parte == "..":
                    revisar_ruta(parte, vcwd, origen="«" + parte + "»")
        if vcwd != cwd:           # tras un cd, lo relativo se mide desde ahí
            for tok in toks[1:]:
                if not tok.startswith(("-", "/", "~", "$")) and "/" in tok:
                    revisar_ruta(tok, vcwd, origen="«" + tok + "»")
        palabras = trozo.strip().split()
        while palabras and re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*=\S*", palabras[0]):
            palabras = palabras[1:]            # VAR=valor cmd …
        if not palabras:
            continue
        prog = os.path.basename(palabras[0])
        if prog in ("env", "nice", "nohup", "timeout", "command", "exec", "time", "stdbuf", "ionice") and len(palabras) > 1:
            resto = [w for w in palabras[1:] if not w.startswith("-") and not re.fullmatch(r"[0-9.]+[smhd]?|[A-Za-z_]+=\S*", w)]
            if resto:
                prog = os.path.basename(resto[0])
                if prog == "sudo":
                    revisar_sudo(trozo.strip()[trozo.strip().index("sudo"):], permitidos)
                    continue
        if prog == "sudo":
            revisar_sudo(" ".join(palabras), permitidos)
            continue
        if prog in ("printenv", "export", "declare", "set", "compgen") or (prog == "env" and len(palabras) == 1):
            bloquear("«" + prog + "» imprime el entorno (trae el token de Claude)")
        if prog in ESCAPES:
            bloquear("«" + prog + "» corre fuera de tu espacio aislado")
        if prog == "systemctl":
            verbos = [w for w in palabras[1:] if not w.startswith("-")]
            if "--user" in palabras or (verbos and verbos[0] not in SYSTEMCTL_LECTURA):
                bloquear("systemctl sólo para mirar (status/is-active/show); reiniciar va con tu sudo permitido")
        if prog in ("ssh", "scp", "rsync", "sftp") and re.search(r"(^|[\s@])(localhost|127\.0\.0\.1|::1|vmi3091691|185\.239\.209\.203)\b", trozo):
            bloquear("ssh a esta misma VPS te saca de tu espacio")


def main():
    global AGENTE, MIO
    try:
        ev = json.load(sys.stdin)
    except Exception:
        AGENTE, MIO = "?", "?"
        bloquear("no pude leer la llamada")
    AGENTE = agente_actual()
    MIO = AGENTES + "/" + AGENTE
    if not AGENTE or not os.path.isdir(MIO):
        MIO = MIO if AGENTE else "?"
        bloquear("no sé qué agente eres (ZORO_AGENT_NAME / CLAUDE_CONFIG_DIR)")
    conf = cargar_config()
    permitidos = conf.get("sudo", {}).get(AGENTE, [])
    tool = ev.get("tool_name", "")
    ti = ev.get("tool_input", {}) or {}
    cwd = ev.get("cwd") or os.getcwd()
    revisar_ruta(cwd, "/", origen="tu carpeta de trabajo")

    if tool in ("Read", "NotebookRead"):
        revisar_ruta(ti.get("file_path") or ti.get("notebook_path"), cwd)
    elif tool in ("Write", "Edit", "MultiEdit", "NotebookEdit"):
        revisar_ruta(ti.get("file_path") or ti.get("notebook_path"), cwd, escribe=True)
        revisar_texto_peligroso(json.dumps(ti))
    elif tool in ("Glob", "Grep", "LS"):
        revisar_ruta(ti.get("path") or cwd, cwd)
        pat = ti.get("pattern") or ""
        if pat.startswith(("/", "~")) or ".." in pat:
            revisar_ruta(re.split(r"[*?\[{]", pat)[0] or "/", ti.get("path") or cwd)
    elif tool == "Bash":
        revisar_bash(ti.get("command", ""), cwd, permitidos)
    elif tool.startswith("mcp__"):
        bloquear("los sub-agentes no usan MCP")
    sys.exit(0)


AGENTE, MIO = "?", "?"
if __name__ == "__main__":
    main()
