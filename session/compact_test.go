package session

import (
	"strings"
	"testing"

	"github.com/sleepysoong/kkode/llm"
)

func TestBuildExtractiveSummaryUsesPromptTemplate(t *testing.T) {
	sess := NewSession("/repo", "openai", "gpt", "agent", AgentModeBuild)
	for _, prompt := range []string{"첫 요청", "둘째 요청", "셋째 요청"} {
		turn := NewTurn(prompt, llm.Request{Model: "gpt"})
		turn.Response = &llm.Response{Text: "응답 첫 줄\n자세한 내용"}
		sess.AppendTurn(turn)
	}
	summary := BuildExtractiveSummary(sess, 1, 1)
	if !strings.Contains(summary, "보존하지 않는 오래된 turn") || !strings.Contains(summary, "둘째 요청") || strings.Contains(summary, "첫 요청") {
		t.Fatalf("summary=%q", summary)
	}
}
