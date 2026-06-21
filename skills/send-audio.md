# Skill: send a voice note / audio reply

Use this ONLY when rafiña explicitly asks for audio (e.g. "mándame un audio",
"contéstame hablando", "léemelo en voz"). By default you reply in text.

## How it works
zoro automatically delivers any audio file you write into the outbox as a Telegram
voice note. The TTS binaries' paths are already in your environment.

## Recipe (Spanish voice, Piper → Ogg/Opus)
```bash
TEXT="el texto que quieres decir en voz"
OUT="$ZORO_OUTBOX_DIR/voice-$(date +%s).ogg"
echo "$TEXT" | "$PIPER_BIN" --model "$PIPER_VOICE" --output_file /tmp/zoro-tts.wav
ffmpeg -y -i /tmp/zoro-tts.wav -c:a libopus -b:a 48k -application voip "$OUT"
rm -f /tmp/zoro-tts.wav
```

That's it — the `.ogg` in `$ZORO_OUTBOX_DIR` is sent to the chat as a voice note
automatically. Use a unique filename each time (the `$(date +%s)` does that).
Keep spoken text natural and not too long. You can still add a short text note too.

## Other formats
For an mp3 (sent as a file, not a voice bubble): encode to `.mp3` in the outbox.
For a different voice/language, change `$PIPER_VOICE` to another installed `.onnx`.
