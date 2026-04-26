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
