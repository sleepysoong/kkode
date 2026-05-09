package gateway

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/session"
)

const maxResourceIDBytes = 128
const maxResourceNameBytes = 256
const maxResourceDescriptionBytes = 4096
const maxResourceConfigBytes = 1 << 20
const maxResourceConfigStringBytes = 64 << 10
const maxResourceInlineMCPLabelBytes = 128
const maxResourceStringArrayItems = 256
const maxResourceStringArrayItemBytes = 4096
const maxResourceStringMapItems = 256
const maxResourceStringMapKeyBytes = 256

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
	TotalResources  int           `json:"total_resources,omitempty"`
	Limit           int           `json:"limit,omitempty"`
	Offset          int           `json:"offset,omitempty"`
	NextOffset      int           `json:"next_offset,omitempty"`
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
	if len(parts) == 4 && parts[3] == "tools" {
		writeMethodNotAllowed(w, r, "지원하지 않는 mcp tools method예요", http.MethodGet)
		return
	}
	if len(parts) == 4 && parts[3] == "resources" && r.Method == http.MethodGet {
		s.listMCPServerResources(w, r, parts[2])
		return
	}
	if len(parts) == 4 && parts[3] == "resources" {
		writeMethodNotAllowed(w, r, "지원하지 않는 mcp resources method예요", http.MethodGet)
		return
	}
	if len(parts) == 5 && parts[3] == "resources" && parts[4] == "read" && r.Method == http.MethodGet {
		s.readMCPServerResource(w, r, parts[2])
		return
	}
	if len(parts) == 5 && parts[3] == "resources" && parts[4] == "read" {
		writeMethodNotAllowed(w, r, "지원하지 않는 mcp resource read method예요", http.MethodGet)
		return
	}
	if len(parts) == 4 && parts[3] == "prompts" && r.Method == http.MethodGet {
		s.listMCPServerPrompts(w, r, parts[2])
		return
	}
	if len(parts) == 4 && parts[3] == "prompts" {
		writeMethodNotAllowed(w, r, "지원하지 않는 mcp prompts method예요", http.MethodGet)
		return
	}
	if len(parts) == 6 && parts[3] == "prompts" && parts[5] == "get" && r.Method == http.MethodPost {
		s.getMCPServerPrompt(w, r, parts[2], parts[4])
		return
	}
	if len(parts) == 6 && parts[3] == "prompts" && parts[5] == "get" {
		writeMethodNotAllowed(w, r, "지원하지 않는 mcp prompt get method예요", http.MethodPost)
		return
	}
	if len(parts) == 6 && parts[3] == "tools" && parts[5] == "call" && r.Method == http.MethodPost {
		s.callMCPServerTool(w, r, parts[2], parts[4])
		return
	}
	if len(parts) == 6 && parts[3] == "tools" && parts[5] == "call" {
		writeMethodNotAllowed(w, r, "지원하지 않는 mcp tool call method예요", http.MethodPost)
		return
	}
	s.handleResources(w, r, parts[2:], resourceRoute{Kind: session.ResourceMCPServer, Name: "mcp server"})
}

func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 3 && parts[2] == "preview" && r.Method == http.MethodGet {
		s.previewSkill(w, r, parts[1])
		return
	}
	if len(parts) == 3 && parts[2] == "preview" {
		writeMethodNotAllowed(w, r, "지원하지 않는 skill preview method예요", http.MethodGet)
		return
	}
	s.handleResources(w, r, parts[1:], resourceRoute{Kind: session.ResourceSkill, Name: "skill"})
}

func (s *Server) handleSubagents(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 3 && parts[2] == "preview" && r.Method == http.MethodGet {
		s.previewSubagent(w, r, parts[1])
		return
	}
	if len(parts) == 3 && parts[2] == "preview" {
		writeMethodNotAllowed(w, r, "지원하지 않는 subagent preview method예요", http.MethodGet)
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
			writeMethodNotAllowed(w, r, "지원하지 않는 resource method예요", http.MethodGet, http.MethodPost)
		}
		return
	}
	if len(rest) != 1 {
		writeError(w, r, http.StatusNotFound, "not_found", route.Name+" endpoint를 찾을 수 없어요")
		return
	}
	id := strings.TrimSpace(rest[0])
	if err := validateResourceIDText(id); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_resource", err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.withResource(w, r, route.Kind, id, func(resource session.Resource) {
			writeJSON(w, publicResourceDTO(resource))
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
		writeMethodNotAllowed(w, r, "지원하지 않는 resource method예요", http.MethodGet, http.MethodPut, http.MethodDelete)
	}
}

func (s *Server) withResource(w http.ResponseWriter, r *http.Request, kind session.ResourceKind, id string, fn func(session.Resource)) {
	id = strings.TrimSpace(id)
	if err := validateResourceIDText(id); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_resource", err.Error())
		return
	}
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
	limit, ok := queryLimitParam(w, r, "limit", 100, 500, "invalid_resource_list")
	if !ok {
		return
	}
	offset, ok := queryOffsetParam(w, r, "offset", "invalid_resource_list")
	if !ok {
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if len(name) > maxResourceNameBytes {
		writeError(w, r, http.StatusBadRequest, "invalid_resource_list", fmt.Sprintf("resource name은 %d byte 이하여야 해요", maxResourceNameBytes))
		return
	}
	var enabled *bool
	if strings.TrimSpace(r.URL.Query().Get("enabled")) != "" {
		value, ok := queryBoolParam(w, r, "enabled", false, "invalid_resource_list")
		if !ok {
			return
		}
		enabled = &value
	}
	query := session.ResourceQuery{Kind: route.Kind, Name: name, Enabled: enabled}
	totalResources := 0
	if counter, ok := store.(session.ResourceCounter); ok {
		total, err := counter.CountResources(r.Context(), query)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "count_resources_failed", err.Error())
			return
		}
		totalResources = total
	}
	pageQuery := query
	pageQuery.Limit = limit + 1
	pageQuery.Offset = offset
	resources, err := store.ListResources(r.Context(), pageQuery)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "list_resources_failed", err.Error())
		return
	}
	resources, returned, truncated := trimResources(resources, limit)
	out := make([]ResourceDTO, 0, len(resources))
	for _, resource := range resources {
		out = append(out, publicResourceDTO(resource))
	}
	writeJSON(w, ResourceListResponse{Resources: out, TotalResources: totalResources, Limit: limit, Offset: offset, NextOffset: nextOffset(offset, returned, truncated), ResultTruncated: truncated})
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
	writeJSONStatus(w, status, publicResourceDTO(saved))
}

func (s *Server) resourceStore() session.ResourceStore {
	if s.cfg.ResourceStore != nil {
		return s.cfg.ResourceStore
	}
	store, _ := s.cfg.Store.(session.ResourceStore)
	return store
}

func resourceFromDTO(kind session.ResourceKind, id string, dto ResourceDTO) (session.Resource, error) {
	id = strings.TrimSpace(id)
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
	if err := validateResourceConfig(kind, config); err != nil {
		return session.Resource{}, err
	}
	config = normalizeResourceConfig(kind, config)
	encoded, err := json.Marshal(config)
	if err != nil {
		return session.Resource{}, err
	}
	if id == "" {
		id = strings.TrimSpace(dto.ID)
	}
	if err := validateResourceIdentity(id, name, dto.Description); err != nil {
		return session.Resource{}, err
	}
	if len(encoded) > maxResourceConfigBytes {
		return session.Resource{}, fmt.Errorf("resource config는 %d byte 이하여야 해요", maxResourceConfigBytes)
	}
	return session.Resource{ID: id, Kind: kind, Name: name, Description: strings.TrimSpace(dto.Description), Enabled: enabled, Config: encoded}, nil
}

func validateResourceIdentity(id string, name string, description string) error {
	if id != "" {
		if err := validateResourceIDText(id); err != nil {
			return err
		}
	}
	if len(name) > maxResourceNameBytes {
		return fmt.Errorf("resource name은 %d byte 이하여야 해요", maxResourceNameBytes)
	}
	if len(strings.TrimSpace(description)) > maxResourceDescriptionBytes {
		return fmt.Errorf("resource description은 %d byte 이하여야 해요", maxResourceDescriptionBytes)
	}
	return nil
}

func validateResourceIDText(id string) error {
	if id == "" {
		return fmt.Errorf("resource id가 필요해요")
	}
	if len(id) > maxResourceIDBytes {
		return fmt.Errorf("resource id는 %d byte 이하여야 해요", maxResourceIDBytes)
	}
	if !validRunMetadataKey(id) {
		return fmt.Errorf("resource id는 영문/숫자/._- 문자만 쓸 수 있어요")
	}
	return nil
}

func validateResourceConfig(kind session.ResourceKind, config map[string]any) error {
	switch kind {
	case session.ResourceMCPServer:
		return validateMCPResourceConfig(config, "config")
	case session.ResourceSkill:
		if err := validateStringConfigFields(config, "skill config", "path", "directory"); err != nil {
			return err
		}
		if strings.TrimSpace(configString(config, "path")) == "" && strings.TrimSpace(configString(config, "directory")) == "" {
			return fmt.Errorf("skill config에는 path 또는 directory가 필요해요")
		}
	case session.ResourceSubagent:
		return validateSubagentResourceConfig(config)
	default:
		return fmt.Errorf("resource kind는 mcp_server, skill, subagent 중 하나여야 해요")
	}
	return nil
}

func validateSubagentResourceConfig(config map[string]any) error {
	if err := validateStringConfigFields(config, "subagent config", "display_name", "description", "prompt"); err != nil {
		return err
	}
	if err := validateStringArrayConfig(config, "tools"); err != nil {
		return err
	}
	if err := validateStringArrayConfig(config, "skills"); err != nil {
		return err
	}
	if err := validateStringArrayConfig(config, "mcp_server_ids"); err != nil {
		return err
	}
	rawServers, ok := config["mcp_servers"]
	if !ok || rawServers == nil {
		return nil
	}
	servers, ok := rawServers.(map[string]any)
	if !ok {
		return fmt.Errorf("subagent config mcp_servers는 object여야 해요")
	}
	labels := map[string]bool{}
	for name, raw := range servers {
		serverName := strings.TrimSpace(name)
		if serverName == "" {
			return fmt.Errorf("subagent config mcp_servers label은 비어 있지 않아야 해요")
		}
		if labels[serverName] {
			return fmt.Errorf("subagent config mcp_servers.%s label이 중복됐어요", serverName)
		}
		if len(serverName) > maxResourceInlineMCPLabelBytes {
			return fmt.Errorf("subagent config mcp_servers.%s label은 %d byte 이하여야 해요", serverName, maxResourceInlineMCPLabelBytes)
		}
		labels[serverName] = true
		label := "subagent config mcp_servers." + serverName
		switch value := raw.(type) {
		case string:
			command := strings.TrimSpace(value)
			if command == "" {
				return fmt.Errorf("%s command가 필요해요", label)
			}
			if len(command) > maxResourceConfigStringBytes {
				return fmt.Errorf("%s command는 %d byte 이하여야 해요", label, maxResourceConfigStringBytes)
			}
		case map[string]any:
			if err := validateMCPResourceConfig(value, label); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s는 command string 또는 MCP config object여야 해요", label)
		}
	}
	return nil
}

func validateMCPResourceConfig(config map[string]any, label string) error {
	if err := validateStringConfigFields(config, label, "kind", "name", "command", "url", "cwd"); err != nil {
		return err
	}
	kind := strings.TrimSpace(configString(config, "kind"))
	command := strings.TrimSpace(configString(config, "command"))
	rawURL := strings.TrimSpace(configString(config, "url"))
	if err := validateStringArrayConfig(config, "args"); err != nil {
		return err
	}
	if err := validateStringMapConfig(config, label, "env"); err != nil {
		return err
	}
	if err := validateStringMapConfig(config, label, "headers"); err != nil {
		return err
	}
	if _, err := nonNegativeIntConfig(config, "timeout"); err != nil {
		return err
	}
	switch kind {
	case "":
		if rawURL == "" && command == "" {
			return fmt.Errorf("%s에는 command 또는 url이 필요해요", label)
		}
	case string(llm.MCPStdio):
		if command == "" {
			return fmt.Errorf("%s stdio MCP에는 command가 필요해요", label)
		}
	case string(llm.MCPHTTP):
		if rawURL == "" {
			return fmt.Errorf("%s http MCP에는 url이 필요해요", label)
		}
	default:
		return fmt.Errorf("%s kind는 stdio 또는 http여야 해요", label)
	}
	if rawURL != "" && !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return fmt.Errorf("%s url은 http/https여야 해요", label)
	}
	return nil
}

func validateStringConfigFields(config map[string]any, label string, keys ...string) error {
	for _, key := range keys {
		raw, ok := config[key]
		if !ok || raw == nil {
			continue
		}
		value, ok := raw.(string)
		if !ok {
			return fmt.Errorf("%s %s는 string이어야 해요", label, key)
		}
		if len(strings.TrimSpace(value)) > maxResourceConfigStringBytes {
			return fmt.Errorf("%s %s는 %d byte 이하여야 해요", label, key, maxResourceConfigStringBytes)
		}
	}
	return nil
}

func validateStringMapConfig(config map[string]any, label string, key string) error {
	raw, ok := config[key]
	if !ok || raw == nil {
		return nil
	}
	values, err := stringMapConfigValues(raw)
	if err != nil {
		return fmt.Errorf("%s %s는 string object여야 해요", label, key)
	}
	if len(values) > maxResourceStringMapItems {
		return fmt.Errorf("%s %s는 최대 %d개까지 허용돼요", label, key, maxResourceStringMapItems)
	}
	seen := map[string]bool{}
	for rawKey, rawValue := range values {
		itemKey := strings.TrimSpace(rawKey)
		if itemKey == "" {
			return fmt.Errorf("%s %s key는 비어 있지 않아야 해요", label, key)
		}
		if seen[itemKey] {
			return fmt.Errorf("%s %s.%s key가 중복됐어요", label, key, itemKey)
		}
		seen[itemKey] = true
		if len(itemKey) > maxResourceStringMapKeyBytes {
			return fmt.Errorf("%s %s key는 %d byte 이하여야 해요", label, key, maxResourceStringMapKeyBytes)
		}
		value := strings.TrimSpace(rawValue)
		if len(value) > maxResourceConfigStringBytes {
			return fmt.Errorf("%s %s.%s는 %d byte 이하여야 해요", label, key, itemKey, maxResourceConfigStringBytes)
		}
	}
	return nil
}

func stringMapConfigValues(raw any) (map[string]string, error) {
	switch values := raw.(type) {
	case map[string]any:
		out := make(map[string]string, len(values))
		for key, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("non-string value")
			}
			out[key] = text
		}
		return out, nil
	case map[string]string:
		out := make(map[string]string, len(values))
		for key, value := range values {
			out[key] = value
		}
		return out, nil
	default:
		return nil, fmt.Errorf("not a map")
	}
}

func normalizeResourceConfig(kind session.ResourceKind, config map[string]any) map[string]any {
	out := cloneAnyMap(config)
	switch kind {
	case session.ResourceMCPServer:
		normalizeMCPResourceConfig(out)
	case session.ResourceSkill:
		trimStringConfig(out, "path")
		trimStringConfig(out, "directory")
	case session.ResourceSubagent:
		trimStringConfig(out, "display_name")
		trimStringConfig(out, "description")
		normalizeStringArrayConfig(out, "tools")
		normalizeStringArrayConfig(out, "skills")
		normalizeStringArrayConfig(out, "mcp_server_ids")
		normalizeInlineMCPServers(out)
	}
	return out
}

func normalizeMCPResourceConfig(config map[string]any) {
	for _, key := range []string{"kind", "name", "command", "url", "cwd"} {
		trimStringConfig(config, key)
	}
	normalizeStringMapConfig(config, "env")
	normalizeStringMapConfig(config, "headers")
}

func normalizeInlineMCPServers(config map[string]any) {
	rawServers, ok := config["mcp_servers"]
	if !ok || rawServers == nil {
		return
	}
	servers, ok := rawServers.(map[string]any)
	if !ok {
		return
	}
	out := make(map[string]any, len(servers))
	for name, raw := range servers {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		switch value := raw.(type) {
		case string:
			out[name] = strings.TrimSpace(value)
		case map[string]any:
			nested := cloneAnyMap(value)
			normalizeMCPResourceConfig(nested)
			out[name] = nested
		default:
			out[name] = raw
		}
	}
	config["mcp_servers"] = out
}

func trimStringConfig(config map[string]any, key string) {
	value, ok := config[key].(string)
	if ok {
		config[key] = strings.TrimSpace(value)
	}
}

func normalizeStringMapConfig(config map[string]any, key string) {
	raw, ok := config[key]
	if !ok || raw == nil {
		return
	}
	values, err := stringMapConfigValues(raw)
	if err != nil {
		return
	}
	out := make(map[string]string, len(values))
	for rawKey, rawValue := range values {
		itemKey := strings.TrimSpace(rawKey)
		if itemKey == "" {
			continue
		}
		out[itemKey] = strings.TrimSpace(rawValue)
	}
	config[key] = out
}

func normalizeStringArrayConfig(config map[string]any, key string) {
	raw, ok := config[key]
	if !ok || raw == nil {
		return
	}
	var values []string
	switch items := raw.(type) {
	case []any:
		values = make([]string, 0, len(items))
		for _, item := range items {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
	case []string:
		values = append([]string(nil), items...)
	default:
		return
	}
	config[key] = sanitizeUniqueStrings(values)
}

func configString(config map[string]any, key string) string {
	value, _ := config[key].(string)
	return value
}

func nonNegativeIntConfig(config map[string]any, key string) (int, error) {
	raw, ok := config[key]
	if !ok || raw == nil {
		return 0, nil
	}
	switch value := raw.(type) {
	case float64:
		if value < 0 {
			return 0, fmt.Errorf("%s는 0 이상이어야 해요", key)
		}
		if value != math.Trunc(value) {
			return 0, fmt.Errorf("%s는 integer여야 해요", key)
		}
		return int(value), nil
	case int:
		if value < 0 {
			return 0, fmt.Errorf("%s는 0 이상이어야 해요", key)
		}
		return value, nil
	case json.Number:
		n, err := value.Int64()
		if err != nil {
			return 0, err
		}
		if n < 0 {
			return 0, fmt.Errorf("%s는 0 이상이어야 해요", key)
		}
		return int(n), nil
	default:
		return 0, fmt.Errorf("%s는 number여야 해요", key)
	}
}

func validateStringArrayConfig(config map[string]any, key string) error {
	raw, ok := config[key]
	if !ok || raw == nil {
		return nil
	}
	var values []string
	switch items := raw.(type) {
	case []any:
		values = make([]string, 0, len(items))
		for _, value := range items {
			text, ok := value.(string)
			if !ok {
				return fmt.Errorf("%s는 string array여야 해요", key)
			}
			values = append(values, text)
		}
	case []string:
		values = items
	default:
		return fmt.Errorf("%s는 string array여야 해요", key)
	}
	for i, text := range values {
		text = strings.TrimSpace(text)
		if text == "" {
			return fmt.Errorf("%s[%d]는 비어 있지 않은 string이어야 해요", key, i)
		}
		if len(text) > maxResourceStringArrayItemBytes {
			return fmt.Errorf("%s[%d]는 %d byte 이하여야 해요", key, i, maxResourceStringArrayItemBytes)
		}
	}
	if len(values) > maxResourceStringArrayItems {
		return fmt.Errorf("%s는 최대 %d개까지 허용돼요", key, maxResourceStringArrayItems)
	}
	return nil
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

func publicResourceDTO(resource session.Resource) ResourceDTO {
	return RedactResourceDTO(toResourceDTO(resource))
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
