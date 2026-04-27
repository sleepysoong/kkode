# 01. Agent loop, session, state 강화 제안

작성일: 2026-04-27

## 결론

현재 `agent.Agent`는 “한 번 prompt를 넣고 tool loop를 실행하는 wrapper”예요. 실제 바이브코딩 툴로 가려면 **세션을 중심으로 모든 상태가 흘러야해요.**

필요한 핵심은 다음이에요.

- multi-turn session
- resume/continue/fork
- streaming event log
- todo list
- auto compaction
- token/cost budget
- subagent orchestration
- background job control
- structured result

## 근거

Claude Agent SDK 문서는 agent loop가 prompt, system prompt, tool definition, conversation history를 받고, model이 tool call 또는 final answer를 낼 때까지 반복한다고 설명해요. 마지막에는 final text, token usage, cost, session ID가 포함된 결과를 낸다고 해요.

OpenCode는 CLI에서 `--continue`, `--session`, `--fork`를 제공하고, TUI/CLI가 같은 backend 세션을 이어갈 수 있어요. Codex SDK 문서도 thread를 시작하고 같은 thread에 다시 `run()`하거나 thread ID로 resume하는 예제를 제공해요.

따라서 `kkode`의 현재 `transcript.Transcript`는 보조 기록으로는 좋지만, 제품의 중심 state로는 부족해요.

## 현재 구현의 한계

### `agent.Agent.Run` 한계

현재 구조는 다음이에요.

```go
func (a *Agent) Run(ctx context.Context, prompt string) (*RunResult, error)
```

장점은 단순해요. 하지만 아래가 없어요.

- session ID가 없어요.
- 같은 session에서 이전 turn을 자동으로 이어받지 않아요.
- `PreviousResponseID`, `InputItems`, compact summary를 session store에서 복구하지 않아요.
- interrupt/cancel/background job이 없어요.
- token budget과 cost budget이 없어요.
- todo를 모델이 tool로 관리하지 않아요.
- user가 `/continue`, `/fork`, `/rewind`할 수 없어요.

## 제안 패키지 구조

```text
session/
├── session.go          # Session, Turn, Event, Store interface예요
├── sqlite_store.go     # SQLite 저장소예요
├── json_store.go       # 단순 파일 저장소예요. 초기 구현에 좋아요
├── compact.go          # compaction policy와 summary 생성기예요
├── fork.go             # session fork helper예요
└── export.go           # import/export용 JSON schema예요

runtime/
├── runtime.go          # AgentRuntime이에요
├── events.go           # streaming event bus예요
├── budget.go           # token/cost/time budget이에요
├── todo.go             # todo list tool/state예요
└── jobs.go             # background job manager예요
```

`agent`는 더 얇게 두고, 실제 제품 실행은 `runtime.AgentRuntime`이 잡는 게 좋아요.

## 핵심 타입 제안

```go
type Session struct {
    ID              string
    ProjectRoot     string
    ProviderName    string
    Model           string
    AgentName       string
    Mode            AgentMode
    CreatedAt       time.Time
    UpdatedAt       time.Time
    Turns           []Turn
    Events          []Event
    Todos           []Todo
    Summary         string
    LastResponseID  string
    LastInputItems  []llm.Item
    Metadata        map[string]string
}

type AgentMode string
const (
    AgentModeBuild AgentMode = "build"
    AgentModePlan  AgentMode = "plan"
    AgentModeAsk   AgentMode = "ask"
)

type Turn struct {
    ID        string
    Prompt    string
    Request   llm.Request
    Response  *llm.Response
    StartedAt time.Time
    EndedAt   time.Time
    Error     string
}

type Event struct {
    ID        string
    SessionID string
    TurnID    string
    At        time.Time
    Type      string
    Tool      string
    Payload   json.RawMessage
    Error     string
}
```

## Store 제안

초기에는 JSON 파일도 가능하지만, OpenCode와 Codex가 세션/상태 DB를 중요하게 다루는 걸 보면 SQLite가 좋아요.

```go
type Store interface {
    CreateSession(ctx context.Context, s *Session) error
    LoadSession(ctx context.Context, id string) (*Session, error)
    SaveSession(ctx context.Context, s *Session) error
    ListSessions(ctx context.Context, q SessionQuery) ([]SessionSummary, error)
    AppendEvent(ctx context.Context, ev Event) error
    SaveCheckpoint(ctx context.Context, cp Checkpoint) error
}
```

추천 경로는 아래예요.

```text
~/.kkode/state.db
.kkode/sessions/<session-id>.json   # export/debug용
.kkode/checkpoints/<session-id>/...
```

## Auto compaction 제안

OpenCode 문서는 context window 95% 근처에서 auto compact를 수행한다고 설명해요. Claude Code도 checkpoint/rewind에서 “summarize from here”를 제공해요. 우리도 다음 정책이 필요해요.

```go
type CompactionPolicy struct {
    Enabled              bool
    TriggerTokenRatio    float64 // 예: 0.85 또는 0.90
    PreserveFirstNTurns  int
    PreserveLastNTurns   int
    SummaryModel         string
    MaxSummaryTokens     int
}
```

동작 방식은 이렇게 해요.

1. provider usage 또는 tokenizer estimate로 session token을 계산해요.
2. threshold를 넘으면 오래된 turn을 summary로 압축해요.
3. `Session.Summary`에 저장해요.
4. 다음 `llm.Request.Instructions` 또는 첫 user context item에 summary를 넣어요.
5. 원본 transcript는 store에 유지해서 audit 가능하게 해요.

## Todo tool 제안

OpenCode는 `todowrite`를 built-in tool로 둬서 복잡한 작업 중 진행 목록을 관리하게 해요. Claude Agent SDK도 todo lists를 별도 기능으로 문서화해요.

`kkode`도 다음 tool을 넣어야해요.

```go
type Todo struct {
    ID        string
    Content   string
    Status    TodoStatus // pending, in_progress, completed, cancelled
    Priority  string
    UpdatedAt time.Time
}

func TodoTools(store Store, sessionID string) ([]llm.Tool, llm.ToolRegistry)
```

Tool은 아래 정도면 돼요.

- `todo_write`: 전체 todo list를 갱신해요.
- `todo_update`: 특정 todo status를 바꿔요.
- `todo_list`: 현재 todo를 반환해요.

## Subagent 제안

OpenCode는 primary agent와 subagent를 나누고, plan/build/explore/general 같은 기본 역할을 제공해요. Claude Code는 subagent 파일, tool 제한, permission mode, MCP scope, persistent memory, foreground/background 실행까지 설명해요. Codex config도 agent thread 수와 nesting depth를 다뤄요.

우리도 `agent.Config`를 그대로 확장하기보다 `AgentDefinition`을 별도 모델로 가져가야해요.

```go
type AgentDefinition struct {
    Name        string
    Description string
    Mode        AgentMode
    Model       string
    Prompt      string
    Tools       map[string]PermissionAction
    MCPServers  []string
    MaxSteps    int
    Temperature *float64
    Hidden      bool
}
```

실행 API는 이렇게 가면 좋아요.

```go
type Runtime interface {
    Run(ctx context.Context, sessionID string, prompt string) (*RunResult, error)
    Spawn(ctx context.Context, parentSessionID string, agentName string, prompt string) (*Job, error)
    Resume(ctx context.Context, sessionID string) (*Session, error)
    Fork(ctx context.Context, sessionID string, atTurnID string) (*Session, error)
}
```

## CLI 명령 제안

```bash
kkode run "테스트 고쳐줘" --agent build --model openai/gpt-5-mini
kkode session list
kkode session resume <id>
kkode session fork <id> --at <turn-id>
kkode session export <id> > session.json
kkode session import session.json
kkode compact <id>
```

## 테스트 제안

- session resume가 이전 `InputItems`와 summary를 포함하는지 테스트해야해요.
- fork가 parent session을 변경하지 않는지 테스트해야해요.
- compaction이 원본 transcript를 삭제하지 않는지 테스트해야해요.
- todo tool이 복잡한 multi-step prompt에서 trace와 같이 저장되는지 테스트해야해요.

## 구현 우선순위

1. `session.Store` interface + JSON store
2. `Agent.Run`이 `Session`을 입력받는 overload 추가
3. `kkode session list/resume`
4. `todo_write` tool
5. compaction policy
6. fork/export/import
7. SQLite store
8. subagent runtime

## 참고 소스

- Claude Agent SDK loop: https://code.claude.com/docs/en/agent-sdk/agent-loop
- Claude Code subagents: https://code.claude.com/docs/en/sub-agents
- Claude Code checkpointing: https://code.claude.com/docs/en/checkpointing
- OpenCode Agents: https://opencode.ai/docs/agents/
- OpenCode CLI: https://opencode.ai/docs/cli/
- OpenAI Codex SDK: https://developers.openai.com/codex/sdk
- OpenAI Codex App Server: https://developers.openai.com/codex/app-server
