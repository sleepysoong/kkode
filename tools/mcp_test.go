package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sleepysoong/kkode/llm"
)

func TestMCPToolsCallsConfiguredHTTPServer(t *testing.T) {
	var sawSession atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		method, _ := payload["method"].(string)
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess_mcp")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26"}}`))
		case "notifications/initialized":
			if r.Header.Get("Mcp-Session-Id") == "sess_mcp" {
				sawSession.Store(true)
			}
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			if r.Header.Get("Mcp-Session-Id") != "sess_mcp" {
				t.Fatalf("tools/call must reuse MCP session header, got %q", r.Header.Get("Mcp-Session-Id"))
			}
			params, _ := payload["params"].(map[string]any)
			args, _ := params["arguments"].(map[string]any)
			if params["name"] != "echo" || args["text"] != "hello" {
				t.Fatalf("unexpected MCP tool call params: %+v", params)
			}
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"hello"}]}}`))
		default:
			t.Fatalf("unexpected MCP method %q", method)
		}
	}))
	defer upstream.Close()

	defs, handlers := MCPTools(map[string]llm.MCPServer{
		"context7": {Kind: llm.MCPHTTP, URL: upstream.URL, Timeout: 2},
	})
	if len(defs) != 1 || defs[0].Name != "mcp_call" {
		t.Fatalf("MCP tool surface should expose generic mcp_call: %+v", defs)
	}
	result, err := handlers.Execute(context.Background(), llm.ToolCall{Name: "mcp_call", Arguments: []byte(`{"server":"context7","tool":"echo","arguments":{"text":"hello"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !sawSession.Load() || !strings.Contains(result.Output, `"server":"context7"`) || !strings.Contains(result.Output, `"text":"hello"`) {
		t.Fatalf("mcp_call output/session is wrong: sawSession=%v output=%s", sawSession.Load(), result.Output)
	}
	_, err = handlers.Execute(context.Background(), llm.ToolCall{Name: "mcp_call", Arguments: []byte(`{"server":"context7","tool":"echo","max_output_bytes":-1}`)})
	if err == nil || !strings.Contains(err.Error(), "max_output_bytes") {
		t.Fatalf("negative MCP max_output_bytes는 거부해야 해요: %v", err)
	}
}

func TestMCPToolsRejectsUnknownServer(t *testing.T) {
	_, handlers := MCPTools(map[string]llm.MCPServer{"serena": {Kind: llm.MCPHTTP, URL: "https://example.test/mcp"}})
	_, err := handlers.Execute(context.Background(), llm.ToolCall{Name: "mcp_call", Arguments: []byte(`{"server":"context7","tool":"lookup"}`)})
	if err == nil || !strings.Contains(err.Error(), "available=serena") {
		t.Fatalf("unknown server error should list configured servers: %v", err)
	}
}

func TestReadMCPFrameRejectsOversizedContentLength(t *testing.T) {
	frame := fmt.Sprintf("Content-Length: %d\r\n\r\n", maxMCPResponseBytes+1)
	if _, err := readMCPFrame(bufio.NewReader(strings.NewReader(frame))); err == nil || !strings.Contains(err.Error(), "너무 커요") {
		t.Fatalf("oversized stdio MCP frame should be rejected before allocation: %v", err)
	}
}

func TestLimitedMCPStderrBuffer(t *testing.T) {
	buf := &limitedMCPBuffer{max: 5}
	n, err := buf.Write([]byte("abcdef"))
	if err != nil || n != 6 {
		t.Fatalf("stderr buffer should accept full producer payload: n=%d err=%v", n, err)
	}
	got := buf.String()
	if !strings.Contains(got, "abcde") || strings.Contains(got, "f") || !strings.Contains(got, "stderr truncated") {
		t.Fatalf("stderr buffer should be bounded and marked truncated: %q", got)
	}
}

func TestReadLimitedMCPBodyDefaultsToResponseEnvelope(t *testing.T) {
	if _, err := ReadLimitedMCPBody(strings.NewReader(strings.Repeat("x", maxMCPResponseBytes+1)), 0); err == nil || !strings.Contains(err.Error(), "너무 커요") {
		t.Fatalf("default MCP body envelope should reject oversized responses: %v", err)
	}
}
