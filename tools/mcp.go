package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sleepysoong/kkode/llm"
)

const maxMCPResponseBytes = 8 << 20
const maxMCPStderrBytes = 1 << 20

type MCPToolCallResult struct {
	Server          string         `json:"server"`
	Tool            string         `json:"tool"`
	Result          map[string]any `json:"result"`
	ResultBytes     int            `json:"result_bytes,omitempty"`
	ResultTruncated bool           `json:"result_truncated,omitempty"`
}

// MCPTools exposes selected MCP servers through one generic local tool call.
func MCPTools(servers map[string]llm.MCPServer) ([]llm.Tool, llm.ToolRegistry) {
	if len(servers) == 0 {
		return nil, nil
	}
	strict := true
	defs := []llm.Tool{{
		Kind:        llm.ToolFunction,
		Name:        "mcp_call",
		Description: "선택된 MCP server의 tool을 호출하고 JSON result를 반환해요",
		Strict:      &strict,
		Parameters: ObjectSchemaRequired(map[string]any{
			"server":           StringSchema(),
			"tool":             StringSchema(),
			"arguments":        map[string]any{"type": "object", "additionalProperties": true},
			"max_output_bytes": NonNegativeIntegerSchema(),
		}, []string{"server", "tool"}),
	}}
	cloned := cloneMCPServers(servers)
	handlers := llm.ToolRegistry{
		"mcp_call": llm.JSONToolHandler(func(ctx context.Context, in struct {
			Server         string         `json:"server"`
			Tool           string         `json:"tool"`
			Arguments      map[string]any `json:"arguments"`
			MaxOutputBytes int            `json:"max_output_bytes"`
		}) (string, error) {
			serverName := strings.TrimSpace(in.Server)
			server, ok := cloned[serverName]
			if !ok {
				return "", fmt.Errorf("MCP server %q is not configured; available=%s", serverName, strings.Join(sortedMCPServerNames(cloned), ","))
			}
			if in.MaxOutputBytes < 0 {
				return "", fmt.Errorf("max_output_bytes must be >= 0")
			}
			result, err := CallMCPTool(ctx, server, in.Tool, in.Arguments)
			if err != nil {
				return "", err
			}
			result, resultBytes, truncated := truncateMCPResult(result, in.MaxOutputBytes)
			out := MCPToolCallResult{Server: serverName, Tool: strings.TrimSpace(in.Tool), Result: result, ResultBytes: resultBytes, ResultTruncated: truncated}
			data, err := json.Marshal(out)
			if err != nil {
				return "", err
			}
			return string(data), nil
		}),
	}
	return defs, handlers
}

func CallMCPTool(ctx context.Context, server llm.MCPServer, toolName string, arguments map[string]any) (map[string]any, error) {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return nil, fmt.Errorf("MCP tool 이름이 필요해요")
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	resp, err := RunMCPRequest(ctx, server, "tools/call", map[string]any{"name": toolName, "arguments": arguments})
	if err != nil {
		return nil, err
	}
	result, _ := resp["result"].(map[string]any)
	if result == nil {
		result = map[string]any{}
	}
	return result, nil
}

func RunMCPRequest(ctx context.Context, server llm.MCPServer, method string, params map[string]any) (map[string]any, error) {
	switch server.Kind {
	case "", llm.MCPStdio:
		return runStdioMCPRequest(ctx, server, method, params)
	case llm.MCPHTTP:
		return runHTTPMCPRequest(ctx, server, method, params)
	default:
		return nil, fmt.Errorf("지원하지 않는 MCP kind예요: %s", server.Kind)
	}
}

func runStdioMCPRequest(ctx context.Context, server llm.MCPServer, method string, params map[string]any) (map[string]any, error) {
	if strings.TrimSpace(server.Command) == "" {
		return nil, fmt.Errorf("stdio MCP command가 필요해요")
	}
	timeout := time.Duration(server.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, server.Command, server.Args...)
	if server.Cwd != "" {
		cmd.Dir = server.Cwd
	}
	for k, v := range server.Env {
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
	stderr := &limitedMCPBuffer{max: maxMCPStderrBytes}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	reader := bufio.NewReader(stdout)
	if err := writeMCPFrame(stdin, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "kkode", "version": "dev"}}}); err != nil {
		return nil, err
	}
	if _, err := readMCPResponse(ctx, reader, 1); err != nil {
		return nil, withMCPStderr(err, stderr.String())
	}
	_ = writeMCPFrame(stdin, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized", "params": map[string]any{}})
	if err := writeMCPFrame(stdin, map[string]any{"jsonrpc": "2.0", "id": 2, "method": method, "params": params}); err != nil {
		return nil, err
	}
	resp, err := readMCPResponse(ctx, reader, 2)
	if err != nil {
		return nil, withMCPStderr(err, stderr.String())
	}
	return resp, nil
}

func runHTTPMCPRequest(ctx context.Context, server llm.MCPServer, method string, params map[string]any) (map[string]any, error) {
	if strings.TrimSpace(server.URL) == "" {
		return nil, fmt.Errorf("HTTP MCP url이 필요해요")
	}
	timeout := time.Duration(server.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	initResp, sessionID, err := postHTTPMCP(ctx, server, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "kkode", "version": "dev"}}}, 1, "")
	if err != nil {
		return nil, err
	}
	if sessionID == "" {
		sessionID = stringMCPValue(initResp["Mcp-Session-Id"])
	}
	_ = postHTTPMCPNotification(ctx, server, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized", "params": map[string]any{}}, sessionID)
	resp, _, err := postHTTPMCP(ctx, server, map[string]any{"jsonrpc": "2.0", "id": 2, "method": method, "params": params}, 2, sessionID)
	return resp, err
}

func postHTTPMCPNotification(ctx context.Context, server llm.MCPServer, payload map[string]any, sessionID string) error {
	_, _, err := postHTTPMCP(ctx, server, payload, 0, sessionID)
	return err
}

func postHTTPMCP(ctx context.Context, server llm.MCPServer, payload map[string]any, id int, sessionID string) (map[string]any, string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2025-03-26")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	for k, v := range server.Headers {
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
	data, err := ReadLimitedMCPBody(res.Body, maxMCPResponseBytes)
	if err != nil {
		return nil, nextSessionID, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, nextSessionID, fmt.Errorf("HTTP MCP %s failed: status=%d body=%s", stringMCPValue(payload["method"]), res.StatusCode, strings.TrimSpace(string(data)))
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

func writeMCPFrame(w io.Writer, payload map[string]any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(data)); err != nil {
		return err
	}
	_, err = w.Write(data)
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
		if rawErr, ok := msg["error"]; ok {
			return nil, fmt.Errorf("mcp error: %v", rawErr)
		}
		if intMCPValue(msg["id"]) == id {
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
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 && strings.EqualFold(strings.TrimSpace(parts[0]), "content-length") {
			contentLength = intMCPValue(strings.TrimSpace(parts[1]))
		}
	}
	if contentLength <= 0 {
		return nil, fmt.Errorf("MCP Content-Length header가 필요해요")
	}
	if contentLength > maxMCPResponseBytes {
		return nil, fmt.Errorf("MCP response body가 너무 커요: max_bytes=%d", maxMCPResponseBytes)
	}
	data := make([]byte, contentLength)
	_, err := io.ReadFull(r, data)
	return data, err
}

func readHTTPSSEMCPResponse(data []byte, id int) (map[string]any, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), maxMCPResponseBytes)
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
		if rawErr, ok := msg["error"]; ok {
			return nil, false, fmt.Errorf("mcp error: %v", rawErr)
		}
		if intMCPValue(msg["id"]) == id {
			return msg, true, nil
		}
		return nil, false, nil
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
			value = strings.TrimSpace(value)
			if value == "" || value == "[DONE]" {
				continue
			}
			if eventData.Len() > 0 {
				eventData.WriteByte('\n')
			}
			eventData.WriteString(value)
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

func truncateMCPResult(result map[string]any, maxBytes int) (map[string]any, int, bool) {
	data, err := json.Marshal(result)
	if err != nil {
		return result, 0, false
	}
	resultBytes := len(data)
	if maxBytes <= 0 || resultBytes <= maxBytes {
		return result, resultBytes, false
	}
	text := string(data)
	if len(text) > maxBytes {
		text = truncateMCPUTF8(text, maxBytes)
	}
	return map[string]any{
		"content":          []any{map[string]any{"type": "text", "text": text}},
		"_kkode_truncated": true,
	}, resultBytes, true
}

func truncateMCPUTF8(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	end := maxBytes
	for end > 0 && !utf8.ValidString(text[:end]) {
		end--
	}
	return text[:end]
}

func ReadLimitedMCPBody(r io.Reader, maxBytes int) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = maxMCPResponseBytes
	}
	data, err := io.ReadAll(io.LimitReader(r, int64(maxBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBytes {
		return nil, fmt.Errorf("HTTP MCP 응답 body가 너무 커요: max_bytes=%d", maxBytes)
	}
	return data, nil
}

func withMCPStderr(err error, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return err
	}
	return fmt.Errorf("%w stderr=%s", err, stderr)
}

type limitedMCPBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (b *limitedMCPBuffer) Write(p []byte) (int, error) {
	if b.max <= 0 {
		b.truncated = true
		return len(p), nil
	}
	remaining := b.max - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}

func (b *limitedMCPBuffer) String() string {
	data := b.buf.Bytes()
	if !utf8.Valid(data) {
		end := len(data)
		for end > 0 && !utf8.Valid(data[:end]) {
			end--
		}
		data = data[:end]
	}
	text := string(data)
	if b.truncated {
		return strings.TrimRight(text, "\n") + "\n[MCP stderr truncated]"
	}
	return text
}

func cloneMCPServers(servers map[string]llm.MCPServer) map[string]llm.MCPServer {
	out := map[string]llm.MCPServer{}
	for name, server := range servers {
		server.Tools = append([]string{}, server.Tools...)
		server.Args = append([]string{}, server.Args...)
		server.Env = cloneStringMap(server.Env)
		server.Headers = cloneStringMap(server.Headers)
		out[name] = server
	}
	return out
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for k, v := range values {
		out[k] = v
	}
	return out
}

func sortedMCPServerNames(servers map[string]llm.MCPServer) []string {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func stringMCPValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func intMCPValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		var n int
		_, _ = fmt.Sscanf(v, "%d", &n)
		return n
	default:
		return 0
	}
}
