package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/providers/httpjson"
	"github.com/sleepysoong/kkode/providers/internal/httptransport"
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

type RetryConfig = httptransport.RetryConfig

type Client struct {
	baseURL      string
	apiKey       string
	httpClient   *http.Client
	headers      map[string]string
	retry        RetryConfig
	providerName string
	caller       *httpjson.Caller
}

func New(cfg Config) *Client {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = defaultBaseURL
	}
	hc := httptransport.DefaultClient(cfg.HTTPClient)
	retry := httptransport.NormalizeRetry(cfg.Retry)
	providerName := strings.TrimSpace(cfg.ProviderName)
	if providerName == "" {
		providerName = "openai-compatible"
	}
	headers := httptransport.CloneHeaders(cfg.Headers)
	caller := httpjson.New(httpjson.Config{
		ProviderName:     providerName,
		BaseURL:          base,
		APIKey:           cfg.APIKey,
		HTTPClient:       hc,
		Headers:          headers,
		Retry:            retry,
		DefaultOperation: responsesOperation,
		Routes:           map[string]httpjson.Route{responsesOperation: {Path: "/responses"}},
	})
	return &Client{baseURL: base, apiKey: cfg.APIKey, httpClient: hc, headers: headers, retry: retry, providerName: providerName, caller: caller}
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
	adapter := llm.AdaptedProvider{
		ProviderName:         c.Name(),
		ProviderCapabilities: c.Capabilities(),
		Converter:            ResponsesConverter{ProviderName: c.Name()},
		Caller:               c,
		Options:              llm.ConvertOptions{Operation: responsesOperation},
	}
	return adapter.Generate(ctx, req)
}

func (c *Client) CallProvider(ctx context.Context, req llm.ProviderRequest) (llm.ProviderResult, error) {
	if req.Stream {
		return llm.ProviderResult{}, errors.New("stream 요청은 Stream API를 사용해야 해요")
	}
	if req.Operation != "" && req.Operation != responsesOperation {
		return llm.ProviderResult{}, fmt.Errorf("지원하지 않는 OpenAI-compatible operation이에요: %s", req.Operation)
	}
	if c == nil || c.caller == nil {
		return llm.ProviderResult{}, errors.New("OpenAI-compatible HTTP JSON caller가 필요해요")
	}
	return c.caller.CallProvider(ctx, req)
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
	return httptransport.DoWithRetry(c.httpClient, req, payload, c.retry)
}
