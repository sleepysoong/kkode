package tools

import (
	"net/http"
	"time"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/workspace"
)

// SurfaceOptions는 agent, gateway, adapter가 같은 표준 tool 묶음을 만들 때 쓰는 옵션이에요.
type SurfaceOptions struct {
	Workspace   *workspace.Workspace
	NoWeb       bool
	WebMaxBytes int64
	HTTPClient  *http.Client
	UserAgent   string
	Timeout     time.Duration
}

// StandardToolSet은 file/shell/web 표준 tool surface를 한 묶음으로 조립해요.
// 권한이나 승인 단계는 여기 없고, 연결된 workspace와 HTTP 설정으로 바로 실행해요.
func StandardToolSet(opts SurfaceOptions) llm.ToolSet {
	defs, handlers := FileTools(opts.Workspace)
	set := llm.NewToolSet(defs, handlers)
	if opts.NoWeb {
		return set
	}
	webDefs, webHandlers := WebTools(WebConfig{HTTPClient: opts.HTTPClient, UserAgent: opts.UserAgent, MaxBytes: opts.WebMaxBytes, Timeout: opts.Timeout})
	set.Merge(llm.NewToolSet(webDefs, webHandlers))
	return set
}

// StandardTools는 기존 caller가 정의/handler를 따로 받을 수 있게 유지하는 wrapper예요.
func StandardTools(opts SurfaceOptions) ([]llm.Tool, llm.ToolRegistry) {
	return StandardToolSet(opts).Parts()
}
