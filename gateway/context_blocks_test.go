package gateway

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeContextBlocksRedactsAndTruncates(t *testing.T) {
	huge := strings.Repeat("가", maxRunContextBlockBytes)
	blocks := SanitizeContextBlocks([]string{
		" ",
		"token=ghp_123456789012345678901234567890123456\n요약이에요",
		huge,
	})
	if len(blocks) != 2 {
		t.Fatalf("빈 context block은 제거돼야 해요: %#v", blocks)
	}
	if strings.Contains(blocks[0], "ghp_") || !strings.Contains(blocks[0], "[REDACTED]") || !strings.Contains(blocks[0], "요약이에요") {
		t.Fatalf("context block secret 마스킹이 필요해요: %q", blocks[0])
	}
	if !strings.Contains(blocks[1], "일부만 저장") || !utf8.ValidString(blocks[1]) {
		t.Fatalf("context block은 UTF-8을 유지하며 잘려야 해요: %q", blocks[1])
	}
}

func TestRunMetadataIsSanitizedAndValidated(t *testing.T) {
	metadata := sanitizeRunMetadata(map[string]string{
		" trace-id ":       " abc ",
		"":                 "drop",
		"empty":            " ",
		"metadata.version": " 2026-05-08 ",
	})
	if len(metadata) != 2 || metadata["trace-id"] != "abc" || metadata["metadata.version"] != "2026-05-08" {
		t.Fatalf("metadata trim/drop 결과가 이상해요: %#v", metadata)
	}
	if err := validateRunMetadata(metadata); err != nil {
		t.Fatalf("정상 metadata가 거부됐어요: %v", err)
	}
	if err := validateRunMetadata(map[string]string{"bad key": "value"}); err == nil || !strings.Contains(err.Error(), "metadata key") {
		t.Fatalf("공백이 있는 metadata key는 거부해야 해요: %v", err)
	}
	if err := validateRunMetadata(map[string]string{"huge": strings.Repeat("x", maxRunMetadataValueBytes+1)}); err == nil || !strings.Contains(err.Error(), "huge") {
		t.Fatalf("너무 긴 metadata value는 거부해야 해요: %v", err)
	}
}

func TestRunRequestShapeIsValidated(t *testing.T) {
	valid := RunStartRequest{
		Prompt:        "go test",
		Provider:      "copilot",
		Model:         "gpt-5-mini",
		MCPServers:    []string{"mcp_1"},
		Skills:        []string{"skill_1"},
		Subagents:     []string{"agent_1"},
		EnabledTools:  []string{"file_read"},
		DisabledTools: []string{"shell_run"},
		ContextBlocks: []string{"adapter context"},
	}
	if err := validateRunRequestShape(valid); err != nil {
		t.Fatalf("정상 run request shape이 거부됐어요: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*RunStartRequest)
		want   string
	}{
		{name: "prompt", mutate: func(req *RunStartRequest) { req.Prompt = strings.Repeat("x", maxRunPromptBytes+1) }, want: "prompt"},
		{name: "provider", mutate: func(req *RunStartRequest) { req.Provider = strings.Repeat("x", maxRunProviderModelBytes+1) }, want: "provider"},
		{name: "model", mutate: func(req *RunStartRequest) { req.Model = strings.Repeat("x", maxRunProviderModelBytes+1) }, want: "model"},
		{name: "mcp count", mutate: func(req *RunStartRequest) { req.MCPServers = repeatedRunValues("mcp", maxRunSelectorItems+1) }, want: "mcp_servers"},
		{name: "mcp item", mutate: func(req *RunStartRequest) { req.MCPServers = []string{strings.Repeat("x", maxRunSelectorItemBytes+1)} }, want: "mcp_servers[0]"},
		{name: "mcp id", mutate: func(req *RunStartRequest) { req.MCPServers = []string{"bad id"} }, want: "mcp_servers[0]"},
		{name: "tool item", mutate: func(req *RunStartRequest) {
			req.EnabledTools = []string{strings.Repeat("x", maxRunSelectorItemBytes+1)}
		}, want: "enabled_tools[0]"},
		{name: "context count", mutate: func(req *RunStartRequest) { req.ContextBlocks = repeatedRunValues("context", maxRunContextBlocks+1) }, want: "context_blocks"},
	} {
		req := valid
		tc.mutate(&req)
		if err := validateRunRequestShape(req); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s invalid run request shape 오류가 이상해요: %v", tc.name, err)
		}
	}
}

func repeatedRunValues(prefix string, count int) []string {
	values := make([]string, 0, count)
	for i := 0; i < count; i++ {
		values = append(values, prefix)
	}
	return values
}
