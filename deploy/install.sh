#!/usr/bin/env bash
# Build zoro and (re)install it as a managed service.
#   Linux  → systemd unit  (deploy/zoro.service),        needs sudo
#   macOS  → launchd agent (deploy/com.zoro.agent.plist), no sudo
# Idempotent: safe to re-run to apply code changes.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DIR"

OS="$(uname -s)"
GO="${GO:-$(command -v go || true)}"
[ -z "$GO" ] && [ -x /usr/local/go/bin/go ] && GO=/usr/local/go/bin/go
[ -z "$GO" ] && [ -x /opt/homebrew/bin/go ] && GO=/opt/homebrew/bin/go

# ── Dependency checks ────────────────────────────────────────────────────────
miss=0
need() { command -v "$1" >/dev/null 2>&1 || { echo "  ✗ missing: $1 — $2"; miss=1; }; }
echo "==> Checking dependencies…"
[ -n "$GO" ] && [ -x "$GO" ] && echo "  ✓ go ($GO)" || { echo "  ✗ missing: go — install from go.dev or 'brew install go'"; miss=1; }
need git    "install git"
need claude "install the Claude Code CLI and run 'claude /login' once"
command -v ffmpeg >/dev/null 2>&1 && echo "  ✓ ffmpeg" || echo "  ⚠ ffmpeg not found (voice in/out disabled; text still works)"
[ "$miss" = 1 ] && { echo "==> Install the missing required deps and re-run."; exit 1; }

# ── .env ─────────────────────────────────────────────────────────────────────
if [ ! -f .env ]; then
  cp .env.example .env
  chmod 600 .env
  echo "==> Created .env from template. Fill in TELEGRAM_BOT_TOKEN + TELEGRAM_OWNER_ID, then re-run."
  exit 1
fi
grep -q '^TELEGRAM_BOT_TOKEN=.\+' .env || { echo "==> Set TELEGRAM_BOT_TOKEN in .env, then re-run."; exit 1; }
grep -q '^TELEGRAM_OWNER_ID=.\+'  .env || { echo "==> Set TELEGRAM_OWNER_ID in .env, then re-run."; exit 1; }

# ── Build ────────────────────────────────────────────────────────────────────
echo "==> Building zoro…"
"$GO" build -o bin/zoro ./cmd/zoro

# ── Install service ──────────────────────────────────────────────────────────
case "$OS" in
  Linux)
    echo "==> Installing systemd unit (sudo)…"
    sudo cp deploy/zoro.service /etc/systemd/system/zoro.service
    sudo systemctl daemon-reload
    sudo systemctl enable zoro
    sudo systemctl restart zoro
    echo "==> Done. Logs: journalctl -u zoro -f"
    sudo systemctl --no-pager --full status zoro || true
    ;;
  Darwin)
    LABEL="com.zoro.agent"
    PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
    echo "==> Installing launchd agent → $PLIST"
    mkdir -p "$HOME/Library/LaunchAgents"
    sed -e "s#__ENGINE_DIR__#$DIR#g" -e "s#__HOME__#$HOME#g" \
      deploy/com.zoro.agent.plist > "$PLIST"
    launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null || true
    launchctl bootstrap "gui/$(id -u)" "$PLIST"
    launchctl enable "gui/$(id -u)/$LABEL"
    launchctl kickstart -k "gui/$(id -u)/$LABEL"
    echo "==> Done. Logs: tail -f $DIR/state/zoro.err.log"
    echo "    Tip: keep the Mac awake → System Settings ▸ Battery/Energy, or 'caffeinate -s'."
    ;;
  *)
    echo "Unsupported OS: $OS (only Linux/systemd and macOS/launchd)"; exit 1 ;;
esac
