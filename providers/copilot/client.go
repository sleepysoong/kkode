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
	ApproveAll       bool
}

type Client struct {
	cfg    Config
	client *ghcopilot.Client
	mu     sync.Mutex
}

func New(cfg Config) *Client { return &Client{cfg: cfg} }

func (c *Client) Name() string { return "github-copilot-sdk" }

func (c *Client) Capabilities() llm.Capabilities {
	return llm.Capabilities{
		Tools:              true,
		CustomTools:        true,
		Reasoning:          true,
		ReasoningSummaries: false,
		StructuredOutput:   false,
		Streaming:          true,
		ToolChoice:         false,
		ParallelToolCalls:  true,
		MCP:                true,
		Skills:             true,
		CustomAgents:       true,
	}
}

func (c *Client) Generate(ctx context.Context, req llm.Request) (*llm.Response, error) {
	client, err := c.ensureClient(ctx)
	if err != nil {
		return nil, err
	}
	var finalText strings.Builder
	session, err := client.CreateSession(ctx, &ghcopilot.SessionConfig{
		ClientName:       firstNonEmpty(c.cfg.ClientName, "kkode"),
		Model:            req.Model,
		ReasoningEffort:  reasoningEffort(req.Reasoning),
		WorkingDirectory: c.cfg.WorkingDirectory,
		Tools:            c.cfg.Tools,
		MCPServers:       c.cfg.MCPServers,
		CustomAgents:     c.cfg.CustomAgents,
		SkillDirectories: c.cfg.SkillDirectories,
		DisabledSkills:   c.cfg.DisabledSkills,
		OnPermissionRequest: func(request ghcopilot.PermissionRequest, invocation ghcopilot.PermissionInvocation) (ghcopilot.PermissionRequestResult, error) {
			if c.cfg.ApproveAll {
				return ghcopilot.PermissionRequestResult{Kind: ghcopilot.PermissionRequestResultKindApproved}, nil
			}
			return ghcopilot.PermissionRequestResult{Kind: ghcopilot.PermissionRequestResultKindUserNotAvailable}, nil
		},
		OnEvent: func(event ghcopilot.SessionEvent) {
			if d, ok := event.Data.(*ghcopilot.AssistantMessageData); ok {
				finalText.WriteString(d.Content)
			}
		},
	})
	if err != nil {
		return nil, err
	}
	defer session.Destroy()
	prompt := renderPrompt(req)
	if prompt == "" {
		return nil, fmt.Errorf("copilot provider requires at least one user message or input item")
	}
	event, err := session.SendAndWait(ctx, ghcopilot.MessageOptions{Prompt: prompt})
	if err != nil {
		return nil, err
	}
	if event != nil {
		if d, ok := event.Data.(*ghcopilot.AssistantMessageData); ok && finalText.Len() == 0 {
			finalText.WriteString(d.Content)
		}
	}
	return &llm.Response{
		Provider: c.Name(),
		Model:    req.Model,
		Status:   "completed",
		Text:     finalText.String(),
		Output: []llm.Item{{
			Type:    llm.ItemMessage,
			Role:    llm.RoleAssistant,
			Content: finalText.String(),
		}},
	}, nil
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
	var b strings.Builder
	if req.Instructions != "" {
		b.WriteString("Instructions:\n")
		b.WriteString(req.Instructions)
		b.WriteString("\n\n")
	}
	for _, msg := range req.Messages {
		if msg.Content == "" {
			continue
		}
		b.WriteString(strings.ToUpper(string(msg.Role)))
		b.WriteString(": ")
		b.WriteString(msg.Content)
		b.WriteString("\n")
	}
	for _, item := range req.InputItems {
		if item.Content != "" {
			b.WriteString(item.Content)
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
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
