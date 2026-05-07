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
// 새 provider는 보통 RequestConverter/ResponseConverter/ProviderCaller를 조합하거나,
// 한 타입이 양쪽 변환을 모두 처리할 때 이 계약을 그대로 구현하면 돼요.
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
// provider별 차이는 converter/caller/streamer에 격리해요.
type AdaptedProvider struct {
	ProviderName         string
	ProviderCapabilities Capabilities
	// Converter는 기존 양방향 변환기를 위한 편의 필드예요.
	// 더 확장 가능한 provider는 RequestConverter와 ResponseConverter를 따로 꽂아도 돼요.
	Converter Converter
	// RequestConverter는 표준 요청을 provider/source 요청으로 바꾸는 전용 변환기예요.
	RequestConverter RequestConverter
	// ResponseConverter는 provider/source 응답을 표준 응답으로 되돌리는 전용 변환기예요.
	ResponseConverter ResponseConverter
	Caller            ProviderCaller
	Streamer          ProviderStreamCaller
	Options           ConvertOptions
	StreamOptions     ConvertOptions
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
	return p.pipeline().Generate(ctx, req)
}

func (p *AdaptedProvider) Stream(ctx context.Context, req Request) (EventStream, error) {
	if p == nil {
		return nil, fmt.Errorf("provider adapter가 필요해요")
	}
	return p.pipeline().Stream(ctx, req)
}

func (p *AdaptedProvider) requestConverter() RequestConverter {
	if p == nil {
		return nil
	}
	if p.RequestConverter != nil {
		return p.RequestConverter
	}
	return p.Converter
}

func (p *AdaptedProvider) responseConverter() ResponseConverter {
	if p == nil {
		return nil
	}
	if p.ResponseConverter != nil {
		return p.ResponseConverter
	}
	return p.Converter
}

func (p *AdaptedProvider) pipeline() ProviderPipeline {
	return ProviderPipeline{
		ProviderName:      p.Name(),
		RequestConverter:  p.requestConverter(),
		ResponseConverter: p.responseConverter(),
		Caller:            p.Caller,
		Streamer:          p.Streamer,
		Options:           p.Options,
		StreamOptions:     p.StreamOptions,
	}
}
