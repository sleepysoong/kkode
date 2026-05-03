package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/session"
)

func TestGatewayAPIIndex(t *testing.T) {
	store := openTestStore(t)
	srv := newTestServer(t, store, "")

	req := httptest.NewRequest(http.MethodGet, "/api/v1", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var index APIIndexResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &index); err != nil {
		t.Fatal(err)
	}
	if index.Version != "test" || index.Links["openapi"] != "/api/v1/openapi.yaml" || index.Links["sessions"] != "/api/v1/sessions" {
		t.Fatalf("API index 응답이 이상해요: %+v", index)
	}
}

func TestGatewayReadyChecksStoreHealth(t *testing.T) {
	store := openTestStore(t)
	srv := newTestServer(t, store, "")
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ready status = %d body = %s", rec.Code, rec.Body.String())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("닫힌 store는 ready가 아니어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGatewayRejectsOversizedRequestBody(t *testing.T) {
	store := openTestStore(t)
	srv, err := New(Config{Store: store, Version: "test", MaxRequestBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader(`{"too":"large"}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("큰 요청 body는 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "request_too_large" {
		t.Fatalf("오류 코드가 이상해요: %+v", body)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader(`{"also":"large"}`))
	req.ContentLength = -1
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked 큰 요청 body도 413이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGatewayRejectsTrailingJSONValue(t *testing.T) {
	store := openTestStore(t)
	srv := newTestServer(t, store, "")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader(`{"project_root":"/tmp/repo","provider":"openai","model":"gpt-5-mini"} {"extra":true}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("추가 JSON 값은 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "invalid_json" {
		t.Fatalf("오류 코드가 이상해요: %+v", body)
	}
}

func TestGatewayStatsEndpoint(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	sess := session.NewSession("/repo", "openai", "gpt-5-mini", "agent", session.AgentModeBuild)
	turn := session.NewTurn("통계", llm.Request{Model: "gpt-5-mini"})
	turn.Response = &llm.Response{ID: "resp_stats", Text: "ok"}
	turn.EndedAt = turn.StartedAt
	sess.AppendTurn(turn)
	sess.AppendEvent(session.Event{ID: "ev_stats", SessionID: sess.ID, TurnID: turn.ID, Type: "turn.completed"})
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveRun(ctx, session.Run{ID: "run_stats", SessionID: sess.ID, Status: "running", Prompt: "go"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveResource(ctx, session.Resource{Kind: session.ResourceMCPServer, Name: "mcp"}); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, store, "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var stats StatsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatal(err)
	}
	if stats.Sessions != 1 || stats.Turns != 1 || stats.Events != 1 || stats.Runs["running"] != 1 || stats.Resources[string(session.ResourceMCPServer)] != 1 {
		t.Fatalf("stats 응답이 이상해요: %+v", stats)
	}
}

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

func TestGatewayLimitsSessionEvents(t *testing.T) {
	store := openTestStore(t)
	sess := session.NewSession("/tmp/repo", "openai", "gpt-5-mini", "agent", session.AgentModeBuild)
	for _, typ := range []string{"one", "two", "three"} {
		sess.AppendEvent(session.NewEvent(sess.ID, "turn_1", typ))
	}
	if err := store.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, store, "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/events?after_seq=1&limit=1", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var events EventListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}
	if len(events.Events) != 1 || events.Events[0].Seq != 2 || events.Events[0].Type != "two" {
		t.Fatalf("limited events가 이상해요: %+v", events)
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

func TestGatewayCORSPreflightAndHeaders(t *testing.T) {
	store := openTestStore(t)
	srv, err := New(Config{Store: store, Version: "test", APIKey: "secret", CORSOrigins: []string{"https://panel.example"}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/version", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	req.Header.Set("Origin", "https://panel.example")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "https://panel.example" || !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), RequestIDHeader) {
		t.Fatalf("CORS preflight가 이상해요: status=%d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	req.Header.Set("Origin", "https://panel.example")
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "https://panel.example" {
		t.Fatalf("CORS response header가 필요해요: status=%d headers=%v", rec.Code, rec.Header())
	}
}

func TestGatewayRequestIDHeaderAndErrorEnvelope(t *testing.T) {
	store := openTestStore(t)
	srv, err := New(Config{Store: store, Version: "test", RequestIDGenerator: func() string { return "req_test" }})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Header().Get(RequestIDHeader) != "req_test" {
		t.Fatalf("성공 응답에도 request id header가 필요해요: %v", rec.Header())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/unknown", nil)
	req.Header.Set(RequestIDHeader, "client_req")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Header().Get(RequestIDHeader) != "client_req" {
		t.Fatalf("client request id를 보존해야 해요: %v", rec.Header())
	}
	var body errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.RequestID != "client_req" || body.Error.Code != "not_found" {
		t.Fatalf("오류 envelope request id가 이상해요: %+v", body)
	}
}

func TestGatewayAccessLoggerUsesRequestID(t *testing.T) {
	store := openTestStore(t)
	var entries []AccessLogEntry
	now := time.Unix(100, 0).UTC()
	srv, err := New(Config{
		Store:              store,
		Version:            "test",
		RequestIDGenerator: func() string { return "req_log" },
		Now:                func() time.Time { return now },
		AccessLogger: func(entry AccessLogEntry) {
			entries = append(entries, entry)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/version?detail=true", nil)
	req.RemoteAddr = "198.51.100.7:4567"
	req.Header.Set("User-Agent", "panel-test")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if len(entries) != 1 {
		t.Fatalf("access log entry가 하나 필요해요: %+v", entries)
	}
	entry := entries[0]
	if entry.RequestID != "req_log" || entry.Method != http.MethodGet || entry.Path != "/api/v1/version?detail=true" || entry.Status != http.StatusOK || entry.Bytes <= 0 || entry.Remote != "198.51.100.7:4567" || entry.UserAgent != "panel-test" {
		t.Fatalf("access log entry가 이상해요: %+v", entry)
	}
}

func TestGatewayRunStarterBoundary(t *testing.T) {
	store := openTestStore(t)
	var started RunStartRequest
	srv, err := New(Config{
		Store: store,
		RunStarter: func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
			started = req
			return &RunDTO{ID: "run_test", SessionID: req.SessionID, Status: "queued", EventsURL: "/api/v1/runs/run_test/events", Metadata: req.Metadata}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewBufferString(`{"session_id":"sess_1","prompt":"go test","metadata":{"source":"panel"}}`))
	req.Header.Set(RequestIDHeader, "req_run")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var run RunDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.ID != "run_test" || run.Status != "queued" || run.Metadata[RequestIDMetadataKey] != "req_run" || started.Metadata[RequestIDMetadataKey] != "req_run" || started.Metadata["source"] != "panel" {
		t.Fatalf("unexpected run: %+v", run)
	}
}

func TestGatewayListsRunsByRequestID(t *testing.T) {
	store := openTestStore(t)
	var query RunQuery
	srv, err := New(Config{
		Store: store,
		RunLister: func(ctx context.Context, q RunQuery) ([]RunDTO, error) {
			query = q
			return []RunDTO{{ID: "run_req", SessionID: "sess_1", Status: "completed", Metadata: map[string]string{RequestIDMetadataKey: q.RequestID}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs?request_id=req_filter&limit=5", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if query.RequestID != "req_filter" || query.Limit != 5 {
		t.Fatalf("run query가 이상해요: %+v", query)
	}
	var body RunListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Runs) != 1 || body.Runs[0].Metadata[RequestIDMetadataKey] != "req_filter" {
		t.Fatalf("run list 응답이 이상해요: %+v", body)
	}
}

func TestGatewayRequestCorrelationRunsEndpoint(t *testing.T) {
	store := openTestStore(t)
	var query RunQuery
	srv, err := New(Config{
		Store: store,
		RunLister: func(ctx context.Context, q RunQuery) ([]RunDTO, error) {
			query = q
			return []RunDTO{{ID: "run_req", SessionID: "sess_1", Status: "completed", Metadata: map[string]string{RequestIDMetadataKey: q.RequestID}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/requests/req_filter/runs?limit=7", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if query.RequestID != "req_filter" || query.Limit != 7 {
		t.Fatalf("request correlation query가 이상해요: %+v", query)
	}
	var body RequestCorrelationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.RequestID != "req_filter" || len(body.Runs) != 1 || body.Runs[0].ID != "run_req" {
		t.Fatalf("request correlation 응답이 이상해요: %+v", body)
	}
}

func TestGatewayRequestCorrelationEventsEndpoint(t *testing.T) {
	store := openTestStore(t)
	var query RunQuery
	var eventRunID string
	srv, err := New(Config{
		Store: store,
		RunLister: func(ctx context.Context, q RunQuery) ([]RunDTO, error) {
			query = q
			return []RunDTO{{ID: "run_req", SessionID: "sess_1", Status: "completed", Metadata: map[string]string{RequestIDMetadataKey: q.RequestID}}}, nil
		},
		RunEventLister: func(ctx context.Context, runID string, afterSeq int, limit int) ([]RunEventDTO, error) {
			eventRunID = runID
			return []RunEventDTO{
				{Seq: 1, At: time.Unix(2, 0).UTC(), Type: "run.completed", Run: RunDTO{ID: runID, Status: "completed"}},
				{Seq: 2, At: time.Unix(1, 0).UTC(), Type: "run.queued", Run: RunDTO{ID: runID, Status: "queued"}},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/requests/req_filter/events?limit=5", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if query.RequestID != "req_filter" || eventRunID != "run_req" {
		t.Fatalf("request event query가 이상해요: query=%+v runID=%s", query, eventRunID)
	}
	var body RequestCorrelationEventsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.RequestID != "req_filter" || len(body.Events) != 2 || body.Events[0].Type != "run.queued" || body.Events[1].Type != "run.completed" {
		t.Fatalf("request correlation event 응답이 이상해요: %+v", body)
	}
}

func TestGatewayStreamsRequestCorrelationEvents(t *testing.T) {
	store := openTestStore(t)
	bus := NewRunEventBus()
	run := RunDTO{ID: "run_req_stream", SessionID: "sess_1", Status: "running", Metadata: map[string]string{RequestIDMetadataKey: "req_stream"}}
	srv, err := New(Config{
		Store: store,
		RunLister: func(ctx context.Context, q RunQuery) ([]RunDTO, error) {
			if q.RequestID != "req_stream" {
				return nil, nil
			}
			copy := run
			return []RunDTO{copy}, nil
		},
		RunSubscriber: bus.Subscribe,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/requests/req_stream/events?stream=true", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.ServeHTTP(rec, req)
		close(done)
	}()
	waitForRunSubscription(t, bus, "run_req_stream")
	bus.Publish(RunDTO{ID: "run_req_stream", SessionID: "sess_1", Status: "completed", Metadata: map[string]string{RequestIDMetadataKey: "req_stream"}})
	select {
	case <-done:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("request correlation SSE가 종료되지 않았어요")
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("unexpected response: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: run.running") || !strings.Contains(body, "event: run.completed") || !strings.Contains(body, "run_req_stream") {
		t.Fatalf("request correlation SSE body가 이상해요: %s", body)
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
	srv, err := New(Config{Store: store, Version: "test", MaxRequestBytes: 1234, Providers: []ProviderDTO{{Name: "copilot", Capabilities: map[string]any{"skills": true}}}, DefaultMCPServers: []ResourceDTO{{Name: "context7", Kind: string(session.ResourceMCPServer), Config: map[string]any{"url": "https://mcp.context7.com/mcp"}}}})
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
	if caps.Version != "test" || len(caps.Providers) != 1 || len(caps.Features) == 0 || len(caps.DefaultMCPServers) != 1 || caps.Limits.MaxRequestBytes != 1234 {
		t.Fatalf("capability discovery가 이상해요: %+v", caps)
	}
	if caps.DefaultMCPServers[0].Name != "context7" || caps.DefaultMCPServers[0].Config["url"] == "" {
		t.Fatalf("기본 MCP discovery가 필요해요: %+v", caps.DefaultMCPServers)
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

func TestGatewayDiagnosticsReportsRuntimeWiring(t *testing.T) {
	store := openTestStore(t)
	srv, err := New(Config{
		Store:             store,
		Version:           "test",
		MaxRequestBytes:   123,
		Providers:         []ProviderDTO{{Name: "openai"}},
		DefaultMCPServers: []ResourceDTO{{Name: "context7"}},
		RunStarter: func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
			return &RunDTO{}, nil
		},
		RunPreviewer: func(ctx context.Context, req RunStartRequest) (*RunPreviewResponse, error) {
			return &RunPreviewResponse{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/diagnostics", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var diagnostics DiagnosticsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &diagnostics); err != nil {
		t.Fatal(err)
	}
	if !diagnostics.OK || diagnostics.Providers != 1 || diagnostics.DefaultMCPServers != 1 || diagnostics.MaxRequestBytes != 123 {
		t.Fatalf("diagnostics 응답이 이상해요: %+v", diagnostics)
	}
	var sawStore bool
	for _, check := range diagnostics.Checks {
		if check.Name == "store" && check.Status == "ok" {
			sawStore = true
		}
	}
	if !sawStore {
		t.Fatalf("store check가 필요해요: %+v", diagnostics.Checks)
	}
}

func TestGatewayPreviewsRunAssembly(t *testing.T) {
	store := openTestStore(t)
	sess := session.NewSession("/tmp/repo", "openai", "gpt-5-mini", "agent", session.AgentModeBuild)
	if err := store.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	var gotReq RunStartRequest
	srv, err := New(Config{
		Store: store,
		RunPreviewer: func(ctx context.Context, req RunStartRequest) (*RunPreviewResponse, error) {
			gotReq = req
			return &RunPreviewResponse{SessionID: req.SessionID, Provider: "openai", Model: "gpt-5-mini", BaseRequestTools: []string{"mcp"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/preview", strings.NewReader(`{"session_id":"`+sess.ID+`","prompt":"미리보기","provider":"openai"}`))
	req.Header.Set(RequestIDHeader, "req_preview")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var preview RunPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.SessionID != sess.ID || preview.BaseRequestTools[0] != "mcp" || gotReq.Metadata[RequestIDMetadataKey] != "req_preview" {
		t.Fatalf("run preview 응답/요청이 이상해요: preview=%+v req=%+v", preview, gotReq)
	}
}

func TestGatewayServesOpenAPISpec(t *testing.T) {
	store := openTestStore(t)
	srv := newTestServer(t, store, "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.yaml", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(rec.Header().Get("Content-Type"), "application/yaml") || !strings.Contains(body, "openapi: 3.0.3") || !strings.Contains(body, "/api/v1/openapi.yaml") {
		t.Fatalf("openapi 응답이 이상해요: content_type=%s body=%s", rec.Header().Get("Content-Type"), body[:min(len(body), 120)])
	}
}

func TestGatewaySessionTurnsAPI(t *testing.T) {
	store := openTestStore(t)
	sess := session.NewSession("/repo", "openai", "gpt-5-mini", "web", session.AgentModeBuild)
	first := session.NewTurn("첫 요청", llm.Request{Model: "gpt-5-mini", Messages: []llm.Message{llm.UserText("첫 요청")}})
	first.Response = llm.TextResponse("openai", "gpt-5-mini", "첫 응답")
	first.Response.ID = "resp_1"
	first.Response.Usage = llm.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
	first.EndedAt = first.StartedAt.Add(time.Second)
	sess.AppendTurn(first)
	second := session.NewTurn("둘째 요청", llm.Request{Model: "gpt-5-mini", Messages: []llm.Message{llm.UserText("둘째 요청")}})
	second.Response = llm.TextResponse("openai", "gpt-5-mini", "둘째 응답")
	second.EndedAt = second.StartedAt.Add(time.Second)
	sess.AppendTurn(second)
	if err := store.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, store, "")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/turns?after_seq=0&limit=1", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var listed TurnListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Turns) != 1 || listed.Turns[0].Seq != 1 || listed.Turns[0].Prompt != "첫 요청" || listed.Turns[0].ResponseText != "첫 응답" {
		t.Fatalf("turn 목록이 이상해요: %+v", listed)
	}
	if listed.Turns[0].Usage == nil || listed.Turns[0].Usage.TotalTokens != 15 {
		t.Fatalf("turn usage가 이상해요: %+v", listed.Turns[0])
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/turns/"+second.ID, nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got TurnDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != second.ID || got.Seq != 2 || got.Messages[0].Content != "둘째 요청" {
		t.Fatalf("turn 상세가 이상해요: %+v", got)
	}
}

func TestGatewayModelsDiscovery(t *testing.T) {
	store := openTestStore(t)
	srv, err := New(Config{Store: store, Providers: []ProviderDTO{
		{Name: "openai", Models: []string{"gpt-5-mini", "gpt-5-large"}, DefaultModel: "gpt-5-mini", Capabilities: map[string]any{"tools": true}, AuthStatus: "configured"},
		{Name: "codex", Models: []string{"gpt-5.3-codex"}, DefaultModel: "gpt-5.3-codex", AuthStatus: "local"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/models?provider=openai", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var listed ModelListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Models) != 2 {
		t.Fatalf("openai model 목록이 이상해요: %+v", listed)
	}
	if listed.Models[0].Provider != "openai" || listed.Models[0].ID != "gpt-5-mini" || !listed.Models[0].Default || listed.Models[0].AuthStatus != "configured" {
		t.Fatalf("기본 model discovery가 이상해요: %+v", listed.Models[0])
	}
	listed.Models[0].Capabilities["tools"] = false
	req = httptest.NewRequest(http.MethodGet, "/api/v1/models?provider=openai", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Models[0].Capabilities["tools"] != true {
		t.Fatal("model capability map은 응답마다 방어 복사해야 해요")
	}
}

func TestGatewayPromptTemplateAPIs(t *testing.T) {
	store := openTestStore(t)
	srv := newTestServer(t, store, "")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/prompts", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var listed PromptTemplateListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Prompts) == 0 || listed.Prompts[0].Name == "" {
		t.Fatalf("prompt 목록이 이상해요: %+v", listed)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/prompts/agent-system.md", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got PromptTemplateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "agent-system.md" || !strings.Contains(got.Text, "{{.AgentName}}") {
		t.Fatalf("prompt 원문이 이상해요: %+v", got)
	}

	body := bytes.NewBufferString(`{"data":{"AgentName":"kkode","ToolNames":["file_read","shell_run"]}}`)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/prompts/agent-system.md/render", body)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var rendered PromptRenderResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &rendered); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered.Text, "kkode") || !strings.Contains(rendered.Text, "shell_run") {
		t.Fatalf("prompt 렌더링이 이상해요: %+v", rendered)
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
	waitForRunSubscription(t, bus, "run_stream")
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

func waitForRunSubscription(t *testing.T, bus *RunEventBus, runID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		bus.mu.Lock()
		count := len(bus.subscribers[runID])
		bus.mu.Unlock()
		if count > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run SSE 구독이 준비되지 않았어요")
}

func TestGatewayRetriesRun(t *testing.T) {
	store := openTestStore(t)
	original := RunDTO{ID: "run_old", SessionID: "sess_1", Status: "failed", Prompt: "go test", Provider: "copilot", Model: "gpt-5-mini", MCPServers: []string{"mcp_1"}, Skills: []string{"skill_1"}, Subagents: []string{"agent_1"}, Metadata: map[string]string{"source": "discord"}}
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
	req.Header.Set(RequestIDHeader, "req_retry")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var retried RunDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &retried); err != nil {
		t.Fatal(err)
	}
	if retried.ID != "run_new" || retryReq.Metadata["retried_from"] != "run_old" || retryReq.Metadata["source"] != "discord" || retryReq.Metadata[RequestIDMetadataKey] != "req_retry" {
		t.Fatalf("retry run이 이상해요: run=%+v req=%+v", retried, retryReq)
	}
	if retryReq.Provider != "copilot" || retryReq.Model != "gpt-5-mini" || len(retryReq.MCPServers) != 1 || retryReq.MCPServers[0] != "mcp_1" || len(retryReq.Skills) != 1 || retryReq.Skills[0] != "skill_1" || len(retryReq.Subagents) != 1 || retryReq.Subagents[0] != "agent_1" {
		t.Fatalf("retry가 실행 옵션을 보존해야 해요: %+v", retryReq)
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

func TestGatewayProbesMCPServerResourcesAndPrompts(t *testing.T) {
	root := t.TempDir()
	serverPath := filepath.Join(root, "fake_mcp_resources.py")
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
        write_frame({"jsonrpc":"2.0","id":msg["id"],"result":{"protocolVersion":"2024-11-05","capabilities":{"resources":{},"prompts":{}}}})
    elif method == "resources/list":
        write_frame({"jsonrpc":"2.0","id":msg["id"],"result":{"resources":[{"uri":"file:///README.md","name":"README","description":"문서","mimeType":"text/markdown"}]}})
        break
    elif method == "prompts/list":
        write_frame({"jsonrpc":"2.0","id":msg["id"],"result":{"prompts":[{"name":"review","description":"리뷰","arguments":[{"name":"path","description":"대상","required":True}]}]}})
        break
`)
	store := openTestStore(t)
	resource, err := store.SaveResource(context.Background(), session.Resource{Kind: session.ResourceMCPServer, Name: "fake", Enabled: true, Config: []byte(`{"kind":"stdio","command":"python3","args":["` + serverPath + `"],"timeout":3}`)})
	if err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, store, "")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/mcp/servers/"+resource.ID+"/resources", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("resources status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resources MCPResourceListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resources); err != nil {
		t.Fatal(err)
	}
	if resources.Server.ID != resource.ID || len(resources.Resources) != 1 || resources.Resources[0].URI != "file:///README.md" || resources.Resources[0].MimeType != "text/markdown" {
		t.Fatalf("MCP resources/list 결과가 이상해요: %+v", resources)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/mcp/servers/"+resource.ID+"/prompts", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("prompts status = %d body = %s", rec.Code, rec.Body.String())
	}
	var prompts MCPPromptListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &prompts); err != nil {
		t.Fatal(err)
	}
	if prompts.Server.ID != resource.ID || len(prompts.Prompts) != 1 || prompts.Prompts[0].Name != "review" || len(prompts.Prompts[0].Arguments) != 1 || !prompts.Prompts[0].Arguments[0].Required {
		t.Fatalf("MCP prompts/list 결과가 이상해요: %+v", prompts)
	}
}

func TestGatewayReadsMCPResourceAndGetsPrompt(t *testing.T) {
	root := t.TempDir()
	serverPath := filepath.Join(root, "fake_mcp_read_prompt.py")
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
        write_frame({"jsonrpc":"2.0","id":msg["id"],"result":{"protocolVersion":"2024-11-05","capabilities":{"resources":{},"prompts":{}}}})
    elif method == "resources/read":
        uri = msg.get("params", {}).get("uri", "")
        write_frame({"jsonrpc":"2.0","id":msg["id"],"result":{"contents":[{"uri":uri,"mimeType":"text/plain","text":"hello resource"}]}})
        break
    elif method == "prompts/get":
        params = msg.get("params", {})
        name = params.get("name", "")
        path = params.get("arguments", {}).get("path", "README.md")
        write_frame({"jsonrpc":"2.0","id":msg["id"],"result":{"messages":[{"role":"user","content":{"type":"text","text":"review " + name + " " + path}}]}})
        break
`)
	store := openTestStore(t)
	resource, err := store.SaveResource(context.Background(), session.Resource{Kind: session.ResourceMCPServer, Name: "fake", Enabled: true, Config: []byte(`{"kind":"stdio","command":"python3","args":["` + serverPath + `"],"timeout":3}`)})
	if err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, store, "")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/mcp/servers/"+resource.ID+"/resources/read?uri="+url.QueryEscape("file:///README.md"), nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("resource read status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resourceRead MCPResourceReadResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resourceRead); err != nil {
		t.Fatal(err)
	}
	if resourceRead.URI != "file:///README.md" || len(resourceRead.Contents) != 1 || resourceRead.Contents[0].Text != "hello resource" {
		t.Fatalf("MCP resources/read 결과가 이상해요: %+v", resourceRead)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/mcp/servers/"+resource.ID+"/prompts/review/get", bytes.NewBufferString(`{"arguments":{"path":"main.go"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("prompt get status = %d body = %s", rec.Code, rec.Body.String())
	}
	var promptGet MCPPromptGetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &promptGet); err != nil {
		t.Fatal(err)
	}
	if promptGet.Prompt != "review" || len(promptGet.Messages) != 1 || promptGet.Messages[0].Content["text"] != "review review main.go" {
		t.Fatalf("MCP prompts/get 결과가 이상해요: %+v", promptGet)
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

func TestGatewayGitStatusDiffAndLog(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary가 없어요")
	}
	root := t.TempDir()
	runTestGit(t, root, "init")
	runTestGit(t, root, "config", "user.email", "kkode@example.com")
	runTestGit(t, root, "config", "user.name", "kkode")
	writeTestFile(t, filepath.Join(root, "README.md"), "hello\n")
	runTestGit(t, root, "add", "README.md")
	runTestGit(t, root, "commit", "-m", "initial")
	writeTestFile(t, filepath.Join(root, "README.md"), "hello\nworld\n")
	writeTestFile(t, filepath.Join(root, "new.txt"), "new\n")

	store := openTestStore(t)
	srv := newTestServer(t, store, "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/git/status?project_root="+url.QueryEscape(root), nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var status GitStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.ProjectRoot == "" || len(status.Entries) < 2 || !hasGitPath(status.Entries, "README.md") || !hasGitPath(status.Entries, "new.txt") {
		t.Fatalf("git status 응답이 이상해요: %+v", status)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/git/diff?project_root="+url.QueryEscape(root)+"&path=README.md", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("diff status = %d body = %s", rec.Code, rec.Body.String())
	}
	var diff GitDiffResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &diff); err != nil {
		t.Fatal(err)
	}
	if diff.Path != "README.md" || !strings.Contains(diff.Diff, "+world") {
		t.Fatalf("git diff 응답이 이상해요: %+v", diff)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/git/log?project_root="+url.QueryEscape(root)+"&limit=1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("log status = %d body = %s", rec.Code, rec.Body.String())
	}
	var log GitLogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &log); err != nil {
		t.Fatal(err)
	}
	if len(log.Commits) != 1 || log.Commits[0].Subject != "initial" {
		t.Fatalf("git log 응답이 이상해요: %+v", log)
	}
}

func runTestGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func hasGitPath(entries []GitStatusEntryDTO, path string) bool {
	for _, entry := range entries {
		if entry.Path == path {
			return true
		}
	}
	return false
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

func TestGatewaySessionTranscriptAPI(t *testing.T) {
	store := openTestStore(t)
	sess := session.NewSession("/repo", "openai", "gpt-5-mini", "web", session.AgentModeBuild)
	turn := session.NewTurn("토큰은 token=abc1234567890secretvalue 예요", llm.Request{Model: "gpt-5-mini", Messages: []llm.Message{llm.UserText("토큰은 token=abc1234567890secretvalue 예요")}})
	turn.Response = llm.TextResponse("openai", "gpt-5-mini", "응답에도 token=abc1234567890secretvalue 가 있어요")
	turn.EndedAt = turn.StartedAt.Add(time.Second)
	sess.AppendTurn(turn)
	if err := store.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, store, "")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/transcript", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got TranscriptResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Session.ID != sess.ID || len(got.Turns) != 1 || !got.Redacted || !strings.Contains(got.Markdown, "[REDACTED]") {
		t.Fatalf("transcript 응답이 이상해요: %+v", got)
	}
	if strings.Contains(got.Markdown, "abc1234567890secretvalue") || strings.Contains(got.Turns[0].Prompt, "abc1234567890secretvalue") {
		t.Fatalf("transcript는 기본적으로 secret을 가려야 해요: %+v", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/transcript?redact=false", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Redacted || !strings.Contains(got.Markdown, "abc1234567890secretvalue") {
		t.Fatalf("redact=false transcript가 이상해요: %+v", got)
	}
}

func TestGatewayCompactsSessionAndCreatesCheckpoint(t *testing.T) {
	store := openTestStore(t)
	sess := session.NewSession("/repo", "openai", "gpt-5-mini", "web", session.AgentModeBuild)
	for _, prompt := range []string{"첫 요청", "둘째 요청", "셋째 요청"} {
		turn := session.NewTurn(prompt, llm.Request{Model: "gpt-5-mini", Messages: []llm.Message{llm.UserText(prompt)}})
		turn.Response = llm.TextResponse("openai", "gpt-5-mini", prompt+" 응답")
		turn.EndedAt = turn.StartedAt.Add(time.Second)
		sess.AppendTurn(turn)
	}
	if err := store.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, store, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/compact", bytes.NewBufferString(`{"preserve_first_n_turns":1,"preserve_last_n_turns":1,"checkpoint":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got SessionCompactResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Session.ID != sess.ID || got.Summary == "" || !strings.Contains(got.Summary, "둘째 요청") || got.Checkpoint == nil {
		t.Fatalf("compact 응답이 이상해요: %+v", got)
	}
	loaded, err := store.LoadSession(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Summary != got.Summary {
		t.Fatalf("session summary가 저장되지 않았어요: %q != %q", loaded.Summary, got.Summary)
	}
	cp, err := store.LoadCheckpoint(context.Background(), sess.ID, got.Checkpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cp.Payload), "session.compaction") {
		t.Fatalf("checkpoint payload가 이상해요: %s", cp.Payload)
	}
}
