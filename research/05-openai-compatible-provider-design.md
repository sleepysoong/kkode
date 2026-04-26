# 05. OpenAI API 기반 multi-provider 설계 조사 및 구현 메모

작성일: 2026-04-26

## 목표

Go로 vibe-coding 앱을 만들 때 여러 provider를 같은 상위 인터페이스로 붙일 수 있게 한다.

이번 구현의 기준은 **OpenAI Responses API compatible** 이다. 이유:

- Responses API는 input item, output item, tool call/output, reasoning item을 한 모델로 묶는다.
- tool call과 tool output이 `call_id`로 연결되므로 provider-agnostic tool loop 설계에 맞다.
- reasoning 모델은 reasoning item을 다음 턴에 보존해야 하는 경우가 있어, 단순 chat message 배열보다 item 기반 추상화가 유리하다.
- structured output은 `text.format` 기반으로 표현할 수 있다.
- OpenAI-compatible server나 proxy를 만들 때 `/v1/responses`를 기준으로 삼으면 Codex/Copilot/기타 provider adapter가 붙기 쉽다.

## 공식 문서에서 확인한 핵심

### Responses API가 기준이 되어야 하는 이유

OpenAI migration 문서는 Responses에서 tool call과 tool output이 서로 다른 item이고 `call_id`로 연결된다고 설명한다. 따라서 LangChain식 message-only loop보다 item-preserving loop가 필요하다.

### tool/function calling

OpenAI function calling 문서 기준:

- tool은 `type: "function"`, `name`, `description`, `parameters` JSON Schema로 정의한다.
- strict mode를 켜면 schema를 더 안정적으로 따르게 할 수 있다.
- strict mode에서는 object마다 `additionalProperties: false`가 필요하고 모든 properties가 required여야 한다.
- parallel function calling은 한 턴에서 여러 tool call을 만들 수 있다.
- `parallel_tool_calls: false`로 0개 또는 1개 tool call만 허용할 수 있다.

### reasoning / thinking

Reasoning 모델은 보통 다음 필드가 중요하다.

- request: `reasoning.effort` — `low`, `medium`, `high`, 일부 SDK/runtime에서는 `xhigh` 등 provider별 확장이 있을 수 있음.
- request: `reasoning.summary` — summary 생성 요청.
- response: `output` 안의 `reasoning` item.
- response usage: `output_tokens_details.reasoning_tokens`.

구현상 중요한 점:

- reasoning text를 사용자에게 그대로 노출하는 provider는 드물다.
- 하지만 reasoning item/raw item은 다음 request context 유지에 필요할 수 있다.
- 그래서 `llm.Item.ProviderRaw`에 provider raw JSON을 보존한다.

### structured output

Responses API는 structured output을 `text.format` 아래에 둔다. 이 repo의 `llm.TextFormat`은 다음을 표현한다.

- `json_schema`
- `json_object`
- provider-specific format

## 구현된 추상화

### `llm.Provider`

```go
type Provider interface {
    Name() string
    Capabilities() Capabilities
    Generate(ctx context.Context, req Request) (*Response, error)
}
```

### 주요 타입

- `llm.Model` / `llm.ModelRegistry`
  - provider별 model ID, capability, context window, pricing metadata를 분리해 저장한다.
- `llm.Request`
  - `Model`
  - `Instructions`
  - `Messages`
  - `InputItems`
  - `Tools`
  - `ToolChoice`
  - `Reasoning`
  - `TextFormat`
  - `PreviousResponseID`
  - `Store`
  - `ParallelToolCalls`
- `llm.Response`
  - `Text`
  - `Output []Item`
  - `ToolCalls []ToolCall`
  - `Reasoning []ReasoningItem`
  - `Usage`
  - `Raw`
- `llm.Item`
  - `message`
  - `function_call`
  - `function_call_output`
  - `custom_tool_call`
  - `custom_tool_call_output`
  - `reasoning`
  - `unknown`

### Tool loop

`llm.RunToolLoop`는 OpenAI Responses 스타일로 동작한다.

1. provider 호출.
2. response의 `ToolCalls` 확인.
3. tool call이 없으면 최종 response 반환.
4. tool call이 있으면 response의 raw output item을 다음 request `InputItems`에 보존.
5. local `ToolRegistry`에서 tool 실행.
6. `function_call_output` 또는 `custom_tool_call_output` item 추가.
7. 반복.

이 방식은 reasoning item과 tool call item을 잃지 않으므로 reasoning 모델/provider adapter에 유리하다.

## 구현된 OpenAI-compatible provider

패키지: `providers/openai`

특징:

- stdlib `net/http`만 사용.
- 기본 base URL: `https://api.openai.com/v1`.
- `Config.BaseURL`로 OpenAI-compatible endpoint 지정 가능.
- `/responses` endpoint 호출.
- request builder: `BuildResponsesRequest`.
- response parser: `ParseResponsesResponse`.
- function/custom tool, tool_choice, reasoning, text.format, usage parsing 지원.

예시:

```go
client := openai.New(openai.Config{APIKey: os.Getenv("OPENAI_API_KEY")})
resp, err := client.Generate(ctx, llm.Request{
    Model: "gpt-5-mini",
    Instructions: "You are a coding assistant.",
    Messages: []llm.Message{llm.UserText("Make a plan")},
    Reasoning: &llm.ReasoningConfig{Effort: "medium", Summary: "auto"},
    Tools: []llm.Tool{{
        Kind: llm.ToolFunction,
        Name: "read_file",
        Description: "Read a file from the workspace",
        Strict: llm.Bool(true),
        Parameters: map[string]any{
            "type": "object",
            "properties": map[string]any{
                "path": map[string]any{"type":"string"},
            },
            "required": []any{"path"},
            "additionalProperties": false,
        },
    }},
})
if err != nil { panic(err) }
fmt.Println(resp.Text)
```

Tool loop 예시:

```go
registry := llm.ToolRegistry{
    "read_file": llm.JSONToolHandler(func(ctx context.Context, in struct{ Path string `json:"path"` }) (string, error) {
        b, err := os.ReadFile(in.Path)
        if err != nil { return "", err }
        return string(b), nil
    }),
}

resp, err := llm.RunToolLoop(ctx, client, req, registry, llm.ToolLoopOptions{MaxIterations: 8})
```

## Codex / ChatGPT subscription provider 방향

ChatGPT subscription/Codex OAuth는 OpenAI Platform API와 같은 인증/과금 surface가 아니다. 따라서 core에는 다음 식으로 붙이는 것이 안전하다.

- `providers/openai` — 공식 Platform/OpenAI-compatible HTTP.
- `providers/codexcli` 또는 `providers/codexharness` — Codex CLI/app-server subprocess or harness adapter.
- `providers/openclaw` / `providers/opencode` — 해당 local API나 plugin을 감싸는 adapter.

중요 원칙:

- core `llm.Provider`는 OAuth/token refresh/storage를 직접 알지 않는다.
- provider package가 auth를 소유한다.
- subscription provider는 production API로 간주하지 않고 local/dev provider로 분류한다.

## 검증

- `go test ./...` 통과.
- OpenAI-compatible provider는 `httptest.Server`로 request mapping과 response parsing 검증.
- reasoning item, function call, custom tool output mapping 테스트 추가.
- tool loop는 fake provider로 reasoning/tool item 보존 검증.

## 소스

- OpenAI tools guide: https://developers.openai.com/api/docs/guides/tools
- OpenAI function calling guide: https://developers.openai.com/api/docs/guides/function-calling
- OpenAI migrate to Responses API: https://developers.openai.com/api/docs/guides/migrate-to-responses
- OpenAI Responses create reference: https://developers.openai.com/api/reference/resources/responses/methods/create
- OpenAI reasoning best practices: https://developers.openai.com/api/docs/guides/reasoning-best-practices
- OpenAI structured output guide: https://developers.openai.com/api/docs/guides/structured-output
