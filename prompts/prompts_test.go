package prompts

import (
	"strings"
	"testing"
)

func TestRenderAgentSystem(t *testing.T) {
	out, err := Render(AgentSystem, map[string]any{"AgentName": "kkode", "ToolNames": []string{"file_read", "shell_run"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "kkode") || !strings.Contains(out, "file_read") || !strings.Contains(out, "shell_run") {
		t.Fatalf("template output=%q", out)
	}
}

func TestRenderSessionCompaction(t *testing.T) {
	out, err := Render(SessionCompaction, map[string]any{"Turns": []map[string]string{{"Prompt": "테스트", "Response": "통과"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "테스트") || !strings.Contains(out, "통과") {
		t.Fatalf("template output=%q", out)
	}
}
