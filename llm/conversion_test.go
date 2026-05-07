package llm

import (
	"context"
	"testing"
)

func TestAdaptedProviderRunsConversionCallerAndResponseMapping(t *testing.T) {
	converter := fakeConverter{}
	caller := &fakeCaller{}
	provider := &AdaptedProvider{
		ProviderName:         "fake-api",
		ProviderCapabilities: Capabilities{Tools: true},
		Converter:            converter,
		Caller:               caller,
	}
	resp, err := provider.Generate(context.Background(), Request{Model: "fake-model", Messages: []Message{UserText("안녕")}})
	if err != nil {
		t.Fatal(err)
	}
	if caller.got.Operation != "responses.create" || caller.got.Model != "fake-model" {
		t.Fatalf("변환된 provider 요청이 이상해요: %+v", caller.got)
	}
	if resp.Provider != "fake-api" || resp.Model != "fake-model" || resp.Text != "ok" {
		t.Fatalf("표준 응답 보정이 이상해요: %+v", resp)
	}
	if !provider.Capabilities().Tools {
		t.Fatalf("capability를 그대로 노출해야 해요")
	}
}

func TestAdaptedProviderRunsStreamingConversionAndCaller(t *testing.T) {
	converter := fakeConverter{}
	streamer := &fakeStreamer{}
	provider := &AdaptedProvider{
		ProviderName:  "fake-api",
		Converter:     converter,
		Streamer:      streamer,
		Options:       ConvertOptions{Operation: "responses.create"},
		StreamOptions: ConvertOptions{Operation: "responses.stream"},
	}
	stream, err := provider.Stream(context.Background(), Request{Model: "fake-model", Messages: []Message{UserText("안녕")}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if streamer.got.Operation != "responses.stream" || streamer.got.Model != "fake-model" || !streamer.got.Stream {
		t.Fatalf("stream 변환 요청이 이상해요: %+v", streamer.got)
	}
	ev, err := stream.Recv()
	if err != nil || ev.Type != StreamEventCompleted {
		t.Fatalf("stream event가 이상해요: %+v err=%v", ev, err)
	}
}

type fakeConverter struct{}

func (fakeConverter) ConvertRequest(ctx context.Context, req Request, opts ConvertOptions) (ProviderRequest, error) {
	operation := opts.Operation
	if operation == "" {
		operation = "responses.create"
	}
	return ProviderRequest{Operation: operation, Model: req.Model, Body: map[string]any{"model": req.Model}, Stream: opts.Stream}, nil
}

func (fakeConverter) ConvertResponse(ctx context.Context, result ProviderResult) (*Response, error) {
	return &Response{Provider: result.Provider, Model: result.Model, Status: "completed", Text: "ok"}, nil
}

type fakeCaller struct{ got ProviderRequest }

func (f *fakeCaller) CallProvider(ctx context.Context, req ProviderRequest) (ProviderResult, error) {
	f.got = req
	return ProviderResult{}, nil
}

type fakeStreamer struct{ got ProviderRequest }

func (f *fakeStreamer) StreamProvider(ctx context.Context, req ProviderRequest) (EventStream, error) {
	f.got = req
	events := make(chan StreamEvent, 1)
	events <- StreamEvent{Type: StreamEventCompleted}
	close(events)
	return NewChannelStream(ctx, events, nil), nil
}
