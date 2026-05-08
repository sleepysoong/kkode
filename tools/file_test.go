package tools

import (
	"context"
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
}

func TestStandardToolsComposesFileAndWebSurface(t *testing.T) {
	ws, err := workspace.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteFile("main.go", "package main\n\n// Run executes work.\nfunc Run() {}\n"); err != nil {
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
