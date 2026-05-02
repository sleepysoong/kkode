package llm

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestToolSetCloneMergeAndPartsAreReusable(t *testing.T) {
	first := NewToolSet([]Tool{{Kind: ToolFunction, Name: "same"}}, ToolRegistry{
		"same": JSONToolHandler(func(ctx context.Context, in struct{}) (string, error) { return "first", nil }),
	})
	second := NewToolSet([]Tool{{Kind: ToolFunction, Name: "other"}}, ToolRegistry{
		"same":  JSONToolHandler(func(ctx context.Context, in struct{}) (string, error) { return "second", nil }),
		"other": JSONToolHandler(func(ctx context.Context, in struct{}) (string, error) { return "other", nil }),
	})
	first.Merge(second)
	defs, handlers := first.Parts()
	if len(defs) != 2 {
		t.Fatalf("merged definitions=%#v", defs)
	}
	result, err := handlers.Execute(context.Background(), ToolCall{Name: "same", Arguments: []byte(`{}`)})
	if err != nil || result.Output != "second" {
		t.Fatalf("나중에 merge한 handler가 이겨야 해요: result=%#v err=%v", result, err)
	}
	handlers["same"] = nil
	_, freshHandlers := first.Parts()
	if freshHandlers["same"] == nil {
		t.Fatal("Parts는 handler map을 방어 복사해야 해요")
	}
}

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

func TestRunToolLoopLimitsParallelToolCalls(t *testing.T) {
	p := &manyToolProvider{count: 20}
	var active atomic.Int32
	var maxSeen atomic.Int32
	reg := ToolRegistry{
		"work": JSONToolHandler(func(ctx context.Context, in struct{}) (string, error) {
			now := active.Add(1)
			for {
				previous := maxSeen.Load()
				if now <= previous || maxSeen.CompareAndSwap(previous, now) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			active.Add(-1)
			return "ok", nil
		}),
	}
	if _, err := RunToolLoop(context.Background(), p, Request{Model: "test", Messages: []Message{UserText("go")}}, reg, ToolLoopOptions{MaxIterations: 2, ParallelToolCalls: true, MaxParallelToolCalls: 3}); err != nil {
		t.Fatal(err)
	}
	if maxSeen.Load() > 3 {
		t.Fatalf("parallel limit exceeded: %d", maxSeen.Load())
	}
}

type parallelProvider struct {
	calls      int
	sawOutputs string
}

type manyToolProvider struct {
	count int
	calls int
}

func (p *manyToolProvider) Name() string { return "many" }
func (p *manyToolProvider) Capabilities() Capabilities {
	return Capabilities{Tools: true, ParallelToolCalls: true}
}
func (p *manyToolProvider) Generate(ctx context.Context, req Request) (*Response, error) {
	p.calls++
	if p.calls > 1 {
		return TextResponse(p.Name(), req.Model, "done"), nil
	}
	calls := make([]ToolCall, 0, p.count)
	output := make([]Item, 0, p.count)
	for i := 0; i < p.count; i++ {
		call := ToolCall{CallID: fmt.Sprintf("call_%d", i), Name: "work", Arguments: json.RawMessage(`{}`)}
		calls = append(calls, call)
		output = append(output, Item{Type: ItemFunctionCall, ToolCall: &calls[len(calls)-1]})
	}
	return &Response{ID: "many_1", Output: output, ToolCalls: calls}, nil
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
