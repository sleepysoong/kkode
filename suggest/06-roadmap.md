# 06. kkode 구현 로드맵 제안

작성일: 2026-04-27

## 목표

`opencode`, Codex, Claude Code 같은 도구를 Go로 포팅한다고 생각하면, 목표는 “provider wrapper”가 아니라 **안전한 autonomous coding runtime + 좋은 terminal product**예요.

아래 로드맵은 현재 `kkode` 상태에서 가장 적은 낭비로 제품 느낌을 만드는 순서예요.

## Phase 0: 기준선 고정

기간: 1~2일

### 작업

- 현재 `agent`, `workspace`, `providers` 테스트를 유지해요.
- `suggest/` 내용을 GitHub issue 또는 TODO로 변환해요.
- `cmd/kkode-agent`를 유지하되 새 root CLI 설계만 문서화해요.

### 완료 기준

- `go test ./...` 통과해요.
- `go vet ./...` 통과해요.
- Codex CLI smoke가 계속 통과해요.

## Phase 1: Session runtime

기간: 3~5일

### 작업

- `session/` package 추가해요.
- JSON store 먼저 구현해요.
- session ID, turns, events, summary, last response ID를 저장해요.
- `kkode run --session`, `kkode session list/resume/fork`를 추가해요.

### 완료 기준

- 같은 session에서 두 번째 prompt가 이전 turn을 이어받아요.
- session export/import가 돼요.
- fork가 원본 session을 바꾸지 않아요.

## Phase 2: Permission engine + safer tools

기간: 5~7일

### 작업

- 권한 기능은 만들지 않고 YOLO 실행 정책을 유지해요.
- `workspace_read_file` line range를 지원해요.
- `file_glob`, `file_grep`, `file_apply_patch`, `file_delete`, `file_move`를 유지해요.
- command result를 구조화해요.

### 완료 기준

- `.git/config` write는 기본 차단돼요.
- `go test ./...`는 allow rule로 자동 실행돼요.
- `rm *` deny rule 테스트가 있어요.
- patch 적용 전후 snapshot 테스트가 있어요.

## Phase 3: Checkpoint/undo/redo

기간: 4~6일

### 작업

- file edit tool 실행 전 snapshot 저장은 구현됐고 유지해요.
- file checkpoint restore/list/detail/delete/prune tool/API와 keep-latest retention 정리는 구현됐어요.
- `kkode undo`, `kkode redo`, `kkode rewind`를 추가해요.
- bash command 변경은 추적 한계를 명확히 표시해요.

### 완료 기준

- write/edit/apply_patch/delete/move로 만든 변경은 checkpoint id로 restore할 수 있어요.
- conversation-only rewind와 code restore를 분리해요.
- checkpoint가 session resume 뒤에도 남아 있어요.
- 오래된 file checkpoint는 `keep_latest` 기준으로 한 번에 정리할 수 있어요.

## Phase 4: Project context + commands + skills

기간: 5~7일

### 작업

- root AGENTS.md, CLAUDE.md, KKODE.md loader는 구현됐고, subdir scope와 `/init` 초안을 보강해요.
- `.kkode/rules/*.md` path-specific rule을 구현해요.
- `/init`, `/plan`, `/build`, `/compact` command를 추가해요.
- `skills/` package와 `SKILL.md` progressive disclosure를 구현해요.

### 완료 기준

- `/init`이 프로젝트를 분석해 AGENTS.md 초안을 만들어요.
- skill 목록은 name/description만 prompt에 들어가요.
- 선택된 skill만 전문을 읽어요.

## Phase 5: MCP client

기간: 7~10일

### 작업

- MCP stdio client를 구현해요.
- MCP HTTP/SSE client를 구현해요.
- enabled_tools/disabled_tools/tool timeout을 지원해요.
- MCP tool을 `llm.ToolRegistry`에 붙여요.
- MCP tool search를 최소 keyword matching으로 구현해요.

### 완료 기준

- filesystem 같은 local MCP 서버 tool을 호출할 수 있어요.
- HTTP MCP 서버 tool 목록을 가져올 수 있어요.
- disabled tool은 request에 노출되지 않아요.

## Phase 6: LSP/code intelligence

기간: 5~10일

### 작업

- `codeintel/` package 추가해요.
- `gopls` client를 붙여요.
- diagnostics, definition, references, hover, symbols를 tool로 제공해요.
- `workspace_grep`와 LSP 결과를 같이 보여주는 explore mode를 만들어요.

### 완료 기준

- broken Go file diagnostics를 모델에게 제공할 수 있어요.
- symbol/reference 기반으로 수정 위치를 찾을 수 있어요.

## Phase 7: TUI MVP

기간: 10~14일

### 작업

- `cmd/kkode` root command를 만들어요.
- Bubble Tea TUI를 추가해요.
- conversation viewport, input, statusline, tool event log, approval modal을 만들어요.
- `Tab` plan/build toggle과 `/` command palette를 넣어요.

### 완료 기준

- TUI에서 prompt 입력, streaming 출력, tool approval, diff 확인이 가능해요.
- `kkode run`과 TUI가 같은 session store를 사용해요.

## Phase 8: server/SDK/provider polish

기간: 10~20일

### 작업

- `kkode serve` JSON-RPC/SSE를 추가해요.
- Go SDK client를 추가해요.
- auth store와 model catalog를 추가해요.
- OpenCode Go provider와 chat compatibility bridge를 추가해요.
- Codex app-server provider를 추가해요.
- usage/cost budget을 runtime에 연결해요.

### 완료 기준

- 외부 Go 앱이 kkode agent session을 시작하고 event stream을 받을 수 있어요.
- provider/model list가 CLI에서 보이고 routing policy가 설명 가능해요.
- 비용/usage budget 초과 시 stop/ask/fallback이 작동해요.


## Phase 9: Gateway API / integration surface

기간: 7~14일

### 작업

- `gateway/` package와 `cmd/kkode-gateway` 또는 `kkode serve`를 추가해요.
- REST API로 session/run/event/file/tool/provider를 노출해요.
- SSE event stream을 추가해요.
- API key auth와 localhost 개발 모드를 추가해요.
- Discord/webhook adapter 설계를 시작해요.
- `gateway/openapi.yaml`을 작성해요.

### 완료 기준

- curl로 session 생성, run 시작, event 조회가 가능해요.
- 웹 패널이 session list와 run event stream을 볼 수 있어요.
- Discord bot이 `POST /runs`를 호출해서 작업을 시작할 수 있어요.

상세 계획은 `suggest/07-gateway-api-plan.md`에 정리했어요.

## 추천 디렉터리 구조

```text
kkode/
├── cmd/kkode/
├── runtime/
├── session/
├── sandbox/
├── workspace/
├── codeintel/
├── mcp/
├── skills/
├── commands/
├── config/
├── auth/
├── modelcatalog/
├── tui/
├── server/
├── gateway/
├── sdk/
└── providers/
```

## P0 issue 초안

1. `session: add JSON-backed session store and resume support`
2. `tools: keep YOLO execution bounded and observable`
3. `checkpoint: add undo/redo UX on top of file restore checkpoints`
4. `workspace: maintain grep/glob/read-range/apply-patch/delete/move tools`
5. `context: add subdir-scoped rules and /init on top of root AGENTS/CLAUDE/KKODE loader`
6. `mcp: implement stdio client and tool registry adapter`
7. `cli: replace kkode-agent with kkode run/session/auth/models`
8. `gateway: expose sessions/runs/events over REST and SSE`

## 참고 소스

- OpenCode Intro: https://opencode.ai/docs/
- OpenCode CLI: https://opencode.ai/docs/cli/
- OpenCode Tools: https://opencode.ai/docs/tools/
- OpenCode Agents: https://opencode.ai/docs/agents/
- OpenAI Codex App Server: https://developers.openai.com/codex/app-server
- OpenAI Codex SDK: https://developers.openai.com/codex/sdk
- Claude Agent SDK loop: https://code.claude.com/docs/en/agent-sdk/agent-loop
- Claude Code permissions/sandbox/checkpoint docs: https://code.claude.com/docs/en/permissions
