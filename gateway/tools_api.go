package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/sleepysoong/kkode/llm"
	ktools "github.com/sleepysoong/kkode/tools"
)

// ToolDTO는 gateway가 외부 adapter에 노출하는 표준 tool 정의예요.
type ToolDTO struct {
	Kind        string         `json:"kind"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Strict      *bool          `json:"strict,omitempty"`
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

func (s *Server) callTool(w http.ResponseWriter, r *http.Request) {
	var req ToolCallRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONDecodeError(w, r, err)
		return
	}
	req.ProjectRoot = strings.TrimSpace(req.ProjectRoot)
	req.Tool = strings.TrimSpace(req.Tool)
	if req.ProjectRoot == "" || req.Tool == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_tool_call", "project_root와 tool이 필요해요")
		return
	}
	ws, _, err := newWorkspace(req.ProjectRoot)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_workspace", err.Error())
		return
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
	return ToolDTO{Kind: string(tool.Kind), Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters, Strict: tool.Strict}
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
