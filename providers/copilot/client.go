package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	ghcopilot "github.com/github/copilot-sdk/go"
	"github.com/sleepysoong/kkode/llm"
)

type Config struct {
	CLIPath          string
	GitHubToken      string
	UseLoggedInUser  *bool
	WorkingDirectory string
	ClientName       string
	Tools            []ghcopilot.Tool
	MCPServers       map[string]ghcopilot.MCPServerConfig
	CustomAgents     []ghcopilot.CustomAgentConfig
	SkillDirectories []string
	DisabledSkills   []string
}

type Client struct {
	cfg    Config
	client *ghcopilot.Client
	mu     sync.Mutex
}

func New(cfg Config) *Client { return &Client{cfg: cfg} }

func (c *Client) Name() string { return "github-copilot-sdk" }

func (c *Client) Capabilities() llm.Capabilities { return DefaultCapabilities() }

// DefaultCapabilities는 GitHub Copilot SDK provider의 기능 계약이에요.
func DefaultCapabilities() llm.Capabilities {
	return llm.Capabilities{
		Tools:             true,
		CustomTools:       true,
		Reasoning:         true,
		Streaming:         true,
		ParallelToolCalls: true,
		MCP:               true,
		Skills:            true,
		CustomAgents:      true,
	}
}

func (c *Client) Generate(ctx context.Context, req llm.Request) (*llm.Response, error) {
	adapter := llm.AdaptedProvider{
		ProviderName:         c.Name(),
		ProviderCapabilities: c.Capabilities(),
		Converter:            SessionConverter{},
		Caller:               c,
		Options:              llm.ConvertOptions{Operation: sessionSendOperation},
	}
	return adapter.Generate(ctx, req)
}

func (c *Client) CallProvider(ctx context.Context, req llm.ProviderRequest) (llm.ProviderResult, error) {
	if req.Operation != "" && req.Operation != sessionSendOperation {
		return llm.ProviderResult{}, fmt.Errorf("지원하지 않는 Copilot SDK operation이에요: %s", req.Operation)
	}
	payload, ok := req.Raw.(sessionSendPayload)
	if !ok {
		return llm.ProviderResult{}, fmt.Errorf("copilot session payload가 필요해요")
	}
	sess, err := c.NewSession(ctx, llm.SessionRequest{Model: req.Model, WorkingDirectory: c.cfg.WorkingDirectory, Reasoning: payload.Request.Reasoning})
	if err != nil {
		return llm.ProviderResult{}, err
	}
	defer sess.Close()
	if concrete, ok := sess.(*Session); ok {
		resp, err := concrete.sendPrompt(ctx, payload.Request, payload.Prompt)
		if err != nil {
			return llm.ProviderResult{}, err
		}
		return llm.ProviderResult{Provider: c.Name(), Model: req.Model, Raw: resp}, nil
	}
	resp, err := sess.Send(ctx, payload.Request)
	if err != nil {
		return llm.ProviderResult{}, err
	}
	return llm.ProviderResult{Provider: c.Name(), Model: req.Model, Raw: resp}, nil
}

func (c *Client) ensureClient(ctx context.Context) (*ghcopilot.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		return c.client, nil
	}
	opts := &ghcopilot.ClientOptions{
		CLIPath:         c.cfg.CLIPath,
		GitHubToken:     c.cfg.GitHubToken,
		UseLoggedInUser: c.cfg.UseLoggedInUser,
		LogLevel:        "error",
	}
	client := ghcopilot.NewClient(opts)
	if err := client.Start(ctx); err != nil {
		return nil, err
	}
	c.client = client
	return c.client, nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return nil
	}
	err := c.client.Stop()
	c.client = nil
	return err
}

func ToCopilotTool(tool llm.Tool, handler llm.ToolHandler) ghcopilot.Tool {
	return ghcopilot.Tool{
		Name:        tool.Name,
		Description: tool.Description,
		Parameters:  tool.Parameters,
		Handler: func(invocation ghcopilot.ToolInvocation) (ghcopilot.ToolResult, error) {
			args, err := json.Marshal(invocation.Arguments)
			if err != nil {
				return ghcopilot.ToolResult{ResultType: "error", Error: err.Error()}, err
			}
			result, err := handler(invocation.TraceContext, llm.ToolCall{
				CallID:    invocation.ToolCallID,
				Name:      invocation.ToolName,
				Arguments: args,
			})
			if err != nil {
				return ghcopilot.ToolResult{ResultType: "error", Error: err.Error(), TextResultForLLM: result.Error}, err
			}
			if result.Error != "" {
				return ghcopilot.ToolResult{ResultType: "error", Error: result.Error, TextResultForLLM: result.Error}, nil
			}
			return ghcopilot.ToolResult{ResultType: "text", TextResultForLLM: result.Output}, nil
		},
	}
}

func renderPrompt(req llm.Request) string {
	return llm.RenderTranscriptPrompt(req, llm.TranscriptPromptOptions{InstructionHeader: "Instructions:"})
}

func reasoningEffort(r *llm.ReasoningConfig) string {
	if r == nil {
		return ""
	}
	return r.Effort
}

func firstNonEmpty(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}

func (c *Client) NewSession(ctx context.Context, req llm.SessionRequest) (llm.Session, error) {
	client, err := c.ensureClient(ctx)
	if err != nil {
		return nil, err
	}
	config := c.sessionConfig(req)
	session, err := client.CreateSession(ctx, config)
	if err != nil {
		return nil, err
	}
	return &Session{client: c, session: session, model: req.Model}, nil
}

func (c *Client) Stream(ctx context.Context, req llm.Request) (llm.EventStream, error) {
	sess, err := c.NewSession(ctx, llm.SessionRequest{Model: req.Model, WorkingDirectory: c.cfg.WorkingDirectory, Reasoning: req.Reasoning})
	if err != nil {
		return nil, err
	}
	return sess.Stream(ctx, req)
}

type Session struct {
	client  *Client
	session *ghcopilot.Session
	model   string
}

func (s *Session) ID() string { return s.session.SessionID }

func (s *Session) Send(ctx context.Context, req llm.Request) (*llm.Response, error) {
	return s.sendPrompt(ctx, req, renderPrompt(req))
}

func (s *Session) sendPrompt(ctx context.Context, req llm.Request, prompt string) (*llm.Response, error) {
	var finalText strings.Builder
	unsubscribe := s.session.On(func(event ghcopilot.SessionEvent) {
		if d, ok := event.Data.(*ghcopilot.AssistantMessageData); ok {
			finalText.WriteString(d.Content)
		}
	})
	defer unsubscribe()
	event, err := s.session.SendAndWait(ctx, ghcopilot.MessageOptions{Prompt: prompt})
	if err != nil {
		return nil, err
	}
	if finalText.Len() == 0 && event != nil {
		if d, ok := event.Data.(*ghcopilot.AssistantMessageData); ok {
			finalText.WriteString(d.Content)
		}
	}
	return llm.TextResponse(s.client.Name(), firstNonEmpty(req.Model, s.model), finalText.String()), nil
}

func (s *Session) Stream(ctx context.Context, req llm.Request) (llm.EventStream, error) {
	events := make(chan llm.StreamEvent, 64)
	var closeOnce sync.Once
	closeEvents := func() { closeOnce.Do(func() { close(events) }) }
	unsubscribe := s.session.On(func(event ghcopilot.SessionEvent) {
		ev := copilotEventToStream(event, s.client.Name(), firstNonEmpty(req.Model, s.model))
		select {
		case <-ctx.Done():
		case events <- ev:
		}
		if event.Type == ghcopilot.SessionEventTypeSessionIdle || event.Type == ghcopilot.SessionEventTypeSessionError {
			closeEvents()
		}
	})
	_, err := s.session.Send(ctx, ghcopilot.MessageOptions{Prompt: renderPrompt(req)})
	if err != nil {
		unsubscribe()
		closeEvents()
		return nil, err
	}
	return llm.NewChannelStream(ctx, events, closeFunc(func() error { unsubscribe(); closeEvents(); return nil })), nil
}

func (s *Session) Close() error { return s.session.Disconnect() }

type closeFunc func() error

func (f closeFunc) Close() error { return f() }

func (c *Client) sessionConfig(req llm.SessionRequest) *ghcopilot.SessionConfig {
	cfg := &ghcopilot.SessionConfig{
		ClientName:          firstNonEmpty(c.cfg.ClientName, "kkode"),
		Model:               req.Model,
		ReasoningEffort:     reasoningEffort(req.Reasoning),
		WorkingDirectory:    firstNonEmpty(req.WorkingDirectory, c.cfg.WorkingDirectory),
		Tools:               c.cfg.Tools,
		MCPServers:          map[string]ghcopilot.MCPServerConfig{},
		CustomAgents:        c.cfg.CustomAgents,
		SkillDirectories:    append([]string{}, c.cfg.SkillDirectories...),
		DisabledSkills:      append([]string{}, c.cfg.DisabledSkills...),
		OnPermissionRequest: approvePermissionHandler(),
	}
	if req.Instructions != "" {
		cfg.SystemMessage = &ghcopilot.SystemMessageConfig{Mode: "append", Content: req.Instructions}
	}
	for name, server := range c.cfg.MCPServers {
		cfg.MCPServers[name] = server
	}
	for name, server := range req.MCPServers {
		cfg.MCPServers[name] = ToCopilotMCPServer(server)
	}
	for _, tool := range req.Tools {
		cfg.Tools = append(cfg.Tools, ghcopilot.Tool{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters})
	}
	for _, agent := range req.CustomAgents {
		cfg.CustomAgents = append(cfg.CustomAgents, ToCopilotAgent(agent))
	}
	cfg.SkillDirectories = append(cfg.SkillDirectories, req.Skills...)
	return cfg
}

func approvePermissionHandler() ghcopilot.PermissionHandlerFunc {
	return func(request ghcopilot.PermissionRequest, invocation ghcopilot.PermissionInvocation) (ghcopilot.PermissionRequestResult, error) {
		return ghcopilot.PermissionRequestResult{Kind: ghcopilot.PermissionRequestResultKindApproved}, nil
	}
}

func ToCopilotMCPServer(server llm.MCPServer) ghcopilot.MCPServerConfig {
	if server.Kind == llm.MCPHTTP {
		return ghcopilot.MCPHTTPServerConfig{Tools: server.Tools, Timeout: server.Timeout, URL: server.URL, Headers: server.Headers}
	}
	return ghcopilot.MCPStdioServerConfig{Tools: server.Tools, Timeout: server.Timeout, Command: server.Command, Args: server.Args, Env: server.Env, Cwd: server.Cwd}
}

func ToCopilotAgent(agent llm.Agent) ghcopilot.CustomAgentConfig {
	servers := map[string]ghcopilot.MCPServerConfig{}
	for name, server := range agent.MCPServers {
		servers[name] = ToCopilotMCPServer(server)
	}
	return ghcopilot.CustomAgentConfig{Name: agent.Name, DisplayName: agent.DisplayName, Description: agent.Description, Tools: agent.Tools, Prompt: agent.Prompt, MCPServers: servers, Infer: agent.Infer, Skills: agent.Skills}
}

func copilotEventToStream(event ghcopilot.SessionEvent, provider, model string) llm.StreamEvent {
	ev := llm.StreamEvent{Type: llm.StreamEventUnknown, Provider: provider, EventName: string(event.Type)}
	if raw, err := event.Marshal(); err == nil {
		ev.Raw = raw
	}
	switch event.Type {
	case ghcopilot.SessionEventTypeAssistantMessage, ghcopilot.SessionEventTypeAssistantMessageDelta, ghcopilot.SessionEventTypeAssistantStreamingDelta:
		ev.Type = llm.StreamEventTextDelta
		if d, ok := event.Data.(*ghcopilot.AssistantMessageData); ok {
			ev.Delta = d.Content
		}
	case ghcopilot.SessionEventTypeAssistantReasoning, ghcopilot.SessionEventTypeAssistantReasoningDelta:
		ev.Type = llm.StreamEventReasoningDelta
	case ghcopilot.SessionEventTypeToolExecutionStart, ghcopilot.SessionEventTypeToolUserRequested:
		ev.Type = llm.StreamEventToolCall
	case ghcopilot.SessionEventTypeToolExecutionComplete:
		ev.Type = llm.StreamEventToolResult
	case ghcopilot.SessionEventTypeSessionStart, ghcopilot.SessionEventTypeAssistantTurnStart:
		ev.Type = llm.StreamEventStarted
	case ghcopilot.SessionEventTypeSessionIdle, ghcopilot.SessionEventTypeAssistantTurnEnd:
		ev.Type = llm.StreamEventCompleted
		ev.Response = &llm.Response{Provider: provider, Model: model, Status: "completed"}
	case ghcopilot.SessionEventTypeSessionError:
		ev.Type = llm.StreamEventError
	}
	return ev
}

func MCPServerConfigs(servers map[string]llm.MCPServer) map[string]ghcopilot.MCPServerConfig {
	if len(servers) == 0 {
		return nil
	}
	out := make(map[string]ghcopilot.MCPServerConfig, len(servers))
	for name, server := range servers {
		out[name] = ToCopilotMCPServer(server)
	}
	return out
}

func AgentConfigs(agents []llm.Agent) []ghcopilot.CustomAgentConfig {
	if len(agents) == 0 {
		return nil
	}
	out := make([]ghcopilot.CustomAgentConfig, 0, len(agents))
	for _, agent := range agents {
		out = append(out, ToCopilotAgent(agent))
	}
	return out
}
