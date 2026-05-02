package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/sleepysoong/kkode/session"
)

type SkillPreviewResponse struct {
	Skill     ResourceDTO `json:"skill"`
	Directory string      `json:"directory,omitempty"`
	File      string      `json:"file,omitempty"`
	Markdown  string      `json:"markdown,omitempty"`
	Truncated bool        `json:"truncated"`
}

type skillPreviewConfig struct {
	Path      string `json:"path"`
	Directory string `json:"directory"`
}

func (s *Server) previewSkill(w http.ResponseWriter, r *http.Request, skillID string) {
	store := s.resourceStore()
	if store == nil {
		writeError(w, r, http.StatusNotImplemented, "resource_store_missing", "이 gateway에는 resource store가 연결되지 않았어요")
		return
	}
	resource, err := store.LoadResource(r.Context(), session.ResourceSkill, skillID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "resource_not_found", err.Error())
		return
	}
	preview, err := readSkillPreview(resource, queryInt(r, "max_bytes", 65536))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "skill_preview_failed", err.Error())
		return
	}
	writeJSON(w, preview)
}

func readSkillPreview(resource session.Resource, maxBytes int) (SkillPreviewResponse, error) {
	var cfg skillPreviewConfig
	if len(resource.Config) > 0 {
		if err := json.Unmarshal(resource.Config, &cfg); err != nil {
			return SkillPreviewResponse{}, err
		}
	}
	dir := strings.TrimSpace(firstNonEmptyString(cfg.Path, cfg.Directory))
	if dir == "" {
		return SkillPreviewResponse{}, fmt.Errorf("skill path 또는 directory가 필요해요")
	}
	info, err := os.Stat(dir)
	if err != nil {
		return SkillPreviewResponse{}, err
	}
	file := dir
	if info.IsDir() {
		file = firstExistingFile(filepath.Join(dir, "SKILL.md"), filepath.Join(dir, "README.md"), filepath.Join(dir, "skill.md"))
		if file == "" {
			return SkillPreviewResponse{}, fmt.Errorf("skill directory에 SKILL.md 또는 README.md가 필요해요: %s", dir)
		}
	}
	if maxBytes <= 0 {
		maxBytes = 65536
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return SkillPreviewResponse{}, err
	}
	truncated := len(data) > maxBytes
	if truncated {
		data = data[:maxBytes]
	}
	return SkillPreviewResponse{Skill: toResourceDTO(resource), Directory: dir, File: file, Markdown: string(data), Truncated: truncated}, nil
}

func firstExistingFile(paths ...string) string {
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
