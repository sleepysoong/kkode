package main

import (
	"io"
	"strings"
	"testing"

	"github.com/sleepysoong/kkode/app"
)

func TestNormalizeAgentBudgetsBoundsToolLoopAndWebFetch(t *testing.T) {
	iterations := 0
	webBytes := int64(0)
	if err := normalizeAgentBudgets(&iterations, &webBytes); err != nil {
		t.Fatal(err)
	}
	if iterations != app.DefaultAgentMaxIterations || webBytes != app.DefaultAgentWebMaxBytes {
		t.Fatalf("default agent budgets가 이상해요: iterations=%d web=%d", iterations, webBytes)
	}
	for _, tc := range []struct {
		name       string
		iterations int
		webBytes   int64
		want       string
	}{
		{name: "negative iterations", iterations: -1, webBytes: 1, want: "max-iterations"},
		{name: "large iterations", iterations: app.MaxAgentMaxIterations + 1, webBytes: 1, want: "max-iterations"},
		{name: "negative web", iterations: 1, webBytes: -1, want: "web-max-bytes"},
		{name: "large web", iterations: 1, webBytes: app.MaxAgentWebMaxBytes + 1, want: "web-max-bytes"},
	} {
		iterations := tc.iterations
		webBytes := tc.webBytes
		if err := normalizeAgentBudgets(&iterations, &webBytes); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s 오류가 이상해요: %v", tc.name, err)
		}
	}
}

func TestReadPromptBoundsArgumentsAndStdin(t *testing.T) {
	prompt, err := readPrompt([]string{"  hello", "kkode  "}, strings.NewReader(""))
	if err != nil || prompt != "hello kkode" {
		t.Fatalf("argument prompt=%q err=%v", prompt, err)
	}
	prompt, err = readPrompt(nil, strings.NewReader("  stdin prompt  \n"))
	if err != nil || prompt != "stdin prompt" {
		t.Fatalf("stdin prompt=%q err=%v", prompt, err)
	}
	if _, err := readPrompt([]string{strings.Repeat("x", app.MaxAgentPromptBytes+1)}, strings.NewReader("")); err == nil || !strings.Contains(err.Error(), "prompt") {
		t.Fatalf("large argument prompt는 거부해야 해요: %v", err)
	}
	if _, err := readPrompt(nil, strings.NewReader(strings.Repeat("x", app.MaxAgentPromptBytes+1))); err == nil || !strings.Contains(err.Error(), "prompt") {
		t.Fatalf("large stdin prompt는 거부해야 해요: %v", err)
	}
	if _, err := readPrompt(nil, strings.NewReader("  \n\t")); err == nil || !strings.Contains(err.Error(), "prompt") {
		t.Fatalf("empty prompt는 거부해야 해요: %v", err)
	}
}

func TestReadPromptStopsAfterConfiguredEnvelope(t *testing.T) {
	reader := &countingReader{remaining: app.MaxAgentPromptBytes + 1024}
	if _, err := readPrompt(nil, reader); err == nil || !strings.Contains(err.Error(), "prompt") {
		t.Fatalf("large stdin prompt는 거부해야 해요: %v", err)
	}
	if reader.read > app.MaxAgentPromptBytes+1 {
		t.Fatalf("stdin read should stop at %d bytes, got %d", app.MaxAgentPromptBytes+1, reader.read)
	}
}

type countingReader struct {
	remaining int
	read      int
}

func (r *countingReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	if len(p) > r.remaining {
		p = p[:r.remaining]
	}
	for i := range p {
		p[i] = 'x'
	}
	r.remaining -= len(p)
	r.read += len(p)
	return len(p), nil
}
