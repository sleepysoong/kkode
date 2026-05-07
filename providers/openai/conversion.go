package openai

import (
	"context"
	"fmt"

	"github.com/sleepysoong/kkode/llm"
)

const responsesOperation = "responses.create"

// ResponsesConverter는 kkode 표준 Request/Response와 OpenAI-compatible Responses API payload를 오가요.
// 새 OpenAI-compatible provider는 이 converter를 재사용하고 caller/source만 바꾸면 돼요.
type ResponsesConverter struct {
	ProviderName string
}

func (c ResponsesConverter) ConvertRequest(ctx context.Context, req llm.Request, opts llm.ConvertOptions) (llm.ProviderRequest, error) {
	body, err := BuildResponsesRequest(req)
	if err != nil {
		return llm.ProviderRequest{}, err
	}
	operation := opts.Operation
	if operation == "" {
		operation = responsesOperation
	}
	return llm.ProviderRequest{
		Operation: operation,
		Model:     req.Model,
		Metadata:  llm.CloneMetadata(req.Metadata),
		Body:      body,
		Stream:    opts.Stream,
	}, nil
}

func (c ResponsesConverter) ConvertResponse(ctx context.Context, result llm.ProviderResult) (*llm.Response, error) {
	if len(result.Body) == 0 {
		return nil, fmt.Errorf("OpenAI-compatible 응답 body가 비어 있어요")
	}
	provider := result.Provider
	if provider == "" {
		provider = c.ProviderName
	}
	return ParseResponsesResponse(result.Body, provider)
}
