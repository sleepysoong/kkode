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

- [x] `SessionProvider` 인터페이스 추가: Copilot/Codex처럼 session lifecycle이 중요한 provider용.
- [x] streaming 인터페이스 추가: `Stream(ctx, Request) (EventStream, error)`.
- [x] prompt template/variables 구현: `llm.Template`.
- [x] model registry 추가: provider별 model capability map.
- [x] provider/request validation 추가: `Request.Validate`.
- [x] token/cost accounting helper 추가: `Usage.EstimatedCost`.

### OpenAI-compatible

- [x] streaming SSE parser 구현.
- [x] built-in tools mapping 확장: web_search, file_search, computer_use, code_interpreter, image_generation, MCP. shell/apply_patch는 OpenAI/Codex built-in별 provider option으로 확장 예정.
- [x] conversations/state API와 Responses API state 전략 선택: core는 Responses item-preserving loop, transcript는 앱 state로 분리.
- [x] retry/backoff/rate-limit handling 추가: 429/5xx retry.
- [x] OpenAI live integration test 추가: `OPENAI_API_KEY` 있을 때만 실행.

### Codex / ChatGPT subscription

- [x] `providers/codexcli` 작성: `codex exec --json` subprocess adapter.
- [x] Codex app-server/harness는 별도 provider boundary로 architecture 문서화. 실제 app-server API 구현은 안정 API 확인 후 별도 패키지로 진행.
- [x] OpenClaw/OpenCode subscription OAuth provider는 local/dev-only로 분류하고 auth storage 안전장치 원칙 문서화.

### GitHub Copilot

- [x] `SessionProvider` 기반으로 `providers/copilot` 확장.
- [x] MCP server config adapter 추가.
- [x] permission mapping 추가, stream/event 변환 추가. pre/post hook audit는 SDK hook 타입 조사 완료 후 확장 지점 유지.
- [x] streaming events를 `llm.StreamEvent`로 변환.
- [x] custom agents/skills config builder 추가.
- [x] live tool-call test 추가: `scripts/copilot-tool-smoke.sh gpt-5-mini`.

### Vibe-coding app layer

- [x] workspace abstraction: 파일 read/write/list/search/shell tool. patch는 다음 단계에서 unified diff 적용기로 확장.
- [x] approval policy: deny/read-only/trusted-writes/allow-all.
- [x] transcript/state persistence.
- [x] provider selection: `llm.Router` provider/model routing.
- [x] safety: path sandbox, command allowlist, basic secret redaction helper.

## 검증 명령

```bash
go test ./...
go vet ./...
./scripts/verify-go-examples.sh
./scripts/copilot-smoke.sh gpt-5-mini
./scripts/copilot-tool-smoke.sh gpt-5-mini
./scripts/codexcli-smoke.sh gpt-5.3-codex
```
