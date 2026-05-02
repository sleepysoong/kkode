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
		writeError(w, r, http.StatusBadRequest, "invalid_json", err.Error())
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
