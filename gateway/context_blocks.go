package gateway

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/sleepysoong/kkode/llm"
)

const maxRunContextBlockBytes = 32 << 10
const maxRunContextBlocks = 64
const maxRunMetadataEntries = 64
const maxRunMetadataKeyBytes = 128
const maxRunMetadataValueBytes = 1024
const maxRunPromptBytes = 256 << 10
const maxRunProviderModelBytes = 128
const maxRunSelectorItems = 256
const maxRunSelectorItemBytes = 128

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
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.Provider = strings.TrimSpace(req.Provider)
	req.Model = strings.TrimSpace(req.Model)
	req.Metadata = sanitizeRunMetadata(req.Metadata)
	req.MCPServers = sanitizeResourceIDs(req.MCPServers)
	req.Skills = sanitizeResourceIDs(req.Skills)
	req.Subagents = sanitizeResourceIDs(req.Subagents)
	req.ContextBlocks = SanitizeContextBlocks(req.ContextBlocks)
	req.EnabledTools = sanitizeToolNames(req.EnabledTools)
	req.DisabledTools = sanitizeToolNames(req.DisabledTools)
	return req
}

func validateRunRequestShape(req RunStartRequest) error {
	if len(req.Prompt) > maxRunPromptBytes {
		return fmt.Errorf("prompt는 %d byte 이하여야 해요", maxRunPromptBytes)
	}
	if len(req.Provider) > maxRunProviderModelBytes {
		return fmt.Errorf("provider는 %d byte 이하여야 해요", maxRunProviderModelBytes)
	}
	if len(req.Model) > maxRunProviderModelBytes {
		return fmt.Errorf("model은 %d byte 이하여야 해요", maxRunProviderModelBytes)
	}
	if err := validateRunSelectorList("mcp_servers", req.MCPServers, true); err != nil {
		return err
	}
	if err := validateRunSelectorList("skills", req.Skills, true); err != nil {
		return err
	}
	if err := validateRunSelectorList("subagents", req.Subagents, true); err != nil {
		return err
	}
	if err := validateRunSelectorList("enabled_tools", req.EnabledTools, false); err != nil {
		return err
	}
	if err := validateRunSelectorList("disabled_tools", req.DisabledTools, false); err != nil {
		return err
	}
	if len(req.ContextBlocks) > maxRunContextBlocks {
		return fmt.Errorf("context_blocks는 최대 %d개까지 허용돼요", maxRunContextBlocks)
	}
	return nil
}

func validateRunSelectorList(label string, values []string, identifier bool) error {
	if len(values) > maxRunSelectorItems {
		return fmt.Errorf("%s는 최대 %d개까지 허용돼요", label, maxRunSelectorItems)
	}
	for i, value := range values {
		if len(value) > maxRunSelectorItemBytes {
			return fmt.Errorf("%s[%d]는 %d byte 이하여야 해요", label, i, maxRunSelectorItemBytes)
		}
		if identifier && !validRunMetadataKey(value) {
			return fmt.Errorf("%s[%d]는 영문/숫자/._- 문자만 쓸 수 있어요", label, i)
		}
	}
	return nil
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

func sanitizeRunMetadata(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make(map[string]string, len(metadata))
	for _, key := range keys {
		cleanKey := strings.TrimSpace(key)
		cleanValue := strings.TrimSpace(metadata[key])
		if cleanKey == "" || cleanValue == "" {
			continue
		}
		out[cleanKey] = cleanValue
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func validateRunMetadata(metadata map[string]string) error {
	if len(metadata) > maxRunMetadataEntries {
		return fmt.Errorf("metadata는 최대 %d개까지 허용돼요", maxRunMetadataEntries)
	}
	for key, value := range metadata {
		if len(key) > maxRunMetadataKeyBytes {
			return fmt.Errorf("metadata key %q는 %d byte 이하여야 해요", key, maxRunMetadataKeyBytes)
		}
		if !validRunMetadataKey(key) {
			return fmt.Errorf("metadata key %q는 영문/숫자/._- 문자만 쓸 수 있어요", key)
		}
		if len(value) > maxRunMetadataValueBytes {
			return fmt.Errorf("metadata %q 값은 %d byte 이하여야 해요", key, maxRunMetadataValueBytes)
		}
	}
	return nil
}

func validRunMetadataKey(key string) bool {
	if key == "" {
		return false
	}
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.':
		default:
			return false
		}
	}
	return true
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
