package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/session"
)

// ResourceDTO는 MCP server, skill, subagent를 외부 API에 노출하는 공통 manifest예요.
type ResourceDTO struct {
	ID          string         `json:"id,omitempty"`
	Kind        string         `json:"kind,omitempty"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Enabled     *bool          `json:"enabled,omitempty"`
	Config      map[string]any `json:"config,omitempty"`
	CreatedAt   string         `json:"created_at,omitempty"`
	UpdatedAt   string         `json:"updated_at,omitempty"`
}

type ResourceListResponse struct {
	Resources       []ResourceDTO `json:"resources"`
	Limit           int           `json:"limit,omitempty"`
	ResultTruncated bool          `json:"result_truncated,omitempty"`
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) < 2 || parts[1] != "servers" {
		writeError(w, r, http.StatusNotFound, "not_found", "mcp endpoint를 찾을 수 없어요")
		return
	}
	if len(parts) == 4 && parts[3] == "tools" && r.Method == http.MethodGet {
		s.listMCPServerTools(w, r, parts[2])
		return
	}
	if len(parts) == 4 && parts[3] == "resources" && r.Method == http.MethodGet {
		s.listMCPServerResources(w, r, parts[2])
		return
	}
	if len(parts) == 5 && parts[3] == "resources" && parts[4] == "read" && r.Method == http.MethodGet {
		s.readMCPServerResource(w, r, parts[2])
		return
	}
	if len(parts) == 4 && parts[3] == "prompts" && r.Method == http.MethodGet {
		s.listMCPServerPrompts(w, r, parts[2])
		return
	}
	if len(parts) == 6 && parts[3] == "prompts" && parts[5] == "get" && r.Method == http.MethodPost {
		s.getMCPServerPrompt(w, r, parts[2], parts[4])
		return
	}
	if len(parts) == 6 && parts[3] == "tools" && parts[5] == "call" && r.Method == http.MethodPost {
		s.callMCPServerTool(w, r, parts[2], parts[4])
		return
	}
	s.handleResources(w, r, parts[2:], resourceRoute{Kind: session.ResourceMCPServer, Name: "mcp server"})
}

func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 3 && parts[2] == "preview" && r.Method == http.MethodGet {
		s.previewSkill(w, r, parts[1])
		return
	}
	s.handleResources(w, r, parts[1:], resourceRoute{Kind: session.ResourceSkill, Name: "skill"})
}

func (s *Server) handleSubagents(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 3 && parts[2] == "preview" && r.Method == http.MethodGet {
		s.previewSubagent(w, r, parts[1])
		return
	}
	s.handleResources(w, r, parts[1:], resourceRoute{Kind: session.ResourceSubagent, Name: "subagent"})
}

type resourceRoute struct {
	Kind session.ResourceKind
	Name string
}

func (s *Server) handleResources(w http.ResponseWriter, r *http.Request, rest []string, route resourceRoute) {
	store := s.resourceStore()
	if store == nil {
		writeError(w, r, http.StatusNotImplemented, "resource_store_missing", "이 gateway에는 resource store가 연결되지 않았어요")
		return
	}
	if len(rest) == 0 {
		switch r.Method {
		case http.MethodGet:
			s.listResources(w, r, store, route)
		case http.MethodPost:
			s.saveResource(w, r, store, route, "")
		default:
			writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 resource method예요")
		}
		return
	}
	if len(rest) != 1 {
		writeError(w, r, http.StatusNotFound, "not_found", route.Name+" endpoint를 찾을 수 없어요")
		return
	}
	id := rest[0]
	switch r.Method {
	case http.MethodGet:
		s.withResource(w, r, route.Kind, id, func(resource session.Resource) {
			writeJSON(w, toResourceDTO(resource))
		})
	case http.MethodPut:
		s.saveResource(w, r, store, route, id)
	case http.MethodDelete:
		if err := store.DeleteResource(r.Context(), route.Kind, id); err != nil {
			writeError(w, r, http.StatusNotFound, "resource_not_found", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 resource method예요")
	}
}

func (s *Server) withResource(w http.ResponseWriter, r *http.Request, kind session.ResourceKind, id string, fn func(session.Resource)) {
	store := s.resourceStore()
	if store == nil {
		writeError(w, r, http.StatusNotImplemented, "resource_store_missing", "이 gateway에는 resource store가 연결되지 않았어요")
		return
	}
	resource, err := store.LoadResource(r.Context(), kind, id)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "resource_not_found", err.Error())
		return
	}
	fn(resource)
}

func (s *Server) listResources(w http.ResponseWriter, r *http.Request, store session.ResourceStore, route resourceRoute) {
	limit := queryLimit(r, "limit", 100, 500)
	var enabled *bool
	if raw := strings.TrimSpace(r.URL.Query().Get("enabled")); raw != "" {
		value := raw == "1" || strings.EqualFold(raw, "true") || strings.EqualFold(raw, "yes")
		enabled = &value
	}
	resources, err := store.ListResources(r.Context(), session.ResourceQuery{Kind: route.Kind, Enabled: enabled, Limit: limit + 1})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "list_resources_failed", err.Error())
		return
	}
	resources, truncated := trimResources(resources, limit)
	out := make([]ResourceDTO, 0, len(resources))
	for _, resource := range resources {
		out = append(out, toResourceDTO(resource))
	}
	writeJSON(w, ResourceListResponse{Resources: out, Limit: limit, ResultTruncated: truncated})
}

func (s *Server) saveResource(w http.ResponseWriter, r *http.Request, store session.ResourceStore, route resourceRoute, id string) {
	var req ResourceDTO
	if err := decodeJSON(r, &req); err != nil {
		writeJSONDecodeError(w, r, err)
		return
	}
	resource, err := resourceFromDTO(route.Kind, id, req)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_resource", err.Error())
		return
	}
	saved, err := store.SaveResource(r.Context(), resource)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "save_resource_failed", err.Error())
		return
	}
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	writeJSONStatus(w, status, toResourceDTO(saved))
}

func (s *Server) resourceStore() session.ResourceStore {
	if s.cfg.ResourceStore != nil {
		return s.cfg.ResourceStore
	}
	store, _ := s.cfg.Store.(session.ResourceStore)
	return store
}

func resourceFromDTO(kind session.ResourceKind, id string, dto ResourceDTO) (session.Resource, error) {
	name := strings.TrimSpace(dto.Name)
	if name == "" {
		return session.Resource{}, errResourceNameRequired{}
	}
	enabled := true
	if dto.Enabled != nil {
		enabled = *dto.Enabled
	}
	config := dto.Config
	if config == nil {
		config = map[string]any{}
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return session.Resource{}, err
	}
	if id == "" {
		id = strings.TrimSpace(dto.ID)
	}
	return session.Resource{ID: id, Kind: kind, Name: name, Description: strings.TrimSpace(dto.Description), Enabled: enabled, Config: encoded}, nil
}

type errResourceNameRequired struct{}

func (errResourceNameRequired) Error() string { return "name이 필요해요" }

func toResourceDTO(resource session.Resource) ResourceDTO {
	config := map[string]any{}
	if len(resource.Config) > 0 {
		_ = json.Unmarshal(resource.Config, &config)
	}
	enabled := resource.Enabled
	return ResourceDTO{ID: resource.ID, Kind: string(resource.Kind), Name: resource.Name, Description: resource.Description, Enabled: &enabled, Config: config, CreatedAt: resource.CreatedAt.Format(time.RFC3339Nano), UpdatedAt: resource.UpdatedAt.Format(time.RFC3339Nano)}
}

func cloneResourceDTOs(resources []ResourceDTO) []ResourceDTO {
	out := make([]ResourceDTO, 0, len(resources))
	for _, resource := range resources {
		cloned := resource
		if resource.Enabled != nil {
			enabled := *resource.Enabled
			cloned.Enabled = &enabled
		}
		if resource.Config != nil {
			config := make(map[string]any, len(resource.Config))
			for key, value := range resource.Config {
				config[key] = value
			}
			cloned.Config = config
		}
		out = append(out, cloned)
	}
	return out
}

// RedactResourceDTO는 discovery/preview 응답에서 secret성 config 값을 숨겨요.
// 실행 경로는 원본 manifest를 쓰고, 외부 adapter용 응답만 마스킹해요.
func RedactResourceDTO(resource ResourceDTO) ResourceDTO {
	resources := cloneResourceDTOs([]ResourceDTO{resource})
	if len(resources) == 0 {
		return ResourceDTO{}
	}
	resources[0].Config = redactConfigMap(resources[0].Config)
	return resources[0]
}

func RedactResourceDTOs(resources []ResourceDTO) []ResourceDTO {
	out := make([]ResourceDTO, 0, len(resources))
	for _, resource := range resources {
		out = append(out, RedactResourceDTO(resource))
	}
	return out
}

func redactConfigMap(config map[string]any) map[string]any {
	if config == nil {
		return nil
	}
	out := make(map[string]any, len(config))
	for key, value := range config {
		if isSecretConfigKey(key) {
			out[key] = redactWholeValue(value)
			continue
		}
		out[key] = redactConfigValue(value)
	}
	return out
}

func redactConfigValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return redactConfigMap(v)
	case map[string]string:
		out := make(map[string]string, len(v))
		for key, value := range v {
			if isSecretConfigKey(key) {
				out[key] = "[REDACTED]"
			} else {
				out[key] = llm.RedactSecrets(value)
			}
		}
		return out
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, redactConfigValue(item))
		}
		return out
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			out = append(out, llm.RedactSecrets(item))
		}
		return out
	case string:
		return llm.RedactSecrets(v)
	default:
		return v
	}
}

func redactWholeValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key := range v {
			out[key] = "[REDACTED]"
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(v))
		for key := range v {
			out[key] = "[REDACTED]"
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = "[REDACTED]"
		}
		return out
	case []string:
		out := make([]string, len(v))
		for i := range v {
			out[i] = "[REDACTED]"
		}
		return out
	case string:
		if v == "" {
			return ""
		}
		return "[REDACTED]"
	default:
		return value
	}
}

func isSecretConfigKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	return strings.Contains(key, "key") || strings.Contains(key, "token") || strings.Contains(key, "secret") || strings.Contains(key, "authorization") || strings.Contains(key, "password")
}
