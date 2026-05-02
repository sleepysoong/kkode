package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sleepysoong/kkode/session"
)

func TestGatewayCreatesAndListsSessions(t *testing.T) {
	store := openTestStore(t)
	srv := newTestServer(t, store, "")

	body := bytes.NewBufferString(`{"project_root":"/tmp/repo","provider":"openai","model":"gpt-5-mini","agent":"web","metadata":{"source":"test"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var created SessionDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.ProviderName != "openai" || created.Metadata["source"] != "test" {
		t.Fatalf("unexpected session: %+v", created)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions?limit=10", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var listed SessionListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) != 1 || listed.Sessions[0].ID != created.ID {
		t.Fatalf("unexpected list: %+v", listed)
	}
}

func TestGatewayReplaysEventsAsJSONAndSSE(t *testing.T) {
	store := openTestStore(t)
	sess := session.NewSession("/tmp/repo", "openai", "gpt-5-mini", "agent", session.AgentModeBuild)
	sess.AppendEvent(session.NewEvent(sess.ID, "turn_1", "turn.started"))
	ev := session.NewEvent(sess.ID, "turn_1", "tool.output")
	ev.Tool = "file_read"
	ev.Payload = []byte(`{"path":"README.md"}`)
	sess.AppendEvent(ev)
	if err := store.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, store, "")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/events?after_seq=1", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var events EventListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}
	if len(events.Events) != 1 || events.Events[0].Seq != 2 || events.Events[0].Tool != "file_read" {
		t.Fatalf("unexpected events: %+v", events)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/events?stream=true", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("unexpected sse response: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "event: tool.output") {
		t.Fatalf("sse body did not include event: %s", rec.Body.String())
	}
}

func TestGatewayRequiresAPIKeyWhenConfigured(t *testing.T) {
	store := openTestStore(t)
	srv := newTestServer(t, store, "secret")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestGatewayRunStarterBoundary(t *testing.T) {
	store := openTestStore(t)
	srv, err := New(Config{
		Store: store,
		RunStarter: func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
			return &RunDTO{ID: "run_test", SessionID: req.SessionID, Status: "queued", EventsURL: "/api/v1/runs/run_test/events"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewBufferString(`{"session_id":"sess_1","prompt":"go test"}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var run RunDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.ID != "run_test" || run.Status != "queued" {
		t.Fatalf("unexpected run: %+v", run)
	}
}

func openTestStore(t *testing.T) *session.SQLiteStore {
	t.Helper()
	store, err := session.OpenSQLite(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newTestServer(t *testing.T, store session.Store, apiKey string) *Server {
	t.Helper()
	srv, err := New(Config{Store: store, APIKey: apiKey, Version: "test", Providers: []ProviderDTO{{Name: "openai"}}})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestGatewayListsGetsAndCancelsRuns(t *testing.T) {
	store := openTestStore(t)
	runs := map[string]RunDTO{
		"run_1": {ID: "run_1", SessionID: "sess_1", Status: "running", EventsURL: "/api/v1/sessions/sess_1/events"},
	}
	srv, err := New(Config{
		Store: store,
		RunLister: func(ctx context.Context, q RunQuery) ([]RunDTO, error) {
			return []RunDTO{runs["run_1"]}, nil
		},
		RunGetter: func(ctx context.Context, runID string) (*RunDTO, error) {
			run := runs[runID]
			return &run, nil
		},
		RunCanceler: func(ctx context.Context, runID string) (*RunDTO, error) {
			run := runs[runID]
			run.Status = "cancelled"
			runs[runID] = run
			return &run, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs?session_id=sess_1", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var listed RunListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Runs) != 1 || listed.Runs[0].ID != "run_1" {
		t.Fatalf("run 목록이 이상해요: %+v", listed)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/runs/run_1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got RunDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "running" {
		t.Fatalf("run 상세가 이상해요: %+v", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/runs/run_1/cancel", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var cancelled RunDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &cancelled); err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != "cancelled" {
		t.Fatalf("cancel 응답이 이상해요: %+v", cancelled)
	}
}

func TestGatewayCapabilitiesDiscovery(t *testing.T) {
	store := openTestStore(t)
	srv, err := New(Config{Store: store, Version: "test", Providers: []ProviderDTO{{Name: "copilot", Capabilities: map[string]any{"skills": true}}}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var caps CapabilityResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &caps); err != nil {
		t.Fatal(err)
	}
	if caps.Version != "test" || len(caps.Providers) != 1 || len(caps.Features) == 0 {
		t.Fatalf("capability discovery가 이상해요: %+v", caps)
	}
	var sawBackground bool
	for _, feature := range caps.Features {
		if feature.Name == "background_runs" && feature.Status == "implemented" {
			sawBackground = true
		}
	}
	if !sawBackground {
		t.Fatalf("background run feature를 discovery해야 해요: %+v", caps.Features)
	}
}

func TestGatewayResourceManifestLifecycle(t *testing.T) {
	store := openTestStore(t)
	srv := newTestServer(t, store, "")

	body := bytes.NewBufferString(`{"name":"filesystem","description":"파일 MCP예요","config":{"kind":"stdio","command":"mcp-fs","args":["."]}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp/servers", body)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var created ResourceDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Kind != string(session.ResourceMCPServer) || created.Config["command"] != "mcp-fs" {
		t.Fatalf("생성된 MCP manifest가 이상해요: %+v", created)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/mcp/servers/"+created.ID, nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got ResourceDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "filesystem" {
		t.Fatalf("조회된 MCP manifest가 이상해요: %+v", got)
	}

	body = bytes.NewBufferString(`{"name":"planner","config":{"prompt":"계획을 세워요","tools":["file_read"]}}`)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/subagents", body)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/subagents", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var listed ResourceListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Resources) != 1 || listed.Resources[0].Name != "planner" {
		t.Fatalf("subagent 목록이 이상해요: %+v", listed)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/mcp/servers/"+created.ID, nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestGatewayLSPSymbolSearch(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "main.go"), `package demo

type Runner struct{}

func NewRunner() Runner { return Runner{} }

func (r *Runner) Run() {}
`)
	store := openTestStore(t)
	srv := newTestServer(t, store, "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/lsp/symbols?project_root="+root+"&query=run", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var symbols LSPSymbolListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &symbols); err != nil {
		t.Fatal(err)
	}
	var sawType, sawMethod bool
	for _, symbol := range symbols.Symbols {
		if symbol.Name == "Runner" && symbol.Kind == "type" {
			sawType = true
		}
		if symbol.Name == "Run" && symbol.Kind == "method" && symbol.Container == "Runner" {
			sawMethod = true
		}
	}
	if !sawType || !sawMethod {
		t.Fatalf("LSP symbol 검색이 이상해요: %+v", symbols.Symbols)
	}
}

func TestGatewayLSPDefinitionsAndReferences(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "main.go"), `package demo

type Runner struct{}

func NewRunner() Runner { return Runner{} }

func (r *Runner) Run() {}

func main() {
	r := NewRunner()
	r.Run()
}
`)
	store := openTestStore(t)
	srv := newTestServer(t, store, "")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/lsp/definitions?project_root="+root+"&symbol=Runner", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var defs LSPLocationListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &defs); err != nil {
		t.Fatal(err)
	}
	if len(defs.Locations) != 1 || defs.Locations[0].Name != "Runner" || defs.Locations[0].Kind != "type" {
		t.Fatalf("definition 결과가 이상해요: %+v", defs)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/lsp/definitions?project_root="+root+"&symbol=Runner.Run", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	defs = LSPLocationListResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &defs); err != nil {
		t.Fatal(err)
	}
	if len(defs.Locations) != 1 || defs.Locations[0].Name != "Run" || defs.Locations[0].Container != "Runner" {
		t.Fatalf("method definition 결과가 이상해요: %+v", defs)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/lsp/references?project_root="+root+"&symbol=Runner&limit=20", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var refs LSPReferenceListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &refs); err != nil {
		t.Fatal(err)
	}
	if len(refs.References) < 3 {
		t.Fatalf("reference 결과가 너무 적어요: %+v", refs)
	}
	var sawCtor bool
	for _, ref := range refs.References {
		if strings.Contains(ref.Excerpt, "NewRunner") {
			sawCtor = true
		}
	}
	if !sawCtor {
		t.Fatalf("reference excerpt가 이상해요: %+v", refs.References)
	}
}

func TestGatewayLSPDiagnosticsAndHover(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "main.go"), `package demo

// Runner는 작업을 실행해요.
type Runner struct{}

// Run은 실행 진입점이에요.
func (r *Runner) Run() {}
`)
	writeTestFile(t, filepath.Join(root, "broken.go"), `package demo

func Broken( {
`)
	store := openTestStore(t)
	srv := newTestServer(t, store, "")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/lsp/diagnostics?project_root="+root+"&path=broken.go", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var diagnostics LSPDiagnosticListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &diagnostics); err != nil {
		t.Fatal(err)
	}
	if len(diagnostics.Diagnostics) == 0 || diagnostics.Diagnostics[0].File != "broken.go" || diagnostics.Diagnostics[0].Severity != "error" {
		t.Fatalf("diagnostics 결과가 이상해요: %+v", diagnostics)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/lsp/hover?project_root="+root+"&symbol=Runner.Run", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var hover LSPHoverResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &hover); err != nil {
		t.Fatal(err)
	}
	if !hover.Found || hover.Symbol != "Run" || !strings.Contains(hover.Documentation, "실행 진입점") || !strings.Contains(hover.Signature, "func (r *Runner) Run()") {
		t.Fatalf("hover 결과가 이상해요: %+v", hover)
	}
}

func writeTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayStreamsRunEvents(t *testing.T) {
	store := openTestStore(t)
	bus := NewRunEventBus()
	run := RunDTO{ID: "run_stream", SessionID: "sess_1", Status: "running", EventsURL: "/api/v1/sessions/sess_1/events"}
	srv, err := New(Config{
		Store: store,
		RunGetter: func(ctx context.Context, runID string) (*RunDTO, error) {
			copy := run
			return &copy, nil
		},
		RunSubscriber: bus.Subscribe,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run_stream/events?stream=true", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.ServeHTTP(rec, req)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	bus.Publish(RunDTO{ID: "run_stream", SessionID: "sess_1", Status: "completed"})
	select {
	case <-done:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("run SSE가 종료되지 않았어요")
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("unexpected response: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: run.running") || !strings.Contains(body, "event: run.completed") {
		t.Fatalf("run SSE body가 이상해요: %s", body)
	}
}

func TestGatewayRetriesRun(t *testing.T) {
	store := openTestStore(t)
	original := RunDTO{ID: "run_old", SessionID: "sess_1", Status: "failed", Prompt: "go test", Metadata: map[string]string{"source": "discord"}}
	var retryReq RunStartRequest
	srv, err := New(Config{
		Store: store,
		RunGetter: func(ctx context.Context, runID string) (*RunDTO, error) {
			copy := original
			return &copy, nil
		},
		RunStarter: func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
			retryReq = req
			return &RunDTO{ID: "run_new", SessionID: req.SessionID, Status: "queued", Prompt: req.Prompt, Metadata: req.Metadata}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run_old/retry", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var retried RunDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &retried); err != nil {
		t.Fatal(err)
	}
	if retried.ID != "run_new" || retryReq.Metadata["retried_from"] != "run_old" || retryReq.Metadata["source"] != "discord" {
		t.Fatalf("retry run이 이상해요: run=%+v req=%+v", retried, retryReq)
	}
}

func TestGatewayProbesMCPServerTools(t *testing.T) {
	root := t.TempDir()
	serverPath := filepath.Join(root, "fake_mcp.py")
	writeTestFile(t, serverPath, `import json, sys

def read_frame():
    headers = {}
    while True:
        line = sys.stdin.buffer.readline().decode().strip()
        if not line:
            break
        key, value = line.split(":", 1)
        headers[key.lower()] = value.strip()
    body = sys.stdin.buffer.read(int(headers["content-length"]))
    return json.loads(body)

def write_frame(payload):
    data = json.dumps(payload).encode()
    sys.stdout.buffer.write(b"Content-Length: %d\r\n\r\n" % len(data) + data)
    sys.stdout.buffer.flush()

while True:
    msg = read_frame()
    method = msg.get("method")
    if method == "initialize":
        write_frame({"jsonrpc":"2.0","id":msg["id"],"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}}}})
    elif method == "tools/list":
        write_frame({"jsonrpc":"2.0","id":msg["id"],"result":{"tools":[{"name":"echo","description":"Echo text","inputSchema":{"type":"object"}}]}})
        break
`)
	store := openTestStore(t)
	resource, err := store.SaveResource(context.Background(), session.Resource{Kind: session.ResourceMCPServer, Name: "fake", Enabled: true, Config: []byte(`{"kind":"stdio","command":"python3","args":["` + serverPath + `"],"timeout":3}`)})
	if err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, store, "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mcp/servers/"+resource.ID+"/tools", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var tools MCPToolListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &tools); err != nil {
		t.Fatal(err)
	}
	if tools.Server.ID != resource.ID || len(tools.Tools) != 1 || tools.Tools[0].Name != "echo" {
		t.Fatalf("MCP tools/list 결과가 이상해요: %+v", tools)
	}
}

func TestGatewayCallsMCPServerTool(t *testing.T) {
	root := t.TempDir()
	serverPath := filepath.Join(root, "fake_mcp_call.py")
	writeTestFile(t, serverPath, `import json, sys

def read_frame():
    headers = {}
    while True:
        line = sys.stdin.buffer.readline().decode().strip()
        if not line:
            break
        key, value = line.split(":", 1)
        headers[key.lower()] = value.strip()
    body = sys.stdin.buffer.read(int(headers["content-length"]))
    return json.loads(body)

def write_frame(payload):
    data = json.dumps(payload).encode()
    sys.stdout.buffer.write(b"Content-Length: %d\r\n\r\n" % len(data) + data)
    sys.stdout.buffer.flush()

while True:
    msg = read_frame()
    method = msg.get("method")
    if method == "initialize":
        write_frame({"jsonrpc":"2.0","id":msg["id"],"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}}}})
    elif method == "tools/call":
        args = msg.get("params", {}).get("arguments", {})
        write_frame({"jsonrpc":"2.0","id":msg["id"],"result":{"content":[{"type":"text","text":"hello " + args.get("name", "world")}],"isError":False}})
        break
`)
	store := openTestStore(t)
	resource, err := store.SaveResource(context.Background(), session.Resource{Kind: session.ResourceMCPServer, Name: "fake", Enabled: true, Config: []byte(`{"kind":"stdio","command":"python3","args":["` + serverPath + `"],"timeout":3}`)})
	if err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, store, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp/servers/"+resource.ID+"/tools/echo/call", bytes.NewBufferString(`{"arguments":{"name":"kkode"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got MCPToolCallResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	content, _ := got.Result["content"].([]any)
	first, _ := content[0].(map[string]any)
	if got.Tool != "echo" || first["text"] != "hello kkode" {
		t.Fatalf("MCP tools/call 결과가 이상해요: %+v", got)
	}
}

func TestGatewayListsAndCallsStandardTools(t *testing.T) {
	store := openTestStore(t)
	srv := newTestServer(t, store, "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tools", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var listed ToolListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if !hasTool(listed.Tools, "file_write") || !hasTool(listed.Tools, "web_fetch") || !hasTool(listed.Tools, "shell_run") {
		t.Fatalf("표준 tool 목록이 부족해요: %+v", listed.Tools)
	}

	root := t.TempDir()
	body := `{"project_root":"` + root + `","tool":"file_write","arguments":{"path":"notes/todo.md","content":"hello"},"call_id":"call_1"}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var called ToolCallResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &called); err != nil {
		t.Fatal(err)
	}
	if called.CallID != "call_1" || called.Tool != "file_write" || called.Error != "" {
		t.Fatalf("tool call 응답이 이상해요: %+v", called)
	}
	data, err := os.ReadFile(filepath.Join(root, "notes", "todo.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("file_write가 실행되지 않았어요: %q", data)
	}
}

func TestGatewayCallsWebFetchTool(t *testing.T) {
	store := openTestStore(t)
	srv := newTestServer(t, store, "")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("pong"))
	}))
	defer upstream.Close()
	root := t.TempDir()
	body := `{"project_root":"` + root + `","tool":"web_fetch","arguments":{"url":"` + upstream.URL + `","max_bytes":4}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var called ToolCallResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &called); err != nil {
		t.Fatal(err)
	}
	if called.Tool != "web_fetch" || !strings.Contains(called.Output, "pong") {
		t.Fatalf("web_fetch 결과가 이상해요: %+v", called)
	}
}

func hasTool(tools []ToolDTO, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func TestGatewayMutatesSessionTodos(t *testing.T) {
	store := openTestStore(t)
	sess := session.NewSession("/repo", "openai", "gpt", "agent", session.AgentModeBuild)
	if err := store.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, store, "")

	req := httptest.NewRequest(http.MethodPut, "/api/v1/sessions/"+sess.ID+"/todos", bytes.NewBufferString(`{"todos":[{"id":"todo_1","content":"구현해요","status":"in_progress","priority":"high"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var listed TodoListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Todos) != 1 || listed.Todos[0].ID != "todo_1" || listed.Todos[0].Status != "in_progress" {
		t.Fatalf("todo replace 결과가 이상해요: %+v", listed)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/todos", bytes.NewBufferString(`{"id":"todo_1","content":"검증해요","status":"completed"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Todos) != 1 || listed.Todos[0].Content != "검증해요" || listed.Todos[0].Status != "completed" {
		t.Fatalf("todo upsert 결과가 이상해요: %+v", listed)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/sessions/"+sess.ID+"/todos/todo_1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	loaded, err := store.LoadSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Todos) != 0 {
		t.Fatalf("todo가 삭제되지 않았어요: %+v", loaded.Todos)
	}
}

func TestGatewayPreviewsSkillMarkdown(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "review")
	writeTestFile(t, filepath.Join(skillDir, "SKILL.md"), "# 리뷰 스킬\n\n코드를 리뷰해요.\n")
	store := openTestStore(t)
	resource, err := store.SaveResource(context.Background(), session.Resource{Kind: session.ResourceSkill, Name: "review", Enabled: true, Config: []byte(`{"path":"` + skillDir + `"}`)})
	if err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, store, "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/skills/"+resource.ID+"/preview?max_bytes=8", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var preview SkillPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Skill.ID != resource.ID || preview.File == "" || !preview.Truncated || !strings.Contains(preview.Markdown, "리뷰") {
		t.Fatalf("skill preview가 이상해요: %+v", preview)
	}
}

func TestGatewayPreviewsSubagentManifest(t *testing.T) {
	store := openTestStore(t)
	resource, err := store.SaveResource(context.Background(), session.Resource{Kind: session.ResourceSubagent, Name: "planner", Description: "계획 agent예요", Enabled: true, Config: []byte(`{"display_name":"Planner","prompt":"계획을 세워요","tools":["file_read"],"skills":["review"],"mcp_servers":{"fs":"mcp-fs"},"infer":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, store, "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/subagents/"+resource.ID+"/preview", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var preview SubagentPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Subagent.ID != resource.ID || preview.DisplayName != "Planner" || preview.Prompt == "" || len(preview.Tools) != 1 || preview.MCPServers["fs"] != "mcp-fs" || preview.Infer == nil || !*preview.Infer {
		t.Fatalf("subagent preview가 이상해요: %+v", preview)
	}
}

func TestGatewayCreatesAndReadsCheckpoints(t *testing.T) {
	store := openTestStore(t)
	sess := session.NewSession("/repo", "openai", "gpt", "agent", session.AgentModeBuild)
	if err := store.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, store, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/checkpoints", bytes.NewBufferString(`{"turn_id":"turn_1","payload":{"summary":"저장해요"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var created CheckpointDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.SessionID != sess.ID || !strings.Contains(string(created.Payload), "저장") {
		t.Fatalf("checkpoint 생성 응답이 이상해요: %+v", created)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/checkpoints", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var listed CheckpointListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Checkpoints) != 1 || listed.Checkpoints[0].ID != created.ID {
		t.Fatalf("checkpoint 목록이 이상해요: %+v", listed)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/checkpoints/"+created.ID, nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got CheckpointDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != created.ID || got.TurnID != "turn_1" {
		t.Fatalf("checkpoint 상세가 이상해요: %+v", got)
	}
}

func TestGatewayFilesAPIListsReadsAndWrites(t *testing.T) {
	store := openTestStore(t)
	srv := newTestServer(t, store, "")
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "docs", "a.md"), "one\ntwo\nthree")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/files?project_root="+root+"&path=docs", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var listed FileListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Entries) != 1 || listed.Entries[0].Name != "a.md" || listed.Entries[0].Kind != "file" {
		t.Fatalf("files list가 이상해요: %+v", listed)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/files/content?project_root="+root+"&path=docs/a.md&offset_line=2&limit_lines=1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var content FileContentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &content); err != nil {
		t.Fatal(err)
	}
	if content.Content != "two" {
		t.Fatalf("file content range가 이상해요: %+v", content)
	}

	body := `{"project_root":"` + root + `","path":"docs/b.md","content":"new"}`
	req = httptest.NewRequest(http.MethodPut, "/api/v1/files/content", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(root, "docs", "b.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("file write가 이상해요: %q", data)
	}
}
