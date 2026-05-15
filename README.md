<p align="center">
  <h1 align="center">LIAM</h1>
  <p align="center"><em>Lightweight Infrastructure for AI Models</em></p>
  <p align="center">
    Self-hosted AI proxy gateway with multi-account rotation, anti-ban protection, and CLI tool integrations.
  </p>
  <p align="center">
    <a href="#features">Features</a> •
    <a href="#quick-start">Quick Start</a> •
    <a href="#architecture">Architecture</a> •
    <a href="#providers">Providers</a> •
    <a href="#integrations">Integrations</a> •
    <a href="#configuration">Configuration</a>
  </p>
</p>

---

## What is LIAM?

LIAM is a single-binary AI proxy that sits between your coding tools and AI providers. It manages hundreds of accounts, rotates them intelligently to avoid bans, translates between API formats, and exposes a single OpenAI-compatible endpoint that works with any tool.

```
Your Tools (Claude Code, Codex, OpenCode, Cursor...)
        │
        ▼
   ┌─────────┐
   │  LIAM   │  ← Single endpoint, manages everything
   └────┬────┘
        │ Smart rotation, anti-ban, format translation
        ▼
   ┌─────────────────────────────┐
   │  Antigravity  │    Kiro     │  ← Multiple providers
   │  (Google)     │   (AWS)     │
   └─────────────────────────────┘
```

---

## Features

### Core Proxy
- **OpenAI-compatible API** — drop-in replacement, works with any tool that supports OpenAI/Anthropic format
- **Multi-provider** — Antigravity (Google Gemini Code Assist) and Kiro (AWS CodeWhisperer)
- **SSE Streaming** — real-time streaming with on-the-fly format translation
- **Format Translation** — OpenAI ↔ Gemini Cloud Code, OpenAI ↔ AWS EventStream (binary protocol)

### Anti-Ban Protection
- **Per-account rate limiting** — configurable RPM per account (default: 10)
- **Minimum usage gap** — prevents reusing same account too quickly (default: 6s)
- **Sticky rotation** — uses same account for N requests before rotating (default: 3)
- **Exponential cooldown** — progressive backoff on errors (60s → 120s → 240s... up to 30min)
- **Stable session IDs** — per-account persistent session for prompt caching
- **Auto-disable** — permanently disables accounts after 10 consecutive errors

### Thinking Mode (Option C)
Two ways to enable thinking/reasoning:

```jsonc
// Method 1: Suffix-based
{ "model": "ag/claude-opus-4-6-thinking" }
{ "model": "kr/claude-opus-4.6-thinking" }

// Method 2: Parameter-based
{ "model": "ag/claude-opus-4-6", "reasoning_effort": "high" }
```

| Effort | Thinking Budget |
|--------|----------------|
| `low` | 2,048 tokens |
| `medium` | 8,192 tokens |
| `high` | 32,768 tokens |
| `max` | 65,536 tokens |

### Account Management
- **Batch harvest** — automated Google OAuth login via Camoufox (anti-detection browser)
- **Auto token refresh** — background worker refreshes expiring tokens proactively
- **Health checks** — periodic validation of account status
- **Quota tracking** — per-account quota bars with visual indicators

### CLI Tool Integrations
One-click auto-apply LIAM config to your coding tools:

| Tool | Auto-Apply | Model Slots |
|------|-----------|-------------|
| Claude Code | ✅ | Opus, Sonnet, Haiku |
| Codex CLI | ✅ | Primary |
| OpenCode | ✅ | Primary, Subagent |
| Cursor | Manual | Primary |
| Cline | Manual | Primary |
| Open Claw | ✅ | Primary + per-agent |
| Hermes Agent | ✅ | Primary |

### Cloud Sync (Supabase)
- **Bidirectional sync** — accounts, API keys, custom models sync across devices
- **Multi-device** — harvest on Mac, proxy on VPS, everything in sync
- **30-second interval** — automatic background sync
- **Conflict resolution** — last-write-wins based on timestamps

### Web Dashboard
- **Dark theme** (charcoal + wine red accent)
- **Collapsible sidebar** with persistent state
- **Smooth page transitions** (fade + slide)
- **Real-time SSE feed** of live requests
- **Usage charts** (Chart.js, 24h request history)
- **Model registry** with test button, custom models, aliases
- **Provider detail** with model cards grid + accounts table + quota bars
- **Integrations** with 2-level navigation (grid → per-tool config)
- **Password protected** (token-based auth, configurable)

---

## Quick Start

### 1. Build

```bash
git clone https://github.com/IlhamFaturachman/liam.git
cd liam
make build
```

### 2. Setup

```bash
./bin/liam setup
# Interactive wizard:
#   → Harvest module (Python + Camoufox)
#   → Supabase sync (optional)
#   → Dashboard password
#   → Port
```

### 3. Run

```bash
# Foreground (see logs)
./bin/liam serve

# Background daemon
./bin/liam start
./bin/liam status
./bin/liam stop
```

### 4. Open Dashboard

```
http://localhost:666/dashboard
Password: 123456 (or what you set during setup)
```

### 5. Connect Your Tools

Go to **Integrations** tab → click your tool → configure endpoint + API key → **Apply**.

Or manually:
```bash
# Example: Claude Code
export ANTHROPIC_BASE_URL=http://localhost:666/v1
export ANTHROPIC_AUTH_TOKEN=li-your-api-key-here
```

---

## Architecture

```
liam/
├── cmd/liam/                     # CLI entry (serve, start, stop, setup, harvest, keys, accounts)
├── internal/
│   ├── config/                   # Settings, auto-load ~/.liam/.env
│   ├── db/                       # SQLite (pure Go, modernc.org/sqlite)
│   ├── models/                   # Model registry + aliases
│   ├── proxy/                    # HTTP server, account pool, SSE, middleware
│   ├── providers/
│   │   ├── antigravity/          # Google Gemini Code Assist (format translation + executor)
│   │   └── kiro/                 # AWS CodeWhisperer (EventStream binary protocol)
│   ├── workers/                  # Background goroutines (refresh, health, cleanup)
│   ├── integrations/             # 7 CLI tool adapters (auto-apply config)
│   ├── sync/                     # Supabase bidirectional sync
│   ├── dashboard/                # Embedded web UI (Alpine.js + Tailwind CDN + Chart.js)
│   └── harvest/                  # Python harvest module manager
├── harvest/                      # Python batch login (Camoufox + multi-provider)
├── Makefile                      # Cross-platform build targets
└── .env.example                  # Configuration template
```

---

## Providers

### Antigravity (`ag/`)

Google Gemini Code Assist — accessed via the same OAuth credentials as the Antigravity IDE extension.

| Model | Description |
|-------|-------------|
| `ag/gemini-3.1-pro-high` | Gemini 3 Pro (high thinking budget) |
| `ag/gemini-3.1-pro-low` | Gemini 3 Pro (low thinking budget) |
| `ag/gemini-3-flash` | Gemini 3 Flash (no thinking) |
| `ag/claude-sonnet-4-6` | Claude Sonnet 4.6 |
| `ag/claude-opus-4-6-thinking` | Claude Opus 4.6 Thinking |
| `ag/gpt-oss-120b-medium` | GPT OSS 120B Medium |

### Kiro (`kr/`)

AWS CodeWhisperer — accessed via AWS SSO OIDC refresh tokens.

| Model | Description |
|-------|-------------|
| `kr/claude-opus-4.7` | Claude Opus 4.7 |
| `kr/claude-opus-4.6` | Claude Opus 4.6 |
| `kr/claude-sonnet-4.5` | Claude Sonnet 4.5 |
| `kr/claude-haiku-4.5` | Claude Haiku 4.5 |
| `kr/deepseek-3.2` | DeepSeek 3.2 |
| `kr/qwen3-coder-next` | Qwen3 Coder Next |
| `kr/glm-5` | GLM 5 |
| `kr/MiniMax-M2.5` | MiniMax M2.5 |

Custom models can be added via the dashboard.

---

## Integrations

### Supported Tools

LIAM auto-configures these tools to route through the proxy:

| Tool | Config File | Method |
|------|-------------|--------|
| **Claude Code** | `~/.claude/settings.json` | Env vars (ANTHROPIC_BASE_URL, etc.) |
| **Codex CLI** | `~/.codex/config.toml` | TOML provider block |
| **OpenCode** | `~/.config/opencode/opencode.json` | JSON provider entry |
| **Cursor** | UI settings | Manual (Base URL + Key) |
| **Cline** | VSCode extension settings | Manual (OpenAI Compatible) |
| **Open Claw** | `~/.openclaw/openclaw.json` | JSON provider + agent models |
| **Hermes** | `~/.hermes/config.yaml` + `.env` | YAML model block + env |

### Auto-Apply

From the dashboard Integrations page:
1. Select your tool
2. Choose endpoint (localhost or custom URL for VPS)
3. Select API key
4. Configure model slots (per-tool)
5. Click **Apply** — config written automatically

Backup of existing config is created before any modification (`.bak` files, keeps last 3).

---

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `LIAM_PORT` | `666` | Server port |
| `LIAM_DB_PATH` | `~/.liam/data.db` | SQLite database path |
| `LIAM_ACCOUNT_RPM` | `10` | Max requests/min per account |
| `LIAM_ACCOUNT_MIN_GAP` | `6` | Min seconds between account reuse |
| `LIAM_STICKY_REQUESTS` | `3` | Requests before rotating account |
| `LIAM_COOLDOWN_BASE` | `60` | Base cooldown seconds on error |
| `LIAM_COOLDOWN_MAX` | `1800` | Max cooldown (30 min) |
| `LIAM_DISABLE_AFTER_ERRORS` | `10` | Disable account after N errors |
| `LIAM_AG_CLIENT_ID` | (built-in) | Antigravity OAuth Client ID |
| `LIAM_AG_CLIENT_SECRET` | (built-in) | Antigravity OAuth Client Secret |
| `SUPABASE_URL` | (empty) | Supabase project URL (enables sync) |
| `SUPABASE_KEY` | (empty) | Supabase service role key |
| `SUPABASE_DB_PASSWORD` | (empty) | Database password (for table creation) |

All config is auto-loaded from `~/.liam/.env` on startup.

### CLI Commands

```bash
liam setup                    # Interactive setup wizard
liam serve                    # Start in foreground
liam start [--port 666]       # Start as background daemon
liam stop                     # Stop daemon
liam status                   # Check if running
liam harvest --ui             # Start harvest web UI
liam harvest --provider ag --file accounts.txt [--concurrency 4] [--headless]
liam accounts list            # List all accounts
liam keys create              # Create API key
liam keys list                # List keys
```

---

## API Endpoints

### OpenAI-Compatible (requires Bearer API key)

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/chat/completions` | Chat completions (streaming + non-streaming) |
| `GET` | `/v1/models` | List available models |

### Management (no auth, localhost only)

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/health` | Health check |
| `GET` | `/api/accounts` | List accounts |
| `POST` | `/api/accounts` | Add account |
| `GET/POST` | `/api/keys` | List/create API keys |
| `DELETE` | `/api/keys/:id` | Delete key |
| `GET` | `/api/models` | Model registry |
| `POST` | `/api/models/test` | Test a model |
| `POST` | `/api/models/custom` | Add custom model |
| `GET` | `/api/integrations` | List CLI tools |
| `POST` | `/api/integrations/:tool/apply` | Auto-apply config |
| `GET` | `/api/sync/status` | Supabase sync status |
| `POST` | `/api/sync/now` | Trigger manual sync |
| `GET` | `/sse/requests` | Live request feed (SSE) |

---

## Cross-Platform Build

```bash
make build-all
```

Produces binaries for all platforms (no CGO, pure Go):

```
bin/liam-darwin-arm64       (macOS Apple Silicon)
bin/liam-darwin-amd64       (macOS Intel)
bin/liam-linux-amd64        (Linux x86_64)
bin/liam-linux-arm64        (Linux ARM)
bin/liam-windows-amd64.exe  (Windows)
```

---

## Harvest Module

Batch login Google accounts to providers using Camoufox (anti-detection Firefox):

```bash
# Setup (one-time)
liam setup

# Web UI mode
liam harvest --ui
# → Open http://localhost:8000

# CLI mode
liam harvest --provider ag --file accounts.txt --concurrency 4 --headless
```

### Features
- Concurrent browser automation (1-8 parallel)
- Anti-detection (Camoufox fingerprint randomization)
- CAPTCHA detection (auto-skip)
- Edge case handling (account picker, TOS, region selection, verify challenges)
- Per-account status tracking (live progress)
- Auto-import to proxy DB on success
- Retry failed accounts (retryable errors only)

### accounts.txt Format
```
email1@gmail.com:password123
email2@gmail.com:password456
email3@gmail.com:password789
```

---

## Supabase Sync

Enable multi-device sync by connecting to Supabase:

```bash
# During setup
liam setup
# → Enable cloud sync? y
# → Supabase URL: https://xxxxx.supabase.co
# → Service Role Key: eyJhbGci...
# → Database Password: ********
```

Or manually set env vars:
```bash
export SUPABASE_URL=https://xxxxx.supabase.co
export SUPABASE_KEY=eyJhbGci...
```

### What Syncs

| Data | Direction | Interval |
|------|-----------|----------|
| Accounts (credentials, status, quota) | Bidirectional | 30s |
| API Keys | Bidirectional | 30s |
| Custom Models | Bidirectional | 30s |
| Settings | NOT synced (per-device) | — |
| Usage Logs | NOT synced (local only) | — |

---

## Security Notes

- Dashboard is password-protected (token-based, configurable)
- API keys are SHA-256 hashed in database (raw key shown once at creation)
- Supabase credentials stored in `~/.liam/.env` with `0600` permissions
- Account credentials (OAuth tokens) stored encrypted-at-rest via Supabase
- AG OAuth Client ID/Secret are public (same as Antigravity IDE extension, used by millions)
- All traffic to providers uses HTTPS

---

## License

MIT — Living Your Dream
