package copilot

import (
	"strings"

	ghcopilot "github.com/github/copilot-sdk/go"
	kagent "github.com/sleepysoong/kkode/agent"
	"github.com/sleepysoong/kkode/llm"
)

type AgentConfigOptions struct {
	Name        string
	DisplayName string
	Description string
	Skills      []string
	MCPServers  map[string]llm.MCPServer
	Infer       *bool
}

func AgentFromConfig(cfg kagent.Config, opts AgentConfigOptions) llm.Agent {
	name := firstNonEmpty(strings.TrimSpace(opts.Name), strings.TrimSpace(cfg.Name))
	if name == "" {
		name = "kkode-agent"
	}
	prompt := strings.TrimSpace(firstNonEmpty(cfg.Instructions, cfg.BaseRequest.Instructions))
	if len(cfg.ContextBlocks) > 0 {
		blocks := make([]string, 0, len(cfg.ContextBlocks))
		for _, block := range cfg.ContextBlocks {
			if text := strings.TrimSpace(block); text != "" {
				blocks = append(blocks, text)
			}
		}
		if len(blocks) > 0 {
			if prompt != "" {
				prompt += "\n\n"
			}
			prompt += strings.Join(blocks, "\n\n---\n\n")
		}
	}
	return llm.Agent{
		Name:        name,
		DisplayName: strings.TrimSpace(opts.DisplayName),
		Description: strings.TrimSpace(opts.Description),
		Prompt:      prompt,
		Tools:       agentToolNames(cfg),
		MCPServers:  cloneMCPServers(opts.MCPServers),
		Infer:       opts.Infer,
		Skills:      cloneNonEmptyStrings(opts.Skills),
	}
}

func CustomAgentConfigFromAgentConfig(cfg kagent.Config, opts AgentConfigOptions) ghcopilot.CustomAgentConfig {
	return ToCopilotAgent(AgentFromConfig(cfg, opts))
}

func agentToolNames(cfg kagent.Config) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}
	for _, tool := range cfg.ToolSet.Definitions {
		add(tool.Name)
	}
	for _, tool := range cfg.Tools {
		add(tool.Name)
	}
	return out
}

func cloneMCPServers(servers map[string]llm.MCPServer) map[string]llm.MCPServer {
	if len(servers) == 0 {
		return nil
	}
	out := make(map[string]llm.MCPServer, len(servers))
	for name, server := range servers {
		server.Tools = cloneNonEmptyStrings(server.Tools)
		server.Args = append([]string{}, server.Args...)
		server.Env = cloneStringMap(server.Env)
		server.Headers = cloneStringMap(server.Headers)
		out[name] = server
	}
	return out
}

func cloneNonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if text := strings.TrimSpace(value); text != "" {
			out = append(out, text)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
