# kkode 연구 인덱스 / TODO

작성일: 2026-04-26  
작업 디렉터리: `/home/user/kkode`  
Git: `git init` 완료

## 사용자가 요청한 조사 항목

- [x] `01-go-mcp.md` — Go 언어로 MCP(Model Context Protocol) 서버/클라이언트 연결하는 법
- [x] `02-claude-code-skills.md` — Claude Code Skills의 원리, 구조, 작성/동작 방식
- [x] `03-chatgpt-subscription-codex-opencode-openclaw-go.md` — ChatGPT 구독 기반 Codex/OAuth 사용, OpenClaw/OpenCode 쪽 흐름, Go SDK 후보
- [x] `04-github-copilot-go-sdk.md` — GitHub Copilot SDK, Go SDK 설치/사용/인증/예제
- [x] `05-openai-compatible-provider-design.md` — OpenAI Responses API 중심 multi-provider 설계/구현 메모
- [x] `06-copilot-sdk-provider-followup.md` — Copilot SDK tool/session/hook 후속 조사 및 provider adapter 메모
- [x] `07-implementation-todo.md` — 구현 TODO와 검증 명령

## 검증 원칙

1. 공식 문서 우선: MCP 공식 SDK/스펙, Anthropic/Claude 공식 문서, OpenAI 공식 Codex 문서, GitHub 공식 Copilot 문서.
2. SDK는 `pkg.go.dev`와 GitHub 저장소를 함께 확인.
3. 비공식/커뮤니티 프로젝트는 “공식 아님”을 명시하고, 운영/상용 사용 시 리스크를 따로 적음.
4. 예제 코드는 복붙 시작점으로 충분히 자세히 쓰되, 빠르게 변하는 preview SDK는 최신 문서와 `go doc`/README를 다시 확인하라는 주석을 포함.

## 로컬 검증

- [x] Go 설치/실행 확인: `go version go1.26.2 linux/amd64`
- [x] 공식 MCP Go SDK 대표 예제 컴파일 검증
- [x] mark3labs/mcp-go 대표 예제 컴파일 검증
- [x] OpenAI 공식 Go SDK Responses 예제 컴파일 검증
- [x] opencode-sdk-go 대표 예제 컴파일 검증
- [x] GitHub Copilot Go SDK 대표 예제 컴파일 검증
- [x] 재실행 가능한 검증 스크립트 추가: `scripts/verify-go-examples.sh`
- [x] core/provider 단위 테스트 추가: `go test ./...`
- [x] Copilot CLI live smoke test 추가: `scripts/copilot-smoke.sh gpt-5-mini`

## 결론 요약

- Go에서 MCP는 이제 `github.com/modelcontextprotocol/go-sdk` 공식 Go SDK가 가장 먼저 검토할 선택지다. 기존 인기 대안으로 `mark3labs/mcp-go`, `metoro-io/mcp-golang`도 있다.
- Claude Code Skills는 `SKILL.md`가 중심인 디렉터리 패키지다. YAML frontmatter의 `description`이 자동 로딩 트리거 역할을 하고, 본문/참조파일/스크립트는 progressive disclosure 방식으로 필요할 때 들어온다.
- ChatGPT 구독을 “일반 OpenAI API 키처럼 무제한 API”로 쓰는 것은 공식적으로 같은 개념이 아니다. 공식적으로는 Codex CLI/IDE/Web/App에서 ChatGPT 로그인으로 Codex를 쓰는 흐름이 지원된다. OpenClaw/OpenCode 쪽은 Codex OAuth/provider/plugin/프록시 계열이 존재하지만, 개인용/preview/정책 리스크를 분리해서 봐야 한다.
- GitHub Copilot은 공식 `github.com/github/copilot-sdk/go` Go SDK가 public/technical preview로 제공된다. Copilot CLI와 JSON-RPC로 통신하며, 로컬 로그인/토큰/OAuth GitHub App/BYOK 경로가 있다.

## 현재 구현 요약

- `llm` package: Provider, Model, Auth, PromptRef, Request/Response, Tool, Reasoning, TextFormat, ToolRegistry, ToolLoop.
- `providers/openai`: OpenAI-compatible `/v1/responses` HTTP provider.
- `providers/copilot`: GitHub Copilot SDK session adapter 및 tool 변환기.

