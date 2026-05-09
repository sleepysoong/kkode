package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	if err := w.WriteFile("large.txt", strings.Repeat("x", MaxFileWriteBytes+1)); err == nil || !strings.Contains(err.Error(), "content") {
		t.Fatalf("large write는 거부해야 해요: %v", err)
	}
}

func TestWorkspaceListAndGlobUseBoundedEnvelope(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxListEntries+2; i++ {
		name := fmt.Sprintf("entry-%05d.txt", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := w.List(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != MaxListEntries {
		t.Fatalf("list should cap at %d entries, got %d", MaxListEntries, len(listed))
	}
	matches, err := w.Glob("*.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != MaxGlobMatches {
		t.Fatalf("glob should cap at %d matches, got %d", MaxGlobMatches, len(matches))
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
	if len(defs) != 12 {
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

func TestWorkspaceCommandOutputUsesBoundedEnvelope(t *testing.T) {
	buf := &boundedCommandBuffer{max: 5}
	n, err := buf.Write([]byte("abcdef"))
	if err != nil || n != 6 {
		t.Fatalf("write should accept full producer payload: n=%d err=%v", n, err)
	}
	if got := buf.String(); got != "abcde" || !buf.truncated {
		t.Fatalf("buffer should retain only the configured envelope: got=%q truncated=%v", got, buf.truncated)
	}

	utf8Buf := &boundedCommandBuffer{max: 4}
	if _, err := utf8Buf.Write([]byte("가나")); err != nil {
		t.Fatal(err)
	}
	if got := utf8Buf.String(); got != "가" || !utf8.ValidString(got) || !utf8Buf.truncated {
		t.Fatalf("truncated command output should stay UTF-8 safe: got=%q truncated=%v", got, utf8Buf.truncated)
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
	if _, err := handlers.Execute(context.Background(), llm.ToolCall{Name: "workspace_apply_patch", CallID: "3", Arguments: []byte(`{"patch_text":"*** Begin Patch\n*** Update File: a.txt\n@@\n-hello new\n+hello patched\n*** End Patch\n"}`)}); err != nil {
		t.Fatal(err)
	}
	content, err = w.ReadFile("a.txt")
	if err != nil || content != "hello patched" {
		t.Fatalf("patched content=%q err=%v", content, err)
	}
	if _, err := handlers.Execute(context.Background(), llm.ToolCall{Name: "workspace_move_path", CallID: "4", Arguments: []byte(`{"source":"a.txt","destination":"moved/a.txt"}`)}); err != nil {
		t.Fatal(err)
	}
	moved, err := w.ReadFile("moved/a.txt")
	if err != nil || moved != "hello patched" {
		t.Fatalf("moved content=%q err=%v", moved, err)
	}
	if _, err := handlers.Execute(context.Background(), llm.ToolCall{Name: "workspace_delete_path", CallID: "5", Arguments: []byte(`{"path":"moved/a.txt"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ReadFile("moved/a.txt"); err == nil {
		t.Fatal("deleted file should not remain readable")
	}
	out, err := handlers.Execute(context.Background(), llm.ToolCall{Name: "workspace_run_command", CallID: "6", Arguments: []byte(`{"command":"echo","args":["ok"],"timeout_ms":1000}`)})
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
	if cmd.DurationMS < 0 || cmd.StartedAt.IsZero() || cmd.EndedAt.IsZero() {
		t.Fatalf("cmd timing이 이상해요: %#v", cmd)
	}
	out, err = handlers.Execute(context.Background(), llm.ToolCall{Name: "workspace_run_command", CallID: "7", Arguments: []byte(`{"command":"sh","args":["-c","echo out; echo err >&2; exit 7"],"timeout_ms":1000}`)})
	if err != nil {
		t.Fatalf("non-zero command should still return structured output: %v", err)
	}
	cmd = CommandResult{}
	if err := json.Unmarshal([]byte(out.Output), &cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.ExitCode != 7 || cmd.Stdout != "out\n" || !strings.Contains(cmd.Stderr, "err") || cmd.DurationMS < 0 {
		t.Fatalf("failed command result가 이상해요: %#v", cmd)
	}
	if _, err := handlers.Execute(context.Background(), llm.ToolCall{Name: "workspace_run_command", CallID: "8", Arguments: []byte(`{"command":"definitely-missing-kkode-command","timeout_ms":1000}`)}); err == nil || !strings.Contains(err.Error(), "definitely-missing-kkode-command") {
		t.Fatalf("missing command should remain a tool error: %v", err)
	}
}

func TestWorkspaceCheckpointRestoresMutations(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFile("docs/a.txt", "alpha"); err != nil {
		t.Fatal(err)
	}
	cp, err := w.CreateCheckpoint([]string{"docs/a.txt", "new.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cp.Entries) != 2 {
		t.Fatalf("checkpoint entries=%+v", cp.Entries)
	}
	if err := w.WriteFile("docs/a.txt", "beta"); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFile("new.txt", "created"); err != nil {
		t.Fatal(err)
	}
	restored, err := w.RestoreCheckpoint(cp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.ID != cp.ID {
		t.Fatalf("restored checkpoint id=%s want %s", restored.ID, cp.ID)
	}
	got, err := w.ReadFile("docs/a.txt")
	if err != nil || got != "alpha" {
		t.Fatalf("restored content=%q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore should remove paths that were absent in checkpoint: %v", err)
	}
	if matches, err := w.Glob(".kkode/**/*"); err != nil || len(matches) != 0 {
		t.Fatalf("checkpoint internals should stay out of glob: matches=%v err=%v", matches, err)
	}
}

func TestWorkspaceDeleteAndMovePath(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFile("src/a.txt", "alpha"); err != nil {
		t.Fatal(err)
	}
	if err := w.MovePath("src/a.txt", "dst/a.txt", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "src", "a.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source should be gone after move: %v", err)
	}
	got, err := w.ReadFile("dst/a.txt")
	if err != nil || got != "alpha" {
		t.Fatalf("moved file=%q err=%v", got, err)
	}
	if err := w.WriteFile("dst/existing.txt", "old"); err != nil {
		t.Fatal(err)
	}
	if err := w.MovePath("dst/a.txt", "dst/existing.txt", false); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("move without overwrite should reject existing destination: %v", err)
	}
	if err := w.MovePath("dst/a.txt", "dst/existing.txt", true); err != nil {
		t.Fatal(err)
	}
	got, err = w.ReadFile("dst/existing.txt")
	if err != nil || got != "alpha" {
		t.Fatalf("overwritten file=%q err=%v", got, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "nested", "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := w.MovePath("nested", "nested/child/moved", false); err == nil || !strings.Contains(err.Error(), "inside source") {
		t.Fatalf("moving directory into itself should fail: %v", err)
	}
	if err := w.DeletePath("nested", false); err == nil || !strings.Contains(err.Error(), "recursive") {
		t.Fatalf("directory delete should require recursive=true: %v", err)
	}
	if err := w.DeletePath("nested", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "nested")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recursive delete should remove directory: %v", err)
	}
	if err := w.DeletePath(".", true); err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("workspace root delete should fail: %v", err)
	}
	if err := w.MovePath("dst/existing.txt", ".", true); err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("workspace root overwrite should fail: %v", err)
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
	res, err := w.RunDetailed(context.Background(), "sh", []string{"-c", "echo out; echo err >&2; exit 7"}, CommandOptions{Timeout: time.Second})
	if err == nil {
		t.Fatal("non-zero command should still return an execution error from RunDetailed")
	}
	if res.ExitCode != 7 || res.Stdout != "out\n" || !strings.Contains(res.Stderr, "err") || res.DurationMS < 0 || res.StartedAt.IsZero() || res.EndedAt.IsZero() {
		t.Fatalf("structured command failure가 이상해요: %#v err=%v", res, err)
	}
	if !res.IsProcessOutcome(err) {
		t.Fatalf("non-zero exit should be reportable as a process outcome: %#v err=%v", res, err)
	}
	missing, err := w.RunDetailed(context.Background(), "definitely-missing-kkode-command", nil, CommandOptions{Timeout: time.Second})
	if err == nil {
		t.Fatal("missing executable should return an error")
	}
	if missing.IsProcessOutcome(err) {
		t.Fatalf("missing executable should not be reported as a process outcome: %#v err=%v", missing, err)
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

func TestWorkspaceApplyPatchBoundsPayloadAndResult(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.ApplyPatch(strings.Repeat("x", MaxPatchBytes+1)); err == nil || !strings.Contains(err.Error(), "patch_text") {
		t.Fatalf("large patch_text는 거부해야 해요: %v", err)
	}
	if err := w.WriteFile("small.txt", "small\n"); err != nil {
		t.Fatal(err)
	}
	original := "old" + strings.Repeat("x", MaxFileWriteBytes-len("old"))
	if err := w.WriteFile("large.txt", original); err != nil {
		t.Fatal(err)
	}
	patch := `*** Begin Patch
*** Update File: large.txt
@@
-old
+new-content
*** End Patch
`
	if err := w.ApplyPatch(patch); err == nil || !strings.Contains(err.Error(), "patched content") {
		t.Fatalf("large patched content는 거부해야 해요: %v", err)
	}
	unchanged, err := os.ReadFile(filepath.Join(dir, "large.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != original {
		t.Fatal("oversize patch result should not be written")
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
