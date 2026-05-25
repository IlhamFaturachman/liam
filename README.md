<div align="center">

```
  ██╗     ██╗ █████╗ ███╗   ███╗
  ██║     ██║██╔══██╗████╗ ████║
  ██║     ██║███████║██╔████╔██║
  ██║     ██║██╔══██║██║╚██╔╝██║
  ███████╗██║██║  ██║██║ ╚═╝ ██║
  ╚══════╝╚═╝╚═╝  ╚═╝╚═╝     ╚═╝
```

### **L**ightweight **I**nfrastructure for **A**I **M**odels

**Self-hosted AI proxy with multi-account rotation, anti-ban protection, and token-saving compression. Built for non-stop coding.**

[![Go Version](https://img.shields.io/badge/go-1.25%2B-00ADD8?logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/license-MIT-green)](LICENSE)
[![Single Binary](https://img.shields.io/badge/deploy-single%20binary-blue)](#-build--deploy)
[![No CGO](https://img.shields.io/badge/CGO-disabled-success)](#)
[![Tests](https://img.shields.io/badge/tests-51%20passing-brightgreen)](#)

[Quick Start](#-quick-start) · [Features](#-features) · [Architecture](#-architecture) · [Models](#-models) · [Configuration](#%EF%B8%8F-configuration)

</div>

---

## 🎯 What is LIAM?

A single-binary proxy that sits between your coding tools (OpenCode, Claude Code, Cursor, Codex, Cline, etc.) and AI providers (Antigravity, Kiro). One OpenAI-compatible endpoint, multi-account rotation, intelligent fallback when accounts hit limits.

```
┌─────────────────────────────────────────────────────────┐
│  Your tools                                             │
│  ┌──────────┐  ┌──────────┐  ┌────────┐  ┌──────────┐   │
│  │ OpenCode │  │  Claude  │  │ Cursor │  │  Codex   │   │
│  └────┬─────┘  └────┬─────┘  └───┬────┘  └────┬─────┘   │
└───────┼─────────────┼────────────┼────────────┼─────────┘
        └─────────────┴────────────┴────────────┘
                            │  OpenAI-compatible API
                            ▼
                    ┌───────────────┐
                    │     LIAM      │  ← anti-ban rotation
                    │               │  ← RTK + Caveman savers
                    │               │  ← session affinity
                    └───────┬───────┘
                            │
            ┌───────────────┴───────────────┐
            ▼                               ▼
    ┌───────────────┐               ┌───────────────┐
    │  Antigravity  │               │     Kiro      │
    │   (Google)    │               │     (AWS)     │
    └───────────────┘               └───────────────┘
```

---

## ✨ Features

### 🛡️ Anti-Ban Stack
- **Per-account stable User-Agent** — deterministic device profile pinned to each account hash
- **Per-account exponential backoff** — 2s → 4s → 8s → ... → 5min cap (matches 9router exactly)
- **Session affinity** — same conversation routes to same account (priority chain: `X-Session-ID` → `metadata.user_id` → `X-Client-Request-Id` → `conversation_id`)
- **Sticky rotation** — N requests per account before rotating (configurable, default 3)
- **Smart 429 classifier** — text-first then status, honors `Retry-After`, distinguishes transient from permanent

### 💰 Token Savers (NEW)
- **RTK Compression** (default ON) — compresses `git diff`, `grep`, `ls`, build output, and other tool results in request body. **Saves 20-40% input tokens** with zero output style change. Invisible to model.
- **Caveman Mode** (opt-in) — inject terse-style instruction. **Saves ~65% output tokens** while keeping code/paths/errors verbatim. 3 levels: lite / full / ultra.
- **Provider-agnostic** — both run before any provider translation. Future providers automatically benefit.

### 🚀 Provider Excellence
- **Antigravity (Google Code Assist)** — full Gemini 3 Pro / Claude / GPT-OSS access via official IDE OAuth
- **Kiro (AWS CodeWhisperer)** — Claude Opus 4.7, Sonnet 4.5/4.6, Haiku 4.5, DeepSeek, Qwen, GLM-5, MiniMax M2.5
- **LIAM Overlay** — re-frames Kiro Claude as general-purpose assistant (unlocks persona, creative work, non-coding tasks beyond default Kiro IDE scope)
- **Inline thinking blocks** stripped from Kiro response stream (clean output, raw `reasoning_content` available)

### 🎨 Embedded Dashboard
- **Single-binary deploy** — Alpine.js + Tailwind UI baked into the executable via `go:embed`
- **Live request feed** — SSE stream of every request with status, latency, tokens
- **Per-account drill-down** — plan, quota breakdown by resource, cooldown countdown, backoff level
- **Provider stats**, usage charts (24h / 7d / 30d / 60d), top models, recent errors

### 🌾 Harvest Module
- **Browser-based UI** — paste `email:password` list, click harvest, watch progress
- **CLI batch mode** — `liam harvest --provider ag --file accounts.txt --concurrency 4`
- **Camoufox anti-detect** — randomized OS pool, humanized cursor, WebRTC blocked, geoip-aware
- **Auto-import** — successful harvests pushed straight to LIAM proxy DB

### 🔐 Production-Grade
- **Streaming truncation safe** — body streams uncapped (multi-minute tool calls don't get cut)
- **Single-account graceful degradation** — sleeps cooldown + retries instead of 503'ing
- **Combo mode** — chained model fallback for resilience
- **Optional Supabase sync** — bidirectional account/key/model sync across devices, IPv4-first dialer with retry-with-backoff
- **51 stress tests passing** — overlay, tool calls, thinking, multimodal, all verified

---

## 🚀 Quick Start

### Install

```bash
git clone https://github.com/IlhamFaturachman/liam.git
cd liam
make build
```

Or grab a pre-built binary from [Releases](https://github.com/IlhamFaturachman/liam/releases) (mac, linux, windows).

### Project Structure

```
liam/
├── cmd/liam/main.go        # Entrypoint (CLI)
├── internal/               # Core logic (proxy, providers, db, dashboard, etc.)
├── harvest/                # Python batch login module
├── bin/                    # Build output (cross-platform binaries)
└── liam                    # Root binary (after `go build`)
```

LIAM follows standard Go project layout. The main entrypoint is in `cmd/liam/main.go`, not the root directory.

### First-time setup

```bash
./bin/liam setup
```

The wizard installs harvest dependencies (Python venv + Camoufox), optionally configures Supabase sync, and sets your dashboard password. **Live progress** for every step — no more staring at frozen terminals.

### Run

**Development mode** (auto-recompile):
```bash
go run ./cmd/liam serve
```

**Production mode** (build first):
```bash
# Build binary
go build -o liam ./cmd/liam

# Run foreground (debug)
./liam serve

# OR run as daemon
./liam start
./liam status
./liam stop
```

**Cross-platform build** (outputs to `bin/`):
```bash
make build-all
./bin/liam-darwin-arm64 serve
```

**Harvest module** (batch login):
```bash
# Web UI
liam harvest --ui                # http://localhost:8000

# CLI batch mode
liam harvest --provider ag --file accounts.txt --concurrency 4 --headless
```

Dashboard: **http://localhost:666/dashboard** (default password: `123456`, change in setup).

### Connect a tool

Dashboard → **Integrations** → pick your CLI → **Apply**.

Or set env vars manually:

```bash
# Claude Code
export ANTHROPIC_BASE_URL=http://localhost:666/v1
export ANTHROPIC_AUTH_TOKEN=lyd-your-api-key

# Codex / OpenAI-compatible
export OPENAI_BASE_URL=http://localhost:666/v1
export OPENAI_API_KEY=lyd-your-api-key
```

Generate API keys via **Dashboard → Keys**. Format: `lyd-...`.

---

## 📦 Models

### Antigravity (`ag/`)
Google Gemini Code Assist via the Antigravity IDE OAuth. **Free tier**, multi-account ready.

| Model | Notes |
|---|---|
| `ag/gemini-3.1-pro-high` · `ag/gemini-3.1-pro-low` | Thinking budget variants, 1M context |
| `ag/gemini-3-flash` | Fast, no thinking |
| `ag/claude-sonnet-4-6` · `ag/claude-opus-4-6-thinking` | Claude via AG |
| `ag/gpt-oss-120b-medium` | GPT OSS |

### Kiro (`kr/`)
AWS CodeWhisperer via Kiro Desktop OAuth. **200k token cap**, multi-account essential.

| Model | Notes |
|---|---|
| `kr/claude-opus-4.7` · `kr/claude-opus-4.6` | Top-tier reasoning, persona-stable with LIAM overlay |
| `kr/claude-sonnet-4.6` · `kr/claude-sonnet-4.5` | Balanced; Sonnet 4.5 unlocks persona swap |
| `kr/claude-haiku-4.5` | Fastest Claude (overlay-bypassed) |
| `kr/deepseek-3.2` · `kr/qwen3-coder-next` | Non-Anthropic |
| `kr/glm-5` · `kr/MiniMax-M2.5` | Alternatives |

### Thinking DSL

Inline thinking control via `model(value)` syntax:

```
ag/claude-opus-4-6(8192)        # direct token budget
kr/claude-opus-4.7(high)        # level: low=2048, medium=8192, high=32768, max=65536
kr/claude-sonnet-4.5(none)      # disable
kr/claude-sonnet-4.5(auto)      # let model decide
```

Backward-compat: `model-thinking` suffix → `reasoning_effort=high`.

---

## 🏗️ Architecture

### Request lifecycle

```
1. Client request (OpenAI shape)
        │
        ▼
2. Parse · resolve aliases · strip thinking DSL
        │
        ▼
3. Token Savers (provider-agnostic)
   ├─ RTK     compress tool_result content
   └─ Caveman inject terse-style system prompt
        │
        ▼
4. Account picker
   ├─ Session affinity (same convo → same account)
   ├─ Sticky rotation (N reqs/account)
   └─ Cooldown filter (skip backoff)
        │
        ▼
5. Provider executor
   ├─ Inline token refresh (singleflight dedup)
   ├─ Per-account stable User-Agent
   └─ Translate to Kiro EventStream / AG Gemini
        │
        ▼
6. Stream response · classify errors · log usage · broadcast SSE
```

### Adding providers

LIAM is provider-extensible. Implement the `ProviderExecutor` interface, register at boot:

```go
s.providers.Register(&ProviderInfo{
    ID:       "newprovider",
    Aliases:  []string{"np"},
    Label:    "New Provider",
    Executor: yourExecutor,
    Refresh:  yourRefresher, // optional
})
```

Dashboard, routing, stats, integrations, and **all token savers** pick it up automatically. Zero touches to existing code.

---

## 🗂️ CLI

```bash
liam setup                                          # interactive wizard
liam serve [--port 666]                             # foreground
liam start [--port 666]                             # background daemon
liam stop / status                                  # daemon lifecycle
liam harvest --ui                                   # browser harvest UI
liam harvest --provider ag --file accounts.txt     # batch harvest
liam accounts list                                  # list accounts
liam keys create / list                             # API key admin
```

---

## ⚙️ Configuration

Config lives at `~/.liam/.env` (auto-loaded at startup).

| Variable | Default | What it does |
|---|---|---|
| `LIAM_PORT` | `666` | Server port |
| `LIAM_DB_PATH` | `~/.liam/data.db` | SQLite path |
| `LIAM_STICKY_REQUESTS` | `3` | Requests per account before rotating |
| `LIAM_COOLDOWN_BASE` / `_MAX` | `60` / `1800` | Exponential cooldown bounds (seconds) |
| `LIAM_DISABLE_AFTER_ERRORS` | `10` | Auto-disable after N consecutive errors |
| `LIAM_ACCOUNT_RPM` | `0` (off) | Optional pre-emptive cap per account |
| `LIAM_ACCOUNT_MIN_GAP` | `0` (off) | Optional min seconds between account reuse |
| `LIAM_KIRO_THINKING_DEFAULT` | `max` | Default Kiro thinking level when no DSL suffix |
| `LIAM_DASHBOARD_PASSWORD` | `123456` | Initial dashboard password |
| `SUPABASE_URL` · `SUPABASE_KEY` | — | Enable cloud sync |
| `SUPABASE_DB_PASSWORD` | — | Direct Postgres for table bootstrap |

### Token saver settings (Dashboard → Settings → Token Savers)

| Setting | Default | What it does |
|---|---|---|
| RTK Compression | **ON** | Compress tool_result content (invisible) |
| Caveman Mode | **OFF** | Inject terse-style system prompt |
| Caveman Level | `lite` | `lite` / `full` / `ultra` |

Per-request override via headers:

```bash
curl ... -H 'X-Liam-Rtk: off' -H 'X-Liam-Caveman: ultra' ...
```

---

## 🔌 API

### Public (Bearer API key required)

```
POST  /v1/chat/completions   # OpenAI-compatible chat
GET   /v1/models             # available models
```

### Management (localhost only by default)

```
# Accounts
GET    /api/accounts                  POST   /api/accounts
PATCH  /api/accounts/{id}             DELETE /api/accounts/{id}
POST   /api/accounts/{id}/test        POST   /api/accounts/{id}/refresh-quota
POST   /api/accounts/{id}/excluded-models
POST   /api/accounts/import/{ag,kiro} # paste refresh token
POST   /api/accounts/reorder          # drag-and-drop priority

# Keys / Models / Combos / Aliases
GET / POST / DELETE pattern for each

# Stats
GET    /api/overview                  # bundled homepage data
GET    /api/usage/recent / stats / chart / {id}
GET    /api/providers/stats

# Token savers
GET    /api/settings/token-saver
POST   /api/settings/token-saver

# Sync
GET    /api/sync/status               POST   /api/sync/now

# Live feed
GET    /sse/requests                  # Server-Sent Events
```

---

## 🛠️ Build & Deploy

### Cross-platform

```bash
make build-all
# Output: bin/liam-{darwin,linux,windows}-{arm64,amd64}
```

**Pure Go, no CGO.** Single binary, self-contained, ~24 MB.

### Docker (optional)

```bash
docker build -t liam .
docker run -d --name liam -p 666:666 \
  -v "$HOME/.liam:/root/.liam" \
  liam
```

---

## 🔒 Security

- **Dashboard auth**: token-based JWT, password configurable
- **API keys**: SHA-256 hashed; raw value shown only at creation
- **Account credentials**: encrypted at rest if Supabase configured (Postgres row encryption)
- **AG OAuth client_id/secret**: public (same as IDE extension, shared with millions of users)
- **All upstream traffic**: HTTPS
- **No telemetry**: nothing leaves your machine except direct provider API calls

---

## 🧪 Testing

```bash
go test ./...                # full suite
go test ./internal/rtk/...   # RTK filters + autodetect (28 tests)
go test ./internal/caveman/... # Caveman shapes + safety (12 tests)
go test -run TestPipeline ./internal/proxy/... # End-to-end stress (9 tests)
```

The pipeline stress suite verifies:
- Tool call arguments survive RTK + caveman + Kiro translate
- LIAM overlay prepends correctly after caveman injection
- Caveman prompt reaches upstream model
- `is_error` tool_results preserved verbatim
- Multimodal image parts not mutated
- Thinking DSL parsed independently of token savers

---

## 🧩 Tool Integrations

Auto-config via Dashboard → **Integrations**:

| Tool | Auth env | Status |
|---|---|---|
| **Claude Code** | `ANTHROPIC_BASE_URL` + `ANTHROPIC_AUTH_TOKEN` | ✅ Tested |
| **Codex** | `OPENAI_BASE_URL` + `OPENAI_API_KEY` | ✅ Tested |
| **OpenCode** | OpenAI-compatible | ✅ Tested |
| **Cursor** | OpenAI-compatible | ✅ Tested |
| **Cline** | OpenAI-compatible | ✅ Tested |
| **OpenClaw** | Claude-compatible | ✅ Tested |
| **Hermes** | OpenAI-compatible | ✅ Tested |

---

## 📚 Resources

- **Dashboard tour**: [docs/dashboard.md](docs/dashboard.md) *(coming soon)*
- **Anti-ban deep dive**: [docs/antiban.md](docs/antiban.md) *(coming soon)*
- **Adding a provider**: see [Architecture → Adding providers](#adding-providers)

---

## 🙏 Credits

Inspired by and ports patterns from:
- [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) — the original Go AI proxy
- [9router](https://github.com/decolua/9router) — Next.js port with RTK Token Saver and Caveman Mode
- [Caveman skill](https://github.com/JuliusBrussee/caveman) — terse-style prompt design

LIAM stands on the shoulders of these projects with a focused, lean, single-binary implementation.

---

## 📜 License

[MIT](LICENSE) — *Living Your Dream*
