package gateway

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sleepysoong/kkode/workspace"
)

const defaultFileContentBytes = 1 << 20
const maxFileContentBytes = workspace.MaxFileReadBytes

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
	CheckpointID     string `json:"checkpoint_id,omitempty"`
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

type FileDeleteRequest struct {
	ProjectRoot string `json:"project_root"`
	Path        string `json:"path"`
	Recursive   bool   `json:"recursive,omitempty"`
}

type FileDeleteResponse struct {
	ProjectRoot  string `json:"project_root"`
	Path         string `json:"path"`
	Deleted      bool   `json:"deleted"`
	CheckpointID string `json:"checkpoint_id,omitempty"`
}

type FileMoveRequest struct {
	ProjectRoot string `json:"project_root"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Overwrite   bool   `json:"overwrite,omitempty"`
}

type FileMoveResponse struct {
	ProjectRoot  string `json:"project_root"`
	Source       string `json:"source"`
	Destination  string `json:"destination"`
	Moved        bool   `json:"moved"`
	CheckpointID string `json:"checkpoint_id,omitempty"`
}

type FilePatchRequest struct {
	ProjectRoot string `json:"project_root"`
	PatchText   string `json:"patch_text"`
}

type FilePatchResponse struct {
	ProjectRoot  string `json:"project_root"`
	Applied      bool   `json:"applied"`
	PatchBytes   int    `json:"patch_bytes,omitempty"`
	CheckpointID string `json:"checkpoint_id,omitempty"`
}

type FileRestoreRequest struct {
	ProjectRoot  string `json:"project_root"`
	CheckpointID string `json:"checkpoint_id"`
}

type FileRestoreResponse struct {
	ProjectRoot  string `json:"project_root"`
	CheckpointID string `json:"checkpoint_id"`
	Restored     bool   `json:"restored"`
	Entries      int    `json:"entries,omitempty"`
}

type FileCheckpointListResponse struct {
	ProjectRoot      string              `json:"project_root"`
	Checkpoints      []FileCheckpointDTO `json:"checkpoints"`
	TotalCheckpoints int                 `json:"total_checkpoints,omitempty"`
	Limit            int                 `json:"limit,omitempty"`
	Offset           int                 `json:"offset,omitempty"`
	NextOffset       int                 `json:"next_offset,omitempty"`
	ResultTruncated  bool                `json:"result_truncated,omitempty"`
}

type FileCheckpointDTO struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Entries   int       `json:"entries"`
	Paths     []string  `json:"paths,omitempty"`
}

type FileCheckpointDeleteResponse struct {
	ProjectRoot  string `json:"project_root"`
	CheckpointID string `json:"checkpoint_id"`
	Deleted      bool   `json:"deleted"`
}

type FileCheckpointPruneRequest struct {
	ProjectRoot string `json:"project_root"`
	KeepLatest  int    `json:"keep_latest"`
}

type FileCheckpointPruneResponse struct {
	ProjectRoot      string              `json:"project_root"`
	Deleted          []FileCheckpointDTO `json:"deleted"`
	DeletedCount     int                 `json:"deleted_count"`
	Kept             int                 `json:"kept"`
	TotalCheckpoints int                 `json:"total_checkpoints"`
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
	if len(parts) == 2 && parts[1] == "delete" && r.Method == http.MethodPost {
		s.deleteFilePath(w, r)
		return
	}
	if len(parts) == 2 && parts[1] == "move" && r.Method == http.MethodPost {
		s.moveFilePath(w, r)
		return
	}
	if len(parts) == 2 && parts[1] == "restore" && r.Method == http.MethodPost {
		s.restoreFileCheckpoint(w, r)
		return
	}
	if len(parts) == 2 && parts[1] == "checkpoints" && r.Method == http.MethodGet {
		s.listFileCheckpoints(w, r)
		return
	}
	if len(parts) == 3 && parts[1] == "checkpoints" && parts[2] == "prune" && r.Method == http.MethodPost {
		s.pruneFileCheckpoints(w, r)
		return
	}
	if len(parts) == 3 && parts[1] == "checkpoints" && r.Method == http.MethodGet {
		s.getFileCheckpoint(w, r, parts[2])
		return
	}
	if len(parts) == 3 && parts[1] == "checkpoints" && r.Method == http.MethodDelete {
		s.deleteFileCheckpoint(w, r, parts[2])
		return
	}
	if len(parts) == 1 {
		writeMethodNotAllowed(w, r, "지원하지 않는 files method예요", http.MethodGet)
		return
	}
	if len(parts) == 2 {
		switch parts[1] {
		case "content":
			writeMethodNotAllowed(w, r, "지원하지 않는 file content method예요", http.MethodGet, http.MethodPut)
			return
		case "grep", "glob":
			writeMethodNotAllowed(w, r, "지원하지 않는 files method예요", http.MethodGet)
			return
		case "patch":
			writeMethodNotAllowed(w, r, "지원하지 않는 files method예요", http.MethodPost)
			return
		case "delete", "move", "restore":
			writeMethodNotAllowed(w, r, "지원하지 않는 files method예요", http.MethodPost)
			return
		case "checkpoints":
			writeMethodNotAllowed(w, r, "지원하지 않는 files checkpoint method예요", http.MethodGet)
			return
		}
	}
	if len(parts) == 3 && parts[1] == "checkpoints" && parts[2] == "prune" {
		writeMethodNotAllowed(w, r, "지원하지 않는 files checkpoint prune method예요", http.MethodPost)
		return
	}
	if len(parts) == 3 && parts[1] == "checkpoints" {
		writeMethodNotAllowed(w, r, "지원하지 않는 files checkpoint method예요", http.MethodGet, http.MethodDelete)
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
	limit, ok := queryLimitParam(w, r, "limit", 500, workspace.MaxListEntries, "invalid_file_list")
	if !ok {
		return
	}
	offset, ok := queryOffsetParam(w, r, "offset", "invalid_file_list")
	if !ok {
		return
	}
	entries, workspaceTruncated, err := readBoundedDirEntries(rooted, workspace.MaxListEntries)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "list_files_failed", err.Error())
		return
	}
	total := len(entries)
	if offset >= total {
		entries = nil
	} else if offset > 0 {
		entries = entries[offset:]
	}
	pageTruncated := len(entries) > limit
	if pageTruncated {
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
	writeJSON(w, FileListResponse{ProjectRoot: projectRoot, Path: filepath.ToSlash(rel), Entries: out, TotalEntries: total, Limit: limit, Offset: offset, NextOffset: nextOffset(offset, returned, pageTruncated), EntriesTruncated: pageTruncated || workspaceTruncated})
}

func readBoundedDirEntries(path string, maxEntries int) ([]os.DirEntry, bool, error) {
	dir, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(maxEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	if len(entries) <= maxEntries {
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		return entries, false, nil
	}
	entries = entries[:maxEntries]
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, true, nil
}

func (s *Server) handleFileContent(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.readFileContent(w, r)
	case http.MethodPut:
		s.writeFileContent(w, r)
	default:
		writeMethodNotAllowed(w, r, "지원하지 않는 file content method예요", http.MethodGet, http.MethodPut)
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
	cp, err := ws.CreateCheckpoint([]string{req.Path})
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "file_checkpoint_failed", err.Error())
		return
	}
	if err := ws.WriteFile(req.Path, req.Content); err != nil {
		writeError(w, r, http.StatusBadRequest, "write_file_failed", err.Error())
		return
	}
	writeJSON(w, FileContentResponse{ProjectRoot: projectRoot, Path: req.Path, Content: req.Content, CheckpointID: cp.ID, ContentBytes: len(req.Content), FileBytes: int64(len(req.Content))})
}

func (s *Server) deleteFilePath(w http.ResponseWriter, r *http.Request) {
	var req FileDeleteRequest
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
	cp, err := ws.CreateCheckpoint([]string{req.Path})
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "file_checkpoint_failed", err.Error())
		return
	}
	if err := ws.DeletePath(req.Path, req.Recursive); err != nil {
		writeError(w, r, http.StatusBadRequest, "delete_file_failed", err.Error())
		return
	}
	writeJSON(w, FileDeleteResponse{ProjectRoot: projectRoot, Path: req.Path, Deleted: true, CheckpointID: cp.ID})
}

func (s *Server) moveFilePath(w http.ResponseWriter, r *http.Request) {
	var req FileMoveRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONDecodeError(w, r, err)
		return
	}
	req.ProjectRoot = strings.TrimSpace(req.ProjectRoot)
	req.Source = strings.TrimSpace(req.Source)
	req.Destination = strings.TrimSpace(req.Destination)
	ws, projectRoot, err := newWorkspace(req.ProjectRoot)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_workspace", err.Error())
		return
	}
	cp, err := ws.CreateCheckpoint([]string{req.Source, req.Destination})
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "file_checkpoint_failed", err.Error())
		return
	}
	if err := ws.MovePath(req.Source, req.Destination, req.Overwrite); err != nil {
		writeError(w, r, http.StatusBadRequest, "move_file_failed", err.Error())
		return
	}
	writeJSON(w, FileMoveResponse{ProjectRoot: projectRoot, Source: req.Source, Destination: req.Destination, Moved: true, CheckpointID: cp.ID})
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
	paths, err := ws.PatchPaths(req.PatchText)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_patch", err.Error())
		return
	}
	cp, err := ws.CreateCheckpoint(paths)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "file_checkpoint_failed", err.Error())
		return
	}
	if err := ws.ApplyPatch(req.PatchText); err != nil {
		writeError(w, r, http.StatusBadRequest, "apply_patch_failed", err.Error())
		return
	}
	writeJSON(w, FilePatchResponse{ProjectRoot: projectRoot, Applied: true, PatchBytes: len(req.PatchText), CheckpointID: cp.ID})
}

func (s *Server) restoreFileCheckpoint(w http.ResponseWriter, r *http.Request) {
	var req FileRestoreRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONDecodeError(w, r, err)
		return
	}
	req.ProjectRoot = strings.TrimSpace(req.ProjectRoot)
	req.CheckpointID = strings.TrimSpace(req.CheckpointID)
	if req.CheckpointID == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_checkpoint", "checkpoint_id가 필요해요")
		return
	}
	ws, projectRoot, err := newWorkspace(req.ProjectRoot)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_workspace", err.Error())
		return
	}
	cp, err := ws.RestoreCheckpoint(req.CheckpointID)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "restore_file_checkpoint_failed", err.Error())
		return
	}
	writeJSON(w, FileRestoreResponse{ProjectRoot: projectRoot, CheckpointID: cp.ID, Restored: true, Entries: len(cp.Entries)})
}

func (s *Server) listFileCheckpoints(w http.ResponseWriter, r *http.Request) {
	ws, projectRoot, ok := workspaceFromQuery(w, r)
	if !ok {
		return
	}
	limit, ok := queryLimitParam(w, r, "limit", 50, 500, "invalid_file_checkpoint_list")
	if !ok {
		return
	}
	offset, ok := queryOffsetParam(w, r, "offset", "invalid_file_checkpoint_list")
	if !ok {
		return
	}
	pathFilter, ok := fileCheckpointPathFilter(w, r, ws)
	if !ok {
		return
	}
	items, err := ws.ListCheckpoints()
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "list_file_checkpoints_failed", err.Error())
		return
	}
	if pathFilter != "" {
		items = filterFileCheckpointsByPath(items, pathFilter)
	}
	total := len(items)
	page, returned, truncated := pageSlice(items, limit, offset)
	writeJSON(w, FileCheckpointListResponse{ProjectRoot: projectRoot, Checkpoints: fileCheckpointDTOs(page), TotalCheckpoints: total, Limit: limit, Offset: offset, NextOffset: nextOffset(offset, returned, truncated), ResultTruncated: truncated})
}

func (s *Server) getFileCheckpoint(w http.ResponseWriter, r *http.Request, checkpointID string) {
	ws, projectRoot, ok := workspaceFromQuery(w, r)
	if !ok {
		return
	}
	cp, err := ws.LoadCheckpoint(strings.TrimSpace(checkpointID))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "file_checkpoint_not_found", err.Error())
		return
	}
	writeJSON(w, FileCheckpointListResponse{ProjectRoot: projectRoot, Checkpoints: []FileCheckpointDTO{fileCheckpointDTO(workspace.FileCheckpointSummary{ID: cp.ID, CreatedAt: cp.CreatedAt, Entries: len(cp.Entries), Paths: fileCheckpointPaths(cp)})}, TotalCheckpoints: 1, Limit: 1})
}

func (s *Server) deleteFileCheckpoint(w http.ResponseWriter, r *http.Request, checkpointID string) {
	ws, projectRoot, ok := workspaceFromQuery(w, r)
	if !ok {
		return
	}
	checkpointID = strings.TrimSpace(checkpointID)
	if err := ws.DeleteCheckpoint(checkpointID); err != nil {
		writeError(w, r, http.StatusBadRequest, "delete_file_checkpoint_failed", err.Error())
		return
	}
	writeJSON(w, FileCheckpointDeleteResponse{ProjectRoot: projectRoot, CheckpointID: checkpointID, Deleted: true})
}

func (s *Server) pruneFileCheckpoints(w http.ResponseWriter, r *http.Request) {
	var req FileCheckpointPruneRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONDecodeError(w, r, err)
		return
	}
	req.ProjectRoot = strings.TrimSpace(req.ProjectRoot)
	ws, projectRoot, err := newWorkspace(req.ProjectRoot)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_workspace", err.Error())
		return
	}
	result, err := ws.PruneCheckpoints(req.KeepLatest)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "prune_file_checkpoints_failed", err.Error())
		return
	}
	deleted := fileCheckpointDTOs(result.Deleted)
	writeJSON(w, FileCheckpointPruneResponse{ProjectRoot: projectRoot, Deleted: deleted, DeletedCount: len(deleted), Kept: result.Kept, TotalCheckpoints: result.TotalBefore})
}

func fileCheckpointDTOs(items []workspace.FileCheckpointSummary) []FileCheckpointDTO {
	out := make([]FileCheckpointDTO, 0, len(items))
	for _, item := range items {
		out = append(out, fileCheckpointDTO(item))
	}
	return out
}

func fileCheckpointDTO(item workspace.FileCheckpointSummary) FileCheckpointDTO {
	return FileCheckpointDTO{ID: item.ID, CreatedAt: item.CreatedAt, Entries: item.Entries, Paths: item.Paths}
}

func fileCheckpointPathFilter(w http.ResponseWriter, r *http.Request, ws *workspace.Workspace) (string, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("path"))
	if raw == "" {
		return "", true
	}
	abs, err := ws.Resolve(raw)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_file_checkpoint_list", err.Error())
		return "", false
	}
	rel, err := filepath.Rel(ws.Root, abs)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_file_checkpoint_list", err.Error())
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func filterFileCheckpointsByPath(items []workspace.FileCheckpointSummary, path string) []workspace.FileCheckpointSummary {
	out := items[:0]
	for _, item := range items {
		if fileCheckpointContainsPath(item, path) {
			out = append(out, item)
		}
	}
	return out
}

func fileCheckpointContainsPath(item workspace.FileCheckpointSummary, path string) bool {
	for _, candidate := range item.Paths {
		if candidate == path {
			return true
		}
	}
	return false
}

func fileCheckpointPaths(cp workspace.FileCheckpoint) []string {
	paths := make([]string, 0, len(cp.Entries))
	for _, entry := range cp.Entries {
		paths = append(paths, entry.Path)
	}
	return paths
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
	limit, ok := queryLimitParam(w, r, "max_matches", 100, workspace.MaxGrepMatches, "invalid_file_grep")
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
	maxMatches := limit
	if limit < workspace.MaxGrepMatches {
		maxMatches = limit + 1
	}
	opts := workspace.GrepOptions{
		PathGlob:      strings.TrimSpace(r.URL.Query().Get("path_glob")),
		Regex:         regex,
		CaseSensitive: caseSensitive,
		MaxMatches:    maxMatches,
	}
	matches, err := ws.Grep(pattern, opts)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "grep_files_failed", err.Error())
		return
	}
	truncated := len(matches) > limit || len(matches) == workspace.MaxGrepMatches
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
	limit, ok := queryLimitParam(w, r, "limit", 500, workspace.MaxGlobMatches, "invalid_file_glob")
	if !ok {
		return
	}
	offset, ok := queryOffsetParam(w, r, "offset", "invalid_file_glob")
	if !ok {
		return
	}
	total := len(paths)
	workspaceTruncated := total == workspace.MaxGlobMatches
	if offset >= total {
		paths = nil
	} else if offset > 0 {
		paths = paths[offset:]
	}
	pageTruncated := len(paths) > limit
	if pageTruncated {
		paths = paths[:limit]
	}
	returned := len(paths)
	writeJSON(w, FileGlobResponse{ProjectRoot: projectRoot, Pattern: pattern, Paths: paths, TotalPaths: total, Limit: limit, Offset: offset, NextOffset: nextOffset(offset, returned, pageTruncated), PathsTruncated: pageTruncated || workspaceTruncated})
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
