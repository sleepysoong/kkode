package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sleepysoong/kkode/gateway"
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
	mcp, err := store.SaveResource(ctx, session.Resource{Kind: session.ResourceMCPServer, Name: "filesystem", Enabled: true, Config: []byte(`{"kind":"stdio","command":"mcp-fs","args":["."],"tools":["read"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	skill, err := store.SaveResource(ctx, session.Resource{Kind: session.ResourceSkill, Name: "review", Enabled: true, Config: []byte(`{"path":"/tmp/skills/review"}`)})
	if err != nil {
		t.Fatal(err)
	}
	subagent, err := store.SaveResource(ctx, session.Resource{Kind: session.ResourceSubagent, Name: "planner", Enabled: true, Config: []byte(`{"prompt":"계획해요","tools":["file_read"],"skills":["review"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	opts, err := loadProviderOptions(ctx, store, gateway.RunStartRequest{MCPServers: []string{mcp.ID}, Skills: []string{skill.ID}, Subagents: []string{subagent.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if opts.MCPServers["filesystem"].Command != "mcp-fs" || len(opts.SkillDirectories) != 1 || opts.SkillDirectories[0] != "/tmp/skills/review" || len(opts.CustomAgents) != 1 || opts.CustomAgents[0].DisplayName != "planner" {
		t.Fatalf("provider options가 이상해요: %+v", opts)
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
	mcp, err := store.SaveResource(ctx, session.Resource{Kind: session.ResourceMCPServer, Name: "context7", Enabled: true, Config: []byte(`{"kind":"http","url":"https://mcp.context7.com/mcp","tools":["*"],"headers":{"CONTEXT7_API_KEY":"secret"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := syncRunPreviewer(store)(ctx, gateway.RunStartRequest{SessionID: sess.ID, Prompt: "preview", MCPServers: []string{mcp.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Provider != "openai" || preview.Model != "gpt-5-mini" || len(preview.MCPServers) != 1 || preview.MCPServers[0].Name != "context7" {
		t.Fatalf("preview 기본 정보가 이상해요: %+v", preview)
	}
	if len(preview.BaseRequestTools) != 1 || preview.BaseRequestTools[0] != "mcp" {
		t.Fatalf("OpenAI-compatible MCP tool preview가 필요해요: %+v", preview.BaseRequestTools)
	}
	headers := preview.MCPServers[0].Config["headers"].(map[string]any)
	if headers["CONTEXT7_API_KEY"] != "[REDACTED]" {
		t.Fatalf("run preview는 MCP secret header를 숨겨야 해요: %+v", preview.MCPServers[0].Config)
	}
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
