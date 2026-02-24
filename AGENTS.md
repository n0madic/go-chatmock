# AGENTS.md

Technical guidance for coding agents working in this repository.

## Project Overview

go-chatmock is a Go proxy server that exposes OpenAI-compatible (`/v1/*`), Anthropic-compatible (`/v1/messages*`), and Ollama-compatible (`/api/*`) endpoints backed by authenticated ChatGPT access.

The upstream is the Codex backend Responses API (`chatgpt.com/backend-api/codex/responses`) over SSE. Because upstream is not the public OpenAI API directly, some compatibility deviations are intentional and must be documented/tests-backed.

Requires Go 1.24+.

## Core Commands

```bash
# Build
go build -o go-chatmock .

# Full test suite
go test ./... -count=1

# API compatibility suites (proxy behavior)
go test ./internal/proxy -run OpenAIClientCompat -count=1
go test ./internal/proxy -run OpenAIGoSDKSmoke -count=1

# Streaming/tools stress suites
go test ./internal/sse -run 'TranslateChatArgs|TranslateChatStress' -count=1

# Run server locally
./go-chatmock login
./go-chatmock serve --port 8000 --verbose
./go-chatmock info --json
```

## Server-Side API Compatibility

### Route-to-Handler Map

- `POST /v1/chat/completions` -> `handleChatCompletions()` -> `handleUnifiedCompletions(..., universalRouteChat)`
- `POST /v1/responses` -> `handleResponses()` -> `handleUnifiedCompletions(..., universalRouteResponses)`
- `POST /v1/completions` -> `handleCompletions()` (separate path, not unified)
- `POST /api/chat` -> `handleOllamaChat()` (Ollama-specific transform path)

The `route` parameter reflects the URL path. The **response format** is determined separately by `req.ResponseFormat`, which is derived from the request body: if the body uses `input` (Responses API shape), the response is Responses API format; if it uses `messages` (Chat shape), the response is Chat Completions format. This allows clients like Cursor that send `input` to `/v1/chat/completions` to receive Responses API events back.

### Universal Normalization Rules (`internal/proxy/universal.go`)

- Body is decoded into both Chat and Responses request structs from the same raw JSON.
- Input source precedence:
  - On chat route: prefer `messages`, fallback to valid `input`, then fallback to `prompt`.
  - On responses route: prefer `input`, fallback to valid `messages`, then fallback to `prompt`.
- Tool normalization accepts multiple shapes:
  - Chat tools: `{"type":"function","function":{...}}`
  - Responses tools: `{"type":"function","name":"...","parameters":...}`
  - Custom tools: `{"type":"custom","name":"...","format":...}` (e.g. Cursor's `ApplyPatch` with grammar-based format)
- Tool selection preference follows `ResponseFormat`: when the request uses `input` (Responses API format), Responses-style tool parsing is preferred, which supports `custom` tool types that Chat format cannot represent.
- `responses_tools` is additive and currently supports only `web_search` / `web_search_preview`.
- `tool_choice` and `parallel_tool_calls` are normalized from either schema.
- System text from input/messages is folded into `instructions` when possible.
- Route-specific instruction policy:
  - chat route: client instructions take precedence; otherwise server prompt is used.
  - responses route: client instructions are passed through; when empty and `previous_response_id` is present, prior stored instructions can be inherited.
- `conversation_id` / `conversationId` / `cursorConversationId` can be used to auto-resolve latest `previous_response_id` from local state.

### Local Tool-Loop Polyfill (`internal/proxy/responses_polyfill.go`)

- `previous_response_id` is resolved locally from in-memory `responses-state`.
- Missing `function_call` items are reconstructed when only `function_call_output` is provided.
- For `/v1/responses`, prior context is prepended when needed.
- Unknown/expired response IDs or unresolved `call_id` values return descriptive `400`.
- State is process-local and reset on restart.

### Intentional Compatibility Deviations

- Upstream `store` is always sent as `false` (`normalizeStoreForUpstream`), because Codex backend may reject `store:true`.
- `previous_response_id` is not forwarded upstream; continuity is local/in-memory.
- Best-effort auto-continuation via conversation metadata is proxy-specific behavior.
- `responses_tools` is intentionally restricted to web-search variants.
- For `/v1/responses`, text-only system messages are moved into `instructions` for upstream compatibility.

### Debug/Diagnostics Behavior

- With `--debug`, server prints explicit dump boundaries for inbound request and upstream response blocks.
- For upstream SSE, debug body dump is intentionally reduced to `response.completed`.

## Streaming and Tools Behavior

### Chat Streaming (`internal/sse/translate_chat.go`)

- Converts upstream Responses SSE into OpenAI chat completion chunks.
- Reconstructs tool arguments from:
  - `response.function_call_arguments.delta`
  - `response.function_call_arguments.done`
  - `response.output_item.done` fallback args
- Handles interleaving/out-of-order events:
  - multiple concurrent tool calls
  - argument deltas arriving before `output_item.added`
  - text deltas interleaved with tool deltas
  - web_search call events interleaved with function calls
- Emits `finish_reason: "tool_calls"` for tool-call turns.
- Filters out commentary-phase hidden text in chat output.

### Responses Streaming (`internal/proxy/responses.go`)

- `streamResponsesWithState()` forwards upstream SSE events (plus final `data: [DONE]`).
- In parallel, captures output items/tool calls for local state continuity.
- Non-streaming collector assembles output from `response.output_item.done` events and merges usage from created/completed events.

## Package Responsibilities

| Package | Role |
|---------|------|
| `proxy/` | HTTP server, middleware, route handlers, unified chat/responses normalization. |
| `upstream/` | Builds and sends Codex Responses API requests. |
| `sse/` | SSE reader and stream translators for chat/text/ollama (responses translator kept for tests/utility). |
| `responses-state/` | In-memory previous-response snapshots, function-call index, instructions, conversation mapping (TTL/capacity). |
| `transform/` | Message/tool conversions between client-facing schemas and Responses input. |
| `types/` | Shared request/response structs across OpenAI/Ollama/Responses/Anthropic shapes. |
| `models/` | Model registry, alias normalization, reasoning-variant exposure. |
| `reasoning/` | Effort/summary normalization and chat output formatting for compat modes. |
| `auth/` | Auth persistence and token refresh. |
| `config/` | Runtime flags/env configuration and prompt selection. |
| `session/` | Deterministic prompt-session mapping for upstream caching hints. |
| `limits/` | Parses/persists usage limit headers. |
| `oauth/` | Browser OAuth callback server and PKCE flow. |

## Development Notes

- Prefer touching `internal/proxy/universal.go` for chat/responses behavior changes; avoid duplicating logic across route handlers.
- When changing tools or streaming logic, update both:
  - proxy compatibility tests in `internal/proxy/*compat*_test.go`
  - SSE argument/interleaving tests in `internal/sse/translate_chat_*_test.go`
- Official SDK smoke coverage lives in `internal/proxy/openai_go_sdk_smoke_test.go` and should stay green for standard OpenAI Go client usage.
- Model validation is performed against dynamic registry unless `--debug-model` is set.
- Auth storage and env vars are shared with sibling implementations (`~/.chatgpt-local/auth.json`), so behavior changes can affect multi-client setups.
- Prompts in `prompts/` are sensitive system instructions injected upstream; do not change without maintainer approval.
