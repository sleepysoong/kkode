package session

import (
	"strings"

	"github.com/sleepysoong/kkode/prompts"
)

type CompactionPolicy struct {
	Enabled             bool
	TriggerTokenRatio   float64
	PreserveFirstNTurns int
	PreserveLastNTurns  int
	SummaryModel        string
	MaxSummaryTokens    int
}

func BuildExtractiveSummary(s *Session, preserveFirst, preserveLast int) string {
	if s == nil || len(s.Turns) == 0 {
		return ""
	}
	turns := make([]map[string]string, 0, len(s.Turns))
	for i, turn := range s.Turns {
		if shouldPreserveIndex(i, len(s.Turns), preserveFirst, preserveLast) {
			continue
		}
		item := map[string]string{"Prompt": strings.TrimSpace(turn.Prompt)}
		if turn.Response != nil && strings.TrimSpace(turn.Response.Text) != "" {
			item["Response"] = firstLine(turn.Response.Text)
		}
		if turn.Error != "" {
			item["Error"] = turn.Error
		}
		turns = append(turns, item)
	}
	if len(turns) == 0 {
		return ""
	}
	out, err := prompts.Render(prompts.SessionCompaction, map[string]any{"Turns": turns})
	if err != nil {
		return fallbackExtractiveSummary(turns)
	}
	return out
}

func shouldPreserveIndex(i, total, first, last int) bool {
	return (first > 0 && i < first) || (last > 0 && i >= total-last)
}

func firstLine(v string) string {
	v = strings.TrimSpace(v)
	if idx := strings.IndexByte(v, '\n'); idx >= 0 {
		return strings.TrimSpace(v[:idx])
	}
	return v
}

func fallbackExtractiveSummary(turns []map[string]string) string {
	var b strings.Builder
	b.WriteString("이전 세션 요약이에요.\n")
	for _, turn := range turns {
		b.WriteString("- 사용자 요청: ")
		b.WriteString(turn["Prompt"])
		if turn["Response"] != "" {
			b.WriteString("\n  응답 요약: ")
			b.WriteString(turn["Response"])
		}
		if turn["Error"] != "" {
			b.WriteString("\n  오류: ")
			b.WriteString(turn["Error"])
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}
