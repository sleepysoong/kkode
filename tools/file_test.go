package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/workspace"
)

func TestFileToolsReadWriteAndGrep(t *testing.T) {
	ws, err := workspace.New(t.TempDir(), llm.ApprovalPolicy{Mode: llm.ApprovalAllowAll})
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
