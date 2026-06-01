# kkode 아키텍처

이 문서는 `kkode`의 파일 트리, 핵심 구현체, 함수 시그니처, 사용 예제를 설명해요. 앞으로 새 provider나 tool을 추가할 때 이 문서를 기준으로 맞춰가면 돼요.

## 설계 목표

`kkode`는 Go 기반 바이브코딩 앱을 만들기 위한 provider runtime이에요. 핵심 방향은 다음과 같아요.

1. OpenAI Responses API의 item semantics를 기본 호환 모델로 삼아요.
2. Copilot SDK나 Codex CLI처럼 session 중심인 provider도 같은 앱에서 사용할 수 있게 해요.
3. Tool, Provider, Auth, Model, Response, Prompt를 직접 소유해요.
4. provider별 특수 기능은 adapter 안에 가두고 core는 최대한 provider-neutral하게 유지해요.
5. workspace 접근과 shell 실행은 별도 권한 엔진 없이 즉시 실행해야해요.
6. 실제 agent 실행 단위는 provider, workspace tool, guardrail, transcript, trace를 한 번에 묶어야해요.

## 파일 트리

```text
kkode/
├── README.md                         # 프로젝트 소개와 빠른 사용법이에요
├── ARCHITECTURE.md                   # 현재 문서예요
├── go.mod
├── go.sum
├── app/                              # CLI/gateway가 공유하는 provider/agent 조립 도우미예요
│   ├── app.go
│   ├── mcp_defaults.go
│   ├── project_instructions.go
│   ├── provider_conversion.go
│   ├── provider_registry.go
│   └── provider_tools.go
├── agent/                            # 실제 coding agent loop와 guardrail/trace예요
├── cmd/
│   ├── kkode-agent/                  # provider 선택형 agent CLI예요
│   └── kkode-gateway/                # HTTP gateway API server예요
├── gateway/                          # 스트리밍 코딩 에이전트를 위한 HTTP API예요 (sessions, runs, providers, models)
├── llm/                              # provider-neutral core예요
├── providers/
│   ├── codexcli/
│   ├── copilot/
│   ├── httpjson/
│   ├── internal/httptransport/
│   ├── omniroute/
│   └── openai/
├── prompts/                          # system/session/todo prompt 템플릿이에요
├── research/                         # 조사 문서와 TODO예요
├── runtime/                          # agent와 session store를 묶는 실행 runtime이에요
├── scripts/                          # 검증용 smoke scripts예요
├── session/                          # SQLite session/run/checkpoint/artifact/todo store예요
├── suggest/                          # 다음 구현 제안과 roadmap이에요
├── tools/                            # 표준 file/web/codeintel tool 이름 adapter예요
├── transcript/                       # transcript 저장소예요
└── workspace/                       # workspace file/write/search/shell/checkpoint helper예요
```

## 핵심 인터페이스

### `llm.Provider`

단발성 생성 요청을 처리하는 최소 인터페이스예요.

```go
type Provider interface {
    Name() string
    Capabilities() Capabilities
    Generate(ctx context.Context, req Request) (*Response, error)
}
```

구현체는 가능한 경우 provider raw output item을 `Response.Output[].ProviderRaw`에 보존해야해요. 그래야 reasoning item이나 tool call item을 다음 턴으로 이어갈 수 있어요.

### `llm.StreamProvider`

SSE, JSONL, SDK event stream을 공통 stream으로 바꾸는 인터페이스예요.

```go
type StreamProvider interface {
    Provider
    Stream(ctx context.Context, req Request) (EventStream, error)
}

type EventStream interface {
    Recv() (StreamEvent, error)
    Close() error
}
```

대표 event type은 다음과 같아요.

```go
const (
    StreamEventStarted        StreamEventType = "started"
    StreamEventTextDelta      StreamEventType = "text_delta"
    StreamEventReasoningDelta StreamEventType = "reasoning_delta"
    StreamEventToolCall       StreamEventType = "tool_call"
    StreamEventToolResult     StreamEventType = "tool_result"
    StreamEventCompleted      StreamEventType = "completed"
    StreamEventError          StreamEventType = "error"
)
```

### `llm.SessionProvider`

Copilot SDK, Codex app server, future agent runtime처럼 session lifecycle이 중요한 provider를 위한 인터페이스예요.

```go
type SessionProvider interface {
    Provider
    NewSession(ctx context.Context, req SessionRequest) (Session, error)
}

type Session interface {
    ID() string
    Send(ctx context.Context, req Request) (*Response, error)
    Stream(ctx context.Context, req Request) (EventStream, error)
    Close() error
}
```

### `llm.Request`

provider 공통 요청 타입이에요.

```go
type Request struct {
    Model              string
    Instructions       string
    Messages           []Message
    InputItems         []Item
    Prompt             *PromptRef
    Tools              []Tool
    ToolChoice         ToolChoice
    Reasoning          *ReasoningConfig
    TextFormat         *TextFormat
    MaxOutputTokens    int
    MaxToolCalls       int
    Temperature        *float64
    TopP               *float64
    Store              *bool
    PreviousResponseID string
    Include            []string
    Metadata           map[string]string
    ParallelToolCalls  *bool
    SafetyIdentifier   string
    PromptCacheKey     string
}
```

`Messages`는 사람이 쓰기 쉬운 입력이고, `InputItems`는 Responses-style loop에서 raw item을 보존하기 위한 입력이에요.

### `llm` 변환 레이어

provider 추가 비용을 줄이기 위해 core에는 얇은 변환 계약만 둬요. agent/runtime/gateway는 계속 표준 `llm.Request`와 `llm.Response`만 다루고, provider 패키지가 실제 API payload나 SDK 호출값으로 바꿔요.

```go
type RequestConverter interface {
    ConvertRequest(ctx context.Context, req Request, opts ConvertOptions) (ProviderRequest, error)
}

type ResponseConverter interface {
    ConvertResponse(ctx context.Context, result ProviderResult) (*Response, error)
}

type ProviderCaller interface {
    CallProvider(ctx context.Context, req ProviderRequest) (ProviderResult, error)
}

type ProviderStreamCaller interface {
    StreamProvider(ctx context.Context, req ProviderRequest) (EventStream, error)
}

type RequestConverterFunc func(ctx context.Context, req Request, opts ConvertOptions) (ProviderRequest, error)
type ResponseConverterFunc func(ctx context.Context, result ProviderResult) (*Response, error)
type ProviderCallerFunc func(ctx context.Context, req ProviderRequest) (ProviderResult, error)
type ProviderStreamCallerFunc func(ctx context.Context, req ProviderRequest) (EventStream, error)

type ProviderPipeline struct {
    ProviderName      string
    RequestConverter  RequestConverter
    ResponseConverter ResponseConverter
    Caller            ProviderCaller
    Streamer          ProviderStreamCaller
    Options           ConvertOptions
    StreamOptions     ConvertOptions
}

func (p ProviderPipeline) Prepare(ctx context.Context, req Request) (ProviderRequest, error)
func (p ProviderPipeline) Call(ctx context.Context, preq ProviderRequest) (ProviderResult, error)
func (p ProviderPipeline) Decode(ctx context.Context, result ProviderResult) (*Response, error)
func (p ProviderPipeline) Generate(ctx context.Context, req Request) (*Response, error)
func (p ProviderPipeline) PrepareStream(ctx context.Context, req Request) (ProviderRequest, error)
func (p ProviderPipeline) Stream(ctx context.Context, req Request) (EventStream, error)

type AdaptedProvider struct {
    ProviderName         string
    ProviderCapabilities Capabilities
    Converter            Converter
    RequestConverter     RequestConverter
    ResponseConverter    ResponseConverter
    Caller               ProviderCaller
    Streamer             ProviderStreamCaller
    Options              ConvertOptions
    StreamOptions        ConvertOptions
}
```

단발성 흐름은 `llm.Request → RequestConverter → ProviderRequest → ProviderCaller → ProviderResult → ResponseConverter → llm.Response`예요. 이 흐름은 `ProviderPipeline`이 실제 단계로 나눠서 실행해요. `Prepare`는 변환 preview나 debug UI가 재사용하고, `Call`은 API/SDK/CLI source 경계만 담당하며, `Decode`는 source 결과를 다시 표준 응답으로 맞춰요. 그래서 새 API를 붙일 때 core 타입을 수정하지 않고 converter와 caller만 추가하거나, OpenAI-compatible request builder와 별도 API caller/response parser를 조합하면 돼요. OpenAI-compatible HTTP JSON 파생 API는 `providers/httpjson.Caller`에 base URL과 operation route만 넣어서 source client 중복 없이 붙일 수 있고, `app.BuildHTTPJSONProviderAdapter`는 registry route를 기본값으로 읽어 `BaseURL/APIKey/ProviderName`만으로 `llm.Provider`를 만들어요. `app.RegisterHTTPJSONProvider`는 같은 profile을 별도 provider 이름으로 discovery/routing까지 등록해서 proxy, gateway, 사내 API처럼 converter가 같은 source를 설정만으로 추가하게 해요. HTTP JSON route는 `Path`와 `Query` template를 지원해서 `{model}`, `{operation}`, `{metadata.key}` 또는 `{key}` 값을 `ProviderRequest.Metadata`에서 꺼내 endpoint를 만들어요. gateway run/test `metadata`는 provider request metadata까지 전달되므로 웹 패널이나 Discord adapter가 넣은 `trace_id`, `deployment`, `api_version` 같은 값을 source route 조립에 그대로 쓸 수 있어요. `ProviderRequestPreview.Route`는 매칭 route와 resolved path/query를 노출해서 live 호출 전에 endpoint template 누락을 확인하게 해요. 그래서 `/providers/{provider}/models/{model}/generate?api-version={metadata.api_version}`처럼 OpenAI-compatible이 아닌 HTTP API도 caller를 새로 쓰지 않고 converter metadata만 채워 붙일 수 있어요. `MaxResponseBytes`/`max_response_bytes`는 HTTP JSON source의 success/error body 상한을 조절하고, 0이면 기본 32MiB를 쓰며 32MiB보다 큰 값은 adapter 생성/등록에서 거부해요. gateway는 이 상한을 `/api/v1` bootstrap 응답의 limits에 노출해요. success body가 상한을 넘으면 partial JSON decode 대신 실패하고, error body는 상한에서 잘린 뒤 `HTTPError.Body`에 `[truncated]` marker를 붙여 남겨요. `DisableStreaming`을 켜면 registry의 OpenAI-compatible capability에서 `streaming`만 꺼서 JSON-only source가 SSE 지원처럼 광고되지 않게 해요. 기본 OpenAI-compatible client의 단발 호출도 이 caller를 사용해서 새 provider와 같은 transport 경계를 검증해요. `AdaptedProvider`는 이 pipeline을 감싼 `llm.Provider` 구현체라서 기존 provider는 간단한 struct 조립만 유지해요.

`RequestConverterFunc`, `ResponseConverterFunc`, `ProviderCallerFunc`, `ProviderStreamCallerFunc`는 작은 API source나 plugin을 빠르게 붙이는 함수형 adapter예요. 완전한 provider 패키지를 만들기 전에는 `ProviderPipeline`에 이 함수들을 바로 넣어서 `요청 → 컨버팅 레이어 → API 호출 → 표준 응답`을 검증하고, 나중에 source가 커지면 같은 함수 내용을 struct 구현체로 옮기면 돼요. nil 함수 adapter는 명확한 오류를 돌려주므로 잘못 조립된 plugin이 panic으로 죽지 않아요.

Streaming 흐름은 `PrepareStream → ProviderStreamCaller → EventStream`이에요. SSE/JSONL/SDK event stream처럼 응답이 event 단위인 provider는 response converter 없이도 stream provider를 구현할 수 있어요. `providers/httpjson.Caller`는 raw SSE frame을 범용 `StreamEvent`로 전달하고, provider별 delta/tool call 의미 해석은 전용 streamer가 맡으면 돼요. `app.ProviderConversionSet.Pipeline`은 registry에 등록된 변환기와 source caller를 조합하는 표준 진입점이에요. 그래서 OpenAI SSE, Codex JSONL, Copilot SDK event stream처럼 source 모양이 달라도 app/agent/gateway는 계속 `llm.StreamEvent`만 보면 돼요. HTTP API, subprocess, SDK session, in-memory fake 모두 caller 뒤에 숨길 수 있어요. 권한/승인 레이어는 이 흐름에 넣지 않고, workspace/tool/provider는 요청을 받으면 즉시 실행해요.

### `llm.Response`

provider 공통 응답 타입이에요.

```go
type Response struct {
    ID                 string
    Provider           string
    Model              string
    Status             string
    Text               string
    Output             []Item
    ToolCalls          []ToolCall
    Reasoning          []ReasoningItem
    Usage              Usage
    PreviousResponseID string
    Raw                json.RawMessage
}
```

### `llm.Tool`

function tool, custom tool, provider built-in tool을 하나로 표현해요.

```go
type Tool struct {
    Kind            ToolKind
    Name            string
    Description     string
    Parameters      map[string]any
    Strict          *bool
    Grammar         *Grammar
    ProviderOptions map[string]any
}
```


## App 조립 도우미

패키지는 `app`이에요. CLI와 gateway가 provider 생성, workspace 정책, 표준 tool 조립을 중복 구현하지 않도록 모아둔 얇은 조립 계층이에요. 핵심 domain 타입은 여전히 `llm`, `agent`, `runtime`, `session`이 소유해요.

```go
func ProviderSpecs() []ProviderSpec
func ResolveProviderSpec(name string) (ProviderSpec, bool)
func ProviderAuthStatus(spec ProviderSpec) string
func BuildProvider(name, root string) (ProviderHandle, error)
func BuildProviderWithOptions(name, root string, opts ProviderOptions) (ProviderHandle, error)
func RegisterProvider(reg ProviderRegistration) (func(), error)
func RegisterHTTPJSONProvider(reg HTTPJSONProviderRegistration) (func(), error)
func RegisterHTTPJSONProvidersFromEnv(key string) (func(), error)
func RegisterHTTPJSONProvidersFromJSON(raw string) (func(), error)
func BuildProviderPipeline(provider string, caller llm.ProviderCaller, streamer llm.ProviderStreamCaller) (llm.ProviderPipeline, error)
func BuildProviderAdapter(provider string, opts ProviderAdapterOptions) (*llm.AdaptedProvider, error)
func BuildHTTPJSONProviderAdapter(profile string, opts HTTPJSONProviderOptions) (*llm.AdaptedProvider, error)
func DefaultProviderOptions(root string) ProviderOptions
func DefaultMCPServers(root string) map[string]llm.MCPServer
func MergeProviderOptions(defaults ProviderOptions, explicit ProviderOptions) ProviderOptions
func DefaultModel(provider string) string
func NewWorkspace(opts WorkspaceOptions) (*workspace.Workspace, string, error)
func NewAgent(provider llm.Provider, ws *workspace.Workspace, opts AgentOptions) (*agent.Agent, error)
func NewRuntime(store session.Store, ag *agent.Agent, opts RuntimeOptions) *runtime.Runtime
func DefaultCompactionPolicy() session.CompactionPolicy
```

```go
type ProviderAdapterOptions struct {
    ProviderName string
    Caller       llm.ProviderCaller
    Streamer     llm.ProviderStreamCaller
    Capabilities llm.Capabilities
}

type HTTPJSONProviderOptions struct {
    ProviderName      string
    BaseURL           string
    APIKey            string
    Headers           map[string]string
    HTTPClient        *http.Client
    Retry             httpjson.RetryConfig
    MaxResponseBytes  int64
    DefaultOperation  string
    Routes            map[string]httpjson.Route
    Capabilities      llm.Capabilities
    DisableStreaming  bool
    AdditionalHeaders map[string]string
}

type HTTPJSONProviderRegistration struct {
    Name              string
    Aliases           []string
    Profile           string
    DefaultModel      string
    Models            []string
    AuthEnv           []string
    BaseURL           string
    BaseURLEnv        []string
    APIKey            string
    APIKeyEnv         []string
    Headers           map[string]string
    AdditionalHeaders map[string]string
    Routes            []ProviderRouteSpec // route별 Path/Query template를 포함해요
    DefaultOperation  string
    Capabilities      map[string]any
    Local             bool
    DisableStreaming  bool
    HTTPClient        *http.Client
    Retry             httpjson.RetryConfig
    MaxResponseBytes  int64
    Source            string
}

type ProviderRouteSpec struct {
    Operation string
    Method    string
    Path      string
    Accept    string
    Query     map[string]string
}

type ProviderRoutePreview struct {
    Operation     string
    Method        string
    Path          string
    Accept        string
    Query         map[string]string
    ResolvedPath  string
    ResolvedQuery map[string]string
}

type ProviderTestRequest struct {
    Model           string
    Prompt          string
    Stream          bool
    Live            bool
    Metadata        map[string]string // route template와 provider trace에 전달해요
    MaxPreviewBytes int
    MaxOutputTokens int
    MaxResultBytes  int // live smoke result text byte 제한이에요
    TimeoutMS       int // live smoke timeout이에요. 0이면 기본 60초예요
}
```

`llm.Router`는 `provider/model` 직접 지정과 alias prefix routing을 지원하고, alias가 겹치면 가장 긴 prefix를 우선해서 provider 선택이 Go map 순서에 흔들리지 않게 해요. `ProviderSpecs`는 provider registry에서 방어 복사한 spec을 돌려줘서 CLI/gateway/provider 기본 모델, 인증 상태, 변환 profile 표시를 공유해요. 같은 registry entry는 실행형 `ProviderConversionSet`도 들고 있어서 `PreviewProviderRequest`, `BuildProviderPipeline`, `BuildProviderAdapter`, `BuildHTTPJSONProviderAdapter`가 모두 같은 converter, operation 기본값, HTTP route metadata를 써요. 그래서 새 provider를 추가할 때 spec, alias, capability, 변환 profile, source 생성 로직을 한 entry에 맞추면 되고, OpenAI-compatible 파생 API는 `RegisterHTTPJSONProvider`나 `KKODE_HTTPJSON_PROVIDERS` JSON으로 profile, `BaseURL`, API key/env, route만 지정하거나 전용 `ProviderCaller`/`ProviderStreamCaller` source만 바꾸면 돼요. 외부 패키지나 테스트 plugin은 `RegisterProvider`로 `ProviderRegistration`을 런타임에 추가하고 반환된 unregister 함수로 되돌릴 수 있어서 core registry 파일을 직접 수정하지 않아도 돼요. registry는 mutex로 보호하고 `ProviderSpecs`/`ResolveProviderSpec`이 방어 복사본을 반환해서 discovery 호출과 테스트 등록이 서로 slice/map을 오염시키지 않게 해요. gateway의 provider discovery도 `conversion.routes[]`를 노출해서 웹 패널이 실제 API 호출 route를 preview/debug 화면에 보여줄 수 있어요. `BuildProviderWithOptions`는 `DefaultProviderOptions(root)`와 저장 resource manifest를 `MergeProviderOptions`로 합친 뒤 같은 registry entry의 factory를 실행해요. 기본 provider option은 project root부터 요청 `working_directory`까지의 `AGENTS.md`, `CLAUDE.md`, `KKODE.md`를 bounded/redacted context block으로 읽고, `KKODE_PROJECT_INSTRUCTIONS=off`면 끌 수 있어요. `KKODE_GLOBAL_PROJECT_INSTRUCTIONS=1`이면 `~/.kkode/KKODE.md`, `~/.codex/AGENTS.md`, `~/.claude/CLAUDE.md`도 앞에 붙여요. 기본 MCP는 Context7 원격 HTTP MCP(`https://mcp.context7.com/mcp`)와 Serena stdio MCP예요. Serena는 `uvx` 또는 `KKODE_SERENA_COMMAND`가 있을 때만 붙여서 실행 환경에 없는 바이너리 때문에 기본 run이 깨지지 않게 해요. `KKODE_DEFAULT_MCP=off`면 기본 MCP만 붙이지 않고 project instruction context는 유지해요. `ProviderHandle.BaseRequest`는 OpenAI-compatible HTTP MCP를 hosted `mcp` tool로 넘기고, Copilot은 stdio/http MCP를 SDK session config로 넘겨요. `MCPToolsFromProviderOptions`는 같은 `ProviderOptions.MCPServers` manifest에서 OpenAI-compatible hosted MCP tool과 agent용 local `mcp_call` toolset을 동시에 만들어서 provider 기본 request와 local MCP surface가 서로 다른 설정을 보지 않게 해요. CLI와 gateway는 `MergeBaseRequest` 또는 `AgentOptions.BaseRequest`로 이 기본 request를 agent에 전달해요. `NewRuntime`은 history/todo/compaction 기본값을 CLI와 gateway가 같은 방식으로 쓰게 해요. `NewAgent`는 `tools.StandardTools`를 통해 `tools.FileTools`와 선택적 `tools.WebTools`를 같은 방식으로 붙여요. 예전 `workspace_*` tool은 `workspace.Workspace.Tools()`로 직접 사용할 수 있지만, 일반 agent 표면에는 `file_read`, `file_write`, `file_delete`, `file_move`, `file_edit`, `file_apply_patch`, `file_restore_checkpoint`, `file_prune_checkpoints`, `file_list`, `file_glob`, `file_grep`, `shell_run`, `web_fetch`만 노출해요.

## Prompt 템플릿 구현체

패키지는 `prompts`예요. system prompt, session summary context, compaction prompt, todo instructions를 `prompts/*.md` 파일로 분리해요. 코드에서는 템플릿 이름만 참조하므로 문구 수정과 provider/runtime 구현 변경을 분리할 수 있어요.

```go
const (
    AgentSystem           = "agent-system.md"
    SessionSummaryContext = "session-summary-context.md"
    SessionCompaction     = "session-compaction.md"
    TodoInstructions      = "todo-instructions.md"
)

func Text(name string) (string, error)
func Render(name string, data any) (string, error)
func MustRender(name string, data any) string
```

`agent.Agent`는 기본 system prompt를 `prompts/agent-system.md`에서 만들어요. `runtime.Runtime`은 session summary를 대화 앞에 붙일 때 `prompts/session-summary-context.md`를 쓰고, `session.BuildExtractiveSummary`는 오래된 turn을 `prompts/session-compaction.md`로 압축해요.

## Agent runtime 구현체

패키지는 `agent`예요. `llm.Provider`만 있는 상태에서는 provider 호출과 tool loop를 직접 엮어야 해요. `agent.Agent`는 이 반복 구조를 앱에서 바로 쓸 수 있게 묶어줘요.

주요 타입은 다음과 같아요.

```go
type Config struct {
    Name          string
    Provider      llm.Provider
    Model         string
    Instructions  string
    BaseRequest   llm.Request
    Tools         []llm.Tool
    ToolHandlers  llm.ToolRegistry
    MaxIterations int
    Transcript    *transcript.Transcript
    Observer      Observer
    Guardrails    Guardrails
}

type Agent struct { /* 내부 설정을 보관해요 */ }

type RunResult struct {
    Response *llm.Response
    Trace    []TraceEvent
}

type Observer interface {
    OnEvent(ctx context.Context, event TraceEvent)
}

func OTelObserver(tracer trace.Tracer) Observer
func GlobalOTelObserver(instrumentationName string) Observer

type TraceEvent struct {
    At      time.Time
    Type    string
    Message string
    Tool    string
    Error   string
}

type Guardrails struct {
    BlockedSubstrings       []string
    BlockedOutputSubstrings []string
    InputPolicies           []GuardrailPolicy
    OutputPolicies          []GuardrailPolicy
    RedactTranscript        bool
}

func New(cfg Config) (*Agent, error)
func (a *Agent) Run(ctx context.Context, prompt string) (*RunResult, error)
func (a *Agent) Stream(ctx context.Context, prompt string) (llm.EventStream, error)
```

`Run`은 다음 순서로 실행해요.

1. 입력 guardrail을 검사해요.
2. custom tool과 workspace tool을 합쳐요.
3. `BaseRequest`에 model, instructions, prompt, tools를 얹어요.
4. `llm.RunToolLoop`로 provider와 tool call을 반복해요.
5. `ToolRegistry.WithMiddleware`로 감싼 handler가 tool 시작/완료/실패 event를 trace에 남겨요. 필요하면 `OTelObserver`나 `GlobalOTelObserver`로 같은 event를 OpenTelemetry span으로 내보내요.
6. 출력 guardrail을 검사해요.
7. transcript가 있으면 요청/응답/오류를 저장 가능한 구조로 누적해요.

예제는 이렇게 써요.

```go
ws, err := workspace.New(".")
if err != nil {
    panic(err)
}

client := openai.New(openai.Config{APIKey: os.Getenv("OPENAI_API_KEY")})
tr := transcript.New("session-1")

defs, handlers := tools.StandardTools(tools.SurfaceOptions{Workspace: ws})

ag, err := agent.New(agent.Config{
    Provider:     client,
    Model:        "gpt-5-mini",
    Tools:        defs,
    ToolHandlers: handlers,
    Transcript:   tr,
    Instructions: "너는 Go 코딩 agent예요. 수정 뒤에는 테스트를 실행해야해요.",
    BaseRequest: llm.Request{
        Reasoning: &llm.ReasoningConfig{Effort: "medium", Summary: "auto"},
        Include:   []string{"reasoning.encrypted_content"},
    },
    Guardrails: agent.Guardrails{
        BlockedSubstrings:       []string{"비밀키 출력"},
        BlockedOutputSubstrings: []string{"sk-"},
        OutputPolicies: []agent.GuardrailPolicy{
            agent.JSONRequiredFieldsPolicy("run-result", "summary", "status"),
        },
        RedactTranscript:        true,
    },
    Observer: agent.ObserverFunc(func(ctx context.Context, ev agent.TraceEvent) {
        fmt.Println(ev.Type, ev.Tool, ev.Error)
    }),
})
if err != nil {
    panic(err)
}

result, err := ag.Run(ctx, "테스트를 실행하고 실패 원인을 고쳐줘")
if err != nil {
    panic(err)
}
fmt.Println(result.Response.Text)
fmt.Println("trace events:", len(result.Trace))
_ = tr.SaveRedacted(".kkode/transcript.json")
```

## Gateway API 구현체

패키지는 `gateway`예요. 웹 패널, Discord bot, 외부 SDK가 SQLite를 직접 읽지 않고 같은 HTTP 계약으로 session state를 다루게 하는 경계예요.

주요 타입은 다음과 같아요.

```go
type Config struct {
    Store                session.Store
    StatePath            string
    MinStateFreeBytes    int64
    Version              string
    Commit               string
    APIKey               string
    AllowLocalhostNoAuth bool
    CORSOrigins          []string
    RequestIDGenerator   func() string
    MaxRequestBytes      int64
    MaxConcurrentRuns    int
    RunTimeout           time.Duration
    RunMaxIterations     int
    RunWebMaxBytes       int64
    AccessLogger         AccessLogger
    Providers            []ProviderDTO
    DefaultMCPServers    []ResourceDTO
    DiagnosticChecks     []DiagnosticCheckDTO
    Features             []FeatureDTO
    ResourceStore        session.ResourceStore
    RunStarter           RunStarter
    RunPreviewer         RunPreviewer
    RunValidator         RunValidator
    ProviderTester       ProviderTester
    RunRuntimeStats      RunRuntimeStatsGetter
    RunGetter            RunGetter
    RunLister            RunLister
    RunCounter           RunCounter
    RunCanceler          RunCanceler
    RunEventLister       RunEventLister
    RunSubscriber        RunEventSubscriber
    RunEventSubscriber   RunEventStreamSubscriber
    Now                  func() time.Time
}

type RunStarter func(ctx context.Context, req RunStartRequest) (*RunDTO, error)
type RunGetter func(ctx context.Context, runID string) (*RunDTO, error)
type RunLister func(ctx context.Context, q RunQuery) ([]RunDTO, error)
type RunCanceler func(ctx context.Context, runID string) (*RunDTO, error)
type AccessLogger func(AccessLogEntry)

type AsyncRunManager struct { /* background run 상태, cancel 함수, RunSnapshotStore 원자 저장 경로를 보관해요 */ }

func NewAsyncRunManager(starter RunStarter) *AsyncRunManager
func NewAsyncRunManagerWithStore(starter RunStarter, store session.RunStore) *AsyncRunManager
func (m *AsyncRunManager) Start(ctx context.Context, req RunStartRequest) (*RunDTO, error)
func (m *AsyncRunManager) Get(ctx context.Context, runID string) (*RunDTO, error)
func (m *AsyncRunManager) List(ctx context.Context, q RunQuery) ([]RunDTO, error)
func (m *AsyncRunManager) Cancel(ctx context.Context, runID string) (*RunDTO, error)
func (m *AsyncRunManager) Subscribe(ctx context.Context, runID string) (<-chan RunDTO, func())
func (m *AsyncRunManager) RecoverStaleRuns(ctx context.Context) error

type Server struct { /* net/http handler를 보관해요 */ }

func New(cfg Config) (*Server, error)
func (s *Server) Handler() http.Handler
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request)
```

`RunQuery`는 `session_id`, `turn_id`, `status`, `provider`, `model`, `request_id`, `idempotency_key`, `limit`, `offset`을 받아 외부 adapter가 run dashboard나 turn detail에서 본 provider/model/turn bucket을 그대로 목록 조회로 좁힐 수 있게 해요.

Run 목록 API는 `GET /api/v1/runs?turn_id=...`에서 turn 필터를 지원해서 adapter가 전체 run 목록을 스캔하지 않고 turn detail을 좁힐 수 있게 해요.

현재 endpoint는 다음과 같아요. 스트리밍 코딩 에이전트를 위한 핵심 API만 유지하고 있어요.

```text
GET  /healthz
GET  /readyz
GET  /api/v1
GET  /api/v1/openapi.yaml
GET  /api/v1/providers
GET  /api/v1/providers/{provider}
POST /api/v1/providers/{provider}/test
GET  /api/v1/models
POST /api/v1/sessions
GET  /api/v1/sessions
GET  /api/v1/sessions/{session_id}
POST /api/v1/runs
GET  /api/v1/runs
GET  /api/v1/runs/{run_id}
POST /api/v1/runs/{run_id}/cancel
GET  /api/v1/runs/{run_id}/events
GET  /api/v1/runs/{run_id}/transcript
POST /api/v1/runs/{run_id}/retry
```

`GET /api/v1`은 adapter bootstrap용 discovery index예요. `links` map과 `{name, method, path}` 형태의 `operations` 배열을 함께 반환해서 외부 adapter가 OpenAPI를 다운로드하기 전에도 health/readiness/session/run endpoint를 바로 알 수 있게 해요. `Config.CORSOrigins`는 별도 웹 패널 origin의 preflight와 bearer auth 호출을 허용하고 브라우저가 `X-Request-Id`와 `X-Idempotent-Replay` 응답 header를 읽게 해요. 모든 gateway 응답은 `X-Request-Id`를 보존하거나 생성하고, 실패 응답은 `ErrorEnvelope{error:{code,message,request_id,details}}` 형태로 반환해요. `accessLogMiddleware`는 선택적으로 request id, method, path, status, byte 수, duration을 JSONL로 stderr에 남겨요.

`SessionQuery`는 `project_root`, `provider`, `model`, `mode`, `limit`, `offset`을 받아 dashboard의 session provider/model/mode bucket을 목록 조회로 좁혀요.

Run event replay와 request correlation event replay는 같은 incremental cursor 계약을 공유해요. JSON과 SSE 요청은 durable event를 읽기 전에 `after_seq`를 검증하고, adapter가 polling을 이어가야 하면 JSON 응답에 `next_after_seq`를 노출해요.

Manifest 저장/import 경계에서는 MCP server, skill, subagent config를 검증한 뒤 identifier-like 문자열과 목록을 canonical 값으로 정리해요. 이 때문에 export, preview, run assembly가 같은 resource 값을 사용하고 외부 adapter는 공백/중복이 제거된 manifest 계약을 기준으로 UI cache와 diff를 만들 수 있어요.

```bash
curl -N 'http://127.0.0.1:41234/api/v1/runs/run_.../events?stream=true&after_seq=0'
```

`cmd/kkode-gateway`는 기본적으로 `127.0.0.1:41234`에 bind해요. `/readyz`는 `session.HealthChecker`를 구현한 store ping과 run starter wiring을 확인해서 SQLite 연결이 닫히거나 필수 runtime 경계가 빠진 경우 503을 반환하고, health/ready 성공 응답은 OpenAPI DTO로 고정해요. `0.0.0.0` 같은 remote bind는 `--api-key` 또는 `--api-key-env`가 없으면 거부해야해요.

```bash
go run ./cmd/kkode-gateway -addr 127.0.0.1:41234 -state .kkode/state.db -cors-origins http://localhost:3000
```

`-cors-origins` 또는 `KKODE_CORS_ORIGINS`는 쉼표로 여러 origin을 받을 수 있어요. preflight는 처리하고 브라우저가 `Idempotency-Key` 요청 header를 보내고 `X-Request-Id`와 `X-Idempotent-Replay` 응답 header를 읽을 수 있게 CORS header를 열지만, 실제 API 호출은 bearer auth 정책을 그대로 따라요. 외부 adapter는 `X-Request-Id`를 직접 넣어 여러 시스템 로그를 연결할 수 있고, 넣지 않으면 gateway가 `req_...` 값을 생성해요. `X-Request-Id`, run metadata `request_id`, `Idempotency-Key`, metadata `idempotency_key`는 각각 128 byte까지만 허용하며, 너무 긴 request id header는 그대로 반사하지 않고 새 request id가 붙은 400 오류로 닫아요. 이 값은 background run metadata에도 `request_id`로 저장돼요. `-access-log` 또는 `KKODE_ACCESS_LOG=1`은 `gateway.AccessLogEntry`를 JSONL stderr 로그로 연결해서 컨테이너/VM 배포에서 바로 수집할 수 있게 해요. `KKODE_HTTPJSON_PROVIDERS`는 단일 객체나 배열 JSON으로 OpenAI-compatible HTTP JSON source를 추가해요. gateway와 CLI 모두 시작 시 이 값을 읽고 `RegisterHTTPJSONProvidersFromEnv`로 등록해서 provider discovery, model catalog, session 생성, run preview/test, 실제 run에서 같은 provider 이름을 쓸 수 있게 해요. `max_response_bytes`를 지정하면 해당 HTTP JSON provider의 success/error response body read 한도를 조절하고, 생략하면 기본 32MiB 한도를 쓰며 32MiB를 넘는 값은 시작 시 거부해서 provider가 과도한 응답으로 adapter나 run worker 메모리를 점유하지 못하게 해요. `-max-body-bytes` 또는 `KKODE_MAX_BODY_BYTES`는 JSON API 요청 body 제한을 조절해서 너무 큰 adapter 요청을 빠르게 거부해요. `-max-iterations`/`KKODE_MAX_ITERATIONS`는 128 이하, `-web-max-bytes`/`KKODE_WEB_MAX_BYTES`는 8388608 byte 이하로 시작 시 검증하고, 실제 값은 `GET /api/v1` bootstrap 응답의 limits에 노출해요. `-read-header-timeout`, `-read-timeout`, `-write-timeout`, `-idle-timeout`, `-shutdown-timeout`과 대응 `KKODE_*_TIMEOUT` 환경변수는 프록시, SSE, VM 배포 특성에 맞춰 HTTP lifecycle을 조절해요. `-max-concurrent-runs`/`KKODE_MAX_CONCURRENT_RUNS`는 background run 동시 실행 수를 제한하고, `-run-timeout`/`KKODE_RUN_TIMEOUT`은 run 실행 시간을 제한해요. `-min-state-free-bytes`/`KKODE_MIN_STATE_FREE_BYTES`는 state DB filesystem 여유 공간 warning 기준이고, 0이면 이 diagnostic을 끄며 기본값은 100MiB예요. gateway CLI는 SIGINT/SIGTERM을 `http.Server.Shutdown`으로 처리해서 종료 시 진행 중 요청을 정리하고, `AsyncRunManager.Shutdown`으로 소유 중인 active run을 취소 상태로 저장해요.

OpenAPI 계약은 `gateway/openapi.yaml`에 있어요. 웹 패널과 Discord adapter는 이 계약을 기준으로 붙이면 돼요. `/api/v1` bootstrap 응답은 기존 `links` map과 함께 `{name, method, path}` 형태의 `operations` 배열을 제공해서 OpenAPI를 내려받기 전에도 write/action route method를 추론 없이 알 수 있게 해요. `gateway/openapi_contract_test.go`는 feature catalog endpoint가 OpenAPI paths와 API index operations에서 빠지지 않았는지 검사해요.

## Session runtime 구현체

패키지는 `session`과 `runtime`이에요. `session`은 SQLite에 장기 상태를 저장하고, `runtime`은 `agent.Agent`를 session-aware 실행 단위로 감싸요.

주요 타입은 다음과 같아요.

```go
type Session struct {
    ID             string
    ProjectRoot    string
    ProviderName   string
    Model          string
    AgentName      string
    Mode           AgentMode
    CreatedAt      time.Time
    UpdatedAt      time.Time
    Turns          []Turn
    Events         []Event
    Todos          []Todo
    Summary        string
    LastResponseID string
    LastInputItems []llm.Item
    Metadata       map[string]string
}

type Store interface {
    CreateSession(ctx context.Context, s *Session) error
    LoadSession(ctx context.Context, id string) (*Session, error)
    SaveSession(ctx context.Context, s *Session) error
    ListSessions(ctx context.Context, q SessionQuery) ([]SessionSummary, error)
    AppendEvent(ctx context.Context, ev Event) error
    SaveCheckpoint(ctx context.Context, cp Checkpoint) error
    Close() error
}

func OpenSQLite(path string) (*SQLiteStore, error)
func Fork(ctx context.Context, store Store, sourceID string, atTurnID string) (*Session, error)
func TodoTools(store todoSaver, sessionID string) ([]llm.Tool, llm.ToolRegistry)
```

SQLiteStore는 선택 interface인 `CountSessions`도 제공해서 session list API가 `project_root`/provider/model/mode 필터와 같은 조건의 `total_sessions`를 함께 내려요.

`kruntime.Runtime`은 prompt 실행 전에 기존 turn을 message history로 붙이고, todo tool을 추가하고, 실행 뒤에는 turn/event/todo를 SQLite에 저장해요.

```go
type Runtime struct {
    Store           session.Store
    Agent           *agent.Agent
    ProjectRoot     string
    ProviderName    string
    Model           string
    AgentName       string
    Mode            session.AgentMode
    MaxHistoryTurns int
    Compaction      session.CompactionPolicy
    EnableTodos     bool
}

func (r *Runtime) Run(ctx context.Context, opts RunOptions) (*RunResult, error)
func (r *Runtime) Resume(ctx context.Context, sessionID string) (*session.Session, error)
func (r *Runtime) Fork(ctx context.Context, sessionID string, atTurnID string) (*session.Session, error)
```

예제는 이렇게 써요.

```go
store, err := session.OpenSQLite(".kkode/state.db")
if err != nil {
    panic(err)
}
defer store.Close()

rt := &kruntime.Runtime{
    Store:           store,
    Agent:           ag,
    ProjectRoot:     ".",
    ProviderName:    provider.Name(),
    Model:           "gpt-5-mini",
    MaxHistoryTurns: 8,
    EnableTodos:     true,
}

result, err := rt.Run(ctx, kruntime.RunOptions{
    SessionID: "sess_...",
    Prompt:    "이전 작업을 이어서 테스트를 고쳐줘",
})
if err != nil {
    panic(err)
}
fmt.Println(result.Session.ID, result.Turn.ID)
```

## Agent CLI 구현체

`cmd/kkode-agent`는 위 agent runtime을 바로 실행하는 얇은 앱이에요. provider는 flag 또는 환경변수로 고르고, workspace 파일 작업과 shell 실행은 별도 권한 엔진 없이 바로 실행해요.

주요 flag는 다음과 같아요.

| Flag | 의미 | 기본값 |
|---|---|---|
| `-provider` | `openai`, `omniroute`, `copilot`, `codex` 또는 `KKODE_HTTPJSON_PROVIDERS`로 등록한 provider예요 | `KKODE_PROVIDER` 또는 `openai` |
| `-model` | provider에 넘길 모델이에요 | provider별 기본값이에요 |
| `-root` | workspace root예요 | `.` |
| `-reasoning-effort` | Responses API reasoning effort예요 | 비어 있음 |
| `-reasoning-summary` | reasoning summary 설정이에요 | 비어 있음 |
| `-include` | Responses API include 값이에요 | 비어 있음 |
| `-transcript` | transcript 저장 경로예요 | 비어 있음 |
| `-state` | SQLite session DB 경로예요 | `.kkode/state.db` |
| `-session` | 이어갈 session ID예요 | 비어 있음 |
| `-fork-session` | fork할 원본 session ID예요 | 비어 있음 |
| `-fork-at` | fork 기준 turn ID예요 | 비어 있음 |
| `-list-sessions` | 저장된 session 목록을 출력해요 | `false` |
| `-no-session` | SQLite session 저장을 끄고 단발 실행해요 | `false` |
| `-no-web` | `web_fetch` tool을 비활성화해요 | `false` |
| `-web-max-bytes` | `web_fetch`가 읽을 최대 byte 수예요 | `1048576` |
| `-redact-transcript` | transcript 저장 시 secret을 마스킹해요 | `false` |
| `-blocked-input` | 입력 차단 substring 목록이에요 | 비어 있음 |
| `-blocked-output` | 출력 차단 substring 목록이에요 | 비어 있음 |

`-max-iterations`는 128 이하, `-web-max-bytes`는 8388608 byte 이하로 시작 시 검증해서 CLI 단발 실행도 gateway background run과 같은 bounded agent surface를 써요.

실행 예제는 이렇게 써요.

```bash
go run ./cmd/kkode-agent \
  -provider openai \
  -model gpt-5-mini \
  -root . \
  -reasoning-effort medium \
  -reasoning-summary auto \
  -transcript .kkode/transcript.json \
  "실패하는 테스트를 고치고 검증 결과를 알려줘"
```

## Tool loop 흐름

`llm.RunToolLoop`는 OpenAI Responses API 방식으로 반복해요.

```go
func RunToolLoop(
    ctx context.Context,
    p Provider,
    req Request,
    tools ToolRegistry,
    opts ToolLoopOptions,
) (*Response, error)
```

동작 순서는 다음과 같아요.

1. provider를 호출해요.
2. `Response.ToolCalls`가 없으면 최종 응답을 반환해요.
3. provider가 돌려준 `Response.Output` item을 다음 request에 보존해요.
4. local tool을 실행해요. `ToolRegistry.WithMiddleware`를 쓰면 tracing, timeout, metric 같은 공통 실행 전후 처리를 registry 복사본에 붙일 수 있어요. `ToolLoopOptions.ParallelToolCalls`가 true면 여러 tool call을 `MaxParallelToolCalls` 상한 안에서 비동기로 실행하고 결과 순서는 보존해요.
5. `function_call_output` 또는 `custom_tool_call_output` item을 추가해요.
6. 최대 반복 횟수까지 다시 호출해요.

예제는 이렇게 써요.

```go
registry := llm.ToolRegistry{
    "read_file": llm.JSONToolHandler(func(ctx context.Context, in struct {
        Path string `json:"path"`
    }) (string, error) {
        b, err := os.ReadFile(in.Path)
        if err != nil {
            return "", err
        }
        return string(b), nil
    }),
}

resp, err := llm.RunToolLoop(ctx, provider, req, registry, llm.ToolLoopOptions{
    MaxIterations:        8,
    ParallelToolCalls:    true,
    MaxParallelToolCalls: 4,
})
```

## Provider 구현체

### OpenAI-compatible provider

패키지는 `providers/openai`예요.

주요 생성자와 메서드는 다음과 같아요.

```go
type Config struct {
    BaseURL string
    APIKey string
    ProviderName string // 파생 provider telemetry label이에요.
}
func New(cfg Config) *Client
func (c *Client) Generate(ctx context.Context, req llm.Request) (*llm.Response, error)
func (c *Client) CallProvider(ctx context.Context, req llm.ProviderRequest) (llm.ProviderResult, error)
func (c *Client) Stream(ctx context.Context, req llm.Request) (llm.EventStream, error)
type ResponsesConverter struct { ProviderName string }
func (c ResponsesConverter) ConvertRequest(ctx context.Context, req llm.Request, opts llm.ConvertOptions) (llm.ProviderRequest, error)
func (c ResponsesConverter) ConvertResponse(ctx context.Context, result llm.ProviderResult) (*llm.Response, error)
func BuildResponsesRequest(req llm.Request) (map[string]any, error)
func ParseResponsesResponse(data []byte, providerName string) (*llm.Response, error)
```

`Generate`는 `llm.AdaptedProvider`를 통해 `ResponsesConverter`와 `Client.CallProvider`를 연결해요. `Stream`도 같은 adapter의 `ProviderStreamCaller` 경로를 써서 표준 request를 먼저 OpenAI-compatible payload로 변환한 뒤 SSE API를 호출해요. 그래서 새 OpenAI-compatible 파생 provider는 converter를 재사용하고 caller/stream caller 설정만 바꾸면 돼요. JSON request 생성, bearer auth, custom header 복사, retry/backoff, `Retry-After` backoff 반영, SSE line framing, SSE event당 4194304 byte data envelope, response body read 상한, retry response body drain 상한, HTTP 실패 분류는 `providers/internal/httptransport`를 써서 OmniRoute 같은 파생 provider와 같은 HTTP 처리 규칙을 재사용해요. provider 오류는 `httptransport.HTTPError`로 감싸서 gateway나 외부 adapter가 `errors.As`로 status code와 body를 일관되게 읽을 수 있고, 오류 body가 너무 크면 UTF-8 안전 경계에서 잘라 `[truncated]` marker를 붙여요. `ProviderName`을 지정하면 OpenAI-compatible 파생 provider가 response와 stream event provider label을 자기 이름으로 고정할 수 있어요.

built-in tool helper도 제공해요.

```go
func WebSearchTool(options map[string]any) llm.Tool
func FileSearchTool(vectorStoreIDs []string, maxNumResults int) llm.Tool
func ComputerUseTool(options map[string]any) llm.Tool
func CodeInterpreterTool(options map[string]any) llm.Tool
func ImageGenerationTool(options map[string]any) llm.Tool
func MCPTool(serverLabel, serverURL string, headers map[string]string) llm.Tool
```

예제는 이렇게 써요.

```go
client := openai.New(openai.Config{
    BaseURL: "https://api.openai.com/v1",
    APIKey:  os.Getenv("OPENAI_API_KEY"),
})

stream, err := client.Stream(ctx, llm.Request{
    Model:    "gpt-5-mini",
    Messages: []llm.Message{llm.UserText("짧게 설명해줘")},
})
if err != nil {
    panic(err)
}
defer stream.Close()

for {
    ev, err := stream.Recv()
    if errors.Is(err, io.EOF) {
        break
    }
    if err != nil {
        panic(err)
    }
    if ev.Type == llm.StreamEventTextDelta {
        fmt.Print(ev.Delta)
    }
}
```

### GitHub Copilot SDK provider

패키지는 `providers/copilot`이에요. Copilot은 OpenAI-compatible HTTP API가 아니라 Copilot CLI 기반 JSON-RPC session runtime이에요.

주요 시그니처는 다음과 같아요.

```go
func New(cfg Config) *Client
func (c *Client) Generate(ctx context.Context, req llm.Request) (*llm.Response, error)
func (c *Client) CallProvider(ctx context.Context, req llm.ProviderRequest) (llm.ProviderResult, error)
func (c *Client) Stream(ctx context.Context, req llm.Request) (llm.EventStream, error)
func (c *Client) NewSession(ctx context.Context, req llm.SessionRequest) (llm.Session, error)
type SessionConverter struct{}
func (SessionConverter) ConvertRequest(ctx context.Context, req llm.Request, opts llm.ConvertOptions) (llm.ProviderRequest, error)
func (SessionConverter) ConvertResponse(ctx context.Context, result llm.ProviderResult) (*llm.Response, error)
func ToCopilotTool(tool llm.Tool, handler llm.ToolHandler) copilot.Tool
func ToCopilotMCPServer(server llm.MCPServer) copilot.MCPServerConfig
func ToCopilotAgent(agent llm.Agent) copilot.CustomAgentConfig
func AgentFromConfig(cfg agent.Config, opts AgentConfigOptions) llm.Agent
func CustomAgentConfigFromAgentConfig(cfg agent.Config, opts AgentConfigOptions) copilot.CustomAgentConfig
```

`Generate`는 `llm.AdaptedProvider`를 통해 `SessionConverter`와 `Client.CallProvider`를 연결해요. `Stream`도 같은 adapter와 `ProviderStreamCaller`를 통해 표준 request를 SDK session prompt payload로 먼저 바꾼 뒤 Copilot session event stream에 전달해요. converter는 표준 request를 SDK session prompt payload로 만들고, caller는 Copilot session 생성, send, close lifetime을 관리해요. `AgentFromConfig`는 provider-neutral `agent.Config`의 이름, 지침/context block, tool 정의를 Copilot custom agent용 `llm.Agent`로 옮기고, `CustomAgentConfigFromAgentConfig`는 SDK config까지 바로 만들어서 앱/gateway가 Copilot SDK type을 직접 조립하지 않아도 돼요. SDK session send에서 누적하는 final response text는 8388608 byte envelope로 제한하고, SDK가 요청하는 실행 확인은 별도 권한 시스템으로 끌어올리지 않고 기존 YOLO 승인 handler로 즉시 승인해요.

예제는 이렇게 써요.

```go
wd, err := os.Getwd()
if err != nil {
    panic(err)
}
client := copilot.New(copilot.Config{
    ClientName:       "kkode-app",
    WorkingDirectory: wd,
})
defer client.Close()

resp, err := client.Generate(ctx, llm.Request{
    Model:    "gpt-5-mini",
    Messages: []llm.Message{llm.UserText("정확히 OK만 답해요")},
})
```

### Codex CLI provider

패키지는 `providers/codexcli`예요. `codex exec --json`을 subprocess로 실행해요.

```go
func New(cfg Config) *Client
func (c *Client) Generate(ctx context.Context, req llm.Request) (*llm.Response, error)
func (c *Client) CallProvider(ctx context.Context, req llm.ProviderRequest) (llm.ProviderResult, error)
func (c *Client) Stream(ctx context.Context, req llm.Request) (llm.EventStream, error)
type ExecConverter struct{}
func (ExecConverter) ConvertRequest(ctx context.Context, req llm.Request, opts llm.ConvertOptions) (llm.ProviderRequest, error)
func (ExecConverter) ConvertResponse(ctx context.Context, result llm.ProviderResult) (*llm.Response, error)
```

`Generate`는 `llm.AdaptedProvider`를 통해 `ExecConverter`와 `Client.CallProvider`를 연결해요. `Stream`도 같은 adapter와 `ProviderStreamCaller`를 통해 표준 request를 Codex CLI prompt 실행 payload로 먼저 바꾼 뒤 JSONL subprocess에 전달해요. converter는 표준 request를 Codex CLI prompt 실행 payload로 만들고, caller는 `codex exec --json -a never --sandbox danger-full-access` 흐름을 유지해요. streaming은 stdout JSONL lifetime을 직접 관리해서 event를 `llm.StreamEvent`로 바꾸고, 누적 final response text는 8388608 byte envelope로 제한해요.

예제는 이렇게 써요.

```go
wd, err := os.Getwd()
if err != nil {
    panic(err)
}
client := codexcli.New(codexcli.Config{
    WorkingDirectory: wd,
    Ephemeral:        true,
})

resp, err := client.Generate(ctx, llm.Request{
    Model:    "gpt-5.3-codex",
    Messages: []llm.Message{llm.UserText("정확히 OK만 답해요")},
})
```

### OmniRoute provider

패키지는 `providers/omniroute`예요. OmniRoute는 model vendor가 아니라 routing gateway예요. 그래서 generation은 OpenAI-compatible `/v1/responses`를 사용하고, management 기능은 별도 helper로 분리해요. generation과 management 호출 모두 `providers/internal/httptransport`의 header/auth/body 처리 규칙을 공유해요.

주요 시그니처는 다음과 같아요.

```go
func New(cfg Config) *Client
func NewFromGatewayBase(serverRoot string, cfg Config) *Client
func NewFromOpenAPIServer(serverRoot string, cfg Config) *Client
func (c *Client) Generate(ctx context.Context, req llm.Request) (*llm.Response, error)
func (c *Client) Stream(ctx context.Context, req llm.Request) (llm.EventStream, error)
func (c *Client) ListModels(ctx context.Context) (*ModelList, error)
func (c *Client) Health(ctx context.Context) (*Health, error)
func (c *Client) A2ASend(ctx context.Context, req A2ARequest) (*A2AResponse, error)
func (c *Client) Translate(ctx context.Context, req TranslateRequest) (map[string]any, error)
func (c *Client) GetThinkingBudget(ctx context.Context) (*ThinkingBudget, error)
func (c *Client) UpdateThinkingBudget(ctx context.Context, budget ThinkingBudget) (*ThinkingBudget, error)
func (c *Client) ListFallbackChains(ctx context.Context) (map[string]any, error)
func (c *Client) CreateFallbackChain(ctx context.Context, req CreateFallbackChainRequest) (map[string]any, error)
func (c *Client) DeleteFallbackChain(ctx context.Context, model string) (map[string]any, error)
func (c *Client) CacheStats(ctx context.Context) (map[string]any, error)
func (c *Client) RateLimits(ctx context.Context) (map[string]any, error)
func (c *Client) Sessions(ctx context.Context) (map[string]any, error)
```

OmniRoute는 문서에 `/v1` 경로와 OpenAPI의 `/api/v1` 경로가 같이 보여요. 그래서 생성자를 둘로 나눴어요.

```go
// User Guide 기준이에요: http://localhost:20128/v1
client := omniroute.NewFromGatewayBase("http://localhost:20128", omniroute.Config{
    APIKey:    os.Getenv("OMNIROUTE_API_KEY"),
    SessionID: "kkode-session-1",
    NoCache:   true,
})

// docs/openapi.yaml 기준이에요: http://localhost:20128/api/v1
clientFromSpec := omniroute.NewFromOpenAPIServer("http://localhost:20128", omniroute.Config{})
```

A2A helper 예제는 이렇게 써요.

```go
a2a, err := client.A2ASend(ctx, omniroute.A2ARequest{
    Skill: "smart-routing",
    Messages: []llm.Message{
        llm.UserText("코딩 작업에 가장 싼 라우팅을 추천해줘"),
    },
    Metadata: map[string]any{
        "role":  "coding",
        "model": "auto",
    },
})
if err != nil {
    panic(err)
}
fmt.Println(a2a.Text)
```

## 표준 Tool 구현체

패키지는 `tools`예요. `workspace.Tools()`는 기존 `workspace_*` 이름을 유지하고, `tools.FileTools`는 실제 agent prompt에서 쓰기 쉬운 짧은 표준 이름을 제공해요.

```go
type SurfaceOptions struct {
    Workspace *workspace.Workspace
    NoWeb bool
    WebMaxBytes int64
}
func StandardTools(opts SurfaceOptions) ([]llm.Tool, llm.ToolRegistry)
func FileTools(ws *workspace.Workspace) ([]llm.Tool, llm.ToolRegistry)
func WebTools(cfg WebConfig) ([]llm.Tool, llm.ToolRegistry)
func Fetch(ctx context.Context, cfg WebConfig, rawURL string, maxBytes int64, timeout time.Duration) (*WebFetchResult, error)
```

제공하는 표준 tool 이름은 다음과 같아요.

| Tool | 역할 |
|---|---|
| `file_read` | 파일을 읽고 line range를 지원해요 |
| `file_write` | 파일을 써요 |
| `file_delete` | 파일이나 디렉터리를 삭제해요 |
| `file_move` | 파일이나 디렉터리를 이동하거나 이름을 바꿔요 |
| `file_edit` | old/new 텍스트 교체와 expected replacement count를 지원해요 |
| `file_apply_patch` | apply_patch 형식 patch를 적용해요 |
| `file_restore_checkpoint` | file checkpoint를 복구해요 |
| `file_prune_checkpoints` | 최신 checkpoint만 남기고 오래된 snapshot을 삭제해요 |
| `file_list` | 디렉터리를 나열해요 |
| `file_glob` | glob으로 파일을 찾아요 |
| `file_grep` | literal/regex 검색을 해요 |
| `shell_run` | command를 실행하고 exit code/stdout/stderr/duration이 있는 JSON `CommandResult`를 반환해요 |
| `web_fetch` | HTTP/HTTPS URL을 가져와 JSON `WebFetchResult`를 반환해요 |

`cmd/kkode-agent`는 기본적으로 `FileTools`와 `WebTools`를 agent에 붙여요. `web_fetch`를 끄고 싶으면 `-no-web`을 사용해요.

## Workspace 실행 정책

현재 제품 방향은 빠른 구현 검증을 위해 권한 엔진을 완전히 제거하고 항상 실행 모드로 단순화해요. `cmd/kkode-agent`, `cmd/kkode-gateway`, `workspace`는 파일 쓰기와 shell 실행을 묻지 않고 바로 수행해요.

승인 정책 타입, 읽기 전용 모드, 명령 허용 목록, 보호 경로 차단은 코드에서 제거했어요. 외부 provider가 권한 callback을 요구하는 경우에도 Copilot provider는 항상 approve를 반환하고, Codex CLI adapter는 `-a never`와 `danger-full-access` sandbox 기본값으로 실행해요.

## Workspace 구현체

패키지는 `workspace`예요. provider tool로 붙일 수 있는 local workspace adapter예요.

```go
func New(root string) (*Workspace, error)
func (w *Workspace) Resolve(rel string) (string, error)
func (w *Workspace) ReadFile(rel string) (string, error)
func (w *Workspace) ReadFileRange(rel string, opts ReadOptions) (string, error)
func (w *Workspace) WriteFile(rel, content string) error
func (w *Workspace) ReplaceInFile(rel, old, new string) error
func (w *Workspace) EditFile(rel, old, new string, expectedReplacements int) error
func (w *Workspace) ApplyPatch(patchText string) error
func (w *Workspace) List(rel string) ([]string, error)
func (w *Workspace) Glob(pattern string) ([]string, error)
func (w *Workspace) Search(needle string) ([]string, error)
func (w *Workspace) Grep(pattern string, opts GrepOptions) ([]SearchMatch, error)
func (w *Workspace) Run(ctx context.Context, command string, args ...string) (string, error)
func (w *Workspace) RunDetailed(ctx context.Context, command string, args []string, opts CommandOptions) (CommandResult, error)
func (w *Workspace) Tools() (defs []llm.Tool, handlers llm.ToolRegistry)
```

예제는 이렇게 써요.

```go
ws, err := workspace.New(".")
if err != nil {
    panic(err)
}

toolDefs, handlers := ws.Tools()
req.Tools = append(req.Tools, toolDefs...)
resp, err := llm.RunToolLoop(ctx, provider, req, handlers, llm.ToolLoopOptions{ParallelToolCalls: true})
```

## Transcript 구현체

패키지는 `transcript`예요.

```go
func New(id string) *Transcript
func Load(path string) (*Transcript, error)
func (t *Transcript) Add(req llm.Request, resp *llm.Response, err error)
func (t *Transcript) Save(path string) error
func (t *Transcript) SaveRedacted(path string) error
```

`Load`, `Save`, `SaveRedacted`는 transcript JSON 파일을 최대 8388608 byte로 제한해서 CLI transcript 경로가 과도한 파일을 한 번에 읽거나 쓰지 않게 해요.

예제는 이렇게 써요.

```go
tr := transcript.New("session-1")
resp, err := provider.Generate(ctx, req)
tr.Add(req, resp, err)
if err := tr.SaveRedacted(".kkode/transcript.json"); err != nil {
    panic(err)
}
```

## Provider routing 전략

`llm.Router`는 `provider/model` 형식을 지원해요.

```go
func NewRouter() *Router
func (r *Router) Register(name string, provider Provider)
func (r *Router) Alias(prefix, provider string)
func (r *Router) ProviderFor(model string) (Provider, string, error)
func (r *Router) Generate(ctx context.Context, req Request) (*Response, error)
```

예제는 이렇게 써요.

```go
router := llm.NewRouter()
router.Register("default", openAIProvider)
router.Register("omniroute", omniRouteProvider)

resp, err := router.Generate(ctx, llm.Request{
    Model:    "omniroute/auto",
    Messages: []llm.Message{llm.UserText("테스트해줘")},
})
```

## 보안 경계

현재 보안 경계는 다음과 같아요.

- 기본 CLI는 write/replace/apply_patch/shell을 바로 실행할 수 있어요.
- workspace path는 root 바깥으로 탈출할 수 없게 막지만, root 안 보호 경로 차단은 하지 않아요.
- `agent.Guardrails`는 입력/출력 substring 차단과 `GuardrailPolicy` hook을 제공해서 adapter가 JSON 필수 field 같은 schema형 검사나 조직별 policy 함수를 agent loop 바깥에서 재사용할 수 있게 해요.
- transcript는 `SaveRedacted`로 token/API key 패턴을 지워 저장할 수 있어요.
- provider OAuth/token 저장은 provider package가 소유해야해요.
- MCP tool은 session/tool attachment로 취급하고, core provider method로 섞지 않아야해요.

## 다음 작업 방향

다음 단계는 아래 순서로 가면 좋아요.

1. OpenAI-compatible chat/embedding/image provider surface를 추가해요.
2. OmniRoute `/api/models/catalog`, `/api/combos`, `/api/combos/metrics`, `/api/resilience` typed helper를 더 추가해요.
3. Codex app-server/harness provider를 CLI adapter와 분리해서 추가해요.
4. streaming aggregation과 event replay를 강화해요.
5. workspace patch tool과 command 실행 로그를 추가해요.
6. agent handoff/session memory를 `SessionProvider`와 연결해요.
7. Copilot custom agent와 OpenAI hosted MCP tool을 같은 설정 파일에서 선언할 수 있게 해요.
