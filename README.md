# go-chatmock

Go implementation of [ChatMock](https://github.com/RayBytes/ChatMock) — a local proxy server that exposes OpenAI and Ollama compatible APIs powered by your authenticated ChatGPT account.

Requires a paid ChatGPT account. Uses the Codex OAuth client to authenticate and forwards requests to ChatGPT's backend Responses API, translating between API formats on the fly.

## Installation

```bash
go install github.com/n0madic/go-chatmock@latest
```

## Build

```bash
go build -o go-chatmock .
```

Requires Go 1.24+.

## Usage

### Login

Authenticate with your ChatGPT account via browser-based OAuth:

```bash
./go-chatmock login
```

Tokens are saved to `~/.chatgpt-local/auth.json`. You can verify with:

```bash
./go-chatmock info
./go-chatmock info --json
```

If the browser can't reach the machine (e.g. running over SSH), paste the full redirect URL into the terminal when prompted.

Use `--no-browser` to skip auto-opening the browser:

```bash
./go-chatmock login --no-browser
```

### Serve

Start the proxy server:

```bash
./go-chatmock serve
```

The server listens on `http://127.0.0.1:8000` by default.

```bash
./go-chatmock serve --host 0.0.0.0 --port 9000 --verbose
./go-chatmock serve --debug
./go-chatmock serve --reasoning-effort high --reasoning-summary detailed
./go-chatmock serve --expose-reasoning-models --enable-web-search
./go-chatmock serve --access-token my-local-token
```

| Flag | Default | Description |
|------|---------|-------------|
| `--host` | `127.0.0.1` | Bind address |
| `--port` | `8000` | Listen port |
| `--verbose` | `false` | Log structured request/upstream summaries |
| `--debug` | `false` | Dump inbound HTTP requests (method, path, headers, body) |
| `--access-token` | | Require `Authorization: Bearer <token>` on API routes (except `/` and `/health`) |
| `--reasoning-effort` | `medium` | Default reasoning effort (`minimal`, `low`, `medium`, `high`, `xhigh`) |
| `--reasoning-summary` | `auto` | Reasoning summary mode (`auto`, `concise`, `detailed`, `none`) |
| `--reasoning-compat` | `think-tags` | Reasoning output format (`think-tags`, `o3`, `legacy`) |
| `--debug-model` | | Force a specific model name for all requests |
| `--expose-reasoning-models` | `false` | Expose effort-level variants as separate models (e.g. `gpt-5-high`) |
| `--enable-web-search` | `false` | Enable web search tool by default |

All flags can also be set via environment variables:

| Environment Variable | Flag Equivalent |
|---|---|
| `CHATGPT_LOCAL_REASONING_EFFORT` | `--reasoning-effort` |
| `CHATGPT_LOCAL_REASONING_SUMMARY` | `--reasoning-summary` |
| `CHATGPT_LOCAL_REASONING_COMPAT` | `--reasoning-compat` |
| `CHATGPT_LOCAL_DEBUG` | `--debug` |
| `CHATGPT_LOCAL_ACCESS_TOKEN` | `--access-token` |
| `CHATGPT_LOCAL_DEBUG_MODEL` | `--debug-model` |
| `CHATGPT_LOCAL_EXPOSE_REASONING_MODELS` | `--expose-reasoning-models` |
| `CHATGPT_LOCAL_ENABLE_WEB_SEARCH` | `--enable-web-search` |
| `CHATGPT_LOCAL_CLIENT_ID` | OAuth client ID override |
| `CHATGPT_LOCAL_HOME` / `CODEX_HOME` | Auth storage directory (default `~/.chatgpt-local`) |
| `CHATGPT_LOCAL_LOGIN_BIND` | Bind address for login callback server |

## API Endpoints

### OpenAI-compatible

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/chat/completions` | Chat completions (streaming and non-streaming) |
| `POST` | `/v1/completions` | Text completions |
| `POST` | `/v1/responses` | Responses API (streaming and non-streaming) |
| `GET` | `/v1/models` | List available models |

### Anthropic-compatible (Claude Code gateway)

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/messages` | Anthropic Messages API (streaming and non-streaming) |
| `POST` | `/v1/messages/count_tokens` | Approximate local token count |
| `GET` | `/v1/models` | Anthropic model list schema when `anthropic-version` header is present |

### Ollama-compatible

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/chat` | Ollama chat |
| `GET` | `/api/tags` | List models |
| `POST` | `/api/show` | Model info |
| `GET` | `/api/version` | Ollama version |

### Other

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/` | Health check |
| `GET` | `/health` | Health check |

## Supported Models

- `codex-mini`
- `gpt-5`
- `gpt-5-codex`
- `gpt-5.1`
- `gpt-5.1-codex`
- `gpt-5.1-codex-max`
- `gpt-5.1-codex-mini`
- `gpt-5.2`
- `gpt-5.2-codex`
- `gpt-5.3-codex`

With `--expose-reasoning-models`, each model also exposes effort-level variants (e.g. `gpt-5-high`, `gpt-5.2-xhigh`).

## Example

```bash
curl http://127.0.0.1:8000/v1/chat/completions \
  -H "Authorization: Bearer anything" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5",
    "messages": [{"role": "user", "content": "Hello!"}],
    "stream": false
  }'
```

When `--access-token` is not set, the `Authorization` header value is ignored and authentication uses stored ChatGPT tokens.
When `--access-token` is set, all API routes except `/` and `/health` require `Authorization: Bearer <token>`.

## Features

- **Streaming and non-streaming** responses for both OpenAI and Ollama formats
- **Anthropic Messages API gateway** for Claude Code (`/v1/messages`, `/v1/messages/count_tokens`, `/v1/models` dual schema)
- **Responses API support** (`/v1/responses`) including local tool-loop continuity
- **Tool/function calling** support with automatic format translation
- **Vision/image** support (base64 images in Ollama format are converted automatically)
- **Reasoning effort** control per-request or globally via server flags
- **Reasoning summaries** in three compat modes: `think-tags` (wrapped in `<think>` tags), `o3` (structured reasoning object), `legacy` (separate fields)
- **Web search** passthrough via `responses_tools` field
- **Session-based prompt caching** using deterministic SHA256 fingerprints
- **Local `previous_response_id` polyfill** for `/v1/responses` tool loops:
  go-chatmock stores reconstructed input context and tool calls in memory
  (TTL 60 minutes, max 10k responses), replays prior context for chained turns,
  and re-injects missing `function_call` items when clients send only `function_call_output`
- **Automatic token refresh** with thread-safe management
- **Rate limit tracking** — usage snapshots saved to `~/.chatgpt-local/usage_limits.json`, viewable via `info`
- **CORS** enabled for all origins

For Anthropic-compatible routes, include:

- `anthropic-version: 2023-06-01` (required)
- auth header (required; validated only for presence):
  `x-api-key: <any non-empty value>` or `Authorization: Bearer <token>`

If `--access-token` (or `CHATGPT_LOCAL_ACCESS_TOKEN`) is set, a matching
`Authorization: Bearer <token>` is required before route-specific validation.

`previous_response_id` is handled locally and not forwarded upstream. The in-memory
state is cleared on process restart. If a tool loop references an unknown/expired
response ID or missing `call_id`, the proxy returns a descriptive `400` error.

For `/v1/responses`, client `instructions` are passed through as-is, inherited across
`previous_response_id` chains, and text-only `input` messages with `role: "system"`
are moved into `instructions` for upstream compatibility.

## Architecture

```
main.go                    CLI entry point (login, serve, info)
internal/
  auth/                    Auth file I/O, JWT parsing, OAuth2 config, token refresh
  config/                  Server configuration, environment defaults
  models/                  Model catalog, alias mapping, effort-level variants
  oauth/                   OAuth callback server (port 1455), PKCE via golang.org/x/oauth2
  proxy/                   HTTP server, CORS middleware, OpenAI + Ollama route handlers
  reasoning/               Reasoning effort/summary building, compat mode formatting
  responses-state/          In-memory previous_response_id polyfill state (TTL + capacity)
  session/                 Deterministic session ID cache (SHA256 + UUID, LRU 10k entries)
  sse/                     SSE reader, chat/text/Ollama stream translators
  transform/               Message format conversion (Chat → Responses API, Ollama → OpenAI)
  upstream/                Responses API client (POST to chatgpt.com backend)
  limits/                  Rate limit header parsing, JSON persistence
```

System instruction prompts (`prompts/prompt.md`, `prompts/prompt_gpt5_codex.md`) are embedded into the binary at compile time via `go:embed`.

## Tests

```bash
go test ./...
```

Packages with tests include: `auth`, `models`, `proxy`, `responses-state`, `sse`, `transform`, `types`, `upstream`.

## Interoperability

The Go and Python implementations share the same auth storage (`~/.chatgpt-local/auth.json`) and configuration environment variables. They are drop-in replacements for each other.
