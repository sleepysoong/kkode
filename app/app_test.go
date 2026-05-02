package app

import (
	"context"
	"testing"

	"github.com/sleepysoong/kkode/llm"
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

func TestProviderSpecsAreDefensiveCopies(t *testing.T) {
	specs := ProviderSpecs()
	if len(specs) == 0 {
		t.Fatal("provider registry가 비면 안 돼요")
	}
	specs[0].Aliases = append(specs[0].Aliases, "mutated")
	specs[0].Models = append(specs[0].Models, "mutated-model")
	specs[0].Capabilities["tools"] = false
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
	if _, ok := handlers["workspace_read_file"]; ok {
		t.Fatal("이전 workspace_* tool은 agent에 자동 노출하지 않아요")
	}
	for _, tool := range req.Tools {
		if tool.Name == "workspace_read_file" {
			t.Fatal("이전 workspace_* tool 정의가 request에 들어가면 안 돼요")
		}
	}
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
