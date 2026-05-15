<h1 align="center">LIAM</h1>
<p align="center"><em>Lightweight Infrastructure for AI Models</em></p>
<p align="center">
  Self-hosted AI proxy with multi-account rotation, anti-ban protection, and CLI integrations.
</p>

---

## What is LIAM?

A single-binary proxy that sits between your coding tools and AI providers (Antigravity, Kiro). One OpenAI-compatible endpoint, multi-account rotation, intelligent fallback when accounts hit limits.

```
Your tools (OpenCode, Claude Code, Codex, Cursor, ...)
        │
        ▼
   ┌─────────┐
   │  LIAM   │  ← single endpoint, anti-ban rotation
   └────┬────┘
        │
        ▼
   Antigravity (Google)  ·  Kiro (AWS)
```

## Quick Start

```bash
# Build
git clone https://github.com/IlhamFaturachman/liam.git
cd liam && make build

# First-time setup (harvest deps, optional Supabase sync, dashboard password)
./bin/liam setup

# Run (foreground)
./bin/liam serve

# Or background daemon
./bin/liam start
./bin/liam status
./bin/liam stop
```

Open `http://localhost:666/dashboard` (default password `123456`, change in setup).

## Connect Your Tools

Dashboard → **Integrations** → pick your CLI → choose endpoint + API key + model → click **Apply**.

Or set env vars manually (Claude Code example):

```bash
export ANTHROPIC_BASE_URL=http://localhost:666/v1
export ANTHROPIC_AUTH_TOKEN=lyd-your-api-key
```

Generate API keys via the **Keys** page. They start with `lyd-`.

## Providers & Models

### Antigravity (`ag/`)
Google Gemini Code Assist via the Antigravity IDE OAuth.

| Model | Notes |
|---|---|
| `ag/gemini-3.1-pro-high`, `ag/gemini-3.1-pro-low` | thinking budget variants |
| `ag/gemini-3-flash` | fast, no thinking |
| `ag/claude-sonnet-4-6`, `ag/claude-opus-4-6-thinking` | Claude via AG |
| `ag/gpt-oss-120b-medium` | GPT OSS |

### Kiro (`kr/`)
AWS CodeWhisperer via Kiro Desktop OAuth.

| Model | Notes |
|---|---|
| `kr/claude-opus-4.7`, `kr/claude-opus-4.6` | top-tier reasoning |
| `kr/claude-sonnet-4.6`, `kr/claude-sonnet-4.5` | balanced |
| `kr/claude-haiku-4.5` | fastest Claude |
| `kr/deepseek-3.2`, `kr/qwen3-coder-next` | non-Anthropic |
| `kr/glm-5`, `kr/MiniMax-M2.5` | alternatives |

Add custom models via the dashboard provider page.

### Adding accounts

Two ways:
- **Refresh token import** (Providers → provider page → Add Account → paste refresh token)
- **Batch harvest** (`liam harvest --ui` for browser-based, or `liam harvest --provider ag --file accounts.txt`)

## Features

- **OpenAI-compatible** — drop-in replacement, works with any tool
- **Multi-account rotation** — sticky rotation with per-`(account, model)` cooldown
- **Anti-ban** — per-account stable User-Agent, exponential cooldown, 429 decision engine
- **Long context handling** — auto-trim history when payload approaches the upstream cap
- **Image support** — auto-resize/compress oversized screenshots before sending
- **Tool calls** — schema sanitization to reduce false validation errors
- **Reasoning content** — Kiro's native thinking events (`deepseek-3.2`, `glm-5`, etc.) surfaced as `reasoning_content`
- **Embedded dashboard** — Alpine.js + Tailwind, no external build step
- **Optional Supabase sync** — multi-device account/key/model sync
- **Estimated cost tracker** — per-model pricing rollup ("not actual billing")

## CLI

```bash
liam setup                                          # interactive wizard
liam serve [--port 666]                             # foreground
liam start  [--port 666]                            # background daemon
liam stop / status                                  # daemon lifecycle
liam harvest --ui                                   # browser harvest UI
liam harvest --provider ag --file accounts.txt      # batch harvest
liam accounts list                                  # list accounts
liam keys create / list                             # API key admin
```

## Configuration (`~/.liam/.env`)

| Variable | Default | What it does |
|---|---|---|
| `LIAM_PORT` | `666` | server port |
| `LIAM_DB_PATH` | `~/.liam/data.db` | SQLite path |
| `LIAM_STICKY_REQUESTS` | `3` | requests per account before rotating |
| `LIAM_COOLDOWN_BASE` / `_MAX` | `60` / `1800` | exponential cooldown bounds (seconds) |
| `LIAM_DISABLE_AFTER_ERRORS` | `10` | auto-disable after N consecutive errors |
| `LIAM_ACCOUNT_RPM` | `0` (off) | optional pre-emptive cap (matches 9router behaviour at 0) |
| `LIAM_ACCOUNT_MIN_GAP` | `0` (off) | optional min seconds between reuses |
| `LIAM_DASHBOARD_PASSWORD` | `123456` | initial dashboard password |
| `SUPABASE_URL` / `SUPABASE_KEY` | — | enable cloud sync |
| `SUPABASE_DB_PASSWORD` | — | direct Postgres for table bootstrap |

## API

### Public (Bearer API key)
```
POST /v1/chat/completions   # OpenAI-compatible chat
GET  /v1/models             # available model registry
```

### Management (localhost only by default)
```
GET    /health
GET    /api/overview                        # bundled homepage data
GET    /api/accounts / POST / DELETE
PATCH  /api/accounts/{id}                   # edit
POST   /api/accounts/{id}/test              # validate credentials
POST   /api/accounts/{id}/refresh-quota
POST   /api/accounts/import/{ag,kiro}       # paste refresh token
GET    /api/keys / POST / DELETE
GET    /api/models / POST custom / DELETE / POST test
GET    /api/combos / POST / PUT / DELETE
GET    /api/usage/recent / stats / chart / {id}
GET    /api/integrations / POST {tool}/apply
GET    /api/sync/status / POST /api/sync/now
GET    /sse/requests                        # SSE live feed
```

## Cross-Platform Build

```bash
make build-all
# bin/liam-{darwin,linux,windows}-{arm64,amd64}
```

Pure Go (no CGO). Single binary, self-contained.

## Security Notes

- Dashboard auth: token-based JWT, password configurable
- API keys: SHA-256 hashed; raw value shown only at creation
- Account credentials encrypted at rest if you connect Supabase (PostgreSQL row encryption)
- AG OAuth client id/secret are public (same as the IDE extension)
- All upstream traffic uses HTTPS

## Adding a New Provider

LIAM is provider-extensible. To wire a new backend:

1. Implement the `ProviderExecutor` interface (see `internal/providers/kiro/executor.go`)
2. Register it at server boot in `internal/proxy/server.go`:

```go
s.providers.Register(&ProviderInfo{
    ID:       "newprovider",
    Aliases:  []string{"np"},
    Label:    "New Provider",
    Icon:     "diamond",
    Executor: yourExecutor,
    Refresh:  yourRefresher, // optional
})
```

Dashboard, routing, stats, and integrations pick it up automatically — no other file needs to change.

## License

MIT — Living Your Dream
