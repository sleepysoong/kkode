package app

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/providers/codexcli"
	"github.com/sleepysoong/kkode/providers/copilot"
	"github.com/sleepysoong/kkode/providers/openai"
)

const defaultProviderPreviewBytes = 64 << 10

// ProviderRequestPreview는 API 호출 직전 provider/source 요청을 사람이 확인하기 쉬운 형태로 보여줘요.
// body/raw는 실행에 쓰지 않는 preview 문자열이라서 길이 제한과 secret 마스킹을 적용해요.
type ProviderRequestPreview struct {
	Provider      string            `json:"provider"`
	Operation     string            `json:"operation,omitempty"`
	Model         string            `json:"model,omitempty"`
	Stream        bool              `json:"stream,omitempty"`
	BodyJSON      string            `json:"body_json,omitempty"`
	BodyTruncated bool              `json:"body_truncated,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	RawType       string            `json:"raw_type,omitempty"`
	RawJSON       string            `json:"raw_json,omitempty"`
	RawTruncated  bool              `json:"raw_truncated,omitempty"`
}

// PreviewProviderRequest는 표준 llm.Request를 provider별 API/source 요청으로 변환하지만 실제 호출은 하지 않아요.
func PreviewProviderRequest(ctx context.Context, provider string, req llm.Request, stream bool, maxBytes int) (*ProviderRequestPreview, error) {
	spec, ok := ResolveProviderSpec(provider)
	if !ok {
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}
	converter, err := requestConverterForProvider(spec.Name)
	if err != nil {
		return nil, err
	}
	opts := llm.ConvertOptions{Stream: stream}
	if len(spec.Conversion.Operations) > 0 {
		opts.Operation = spec.Conversion.Operations[0]
	}
	pipeline := llm.ProviderPipeline{
		ProviderName:     spec.Name,
		RequestConverter: converter,
		Options:          opts,
		StreamOptions:    opts,
	}
	var preq llm.ProviderRequest
	if stream {
		preq, err = pipeline.PrepareStream(ctx, req)
	} else {
		preq, err = pipeline.Prepare(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	limit := normalizePreviewBytes(maxBytes)
	body, bodyTruncated, err := previewJSON(preq.Body, limit)
	if err != nil {
		return nil, fmt.Errorf("provider request body preview failed: %w", err)
	}
	rawType, raw, rawTruncated, err := previewRaw(preq.Raw, limit)
	if err != nil {
		return nil, fmt.Errorf("provider request raw preview failed: %w", err)
	}
	return &ProviderRequestPreview{
		Provider:      spec.Name,
		Operation:     preq.Operation,
		Model:         preq.Model,
		Stream:        preq.Stream,
		BodyJSON:      body,
		BodyTruncated: bodyTruncated,
		Headers:       redactStringMap(preq.Headers),
		Metadata:      redactStringMap(preq.Metadata),
		RawType:       rawType,
		RawJSON:       raw,
		RawTruncated:  rawTruncated,
	}, nil
}

func requestConverterForProvider(name string) (llm.RequestConverter, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "openai":
		return openai.ResponsesConverter{ProviderName: "openai"}, nil
	case "omniroute":
		return openai.ResponsesConverter{ProviderName: "omniroute"}, nil
	case "copilot":
		return copilot.SessionConverter{}, nil
	case "codex":
		return codexcli.ExecConverter{}, nil
	default:
		return nil, fmt.Errorf("provider request converter가 등록되지 않았어요: %s", name)
	}
}

func previewRaw(raw any, maxBytes int) (string, string, bool, error) {
	if raw == nil {
		return "", "", false, nil
	}
	body, truncated, err := previewJSON(raw, maxBytes)
	if err != nil {
		return "", "", false, err
	}
	return typeName(raw), body, truncated, nil
}

func previewJSON(value any, maxBytes int) (string, bool, error) {
	if value == nil {
		return "", false, nil
	}
	data, err := json.MarshalIndent(redactAny(value), "", "  ")
	if err != nil {
		return "", false, err
	}
	text := string(data)
	truncated := false
	if len(text) > maxBytes {
		text = text[:maxBytes]
		truncated = true
	}
	return text, truncated, nil
}

func normalizePreviewBytes(maxBytes int) int {
	if maxBytes <= 0 {
		return defaultProviderPreviewBytes
	}
	if maxBytes < 1024 {
		return 1024
	}
	return maxBytes
}

func redactAny(value any) any {
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		return llm.RedactSecrets(v)
	case bool, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return v
	case json.Number:
		return v
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			if isSensitivePreviewKey(key) {
				out[key] = "[REDACTED]"
				continue
			}
			out[key] = redactAny(item)
		}
		return out
	case map[string]string:
		return redactStringMap(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = redactAny(item)
		}
		return out
	case []string:
		out := make([]string, len(v))
		for i, item := range v {
			out[i] = llm.RedactSecrets(item)
		}
		return out
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return llm.RedactSecrets(fmt.Sprint(v))
		}
		var decoded any
		if err := json.Unmarshal(data, &decoded); err != nil {
			return llm.RedactSecrets(string(data))
		}
		return redactAny(decoded)
	}
}

func redactStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		if isSensitivePreviewKey(key) {
			out[key] = "[REDACTED]"
			continue
		}
		out[key] = llm.RedactSecrets(value)
	}
	return out
}

func isSensitivePreviewKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
	return strings.Contains(normalized, "api_key") || strings.Contains(normalized, "apikey") || strings.Contains(normalized, "token") || strings.Contains(normalized, "secret") || strings.Contains(normalized, "authorization")
}

func typeName(value any) string {
	t := reflect.TypeOf(value)
	if t == nil {
		return ""
	}
	return t.String()
}
