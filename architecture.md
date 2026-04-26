# kkode Architecture

## Design thesis

`kkode` treats OpenAI Responses API as the base protocol shape for all providers because Responses has first-class item semantics:

```text
user/developer/system input -> response output items -> tool outputs -> next response
```

This is better for a coding-agent framework than plain chat messages because a coding agent must preserve:

- reasoning items
- function/custom tool calls
- tool outputs correlated by `call_id`
- built-in tool events
- provider raw items needed for future turns

## Package map

```text
llm/                 Provider-neutral contracts and loops
providers/openai/    OpenAI-compatible /v1/responses provider
providers/copilot/   GitHub Copilot SDK session adapter
providers/codexcli/  Codex CLI subprocess adapter
providers/omniroute/ OmniRoute gateway adapter
workspace/           Local workspace tools and safety boundaries
transcript/          JSON transcript persistence
research/            Investigation notes and TODOs
scripts/             Reproducible smoke tests
```

## Core abstractions

### Provider

```go
type Provider interface {
    Name() string
    Capabilities() Capabilities
    Generate(ctx context.Context, req Request) (*Response, error)
}
```

Use this for one-shot model generation.

### StreamProvider

```go
type StreamProvider interface {
    Provider
    Stream(ctx context.Context, req Request) (EventStream, error)
}
```

OpenAI SSE, Copilot session events, and Codex JSONL events are normalized into `llm.StreamEvent`.

### SessionProvider

```go
type SessionProvider interface {
    Provider
    NewSession(ctx context.Context, req SessionRequest) (Session, error)
}
```

Use this for provider runtimes where sessions matter: Copilot SDK, Codex app server, future Codex harness, long-running workspace agents.

## Request / response model

`llm.Request` intentionally supports both simple messages and raw item loops.

- `Messages` are ergonomic input.
- `InputItems` preserve provider output items and tool outputs.
- `Tools` are provider-neutral tool definitions.
- `Reasoning` carries effort/summary request knobs.
- `TextFormat` carries structured output settings.
- `PromptRef` supports future prompt-template provider APIs.

`llm.Response` contains:

- final `Text`
- raw `Output []Item`
- extracted `ToolCalls`
- extracted `Reasoning`
- `Usage`
- raw provider JSON

## Tool loop

`llm.RunToolLoop` is Responses-style:

1. Generate response.
2. If no tool calls, return final response.
3. Append raw provider output items to the next request.
4. Execute matching local tools from `ToolRegistry`.
5. Append `function_call_output` or `custom_tool_call_output`.
6. Repeat until final response or max iterations.

This avoids losing reasoning/tool-call state between turns.

## Provider boundaries

### OpenAI-compatible

`providers/openai` talks to `/v1/responses` over HTTP. It owns:

- request JSON mapping
- built-in tool mapping
- SSE parsing
- retry/backoff
- response item parsing

It does not own local tools; local tools stay in `llm.ToolRegistry` or `workspace`.


### OmniRoute

`providers/omniroute` wraps OmniRoute as a gateway provider. It delegates generation and streaming to the OpenAI-compatible `/v1/responses` surface, then adds OmniRoute-specific control-plane helpers:

- `ListModels` for `/v1/models` or OpenAPI `/api/v1/models`
- `Health` for `/api/monitoring/health`
- thinking-budget, fallback-chain, cache/rate/session, and translator helpers from `docs/openapi.yaml`
- `A2ASend` for JSON-RPC `message/send` on `/a2a`
- headers for sticky sessions, cache bypass, progress, and idempotency

The provider has two constructors because OmniRoute docs expose both external gateway-style `/v1` paths and OpenAPI `/api/v1` paths: `NewFromGatewayBase` and `NewFromOpenAPIServer`.

OmniRoute MCP integration should usually be modeled as an `llm.MCPServer` attached to session providers, while normal inference should use the OpenAI-compatible gateway path.

### GitHub Copilot SDK

Copilot SDK is not an OpenAI-compatible HTTP API. It is a session runtime backed by Copilot CLI JSON-RPC. The adapter owns:

- SDK client lifecycle
- session creation
- tool conversion
- MCP/custom agent/skill mapping
- event conversion
- permission defaulting

The current adapter is suitable for local/prototype app use. Production use needs OAuth GitHub App or BYOK design.

### Codex CLI

`providers/codexcli` wraps `codex exec --json` and reads the last assistant message or JSONL events. This is a local/dev provider, not a general OpenAI Platform API replacement.

Future Codex app-server/harness support should be implemented as a separate `providers/codexharness` package rather than overloading the CLI adapter.

## Workspace safety

The `workspace` package prevents path traversal by resolving all paths under a root. Approval policy gates:

- reads
- writes
- commands

Default vibe-coding app policy should be read-only first, then explicit write/shell allow rules.

## State

The `transcript` package persists request/response/error turns as JSON. A full app should layer session IDs, provider IDs, workspace snapshots, and tool audit logs on top.

## Provider selection

`llm.Router` supports `provider/model` naming, e.g.:

```text
openai/gpt-5-mini
copilot/gpt-5-mini
codex/gpt-5.3-codex
```

This keeps UI model selection simple and makes provider-specific model IDs explicit.

## Next architecture steps

- Add production-grade streaming aggregation.
- Add full Codex app-server/harness provider.
- Add OpenCode/OpenClaw local API providers as dev-only providers.
- Add policy/audit middleware around every tool call.
- Add persistent session manager and workspace snapshot support.
