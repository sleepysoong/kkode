package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sleepysoong/kkode/agent"
	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/providers/codexcli"
	"github.com/sleepysoong/kkode/providers/copilot"
	"github.com/sleepysoong/kkode/providers/omniroute"
	"github.com/sleepysoong/kkode/providers/openai"
	ktools "github.com/sleepysoong/kkode/tools"
	"github.com/sleepysoong/kkode/transcript"
	"github.com/sleepysoong/kkode/workspace"
)

// ProviderHandle은 provider와 종료 함수를 함께 들고 다니는 실행 핸들이에요.
type ProviderHandle struct {
	Provider llm.Provider
	Close    func() error
}

// WorkspaceOptions는 workspace 경계와 쓰기 모드를 정하는 옵션이에요.
type WorkspaceOptions struct {
	Root     string
	ReadOnly bool
}

// AgentOptions는 CLI와 gateway가 같은 agent 조립 경로를 쓰도록 모아둔 옵션이에요.
type AgentOptions struct {
	Model         string
	Instructions  string
	BaseRequest   llm.Request
	MaxIterations int
	NoWeb         bool
	WebMaxBytes   int64
	Transcript    *transcript.Transcript
	Guardrails    agent.Guardrails
	Observer      agent.Observer
}

// BuildProvider는 환경변수 기반 provider 생성을 한 곳에서 처리해요.
func BuildProvider(name, root string) (ProviderHandle, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "openai", "openai-compatible":
		return ProviderHandle{Provider: openai.New(openai.Config{BaseURL: os.Getenv("OPENAI_BASE_URL"), APIKey: os.Getenv("OPENAI_API_KEY")})}, nil
	case "omniroute":
		return ProviderHandle{Provider: omniroute.New(omniroute.Config{BaseURL: os.Getenv("OMNIROUTE_BASE_URL"), APIKey: EnvDefault("OMNIROUTE_API_KEY", os.Getenv("OPENAI_API_KEY")), SessionID: os.Getenv("OMNIROUTE_SESSION_ID"), Progress: EnvBool("OMNIROUTE_PROGRESS")})}, nil
	case "copilot", "github-copilot":
		client := copilot.New(copilot.Config{WorkingDirectory: root, GitHubToken: EnvDefault("COPILOT_GITHUB_TOKEN", EnvDefault("GH_TOKEN", os.Getenv("GITHUB_TOKEN"))), ApproveAll: EnvBool("COPILOT_APPROVE_ALL")})
		return ProviderHandle{Provider: client, Close: client.Close}, nil
	case "codex", "codexcli", "codex-cli":
		return ProviderHandle{Provider: codexcli.New(codexcli.Config{WorkingDirectory: root, Sandbox: EnvDefault("CODEX_SANDBOX", "read-only"), Approval: EnvDefault("CODEX_APPROVAL", "never"), Ephemeral: EnvBool("CODEX_EPHEMERAL")})}, nil
	default:
		return ProviderHandle{}, fmt.Errorf("unknown provider: %s", name)
	}
}

// DefaultModel은 provider별 기본 모델을 정해요.
func DefaultModel(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex", "codexcli", "codex-cli":
		return "gpt-5.3-codex"
	default:
		return "gpt-5-mini"
	}
}

// NewWorkspace는 root를 절대 경로로 정규화하고 workspace 정책을 만들어요.
func NewWorkspace(opts WorkspaceOptions) (*workspace.Workspace, string, error) {
	root := opts.Root
	if root == "" {
		root = "."
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, "", err
	}
	policy := llm.ApprovalPolicy{Mode: llm.ApprovalAllowAll, AllowedPaths: []string{absRoot}}
	if opts.ReadOnly {
		policy.Mode = llm.ApprovalReadOnly
	}
	ws, err := workspace.New(absRoot, policy)
	if err != nil {
		return nil, "", err
	}
	return ws, absRoot, nil
}

// NewAgent는 표준 file/web tool만 연결해서 agent를 만들어요.
func NewAgent(provider llm.Provider, ws *workspace.Workspace, opts AgentOptions) (*agent.Agent, error) {
	if ws == nil {
		return nil, fmt.Errorf("workspace is required")
	}
	if opts.Model == "" && provider != nil {
		opts.Model = DefaultModel(provider.Name())
	}
	toolDefs, toolHandlers := ktools.FileTools(ws)
	if !opts.NoWeb {
		webDefs, webHandlers := ktools.WebTools(ktools.WebConfig{MaxBytes: opts.WebMaxBytes})
		toolDefs = append(toolDefs, webDefs...)
		for name, handler := range webHandlers {
			toolHandlers[name] = handler
		}
	}
	return agent.New(agent.Config{
		Provider:      provider,
		Model:         opts.Model,
		Instructions:  opts.Instructions,
		BaseRequest:   opts.BaseRequest,
		Tools:         toolDefs,
		ToolHandlers:  toolHandlers,
		MaxIterations: opts.MaxIterations,
		Transcript:    opts.Transcript,
		Guardrails:    opts.Guardrails,
		Observer:      opts.Observer,
	})
}

// CSV는 쉼표로 구분한 환경변수/flag 값을 빈 항목 없이 잘라요.
func CSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// EnvDefault는 환경변수가 비어 있으면 fallback을 돌려줘요.
func EnvDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// EnvBool은 흔히 쓰는 true 표기를 bool로 바꿔요.
func EnvBool(key string) bool {
	return EnvBoolValue(os.Getenv(key))
}

// EnvBoolDefault는 환경변수가 비어 있을 때 fallback bool을 유지해요.
func EnvBoolDefault(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return EnvBoolValue(value)
}

// EnvBoolValue는 문자열 하나를 bool로 해석해요.
func EnvBoolValue(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "1" || value == "true" || value == "yes" || value == "y" || value == "on"
}

// EnvInt는 양수 정수 환경변수를 읽고 실패하면 fallback을 써요.
func EnvInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	var out int
	if _, err := fmt.Sscanf(value, "%d", &out); err != nil || out <= 0 {
		return fallback
	}
	return out
}

// EnvInt64는 양수 int64 환경변수를 읽고 실패하면 fallback을 써요.
func EnvInt64(key string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	var out int64
	if _, err := fmt.Sscanf(value, "%d", &out); err != nil || out <= 0 {
		return fallback
	}
	return out
}
