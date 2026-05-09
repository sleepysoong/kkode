package copilot

import (
	"context"
	"encoding/json"
	"testing"

	ghcopilot "github.com/github/copilot-sdk/go"
	kagent "github.com/sleepysoong/kkode/agent"
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

func TestSessionConverterBuildsProviderRequest(t *testing.T) {
	preq, err := SessionConverter{}.ConvertRequest(context.Background(), llm.Request{Model: "gpt-5-mini", Messages: []llm.Message{llm.UserText("hello")}}, llm.ConvertOptions{})
	if err != nil {
		t.Fatal(err)
	}
	payload := preq.Raw.(sessionSendPayload)
	if preq.Operation != sessionSendOperation || preq.Model != "gpt-5-mini" || payload.Prompt != "USER: hello" {
		t.Fatalf("Copilot provider request가 이상해요: %+v payload=%+v", preq, payload)
	}
}

func TestSessionConverterMapsResponse(t *testing.T) {
	want := llm.TextResponse("copilot", "gpt-5-mini", "ok")
	resp, err := SessionConverter{}.ConvertResponse(context.Background(), llm.ProviderResult{Provider: "copilot", Model: "gpt-5-mini", Raw: want})
	if err != nil {
		t.Fatal(err)
	}
	if resp != want {
		t.Fatalf("이미 표준 응답이면 그대로 돌려줘야 해요")
	}
	resp, err = SessionConverter{}.ConvertResponse(context.Background(), llm.ProviderResult{Provider: "copilot", Model: "gpt-5-mini", Raw: "text"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "copilot" || resp.Model != "gpt-5-mini" || resp.Text != "text" {
		t.Fatalf("문자열 응답 변환이 이상해요: %+v", resp)
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

func TestAgentFromConfigBuildsCopilotCustomAgent(t *testing.T) {
	infer := true
	cfg := kagent.Config{
		Name:          "planner",
		Instructions:  "plan carefully",
		ContextBlocks: []string{"skill context", "  ", "subagent context"},
		ToolSet:       llm.NewToolSet([]llm.Tool{{Name: "file_read"}, {Name: "shell_run"}}, nil),
		Tools:         []llm.Tool{{Name: "file_read"}, {Name: "lsp_symbols"}},
	}
	agent := AgentFromConfig(cfg, AgentConfigOptions{
		DisplayName: "Planner",
		Description: "Plans repo edits",
		Skills:      []string{"review", "", "test"},
		Infer:       &infer,
		MCPServers: map[string]llm.MCPServer{
			"context7": {Kind: llm.MCPHTTP, URL: "https://mcp.context7.com/mcp", Tools: []string{"resolve-library-id"}},
		},
	})
	if agent.Name != "planner" || agent.DisplayName != "Planner" || agent.Description != "Plans repo edits" || agent.Infer == nil || !*agent.Infer {
		t.Fatalf("agent identity가 이상해요: %+v", agent)
	}
	if agent.Prompt != "plan carefully\n\nskill context\n\n---\n\nsubagent context" {
		t.Fatalf("prompt가 이상해요: %q", agent.Prompt)
	}
	if len(agent.Tools) != 3 || agent.Tools[0] != "file_read" || agent.Tools[1] != "shell_run" || agent.Tools[2] != "lsp_symbols" {
		t.Fatalf("tool 이름은 순서 보존 + 중복 제거가 필요해요: %+v", agent.Tools)
	}
	if len(agent.Skills) != 2 || agent.Skills[0] != "review" || agent.Skills[1] != "test" {
		t.Fatalf("skill 목록 정리가 이상해요: %+v", agent.Skills)
	}
	copilotAgent := CustomAgentConfigFromAgentConfig(cfg, AgentConfigOptions{MCPServers: agent.MCPServers})
	if copilotAgent.Name != "planner" || len(copilotAgent.MCPServers) != 1 {
		t.Fatalf("Copilot custom agent 변환이 이상해요: %+v", copilotAgent)
	}
}

func TestCopilotEventToStream(t *testing.T) {
	ev := copilotEventToStream(ghcopilot.SessionEvent{Type: ghcopilot.SessionEventTypeAssistantMessage, Data: &ghcopilot.AssistantMessageData{Content: "hi"}}, "copilot", "m")
	if ev.Type != llm.StreamEventTextDelta || ev.Delta != "hi" {
		t.Fatalf("event=%#v", ev)
	}
}

func TestCopilotStreamProviderValidatesConvertedRequest(t *testing.T) {
	client := New(Config{})
	if _, err := client.StreamProvider(context.Background(), llm.ProviderRequest{Operation: "other"}); err == nil {
		t.Fatal("지원하지 않는 stream operation은 거부해야 해요")
	}
	if _, err := client.StreamProvider(context.Background(), llm.ProviderRequest{Operation: sessionSendOperation}); err == nil {
		t.Fatal("변환된 session stream payload가 없으면 거부해야 해요")
	}
}

func TestLimitedTextBufferKeepsBoundedOutput(t *testing.T) {
	buf := newLimitedTextBuffer(4)
	buf.WriteString("가나다")
	got := buf.String()
	if got != "가\n[output truncated]" {
		t.Fatalf("Copilot response text should be UTF-8 bounded and marked truncated: %q", got)
	}
}
