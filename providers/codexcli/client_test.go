package codexcli

import (
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
