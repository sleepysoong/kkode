package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sleepysoong/kkode/app"
	"github.com/sleepysoong/kkode/gateway"
	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/session"
)

func TestRemoteBindRequiresAPIKey(t *testing.T) {
	err := run([]string{"-addr", "0.0.0.0:41234", "-state", t.TempDir() + "/state.db"})
	if err == nil || !strings.Contains(err.Error(), "--api-key") {
		t.Fatalf("expected api key error, got %v", err)
	}
}

func TestLoopbackListenAddr(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:41234": true,
		"localhost:41234": true,
		"[::1]:41234":     true,
		"0.0.0.0:41234":   false,
		":41234":          false,
	}
	for addr, want := range cases {
		if got := isLoopbackListenAddr(addr); got != want {
			t.Fatalf("isLoopbackListenAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestSplitCSV(t *testing.T) {
	got := splitCSV(" https://panel.example, ,http://localhost:3000 ")
	if len(got) != 2 || got[0] != "https://panel.example" || got[1] != "http://localhost:3000" {
		t.Fatalf("splitCSV 결과가 이상해요: %+v", got)
	}
}

func TestProviderDTOsExposeConversionProfile(t *testing.T) {
	providers := providerDTOs()
	if len(providers) == 0 {
		t.Fatal("provider 목록이 필요해요")
	}
	for _, provider := range providers {
		if provider.Conversion == nil || provider.Conversion.RequestConverter == "" || provider.Conversion.Source == "" {
			t.Fatalf("%s provider 변환 profile이 gateway discovery에 필요해요: %+v", provider.Name, provider.Conversion)
		}
		if provider.Name == "openai" && (len(provider.Conversion.Routes) == 0 || provider.Conversion.Routes[0].Path != "/responses") {
			t.Fatalf("OpenAI-compatible provider route discovery가 필요해요: %+v", provider.Conversion)
		}
		if provider.Name == "copilot" && len(provider.Aliases) == 0 {
			t.Fatalf("마이그레이션 친화 provider alias가 필요해요: %+v", provider)
		}
		if provider.AuthStatus != "local" && len(provider.AuthEnv) == 0 {
			t.Fatalf("%s provider 설정 UI가 쓸 auth env 힌트가 필요해요: %+v", provider.Name, provider)
		}
	}
}

func TestProviderDTOsIncludeEnvRegisteredHTTPJSONProviders(t *testing.T) {
	t.Setenv("KKODE_TEST_HTTPJSON_PROVIDERS", `{"name":"gateway-env-http","aliases":["gateway-env-compatible"],"default_model":"env-model","base_url":"https://env.example.test/v1","source":"env-http-json","routes":[{"operation":"responses.create","path":"/responses/{model}","query":{"trace":"{metadata.trace_id}"}}]}`)
	unregister, err := app.RegisterHTTPJSONProvidersFromEnv("KKODE_TEST_HTTPJSON_PROVIDERS")
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()

	providers := providerDTOs()
	var found gateway.ProviderDTO
	for _, provider := range providers {
		if provider.Name == "gateway-env-http" {
			found = provider
			break
		}
	}
	if found.Name == "" || found.DefaultModel != "env-model" || found.Conversion == nil || found.Conversion.Source != "env-http-json" || len(found.Aliases) != 1 || found.Aliases[0] != "gateway-env-compatible" || len(found.Conversion.Routes) != 1 || found.Conversion.Routes[0].Query["trace"] != "{metadata.trace_id}" {
		t.Fatalf("env 등록 provider가 gateway discovery에 필요해요: %+v", found)
	}
}

func TestAccessLoggerWritesJSONL(t *testing.T) {
	var buf bytes.Buffer
	logger := accessLoggerForFlag(true, &buf)
	if logger == nil {
		t.Fatal("access logger가 필요해요")
	}
	logger(gateway.AccessLogEntry{RequestID: "req_1", Method: "GET", Path: "/api/v1/version", Status: 200, Bytes: 42, Duration: 1500 * time.Microsecond, Remote: "127.0.0.1:1", UserAgent: "test"})
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("JSONL access log가 필요해요: %s err=%v", buf.String(), err)
	}
	if got["type"] != "access" || got["request_id"] != "req_1" || got["path"] != "/api/v1/version" || got["duration_ms"].(float64) != 1.5 {
		t.Fatalf("access log payload가 이상해요: %+v", got)
	}
	if accessLoggerForFlag(false, &buf) != nil {
		t.Fatal("비활성화된 access logger는 nil이어야 해요")
	}
}

func TestEnvDuration(t *testing.T) {
	t.Setenv("KKODE_TEST_DURATION", "1500ms")
	if got := envDuration("KKODE_TEST_DURATION", time.Second); got != 1500*time.Millisecond {
		t.Fatalf("duration env가 이상해요: %s", got)
	}
	t.Setenv("KKODE_TEST_DURATION", "bad")
	if got := envDuration("KKODE_TEST_DURATION", time.Second); got != time.Second {
		t.Fatalf("잘못된 duration은 fallback이어야 해요: %s", got)
	}
}

func TestServeHTTPGracefulShutdown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	hookCalled := make(chan struct{}, 1)
	server := &http.Server{
		Addr:              addr,
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }),
		ReadHeaderTimeout: time.Second,
	}
	done := make(chan error, 1)
	go func() {
		done <- serveHTTP(ctx, server, nil, time.Second, func(ctx context.Context) error {
			hookCalled <- struct{}{}
			return nil
		})
	}()
	deadline := time.Now().Add(time.Second)
	ready := false
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr)
		if err == nil {
			_ = resp.Body.Close()
			ready = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		cancel()
		t.Fatal("test HTTP server가 시작되지 않았어요")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("graceful shutdown은 nil이어야 해요: %v", err)
		}
		select {
		case <-hookCalled:
		default:
			t.Fatal("shutdown hook이 호출돼야 해요")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("graceful shutdown이 끝나지 않았어요")
	}
}

func TestLoadProviderOptionsFromResources(t *testing.T) {
	store, err := session.OpenSQLite(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	skillDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# 리뷰 스킬\n\n코드를 리뷰해요.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mcp, err := store.SaveResource(ctx, session.Resource{Kind: session.ResourceMCPServer, Name: "filesystem", Enabled: true, Config: []byte(`{"kind":"stdio","command":"mcp-fs","args":["."],"tools":["read"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	skill, err := store.SaveResource(ctx, session.Resource{Kind: session.ResourceSkill, Name: "review", Enabled: true, Config: []byte(`{"path":"` + skillDir + `"}`)})
	if err != nil {
		t.Fatal(err)
	}
	subagent, err := store.SaveResource(ctx, session.Resource{Kind: session.ResourceSubagent, Name: "planner", Enabled: true, Config: []byte(`{"prompt":"계획해요","tools":["file_read"],"skills":["review"],"mcp_server_ids":["` + mcp.ID + `"],"mcp_servers":{"context7":{"kind":"http","url":"https://mcp.context7.com/mcp","headers":{"X-Test":"yes"}}}}`)})
	if err != nil {
		t.Fatal(err)
	}
	opts, err := loadProviderOptions(ctx, store, gateway.RunStartRequest{MCPServers: []string{mcp.ID}, Skills: []string{skill.ID}, Subagents: []string{subagent.ID}, ContextBlocks: []string{"임시 지시 token=ghp_123456789012345678901234567890123456"}})
	if err != nil {
		t.Fatal(err)
	}
	if opts.MCPServers["filesystem"].Command != "mcp-fs" || len(opts.SkillDirectories) != 1 || opts.SkillDirectories[0] != skillDir || len(opts.CustomAgents) != 1 || opts.CustomAgents[0].DisplayName != "planner" || len(opts.ContextBlocks) != 3 {
		t.Fatalf("provider options가 이상해요: %+v", opts)
	}
	if !strings.Contains(opts.ContextBlocks[0], "요청 추가 컨텍스트") || !strings.Contains(opts.ContextBlocks[0], "[REDACTED]") || strings.Contains(opts.ContextBlocks[0], "ghp_") || !strings.Contains(opts.ContextBlocks[1], "코드를 리뷰해요") || !strings.Contains(opts.ContextBlocks[2], "계획해요") {
		t.Fatalf("skill/subagent context block이 필요해요: %+v", opts.ContextBlocks)
	}
	agent := opts.CustomAgents[0]
	if agent.MCPServers["filesystem"].Command != "mcp-fs" || agent.MCPServers["context7"].Kind != llm.MCPHTTP || agent.MCPServers["context7"].URL != "https://mcp.context7.com/mcp" {
		t.Fatalf("subagent MCP 연결이 이상해요: %+v", agent.MCPServers)
	}
}

func TestLoadProviderOptionsRejectsDisabledResources(t *testing.T) {
	store, err := session.OpenSQLite(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	disabled, err := store.SaveResource(ctx, session.Resource{Kind: session.ResourceSkill, Name: "off", Enabled: false, Config: []byte(`{"path":"/tmp/skills/off"}`)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = loadProviderOptions(ctx, store, gateway.RunStartRequest{Skills: []string{disabled.ID}})
	if err == nil || !strings.Contains(err.Error(), "비활성화") {
		t.Fatalf("비활성 resource는 run에 연결하면 안 돼요: %v", err)
	}
}

func TestLoadProviderOptionsRejectsInvalidMCPManifests(t *testing.T) {
	store, err := session.OpenSQLite(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	invalid, err := store.SaveResource(ctx, session.Resource{Kind: session.ResourceMCPServer, Name: "broken", Enabled: true, Config: []byte(`{"kind":"http","timeout":-1}`)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = loadProviderOptions(ctx, store, gateway.RunStartRequest{MCPServers: []string{invalid.ID}})
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("invalid MCP resource는 run 조립에서 거부해야 해요: %v", err)
	}

	agent, err := store.SaveResource(ctx, session.Resource{Kind: session.ResourceSubagent, Name: "planner", Enabled: true, Config: []byte(`{"prompt":"계획해요","mcp_servers":{"context7":{"kind":"http"}}}`)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = loadProviderOptions(ctx, store, gateway.RunStartRequest{Subagents: []string{agent.ID}})
	if err == nil || !strings.Contains(err.Error(), "url") {
		t.Fatalf("invalid inline MCP resource는 subagent 조립에서 거부해야 해요: %v", err)
	}
}

func TestLoadProviderOptionsRejectsInvalidSkillManifest(t *testing.T) {
	store, err := session.OpenSQLite(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	missingPath, err := store.SaveResource(ctx, session.Resource{Kind: session.ResourceSkill, Name: "missing_path", Enabled: true, Config: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = loadProviderOptions(ctx, store, gateway.RunStartRequest{Skills: []string{missingPath.ID}})
	if err == nil || !strings.Contains(err.Error(), "path 또는 directory") {
		t.Fatalf("path 없는 skill은 run 전에 거부해야 해요: %v", err)
	}

	emptyDir := t.TempDir()
	emptySkill, err := store.SaveResource(ctx, session.Resource{Kind: session.ResourceSkill, Name: "empty", Enabled: true, Config: []byte(`{"path":"` + emptyDir + `"}`)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = loadProviderOptions(ctx, store, gateway.RunStartRequest{Skills: []string{emptySkill.ID}})
	if err == nil || !strings.Contains(err.Error(), "SKILL.md") {
		t.Fatalf("SKILL.md 없는 skill directory는 run 전에 거부해야 해요: %v", err)
	}
}

func TestSyncRunValidatorChecksSessionProviderAndResources(t *testing.T) {
	store, err := session.OpenSQLite(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	sess := session.NewSession(t.TempDir(), "openai", "gpt-5-mini", "kkode", session.AgentModeBuild)
	sess.ID = "sess_validate"
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	disabled, err := store.SaveResource(ctx, session.Resource{Kind: session.ResourceMCPServer, Name: "off", Enabled: false, Config: []byte(`{"kind":"stdio","command":"off"}`)})
	if err != nil {
		t.Fatal(err)
	}
	validator := syncRunValidator(store)
	if err := validator(ctx, gateway.RunStartRequest{SessionID: sess.ID, Prompt: "go"}); err != nil {
		t.Fatalf("정상 run preflight가 실패하면 안 돼요: %v", err)
	}
	if err := validator(ctx, gateway.RunStartRequest{SessionID: sess.ID, Prompt: "go", Provider: "missing-provider"}); err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("알 수 없는 provider는 queue 전에 거부해야 해요: %v", err)
	}
	if err := validator(ctx, gateway.RunStartRequest{SessionID: sess.ID, Prompt: "go", MCPServers: []string{disabled.ID}}); err == nil || !strings.Contains(err.Error(), "비활성화") {
		t.Fatalf("비활성 resource는 queue 전에 거부해야 해요: %v", err)
	}
	missingRoot := session.NewSession(t.TempDir()+"/missing", "openai", "gpt-5-mini", "kkode", session.AgentModeBuild)
	missingRoot.ID = "sess_missing_root"
	if err := store.CreateSession(ctx, missingRoot); err != nil {
		t.Fatal(err)
	}
	if err := validator(ctx, gateway.RunStartRequest{SessionID: missingRoot.ID, Prompt: "go"}); err == nil || !strings.Contains(err.Error(), "workspace preflight failed") {
		t.Fatalf("없는 workspace root는 queue 전에 거부해야 해요: %v", err)
	}
}

func TestSyncRunPreviewerShowsEffectiveAssembly(t *testing.T) {
	t.Setenv("KKODE_DEFAULT_MCP", "off")
	store, err := session.OpenSQLite(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	sess := session.NewSession(t.TempDir(), "openai", "gpt-5-mini", "agent", session.AgentModeBuild)
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	skillDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# 리뷰 스킬\n\n코드를 리뷰해요.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mcp, err := store.SaveResource(ctx, session.Resource{Kind: session.ResourceMCPServer, Name: "context7", Enabled: true, Config: []byte(`{"kind":"http","url":"https://mcp.context7.com/mcp","tools":["*"],"headers":{"CONTEXT7_API_KEY":"secret"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	skill, err := store.SaveResource(ctx, session.Resource{Kind: session.ResourceSkill, Name: "review", Enabled: true, Config: []byte(`{"path":"` + skillDir + `"}`)})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := syncRunPreviewer(store, runOptions{NoWeb: true})(ctx, gateway.RunStartRequest{SessionID: sess.ID, Prompt: "preview", Metadata: map[string]string{"trace_id": "trace_preview"}, MCPServers: []string{mcp.ID}, Skills: []string{skill.ID}, EnabledTools: []string{"file_read", "shell_run", "lsp_symbols", "mcp_call"}, DisabledTools: []string{"shell_run"}, ContextBlocks: []string{"Discord thread summary예요"}})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Provider != "openai" || preview.Model != "gpt-5-mini" || len(preview.MCPServers) != 1 || preview.MCPServers[0].Name != "context7" {
		t.Fatalf("preview 기본 정보가 이상해요: %+v", preview)
	}
	if len(preview.BaseRequestTools) != 1 || preview.BaseRequestTools[0] != "mcp" {
		t.Fatalf("OpenAI-compatible MCP tool preview가 필요해요: %+v", preview.BaseRequestTools)
	}
	if len(preview.LocalTools) != 3 || preview.LocalTools[0] != "file_read" || preview.LocalTools[1] != "lsp_symbols" || preview.LocalTools[2] != "mcp_call" {
		t.Fatalf("run preview는 필터링된 local tool 목록을 보여줘야 해요: %+v", preview.LocalTools)
	}
	if len(preview.ContextBlocks) != 2 || !strings.Contains(preview.ContextBlocks[0], "Discord thread summary") || !strings.Contains(preview.ContextBlocks[1], "코드를 리뷰해요") || preview.ContextTruncated {
		t.Fatalf("run preview는 선택된 prompt context를 직접 보여줘야 해요: blocks=%q truncated=%v", preview.ContextBlocks, preview.ContextTruncated)
	}
	if preview.ProviderRequest == nil || preview.ProviderRequest.Provider != "openai" || preview.ProviderRequest.Operation != "responses.create" || preview.ProviderRequest.Route == nil || preview.ProviderRequest.Route.ResolvedPath != "/responses" || preview.ProviderRequest.Metadata["trace_id"] != "trace_preview" || !strings.Contains(preview.ProviderRequest.BodyJSON, "preview") || !strings.Contains(preview.ProviderRequest.BodyJSON, "trace_preview") || !strings.Contains(preview.ProviderRequest.BodyJSON, "Discord thread summary") || !strings.Contains(preview.ProviderRequest.BodyJSON, "코드를 리뷰해요") || !strings.Contains(preview.ProviderRequest.BodyJSON, "file_read") || !strings.Contains(preview.ProviderRequest.BodyJSON, "lsp_symbols") || !strings.Contains(preview.ProviderRequest.BodyJSON, "mcp_call") || strings.Contains(preview.ProviderRequest.BodyJSON, "shell_run") {
		t.Fatalf("provider request 변환 preview가 필요해요: %+v", preview.ProviderRequest)
	}
	streamPreview, err := syncRunPreviewer(store, runOptions{NoWeb: true})(ctx, gateway.RunStartRequest{SessionID: sess.ID, Prompt: "preview", MCPServers: []string{mcp.ID}, PreviewStream: true})
	if err != nil {
		t.Fatal(err)
	}
	if streamPreview.ProviderRequest == nil || !streamPreview.ProviderRequest.Stream {
		t.Fatalf("stream provider request 변환 preview가 필요해요: %+v", streamPreview.ProviderRequest)
	}
	if strings.Contains(preview.ProviderRequest.BodyJSON, "secret") || !strings.Contains(preview.ProviderRequest.BodyJSON, "[REDACTED]") {
		t.Fatalf("provider request preview도 secret을 숨겨야 해요: %s", preview.ProviderRequest.BodyJSON)
	}
	headers := preview.MCPServers[0].Config["headers"].(map[string]any)
	if headers["CONTEXT7_API_KEY"] != "[REDACTED]" {
		t.Fatalf("run preview는 MCP secret header를 숨겨야 해요: %+v", preview.MCPServers[0].Config)
	}
}

func TestSyncRunPreviewerExposesDefaultMCPToLocalTools(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("KKODE_SERENA_COMMAND", "")
	t.Setenv("KKODE_CONTEXT7_URL", "https://mcp.context7.com/mcp")
	store, err := session.OpenSQLite(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	sess := session.NewSession(t.TempDir(), "openai", "gpt-5-mini", "agent", session.AgentModeBuild)
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	preview, err := syncRunPreviewer(store, runOptions{NoWeb: true})(ctx, gateway.RunStartRequest{SessionID: sess.ID, Prompt: "preview default MCP", EnabledTools: []string{"mcp_call"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(preview.BaseRequestTools, "mcp") {
		t.Fatalf("default Context7 should be in provider base request tools: %+v", preview.BaseRequestTools)
	}
	if len(preview.LocalTools) != 1 || preview.LocalTools[0] != "mcp_call" {
		t.Fatalf("default MCP should expose mcp_call on the local tool surface: %+v", preview.LocalTools)
	}
	var sawContext7 bool
	for _, resource := range preview.DefaultMCPServers {
		if resource.Name == "context7" {
			sawContext7 = true
		}
	}
	if !sawContext7 {
		t.Fatalf("default MCP discovery should include Context7: %+v", preview.DefaultMCPServers)
	}
	if preview.ProviderRequest == nil || !strings.Contains(preview.ProviderRequest.BodyJSON, "mcp_call") {
		t.Fatalf("provider preview should include the default-backed local mcp_call tool: %+v", preview.ProviderRequest)
	}
}

func TestPreviewContextBlocksRedactsAndTruncatesUTF8(t *testing.T) {
	if runPreviewBytes(0) != 64<<10 || runPreviewBytes(123) != 123 || runPreviewBytes(gateway.MaxRunPreviewBytes+1) != gateway.MaxRunPreviewBytes {
		t.Fatal("run preview byte 예산 기본값/override가 이상해요")
	}
	requestBlocks := requestContextBlocks([]string{"   ", "token=ghp_123456789012345678901234567890123456\n요청 컨텍스트예요"})
	if len(requestBlocks) != 1 || strings.Contains(requestBlocks[0], "ghp_") || !strings.Contains(requestBlocks[0], "[REDACTED]") || !strings.Contains(requestBlocks[0], "요청 추가 컨텍스트") {
		t.Fatalf("요청 context block 정규화가 이상해요: %#v", requestBlocks)
	}
	blocks, truncated := previewContextBlocks([]string{
		"token=ghp_123456789012345678901234567890123456\n가나다라마",
		"두 번째 블록이에요",
	}, 20)
	if !truncated {
		t.Fatal("context preview는 byte 예산을 넘으면 잘림을 알려줘야 해요")
	}
	if len(blocks) != 1 || strings.Contains(blocks[0], "ghp_") || !strings.Contains(blocks[0], "[REDACTED]") {
		t.Fatalf("context preview는 secret을 숨기고 첫 블록을 반환해야 해요: %#v", blocks)
	}
	if !utf8.ValidString(blocks[0]) {
		t.Fatalf("context preview는 UTF-8을 깨면 안 돼요: %q", blocks[0])
	}
}

func TestSyncRunStarterPassesRunMetadataToProviderRequest(t *testing.T) {
	requests := make(chan llm.Request, 1)
	unregister, err := app.RegisterProvider(app.ProviderRegistration{
		Spec: app.ProviderSpec{
			Name:         "capture-meta",
			DefaultModel: "capture-model",
			Models:       []string{"capture-model"},
			Local:        true,
			Conversion: app.ProviderConversionSpec{
				Operations: []string{"capture.generate"},
			},
		},
		Conversion: func(spec app.ProviderSpec) app.ProviderConversionSet {
			return app.ProviderConversionSet{RequestConverter: llm.RequestConverterFunc(func(ctx context.Context, req llm.Request, opts llm.ConvertOptions) (llm.ProviderRequest, error) {
				return llm.ProviderRequest{Operation: "capture.generate", Model: req.Model, Metadata: llm.CloneMetadata(req.Metadata)}, nil
			})}
		},
		Factory: func(root string, opts app.ProviderOptions) (app.ProviderHandle, error) {
			return app.ProviderHandle{Provider: captureMetadataProvider{requests: requests}, BaseRequest: llm.Request{Metadata: map[string]string{"provider_default": "yes"}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()

	store, err := session.OpenSQLite(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sess := session.NewSession(t.TempDir(), "capture-meta", "capture-model", "agent", session.AgentModeBuild)
	if err := store.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	run, err := syncRunStarter(store, runOptions{NoWeb: true})(context.Background(), gateway.RunStartRequest{SessionID: sess.ID, Prompt: "metadata run", Metadata: map[string]string{"trace_id": "trace_run"}})
	if err != nil {
		t.Fatal(err)
	}
	if run.Metadata["trace_id"] != "trace_run" {
		t.Fatalf("run metadata도 보존해야 해요: %+v", run.Metadata)
	}
	select {
	case got := <-requests:
		if got.Metadata["provider_default"] != "yes" || got.Metadata["trace_id"] != "trace_run" {
			t.Fatalf("run metadata가 provider request까지 전달돼야 해요: %+v", got.Metadata)
		}
	case <-time.After(time.Second):
		t.Fatal("provider 호출이 필요해요")
	}
}

type captureMetadataProvider struct {
	requests chan<- llm.Request
}

func (p captureMetadataProvider) Name() string { return "capture-meta" }

func (p captureMetadataProvider) Capabilities() llm.Capabilities {
	return llm.Capabilities{Tools: true}
}

func (p captureMetadataProvider) Generate(ctx context.Context, req llm.Request) (*llm.Response, error) {
	select {
	case p.requests <- req:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return llm.TextResponse("capture-meta", req.Model, "ok"), nil
}

func TestSyncProviderTesterPreviewsWithoutSession(t *testing.T) {
	tester := syncProviderTester()
	resp, err := tester(context.Background(), "openai-compatible", gateway.ProviderTestRequest{Model: " gpt-5-mini ", Prompt: "provider preview", Metadata: map[string]string{"trace_id": "trace_provider"}})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Provider != "openai" || resp.Model != "gpt-5-mini" || resp.ProviderRequest == nil || resp.ProviderRequest.Operation != "responses.create" || resp.ProviderRequest.Route == nil || resp.ProviderRequest.Route.ResolvedPath != "/responses" || resp.ProviderRequest.Metadata["trace_id"] != "trace_provider" || !strings.Contains(resp.ProviderRequest.BodyJSON, "provider preview") || !strings.Contains(resp.ProviderRequest.BodyJSON, "trace_provider") {
		t.Fatalf("provider tester preview가 이상해요: %+v", resp)
	}
	if resp.Live {
		t.Fatal("live=false 기본 provider test는 외부 API를 호출하면 안 돼요")
	}
}

func TestSyncProviderTesterReportsMissingAuthBeforeLive(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	resp, err := syncProviderTester()(context.Background(), "openai-compatible", gateway.ProviderTestRequest{Model: "gpt-5-mini", Live: true})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.Code != "provider_auth_missing" || resp.AuthStatus != "missing" || !strings.Contains(resp.Message, "OPENAI_API_KEY") {
		t.Fatalf("provider live smoke는 인증 누락을 구조화해 반환해야 해요: %+v", resp)
	}
	if resp.ProviderRequest == nil {
		t.Fatalf("인증 누락이어도 변환 preview는 보여줘야 해요: %+v", resp)
	}
}

func TestSyncProviderTesterAppliesLiveTimeout(t *testing.T) {
	unregister, err := app.RegisterProvider(app.ProviderRegistration{
		Spec: app.ProviderSpec{
			Name:         "slow-provider-test",
			DefaultModel: "slow-model",
			Models:       []string{"slow-model"},
			Local:        true,
			Conversion: app.ProviderConversionSpec{
				Operations: []string{"slow.generate"},
			},
		},
		Conversion: func(spec app.ProviderSpec) app.ProviderConversionSet {
			return app.ProviderConversionSet{
				RequestConverter: llm.RequestConverterFunc(func(ctx context.Context, req llm.Request, opts llm.ConvertOptions) (llm.ProviderRequest, error) {
					return llm.ProviderRequest{Operation: "slow.generate", Model: req.Model, Body: map[string]any{"input": req.Messages[0].Content}}, nil
				}),
				Options: llm.ConvertOptions{Operation: "slow.generate"},
			}
		},
		Factory: func(root string, opts app.ProviderOptions) (app.ProviderHandle, error) {
			return app.ProviderHandle{Provider: slowProviderTest{}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()

	resp, err := syncProviderTester()(context.Background(), "slow-provider-test", gateway.ProviderTestRequest{Live: true, TimeoutMS: 5})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.Code != "provider_live_failed" || !resp.Live || !strings.Contains(resp.Message, "context deadline exceeded") {
		t.Fatalf("provider live smoke timeout이 적용돼야 해요: %+v", resp)
	}
	if _, err := providerTestTimeout(-1); err == nil {
		t.Fatal("음수 provider test timeout은 거부해야 해요")
	}
}

func TestSyncProviderTesterTruncatesLiveResult(t *testing.T) {
	unregister, err := app.RegisterProvider(app.ProviderRegistration{
		Spec: app.ProviderSpec{
			Name:         "large-provider-test",
			DefaultModel: "large-model",
			Models:       []string{"large-model"},
			Local:        true,
			Conversion: app.ProviderConversionSpec{
				Operations: []string{"large.generate"},
			},
		},
		Conversion: func(spec app.ProviderSpec) app.ProviderConversionSet {
			return app.ProviderConversionSet{
				RequestConverter: llm.RequestConverterFunc(func(ctx context.Context, req llm.Request, opts llm.ConvertOptions) (llm.ProviderRequest, error) {
					return llm.ProviderRequest{Operation: "large.generate", Model: req.Model, Body: map[string]any{"input": req.Messages[0].Content}}, nil
				}),
				Options: llm.ConvertOptions{Operation: "large.generate"},
			}
		},
		Factory: func(root string, opts app.ProviderOptions) (app.ProviderHandle, error) {
			return app.ProviderHandle{Provider: largeProviderTest{}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()

	resp, err := syncProviderTester()(context.Background(), "large-provider-test", gateway.ProviderTestRequest{Live: true, MaxResultBytes: 18})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Result == nil || !resp.Result.TextTruncated || resp.Result.TextBytes <= len(resp.Result.Text) || !utf8.ValidString(resp.Result.Text) {
		t.Fatalf("provider live smoke 결과 제한이 필요해요: %+v", resp)
	}
	if strings.Contains(resp.Result.Text, "ghp_") || !strings.Contains(resp.Result.Text, "[REDACTED]") {
		t.Fatalf("provider live smoke 결과는 secret을 먼저 숨겨야 해요: %+v", resp.Result)
	}

	_, err = syncProviderTester()(context.Background(), "large-provider-test", gateway.ProviderTestRequest{Live: true, MaxResultBytes: -1})
	if err == nil || !strings.Contains(err.Error(), "max_result_bytes") {
		t.Fatalf("negative max_result_bytes는 거부해야 해요: %v", err)
	}
	_, err = syncProviderTester()(context.Background(), "large-provider-test", gateway.ProviderTestRequest{MaxPreviewBytes: gateway.MaxProviderTestPreviewBytes + 1})
	if err == nil || !strings.Contains(err.Error(), "max_preview_bytes") {
		t.Fatalf("large max_preview_bytes는 거부해야 해요: %v", err)
	}
	_, err = syncProviderTester()(context.Background(), "large-provider-test", gateway.ProviderTestRequest{MaxOutputTokens: gateway.MaxProviderTestOutputTokens + 1})
	if err == nil || !strings.Contains(err.Error(), "max_output_tokens") {
		t.Fatalf("large max_output_tokens는 거부해야 해요: %v", err)
	}
	_, err = syncProviderTester()(context.Background(), "large-provider-test", gateway.ProviderTestRequest{MaxResultBytes: gateway.MaxProviderTestResultBytes + 1})
	if err == nil || !strings.Contains(err.Error(), "max_result_bytes") {
		t.Fatalf("large max_result_bytes는 거부해야 해요: %v", err)
	}
	_, err = syncProviderTester()(context.Background(), "large-provider-test", gateway.ProviderTestRequest{TimeoutMS: gateway.MaxProviderTestTimeoutMS + 1})
	if err == nil || !strings.Contains(err.Error(), "timeout_ms") {
		t.Fatalf("large timeout_ms는 거부해야 해요: %v", err)
	}
}

func TestSyncProviderTesterReturnsPreviewErrorCode(t *testing.T) {
	unregister, err := app.RegisterHTTPJSONProvider(app.HTTPJSONProviderRegistration{
		Name:         "provider-preview-error-test",
		Profile:      "openai-compatible",
		BaseURL:      "https://preview-error.example.test/v1",
		DefaultModel: "gpt-5-mini",
		Models:       []string{"gpt-5-mini"},
		Routes: []app.ProviderRouteSpec{{
			Operation: "responses.create",
			Method:    "POST",
			Path:      "/deployments/{metadata.deployment}/responses",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()

	resp, err := syncProviderTester()(context.Background(), "provider-preview-error-test", gateway.ProviderTestRequest{Model: "gpt-5-mini"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.Code != "provider_preview_failed" || !strings.Contains(resp.Message, "metadata.deployment") {
		t.Fatalf("provider preview 오류 코드를 반환해야 해요: %+v", resp)
	}
}

type slowProviderTest struct{}

func (slowProviderTest) Name() string { return "slow-provider-test" }

func (slowProviderTest) Capabilities() llm.Capabilities {
	return llm.Capabilities{}
}

func (slowProviderTest) Generate(ctx context.Context, req llm.Request) (*llm.Response, error) {
	select {
	case <-time.After(time.Second):
		return llm.TextResponse("slow-provider-test", req.Model, "late"), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type largeProviderTest struct{}

func (largeProviderTest) Name() string { return "large-provider-test" }

func (largeProviderTest) Capabilities() llm.Capabilities {
	return llm.Capabilities{}
}

func (largeProviderTest) Generate(ctx context.Context, req llm.Request) (*llm.Response, error) {
	return llm.TextResponse("large-provider-test", req.Model, "token=ghp_123456789012345678901234567890123456 가나다라마바"), nil
}

func TestDefaultMCPDTOsAreStableAndRedacted(t *testing.T) {
	t.Setenv("KKODE_SERENA_COMMAND", "uvx")
	t.Setenv("CONTEXT7_API_KEY", "secret")
	resources := defaultMCPDTOs()
	if len(resources) < 2 {
		t.Fatalf("Serena와 Context7 기본 MCP가 필요해요: %+v", resources)
	}
	if resources[0].Name != "context7" {
		t.Fatalf("default MCP discovery 순서는 이름순이어야 해요: %+v", resources)
	}
	headers := resources[0].Config["headers"].(map[string]string)
	if headers["CONTEXT7_API_KEY"] != "[REDACTED]" {
		t.Fatalf("default MCP DTO는 secret을 숨겨야 해요: %+v", resources[0].Config)
	}
}

func TestDefaultMCPDiagnosticChecksExplainMissingSerena(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("KKODE_SERENA_COMMAND", "")
	checks := defaultMCPDiagnosticChecks()
	var sawSerena bool
	for _, check := range checks {
		if check.Name == "default_mcp.serena" {
			sawSerena = true
			if check.Status != "missing" || !strings.Contains(check.Message, "uvx") {
				t.Fatalf("Serena diagnostics가 이상해요: %+v", check)
			}
		}
	}
	if !sawSerena {
		t.Fatalf("Serena default MCP check가 필요해요: %+v", checks)
	}
}
