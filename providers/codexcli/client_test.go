package codexcli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sleepysoong/kkode/llm"
)

func TestParseCodexEvent(t *testing.T) {
	ev := parseCodexEvent([]byte(`{"type":"item.completed","item":{"type":"agent_message","text":"OK"}}`), "codex")
	if ev.Type != llm.StreamEventTextDelta || ev.Delta != "OK" {
		t.Fatalf("ev=%#v", ev)
	}
	ev = parseCodexEvent([]byte(`{"type":"turn.completed"}`), "codex")
	if ev.Type != llm.StreamEventCompleted {
		t.Fatalf("ev=%#v", ev)
	}
}

func TestRenderPromptUsesSharedTranscriptRenderer(t *testing.T) {
	got := renderPrompt(llm.Request{Instructions: "rules", Messages: []llm.Message{llm.UserText("hi")}})
	if got != "rules\n\nUSER: hi" {
		t.Fatalf("prompt=%q", got)
	}
}

func TestExecConverterBuildsProviderRequest(t *testing.T) {
	preq, err := ExecConverter{}.ConvertRequest(context.Background(), llm.Request{Model: "gpt-5.3-codex", Messages: []llm.Message{llm.UserText("hi")}}, llm.ConvertOptions{})
	if err != nil {
		t.Fatal(err)
	}
	payload := preq.Raw.(execPayload)
	if preq.Operation != execOperation || preq.Model != "gpt-5.3-codex" || payload.Prompt != "USER: hi" {
		t.Fatalf("Codex CLI provider request가 이상해요: %+v payload=%+v", preq, payload)
	}
}

func TestClientGenerateUsesConverterAndYoloCommand(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-codex")
	argsPath := filepath.Join(dir, "args.txt")
	script := `#!/bin/sh
printf '%s\n' "$@" > ` + argsPath + `
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    shift
    out="$1"
  fi
  shift
done
printf ' converted\n' > "$out"
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	client := New(Config{Binary: bin, WorkingDirectory: dir})
	resp, err := client.Generate(context.Background(), llm.Request{Model: "gpt-5.3-codex", Messages: []llm.Message{llm.UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "codex-cli" || resp.Text != "converted" {
		t.Fatalf("Codex CLI 표준 응답이 이상해요: %+v", resp)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	gotArgs := string(args)
	if !strings.Contains(gotArgs, "-a\nnever\n") || !strings.Contains(gotArgs, "--sandbox\ndanger-full-access\n") {
		t.Fatalf("YOLO 실행 인자를 유지해야 해요: %q", gotArgs)
	}
}

func TestClientStreamUsesConverterAndYoloCommand(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-codex")
	argsPath := filepath.Join(dir, "stream-args.txt")
	script := `#!/bin/sh
printf '%s\n' "$@" > ` + argsPath + `
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"streamed"}}'
printf '%s\n' '{"type":"turn.completed"}'
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	client := New(Config{Binary: bin, WorkingDirectory: dir})
	stream, err := client.Stream(context.Background(), llm.Request{Model: "gpt-5.3-codex", Messages: []llm.Message{llm.UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	ev, err := stream.Recv()
	if err != nil || ev.Type != llm.StreamEventTextDelta || ev.Delta != "streamed" {
		t.Fatalf("stream 첫 event가 이상해요: %+v err=%v", ev, err)
	}
	for {
		ev, err = stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Type == llm.StreamEventCompleted {
			break
		}
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	gotArgs := string(args)
	if !strings.Contains(gotArgs, "-a\nnever\n") || !strings.Contains(gotArgs, "--sandbox\ndanger-full-access\n") || !strings.Contains(gotArgs, "USER: hi\n") {
		t.Fatalf("stream도 변환된 prompt와 YOLO 실행 인자를 써야 해요: %q", gotArgs)
	}
}

func TestReadLimitedFileRejectsLargeCodexOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	if err := os.WriteFile(path, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readLimitedFile(path, 4); err == nil || !strings.Contains(err.Error(), "max_bytes=4") {
		t.Fatalf("large codex output should be rejected: %v", err)
	}
	data, err := readLimitedFile(path, 5)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "12345" {
		t.Fatalf("limited read changed output: %q", data)
	}
}

func TestLimitedBufferKeepsBoundedStderr(t *testing.T) {
	buf := newLimitedBuffer(4)
	if n, err := buf.Write([]byte("abcdef")); err != nil || n != 6 {
		t.Fatalf("limited stderr write should report full write: n=%d err=%v", n, err)
	}
	got := buf.String()
	if !strings.Contains(got, "abcd") || strings.Contains(got, "ef") || !strings.Contains(got, "stderr truncated") {
		t.Fatalf("stderr buffer should be bounded and marked truncated: %q", got)
	}
}
