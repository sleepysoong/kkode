package gateway

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/sleepysoong/kkode/prompts"
)

type PromptTemplateDTO struct {
	Name string `json:"name"`
}

type PromptTemplateListResponse struct {
	Prompts         []PromptTemplateDTO `json:"prompts"`
	TotalPrompts    int                 `json:"total_prompts,omitempty"`
	Limit           int                 `json:"limit,omitempty"`
	Offset          int                 `json:"offset,omitempty"`
	NextOffset      int                 `json:"next_offset,omitempty"`
	ResultTruncated bool                `json:"result_truncated,omitempty"`
}

type PromptTemplateResponse struct {
	Name          string `json:"name"`
	Text          string `json:"text"`
	TextBytes     int    `json:"text_bytes,omitempty"`
	TextTruncated bool   `json:"text_truncated,omitempty"`
}

type PromptRenderRequest struct {
	Data         map[string]any `json:"data"`
	MaxTextBytes int            `json:"max_text_bytes,omitempty"`
}

type PromptRenderResponse struct {
	Name          string `json:"name"`
	Text          string `json:"text"`
	TextBytes     int    `json:"text_bytes,omitempty"`
	TextTruncated bool   `json:"text_truncated,omitempty"`
}

const (
	defaultPromptTextBytes     = 65536
	maxPromptTextBytes         = 1 << 20
	maxPromptTemplateNameBytes = 128
)

func (s *Server) handlePrompts(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, r, "지원하지 않는 prompts method예요", http.MethodGet)
			return
		}
		s.listPromptTemplates(w, r)
		return
	}
	name := strings.TrimSpace(parts[1])
	if err := validatePromptTemplateName(name); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_prompt", err.Error())
		return
	}
	if len(parts) == 2 && r.Method == http.MethodGet {
		s.getPromptTemplate(w, r, name)
		return
	}
	if len(parts) == 3 && parts[2] == "render" && r.Method == http.MethodPost {
		s.renderPromptTemplate(w, r, name)
		return
	}
	if len(parts) == 2 {
		writeMethodNotAllowed(w, r, "지원하지 않는 prompts method예요", http.MethodGet)
		return
	}
	if len(parts) == 3 && parts[2] == "render" {
		writeMethodNotAllowed(w, r, "지원하지 않는 prompts method예요", http.MethodPost)
		return
	}
	writeError(w, r, http.StatusNotFound, "not_found", "prompt endpoint를 찾을 수 없어요")
}

func (s *Server) listPromptTemplates(w http.ResponseWriter, r *http.Request) {
	names, err := prompts.List()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "list_prompts_failed", err.Error())
		return
	}
	out := make([]PromptTemplateDTO, 0, len(names))
	for _, name := range names {
		out = append(out, PromptTemplateDTO{Name: name})
	}
	limit, ok := queryLimitParam(w, r, "limit", len(out), 500, "invalid_prompt_list")
	if !ok {
		return
	}
	offset, ok := queryOffsetParam(w, r, "offset", "invalid_prompt_list")
	if !ok {
		return
	}
	page, returned, truncated := pageSlice(out, limit, offset)
	writeJSON(w, PromptTemplateListResponse{Prompts: page, TotalPrompts: len(out), Limit: limit, Offset: offset, NextOffset: nextOffset(offset, returned, truncated), ResultTruncated: truncated})
}

func (s *Server) getPromptTemplate(w http.ResponseWriter, r *http.Request, name string) {
	text, err := prompts.Text(name)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "prompt_not_found", err.Error())
		return
	}
	maxTextBytes, ok := queryNonNegativeLimitParam(w, r, "max_text_bytes", defaultPromptTextBytes, maxPromptTextBytes, "invalid_prompt")
	if !ok {
		return
	}
	text, textBytes, truncated := truncateToolOutput(text, maxTextBytes)
	writeJSON(w, PromptTemplateResponse{Name: name, Text: text, TextBytes: textBytes, TextTruncated: truncated})
}

func (s *Server) renderPromptTemplate(w http.ResponseWriter, r *http.Request, name string) {
	var req PromptRenderRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONDecodeError(w, r, err)
		return
	}
	if req.MaxTextBytes < 0 {
		writeError(w, r, http.StatusBadRequest, "invalid_prompt_render", "max_text_bytes는 0 이상이어야 해요")
		return
	}
	text, err := prompts.Render(name, req.Data)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "render_prompt_failed", err.Error())
		return
	}
	text, textBytes, truncated := truncateToolOutput(text, promptRenderTextLimit(req.MaxTextBytes))
	writeJSON(w, PromptRenderResponse{Name: name, Text: text, TextBytes: textBytes, TextTruncated: truncated})
}

func promptRenderTextLimit(limit int) int {
	if limit <= 0 {
		return defaultPromptTextBytes
	}
	if limit > maxPromptTextBytes {
		return maxPromptTextBytes
	}
	return limit
}

func validatePromptTemplateName(name string) error {
	if name == "" {
		return fmt.Errorf("prompt template 이름이 필요해요")
	}
	if len(name) > maxPromptTemplateNameBytes {
		return fmt.Errorf("prompt template 이름은 %d byte 이하여야 해요", maxPromptTemplateNameBytes)
	}
	return nil
}
