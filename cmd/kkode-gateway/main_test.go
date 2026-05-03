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
	mcp, err := store.SaveResource(ctx, session.Resource{Kind: session.ResourceMCPServer, Name: "context7", Enabled: true, Config: []byte(`{"kind":"http","url":"https://mcp.context7.com/mcp","tools":["*"]}`)})
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
}
