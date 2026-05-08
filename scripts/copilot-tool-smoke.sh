#!/usr/bin/env bash
set -euo pipefail

model="${1:-gpt-5-mini}"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
cat > "$tmpdir/kkode-copilot-tool-smoke.go" <<'GO'
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	ghcopilot "github.com/github/copilot-sdk/go"
	"github.com/sleepysoong/kkode/llm"
	kc "github.com/sleepysoong/kkode/providers/copilot"
)

func main() {
	model := "gpt-5-mini"
	if len(os.Args) > 1 {
		model = os.Args[1]
	}
	called := false
	toolDef := llm.Tool{Name: "echo_text", Description: "Echo input text. Use this when asked to echo through a tool.", Parameters: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []any{"text"}, "additionalProperties": false}}
	tool := kc.ToCopilotTool(toolDef, llm.JSONToolHandler(func(ctx context.Context, in struct{ Text string `json:"text"` }) (string, error) {
		called = true
		return "TOOL:"+in.Text, nil
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	client := ghcopilot.NewClient(&ghcopilot.ClientOptions{LogLevel: "error"})
	tool.SkipPermission = true
	if err := client.Start(ctx); err != nil {
		if isAuthUnavailable(err) {
			fmt.Printf("SKIP: Copilot tool smoke unavailable: %v\n", err)
			return
		}
		panic(err)
	}
	defer client.Stop()
	session, err := client.CreateSession(ctx, &ghcopilot.SessionConfig{Model: model, ClientName: "kkode-tool-smoke", Tools: []ghcopilot.Tool{tool}, OnPermissionRequest: func(req ghcopilot.PermissionRequest, inv ghcopilot.PermissionInvocation) (ghcopilot.PermissionRequestResult, error) {
		return ghcopilot.PermissionRequestResult{Kind: ghcopilot.PermissionRequestResultKindApproved}, nil
	}})
	if err != nil {
		if isAuthUnavailable(err) {
			fmt.Printf("SKIP: Copilot tool smoke unavailable: %v\n", err)
			return
		}
		panic(err)
	}
	defer session.Destroy()
	resp, err := session.SendAndWait(ctx, ghcopilot.MessageOptions{Prompt: "You must call the echo_text tool with text=hello. After the tool returns, reply with exactly the tool output and nothing else."})
	if err != nil {
		if isAuthUnavailable(err) {
			fmt.Printf("SKIP: Copilot tool smoke unavailable: %v\n", err)
			return
		}
		panic(err)
	}
	text := ""
	if resp != nil {
		if d, ok := resp.Data.(*ghcopilot.AssistantMessageData); ok {
			text = d.Content
		}
	}
	fmt.Println(strings.TrimSpace(text))
	if !called {
		panic("echo_text tool was not called")
	}
}

func isAuthUnavailable(err error) bool {
	text := strings.ToLower(err.Error())
	for _, needle := range []string{"auth", "credential", "login", "unauthorized", "forbidden", "not signed in"} {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
GO

go run "$tmpdir/kkode-copilot-tool-smoke.go" "$model"
