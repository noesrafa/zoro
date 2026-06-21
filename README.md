# zoro ⚔️

A single-user Telegram agent backed by the Claude Code CLI. One persistent
conversation, running inside `/home/rafael`, supervised by systemd so it never
dies and revives on reboot. Pure Go, zero external dependencies.

## How it works

```
Telegram (long poll) ──▶ bot ──▶ claude -p --resume <uuid> --output-format stream-json
        ▲                 │              (cwd = /home/rafael, OAuth subscription)
        │                 ├─ voice in:  ffmpeg → whisper.cpp → text
        └── reply ◀───────┤─ voice out: piper → ffmpeg → Ogg/Opus → sendVoice
                          └─ files out: anything written to ./outbox/ is delivered
```

- **Sessions:** one fixed UUID in `state/state.json`; created once with
  `--session-id`, then `--resume`d forever. `cmd.Dir` is pinned to `ZORO_WORK_DIR`
  (resume is cwd-scoped). `/newsession` rotates the UUID; `/compact` compresses.
- **Auth:** uses the CLI's own OAuth (`~/.claude/.credentials.json`). The driver
  strips `ANTHROPIC_API_KEY`/`ANTHROPIC_AUTH_TOKEN` so billing stays on the
  subscription. If login expires, run `claude /login` interactively.
- **Single user:** every update is checked against `TELEGRAM_OWNER_ID`.

## Commands

`/newsession` `/compact` `/voice <msg>` `/status` `/cancel` `/restart` `/help`

## Build & run

```bash
make build      # → bin/zoro
make run        # local (reads ./.env)
make install    # build + install/enable/restart systemd unit (needs sudo)
make logs       # journalctl -u zoro -f
```

## Voice (optional, off until installed)

Set in `.env` after installing the binaries:

- **STT:** `WHISPER_BIN` + `WHISPER_MODEL` (whisper.cpp, e.g. ggml-large-v3-turbo).
- **TTS:** `PIPER_BIN` + `PIPER_VOICE` (Piper, e.g. an `es_ES-*-medium.onnx` voice).

Until then, voice notes get a "not installed yet" reply and everything else works.

## Config

See `.env.example`. Defaults target this VPS (`/home/rafael`, `opus`, full autonomy).
```
.env             secrets + config (chmod 600, not committed)
state/           session id + model/effort + tts scratch
inbox/           downloaded Telegram attachments
outbox/          files the agent writes here are sent to chat
```

The agent's brain is NOT in this repo — it lives in `~/.zoro` (its own git repo), split in two:
- `~/.zoro/soul.md` — WHO zoro is + HOW it behaves. Injected into **every message**
  (`--append-system-prompt`); editing hot-reloads on the next message.
- `~/.zoro/context.md` — durable background about the user. Injected **once at the start
  of each conversation** (prepended to the first message, then carried in history via
  `--resume`); editing applies to the next new conversation (`/newsession`).

The daemon seeds defaults for both on first run if missing.
