package kruntime

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sleepysoong/kkode/agent"
	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/session"
)

type historyProvider struct {
	requests []llm.Request
}

func (p *historyProvider) Name() string { return "history" }
func (p *historyProvider) Capabilities() llm.Capabilities {
	return llm.Capabilities{Tools: true}
}
func (p *historyProvider) Generate(ctx context.Context, req llm.Request) (*llm.Response, error) {
	p.requests = append(p.requests, req)
	return &llm.Response{ID: session.NewID("resp"), Provider: p.Name(), Model: req.Model, Text: "응답", Output: []llm.Item{{Type: llm.ItemMessage, Role: llm.RoleAssistant, Content: "응답"}}}, nil
}

func TestRuntimeRunResumeAndTodoTools(t *testing.T) {
	ctx := context.Background()
	store, err := session.OpenSQLite(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider := &historyProvider{}
	ag, err := agent.New(agent.Config{Provider: provider, Model: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	rt := &Runtime{Store: store, Agent: ag, ProjectRoot: "/repo", ProviderName: provider.Name(), Model: "fake", EnableTodos: true}
	first, err := rt.Run(ctx, RunOptions{Prompt: "첫 요청"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Session.ID == "" || len(first.Session.Turns) != 1 {
		t.Fatalf("first=%#v", first.Session)
	}
	second, err := rt.Run(ctx, RunOptions{SessionID: first.Session.ID, Prompt: "둘째 요청"})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Session.Turns) != 2 {
		t.Fatalf("turns=%d", len(second.Session.Turns))
	}
	if len(provider.requests) != 2 || len(provider.requests[1].Messages) < 3 {
		t.Fatalf("history not attached: %#v", provider.requests)
	}
	var hasTodo bool
	for _, tool := range provider.requests[0].Tools {
		if tool.Name == "todo_write" {
			hasTodo = true
		}
	}
	if !hasTodo {
		t.Fatalf("todo tool missing: %#v", provider.requests[0].Tools)
	}
	if provider.requests[0].Messages[0].Role != llm.RoleDeveloper || !strings.Contains(provider.requests[0].Messages[0].Content, "todo_write") {
		t.Fatalf("todo instructions missing: %#v", provider.requests[0].Messages)
	}
}

func TestRuntimeTraceEventsSaved(t *testing.T) {
	ctx := context.Background()
	store, err := session.OpenSQLite(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider := &toolProvider{}
	ag, err := agent.New(agent.Config{Provider: provider, Model: "fake", Tools: []llm.Tool{{Kind: llm.ToolFunction, Name: "echo", Parameters: map[string]any{"type": "object"}}}, ToolHandlers: llm.ToolRegistry{"echo": llm.JSONToolHandler(func(ctx context.Context, in struct {
		Text string `json:"text"`
	}) (string, error) {
		return in.Text, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	rt := &Runtime{Store: store, Agent: ag, ProjectRoot: "/repo", ProviderName: provider.Name(), Model: "fake"}
	res, err := rt.Run(ctx, RunOptions{Prompt: "도구"})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadSession(ctx, res.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sawTool bool
	for _, ev := range loaded.Events {
		if ev.Type == "tool.completed" && ev.Tool == "echo" {
			sawTool = true
		}
	}
	if !sawTool {
		t.Fatalf("events=%#v", loaded.Events)
	}
}

type toolProvider struct{ calls int }

func (p *toolProvider) Name() string                   { return "tool" }
func (p *toolProvider) Capabilities() llm.Capabilities { return llm.Capabilities{Tools: true} }
func (p *toolProvider) Generate(ctx context.Context, req llm.Request) (*llm.Response, error) {
	p.calls++
	if p.calls == 1 {
		args, _ := json.Marshal(map[string]string{"text": "ok"})
		call := llm.ToolCall{CallID: "call_1", Name: "echo", Arguments: args}
		return &llm.Response{ID: "resp_tool", ToolCalls: []llm.ToolCall{call}, Output: []llm.Item{{Type: llm.ItemFunctionCall, ToolCall: &call}}}, nil
	}
	return &llm.Response{ID: "resp_final", Text: "완료", Output: []llm.Item{{Type: llm.ItemMessage, Role: llm.RoleAssistant, Content: "완료"}}}, nil
}
