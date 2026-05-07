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

func TestAdaptedProviderAllowsSplitConverters(t *testing.T) {
	requestConverter := &splitRequestConverter{}
	responseConverter := splitResponseConverter{}
	caller := &fakeCaller{}
	provider := &AdaptedProvider{
		ProviderName:         "split-api",
		RequestConverter:     requestConverter,
		ResponseConverter:    responseConverter,
		Caller:               caller,
		Options:              ConvertOptions{Operation: "custom.create"},
		ProviderCapabilities: Capabilities{StructuredOutput: true},
	}
	resp, err := provider.Generate(context.Background(), Request{Model: "split-model", Messages: []Message{UserText("안녕")}})
	if err != nil {
		t.Fatal(err)
	}
	if caller.got.Operation != "custom.create" || caller.got.Model != "split-model" {
		t.Fatalf("분리된 request converter 결과가 이상해요: %+v", caller.got)
	}
	if !requestConverter.called {
		t.Fatalf("request converter가 호출돼야 해요")
	}
	if resp.Provider != "split-api" || resp.Model != "split-model" || resp.Text != "split-ok" {
		t.Fatalf("분리된 response converter 결과가 이상해요: %+v", resp)
	}
	if !provider.Capabilities().StructuredOutput {
		t.Fatalf("capability를 그대로 노출해야 해요")
	}
}

func TestAdaptedProviderStreamOnlyNeedsRequestConverter(t *testing.T) {
	requestConverter := &splitRequestConverter{}
	streamer := &fakeStreamer{}
	provider := &AdaptedProvider{
		ProviderName:     "stream-only-api",
		RequestConverter: requestConverter,
		Streamer:         streamer,
		Options:          ConvertOptions{Operation: "custom.create"},
		StreamOptions:    ConvertOptions{Operation: "custom.stream"},
	}
	stream, err := provider.Stream(context.Background(), Request{Model: "stream-model", Messages: []Message{UserText("안녕")}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if streamer.got.Operation != "custom.stream" || streamer.got.Model != "stream-model" || !streamer.got.Stream {
		t.Fatalf("stream-only 변환 요청이 이상해요: %+v", streamer.got)
	}
	if !requestConverter.called {
		t.Fatalf("stream에서도 request converter가 호출돼야 해요")
	}
}

func TestProviderPipelineExposesPrepareCallDecodeStages(t *testing.T) {
	requestConverter := &splitRequestConverter{}
	responseConverter := splitResponseConverter{}
	caller := &fakeCaller{}
	pipeline := ProviderPipeline{
		ProviderName:      "pipeline-api",
		RequestConverter:  requestConverter,
		ResponseConverter: responseConverter,
		Caller:            caller,
		Options:           ConvertOptions{Operation: "pipeline.create"},
	}
	req := Request{Model: "pipeline-model", Messages: []Message{UserText("안녕")}}
	preq, err := pipeline.Prepare(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if preq.Operation != "pipeline.create" || preq.Model != "pipeline-model" || preq.Stream {
		t.Fatalf("prepare 단계 결과가 이상해요: %+v", preq)
	}
	result, err := pipeline.Call(context.Background(), preq)
	if err != nil {
		t.Fatal(err)
	}
	if caller.got.Operation != "pipeline.create" {
		t.Fatalf("caller가 변환 요청을 받아야 해요: %+v", caller.got)
	}
	if result.Provider != "pipeline-api" || result.Model != "pipeline-model" {
		t.Fatalf("call 단계 표준 보정이 이상해요: %+v", result)
	}
	resp, err := pipeline.Decode(context.Background(), result)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "pipeline-api" || resp.Model != "pipeline-model" || resp.Text != "split-ok" {
		t.Fatalf("decode 단계 결과가 이상해요: %+v", resp)
	}
	if !requestConverter.called {
		t.Fatalf("request converter가 호출돼야 해요")
	}
}

func TestProviderPipelinePrepareStreamFallsBackToDefaultOperation(t *testing.T) {
	requestConverter := &splitRequestConverter{}
	pipeline := ProviderPipeline{
		ProviderName:     "stream-api",
		RequestConverter: requestConverter,
		Options:          ConvertOptions{Operation: "default.create"},
	}
	preq, err := pipeline.PrepareStream(context.Background(), Request{Model: "m", Messages: []Message{UserText("안녕")}})
	if err != nil {
		t.Fatal(err)
	}
	if preq.Operation != "default.create" || !preq.Stream {
		t.Fatalf("stream prepare 기본 operation 보정이 이상해요: %+v", preq)
	}
}

func TestProviderPipelineAcceptsFunctionAdapters(t *testing.T) {
	var converted ProviderRequest
	pipeline := ProviderPipeline{
		ProviderName: "func-api",
		RequestConverter: RequestConverterFunc(func(ctx context.Context, req Request, opts ConvertOptions) (ProviderRequest, error) {
			return ProviderRequest{Operation: opts.Operation, Model: req.Model, Body: map[string]any{"input": req.Messages[0].Content}}, nil
		}),
		Caller: ProviderCallerFunc(func(ctx context.Context, req ProviderRequest) (ProviderResult, error) {
			converted = req
			return ProviderResult{Body: []byte(`func source raw`)}, nil
		}),
		ResponseConverter: ResponseConverterFunc(func(ctx context.Context, result ProviderResult) (*Response, error) {
			return &Response{Provider: result.Provider, Model: result.Model, Status: "completed", Text: string(result.Body)}, nil
		}),
		Options: ConvertOptions{Operation: "func.create"},
	}

	resp, err := pipeline.Generate(context.Background(), Request{Model: "func-model", Messages: []Message{UserText("함수형 source")}})
	if err != nil {
		t.Fatal(err)
	}
	if converted.Operation != "func.create" || converted.Model != "func-model" {
		t.Fatalf("함수 adapter가 변환 요청을 source에 넘겨야 해요: %+v", converted)
	}
	if resp.Provider != "func-api" || resp.Model != "func-model" || resp.Text != "func source raw" {
		t.Fatalf("함수 adapter 응답 정규화가 이상해요: %+v", resp)
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

type splitRequestConverter struct {
	called bool
}

func (c *splitRequestConverter) ConvertRequest(ctx context.Context, req Request, opts ConvertOptions) (ProviderRequest, error) {
	c.called = true
	return ProviderRequest{Operation: opts.Operation, Model: req.Model, Body: map[string]any{"model": req.Model}, Stream: opts.Stream}, nil
}

type splitResponseConverter struct{}

func (splitResponseConverter) ConvertResponse(ctx context.Context, result ProviderResult) (*Response, error) {
	return &Response{Provider: result.Provider, Model: result.Model, Status: "completed", Text: "split-ok"}, nil
}
