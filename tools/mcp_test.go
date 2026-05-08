package tools

import (
	"context"
	"encoding/json"
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
}

func TestMCPToolsRejectsUnknownServer(t *testing.T) {
	_, handlers := MCPTools(map[string]llm.MCPServer{"serena": {Kind: llm.MCPHTTP, URL: "https://example.test/mcp"}})
	_, err := handlers.Execute(context.Background(), llm.ToolCall{Name: "mcp_call", Arguments: []byte(`{"server":"context7","tool":"lookup"}`)})
	if err == nil || !strings.Contains(err.Error(), "available=serena") {
		t.Fatalf("unknown server error should list configured servers: %v", err)
	}
}
