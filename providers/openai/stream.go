package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/sleepysoong/kkode/llm"
)

func (c *Client) Stream(ctx context.Context, req llm.Request) (llm.EventStream, error) {
	body, err := BuildResponsesRequest(req)
	if err != nil {
		return nil, err
	}
	body["stream"] = true
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	u, err := url.JoinPath(c.baseURL, "responses")
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "text/event-stream")
	if c.apiKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	for k, v := range c.headers {
		hreq.Header.Set(k, v)
	}
	res, err := c.httpClient.Do(hreq)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		return nil, fmt.Errorf("openai-compatible stream returned %s: %s", res.Status, string(data))
	}
	events := make(chan llm.StreamEvent, 32)
	go readSSE(ctx, res.Body, c.Name(), events)
	return llm.NewChannelStream(ctx, events, res.Body), nil
}

func readSSE(ctx context.Context, r io.Reader, provider string, out chan<- llm.StreamEvent) {
	defer close(out)
	s := bufio.NewScanner(r)
	// 큰 JSON event도 처리할 수 있게 buffer를 넉넉하게 잡아요.
	s.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var eventName string
	var dataLines []string
	flush := func() bool {
		if len(dataLines) == 0 {
			eventName = ""
			return true
		}
		data := strings.Join(dataLines, "\n")
		dataLines = nil
		if data == "[DONE]" {
			return false
		}
		ev := parseStreamEvent(eventName, []byte(data), provider)
		select {
		case <-ctx.Done():
			return false
		case out <- ev:
		}
		eventName = ""
		return true
	}
	for s.Scan() {
		line := s.Text()
		if line == "" {
			if !flush() {
				return
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			continue
		}
	}
	if err := s.Err(); err != nil {
		out <- llm.StreamEvent{Type: llm.StreamEventError, Provider: provider, Error: err}
		return
	}
	flush()
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
