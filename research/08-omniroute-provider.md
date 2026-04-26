# 08. OmniRoute provider 조사 및 구현 메모

작성일: 2026-04-26  
대상 repo: https://github.com/diegosouzapw/OmniRoute

## 핵심 결론

OmniRoute는 kkode에서 **OpenAI-compatible gateway provider**로 붙이는 것이 가장 자연스럽다.

이유:

- OmniRoute 문서는 coding agent/CLI들이 `http://localhost:20128/v1` 또는 cloud endpoint의 `/v1`에 연결한다고 설명한다.
- API Reference가 `/v1/chat/completions`, `/v1/responses`, `/v1/embeddings`, `/v1/images/generations`, `/v1/models` compatibility endpoint를 명시한다.
- kkode의 compatibility baseline은 OpenAI Responses API이므로 OmniRoute provider는 `/v1/responses`를 우선 사용하면 된다.
- OmniRoute는 단순 model provider가 아니라 routing gateway라서 cache/session/idempotency/progress headers도 adapter에서 노출해야 한다.
- OmniRoute A2A와 MCP는 별도 agent/control plane이다. 기본 generation은 `/v1/responses`, routing/status/agent orchestration은 A2A/MCP/management API로 분리하는 것이 안전하다.

## 조사한 문서

사용자가 지정한 문서 중 구현에 직접 반영한 문서:

- `README.md`
- `docs/USER_GUIDE.md`
- `docs/API_REFERENCE.md`
- `docs/openapi.yaml`
- `open-sse/mcp-server/README.md`
- `src/lib/a2a/README.md`
- `docs/AUTO-COMBO.md`
- `docs/features/context-relay.md`
- `docs/ENVIRONMENT.md`
- `docs/ARCHITECTURE.md`
- `docs/TROUBLESHOOTING.md`
- `SECURITY.md`

## OmniRoute API surface 요약

### OpenAI-compatible endpoints

API Reference 기준 compatibility endpoints:

| Method | Path | Format |
|---|---|---|
| POST | `/v1/chat/completions` | OpenAI |
| POST | `/v1/messages` | Anthropic |
| POST | `/v1/responses` | OpenAI Responses |
| POST | `/v1/embeddings` | OpenAI |
| POST | `/v1/images/generations` | OpenAI |
| GET | `/v1/models` | OpenAI |
| POST | `/v1/messages/count_tokens` | Anthropic |
| GET | `/v1beta/models` | Gemini |
| POST | `/v1beta/models/{...path}` | Gemini generateContent |
| POST | `/v1/api/chat` | Ollama |

kkode provider는 현재 `/v1/responses`와 `/v1/models`를 구현했다.

### Dedicated provider routes

OmniRoute는 특정 provider로 직접 라우팅하는 endpoint도 제공한다.

```text
POST /v1/providers/{provider}/chat/completions
POST /v1/providers/{provider}/embeddings
POST /v1/providers/{provider}/images/generations
```

현재 kkode의 OmniRoute adapter는 model prefix 또는 OmniRoute combo/model name을 그대로 `/v1/responses`에 넘긴다. Dedicated provider route는 chat/embedding/image 확장 때 추가하면 된다.

### Custom headers

API Reference에서 확인한 request headers:

| Header | 의미 |
|---|---|
| `X-OmniRoute-No-Cache` | cache bypass |
| `X-OmniRoute-Progress` | progress event 활성화 |
| `X-Session-Id` / `x_session_id` | sticky session/external session affinity |
| `Idempotency-Key` | dedup key |
| `X-Request-Id` | alternative dedup key |

kkode `providers/omniroute.Config`에 반영한 필드:

- `SessionID`
- `NoCache`
- `Progress`
- `IdempotencyKey`
- `RequestID`
- `Headers`

### Management API

kkode에서 구현한 management helper:

- `ListModels(ctx)` → `GET /v1/models`
- `Health(ctx)` → `GET /api/monitoring/health`
- `A2ASend(ctx, req)` → `POST /a2a`, JSON-RPC `message/send`

추후 확장 후보:

- `/api/combos`
- `/api/combos/metrics`
- `/api/usage/*`
- `/api/rate-limits`
- `/api/cache/stats`
- `/api/settings/thinking-budget`
- `/api/resilience`

## A2A Server 조사

`src/lib/a2a/README.md` 기준 OmniRoute A2A server는 Agent-to-Agent Protocol v0.3 JSON-RPC 2.0 endpoint다.

- discovery: `GET /.well-known/agent.json`
- JSON-RPC endpoint: `POST /a2a`
- sync method: `message/send`
- streaming method: `message/stream`
- task status: `tasks/get`
- cancel: `tasks/cancel`

주요 skill:

- `smart-routing` — OmniRoute intelligent pipeline으로 prompt 라우팅.
- `quota-management` — quota/provider 상태 질의.

kkode는 이번에 `A2ASend`만 구현했다. streaming A2A는 OpenAI SSE와 별도로 JSON-RPC SSE event parser가 필요하므로 다음 단계로 남긴다.

## MCP Server 조사

`open-sse/mcp-server/README.md` 기준 OmniRoute MCP server는 stdio 또는 HTTP로 사용 가능하며 gateway intelligence를 tools로 노출한다.

문서 상 tool 수는 README 상단에는 16 tools로 표시되어 있었고, 사용자 요청 표에는 25 MCP tools라고 되어 있다. 최신 README clone 기준 Phase 1/2 합계 16개가 명확히 문서화되어 있었다.

핵심 tools:

- `omniroute_get_health`
- `omniroute_list_combos`
- `omniroute_check_quota`
- `omniroute_route_request`
- `omniroute_cost_report`
- `omniroute_list_models_catalog`
- `omniroute_best_combo_for_task`
- `omniroute_explain_route`

kkode 입장에서는 두 가지 연결 방식이 가능하다.

1. `providers/omniroute` direct HTTP provider: 일반 생성/모델/health.
2. `llm.MCPServer`로 OmniRoute MCP server를 Copilot/OpenAI provider에 붙이기.

이번 구현은 1번을 추가했고, 2번은 이미 `llm.MCPServer` 및 Copilot MCP mapping이 있으므로 설정만 만들면 된다.

## Auto-Combo / Context Relay 설계 영향

### Auto-Combo

`docs/AUTO-COMBO.md` 기준 OmniRoute Auto-Combo는 6-factor scoring을 사용한다.

- Quota: 0.20
- Health: 0.25
- CostInv: 0.20
- LatencyInv: 0.15
- TaskFit: 0.10
- Stability: 0.10

Mode packs:

- Ship Fast
- Cost Saver
- Quality First
- Offline Friendly

kkode에서는 model ID를 `auto`, combo name, 또는 OmniRoute model prefix 그대로 넘기고, combo 선택은 OmniRoute 내부에 맡기는 방식이 좋다.

### Context Relay

`context-relay`는 account rotation 때 session continuity를 유지하는 combo strategy다.

중요 포인트:

- stable `sessionId`가 중요하다.
- `X-Session-Id` header를 유지해야 account rotation과 handoff injection이 의미 있다.
- 85~94% quota 사용 구간에서 summary generation이 준비되고, account switch 후 system message로 주입된다.

그래서 kkode provider에 `SessionID` 설정을 넣었다.

## 구현 내용

패키지: `providers/omniroute`

### `Config`

```go
type Config struct {
    BaseURL string          // default http://localhost:20128/v1
    AdminBaseURL string     // default BaseURL에서 /v1 제거
    APIKey string
    HTTPClient *http.Client
    Headers map[string]string
    SessionID string
    NoCache bool
    Progress bool
    IdempotencyKey string
    RequestID string
    Retry openai.RetryConfig
}
```

### `Client`

- `llm.Provider` 구현.
- `llm.StreamProvider` 구현.
- 내부적으로 `providers/openai.Client`를 사용해 `/v1/responses` 호출.
- response provider 이름은 `omniroute`로 재설정.

### helper methods

- `ListModels(ctx)`
- `Health(ctx)`
- `A2ASend(ctx, A2ARequest)`

## 사용 예시

```go
client := omniroute.New(omniroute.Config{
    BaseURL: "http://localhost:20128/v1",
    APIKey: os.Getenv("OMNIROUTE_API_KEY"),
    SessionID: "kkode-session-1",
    NoCache: false,
})

resp, err := client.Generate(ctx, llm.Request{
    Model: "auto",
    Messages: []llm.Message{llm.UserText("Write a Go hello world")},
    Reasoning: &llm.ReasoningConfig{Effort: "medium"},
})
```

A2A:

```go
a2a, err := client.A2ASend(ctx, omniroute.A2ARequest{
    Skill: "smart-routing",
    Messages: []llm.Message{llm.UserText("Suggest a cheap coding route")},
    Metadata: map[string]any{"role": "coding", "model": "auto"},
})
```

Router:

```go
router := llm.NewRouter()
router.Register("omniroute", omniroute.New(omniroute.Config{}))
resp, err := router.Generate(ctx, llm.Request{
    Model: "omniroute/auto",
    Messages: []llm.Message{llm.UserText("Plan this refactor")},
})
```

## 검증

- `go test ./providers/omniroute` 통과.
- `go test ./...` 통과.
- `go vet ./...` 통과.
- `httptest.Server`로 확인한 것:
  - `/v1/responses` 호출.
  - `Authorization`, `X-Session-Id`, `X-OmniRoute-No-Cache` header 전달.
  - `/v1/models` parsing.
  - `/api/monitoring/health` parsing.
  - `/a2a` JSON-RPC `message/send` parsing.

## 소스

- OmniRoute repo: https://github.com/diegosouzapw/OmniRoute
- User Guide: https://github.com/diegosouzapw/OmniRoute/blob/main/docs/USER_GUIDE.md
- API Reference: https://github.com/diegosouzapw/OmniRoute/blob/main/docs/API_REFERENCE.md
- OpenAPI spec: https://github.com/diegosouzapw/OmniRoute/blob/main/docs/openapi.yaml
- MCP Server: https://github.com/diegosouzapw/OmniRoute/blob/main/open-sse/mcp-server/README.md
- A2A Server: https://github.com/diegosouzapw/OmniRoute/blob/main/src/lib/a2a/README.md
- Auto-Combo: https://github.com/diegosouzapw/OmniRoute/blob/main/docs/AUTO-COMBO.md
- Context Relay: https://github.com/diegosouzapw/OmniRoute/blob/main/docs/features/context-relay.md
- Environment Config: https://github.com/diegosouzapw/OmniRoute/blob/main/docs/ENVIRONMENT.md
