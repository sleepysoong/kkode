package gateway

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type GitStatusEntryDTO struct {
	XY   string `json:"xy"`
	Path string `json:"path"`
	Raw  string `json:"raw"`
}

type GitStatusResponse struct {
	ProjectRoot      string              `json:"project_root"`
	Branch           string              `json:"branch,omitempty"`
	Entries          []GitStatusEntryDTO `json:"entries"`
	TotalEntries     int                 `json:"total_entries,omitempty"`
	Limit            int                 `json:"limit,omitempty"`
	EntriesTruncated bool                `json:"entries_truncated,omitempty"`
	OutputTruncated  bool                `json:"output_truncated,omitempty"`
}

type GitDiffResponse struct {
	ProjectRoot string `json:"project_root"`
	Path        string `json:"path,omitempty"`
	Diff        string `json:"diff"`
	Truncated   bool   `json:"truncated,omitempty"`
}

type GitLogEntryDTO struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
	Raw     string `json:"raw"`
}

type GitLogResponse struct {
	ProjectRoot string           `json:"project_root"`
	Commits     []GitLogEntryDTO `json:"commits"`
}

func (s *Server) handleGit(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) != 2 {
		writeError(w, r, http.StatusNotFound, "not_found", "git endpoint를 찾을 수 없어요")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 git method예요")
		return
	}
	switch parts[1] {
	case "status":
		s.gitStatus(w, r)
	case "diff":
		s.gitDiff(w, r)
	case "log":
		s.gitLog(w, r)
	default:
		writeError(w, r, http.StatusNotFound, "not_found", "git endpoint를 찾을 수 없어요")
	}
}

func (s *Server) gitStatus(w http.ResponseWriter, r *http.Request) {
	_, root, ok := workspaceFromQuery(w, r)
	if !ok {
		return
	}
	limit := queryLimit(r, "limit", 500, 5000)
	out, outputTruncated, err := runGitCommand(r.Context(), root, []string{"status", "--short", "--branch"}, 512*1024)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "git_status_failed", err.Error())
		return
	}
	writeJSON(w, limitGitStatus(parseGitStatus(root, out), limit, outputTruncated))
}

func (s *Server) gitDiff(w http.ResponseWriter, r *http.Request) {
	ws, root, ok := workspaceFromQuery(w, r)
	if !ok {
		return
	}
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	args := []string{"diff", "--"}
	if rel != "" {
		if filepath.IsAbs(rel) {
			writeError(w, r, http.StatusBadRequest, "invalid_path", "git diff path는 project_root 기준 상대 경로여야 해요")
			return
		}
		if _, err := ws.Resolve(rel); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_path", err.Error())
			return
		}
		args = append(args, filepath.ToSlash(filepath.Clean(rel)))
	}
	maxBytes := queryLimit(r, "max_bytes", 1<<20, 4<<20)
	out, truncated, err := runGitCommand(r.Context(), root, args, int64(maxBytes))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "git_diff_failed", err.Error())
		return
	}
	writeJSON(w, GitDiffResponse{ProjectRoot: root, Path: filepath.ToSlash(rel), Diff: out, Truncated: truncated})
}

func (s *Server) gitLog(w http.ResponseWriter, r *http.Request) {
	_, root, ok := workspaceFromQuery(w, r)
	if !ok {
		return
	}
	limit := queryLimit(r, "limit", 20, 100)
	out, _, err := runGitCommand(r.Context(), root, []string{"log", "--oneline", "-n", fmt.Sprint(limit)}, 512*1024)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "git_log_failed", err.Error())
		return
	}
	writeJSON(w, GitLogResponse{ProjectRoot: root, Commits: parseGitLog(out)})
}

func parseGitStatus(root string, out string) GitStatusResponse {
	resp := GitStatusResponse{ProjectRoot: root}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			resp.Branch = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			continue
		}
		xy := line
		path := strings.TrimSpace(line)
		if len(line) >= 3 {
			xy = line[:2]
			path = strings.TrimSpace(line[3:])
		}
		resp.Entries = append(resp.Entries, GitStatusEntryDTO{XY: xy, Path: path, Raw: line})
	}
	return resp
}

func limitGitStatus(resp GitStatusResponse, limit int, outputTruncated bool) GitStatusResponse {
	resp.TotalEntries = len(resp.Entries)
	resp.Limit = limit
	resp.OutputTruncated = outputTruncated
	if limit > 0 && len(resp.Entries) > limit {
		resp.Entries = resp.Entries[:limit]
		resp.EntriesTruncated = true
	}
	if outputTruncated {
		resp.EntriesTruncated = true
	}
	return resp
}

func parseGitLog(out string) []GitLogEntryDTO {
	var commits []GitLogEntryDTO
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		hash, subject, ok := strings.Cut(line, " ")
		if !ok {
			hash = line
		}
		commits = append(commits, GitLogEntryDTO{Hash: hash, Subject: strings.TrimSpace(subject), Raw: line})
	}
	return commits
}

func runGitCommand(ctx context.Context, root string, args []string, maxBytes int64) (string, bool, error) {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	stdout := &boundedBuffer{max: maxBytes}
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", stdout.truncated, fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.String(), stdout.truncated, nil
}

type boundedBuffer struct {
	buf       bytes.Buffer
	max       int64
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.max <= 0 {
		b.max = 1 << 20
	}
	remaining := b.max - int64(b.buf.Len())
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if int64(len(p)) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	data := b.buf.Bytes()
	if utf8.Valid(data) {
		return string(data)
	}
	end := len(data)
	for end > 0 && !utf8.Valid(data[:end]) {
		end--
	}
	return string(data[:end])
}
