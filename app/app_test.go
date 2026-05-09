package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/providers/codexcli"
	"github.com/sleepysoong/kkode/providers/copilot"
	"github.com/sleepysoong/kkode/providers/httpjson"
	"github.com/sleepysoong/kkode/providers/omniroute"
	"github.com/sleepysoong/kkode/providers/openai"
	"github.com/sleepysoong/kkode/session"
	"github.com/sleepysoong/kkode/workspace"
)

func TestCSVAndDefaultModel(t *testing.T) {
	got := CSV(" a, ,b ")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("CSV=%#v", got)
	}
	if !EnvBoolValue("yes") || EnvBoolValue("no") {
		t.Fatal("bool 문자열 해석이 흔들리면 안 돼요")
	}
	if DefaultModel("codex") != "gpt-5.3-codex" {
		t.Fatal("codex 기본 모델이 바뀌면 안 돼요")
	}
	if DefaultModel("openai") != "gpt-5-mini" {
		t.Fatal("openai 기본 모델이 바뀌면 안 돼요")
	}
	if DefaultModel("codex-cli") != DefaultModel("codex") {
		t.Fatal("provider alias 기본 모델이 흔들리면 안 돼요")
	}
	if spec, ok := ResolveProviderSpec("github-copilot"); !ok || spec.Name != "copilot" {
		t.Fatalf("provider alias resolve failed: %#v %v", spec, ok)
	} else if spec.Capabilities["skills"] != true || spec.Capabilities["custom_agents"] != true {
		t.Fatalf("copilot provider capability가 gateway discovery에 필요해요: %#v", spec.Capabilities)
	}
	if ProviderAuthStatus(ProviderSpec{Name: "local", Local: true}) != "local" {
		t.Fatal("local provider auth status가 바뀌면 안 돼요")
	}
}

func TestProviderSpecsUseProviderCapabilityContracts(t *testing.T) {
	expected := map[string]map[string]any{
		"openai":    openai.DefaultCapabilities().ToMap(),
		"omniroute": omniroute.DefaultCapabilities().ToMap(),
		"copilot":   copilot.DefaultCapabilities().ToMap(),
		"codex":     codexcli.DefaultCapabilities().ToMap(),
	}
	for _, spec := range ProviderSpecs() {
		want, ok := expected[spec.Name]
		if !ok {
			continue
		}
		if !reflect.DeepEqual(spec.Capabilities, want) {
			t.Fatalf("%s provider capability discovery drifted: got %#v want %#v", spec.Name, spec.Capabilities, want)
		}
	}
}

func TestProviderSpecsAreDefensiveCopies(t *testing.T) {
	specs := ProviderSpecs()
	if len(specs) == 0 {
		t.Fatal("provider registry가 비면 안 돼요")
	}
	specs[0].Aliases = append(specs[0].Aliases, "mutated")
	specs[0].Models = append(specs[0].Models, "mutated-model")
	specs[0].Capabilities["tools"] = false
	specs[0].Conversion.Operations = append(specs[0].Conversion.Operations, "mutated-operation")
	specs[0].Conversion.Routes = append(specs[0].Conversion.Routes, ProviderRouteSpec{Operation: "mutated-route"})
	if len(specs[0].Conversion.Routes) > 0 {
		if specs[0].Conversion.Routes[0].Query == nil {
			specs[0].Conversion.Routes[0].Query = map[string]string{}
		}
		specs[0].Conversion.Routes[0].Query["mutated"] = "yes"
	}
	fresh := ProviderSpecs()
	if len(fresh[0].Aliases) > 0 && fresh[0].Aliases[len(fresh[0].Aliases)-1] == "mutated" {
		t.Fatal("ProviderSpecs는 alias slice를 방어 복사해야 해요")
	}
	if len(fresh[0].Models) > 0 && fresh[0].Models[len(fresh[0].Models)-1] == "mutated-model" {
		t.Fatal("ProviderSpecs는 model slice를 방어 복사해야 해요")
	}
	if fresh[0].Capabilities["tools"] != true {
		t.Fatal("ProviderSpecs는 capability map을 방어 복사해야 해요")
	}
	if len(fresh[0].Conversion.Operations) > 0 && fresh[0].Conversion.Operations[len(fresh[0].Conversion.Operations)-1] == "mutated-operation" {
		t.Fatal("ProviderSpecs는 conversion operation slice를 방어 복사해야 해요")
	}
	if len(fresh[0].Conversion.Routes) > 0 && fresh[0].Conversion.Routes[len(fresh[0].Conversion.Routes)-1].Operation == "mutated-route" {
		t.Fatal("ProviderSpecs는 conversion route slice를 방어 복사해야 해요")
	}
	if len(fresh[0].Conversion.Routes) > 0 && fresh[0].Conversion.Routes[0].Query["mutated"] == "yes" {
		t.Fatal("ProviderSpecs는 conversion route query map을 방어 복사해야 해요")
	}
}

func TestProviderSpecsExposeConversionProfiles(t *testing.T) {
	for _, spec := range ProviderSpecs() {
		if spec.Conversion.RequestConverter == "" || spec.Conversion.Call == "" || spec.Conversion.Source == "" {
			t.Fatalf("%s provider는 변환 profile을 노출해야 해요: %+v", spec.Name, spec.Conversion)
		}
		if len(spec.Conversion.Operations) == 0 {
			t.Fatalf("%s provider는 operation 힌트를 노출해야 해요", spec.Name)
		}
		if (spec.Name == "openai" || spec.Name == "omniroute") && (len(spec.Conversion.Routes) == 0 || spec.Conversion.Routes[0].Path != "/responses") {
			t.Fatalf("%s HTTP provider는 route 힌트를 노출해야 해요: %+v", spec.Name, spec.Conversion.Routes)
		}
	}
}

func TestRegisterProviderAddsExternalConversionProfile(t *testing.T) {
	unregister, err := RegisterProvider(ProviderRegistration{
		Spec: ProviderSpec{
			Name:         "registered-api",
			Aliases:      []string{"registered-compatible"},
			DefaultModel: "registered-model",
			Models:       []string{"registered-model"},
			Capabilities: llm.Capabilities{Tools: true, StructuredOutput: true}.ToMap(),
			Conversion: ProviderConversionSpec{
				RequestConverter:  "registered.RequestConverter",
				ResponseConverter: "registered.ResponseConverter",
				Call:              "registered.Caller.CallProvider",
				Source:            "external-test-source",
				Operations:        []string{"registered.create"},
			},
		},
		Conversion: func(spec ProviderSpec) ProviderConversionSet {
			operation := firstOperation(spec)
			converter := openai.ResponsesConverter{ProviderName: spec.Name}
			return ProviderConversionSet{
				RequestConverter:  converter,
				ResponseConverter: converter,
				Options:           llm.ConvertOptions{Operation: operation},
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()

	spec, ok := ResolveProviderSpec("registered-compatible")
	if !ok || spec.Name != "registered-api" || spec.Conversion.Source != "external-test-source" {
		t.Fatalf("등록 provider discovery가 이상해요: spec=%+v ok=%v", spec, ok)
	}
	provider, err := BuildProviderAdapter("registered-compatible", ProviderAdapterOptions{Caller: &sourceOnlyCaller{}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := provider.Generate(context.Background(), llm.Request{Model: "registered-model", Messages: []llm.Message{llm.UserText("확장")}})
	if err != nil {
		t.Fatal(err)
	}
	if !provider.Capabilities().StructuredOutput || resp.Provider != "registered-api" || resp.Text != "source-only 응답이에요" {
		t.Fatalf("등록 provider가 변환 레이어와 adapter를 재사용해야 해요: caps=%+v resp=%+v", provider.Capabilities(), resp)
	}

	unregister()
	if _, ok := ResolveProviderSpec("registered-compatible"); ok {
		t.Fatal("unregister 뒤에는 provider discovery에서 사라져야 해요")
	}
}

func TestRegisterProviderRejectsDuplicateAliases(t *testing.T) {
	unregister, err := RegisterProvider(ProviderRegistration{
		Spec: ProviderSpec{
			Name: "duplicate-test",
			Conversion: ProviderConversionSpec{
				Operations: []string{"duplicate.create"},
			},
		},
		Conversion: func(spec ProviderSpec) ProviderConversionSet {
			converter := openai.ResponsesConverter{ProviderName: spec.Name}
			return ProviderConversionSet{RequestConverter: converter, ResponseConverter: converter, Options: llm.ConvertOptions{Operation: firstOperation(spec)}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()

	if _, err := RegisterProvider(ProviderRegistration{
		Spec: ProviderSpec{
			Name:    "duplicate-test-2",
			Aliases: []string{"duplicate-test"},
			Conversion: ProviderConversionSpec{
				Operations: []string{"duplicate.create"},
			},
		},
		Conversion: func(spec ProviderSpec) ProviderConversionSet {
			converter := openai.ResponsesConverter{ProviderName: spec.Name}
			return ProviderConversionSet{RequestConverter: converter, ResponseConverter: converter, Options: llm.ConvertOptions{Operation: firstOperation(spec)}}
		},
	}); err == nil {
		t.Fatal("이미 등록된 provider name을 alias로 재사용하면 안 돼요")
	}
}

func TestDefaultMCPServersExposeSerenaAndContext7(t *testing.T) {
	root := t.TempDir()
	t.Setenv("KKODE_SERENA_COMMAND", "uvx")
	t.Setenv("CONTEXT7_API_KEY", "ctx7sk-test")
	servers := DefaultMCPServers(root)
	serena := servers["serena"]
	if serena.Kind != llm.MCPStdio || serena.Command != "uvx" || serena.Cwd == "" || !containsString(serena.Args, "--project") || !containsString(serena.Args, serena.Cwd) {
		t.Fatalf("Serena 기본 MCP manifest가 이상해요: %+v", serena)
	}
	context7 := servers["context7"]
	if context7.Kind != llm.MCPHTTP || context7.URL != defaultContext7URL || context7.Headers["CONTEXT7_API_KEY"] != "ctx7sk-test" {
		t.Fatalf("Context7 기본 MCP manifest가 이상해요: %+v", context7)
	}
}

func TestDefaultMCPDiagnosticsExplainSerenaAvailability(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("KKODE_SERENA_COMMAND", "")
	diagnostics := DefaultMCPDiagnostics(root)
	serena := diagnosticByName(diagnostics, "serena")
	if serena.Status != "missing" || !strings.Contains(serena.Message, "uvx") {
		t.Fatalf("Serena missing 이유를 diagnostics에 담아야 해요: %+v", diagnostics)
	}
	t.Setenv("KKODE_SERENA_COMMAND", "serena-bin")
	diagnostics = DefaultMCPDiagnostics(root)
	serena = diagnosticByName(diagnostics, "serena")
	if serena.Status != "configured" || !strings.Contains(serena.Message, "serena-bin") {
		t.Fatalf("Serena configured 상태가 필요해요: %+v", diagnostics)
	}
}

func diagnosticByName(items []DefaultMCPDiagnostic, name string) DefaultMCPDiagnostic {
	for _, item := range items {
		if item.Name == name {
			return item
		}
	}
	return DefaultMCPDiagnostic{}
}

func TestDefaultProviderOptionsCanDisableMCPDefaults(t *testing.T) {
	t.Setenv("KKODE_DEFAULT_MCP", "off")
	if opts := DefaultProviderOptions(t.TempDir()); len(opts.MCPServers) != 0 {
		t.Fatalf("KKODE_DEFAULT_MCP=off면 기본 MCP를 붙이면 안 돼요: %+v", opts)
	}
}

func TestMergeProviderOptionsLetsExplicitMCPOverrideDefaults(t *testing.T) {
	merged := MergeProviderOptions(
		ProviderOptions{MCPServers: map[string]llm.MCPServer{"context7": {Name: "context7", Kind: llm.MCPHTTP, URL: "https://default.test"}}, ContextBlocks: []string{"default context"}},
		ProviderOptions{MCPServers: map[string]llm.MCPServer{"context7": {Name: "context7", Kind: llm.MCPStdio, Command: "custom"}}, ContextBlocks: []string{"explicit context"}},
	)
	if merged.MCPServers["context7"].Command != "custom" || merged.MCPServers["context7"].URL != "" {
		t.Fatalf("명시 MCP 설정이 default를 덮어써야 해요: %+v", merged.MCPServers["context7"])
	}
	if len(merged.ContextBlocks) != 2 || merged.ContextBlocks[0] != "default context" || merged.ContextBlocks[1] != "explicit context" {
		t.Fatalf("provider context block도 순서대로 합쳐야 해요: %+v", merged.ContextBlocks)
	}
}

func TestBuildProviderWithOptionsMapsHTTPMCPToOpenAITools(t *testing.T) {
	t.Setenv("KKODE_DEFAULT_MCP", "off")
	opts := ProviderOptions{MCPServers: map[string]llm.MCPServer{
		"context7": {Kind: llm.MCPHTTP, Name: "context7", URL: "https://mcp.context7.com/mcp", Headers: map[string]string{"CONTEXT7_API_KEY": "test"}},
		"serena":   {Kind: llm.MCPStdio, Name: "serena", Command: "uvx"},
	}}
	handle, err := BuildProviderWithOptions("openai", t.TempDir(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(handle.BaseRequest.Tools) != 1 {
		t.Fatalf("HTTP MCP만 OpenAI-compatible tool로 붙어야 해요: %+v", handle.BaseRequest.Tools)
	}
	tool := handle.BaseRequest.Tools[0]
	if tool.Kind != llm.ToolBuiltin || tool.Name != "mcp" || tool.ProviderOptions["server_label"] != "context7" || tool.ProviderOptions["server_url"] != "https://mcp.context7.com/mcp" {
		t.Fatalf("OpenAI-compatible MCP tool 변환이 이상해요: %+v", tool)
	}
	surfaces := MCPToolsFromProviderOptions(opts)
	if len(surfaces.Hosted) != 1 || surfaces.Hosted[0].ProviderOptions["server_label"] != "context7" {
		t.Fatalf("hosted MCP surface가 provider 기본 request와 같아야 해요: %+v", surfaces.Hosted)
	}
	localDefs, localHandlers := surfaces.Local.Parts()
	if len(localDefs) != 1 || localDefs[0].Name != "mcp_call" {
		t.Fatalf("local MCP surface는 같은 manifest에서 mcp_call을 노출해야 해요: %+v", localDefs)
	}
	if _, ok := localHandlers["mcp_call"]; !ok {
		t.Fatal("local MCP surface에는 mcp_call handler가 필요해요")
	}
}

func TestBuildProviderWithResolvedOptionsDoesNotReapplyDefaults(t *testing.T) {
	t.Setenv("KKODE_CONTEXT7_URL", "https://mcp.context7.com/mcp")
	handle, err := BuildProviderWithResolvedOptions("openai", t.TempDir(), ProviderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(handle.BaseRequest.Tools) != 0 {
		t.Fatalf("resolved provider options should not reapply default MCP tools: %+v", handle.BaseRequest.Tools)
	}
}

func TestMergeBaseRequestPreservesProviderDefaults(t *testing.T) {
	merged := MergeBaseRequest(
		llm.Request{Instructions: "default", Tools: []llm.Tool{{Name: "mcp", Kind: llm.ToolBuiltin}}, Include: []string{"reasoning.encrypted_content"}, Metadata: map[string]string{"default": "yes"}},
		llm.Request{Tools: []llm.Tool{{Name: "file_read"}}, Include: []string{"output"}, Metadata: map[string]string{"explicit": "yes"}},
	)
	if merged.Instructions != "default" || len(merged.Tools) != 2 || merged.Tools[0].Name != "mcp" || merged.Tools[1].Name != "file_read" {
		t.Fatalf("provider default request와 explicit request를 순서대로 합쳐야 해요: %+v", merged)
	}
	if len(merged.Include) != 2 || merged.Metadata["default"] != "yes" || merged.Metadata["explicit"] != "yes" {
		t.Fatalf("slice/map 필드 merge가 이상해요: %+v", merged)
	}
}

func TestPreviewProviderRequestConvertsAndRedacts(t *testing.T) {
	preview, err := PreviewProviderRequest(context.Background(), "openai", llm.Request{
		Model:    "gpt-5-mini",
		Messages: []llm.Message{llm.UserText("token=abc1234567890secretvalue 를 숨겨요")},
		Tools:    []llm.Tool{{Name: "file_read", Description: "파일을 읽어요"}},
	}, false, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Provider != "openai" || preview.Operation != "responses.create" || preview.Model != "gpt-5-mini" {
		t.Fatalf("provider request preview 기본값이 이상해요: %+v", preview)
	}
	if !strings.Contains(preview.BodyJSON, "file_read") || !strings.Contains(preview.BodyJSON, "[REDACTED]") || strings.Contains(preview.BodyJSON, "abc1234567890secretvalue") {
		t.Fatalf("body preview 변환/마스킹이 이상해요: %s", preview.BodyJSON)
	}
	body, truncated, err := previewJSON(map[string]any{"text": "가나다라마바사"}, 15)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || !utf8.ValidString(body) {
		t.Fatalf("provider body preview는 UTF-8 경계에서 잘려야 해요: truncated=%v body=%q", truncated, body)
	}
}

func TestPreviewProviderRequestSupportsAliasAndRawPayload(t *testing.T) {
	preview, err := PreviewProviderRequest(context.Background(), "github-copilot", llm.Request{Model: "gpt-5-mini", Messages: []llm.Message{llm.UserText("안녕")}}, true, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Provider != "copilot" || preview.Operation != "copilot.session.send" || !preview.Stream || !strings.Contains(preview.RawType, "sessionSendPayload") || !strings.Contains(preview.RawJSON, "안녕") {
		t.Fatalf("alias/raw provider request preview가 이상해요: %+v", preview)
	}
}

func TestBuildProviderPipelineLetsSourceOnlyCallerReuseConversion(t *testing.T) {
	caller := &sourceOnlyCaller{}
	pipeline, err := BuildProviderPipeline("openai-compatible", caller, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := pipeline.Generate(context.Background(), llm.Request{Model: "gpt-5-mini", Messages: []llm.Message{llm.UserText("안녕")}})
	if err != nil {
		t.Fatal(err)
	}
	if caller.got.Operation != "responses.create" || caller.got.Model != "gpt-5-mini" || caller.got.Body == nil {
		t.Fatalf("표준 요청이 provider 요청으로 변환되지 않았어요: %+v", caller.got)
	}
	if resp.Provider != "openai" || resp.Text != "source-only 응답이에요" {
		t.Fatalf("source 응답이 표준 응답으로 복원되지 않았어요: %+v", resp)
	}
}

func TestBuildProviderPipelineCanUseGenericHTTPJSONCaller(t *testing.T) {
	var gotPath string
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		_, _ = w.Write([]byte(`{"id":"resp_httpjson","model":"gpt-5-mini","status":"completed","output_text":"httpjson 응답이에요"}`))
	}))
	defer server.Close()

	caller := httpjson.New(httpjson.Config{
		ProviderName:     "custom-openai-compatible",
		BaseURL:          server.URL + "/v1",
		HTTPClient:       server.Client(),
		DefaultOperation: "responses.create",
		Routes:           map[string]httpjson.Route{"responses.create": {Path: "/responses"}},
	})
	pipeline, err := BuildProviderPipeline("openai-compatible", caller, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := pipeline.Generate(context.Background(), llm.Request{Model: "gpt-5-mini", Messages: []llm.Message{llm.UserText("안녕")}})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/responses" || !strings.Contains(gotBody, "안녕") || resp.Provider != "custom-openai-compatible" || resp.Text != "httpjson 응답이에요" {
		t.Fatalf("generic HTTP JSON caller 연결이 이상해요: path=%s body=%s resp=%+v", gotPath, gotBody, resp)
	}
}

func TestBuildProviderAdapterWrapsCustomSource(t *testing.T) {
	caller := &sourceOnlyCaller{}
	provider, err := BuildProviderAdapter("openai", ProviderAdapterOptions{Caller: caller})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := provider.Generate(context.Background(), llm.Request{Model: "gpt-5-mini", Messages: []llm.Message{llm.UserText("어댑터")}})
	if err != nil {
		t.Fatal(err)
	}
	if provider.Name() != "openai" || !provider.Capabilities().Tools || !provider.Capabilities().Streaming {
		t.Fatalf("registry capability가 adapter에 전파되어야 해요: %s %+v", provider.Name(), provider.Capabilities())
	}
	if caller.got.Operation != "responses.create" || resp.Text != "source-only 응답이에요" {
		t.Fatalf("adapter가 변환 레이어와 source caller를 연결하지 못했어요: got=%+v resp=%+v", caller.got, resp)
	}
}

func TestBuildProviderAdapterAllowsCustomProviderName(t *testing.T) {
	caller := &sourceOnlyCaller{}
	provider, err := BuildProviderAdapter("openai", ProviderAdapterOptions{ProviderName: "custom-source", Caller: caller})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := provider.Generate(context.Background(), llm.Request{Model: "gpt-5-mini", Messages: []llm.Message{llm.UserText("커스텀")}})
	if err != nil {
		t.Fatal(err)
	}
	if provider.Name() != "custom-source" || resp.Provider != "custom-source" {
		t.Fatalf("custom provider label이 표준 응답까지 유지돼야 해요: name=%s resp=%+v", provider.Name(), resp)
	}
}

func TestBuildHTTPJSONProviderAdapterUsesRegistryRoutes(t *testing.T) {
	var gotPath string
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"id":"resp_http_adapter","model":"gpt-5-mini","status":"completed","output_text":"adapter 응답이에요"}`))
	}))
	defer server.Close()

	provider, err := BuildHTTPJSONProviderAdapter("openai-compatible", HTTPJSONProviderOptions{
		ProviderName: "custom-http",
		BaseURL:      server.URL + "/v1",
		APIKey:       "sk-test",
		HTTPClient:   server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := provider.Generate(context.Background(), llm.Request{Model: "gpt-5-mini", Messages: []llm.Message{llm.UserText("안녕")}})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/responses" || gotAuth == "" || provider.Name() != "custom-http" || resp.Provider != "custom-http" || resp.Text != "adapter 응답이에요" {
		t.Fatalf("HTTP JSON adapter가 registry route/source label을 재사용하지 못했어요: path=%s auth=%s name=%s resp=%+v", gotPath, gotAuth, provider.Name(), resp)
	}
}

func TestBuildHTTPJSONProviderAdapterAppliesResponseLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"resp_http_adapter","model":"gpt-5-mini","status":"completed","output_text":"adapter 응답이에요"}`))
	}))
	defer server.Close()

	provider, err := BuildHTTPJSONProviderAdapter("openai-compatible", HTTPJSONProviderOptions{
		ProviderName:     "limited-http",
		BaseURL:          server.URL + "/v1",
		HTTPClient:       server.Client(),
		MaxResponseBytes: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Generate(context.Background(), llm.Request{Model: "gpt-5-mini", Messages: []llm.Message{llm.UserText("안녕")}})
	if err == nil || !strings.Contains(err.Error(), "max_bytes=8") {
		t.Fatalf("HTTP JSON adapter response limit이 적용돼야 해요: %v", err)
	}

	_, err = BuildHTTPJSONProviderAdapter("openai-compatible", HTTPJSONProviderOptions{MaxResponseBytes: -1})
	if err == nil || !strings.Contains(err.Error(), "max_response_bytes") {
		t.Fatalf("negative HTTP JSON adapter response limit은 거부해야 해요: %v", err)
	}
	_, err = BuildHTTPJSONProviderAdapter("openai-compatible", HTTPJSONProviderOptions{MaxResponseBytes: httpjson.MaxResponseBytes + 1})
	if err == nil || !strings.Contains(err.Error(), "max_response_bytes") {
		t.Fatalf("large HTTP JSON adapter response limit은 거부해야 해요: %v", err)
	}
}

func TestPreviewProviderRequestShowsResolvedHTTPRoute(t *testing.T) {
	unregister, err := RegisterHTTPJSONProvider(HTTPJSONProviderRegistration{
		Name:         "preview-route-http",
		Profile:      "openai-compatible",
		DefaultModel: "model/with/slash",
		BaseURL:      "https://preview.example.test/v1",
		Routes: []ProviderRouteSpec{{
			Operation: "responses.create",
			Method:    http.MethodPost,
			Path:      "/deployments/{model}/responses",
			Query:     map[string]string{"api-version": "{metadata.api_version}"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()

	preview, err := PreviewProviderRequest(context.Background(), "preview-route-http", llm.Request{Model: "model/with/slash", Messages: []llm.Message{llm.UserText("route")}, Metadata: map[string]string{"api_version": "2026-05-07"}}, false, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Route == nil || preview.Route.Path != "/deployments/{model}/responses" || preview.Route.ResolvedPath != "/deployments/model%2Fwith%2Fslash/responses" || preview.Route.ResolvedQuery["api-version"] != "2026-05-07" {
		t.Fatalf("route preview가 template를 확장해야 해요: %+v", preview.Route)
	}
}

func TestPreviewProviderRequestFailsWhenRouteTemplateMetadataMissing(t *testing.T) {
	unregister, err := RegisterHTTPJSONProvider(HTTPJSONProviderRegistration{
		Name:         "preview-route-missing-http",
		Profile:      "openai-compatible",
		DefaultModel: "gpt-5-mini",
		BaseURL:      "https://preview.example.test/v1",
		Routes:       []ProviderRouteSpec{{Operation: "responses.create", Path: "/responses", Query: map[string]string{"api-version": "{metadata.api_version}"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()

	_, err = PreviewProviderRequest(context.Background(), "preview-route-missing-http", llm.Request{Model: "gpt-5-mini", Messages: []llm.Message{llm.UserText("route")}}, false, 4096)
	if err == nil || !strings.Contains(err.Error(), "api_version") {
		t.Fatalf("route template metadata 누락은 preview에서 잡아야 해요: %v", err)
	}
}

func TestBuildHTTPJSONProviderAdapterCanDisableStreamingCapability(t *testing.T) {
	provider, err := BuildHTTPJSONProviderAdapter("openai-compatible", HTTPJSONProviderOptions{
		ProviderName:     "json-only",
		BaseURL:          "https://example.test/v1",
		DisableStreaming: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	caps := provider.Capabilities()
	if !caps.Tools || caps.Streaming {
		t.Fatalf("JSON-only HTTP source는 tools는 유지하되 streaming capability를 끄야 해요: %+v", caps)
	}
	if _, err := provider.Stream(context.Background(), llm.Request{Model: "gpt-5-mini", Messages: []llm.Message{llm.UserText("안녕")}}); err == nil || !strings.Contains(err.Error(), "stream caller") {
		t.Fatalf("streamer가 없으면 Stream 호출은 명확히 실패해야 해요: %v", err)
	}
}

func TestRegisterHTTPJSONProviderReusesProfileWithOnlySourceConfig(t *testing.T) {
	t.Setenv("CUSTOM_HTTP_KEY", "sk-env")
	var gotPath string
	var gotAuth string
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		_, _ = w.Write([]byte(`{"id":"resp_registered_http","model":"source-model","status":"completed","output_text":"등록 source 응답이에요"}`))
	}))
	defer server.Close()

	unregister, err := RegisterHTTPJSONProvider(HTTPJSONProviderRegistration{
		Name:         "registered-http",
		Aliases:      []string{"registered-http-compatible"},
		Profile:      "openai-compatible",
		DefaultModel: "source-model",
		AuthEnv:      []string{"CUSTOM_HTTP_KEY"},
		BaseURL:      server.URL + "/v1",
		APIKeyEnv:    []string{"CUSTOM_HTTP_KEY"},
		HTTPClient:   server.Client(),
		Source:       "test-http-json",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()

	spec, ok := ResolveProviderSpec("registered-http-compatible")
	if !ok || spec.Name != "registered-http" || spec.DefaultModel != "source-model" || spec.Conversion.Source != "test-http-json" || spec.Conversion.Call != "httpjson.Caller.CallProvider" || spec.Conversion.Routes[0].Path != "/responses" {
		t.Fatalf("HTTP JSON source discovery가 profile과 source 설정을 함께 보여줘야 해요: spec=%+v ok=%v", spec, ok)
	}
	if ProviderAuthStatus(spec) != "configured" {
		t.Fatalf("source 등록 auth env가 discovery에 반영돼야 해요: %+v", spec.AuthEnv)
	}

	handle, err := BuildProvider("registered-http-compatible", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := handle.Provider.Generate(context.Background(), llm.Request{Model: "source-model", Messages: []llm.Message{llm.UserText("source만 추가해요")}})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/responses" || gotAuth != "Bearer sk-env" || !strings.Contains(gotBody, "source만 추가해요") || resp.Provider != "registered-http" || resp.Text != "등록 source 응답이에요" {
		t.Fatalf("등록 HTTP source가 기존 변환 profile을 재사용해야 해요: path=%s auth=%s body=%s resp=%+v", gotPath, gotAuth, gotBody, resp)
	}
}

func TestRegisterHTTPJSONProvidersFromJSONAndEnv(t *testing.T) {
	t.Setenv("CUSTOM_JSON_KEY", "sk-json")
	raw := `{
		"name": "registered-json-http",
		"aliases": ["registered-json-compatible"],
		"profile": "openai-compatible",
		"default_model": "json-model",
		"auth_env": ["CUSTOM_JSON_KEY"],
		"base_url": "https://json.example.test/v1",
		"api_key_env": ["CUSTOM_JSON_KEY"],
		"disable_streaming": true,
		"source": "json-config",
		"routes": [{"operation":"responses.create","method":"POST","path":"/responses/{model}","query":{"api-version":"{metadata.api_version}"}}]
	}`
	unregister, err := RegisterHTTPJSONProvidersFromJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()

	spec, ok := ResolveProviderSpec("registered-json-compatible")
	if !ok || spec.Name != "registered-json-http" || spec.DefaultModel != "json-model" || spec.Conversion.Source != "json-config" || spec.Capabilities["streaming"] != false || len(spec.Conversion.Routes) != 1 || spec.Conversion.Routes[0].Query["api-version"] != "{metadata.api_version}" {
		t.Fatalf("JSON provider 등록이 discovery에 반영돼야 해요: spec=%+v ok=%v", spec, ok)
	}
	if ProviderAuthStatus(spec) != "configured" {
		t.Fatalf("JSON provider auth env가 반영돼야 해요: %+v", spec.AuthEnv)
	}
	unregister()
	if _, ok := ResolveProviderSpec("registered-json-compatible"); ok {
		t.Fatal("JSON provider unregister가 registry를 되돌려야 해요")
	}

	t.Setenv("KKODE_TEST_HTTPJSON_PROVIDERS", `[{"name":"registered-env-http","default_model":"env-model","base_url":"https://env.example.test/v1"}]`)
	unregisterEnv, err := RegisterHTTPJSONProvidersFromEnv("KKODE_TEST_HTTPJSON_PROVIDERS")
	if err != nil {
		t.Fatal(err)
	}
	defer unregisterEnv()
	if spec, ok := ResolveProviderSpec("registered-env-http"); !ok || spec.DefaultModel != "env-model" {
		t.Fatalf("env provider 등록이 필요해요: spec=%+v ok=%v", spec, ok)
	}

	_, err = RegisterHTTPJSONProvidersFromJSON(`{"name":"negative-http","base_url":"https://negative.example.test/v1","max_response_bytes":-1}`)
	if err == nil || !strings.Contains(err.Error(), "max_response_bytes") {
		t.Fatalf("negative HTTP JSON provider response limit은 등록에서 거부해야 해요: %v", err)
	}
	_, err = RegisterHTTPJSONProvidersFromJSON(`{"name":"large-http","base_url":"https://large.example.test/v1","max_response_bytes":33554433}`)
	if err == nil || !strings.Contains(err.Error(), "max_response_bytes") {
		t.Fatalf("large HTTP JSON provider response limit은 등록에서 거부해야 해요: %v", err)
	}
}

func TestRegisterHTTPJSONProvidersFromJSONRollsBackOnFailure(t *testing.T) {
	_, err := RegisterHTTPJSONProvidersFromJSON(`[
		{"name":"rollback-http","base_url":"https://rollback.example.test/v1"},
		{"name":"rollback-http","base_url":"https://duplicate.example.test/v1"}
	]`)
	if err == nil {
		t.Fatal("중복 provider가 있으면 등록이 실패해야 해요")
	}
	if _, ok := ResolveProviderSpec("rollback-http"); ok {
		t.Fatal("부분 등록 실패 시 앞 provider도 되돌려야 해요")
	}
}

type sourceOnlyCaller struct {
	got llm.ProviderRequest
}

func (c *sourceOnlyCaller) CallProvider(ctx context.Context, req llm.ProviderRequest) (llm.ProviderResult, error) {
	c.got = req
	return llm.ProviderResult{Body: []byte(`{"id":"resp_source","model":"gpt-5-mini","status":"completed","output_text":"source-only 응답이에요"}`)}, nil
}

func TestNewAgentUsesStandardToolsOnly(t *testing.T) {
	ws, _, err := NewWorkspace(WorkspaceOptions{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ag, err := NewAgent(fakeProvider{}, ws, AgentOptions{Model: "fake", NoWeb: true, MCPServers: map[string]llm.MCPServer{"serena": {Kind: llm.MCPStdio, Command: "serena"}}})
	if err != nil {
		t.Fatal(err)
	}
	req, handlers := ag.Prepare("목록을 봐요")
	if _, ok := handlers["file_read"]; !ok {
		t.Fatal("표준 file_read tool은 노출해야해요")
	}
	if _, ok := handlers["file_delete"]; !ok {
		t.Fatal("표준 file_delete tool은 agent surface에 노출해야 해요")
	}
	if _, ok := handlers["file_move"]; !ok {
		t.Fatal("표준 file_move tool은 agent surface에 노출해야 해요")
	}
	if _, ok := handlers["lsp_symbols"]; !ok {
		t.Fatal("agent run에도 codeintel lsp_symbols tool을 노출해야 해요")
	}
	if _, ok := handlers["mcp_call"]; !ok {
		t.Fatal("선택된 MCP server는 agent run의 mcp_call local tool로 노출해야 해요")
	}
	if _, ok := handlers["web_fetch"]; ok {
		t.Fatal("NoWeb이면 web_fetch를 자동 노출하지 않아요")
	}
	if _, ok := handlers["workspace_read_file"]; ok {
		t.Fatal("이전 workspace_* tool은 agent에 자동 노출하지 않아요")
	}
	for _, tool := range req.Tools {
		if tool.Name == "workspace_read_file" {
			t.Fatal("이전 workspace_* tool 정의가 request에 들어가면 안 돼요")
		}
	}

	ag, err = NewAgent(fakeProvider{}, ws, AgentOptions{Model: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	_, handlers = ag.Prepare("웹도 써요")
	if _, ok := handlers["web_fetch"]; !ok {
		t.Fatal("기본 agent surface에는 web_fetch도 붙어야 해요")
	}

	ag, err = NewAgent(fakeProvider{}, ws, AgentOptions{Model: "fake", EnabledTools: []string{"file_read", "file_move", "shell_run", "lsp_symbols", "mcp_call"}, DisabledTools: []string{"shell_run"}, MCPServers: map[string]llm.MCPServer{"serena": {Kind: llm.MCPStdio, Command: "serena"}}})
	if err != nil {
		t.Fatal(err)
	}
	req, handlers = ag.Prepare("읽기만 해요")
	if _, ok := handlers["file_read"]; !ok {
		t.Fatal("enabled_tools에 포함된 tool은 노출해야 해요")
	}
	if _, ok := handlers["file_move"]; !ok {
		t.Fatal("enabled_tools에 포함된 file mutation tool은 노출해야 해요")
	}
	if _, ok := handlers["lsp_symbols"]; !ok {
		t.Fatal("enabled_tools에 포함된 lsp tool은 노출해야 해요")
	}
	if _, ok := handlers["mcp_call"]; !ok {
		t.Fatal("enabled_tools에 포함된 mcp_call tool은 노출해야 해요")
	}
	if _, ok := handlers["shell_run"]; ok {
		t.Fatal("disabled_tools는 enabled_tools보다 우선해서 tool을 숨겨야 해요")
	}
	if len(req.Tools) != 4 || req.Tools[0].Name != "file_read" || req.Tools[1].Name != "file_move" || req.Tools[2].Name != "lsp_symbols" || req.Tools[3].Name != "mcp_call" {
		t.Fatalf("요청 tool 정의도 필터링해야 해요: %+v", req.Tools)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestNewAgentRejectsNilWorkspace(t *testing.T) {
	if _, err := NewAgent(fakeProvider{}, nil, AgentOptions{Model: "fake"}); err == nil {
		t.Fatal("workspace 없이 agent를 조립하면 안 돼요")
	}
}

func TestNewRuntimeAppliesReusableDefaults(t *testing.T) {
	ag, err := NewAgent(fakeProvider{}, mustWorkspace(t), AgentOptions{Model: "fake", NoWeb: true})
	if err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(nil, ag, RuntimeOptions{ProjectRoot: "/repo", ProviderName: "fake", Model: "fake"})
	if rt.AgentName != "kkode-agent" || rt.Mode != session.AgentModeBuild || rt.MaxHistoryTurns != 8 {
		t.Fatalf("runtime defaults=%#v", rt)
	}
	if !rt.EnableTodos || !rt.Compaction.Enabled || rt.Compaction.PreserveLastNTurns != 4 {
		t.Fatalf("runtime policies=%#v", rt)
	}
}

func mustWorkspace(t *testing.T) *workspace.Workspace {
	t.Helper()
	ws, _, err := NewWorkspace(WorkspaceOptions{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

type fakeProvider struct{}

func (fakeProvider) Name() string                   { return "fake" }
func (fakeProvider) Capabilities() llm.Capabilities { return llm.Capabilities{Tools: true} }
func (fakeProvider) Generate(ctx context.Context, req llm.Request) (*llm.Response, error) {
	return &llm.Response{Text: "ok"}, nil
}
