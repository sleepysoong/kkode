package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
