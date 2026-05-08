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

const defaultSkillPreviewBytes = 65536
const maxSkillPreviewBytes = 1 << 20

type SkillPreviewResponse struct {
	Skill             ResourceDTO `json:"skill"`
	Directory         string      `json:"directory,omitempty"`
	File              string      `json:"file,omitempty"`
	Markdown          string      `json:"markdown,omitempty"`
	MarkdownBytes     int         `json:"markdown_bytes,omitempty"`
	MarkdownTruncated bool        `json:"markdown_truncated,omitempty"`
	Truncated         bool        `json:"truncated"`
}

type skillPreviewConfig struct {
	Path      string `json:"path"`
	Directory string `json:"directory"`
}

func (s *Server) previewSkill(w http.ResponseWriter, r *http.Request, skillID string) {
	maxBytes, ok := queryNonNegativeIntParam(w, r, "max_bytes", defaultSkillPreviewBytes, "invalid_skill_preview")
	if !ok {
		return
	}
	if maxBytes > maxSkillPreviewBytes {
		writeError(w, r, http.StatusBadRequest, "invalid_skill_preview", fmt.Sprintf("max_bytes는 %d 이하여야 해요", maxSkillPreviewBytes))
		return
	}
	s.withResource(w, r, session.ResourceSkill, skillID, func(resource session.Resource) {
		preview, err := readSkillPreview(resource, maxBytes)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "skill_preview_failed", err.Error())
			return
		}
		writeJSON(w, preview)
	})
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
		maxBytes = defaultSkillPreviewBytes
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return SkillPreviewResponse{}, err
	}
	markdown, markdownBytes, truncated := truncateToolOutput(string(data), maxBytes)
	return SkillPreviewResponse{Skill: publicResourceDTO(resource), Directory: dir, File: file, Markdown: markdown, MarkdownBytes: markdownBytes, MarkdownTruncated: truncated, Truncated: truncated}, nil
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
