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
├── app/                             # CLI/gateway가 공유하는 provider/agent 조립 도우미예요
│   ├── provider_registry.go          # provider spec, converter profile, source factory registry예요
│   └── provider_conversion.go        # 실행 없이 provider 요청 preview를 만드는 변환 도우미예요
├── agent/                           # 실제 coding agent loop와 guardrail/trace예요
├── session/                         # SQLite session store와 resume/fork 상태예요
├── runtime/                         # agent와 session store를 묶는 실행 runtime이에요
├── prompts/                         # system/session/todo prompt 템플릿이에요
├── cmd/
│   ├── kkode-agent/                  # provider 선택형 agent CLI예요
│   └── kkode-gateway/                # HTTP gateway API server예요
├── llm/                              # provider-neutral core예요
│   ├── model.go                      # 모델 registry와 pricing 타입이에요
│   ├── conversion.go                 # provider 변환/호출 인터페이스와 adapter예요
│   ├── pipeline.go                   # 요청→변환→source 호출→응답 변환 파이프라인이에요
│   ├── prompt.go                     # 메시지 helper예요
│   ├── redact.go                     # secret redaction helper예요
│   ├── router.go                     # provider/model router예요
│   ├── session.go                    # session provider 인터페이스예요
│   ├── stream.go                     # streaming event 인터페이스예요
│   ├── template.go                   # prompt template helper예요
│   ├── tools.go                      # tool registry와 tool loop예요
│   ├── types.go                      # 핵심 request/response/tool 타입이에요
│   ├── usage.go                      # usage cost helper예요
│   └── validate.go                   # request validation이에요
├── providers/
│   ├── internal/httptransport/       # provider 공통 JSON HTTP transport helper예요
│   ├── openai/                       # OpenAI-compatible Responses 변환/caller provider예요
│   ├── copilot/                      # GitHub Copilot SDK adapter예요
│   ├── codexcli/                     # Codex CLI subprocess 변환/caller adapter예요
│   └── omniroute/                    # OmniRoute gateway adapter예요
├── gateway/                         # session/run/event HTTP API와 OpenAPI 계약이에요
├── workspace/                        # workspace file/write/replace/search/shell tool이에요
├── tools/                            # 표준 file/web/shell tool 이름 adapter예요
├── transcript/                       # transcript 저장소예요
├── scripts/                          # 검증용 smoke scripts예요
├── research/                         # 조사 문서와 TODO예요
└── suggest/                          # 다음 구현 제안과 roadmap이에요
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

단발성 흐름은 `llm.Request → RequestConverter → ProviderRequest → ProviderCaller → ProviderResult → ResponseConverter → llm.Response`예요. 이 흐름은 `ProviderPipeline`이 실제 단계로 나눠서 실행해요. `Prepare`는 변환 preview나 debug UI가 재사용하고, `Call`은 API/SDK/CLI source 경계만 담당하며, `Decode`는 source 결과를 다시 표준 응답으로 맞춰요. 그래서 새 API를 붙일 때 core 타입을 수정하지 않고 converter와 caller만 추가하거나, OpenAI-compatible request builder와 별도 API caller/response parser를 조합하면 돼요. OpenAI-compatible HTTP JSON 파생 API는 `providers/httpjson.Caller`에 base URL과 operation route만 넣어서 source client 중복 없이 붙일 수 있고, `app.BuildHTTPJSONProviderAdapter`는 registry route를 기본값으로 읽어 `BaseURL/APIKey/ProviderName`만으로 `llm.Provider`를 만들어요. `app.RegisterHTTPJSONProvider`는 같은 profile을 별도 provider 이름으로 discovery/routing까지 등록해서 proxy, gateway, 사내 API처럼 converter가 같은 source를 설정만으로 추가하게 해요. HTTP JSON route는 `Path`와 `Query` template를 지원해서 `{model}`, `{operation}`, `{metadata.key}` 또는 `{key}` 값을 `ProviderRequest.Metadata`에서 꺼내 endpoint를 만들어요. gateway run/test `metadata`는 provider request metadata까지 전달되므로 웹 패널이나 Discord adapter가 넣은 `trace_id`, `deployment`, `api_version` 같은 값을 source route 조립에 그대로 쓸 수 있어요. `ProviderRequestPreview.Route`는 매칭 route와 resolved path/query를 노출해서 live 호출 전에 endpoint template 누락을 확인하게 해요. 그래서 `/providers/{provider}/models/{model}/generate?api-version={metadata.api_version}`처럼 OpenAI-compatible이 아닌 HTTP API도 caller를 새로 쓰지 않고 converter metadata만 채워 붙일 수 있어요. `MaxResponseBytes`/`max_response_bytes`는 HTTP JSON source의 success/error body 상한을 조절하고, 0이면 기본 32MiB를 쓰며 32MiB보다 큰 값은 adapter 생성/등록에서 거부해요. gateway는 이 상한을 `/capabilities.limits.max_http_json_response_bytes`로 노출해요. success body가 상한을 넘으면 partial JSON decode 대신 실패하고, error body는 상한에서 잘린 뒤 `HTTPError.Body`에 `[truncated]` marker를 붙여 남겨요. `DisableStreaming`을 켜면 registry의 OpenAI-compatible capability에서 `streaming`만 꺼서 JSON-only source가 SSE 지원처럼 광고되지 않게 해요. 기본 OpenAI-compatible client의 단발 호출도 이 caller를 사용해서 새 provider와 같은 transport 경계를 검증해요. `AdaptedProvider`는 이 pipeline을 감싼 `llm.Provider` 구현체라서 기존 provider는 간단한 struct 조립만 유지해요.

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

`llm.Router`는 `provider/model` 직접 지정과 alias prefix routing을 지원하고, alias가 겹치면 가장 긴 prefix를 우선해서 provider 선택이 Go map 순서에 흔들리지 않게 해요. `ProviderSpecs`는 provider registry에서 방어 복사한 spec을 돌려줘서 CLI/gateway/provider 기본 모델, 인증 상태, 변환 profile 표시를 공유해요. 같은 registry entry는 실행형 `ProviderConversionSet`도 들고 있어서 `PreviewProviderRequest`, `BuildProviderPipeline`, `BuildProviderAdapter`, `BuildHTTPJSONProviderAdapter`가 모두 같은 converter, operation 기본값, HTTP route metadata를 써요. 그래서 새 provider를 추가할 때 spec, alias, capability, 변환 profile, source 생성 로직을 한 entry에 맞추면 되고, OpenAI-compatible 파생 API는 `RegisterHTTPJSONProvider`나 `KKODE_HTTPJSON_PROVIDERS` JSON으로 profile, `BaseURL`, API key/env, route만 지정하거나 전용 `ProviderCaller`/`ProviderStreamCaller` source만 바꾸면 돼요. 외부 패키지나 테스트 plugin은 `RegisterProvider`로 `ProviderRegistration`을 런타임에 추가하고 반환된 unregister 함수로 되돌릴 수 있어서 core registry 파일을 직접 수정하지 않아도 돼요. registry는 mutex로 보호하고 `ProviderSpecs`/`ResolveProviderSpec`이 방어 복사본을 반환해서 discovery 호출과 테스트 등록이 서로 slice/map을 오염시키지 않게 해요. gateway의 provider discovery도 `conversion.routes[]`를 노출해서 웹 패널이 실제 API 호출 route를 preview/debug 화면에 보여줄 수 있어요. `BuildProviderWithOptions`는 `DefaultProviderOptions(root)`와 저장 resource manifest를 `MergeProviderOptions`로 합친 뒤 같은 registry entry의 factory를 실행해요. 기본 MCP는 Context7 원격 HTTP MCP(`https://mcp.context7.com/mcp`)와 Serena stdio MCP예요. Serena는 `uvx` 또는 `KKODE_SERENA_COMMAND`가 있을 때만 붙여서 실행 환경에 없는 바이너리 때문에 기본 run이 깨지지 않게 해요. `KKODE_DEFAULT_MCP=off`면 기본 MCP를 붙이지 않아요. `ProviderHandle.BaseRequest`는 OpenAI-compatible HTTP MCP를 hosted `mcp` tool로 넘기고, Copilot은 stdio/http MCP를 SDK session config로 넘겨요. CLI와 gateway는 `MergeBaseRequest` 또는 `AgentOptions.BaseRequest`로 이 기본 request를 agent에 전달해요. `NewRuntime`은 history/todo/compaction 기본값을 CLI와 gateway가 같은 방식으로 쓰게 해요. `NewAgent`는 `tools.StandardTools`를 통해 `tools.FileTools`와 선택적 `tools.WebTools`를 같은 방식으로 붙여요. 예전 `workspace_*` tool은 `workspace.Workspace.Tools()`로 직접 사용할 수 있지만, 일반 agent 표면에는 `file_read`, `file_write`, `file_edit`, `file_apply_patch`, `file_list`, `file_glob`, `file_grep`, `shell_run`, `web_fetch`만 노출해요.

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
5. `ToolRegistry.WithMiddleware`로 감싼 handler가 tool 시작/완료/실패 event를 trace에 남겨요.
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
    Version              string
    Commit               string
    APIKey               string
    AllowLocalhostNoAuth bool
    CORSOrigins          []string
    RequestIDGenerator   func() string
    MaxRequestBytes      int64
    AccessLogger         AccessLogger
    Providers            []ProviderDTO
    Features             []FeatureDTO
    ResourceStore        session.ResourceStore
    RunStarter           RunStarter
    RunGetter            RunGetter
    RunLister            RunLister
    RunCanceler          RunCanceler
    RunSubscriber        RunEventSubscriber
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

현재 endpoint는 다음과 같아요.

```text
GET  /healthz
GET  /readyz
GET  /api/v1
GET  /api/v1/openapi.yaml
GET  /api/v1/version
GET  /api/v1/capabilities
GET  /api/v1/diagnostics
GET  /api/v1/providers
GET  /api/v1/providers/{provider}
POST /api/v1/providers/{provider}/test
GET  /api/v1/models
GET  /api/v1/stats
GET  /api/v1/prompts
GET  /api/v1/prompts/{template_name}
POST /api/v1/prompts/{template_name}/render
GET  /api/v1/tools
GET  /api/v1/tools/{tool}
POST /api/v1/tools/call
GET  /api/v1/files
GET  /api/v1/files/content
PUT  /api/v1/files/content
GET  /api/v1/git/status
GET  /api/v1/git/diff
GET  /api/v1/git/log
POST /api/v1/sessions
GET  /api/v1/sessions
GET  /api/v1/sessions/{session_id}
GET  /api/v1/sessions/{session_id}/turns
GET  /api/v1/sessions/{session_id}/turns/{turn_id}
POST /api/v1/sessions/import
GET  /api/v1/sessions/{session_id}/export
GET  /api/v1/sessions/{session_id}/transcript
POST /api/v1/sessions/{session_id}/compact
POST /api/v1/sessions/{session_id}/fork
GET  /api/v1/sessions/{session_id}/events
GET  /api/v1/sessions/{session_id}/checkpoints
POST /api/v1/sessions/{session_id}/checkpoints
GET  /api/v1/sessions/{session_id}/checkpoints/{checkpoint_id}
GET  /api/v1/sessions/{session_id}/todos
PUT  /api/v1/sessions/{session_id}/todos
POST /api/v1/sessions/{session_id}/todos
DELETE /api/v1/sessions/{session_id}/todos/{todo_id}
GET  /api/v1/mcp/servers
POST /api/v1/mcp/servers
GET  /api/v1/mcp/servers/{resource_id}
PUT  /api/v1/mcp/servers/{resource_id}
DELETE /api/v1/mcp/servers/{resource_id}
GET  /api/v1/mcp/servers/{resource_id}/tools
GET  /api/v1/mcp/servers/{resource_id}/resources
GET  /api/v1/mcp/servers/{resource_id}/resources/read
GET  /api/v1/mcp/servers/{resource_id}/prompts
POST /api/v1/mcp/servers/{resource_id}/prompts/{prompt_name}/get
POST /api/v1/mcp/servers/{resource_id}/tools/{tool_name}/call
GET  /api/v1/skills
POST /api/v1/skills
GET  /api/v1/skills/{resource_id}
PUT  /api/v1/skills/{resource_id}
DELETE /api/v1/skills/{resource_id}
GET  /api/v1/skills/{resource_id}/preview
GET  /api/v1/subagents
POST /api/v1/subagents
GET  /api/v1/subagents/{resource_id}
PUT  /api/v1/subagents/{resource_id}
DELETE /api/v1/subagents/{resource_id}
GET  /api/v1/subagents/{resource_id}/preview
GET  /api/v1/lsp/symbols
GET  /api/v1/lsp/document-symbols
GET  /api/v1/lsp/definitions
GET  /api/v1/lsp/references
GET  /api/v1/lsp/rename-preview
GET  /api/v1/lsp/format-preview
GET  /api/v1/lsp/diagnostics
GET  /api/v1/lsp/hover
GET  /api/v1/runs
POST /api/v1/runs
GET  /api/v1/runs/{run_id}
GET  /api/v1/runs/{run_id}/events
GET  /api/v1/runs/{run_id}/transcript
GET  /api/v1/requests/{request_id}/runs
GET  /api/v1/requests/{request_id}/events
GET  /api/v1/requests/{request_id}/transcript
POST /api/v1/runs/{run_id}/cancel
POST /api/v1/runs/{run_id}/retry
```

`GET /api/v1`은 adapter bootstrap용 discovery index라서 health/readiness/OpenAPI/capabilities/session/run/transcript/event/preview 대표 URL을 한 번에 찾게 해요. `Config.CORSOrigins`는 별도 웹 패널 origin의 preflight와 bearer auth 호출을 허용하고 브라우저가 `X-Request-Id`와 `X-Idempotent-Replay` 응답 header를 읽게 해요. `requestIDMiddleware`는 모든 응답에 `X-Request-Id`를 붙이고 client가 보낸 ID를 공개 `ErrorEnvelope` DTO에도 보존해서 Discord/Slack/web adapter 로그와 오류 처리를 같은 요청으로 묶게 해요. `securityHeadersMiddleware`는 모든 HTTP 응답에 `X-Content-Type-Options: nosniff`를 붙여 브라우저 기반 패널에서 content sniffing을 줄여요. `accessLogMiddleware`는 선택적으로 `AccessLogEntry`를 발행해서 host app이 request id, method, path, status, byte 수, duration을 structured log나 metric으로 재사용하게 해요. `startRun`/`validateRun`/`previewRun`/`retryRun`은 같은 request id를 `RunDTO.Metadata["request_id"]`에 주입하고, 기본 MCP 이름은 `RunDTO.Metadata["default_mcp_servers"]`에 정렬된 쉼표 목록으로 남겨서 durable replay와 외부 adapter 실행 자산 표시가 명시 resource ID만 보고 흔들리지 않게 해요. `GET /api/v1/openapi.yaml`은 gateway가 실행 중인 API 계약을 그대로 제공하고 모든 operation에 표준 `ErrorEnvelope` response reference를 포함해서 SDK 생성과 adapter smoke test에 쓸 수 있게 해요. `GET /api/v1/capabilities`는 외부 adapter가 gateway의 구현/연결 상태, provider capability key catalog, `요청 → 변환 → API/source 호출 → 응답 변환` pipeline catalog, 기본 Serena/Context7 MCP manifest, `limits.max_request_bytes`, `limits.max_request_id_bytes`, `limits.max_idempotency_key_bytes`, `limits.max_tool_call_name_bytes`, `limits.max_tool_call_id_bytes`, `limits.max_tool_call_argument_bytes`, `limits.max_tool_call_output_bytes`, `limits.max_tool_call_web_bytes`, `limits.max_shell_timeout_ms`, `limits.max_shell_output_bytes`, `limits.max_shell_stderr_bytes`, `limits.max_concurrent_runs`, `limits.run_timeout_seconds`, `limits.max_mcp_http_response_bytes`, `limits.max_mcp_probe_name_bytes`, `limits.max_mcp_probe_uri_bytes`, `limits.max_mcp_probe_argument_bytes`, `limits.max_mcp_probe_output_bytes`, `limits.max_file_content_bytes`, `limits.max_workspace_file_read_bytes`, `limits.max_workspace_file_write_bytes`, `limits.max_workspace_list_entries`, `limits.max_workspace_glob_matches`, `limits.max_workspace_grep_matches`, `limits.max_workspace_patch_bytes`, `limits.max_skill_preview_bytes`, `limits.max_lsp_format_input_bytes`, `limits.max_lsp_format_preview_bytes`, `limits.max_run_prompt_bytes`, `limits.max_run_selector_items`, `limits.max_run_context_blocks` 같은 운영 제한값을 discovery할 수 있게 해요. 각 provider의 capability map은 true 값만 노출하므로, 외부 adapter는 `provider_capabilities` catalog를 기준으로 빠진 key를 false처럼 해석하면 돼요. `provider_pipeline`은 preview/test/run UI가 같은 단계 이름을 쓰게 해줘요. `GET /api/v1/diagnostics`는 store ping, run starter/previewer/validator/provider tester와 run 조회/취소/event stream 연결, provider auth 상태, provider/default MCP 개수, Serena/Context7 기본 MCP 상태, 동시 run 제한, run timeout, 현재 queued/running/cancelling queue 상태 같은 배포 진단값을 한 번에 보여주고, provider auth나 필수 runtime wiring 같은 hard check가 실패하면 `ok=false`, `failing_checks`, 필요한 env 힌트를 반환해요. Serena 실행 command가 없는 것처럼 optional default MCP가 빠진 경우는 `warning` check로 노출해서 운영자가 원인을 볼 수 있지만 gateway readiness를 실패시키지는 않고, 필수 runtime wiring이 빠지면 `missing_runtime_wiring` 목록과 `/readyz` 오류 details에도 같은 목록을 담아요. default MCP discovery/preview 응답은 header/env secret 값을 마스킹해요. `GET /api/v1/providers`와 `GET /api/v1/providers/{provider}`는 provider별 alias, auth env 힌트, converter/caller/source/operation 변환 profile을 포함하고, `POST /api/v1/providers/{provider}/test`는 session 없이 provider 요청 변환 preview와 선택적 `live=true` smoke 결과를 반환해서 provider debug 화면이 실행 전 인증/변환 문제를 확인하게 해요. `timeout_ms`를 주면 live smoke context를 그 시간 안에 끊고, 0이면 기본 60초를 사용해요. `max_result_bytes`를 주면 secret 마스킹 뒤 결과 text를 UTF-8 안전 byte 경계에서 잘라 `text_bytes`와 `text_truncated`로 알려줘요. provider test 실패는 `ok=false`, 안정적인 `code`, 사람이 읽는 `message`를 함께 반환해 외부 adapter가 문자열 파싱 없이 분기하게 해요. `live=true`에서 인증 환경변수가 없으면 source 호출 전에 `provider_auth_missing`으로 중단하고 변환 preview는 계속 보여줘요. provider/capabilities discovery는 provider 이름순으로 정렬하고 `total_providers`, `limit`, `offset`, `next_offset`, `result_truncated` page metadata를 제공하며, `GET /api/v1/models`는 provider 이름순 안에서 기본 모델을 먼저 둔 뒤 나머지를 정렬하고 `total_models`, `limit`, `offset`, `next_offset`, `result_truncated` page metadata를 제공해서 모델 선택 UI와 provider debug 화면의 cache/diff가 config 주입 순서나 큰 catalog에 흔들리지 않게 해요. `GET /api/v1/stats`는 dashboard adapter가 sessions/turns/events/todos/checkpoints/runs/resources 카운트와 `total_runs`, `total_resources`를 한 번에 읽게 해요. `GET /api/v1/prompts` 계열은 system/session/todo prompt template 목록, 원문, 렌더링 preview를 제공하고 prompt list pagination metadata와 `max_text_bytes`, `text_bytes`, `text_truncated` preview metadata를 함께 내려서 외부 패널이 prompt 설정 화면을 만들 수 있게 해요. `GET /api/v1/tools`, `GET /api/v1/tools/{tool}`, `POST /api/v1/tools/call`은 표준 file/shell/web/codeintel tool surface를 API로 직접 노출하고, 같은 local tool surface를 agent run에도 붙여 provider tool call에서 `lsp_*` codeintel 도구와 선택된 MCP server용 `mcp_call`을 실행하게 하며, `enabled_tools`/`disabled_tools`로 run 단위 선택을 적용하고, tool list pagination metadata와 tool별 `category`, UI 표시용 `effects`, `output_format`, JSON Schema `parameters`, 안전한 `example_arguments`, `requires_workspace`를 알려주며, `web_fetch`는 `project_root` 없이도 실행하고, LSP codeintel tool은 symbols/definitions/references/hover/diagnostics를 JSON output으로 반환하며, 직접 tool 호출은 tool 이름, call id, arguments JSON 크기를 먼저 제한하고, `timeout_ms`로 전체 시간을 제한하며, `max_output_bytes`로 큰 output을 adapter 친화적으로 자를 수 있게 해요. `GET /api/v1/git/status`, `/diff`, `/log`는 패널이 변경사항과 commit 흐름을 명령 조립 없이 렌더링하게 해요. status 응답은 `total_entries`, `limit`, `entries_truncated`, `output_truncated`를 포함하고 log 응답은 `limit`, `commits_truncated`를 포함해서 변경 파일이나 commit이 많은 repo도 bounded list처럼 다루게 해요. git stdout/stderr byte 제한도 UTF-8 문자 경계를 보존해서 한글 commit/diff와 오류가 깨지지 않게 해요. `GET /api/v1/files`, `GET/PUT /api/v1/files/content`, `POST /api/v1/files/patch`, `GET /api/v1/files/glob`, `GET /api/v1/files/grep`는 웹 패널 파일 브라우저와 검색 UI가 쓰기 쉬운 전용 wrapper예요. 파일 목록 응답은 최대 5000개 entry envelope 안에서 `total_entries`, `limit`, `offset`, `next_offset`, `entries_truncated`로 큰 디렉터리의 page 상태를 알려줘요. 파일 content 응답은 `content_bytes`, `file_bytes`, `content_truncated`를 포함해서 대용량 preview 상태를 명확히 보여주고, patch 응답은 `patch_bytes`로 적용한 patch request 크기를 알려주며, `max_bytes` 제한은 UTF-8 문자 경계를 보존해서 한글/이모지 preview가 깨지지 않게 해요. glob 응답은 최대 5000개 match envelope 안에서 `total_paths`, `limit`, `offset`, `next_offset`, `paths_truncated`로 page 상태를 알려주고, grep 응답은 최대 1000개 match envelope 안에서 `limit`, `result_truncated`로 검색 결과 잘림 상태를 알려줘요. 이 endpoint도 권한 프롬프트 없이 project root 기준으로 즉시 실행해요. MCP server, skill, subagent는 `session.ResourceStore`와 `resources` SQLite table에 manifest로 저장해요. MCP stdio/http manifest는 `/api/v1/mcp/servers/{resource_id}/tools`, `/resources`, `/prompts`로 `initialize` 뒤 `tools/list`, `resources/list`, `prompts/list` probe를 실행할 수 있고, live MCP catalog 응답은 `limit`, `offset`, `next_offset`, `result_truncated`로 page 상태를 알려주며, MCP tool 목록은 `input_schema`, schema 기반 `example_arguments`, `category/effects/output_format`을 함께 반환하며, `/resources/read`, `/prompts/{prompt_name}/get`, `/tools/{tool_name}/call`로 resource/prompt/tool 동작을 직접 검증할 수 있어요. MCP resource/prompt preview는 URI/이름/arguments와 max byte 제한, 원본 byte/truncated metadata를 제공하고, MCP tool 직접 호출은 `max_output_bytes`, `result_bytes`, `result_truncated`를 제공해서 큰 text/blob/output payload를 웹/Discord adapter가 안전하게 렌더링하게 해요. `GET /api/v1/skills/{resource_id}/preview`는 저장된 skill directory의 `SKILL.md` 또는 `README.md`를 읽고 `markdown_bytes`, `markdown_truncated`로 잘림 상태를 알려주며 외부 패널 preview로 돌려줘요. Skill manifest는 `path` 또는 `directory`가 실제 존재하고 directory일 때 `SKILL.md`/`README.md`/`skill.md` 중 하나가 있어야 run에 연결돼요. `GET /api/v1/subagents/{resource_id}/preview`는 subagent prompt, tools, MCP server alias/id, skill 참조를 실행 전 확인하고 `max_prompt_bytes`, `prompt_bytes`, `prompt_truncated`로 큰 prompt preview를 안전하게 잘라요. Subagent manifest는 `mcp_server_ids`로 저장된 MCP resource를 재사용하거나 `mcp_servers`에 legacy command 문자열 또는 stdio/http MCP object를 inline으로 넣을 수 있어요. `RunStartRequest.mcp_servers`, `skills`, `subagents`는 저장된 manifest ID 목록이고, `RunStartRequest.context_blocks`는 Discord thread 요약이나 웹 패널 임시 지침처럼 저장 resource 없이 이번 run에만 붙일 provider-neutral context예요. gateway는 이 context를 실행/영속화 전에 secret 마스킹하고 UTF-8 안전 byte 경계에서 길이와 개수를 제한하며, run prompt와 실행 자산 selector도 queue 전에 제한해요. `cmd/kkode-gateway`는 이 둘을 `app.ProviderOptions`로 변환해서 Copilot 같은 provider 설정에 연결해요. 동시에 요청 context, skill markdown, subagent prompt/tools/skills/MCP 요약을 `ContextBlocks`로 만들어 agent system prompt에 붙이므로 OpenAI-compatible, Codex CLI, OmniRoute 같은 provider도 같은 실행 자산을 참고해요. `POST /api/v1/runs/preview`는 이 context를 `context_blocks`와 `context_truncated`로 노출해서 웹 패널이 실행 전에 실제 추가 지침을 확인하게 해요. 선택한 manifest가 `enabled=false`이면 provider를 만들기 전에 오류를 반환해서 비활성 실행 자산이 조용히 섞이지 않게 해요. `GET /api/v1/lsp/symbols`는 files/git API와 같은 workspace root 검증을 거친 뒤 Go parser 기반 workspace symbol index를 반환하고, `GET /api/v1/lsp/document-symbols`는 파일 하나의 outline을 반환해요. `GET /api/v1/lsp/definitions`와 `GET /api/v1/lsp/references`는 `symbol` 이름 또는 `path,line,column` 커서 위치에서 찾은 Go 식별자를 기준으로 definition/reference 위치와 excerpt를 반환해서 외부 패널의 go-to-definition/reference view를 만들 수 있게 해요. `GET /api/v1/lsp/rename-preview`는 같은 symbol/cursor query와 `new_name`을 받아 파일을 수정하지 않고 rename 후보 edit 목록을 반환해요. `GET /api/v1/lsp/format-preview`는 Go 파일을 쓰지 않고 gofmt 결과와 변경 여부, UTF-8 안전 content preview를 반환해요. `GET /api/v1/lsp/diagnostics`는 Go parser diagnostic을 반환하고, `GET /api/v1/lsp/hover`는 같은 `symbol` 또는 cursor query로 symbol signature와 doc comment를 반환해요. LSP list 응답은 `limit`과 `result_truncated`를 포함해서 코드 탐색 결과가 더 있는지 표시해요. LSP scan은 공통 `walkParsedGoFiles` helper로 `node_modules`, `vendor`, `.omx` 같은 무거운 디렉터리를 건너뛰며 `limit`에 도달하면 scan을 조기 중단해요. 이 manifest의 `config`에는 stdio/http MCP 설정, skill path/prompt, subagent prompt/tools/skills 같은 provider별 설정을 담아요. `POST /api/v1/runs/preview`는 실행 없이 provider/model/default MCP/선택 manifest/base request tool 조립 결과와 `llm.Request -> RequestConverter -> ProviderRequest` 변환 preview를 돌려줘서 외부 adapter가 UI 확인 단계와 provider debug 화면을 만들 수 있게 해요. `preview_stream=true`면 `ProviderPipeline.PrepareStream` 경로를 사용해서 SSE/SDK streaming payload도 실제 호출 없이 확인하게 해요. preview의 body/raw/context JSON은 secret 마스킹과 `max_preview_bytes` 길이 제한을 적용하고 UTF-8 문자 경계를 보존해요. `POST /api/v1/runs/validate`는 같은 preflight를 실행하지만 queue를 만들지 않고 `ok/code/message/existing_run` 결과만 반환해요. `POST /api/v1/runs`는 `RunValidator`로 session/provider/resource/workspace/provider-factory preflight를 먼저 통과한 뒤 즉시 `queued` 상태의 `RunDTO`를 반환하고, `AsyncRunManager`가 concurrency slot을 얻은 뒤 goroutine에서 실제 `RunStarter`를 실행해요. run context가 취소되거나 timeout되면 starter가 뒤늦게 성공 응답을 반환해도 최종 상태는 `cancelled`로 고정해서 외부 adapter의 취소 표시가 완료 상태로 뒤집히지 않게 해요. `Idempotency-Key` header 또는 `metadata.idempotency_key`가 있으면 session+key 기반 결정적 run id를 쓰고, SQLite insert-only claim이나 같은 프로세스에서 관리 중인 run과 같으면 새 작업을 만들지 않고 기존 run을 `200` + `X-Idempotent-Replay: true`로 반환해요. `KKODE_MAX_CONCURRENT_RUNS`/`-max-concurrent-runs`는 동시에 running 상태로 진입하는 run 수를 제한하고 초과분은 queued 상태로 대기해요. `KKODE_RUN_TIMEOUT`/`-run-timeout`은 running run이 provider/tool context를 너무 오래 점유하지 않도록 실행 시간을 제한해요. `/api/v1/diagnostics.run_runtime`은 현재 process-local tracked/queued/running/cancelling/terminal run 수와 slot 점유량을 보여줘요. run 상태는 `session.RunStore`와 `runs` SQLite table에도 저장돼요. gateway 시작 시 `RecoverStaleRuns`가 소유자가 사라진 `queued/running/cancelling` run을 `failed`로 닫고 durable run event를 남겨요. run 레코드는 provider/model/MCP/skills/subagents/context_blocks 선택을 함께 저장해서 retry가 같은 실행 맥락을 복원해요. `RunEventBus`는 같은 프로세스 안의 run 상태 변경과 agent/tool progress event를 `/api/v1/runs/{run_id}/events?stream=true` SSE로 전달하고, idle 중에는 `heartbeat_ms` 또는 기본 15초 주기로 `: heartbeat` comment를 보내고, `session.RunEventStore`는 같은 상태 변경과 progress event를 `run_events` SQLite table에 저장해요. SQLite store는 `session.RunSnapshotStore.SaveRunWithEvent`로 run snapshot과 durable event를 같은 transaction에 저장해서 replay 누락을 줄여요. `turns(session_id, ordinal)`, `events(session_id, ordinal)`, `run_events(run_id, seq)`는 unique sequence를 강제하고 `retrySQLiteSequence`가 짧게 재시도해서 동시 append 경합을 완화해요. 외부 Discord/Slack/web adapter는 session turns/transcript API, run transcript API, request transcript API로 대화 히스토리와 run/request-scoped 결과를 렌더링하고, transcript markdown은 max byte 제한과 원본 byte/truncated metadata로 Discord/web preview를 안전하게 자르며, session export/import API로 raw session snapshot, run이 참조한 MCP/skill/subagent resources, counts가 포함된 복구/이관/debug bundle을 저장하거나 복원하며, export preview는 `include_raw=false`와 turn/event/checkpoint/run limit 및 truncation metadata로 큰 session payload를 줄일 수 있고, 공유/debug 용도에서는 `redact=true`로 raw_session을 제외하고 secret 패턴을 마스킹하며, session compact API로 오래된 turn을 summary로 압축하고 `total_turns`, `compacted_turns`, `preserved_turns`, `summary_bytes`, `checkpoint_created` metadata를 확인하며, session checkpoint API로 복구용 snapshot payload를 저장하고, session todo API로 진행 상태를 직접 보정하고 todo list pagination metadata로 status UI polling payload를 줄이거나, `GET /api/v1/runs/{run_id}`로 `queued/running/completed/failed/cancelled` 상태를 확인하고, `GET /api/v1/runs?request_id=...`, `GET /api/v1/runs?idempotency_key=...` 또는 `GET /api/v1/requests/{request_id}/runs`로 특정 외부 요청에서 파생된 run을 다시 찾고, `GET /api/v1/requests/{request_id}/events`로 관련 run event를 한 응답에서 모으거나 `stream=true` SSE로 live update를 받고, `GET /api/v1/requests/{request_id}/transcript`로 같은 request id에서 파생된 run transcript를 묶어서 렌더링하고, SQLite의 `idx_runs_request_id_updated` expression index로 metadata JSON 필터 비용을 줄이고, `events_url`이 가리키는 run event replay도 읽으면 돼요. session `/events`는 `after_seq`/`limit` 기반 저장 event replay이고, session turn/event list JSON 응답은 `limit`, `result_truncated`, 필요한 경우 `next_after_seq`를 제공하며, session/checkpoint/resource manifest list와 run/request run list JSON 응답은 `limit`, `offset`, `next_offset`, `result_truncated`를 제공해서 외부 adapter가 다음 page와 replay cursor를 안전하게 계산하게 해요. run `/events`와 request correlation `/events`는 durable replay와 live 상태/progress 변경을 함께 제공하고, SSE는 replay 전에 live subscription을 준비해서 replay/live 경계의 terminal update 누락을 줄여요. `RunEventBus`는 subscriber buffer가 꽉 차도 terminal update는 오래된 update 하나를 밀어내고 보존해요. SQLite `TimelineStore`는 session 전체를 로드하지 않고 `ListTurns`, `LoadTurn`, `ListEvents`로 필요한 범위만 읽어서 긴 세션 패널 렌더링 비용을 줄여요. `TurnEventStore`는 새 turn, event, session state를 한 transaction으로 저장해서 새 turn 하나 때문에 기존 turns/events/todos를 통째로 지우고 다시 쓰지 않게 해요. `IncrementalStore`는 이 원자 경로가 없는 store를 위한 호환 fallback이에요.

Skill preview의 `max_bytes`는 기본 65536 byte, 최대 1048576 byte로 제한해서 저장된 SKILL.md/README.md를 외부 adapter용 bounded preview로만 반환해요.

Subagent preview의 `max_prompt_bytes`, prompt 원문/렌더링의 `max_text_bytes`, transcript의 `max_markdown_bytes`, git diff의 `max_bytes` 최대값은 `/capabilities.limits`에도 노출해서 외부 adapter가 preview 요청을 실행 전에 같은 byte envelope로 맞추게 해요.

Run preview와 provider test의 `max_preview_bytes`/`max_result_bytes`도 최대 8388608 byte로 제한하고 `/capabilities.limits`에 노출해서 preflight/debug 호출이 대형 payload budget으로 gateway를 압박하지 않게 해요.

CLI와 gateway run prompt는 공통 `app.MaxAgentPromptBytes` 262144 byte envelope를 써요. gateway는 `/capabilities.limits.max_run_prompt_bytes`로 노출하고, `kkode-agent`는 stdin prompt를 같은 크기 + 1 byte까지만 읽은 뒤 초과를 거부해서 pipe 입력이 unbounded memory read가 되지 않게 해요.

Provider live smoke의 `max_output_tokens`는 최대 8192 token, `timeout_ms`는 최대 300000ms로 제한하고 `/capabilities.limits`에 노출해서 provider debug 호출이 장시간 generation으로 runtime slot을 오래 점유하지 않게 해요.

파일 content preview의 `max_bytes`는 기본 1048576 byte, 최대 8388608 byte로 제한하고, `workspace.ReadFileRange`는 `max_bytes` 생략 시에도 최대 8388608 byte까지만 읽으며 더 큰 명시값은 거부하고 UTF-8 안전 경계에서 잘라요.

Workspace write는 최종 content를 8388608 byte 이하로 제한하고, `ApplyPatch` 입력은 1048576 byte 이하로 제한하며, patch 결과 파일도 write envelope를 넘으면 적용 전에 거부해요.

LSP format preview는 입력 Go 파일을 8388608 byte까지 허용하고, formatted content preview도 `max_bytes`/UTF-8 안전 경계로 잘라 외부 adapter의 format preview 요청이 큰 파일에 과도한 gofmt 비용을 쓰지 않게 해요.

직접 tool 호출의 `max_output_bytes`는 기본 1048576 byte, 최대 8388608 byte이고, `web_max_bytes`도 최대 8388608 byte로 제한해서 권한 프롬프트 없는 adapter tool 실행 응답이 bounded envelope를 유지하게 해요.

`shell_run`과 legacy `workspace_run_command`의 `timeout_ms`는 workspace 계층에서 최대 300000ms로 제한해요. 같은 계층에서 stdout은 최대 8388608 byte, stderr는 최대 1048576 byte까지만 보존해서 agent/provider tool call이 direct tool API 출력 제한을 우회해 메모리를 과점하지 못하게 해요.

Provider live smoke의 `max_result_bytes`를 생략해도 결과 text와 streaming 누적 text는 8388608 byte envelope 안에서만 보존해요.

OmniRoute A2A helper는 artifact content를 합칠 때 최대 8388608 byte envelope 안에서만 text를 보존해서 bounded HTTP body를 다시 unbounded provider text로 복제하지 않아요.

`web_fetch` tool argument의 `max_bytes`는 `WebConfig.MaxBytes`로 정해진 configured envelope를 넘으면 거부해서 agent run과 direct tool call 모두 배포자가 정한 web body 상한을 우회하지 못하게 해요. body truncation은 UTF-8 안전 byte 경계를 보존해서 외부 adapter가 한글/이모지 web 응답을 깨진 문자열로 받지 않게 해요.

MCP prompt/tool 직접 검증의 `max_message_bytes`와 `max_output_bytes`는 기본 1048576 byte, 최대 8388608 byte로 제한해서 저장된 stdio/http MCP 서버 응답도 bounded adapter envelope로 반환해요. HTTP MCP body reader도 명시 상한이 없으면 8388608 byte envelope를 기본으로 써요.

stdio MCP frame의 `Content-Length`는 8388608 byte를 넘으면 본문 할당 전에 거부하고, stdio MCP stderr는 최대 1048576 byte까지만 오류 context에 보존해요.

Run event replay와 request correlation event replay는 같은 incremental cursor 계약을 공유해요. JSON과 SSE 요청은 durable event를 읽기 전에 `after_seq`를 검증하고, adapter가 polling을 이어가야 하면 JSON 응답에 `next_after_seq`를 노출해요.

Manifest 저장/import 경계에서는 MCP server, skill, subagent config를 검증한 뒤 identifier-like 문자열과 목록을 canonical 값으로 정리해요. 이 때문에 export, preview, run assembly가 같은 resource 값을 사용하고 외부 adapter는 공백/중복이 제거된 manifest 계약을 기준으로 UI cache와 diff를 만들 수 있어요.

```bash
curl -N 'http://127.0.0.1:41234/api/v1/sessions/sess_.../events?stream=true&after_seq=0'
```

`cmd/kkode-gateway`는 기본적으로 `127.0.0.1:41234`에 bind해요. `/readyz`는 `session.HealthChecker`를 구현한 store ping과 run starter/previewer/validator/provider tester와 run 조회/취소/event stream wiring을 확인해서 SQLite 연결이 닫히거나 필수 runtime 경계가 빠진 경우 503을 반환하고, health/ready 성공 응답은 OpenAPI DTO로 고정해요. `0.0.0.0` 같은 remote bind는 `--api-key` 또는 `--api-key-env`가 없으면 거부해야해요. file/shell/web tool surface가 외부에 노출될 수 있기 때문이에요.

```bash
go run ./cmd/kkode-gateway -addr 127.0.0.1:41234 -state .kkode/state.db -cors-origins http://localhost:3000
```

`-cors-origins` 또는 `KKODE_CORS_ORIGINS`는 쉼표로 여러 origin을 받을 수 있어요. preflight는 처리하고 브라우저가 `Idempotency-Key` 요청 header를 보내고 `X-Request-Id`와 `X-Idempotent-Replay` 응답 header를 읽을 수 있게 CORS header를 열지만, 실제 API 호출은 bearer auth 정책을 그대로 따라요. 외부 adapter는 `X-Request-Id`를 직접 넣어 여러 시스템 로그를 연결할 수 있고, 넣지 않으면 gateway가 `req_...` 값을 생성해요. `X-Request-Id`, run metadata `request_id`, `Idempotency-Key`, metadata `idempotency_key`는 각각 128 byte까지만 허용하며, 너무 긴 request id header는 그대로 반사하지 않고 새 request id가 붙은 400 오류로 닫아요. 이 값은 background run metadata에도 `request_id`로 저장돼요. `-access-log` 또는 `KKODE_ACCESS_LOG=1`은 `gateway.AccessLogEntry`를 JSONL stderr 로그로 연결해서 컨테이너/VM 배포에서 바로 수집할 수 있게 해요. `KKODE_HTTPJSON_PROVIDERS`는 단일 객체나 배열 JSON으로 OpenAI-compatible HTTP JSON source를 추가해요. gateway와 CLI 모두 시작 시 이 값을 읽고 `RegisterHTTPJSONProvidersFromEnv`로 등록해서 provider discovery, model catalog, session 생성, run preview/test, 실제 run에서 같은 provider 이름을 쓸 수 있게 해요. `max_response_bytes`를 지정하면 해당 HTTP JSON provider의 success/error response body read 한도를 조절하고, 생략하면 기본 32MiB 한도를 쓰며 32MiB를 넘는 값은 시작 시 거부해서 provider가 과도한 응답으로 adapter나 run worker 메모리를 점유하지 못하게 해요. `-max-body-bytes` 또는 `KKODE_MAX_BODY_BYTES`는 JSON API 요청 body 제한을 조절해서 너무 큰 adapter 요청을 빠르게 거부해요. `-max-iterations`/`KKODE_MAX_ITERATIONS`는 128 이하, `-web-max-bytes`/`KKODE_WEB_MAX_BYTES`는 8388608 byte 이하로 시작 시 검증하고, 실제 값은 `/capabilities.limits.run_max_iterations`와 `run_web_max_bytes`에 노출해요. `-read-header-timeout`, `-read-timeout`, `-write-timeout`, `-idle-timeout`, `-shutdown-timeout`과 대응 `KKODE_*_TIMEOUT` 환경변수는 프록시, SSE, VM 배포 특성에 맞춰 HTTP lifecycle을 조절해요. `-max-concurrent-runs`/`KKODE_MAX_CONCURRENT_RUNS`는 background run 동시 실행 수를 제한하고, `-run-timeout`/`KKODE_RUN_TIMEOUT`은 run 실행 시간을 제한해요. gateway CLI는 SIGINT/SIGTERM을 `http.Server.Shutdown`으로 처리해서 종료 시 진행 중 요청을 정리하고, `AsyncRunManager.Shutdown`으로 소유 중인 active run을 취소 상태로 저장해요.

OpenAPI 계약은 `gateway/openapi.yaml`에 있어요. 웹 패널과 Discord adapter는 이 계약을 기준으로 붙이면 돼요. `gateway/openapi_contract_test.go`는 feature catalog endpoint가 OpenAPI paths에서 빠지지 않았는지 검사해요.

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
```

`Generate`는 `llm.AdaptedProvider`를 통해 `SessionConverter`와 `Client.CallProvider`를 연결해요. `Stream`도 같은 adapter와 `ProviderStreamCaller`를 통해 표준 request를 SDK session prompt payload로 먼저 바꾼 뒤 Copilot session event stream에 전달해요. converter는 표준 request를 SDK session prompt payload로 만들고, caller는 Copilot session 생성, send, close lifetime을 관리해요. SDK session send에서 누적하는 final response text는 8388608 byte envelope로 제한하고, SDK가 요청하는 실행 확인은 별도 권한 시스템으로 끌어올리지 않고 기존 YOLO 승인 handler로 즉시 승인해요.

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
| `file_read` | 파일을 읽고 line range/max bytes를 지원해요 |
| `file_write` | 파일을 써요 |
| `file_edit` | old/new 텍스트 교체와 expected replacement count를 지원해요 |
| `file_apply_patch` | apply_patch 형식 patch를 적용해요 |
| `file_list` | 디렉터리를 나열해요 |
| `file_glob` | glob으로 파일을 찾아요 |
| `file_grep` | literal/regex 검색을 해요 |
| `shell_run` | command를 실행하고 JSON `CommandResult`를 반환해요 |
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
- `agent.Guardrails`는 입력/출력 substring 차단을 제공하고, 더 정교한 정책은 별도 guardrail 구현체로 확장해야해요.
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
