package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/workspace"
)

func TestFileToolsReadWriteAndGrep(t *testing.T) {
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defs, handlers := FileTools(ws)
	if len(defs) < 6 {
		t.Fatalf("defs=%d", len(defs))
	}
	ctx := context.Background()
	if _, err := handlers.Execute(ctx, llm.ToolCall{Name: "file_write", CallID: "1", Arguments: []byte(`{"path":"a.txt","content":"one\ntwo needle\nthree"}`)}); err != nil {
		t.Fatal(err)
	}
	read, err := handlers.Execute(ctx, llm.ToolCall{Name: "file_read", CallID: "2", Arguments: []byte(`{"path":"a.txt","offset_line":2,"limit_lines":1}`)})
	if err != nil || read.Output != "two needle" {
		t.Fatalf("read=%#v err=%v", read, err)
	}
	grep, err := handlers.Execute(ctx, llm.ToolCall{Name: "file_grep", CallID: "3", Arguments: []byte(`{"pattern":"needle","path_glob":"*.txt"}`)})
	if err != nil || !strings.Contains(grep.Output, "needle") {
		t.Fatalf("grep=%#v err=%v", grep, err)
	}
	if _, err := handlers.Execute(ctx, llm.ToolCall{Name: "file_apply_patch", CallID: "4", Arguments: []byte(`{"patch_text":"*** Begin Patch\n*** Update File: a.txt\n@@\n one\n-two needle\n+two patched\n three\n*** End Patch\n"}`)}); err != nil {
		t.Fatal(err)
	}
	patched, err := ws.ReadFile("a.txt")
	if err != nil || !strings.Contains(patched, "two patched") {
		t.Fatalf("patched=%q err=%v", patched, err)
	}
	if _, err := handlers.Execute(ctx, llm.ToolCall{Name: "file_move", CallID: "5", Arguments: []byte(`{"source":"a.txt","destination":"archive/a.txt"}`)}); err != nil {
		t.Fatal(err)
	}
	moved, err := ws.ReadFile("archive/a.txt")
	if err != nil || !strings.Contains(moved, "two patched") {
		t.Fatalf("moved=%q err=%v", moved, err)
	}
	deleteResult, err := handlers.Execute(ctx, llm.ToolCall{Name: "file_delete", CallID: "6", Arguments: []byte(`{"path":"archive/a.txt"}`)})
	if err != nil {
		t.Fatal(err)
	}
	deleteCheckpoint := checkpointIDFromToolOutput(t, deleteResult.Output)
	if _, err := ws.ReadFile("archive/a.txt"); err == nil {
		t.Fatal("file_delete should remove the file")
	}
	if _, err := handlers.Execute(ctx, llm.ToolCall{Name: "file_restore_checkpoint", CallID: "restore", Arguments: []byte(`{"checkpoint_id":"` + deleteCheckpoint + `"}`)}); err != nil {
		t.Fatal(err)
	}
	restored, err := ws.ReadFile("archive/a.txt")
	if err != nil || !strings.Contains(restored, "two patched") {
		t.Fatalf("restore should recover deleted file: %q err=%v", restored, err)
	}
	pruned, err := handlers.Execute(ctx, llm.ToolCall{Name: "file_prune_checkpoints", CallID: "prune", Arguments: []byte(`{"keep_latest":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	var pruneResult workspace.FileCheckpointPruneResult
	if err := json.Unmarshal([]byte(pruned.Output), &pruneResult); err != nil {
		t.Fatal(err)
	}
	if pruneResult.Kept != 1 || len(pruneResult.Deleted) == 0 {
		t.Fatalf("file_prune_checkpoints result가 이상해요: %+v", pruneResult)
	}
	shell, err := handlers.Execute(ctx, llm.ToolCall{Name: "shell_run", CallID: "7", Arguments: []byte(`{"command":"sh","args":["-c","echo out; echo err >&2; exit 7"],"timeout_ms":1000}`)})
	if err != nil {
		t.Fatalf("non-zero shell command should return structured output: %v", err)
	}
	var cmd workspace.CommandResult
	if err := json.Unmarshal([]byte(shell.Output), &cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.ExitCode != 7 || cmd.Stdout != "out\n" || !strings.Contains(cmd.Stderr, "err") || cmd.DurationMS < 0 {
		t.Fatalf("shell_run result가 이상해요: %#v", cmd)
	}
	if _, err := handlers.Execute(ctx, llm.ToolCall{Name: "shell_run", CallID: "8", Arguments: []byte(`{"command":"definitely-missing-kkode-command","timeout_ms":1000}`)}); err == nil || !strings.Contains(err.Error(), "definitely-missing-kkode-command") {
		t.Fatalf("missing shell command should remain a tool error: %v", err)
	}
}

func checkpointIDFromToolOutput(t *testing.T, output string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "checkpoint_id:") {
			id := strings.TrimSpace(strings.TrimPrefix(line, "checkpoint_id:"))
			if id != "" {
				return id
			}
		}
	}
	t.Fatalf("tool output did not include checkpoint_id: %q", output)
	return ""
}

func TestStandardToolsComposesFileAndWebSurface(t *testing.T) {
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteFile("main.go", "package main\n\n// Run executes work.\nfunc Run() {}\n"); err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteFile("extra.go", "package main\n"); err != nil {
		t.Fatal(err)
	}
	defs, handlers := StandardTools(SurfaceOptions{Workspace: ws, WebMaxBytes: 1024})
	if _, ok := handlers["file_read"]; !ok {
		t.Fatal("file_read handler는 표준 surface에 있어야 해요")
	}
	if _, ok := handlers["lsp_symbols"]; !ok {
		t.Fatal("lsp_symbols handler는 표준 surface에 있어야 해요")
	}
	if _, ok := handlers["web_fetch"]; !ok {
		t.Fatal("web_fetch handler는 표준 surface에 있어야 해요")
	}
	seen := map[string]bool{}
	for _, def := range defs {
		seen[def.Name] = true
	}
	if !seen["shell_run"] || !seen["web_fetch"] || !seen["lsp_hover"] {
		t.Fatalf("표준 surface 정의가 부족해요: %#v", seen)
	}
	result, err := handlers.Execute(context.Background(), llm.ToolCall{Name: "lsp_symbols", Arguments: []byte(`{"query":"Run","limit":5}`)})
	if err != nil || !strings.Contains(result.Output, `"Run"`) {
		t.Fatalf("lsp_symbols가 agent surface에서 실행돼야 해요: result=%+v err=%v", result, err)
	}
	if _, err := handlers.Execute(context.Background(), llm.ToolCall{Name: "lsp_symbols", Arguments: []byte(`{"query":"Run","limit":-1}`)}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("negative lsp_symbols limit은 거부해야 해요: %v", err)
	}
	result, err = handlers.Execute(context.Background(), llm.ToolCall{Name: "file_list", Arguments: []byte(`{"path":".","limit":1}`)})
	if err != nil || !strings.Contains(result.Output, "[result_truncated]") {
		t.Fatalf("file_list limit truncation metadata가 필요해요: result=%+v err=%v", result, err)
	}
	if _, err := handlers.Execute(context.Background(), llm.ToolCall{Name: "file_list", Arguments: []byte(`{"path":".","limit":-1}`)}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("negative file_list limit은 거부해야 해요: %v", err)
	}
	result, err = handlers.Execute(context.Background(), llm.ToolCall{Name: "file_glob", Arguments: []byte(`{"pattern":"*.go","limit":1}`)})
	if err != nil || !strings.Contains(result.Output, "[result_truncated]") {
		t.Fatalf("file_glob limit truncation metadata가 필요해요: result=%+v err=%v", result, err)
	}
	if _, err := handlers.Execute(context.Background(), llm.ToolCall{Name: "file_glob", Arguments: []byte(`{"pattern":"*.go","limit":-1}`)}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("negative file_glob limit은 거부해야 해요: %v", err)
	}
	if err := ws.WriteFile("outline.go", "package main\n\ntype Worker struct{}\nfunc (Worker) Run() {}\nfunc (Worker) Stop() {}\n"); err != nil {
		t.Fatal(err)
	}
	result, err = handlers.Execute(context.Background(), llm.ToolCall{Name: "lsp_document_symbols", Arguments: []byte(`{"path":"outline.go","limit":1}`)})
	if err != nil {
		t.Fatalf("lsp_document_symbols limit 실행 실패: %v", err)
	}
	var outline struct {
		Symbols         []struct{ Name string } `json:"symbols"`
		Limit           int                     `json:"limit"`
		ResultTruncated bool                    `json:"result_truncated"`
	}
	if err := json.Unmarshal([]byte(result.Output), &outline); err != nil {
		t.Fatal(err)
	}
	if len(outline.Symbols) != 1 || outline.Limit != 1 || !outline.ResultTruncated {
		t.Fatalf("lsp_document_symbols limit metadata가 이상해요: %+v output=%s", outline, result.Output)
	}
	if _, err := handlers.Execute(context.Background(), llm.ToolCall{Name: "lsp_document_symbols", Arguments: []byte(`{"path":"outline.go","limit":-1}`)}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("negative lsp_document_symbols limit은 거부해야 해요: %v", err)
	}
	largePath := filepath.Join(ws.Root, "large.go")
	large, err := os.Create(largePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := large.Truncate(int64(workspace.MaxFileReadBytes + 1)); err != nil {
		_ = large.Close()
		t.Fatal(err)
	}
	if err := large.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Execute(context.Background(), llm.ToolCall{Name: "lsp_document_symbols", Arguments: []byte(`{"path":"large.go"}`)}); err == nil || !strings.Contains(err.Error(), "max_bytes") {
		t.Fatalf("large lsp_document_symbols input은 거부해야 해요: %v", err)
	}

	defs, handlers = StandardTools(SurfaceOptions{Workspace: ws, NoWeb: true})
	if _, ok := handlers["web_fetch"]; ok {
		t.Fatal("NoWeb이면 web_fetch handler를 붙이면 안 돼요")
	}
	if _, ok := handlers["lsp_symbols"]; !ok {
		t.Fatal("NoWeb이어도 codeintel tool은 유지해야 해요")
	}
	for _, def := range defs {
		if def.Name == "web_fetch" {
			t.Fatal("NoWeb이면 web_fetch 정의도 빠져야 해요")
		}
	}

	defs, handlers = StandardTools(SurfaceOptions{Workspace: ws, Enabled: []string{"lsp_symbols", "file_read"}, Disabled: []string{"file_read"}})
	if _, ok := handlers["lsp_symbols"]; !ok {
		t.Fatal("enabled_tools로 lsp tool을 선택할 수 있어야 해요")
	}
	if _, ok := handlers["file_read"]; ok {
		t.Fatal("disabled_tools가 file_read를 숨겨야 해요")
	}
	if len(defs) != 1 || defs[0].Name != "lsp_symbols" {
		t.Fatalf("filtered 표준 surface가 이상해요: %+v", defs)
	}
}
