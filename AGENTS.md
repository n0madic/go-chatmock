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

# Run server locally
./go-chatmock login
./go-chatmock serve --port 8000 --verbose
./go-chatmock info --json
```

## Server-Side API Compatibility

### Route-to-Handler Map

- `POST /v1/chat/completions` → `server.handleChatCompletions()` → `pipeline.Execute(..., "chat", ...)`
- `POST /v1/responses` → `server.handleResponses()`
  - When the request body contains an `input` field (native Responses API format), routes to `pipeline.ExecutePassthrough()` which bypasses normalization and sends the request upstream with minimal patching (model, store, instructions, reasoning). This preserves all SDK fields (metadata, custom tool formats, prompt_cache_retention, etc.).
  - Otherwise routes to `pipeline.Execute(..., "responses", ...)`
- `POST /v1/completions` → `server.handleTextCompletions()` (separate path, not unified pipeline)
- `POST /v1/messages` → `server.handleAnthropicMessages()` (Anthropic Messages API)
- `POST /api/chat` → `server.handleOllamaChat()` (Ollama-specific transform path)

### Response Format Routing Rule

**The response format is determined by the request format, NOT the URL route.** The `route` parameter only reflects the URL path. The actual response format (`req.ResponseFormat`) is derived from the request body:

- Request uses `input` field (Responses API shape) → response is **Responses API format** (SSE with `response.*` events or `ResponsesResponse` JSON)
- Request uses `messages` field (Chat Completions shape) → response is **Chat Completions format** (SSE with `chat.completion.chunk` or `ChatCompletionResponse` JSON)

This means a Responses API request sent to `/v1/chat/completions` receives a Responses API response, and a Chat Completions request sent to `/v1/responses` receives a Chat Completions response. The route is irrelevant — only the request body shape matters. This allows clients like Cursor that send `input` to any endpoint to always receive Responses API events back.

### Responses API Passthrough (`internal/pipeline/passthrough.go`)

When the `/v1/responses` route receives a request body with a top-level `input` field (detected by `pipeline.BodyHasInputField()`), the passthrough handler bypasses universal normalization entirely:
- Patches only `model`, `store`, `instructions`, and `reasoning` in the raw JSON map.
- Handles `previous_response_id` polyfill (local state) and conversation ID auto-resolution.
- Sends the patched body via `DoRaw()` preserving all other SDK fields (metadata, custom tools, prompt_cache_retention, include, etc.).
- Streams or collects the response using the same state-tracking logic as the normalized path.

This is the primary path for Cursor and other Responses API native clients.

Note: On the chat route (`/v1/chat/completions`), requests with `input` still go through normalization (which handles system-message extraction from input items, targeted function_call injection, server prompt fallback, etc.), but the **response format** is still set to Responses API based on input source — see the response format routing rule above.

### Unified Request Handling Principle

**Request processing is route-agnostic.** It does not matter which endpoint (`/v1/chat/completions` or `/v1/responses`) receives the request — the handling logic is unified and driven by the request body shape (`input` vs `messages`), not the URL path. The route only affects field priority when both are present:
- On `/v1/responses`: prefer `input`, fallback to `messages`.
- On `/v1/chat/completions`: prefer `messages`, fallback to `input`.

Instructions, tools, reasoning, and all other fields are processed identically regardless of route. The built-in server prompt is used as fallback on both routes when no client instructions are provided.

### Universal Normalization Rules (`internal/normalize/`)

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
- Instruction policy is unified across routes: client instructions take precedence; when empty and `previous_response_id` is present (responses route), prior stored instructions are inherited; otherwise the built-in server prompt (`InstructionsForModel`) is used as fallback.
- `conversation_id` / `conversationId` / `cursorConversationId` can be used to auto-resolve latest `previous_response_id` from local state.

### Local Tool-Loop Polyfill (`internal/state/polyfill.go`)

- `previous_response_id` is resolved locally from in-memory state store.
- Missing `function_call` items are reconstructed when only `function_call_output` is provided.
- For `/v1/responses`, prior context is prepended when needed.
- Unknown/expired response IDs or unresolved `call_id` values return descriptive `400`.
- State is process-local and reset on restart.

### Upstream Endpoint Constraints (ChatGPT backend-api/codex/responses)

The upstream ChatGPT Codex endpoint differs from the official OpenAI Platform API in several ways that the proxy must account for:

- **`store` must be `false`:** The upstream endpoint returns HTTP 400 (`"Store must be set to false"`) for any other value, including when omitted. `NormalizeStoreForUpstream()` in `state/polyfill.go` always forces `store=false` before forwarding. When a client sends `store=true`, the proxy logs a warning but silently strips it.
- **`previous_response_id` is local-only:** The upstream endpoint does not support this parameter. The proxy resolves it from the in-memory state store (`state.Store`) and prepends prior context inline in `input`. This means continuity is process-local and reset on restart.
- **Upstream response ID references (`rs_…`) are not reusable across calls:** The ChatGPT endpoint does not support referencing upstream item IDs in subsequent requests. Clients should include content inline or rely on the proxy's local `previous_response_id` polyfill for conversation threading.
- **`responses_tools` is intentionally restricted** to web-search variants (`web_search`, `web_search_preview`).
- For `/v1/responses`, text-only system messages are moved into `instructions` for upstream compatibility.

### Debug/Diagnostics Behavior

- With `--debug`, server prints explicit dump boundaries for inbound request and upstream response blocks.
- For upstream SSE, debug body dump is intentionally reduced to `response.completed`.

## Streaming and Tools Behavior

### Chat Streaming (`internal/codec/openai_chat.go`)

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

### Responses Streaming (`internal/codec/openai_responses.go`)

- Forwards upstream SSE events as-is (plus final `data: [DONE]`).
- State extraction (response ID, output items, tool calls) is performed by the pipeline after streaming completes via a TeeReader.

## Architecture

### Request Processing Pipeline

```
HTTP request → server/ (routing, body read, auth middleware)
            → pipeline/ (orchestration)
                → normalize/ (decode + enrich into CanonicalRequest)
                → upstream/ (SDK param building, HTTP send)
                → codec/ (stream translation or collected response encoding)
            → state/ (post-response state storage for continuity)
```

For passthrough requests (Responses API with `input` field):
```
HTTP request → server/ → pipeline/passthrough (minimal JSON patching)
                       → upstream/DoRaw (pre-built JSON)
                       → codec/ (stream/collect)
                       → state/
```

### Package Responsibilities

| Package | Role |
|---------|------|
| `server/` | HTTP server, mux routing, CORS middleware, auth enforcement, health/model endpoints, Anthropic/Ollama handlers. |
| `pipeline/` | Orchestrates the decode → normalize → upstream → translate → encode flow. `Execute()` for normalized path, `ExecutePassthrough()` for Responses API native path. State extraction after streaming. |
| `normalize/` | Decodes raw request body into `CanonicalRequest`. Handles input source precedence, tool normalization, instruction policy, conversation/response ID resolution, store normalization. |
| `codec/` | Format-specific `Encoder` implementations (Chat, Responses, Text, Anthropic, Ollama). Each provides stream headers, `Translator` for SSE translation, collected response writing, and error formatting. Anthropic codec includes tool input extraction helpers inlined from the former `anthropic/` package. |
| `stream/` | SSE `Reader` (line-based parser), `ToolBuffer` for argument accumulation, `CollectTextFromSSE` collector, usage extraction (`ExtractUsageFromEvent`, `Int64FromAny`), and helpers (`StringOr`, `ResponseIDFromEvent`). |
| `upstream/` | Builds and sends Codex Responses API requests. `Do()` converts custom types to `openai-go/v3` SDK params via `sdkcompat.go`; `DoRaw()` forwards pre-built JSON. `DoWithRetry()` handles upstream 4xx retries with web-search tool stripping. |
| `state/` | In-memory LRU store for previous-response snapshots, function-call index, instructions, and conversation→response mapping (TTL/capacity). `polyfill.go` restores function_call context for tool-loop continuity. |
| `types/` | Shared request/response structs across OpenAI/Ollama/Responses/Anthropic shapes. `CanonicalRequest` (unified normalized request). Pointer helpers (`StringPtr`, `BoolPtr`). |
| `transform/` | Message/tool conversions between client-facing schemas and Responses input (Anthropic messages→input items, Chat messages→input items, tool format conversions). |
| `models/` | Model registry, alias normalization, reasoning-variant exposure, Anthropic model mapping. |
| `reasoning/` | Effort/summary normalization and chat output formatting for compat modes (think-tags, o3, legacy). |
| `auth/` | Auth persistence, token refresh, JWT decoding. |
| `config/` | Runtime flags/env configuration, prompt selection, Codex client headers. |
| `session/` | Deterministic prompt-session mapping for upstream caching hints. |
| `limits/` | Parses/persists usage limit headers. |
| `oauth/` | Browser OAuth callback server and PKCE flow. |

## Development Notes

- Prefer touching `internal/normalize/` for chat/responses behavior changes; avoid duplicating logic across route handlers.
- When changing tools or streaming logic, update both codec stream translators and any related tests.
- SDK type conversions (`openai-go/v3` param types) live in `upstream/sdkcompat.go` — this is the only file that imports `openai-go` SDK param types for request building.
- Anthropic tool input helpers (`extractToolInputFromMap`, `functionCallItemKeys`, `bufferedToolInput`) are private to `codec/anthropic.go` — they are used only by the Anthropic stream translator.
- Usage extraction from SSE events (`stream.ExtractUsageFromEvent`) is used by all codec translators and the pipeline collector.
- Model validation is performed against dynamic registry unless `--debug-model` is set.
- Auth storage and env vars are shared with sibling implementations (`~/.chatgpt-local/auth.json`), so behavior changes can affect multi-client setups.
- Prompts in `prompts/` are sensitive system instructions injected upstream; do not change without maintainer approval.
