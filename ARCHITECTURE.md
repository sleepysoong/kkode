# kkode 아키텍처

이 문서는 `kkode`의 파일 트리, 핵심 구현체, 함수 시그니처, 사용 예제를 설명해요. 앞으로 새 provider나 tool을 추가할 때 이 문서를 기준으로 맞춰가면 돼요.

## 설계 목표

`kkode`는 Go 기반 바이브코딩 앱을 만들기 위한 provider runtime이에요. 핵심 방향은 다음과 같아요.

1. OpenAI Responses API의 item semantics를 기본 호환 모델로 삼아요.
2. Copilot SDK나 Codex CLI처럼 session 중심인 provider도 같은 앱에서 사용할 수 있게 해요.
3. Tool, Provider, Auth, Model, Response, Prompt를 직접 소유해요.
4. provider별 특수 기능은 adapter 안에 가두고 core는 최대한 provider-neutral하게 유지해요.
5. workspace 접근과 shell 실행은 approval policy로 제한해야해요.

## 파일 트리

```text
kkode/
├── README.md                         # 프로젝트 소개와 빠른 사용법이에요
├── ARCHITECTURE.md                   # 현재 문서예요
├── go.mod
├── go.sum
├── llm/                              # provider-neutral core예요
│   ├── approval.go                   # 승인 정책이에요
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
│   ├── openai/                       # OpenAI-compatible Responses provider예요
│   ├── copilot/                      # GitHub Copilot SDK adapter예요
│   ├── codexcli/                     # Codex CLI subprocess adapter예요
│   └── omniroute/                    # OmniRoute gateway adapter예요
├── workspace/                        # workspace file/search/shell tool이에요
├── transcript/                       # transcript 저장소예요
├── scripts/                          # 검증용 smoke scripts예요
└── research/                         # 조사 문서와 TODO예요
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
4. local tool을 실행해요.
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
    MaxIterations: 8,
})
```

## Provider 구현체

### OpenAI-compatible provider

패키지는 `providers/openai`예요.

주요 생성자와 메서드는 다음과 같아요.

```go
func New(cfg Config) *Client
func (c *Client) Generate(ctx context.Context, req llm.Request) (*llm.Response, error)
func (c *Client) Stream(ctx context.Context, req llm.Request) (llm.EventStream, error)
func BuildResponsesRequest(req llm.Request) (map[string]any, error)
func ParseResponsesResponse(data []byte, providerName string) (*llm.Response, error)
```

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

패키지는 `providers/omniroute`예요. OmniRoute는 model vendor가 아니라 routing gateway예요. 그래서 generation은 OpenAI-compatible `/v1/responses`를 사용하고, management 기능은 별도 helper로 분리해요.

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

## Workspace 구현체

패키지는 `workspace`예요. provider tool로 붙일 수 있는 local workspace adapter예요.

```go
func New(root string, policy llm.ApprovalPolicy) (*Workspace, error)
func (w *Workspace) Resolve(rel string) (string, error)
func (w *Workspace) ReadFile(rel string) (string, error)
func (w *Workspace) WriteFile(rel, content string) error
func (w *Workspace) List(rel string) ([]string, error)
func (w *Workspace) Search(needle string) ([]string, error)
func (w *Workspace) Run(ctx context.Context, command string, args ...string) (string, error)
func (w *Workspace) Tools() (defs []llm.Tool, handlers llm.ToolRegistry)
```

예제는 이렇게 써요.

```go
ws, err := workspace.New(".", llm.ApprovalPolicy{
    Mode:         llm.ApprovalReadOnly,
    AllowedPaths: []string{"."},
})
if err != nil {
    panic(err)
}

toolDefs, handlers := ws.Tools()
req.Tools = append(req.Tools, toolDefs...)
resp, err := llm.RunToolLoop(ctx, provider, req, handlers, llm.ToolLoopOptions{})
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

- workspace path는 root 바깥으로 탈출할 수 없게 막아요.
- write/shell은 `ApprovalPolicy`가 허용해야 실행해요.
- transcript는 `SaveRedacted`로 token/API key 패턴을 지워 저장할 수 있어요.
- provider OAuth/token 저장은 provider package가 소유해야해요.
- MCP tool은 session/tool attachment로 취급하고, core provider method로 섞지 않아야해요.

## 다음 작업 방향

다음 단계는 아래 순서로 가면 좋아요.

1. OpenAI-compatible chat/embedding/image provider surface를 추가해요.
2. OmniRoute `/api/models/catalog`, `/api/combos`, `/api/combos/metrics`, `/api/resilience` typed helper를 더 추가해요.
3. Codex app-server/harness provider를 CLI adapter와 분리해서 추가해요.
4. streaming aggregation과 event replay를 강화해요.
5. workspace patch tool과 command policy audit log를 추가해요.
