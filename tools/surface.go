package tools

import (
	"net/http"
	"time"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/workspace"
)

// SurfaceOptions는 agent, gateway, adapter가 같은 표준 tool 묶음을 만들 때 쓰는 옵션이에요.
type SurfaceOptions struct {
	Workspace   *workspace.Workspace
	NoWeb       bool
	WebMaxBytes int64
	HTTPClient  *http.Client
	UserAgent   string
	Timeout     time.Duration
	Enabled     []string
	Disabled    []string
	MCPServers  map[string]llm.MCPServer
}

// StandardToolSet은 file/shell/web/codeintel 표준 tool surface를 한 묶음으로 조립해요.
// 권한이나 승인 단계는 여기 없고, 연결된 workspace와 HTTP 설정으로 바로 실행해요.
func StandardToolSet(opts SurfaceOptions) llm.ToolSet {
	defs, handlers := FileTools(opts.Workspace)
	set := llm.NewToolSet(defs, handlers)
	codeIntelDefs, codeIntelHandlers := CodeIntelTools(opts.Workspace)
	set.Merge(llm.NewToolSet(codeIntelDefs, codeIntelHandlers))
	mcpDefs, mcpHandlers := MCPTools(opts.MCPServers)
	set.Merge(llm.NewToolSet(mcpDefs, mcpHandlers))
	if opts.NoWeb {
		return filterToolSet(set, opts.Enabled, opts.Disabled)
	}
	webDefs, webHandlers := WebTools(WebConfig{HTTPClient: opts.HTTPClient, UserAgent: opts.UserAgent, MaxBytes: opts.WebMaxBytes, Timeout: opts.Timeout})
	set.Merge(llm.NewToolSet(webDefs, webHandlers))
	return filterToolSet(set, opts.Enabled, opts.Disabled)
}

// StandardTools는 기존 caller가 정의/handler를 따로 받을 수 있게 유지하는 wrapper예요.
func StandardTools(opts SurfaceOptions) ([]llm.Tool, llm.ToolRegistry) {
	return StandardToolSet(opts).Parts()
}

func filterToolSet(set llm.ToolSet, enabled []string, disabled []string) llm.ToolSet {
	enabledSet := toolNameSet(enabled)
	disabledSet := toolNameSet(disabled)
	if len(enabledSet) == 0 && len(disabledSet) == 0 {
		return set
	}
	out := llm.ToolSet{Handlers: llm.ToolRegistry{}}
	for _, def := range set.Definitions {
		if len(enabledSet) > 0 && !enabledSet[def.Name] {
			continue
		}
		if disabledSet[def.Name] {
			continue
		}
		out.Definitions = append(out.Definitions, def)
		if handler, ok := set.Handlers[def.Name]; ok {
			out.Handlers[def.Name] = handler
		}
	}
	if len(out.Handlers) == 0 {
		out.Handlers = nil
	}
	return out
}

func toolNameSet(names []string) map[string]bool {
	out := map[string]bool{}
	for _, name := range names {
		if name != "" {
			out[name] = true
		}
	}
	return out
}
