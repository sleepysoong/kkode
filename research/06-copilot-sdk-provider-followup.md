# 06. GitHub Copilot SDK provider/tool 후속 조사 및 구현 메모

작성일: 2026-04-26

## 핵심 결론

GitHub Copilot SDK는 OpenAI-compatible HTTP API가 아니라 **Copilot CLI를 JSON-RPC로 구동하는 agent runtime SDK**다. 따라서 OpenAI provider와 같은 request/response 변환만으로는 충분하지 않고, adapter는 다음 차이를 흡수해야 한다.

- Copilot SDK는 `SessionConfig` 중심이다.
- caller-implemented tools는 session 생성 시 `Tools []Tool`로 붙인다.
- tool handler는 `ToolInvocation`을 받아 `ToolResult`를 반환한다.
- MCP 서버는 `MCPServers map[string]MCPServerConfig`로 붙인다.
- custom agents, skills, hooks, permission handler가 session config에 들어간다.
- auth는 GitHub 로그인, OAuth GitHub App, env token, BYOK 등 여러 경로가 있다.

## 공식/SDK에서 확인한 현재 Go API

`github.com/github/copilot-sdk/go v0.3.0` 기준:

### `SessionConfig`

중요 필드:

- `Model string`
- `ReasoningEffort string`
- `Tools []Tool`
- `MCPServers map[string]MCPServerConfig`
- `CustomAgents []CustomAgentConfig`
- `SkillDirectories []string`
- `DisabledSkills []string`
- `WorkingDirectory string`
- `OnPermissionRequest PermissionHandlerFunc`
- `Hooks *SessionHooks`
- `Streaming bool`
- `GitHubToken string`

### custom tool

```go
type Tool struct {
    Name string
    Description string
    Parameters map[string]any
    OverridesBuiltInTool bool
    SkipPermission bool
    Handler ToolHandler
}

type ToolHandler func(invocation ToolInvocation) (ToolResult, error)
```

`ToolInvocation`에는 `SessionID`, `ToolCallID`, `ToolName`, `Arguments`, `TraceContext`가 있다.

`ToolResult`에는 `TextResultForLLM`, binary results, `ResultType`, `Error`, session log, telemetry가 있다.

### hooks / permission

- `OnPreToolUse` — 실행 전 args 수정/허용/거부/추가 context.
- `OnPostToolUse` — 실행 후 결과 수정/추가 context.
- `OnPermissionRequest` — file write, shell, URL fetch 같은 작업 승인/거부.
- permission result kind는 `approve-once`, `reject`, `user-not-available`, `no-result` 계열이다.

### MCP

- `MCPStdioServerConfig` — command/args/env/cwd/tools.
- `MCPHTTPServerConfig` — url/headers/tools.

### custom agents / skills

GitHub Docs 기준 custom agents는 자체 prompt와 tool 제한, optional MCP server를 가진 session-attached agent다. runtime은 사용자 intent에 맞춰 sub-agent delegation을 할 수 있다.

Skills는 `SKILL.md` 디렉터리 패키지를 session에 로드하는 방식이고, Go에서는 `SkillDirectories`, `DisabledSkills`로 제어한다.

## 구현된 adapter

패키지: `providers/copilot`

### `copilot.Client`

`llm.Provider`를 구현한다.

- `Generate(ctx, llm.Request)`를 Copilot session `SendAndWait`로 변환.
- `Model` -> `SessionConfig.Model`.
- `Reasoning.Effort` -> `SessionConfig.ReasoningEffort`.
- `Messages`/`Instructions` -> 단일 prompt 문자열 렌더링.
- final assistant message -> `llm.Response.Text`.
- default permission은 deny. `Config.ApproveAll`을 켜야 approve-once를 반환한다.

### `ToCopilotTool`

core `llm.Tool` + `llm.ToolHandler`를 Copilot SDK `Tool`로 변환한다.

```go
converted := copilot.ToCopilotTool(tool, llm.JSONToolHandler(func(ctx context.Context, in Input) (string, error) {
    return "result", nil
}))
```

## 현재 한계

Copilot SDK adapter는 OpenAI-compatible Responses adapter보다 agent-runtime 성격이 강하다.

- OpenAI처럼 raw reasoning/tool item을 직접 request/response에 노출하지 않는다.
- request마다 tool schema를 보내는 것보다 session에 tool을 등록하는 구조다.
- structured output은 prompt/schema convention이나 BYOK provider layer로 별도 설계해야 한다.
- permission/hook/session lifecycle을 제대로 다루려면 앱 레벨 session manager가 필요하다.

따라서 long-term 설계는 다음이 좋다.

```text
llm.Provider          // 단발/공통 생성 인터페이스
llm.SessionProvider   // Copilot/Codex 같은 agent session provider
llm.ToolRegistry      // provider별 tool adapter로 변환
llm.AuthProvider      // API key, OAuth, local login, BYOK
```

## live 검증

환경에 Copilot CLI가 설치되어 있었고 `copilot --version` 결과는 다음이었다.

```text
GitHub Copilot CLI 1.0.34
```

SDK `ListModels`로 확인한 모델 일부:

```text
auto
claude-haiku-4.5
gpt-5.3-codex
gpt-5.2-codex
gpt-5.2
gpt-5.4-mini
gpt-5-mini
gpt-4.1
```

`providers/copilot` adapter로 실제 `gpt-5-mini` smoke test를 실행했다.

```bash
./scripts/copilot-smoke.sh gpt-5-mini
```

결과:

```text
OK
```

## 소스

- GitHub Copilot SDK setup path: https://docs.github.com/en/copilot/how-tos/copilot-sdk/set-up-copilot-sdk/choosing-a-setup-path
- GitHub Copilot SDK auth: https://docs.github.com/en/copilot/how-tos/copilot-sdk/authenticate-copilot-sdk/authenticate-copilot-sdk
- GitHub Copilot SDK custom agents: https://docs.github.com/en/copilot/how-tos/copilot-sdk/use-copilot-sdk/custom-agents
- GitHub Copilot SDK hooks: https://docs.github.com/en/copilot/how-tos/copilot-sdk/use-copilot-sdk/working-with-hooks
- GitHub Copilot SDK custom skills: https://docs.github.com/en/copilot/how-tos/copilot-sdk/use-copilot-sdk/custom-skills
- Go package docs: https://pkg.go.dev/github.com/github/copilot-sdk/go
