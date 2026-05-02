package omniroute

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sleepysoong/kkode/llm"
)

func TestGenerateUsesResponsesEndpointAndOmniRouteHeaders(t *testing.T) {
	var gotPath, gotSession, gotCache, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSession = r.Header.Get("X-Session-Id")
		gotCache = r.Header.Get("X-OmniRoute-No-Cache")
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"id":"resp_1","model":"auto","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer server.Close()
	client := New(Config{BaseURL: server.URL + "/v1", APIKey: "k", SessionID: "s1", NoCache: true})
	resp, err := client.Generate(context.Background(), llm.Request{Model: "auto", Messages: []llm.Message{llm.UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/responses" || gotSession != "s1" || gotCache != "true" || gotAuth != "Bearer k" {
		t.Fatalf("path=%s session=%s cache=%s auth=%s", gotPath, gotSession, gotCache, gotAuth)
	}
	if resp.Provider != "omniroute" || resp.Text != "ok" {
		t.Fatalf("resp=%#v", resp)
	}
}

func TestStreamLabelsEventsAsOmniRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"))
	}))
	defer server.Close()
	client := New(Config{BaseURL: server.URL + "/v1"})
	stream, err := client.Stream(context.Background(), llm.Request{Model: "auto", Messages: []llm.Message{llm.UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	ev, err := stream.Recv()
	if err != nil || ev.Provider != "omniroute" || ev.Delta != "hi" {
		t.Fatalf("ev=%#v err=%v", ev, err)
	}
}

func TestListModelsHealthAndA2A(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(ModelList{Object: "list", Data: []Model{{ID: "combo/free", Provider: "combo", Type: "chat"}}})
		case "/api/monitoring/health":
			_, _ = w.Write([]byte(`{"status":"healthy","catalogCount":10,"configuredCount":2,"activeCount":1,"monitoredCount":1}`))
		case "/a2a":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"task":{"id":"t1","state":"completed"},"artifacts":[{"type":"text","content":"hello"}],"metadata":{"routing_explanation":"auto"}}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := New(Config{BaseURL: server.URL + "/v1"})
	models, err := client.ListModels(context.Background())
	if err != nil || len(models.Data) != 1 || models.Data[0].ID != "combo/free" {
		t.Fatalf("models=%#v err=%v", models, err)
	}
	health, err := client.Health(context.Background())
	if err != nil || health.Status != "healthy" || health.CatalogCount != 10 {
		t.Fatalf("health=%#v err=%v", health, err)
	}
	a2a, err := client.A2ASend(context.Background(), A2ARequest{Messages: []llm.Message{llm.UserText("hi")}})
	if err != nil || a2a.Text != "hello" || a2a.TaskID != "t1" {
		t.Fatalf("a2a=%#v err=%v", a2a, err)
	}
}

func TestDeriveAdminBaseURL(t *testing.T) {
	if got := deriveAdminBaseURL("http://localhost:20128/v1"); got != "http://localhost:20128" {
		t.Fatalf("got %s", got)
	}
}

func TestOpenAPIConstructorAndManagementHelpers(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/api/v1/models":
			_ = json.NewEncoder(w).Encode(ModelList{Object: "list", Data: []Model{{ID: "auto"}}})
		case "/api/settings/thinking-budget":
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`{"mode":"auto","customBudget":1024,"effortLevel":"medium"}`))
			} else {
				_, _ = w.Write([]byte(`{"mode":"manual","customBudget":2048,"effortLevel":"high"}`))
			}
		case "/api/translator/translate":
			_, _ = w.Write([]byte(`{"targetFormat":"openai","body":{"model":"auto"}}`))
		case "/api/fallback/chains":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/api/cache/stats", "/api/rate-limits", "/api/sessions":
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := NewFromOpenAPIServer(server.URL, Config{})
	models, err := client.ListModels(context.Background())
	if err != nil || models.Data[0].ID != "auto" {
		t.Fatalf("models=%#v err=%v", models, err)
	}
	budget, err := client.GetThinkingBudget(context.Background())
	if err != nil || budget.EffortLevel != "medium" || budget.CustomBudget != 1024 {
		t.Fatalf("budget=%#v err=%v", budget, err)
	}
	budget, err = client.UpdateThinkingBudget(context.Background(), ThinkingBudget{Mode: "manual", CustomBudget: 2048, EffortLevel: "high"})
	if err != nil || budget.EffortLevel != "high" {
		t.Fatalf("updated budget=%#v err=%v", budget, err)
	}
	translated, err := client.Translate(context.Background(), TranslateRequest{SourceFormat: "anthropic", TargetFormat: "openai", Body: map[string]any{"model": "auto"}})
	if err != nil || translated["targetFormat"] != "openai" {
		t.Fatalf("translated=%#v err=%v", translated, err)
	}
	if _, err := client.ListFallbackChains(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateFallbackChain(context.Background(), CreateFallbackChainRequest{Model: "auto", Chain: []FallbackChain{{Provider: "openai", Priority: 1, Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.DeleteFallbackChain(context.Background(), "auto"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CacheStats(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RateLimits(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Sessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if paths[0] != "/api/v1/models" {
		t.Fatalf("constructor used wrong model path: %v", paths)
	}
}
