# 05. TUI, server, SDK, IDE, automation 제품 surface 제안

작성일: 2026-04-27

## 결론

`kkode`가 “Go로 포팅한 바이브코딩 앱”이 되려면 core library만으로는 부족해요. 실제 사용자는 아래 surface를 기대해요.

- interactive TUI
- non-interactive CLI
- local server/app-server
- Go SDK
- IDE/ACP integration
- GitHub/CI automation
- web/share surface

현재는 `cmd/kkode-agent` 단발 실행만 있어요. 이것을 `kkode` 제품 CLI로 확장해야해요.

## OpenCode에서 배울 점

OpenCode 공식 intro는 terminal interface, desktop app, IDE extension을 제공한다고 소개해요. CLI 문서는 기본 실행 시 TUI가 열리고, `run`, `serve`, `session`, `auth`, `models`, `mcp`, `github`, `export/import`, `attach` 같은 programmatic command를 제공해요. SDK 문서는 server를 시작하고 type-safe client로 sessions/files/events/auth를 다룰 수 있게 해요.

즉, core runtime과 UI/server는 분리되어야해요.

## Codex에서 배울 점

Codex app-server 문서는 rich client가 authentication, conversation history, approvals, streamed agent events를 다루기 위해 JSON-RPC 2.0 app-server protocol을 쓴다고 설명해요. transport는 stdio와 websocket을 지원해요. SDK 문서는 CI/CD, 내부 도구, 앱 integration을 위해 local Codex agent를 programmatically control한다고 해요.

즉, `kkode`도 `kkode serve`를 만들어 TUI/IDE/web이 같은 backend를 보게 해야해요.

## Claude Code에서 배울 점

Claude Agent SDK는 CLI가 없어도 agent loop, context management, tools, permissions, cost limits, output을 library로 제어할 수 있다고 설명해요. 또한 SDK docs에는 streaming input/output, approvals/user input, structured output, custom tools, MCP, subagents, hooks, checkpointing, OTel, todo lists가 분리돼요.

즉, Go SDK는 단순 provider wrapper가 아니라 runtime control API여야해요.

## 제안 명령 체계

`cmd/kkode-agent`를 최종적으로 `cmd/kkode`로 승격하는 게 좋아요.

```bash
kkode                         # TUI 시작
kkode run "프롬프트"          # non-interactive 실행
kkode serve                   # local JSON-RPC/SSE server
kkode attach <url>            # 실행 중 server에 TUI attach
kkode session list            # 세션 목록
kkode session resume <id>     # 세션 재개
kkode session fork <id>       # 세션 분기
kkode auth login <provider>   # 인증
kkode models list             # 모델 목록
kkode mcp add/list/remove     # MCP 서버 관리
kkode agent list/create       # agent 정의 관리
kkode skills list/install     # skill 관리
kkode doctor                  # 환경 점검
```

## TUI 제안

Go라면 아래 조합이 좋아요.

- Bubble Tea: TUI app loop
- Lip Gloss: styling
- Bubbles: textarea/list/spinner
- Glamour: markdown rendering

화면 구조:

```text
┌ Session / Agent / Model / Mode / Cost ┐
│ conversation viewport                  │
│ tool event stream                      │
│ diff preview / approval panel          │
├────────────────────────────────────────┤
│ input composer                         │
└ status: cwd, git branch, sandbox, MCP ┘
```

필수 UX:

- `Tab`: plan/build mode toggle
- `Ctrl+R`: review diff
- `Esc Esc`: rewind
- `@`: file/agent mention fuzzy search
- `/`: command palette
- approval modal
- tool event collapsible log
- token/cost indicator

## Server protocol 제안

`kkode serve`는 JSON-RPC + SSE/WebSocket 둘 중 하나로 시작해요.

초기에는 JSON-RPC over stdio와 HTTP/SSE가 좋아요.

```go
type Server interface {
    StartSession(ctx context.Context, req StartSessionRequest) (*Session, error)
    SendPrompt(ctx context.Context, req PromptRequest) (*RunHandle, error)
    StreamEvents(ctx context.Context, sessionID string) (<-chan Event, error)
    Approve(ctx context.Context, req ApprovalResponse) error
    ListSessions(ctx context.Context) ([]SessionSummary, error)
    ReadFile(ctx context.Context, req FileReadRequest) (*FileContent, error)
}
```

Event 종류:

```text
session.started
turn.started
message.delta
reasoning.delta
tool.started
tool.output
tool.failed
permission.requested
permission.resolved
checkpoint.created
diff.updated
turn.completed
turn.failed
```

## Go SDK 제안

사용자는 내부 자동화에서 이렇게 쓰고 싶어할 거예요.

```go
client := kkode.NewClient(kkode.ClientConfig{
    Root: ".",
    Provider: "openai",
    Model: "gpt-5-mini",
})

sess, err := client.StartSession(ctx, kkode.SessionOptions{
    Agent: "build",
    Sandbox: "workspace-write",
})

stream, err := sess.RunStream(ctx, "실패하는 테스트를 고쳐요")
for ev := range stream.Events() {
    fmt.Println(ev.Type, ev.Tool, ev.Text)
}
```

SDK는 `llm.Provider`보다 한 층 위여야해요. 즉 session, permission, tools, checkpoints를 포함해야해요.

## IDE/ACP 제안

OpenCode 문서에는 ACP support와 IDE extension surface가 있어요. `kkode`도 바로 IDE extension을 만들기보다 ACP/JSON-RPC server부터 맞춰야해요.

초기 목표:

- VS Code extension 없이도 stdio server로 붙을 수 있게 해요.
- file mention/search API를 제공해요.
- diff/approval event를 UI가 처리할 수 있게 해요.

## GitHub/CI automation 제안

OpenCode CLI에는 GitHub agent 설치/run 흐름이 있어요. Codex도 GitHub Action을 automation surface로 둬요.

`kkode`는 나중에 다음을 제공할 수 있어요.

```bash
kkode github install
kkode github run --event pull_request
kkode ci review --base main
kkode ci fix --command "go test ./..."
```

P2지만 제품 신뢰도에는 좋아요.

## Share/export/import

OpenCode는 conversation share와 export/import를 제공해요. `kkode`도 민감정보 masking을 포함해 export가 필요해요.

```bash
kkode session export <id> --redact > session.json
kkode session import session.json
kkode share <id> --private
```

초기에는 share 서버 없이 export만 해도 돼요.

## 구현 우선순위

1. `cmd/kkode` root command로 CLI 재구성
2. `kkode run`이 현재 `cmd/kkode-agent` 기능을 흡수
3. `kkode session` 추가
4. Bubble Tea TUI MVP
5. `kkode serve` JSON-RPC/SSE
6. Go SDK runtime client
7. IDE/ACP adapter
8. GitHub/CI automation

## 참고 소스

- OpenCode Intro: https://opencode.ai/docs/
- OpenCode CLI: https://opencode.ai/docs/cli/
- OpenCode SDK: https://opencode.ai/docs/sdk/
- OpenCode Server: https://opencode.ai/docs/server/
- OpenAI Codex App Server: https://developers.openai.com/codex/app-server
- OpenAI Codex SDK: https://developers.openai.com/codex/sdk
- Claude Agent SDK overview: https://code.claude.com/docs/en/agent-sdk/overview
- Claude Agent SDK loop: https://code.claude.com/docs/en/agent-sdk/agent-loop
