package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sleepysoong/kkode/llm"
)

func toolCall(name string, args string) llm.ToolCall {
	return llm.ToolCall{Name: name, CallID: "call", Arguments: []byte(args)}
}

func TestWebFetchTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello web fetch"))
	}))
	defer server.Close()
	_, handlers := WebTools(WebConfig{MaxBytes: 1024})
	res, err := handlers.Execute(context.Background(), toolCall("web_fetch", `{"url":"`+server.URL+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out WebFetchResult
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatal(err)
	}
	if out.StatusCode != 200 || out.Body != "hello web fetch" || out.ContentType != "text/plain" {
		t.Fatalf("out=%#v", out)
	}
}

func TestWebFetchRejectsUnsupportedScheme(t *testing.T) {
	_, err := Fetch(context.Background(), WebConfig{}, "file:///etc/passwd", 0, 0)
	if err == nil {
		t.Fatal("expected unsupported scheme error")
	}
}

func TestWebFetchRejectsNegativeBudgets(t *testing.T) {
	if _, err := Fetch(context.Background(), WebConfig{}, "https://example.test", -1, 0); err == nil || !strings.Contains(err.Error(), "max_bytes") {
		t.Fatalf("negative max_bytes는 거부해야 해요: %v", err)
	}
	if _, err := Fetch(context.Background(), WebConfig{}, "https://example.test", 0, -1); err == nil || !strings.Contains(err.Error(), "timeout_ms") {
		t.Fatalf("negative timeout_ms는 거부해야 해요: %v", err)
	}
}

func TestWebFetchRejectsMaxBytesAboveConfiguredEnvelope(t *testing.T) {
	if _, err := Fetch(context.Background(), WebConfig{MaxBytes: 4}, "https://example.test", 5, 0); err == nil || !strings.Contains(err.Error(), "max_bytes") {
		t.Fatalf("configured max_bytes 초과는 거부해야 해요: %v", err)
	}
	_, handlers := WebTools(WebConfig{MaxBytes: 4})
	if _, err := handlers.Execute(context.Background(), toolCall("web_fetch", `{"url":"https://example.test","max_bytes":5}`)); err == nil || !strings.Contains(err.Error(), "max_bytes") {
		t.Fatalf("tool argument max_bytes 초과는 거부해야 해요: %v", err)
	}
}

func TestWebFetchTruncatesAtUTF8Boundary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("가나다"))
	}))
	defer server.Close()
	out, err := Fetch(context.Background(), WebConfig{MaxBytes: 4}, server.URL, 4, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Truncated || out.Body != "가" || !utf8.ValidString(out.Body) {
		t.Fatalf("web_fetch body should be UTF-8 bounded: body=%q truncated=%v valid=%v", out.Body, out.Truncated, utf8.ValidString(out.Body))
	}
}
