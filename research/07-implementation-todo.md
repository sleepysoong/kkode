# 07. kkode 구현 TODO

작성일: 2026-04-26

## 이번 턴 완료

- [x] Go module 생성: `github.com/sleepysoong/kkode`
- [x] core package 추가: `llm`
- [x] OpenAI-compatible Responses provider 추가: `providers/openai`
- [x] GitHub Copilot SDK adapter 추가: `providers/copilot`
- [x] Tool abstraction 추가: `Tool`, `ToolCall`, `ToolResult`, `ToolRegistry`, `RunToolLoop`
- [x] Reasoning item 보존 구조 추가: `ReasoningItem`, `Item.ProviderRaw`
- [x] Structured output config 추가: `TextFormat`
- [x] Auth 확장용 타입 추가: `Auth`, `AuthType`
- [x] Model 확장용 타입 추가: `Model`, `ModelRegistry`, `ModelPricing`
- [x] OpenAI request mapping 테스트
- [x] OpenAI response parsing 테스트
- [x] OpenAI-compatible HTTP provider 테스트
- [x] Tool loop 테스트
- [x] Copilot tool adapter 테스트
- [x] Copilot live smoke test: `gpt-5-mini` -> `OK`

## 다음 구현 TODO

### Core

- [ ] `SessionProvider` 인터페이스 추가: Copilot/Codex처럼 session lifecycle이 중요한 provider용.
- [ ] streaming 인터페이스 추가: `Stream(ctx, Request) (EventStream, error)`.
- [ ] prompt template/partials/variables package 분리.
- [ ] model registry 추가: provider별 model capability map.
- [ ] provider option validation 추가.
- [ ] token/cost accounting adapter 추가.

### OpenAI-compatible

- [ ] streaming SSE parser 구현.
- [ ] built-in tools mapping 확장: web_search, file_search, computer_use, shell, apply_patch, MCP.
- [ ] conversations/state API와 Responses API state 전략 비교 후 선택.
- [ ] retry/backoff/rate-limit handling 추가.
- [ ] OpenAI live integration test 추가: `OPENAI_API_KEY` 있을 때만 실행.

### Codex / ChatGPT subscription

- [ ] `providers/codexcli` 작성: `codex exec --json` subprocess adapter.
- [ ] Codex app-server/harness API 조사 후 `providers/codexharness` 설계.
- [ ] OpenClaw/OpenCode subscription OAuth provider는 local/dev-only로 분류하고 auth storage 안전장치 설계.

### GitHub Copilot

- [ ] `SessionProvider` 기반으로 `providers/copilot` 재정렬.
- [ ] MCP server config adapter 추가.
- [ ] Hook mapping 추가: pre-tool/post-tool/permission/audit.
- [ ] streaming events를 `llm.StreamEvent`로 변환.
- [ ] custom agents/skills config builder 추가.
- [ ] live tool-call test 추가: Copilot이 custom `echo` tool을 실제 호출하는지 검증.

### Vibe-coding app layer

- [ ] workspace abstraction: 파일 read/write/search/patch/shell tool.
- [ ] approval policy: read-only auto allow, write/shell/network require approval 등.
- [ ] transcript/state persistence.
- [ ] provider selection: model prefix 또는 config 기반 routing.
- [ ] safety: path sandbox, command allowlist, secret redaction.

## 검증 명령

```bash
go test ./...
./scripts/verify-go-examples.sh
./scripts/copilot-smoke.sh gpt-5-mini
```
