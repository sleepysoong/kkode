package llm

import "regexp"

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`(?i)(api[_-]?key|token|secret)\s*[:=]\s*['\"]?[^\s'\"]+`),
}

func RedactSecrets(s string) string {
	out := s
	for _, re := range secretPatterns {
		out = re.ReplaceAllString(out, "[REDACTED]")
	}
	return out
}
