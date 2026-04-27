package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/workspace"
)

type fakeProvider struct{ calls int }

func (p *fakeProvider) Name() string                   { return "fake" }
func (p *fakeProvider) Capabilities() llm.Capabilities { return llm.Capabilities{Tools: true} }
func (p *fakeProvider) Generate(ctx context.Context, req llm.Request) (*llm.Response, error) {
	p.calls++
	if p.calls == 1 {
		return &llm.Response{ID: "r1", Output: []llm.Item{{Type: llm.ItemFunctionCall, ToolCall: &llm.ToolCall{CallID: "c1", Name: "workspace_list", Arguments: json.RawMessage(`{"path":"."}`)}}}, ToolCalls: []llm.ToolCall{{CallID: "c1", Name: "workspace_list", Arguments: json.RawMessage(`{"path":"."}`)}}}, nil
	}
	return &llm.Response{Text: "완료했어요", Output: []llm.Item{{Type: llm.ItemMessage, Role: llm.RoleAssistant, Content: "완료했어요"}}}, nil
}

func TestAgentRunWithWorkspaceTool(t *testing.T) {
	ws, err := workspace.New(t.TempDir(), llm.ApprovalPolicy{Mode: llm.ApprovalReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	var events []TraceEvent
	ag, err := New(Config{Provider: &fakeProvider{}, Model: "fake", Workspace: ws, Observer: ObserverFunc(func(ctx context.Context, event TraceEvent) { events = append(events, event) })})
	if err != nil {
		t.Fatal(err)
	}
	res, err := ag.Run(context.Background(), "목록을 봐요")
	if err != nil {
		t.Fatal(err)
	}
	if res.Response.Text != "완료했어요" {
		t.Fatalf("response=%#v", res.Response)
	}
	if len(res.Trace) == 0 {
		t.Fatal("trace가 비어 있으면 안 돼요")
	}
	var sawTool bool
	for _, ev := range events {
		if ev.Type == "tool.completed" && ev.Tool == "workspace_list" {
			sawTool = true
		}
	}
	if !sawTool {
		t.Fatalf("tool event missing: %#v", events)
	}
}

func TestGuardrailBlocks(t *testing.T) {
	ag, err := New(Config{Provider: &fakeProvider{}, Model: "fake", Guardrails: Guardrails{BlockedSubstrings: []string{"password"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ag.Run(context.Background(), "show password")
	if err == nil || !strings.Contains(err.Error(), "guardrail") {
		t.Fatalf("expected guardrail error, got %v", err)
	}
}

func TestOutputGuardrailBlocks(t *testing.T) {
	ag, err := New(Config{Provider: &fakeProvider{calls: 1}, Model: "fake", Guardrails: Guardrails{BlockedOutputSubstrings: []string{"완료"}}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := ag.Run(context.Background(), "응답해요")
	if err == nil || !strings.Contains(err.Error(), "guardrail") {
		t.Fatalf("expected guardrail error, got %v", err)
	}
	if res == nil || res.Response == nil || res.Response.Text != "완료했어요" {
		t.Fatalf("response=%#v", res)
	}
}
