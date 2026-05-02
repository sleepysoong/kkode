package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/sleepysoong/kkode/llm"
)

func TestBuildResponsesRequestIncludesToolsReasoningAndStructuredOutput(t *testing.T) {
	strict := true
	req := llm.Request{
		Model:        "gpt-5-mini",
		Instructions: "You are a coding assistant.",
		Messages:     []llm.Message{llm.UserText("inspect repo")},
		Tools: []llm.Tool{{
			Kind:        llm.ToolFunction,
			Name:        "read_file",
			Description: "Read a file",
			Strict:      &strict,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
				"required":             []any{"path"},
				"additionalProperties": false,
			},
		}},
		ToolChoice: llm.ToolChoice{Mode: llm.ToolChoiceFunction, Name: "read_file"},
		Reasoning:  &llm.ReasoningConfig{Effort: "medium", Summary: "auto"},
		TextFormat: &llm.TextFormat{Type: "json_schema", Name: "plan", Strict: true, Schema: map[string]any{"type": "object"}},
		Store:      llm.Bool(false),
	}
	body, err := BuildResponsesRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if body["model"] != "gpt-5-mini" {
		t.Fatalf("model not mapped: %#v", body["model"])
	}
	if body["store"] != false {
		t.Fatalf("store not mapped: %#v", body["store"])
	}
	if got := body["reasoning"].(map[string]any)["effort"]; got != "medium" {
		t.Fatalf("reasoning effort = %#v", got)
	}
	if got := body["tool_choice"].(map[string]any)["name"]; got != "read_file" {
		t.Fatalf("tool_choice name = %#v", got)
	}
	tools := body["tools"].([]any)
	if got := tools[0].(map[string]any)["strict"]; got != true {
		t.Fatalf("strict = %#v", got)
	}
	if _, err := json.Marshal(body); err != nil {
		t.Fatalf("request body is not json: %v", err)
	}
}

func TestParseResponsesResponsePreservesReasoningAndToolCalls(t *testing.T) {
	data := []byte(`{
		"id":"resp_1",
		"model":"gpt-5-mini",
		"status":"requires_action",
		"output":[
			{"type":"reasoning","id":"rs_1","summary":[{"text":"Need file"}]},
			{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"README.md\"}"},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done"}]}
		],
		"usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8,"output_tokens_details":{"reasoning_tokens":2}}
	}`)
	resp, err := ParseResponsesResponse(data, "test")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "Done" || resp.ID != "resp_1" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "read_file" {
		t.Fatalf("tool calls not parsed: %#v", resp.ToolCalls)
	}
	if string(resp.ToolCalls[0].Arguments) != `{"path":"README.md"}` {
		t.Fatalf("arguments not normalized: %s", resp.ToolCalls[0].Arguments)
	}
	if len(resp.Reasoning) != 1 || resp.Reasoning[0].Summary[0] != "Need file" {
		t.Fatalf("reasoning not parsed: %#v", resp.Reasoning)
	}
	if resp.Usage.ReasoningTokens != 2 {
		t.Fatalf("reasoning tokens = %d", resp.Usage.ReasoningTokens)
	}
}

func TestBuildResponsesRequestMapsCustomToolOutput(t *testing.T) {
	body, err := BuildResponsesRequest(llm.Request{
		Model: "gpt-5-mini",
		InputItems: []llm.Item{{
			Type:       llm.ItemCustomToolOutput,
			ToolResult: &llm.ToolResult{CallID: "call_custom", Output: "custom ok", Custom: true},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := body["input"].([]any)
	item := input[0].(map[string]any)
	if item["type"] != "custom_tool_call_output" {
		t.Fatalf("custom output type = %#v", item["type"])
	}
}

func TestClientGenerateAgainstHTTPServer(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_2","model":"gpt-5-mini","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer server.Close()
	client := New(Config{BaseURL: server.URL + "/v1", APIKey: "sk-test"})
	resp, err := client.Generate(context.Background(), llm.Request{Model: "gpt-5-mini", Messages: []llm.Message{llm.UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	if gotBody["model"] != "gpt-5-mini" || resp.Text != "ok" {
		t.Fatalf("unexpected body/response: %#v %#v", gotBody, resp)
	}
}

func TestBuildResponsesRequestBuiltinTool(t *testing.T) {
	body, err := BuildResponsesRequest(llm.Request{Model: "gpt-5-mini", Messages: []llm.Message{llm.UserText("search")}, Tools: []llm.Tool{WebSearchTool(map[string]any{"search_context_size": "low"})}})
	if err != nil {
		t.Fatal(err)
	}
	tool := body["tools"].([]any)[0].(map[string]any)
	if tool["type"] != "web_search_preview" || tool["search_context_size"] != "low" {
		t.Fatalf("unexpected builtin tool: %#v", tool)
	}
}

func TestStreamParsesSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"he\"}\n\n"))
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"model\":\"gpt\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}]}}\n\n"))
	}))
	defer server.Close()
	client := New(Config{BaseURL: server.URL + "/v1"})
	stream, err := client.Stream(context.Background(), llm.Request{Model: "gpt", Messages: []llm.Message{llm.UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	ev, err := stream.Recv()
	if err != nil || ev.Type != llm.StreamEventTextDelta || ev.Delta != "he" {
		t.Fatalf("ev=%#v err=%v", ev, err)
	}
	ev, err = stream.Recv()
	if err != nil || ev.Type != llm.StreamEventCompleted || ev.Response.Text != "hello" {
		t.Fatalf("ev=%#v err=%v", ev, err)
	}
}

func TestStreamRetriesRetryableStatusAndUsesProviderName(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "try again", http.StatusTooManyRequests)
			return
		}
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Fatalf("stream accept header가 필요해요: %q", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"))
	}))
	defer server.Close()
	client := New(Config{BaseURL: server.URL + "/v1", Retry: RetryConfig{MaxRetries: 1}, ProviderName: "derived"})
	stream, err := client.Stream(context.Background(), llm.Request{Model: "gpt", Messages: []llm.Message{llm.UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	ev, err := stream.Recv()
	if err != nil || ev.Type != llm.StreamEventTextDelta || ev.Provider != "derived" || calls != 2 {
		t.Fatalf("ev=%#v calls=%d err=%v", ev, calls, err)
	}
}

func TestClientRetriesRetryableStatus(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "try again", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"id":"r","model":"gpt","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer server.Close()
	client := New(Config{BaseURL: server.URL + "/v1", Retry: RetryConfig{MaxRetries: 1}})
	resp, err := client.Generate(context.Background(), llm.Request{Model: "gpt", Messages: []llm.Message{llm.UserText("hi")}})
	if err != nil || resp.Text != "ok" || calls != 2 {
		t.Fatalf("resp=%#v calls=%d err=%v", resp, calls, err)
	}
}

func TestOpenAILiveIfConfigured(t *testing.T) {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		t.Skip("OPENAI_API_KEY not set")
	}
	client := New(Config{APIKey: key})
	resp, err := client.Generate(context.Background(), llm.Request{Model: firstEnv("OPENAI_TEST_MODEL", "gpt-5-mini"), Messages: []llm.Message{llm.UserText("Reply with exactly OK")}, MaxOutputTokens: 16, Store: llm.Bool(false)})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text == "" {
		t.Fatalf("empty live response: %#v", resp)
	}
}

func firstEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
