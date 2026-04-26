package llm

import (
	"context"
	"encoding/json"
	"io"
)

type StreamProvider interface {
	Provider
	Stream(ctx context.Context, req Request) (EventStream, error)
}

type StreamEventType string

const (
	StreamEventUnknown        StreamEventType = "unknown"
	StreamEventStarted        StreamEventType = "started"
	StreamEventTextDelta      StreamEventType = "text_delta"
	StreamEventReasoningDelta StreamEventType = "reasoning_delta"
	StreamEventToolCall       StreamEventType = "tool_call"
	StreamEventToolResult     StreamEventType = "tool_result"
	StreamEventCompleted      StreamEventType = "completed"
	StreamEventError          StreamEventType = "error"
)

type StreamEvent struct {
	Type      StreamEventType
	Delta     string
	Response  *Response
	ToolCall  *ToolCall
	Error     error
	Provider  string
	EventName string
	Raw       json.RawMessage
}

type EventStream interface {
	Recv() (StreamEvent, error)
	Close() error
}

type channelStream struct {
	ctx    context.Context
	cancel context.CancelFunc
	events <-chan StreamEvent
	closer io.Closer
}

func NewChannelStream(ctx context.Context, events <-chan StreamEvent, closer io.Closer) EventStream {
	cctx, cancel := context.WithCancel(ctx)
	return &channelStream{ctx: cctx, cancel: cancel, events: events, closer: closer}
}

func (s *channelStream) Recv() (StreamEvent, error) {
	select {
	case <-s.ctx.Done():
		return StreamEvent{}, s.ctx.Err()
	case ev, ok := <-s.events:
		if !ok {
			return StreamEvent{}, io.EOF
		}
		return ev, nil
	}
}

func (s *channelStream) Close() error {
	s.cancel()
	if s.closer != nil {
		return s.closer.Close()
	}
	return nil
}
