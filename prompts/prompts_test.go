package prompts

import (
	"reflect"
	"strings"
	"testing"
)

func TestListTemplates(t *testing.T) {
	got, err := List()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{AgentSystem, SessionCompaction, SessionSummaryContext, TodoInstructions}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("template 목록이 이상해요: got=%v want=%v", got, want)
	}
}

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

func TestRenderIsConcurrentSafe(t *testing.T) {
	for i := 0; i < 20; i++ {
		i := i
		t.Run("render", func(t *testing.T) {
			t.Parallel()
			out, err := Render(AgentSystem, map[string]any{"AgentName": "kkode", "ToolNames": []string{"file_read"}})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "kkode") || i < 0 {
				t.Fatalf("template output=%q", out)
			}
		})
	}
}
