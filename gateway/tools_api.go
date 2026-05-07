package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/sleepysoong/kkode/llm"
	ktools "github.com/sleepysoong/kkode/tools"
	"github.com/sleepysoong/kkode/workspace"
)

// ToolDTO는 gateway가 외부 adapter에 노출하는 표준 tool 정의예요.
type ToolDTO struct {
	Kind              string         `json:"kind"`
	Name              string         `json:"name"`
	Description       string         `json:"description,omitempty"`
	Parameters        map[string]any `json:"parameters,omitempty"`
	ExampleArguments  map[string]any `json:"example_arguments,omitempty"`
	Strict            *bool          `json:"strict,omitempty"`
	RequiresWorkspace bool           `json:"requires_workspace,omitempty"`
}

type ToolListResponse struct {
	Tools []ToolDTO `json:"tools"`
}

type ToolCallRequest struct {
	ProjectRoot    string         `json:"project_root"`
	Tool           string         `json:"tool"`
	Arguments      map[string]any `json:"arguments,omitempty"`
	CallID         string         `json:"call_id,omitempty"`
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
	if len(parts) == 2 && r.Method == http.MethodGet {
		s.getTool(w, r, parts[1])
		return
	}
	if len(parts) == 2 && parts[1] == "call" && r.Method == http.MethodPost {
		s.callTool(w, r)
		return
	}
	if len(parts) == 1 || (len(parts) == 2 && parts[1] == "call") {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 tools method예요")
		return
	}
	writeError(w, r, http.StatusNotFound, "not_found", "tools endpoint를 찾을 수 없어요")
}

func (s *Server) listTools(w http.ResponseWriter, r *http.Request) {
	defs, _ := ktools.StandardToolSet(ktools.SurfaceOptions{}).Parts()
	out := make([]ToolDTO, 0, len(defs))
	for _, tool := range defs {
		out = append(out, toToolDTO(tool))
	}
	writeJSON(w, ToolListResponse{Tools: out})
}

func (s *Server) getTool(w http.ResponseWriter, r *http.Request, name string) {
	name = strings.TrimSpace(name)
	defs, _ := ktools.StandardToolSet(ktools.SurfaceOptions{}).Parts()
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
	if req.Tool == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_tool_call", "tool이 필요해요")
		return
	}
	var ws *workspace.Workspace
	var err error
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
	_, handlers := ktools.StandardToolSet(ktools.SurfaceOptions{Workspace: ws, WebMaxBytes: req.WebMaxBytes}).Parts()
	args, err := json.Marshal(req.Arguments)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_arguments", err.Error())
		return
	}
	result, err := handlers.Execute(r.Context(), llm.ToolCall{CallID: req.CallID, Name: req.Tool, Arguments: args})
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "tool_call_failed", err.Error())
		return
	}
	output, outputBytes, truncated := truncateToolOutput(result.Output, req.MaxOutputBytes)
	writeJSON(w, ToolCallResponse{CallID: result.CallID, Tool: result.Name, Output: output, Error: result.Error, OutputBytes: outputBytes, OutputTruncated: truncated})
}

func toToolDTO(tool llm.Tool) ToolDTO {
	return ToolDTO{Kind: string(tool.Kind), Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters, ExampleArguments: toolExampleArguments(tool.Name), Strict: tool.Strict, RequiresWorkspace: toolRequiresWorkspace(tool.Name)}
}

func toolExampleArguments(name string) map[string]any {
	switch strings.TrimSpace(name) {
	case "file_read":
		return map[string]any{"path": "README.md", "max_bytes": 4096}
	case "file_write":
		return map[string]any{"path": "notes/todo.md", "content": "할 일을 정리해요\n"}
	case "file_edit":
		return map[string]any{"path": "README.md", "old": "기존 문장", "new": "새 문장", "expected_replacements": 1}
	case "file_apply_patch":
		return map[string]any{"patch_text": "*** Begin Patch\n*** Update File: README.md\n@@\n-기존 문장\n+새 문장\n*** End Patch\n"}
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
	default:
		return nil
	}
}

func toolRequiresWorkspace(name string) bool {
	name = strings.TrimSpace(name)
	return name == "shell_run" || strings.HasPrefix(name, "file_")
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
