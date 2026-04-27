package agent

import (
	"fmt"
	"strings"
)

func (g Guardrails) CheckInput(input string) error {
	return checkBlocked("input", input, g.BlockedSubstrings)
}

func (g Guardrails) CheckOutput(output string) error {
	return checkBlocked("output", output, g.BlockedOutputSubstrings)
}

func checkBlocked(kind string, text string, blockedSubstrings []string) error {
	lower := strings.ToLower(text)
	for _, blocked := range blockedSubstrings {
		if blocked == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(blocked)) {
			return fmt.Errorf("%s blocked by guardrail: %s", kind, blocked)
		}
	}
	return nil
}
