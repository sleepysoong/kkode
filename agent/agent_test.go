package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/tools"
	"github.com/sleepysoong/kkode/workspace"
)

type fakeProvider struct{ calls int }

func (p *fakeProvider) Name() string                   { return "fake" }
func (p *fakeProvider) Capabilities() llm.Capabilities { return llm.Capabilities{Tools: true} }
func (p *fakeProvider) Generate(ctx context.Context, req llm.Request) (*llm.Response, error) {
	p.calls++
	if p.calls == 1 {
		return &llm.Response{ID: "r1", Output: []llm.Item{{Type: llm.ItemFunctionCall, ToolCall: &llm.ToolCall{CallID: "c1", Name: "file_list", Arguments: json.RawMessage(`{"path":"."}`)}}}, ToolCalls: []llm.ToolCall{{CallID: "c1", Name: "file_list", Arguments: json.RawMessage(`{"path":"."}`)}}}, nil
	}
	return &llm.Response{Text: "완료했어요", Output: []llm.Item{{Type: llm.ItemMessage, Role: llm.RoleAssistant, Content: "완료했어요"}}}, nil
}

func TestAgentRunWithWorkspaceTool(t *testing.T) {
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var events []TraceEvent
	defs, handlers := tools.FileTools(ws)
	ag, err := New(Config{Provider: &fakeProvider{}, Model: "fake", Tools: defs, ToolHandlers: handlers, Observer: ObserverFunc(func(ctx context.Context, event TraceEvent) { events = append(events, event) })})
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
		if ev.Type == "tool.completed" && ev.Tool == "file_list" {
			sawTool = true
		}
	}
	if !sawTool {
		t.Fatalf("tool event missing: %#v", events)
	}
}

func TestAgentDefaultInstructionsComeFromPromptTemplate(t *testing.T) {
	ag, err := New(Config{Provider: &fakeProvider{}, Model: "fake", Tools: []llm.Tool{{Kind: llm.ToolFunction, Name: "file_read"}}})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := ag.Prepare("준비해요")
	if !strings.Contains(req.Instructions, "Go 바이브코딩 에이전트") || !strings.Contains(req.Instructions, "file_read") {
		t.Fatalf("instructions=%q", req.Instructions)
	}
	if req.ParallelToolCalls == nil || !*req.ParallelToolCalls {
		t.Fatal("tool call 병렬 실행 힌트가 기본으로 켜져야해요")
	}
}

func TestAgentInstructionsAppendContextBlocks(t *testing.T) {
	ag, err := New(Config{Provider: &fakeProvider{}, Model: "fake", Instructions: "기본 지침이에요", ContextBlocks: []string{"선택된 Skill이에요: 리뷰", "사용 가능한 Subagent예요: planner"}})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := ag.Prepare("준비해요")
	if !strings.Contains(req.Instructions, "기본 지침이에요") || !strings.Contains(req.Instructions, "선택된 Skill이에요: 리뷰") || !strings.Contains(req.Instructions, "사용 가능한 Subagent예요: planner") {
		t.Fatalf("context block이 instructions에 붙어야 해요: %q", req.Instructions)
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
