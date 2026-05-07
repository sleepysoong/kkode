package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/sleepysoong/kkode/session"
)

// MCPToolDTO는 MCP tools/list 결과를 외부 API에 노출하는 항목이에요.
type MCPToolDTO struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

type MCPToolListResponse struct {
	Server ResourceDTO  `json:"server"`
	Tools  []MCPToolDTO `json:"tools"`
}

// MCPResourceDTO는 MCP resources/list 결과를 외부 API에 노출하는 항목이에요.
type MCPResourceDTO struct {
	URI         string `json:"uri"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mime_type,omitempty"`
}

type MCPResourceListResponse struct {
	Server    ResourceDTO      `json:"server"`
	Resources []MCPResourceDTO `json:"resources"`
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
	Server  ResourceDTO    `json:"server"`
	Prompts []MCPPromptDTO `json:"prompts"`
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
		writeJSON(w, MCPResourceReadResponse{Server: toResourceDTO(resource), URI: uri, Contents: contents})
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
		writeJSON(w, MCPPromptGetResponse{Server: toResourceDTO(resource), Prompt: promptName, Messages: messages})
	})
}

func (s *Server) listMCPServerToolsLike(w http.ResponseWriter, r *http.Request, serverID string, kind string) {
	s.withMCPServer(w, r, serverID, func(resource session.Resource) {
		switch kind {
		case "tools":
			tools, err := probeMCPTools(r.Context(), resource)
			if err != nil {
				writeError(w, r, http.StatusBadGateway, "mcp_probe_failed", err.Error())
				return
			}
			writeJSON(w, MCPToolListResponse{Server: toResourceDTO(resource), Tools: tools})
		case "resources":
			resources, err := probeMCPResources(r.Context(), resource)
			if err != nil {
				writeError(w, r, http.StatusBadGateway, "mcp_probe_failed", err.Error())
				return
			}
			writeJSON(w, MCPResourceListResponse{Server: toResourceDTO(resource), Resources: resources})
		case "prompts":
			prompts, err := probeMCPPrompts(r.Context(), resource)
			if err != nil {
				writeError(w, r, http.StatusBadGateway, "mcp_probe_failed", err.Error())
				return
			}
			writeJSON(w, MCPPromptListResponse{Server: toResourceDTO(resource), Prompts: prompts})
		}
	})
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
		writeJSON(w, MCPToolCallResponse{Server: toResourceDTO(resource), Tool: toolName, Result: result, ResultBytes: resultBytes, ResultTruncated: truncated})
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
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return nil, fmt.Errorf("MCP tool 이름이 필요해요")
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	resp, err := runMCPRequest(ctx, resource, "tools/call", map[string]any{"name": toolName, "arguments": arguments})
	if err != nil {
		return nil, err
	}
	result, _ := resp["result"].(map[string]any)
	if result == nil {
		result = map[string]any{}
	}
	return result, nil
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
	cfg, err := parseMCPProbeConfig(resource)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Kind)) {
	case "", "stdio":
		return runStdioMCPRequestWithConfig(ctx, cfg, method, params)
	case "http", "streamable_http", "streamable-http":
		return runHTTPMCPRequest(ctx, cfg, method, params)
	default:
		return nil, fmt.Errorf("지원하지 않는 MCP probe kind예요: %s", cfg.Kind)
	}
}

func runStdioMCPRequestWithConfig(ctx context.Context, cfg mcpProbeConfig, method string, params map[string]any) (map[string]any, error) {
	if cfg.Command == "" {
		return nil, fmt.Errorf("stdio MCP command가 필요해요")
	}
	timeout := time.Duration(cfg.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	if cfg.Cwd != "" {
		cmd.Dir = cfg.Cwd
	}
	for k, v := range cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	reader := bufio.NewReader(stdout)
	if err := writeMCPFrame(stdin, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "kkode", "version": "dev"}}}); err != nil {
		return nil, err
	}
	if _, err := readMCPResponse(ctx, reader, 1); err != nil {
		return nil, withStderr(err, stderr.String())
	}
	_ = writeMCPFrame(stdin, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized", "params": map[string]any{}})
	if err := writeMCPFrame(stdin, map[string]any{"jsonrpc": "2.0", "id": 2, "method": method, "params": params}); err != nil {
		return nil, err
	}
	resp, err := readMCPResponse(ctx, reader, 2)
	if err != nil {
		return nil, withStderr(err, stderr.String())
	}
	return resp, nil
}

func runHTTPMCPRequest(ctx context.Context, cfg mcpProbeConfig, method string, params map[string]any) (map[string]any, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, fmt.Errorf("HTTP MCP url이 필요해요")
	}
	timeout := time.Duration(cfg.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	initResp, sessionID, err := postHTTPMCP(ctx, cfg, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "kkode", "version": "dev"}}}, 1, "")
	if err != nil {
		return nil, err
	}
	if sessionID == "" {
		sessionID = stringValue(initResp["Mcp-Session-Id"])
	}
	_ = postHTTPMCPNotification(ctx, cfg, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized", "params": map[string]any{}}, sessionID)
	resp, _, err := postHTTPMCP(ctx, cfg, map[string]any{"jsonrpc": "2.0", "id": 2, "method": method, "params": params}, 2, sessionID)
	return resp, err
}

func postHTTPMCPNotification(ctx context.Context, cfg mcpProbeConfig, payload map[string]any, sessionID string) error {
	_, _, err := postHTTPMCP(ctx, cfg, payload, 0, sessionID)
	return err
}

func postHTTPMCP(ctx context.Context, cfg mcpProbeConfig, payload map[string]any, id int, sessionID string) (map[string]any, string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2025-03-26")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	nextSessionID := res.Header.Get("Mcp-Session-Id")
	if nextSessionID == "" {
		nextSessionID = res.Header.Get("MCP-Session-Id")
	}
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, nextSessionID, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, nextSessionID, fmt.Errorf("HTTP MCP %s failed: status=%d body=%s", stringValue(payload["method"]), res.StatusCode, strings.TrimSpace(string(data)))
	}
	if id == 0 || len(strings.TrimSpace(string(data))) == 0 {
		return map[string]any{}, nextSessionID, nil
	}
	if strings.Contains(strings.ToLower(res.Header.Get("Content-Type")), "text/event-stream") {
		msg, err := readHTTPSSEMCPResponse(data, id)
		return msg, nextSessionID, err
	}
	var msg map[string]any
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, nextSessionID, err
	}
	if rawErr, ok := msg["error"]; ok {
		return nil, nextSessionID, fmt.Errorf("mcp error: %v", rawErr)
	}
	return msg, nextSessionID, nil
}

func readHTTPSSEMCPResponse(data []byte, id int) (map[string]any, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var eventData strings.Builder
	flush := func() (map[string]any, bool, error) {
		if eventData.Len() == 0 {
			return nil, false, nil
		}
		raw := strings.TrimSpace(eventData.String())
		eventData.Reset()
		var msg map[string]any
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			return nil, false, err
		}
		if numericID(msg["id"]) != id {
			return nil, false, nil
		}
		if rawErr, ok := msg["error"]; ok {
			return nil, false, fmt.Errorf("mcp error: %v", rawErr)
		}
		return msg, true, nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			if msg, ok, err := flush(); ok || err != nil {
				return msg, err
			}
			continue
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			if eventData.Len() > 0 {
				eventData.WriteByte('\n')
			}
			eventData.WriteString(strings.TrimSpace(value))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if msg, ok, err := flush(); ok || err != nil {
		return msg, err
	}
	return nil, fmt.Errorf("HTTP MCP SSE response id %d를 찾지 못했어요", id)
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

func writeMCPFrame(w io.Writer, payload map[string]any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "Content-Length: %d\r\n\r\n%s", len(data), data)
	return err
}

func readMCPResponse(ctx context.Context, r *bufio.Reader, id int) (map[string]any, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		payload, err := readMCPFrame(r)
		if err != nil {
			return nil, err
		}
		var msg map[string]any
		if err := json.Unmarshal(payload, &msg); err != nil {
			return nil, err
		}
		if numericID(msg["id"]) == id {
			if rawErr, ok := msg["error"]; ok {
				return nil, fmt.Errorf("mcp error: %v", rawErr)
			}
			return msg, nil
		}
	}
}

func readMCPFrame(r *bufio.Reader) ([]byte, error) {
	contentLength := 0
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			contentLength, _ = strconv.Atoi(strings.TrimSpace(value))
		}
	}
	if contentLength <= 0 {
		return nil, fmt.Errorf("MCP Content-Length header가 필요해요")
	}
	payload := make([]byte, contentLength)
	_, err := io.ReadFull(r, payload)
	return payload, err
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
		tool := MCPToolDTO{Name: stringValue(raw["name"]), Description: stringValue(raw["description"])}
		if schema, ok := raw["inputSchema"].(map[string]any); ok {
			tool.InputSchema = schema
		}
		if tool.Name != "" {
			out = append(out, tool)
		}
	}
	return out, nil
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

func numericID(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
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

func withStderr(err error, stderr string) error {
	if strings.TrimSpace(stderr) == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr))
}
