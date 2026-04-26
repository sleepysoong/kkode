package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sleepysoong/kkode/llm"
)

type responseEnvelope struct {
	ID                 string            `json:"id"`
	Model              string            `json:"model"`
	Status             string            `json:"status"`
	Output             []json.RawMessage `json:"output"`
	OutputText         string            `json:"output_text"`
	PreviousResponseID string            `json:"previous_response_id"`
	Usage              struct {
		InputTokens         int `json:"input_tokens"`
		OutputTokens        int `json:"output_tokens"`
		TotalTokens         int `json:"total_tokens"`
		OutputTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"output_tokens_details"`
	} `json:"usage"`
}

func ParseResponsesResponse(data []byte, providerName string) (*llm.Response, error) {
	var env responseEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	resp := &llm.Response{
		ID:                 env.ID,
		Provider:           providerName,
		Model:              env.Model,
		Status:             env.Status,
		Text:               env.OutputText,
		PreviousResponseID: env.PreviousResponseID,
		Raw:                append([]byte(nil), data...),
		Usage: llm.Usage{
			InputTokens:     env.Usage.InputTokens,
			OutputTokens:    env.Usage.OutputTokens,
			TotalTokens:     env.Usage.TotalTokens,
			ReasoningTokens: env.Usage.OutputTokensDetails.ReasoningTokens,
		},
	}
	var textParts []string
	for _, raw := range env.Output {
		item, err := parseOutputItem(raw)
		if err != nil {
			return nil, err
		}
		resp.Output = append(resp.Output, item)
		switch {
		case item.ToolCall != nil:
			resp.ToolCalls = append(resp.ToolCalls, *item.ToolCall)
		case item.Reasoning != nil:
			resp.Reasoning = append(resp.Reasoning, *item.Reasoning)
		case item.Type == llm.ItemMessage && item.Content != "":
			textParts = append(textParts, item.Content)
		}
	}
	if resp.Text == "" && len(textParts) > 0 {
		resp.Text = strings.Join(textParts, "")
	}
	return resp, nil
}

func parseOutputItem(raw json.RawMessage) (llm.Item, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return llm.Item{}, err
	}
	switch head.Type {
	case "message":
		var msg struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			return llm.Item{}, err
		}
		var parts []string
		for _, c := range msg.Content {
			if c.Type == "output_text" || c.Type == "text" {
				parts = append(parts, c.Text)
			}
		}
		return llm.Item{Type: llm.ItemMessage, Role: llm.Role(msg.Role), Content: strings.Join(parts, ""), ProviderRaw: raw}, nil
	case "function_call":
		var fc struct {
			ID, CallID, Name string
			Arguments        json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(raw, &fc); err != nil {
			return llm.Item{}, err
		}
		args := normalizeArguments(fc.Arguments)
		call := &llm.ToolCall{ID: fc.ID, CallID: fc.CallID, Name: fc.Name, Arguments: args}
		return llm.Item{Type: llm.ItemFunctionCall, ToolCall: call, ProviderRaw: raw}, nil
	case "custom_tool_call":
		var cc struct{ ID, CallID, Name, Input string }
		if err := json.Unmarshal(raw, &cc); err != nil {
			return llm.Item{}, err
		}
		call := &llm.ToolCall{ID: cc.ID, CallID: cc.CallID, Name: cc.Name, Arguments: json.RawMessage(strconvQuote(cc.Input)), Custom: true}
		return llm.Item{Type: llm.ItemCustomToolCall, ToolCall: call, ProviderRaw: raw}, nil
	case "reasoning":
		var r struct {
			ID      string `json:"id"`
			Summary []struct {
				Text string `json:"text"`
			} `json:"summary"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			EncryptedContent string `json:"encrypted_content"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return llm.Item{}, err
		}
		ri := &llm.ReasoningItem{ID: r.ID, EncryptedContent: r.EncryptedContent, Raw: raw}
		for _, s := range r.Summary {
			ri.Summary = append(ri.Summary, s.Text)
		}
		for _, c := range r.Content {
			ri.Text = append(ri.Text, c.Text)
		}
		return llm.Item{Type: llm.ItemReasoning, Reasoning: ri, ProviderRaw: raw}, nil
	default:
		return llm.Item{Type: llm.ItemUnknown, ProviderRaw: raw}, nil
	}
}

func normalizeArguments(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return json.RawMessage(s)
	}
	return raw
}
