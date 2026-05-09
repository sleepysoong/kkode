# 03. Project context, rules, skills, commands, MCP 강화 제안

작성일: 2026-04-27

## 결론

바이브코딩 툴의 품질은 model보다 **project context 공급 방식**이 더 크게 좌우돼요. `kkode`는 아직 AGENTS/CLAUDE/opencode rules/skills/MCP/commands를 runtime 기능으로 갖고 있지 않아요.

필요한 순서는 아래예요.

1. project instruction loader
2. rules scope/precedence
3. slash command system
4. SKILL.md progressive disclosure
5. MCP stdio/HTTP client
6. MCP tool search와 per-agent scope
7. plugin packaging

## 현재 상태

현재 `kkode`는 `README.md`, `ARCHITECTURE.md`, `research/`는 있지만 runtime이 다음을 자동으로 읽지 않아요.

- `AGENTS.md`
- `CLAUDE.md`
- `.claude/rules/`
- `.opencode/agents/`
- `.codex/skills/`
- `.mcp.json`
- `opencode.json`
- `.kkode/config.*`

즉, 프로젝트별 지식이 문서로는 있어도 agent prompt에 체계적으로 주입되지 않아요.

## Project instruction loader

Claude Code memory 문서는 CLAUDE.md 파일과 auto memory가 session 간 지식을 운반한다고 설명해요. OpenCode는 `/init`이 project root에 `AGENTS.md`를 만들고 git commit을 권장해요. Codex도 AGENTS.md와 rules를 별도 configuration surface로 둬요.

`kkode`는 여러 툴의 관습을 다 읽는 호환 loader가 필요해요.

```go
type InstructionSource struct {
    Path      string
    Scope     Scope // user, project, worktree, directory
    Priority  int
    Content   string
    LoadedAt  time.Time
}

type InstructionLoader interface {
    Load(ctx context.Context, root string) ([]InstructionSource, error)
}
```

권장 탐색 순서는 이렇게 해요.

```text
~/.kkode/KKODE.md
~/.codex/AGENTS.md
~/.claude/CLAUDE.md
<repo>/AGENTS.md
<repo>/CLAUDE.md
<repo>/.kkode/rules/*.md
<repo>/.claude/rules/*.md
<repo>/.opencode/rules/*.md
<subdir>/AGENTS.md 또는 CLAUDE.md
```

중요한 건 “무작정 다 넣지 않는 것”이에요. context budget을 계산해서 path-specific rule만 선택해야해요.

## Rules engine

Rules는 tool permission과 달라요. Rules는 prompt/instruction 정책이에요.

```go
type Rule struct {
    ID       string
    Path     string
    Glob     string
    Priority int
    Content  string
}

type RuleEngine interface {
    Select(ctx context.Context, files []string, task string) ([]Rule, error)
}
```

예시:

```yaml
---
glob: "providers/**/*.go"
priority: 80
---
provider adapter는 Response.Output.ProviderRaw를 보존해야해요.
```

## Slash commands

OpenCode CLI와 Claude Code는 command를 주요 UX로 써요. Codex IDE/CLI도 slash command를 제공해요. `kkode`도 command parser가 필요해요.

초기 command는 다음이면 충분해요.

```text
/init         프로젝트 분석 후 AGENTS.md 또는 KKODE.md 생성해요
/plan         write/bash 없이 계획만 세워요
/build        build agent로 전환해요
/review       변경 diff를 review해요
/undo         마지막 checkpoint 복구해요
/redo         undo 되돌려요
/compact      session summary 생성해요
/model        provider/model 선택해요
/mcp          MCP 서버 관리해요
/skills       skill 목록을 보여줘요
```

```go
type Command interface {
    Name() string
    Description() string
    Run(ctx context.Context, rt Runtime, args []string) error
}
```

## Skills

OpenAI Codex skills 문서는 skill이 `SKILL.md`와 optional scripts/references/assets/agents로 구성되고 progressive disclosure를 사용한다고 설명해요. OpenCode도 Agent Skills와 `skill` tool을 제공해요. Claude Code 역시 skills/plugin 흐름을 갖고 있어요.

`kkode`의 skill system은 다음을 지원해야해요.

```text
.kkode/skills/<skill>/SKILL.md
.kkode/skills/<skill>/scripts/*
.kkode/skills/<skill>/references/*
.kkode/skills/<skill>/assets/*
.kkode/skills/<skill>/agents/*
```

```go
type Skill struct {
    Name        string
    Description string
    Path        string
    Instructions string
    Scripts     []string
    References  []string
    Assets      []string
}

type SkillIndex interface {
    List(ctx context.Context) ([]SkillSummary, error)
    Load(ctx context.Context, name string) (*Skill, error)
    Match(ctx context.Context, prompt string) ([]SkillSummary, error)
}
```

중요 정책:

- 초기 prompt에는 name/description/path만 넣어요.
- 선택된 skill만 전체 `SKILL.md`를 읽어요.
- scripts 실행은 별도 권한 프롬프트 없이 YOLO 실행하되 checkpoint/로그를 남겨야해요.
- skill references는 필요할 때만 read tool로 가져와야해요.

## MCP client

OpenCode MCP 문서는 local/remote server를 지원하고, MCP tool이 context를 크게 먹을 수 있으니 조심하라고 해요. Codex config reference는 stdio/HTTP server, enabled/disabled tools, auth headers, OAuth, startup/tool timeout을 자세히 둬요. Claude Code MCP 문서는 local/remote/SSE/OAuth/resources/prompts/tool search를 다뤄요.

`kkode`는 실제 MCP client가 필요해요.

```go
type ServerConfig struct {
    Name       string
    Transport  Transport // stdio, http, sse
    Command    string
    Args       []string
    URL        string
    Env        map[string]string
    Headers    map[string]string
    EnabledTools []string
    DisabledTools []string
    StartupTimeout time.Duration
    ToolTimeout    time.Duration
    OAuth          *OAuthConfig
}

type Client interface {
    Initialize(ctx context.Context) error
    ListTools(ctx context.Context) ([]llm.Tool, error)
    CallTool(ctx context.Context, call llm.ToolCall) (llm.ToolResult, error)
    Close() error
}
```

## MCP tool search

많은 MCP server를 그대로 붙이면 tool definition만으로 context를 다 써버려요. Claude Code와 OpenCode 문서 모두 tool 수/context 문제를 암시해요. 따라서 tool search가 필요해요.

아이디어:

- 전체 MCP tool은 index에만 저장해요.
- prompt와 agent role을 기준으로 top-k tool만 request에 넣어요.
- tool이 빠졌으면 `tool_search` built-in으로 찾게 해요.

```go
type ToolIndex interface {
    Index(server string, tools []llm.Tool) error
    Search(ctx context.Context, query string, k int) ([]ToolMatch, error)
}
```

초기에는 BM25나 simple keyword matching으로 충분해요. 나중에 embedding을 붙여요.

## Plugin packaging

Plugin은 skills, agents, MCP, commands, hooks를 배포하는 단위가 좋아요.

```text
.kkode-plugin/plugin.json
skills/*
agents/*
commands/*
mcp.json
hooks/*
```

`plugin.json` 예시:

```json
{
  "name": "go-dev",
  "version": "0.1.0",
  "description": "Go 개발용 kkode plugin이에요",
  "skills": ["skills/go-test"],
  "agents": ["agents/go-reviewer.md"],
  "commands": ["commands/coverage.md"],
  "mcp": "mcp.json"
}
```

## 구현 우선순위

1. root `AGENTS.md`/`CLAUDE.md`/`KKODE.md` auto-load는 구현됐고, subdir scope와 `/init` 생성을 보강하기
2. `commands/` parser와 `/init`, `/plan`, `/build`, `/compact`
3. `skills/` index + progressive load
4. MCP stdio client
5. MCP HTTP/SSE client
6. MCP OAuth + tool allowlist/denylist
7. tool search
8. plugin package

## 참고 소스

- Claude Code memory: https://code.claude.com/docs/en/memory
- Claude Code MCP: https://code.claude.com/docs/en/mcp
- Claude Code subagents: https://code.claude.com/docs/en/sub-agents
- OpenCode Intro: https://opencode.ai/docs/
- OpenCode Skills: https://opencode.ai/docs/skills/
- OpenCode MCP: https://opencode.ai/docs/mcp-servers/
- OpenAI Codex Skills: https://developers.openai.com/codex/skills
- OpenAI Codex Config Reference: https://developers.openai.com/codex/config-reference
