package codexcli

import (
	"context"
	"fmt"
	"strings"

	"github.com/sleepysoong/kkode/llm"
)

const execOperation = "codex.exec"

type execPayload struct {
	Request llm.Request
	Prompt  string
}

// ExecConverter는 표준 Request를 Codex CLI exec source가 이해하는 prompt 실행 요청으로 바꿔요.
type ExecConverter struct{}

func (ExecConverter) ConvertRequest(ctx context.Context, req llm.Request, opts llm.ConvertOptions) (llm.ProviderRequest, error) {
	operation := opts.Operation
	if operation == "" {
		operation = execOperation
	}
	prompt := renderPrompt(req)
	if strings.TrimSpace(prompt) == "" {
		return llm.ProviderRequest{}, fmt.Errorf("codex exec prompt가 필요해요")
	}
	return llm.ProviderRequest{
		Operation: operation,
		Model:     req.Model,
		Raw:       execPayload{Request: req, Prompt: prompt},
	}, nil
}

func (ExecConverter) ConvertResponse(ctx context.Context, result llm.ProviderResult) (*llm.Response, error) {
	text := strings.TrimSpace(string(result.Body))
	return llm.TextResponse(result.Provider, result.Model, text), nil
}
