package gateway

import (
	"encoding/json"
	"net/http"

	"github.com/sleepysoong/kkode/session"
)

type SubagentPreviewResponse struct {
	Subagent        ResourceDTO    `json:"subagent"`
	Name            string         `json:"name"`
	DisplayName     string         `json:"display_name,omitempty"`
	Description     string         `json:"description,omitempty"`
	Prompt          string         `json:"prompt,omitempty"`
	PromptBytes     int            `json:"prompt_bytes,omitempty"`
	PromptTruncated bool           `json:"prompt_truncated,omitempty"`
	Tools           []string       `json:"tools,omitempty"`
	MCPServers      map[string]any `json:"mcp_servers,omitempty"`
	MCPServerIDs    []string       `json:"mcp_server_ids,omitempty"`
	Skills          []string       `json:"skills,omitempty"`
	Infer           *bool          `json:"infer,omitempty"`
}

const (
	defaultSubagentPreviewPromptBytes = 65536
	maxSubagentPreviewPromptBytes     = 1 << 20
)

type subagentPreviewConfig struct {
	DisplayName  string         `json:"display_name"`
	Description  string         `json:"description"`
	Prompt       string         `json:"prompt"`
	Tools        []string       `json:"tools"`
	MCPServers   map[string]any `json:"mcp_servers"`
	MCPServerIDs []string       `json:"mcp_server_ids"`
	Skills       []string       `json:"skills"`
	Infer        *bool          `json:"infer"`
}

func (s *Server) previewSubagent(w http.ResponseWriter, r *http.Request, subagentID string) {
	maxPromptBytes, ok := queryNonNegativeLimitParam(w, r, "max_prompt_bytes", defaultSubagentPreviewPromptBytes, maxSubagentPreviewPromptBytes, "invalid_subagent_preview")
	if !ok {
		return
	}
	s.withResource(w, r, session.ResourceSubagent, subagentID, func(resource session.Resource) {
		preview, err := subagentPreviewFromResource(resource, maxPromptBytes)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "subagent_preview_failed", err.Error())
			return
		}
		writeJSON(w, preview)
	})
}

func subagentPreviewFromResource(resource session.Resource, maxPromptBytes int) (SubagentPreviewResponse, error) {
	var cfg subagentPreviewConfig
	if len(resource.Config) > 0 {
		if err := json.Unmarshal(resource.Config, &cfg); err != nil {
			return SubagentPreviewResponse{}, err
		}
	}
	prompt, promptBytes, promptTruncated := truncateToolOutput(cfg.Prompt, maxPromptBytes)
	return SubagentPreviewResponse{Subagent: publicResourceDTO(resource), Name: resource.ID, DisplayName: firstNonEmptyString(cfg.DisplayName, resource.Name), Description: firstNonEmptyString(cfg.Description, resource.Description), Prompt: prompt, PromptBytes: promptBytes, PromptTruncated: promptTruncated, Tools: cfg.Tools, MCPServers: cfg.MCPServers, MCPServerIDs: cfg.MCPServerIDs, Skills: cfg.Skills, Infer: cfg.Infer}, nil
}
