# zoro ⚔️

A single-user Telegram agent backed by the Claude Code CLI. One persistent
conversation, running inside your home dir (`ZORO_WORK_DIR`), supervised by
systemd (Linux) or launchd (macOS) so it never dies and revives on reboot. Pure
Go, zero external dependencies. Runs on a VPS or a Mac — see **Install** below.

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

`/newsession` `/compact` `/voice <msg>` `/status` `/cancel` `/redeploy`
`/update` `/restart` `/help`

- **`/redeploy`** — rebuild the current working tree + restart (apply local edits).
- **`/update`** — `git pull --ff-only` from GitHub, then rebuild + restart. Use it
  to roll out code pushed from elsewhere (e.g. your VPS instance → your Mac instance),
  straight from Telegram.

## Install

One script, OS-aware. It checks deps, builds, and installs the service:

```bash
bash deploy/install.sh
#   Linux → systemd unit  (deploy/zoro.service),        needs sudo
#   macOS → launchd agent (deploy/com.zoro.agent.plist), no sudo
```

First run with no `.env` copies `.env.example` and stops so you can fill in
`TELEGRAM_BOT_TOKEN` + `TELEGRAM_OWNER_ID`; re-run to finish. Requires `go`, `git`,
the `claude` CLI (run `claude /login` once); `ffmpeg` is optional (voice).

Dev loop:

```bash
make build      # → bin/zoro
make run        # local (reads ./.env)
make install    # → bash deploy/install.sh
make logs       # journalctl -u zoro -f   (Linux)
```

### Run a second instance (e.g. on a Mac)

The engine is path-portable (all paths default from `ZORO_*` env / `$HOME`), so you
can run another copy elsewhere — a Mac agent that drives Claude Code locally with
your files in reach. Transport is Telegram itself, so **no Tailscale/VPN needed**;
the host just needs outbound internet.

1. Create a **second Telegram bot** with @BotFather (one bot token = one poller — you
   can't share the VPS bot).
2. `git clone` this repo on the Mac, set the new token + your owner id in `.env`
   (point `ZORO_*` paths at the Mac's home).
3. `bash deploy/install.sh` → installs the launchd agent. Keep the Mac awake
   (`caffeinate -s`, or a Mac mini that never sleeps).
4. Push code from anywhere → run **`/update`** in the Mac chat to pull + rebuild.

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
