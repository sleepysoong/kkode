package session

import (
	"strings"
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
	var b strings.Builder
	b.WriteString("이전 세션 요약이에요.\n")
	for i, turn := range s.Turns {
		if shouldPreserveIndex(i, len(s.Turns), preserveFirst, preserveLast) {
			continue
		}
		b.WriteString("- 사용자 요청: ")
		b.WriteString(strings.TrimSpace(turn.Prompt))
		if turn.Response != nil && strings.TrimSpace(turn.Response.Text) != "" {
			b.WriteString("\n  응답 요약: ")
			b.WriteString(firstLine(turn.Response.Text))
		}
		if turn.Error != "" {
			b.WriteString("\n  오류: ")
			b.WriteString(turn.Error)
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
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
