# LIAM

**L**iving **I**ntelligent **A**utomation **M**achine

A self-hosted, OpenAI-compatible AI proxy gateway with multi-account rotation, anti-ban protection, and CLI tool integrations.

## Features

- **OpenAI-compatible API** — works with any tool that supports OpenAI/Anthropic format (Claude Code, Codex, OpenCode, Cursor, Cline, OpenClaw, Hermes)
- **Multi-provider support** — Antigravity (Google Gemini Code Assist) and Kiro (AWS CodeWhisperer)
- **Anti-ban protection** — per-account rate limiting, exponential cooldown, sticky rotation, stable session IDs
- **Smart account pool** — LRU selection, auto token refresh, health checks, cooldown management
- **Format translation** — OpenAI ↔ Gemini, OpenAI ↔ AWS EventStream
- **Streaming support** — SSE end-to-end with format translation on-the-fly
- **Web dashboard** — manage accounts, models, API keys, integrations, usage analytics
- **Harvest module** — batch login Google accounts via Camoufox (Python)
- **CLI tool integrations** — auto-apply LIAM config to Claude Code, Codex, OpenCode, Cursor, Cline, OpenClaw, Hermes
- **Single binary** — no Docker, no Node.js, single Go binary works on macOS/Linux/Windows

## Quick Start

```bash
# Build
make build

# Run (default port 666)
./bin/liam serve

# Or daemon mode
./bin/liam start
./bin/liam status
./bin/liam stop

# Open dashboard
open http://localhost:666/dashboard

# Default password: 123456
```

## Architecture

```
liam/
├── cmd/liam/                  # CLI entry point
├── internal/
│   ├── config/               # Settings + OAuth config
│   ├── db/                   # SQLite (pure Go via modernc.org/sqlite)
│   ├── proxy/                # HTTP server, account pool, streaming
│   ├── models/               # Model registry + aliases
│   ├── providers/
│   │   ├── antigravity/      # Google Gemini Code Assist
│   │   └── kiro/             # AWS CodeWhisperer
│   ├── workers/              # Token refresh, health check, log cleanup
│   ├── integrations/         # CLI tool adapters (auto-apply config)
│   ├── dashboard/            # Embedded web UI
│   ├── harvest/              # Python harvest module manager
│   └── ...
├── harvest/                  # Python batch login (Camoufox)
└── Makefile
```

## CLI Commands

```bash
liam start [--port 666]       Start daemon
liam stop                     Stop daemon
liam status                   Check status
liam serve                    Start in foreground (debug)
liam harvest --ui             Start harvest web UI
liam harvest --provider ag --file accounts.txt --concurrency 4
liam setup                    Install Python + Camoufox for harvest
liam accounts list            List all accounts
liam keys create              Create API key
liam keys list                List keys
```

## Environment Variables

| Variable | Default | Purpose |
|----------|---------|---------|
| `LIAM_PORT` | 666 | Server port |
| `LIAM_DB_PATH` | `~/.liam/data.db` | SQLite path |
| `LIAM_ACCOUNT_RPM` | 10 | Max requests/min per account |
| `LIAM_ACCOUNT_MIN_GAP` | 6 | Min seconds between account reuse |
| `LIAM_STICKY_REQUESTS` | 3 | Requests before rotating account |
| `LIAM_COOLDOWN_BASE` | 60 | Base cooldown seconds on error |
| `LIAM_COOLDOWN_MAX` | 1800 | Max cooldown seconds |
| `LIAM_DISABLE_AFTER_ERRORS` | 10 | Disable account after N errors |

## Cross-Platform Build

```bash
make build-all
# Produces:
#   bin/liam-darwin-arm64
#   bin/liam-darwin-amd64
#   bin/liam-linux-amd64
#   bin/liam-linux-arm64
#   bin/liam-windows-amd64.exe
```

## API Endpoints

### OpenAI-Compatible
- `POST /v1/chat/completions` — chat completions (streaming + non-streaming)
- `GET /v1/models` — list available models

### Management
- `GET /api/accounts` — list accounts
- `GET /api/keys` — list API keys
- `POST /api/keys` — create new API key
- `DELETE /api/keys/:id` — delete key
- `GET /api/models` — model registry
- `POST /api/models/custom` — add custom model
- `POST /api/models/test` — test a model
- `GET /api/integrations` — list CLI tool integrations
- `POST /api/integrations/:tool/apply` — auto-apply LIAM config
- `POST /api/integrations/:tool/reset` — remove LIAM config

### Real-time
- `GET /sse/requests` — SSE feed of live requests

### Auth
- `POST /api/auth/login` — login (returns token)
- `POST /api/auth/password` — change password

## Default Models

### Antigravity (`ag/`)
- `ag/gemini-3.1-pro-high`
- `ag/gemini-3.1-pro-low`
- `ag/gemini-3-flash`
- `ag/claude-sonnet-4-6`
- `ag/claude-opus-4-6-thinking`
- `ag/gpt-oss-120b-medium`

### Kiro (`kr/`)
- `kr/claude-sonnet-4.5`
- `kr/claude-haiku-4.5`
- `kr/claude-opus-4.6`
- `kr/claude-opus-4.7`
- `kr/deepseek-3.2`
- `kr/qwen3-coder-next`
- `kr/glm-5`
- `kr/MiniMax-M2.5`

## Thinking Mode

Two ways to enable thinking:

1. **Suffix-based**: append `-thinking` to model ID
   ```
   ag/claude-opus-4-6-thinking
   kr/claude-opus-4.6-thinking
   ```

2. **Parameter-based**: pass `reasoning_effort` in request body
   ```json
   { "model": "ag/claude-opus-4-6", "reasoning_effort": "high" }
   ```

Effort levels map to thinking budget:
- `low` → 2048 tokens
- `medium` → 8192 tokens
- `high` → 32768 tokens
- `max` → 65536 tokens

## License

MIT — Living Your Dream
