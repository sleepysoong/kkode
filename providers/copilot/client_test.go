package copilot

import (
	"context"
	"encoding/json"
	"testing"

	ghcopilot "github.com/github/copilot-sdk/go"
	"github.com/sleepysoong/kkode/llm"
)

func TestToCopilotToolExecutesLLMHandler(t *testing.T) {
	tool := llm.Tool{Name: "echo", Description: "echo text", Parameters: map[string]any{"type": "object"}}
	converted := ToCopilotTool(tool, llm.JSONToolHandler(func(ctx context.Context, in struct {
		Text string `json:"text"`
	}) (string, error) {
		return "got:" + in.Text, nil
	}))
	args := map[string]any{"text": "hello"}
	if _, err := json.Marshal(args); err != nil {
		t.Fatal(err)
	}
	out, err := converted.Handler(ghcopilot.ToolInvocation{ToolCallID: "call_1", ToolName: "echo", Arguments: args, TraceContext: context.Background()})
	if err != nil {
		t.Fatal(err)
	}
	if out.TextResultForLLM != "got:hello" || out.ResultType != "text" {
		t.Fatalf("unexpected tool result: %#v", out)
	}
}

func TestRenderPromptUsesSharedTranscriptRenderer(t *testing.T) {
	got := renderPrompt(llm.Request{Instructions: "be terse", Messages: []llm.Message{llm.UserText("hello")}})
	want := "Instructions:\nbe terse\n\nUSER: hello"
	if got != want {
		t.Fatalf("renderPrompt() = %q, want %q", got, want)
	}
}

func TestToCopilotMCPServerAndAgent(t *testing.T) {
	stdio := ToCopilotMCPServer(llm.MCPServer{Kind: llm.MCPStdio, Command: "go", Args: []string{"run", "."}, Tools: []string{"*"}})
	if _, ok := stdio.(ghcopilot.MCPStdioServerConfig); !ok {
		t.Fatalf("expected stdio config: %#v", stdio)
	}
	httpCfg := ToCopilotMCPServer(llm.MCPServer{Kind: llm.MCPHTTP, URL: "https://example.test/mcp", Tools: []string{"read"}})
	if _, ok := httpCfg.(ghcopilot.MCPHTTPServerConfig); !ok {
		t.Fatalf("expected http config: %#v", httpCfg)
	}
	agent := ToCopilotAgent(llm.Agent{Name: "researcher", Prompt: "inspect files", Tools: []string{"view"}, MCPServers: map[string]llm.MCPServer{"x": {Kind: llm.MCPHTTP, URL: "https://example.test/mcp"}}})
	if agent.Name != "researcher" || len(agent.MCPServers) != 1 {
		t.Fatalf("agent=%#v", agent)
	}
}

func TestCopilotEventToStream(t *testing.T) {
	ev := copilotEventToStream(ghcopilot.SessionEvent{Type: ghcopilot.SessionEventTypeAssistantMessage, Data: &ghcopilot.AssistantMessageData{Content: "hi"}}, "copilot", "m")
	if ev.Type != llm.StreamEventTextDelta || ev.Delta != "hi" {
		t.Fatalf("event=%#v", ev)
	}
}
