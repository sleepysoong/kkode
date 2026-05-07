package httpjson

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/providers/internal/httptransport"
)

func TestCallerPostsMappedOperationWithRetryAndHeaders(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer key" || r.Header.Get("X-Global") != "yes" || r.Header.Get("X-Route") != "route" || r.Header.Get("X-Req") != "request" {
			t.Fatalf("요청 header/method가 이상해요: method=%s headers=%+v", r.Method, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"model":"gpt"}` {
			t.Fatalf("body = %s", string(body))
		}
		if attempts == 1 {
			http.Error(w, "retry", http.StatusTooManyRequests)
			return
		}
		w.Header().Set("X-Provider", "ok")
		_, _ = w.Write([]byte(`{"id":"resp_1","output_text":"ok"}`))
	}))
	defer server.Close()

	caller := New(Config{
		ProviderName:     "custom-api",
		BaseURL:          server.URL + "/v1",
		APIKey:           "key",
		HTTPClient:       server.Client(),
		Headers:          map[string]string{"X-Global": "yes"},
		DefaultOperation: "responses.create",
		Retry:            RetryConfig{MaxRetries: 1, MinBackoff: time.Nanosecond, MaxBackoff: time.Nanosecond},
		Routes: map[string]Route{
			"responses.create": {Path: "/responses", Headers: map[string]string{"X-Route": "route"}},
		},
	})
	result, err := caller.CallProvider(context.Background(), llm.ProviderRequest{Model: "gpt", Body: map[string]any{"model": "gpt"}, Headers: map[string]string{"X-Req": "request"}})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || result.Provider != "custom-api" || result.Model != "gpt" || string(result.Body) != `{"id":"resp_1","output_text":"ok"}` || len(result.Headers["X-Provider"]) != 1 || result.Headers["X-Provider"][0] != "ok" {
		t.Fatalf("provider result가 이상해요: attempts=%d result=%+v", attempts, result)
	}
}

func TestCallerReportsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad", http.StatusBadGateway)
	}))
	defer server.Close()
	caller := New(Config{ProviderName: "custom-api", BaseURL: server.URL, HTTPClient: server.Client(), Retry: RetryConfig{MaxRetries: -1}, Routes: map[string]Route{"custom.call": {Path: "/call"}}})
	_, err := caller.CallProvider(context.Background(), llm.ProviderRequest{Operation: "custom.call", Body: map[string]any{"ok": true}})
	var httpErr *httptransport.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadGateway || httpErr.Body != "bad\n" {
		t.Fatalf("공통 HTTP 오류가 필요해요: %#v", err)
	}
}

func TestCallerRequiresMappedOperation(t *testing.T) {
	caller := New(Config{BaseURL: "http://example.test", Routes: map[string]Route{"known": {Path: "/known"}}})
	if _, err := caller.CallProvider(context.Background(), llm.ProviderRequest{Operation: "missing"}); err == nil {
		t.Fatal("알 수 없는 operation은 실패해야 해요")
	}
	if _, err := caller.CallProvider(context.Background(), llm.ProviderRequest{}); err == nil {
		t.Fatal("operation 기본값도 route도 없으면 실패해야 해요")
	}
}
