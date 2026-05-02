package gateway

import (
	"encoding/json"
	"net/http"

	"github.com/sleepysoong/kkode/session"
)

type SubagentPreviewResponse struct {
	Subagent    ResourceDTO       `json:"subagent"`
	Name        string            `json:"name"`
	DisplayName string            `json:"display_name,omitempty"`
	Description string            `json:"description,omitempty"`
	Prompt      string            `json:"prompt,omitempty"`
	Tools       []string          `json:"tools,omitempty"`
	MCPServers  map[string]string `json:"mcp_servers,omitempty"`
	Skills      []string          `json:"skills,omitempty"`
	Infer       *bool             `json:"infer,omitempty"`
}

type subagentPreviewConfig struct {
	DisplayName string            `json:"display_name"`
	Description string            `json:"description"`
	Prompt      string            `json:"prompt"`
	Tools       []string          `json:"tools"`
	MCPServers  map[string]string `json:"mcp_servers"`
	Skills      []string          `json:"skills"`
	Infer       *bool             `json:"infer"`
}

func (s *Server) previewSubagent(w http.ResponseWriter, r *http.Request, subagentID string) {
	store := s.resourceStore()
	if store == nil {
		writeError(w, r, http.StatusNotImplemented, "resource_store_missing", "이 gateway에는 resource store가 연결되지 않았어요")
		return
	}
	resource, err := store.LoadResource(r.Context(), session.ResourceSubagent, subagentID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "resource_not_found", err.Error())
		return
	}
	preview, err := subagentPreviewFromResource(resource)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "subagent_preview_failed", err.Error())
		return
	}
	writeJSON(w, preview)
}

func subagentPreviewFromResource(resource session.Resource) (SubagentPreviewResponse, error) {
	var cfg subagentPreviewConfig
	if len(resource.Config) > 0 {
		if err := json.Unmarshal(resource.Config, &cfg); err != nil {
			return SubagentPreviewResponse{}, err
		}
	}
	return SubagentPreviewResponse{Subagent: toResourceDTO(resource), Name: resource.ID, DisplayName: firstNonEmptyString(cfg.DisplayName, resource.Name), Description: firstNonEmptyString(cfg.Description, resource.Description), Prompt: cfg.Prompt, Tools: cfg.Tools, MCPServers: cfg.MCPServers, Skills: cfg.Skills, Infer: cfg.Infer}, nil
}
