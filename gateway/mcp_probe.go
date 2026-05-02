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

type mcpProbeConfig struct {
	Kind    string            `json:"kind"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Cwd     string            `json:"cwd"`
	Timeout int               `json:"timeout"`
}

func (s *Server) listMCPServerTools(w http.ResponseWriter, r *http.Request, serverID string) {
	store := s.resourceStore()
	if store == nil {
		writeError(w, r, http.StatusNotImplemented, "resource_store_missing", "이 gateway에는 resource store가 연결되지 않았어요")
		return
	}
	resource, err := store.LoadResource(r.Context(), session.ResourceMCPServer, serverID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "resource_not_found", err.Error())
		return
	}
	tools, err := probeMCPTools(r.Context(), resource)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "mcp_probe_failed", err.Error())
		return
	}
	writeJSON(w, MCPToolListResponse{Server: toResourceDTO(resource), Tools: tools})
}

func probeMCPTools(ctx context.Context, resource session.Resource) ([]MCPToolDTO, error) {
	var cfg mcpProbeConfig
	if len(resource.Config) > 0 {
		if err := json.Unmarshal(resource.Config, &cfg); err != nil {
			return nil, err
		}
	}
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
	if err := writeMCPFrame(stdin, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}}); err != nil {
		return nil, err
	}
	resp, err := readMCPResponse(ctx, reader, 2)
	if err != nil {
		return nil, withStderr(err, stderr.String())
	}
	return parseMCPTools(resp)
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

func withStderr(err error, stderr string) error {
	if strings.TrimSpace(stderr) == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr))
}
