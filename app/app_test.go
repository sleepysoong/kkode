package app

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/providers/codexcli"
	"github.com/sleepysoong/kkode/providers/copilot"
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
}

func TestProviderSpecsExposeConversionProfiles(t *testing.T) {
	for _, spec := range ProviderSpecs() {
		if spec.Conversion.RequestConverter == "" || spec.Conversion.Call == "" || spec.Conversion.Source == "" {
			t.Fatalf("%s provider는 변환 profile을 노출해야 해요: %+v", spec.Name, spec.Conversion)
		}
		if len(spec.Conversion.Operations) == 0 {
			t.Fatalf("%s provider는 operation 힌트를 노출해야 해요", spec.Name)
		}
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
		ProviderOptions{MCPServers: map[string]llm.MCPServer{"context7": {Name: "context7", Kind: llm.MCPHTTP, URL: "https://default.test"}}},
		ProviderOptions{MCPServers: map[string]llm.MCPServer{"context7": {Name: "context7", Kind: llm.MCPStdio, Command: "custom"}}},
	)
	if merged.MCPServers["context7"].Command != "custom" || merged.MCPServers["context7"].URL != "" {
		t.Fatalf("명시 MCP 설정이 default를 덮어써야 해요: %+v", merged.MCPServers["context7"])
	}
}

func TestBuildProviderWithOptionsMapsHTTPMCPToOpenAITools(t *testing.T) {
	t.Setenv("KKODE_DEFAULT_MCP", "off")
	handle, err := BuildProviderWithOptions("openai", t.TempDir(), ProviderOptions{MCPServers: map[string]llm.MCPServer{
		"context7": {Kind: llm.MCPHTTP, Name: "context7", URL: "https://mcp.context7.com/mcp", Headers: map[string]string{"CONTEXT7_API_KEY": "test"}},
		"serena":   {Kind: llm.MCPStdio, Name: "serena", Command: "uvx"},
	}})
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

func TestNewAgentUsesStandardToolsOnly(t *testing.T) {
	ws, _, err := NewWorkspace(WorkspaceOptions{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ag, err := NewAgent(fakeProvider{}, ws, AgentOptions{Model: "fake", NoWeb: true})
	if err != nil {
		t.Fatal(err)
	}
	req, handlers := ag.Prepare("목록을 봐요")
	if _, ok := handlers["file_read"]; !ok {
		t.Fatal("표준 file_read tool은 노출해야해요")
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
