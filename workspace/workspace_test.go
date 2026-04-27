package workspace

import (
	"context"
	"os"
	"testing"

	"github.com/sleepysoong/kkode/llm"
)

func TestWorkspaceReadSearchAndDenyEscape(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/a.txt", []byte("hello kkode"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := New(dir, llm.ApprovalPolicy{Mode: llm.ApprovalReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	text, err := w.ReadFile("a.txt")
	if err != nil || text != "hello kkode" {
		t.Fatalf("read=%q err=%v", text, err)
	}
	matches, err := w.Search("kkode")
	if err != nil || len(matches) != 1 || matches[0] != "a.txt" {
		t.Fatalf("matches=%#v err=%v", matches, err)
	}
	if _, err := w.ReadFile("../nope"); err == nil {
		t.Fatal("expected escape error")
	}
	if err := w.WriteFile("b.txt", "x"); err == nil {
		t.Fatal("expected write denied")
	}
}

func TestWorkspaceToolsAndCommandPolicy(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir, llm.ApprovalPolicy{Mode: llm.ApprovalAllowAll, AllowedCommands: []string{"echo"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFile("b.txt", "tool text"); err != nil {
		t.Fatal(err)
	}
	defs, handlers := w.Tools()
	if len(defs) != 6 {
		t.Fatalf("defs=%d", len(defs))
	}
	res, err := handlers.Execute(context.Background(), llm.ToolCall{Name: "workspace_read_file", CallID: "1", Arguments: []byte(`{"path":"b.txt"}`)})
	if err != nil || res.Output != "tool text" {
		t.Fatalf("res=%#v err=%v", res, err)
	}
	out, err := w.Run(context.Background(), "echo", "ok")
	if err != nil || out != "ok\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestWorkspaceWriteReplaceAndRunToolPolicy(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir, llm.ApprovalPolicy{Mode: llm.ApprovalTrustedWrites, AllowedPaths: []string{dir}, AllowedCommands: []string{"echo"}})
	if err != nil {
		t.Fatal(err)
	}
	_, handlers := w.Tools()
	if _, err := handlers.Execute(context.Background(), llm.ToolCall{Name: "workspace_write_file", CallID: "1", Arguments: []byte(`{"path":"a.txt","content":"hello old"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Execute(context.Background(), llm.ToolCall{Name: "workspace_replace_in_file", CallID: "2", Arguments: []byte(`{"path":"a.txt","old":"old","new":"new"}`)}); err != nil {
		t.Fatal(err)
	}
	content, err := w.ReadFile("a.txt")
	if err != nil || content != "hello new" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	out, err := handlers.Execute(context.Background(), llm.ToolCall{Name: "workspace_run_command", CallID: "3", Arguments: []byte(`{"command":"echo","args":["ok"]}`)})
	if err != nil || out.Output != "ok\n" {
		t.Fatalf("out=%#v err=%v", out, err)
	}
}
