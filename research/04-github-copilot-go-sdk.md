# 04. GitHub Copilot SDK / Go SDK 조사

작성일: 2026-04-26

## 핵심 결론

GitHub Copilot에는 공식 multi-language SDK가 있고, Go SDK도 제공된다.

- 저장소: `github.com/github/copilot-sdk`
- Go module: `github.com/github/copilot-sdk/go`
- 상태: public preview / technical preview 문구가 함께 보인다. 즉 production 도입 전 breaking change 가능성을 감수해야 한다.
- 구조: 앱이 SDK를 호출하고, SDK가 Copilot CLI와 JSON-RPC로 통신한다.
- 전제: Copilot CLI 설치 및 인증이 필요하다.

설치:

```bash
go get github.com/github/copilot-sdk/go
```

## 공식 문서에서 확인한 구조

GitHub Copilot SDK는 “Copilot CLI 뒤의 agentic runtime을 앱에서 프로그래밍 방식으로 호출”하는 SDK다.

공식 README 기준 지원 언어:

| 언어 | 설치 |
|---|---|
| Node.js/TypeScript | `npm install @github/copilot-sdk` |
| Python | `pip install github-copilot-sdk` |
| Go | `go get github.com/github/copilot-sdk/go` |
| .NET | `dotnet add package GitHub.Copilot.SDK` |
| Java | `com.github:copilot-sdk-java` |

GitHub Docs 기준 전제:

- Copilot SDK는 모든 Copilot plan에서 사용 가능하다고 문서화되어 있다.
- preview/technical preview 상태라 기능과 availability가 바뀔 수 있다.
- Copilot CLI가 설치되어 있어야 한다.
- SDK integration 기본 패턴은 앱 → SDK → Copilot CLI(JSON-RPC)다.

## 인증 방식

공식 GitHub Docs에서 확인한 인증 선택지는 다음과 같다.

### 1. 로컬 로그인 사용자 사용

개인 데스크톱 앱/로컬 도구에 적합.

개념 예:

```go
client := copilot.NewClient(nil)
```

이 방식은 로컬 Copilot CLI/gh 인증 상태를 사용한다.

### 2. OAuth GitHub App

웹앱/SaaS처럼 사용자별로 권한을 받아야 할 때 사용.

흐름:

1. 사용자가 OAuth GitHub App을 승인.
2. 앱이 user access token을 받음.
3. SDK `githubToken` option에 전달.

지원 token type:

- `gho_` OAuth user access token
- `ghu_` GitHub App user access token
- `github_pat_` fine-grained personal access token

지원하지 않음:

- classic PAT `ghp_`

### 3. 환경변수

CI/CD, server-side automation에서 사용.

우선순위:

1. `COPILOT_GITHUB_TOKEN`
2. `GH_TOKEN`
3. `GITHUB_TOKEN`

### 4. BYOK

Bring Your Own Key. Azure AI Foundry, OpenAI, Anthropic, OpenAI-compatible endpoints 등의 provider key를 사용해 Copilot 인증을 우회하는 형태다.

- Copilot subscription이 필요 없을 수 있음.
- model provider에 직접 과금됨.
- enterprise model deployment에 적합.

## Go SDK 기본 예제

> 아래 코드는 `pkg.go.dev/github.com/github/copilot-sdk/go` 문서의 API 모양을 기준으로 정리한 시작점이다. preview SDK이므로 실제 사용 전 `go doc github.com/github/copilot-sdk/go`와 최신 README를 확인하라.

```go
package main

import (
    "context"
    "fmt"
    "log"
    "time"

    copilot "github.com/github/copilot-sdk/go"
)

func main() {
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
    defer cancel()

    client := copilot.NewClient(&copilot.ClientOptions{
        LogLevel: "error",
    })

    if err := client.Start(ctx); err != nil {
        log.Fatalf("start copilot client: %v", err)
    }
    defer client.Stop()

    auth, err := client.GetAuthStatus(ctx)
    if err != nil {
        log.Fatalf("auth status: %v", err)
    }
    fmt.Printf("auth: %+v\n", auth)

    models, err := client.ListModels(ctx)
    if err != nil {
        log.Fatalf("list models: %v", err)
    }
    fmt.Printf("models: %+v\n", models)

    session, err := client.CreateSession(ctx, &copilot.SessionConfig{
        Model: "gpt-5",
    })
    if err != nil {
        log.Fatalf("create session: %v", err)
    }
    defer session.Destroy()

    unsubscribe := session.On(func(event copilot.SessionEvent) {
        switch data := event.Data.(type) {
        case *copilot.AssistantMessageData:
            fmt.Println(data.Content)
        default:
            fmt.Printf("event: %s\n", event.Type)
        }
    })
    defer unsubscribe()

    messageID, err := session.Send(ctx, copilot.MessageOptions{
        Prompt: "Create a small Go HTTP server and explain the test strategy.",
    })
    if err != nil {
        log.Fatalf("send: %v", err)
    }
    fmt.Println("message id:", messageID)

    // 실제 앱에서는 done channel, event stream 종료 조건, timeout, cancellation을 구현한다.
    <-ctx.Done()
}
```

## Go SDK로 custom tool 붙이는 개념 예시

Copilot SDK는 custom tools, MCP server attachment, custom agents/subagents 같은 agentic 기능을 제공한다. Go API는 preview이므로 exact type은 최신 docs를 확인해야 하지만 설계는 보통 다음 형태다.

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"

    copilot "github.com/github/copilot-sdk/go"
)

func main() {
    ctx := context.Background()

    client := copilot.NewClient(nil)
    if err := client.Start(ctx); err != nil {
        panic(err)
    }
    defer client.Stop()

    session, err := client.CreateSession(ctx, &copilot.SessionConfig{
        Model: "gpt-5",
        // Tools: []copilot.Tool{...} // 최신 SDK type 확인 필요
    })
    if err != nil {
        panic(err)
    }

    session.On(func(event copilot.SessionEvent) {
        b, _ := json.MarshalIndent(event, "", "  ")
        fmt.Println(string(b))
    })

    _, _ = session.Send(ctx, copilot.MessageOptions{
        Prompt: "Use available tools to inspect this repo and create a TODO list.",
    })
}
```

실제 custom tool 구현 시 확인할 것:

- tool schema type
- permission handler
- pre/post tool hook
- file edit 권한
- MCP local/remote server config type
- session event type 목록

`pkg.go.dev` index에서 확인한 관련 type:

- `Client`
- `ClientOptions`
- `SessionConfig`
- `MessageOptions`
- `CustomAgentConfig`
- `MCPLocalServerConfig`
- `MCPRemoteServerConfig`
- `MCPServerConfig`
- `PermissionHandlerFunc`
- `PreToolUseHandler`, `PostToolUseHandler`
- `SessionEvent`

## Custom agents / sub-agent orchestration

GitHub Docs에 따르면 Copilot SDK는 specialized agents를 session에 붙일 수 있고, 사용자 요청이 특정 agent 전문성과 맞으면 runtime이 sub-agent로 delegation할 수 있다.

개념:

- 각 custom agent는 system prompt, tool restrictions, optional MCP servers를 가진다.
- parent session은 sub-agent lifecycle events를 stream으로 받을 수 있다.
- agent tree UI를 만들 수 있다.
- agent별 tool scope를 좁히는 패턴이 권장된다.

이건 Claude Code subagent와 비슷한 “작업 분리” 모델이지만, GitHub Copilot SDK runtime이 Copilot CLI agent engine 위에서 제공하는 기능이라는 차이가 있다.

## MCP와 Copilot SDK

GitHub Docs custom agents 문서에는 agent에 MCP server를 attach하는 패턴이 있다. Go SDK type index에도 `MCPLocalServerConfig`, `MCPRemoteServerConfig`, `MCPServerConfig`가 존재한다.

개념 예:

```go
session, err := client.CreateSession(ctx, &copilot.SessionConfig{
    Model: "gpt-5",
    // MCPServers: []copilot.MCPServerConfig{
    //     {
    //         Name: "local-tools",
    //         Local: &copilot.MCPLocalServerConfig{
    //             Command: "go",
    //             Args: []string{"run", "./cmd/mcp-server"},
    //         },
    //     },
    // },
})
```

정확한 field name은 preview SDK 변경 가능성이 있으므로 `go doc`로 확인한다.

## Copilot SDK 도입 판단

| 상황 | 적합성 |
|---|---|
| 개인이 Copilot CLI 로그인 상태로 로컬 앱 만들기 | 높음 |
| 사내 도구에서 GitHub OAuth로 사용자별 Copilot agent 제공 | 가능, OAuth App 설계 필요 |
| CI/CD에서 agentic code 작업 자동화 | 가능, token/권한/감사 필요 |
| OpenAI/Anthropic API key로 model provider 직접 쓰기 | BYOK 검토 |
| 안정 API가 꼭 필요한 production | preview 리스크 때문에 주의 |

## 운영 체크리스트

- [ ] Copilot CLI 설치/버전 고정.
- [ ] SDK 버전 pinning.
- [ ] auth 방식 선택: local login / OAuth GitHub App / env token / BYOK.
- [ ] fine-grained token scope 최소화.
- [ ] classic PAT(`ghp_`) 사용 금지.
- [ ] session timeout/cancel 구현.
- [ ] file edit/tool execution permission handler 구현.
- [ ] event stream logging/observability 구현.
- [ ] preview SDK breaking change 대응 계획 수립.

## 소스 검증

- GitHub Copilot SDK repository: https://github.com/github/copilot-sdk  
  - SDK 목적, 지원 언어, Go 설치 명령, Copilot CLI engine 노출 구조 확인.
- Go package docs: https://pkg.go.dev/github.com/github/copilot-sdk/go  
  - `NewClient`, `Client.Start`, `CreateSession`, `GetAuthStatus`, `ListModels`, `MessageOptions`, MCP/custom agent 관련 type 확인.
- GitHub Docs, Choosing a setup path: https://docs.github.com/en/copilot/how-tos/copilot-sdk/set-up-copilot-sdk/choosing-a-setup-path  
  - Copilot CLI 필요, Go SDK 설치 명령, SDK integration architecture 확인.
- GitHub Docs, Authenticating Copilot SDK: https://docs.github.com/en/copilot/how-tos/copilot-sdk/authenticate-copilot-sdk/authenticate-copilot-sdk  
  - local credentials, OAuth GitHub App, env vars, BYOK, token type 확인.
- GitHub Docs, Custom agents and sub-agent orchestration: https://docs.github.com/en/copilot/how-tos/copilot-sdk/use-copilot-sdk/custom-agents  
  - custom agent/subagent, scoped tools, MCP attachment 확인.
- GitHub Docs, OpenAI Codex agent in Copilot: https://docs.github.com/en/copilot/concepts/agents/openai-codex  
  - OpenAI Codex integration이 public preview이며 Copilot subscription으로 powered 가능하다는 내용 확인.


## 로컬 Go 컴파일 검증 메모

2026-04-26에 Go `go1.26.2 linux/amd64` 환경에서 `github.com/github/copilot-sdk/go v0.3.0` 기본 client/session/event/send 예제를 임시 module로 컴파일 검증했다.

검증 중 확인한 현재 API 포인트:

- `Session`은 `SessionID` 필드를 가진다. 메서드 `ID()`는 없다.
- event payload는 `event.Data`에서 type switch를 하고, assistant text는 `*copilot.AssistantMessageData.Content`로 읽는다.
- `session.Send(ctx, options)`는 `(string, error)`를 반환한다. 반환 문자열은 message ID다.
- session 정리는 예제에서는 `session.Destroy()`를 사용했다. 보존/재개가 필요하면 `Disconnect()`와 `DeleteSession()` 차이를 구분해야 한다.
