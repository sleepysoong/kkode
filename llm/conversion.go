package llm

import (
	"context"
	"fmt"
)

// ConvertOptions는 같은 내부 Request를 provider별 endpoint나 stream mode로 바꿀 때 쓰는 힌트예요.
// core는 이 값을 해석하지 않고 converter에게 그대로 넘겨요.
type ConvertOptions struct {
	Operation string
	Stream    bool
}

// ProviderRequest는 provider-neutral Request가 실제 API/source 호출 직전 형태로 변환된 값이에요.
// HTTP provider는 Body를 JSON payload로 쓰고, CLI/SDK provider는 Raw나 Metadata에 provider 전용 값을 담을 수 있어요.
type ProviderRequest struct {
	Operation string
	Model     string
	Body      any
	Headers   map[string]string
	Metadata  map[string]string
	Stream    bool
	Raw       any
}

// ProviderResult는 API/source 호출 결과를 표준 Response로 파싱하기 직전의 값이에요.
type ProviderResult struct {
	Provider string
	Model    string
	Body     []byte
	Headers  map[string][]string
	Raw      any
}

// RequestConverter는 내부 Request를 provider별 API/source 요청으로 바꿔요.
type RequestConverter interface {
	ConvertRequest(ctx context.Context, req Request, opts ConvertOptions) (ProviderRequest, error)
}

// ResponseConverter는 provider별 API/source 결과를 내부 Response로 되돌려요.
type ResponseConverter interface {
	ConvertResponse(ctx context.Context, result ProviderResult) (*Response, error)
}

// Converter는 양방향 변환 계약이에요.
// 새 provider는 보통 Converter와 ProviderCaller만 추가하면 Provider 인터페이스를 얻을 수 있어요.
type Converter interface {
	RequestConverter
	ResponseConverter
}

// ProviderCaller는 변환된 ProviderRequest를 실제 source에 보내는 경계예요.
// source는 HTTP API, subprocess, SDK session, in-memory fake 어디든 될 수 있어요.
type ProviderCaller interface {
	CallProvider(ctx context.Context, req ProviderRequest) (ProviderResult, error)
}

// ProviderStreamCaller는 변환된 ProviderRequest를 streaming source에 보내는 경계예요.
// SSE, JSONL, SDK event stream 모두 여기서 내부 EventStream으로 정규화해요.
type ProviderStreamCaller interface {
	StreamProvider(ctx context.Context, req ProviderRequest) (EventStream, error)
}

// AdaptedProvider는 "내부 요청 -> 변환 -> source 호출 -> 내부 응답" 흐름을 재사용하는 Provider 구현체예요.
// provider별 차이는 Converter와 ProviderCaller에만 격리해요.
type AdaptedProvider struct {
	ProviderName         string
	ProviderCapabilities Capabilities
	Converter            Converter
	Caller               ProviderCaller
	Streamer             ProviderStreamCaller
	Options              ConvertOptions
	StreamOptions        ConvertOptions
}

func (p *AdaptedProvider) Name() string {
	if p == nil || p.ProviderName == "" {
		return "adapted-provider"
	}
	return p.ProviderName
}

func (p *AdaptedProvider) Capabilities() Capabilities {
	if p == nil {
		return Capabilities{}
	}
	return p.ProviderCapabilities
}

func (p *AdaptedProvider) Generate(ctx context.Context, req Request) (*Response, error) {
	if p == nil {
		return nil, fmt.Errorf("provider adapter가 필요해요")
	}
	if p.Converter == nil {
		return nil, fmt.Errorf("provider converter가 필요해요")
	}
	if p.Caller == nil {
		return nil, fmt.Errorf("provider caller가 필요해요")
	}
	preq, err := p.Converter.ConvertRequest(ctx, req, p.Options)
	if err != nil {
		return nil, err
	}
	if preq.Model == "" {
		preq.Model = req.Model
	}
	result, err := p.Caller.CallProvider(ctx, preq)
	if err != nil {
		return nil, err
	}
	if result.Provider == "" {
		result.Provider = p.Name()
	}
	if result.Model == "" {
		result.Model = preq.Model
	}
	return p.Converter.ConvertResponse(ctx, result)
}

func (p *AdaptedProvider) Stream(ctx context.Context, req Request) (EventStream, error) {
	if p == nil {
		return nil, fmt.Errorf("provider adapter가 필요해요")
	}
	if p.Converter == nil {
		return nil, fmt.Errorf("provider converter가 필요해요")
	}
	if p.Streamer == nil {
		return nil, fmt.Errorf("provider stream caller가 필요해요")
	}
	opts := p.StreamOptions
	if opts.Operation == "" {
		opts.Operation = p.Options.Operation
	}
	opts.Stream = true
	preq, err := p.Converter.ConvertRequest(ctx, req, opts)
	if err != nil {
		return nil, err
	}
	if preq.Model == "" {
		preq.Model = req.Model
	}
	preq.Stream = true
	return p.Streamer.StreamProvider(ctx, preq)
}
