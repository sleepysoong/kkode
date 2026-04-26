# 01. Go 언어로 MCP 연결하는 법

작성일: 2026-04-26

## 핵심 결론

Go에서 MCP(Model Context Protocol)를 구현하거나 연결할 때는 현재 다음 순서로 검토하면 된다.

1. **공식 Go SDK**: `github.com/modelcontextprotocol/go-sdk`  
   - MCP 서버/클라이언트 모두 지원.
   - `mcp` 패키지가 핵심 API.
   - `jsonrpc`, `auth`, `oauthex` 패키지도 제공.
2. **인기 대안**: `github.com/mark3labs/mcp-go`  
   - 서버 예제가 풍부하고, stdio/SSE/Streamable HTTP를 지원.
   - 공식 Go SDK README에서도 주요 대안으로 언급됨.
3. **대안**: `github.com/metoro-io/mcp-golang`  
   - 타입 안전한 Go struct 기반 schema 생성/저보일러플레이트를 강조.

MCP 연결에서 가장 중요한 것은 **transport 선택**이다.

- 로컬 IDE/에이전트가 프로세스를 실행하는 방식: **stdio**
- 원격/웹 서버 방식: **Streamable HTTP**
- 구버전 호환: HTTP+SSE/SSE가 남아 있지만, 최신 스펙은 Streamable HTTP를 표준 원격 transport로 본다.

## MCP 기본 구조

MCP는 LLM 앱(host/client)이 외부 도구/데이터(server)에 표준 방식으로 접근하게 해주는 프로토콜이다.

- **MCP Host**: Claude Desktop, Claude Code, Codex, Copilot, OpenCode류 에이전트 등.
- **MCP Client**: Host 내부에서 각 MCP server와 1:1 연결을 유지하는 구성요소.
- **MCP Server**: tools/resources/prompts를 노출하는 프로그램.
- **Transport**: JSON-RPC 메시지를 실어나르는 채널.

공식 transport 스펙 기준:

- MCP 메시지는 JSON-RPC이고 UTF-8로 인코딩된다.
- 현재 표준 transport는 `stdio`와 `Streamable HTTP`다.
- 클라이언트는 가능하면 stdio를 지원하는 것이 권장된다.
- Streamable HTTP는 기존 HTTP+SSE transport를 대체한다.

## 설치

```bash
go mod init example.com/mcp-demo
go get github.com/modelcontextprotocol/go-sdk@latest
```

> 참고: `pkg.go.dev` 기준 공식 SDK의 `mcp` 패키지는 `Client`, `Server`, `ClientSession`, `ServerSession`, `StdioTransport`, `CommandTransport`, `StreamableClientTransport`, `StreamableHTTPHandler` 등을 제공한다.

## 예제 A: 공식 Go SDK로 stdio MCP 서버 만들기

`cmd/greeter-server/main.go`

```go
package main

import (
    "context"
    "log"

    "github.com/modelcontextprotocol/go-sdk/mcp"
)

type greetArgs struct {
    Name string `json:"name" jsonschema:"the person to greet"`
}

func main() {
    server := mcp.NewServer(&mcp.Implementation{
        Name:    "greeter-server",
        Version: "v0.1.0",
    }, nil)

    mcp.AddTool(server, &mcp.Tool{
        Name:        "greet",
        Description: "Return a greeting for the provided name.",
    }, func(ctx context.Context, req *mcp.CallToolRequest, args greetArgs) (*mcp.CallToolResult, any, error) {
        return &mcp.CallToolResult{
            Content: []mcp.Content{
                &mcp.TextContent{Text: "안녕, " + args.Name + "!"},
            },
        }, nil, nil
    })

    // stdio 서버: stdout에는 MCP JSON-RPC 메시지만 써야 한다.
    if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
        log.Fatalf("MCP server failed: %v", err)
    }
}
```

실행:

```bash
go run ./cmd/greeter-server
```

주의:

- stdio transport에서 서버는 **stdout에 로그를 찍으면 안 된다**. stdout은 MCP 메시지 전용이다.
- 로그는 stderr 또는 파일로 보내야 한다.

## 예제 B: 공식 Go SDK로 stdio MCP 서버에 연결하는 클라이언트

`cmd/greeter-client/main.go`

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os/exec"

    "github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
    ctx := context.Background()

    client := mcp.NewClient(&mcp.Implementation{
        Name:    "greeter-client",
        Version: "v0.1.0",
    }, nil)

    // 서버 바이너리를 하위 프로세스로 실행하고 stdio로 연결한다.
    transport := &mcp.CommandTransport{
        Command: exec.Command("go", "run", "./cmd/greeter-server"),
    }

    session, err := client.Connect(ctx, transport, nil)
    if err != nil {
        log.Fatalf("connect: %v", err)
    }
    defer session.Close()

    tools, err := session.ListTools(ctx, &mcp.ListToolsParams{})
    if err != nil {
        log.Fatalf("list tools: %v", err)
    }
    fmt.Printf("tools: %+v\n", tools.Tools)

    res, err := session.CallTool(ctx, &mcp.CallToolParams{
        Name: "greet",
        Arguments: map[string]any{
            "name": "kkode",
        },
    })
    if err != nil {
        log.Fatalf("call tool: %v", err)
    }

    fmt.Printf("result: %+v\n", res.Content)
}
```

## 예제 C: 공식 Go SDK로 Streamable HTTP 서버 만들기

원격 MCP 서버를 만들 때는 Streamable HTTP가 기본 선택지다. 개념적으로는 HTTP endpoint 하나(`/mcp`)가 POST/GET을 처리한다.

```go
package main

import (
    "context"
    "log"
    "net/http"

    "github.com/modelcontextprotocol/go-sdk/mcp"
)

type echoArgs struct {
    Message string `json:"message" jsonschema:"message to echo"`
}

func main() {
    server := mcp.NewServer(&mcp.Implementation{
        Name:    "http-echo-server",
        Version: "v0.1.0",
    }, nil)

    mcp.AddTool(server, &mcp.Tool{
        Name:        "echo",
        Description: "Echo back a message.",
    }, func(ctx context.Context, req *mcp.CallToolRequest, args echoArgs) (*mcp.CallToolResult, any, error) {
        return &mcp.CallToolResult{
            Content: []mcp.Content{&mcp.TextContent{Text: args.Message}},
        }, nil, nil
    })

    handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
        return server
    }, nil)

    http.Handle("/mcp", handler)
    log.Println("listening on http://localhost:8080/mcp")
    log.Fatal(http.ListenAndServe(":8080", nil))
}
```

> 실제 배포에서는 TLS, 인증, origin 검증, rate limit, request body 제한, tool 권한 검토가 필요하다.

## 예제 D: 공식 Go SDK로 Streamable HTTP 서버에 연결

```go
package main

import (
    "context"
    "log"

    "github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
    ctx := context.Background()

    client := mcp.NewClient(&mcp.Implementation{
        Name:    "http-client",
        Version: "v0.1.0",
    }, nil)

    transport := &mcp.StreamableClientTransport{
        Endpoint: "http://localhost:8080/mcp",
    }

    session, err := client.Connect(ctx, transport, nil)
    if err != nil {
        log.Fatal(err)
    }
    defer session.Close()

    result, err := session.CallTool(ctx, &mcp.CallToolParams{
        Name: "echo",
        Arguments: map[string]any{
            "message": "hello over streamable http",
        },
    })
    if err != nil {
        log.Fatal(err)
    }

    log.Printf("result: %+v", result.Content)
}
```

> 위 구조는 공식 SDK의 공개 API 명칭을 기준으로 작성했다. SDK가 빠르게 변하므로 `go doc github.com/modelcontextprotocol/go-sdk/mcp.StreamableClientTransport`로 필드명을 최종 확인하라.

## 예제 E: mark3labs/mcp-go 서버

설치:

```bash
go get github.com/mark3labs/mcp-go@latest
```

서버:

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/mark3labs/mcp-go/mcp"
    "github.com/mark3labs/mcp-go/server"
)

func main() {
    s := server.NewMCPServer(
        "Calculator Demo",
        "1.0.0",
        server.WithToolCapabilities(false),
    )

    tool := mcp.NewTool("add",
        mcp.WithDescription("Add two integers"),
        mcp.WithNumber("a", mcp.Required(), mcp.Description("first number")),
        mcp.WithNumber("b", mcp.Required(), mcp.Description("second number")),
    )

    s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        a, err := req.RequireFloat("a")
        if err != nil {
            return mcp.NewToolResultError(err.Error()), nil
        }
        b, err := req.RequireFloat("b")
        if err != nil {
            return mcp.NewToolResultError(err.Error()), nil
        }
        return mcp.NewToolResultText(fmt.Sprintf("%v", a+b)), nil
    })

    if err := server.ServeStdio(s); err != nil {
        log.Fatalf("server error: %v", err)
    }
}
```

## MCP 보안 체크리스트

- [ ] stdio 서버는 stdout에 로그를 쓰지 않는다.
- [ ] tool description에 실제 부작용을 명확히 적는다.
- [ ] 파일/네트워크/쉘 도구는 allowlist로 제한한다.
- [ ] 원격 Streamable HTTP 서버는 TLS + 인증 + origin 검증을 적용한다.
- [ ] tool 인자를 schema로 검증한다.
- [ ] 사용자의 파일/토큰/환경변수를 외부로 보내는 tool은 별도 승인 절차를 둔다.
- [ ] LLM이 제공한 인자를 그대로 shell command로 붙이지 않는다.
- [ ] MCP server별 권한을 최소화한다.

## 소스 검증

- MCP 공식 Go SDK GitHub: https://github.com/modelcontextprotocol/go-sdk  
  - 공식 Go SDK이며 `mcp`, `jsonrpc`, `auth`, `oauthex` 패키지를 제공한다고 확인.
- MCP Go SDK docs: https://go.sdk.modelcontextprotocol.io/  
  - 공식 SDK package 구조 확인.
- MCP transport spec: https://modelcontextprotocol.io/specification/draft/basic/transports  
  - stdio와 Streamable HTTP가 현재 표준 transport이며 Streamable HTTP가 HTTP+SSE를 대체한다는 내용 확인.
- `pkg.go.dev` 공식 SDK mcp package: https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp  
  - `NewServer`, `AddTool`, `StdioTransport`, `CommandTransport`, `ClientSession.CallTool`, `ListTools` 등 API 확인.
- mark3labs/mcp-go: https://github.com/mark3labs/mcp-go  
  - 설치, 서버 quickstart, transport 지원 확인.
- metoro-io/mcp-golang: https://github.com/metoro-io/mcp-golang  
  - 비공식 Go implementation이며 stdio/HTTP, client/server 지원, type-safety 강조 확인.


## 로컬 Go 컴파일 검증 메모

2026-04-26에 Go `go1.26.2 linux/amd64` 환경에서 다음 대표 예제를 임시 module로 컴파일 검증했다.

- `github.com/modelcontextprotocol/go-sdk v1.5.0` stdio server/client 및 Streamable HTTP handler/client transport 예제: `go test ./...` 통과.
- `github.com/mark3labs/mcp-go v0.49.0` stdio server/tool 예제: `go test ./...` 통과.

주의: `go get github.com/modelcontextprotocol/go-sdk@latest`만으로 일부 transitive dependency의 `go.sum`이 비는 경우가 있어, 실제 프로젝트에서는 `go get github.com/modelcontextprotocol/go-sdk/mcp@<version>` 또는 `go mod tidy`를 함께 실행하는 것이 안전하다. `mark3labs/mcp-go`도 package 단위 `go get` 또는 `go mod tidy`가 필요할 수 있다.
