package openai

import (
	"context"
	"encoding/json"
	"io"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/providers/internal/httptransport"
)

func (c *Client) Stream(ctx context.Context, req llm.Request) (llm.EventStream, error) {
	hreq, payload, err := c.newResponsesRequest(ctx, req, true)
	if err != nil {
		return nil, err
	}
	res, err := c.do(hreq, payload)
	if err != nil {
		return nil, err
	}
	if !httptransport.IsSuccessStatus(res.StatusCode) {
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		return nil, httptransport.ErrorFromResponse("openai-compatible stream", res, data)
	}
	events := make(chan llm.StreamEvent, 32)
	go readSSE(ctx, res.Body, c.Name(), events)
	return llm.NewChannelStream(ctx, events, res.Body), nil
}

func readSSE(ctx context.Context, r io.Reader, provider string, out chan<- llm.StreamEvent) {
	defer close(out)
	err := httptransport.ReadSSE(ctx, r, func(eventName string, data []byte) bool {
		ev := parseStreamEvent(eventName, data, provider)
		select {
		case <-ctx.Done():
			return false
		case out <- ev:
			return true
		}
	})
	if err != nil {
		out <- llm.StreamEvent{Type: llm.StreamEventError, Provider: provider, Error: err}
	}
}

func parseStreamEvent(eventName string, raw []byte, provider string) llm.StreamEvent {
	var head struct {
		Type        string          `json:"type"`
		Delta       string          `json:"delta"`
		OutputIndex int             `json:"output_index"`
		Response    json.RawMessage `json:"response"`
		Item        json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return llm.StreamEvent{Type: llm.StreamEventError, Provider: provider, EventName: eventName, Raw: raw, Error: err}
	}
	name := firstNonEmpty(head.Type, eventName)
	ev := llm.StreamEvent{Type: llm.StreamEventUnknown, Provider: provider, EventName: name, Raw: append([]byte(nil), raw...)}
	switch name {
	case "response.created", "response.in_progress":
		ev.Type = llm.StreamEventStarted
	case "response.output_text.delta", "response.refusal.delta":
		ev.Type = llm.StreamEventTextDelta
		ev.Delta = head.Delta
	case "response.reasoning_delta", "response.reasoning_summary_text.delta":
		ev.Type = llm.StreamEventReasoningDelta
		ev.Delta = head.Delta
	case "response.output_item.added":
		item, _ := parseOutputItem(head.Item)
		if item.ToolCall != nil {
			ev.Type = llm.StreamEventToolCall
			ev.ToolCall = item.ToolCall
		}
	case "response.completed":
		ev.Type = llm.StreamEventCompleted
		if len(head.Response) > 0 {
			if resp, err := ParseResponsesResponse(head.Response, provider); err == nil {
				ev.Response = resp
			} else {
				ev.Error = err
				ev.Type = llm.StreamEventError
			}
		}
	case "response.failed", "error":
		ev.Type = llm.StreamEventError
	}
	return ev
}

func firstNonEmpty(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}
