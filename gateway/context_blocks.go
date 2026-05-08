package gateway

import (
	"strings"
	"unicode/utf8"

	"github.com/sleepysoong/kkode/llm"
)

const maxRunContextBlockBytes = 32 << 10

// SanitizeContextBlocks는 adapter가 보낸 임시 prompt context를 실행/저장 전에 안전한 형태로 정규화해요.
// secret은 제거하고, 너무 긴 block은 UTF-8을 깨지 않는 byte 경계에서 잘라요.
func SanitizeContextBlocks(blocks []string) []string {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]string, 0, len(blocks))
	for _, block := range blocks {
		text := strings.TrimSpace(llm.RedactSecrets(block))
		if text == "" {
			continue
		}
		truncated := false
		if len(text) > maxRunContextBlockBytes {
			text = truncateContextBlock(text, maxRunContextBlockBytes)
			truncated = true
		}
		if text == "" {
			continue
		}
		if truncated {
			text += "\n[context block이 길어서 일부만 저장했어요]"
		}
		out = append(out, text)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sanitizeRunStartRequest(req RunStartRequest) RunStartRequest {
	req.MCPServers = sanitizeResourceIDs(req.MCPServers)
	req.Skills = sanitizeResourceIDs(req.Skills)
	req.Subagents = sanitizeResourceIDs(req.Subagents)
	req.ContextBlocks = SanitizeContextBlocks(req.ContextBlocks)
	req.EnabledTools = sanitizeToolNames(req.EnabledTools)
	req.DisabledTools = sanitizeToolNames(req.DisabledTools)
	return req
}

func sanitizeResourceIDs(ids []string) []string {
	return sanitizeUniqueStrings(ids)
}

func sanitizeToolNames(names []string) []string {
	return sanitizeUniqueStrings(names)
}

func sanitizeUniqueStrings(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	out := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func truncateContextBlock(text string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	used := 0
	end := 0
	for i, r := range text {
		size := utf8.RuneLen(r)
		if size < 0 {
			size = len(string(r))
		}
		if used+size > maxBytes {
			break
		}
		used += size
		end = i + size
	}
	if end == 0 {
		return ""
	}
	return text[:end]
}
