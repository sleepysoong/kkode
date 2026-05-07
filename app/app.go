package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sleepysoong/kkode/agent"
	"github.com/sleepysoong/kkode/llm"
	kruntime "github.com/sleepysoong/kkode/runtime"
	"github.com/sleepysoong/kkode/session"
	ktools "github.com/sleepysoong/kkode/tools"
	"github.com/sleepysoong/kkode/transcript"
	"github.com/sleepysoong/kkode/workspace"
)

// WorkspaceOptions는 workspace 경계를 정하는 옵션이에요.
type WorkspaceOptions struct {
	Root string
}

// AgentOptions는 CLI와 gateway가 같은 agent 조립 경로를 쓰도록 모아둔 옵션이에요.
type AgentOptions struct {
	Model         string
	Instructions  string
	ContextBlocks []string
	BaseRequest   llm.Request
	MaxIterations int
	NoWeb         bool
	WebMaxBytes   int64
	Transcript    *transcript.Transcript
	Guardrails    agent.Guardrails
	Observer      agent.Observer
}

// RuntimeOptions는 CLI/gateway가 같은 session runtime 기본값을 쓰도록 모아둔 옵션이에요.
type RuntimeOptions struct {
	ProjectRoot        string
	ProviderName       string
	Model              string
	AgentName          string
	Mode               session.AgentMode
	MaxHistoryTurns    int
	EnableTodos        bool
	Compaction         session.CompactionPolicy
	DisableCompaction  bool
	DisableTodoHelpers bool
}

// ProviderOptions는 저장된 MCP/skill/subagent manifest를 provider 생성에 반영하는 옵션이에요.
type ProviderOptions struct {
	MCPServers       map[string]llm.MCPServer
	SkillDirectories []string
	CustomAgents     []llm.Agent
	ContextBlocks    []string
}

// NewWorkspace는 root를 절대 경로로 정규화하고 즉시 실행형 workspace를 만들어요.
func NewWorkspace(opts WorkspaceOptions) (*workspace.Workspace, string, error) {
	root := opts.Root
	if root == "" {
		root = "."
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, "", err
	}
	ws, err := workspace.New(absRoot)
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
	toolSet := ktools.StandardToolSet(ktools.SurfaceOptions{Workspace: ws, NoWeb: opts.NoWeb, WebMaxBytes: opts.WebMaxBytes})
	return agent.New(agent.Config{
		Provider:      provider,
		Model:         opts.Model,
		Instructions:  opts.Instructions,
		ContextBlocks: append([]string{}, opts.ContextBlocks...),
		BaseRequest:   opts.BaseRequest,
		ToolSet:       toolSet,
		MaxIterations: opts.MaxIterations,
		Transcript:    opts.Transcript,
		Guardrails:    opts.Guardrails,
		Observer:      opts.Observer,
	})
}

// NewRuntime은 CLI와 gateway가 공유하는 session runtime 기본값을 적용해요.
func NewRuntime(store session.Store, ag *agent.Agent, opts RuntimeOptions) *kruntime.Runtime {
	if opts.AgentName == "" {
		opts.AgentName = "kkode-agent"
	}
	if opts.Mode == "" {
		opts.Mode = session.AgentModeBuild
	}
	if opts.MaxHistoryTurns <= 0 {
		opts.MaxHistoryTurns = 8
	}
	if !opts.DisableTodoHelpers {
		opts.EnableTodos = true
	}
	if !opts.DisableCompaction && !opts.Compaction.Enabled {
		opts.Compaction = DefaultCompactionPolicy()
	}
	return &kruntime.Runtime{
		Store:           store,
		Agent:           ag,
		ProjectRoot:     opts.ProjectRoot,
		ProviderName:    opts.ProviderName,
		Model:           opts.Model,
		AgentName:       opts.AgentName,
		Mode:            opts.Mode,
		MaxHistoryTurns: opts.MaxHistoryTurns,
		Compaction:      opts.Compaction,
		EnableTodos:     opts.EnableTodos,
	}
}

func DefaultCompactionPolicy() session.CompactionPolicy {
	return session.CompactionPolicy{
		Enabled:             true,
		TriggerTokenRatio:   0.85,
		PreserveFirstNTurns: 1,
		PreserveLastNTurns:  4,
	}
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
