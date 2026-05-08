package main

import (
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
