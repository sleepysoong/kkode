#!/usr/bin/env bash
set -euo pipefail

work="$(mktemp -d "${TMPDIR:-/tmp}/kkode-go-verify.XXXXXX")"
trap 'rm -rf "$work"' EXIT

printf 'Go: '
go version
printf 'Workdir: %s\n' "$work"

run_case() {
  local name="$1"
  shift
  printf '\n== %s ==\n' "$name"
  "$@"
}

run_case official-mcp bash -c '
set -euo pipefail
mkdir -p "$0/mcp-official"
cd "$0/mcp-official"
go mod init verify.local/mcp-official >/dev/null
go get github.com/modelcontextprotocol/go-sdk/mcp@v1.5.0 >/dev/null
cat > main.go <<'GO'
package main

import (
    "context"
    "log"
    "net/http"
    "os/exec"

    "github.com/modelcontextprotocol/go-sdk/mcp"
)

type greetArgs struct {
    Name string
}

func main() {
    server := mcp.NewServer(&mcp.Implementation{Name: "greeter-server", Version: "v0.1.0"}, nil)
    mcp.AddTool(server, &mcp.Tool{Name: "greet", Description: "Return a greeting."},
        func(ctx context.Context, req *mcp.CallToolRequest, args greetArgs) (*mcp.CallToolResult, any, error) {
            return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "안녕, " + args.Name + "!"}}}, nil, nil
        })
    _ = mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server { return server }, nil)
    _ = &mcp.StreamableClientTransport{Endpoint: "http://localhost:8080/mcp"}
    if false {
        if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil { log.Fatal(err) }
        client := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "v0.1.0"}, nil)
        transport := &mcp.CommandTransport{Command: exec.Command("myserver")}
        session, err := client.Connect(context.Background(), transport, nil)
        if err != nil { log.Fatal(err) }
        defer session.Close()
        _, _ = session.ListTools(context.Background(), &mcp.ListToolsParams{})
        _, _ = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "greet", Arguments: map[string]any{"name": "kkode"}})
    }
}
GO
go test ./...
' "$work"

run_case mark3labs-mcp bash -c '
set -euo pipefail
mkdir -p "$0/mcp-mark3labs"
cd "$0/mcp-mark3labs"
go mod init verify.local/mcp-mark3labs >/dev/null
go get github.com/mark3labs/mcp-go/mcp@v0.49.0 github.com/mark3labs/mcp-go/server@v0.49.0 >/dev/null
cat > main.go <<'GO'
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/mark3labs/mcp-go/mcp"
    "github.com/mark3labs/mcp-go/server"
)

func main() {
    s := server.NewMCPServer("Calculator Demo", "1.0.0", server.WithToolCapabilities(false))
    tool := mcp.NewTool("add",
        mcp.WithDescription("Add two integers"),
        mcp.WithNumber("a", mcp.Required(), mcp.Description("first number")),
        mcp.WithNumber("b", mcp.Required(), mcp.Description("second number")),
    )
    s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
        a, err := req.RequireFloat("a"); if err != nil { return mcp.NewToolResultError(err.Error()), nil }
        b, err := req.RequireFloat("b"); if err != nil { return mcp.NewToolResultError(err.Error()), nil }
        return mcp.NewToolResultText(fmt.Sprintf("%v", a+b)), nil
    })
    if false { if err := server.ServeStdio(s); err != nil { log.Fatal(err) } }
}
GO
go test ./...
' "$work"

run_case openai-go bash -c '
set -euo pipefail
mkdir -p "$0/openai-go"
cd "$0/openai-go"
go mod init verify.local/openai-go >/dev/null
go get github.com/openai/openai-go@v1.12.0 >/dev/null
cat > main.go <<'GO'
package main

import (
    "context"
    "fmt"
    "os"

    openai "github.com/openai/openai-go"
    "github.com/openai/openai-go/option"
    "github.com/openai/openai-go/responses"
)

func main() {
    client := openai.NewClient(option.WithAPIKey(os.Getenv("OPENAI_API_KEY")))
    if false {
        resp, err := client.Responses.New(context.Background(), responses.ResponseNewParams{
            Model: "gpt-5.4",
            Input: responses.ResponseNewParamsInputUnion{OfString: openai.String("Write a small Go HTTP server with tests.")},
        })
        if err != nil { panic(err) }
        fmt.Println(resp.OutputText())
    }
}
GO
go test ./...
' "$work"

run_case opencode-sdk-go bash -c '
set -euo pipefail
mkdir -p "$0/opencode"
cd "$0/opencode"
go mod init verify.local/opencode >/dev/null
go get github.com/sst/opencode-sdk-go@v0.19.2 >/dev/null
cat > main.go <<'GO'
package main

import (
    "context"
    "fmt"

    opencode "github.com/sst/opencode-sdk-go"
)

func main() {
    client := opencode.NewClient()
    if false {
        sessions, err := client.Session.List(context.TODO(), opencode.SessionListParams{})
        if err != nil { panic(err.Error()) }
        fmt.Printf("%+v\n", sessions)
    }
}
GO
go test ./...
' "$work"

run_case copilot-sdk-go bash -c '
set -euo pipefail
mkdir -p "$0/copilot"
cd "$0/copilot"
go mod init verify.local/copilot >/dev/null
go get github.com/github/copilot-sdk/go@v0.3.0 >/dev/null
cat > main.go <<'GO'
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
    client := copilot.NewClient(&copilot.ClientOptions{LogLevel: "error"})
    if false {
        if err := client.Start(ctx); err != nil { log.Fatal(err) }
        defer client.Stop()
        auth, err := client.GetAuthStatus(ctx); if err != nil { log.Fatal(err) }
        fmt.Printf("auth: %+v\n", auth)
        models, err := client.ListModels(ctx); if err != nil { log.Fatal(err) }
        fmt.Printf("models: %+v\n", models)
        session, err := client.CreateSession(ctx, &copilot.SessionConfig{Model: "gpt-5"}); if err != nil { log.Fatal(err) }
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
        messageID, err := session.Send(ctx, copilot.MessageOptions{Prompt: "Create a small Go HTTP server and explain the test strategy."})
        if err != nil { log.Fatal(err) }
        fmt.Println("message id:", messageID)
    }
}
GO
go test ./...
' "$work"

printf '\nAll Go compile smoke tests passed.\n'
