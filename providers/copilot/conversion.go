package copilot

import (
	"context"
	"fmt"
	"strings"

	"github.com/sleepysoong/kkode/llm"
)

const sessionSendOperation = "copilot.session.send"

type sessionSendPayload struct {
	Request llm.Request
	Prompt  string
}

// SessionConverter는 표준 Request를 Copilot SDK session prompt 호출로 바꿔요.
type SessionConverter struct{}

func (SessionConverter) ConvertRequest(ctx context.Context, req llm.Request, opts llm.ConvertOptions) (llm.ProviderRequest, error) {
	operation := opts.Operation
	if operation == "" {
		operation = sessionSendOperation
	}
	prompt := renderPrompt(req)
	if strings.TrimSpace(prompt) == "" {
		return llm.ProviderRequest{}, fmt.Errorf("copilot session prompt가 필요해요")
	}
	return llm.ProviderRequest{
		Operation: operation,
		Model:     req.Model,
		Raw:       sessionSendPayload{Request: req, Prompt: prompt},
	}, nil
}

func (SessionConverter) ConvertResponse(ctx context.Context, result llm.ProviderResult) (*llm.Response, error) {
	switch raw := result.Raw.(type) {
	case *llm.Response:
		return raw, nil
	case string:
		return llm.TextResponse(result.Provider, result.Model, raw), nil
	}
	if len(result.Body) > 0 {
		return llm.TextResponse(result.Provider, result.Model, string(result.Body)), nil
	}
	return nil, fmt.Errorf("copilot session 응답이 비어 있어요")
}
