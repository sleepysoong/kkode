package tools

import (
	"context"
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
	defs, handlers := StandardTools(SurfaceOptions{Workspace: ws, WebMaxBytes: 1024})
	if _, ok := handlers["file_read"]; !ok {
		t.Fatal("file_read handler는 표준 surface에 있어야 해요")
	}
	if _, ok := handlers["web_fetch"]; !ok {
		t.Fatal("web_fetch handler는 표준 surface에 있어야 해요")
	}
	seen := map[string]bool{}
	for _, def := range defs {
		seen[def.Name] = true
	}
	if !seen["shell_run"] || !seen["web_fetch"] {
		t.Fatalf("표준 surface 정의가 부족해요: %#v", seen)
	}

	defs, handlers = StandardTools(SurfaceOptions{Workspace: ws, NoWeb: true})
	if _, ok := handlers["web_fetch"]; ok {
		t.Fatal("NoWeb이면 web_fetch handler를 붙이면 안 돼요")
	}
	for _, def := range defs {
		if def.Name == "web_fetch" {
			t.Fatal("NoWeb이면 web_fetch 정의도 빠져야 해요")
		}
	}
}
