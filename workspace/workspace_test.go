package workspace

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sleepysoong/kkode/llm"
)

func TestWorkspaceReadWriteSearchAndPathBoundary(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/a.txt", []byte("hello kkode"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := New(dir)
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
		t.Fatal("workspace root 바깥 경로는 거부해야해요")
	}
	if err := w.WriteFile("b.txt", "x"); err != nil {
		t.Fatalf("write failed: %v", err)
	}
}

func TestWorkspaceToolsReadWriteAndCommand(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFile("b.txt", "tool text"); err != nil {
		t.Fatal(err)
	}
	defs, handlers := w.Tools()
	if len(defs) != 9 {
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

func TestWorkspaceWriteReplaceAndCommandTool(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, handlers := w.Tools()
	if _, err := handlers.Execute(context.Background(), llm.ToolCall{Name: "workspace_write_file", CallID: "1", Arguments: []byte(`{"path":"a.txt","content":"hello old"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := handlers.Execute(context.Background(), llm.ToolCall{Name: "workspace_replace_in_file", CallID: "2", Arguments: []byte(`{"path":"a.txt","old":"old","new":"new","expected_replacements":1}`)}); err != nil {
		t.Fatal(err)
	}
	content, err := w.ReadFile("a.txt")
	if err != nil || content != "hello new" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	out, err := handlers.Execute(context.Background(), llm.ToolCall{Name: "workspace_run_command", CallID: "3", Arguments: []byte(`{"command":"echo","args":["ok"],"timeout_ms":1000}`)})
	if err != nil {
		t.Fatal(err)
	}
	var cmd CommandResult
	if err := json.Unmarshal([]byte(out.Output), &cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.Stdout != "ok\n" || cmd.ExitCode != 0 {
		t.Fatalf("cmd=%#v", cmd)
	}
}

func TestWorkspaceReadRangeGlobGrepAndPatch(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFile("src/a.txt", "one\ntwo needle\nthree\n"); err != nil {
		t.Fatal(err)
	}
	part, err := w.ReadFileRange("src/a.txt", ReadOptions{OffsetLine: 2, LimitLines: 1})
	if err != nil || part != "two needle" {
		t.Fatalf("part=%q err=%v", part, err)
	}
	if err := w.WriteFile("src/utf8.txt", "가나다라마"); err != nil {
		t.Fatal(err)
	}
	part, err = w.ReadFileRange("src/utf8.txt", ReadOptions{MaxBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	if part != "가" || !utf8.ValidString(part) {
		t.Fatalf("max_bytes는 UTF-8 문자를 중간에서 자르면 안 돼요: %q", part)
	}
	glob, err := w.Glob("src/*.txt")
	if err != nil || len(glob) != 2 || glob[0] != "src/a.txt" || glob[1] != "src/utf8.txt" {
		t.Fatalf("glob=%#v err=%v", glob, err)
	}
	matches, err := w.Grep("needle", GrepOptions{PathGlob: "src/**"})
	if err != nil || len(matches) != 1 || matches[0].Line != 2 {
		t.Fatalf("matches=%#v err=%v", matches, err)
	}
	if _, err := w.ReadFileRange("src/a.txt", ReadOptions{MaxBytes: -1}); err == nil || !strings.Contains(err.Error(), "max_bytes") {
		t.Fatalf("negative max_bytes는 거부해야 해요: %v", err)
	}
	if _, err := w.ReadFileRange("src/a.txt", ReadOptions{MaxBytes: MaxFileReadBytes + 1}); err == nil || !strings.Contains(err.Error(), "max_bytes") {
		t.Fatalf("large max_bytes는 거부해야 해요: %v", err)
	}
	if _, err := w.ReadFileRange("src/a.txt", ReadOptions{OffsetLine: -1}); err == nil || !strings.Contains(err.Error(), "offset_line") {
		t.Fatalf("negative offset_line은 거부해야 해요: %v", err)
	}
	if _, err := w.ReadFileRange("src/a.txt", ReadOptions{LimitLines: -1}); err == nil || !strings.Contains(err.Error(), "limit_lines") {
		t.Fatalf("negative limit_lines는 거부해야 해요: %v", err)
	}
	if _, err := w.Grep("needle", GrepOptions{MaxMatches: -1}); err == nil || !strings.Contains(err.Error(), "max_matches") {
		t.Fatalf("negative max_matches는 거부해야 해요: %v", err)
	}
	if _, err := w.Grep("needle", GrepOptions{MaxMatches: MaxGrepMatches + 1}); err == nil || !strings.Contains(err.Error(), "max_matches") {
		t.Fatalf("large max_matches는 거부해야 해요: %v", err)
	}
	if err := w.EditFile("src/a.txt", "needle", "patched", -1); err == nil || !strings.Contains(err.Error(), "expected_replacements") {
		t.Fatalf("negative expected_replacements는 거부해야 해요: %v", err)
	}
	if _, err := w.RunDetailed(context.Background(), "echo", []string{"ok"}, CommandOptions{Timeout: -1 * time.Millisecond}); err == nil || !strings.Contains(err.Error(), "timeout_ms") {
		t.Fatalf("negative timeout_ms는 거부해야 해요: %v", err)
	}
	if _, err := w.RunDetailed(context.Background(), "echo", []string{"ok"}, CommandOptions{Timeout: MaxCommandTimeout + time.Millisecond}); err == nil || !strings.Contains(err.Error(), "timeout_ms") {
		t.Fatalf("large timeout_ms는 거부해야 해요: %v", err)
	}
	patch := `*** Begin Patch
*** Update File: src/a.txt
@@
 one
-two needle
+two patched
 three
*** End Patch
`
	if err := w.ApplyPatch(patch); err != nil {
		t.Fatal(err)
	}
	updated, _ := w.ReadFile("src/a.txt")
	if !strings.Contains(updated, "two patched") {
		t.Fatalf("updated=%q", updated)
	}
}

func TestWorkspaceReadFileDefaultsToBoundedEnvelope(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("a", MaxFileReadBytes+utf8.UTFMax)
	if err := os.WriteFile(dir+"/large.txt", []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := w.ReadFile("large.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != MaxFileReadBytes {
		t.Fatalf("default read should cap at %d bytes, got %d", MaxFileReadBytes, len(got))
	}
}

func TestWorkspaceApplyPatchPrevalidatesBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFile("a.txt", "alpha\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFile("b.txt", "beta\n"); err != nil {
		t.Fatal(err)
	}
	patch := `*** Begin Patch
*** Update File: a.txt
@@
-alpha
+alpha patched
*** Update File: b.txt
@@
-missing
+beta patched
*** End Patch
`
	if err := w.ApplyPatch(patch); err == nil {
		t.Fatal("뒤쪽 patch 검증이 실패하면 전체 patch 적용이 중단돼야 해요")
	}
	unchanged, err := w.ReadFile("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if unchanged != "alpha\n" {
		t.Fatalf("검증 실패 전 파일을 미리 쓰면 안 돼요: %q", unchanged)
	}
}

func TestWorkspaceCanWriteDotGitAndRunCommands(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir+"/.git", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFile(".git/config", "yolo"); err != nil {
		t.Fatalf("dot git write failed: %v", err)
	}
	out, err := w.Run(context.Background(), "echo", "yolo")
	if err != nil || out != "yolo\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestWorkspaceSearchSkipsGeneratedDirectories(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFile("src/a.txt", "needle"); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFile("node_modules/pkg/a.txt", "needle"); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFile(".omx/logs/run.log", "needle"); err != nil {
		t.Fatal(err)
	}
	matches, err := w.Grep("needle", GrepOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Path != "src/a.txt" {
		t.Fatalf("생성물 디렉터리는 검색에서 건너뛰어야 해요: %+v", matches)
	}
}
