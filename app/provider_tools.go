package app

import (
	"sort"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/providers/openai"
	ktools "github.com/sleepysoong/kkode/tools"
)

type MCPToolSurfaces struct {
	Hosted []llm.Tool
	Local  llm.ToolSet
}

func MCPToolsFromProviderOptions(opts ProviderOptions) MCPToolSurfaces {
	defs, handlers := ktools.MCPTools(opts.MCPServers)
	return MCPToolSurfaces{
		Hosted: openAICompatibleMCPTools(opts),
		Local:  llm.NewToolSet(defs, handlers),
	}
}

// openAICompatibleMCPTools는 HTTP MCP server만 OpenAI-compatible built-in MCP tool로 바꿔요.
// stdio MCP는 Copilot 같은 session provider config로 전달하고, OpenAI Responses에는 직접 붙이지 않아요.
func openAICompatibleMCPTools(opts ProviderOptions) []llm.Tool {
	if len(opts.MCPServers) == 0 {
		return nil
	}
	names := make([]string, 0, len(opts.MCPServers))
	for name := range opts.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	tools := make([]llm.Tool, 0, len(names))
	for _, name := range names {
		server := opts.MCPServers[name]
		if server.Kind != llm.MCPHTTP || server.URL == "" {
			continue
		}
		label := server.Name
		if label == "" {
			label = name
		}
		tools = append(tools, openai.MCPTool(label, server.URL, server.Headers))
	}
	return tools
}

// MergeBaseRequest는 provider assembly가 만든 기본 request와 caller option request를 합쳐요.
// slice 필드는 기본값 뒤에 명시값을 붙여서 사용자가 provider 기본 tool을 보존하면서 추가 설정을 넣을 수 있게 해요.
func MergeBaseRequest(defaults llm.Request, explicit llm.Request) llm.Request {
	out := explicit
	out.Tools = append(append([]llm.Tool{}, defaults.Tools...), explicit.Tools...)
	out.Messages = append(append([]llm.Message{}, defaults.Messages...), explicit.Messages...)
	out.InputItems = append(append([]llm.Item{}, defaults.InputItems...), explicit.InputItems...)
	out.Include = append(append([]string{}, defaults.Include...), explicit.Include...)
	if out.Instructions == "" {
		out.Instructions = defaults.Instructions
	}
	if out.Prompt == nil {
		out.Prompt = defaults.Prompt
	}
	if out.ToolChoice.Mode == "" {
		out.ToolChoice = defaults.ToolChoice
	}
	if out.Reasoning == nil {
		out.Reasoning = defaults.Reasoning
	}
	if out.TextFormat == nil {
		out.TextFormat = defaults.TextFormat
	}
	if out.MaxOutputTokens == 0 {
		out.MaxOutputTokens = defaults.MaxOutputTokens
	}
	if out.MaxToolCalls == 0 {
		out.MaxToolCalls = defaults.MaxToolCalls
	}
	if out.Temperature == nil {
		out.Temperature = defaults.Temperature
	}
	if out.TopP == nil {
		out.TopP = defaults.TopP
	}
	if out.Store == nil {
		out.Store = defaults.Store
	}
	if out.PreviousResponseID == "" {
		out.PreviousResponseID = defaults.PreviousResponseID
	}
	if len(defaults.Metadata) > 0 || len(explicit.Metadata) > 0 {
		out.Metadata = map[string]string{}
		for k, v := range defaults.Metadata {
			out.Metadata[k] = v
		}
		for k, v := range explicit.Metadata {
			out.Metadata[k] = v
		}
	}
	if out.ParallelToolCalls == nil {
		out.ParallelToolCalls = defaults.ParallelToolCalls
	}
	if out.SafetyIdentifier == "" {
		out.SafetyIdentifier = defaults.SafetyIdentifier
	}
	if out.PromptCacheKey == "" {
		out.PromptCacheKey = defaults.PromptCacheKey
	}
	return out
}
