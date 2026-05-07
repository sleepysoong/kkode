package app

import (
	"fmt"
	"os"
	"strings"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/providers/codexcli"
	"github.com/sleepysoong/kkode/providers/copilot"
	"github.com/sleepysoong/kkode/providers/omniroute"
	"github.com/sleepysoong/kkode/providers/openai"
)

// ProviderHandle은 provider와 종료 함수를 함께 들고 다니는 실행 핸들이에요.
type ProviderHandle struct {
	Provider    llm.Provider
	BaseRequest llm.Request
	Close       func() error
}

// ProviderFactory는 provider별 source/client 생성 방식을 registry entry 안에 묶는 함수예요.
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
	Conversion   ProviderConversionSpec
}

// ProviderConversionSpec은 provider가 표준 요청을 어떤 source 호출로 바꾸는지 설명해요.
// gateway discovery와 문서가 같은 정보를 쓰도록 registry에 함께 보관해요.
type ProviderConversionSpec struct {
	RequestConverter  string
	ResponseConverter string
	Call              string
	Stream            string
	Source            string
	Operations        []string
}

// ProviderConversionSet은 실제 변환기와 operation 기본값을 묶은 실행형 변환 profile이에요.
// 새 provider는 이 값을 registry에 한 번만 등록하면 preview, adapter, gateway discovery가 같은 계약을 써요.
type ProviderConversionSet struct {
	RequestConverter  llm.RequestConverter
	ResponseConverter llm.ResponseConverter
	Options           llm.ConvertOptions
	StreamOptions     llm.ConvertOptions
}

// Pipeline은 변환 profile에 API/SDK/CLI source caller를 꽂아서 표준 파이프라인을 만들어요.
func (c ProviderConversionSet) Pipeline(providerName string, caller llm.ProviderCaller, streamer llm.ProviderStreamCaller) llm.ProviderPipeline {
	streamOpts := c.StreamOptions
	if streamOpts.Operation == "" {
		streamOpts.Operation = c.Options.Operation
	}
	return llm.ProviderPipeline{
		ProviderName:      providerName,
		RequestConverter:  c.RequestConverter,
		ResponseConverter: c.ResponseConverter,
		Caller:            caller,
		Streamer:          streamer,
		Options:           c.Options,
		StreamOptions:     streamOpts,
	}
}

// ProviderAdapterOptions는 기존 source caller를 llm.Provider로 감쌀 때 쓰는 옵션이에요.
type ProviderAdapterOptions struct {
	Caller       llm.ProviderCaller
	Streamer     llm.ProviderStreamCaller
	Capabilities llm.Capabilities
}

type providerConversionFactory func(spec ProviderSpec) ProviderConversionSet

type providerRegistryEntry struct {
	Spec       ProviderSpec
	Conversion providerConversionFactory
	Factory    ProviderFactory
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
	opts = MergeProviderOptions(DefaultProviderOptions(root), opts)
	return entry.Factory(root, opts)
}

// BuildProviderPipeline은 registry 변환 profile과 외부 source caller를 조합해요.
// OpenAI 호환 gateway, Copilot SDK, Codex CLI 같은 source는 caller만 구현하면 같은 변환 레이어를 재사용해요.
func BuildProviderPipeline(provider string, caller llm.ProviderCaller, streamer llm.ProviderStreamCaller) (llm.ProviderPipeline, error) {
	spec, conversion, err := resolveProviderConversion(provider)
	if err != nil {
		return llm.ProviderPipeline{}, err
	}
	return conversion.Pipeline(spec.Name, caller, streamer), nil
}

// BuildProviderAdapter는 변환 profile과 source caller를 llm.Provider 구현체로 감싸요.
func BuildProviderAdapter(provider string, opts ProviderAdapterOptions) (*llm.AdaptedProvider, error) {
	spec, conversion, err := resolveProviderConversion(provider)
	if err != nil {
		return nil, err
	}
	caps := opts.Capabilities
	if caps == (llm.Capabilities{}) {
		caps = capabilitiesFromMap(spec.Capabilities)
	}
	return &llm.AdaptedProvider{
		ProviderName:         spec.Name,
		ProviderCapabilities: caps,
		RequestConverter:     conversion.RequestConverter,
		ResponseConverter:    conversion.ResponseConverter,
		Caller:               opts.Caller,
		Streamer:             opts.Streamer,
		Options:              conversion.Options,
		StreamOptions:        conversion.StreamOptions,
	}, nil
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
		Spec: ProviderSpec{
			Name:         "openai",
			Aliases:      []string{"openai-compatible"},
			DefaultModel: "gpt-5-mini",
			Models:       []string{"gpt-5-mini"},
			AuthEnv:      []string{"OPENAI_API_KEY"},
			Capabilities: openai.DefaultCapabilities().ToMap(),
			Conversion: ProviderConversionSpec{
				RequestConverter:  "openai.ResponsesConverter",
				ResponseConverter: "openai.ResponsesConverter",
				Call:              "openai.Client.CallProvider",
				Stream:            "openai.Client.StreamProvider",
				Source:            "http-json+sse",
				Operations:        []string{"responses.create"},
			},
		},
		Conversion: openAICompatibleConversion("openai"),
		Factory: func(root string, opts ProviderOptions) (ProviderHandle, error) {
			return ProviderHandle{Provider: openai.New(openai.Config{BaseURL: os.Getenv("OPENAI_BASE_URL"), APIKey: os.Getenv("OPENAI_API_KEY")}), BaseRequest: llm.Request{Tools: openAICompatibleMCPTools(opts)}}, nil
		},
	},
	{
		Spec: ProviderSpec{
			Name:         "omniroute",
			DefaultModel: "gpt-5-mini",
			Models:       []string{"gpt-5-mini", "auto"},
			AuthEnv:      []string{"OMNIROUTE_API_KEY", "OPENAI_API_KEY"},
			Capabilities: omniroute.DefaultCapabilities().ToMap(),
			Conversion: ProviderConversionSpec{
				RequestConverter:  "openai.ResponsesConverter",
				ResponseConverter: "openai.ResponsesConverter",
				Call:              "openai.Client.CallProvider via omniroute headers",
				Stream:            "openai.Client.StreamProvider via omniroute headers",
				Source:            "http-gateway",
				Operations:        []string{"responses.create", "omniroute.management", "omniroute.a2a"},
			},
		},
		Conversion: openAICompatibleConversion("omniroute"),
		Factory: func(root string, opts ProviderOptions) (ProviderHandle, error) {
			return ProviderHandle{Provider: omniroute.New(omniroute.Config{BaseURL: os.Getenv("OMNIROUTE_BASE_URL"), APIKey: EnvDefault("OMNIROUTE_API_KEY", os.Getenv("OPENAI_API_KEY")), SessionID: os.Getenv("OMNIROUTE_SESSION_ID"), Progress: EnvBool("OMNIROUTE_PROGRESS")}), BaseRequest: llm.Request{Tools: openAICompatibleMCPTools(opts)}}, nil
		},
	},
	{
		Spec: ProviderSpec{
			Name:         "copilot",
			Aliases:      []string{"github-copilot"},
			DefaultModel: "gpt-5-mini",
			Models:       []string{"gpt-5-mini"},
			AuthEnv:      []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"},
			Capabilities: copilot.DefaultCapabilities().ToMap(),
			Conversion: ProviderConversionSpec{
				RequestConverter:  "copilot.SessionConverter",
				ResponseConverter: "copilot.SessionConverter",
				Call:              "copilot.Client.CallProvider",
				Stream:            "copilot.Session.Stream",
				Source:            "github-copilot-sdk",
				Operations:        []string{"copilot.session.send"},
			},
		},
		Conversion: copilotConversion,
		Factory: func(root string, opts ProviderOptions) (ProviderHandle, error) {
			client := copilot.New(copilot.Config{WorkingDirectory: root, GitHubToken: EnvDefault("COPILOT_GITHUB_TOKEN", EnvDefault("GH_TOKEN", os.Getenv("GITHUB_TOKEN"))), MCPServers: copilot.MCPServerConfigs(opts.MCPServers), SkillDirectories: opts.SkillDirectories, CustomAgents: copilot.AgentConfigs(opts.CustomAgents)})
			return ProviderHandle{Provider: client, Close: client.Close}, nil
		},
	},
	{
		Spec: ProviderSpec{
			Name:         "codex",
			Aliases:      []string{"codexcli", "codex-cli"},
			DefaultModel: "gpt-5.3-codex",
			Models:       []string{"gpt-5.3-codex"},
			Local:        true,
			Capabilities: codexcli.DefaultCapabilities().ToMap(),
			Conversion: ProviderConversionSpec{
				RequestConverter:  "codexcli.ExecConverter",
				ResponseConverter: "codexcli.ExecConverter",
				Call:              "codexcli.Client.CallProvider",
				Stream:            "codexcli.Client.Stream",
				Source:            "codex-cli-jsonl",
				Operations:        []string{"codex.exec"},
			},
		},
		Conversion: codexCLIConversion,
		Factory: func(root string, opts ProviderOptions) (ProviderHandle, error) {
			return ProviderHandle{Provider: codexcli.New(codexcli.Config{WorkingDirectory: root, Sandbox: os.Getenv("CODEX_SANDBOX"), Ephemeral: EnvBool("CODEX_EPHEMERAL")})}, nil
		},
	},
}

func openAICompatibleConversion(providerName string) providerConversionFactory {
	return func(spec ProviderSpec) ProviderConversionSet {
		operation := firstOperation(spec)
		return ProviderConversionSet{
			RequestConverter:  openai.ResponsesConverter{ProviderName: providerName},
			ResponseConverter: openai.ResponsesConverter{ProviderName: providerName},
			Options:           llm.ConvertOptions{Operation: operation},
			StreamOptions:     llm.ConvertOptions{Operation: operation, Stream: true},
		}
	}
}

func copilotConversion(spec ProviderSpec) ProviderConversionSet {
	operation := firstOperation(spec)
	return ProviderConversionSet{
		RequestConverter:  copilot.SessionConverter{},
		ResponseConverter: copilot.SessionConverter{},
		Options:           llm.ConvertOptions{Operation: operation},
		StreamOptions:     llm.ConvertOptions{Operation: operation, Stream: true},
	}
}

func codexCLIConversion(spec ProviderSpec) ProviderConversionSet {
	operation := firstOperation(spec)
	return ProviderConversionSet{
		RequestConverter:  codexcli.ExecConverter{},
		ResponseConverter: codexcli.ExecConverter{},
		Options:           llm.ConvertOptions{Operation: operation},
		StreamOptions:     llm.ConvertOptions{Operation: operation, Stream: true},
	}
}

func capabilitiesFromMap(values map[string]any) llm.Capabilities {
	truthy := func(name string) bool {
		v, ok := values[name]
		if !ok {
			return false
		}
		b, _ := v.(bool)
		return b
	}
	return llm.Capabilities{
		Tools:              truthy("tools"),
		CustomTools:        truthy("custom_tools"),
		Reasoning:          truthy("reasoning"),
		ReasoningSummaries: truthy("reasoning_summaries"),
		StructuredOutput:   truthy("structured_output"),
		Streaming:          truthy("streaming"),
		ToolChoice:         truthy("tool_choice"),
		ParallelToolCalls:  truthy("parallel_tool_calls"),
		PromptRefs:         truthy("prompt_refs"),
		PreviousResponseID: truthy("previous_response_id"),
		MCP:                truthy("mcp"),
		Skills:             truthy("skills"),
		CustomAgents:       truthy("custom_agents"),
		A2A:                truthy("a2a"),
		Routing:            truthy("routing"),
	}
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

func resolveProviderConversion(name string) (ProviderSpec, ProviderConversionSet, error) {
	entry, ok := resolveProviderEntry(name)
	if !ok {
		return ProviderSpec{}, ProviderConversionSet{}, fmt.Errorf("unknown provider: %s", name)
	}
	if entry.Conversion == nil {
		return ProviderSpec{}, ProviderConversionSet{}, fmt.Errorf("provider conversion이 등록되지 않았어요: %s", entry.Spec.Name)
	}
	spec := cloneProviderSpec(entry.Spec)
	conversion := entry.Conversion(spec)
	if conversion.RequestConverter == nil {
		return ProviderSpec{}, ProviderConversionSet{}, fmt.Errorf("provider request converter가 등록되지 않았어요: %s", spec.Name)
	}
	return spec, conversion, nil
}

func cloneProviderSpec(spec ProviderSpec) ProviderSpec {
	spec.Aliases = append([]string(nil), spec.Aliases...)
	spec.Models = append([]string(nil), spec.Models...)
	spec.AuthEnv = append([]string(nil), spec.AuthEnv...)
	spec.Conversion.Operations = append([]string(nil), spec.Conversion.Operations...)
	if spec.Capabilities != nil {
		capabilities := make(map[string]any, len(spec.Capabilities))
		for key, value := range spec.Capabilities {
			capabilities[key] = value
		}
		spec.Capabilities = capabilities
	}
	return spec
}

func firstOperation(spec ProviderSpec) string {
	if len(spec.Conversion.Operations) == 0 {
		return ""
	}
	return spec.Conversion.Operations[0]
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
