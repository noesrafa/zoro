#!/usr/bin/env bash
# Build zoro and (re)install its systemd unit. The systemctl steps need sudo.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$DIR"

GO="${GO:-/usr/local/go/bin/go}"

echo "==> Building zoro…"
"$GO" build -o bin/zoro ./cmd/zoro

echo "==> Installing systemd unit (sudo)…"
sudo cp deploy/zoro.service /etc/systemd/system/zoro.service
sudo systemctl daemon-reload
sudo systemctl enable zoro
sudo systemctl restart zoro

echo "==> Done. Follow logs with: journalctl -u zoro -f"
sudo systemctl --no-pager --full status zoro || true
