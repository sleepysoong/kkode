package gateway

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sleepysoong/kkode/workspace"
)

const defaultFileContentBytes = 1 << 20
const maxFileContentBytes = 8 << 20

type FileEntryDTO struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"`
	Kind    string    `json:"kind"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time,omitempty"`
}

type FileListResponse struct {
	ProjectRoot      string         `json:"project_root"`
	Path             string         `json:"path"`
	Entries          []FileEntryDTO `json:"entries"`
	TotalEntries     int            `json:"total_entries,omitempty"`
	Limit            int            `json:"limit,omitempty"`
	Offset           int            `json:"offset,omitempty"`
	NextOffset       int            `json:"next_offset,omitempty"`
	EntriesTruncated bool           `json:"entries_truncated,omitempty"`
}

type FileContentResponse struct {
	ProjectRoot      string `json:"project_root"`
	Path             string `json:"path"`
	Content          string `json:"content"`
	ContentBytes     int    `json:"content_bytes,omitempty"`
	FileBytes        int64  `json:"file_bytes,omitempty"`
	ContentTruncated bool   `json:"content_truncated,omitempty"`
}

// FileGlobResponse는 웹 패널 파일 팔레트가 glob 결과를 바로 쓰게 해요.
type FileGlobResponse struct {
	ProjectRoot    string   `json:"project_root"`
	Pattern        string   `json:"pattern"`
	Paths          []string `json:"paths"`
	TotalPaths     int      `json:"total_paths,omitempty"`
	Limit          int      `json:"limit,omitempty"`
	Offset         int      `json:"offset,omitempty"`
	NextOffset     int      `json:"next_offset,omitempty"`
	PathsTruncated bool     `json:"paths_truncated,omitempty"`
}

// FileGrepResponse는 웹 패널 검색 결과를 file_grep tool과 같은 의미로 반환해요.
type FileGrepResponse struct {
	ProjectRoot     string             `json:"project_root"`
	Pattern         string             `json:"pattern"`
	PathGlob        string             `json:"path_glob,omitempty"`
	Regex           bool               `json:"regex,omitempty"`
	Matches         []FileGrepMatchDTO `json:"matches"`
	Limit           int                `json:"limit,omitempty"`
	ResultTruncated bool               `json:"result_truncated,omitempty"`
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

type FilePatchRequest struct {
	ProjectRoot string `json:"project_root"`
	PatchText   string `json:"patch_text"`
}

type FilePatchResponse struct {
	ProjectRoot string `json:"project_root"`
	Applied     bool   `json:"applied"`
	PatchBytes  int    `json:"patch_bytes,omitempty"`
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
	if len(parts) == 2 && parts[1] == "patch" && r.Method == http.MethodPost {
		s.applyFilePatch(w, r)
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
	limit, ok := queryLimitParam(w, r, "limit", 500, 5000, "invalid_file_list")
	if !ok {
		return
	}
	offset, ok := queryOffsetParam(w, r, "offset", "invalid_file_list")
	if !ok {
		return
	}
	total := len(entries)
	if offset >= total {
		entries = nil
	} else if offset > 0 {
		entries = entries[offset:]
	}
	truncated := len(entries) > limit
	if truncated {
		entries = entries[:limit]
	}
	returned := len(entries)
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
	writeJSON(w, FileListResponse{ProjectRoot: projectRoot, Path: filepath.ToSlash(rel), Entries: out, TotalEntries: total, Limit: limit, Offset: offset, NextOffset: nextOffset(offset, returned, truncated), EntriesTruncated: truncated})
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
	offsetLine, ok := queryNonNegativeIntParam(w, r, "offset_line", 0, "invalid_file_range")
	if !ok {
		return
	}
	limitLines, ok := queryNonNegativeIntParam(w, r, "limit_lines", 0, "invalid_file_range")
	if !ok {
		return
	}
	maxBytes, ok := queryNonNegativeIntParam(w, r, "max_bytes", defaultFileContentBytes, "invalid_file_range")
	if !ok {
		return
	}
	if maxBytes <= 0 {
		maxBytes = defaultFileContentBytes
	}
	if maxBytes > maxFileContentBytes {
		writeError(w, r, http.StatusBadRequest, "invalid_file_range", fmt.Sprintf("max_bytes는 %d 이하여야 해요", maxFileContentBytes))
		return
	}
	opts := workspace.ReadOptions{OffsetLine: offsetLine, LimitLines: limitLines, MaxBytes: maxBytes}
	fileBytes, statErr := fileSize(ws, rel)
	if statErr != nil {
		writeError(w, r, http.StatusBadRequest, "read_file_failed", statErr.Error())
		return
	}
	content, err := ws.ReadFileRange(rel, opts)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "read_file_failed", err.Error())
		return
	}
	writeJSON(w, FileContentResponse{ProjectRoot: projectRoot, Path: rel, Content: content, ContentBytes: len(content), FileBytes: fileBytes, ContentTruncated: fileContentTruncated(fileBytes, content, opts)})
}

func (s *Server) writeFileContent(w http.ResponseWriter, r *http.Request) {
	var req FileWriteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONDecodeError(w, r, err)
		return
	}
	req.ProjectRoot = strings.TrimSpace(req.ProjectRoot)
	req.Path = strings.TrimSpace(req.Path)
	ws, projectRoot, err := newWorkspace(req.ProjectRoot)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_workspace", err.Error())
		return
	}
	if err := ws.WriteFile(req.Path, req.Content); err != nil {
		writeError(w, r, http.StatusBadRequest, "write_file_failed", err.Error())
		return
	}
	writeJSON(w, FileContentResponse{ProjectRoot: projectRoot, Path: req.Path, Content: req.Content, ContentBytes: len(req.Content), FileBytes: int64(len(req.Content))})
}

func (s *Server) applyFilePatch(w http.ResponseWriter, r *http.Request) {
	var req FilePatchRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONDecodeError(w, r, err)
		return
	}
	if strings.TrimSpace(req.PatchText) == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_patch", "patch_text가 필요해요")
		return
	}
	ws, projectRoot, err := newWorkspace(req.ProjectRoot)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_workspace", err.Error())
		return
	}
	if err := ws.ApplyPatch(req.PatchText); err != nil {
		writeError(w, r, http.StatusBadRequest, "apply_patch_failed", err.Error())
		return
	}
	writeJSON(w, FilePatchResponse{ProjectRoot: projectRoot, Applied: true, PatchBytes: len(req.PatchText)})
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
	limit, ok := queryLimitParam(w, r, "max_matches", 100, 1000, "invalid_file_grep")
	if !ok {
		return
	}
	regex, ok := queryBoolParam(w, r, "regex", false, "invalid_file_grep")
	if !ok {
		return
	}
	caseSensitive, ok := queryBoolParam(w, r, "case_sensitive", false, "invalid_file_grep")
	if !ok {
		return
	}
	opts := workspace.GrepOptions{
		PathGlob:      strings.TrimSpace(r.URL.Query().Get("path_glob")),
		Regex:         regex,
		CaseSensitive: caseSensitive,
		MaxMatches:    limit + 1,
	}
	matches, err := ws.Grep(pattern, opts)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "grep_files_failed", err.Error())
		return
	}
	truncated := len(matches) > limit
	if truncated {
		matches = matches[:limit]
	}
	writeJSON(w, FileGrepResponse{ProjectRoot: projectRoot, Pattern: pattern, PathGlob: opts.PathGlob, Regex: opts.Regex, Matches: fileGrepMatchDTOs(matches), Limit: limit, ResultTruncated: truncated})
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
	limit, ok := queryLimitParam(w, r, "limit", 500, 5000, "invalid_file_glob")
	if !ok {
		return
	}
	offset, ok := queryOffsetParam(w, r, "offset", "invalid_file_glob")
	if !ok {
		return
	}
	total := len(paths)
	if offset >= total {
		paths = nil
	} else if offset > 0 {
		paths = paths[offset:]
	}
	truncated := len(paths) > limit
	if truncated {
		paths = paths[:limit]
	}
	returned := len(paths)
	writeJSON(w, FileGlobResponse{ProjectRoot: projectRoot, Pattern: pattern, Paths: paths, TotalPaths: total, Limit: limit, Offset: offset, NextOffset: nextOffset(offset, returned, truncated), PathsTruncated: truncated})
}

func fileGrepMatchDTOs(matches []workspace.SearchMatch) []FileGrepMatchDTO {
	out := make([]FileGrepMatchDTO, 0, len(matches))
	for _, match := range matches {
		out = append(out, FileGrepMatchDTO{Path: match.Path, Line: match.Line, Excerpt: match.Excerpt})
	}
	return out
}

func fileSize(ws *workspace.Workspace, rel string) (int64, error) {
	path, err := ws.Resolve(rel)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func fileContentTruncated(fileBytes int64, content string, opts workspace.ReadOptions) bool {
	if opts.MaxBytes > 0 && fileBytes > int64(opts.MaxBytes) {
		return true
	}
	if opts.OffsetLine > 0 || opts.LimitLines > 0 {
		return int64(len(content)) < fileBytes
	}
	return false
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
