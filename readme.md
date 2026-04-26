# kkode

`kkode` is a Go foundation for a vibe-coding app that can route coding-agent requests across multiple model providers while keeping a common tool, prompt, response, auth, model, and session vocabulary.

The compatibility baseline is **OpenAI Responses API semantics** rather than legacy chat-completions semantics. That means tool calls, tool outputs, reasoning items, and messages are represented as typed items and can be preserved across tool-loop turns.

## Current status

Implemented:

- Core `llm` package
  - `Provider`, `StreamProvider`, `SessionProvider`
  - `Request`, `Response`, `Message`, `Item`
  - `Tool`, `ToolCall`, `ToolResult`, `ToolRegistry`
  - `ReasoningConfig`, `ReasoningItem`
  - `TextFormat` for structured output
  - `Auth`, `Model`, `ModelRegistry`, usage cost estimation
  - prompt templates, provider router, approval policy, tool loop
- `providers/openai`
  - OpenAI-compatible `/v1/responses` client
  - tool/function/custom tool mapping
  - built-in tools helpers: web search, file search, MCP, computer use, code interpreter, image generation
  - reasoning/tool/message parsing
  - SSE streaming parser
  - retry/backoff for 429/5xx
  - gated live test if `OPENAI_API_KEY` is set
- `providers/copilot`
  - GitHub Copilot SDK adapter
  - session provider support
  - streaming event adapter
  - custom tool adapter
  - MCP/custom agent/skill config mapping
- `providers/codexcli`
  - Codex CLI `codex exec --json` adapter
  - JSONL event stream parsing
  - smoke test script for ChatGPT/Codex-account Codex models
- `providers/omniroute`
  - OmniRoute OpenAI-compatible `/v1/responses` adapter
  - OmniRoute session/cache/idempotency/progress headers
  - model listing, health check, and A2A `message/send` helper
- `workspace`
  - sandboxed workspace file/list/search tools
  - command execution with approval policy
- `transcript`
  - JSON transcript persistence

## Install / test

```bash
go test ./...
go vet ./...
```

Additional smoke checks:

```bash
./scripts/verify-go-examples.sh
./scripts/copilot-smoke.sh gpt-5-mini
./scripts/copilot-tool-smoke.sh gpt-5-mini
./scripts/codexcli-smoke.sh gpt-5.3-codex
./scripts/omniroute-smoke.sh   # skips if OmniRoute is not running
```

OpenAI live test is skipped unless `OPENAI_API_KEY` is set:

```bash
OPENAI_API_KEY=... OPENAI_TEST_MODEL=gpt-5-mini go test ./providers/openai -run Live
```

## OpenAI-compatible usage

```go
client := openai.New(openai.Config{APIKey: os.Getenv("OPENAI_API_KEY")})
resp, err := client.Generate(ctx, llm.Request{
    Model: "gpt-5-mini",
    Instructions: "You are a coding assistant.",
    Messages: []llm.Message{llm.UserText("Create a plan")},
    Reasoning: &llm.ReasoningConfig{Effort: "medium", Summary: "auto"},
})
```

Tool loop:

```go
registry := llm.ToolRegistry{
    "echo": llm.JSONToolHandler(func(ctx context.Context, in struct{ Text string `json:"text"` }) (string, error) {
        return in.Text, nil
    }),
}
resp, err := llm.RunToolLoop(ctx, client, req, registry, llm.ToolLoopOptions{MaxIterations: 8})
```

## Provider routing

```go
router := llm.NewRouter()
router.Register("openai", openai.New(openai.Config{APIKey: key}))
router.Register("copilot", copilot.New(copilot.Config{}))
router.Register("codex", codexcli.New(codexcli.Config{Ephemeral: true}))
router.Register("omniroute", omniroute.New(omniroute.Config{BaseURL: "http://localhost:20128/v1"}))

resp, err := router.Generate(ctx, llm.Request{
    Model: "openai/gpt-5-mini",
    Messages: []llm.Message{llm.UserText("hello")},
})
```

## Docs

- [`architecture.md`](architecture.md) — implementation architecture and provider boundaries.
- [`research/08-omniroute-provider.md`](research/08-omniroute-provider.md) — OmniRoute API/MCP/A2A research and adapter notes.
- [`research/`](research/) — source-backed research notes and implementation TODOs.

## Commit policy

This repo follows the requested workflow: after meaningful completed work, run verification, commit with a lore-style message, and push to `origin/main`.
