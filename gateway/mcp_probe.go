package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/session"
	ktools "github.com/sleepysoong/kkode/tools"
)

const maxMCPHTTPResponseBytes = 8 << 20

// MCPToolDTO는 MCP tools/list 결과를 외부 API에 노출하는 항목이에요.
type MCPToolDTO struct {
	Name             string         `json:"name"`
	Description      string         `json:"description,omitempty"`
	Category         string         `json:"category,omitempty"`
	Effects          []string       `json:"effects,omitempty"`
	OutputFormat     string         `json:"output_format,omitempty"`
	InputSchema      map[string]any `json:"input_schema,omitempty"`
	ExampleArguments map[string]any `json:"example_arguments,omitempty"`
}

type MCPToolListResponse struct {
	Server          ResourceDTO  `json:"server"`
	Tools           []MCPToolDTO `json:"tools"`
	Limit           int          `json:"limit,omitempty"`
	Offset          int          `json:"offset,omitempty"`
	NextOffset      int          `json:"next_offset,omitempty"`
	ResultTruncated bool         `json:"result_truncated,omitempty"`
}

// MCPResourceDTO는 MCP resources/list 결과를 외부 API에 노출하는 항목이에요.
type MCPResourceDTO struct {
	URI         string `json:"uri"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mime_type,omitempty"`
}

type MCPResourceListResponse struct {
	Server          ResourceDTO      `json:"server"`
	Resources       []MCPResourceDTO `json:"resources"`
	Limit           int              `json:"limit,omitempty"`
	Offset          int              `json:"offset,omitempty"`
	NextOffset      int              `json:"next_offset,omitempty"`
	ResultTruncated bool             `json:"result_truncated,omitempty"`
}

// MCPResourceContentDTO는 MCP resources/read 결과의 content 항목이에요.
type MCPResourceContentDTO struct {
	URI      string `json:"uri,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

type MCPResourceReadResponse struct {
	Server   ResourceDTO             `json:"server"`
	URI      string                  `json:"uri"`
	Contents []MCPResourceContentDTO `json:"contents"`
}

// MCPPromptDTO는 MCP prompts/list 결과를 외부 API에 노출하는 항목이에요.
type MCPPromptDTO struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Arguments   []MCPPromptArgumentDTO `json:"arguments,omitempty"`
}

type MCPPromptArgumentDTO struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

type MCPPromptListResponse struct {
	Server          ResourceDTO    `json:"server"`
	Prompts         []MCPPromptDTO `json:"prompts"`
	Limit           int            `json:"limit,omitempty"`
	Offset          int            `json:"offset,omitempty"`
	NextOffset      int            `json:"next_offset,omitempty"`
	ResultTruncated bool           `json:"result_truncated,omitempty"`
}

type MCPPromptGetRequest struct {
	Arguments map[string]any `json:"arguments,omitempty"`
}

type MCPPromptMessageDTO struct {
	Role    string         `json:"role"`
	Content map[string]any `json:"content,omitempty"`
}

type MCPPromptGetResponse struct {
	Server   ResourceDTO           `json:"server"`
	Prompt   string                `json:"prompt"`
	Messages []MCPPromptMessageDTO `json:"messages"`
}

type MCPToolCallRequest struct {
	Arguments      map[string]any `json:"arguments,omitempty"`
	MaxOutputBytes int            `json:"max_output_bytes,omitempty"`
}

type MCPToolCallResponse struct {
	Server          ResourceDTO    `json:"server"`
	Tool            string         `json:"tool"`
	Result          map[string]any `json:"result"`
	ResultBytes     int            `json:"result_bytes,omitempty"`
	ResultTruncated bool           `json:"result_truncated,omitempty"`
}

type mcpProbeConfig struct {
	Kind    string            `json:"kind"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Cwd     string            `json:"cwd"`
	Timeout int               `json:"timeout"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

func (s *Server) listMCPServerTools(w http.ResponseWriter, r *http.Request, serverID string) {
	s.listMCPServerToolsLike(w, r, serverID, "tools")
}

func (s *Server) listMCPServerResources(w http.ResponseWriter, r *http.Request, serverID string) {
	s.listMCPServerToolsLike(w, r, serverID, "resources")
}

func (s *Server) listMCPServerPrompts(w http.ResponseWriter, r *http.Request, serverID string) {
	s.listMCPServerToolsLike(w, r, serverID, "prompts")
}

func (s *Server) readMCPServerResource(w http.ResponseWriter, r *http.Request, serverID string) {
	s.withMCPServer(w, r, serverID, func(resource session.Resource) {
		uri := strings.TrimSpace(r.URL.Query().Get("uri"))
		if uri == "" {
			writeError(w, r, http.StatusBadRequest, "invalid_mcp_resource", "resource uri가 필요해요")
			return
		}
		contents, err := readMCPResource(r.Context(), resource, uri)
		if err != nil {
			writeError(w, r, http.StatusBadGateway, "mcp_resource_read_failed", err.Error())
			return
		}
		writeJSON(w, MCPResourceReadResponse{Server: publicResourceDTO(resource), URI: uri, Contents: contents})
	})
}

func (s *Server) getMCPServerPrompt(w http.ResponseWriter, r *http.Request, serverID string, promptName string) {
	s.withMCPServer(w, r, serverID, func(resource session.Resource) {
		var req MCPPromptGetRequest
		if r.Body != nil && r.ContentLength != 0 {
			if err := decodeJSON(r, &req); err != nil {
				writeJSONDecodeError(w, r, err)
				return
			}
		}
		messages, err := getMCPPrompt(r.Context(), resource, promptName, req.Arguments)
		if err != nil {
			writeError(w, r, http.StatusBadGateway, "mcp_prompt_get_failed", err.Error())
			return
		}
		writeJSON(w, MCPPromptGetResponse{Server: publicResourceDTO(resource), Prompt: promptName, Messages: messages})
	})
}

func (s *Server) listMCPServerToolsLike(w http.ResponseWriter, r *http.Request, serverID string, kind string) {
	s.withMCPServer(w, r, serverID, func(resource session.Resource) {
		limit := queryLimit(r, "limit", 100, 500)
		offset := queryOffset(r, "offset")
		switch kind {
		case "tools":
			tools, err := probeMCPTools(r.Context(), resource)
			if err != nil {
				writeError(w, r, http.StatusBadGateway, "mcp_probe_failed", err.Error())
				return
			}
			tools, returned, truncated := pageSlice(tools, limit, offset)
			writeJSON(w, MCPToolListResponse{Server: publicResourceDTO(resource), Tools: tools, Limit: limit, Offset: offset, NextOffset: nextOffset(offset, returned, truncated), ResultTruncated: truncated})
		case "resources":
			resources, err := probeMCPResources(r.Context(), resource)
			if err != nil {
				writeError(w, r, http.StatusBadGateway, "mcp_probe_failed", err.Error())
				return
			}
			resources, returned, truncated := pageSlice(resources, limit, offset)
			writeJSON(w, MCPResourceListResponse{Server: publicResourceDTO(resource), Resources: resources, Limit: limit, Offset: offset, NextOffset: nextOffset(offset, returned, truncated), ResultTruncated: truncated})
		case "prompts":
			prompts, err := probeMCPPrompts(r.Context(), resource)
			if err != nil {
				writeError(w, r, http.StatusBadGateway, "mcp_probe_failed", err.Error())
				return
			}
			prompts, returned, truncated := pageSlice(prompts, limit, offset)
			writeJSON(w, MCPPromptListResponse{Server: publicResourceDTO(resource), Prompts: prompts, Limit: limit, Offset: offset, NextOffset: nextOffset(offset, returned, truncated), ResultTruncated: truncated})
		}
	})
}

func pageSlice[T any](items []T, limit int, offset int) ([]T, int, bool) {
	if limit < 0 {
		limit = 0
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		return []T{}, 0, false
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	truncated := end < len(items)
	return items[offset:end], end - offset, truncated
}

func (s *Server) callMCPServerTool(w http.ResponseWriter, r *http.Request, serverID string, toolName string) {
	s.withMCPServer(w, r, serverID, func(resource session.Resource) {
		var req MCPToolCallRequest
		if r.Body != nil && r.ContentLength != 0 {
			if err := decodeJSON(r, &req); err != nil {
				writeJSONDecodeError(w, r, err)
				return
			}
		}
		result, err := callMCPTool(r.Context(), resource, toolName, req.Arguments)
		if err != nil {
			writeError(w, r, http.StatusBadGateway, "mcp_tool_call_failed", err.Error())
			return
		}
		result, resultBytes, truncated := truncateMCPToolResult(result, req.MaxOutputBytes)
		writeJSON(w, MCPToolCallResponse{Server: publicResourceDTO(resource), Tool: toolName, Result: result, ResultBytes: resultBytes, ResultTruncated: truncated})
	})
}

func (s *Server) withMCPServer(w http.ResponseWriter, r *http.Request, serverID string, fn func(session.Resource)) {
	s.withResource(w, r, session.ResourceMCPServer, serverID, fn)
}

func probeMCPTools(ctx context.Context, resource session.Resource) ([]MCPToolDTO, error) {
	resp, err := runMCPRequest(ctx, resource, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	return parseMCPTools(resp)
}

func probeMCPResources(ctx context.Context, resource session.Resource) ([]MCPResourceDTO, error) {
	resp, err := runMCPRequest(ctx, resource, "resources/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	return parseMCPResources(resp)
}

func probeMCPPrompts(ctx context.Context, resource session.Resource) ([]MCPPromptDTO, error) {
	resp, err := runMCPRequest(ctx, resource, "prompts/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	return parseMCPPrompts(resp)
}

func readMCPResource(ctx context.Context, resource session.Resource, uri string) ([]MCPResourceContentDTO, error) {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return nil, fmt.Errorf("MCP resource uri가 필요해요")
	}
	resp, err := runMCPRequest(ctx, resource, "resources/read", map[string]any{"uri": uri})
	if err != nil {
		return nil, err
	}
	return parseMCPResourceContents(resp), nil
}

func getMCPPrompt(ctx context.Context, resource session.Resource, promptName string, arguments map[string]any) ([]MCPPromptMessageDTO, error) {
	promptName = strings.TrimSpace(promptName)
	if promptName == "" {
		return nil, fmt.Errorf("MCP prompt 이름이 필요해요")
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	resp, err := runMCPRequest(ctx, resource, "prompts/get", map[string]any{"name": promptName, "arguments": arguments})
	if err != nil {
		return nil, err
	}
	return parseMCPPromptMessages(resp), nil
}

func callMCPTool(ctx context.Context, resource session.Resource, toolName string, arguments map[string]any) (map[string]any, error) {
	server, err := mcpServerFromProbeResource(resource)
	if err != nil {
		return nil, err
	}
	return ktools.CallMCPTool(ctx, server, toolName, arguments)
}

func mcpServerFromProbeResource(resource session.Resource) (llm.MCPServer, error) {
	cfg, err := parseMCPProbeConfig(resource)
	if err != nil {
		return llm.MCPServer{}, err
	}
	kind := llm.MCPServerKind(cfg.Kind)
	if kind == "" {
		if cfg.URL != "" {
			kind = llm.MCPHTTP
		} else {
			kind = llm.MCPStdio
		}
	}
	return llm.MCPServer{Kind: kind, Name: resource.Name, Timeout: cfg.Timeout, Command: cfg.Command, Args: cfg.Args, Env: cfg.Env, Cwd: cfg.Cwd, URL: cfg.URL, Headers: cfg.Headers}, nil
}

func truncateMCPToolResult(result map[string]any, maxBytes int) (map[string]any, int, bool) {
	data, err := json.Marshal(result)
	if err != nil {
		return result, 0, false
	}
	resultBytes := len(data)
	if maxBytes <= 0 || resultBytes <= maxBytes {
		return result, resultBytes, false
	}
	budget := maxBytes
	copied, truncated := truncateMCPValue(result, "", &budget)
	out, _ := copied.(map[string]any)
	if out == nil {
		out = map[string]any{}
	}
	if !truncated {
		preview, _, _ := truncateToolOutput(string(data), maxBytes)
		out = map[string]any{
			"content":          []any{map[string]any{"type": "text", "text": preview}},
			"_kkode_truncated": true,
		}
		truncated = true
	}
	return out, resultBytes, truncated
}

func truncateMCPValue(value any, key string, budget *int) (any, bool) {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		truncated := false
		for childKey, child := range v {
			copied, childTruncated := truncateMCPValue(child, childKey, budget)
			out[childKey] = copied
			truncated = truncated || childTruncated
		}
		return out, truncated
	case []any:
		out := make([]any, len(v))
		truncated := false
		for i, child := range v {
			copied, childTruncated := truncateMCPValue(child, key, budget)
			out[i] = copied
			truncated = truncated || childTruncated
		}
		return out, truncated
	case string:
		if !isMCPOutputField(key) {
			return v, false
		}
		if *budget <= 0 {
			if v == "" {
				return v, false
			}
			return "", true
		}
		out, bytes, truncated := truncateToolOutput(v, *budget)
		if truncated {
			*budget = 0
			return out, true
		}
		*budget -= bytes
		return out, false
	default:
		return v, false
	}
}

func isMCPOutputField(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "text", "blob", "output":
		return true
	default:
		return false
	}
}

func runMCPRequest(ctx context.Context, resource session.Resource, method string, params map[string]any) (map[string]any, error) {
	server, err := mcpServerFromProbeResource(resource)
	if err != nil {
		return nil, err
	}
	return ktools.RunMCPRequest(ctx, server, method, params)
}

func parseMCPProbeConfig(resource session.Resource) (mcpProbeConfig, error) {
	var cfg mcpProbeConfig
	if len(resource.Config) > 0 {
		if err := json.Unmarshal(resource.Config, &cfg); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

func parseMCPTools(msg map[string]any) ([]MCPToolDTO, error) {
	result, _ := msg["result"].(map[string]any)
	items, _ := result["tools"].([]any)
	out := make([]MCPToolDTO, 0, len(items))
	for _, item := range items {
		raw, _ := item.(map[string]any)
		if raw == nil {
			continue
		}
		tool := MCPToolDTO{Name: stringValue(raw["name"]), Description: stringValue(raw["description"]), Category: "mcp", Effects: []string{"mcp"}, OutputFormat: "json"}
		if schema, ok := raw["inputSchema"].(map[string]any); ok {
			tool.InputSchema = schema
			tool.ExampleArguments = exampleArgumentsFromSchema(schema)
		}
		if tool.Name != "" {
			out = append(out, tool)
		}
	}
	return out, nil
}

func exampleArgumentsFromSchema(schema map[string]any) map[string]any {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return nil
	}
	out := map[string]any{}
	for name, raw := range props {
		prop, _ := raw.(map[string]any)
		if prop == nil {
			continue
		}
		out[name] = exampleValueFromSchema(prop)
		if len(out) >= 8 {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func exampleValueFromSchema(schema map[string]any) any {
	if values, ok := schema["enum"].([]any); ok && len(values) > 0 {
		return values[0]
	}
	switch strings.TrimSpace(stringValue(schema["type"])) {
	case "integer":
		return 1
	case "number":
		return 1.0
	case "boolean":
		return true
	case "array":
		return []any{}
	case "object":
		return map[string]any{}
	default:
		return "value"
	}
}

func parseMCPResources(msg map[string]any) ([]MCPResourceDTO, error) {
	result, _ := msg["result"].(map[string]any)
	items, _ := result["resources"].([]any)
	out := make([]MCPResourceDTO, 0, len(items))
	for _, item := range items {
		raw, _ := item.(map[string]any)
		if raw == nil {
			continue
		}
		resource := MCPResourceDTO{URI: stringValue(raw["uri"]), Name: stringValue(raw["name"]), Description: stringValue(raw["description"]), MimeType: stringValue(raw["mimeType"])}
		if resource.URI != "" {
			out = append(out, resource)
		}
	}
	return out, nil
}

func parseMCPResourceContents(msg map[string]any) []MCPResourceContentDTO {
	result, _ := msg["result"].(map[string]any)
	items, _ := result["contents"].([]any)
	out := make([]MCPResourceContentDTO, 0, len(items))
	for _, item := range items {
		raw, _ := item.(map[string]any)
		if raw == nil {
			continue
		}
		content := MCPResourceContentDTO{URI: stringValue(raw["uri"]), MimeType: stringValue(raw["mimeType"]), Text: stringValue(raw["text"]), Blob: stringValue(raw["blob"])}
		if content.URI != "" || content.Text != "" || content.Blob != "" {
			out = append(out, content)
		}
	}
	return out
}

func parseMCPPrompts(msg map[string]any) ([]MCPPromptDTO, error) {
	result, _ := msg["result"].(map[string]any)
	items, _ := result["prompts"].([]any)
	out := make([]MCPPromptDTO, 0, len(items))
	for _, item := range items {
		raw, _ := item.(map[string]any)
		if raw == nil {
			continue
		}
		prompt := MCPPromptDTO{Name: stringValue(raw["name"]), Description: stringValue(raw["description"]), Arguments: parseMCPPromptArguments(raw["arguments"])}
		if prompt.Name != "" {
			out = append(out, prompt)
		}
	}
	return out, nil
}

func parseMCPPromptMessages(msg map[string]any) []MCPPromptMessageDTO {
	result, _ := msg["result"].(map[string]any)
	items, _ := result["messages"].([]any)
	out := make([]MCPPromptMessageDTO, 0, len(items))
	for _, item := range items {
		raw, _ := item.(map[string]any)
		if raw == nil {
			continue
		}
		content, _ := raw["content"].(map[string]any)
		message := MCPPromptMessageDTO{Role: stringValue(raw["role"]), Content: content}
		if message.Role != "" {
			out = append(out, message)
		}
	}
	return out
}

func parseMCPPromptArguments(value any) []MCPPromptArgumentDTO {
	items, _ := value.([]any)
	out := make([]MCPPromptArgumentDTO, 0, len(items))
	for _, item := range items {
		raw, _ := item.(map[string]any)
		if raw == nil {
			continue
		}
		arg := MCPPromptArgumentDTO{Name: stringValue(raw["name"]), Description: stringValue(raw["description"]), Required: boolValue(raw["required"])}
		if arg.Name != "" {
			out = append(out, arg)
		}
	}
	return out
}

func stringValue(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func boolValue(value any) bool {
	v, _ := value.(bool)
	return v
}
