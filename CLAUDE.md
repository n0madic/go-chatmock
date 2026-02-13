# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

go-chatmock is a Go proxy server that exposes OpenAI and Ollama-compatible APIs backed by a user's authenticated ChatGPT account. It authenticates via the Codex OAuth client, then translates incoming API requests into ChatGPT's backend Responses API (`chatgpt.com/backend-api/codex/responses`) SSE stream, and translates the responses back into the client's expected format.

## Commands

```bash
# Build
go build -o go-chatmock .

# Run all tests
go test ./...

# Run a single package's tests
go test ./internal/transform/
go test ./internal/auth/
go test ./internal/models/
go test ./internal/sse/

# Run a specific test
go test ./internal/transform/ -run TestChatMessagesToResponsesInput

# Login and serve
./go-chatmock login
./go-chatmock serve --port 8000 --verbose --reasoning-effort high --reasoning-summary detailed
./go-chatmock info
```

Requires Go 1.24+.

## Architecture

### Request Flow

```
Client Request (OpenAI or Ollama format)
  → proxy/ route handler (openai.go or ollama.go)
    → models.NormalizeModelName()          # alias resolution, effort suffix stripping
    → transform.ChatMessagesToResponsesInput()  # or ConvertOllamaMessages()
    → transform.ToolsChatToResponses()     # tool format conversion
    → reasoning.BuildReasoningParam()      # merge server defaults + per-request overrides
    → upstream.Client.Do()                 # POST to ChatGPT backend as SSE stream
      → session.EnsureSessionID()          # deterministic SHA256-based prompt cache key
      → auth.TokenManager.GetEffectiveAuth()  # thread-safe token with auto-refresh
    → sse.TranslateChat/Text/Ollama()      # stream translation back to client format
    → limits.RecordFromResponse()          # persist rate limit headers
```

### Key Design Patterns

- **Dual API surface**: Both OpenAI (`/v1/...`) and Ollama (`/api/...`) routes are registered on the same `ServeMux`. Both converge into the same `upstream.Client.Do()` call — the only difference is input/output format translation.
- **SSE stream translation**: Upstream always returns an SSE stream. The `sse/` package reads it event-by-event and re-emits in the target format. Three translators exist: `TranslateChat` (OpenAI chat), `TranslateText` (OpenAI completions), `TranslateOllama` (Ollama NDJSON).
- **Reasoning compat modes**: Reasoning output is formatted in one of three modes (`think-tags`, `o3`, `legacy`) controlled by `--reasoning-compat`. The streaming translator in `sse/translate_chat.go` handles each mode differently for `<think>` tag wrapping, structured reasoning objects, or legacy fields.
- **Web search retry**: If upstream rejects `responses_tools` (web_search), routes automatically retry without them.
- **Embedded prompts**: `prompts/prompt.md` and `prompts/prompt_gpt5_codex.md` are embedded at compile time via `go:embed` in `main.go` and passed into `config.ServerConfig`. `InstructionsForModel()` selects the appropriate prompt.
- **Thread-safe token management**: `auth.TokenManager` uses `sync.Mutex` for concurrent token access and automatic refresh.
- **Session caching**: `session/` maintains an in-memory LRU cache (max 10,000 entries) mapping SHA256 fingerprints of instructions+first-user-message to UUID session IDs for prompt caching.

### Package Responsibilities

| Package | Role |
|---------|------|
| `types/` | All shared request/response structs (OpenAI, Ollama, Responses API, errors). No logic. |
| `proxy/` | HTTP server, CORS middleware, route handlers. `openai.go` and `ollama.go` are the main entry points. |
| `upstream/` | Single function `Client.Do()` that builds and sends the Responses API payload. |
| `sse/` | SSE line reader + three stream translators. |
| `transform/` | Format converters: Chat→Responses, Ollama→OpenAI, tool schemas. |
| `models/` | Model catalog, alias mapping, effort-level variant generation, allowed efforts per model. |
| `reasoning/` | Reasoning param construction, effort extraction from model names, compat mode formatting for non-streaming responses. |
| `auth/` | Auth file I/O (`~/.chatgpt-local/auth.json`), JWT parsing (no verification), `TokenManager` for thread-safe access + refresh. |
| `config/` | `ServerConfig` struct, env var defaults, prompt selection. |
| `session/` | Deterministic session ID generation with LRU cache. |
| `limits/` | Rate limit header parsing (`x-codex-*`), JSON persistence to `usage_limits.json`. |
| `oauth/` | OAuth callback server on port 1455, PKCE flow, code exchange. |

## Development Notes

- System messages in incoming requests are converted to user messages before forwarding upstream (`convertSystemToUser` in `proxy/openai.go`).
- `ChatMessage.Content` is `any` (can be `string` or `[]any` for multimodal), requiring type-switch handling throughout `transform/`.
- OpenAI and Ollama routes must stay in sync — changes to shared behavior (reasoning, tools, streaming) must be applied to both `openai.go` and `ollama.go`.
- This Go implementation shares auth storage (`~/.chatgpt-local/auth.json`) and env vars with the sibling Python implementation in the parent directory — they are interchangeable.
- The `prompts/` files are sensitive system instructions injected into every upstream request. Do not modify without maintainer approval.
