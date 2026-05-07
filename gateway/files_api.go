package gateway

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sleepysoong/kkode/workspace"
)

type FileEntryDTO struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Kind    string    `json:"kind"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time,omitempty"`
}

type FileListResponse struct {
	ProjectRoot string         `json:"project_root"`
	Path        string         `json:"path"`
	Entries     []FileEntryDTO `json:"entries"`
}

type FileContentResponse struct {
	ProjectRoot string `json:"project_root"`
	Path        string `json:"path"`
	Content     string `json:"content"`
}

// FileGlobResponse는 웹 패널 파일 팔레트가 glob 결과를 바로 쓰게 해요.
type FileGlobResponse struct {
	ProjectRoot string   `json:"project_root"`
	Pattern     string   `json:"pattern"`
	Paths       []string `json:"paths"`
}

// FileGrepResponse는 웹 패널 검색 결과를 file_grep tool과 같은 의미로 반환해요.
type FileGrepResponse struct {
	ProjectRoot string             `json:"project_root"`
	Pattern     string             `json:"pattern"`
	PathGlob    string             `json:"path_glob,omitempty"`
	Regex       bool               `json:"regex,omitempty"`
	Matches     []FileGrepMatchDTO `json:"matches"`
}

type FileGrepMatchDTO struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Excerpt string `json:"excerpt"`
}

type FileWriteRequest struct {
	ProjectRoot string `json:"project_root"`
	Path        string `json:"path"`
	Content     string `json:"content"`
}

func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 1 && r.Method == http.MethodGet {
		s.listFiles(w, r)
		return
	}
	if len(parts) == 2 && parts[1] == "content" {
		s.handleFileContent(w, r)
		return
	}
	if len(parts) == 2 && parts[1] == "grep" && r.Method == http.MethodGet {
		s.grepFiles(w, r)
		return
	}
	if len(parts) == 2 && parts[1] == "glob" && r.Method == http.MethodGet {
		s.globFiles(w, r)
		return
	}
	if len(parts) == 1 || (len(parts) == 2 && parts[1] == "content") {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 files method예요")
		return
	}
	writeError(w, r, http.StatusNotFound, "not_found", "files endpoint를 찾을 수 없어요")
}

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	ws, projectRoot, ok := workspaceFromQuery(w, r)
	if !ok {
		return
	}
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	if rel == "" {
		rel = "."
	}
	rooted, err := ws.Resolve(rel)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_path", err.Error())
		return
	}
	entries, err := os.ReadDir(rooted)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "list_files_failed", err.Error())
		return
	}
	out := make([]FileEntryDTO, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		childRel := filepath.ToSlash(filepath.Join(rel, entry.Name()))
		kind := "file"
		if entry.IsDir() {
			kind = "directory"
		}
		out = append(out, FileEntryDTO{Name: entry.Name(), Path: childRel, Kind: kind, Size: info.Size(), ModTime: info.ModTime().UTC()})
	}
	writeJSON(w, FileListResponse{ProjectRoot: projectRoot, Path: filepath.ToSlash(rel), Entries: out})
}

func (s *Server) handleFileContent(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.readFileContent(w, r)
	case http.MethodPut:
		s.writeFileContent(w, r)
	default:
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 file content method예요")
	}
}

func (s *Server) readFileContent(w http.ResponseWriter, r *http.Request) {
	ws, projectRoot, ok := workspaceFromQuery(w, r)
	if !ok {
		return
	}
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	content, err := ws.ReadFileRange(rel, workspace.ReadOptions{OffsetLine: queryInt(r, "offset_line", 0), LimitLines: queryInt(r, "limit_lines", 0), MaxBytes: queryInt(r, "max_bytes", 0)})
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "read_file_failed", err.Error())
		return
	}
	writeJSON(w, FileContentResponse{ProjectRoot: projectRoot, Path: rel, Content: content})
}

func (s *Server) writeFileContent(w http.ResponseWriter, r *http.Request) {
	var req FileWriteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONDecodeError(w, r, err)
		return
	}
	ws, projectRoot, err := newWorkspace(req.ProjectRoot)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_workspace", err.Error())
		return
	}
	if err := ws.WriteFile(req.Path, req.Content); err != nil {
		writeError(w, r, http.StatusBadRequest, "write_file_failed", err.Error())
		return
	}
	writeJSON(w, FileContentResponse{ProjectRoot: projectRoot, Path: req.Path, Content: req.Content})
}

func (s *Server) grepFiles(w http.ResponseWriter, r *http.Request) {
	ws, projectRoot, ok := workspaceFromQuery(w, r)
	if !ok {
		return
	}
	pattern := strings.TrimSpace(r.URL.Query().Get("pattern"))
	if pattern == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_grep", "pattern이 필요해요")
		return
	}
	opts := workspace.GrepOptions{
		PathGlob:      strings.TrimSpace(r.URL.Query().Get("path_glob")),
		Regex:         queryBool(r, "regex", false),
		CaseSensitive: queryBool(r, "case_sensitive", false),
		MaxMatches:    queryLimit(r, "max_matches", 100, 1000),
	}
	matches, err := ws.Grep(pattern, opts)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "grep_files_failed", err.Error())
		return
	}
	writeJSON(w, FileGrepResponse{ProjectRoot: projectRoot, Pattern: pattern, PathGlob: opts.PathGlob, Regex: opts.Regex, Matches: fileGrepMatchDTOs(matches)})
}

func (s *Server) globFiles(w http.ResponseWriter, r *http.Request) {
	ws, projectRoot, ok := workspaceFromQuery(w, r)
	if !ok {
		return
	}
	pattern := strings.TrimSpace(r.URL.Query().Get("pattern"))
	if pattern == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_glob", "pattern이 필요해요")
		return
	}
	paths, err := ws.Glob(pattern)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "glob_files_failed", err.Error())
		return
	}
	limit := queryLimit(r, "limit", 500, 5000)
	if len(paths) > limit {
		paths = paths[:limit]
	}
	writeJSON(w, FileGlobResponse{ProjectRoot: projectRoot, Pattern: pattern, Paths: paths})
}

func fileGrepMatchDTOs(matches []workspace.SearchMatch) []FileGrepMatchDTO {
	out := make([]FileGrepMatchDTO, 0, len(matches))
	for _, match := range matches {
		out = append(out, FileGrepMatchDTO{Path: match.Path, Line: match.Line, Excerpt: match.Excerpt})
	}
	return out
}

func workspaceFromQuery(w http.ResponseWriter, r *http.Request) (*workspace.Workspace, string, bool) {
	projectRoot := strings.TrimSpace(r.URL.Query().Get("project_root"))
	ws, absRoot, err := newWorkspace(projectRoot)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_workspace", err.Error())
		return nil, "", false
	}
	return ws, absRoot, true
}

func newWorkspace(projectRoot string) (*workspace.Workspace, string, error) {
	projectRoot = strings.TrimSpace(projectRoot)
	if projectRoot == "" {
		return nil, "", os.ErrInvalid
	}
	ws, err := workspace.New(projectRoot)
	if err != nil {
		return nil, "", err
	}
	return ws, ws.Root, nil
}
