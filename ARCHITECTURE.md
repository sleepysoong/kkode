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
├── agent/                           # 실제 coding agent loop와 guardrail/trace예요
├── session/                         # SQLite session store와 resume/fork 상태예요
├── runtime/                         # agent와 session store를 묶는 실행 runtime이에요
├── prompts/                         # system/session/todo prompt 템플릿이에요
├── cmd/
│   ├── kkode-agent/                  # provider 선택형 agent CLI예요
│   └── kkode-gateway/                # HTTP gateway API server예요
├── llm/                              # provider-neutral core예요
│   ├── model.go                      # 모델 registry와 pricing 타입이에요
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
│   ├── openai/                       # OpenAI-compatible Responses provider예요
│   ├── copilot/                      # GitHub Copilot SDK adapter예요
│   ├── codexcli/                     # Codex CLI subprocess adapter예요
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
func DefaultModel(provider string) string
func NewWorkspace(opts WorkspaceOptions) (*workspace.Workspace, string, error)
func NewAgent(provider llm.Provider, ws *workspace.Workspace, opts AgentOptions) (*agent.Agent, error)
func NewRuntime(store session.Store, ag *agent.Agent, opts RuntimeOptions) *runtime.Runtime
func DefaultCompactionPolicy() session.CompactionPolicy
```

`ProviderSpecs`는 provider registry에서 방어 복사한 spec을 돌려줘서 CLI/gateway/provider 기본 모델과 인증 상태 표시를 공유해요. `BuildProviderWithOptions`는 같은 registry entry의 factory를 실행하므로 새 provider를 추가할 때 spec, alias, capability, 생성 로직을 한 곳에서 맞추면 돼요. `NewRuntime`은 history/todo/compaction 기본값을 CLI와 gateway가 같은 방식으로 쓰게 해요. `NewAgent`는 `tools.StandardTools`를 통해 `tools.FileTools`와 선택적 `tools.WebTools`를 같은 방식으로 붙여요. 예전 `workspace_*` tool은 `workspace.Workspace.Tools()`로 직접 사용할 수 있지만, 일반 agent 표면에는 `file_read`, `file_write`, `file_edit`, `file_apply_patch`, `file_list`, `file_glob`, `file_grep`, `shell_run`, `web_fetch`만 노출해요.

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
GET  /api/v1/providers
GET  /api/v1/models
GET  /api/v1/stats
GET  /api/v1/prompts
GET  /api/v1/prompts/{template_name}
POST /api/v1/prompts/{template_name}/render
GET  /api/v1/tools
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
GET  /api/v1/lsp/diagnostics
GET  /api/v1/lsp/hover
GET  /api/v1/runs
POST /api/v1/runs
GET  /api/v1/runs/{run_id}
GET  /api/v1/runs/{run_id}/events
POST /api/v1/runs/{run_id}/cancel
POST /api/v1/runs/{run_id}/retry
```

`GET /api/v1`은 adapter bootstrap용 discovery index라서 OpenAPI/capabilities/session/run 대표 URL을 한 번에 찾게 해요. `Config.CORSOrigins`는 별도 웹 패널 origin의 preflight와 bearer auth 호출을 허용해요. `requestIDMiddleware`는 모든 응답에 `X-Request-Id`를 붙이고 client가 보낸 ID를 오류 envelope에도 보존해서 Discord/Slack/web adapter 로그를 같은 요청으로 묶게 해요. `accessLogMiddleware`는 선택적으로 `AccessLogEntry`를 발행해서 host app이 request id, method, path, status, byte 수, duration을 structured log나 metric으로 재사용하게 해요. `startRun`과 `retryRun`은 같은 request id를 `RunDTO.Metadata["request_id"]`에 주입해서 durable run event replay에도 HTTP 요청 추적값이 남게 해요. `GET /api/v1/openapi.yaml`은 gateway가 실행 중인 API 계약을 그대로 제공해서 SDK 생성과 adapter smoke test에 쓸 수 있게 해요. `GET /api/v1/capabilities`는 외부 adapter가 gateway의 구현/연결 상태를 discovery할 수 있게 해요. `GET /api/v1/models`는 provider별 모델 catalog, 기본 모델, capability, auth 상태를 평탄화해서 모델 선택 UI에 바로 쓰게 해요. `GET /api/v1/stats`는 dashboard adapter가 sessions/turns/events/todos/checkpoints/runs/resources 카운트를 한 번에 읽게 해요. `GET /api/v1/prompts` 계열은 system/session/todo prompt template 목록, 원문, 렌더링 preview를 제공해서 외부 패널이 prompt 설정 화면을 만들 수 있게 해요. `GET /api/v1/tools`와 `POST /api/v1/tools/call`은 표준 file/shell/web tool surface를 API로 직접 노출해요. `GET /api/v1/git/status`, `/diff`, `/log`는 패널이 변경사항과 commit 흐름을 명령 조립 없이 렌더링하게 해요. `GET /api/v1/files`와 `GET/PUT /api/v1/files/content`는 웹 패널 파일 브라우저가 쓰기 쉬운 전용 wrapper예요. 이 endpoint도 권한 프롬프트 없이 project root 기준으로 즉시 실행해요. MCP server, skill, subagent는 `session.ResourceStore`와 `resources` SQLite table에 manifest로 저장해요. MCP stdio manifest는 `/api/v1/mcp/servers/{resource_id}/tools`, `/resources`, `/prompts`로 `initialize` 뒤 `tools/list`, `resources/list`, `prompts/list` probe를 실행할 수 있고, `/resources/read`, `/prompts/{prompt_name}/get`, `/tools/{tool_name}/call`로 resource/prompt/tool 동작을 직접 검증할 수 있어요. `GET /api/v1/skills/{resource_id}/preview`는 저장된 skill directory의 `SKILL.md` 또는 `README.md`를 읽어서 외부 패널 preview로 돌려줘요. `GET /api/v1/subagents/{resource_id}/preview`는 subagent prompt, tools, MCP server alias, skill 참조를 실행 전 확인하는 API예요. `RunStartRequest.mcp_servers`, `skills`, `subagents`는 저장된 manifest ID 목록이고, `cmd/kkode-gateway`가 이를 `app.ProviderOptions`로 변환해서 Copilot 같은 provider 설정에 연결해요. `GET /api/v1/lsp/symbols`는 files/git API와 같은 workspace root 검증을 거친 뒤 Go parser 기반 workspace symbol index를 반환하고, `GET /api/v1/lsp/document-symbols`는 파일 하나의 outline을 반환해요. `GET /api/v1/lsp/definitions`와 `GET /api/v1/lsp/references`는 symbol 이름 기준 definition/reference 위치와 excerpt를 반환해서 외부 패널의 go-to-definition/reference view를 만들 수 있게 해요. `GET /api/v1/lsp/diagnostics`는 Go parser diagnostic을 반환하고, `GET /api/v1/lsp/hover`는 symbol signature와 doc comment를 반환해요. LSP scan은 공통 `walkParsedGoFiles` helper로 `node_modules`, `vendor`, `.omx` 같은 무거운 디렉터리를 건너뛰며 `limit`에 도달하면 scan을 조기 중단해요. 이 manifest의 `config`에는 stdio/http MCP 설정, skill path/prompt, subagent prompt/tools/skills 같은 provider별 설정을 담아요. `POST /api/v1/runs`는 즉시 `queued` 상태의 `RunDTO`를 반환하고, `AsyncRunManager`가 goroutine에서 실제 `RunStarter`를 실행해요. run 상태는 `session.RunStore`와 `runs` SQLite table에도 저장돼요. gateway 시작 시 `RecoverStaleRuns`가 소유자가 사라진 `queued/running/cancelling` run을 `failed`로 닫고 durable run event를 남겨요. run 레코드는 provider/model/MCP/skills/subagents 선택을 함께 저장해서 retry가 같은 실행 맥락을 복원해요. `RunEventBus`는 같은 프로세스 안의 run 상태 변경을 `/api/v1/runs/{run_id}/events?stream=true` SSE로 전달하고, `session.RunEventStore`는 같은 상태 변경을 `run_events` SQLite table에 저장해요. SQLite store는 `session.RunSnapshotStore.SaveRunWithEvent`로 run snapshot과 durable event를 같은 transaction에 저장해서 replay 누락을 줄여요. `turns(session_id, ordinal)`, `events(session_id, ordinal)`, `run_events(run_id, seq)`는 unique sequence를 강제하고 `retrySQLiteSequence`가 짧게 재시도해서 동시 append 경합을 완화해요. 외부 Discord/Slack/web adapter는 session turns/transcript API로 대화 히스토리를 렌더링하고, session compact API로 오래된 turn을 summary로 압축하며, session checkpoint API로 복구용 snapshot payload를 저장하고, session todo API로 진행 상태를 직접 보정하거나, `GET /api/v1/runs/{run_id}`로 `queued/running/completed/failed/cancelled` 상태를 확인하고, `events_url`이 가리키는 session event replay를 읽으면 돼요. session `/events`는 `after_seq`/`limit` 기반 저장 event replay이고, run `/events`는 durable replay와 live 상태 변경을 함께 제공해요. SQLite `TimelineStore`는 session 전체를 로드하지 않고 `ListTurns`, `LoadTurn`, `ListEvents`로 필요한 범위만 읽어서 긴 세션 패널 렌더링 비용을 줄여요. `TurnEventStore`는 새 turn, event, session state를 한 transaction으로 저장해서 새 turn 하나 때문에 기존 turns/events/todos를 통째로 지우고 다시 쓰지 않게 해요. `IncrementalStore`는 이 원자 경로가 없는 store를 위한 호환 fallback이에요.

```bash
curl -N 'http://127.0.0.1:41234/api/v1/sessions/sess_.../events?stream=true&after_seq=0'
```

`cmd/kkode-gateway`는 기본적으로 `127.0.0.1:41234`에 bind해요. `0.0.0.0` 같은 remote bind는 `--api-key` 또는 `--api-key-env`가 없으면 거부해야해요. file/shell/web tool surface가 외부에 노출될 수 있기 때문이에요.

```bash
go run ./cmd/kkode-gateway -addr 127.0.0.1:41234 -state .kkode/state.db -cors-origins http://localhost:3000
```

`-cors-origins` 또는 `KKODE_CORS_ORIGINS`는 쉼표로 여러 origin을 받을 수 있어요. preflight는 처리하지만 실제 API 호출은 bearer auth 정책을 그대로 따라요. 외부 adapter는 `X-Request-Id`를 직접 넣어 여러 시스템 로그를 연결할 수 있고, 넣지 않으면 gateway가 `req_...` 값을 생성해요. 이 값은 background run metadata에도 `request_id`로 저장돼요.

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
| `-provider` | `openai`, `omniroute`, `copilot`, `codex` 중 하나예요 | `KKODE_PROVIDER` 또는 `openai` |
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
func (c *Client) Stream(ctx context.Context, req llm.Request) (llm.EventStream, error)
func BuildResponsesRequest(req llm.Request) (map[string]any, error)
func ParseResponsesResponse(data []byte, providerName string) (*llm.Response, error)
```

`Generate`와 `Stream`은 같은 request builder와 retry 경로를 공유해요. JSON request 생성, bearer auth, custom header 복사, retry/backoff, `Retry-After` backoff 반영, SSE line framing, HTTP 실패 분류는 `providers/internal/httptransport`를 써서 OmniRoute 같은 파생 provider와 같은 HTTP 처리 규칙을 재사용해요. provider 오류는 `httptransport.HTTPError`로 감싸서 gateway나 외부 adapter가 `errors.As`로 status code와 body를 일관되게 읽을 수 있어요. `ProviderName`을 지정하면 OpenAI-compatible 파생 provider가 response와 stream event provider label을 자기 이름으로 고정할 수 있어요.

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
func (c *Client) Stream(ctx context.Context, req llm.Request) (llm.EventStream, error)
func (c *Client) NewSession(ctx context.Context, req llm.SessionRequest) (llm.Session, error)
func ToCopilotTool(tool llm.Tool, handler llm.ToolHandler) copilot.Tool
func ToCopilotMCPServer(server llm.MCPServer) copilot.MCPServerConfig
func ToCopilotAgent(agent llm.Agent) copilot.CustomAgentConfig
```

예제는 이렇게 써요.

```go
client := copilot.New(copilot.Config{
    ClientName:       "kkode-app",
    WorkingDirectory: ".",
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
func (c *Client) Stream(ctx context.Context, req llm.Request) (llm.EventStream, error)
```

예제는 이렇게 써요.

```go
client := codexcli.New(codexcli.Config{
    WorkingDirectory: ".",
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
