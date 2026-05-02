package main

import (
	"context"
	"strings"
	"testing"

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
