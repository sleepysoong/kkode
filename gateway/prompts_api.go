package gateway

import (
	"net/http"
	"strings"

	"github.com/sleepysoong/kkode/prompts"
)

type PromptTemplateDTO struct {
	Name string `json:"name"`
}

type PromptTemplateListResponse struct {
	Prompts []PromptTemplateDTO `json:"prompts"`
}

type PromptTemplateResponse struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

type PromptRenderRequest struct {
	Data map[string]any `json:"data"`
}

type PromptRenderResponse struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

func (s *Server) handlePrompts(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 prompts method예요")
			return
		}
		s.listPromptTemplates(w, r)
		return
	}
	name := strings.TrimSpace(parts[1])
	if len(parts) == 2 && r.Method == http.MethodGet {
		s.getPromptTemplate(w, r, name)
		return
	}
	if len(parts) == 3 && parts[2] == "render" && r.Method == http.MethodPost {
		s.renderPromptTemplate(w, r, name)
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
	writeJSON(w, PromptTemplateListResponse{Prompts: out})
}

func (s *Server) getPromptTemplate(w http.ResponseWriter, r *http.Request, name string) {
	text, err := prompts.Text(name)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "prompt_not_found", err.Error())
		return
	}
	writeJSON(w, PromptTemplateResponse{Name: name, Text: text})
}

func (s *Server) renderPromptTemplate(w http.ResponseWriter, r *http.Request, name string) {
	var req PromptRenderRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	text, err := prompts.Render(name, req.Data)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "render_prompt_failed", err.Error())
		return
	}
	writeJSON(w, PromptRenderResponse{Name: name, Text: text})
}
