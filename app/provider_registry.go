package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/providers/codexcli"
	"github.com/sleepysoong/kkode/providers/copilot"
	"github.com/sleepysoong/kkode/providers/httpjson"
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
	Routes            []ProviderRouteSpec
}

// ProviderRouteSpec은 변환 operation이 HTTP source에서 어떤 route로 나가는지 설명해요.
// secret header 값은 담지 않고, 외부 패널과 테스트가 endpoint 계약만 발견하게 해요.
type ProviderRouteSpec struct {
	Operation string            `json:"operation"`
	Method    string            `json:"method,omitempty"`
	Path      string            `json:"path"`
	Accept    string            `json:"accept,omitempty"`
	Query     map[string]string `json:"query,omitempty"`
}

// ProviderConversionSet은 실제 변환기와 operation 기본값을 묶은 실행형 변환 profile이에요.
// 새 provider는 이 값을 registry에 한 번만 등록하면 preview, adapter, gateway discovery가 같은 계약을 써요.
type ProviderConversionSet struct {
	RequestConverter  llm.RequestConverter
	ResponseConverter llm.ResponseConverter
	Options           llm.ConvertOptions
	StreamOptions     llm.ConvertOptions
}

// ProviderConversionFactory는 provider spec을 실행형 변환 profile로 바꿔요.
// 외부 패키지는 RegisterProvider로 이 factory를 등록해서 core 코드를 수정하지 않고 새 API source를 붙일 수 있어요.
type ProviderConversionFactory func(spec ProviderSpec) ProviderConversionSet

// ProviderRegistration은 provider registry에 넣을 단위예요.
// Spec은 discovery와 기본값을 설명하고, Conversion은 변환 레이어를 만들며, Factory는 환경 기반 실제 provider를 만들어요.
type ProviderRegistration struct {
	Spec       ProviderSpec
	Conversion ProviderConversionFactory
	Factory    ProviderFactory
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
	ProviderName string
	Caller       llm.ProviderCaller
	Streamer     llm.ProviderStreamCaller
	Capabilities llm.Capabilities
}

// HTTPJSONProviderOptions는 OpenAI-compatible 파생 HTTP API처럼 route만 다른 source를 붙일 때 쓰는 옵션이에요.
// Routes를 비워두면 registry conversion profile의 route metadata를 기본값으로 써요.
type HTTPJSONProviderOptions struct {
	ProviderName      string
	BaseURL           string
	APIKey            string
	Headers           map[string]string
	HTTPClient        *http.Client
	Retry             httpjson.RetryConfig
	MaxResponseBytes  int64
	DefaultOperation  string
	Routes            map[string]httpjson.Route
	Capabilities      llm.Capabilities
	DisableStreaming  bool
	AdditionalHeaders map[string]string
}

// HTTPJSONProviderRegistration은 OpenAI-compatible 같은 기존 변환 profile에 HTTP JSON source만 새로 붙이는 등록 단위예요.
// converter를 새로 만들 필요가 없는 proxy, gateway, 사내 API는 이 구조체만 채우면 discovery와 실행 registry에 함께 등록돼요.
type HTTPJSONProviderRegistration struct {
	Name              string               `json:"name"`
	Aliases           []string             `json:"aliases,omitempty"`
	Profile           string               `json:"profile,omitempty"`
	DefaultModel      string               `json:"default_model,omitempty"`
	Models            []string             `json:"models,omitempty"`
	AuthEnv           []string             `json:"auth_env,omitempty"`
	BaseURL           string               `json:"base_url,omitempty"`
	BaseURLEnv        []string             `json:"base_url_env,omitempty"`
	APIKey            string               `json:"api_key,omitempty"`
	APIKeyEnv         []string             `json:"api_key_env,omitempty"`
	Headers           map[string]string    `json:"headers,omitempty"`
	AdditionalHeaders map[string]string    `json:"additional_headers,omitempty"`
	Routes            []ProviderRouteSpec  `json:"routes,omitempty"`
	DefaultOperation  string               `json:"default_operation,omitempty"`
	Capabilities      map[string]any       `json:"capabilities,omitempty"`
	Local             bool                 `json:"local,omitempty"`
	DisableStreaming  bool                 `json:"disable_streaming,omitempty"`
	HTTPClient        *http.Client         `json:"-"`
	Retry             httpjson.RetryConfig `json:"retry,omitempty"`
	MaxResponseBytes  int64                `json:"max_response_bytes,omitempty"`
	Source            string               `json:"source,omitempty"`
}

// BuildProvider는 환경변수 기반 provider 생성을 한 곳에서 처리해요.
func BuildProvider(name, root string) (ProviderHandle, error) {
	return BuildProviderWithOptions(name, root, ProviderOptions{})
}

// BuildProviderWithOptions는 gateway resource manifest를 provider별 설정으로 반영해요.
func BuildProviderWithOptions(name, root string, opts ProviderOptions) (ProviderHandle, error) {
	entry, ok := resolveProviderEntry(name)
	if !ok {
		return ProviderHandle{}, fmt.Errorf("unknown provider: %s", name)
	}
	if entry.Factory == nil {
		return ProviderHandle{}, fmt.Errorf("provider factory가 등록되지 않았어요: %s", entry.Spec.Name)
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
	providerName := strings.TrimSpace(opts.ProviderName)
	if providerName == "" {
		providerName = spec.Name
	}
	caps := opts.Capabilities
	if caps == (llm.Capabilities{}) {
		caps = capabilitiesFromMap(spec.Capabilities)
	}
	return &llm.AdaptedProvider{
		ProviderName:         providerName,
		ProviderCapabilities: caps,
		RequestConverter:     conversion.RequestConverter,
		ResponseConverter:    conversion.ResponseConverter,
		Caller:               opts.Caller,
		Streamer:             opts.Streamer,
		Options:              conversion.Options,
		StreamOptions:        conversion.StreamOptions,
	}, nil
}

// BuildHTTPJSONProviderAdapter는 registry 변환 profile에 범용 HTTP JSON source를 꽂아 llm.Provider를 만들어요.
// 새 OpenAI-compatible gateway나 사내 proxy는 converter를 새로 만들지 않고 base URL/API key/route만 넘기면 돼요.
func BuildHTTPJSONProviderAdapter(profile string, opts HTTPJSONProviderOptions) (*llm.AdaptedProvider, error) {
	spec, _, err := resolveProviderConversion(profile)
	if err != nil {
		return nil, err
	}
	if opts.MaxResponseBytes < 0 {
		return nil, fmt.Errorf("max_response_bytes는 0 이상이어야 해요")
	}
	providerName := strings.TrimSpace(opts.ProviderName)
	if providerName == "" {
		providerName = spec.Name
	}
	routes := cloneHTTPJSONRoutes(opts.Routes)
	if len(routes) == 0 {
		routes = httpJSONRoutesFromSpec(spec.Conversion.Routes)
	}
	if len(routes) == 0 {
		return nil, fmt.Errorf("http json route가 필요해요: %s", spec.Name)
	}
	headers := cloneStringMap(opts.Headers)
	if len(opts.AdditionalHeaders) > 0 && headers == nil {
		headers = map[string]string{}
	}
	for key, value := range opts.AdditionalHeaders {
		headers[key] = value
	}
	defaultOperation := strings.TrimSpace(opts.DefaultOperation)
	if defaultOperation == "" {
		defaultOperation = firstOperation(spec)
	}
	caller := httpjson.New(httpjson.Config{
		ProviderName:     providerName,
		BaseURL:          opts.BaseURL,
		APIKey:           opts.APIKey,
		Headers:          headers,
		HTTPClient:       opts.HTTPClient,
		Retry:            opts.Retry,
		MaxResponseBytes: opts.MaxResponseBytes,
		DefaultOperation: defaultOperation,
		Routes:           routes,
	})
	var streamer llm.ProviderStreamCaller
	if !opts.DisableStreaming {
		streamer = caller
	}
	caps := opts.Capabilities
	if caps == (llm.Capabilities{}) && opts.DisableStreaming {
		caps = capabilitiesFromMap(spec.Capabilities)
		caps.Streaming = false
	}
	return BuildProviderAdapter(profile, ProviderAdapterOptions{
		ProviderName: providerName,
		Caller:       caller,
		Streamer:     streamer,
		Capabilities: caps,
	})
}

// RegisterHTTPJSONProvider는 기존 변환 profile에 새 HTTP JSON source만 연결해요.
// 새 OpenAI-compatible gateway는 profile/base URL/API key/env/route만 넘기면 `요청 -> 변환 -> API 호출 -> 응답 변환` 흐름을 재사용해요.
func RegisterHTTPJSONProvider(reg HTTPJSONProviderRegistration) (func(), error) {
	reg = cloneHTTPJSONProviderRegistration(reg)
	if reg.MaxResponseBytes < 0 {
		return nil, fmt.Errorf("max_response_bytes는 0 이상이어야 해요")
	}
	profile := strings.TrimSpace(reg.Profile)
	if profile == "" {
		profile = "openai-compatible"
	}
	profileSpec, ok := ResolveProviderSpec(profile)
	if !ok {
		return nil, fmt.Errorf("provider profile을 찾을 수 없어요: %s", profile)
	}
	spec := httpJSONProviderSpecFromRegistration(reg, profileSpec)
	routes := spec.Conversion.Routes
	if len(routes) == 0 {
		routes = cloneProviderRoutes(profileSpec.Conversion.Routes)
	}
	factory := func(root string, opts ProviderOptions) (ProviderHandle, error) {
		provider, err := BuildHTTPJSONProviderAdapter(profile, HTTPJSONProviderOptions{
			ProviderName:      spec.Name,
			BaseURL:           firstConfiguredValue(reg.BaseURL, reg.BaseURLEnv),
			APIKey:            firstConfiguredValue(reg.APIKey, append(cloneStringSlice(reg.APIKeyEnv), reg.AuthEnv...)),
			Headers:           reg.Headers,
			AdditionalHeaders: reg.AdditionalHeaders,
			HTTPClient:        reg.HTTPClient,
			Retry:             reg.Retry,
			MaxResponseBytes:  reg.MaxResponseBytes,
			DefaultOperation:  firstNonEmpty(reg.DefaultOperation, firstOperation(spec)),
			Routes:            httpJSONRoutesFromSpec(routes),
			Capabilities:      capabilitiesFromMap(spec.Capabilities),
			DisableStreaming:  reg.DisableStreaming,
		})
		if err != nil {
			return ProviderHandle{}, err
		}
		return ProviderHandle{Provider: provider}, nil
	}
	return RegisterProvider(ProviderRegistration{
		Spec:       spec,
		Conversion: providerConversionFromProfile(profile),
		Factory:    factory,
	})
}

// RegisterHTTPJSONProvidersFromEnv는 JSON 환경변수에서 HTTP JSON provider 등록 목록을 읽어요.
// 배포 환경에서는 KKODE_HTTPJSON_PROVIDERS에 배열이나 단일 객체를 넣어 재컴파일 없이 compatible source를 추가할 수 있어요.
func RegisterHTTPJSONProvidersFromEnv(key string) (func(), error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return func() {}, nil
	}
	unregister, err := RegisterHTTPJSONProvidersFromJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("%s provider 설정이 올바르지 않아요: %w", key, err)
	}
	return unregister, nil
}

// RegisterHTTPJSONProvidersFromJSON은 HTTP JSON provider 등록 JSON을 registry에 반영해요.
// 입력은 단일 객체나 배열을 모두 허용하고, 중간 실패 시 이미 등록한 provider를 되돌려요.
func RegisterHTTPJSONProvidersFromJSON(raw string) (func(), error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return func() {}, nil
	}
	regs, err := decodeHTTPJSONProviderRegistrations(raw)
	if err != nil {
		return nil, err
	}
	unregisters := make([]func(), 0, len(regs))
	for i, reg := range regs {
		unregister, err := RegisterHTTPJSONProvider(reg)
		if err != nil {
			for j := len(unregisters) - 1; j >= 0; j-- {
				unregisters[j]()
			}
			return nil, fmt.Errorf("HTTP JSON provider 등록 %d(%s) 실패예요: %w", i, strings.TrimSpace(reg.Name), err)
		}
		unregisters = append(unregisters, unregister)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for i := len(unregisters) - 1; i >= 0; i-- {
				unregisters[i]()
			}
		})
	}, nil
}

// DefaultModel은 provider별 기본 모델을 정해요.
func DefaultModel(provider string) string {
	if spec, ok := ResolveProviderSpec(provider); ok {
		return spec.DefaultModel
	}
	return "gpt-5-mini"
}

var (
	providerRegistryMu sync.RWMutex
	providerRegistry   = []ProviderRegistration{
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
					Routes:            []ProviderRouteSpec{{Operation: "responses.create", Method: http.MethodPost, Path: "/responses", Accept: "application/json"}},
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
					Routes:            []ProviderRouteSpec{{Operation: "responses.create", Method: http.MethodPost, Path: "/responses", Accept: "application/json"}},
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
)

// RegisterProvider는 런타임에 provider profile을 추가해요.
// 반환된 unregister 함수를 호출하면 테스트나 플러그인 종료 시 등록을 되돌릴 수 있어요.
func RegisterProvider(reg ProviderRegistration) (func(), error) {
	reg = cloneProviderRegistration(reg)
	if err := validateProviderRegistration(reg); err != nil {
		return nil, err
	}
	providerRegistryMu.Lock()
	defer providerRegistryMu.Unlock()
	if existing, ok := findProviderRegistrationLocked(reg.Spec.Name); ok {
		return nil, fmt.Errorf("provider가 이미 등록되어 있어요: %s", existing.Spec.Name)
	}
	for _, alias := range reg.Spec.Aliases {
		if existing, ok := findProviderRegistrationLocked(alias); ok {
			return nil, fmt.Errorf("provider alias가 이미 등록되어 있어요: %s -> %s", alias, existing.Spec.Name)
		}
	}
	providerRegistry = append(providerRegistry, reg)
	var once sync.Once
	return func() {
		once.Do(func() {
			providerRegistryMu.Lock()
			defer providerRegistryMu.Unlock()
			name := strings.ToLower(strings.TrimSpace(reg.Spec.Name))
			for i, entry := range providerRegistry {
				if strings.ToLower(strings.TrimSpace(entry.Spec.Name)) == name {
					providerRegistry = append(providerRegistry[:i], providerRegistry[i+1:]...)
					return
				}
			}
		})
	}, nil
}

func openAICompatibleConversion(providerName string) ProviderConversionFactory {
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

func providerConversionFromProfile(profile string) ProviderConversionFactory {
	return func(spec ProviderSpec) ProviderConversionSet {
		_, conversion, err := resolveProviderConversion(profile)
		if err != nil {
			return ProviderConversionSet{}
		}
		if spec.Conversion.Operations != nil {
			operation := firstOperation(spec)
			conversion.Options.Operation = operation
			conversion.StreamOptions.Operation = operation
		}
		if spec.Conversion.Routes != nil && conversion.StreamOptions.Operation == "" {
			conversion.StreamOptions.Operation = conversion.Options.Operation
		}
		return conversion
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
	providerRegistryMu.RLock()
	defer providerRegistryMu.RUnlock()
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

func resolveProviderEntry(name string) (ProviderRegistration, bool) {
	providerRegistryMu.RLock()
	defer providerRegistryMu.RUnlock()
	return findProviderRegistrationLocked(name)
}

func findProviderRegistrationLocked(name string) (ProviderRegistration, bool) {
	needle := strings.ToLower(strings.TrimSpace(name))
	for _, entry := range providerRegistry {
		if needle == strings.ToLower(strings.TrimSpace(entry.Spec.Name)) {
			return cloneProviderRegistration(entry), true
		}
		for _, alias := range entry.Spec.Aliases {
			if needle == strings.ToLower(strings.TrimSpace(alias)) {
				return cloneProviderRegistration(entry), true
			}
		}
	}
	return ProviderRegistration{}, false
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
	spec.Conversion.Routes = cloneProviderRoutes(spec.Conversion.Routes)
	if spec.Capabilities != nil {
		capabilities := make(map[string]any, len(spec.Capabilities))
		for key, value := range spec.Capabilities {
			capabilities[key] = value
		}
		spec.Capabilities = capabilities
	}
	return spec
}

func cloneProviderRegistration(reg ProviderRegistration) ProviderRegistration {
	reg.Spec = cloneProviderSpec(reg.Spec)
	return reg
}

func cloneHTTPJSONProviderRegistration(reg HTTPJSONProviderRegistration) HTTPJSONProviderRegistration {
	reg.Aliases = append([]string(nil), reg.Aliases...)
	reg.Models = append([]string(nil), reg.Models...)
	reg.AuthEnv = append([]string(nil), reg.AuthEnv...)
	reg.BaseURLEnv = append([]string(nil), reg.BaseURLEnv...)
	reg.APIKeyEnv = append([]string(nil), reg.APIKeyEnv...)
	reg.Headers = cloneStringMap(reg.Headers)
	reg.AdditionalHeaders = cloneStringMap(reg.AdditionalHeaders)
	reg.Routes = cloneProviderRoutes(reg.Routes)
	if reg.Capabilities != nil {
		capabilities := make(map[string]any, len(reg.Capabilities))
		for key, value := range reg.Capabilities {
			capabilities[key] = value
		}
		reg.Capabilities = capabilities
	}
	return reg
}

func httpJSONProviderSpecFromRegistration(reg HTTPJSONProviderRegistration, profile ProviderSpec) ProviderSpec {
	defaultModel := firstNonEmpty(reg.DefaultModel, profile.DefaultModel)
	models := append([]string(nil), reg.Models...)
	if len(models) == 0 && defaultModel != "" {
		models = []string{defaultModel}
	}
	authEnv := append([]string(nil), reg.AuthEnv...)
	if len(authEnv) == 0 {
		authEnv = append(authEnv, reg.APIKeyEnv...)
	}
	capabilities := cloneAnyMap(reg.Capabilities)
	if capabilities == nil {
		capabilities = cloneAnyMap(profile.Capabilities)
	}
	if capabilities == nil {
		capabilities = map[string]any{}
	}
	if reg.DisableStreaming {
		capabilities["streaming"] = false
	}
	operations := append([]string(nil), profile.Conversion.Operations...)
	if reg.DefaultOperation != "" {
		operations = []string{strings.TrimSpace(reg.DefaultOperation)}
	}
	routes := cloneProviderRoutes(reg.Routes)
	if len(routes) == 0 {
		routes = cloneProviderRoutes(profile.Conversion.Routes)
	}
	source := firstNonEmpty(reg.Source, "http-json")
	stream := "httpjson.Caller.StreamProvider"
	if reg.DisableStreaming {
		stream = ""
	}
	return ProviderSpec{
		Name:         reg.Name,
		Aliases:      append([]string(nil), reg.Aliases...),
		DefaultModel: defaultModel,
		Models:       models,
		AuthEnv:      authEnv,
		Local:        reg.Local,
		Capabilities: capabilities,
		Conversion: ProviderConversionSpec{
			RequestConverter:  profile.Conversion.RequestConverter,
			ResponseConverter: profile.Conversion.ResponseConverter,
			Call:              "httpjson.Caller.CallProvider",
			Stream:            stream,
			Source:            source,
			Operations:        operations,
			Routes:            routes,
		},
	}
}

func decodeHTTPJSONProviderRegistrations(raw string) ([]HTTPJSONProviderRegistration, error) {
	var regs []HTTPJSONProviderRegistration
	if strings.HasPrefix(raw, "[") {
		if err := json.Unmarshal([]byte(raw), &regs); err != nil {
			return nil, err
		}
	} else {
		var reg HTTPJSONProviderRegistration
		if err := json.Unmarshal([]byte(raw), &reg); err != nil {
			return nil, err
		}
		regs = []HTTPJSONProviderRegistration{reg}
	}
	if len(regs) == 0 {
		return nil, fmt.Errorf("provider 등록 항목이 필요해요")
	}
	return regs, nil
}

func validateProviderRegistration(reg ProviderRegistration) error {
	name := strings.TrimSpace(reg.Spec.Name)
	if name == "" {
		return fmt.Errorf("provider name이 필요해요")
	}
	if strings.Contains(name, "/") || strings.Contains(name, " ") {
		return fmt.Errorf("provider name은 공백이나 slash 없이 써야 해요: %s", name)
	}
	if reg.Conversion == nil {
		return fmt.Errorf("provider conversion factory가 필요해요: %s", name)
	}
	if len(reg.Spec.Conversion.Operations) == 0 {
		return fmt.Errorf("provider conversion operation이 필요해요: %s", name)
	}
	seen := map[string]struct{}{strings.ToLower(name): {}}
	for _, alias := range reg.Spec.Aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			return fmt.Errorf("provider alias는 비워둘 수 없어요: %s", name)
		}
		if strings.Contains(alias, "/") || strings.Contains(alias, " ") {
			return fmt.Errorf("provider alias는 공백이나 slash 없이 써야 해요: %s", alias)
		}
		key := strings.ToLower(alias)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("provider alias가 중복되었어요: %s", alias)
		}
		seen[key] = struct{}{}
	}
	return nil
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

func httpJSONRoutesFromSpec(routes []ProviderRouteSpec) map[string]httpjson.Route {
	out := make(map[string]httpjson.Route, len(routes))
	for _, route := range routes {
		operation := strings.TrimSpace(route.Operation)
		if operation == "" {
			continue
		}
		out[operation] = httpjson.Route{Method: route.Method, Path: route.Path, Accept: route.Accept, Query: cloneStringMap(route.Query)}
	}
	return out
}

func cloneHTTPJSONRoutes(routes map[string]httpjson.Route) map[string]httpjson.Route {
	if len(routes) == 0 {
		return nil
	}
	out := make(map[string]httpjson.Route, len(routes))
	for operation, route := range routes {
		route.Headers = cloneStringMap(route.Headers)
		route.Query = cloneStringMap(route.Query)
		out[operation] = route
	}
	return out
}

func cloneProviderRoutes(routes []ProviderRouteSpec) []ProviderRouteSpec {
	if len(routes) == 0 {
		return nil
	}
	out := make([]ProviderRouteSpec, len(routes))
	for i, route := range routes {
		route.Query = cloneStringMap(route.Query)
		out[i] = route
	}
	return out
}

func cloneStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneAnyMap(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func firstConfiguredValue(explicit string, envKeys []string) string {
	if value := strings.TrimSpace(explicit); value != "" {
		return value
	}
	for _, key := range envKeys {
		if value := strings.TrimSpace(os.Getenv(strings.TrimSpace(key))); value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
