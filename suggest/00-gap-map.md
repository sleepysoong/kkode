# 00. kkode 갭맵: Go 바이브코딩 툴로 가려면 부족한 것

작성일: 2026-04-27

## 한 줄 결론

`kkode`는 지금 **provider-neutral agent core의 씨앗**은 있어요. 하지만 `opencode`, Codex, Claude Code 같은 “실제로 매일 쓰는 바이브코딩 툴”이 되려면 아직 **checkpoint/undo, 코드 인텔리전스 고도화, TUI/server polish, MCP/skills/plugin, provider auth/model catalog**가 더 필요해요. 권한 기능은 사용자 지시에 따라 만들지 않고 YOLO 실행으로 유지해요.

현재 구현은 다음 수준이에요.

- `llm`에 provider-neutral request/response/tool/type이 있어요.
- `providers/openai`, `providers/copilot`, `providers/codexcli`, `providers/omniroute` adapter가 있어요.
- `agent.Agent`가 provider + workspace tools + guardrail + transcript + trace를 묶어요.
- `workspace`는 read/write/replace/list/search/run_command/apply_patch/delete/move를 제공해요.
- `cmd/kkode-agent`는 provider를 골라 단발성 실행을 해요.

이것은 “agent engine prototype”에는 충분하지만, “CLI/TUI 제품”으로는 아직 P0 기능이 많이 비어 있어요.

## 조사 기준

주요 비교 대상은 아래예요.

- OpenCode 공식 문서: tools, agents, permissions, LSP, MCP, CLI, SDK, OpenCode Go provider를 봤어요.
- OpenAI Codex 공식 문서: config, sandbox/approval, skills, app-server, SDK를 봤어요.
- Claude Code 공식 문서: permission, sandboxing, checkpointing, subagents, MCP, Agent SDK loop를 봤어요.
- 우리 프로젝트 현재 구현: `llm`, `agent`, `workspace`, `providers`, `cmd/kkode-agent`를 직접 확인했어요.

## 전체 갭 테이블

| 영역 | 현재 kkode | opencode/Codex/Claude Code 수준 | 부족도 | 제안 파일 |
|---|---|---|---:|---|
| Agent loop/session | SQLite session resume/fork, todo, compaction, background run store, run SSE | interrupt queue, durable event replay stream, checkpoint rewind, cost budget | P0 | `01-agent-loop-session-state.md` |
| Tool surface | `file_*`, `shell_run`, `web_fetch`, grep/glob/range read/apply_patch/delete/move, direct tools API, Go symbol API | question/web search/custom MCP tool execution, richer LSP operations | P0 | `02-tools-sandbox-permissions.md` |
| Permission/sandbox | 사용자 지시대로 권한 엔진 없음, YOLO 즉시 실행 | 안전 제품은 deny/ask/allow가 있지만 kkode에서는 의도적으로 제외해요 | N/A | `02-tools-sandbox-permissions.md` |
| Checkpoint/undo | SQLite checkpoint 저장 타입과 compaction은 있음 | `/undo`, `/redo`, rewind, code vs conversation restore | P0 | `02-tools-sandbox-permissions.md` |
| Project instructions | `prompts/*` 템플릿, system/compaction/todo prompt 분리 | AGENTS.md/CLAUDE.md/rules auto-load, hierarchical scopes | P0 | `03-context-skills-mcp.md` |
| Skills/commands/plugins | skill manifest API와 Copilot skill directory 연결 일부 구현 | SKILL.md progressive disclosure, slash commands, plugins, marketplaces | P1 | `03-context-skills-mcp.md` |
| MCP | MCP manifest CRUD, Copilot 연결, stdio `tools/list` probe와 `tools/call` API | stdio/HTTP/SSE OAuth, tool call endpoint, tool search, resource/prompt support | P0 | `03-context-skills-mcp.md` |
| Provider auth/model catalog | env 기반 auth status, provider registry, default model/capability discovery | login/logout, credentials store, dynamic model registry, budget | P0 | `04-provider-auth-model-router-cost.md` |
| UI surfaces | API-only gateway, background run/session/event/resource/LSP endpoints | TUI/desktop/IDE/ACP는 별도 프로젝트에서 붙여요 | P1 | `05-product-surfaces.md` |
| Observability | transcript, session events, run status, provider usage 일부 | OTel spans, cost/usage 집계, hook lifecycle, metrics endpoint | P1 | `01-agent-loop-session-state.md`, `05-product-surfaces.md` |
| Repo automation | 없음 | GitHub agent, CI, PR review, issue automation | P2 | `05-product-surfaces.md` |
| Packaging | Go module만 있음 | install script, config schema, shell completion, migration | P2 | `06-roadmap.md` |

## P0 우선순위

### 1. Session Runtime부터 만들어야해요

지금은 `cmd/kkode-agent`를 실행하면 한 번 돌고 끝나요. 바이브코딩 앱은 보통 긴 세션을 유지해요. Codex SDK는 thread를 시작하고 다시 run하거나 thread ID로 resume하는 구조를 공식 문서에서 보여줘요. Claude Agent SDK도 session ID, final result, token usage를 agent loop 결과로 다뤄요. OpenCode CLI도 `--continue`, `--session`, `--fork` 흐름을 제공해요.

따라서 `session/` 패키지를 만들어야해요.

필요 타입은 대략 이렇게 가면 좋아요.

```go
type Session struct {
    ID        string
    Root      string
    Provider  string
    Model     string
    AgentName string
    CreatedAt time.Time
    UpdatedAt time.Time
    Turns     []Turn
    Checkpoints []Checkpoint
    Summary   string
}

type Store interface {
    Create(ctx context.Context, s *Session) error
    Load(ctx context.Context, id string) (*Session, error)
    Save(ctx context.Context, s *Session) error
    List(ctx context.Context, filter ListFilter) ([]SessionSummary, error)
}
```

### 2. Permission engine을 tool별 rule로 갈아엎어야해요

현재 `ApprovalPolicy.AllowsCommand`는 command prefix만 봐요. 이 정도면 위험하고 불편해요.

OpenCode는 `permission` config에서 `allow`, `ask`, `deny`와 wildcard/object rule을 지원해요. Claude Code는 deny -> ask -> allow 순서와 tool-specific specifier를 설명해요. Codex도 `approval_policy`, `sandbox_mode`, granular approval, protected paths, MCP approvals를 config reference로 제공해요.

우리도 `llm.ApprovalPolicy`를 다음으로 확장해야해요.

```go
type PermissionAction string
const (
    PermissionAllow PermissionAction = "allow"
    PermissionAsk   PermissionAction = "ask"
    PermissionDeny  PermissionAction = "deny"
)

type PermissionRule struct {
    Tool    string
    Pattern string
    Action  PermissionAction
    Reason  string
}

type PermissionEngine interface {
    Decide(ctx context.Context, req PermissionRequest) (PermissionDecision, error)
}
```

### 3. checkpoint, undo가 없으면 실사용이 힘들어요

`workspace_apply_patch`, `file_delete`, `file_move`, 전용 files API delete/move/patch는 구현됐어요. 그래도 Claude Code처럼 file editing tool 변경을 checkpoint로 추적하고 rewind에서 코드/대화 복구를 분리하는 흐름은 아직 부족해요.

남은 것은 다음이에요.

- edit operation log
- checkpoint store
- `/undo`, `/redo`, `/rewind` command

### 4. LSP tool이 필요해요

지금 `workspace_search`는 literal 검색뿐이에요. OpenCode는 LSP tool로 definition, references, hover, symbols, call hierarchy까지 모델에게 제공해요. Go로 만들 프로젝트라면 특히 `gopls`를 붙여야해요.

P0 시작점은 다음이에요.

```go
type CodeIntel interface {
    Diagnostics(ctx context.Context, path string) ([]Diagnostic, error)
    Definition(ctx context.Context, path string, line, col int) ([]Location, error)
    References(ctx context.Context, path string, line, col int) ([]Location, error)
    Symbols(ctx context.Context, query string) ([]Symbol, error)
}
```

### 5. MCP client가 core에 들어와야해요

MCP는 “추가 provider 기능”이 아니라 coding agent의 기본 tool 확장 방식이에요. OpenCode, Codex, Claude Code 모두 MCP를 주요 확장 지점으로 취급해요. 현재 우리는 Copilot provider mapping 일부와 research만 있어요. 실제 `mcp/` package가 필요해요.

### 6. TUI/server가 없으면 제품 감각이 안 나와요

OpenCode는 terminal interface, desktop app, IDE extension을 제공한다고 소개해요. CLI도 `opencode run`, `opencode serve`, `opencode attach`, `session`, `auth`, `models` 같은 명령을 갖고 있어요. Codex app-server는 rich client가 authentication, conversation history, approvals, streamed agent events를 다룰 수 있게 JSON-RPC 2.0 protocol을 제공해요.

우리도 최소한 다음 surface가 필요해요.

- `kkode` TUI
- `kkode run`
- `kkode session list/resume/fork/export/import`
- `kkode serve` JSON-RPC/SSE
- `kkode auth login/list/logout`
- `kkode models`

## 절대 지금 하지 말아야 할 것

- provider를 더 많이 붙이는 것만으로 제품이 되지 않아요.
- UI 없이 core만 계속 추상화하면 사용성이 검증되지 않아요.
- sandbox 없이 `danger-full-access`식 자동 실행을 기본값으로 두면 신뢰를 잃어요.
- “문서에 있는 tool 이름”만 늘리고 checkpoint/undo 없이 edit tool을 늘리면 위험해요.
- sessions/compaction 없이 long-running task를 하면 context가 터져요.

## 추천 구현 순서

1. `session/` + SQLite store + `kkode session` CLI
2. YOLO 실행 경계 hardening + file mutation checkpoint/undo
3. `grep/glob/read-range` + `gopls` 기반 LSP tool
4. MCP stdio/http client
5. TUI MVP
6. server JSON-RPC/SSE
7. skills/commands/plugin system
8. provider auth/model catalog/cost budget

## 주요 소스

- OpenCode Intro: https://opencode.ai/docs/
- OpenCode Tools: https://opencode.ai/docs/tools/
- OpenCode Agents: https://opencode.ai/docs/agents/
- OpenCode Permissions: https://opencode.ai/docs/permissions/
- OpenCode LSP: https://opencode.ai/docs/lsp/
- OpenCode MCP: https://opencode.ai/docs/mcp-servers/
- OpenCode SDK: https://opencode.ai/docs/sdk/
- OpenAI Codex Config Reference: https://developers.openai.com/codex/config-reference
- OpenAI Codex Skills: https://developers.openai.com/codex/skills
- OpenAI Codex App Server: https://developers.openai.com/codex/app-server
- OpenAI Codex SDK: https://developers.openai.com/codex/sdk
- Claude Code Permissions: https://code.claude.com/docs/en/permissions
- Claude Code Sandboxing: https://code.claude.com/docs/en/sandboxing
- Claude Code Checkpointing: https://code.claude.com/docs/en/checkpointing
- Claude Code Subagents: https://code.claude.com/docs/en/sub-agents
- Claude Agent SDK loop: https://code.claude.com/docs/en/agent-sdk/agent-loop
