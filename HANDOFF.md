# Provider Impersonation — Handoff

Commits: `631159b` → `c9a4205` (6 commits on `master`)

---

## What was built

LIAM's outgoing upstream requests now look indistinguishable from the native IDE clients (Kiro IDE / Antigravity VS Code extension) at three layers: TLS fingerprint, HTTP headers, and response model names.

### Layer 1 — TLS fingerprint (`internal/httputil/utls.go`)

Both provider executors now use a custom `*http.Client` built by `NewUTLSClient`. It hooks `DialTLSContext` and substitutes a [uTLS](https://github.com/refraction-networking/utls) `UClient` configured with `tls.HelloChrome_Auto`. This makes the TLS ClientHello match Chrome's fingerprint instead of Go's stdlib fingerprint, which is trivially identifiable.

`NextProtos` is set to `["http/1.1"]` only (no H2). Both providers use HTTP/1.1 for SSE; the simpler transport avoids a `golang.org/x/net/http2` dependency.

### Layer 2 — HTTP headers (`internal/httputil/headers.go`)

`ScrubUpstreamHeaders` removes 24 headers that reveal proxy origin before the request goes upstream:

- **Proxy tracing**: `X-Forwarded-For`, `X-Forwarded-Host`, `X-Forwarded-Proto`, `X-Forwarded-Port`, `X-Real-IP`, `Forwarded`, `Via`
- **SDK identity**: `X-Title`, `X-Stainless-Lang`, `X-Stainless-Package-Version`, `X-Stainless-Os`, `X-Stainless-Arch`, `X-Stainless-Runtime`, `X-Stainless-Runtime-Version`
- **Browser fingerprint**: `Http-Referer`, `Referer`, `Sec-Ch-Ua`, `Sec-Ch-Ua-Mobile`, `Sec-Ch-Ua-Platform`, `Sec-Fetch-Mode`, `Sec-Fetch-Site`, `Sec-Fetch-Dest`, `Priority`
- **Encoding**: `Accept-Encoding` (removed so providers can't fingerprint the accepted compression set)

After scrubbing, each executor adds back the native client's specific headers:

**Antigravity** (`internal/providers/antigravity/executor.go`):
```
X-Goog-Api-Client: google-genai-sdk/1.41.0 gl-node/v22.19.0
Accept-Encoding: gzip, deflate, br
```
The `X-Goog-Api-Client` value matches the Node.js Gemini SDK header the native VS Code extension sends. `Accept-Encoding` is Node.js's default (no `zstd`, which Electron adds and would be a fingerprint mismatch).

**Kiro** (`internal/providers/kiro/executor.go`):
```
Accept-Encoding: gzip, deflate, br
```
Matches `aws-sdk-js` running inside Electron's Node.js runtime.

### Layer 3 — Response model name rewriting (`internal/proxy/response_rewriter.go`)

The upstream sends its own internal model identifier (e.g. `anthropic.claude-sonnet-4-5-20250219-v1:0` from Kiro). The client requested `kr/claude-sonnet-4-6`. The proxy now rewrites the `"model"` field back to the client-requested name in both streaming and non-streaming responses.

**Streaming** (`server.go:streamResponse`): Uses `bufio.Scanner` to read the SSE body line-by-line (not fixed-size buffer chunks), so rewriting is never split across chunk boundaries. Each `data: {JSON}` line goes through `rewriteModelField`.

**Non-streaming** (`server.go:forwardResponseCapture`): Reads the full body, then calls `rewriteModelInBody`.

**Core rewrite** (`rewriteModelField`): Uses `map[string]json.RawMessage` to decode and re-encode only the `"model"` key, preserving all other fields and their exact raw values (no float/precision drift on nested objects).

---

## What was intentionally NOT changed

**System prompts are unchanged.** The `liamOverlay` system prompt in `internal/providers/kiro/translator.go` is preserved — Kiro requests still get the LIAM overlay that re-frames the model as a general assistant. Antigravity still has no system prompt injection. Mimicking the native system prompts was explicitly rejected.

---

## File map

| File | Status | Purpose |
|------|--------|---------|
| `internal/httputil/utls.go` | NEW | `NewUTLSClient` — Chrome TLS fingerprint transport |
| `internal/httputil/headers.go` | NEW | `ScrubUpstreamHeaders` — strips 24 proxy/fingerprint headers |
| `internal/httputil/headers_test.go` | NEW | Tests for scrubber |
| `internal/proxy/response_rewriter.go` | NEW | `rewriteModelField`, `rewriteModelInChunk`, `rewriteModelInBody` |
| `internal/proxy/response_rewriter_test.go` | NEW | 9 tests for rewriter (including CRLF and malformed JSON cases) |
| `internal/providers/antigravity/executor.go` | MODIFIED | uTLS client, scrubber, `X-Goog-Api-Client`, `Accept-Encoding` |
| `internal/providers/kiro/executor.go` | MODIFIED | uTLS client, scrubber, `Accept-Encoding` |
| `internal/proxy/server.go` | MODIFIED | `model` param on `streamResponse`/`forwardResponseCapture`; line-buffered SSE scanner |
| `go.mod` / `go.sum` | MODIFIED | Added `github.com/refraction-networking/utls v1.8.2` (+ `brotli`, `compress`, `crypto`) |

---

## Testing

### Unit tests

```bash
go test ./internal/httputil/...     # scrubber: 3 tests
go test ./internal/proxy/... -run TestRewrite   # rewriter: 9 tests
go build ./...                      # full build
```

### Manual smoke test

Start the server and send a streaming request to a Kiro model. The `"model"` field in every SSE chunk should reflect the requested model name (`kr/...`), not Kiro's internal `anthropic.claude-*` string.

To inspect raw upstream headers being sent, add a temporary `log.Printf` in `ExecuteWithSession` after `ScrubUpstreamHeaders` is called. Verify `X-Forwarded-For`, `X-Stainless-*`, `Sec-Ch-Ua` are absent, and `Accept-Encoding: gzip, deflate, br` is present.

To verify TLS fingerprinting is active, capture traffic with Wireshark and check the ClientHello — the cipher suite order and extensions should match Chrome, not Go's default.

---

## Architecture notes

**Package name**: `internal/httputil/` uses `package upstream` (not `package httputil`). This avoids shadowing stdlib's `net/http/httputil`. All callers import with alias: `upstream "github.com/liam-auto/liam/internal/httputil"`.

**No circular dependencies**: `internal/httputil` is a leaf package. Both `internal/providers/antigravity` and `internal/providers/kiro` import it. `internal/proxy` does not import provider packages directly (it uses the executor interfaces), so the response rewriter lives in `internal/proxy` without cycles.

**Chunk boundary safety**: The previous implementation called `rewriteModelInChunk` on raw 4 KB buffer reads. A single SSE `data:` line with a large content delta or tool call could exceed 4 KB and be silently split, causing the upstream model name to leak. The fix uses `bufio.Scanner` with a 256 KB line buffer — large enough for any realistic SSE event.
