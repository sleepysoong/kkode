package llm

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type scriptedProvider struct{ calls int }

func (p *scriptedProvider) Name() string { return "scripted" }
func (p *scriptedProvider) Capabilities() Capabilities {
	return Capabilities{Tools: true, Reasoning: true}
}
func (p *scriptedProvider) Generate(ctx context.Context, req Request) (*Response, error) {
	p.calls++
	if p.calls == 1 {
		return &Response{
			ID: "resp_1",
			Output: []Item{
				{Type: ItemReasoning, Reasoning: &ReasoningItem{ID: "rs_1", Summary: []string{"need tool"}}, ProviderRaw: json.RawMessage(`{"type":"reasoning","id":"rs_1"}`)},
				{Type: ItemFunctionCall, ToolCall: &ToolCall{CallID: "call_1", Name: "echo", Arguments: json.RawMessage(`{"text":"hello"}`)}, ProviderRaw: json.RawMessage(`{"type":"function_call","call_id":"call_1","name":"echo"}`)},
			},
			ToolCalls: []ToolCall{{CallID: "call_1", Name: "echo", Arguments: json.RawMessage(`{"text":"hello"}`)}},
		}, nil
	}
	if len(req.InputItems) != 3 {
		testingError = "expected reasoning/call/output input items"
	}
	return &Response{Text: "final", Output: []Item{{Type: ItemMessage, Role: RoleAssistant, Content: "final"}}}, nil
}

var testingError string

func TestRunToolLoopPreservesProviderItemsAndAppendsToolOutput(t *testing.T) {
	testingError = ""
	p := &scriptedProvider{}
	reg := ToolRegistry{
		"echo": JSONToolHandler(func(ctx context.Context, in struct {
			Text string `json:"text"`
		}) (string, error) {
			return in.Text, nil
		}),
	}
	resp, err := RunToolLoop(context.Background(), p, Request{Model: "test", Messages: []Message{UserText("go")}}, reg, ToolLoopOptions{MaxIterations: 2})
	if err != nil {
		t.Fatal(err)
	}
	if testingError != "" {
		t.Fatal(testingError)
	}
	if p.calls != 2 || resp.Text != "final" {
		t.Fatalf("unexpected loop result calls=%d resp=%#v", p.calls, resp)
	}
}

func TestRunToolLoopCanExecuteToolCallsInParallel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	p := &parallelProvider{}
	var started atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	reg := ToolRegistry{
		"wait": JSONToolHandler(func(ctx context.Context, in struct {
			Text string `json:"text"`
		}) (string, error) {
			if started.Add(1) == 2 {
				once.Do(func() { close(release) })
			}
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return in.Text, nil
		}),
	}
	resp, err := RunToolLoop(ctx, p, Request{Model: "test", Messages: []Message{UserText("go")}}, reg, ToolLoopOptions{MaxIterations: 2, ParallelToolCalls: true})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "parallel done" {
		t.Fatalf("resp=%#v", resp)
	}
	if p.sawOutputs != "ab" {
		t.Fatalf("tool output order=%q", p.sawOutputs)
	}
}

type parallelProvider struct {
	calls      int
	sawOutputs string
}

func (p *parallelProvider) Name() string { return "parallel" }
func (p *parallelProvider) Capabilities() Capabilities {
	return Capabilities{Tools: true, ParallelToolCalls: true}
}
func (p *parallelProvider) Generate(ctx context.Context, req Request) (*Response, error) {
	p.calls++
	if p.calls == 1 {
		a, _ := json.Marshal(map[string]string{"text": "a"})
		b, _ := json.Marshal(map[string]string{"text": "b"})
		calls := []ToolCall{{CallID: "call_a", Name: "wait", Arguments: a}, {CallID: "call_b", Name: "wait", Arguments: b}}
		return &Response{
			ID:        "resp_parallel",
			Output:    []Item{{Type: ItemFunctionCall, ToolCall: &calls[0]}, {Type: ItemFunctionCall, ToolCall: &calls[1]}},
			ToolCalls: calls,
		}, nil
	}
	for _, item := range req.InputItems {
		if item.ToolResult != nil {
			p.sawOutputs += item.ToolResult.Output
		}
	}
	return &Response{Text: "parallel done", Output: []Item{{Type: ItemMessage, Role: RoleAssistant, Content: "parallel done"}}}, nil
}
