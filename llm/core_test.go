package llm

import (
	"context"
	"errors"
	"io"
	"testing"
)

func TestTemplateRenderAndValidate(t *testing.T) {
	got, err := (Template{Name: "x", Text: "hello {{.Name}}"}).Render(map[string]any{"Name": "kkode"})
	if err != nil || got != "hello kkode" {
		t.Fatalf("render got %q err %v", got, err)
	}
	if err := (Request{Model: "m", Messages: []Message{UserText("hi")}}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Request{Messages: []Message{UserText("hi")}}).Validate(); err == nil {
		t.Fatal("expected missing model error")
	}
}

func TestRouterAndUsage(t *testing.T) {
	p := scriptedTextProvider{}
	r := NewRouter()
	r.Register("openai", p)
	r.Register("default", p)
	resp, err := r.Generate(context.Background(), Request{Model: "openai/gpt", Messages: []Message{UserText("hi")}})
	if err != nil || resp.Text != "ok" {
		t.Fatalf("router resp=%#v err=%v", resp, err)
	}
	cost := (Usage{InputTokens: 1_000_000, OutputTokens: 500_000, ReasoningTokens: 100_000}).EstimatedCost(ModelPricing{InputPerMillion: 1, OutputPerMillion: 2, ReasoningOutputPerMillion: 3})
	if cost != 2.3 {
		t.Fatalf("cost=%v", cost)
	}
}

type scriptedTextProvider struct{}

func (scriptedTextProvider) Name() string               { return "scripted" }
func (scriptedTextProvider) Capabilities() Capabilities { return Capabilities{} }
func (scriptedTextProvider) Generate(ctx context.Context, req Request) (*Response, error) {
	return &Response{Text: "ok"}, nil
}

func TestChannelStream(t *testing.T) {
	ch := make(chan StreamEvent, 1)
	ch <- StreamEvent{Type: StreamEventTextDelta, Delta: "x"}
	close(ch)
	s := NewChannelStream(context.Background(), ch, nil)
	ev, err := s.Recv()
	if err != nil || ev.Delta != "x" {
		t.Fatalf("event=%#v err=%v", ev, err)
	}
	_, err = s.Recv()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestRedactSecrets(t *testing.T) {
	got := RedactSecrets("token=abc1234567890secretvalue sk-abcdefghijklmnopqrstuvwxyz")
	if got == "token=abc1234567890secretvalue sk-abcdefghijklmnopqrstuvwxyz" {
		t.Fatal("secret was not redacted")
	}
}
