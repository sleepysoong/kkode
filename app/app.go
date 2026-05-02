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
	kruntime "github.com/sleepysoong/kkode/runtime"
	"github.com/sleepysoong/kkode/session"
	ktools "github.com/sleepysoong/kkode/tools"
	"github.com/sleepysoong/kkode/transcript"
	"github.com/sleepysoong/kkode/workspace"
)

// ProviderHandle은 provider와 종료 함수를 함께 들고 다니는 실행 핸들이에요.
type ProviderHandle struct {
	Provider llm.Provider
	Close    func() error
}

// ProviderFactory는 provider별 생성 방식을 registry entry 안에 묶는 함수예요.
type ProviderFactory func(root string, opts ProviderOptions) (ProviderHandle, error)

// ProviderSpec은 provider 이름, alias, 기본 모델, 인증 힌트를 한 곳에서 관리해요.
type ProviderSpec struct {
	Name         string
	Aliases      []string
	DefaultModel string
	Models       []string
	AuthEnv      []string
	Local        bool
	Capabilities map[string]any
}

type providerRegistryEntry struct {
	Spec    ProviderSpec
	Factory ProviderFactory
}

// WorkspaceOptions는 workspace 경계를 정하는 옵션이에요.
type WorkspaceOptions struct {
	Root string
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
}

// BuildProvider는 환경변수 기반 provider 생성을 한 곳에서 처리해요.
func BuildProvider(name, root string) (ProviderHandle, error) {
	return BuildProviderWithOptions(name, root, ProviderOptions{})
}

// BuildProviderWithOptions는 gateway resource manifest를 provider별 설정으로 반영해요.
func BuildProviderWithOptions(name, root string, opts ProviderOptions) (ProviderHandle, error) {
	entry, ok := resolveProviderEntry(name)
	if !ok || entry.Factory == nil {
		return ProviderHandle{}, fmt.Errorf("unknown provider: %s", name)
	}
	return entry.Factory(root, opts)
}

// DefaultModel은 provider별 기본 모델을 정해요.
func DefaultModel(provider string) string {
	if spec, ok := ResolveProviderSpec(provider); ok {
		return spec.DefaultModel
	}
	return "gpt-5-mini"
}

var providerRegistry = []providerRegistryEntry{
	{
		Spec: ProviderSpec{Name: "openai", Aliases: []string{"openai-compatible"}, DefaultModel: "gpt-5-mini", Models: []string{"gpt-5-mini"}, AuthEnv: []string{"OPENAI_API_KEY"}, Capabilities: map[string]any{"tools": true, "custom_tools": true, "reasoning": true, "reasoning_summaries": true, "structured_output": true, "streaming": true, "tool_choice": true, "parallel_tool_calls": true}},
		Factory: func(root string, opts ProviderOptions) (ProviderHandle, error) {
			return ProviderHandle{Provider: openai.New(openai.Config{BaseURL: os.Getenv("OPENAI_BASE_URL"), APIKey: os.Getenv("OPENAI_API_KEY")})}, nil
		},
	},
	{
		Spec: ProviderSpec{Name: "omniroute", DefaultModel: "gpt-5-mini", Models: []string{"gpt-5-mini", "auto"}, AuthEnv: []string{"OMNIROUTE_API_KEY", "OPENAI_API_KEY"}, Capabilities: map[string]any{"tools": true, "reasoning": true, "streaming": true, "mcp": true, "a2a": true, "routing": true}},
		Factory: func(root string, opts ProviderOptions) (ProviderHandle, error) {
			return ProviderHandle{Provider: omniroute.New(omniroute.Config{BaseURL: os.Getenv("OMNIROUTE_BASE_URL"), APIKey: EnvDefault("OMNIROUTE_API_KEY", os.Getenv("OPENAI_API_KEY")), SessionID: os.Getenv("OMNIROUTE_SESSION_ID"), Progress: EnvBool("OMNIROUTE_PROGRESS")})}, nil
		},
	},
	{
		Spec: ProviderSpec{Name: "copilot", Aliases: []string{"github-copilot"}, DefaultModel: "gpt-5-mini", Models: []string{"gpt-5-mini"}, AuthEnv: []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"}, Capabilities: map[string]any{"tools": true, "custom_tools": true, "reasoning": true, "streaming": true, "parallel_tool_calls": true, "mcp": true, "skills": true, "custom_agents": true}},
		Factory: func(root string, opts ProviderOptions) (ProviderHandle, error) {
			client := copilot.New(copilot.Config{WorkingDirectory: root, GitHubToken: EnvDefault("COPILOT_GITHUB_TOKEN", EnvDefault("GH_TOKEN", os.Getenv("GITHUB_TOKEN"))), MCPServers: copilot.MCPServerConfigs(opts.MCPServers), SkillDirectories: opts.SkillDirectories, CustomAgents: copilot.AgentConfigs(opts.CustomAgents)})
			return ProviderHandle{Provider: client, Close: client.Close}, nil
		},
	},
	{
		Spec: ProviderSpec{Name: "codex", Aliases: []string{"codexcli", "codex-cli"}, DefaultModel: "gpt-5.3-codex", Models: []string{"gpt-5.3-codex"}, Local: true, Capabilities: map[string]any{"tools": true, "reasoning": true, "streaming": true, "mcp": true, "skills": true}},
		Factory: func(root string, opts ProviderOptions) (ProviderHandle, error) {
			return ProviderHandle{Provider: codexcli.New(codexcli.Config{WorkingDirectory: root, Sandbox: os.Getenv("CODEX_SANDBOX"), Ephemeral: EnvBool("CODEX_EPHEMERAL")})}, nil
		},
	},
}

func ProviderSpecs() []ProviderSpec {
	specs := make([]ProviderSpec, 0, len(providerRegistry))
	for _, entry := range providerRegistry {
		specs = append(specs, cloneProviderSpec(entry.Spec))
	}
	return specs
}

func ResolveProviderSpec(name string) (ProviderSpec, bool) {
	entry, ok := resolveProviderEntry(name)
	if !ok {
		return ProviderSpec{}, false
	}
	return cloneProviderSpec(entry.Spec), true
}

func resolveProviderEntry(name string) (providerRegistryEntry, bool) {
	needle := strings.ToLower(strings.TrimSpace(name))
	for _, entry := range providerRegistry {
		if needle == entry.Spec.Name {
			return entry, true
		}
		for _, alias := range entry.Spec.Aliases {
			if needle == alias {
				return entry, true
			}
		}
	}
	return providerRegistryEntry{}, false
}

func cloneProviderSpec(spec ProviderSpec) ProviderSpec {
	spec.Aliases = append([]string(nil), spec.Aliases...)
	spec.Models = append([]string(nil), spec.Models...)
	spec.AuthEnv = append([]string(nil), spec.AuthEnv...)
	if spec.Capabilities != nil {
		capabilities := make(map[string]any, len(spec.Capabilities))
		for key, value := range spec.Capabilities {
			capabilities[key] = value
		}
		spec.Capabilities = capabilities
	}
	return spec
}

func ProviderAuthStatus(spec ProviderSpec) string {
	if spec.Local {
		return "local"
	}
	for _, key := range spec.AuthEnv {
		if os.Getenv(key) != "" {
			return "configured"
		}
	}
	return "missing"
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
	toolDefs, toolHandlers := ktools.StandardTools(ktools.SurfaceOptions{Workspace: ws, NoWeb: opts.NoWeb, WebMaxBytes: opts.WebMaxBytes})
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
