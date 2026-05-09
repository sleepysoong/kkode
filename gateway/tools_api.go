package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sleepysoong/kkode/llm"
	ktools "github.com/sleepysoong/kkode/tools"
	"github.com/sleepysoong/kkode/workspace"
)

const maxToolCallNameBytes = 128
const maxToolCallIDBytes = 128
const maxToolCallArgumentsBytes = 1 << 20
const defaultToolCallOutputBytes = 1 << 20
const maxToolCallOutputBytes = 8 << 20
const maxToolCallWebBytes = 8 << 20

// ToolDTO는 gateway가 외부 adapter에 노출하는 표준 tool 정의예요.
type ToolDTO struct {
	Kind              string         `json:"kind"`
	Name              string         `json:"name"`
	Description       string         `json:"description,omitempty"`
	Category          string         `json:"category,omitempty"`
	Effects           []string       `json:"effects,omitempty"`
	OutputFormat      string         `json:"output_format,omitempty"`
	Parameters        map[string]any `json:"parameters,omitempty"`
	ExampleArguments  map[string]any `json:"example_arguments,omitempty"`
	Strict            *bool          `json:"strict,omitempty"`
	RequiresWorkspace bool           `json:"requires_workspace,omitempty"`
}

type ToolListResponse struct {
	Tools           []ToolDTO `json:"tools"`
	TotalTools      int       `json:"total_tools,omitempty"`
	Limit           int       `json:"limit,omitempty"`
	Offset          int       `json:"offset,omitempty"`
	NextOffset      int       `json:"next_offset,omitempty"`
	ResultTruncated bool      `json:"result_truncated,omitempty"`
}

type ToolCallRequest struct {
	ProjectRoot    string         `json:"project_root"`
	Tool           string         `json:"tool"`
	Arguments      map[string]any `json:"arguments,omitempty"`
	CallID         string         `json:"call_id,omitempty"`
	TimeoutMS      int            `json:"timeout_ms,omitempty"`
	WebMaxBytes    int64          `json:"web_max_bytes,omitempty"`
	MaxOutputBytes int            `json:"max_output_bytes,omitempty"`
}

type ToolCallResponse struct {
	CallID          string `json:"call_id,omitempty"`
	Tool            string `json:"tool"`
	Output          string `json:"output,omitempty"`
	Error           string `json:"error,omitempty"`
	OutputBytes     int    `json:"output_bytes,omitempty"`
	OutputTruncated bool   `json:"output_truncated,omitempty"`
}

func (s *Server) handleTools(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 1 && r.Method == http.MethodGet {
		s.listTools(w, r)
		return
	}
	if len(parts) == 2 && parts[1] == "call" && r.Method == http.MethodPost {
		s.callTool(w, r)
		return
	}
	if len(parts) == 2 && parts[1] != "call" && r.Method == http.MethodGet {
		s.getTool(w, r, parts[1])
		return
	}
	if len(parts) == 1 {
		writeMethodNotAllowed(w, r, "지원하지 않는 tools method예요", http.MethodGet)
		return
	}
	if len(parts) == 2 && parts[1] == "call" {
		writeMethodNotAllowed(w, r, "지원하지 않는 tools method예요", http.MethodPost)
		return
	}
	if len(parts) == 2 {
		writeMethodNotAllowed(w, r, "지원하지 않는 tools method예요", http.MethodGet)
		return
	}
	writeError(w, r, http.StatusNotFound, "not_found", "tools endpoint를 찾을 수 없어요")
}

func (s *Server) listTools(w http.ResponseWriter, r *http.Request) {
	defs := gatewayToolDefinitions()
	out := make([]ToolDTO, 0, len(defs))
	for _, tool := range defs {
		out = append(out, toToolDTO(tool))
	}
	limit, ok := queryLimitParam(w, r, "limit", len(out), 500, "invalid_tool_list")
	if !ok {
		return
	}
	offset, ok := queryOffsetParam(w, r, "offset", "invalid_tool_list")
	if !ok {
		return
	}
	page, returned, truncated := pageSlice(out, limit, offset)
	writeJSON(w, ToolListResponse{Tools: page, TotalTools: len(out), Limit: limit, Offset: offset, NextOffset: nextOffset(offset, returned, truncated), ResultTruncated: truncated})
}

func (s *Server) getTool(w http.ResponseWriter, r *http.Request, name string) {
	name = strings.TrimSpace(name)
	defs := gatewayToolDefinitions()
	for _, tool := range defs {
		if tool.Name == name {
			writeJSON(w, toToolDTO(tool))
			return
		}
	}
	writeError(w, r, http.StatusNotFound, "tool_not_found", "tool을 찾을 수 없어요")
}

func (s *Server) callTool(w http.ResponseWriter, r *http.Request) {
	var req ToolCallRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONDecodeError(w, r, err)
		return
	}
	req.ProjectRoot = strings.TrimSpace(req.ProjectRoot)
	req.Tool = strings.TrimSpace(req.Tool)
	req.CallID = strings.TrimSpace(req.CallID)
	args, err := validateToolCallRequest(req)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_tool_call", err.Error())
		return
	}
	if !gatewayToolExists(req.Tool) {
		writeError(w, r, http.StatusNotFound, "tool_not_found", "tool을 찾을 수 없어요")
		return
	}
	execCtx, cancel, err := toolCallContext(r.Context(), req.TimeoutMS)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_tool_call", err.Error())
		return
	}
	defer cancel()
	var ws *workspace.Workspace
	if toolRequiresWorkspace(req.Tool) {
		if req.ProjectRoot == "" {
			writeError(w, r, http.StatusBadRequest, "invalid_tool_call", "이 tool은 project_root가 필요해요")
			return
		}
		ws, _, err = newWorkspace(req.ProjectRoot)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_workspace", err.Error())
			return
		}
	} else if req.ProjectRoot != "" {
		ws, _, err = newWorkspace(req.ProjectRoot)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_workspace", err.Error())
			return
		}
	}
	if strings.HasPrefix(req.Tool, "lsp_") {
		output, err := executeLSPTool(req.ProjectRoot, req.Tool, req.Arguments)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "tool_call_failed", err.Error())
			return
		}
		output, outputBytes, truncated := truncateToolOutput(output, toolCallOutputLimit(req.MaxOutputBytes))
		writeJSON(w, ToolCallResponse{CallID: req.CallID, Tool: req.Tool, Output: output, OutputBytes: outputBytes, OutputTruncated: truncated})
		return
	}
	_, handlers := ktools.StandardToolSet(ktools.SurfaceOptions{Workspace: ws, WebMaxBytes: req.WebMaxBytes, Timeout: toolCallTimeout(req.TimeoutMS)}).Parts()
	result, err := handlers.Execute(execCtx, llm.ToolCall{CallID: req.CallID, Name: req.Tool, Arguments: args})
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "tool_call_failed", err.Error())
		return
	}
	output, outputBytes, truncated := truncateToolOutput(result.Output, toolCallOutputLimit(req.MaxOutputBytes))
	writeJSON(w, ToolCallResponse{CallID: result.CallID, Tool: result.Name, Output: output, Error: result.Error, OutputBytes: outputBytes, OutputTruncated: truncated})
}

func validateToolCallRequest(req ToolCallRequest) ([]byte, error) {
	if req.Tool == "" {
		return nil, errors.New("tool이 필요해요")
	}
	if len(req.Tool) > maxToolCallNameBytes {
		return nil, fmt.Errorf("tool은 %d byte 이하여야 해요", maxToolCallNameBytes)
	}
	if len(req.CallID) > maxToolCallIDBytes {
		return nil, fmt.Errorf("call_id는 %d byte 이하여야 해요", maxToolCallIDBytes)
	}
	if req.MaxOutputBytes < 0 {
		return nil, errors.New("max_output_bytes는 0 이상이어야 해요")
	}
	if req.MaxOutputBytes > maxToolCallOutputBytes {
		return nil, fmt.Errorf("max_output_bytes는 %d 이하여야 해요", maxToolCallOutputBytes)
	}
	if req.WebMaxBytes < 0 {
		return nil, errors.New("web_max_bytes는 0 이상이어야 해요")
	}
	if req.WebMaxBytes > maxToolCallWebBytes {
		return nil, fmt.Errorf("web_max_bytes는 %d 이하여야 해요", maxToolCallWebBytes)
	}
	args, err := json.Marshal(req.Arguments)
	if err != nil {
		return nil, err
	}
	if len(args) > maxToolCallArgumentsBytes {
		return nil, fmt.Errorf("arguments는 %d byte 이하여야 해요", maxToolCallArgumentsBytes)
	}
	return args, nil
}

func toolCallOutputLimit(value int) int {
	if value <= 0 {
		return defaultToolCallOutputBytes
	}
	return value
}

func gatewayToolDefinitions() []llm.Tool {
	defs, _ := ktools.StandardToolSet(ktools.SurfaceOptions{}).Parts()
	return defs
}

func gatewayToolExists(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, tool := range gatewayToolDefinitions() {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func executeLSPTool(root string, name string, args map[string]any) (string, error) {
	if args == nil {
		args = map[string]any{}
	}
	var value any
	var err error
	switch name {
	case "lsp_symbols":
		limit, err := positiveIntArg(args, "limit", 200)
		if err != nil {
			return "", err
		}
		symbols, scanErr := scanGoSymbols(root, stringArg(args, "query"), limit+1)
		if scanErr != nil {
			return "", scanErr
		}
		symbols, truncated := limitLSPSymbols(symbols, limit)
		value = LSPSymbolListResponse{Symbols: symbols, Limit: limit, ResultTruncated: truncated}
	case "lsp_document_symbols":
		limit, err := positiveIntArg(args, "limit", 200)
		if err != nil {
			return "", err
		}
		symbols, scanErr := scanGoDocumentSymbols(root, stringArg(args, "path"), limit+1)
		if scanErr != nil {
			return "", scanErr
		}
		symbols, truncated := limitLSPSymbols(symbols, limit)
		value = LSPSymbolListResponse{Symbols: symbols, Limit: limit, ResultTruncated: truncated}
	case "lsp_definitions":
		symbol, scanErr := lspSymbolFromToolArgs(root, args)
		if scanErr != nil {
			return "", scanErr
		}
		limit, err := positiveIntArg(args, "limit", 50)
		if err != nil {
			return "", err
		}
		locations, scanErr := scanGoDefinitions(root, symbol, limit+1)
		if scanErr != nil {
			return "", scanErr
		}
		locations, truncated := limitLSPSymbols(locations, limit)
		value = LSPLocationListResponse{Locations: locations, Limit: limit, ResultTruncated: truncated}
	case "lsp_references":
		symbol, scanErr := lspSymbolFromToolArgs(root, args)
		if scanErr != nil {
			return "", scanErr
		}
		limit, err := positiveIntArg(args, "limit", 100)
		if err != nil {
			return "", err
		}
		references, scanErr := scanGoReferences(root, symbol, limit+1)
		if scanErr != nil {
			return "", scanErr
		}
		references, truncated := limitLSPReferences(references, limit)
		value = LSPReferenceListResponse{References: references, Limit: limit, ResultTruncated: truncated}
	case "lsp_hover":
		symbol, scanErr := lspSymbolFromToolArgs(root, args)
		if scanErr != nil {
			return "", scanErr
		}
		value, err = scanGoHover(root, symbol)
	case "lsp_diagnostics":
		limit, err := positiveIntArg(args, "limit", 200)
		if err != nil {
			return "", err
		}
		diagnostics, scanErr := scanGoDiagnostics(root, stringArg(args, "path"), limit+1)
		if scanErr != nil {
			return "", scanErr
		}
		diagnostics, truncated := limitLSPDiagnostics(diagnostics, limit)
		value = LSPDiagnosticListResponse{Diagnostics: diagnostics, Limit: limit, ResultTruncated: truncated}
	default:
		return "", errors.New("지원하지 않는 LSP tool이에요")
	}
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func lspSymbolFromToolArgs(root string, args map[string]any) (string, error) {
	if symbol := strings.TrimSpace(stringArg(args, "symbol")); symbol != "" {
		return symbol, nil
	}
	path := strings.TrimSpace(stringArg(args, "path"))
	line := intArg(args, "line", 0)
	column := intArg(args, "column", 0)
	if path == "" && line == 0 && column == 0 {
		return "", errors.New("symbol 또는 path,line,column이 필요해요")
	}
	if path == "" || line <= 0 || column < 0 {
		return "", errors.New("커서 위치 조회에는 path,line,column이 모두 필요해요")
	}
	if column == 0 {
		column = 1
	}
	return scanGoIdentifierAt(root, path, line, column)
}

func stringArg(args map[string]any, name string) string {
	value, _ := args[name].(string)
	return strings.TrimSpace(value)
}

func intArg(args map[string]any, name string, fallback int) int {
	switch value := args[name].(type) {
	case int:
		if value > 0 {
			return value
		}
	case float64:
		if value > 0 && value == math.Trunc(value) {
			return int(value)
		}
	case json.Number:
		if n, err := value.Int64(); err == nil && n > 0 {
			return int(n)
		}
	}
	return fallback
}

func positiveIntArg(args map[string]any, name string, fallback int) (int, error) {
	switch value := args[name].(type) {
	case int:
		return positiveIntValue(name, int64(value), fallback)
	case float64:
		if value < 0 {
			return 0, fmt.Errorf("%s는 0 이상이어야 해요", name)
		}
		if value != math.Trunc(value) {
			return 0, fmt.Errorf("%s는 integer여야 해요", name)
		}
		if value > 0 {
			return int(value), nil
		}
	case json.Number:
		n, err := value.Int64()
		if err != nil {
			return 0, err
		}
		return positiveIntValue(name, n, fallback)
	}
	return fallback, nil
}

func positiveIntValue(name string, value int64, fallback int) (int, error) {
	if value < 0 {
		return 0, fmt.Errorf("%s는 0 이상이어야 해요", name)
	}
	if value == 0 {
		return fallback, nil
	}
	return int(value), nil
}

func toolCallContext(parent context.Context, timeoutMS int) (context.Context, context.CancelFunc, error) {
	if timeoutMS < 0 {
		return nil, nil, errNegativeToolTimeout()
	}
	timeout := toolCallTimeout(timeoutMS)
	if timeout <= 0 {
		return parent, func() {}, nil
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	return ctx, cancel, nil
}

func toolCallTimeout(timeoutMS int) time.Duration {
	if timeoutMS <= 0 {
		return 0
	}
	return time.Duration(timeoutMS) * time.Millisecond
}

func errNegativeToolTimeout() error {
	return errors.New("timeout_ms는 0 이상이어야 해요")
}

func toToolDTO(tool llm.Tool) ToolDTO {
	meta := toolMetadata(tool.Name)
	return ToolDTO{Kind: string(tool.Kind), Name: tool.Name, Description: tool.Description, Category: meta.Category, Effects: meta.Effects, OutputFormat: meta.OutputFormat, Parameters: tool.Parameters, ExampleArguments: toolExampleArguments(tool.Name), Strict: tool.Strict, RequiresWorkspace: toolRequiresWorkspace(tool.Name)}
}

type toolMetadataDTO struct {
	Category     string
	Effects      []string
	OutputFormat string
}

func toolMetadata(name string) toolMetadataDTO {
	switch strings.TrimSpace(name) {
	case "file_read", "file_list", "file_glob", "file_grep":
		return toolMetadataDTO{Category: "file", Effects: []string{"read"}, OutputFormat: toolOutputFormat(name)}
	case "file_write", "file_edit", "file_apply_patch", "file_delete", "file_move", "file_restore_checkpoint":
		return toolMetadataDTO{Category: "file", Effects: []string{"write"}, OutputFormat: "text"}
	case "file_prune_checkpoints":
		return toolMetadataDTO{Category: "file", Effects: []string{"write"}, OutputFormat: "json"}
	case "shell_run":
		return toolMetadataDTO{Category: "shell", Effects: []string{"execute"}, OutputFormat: "json"}
	case "web_fetch":
		return toolMetadataDTO{Category: "web", Effects: []string{"network"}, OutputFormat: "json"}
	case "lsp_symbols", "lsp_document_symbols", "lsp_definitions", "lsp_references", "lsp_hover", "lsp_diagnostics":
		return toolMetadataDTO{Category: "codeintel", Effects: []string{"read"}, OutputFormat: "json"}
	default:
		return toolMetadataDTO{OutputFormat: "text"}
	}
}

func toolOutputFormat(name string) string {
	switch strings.TrimSpace(name) {
	case "file_grep":
		return "json"
	default:
		return "text"
	}
}

func toolExampleArguments(name string) map[string]any {
	switch strings.TrimSpace(name) {
	case "file_read":
		return map[string]any{"path": "README.md", "max_bytes": 4096}
	case "file_write":
		return map[string]any{"path": "notes/todo.md", "content": "할 일을 정리해요\n"}
	case "file_delete":
		return map[string]any{"path": "notes/old.md", "recursive": false}
	case "file_move":
		return map[string]any{"source": "notes/draft.md", "destination": "notes/final.md", "overwrite": false}
	case "file_edit":
		return map[string]any{"path": "README.md", "old": "기존 문장", "new": "새 문장", "expected_replacements": 1}
	case "file_apply_patch":
		return map[string]any{"patch_text": "*** Begin Patch\n*** Update File: README.md\n@@\n-기존 문장\n+새 문장\n*** End Patch\n"}
	case "file_restore_checkpoint":
		return map[string]any{"checkpoint_id": "ws_20260509T120000Z_0000000000000000"}
	case "file_prune_checkpoints":
		return map[string]any{"keep_latest": 50}
	case "file_list":
		return map[string]any{"path": "."}
	case "file_glob":
		return map[string]any{"pattern": "**/*.go"}
	case "file_grep":
		return map[string]any{"pattern": "TODO", "path_glob": "**/*.go", "max_matches": 20}
	case "shell_run":
		return map[string]any{"command": "go", "args": []any{"test", "./..."}, "timeout_ms": 120000}
	case "web_fetch":
		return map[string]any{"url": "https://example.com", "max_bytes": 65536, "timeout_ms": 10000}
	case "lsp_symbols":
		return map[string]any{"query": "Run", "limit": 20}
	case "lsp_document_symbols":
		return map[string]any{"path": "gateway/server.go"}
	case "lsp_definitions":
		return map[string]any{"symbol": "Server", "limit": 20}
	case "lsp_references":
		return map[string]any{"symbol": "RunDTO", "limit": 50}
	case "lsp_hover":
		return map[string]any{"symbol": "Server"}
	case "lsp_diagnostics":
		return map[string]any{"path": "gateway/server.go", "limit": 50}
	default:
		return nil
	}
}

func toolRequiresWorkspace(name string) bool {
	name = strings.TrimSpace(name)
	return name == "shell_run" || strings.HasPrefix(name, "file_") || strings.HasPrefix(name, "lsp_")
}

func truncateToolOutput(output string, maxBytes int) (string, int, bool) {
	outputBytes := len(output)
	if maxBytes <= 0 || outputBytes <= maxBytes {
		return output, outputBytes, false
	}
	used := 0
	end := 0
	for i, r := range output {
		size := utf8.RuneLen(r)
		if size < 0 {
			size = len(string(r))
		}
		if used+size > maxBytes {
			break
		}
		used += size
		end = i + size
	}
	if end == 0 && maxBytes > 0 {
		return "", outputBytes, true
	}
	return output[:end], outputBytes, true
}
