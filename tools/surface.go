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

// StandardTools는 file/shell/web 표준 tool surface를 한 번에 조립해요.
// 권한이나 승인 단계는 여기 없고, 연결된 workspace와 HTTP 설정으로 바로 실행해요.
func StandardTools(opts SurfaceOptions) ([]llm.Tool, llm.ToolRegistry) {
	defs, handlers := FileTools(opts.Workspace)
	if opts.NoWeb {
		return defs, handlers
	}
	webDefs, webHandlers := WebTools(WebConfig{HTTPClient: opts.HTTPClient, UserAgent: opts.UserAgent, MaxBytes: opts.WebMaxBytes, Timeout: opts.Timeout})
	defs = append(defs, webDefs...)
	mergeToolHandlers(handlers, webHandlers)
	return defs, handlers
}

func mergeToolHandlers(dst llm.ToolRegistry, src llm.ToolRegistry) {
	for name, handler := range src {
		dst[name] = handler
	}
}
