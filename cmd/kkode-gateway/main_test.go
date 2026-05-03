package main

import (
	"bytes"
	"context"
	"encoding/json"
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
