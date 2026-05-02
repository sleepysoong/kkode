package httptransport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewJSONRequestAppliesSharedHeaders(t *testing.T) {
	req, payload, err := NewJSONRequest(context.Background(), http.MethodPost, "http://example.test/v1/responses", "sk-test", map[string]string{"X-Test": "yes"}, map[string]any{"model": "gpt"}, "text/event-stream")
	if err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "Bearer sk-test" || req.Header.Get("X-Test") != "yes" || req.Header.Get("Accept") != "text/event-stream" || req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("request header가 이상해요: %+v", req.Header)
	}
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "gpt" {
		t.Fatalf("payload가 이상해요: %s", string(payload))
	}
}

func TestDoJSONRawReturnsErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" || r.Header.Get("X-Test") != "yes" {
			t.Fatalf("headers = %+v", r.Header)
		}
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer server.Close()
	_, err := DoJSONRaw(context.Background(), server.Client(), http.MethodPost, server.URL, "k", map[string]string{"X-Test": "yes"}, map[string]any{"ok": true}, "probe")
	if err == nil || err.Error() != "probe returned 502 Bad Gateway: nope\n" {
		t.Fatalf("error body가 필요해요: %v", err)
	}
}
