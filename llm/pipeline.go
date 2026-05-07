package llm

import (
	"context"
	"fmt"
)

// ProviderPipeline은 모든 provider 호출을 같은 단계로 실행하는 얇은 실행 파이프라인이에요.
// 새 source는 converter/caller/streamer만 꽂으면 `Request -> ProviderRequest -> ProviderResult -> Response` 흐름을 재사용해요.
type ProviderPipeline struct {
	ProviderName      string
	RequestConverter  RequestConverter
	ResponseConverter ResponseConverter
	Caller            ProviderCaller
	Streamer          ProviderStreamCaller
	Options           ConvertOptions
	StreamOptions     ConvertOptions
}

// Prepare는 표준 요청을 API/SDK/CLI 호출 직전의 provider 요청으로 바꿔요.
func (p ProviderPipeline) Prepare(ctx context.Context, req Request) (ProviderRequest, error) {
	return p.prepare(ctx, req, p.Options)
}

// PrepareStream은 streaming source 호출 직전의 provider 요청으로 바꿔요.
func (p ProviderPipeline) PrepareStream(ctx context.Context, req Request) (ProviderRequest, error) {
	opts := p.StreamOptions
	if opts.Operation == "" {
		opts.Operation = p.Options.Operation
	}
	opts.Stream = true
	preq, err := p.prepare(ctx, req, opts)
	if err != nil {
		return ProviderRequest{}, err
	}
	preq.Stream = true
	return preq, nil
}

// Call은 변환된 provider 요청을 실제 source에 보내요.
func (p ProviderPipeline) Call(ctx context.Context, preq ProviderRequest) (ProviderResult, error) {
	if p.Caller == nil {
		return ProviderResult{}, fmt.Errorf("provider caller가 필요해요")
	}
	result, err := p.Caller.CallProvider(ctx, preq)
	if err != nil {
		return ProviderResult{}, fmt.Errorf("provider source 호출 실패예요: %w", err)
	}
	return p.normalizeResult(preq, result), nil
}

// Decode는 provider/source 결과를 표준 응답으로 되돌려요.
func (p ProviderPipeline) Decode(ctx context.Context, result ProviderResult) (*Response, error) {
	if p.ResponseConverter == nil {
		return nil, fmt.Errorf("provider response converter가 필요해요")
	}
	resp, err := p.ResponseConverter.ConvertResponse(ctx, result)
	if err != nil {
		return nil, fmt.Errorf("provider 응답 변환 실패예요: %w", err)
	}
	return resp, nil
}

// Generate는 단발성 provider 호출의 전체 파이프라인을 실행해요.
func (p ProviderPipeline) Generate(ctx context.Context, req Request) (*Response, error) {
	preq, err := p.Prepare(ctx, req)
	if err != nil {
		return nil, err
	}
	result, err := p.Call(ctx, preq)
	if err != nil {
		return nil, err
	}
	return p.Decode(ctx, result)
}

// Stream은 streaming provider 호출의 변환과 source 호출을 실행해요.
func (p ProviderPipeline) Stream(ctx context.Context, req Request) (EventStream, error) {
	if p.Streamer == nil {
		return nil, fmt.Errorf("provider stream caller가 필요해요")
	}
	preq, err := p.PrepareStream(ctx, req)
	if err != nil {
		return nil, err
	}
	stream, err := p.Streamer.StreamProvider(ctx, preq)
	if err != nil {
		return nil, fmt.Errorf("provider stream source 호출 실패예요: %w", err)
	}
	return stream, nil
}

func (p ProviderPipeline) prepare(ctx context.Context, req Request, opts ConvertOptions) (ProviderRequest, error) {
	if p.RequestConverter == nil {
		return ProviderRequest{}, fmt.Errorf("provider request converter가 필요해요")
	}
	preq, err := p.RequestConverter.ConvertRequest(ctx, req, opts)
	if err != nil {
		return ProviderRequest{}, fmt.Errorf("provider 요청 변환 실패예요: %w", err)
	}
	if preq.Model == "" {
		preq.Model = req.Model
	}
	if opts.Stream {
		preq.Stream = true
	}
	return preq, nil
}

func (p ProviderPipeline) normalizeResult(preq ProviderRequest, result ProviderResult) ProviderResult {
	if result.Provider == "" {
		result.Provider = p.providerName()
	}
	if result.Model == "" {
		result.Model = preq.Model
	}
	return result
}

func (p ProviderPipeline) providerName() string {
	if p.ProviderName == "" {
		return "adapted-provider"
	}
	return p.ProviderName
}
