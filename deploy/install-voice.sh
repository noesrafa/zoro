#!/usr/bin/env bash
# Install local STT (whisper.cpp) + TTS (piper) for zoro on this VPS.
# Idempotent-ish: safe to re-run. Logs progress with [voice].
set -euo pipefail
log(){ echo "[voice] $*"; }

SHARE="/home/rafael/.local/share"
WHISPER_DIR="$SHARE/whisper.cpp"
PIPER_DIR="$SHARE/piper"
WMODEL="large-v3-turbo-q5_0"
VOICE="es_ES-davefx-medium"

log "Installing cmake (sudo)…"
sudo apt-get update -y -qq
sudo apt-get install -y -qq cmake

log "Cloning + building whisper.cpp…"
if [ ! -d "$WHISPER_DIR/.git" ]; then
  git clone --depth 1 https://github.com/ggerganov/whisper.cpp "$WHISPER_DIR"
fi
cd "$WHISPER_DIR"
cmake -B build -DCMAKE_BUILD_TYPE=Release >/dev/null
cmake --build build -j"$(nproc)" --config Release >/dev/null
test -x build/bin/whisper-cli && log "whisper-cli OK"

log "Downloading whisper model $WMODEL…"
bash ./models/download-ggml-model.sh "$WMODEL"
test -f "models/ggml-$WMODEL.bin" && log "model OK"

log "Installing piper (prebuilt binary)…"
mkdir -p "$PIPER_DIR/voices"
cd "$PIPER_DIR"
if [ ! -x "$PIPER_DIR/piper/piper" ]; then
  curl -fsSL -o piper.tar.gz \
    https://github.com/rhasspy/piper/releases/download/2023.11.14-2/piper_linux_x86_64.tar.gz
  tar -xzf piper.tar.gz
  rm -f piper.tar.gz
fi
test -x "$PIPER_DIR/piper/piper" && log "piper OK"

log "Downloading Spanish voice $VOICE…"
VBASE="https://huggingface.co/rhasspy/piper-voices/resolve/main/es/es_ES/davefx/medium"
curl -fsSL -o "voices/$VOICE.onnx"      "$VBASE/$VOICE.onnx"
curl -fsSL -o "voices/$VOICE.onnx.json" "$VBASE/$VOICE.onnx.json"
test -f "voices/$VOICE.onnx" && log "voice OK"

log "DONE"
echo "WHISPER_BIN=$WHISPER_DIR/build/bin/whisper-cli"
echo "WHISPER_MODEL=$WHISPER_DIR/models/ggml-$WMODEL.bin"
echo "PIPER_BIN=$PIPER_DIR/piper/piper"
echo "PIPER_VOICE=$PIPER_DIR/voices/$VOICE.onnx"
