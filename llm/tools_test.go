package llm

import (
	"context"
	"encoding/json"
	"testing"
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
