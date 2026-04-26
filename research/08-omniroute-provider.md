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

## 추가 확인: docs/openapi.yaml 분석

요청에 따라 `docs/openapi.yaml`도 별도로 파싱했다. clone 시점의 spec 정보:

- OpenAPI version: `3.1.0`
- OmniRoute API version: `3.7.0`
- paths: `113`
- component schemas: `15`

### 우리 프로젝트에 도움이 되는 점

#### 1. `/v1` 문서와 `/api/v1` OpenAPI path 차이를 흡수해야 함

User Guide/README는 CLI integration에서 `http://localhost:20128/v1`을 강조한다. 반면 `docs/openapi.yaml`은 server root `http://localhost:20128` 아래 path를 `/api/v1/responses`, `/api/v1/chat/completions`, `/api/v1/models`처럼 정의한다.

OmniRoute codebase에는 Next.js route가 `src/app/api/v1/...`에 실제로 존재하고, UI 문서/rewriter는 외부 친화 path `/v1/...`도 보여준다. 따라서 kkode provider는 둘 다 받아야 한다.

반영:

- `omniroute.New(...)` 기본값은 사용자/CLI guide 친화 `http://localhost:20128/v1` 유지.
- `omniroute.NewFromOpenAPIServer("http://localhost:20128", cfg)` 추가: OpenAPI spec 기준 `http://localhost:20128/api/v1` 사용.
- `omniroute.NewFromGatewayBase("http://localhost:20128", cfg)` 추가: User Guide 기준 `http://localhost:20128/v1` 사용.

#### 2. Responses schema는 느슨하므로 OpenAI adapter 재사용이 맞음

`/api/v1/responses` spec은 request schema를 `type: object`, response를 “Response object or SSE stream”으로만 둔다. 즉 OmniRoute가 Responses payload를 넓게 proxy/translate하는 구조라서, 별도 강한 OmniRoute-specific Responses type을 만들기보다 기존 `providers/openai` request/response/SSE mapping을 재사용하는 것이 맞다.

#### 3. Management/control-plane helper 후보가 명확해짐

OpenAPI에서 kkode에 특히 도움이 되는 endpoint:

- `GET /api/models/catalog` — provider/model catalog 동기화 후보.
- `GET/POST /api/combos` — routing combo 생성/조회.
- `GET /api/combos/metrics` — combo 성능 관찰.
- `GET /api/rate-limits` — account/provider rate limit 상태.
- `GET /api/sessions` — active session tracking.
- `GET /api/cache/stats` — cache 상태.
- `GET/PUT /api/settings/thinking-budget` — reasoning/thinking budget 제어.
- `GET/PATCH /api/resilience` — queue/cooldown/circuit breaker 설정.
- `GET/POST/DELETE /api/fallback/chains` — model fallback chain 관리.
- `POST /api/translator/translate` — Anthropic/OpenAI/Gemini 등 format 변환 debug에 유용.

이번 반영:

- `GetThinkingBudget`
- `UpdateThinkingBudget`
- `Translate`
- `ListFallbackChains`
- `CreateFallbackChain`
- `DeleteFallbackChain`
- `CacheStats`
- `RateLimits`
- `Sessions`

#### 4. 향후 codegen 후보

OpenAPI spec은 113개 path를 포함하지만 schema가 상세하지 않은 endpoint도 많다. 지금 당장 전체 client codegen은 과하고, 다음 전략이 좋다.

1. kkode core에 직접 필요한 generation path는 손작성 adapter 유지.
2. management API는 endpoint별로 필요한 것만 thin helper 추가.
3. `/api/models/catalog`, `/api/combos`, `/api/combos/metrics`, `/api/resilience`는 다음 우선순위.
4. spec이 더 정교해지면 `oapi-codegen` 같은 도구로 management client를 분리 검토.

### 결론

`docs/openapi.yaml`은 우리 프로젝트에 **도움 된다**. 특히 `/api/v1` path variant, management API 목록, thinking-budget/fallback/resilience/translator endpoint를 확인하는 데 유용했다. 다만 Responses schema가 느슨하므로 provider 핵심 generation은 기존 OpenAI-compatible adapter를 재사용하고, OmniRoute-specific 기능은 control-plane helper로 추가하는 방향이 맞다.
