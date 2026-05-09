# 04. Provider, auth, model router, cost budget 강화 제안

작성일: 2026-04-27

## 결론

현재 `kkode`는 provider adapter를 여러 개 갖고 있지만, 제품 관점의 provider layer는 아직 부족해요. 실제 바이브코딩 툴은 “요청 보내기”보다 **인증, 모델 선택, capability matching, quota/cost, fallback, session affinity**가 중요해요.

## 현재 상태

현재 provider는 대략 이렇게 있어요.

- `providers/openai`: `/v1/responses` 중심이에요.
- `providers/copilot`: GitHub Copilot SDK adapter예요.
- `providers/codexcli`: `codex exec --json` subprocess adapter예요.
- `providers/omniroute`: gateway adapter예요.

좋은 점:

- `llm.Provider` 추상화가 있어요.
- `Capabilities` 타입이 있어요.
- `Router`가 provider/model prefix routing을 해요.

부족한 점:

- auth store가 없어요.
- provider discovery가 없어요.
- model catalog sync가 없어요.
- context length, pricing, tool capability, reasoning capability가 model별로 정리되지 않아요.
- cost budget/usage limit가 runtime과 연결되지 않아요.
- fallback/route policy가 없어요.
- OpenCode Go 같은 OpenAI-compatible chat-only provider를 Responses API baseline에 어떻게 붙일지 정책이 없어요.

## 조사 기반 관찰

OpenCode는 provider/model configuration을 제품의 핵심으로 다뤄요. `opencode auth login`은 provider credentials를 저장하고, OpenCode Go는 `/zen/go/v1/chat/completions`와 `/zen/go/v1/models` endpoint를 제공해요. OpenCode Go model ID는 `opencode-go/<model-id>` 형식이라고 문서화돼요.

Codex SDK는 현재 TypeScript SDK가 공식 주력이고, Python SDK는 experimental로 app-server JSON-RPC를 제어한다고 설명해요. 즉 Go에서 Codex를 “라이브러리처럼” 쓰려면 현재는 subprocess/app-server protocol adapter가 현실적이에요.

Codex config reference는 model/provider 자체뿐 아니라 service tier, approval, MCP, memories, sandbox, hooks까지 config에 묶어요. provider config만 따로 보면 안 돼요.

## Auth store 제안

```text
~/.kkode/auth.json        # encrypted 또는 OS keyring pointer예요
~/.kkode/config.toml      # provider 설정이에요
<repo>/.kkode/config.toml # project override예요
```

가능하면 token 원문은 OS keyring에 저장하고, 파일에는 reference만 둬야해요.

```go
type Credential struct {
    Provider  string
    Kind      llm.AuthType
    KeyRef    string
    Headers   map[string]string
    CreatedAt time.Time
    UpdatedAt time.Time
}

type AuthStore interface {
    Set(ctx context.Context, cred Credential) error
    Get(ctx context.Context, provider string) (*Credential, error)
    Delete(ctx context.Context, provider string) error
    List(ctx context.Context) ([]CredentialSummary, error)
}
```

CLI:

```bash
kkode auth login openai
kkode auth login opencode-go
kkode auth login copilot
kkode auth list
kkode auth logout openai
```

## Model catalog 제안

```go
type ModelInfo struct {
    ID              string
    Provider        string
    DisplayName     string
    ContextWindow   int
    MaxOutputTokens int
    SupportsTools   bool
    SupportsMCP     bool
    SupportsVision  bool
    SupportsReasoning bool
    SupportsStructuredOutput bool
    InputPricePerMTok  float64
    OutputPricePerMTok float64
    CachedInputPricePerMTok float64
    DefaultReasoningEffort string
    UpdatedAt time.Time
}

type ModelCatalog interface {
    Refresh(ctx context.Context, provider string) error
    List(ctx context.Context, q ModelQuery) ([]ModelInfo, error)
    Get(ctx context.Context, providerModel string) (*ModelInfo, error)
}
```

OpenCode Go `/models`, OmniRoute `/v1/models`, OpenAI `/v1/models`, provider-specific docs, local static overrides를 모두 합쳐야해요.

## Provider routing policy

현재 `llm.Router`는 prefix 기반이에요. 제품에는 policy 기반 router가 필요해요.

```go
type RouteRequest struct {
    TaskKind    string // code, review, search, plan, refactor
    Required    CapabilitySet
    MaxCostUSD  float64
    PreferLocal bool
    ProviderHint string
    ModelHint    string
}

type RouteDecision struct {
    Provider string
    Model    string
    Reason   string
    Fallback []ProviderModel
}
```

예시:

- plan/review는 cheap+reasoning 모델로 보내요.
- edit/build는 tool support와 large context를 우선해요.
- web/search는 web tool이 있는 provider 또는 local web tool을 붙여요.
- long context는 compaction 가능 여부를 같이 봐요.
- subscription provider(Codex/Copilot)는 quota/cost가 API와 다르므로 별도 budget bucket으로 봐요.

## Responses API vs chat completions bridge

우리 baseline은 OpenAI Responses API예요. 하지만 OpenCode Go는 chat completions endpoint도 제공해요. 모든 provider가 Responses API를 지원하지 않아요.

따라서 provider adapter는 두 층이어야해요.

```go
type ResponsesProvider interface { ... }
type ChatProvider interface { ... }
type MessagesProvider interface { ... }
```

그리고 bridge를 둬요.

```go
type ChatCompatProvider struct {
    Chat ChatProvider
}

func (p *ChatCompatProvider) Generate(ctx context.Context, req llm.Request) (*llm.Response, error)
```

제한 사항:

- reasoning item 보존은 어렵거나 불가능해요.
- custom tool 결과 item 형태는 provider별로 손실될 수 있어요.
- hosted MCP/built-in tools는 local tool로 대체해야해요.

이 제한을 `Capabilities`에 명확히 표시해야해요.

## Cost/usage budget

OpenCode Go는 5시간/주간/월간 limit를 dollar value로 설명해요. Claude Agent SDK는 result에 token usage/cost를 포함한다고 문서화해요. Codex/Copilot subscription은 일반 API billing과 다르게 다뤄야해요.

현재 gateway run 요청은 `max_output_tokens`를 받아 provider request, run record, retry에 보존하고 `/capabilities.limits.max_run_output_tokens`로 상한을 노출해요. 남은 작업은 실제 usage ledger와 session/day/provider scope별 누적 budget decision이에요.

```go
type Budget struct {
    SessionMaxUSD float64
    DailyMaxUSD   float64
    MaxTurns      int
    MaxToolCalls  int
    MaxWallTime   time.Duration
}

type UsageLedger interface {
    Record(ctx context.Context, usage UsageRecord) error
    Current(ctx context.Context, scope BudgetScope) (UsageSummary, error)
    Check(ctx context.Context, budget Budget) (BudgetDecision, error)
}
```

budget 초과 시 action:

- stop
- ask user
- switch model
- compact
- fallback provider

## Provider-specific 제안

### OpenAI

- Responses API full support를 유지해요.
- hosted tools(web search/file search/computer/code interpreter/MCP/apply patch/local shell) mapping을 계속 확장해요.
- reasoning encrypted content include를 session store에 저장하는 정책이 필요해요.

### Codex

- `codexcli` subprocess adapter는 smoke test용으로 유지해요.
- `codex app-server` JSON-RPC adapter를 새로 만들어야해요.
- SDK가 TypeScript 중심이라 Go에서는 JSON-RPC protocol 구현이 더 적합해요.

### Copilot

- Copilot SDK session을 `session.Store`와 연결해야해요.
- custom tools/MCP/custom agents를 `AgentDefinition`에서 변환해야해요.
- permission request callback을 우리 `PermissionEngine`으로 연결해야해요.

### OmniRoute

- OpenAI-compatible gateway로 유지해요.
- health/model/route score를 `RouteDecision`에 반영해야해요.
- session ID/header는 `Session.ID`와 연결해요.

### OpenCode Go

- `opencode-go` provider를 추가할 가치가 있어요.
- endpoint가 chat completions/messages 중심이라 bridge가 필요해요.
- model catalog는 `/zen/go/v1/models`에서 가져와요.

## CLI 제안

```bash
kkode providers list
kkode providers doctor
kkode auth login <provider>
kkode models list --provider openai
kkode models refresh
kkode route explain --task code --max-cost 0.05
kkode budget status
```

## 구현 우선순위

1. AuthStore file backend + env fallback
2. ModelCatalog static + provider refresh
3. ChatCompatProvider
4. OpenCode Go provider
5. UsageLedger
6. RoutePolicy
7. Codex app-server JSON-RPC provider
8. provider doctor command

## 참고 소스

- OpenCode Go: https://opencode.ai/docs/go/
- OpenCode CLI auth/models: https://opencode.ai/docs/cli/
- OpenCode SDK: https://opencode.ai/docs/sdk/
- OpenAI Codex SDK: https://developers.openai.com/codex/sdk
- OpenAI Codex App Server: https://developers.openai.com/codex/app-server
- OpenAI Codex Config Reference: https://developers.openai.com/codex/config-reference
