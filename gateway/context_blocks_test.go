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
