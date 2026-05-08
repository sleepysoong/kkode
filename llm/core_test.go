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

func TestTemplateRenderCacheUsesTextInKey(t *testing.T) {
	first, err := (Template{Name: "shared", Text: "first {{.Name}}"}).Render(map[string]any{"Name": "kkode"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := (Template{Name: "shared", Text: "second {{.Name}}"}).Render(map[string]any{"Name": "kkode"})
	if err != nil {
		t.Fatal(err)
	}
	if first != "first kkode" || second != "second kkode" {
		t.Fatalf("same-name templates with different text should not share parsed bodies: first=%q second=%q", first, second)
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

func TestRouterUsesLongestAliasPrefix(t *testing.T) {
	shortProvider := namedProvider{name: "short"}
	longProvider := namedProvider{name: "long"}
	r := NewRouter()
	r.Register("short", shortProvider)
	r.Register("long", longProvider)
	r.Alias("gpt-", "short")
	r.Alias("gpt-5-", "long")
	for i := 0; i < 20; i++ {
		p, model, err := r.ProviderFor("gpt-5-mini")
		if err != nil {
			t.Fatal(err)
		}
		if p.Name() != "long" || model != "mini" {
			t.Fatalf("가장 긴 alias prefix가 이겨야 해요: provider=%s model=%s", p.Name(), model)
		}
	}
	var nilRouter *Router
	if _, _, err := nilRouter.ProviderFor("gpt-5-mini"); err == nil {
		t.Fatal("nil router는 명확한 오류를 반환해야 해요")
	}
}

type scriptedTextProvider struct{}

func (scriptedTextProvider) Name() string               { return "scripted" }
func (scriptedTextProvider) Capabilities() Capabilities { return Capabilities{} }
func (scriptedTextProvider) Generate(ctx context.Context, req Request) (*Response, error) {
	return &Response{Text: "ok"}, nil
}

type namedProvider struct{ name string }

func (p namedProvider) Name() string { return p.name }
func (p namedProvider) Capabilities() Capabilities {
	return Capabilities{}
}
func (p namedProvider) Generate(ctx context.Context, req Request) (*Response, error) {
	return TextResponse(p.name, req.Model, "ok"), nil
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

func TestRenderTranscriptPromptAndTextResponse(t *testing.T) {
	got := RenderTranscriptPrompt(Request{Instructions: "rules", Messages: []Message{UserText("hi")}, InputItems: []Item{{Content: "tail"}}}, TranscriptPromptOptions{InstructionHeader: "Instructions:"})
	want := "Instructions:\nrules\n\nUSER: hi\ntail"
	if got != want {
		t.Fatalf("prompt=%q", got)
	}
	resp := TextResponse("p", "m", "hello")
	if resp.Provider != "p" || resp.Model != "m" || resp.Status != "completed" || resp.Text != "hello" || len(resp.Output) != 1 || resp.Output[0].Role != RoleAssistant {
		t.Fatalf("resp=%#v", resp)
	}
}

func TestCapabilitiesToMapExposesOnlyEnabledCapabilities(t *testing.T) {
	caps := Capabilities{Tools: true, Streaming: true, A2A: true}
	got := caps.ToMap()
	if got["tools"] != true || got["streaming"] != true || got["a2a"] != true {
		t.Fatalf("enabled capability가 map에 있어야 해요: %#v", got)
	}
	if _, ok := got["skills"]; ok {
		t.Fatalf("false capability는 생략해야 해요: %#v", got)
	}
}
