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

func TestRenderPrompt(t *testing.T) {
	got := renderPrompt(llm.Request{Instructions: "be terse", Messages: []llm.Message{llm.UserText("hello")}})
	want := "Instructions:\nbe terse\n\nUSER: hello"
	if got != want {
		t.Fatalf("renderPrompt() = %q, want %q", got, want)
	}
}
