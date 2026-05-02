package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sleepysoong/kkode/llm"
)

const defaultBaseURL = "https://api.openai.com/v1"

type Config struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
	Headers    map[string]string
	Retry      RetryConfig
	// ProviderName은 OpenAI-compatible 파생 provider가 telemetry label을 고정할 때 써요.
	ProviderName string
}

type RetryConfig struct {
	MaxRetries int
	MinBackoff time.Duration
	MaxBackoff time.Duration
}

type Client struct {
	baseURL      string
	apiKey       string
	httpClient   *http.Client
	headers      map[string]string
	retry        RetryConfig
	providerName string
}

func New(cfg Config) *Client {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = defaultBaseURL
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 120 * time.Second}
	}
	retry := cfg.Retry
	if retry.MaxRetries == 0 {
		retry.MaxRetries = 2
	}
	if retry.MinBackoff == 0 {
		retry.MinBackoff = 250 * time.Millisecond
	}
	if retry.MaxBackoff == 0 {
		retry.MaxBackoff = 2 * time.Second
	}
	providerName := strings.TrimSpace(cfg.ProviderName)
	if providerName == "" {
		providerName = "openai-compatible"
	}
	return &Client{baseURL: base, apiKey: cfg.APIKey, httpClient: hc, headers: cfg.Headers, retry: retry, providerName: providerName}
}

func (c *Client) Name() string { return c.providerName }

func (c *Client) Capabilities() llm.Capabilities { return DefaultCapabilities() }

// DefaultCapabilities는 OpenAI-compatible Responses API가 지원하는 기능 계약이에요.
func DefaultCapabilities() llm.Capabilities {
	return llm.Capabilities{
		Tools:              true,
		CustomTools:        true,
		Reasoning:          true,
		ReasoningSummaries: true,
		StructuredOutput:   true,
		Streaming:          true,
		ToolChoice:         true,
		ParallelToolCalls:  true,
		PromptRefs:         true,
		PreviousResponseID: true,
	}
}

func (c *Client) Generate(ctx context.Context, req llm.Request) (*llm.Response, error) {
	hreq, payload, err := c.newResponsesRequest(ctx, req, false)
	if err != nil {
		return nil, err
	}
	res, err := c.do(hreq, payload)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("openai-compatible responses API returned %s: %s", res.Status, string(data))
	}
	return ParseResponsesResponse(data, c.Name())
}

func (c *Client) newResponsesRequest(ctx context.Context, req llm.Request, stream bool) (*http.Request, []byte, error) {
	body, err := BuildResponsesRequest(req)
	if err != nil {
		return nil, nil, err
	}
	if stream {
		body["stream"] = true
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, nil, err
	}
	u, err := url.JoinPath(c.baseURL, "responses")
	if err != nil {
		return nil, nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return nil, nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if stream {
		hreq.Header.Set("Accept", "text/event-stream")
	}
	if c.apiKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	for k, v := range c.headers {
		hreq.Header.Set(k, v)
	}
	return hreq, payload, nil
}

func BuildResponsesRequest(req llm.Request) (map[string]any, error) {
	if req.Model == "" {
		return nil, errors.New("model is required")
	}
	body := map[string]any{"model": req.Model}
	if req.Instructions != "" {
		body["instructions"] = req.Instructions
	}
	input, err := buildInput(req)
	if err != nil {
		return nil, err
	}
	if len(input) > 0 {
		body["input"] = input
	}
	if req.Prompt != nil {
		p := map[string]any{"id": req.Prompt.ID}
		if req.Prompt.Version != "" {
			p["version"] = req.Prompt.Version
		}
		if len(req.Prompt.Variables) > 0 {
			p["variables"] = req.Prompt.Variables
		}
		body["prompt"] = p
	}
	if len(req.Tools) > 0 {
		tools := make([]any, 0, len(req.Tools))
		for _, tool := range req.Tools {
			tools = append(tools, buildTool(tool))
		}
		body["tools"] = tools
	}
	if tc := buildToolChoice(req.ToolChoice); tc != nil {
		body["tool_choice"] = tc
	}
	if req.Reasoning != nil {
		reasoning := map[string]any{}
		if req.Reasoning.Effort != "" {
			reasoning["effort"] = req.Reasoning.Effort
		}
		if req.Reasoning.Summary != "" {
			reasoning["summary"] = req.Reasoning.Summary
		}
		if len(reasoning) > 0 {
			body["reasoning"] = reasoning
		}
	}
	if req.TextFormat != nil {
		body["text"] = map[string]any{"format": buildTextFormat(*req.TextFormat)}
	}
	if req.MaxOutputTokens > 0 {
		body["max_output_tokens"] = req.MaxOutputTokens
	}
	if req.MaxToolCalls > 0 {
		body["max_tool_calls"] = req.MaxToolCalls
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if req.Store != nil {
		body["store"] = *req.Store
	}
	if req.PreviousResponseID != "" {
		body["previous_response_id"] = req.PreviousResponseID
	}
	if len(req.Include) > 0 {
		body["include"] = req.Include
	}
	if len(req.Metadata) > 0 {
		body["metadata"] = req.Metadata
	}
	if req.ParallelToolCalls != nil {
		body["parallel_tool_calls"] = *req.ParallelToolCalls
	}
	if req.SafetyIdentifier != "" {
		body["safety_identifier"] = req.SafetyIdentifier
	}
	if req.PromptCacheKey != "" {
		body["prompt_cache_key"] = req.PromptCacheKey
	}
	return body, nil
}

func buildInput(req llm.Request) ([]any, error) {
	input := make([]any, 0, len(req.Messages)+len(req.InputItems))
	for _, msg := range req.Messages {
		m := map[string]any{"role": string(msg.Role)}
		if len(msg.Parts) == 0 {
			m["content"] = msg.Content
		} else {
			parts := make([]any, 0, len(msg.Parts))
			for _, part := range msg.Parts {
				if len(part.Raw) > 0 {
					var raw any
					if err := json.Unmarshal(part.Raw, &raw); err != nil {
						return nil, err
					}
					parts = append(parts, raw)
					continue
				}
				switch {
				case part.Text != "":
					parts = append(parts, map[string]any{"type": "input_text", "text": part.Text})
				case part.ImageURL != "":
					parts = append(parts, map[string]any{"type": "input_image", "image_url": part.ImageURL})
				case part.FileID != "":
					parts = append(parts, map[string]any{"type": "input_file", "file_id": part.FileID})
				}
			}
			m["content"] = parts
		}
		input = append(input, m)
	}
	for _, item := range req.InputItems {
		raw, err := buildItem(item)
		if err != nil {
			return nil, err
		}
		input = append(input, raw)
	}
	return input, nil
}

func buildItem(item llm.Item) (any, error) {
	if len(item.ProviderRaw) > 0 && item.Type != llm.ItemFunctionOutput && item.Type != llm.ItemCustomToolOutput {
		var raw any
		if err := json.Unmarshal(item.ProviderRaw, &raw); err != nil {
			return nil, err
		}
		return raw, nil
	}
	switch item.Type {
	case llm.ItemFunctionOutput, llm.ItemCustomToolOutput:
		if item.ToolResult == nil {
			return nil, errors.New("tool result item missing ToolResult")
		}
		out := item.ToolResult.Output
		if item.ToolResult.Error != "" {
			out = `{"error":` + strconvQuote(item.ToolResult.Error) + `}`
		}
		typeName := "function_call_output"
		if item.Type == llm.ItemCustomToolOutput || item.ToolResult.Custom {
			typeName = "custom_tool_call_output"
		}
		return map[string]any{"type": typeName, "call_id": item.ToolResult.CallID, "output": out}, nil
	case llm.ItemMessage:
		return map[string]any{"type": "message", "role": string(item.Role), "content": item.Content}, nil
	default:
		if len(item.ProviderRaw) > 0 {
			var raw any
			if err := json.Unmarshal(item.ProviderRaw, &raw); err != nil {
				return nil, err
			}
			return raw, nil
		}
		return nil, fmt.Errorf("cannot marshal input item type %q", item.Type)
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func buildTool(tool llm.Tool) map[string]any {
	kind := tool.Kind
	if kind == "" {
		kind = llm.ToolFunction
	}
	if kind == llm.ToolBuiltin {
		m := map[string]any{"type": tool.Name}
		for k, v := range tool.ProviderOptions {
			m[k] = v
		}
		return m
	}
	m := map[string]any{"type": string(kind), "name": tool.Name}
	if tool.Description != "" {
		m["description"] = tool.Description
	}
	switch kind {
	case llm.ToolFunction:
		if tool.Parameters != nil {
			m["parameters"] = tool.Parameters
		}
		if tool.Strict != nil {
			m["strict"] = *tool.Strict
		}
	case llm.ToolCustom:
		if tool.Grammar != nil {
			m["grammar"] = map[string]any{"syntax": tool.Grammar.Syntax, "definition": tool.Grammar.Definition}
		}
	}
	for k, v := range tool.ProviderOptions {
		m[k] = v
	}
	return m
}

func buildToolChoice(choice llm.ToolChoice) any {
	switch choice.Mode {
	case "":
		return nil
	case llm.ToolChoiceAuto, llm.ToolChoiceNone, llm.ToolChoiceRequired:
		return string(choice.Mode)
	case llm.ToolChoiceFunction:
		return map[string]any{"type": "function", "name": choice.Name}
	case llm.ToolChoiceAllowed:
		tools := make([]any, 0, len(choice.AllowedTools))
		for _, name := range choice.AllowedTools {
			tools = append(tools, map[string]any{"type": "function", "name": name})
		}
		return map[string]any{"type": "allowed_tools", "mode": "auto", "tools": tools}
	default:
		return nil
	}
}

func buildTextFormat(tf llm.TextFormat) map[string]any {
	switch tf.Type {
	case "json_object":
		return map[string]any{"type": "json_object"}
	case "json_schema", "":
		m := map[string]any{"type": "json_schema", "name": tf.Name, "schema": tf.Schema, "strict": tf.Strict}
		if tf.Description != "" {
			m["description"] = tf.Description
		}
		return m
	default:
		return map[string]any{"type": tf.Type}
	}
}

func (c *Client) do(req *http.Request, payload []byte) (*http.Response, error) {
	attempts := c.retry.MaxRetries + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			backoff := c.retry.MinBackoff << (i - 1)
			if backoff > c.retry.MaxBackoff {
				backoff = c.retry.MaxBackoff
			}
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(backoff):
			}
		}
		clone := req.Clone(req.Context())
		clone.Body = io.NopCloser(bytes.NewReader(payload))
		res, err := c.httpClient.Do(clone)
		if err != nil {
			lastErr = err
			continue
		}
		if res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500 {
			lastErr = fmt.Errorf("retryable status: %s", res.Status)
			_, _ = io.Copy(io.Discard, res.Body)
			_ = res.Body.Close()
			continue
		}
		return res, nil
	}
	return nil, lastErr
}
