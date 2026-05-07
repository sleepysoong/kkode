package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/session"
)

// safeResponseRecorder는 스트리밍 핸들러를 테스트할 때 본문을 동시에 읽고 쓰는 레이스를 피하려고 써요.
type safeResponseRecorder struct {
	mu  sync.Mutex
	rec *httptest.ResponseRecorder
}

func newSafeResponseRecorder() *safeResponseRecorder {
	return &safeResponseRecorder{rec: httptest.NewRecorder()}
}

func (r *safeResponseRecorder) Header() http.Header {
	return r.rec.Header()
}

func (r *safeResponseRecorder) WriteHeader(statusCode int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rec.WriteHeader(statusCode)
}

func (r *safeResponseRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rec.Write(p)
}

func (r *safeResponseRecorder) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rec.Flush()
}

func (r *safeResponseRecorder) Code() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rec.Code
}

func (r *safeResponseRecorder) BodyString() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rec.Body.String()
}

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
	if index.Version != "test" || index.Links["health"] != "/healthz" || index.Links["ready"] != "/readyz" || index.Links["openapi"] != "/api/v1/openapi.yaml" || index.Links["sessions"] != "/api/v1/sessions" || index.Links["session_import"] != "/api/v1/sessions/import" || index.Links["session_export"] == "" || index.Links["session_transcript"] == "" || index.Links["run_transcript"] == "" || index.Links["request_transcript"] == "" {
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
	var body ErrorEnvelope
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
	var body ErrorEnvelope
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
	if stats.Sessions != 1 || stats.Turns != 1 || stats.Events != 1 || stats.TotalRuns != 1 || stats.Runs["running"] != 1 || stats.TotalResources != 1 || stats.Resources[string(session.ResourceMCPServer)] != 1 {
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
	if listed.Limit != 10 || listed.ResultTruncated {
		t.Fatalf("session list metadata가 이상해요: %+v", listed)
	}
	extra := session.NewSession("/tmp/repo", "openai", "gpt-5-mini", "agent", session.AgentModeBuild)
	if err := store.CreateSession(context.Background(), extra); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions?limit=1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	listed = SessionListResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) != 1 || !listed.ResultTruncated || listed.Limit != 1 {
		t.Fatalf("session list truncation metadata가 이상해요: %+v", listed)
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
	if events.AfterSeq != 1 || events.Limit == 0 || events.ResultTruncated {
		t.Fatalf("event list metadata가 이상해요: %+v", events)
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
	if !events.ResultTruncated || events.NextAfterSeq != 2 || events.Limit != 1 {
		t.Fatalf("limited event metadata가 이상해요: %+v", events)
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
	var body ErrorEnvelope
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
	var validated RunStartRequest
	srv, err := New(Config{
		Store: store,
		RunValidator: func(ctx context.Context, req RunStartRequest) error {
			validated = req
			return nil
		},
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
	if validated.SessionID != "sess_1" || validated.Metadata[RequestIDMetadataKey] != "req_run" {
		t.Fatalf("run validator는 enqueue 전에 같은 request metadata를 받아야 해요: %+v", validated)
	}
}

func TestGatewayValidatesRunWithoutStarting(t *testing.T) {
	store := openTestStore(t)
	started := false
	var validated RunStartRequest
	srv, err := New(Config{
		Store: store,
		RunLister: func(ctx context.Context, q RunQuery) ([]RunDTO, error) {
			if q.IdempotencyKey == "idem_validate" {
				return []RunDTO{{ID: "run_existing", SessionID: q.SessionID, Status: "queued", Metadata: map[string]string{IdempotencyMetadataKey: q.IdempotencyKey}}}, nil
			}
			return nil, nil
		},
		RunValidator: func(ctx context.Context, req RunStartRequest) error {
			validated = req
			return nil
		},
		RunStarter: func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
			started = true
			return &RunDTO{ID: "run_should_not_start", SessionID: req.SessionID, Status: "queued"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/validate", bytes.NewBufferString(`{"session_id":"sess_1","prompt":"go test"}`))
	req.Header.Set(RequestIDHeader, "req_validate")
	req.Header.Set(IdempotencyKeyHeader, "idem_validate")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got RunValidateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.RequestID != "req_validate" || got.IdempotencyKey != "idem_validate" || got.RunID == "" || got.ExistingRun == nil || got.ExistingRun.ID != "run_existing" || validated.Metadata[RequestIDMetadataKey] != "req_validate" || validated.Metadata[IdempotencyMetadataKey] != "idem_validate" {
		t.Fatalf("validate 응답이 이상해요: got=%+v validated=%+v", got, validated)
	}
	if started {
		t.Fatal("/runs/validate는 RunStarter를 호출하면 안 돼요")
	}
}

func TestGatewayValidateRunReportsPreflightError(t *testing.T) {
	store := openTestStore(t)
	srv, err := New(Config{
		Store: store,
		RunValidator: func(ctx context.Context, req RunStartRequest) error {
			return errors.New("skill missing")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/validate", bytes.NewBufferString(`{"session_id":"sess_1","prompt":"go test"}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("validate 실패도 panel-friendly JSON이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got RunValidateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OK || got.Code != "invalid_run_preflight" || !strings.Contains(got.Message, "skill missing") {
		t.Fatalf("preflight 실패 응답이 이상해요: %+v", got)
	}
}

func TestGatewayRunValidatorRejectsBeforeStarter(t *testing.T) {
	store := openTestStore(t)
	started := false
	srv, err := New(Config{
		Store: store,
		RunValidator: func(ctx context.Context, req RunStartRequest) error {
			return errors.New("resource off")
		},
		RunStarter: func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
			started = true
			return &RunDTO{ID: "run_test", SessionID: req.SessionID, Status: "queued"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewBufferString(`{"session_id":"sess_1","prompt":"go test"}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_run_preflight") {
		t.Fatalf("preflight 오류는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if started {
		t.Fatal("validator가 실패하면 RunStarter를 호출하면 안 돼요")
	}
}

func TestGatewayRunStartIsIdempotent(t *testing.T) {
	store := openTestStore(t)
	started := false
	var listed RunQuery
	srv, err := New(Config{
		Store: store,
		RunLister: func(ctx context.Context, q RunQuery) ([]RunDTO, error) {
			listed = q
			if q.IdempotencyKey == "idem_1" {
				return []RunDTO{{ID: "run_existing", SessionID: q.SessionID, Status: "queued", Metadata: map[string]string{IdempotencyMetadataKey: q.IdempotencyKey}}}, nil
			}
			return nil, nil
		},
		RunStarter: func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
			started = true
			return &RunDTO{ID: "run_new", SessionID: req.SessionID, Status: "queued", Metadata: req.Metadata}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewBufferString(`{"session_id":"sess_1","prompt":"go test"}`))
	req.Header.Set(IdempotencyKeyHeader, "idem_1")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("idempotent 재시도는 기존 run을 200으로 돌려야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get(IdempotencyReplayHeader) != "true" {
		t.Fatalf("idempotent 재사용 응답 header가 필요해요: %s", rec.Header().Get(IdempotencyReplayHeader))
	}
	var run RunDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.ID != "run_existing" || listed.IdempotencyKey != "idem_1" || listed.SessionID != "sess_1" {
		t.Fatalf("기존 run 조회가 이상해요: run=%+v query=%+v", run, listed)
	}
	if run.Metadata[IdempotencyReusedMetadataKey] != "true" {
		t.Fatalf("idempotent 재사용 metadata가 필요해요: %+v", run.Metadata)
	}
	if started {
		t.Fatal("idempotency key로 기존 run을 찾으면 새 run을 시작하면 안 돼요")
	}
}

func TestGatewayRunStartStoresIdempotencyKey(t *testing.T) {
	store := openTestStore(t)
	var started RunStartRequest
	srv, err := New(Config{
		Store: store,
		RunLister: func(ctx context.Context, q RunQuery) ([]RunDTO, error) {
			return nil, nil
		},
		RunStarter: func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
			started = req
			return &RunDTO{ID: "run_new", SessionID: req.SessionID, Status: "queued", Metadata: req.Metadata}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewBufferString(`{"session_id":"sess_1","prompt":"go test","metadata":{"idempotency_key":"body_key"}}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("새 run은 accepted여야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if started.Metadata[IdempotencyMetadataKey] != "body_key" {
		t.Fatalf("idempotency key를 run metadata에 저장해야 해요: %+v", started.Metadata)
	}
	if !strings.HasPrefix(started.RunID, "run_idem_") {
		t.Fatalf("idempotency key가 있으면 결정적 run id를 써야 해요: %s", started.RunID)
	}
}

func TestGatewayRunStartReturnsReplayFromStarter(t *testing.T) {
	store := openTestStore(t)
	srv, err := New(Config{
		Store: store,
		RunStarter: func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
			return &RunDTO{ID: req.RunID, SessionID: req.SessionID, Status: "queued", Metadata: map[string]string{IdempotencyMetadataKey: req.Metadata[IdempotencyMetadataKey], IdempotencyReusedMetadataKey: "true"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewBufferString(`{"session_id":"sess_1","prompt":"go test"}`))
	req.Header.Set(IdempotencyKeyHeader, "idem_2")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get(IdempotencyReplayHeader) != "true" {
		t.Fatalf("RunStarter가 재사용 run을 돌려주면 200 replay여야 해요: status=%d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
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
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs?request_id=req_filter&idempotency_key=idem_filter&limit=5", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if query.RequestID != "req_filter" || query.IdempotencyKey != "idem_filter" || query.Limit != 6 {
		t.Fatalf("run query가 이상해요: %+v", query)
	}
	var body RunListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Runs) != 1 || body.Runs[0].Metadata[RequestIDMetadataKey] != "req_filter" {
		t.Fatalf("run list 응답이 이상해요: %+v", body)
	}
	if body.Limit != 5 || body.ResultTruncated {
		t.Fatalf("run list metadata가 이상해요: %+v", body)
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
	if query.RequestID != "req_filter" || query.Limit != 8 {
		t.Fatalf("request correlation query가 이상해요: %+v", query)
	}
	var body RequestCorrelationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.RequestID != "req_filter" || len(body.Runs) != 1 || body.Runs[0].ID != "run_req" {
		t.Fatalf("request correlation 응답이 이상해요: %+v", body)
	}
	if body.Limit != 7 || body.ResultTruncated {
		t.Fatalf("request correlation metadata가 이상해요: %+v", body)
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
	if body.Limit != 5 || body.ResultTruncated {
		t.Fatalf("request correlation event metadata가 이상해요: %+v", body)
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
	rec := newSafeResponseRecorder()
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
	if rec.Code() != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("unexpected response: %d %s", rec.Code(), rec.Header().Get("Content-Type"))
	}
	body := rec.BodyString()
	if !strings.Contains(body, "event: run.running") || !strings.Contains(body, "event: run.completed") || !strings.Contains(body, "run_req_stream") {
		t.Fatalf("request correlation SSE body가 이상해요: %s", body)
	}
}

func TestGatewayRequestCorrelationSSESendsHeartbeat(t *testing.T) {
	store := openTestStore(t)
	bus := NewRunEventBus()
	run := RunDTO{ID: "run_req_heartbeat", SessionID: "sess_1", Status: "running", Metadata: map[string]string{RequestIDMetadataKey: "req_heartbeat"}}
	srv, err := New(Config{
		Store: store,
		RunLister: func(ctx context.Context, q RunQuery) ([]RunDTO, error) {
			return []RunDTO{run}, nil
		},
		RunSubscriber: bus.Subscribe,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/requests/req_heartbeat/events?stream=true&heartbeat_ms=10", nil).WithContext(ctx)
	rec := newSafeResponseRecorder()
	done := make(chan struct{})
	go func() {
		srv.ServeHTTP(rec, req)
		close(done)
	}()
	waitForRunSubscription(t, bus, "run_req_heartbeat")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !strings.Contains(rec.BodyString(), ": heartbeat") {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("heartbeat 테스트 SSE가 종료되지 않았어요")
	}
	if !strings.Contains(rec.BodyString(), ": heartbeat") {
		t.Fatalf("request correlation SSE heartbeat가 필요해요: %s", rec.BodyString())
	}
}

func TestGatewayRequestCorrelationSSECatchesUpdateDuringReplay(t *testing.T) {
	store := openTestStore(t)
	bus := NewRunEventBus()
	run := RunDTO{ID: "run_req_replay_race", SessionID: "sess_1", Status: "running", Metadata: map[string]string{RequestIDMetadataKey: "req_replay_race"}}
	published := false
	srv, err := New(Config{
		Store: store,
		RunLister: func(ctx context.Context, q RunQuery) ([]RunDTO, error) {
			if q.RequestID != "req_replay_race" {
				return nil, nil
			}
			return []RunDTO{run}, nil
		},
		RunEventLister: func(ctx context.Context, runID string, afterSeq int, limit int) ([]RunEventDTO, error) {
			if !published {
				published = true
				bus.Publish(RunDTO{ID: "run_req_replay_race", SessionID: "sess_1", Status: "completed", Metadata: map[string]string{RequestIDMetadataKey: "req_replay_race"}})
			}
			return []RunEventDTO{{Seq: 1, At: time.Now().UTC(), Type: "run.running", Run: run}}, nil
		},
		RunSubscriber: bus.Subscribe,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/requests/req_replay_race/events?stream=true", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.ServeHTTP(rec, req)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("request replay 중 들어온 terminal update를 놓치면 SSE가 끝나지 않아요")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: run.running") || !strings.Contains(body, "event: run.completed") {
		t.Fatalf("request replay 중 live update를 모두 보내야 해요: %s", body)
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
		"run_1": {ID: "run_1", SessionID: "sess_1", Status: "running", EventsURL: runEventsURL("run_1")},
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
	srv, err := New(Config{Store: store, Version: "test", MaxRequestBytes: 1234, MaxConcurrentRuns: 3, RunTimeout: 2 * time.Minute, Providers: []ProviderDTO{{Name: "copilot", Capabilities: map[string]any{"skills": true}}}, DefaultMCPServers: []ResourceDTO{{Name: "context7", Kind: string(session.ResourceMCPServer), Config: map[string]any{"url": "https://mcp.context7.com/mcp", "headers": map[string]any{"CONTEXT7_API_KEY": "secret-token"}}}}})
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
	if caps.Version != "test" || len(caps.Providers) != 1 || len(caps.Features) == 0 || len(caps.DefaultMCPServers) != 1 || caps.Limits.MaxRequestBytes != 1234 || caps.Limits.MaxConcurrentRuns != 3 || caps.Limits.RunTimeoutSeconds != 120 {
		t.Fatalf("capability discovery가 이상해요: %+v", caps)
	}
	if caps.DefaultMCPServers[0].Name != "context7" || caps.DefaultMCPServers[0].Config["url"] == "" {
		t.Fatalf("기본 MCP discovery가 필요해요: %+v", caps.DefaultMCPServers)
	}
	headers := caps.DefaultMCPServers[0].Config["headers"].(map[string]any)
	if headers["CONTEXT7_API_KEY"] != "[REDACTED]" {
		t.Fatalf("capability discovery는 MCP secret header를 숨겨야 해요: %+v", caps.DefaultMCPServers[0].Config)
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

func TestResourceDTORedactionMasksSecretConfig(t *testing.T) {
	redacted := RedactResourceDTO(ResourceDTO{Config: map[string]any{
		"headers": map[string]any{"Authorization": "Bearer secret", "X-Test": "ok"},
		"env":     map[string]string{"API_KEY": "secret", "PLAIN": "visible"},
		"args":    []string{"--token=abc1234567890secretvalue"},
	}})
	if redacted.Config["headers"].(map[string]any)["Authorization"] != "[REDACTED]" {
		t.Fatalf("Authorization header는 숨겨야 해요: %+v", redacted.Config)
	}
	if redacted.Config["env"].(map[string]string)["API_KEY"] != "[REDACTED]" {
		t.Fatalf("env secret은 숨겨야 해요: %+v", redacted.Config)
	}
	if redacted.Config["env"].(map[string]string)["PLAIN"] != "visible" {
		t.Fatalf("일반 값은 유지해야 해요: %+v", redacted.Config)
	}
	if redacted.Config["args"].([]string)[0] == "--token=abc1234567890secretvalue" {
		t.Fatalf("args 안의 token 패턴도 숨겨야 해요: %+v", redacted.Config)
	}
}

func TestGatewayDiagnosticsReportsRuntimeWiring(t *testing.T) {
	store := openTestStore(t)
	srv, err := New(Config{
		Store:             store,
		Version:           "test",
		MaxRequestBytes:   123,
		MaxConcurrentRuns: 2,
		RunTimeout:        time.Minute,
		Providers:         []ProviderDTO{{Name: "openai"}},
		DefaultMCPServers: []ResourceDTO{{Name: "context7"}},
		DiagnosticChecks:  []DiagnosticCheckDTO{{Name: "default_mcp.context7", Status: "configured", Message: "url=https://mcp.context7.com/mcp"}},
		RunStarter: func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
			return &RunDTO{}, nil
		},
		RunPreviewer: func(ctx context.Context, req RunStartRequest) (*RunPreviewResponse, error) {
			return &RunPreviewResponse{}, nil
		},
		RunValidator: func(ctx context.Context, req RunStartRequest) error {
			return nil
		},
		ProviderTester: func(ctx context.Context, provider string, req ProviderTestRequest) (*ProviderTestResponse, error) {
			return &ProviderTestResponse{OK: true, Provider: provider}, nil
		},
		RunRuntimeStats: func() RunRuntimeStats {
			return RunRuntimeStats{TrackedRuns: 3, ActiveRuns: 2, QueuedRuns: 1, RunningRuns: 1, MaxConcurrentRuns: 2, OccupiedRunSlots: 1, AvailableRunSlots: 1, RunTimeout: time.Minute}
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
	if !diagnostics.OK || diagnostics.Providers != 1 || diagnostics.DefaultMCPServers != 1 || diagnostics.MaxRequestBytes != 123 || diagnostics.MaxConcurrentRuns != 2 || diagnostics.RunTimeoutSeconds != 60 {
		t.Fatalf("diagnostics 응답이 이상해요: %+v", diagnostics)
	}
	if diagnostics.RunRuntime == nil || diagnostics.RunRuntime.TrackedRuns != 3 || diagnostics.RunRuntime.QueuedRuns != 1 || diagnostics.RunRuntime.RunningRuns != 1 || diagnostics.RunRuntime.AvailableRunSlots != 1 || diagnostics.RunRuntime.RunTimeoutSeconds != 60 {
		t.Fatalf("runtime diagnostics가 이상해요: %+v", diagnostics.RunRuntime)
	}
	var sawStore, sawDefaultMCP, sawRunValidator, sawProviderTester bool
	for _, check := range diagnostics.Checks {
		if check.Name == "store" && check.Status == "ok" {
			sawStore = true
		}
		if check.Name == "default_mcp.context7" && check.Status == "configured" {
			sawDefaultMCP = true
		}
		if check.Name == "run_validator" && check.Status == "ok" {
			sawRunValidator = true
		}
		if check.Name == "provider_tester" && check.Status == "ok" {
			sawProviderTester = true
		}
	}
	if !sawStore || !sawDefaultMCP || !sawRunValidator || !sawProviderTester {
		t.Fatalf("store/default MCP/runtime wiring check가 필요해요: %+v", diagnostics.Checks)
	}
}

func TestGatewayDiagnosticsMarksMissingRuntimeWiringUnhealthy(t *testing.T) {
	store := openTestStore(t)
	srv, err := New(Config{
		Store:      store,
		Version:    "test",
		Providers:  []ProviderDTO{{Name: "openai"}},
		RunStarter: func(ctx context.Context, req RunStartRequest) (*RunDTO, error) { return &RunDTO{}, nil },
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
	if diagnostics.OK {
		t.Fatalf("runtime wiring이 빠진 diagnostics는 unhealthy여야 해요: %+v", diagnostics)
	}
	missing := map[string]bool{}
	for _, check := range diagnostics.Checks {
		if check.Status == "missing" {
			missing[check.Name] = true
		}
	}
	if !missing["run_previewer"] || !missing["run_validator"] || !missing["provider_tester"] {
		t.Fatalf("빠진 runtime wiring check가 필요해요: %+v", diagnostics.Checks)
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
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/preview", strings.NewReader(`{"session_id":"`+sess.ID+`","prompt":"미리보기","provider":"openai","preview_stream":true}`))
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
	if preview.SessionID != sess.ID || preview.BaseRequestTools[0] != "mcp" || gotReq.Metadata[RequestIDMetadataKey] != "req_preview" || !gotReq.PreviewStream {
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
	if !listed.ResultTruncated || listed.NextAfterSeq != 1 || listed.Limit != 1 {
		t.Fatalf("turn list metadata가 이상해요: %+v", listed)
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
		{Name: "openai", Aliases: []string{"openai-compatible"}, Models: []string{"gpt-5-mini", "gpt-5-large"}, DefaultModel: "gpt-5-mini", Capabilities: map[string]any{"tools": true}, AuthStatus: "configured", AuthEnv: []string{"OPENAI_API_KEY"}, Conversion: &ConversionDTO{RequestConverter: "openai.ResponsesConverter", Call: "openai.Client.CallProvider", Source: "http-json+sse", Operations: []string{"responses.create"}}},
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
	req = httptest.NewRequest(http.MethodGet, "/api/v1/models?provider=openai-compatible", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("alias model status = %d body = %s", rec.Code, rec.Body.String())
	}
	listed = ModelListResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Models) != 2 || listed.Models[0].Provider != "openai" {
		t.Fatalf("provider alias로 model을 찾아야 해요: %+v", listed)
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

	req = httptest.NewRequest(http.MethodGet, "/api/v1/providers", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var providers ProviderListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &providers); err != nil {
		t.Fatal(err)
	}
	if providers.Providers[0].Conversion == nil || providers.Providers[0].Conversion.Source != "http-json+sse" || providers.Providers[0].Conversion.Operations[0] != "responses.create" {
		t.Fatalf("provider 변환 profile discovery가 필요해요: %+v", providers.Providers[0])
	}
	if len(providers.Providers[0].Aliases) != 1 || providers.Providers[0].Aliases[0] != "openai-compatible" {
		t.Fatalf("provider alias discovery가 필요해요: %+v", providers.Providers[0])
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/providers/openai-compatible", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("provider detail status = %d body = %s", rec.Code, rec.Body.String())
	}
	var provider ProviderDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &provider); err != nil {
		t.Fatal(err)
	}
	if provider.Name != "openai" || len(provider.Aliases) != 1 || len(provider.AuthEnv) != 1 || provider.AuthEnv[0] != "OPENAI_API_KEY" || provider.Conversion == nil || provider.Conversion.Source != "http-json+sse" {
		t.Fatalf("provider 상세 discovery가 이상해요: %+v", provider)
	}

	var testedProvider string
	srv, err = New(Config{
		Store:     store,
		Providers: []ProviderDTO{{Name: "openai", Aliases: []string{"openai-compatible"}}},
		ProviderTester: func(ctx context.Context, provider string, req ProviderTestRequest) (*ProviderTestResponse, error) {
			testedProvider = provider
			return &ProviderTestResponse{OK: true, Provider: "openai", Model: req.Model, Message: "ok", ProviderRequest: &ProviderRequestPreviewDTO{Provider: "openai", Operation: "responses.create"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/providers/openai-compatible/test", strings.NewReader(`{"model":"gpt-5-mini","prompt":"ping"}`))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("provider test status = %d body = %s", rec.Code, rec.Body.String())
	}
	var testResp ProviderTestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &testResp); err != nil {
		t.Fatal(err)
	}
	if testedProvider != "openai-compatible" || !testResp.OK || testResp.ProviderRequest == nil || testResp.ProviderRequest.Operation != "responses.create" {
		t.Fatalf("provider test 응답이 이상해요: provider=%s resp=%+v", testedProvider, testResp)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/providers/missing", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("없는 provider는 404여야 해요: status=%d body=%s", rec.Code, rec.Body.String())
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
	if listed.Limit == 0 || listed.ResultTruncated {
		t.Fatalf("resource list metadata가 이상해요: %+v", listed)
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
	req := httptest.NewRequest(http.MethodGet, "/api/v1/lsp/symbols?project_root="+root+"&query=run&limit=1", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var symbols LSPSymbolListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &symbols); err != nil {
		t.Fatal(err)
	}
	if len(symbols.Symbols) != 1 || symbols.Limit != 1 || !symbols.ResultTruncated {
		t.Fatalf("LSP symbol limit metadata가 이상해요: %+v", symbols)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/lsp/symbols?project_root="+root+"&query=run", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	symbols = LSPSymbolListResponse{}
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
	if defs.Limit != 50 || defs.ResultTruncated {
		t.Fatalf("definition limit metadata가 이상해요: %+v", defs)
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
	if refs.Limit != 20 || refs.ResultTruncated {
		t.Fatalf("reference limit metadata가 이상해요: %+v", refs)
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
	if diagnostics.Limit != 200 || diagnostics.ResultTruncated {
		t.Fatalf("diagnostics limit metadata가 이상해요: %+v", diagnostics)
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
	run := RunDTO{ID: "run_stream", SessionID: "sess_1", Status: "running", EventsURL: runEventsURL("run_stream")}
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

func TestGatewayRunSSESendsHeartbeat(t *testing.T) {
	store := openTestStore(t)
	bus := NewRunEventBus()
	run := RunDTO{ID: "run_heartbeat", SessionID: "sess_1", Status: "running", EventsURL: runEventsURL("run_heartbeat")}
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
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run_heartbeat/events?stream=true&heartbeat_ms=10", nil).WithContext(ctx)
	rec := newSafeResponseRecorder()
	done := make(chan struct{})
	go func() {
		srv.ServeHTTP(rec, req)
		close(done)
	}()
	waitForRunSubscription(t, bus, "run_heartbeat")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !strings.Contains(rec.BodyString(), ": heartbeat") {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("run heartbeat 테스트 SSE가 종료되지 않았어요")
	}
	if !strings.Contains(rec.BodyString(), ": heartbeat") {
		t.Fatalf("run SSE heartbeat가 필요해요: %s", rec.BodyString())
	}
}

func TestGatewayRunSSEStreamsProgressEvents(t *testing.T) {
	store := openTestStore(t)
	bus := NewRunEventBus()
	run := RunDTO{ID: "run_progress", SessionID: "sess_1", Status: "running", EventsURL: runEventsURL("run_progress")}
	srv, err := New(Config{
		Store: store,
		RunGetter: func(ctx context.Context, runID string) (*RunDTO, error) {
			copy := run
			return &copy, nil
		},
		RunEventSubscriber: bus.SubscribeEvents,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run_progress/events?stream=true", nil).WithContext(ctx)
	rec := newSafeResponseRecorder()
	done := make(chan struct{})
	go func() {
		srv.ServeHTTP(rec, req)
		close(done)
	}()
	waitForRunEventSubscription(t, bus, "run_progress")
	bus.PublishEvent(RunEventDTO{Seq: 2, At: time.Now().UTC(), Type: "tool.completed", Tool: "file_read", Message: "ok", Run: run})
	bus.PublishEvent(RunEventDTO{Seq: 3, At: time.Now().UTC(), Type: "run.completed", Run: RunDTO{ID: "run_progress", SessionID: "sess_1", Status: "completed"}})
	select {
	case <-done:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("progress SSE가 종료되지 않았어요")
	}
	body := rec.BodyString()
	if !strings.Contains(body, "event: tool.completed") || !strings.Contains(body, `"tool":"file_read"`) || !strings.Contains(body, `"message":"ok"`) {
		t.Fatalf("run progress SSE body가 이상해요: %s", body)
	}
}

func TestGatewayRunTranscriptEndpoint(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	secret := "token=abc1234567890secretvalue"
	sess := session.NewSession("/repo", "openai", "gpt-5-mini", "agent", session.AgentModeBuild)
	turn := session.NewTurn("prompt "+secret, llm.Request{Model: "gpt-5-mini", Messages: []llm.Message{llm.UserText("prompt " + secret)}})
	turn.Response = llm.TextResponse("openai", "gpt-5-mini", "response "+secret)
	sess.AppendTurn(turn)
	sess.AppendEvent(session.Event{ID: "ev_run_transcript", SessionID: sess.ID, TurnID: turn.ID, Type: "tool.completed", Tool: "file_read", At: time.Now().UTC(), Payload: json.RawMessage(`{"value":"token=abc1234567890secretvalue"}`)})
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	run := session.Run{ID: "run_transcript", SessionID: sess.ID, TurnID: turn.ID, Status: "completed", Prompt: "run " + secret, Provider: "openai", Model: "gpt-5-mini"}
	if _, _, err := store.SaveRunWithEvent(ctx, run, session.RunEvent{RunID: run.ID, Type: "tool.completed", Tool: "file_read", Message: "message " + secret, At: time.Now().UTC(), Run: run}); err != nil {
		t.Fatal(err)
	}
	manager := NewAsyncRunManagerWithStore(nil, store)
	srv, err := New(Config{Store: store, RunGetter: manager.Get, RunEventLister: manager.Events})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run_transcript/transcript", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got RunTranscriptResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Run.ID != "run_transcript" || got.Turn == nil || got.Turn.ID != turn.ID || len(got.Events) != 1 || len(got.RunEvents) != 1 || !strings.Contains(got.Markdown, "Run events") {
		t.Fatalf("run transcript 응답이 이상해요: %+v", got)
	}
	body := rec.Body.String()
	if !got.Redacted || !strings.Contains(body, "[REDACTED]") || strings.Contains(body, secret) {
		t.Fatalf("run transcript는 기본 redaction을 적용해야 해요: %s", body)
	}
}

func TestGatewayRequestCorrelationTranscriptEndpoint(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	secret := "token=abc1234567890secretvalue"
	requestID := "req_transcript"
	sess := session.NewSession("/repo", "openai", "gpt-5-mini", "agent", session.AgentModeBuild)
	turn := session.NewTurn("prompt "+secret, llm.Request{Model: "gpt-5-mini", Messages: []llm.Message{llm.UserText("prompt " + secret)}})
	turn.Response = llm.TextResponse("openai", "gpt-5-mini", "response "+secret)
	sess.AppendTurn(turn)
	sess.AppendEvent(session.Event{ID: "ev_request_transcript", SessionID: sess.ID, TurnID: turn.ID, Type: "tool.completed", Tool: "file_read", At: time.Now().UTC(), Payload: json.RawMessage(`{"value":"token=abc1234567890secretvalue"}`)})
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	run := session.Run{ID: "run_request_transcript", SessionID: sess.ID, TurnID: turn.ID, Status: "completed", Prompt: "run " + secret, Provider: "openai", Model: "gpt-5-mini", Metadata: map[string]string{RequestIDMetadataKey: requestID}}
	if _, _, err := store.SaveRunWithEvent(ctx, run, session.RunEvent{RunID: run.ID, Type: "tool.completed", Tool: "file_read", Message: "message " + secret, At: time.Now().UTC(), Run: run}); err != nil {
		t.Fatal(err)
	}
	manager := NewAsyncRunManagerWithStore(nil, store)
	srv, err := New(Config{Store: store, RunLister: manager.List, RunEventLister: manager.Events})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/requests/"+requestID+"/transcript", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got RequestCorrelationTranscriptResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.RequestID != requestID || len(got.Transcripts) != 1 || got.Transcripts[0].Run.ID != run.ID || !strings.Contains(got.Markdown, "kkode request transcript") || !strings.Contains(got.Markdown, "run_request_transcript") {
		t.Fatalf("request transcript 응답이 이상해요: %+v", got)
	}
	body := rec.Body.String()
	if !got.Redacted || !strings.Contains(body, "[REDACTED]") || strings.Contains(body, secret) {
		t.Fatalf("request transcript는 기본 redaction을 적용해야 해요: %s", body)
	}
}

func TestGatewayRunSSECatchesUpdateDuringReplay(t *testing.T) {
	store := openTestStore(t)
	bus := NewRunEventBus()
	run := RunDTO{ID: "run_replay_race", SessionID: "sess_1", Status: "running"}
	published := false
	srv, err := New(Config{
		Store: store,
		RunGetter: func(ctx context.Context, runID string) (*RunDTO, error) {
			copy := run
			return &copy, nil
		},
		RunEventLister: func(ctx context.Context, runID string, afterSeq int, limit int) ([]RunEventDTO, error) {
			if !published {
				published = true
				bus.Publish(RunDTO{ID: "run_replay_race", SessionID: "sess_1", Status: "completed"})
			}
			return []RunEventDTO{{Seq: 1, At: time.Now().UTC(), Type: "run.running", Run: run}}, nil
		},
		RunSubscriber: bus.Subscribe,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run_replay_race/events?stream=true", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.ServeHTTP(rec, req)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("replay 중 들어온 terminal update를 놓치면 SSE가 끝나지 않아요")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: run.running") || !strings.Contains(body, "event: run.completed") {
		t.Fatalf("replay 중 live update를 모두 보내야 해요: %s", body)
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

func waitForRunEventSubscription(t *testing.T, bus *RunEventBus, runID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		bus.mu.Lock()
		count := len(bus.eventSubscribers[runID])
		bus.mu.Unlock()
		if count > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run event SSE 구독이 준비되지 않았어요")
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

func TestGatewayProbesHTTPMCPServerTools(t *testing.T) {
	var sawSession bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.Contains(r.Header.Get("Accept"), "text/event-stream") || r.Header.Get("X-Test-Token") != "secret" {
			t.Fatalf("HTTP MCP request header가 이상해요: method=%s accept=%s token=%s", r.Method, r.Header.Get("Accept"), r.Header.Get("X-Test-Token"))
		}
		var msg map[string]any
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			t.Fatal(err)
		}
		switch msg["method"] {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess_http_mcp")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg["id"], "result": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{}}}})
		case "notifications/initialized":
			if r.Header.Get("Mcp-Session-Id") != "sess_http_mcp" {
				t.Fatalf("HTTP MCP session header가 이어져야 해요: %s", r.Header.Get("Mcp-Session-Id"))
			}
			sawSession = true
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			if !sawSession || r.Header.Get("Mcp-Session-Id") != "sess_http_mcp" {
				t.Fatalf("tools/list도 session header를 이어야 해요: saw=%v header=%s", sawSession, r.Header.Get("Mcp-Session-Id"))
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"http_echo","description":"HTTP echo","inputSchema":{"type":"object"}}]}}`)
		default:
			t.Fatalf("unexpected HTTP MCP method: %v", msg["method"])
		}
	}))
	defer upstream.Close()
	store := openTestStore(t)
	resource, err := store.SaveResource(context.Background(), session.Resource{Kind: session.ResourceMCPServer, Name: "http", Enabled: true, Config: []byte(`{"kind":"http","url":"` + upstream.URL + `","headers":{"X-Test-Token":"secret"},"timeout":3}`)})
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
	if tools.Server.ID != resource.ID || len(tools.Tools) != 1 || tools.Tools[0].Name != "http_echo" {
		t.Fatalf("HTTP MCP tools/list 결과가 이상해요: %+v", tools)
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
	if got.Tool != "echo" || first["text"] != "hello kkode" || got.ResultBytes == 0 || got.ResultTruncated {
		t.Fatalf("MCP tools/call 결과가 이상해요: %+v", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/mcp/servers/"+resource.ID+"/tools/echo/call", bytes.NewBufferString(`{"arguments":{"name":"kkode"},"max_output_bytes":7}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("truncated status = %d body = %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	content, _ = got.Result["content"].([]any)
	first, _ = content[0].(map[string]any)
	if first["text"] != "hello k" || got.ResultBytes <= 7 || !got.ResultTruncated {
		t.Fatalf("MCP tools/call 출력 제한 응답이 이상해요: %+v", got)
	}
}

func TestGatewayCallsHTTPMCPServerToolJSON(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg map[string]any
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			t.Fatal(err)
		}
		switch msg["method"] {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess_json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg["id"], "result": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{}}}})
		case "notifications/initialized":
			if r.Header.Get("Mcp-Session-Id") != "sess_json" {
				t.Fatalf("HTTP MCP session header가 이어져야 해요: %s", r.Header.Get("Mcp-Session-Id"))
			}
			w.WriteHeader(http.StatusAccepted)
		case "tools/call":
			if r.Header.Get("Mcp-Session-Id") != "sess_json" {
				t.Fatalf("tools/call도 session header를 이어야 해요: %s", r.Header.Get("Mcp-Session-Id"))
			}
			params, _ := msg["params"].(map[string]any)
			if params["name"] != "http_echo" {
				t.Fatalf("HTTP MCP tool 이름이 이상해요: %+v", params)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg["id"], "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "hello from http mcp"}}, "isError": false}})
		default:
			t.Fatalf("unexpected HTTP MCP method: %v", msg["method"])
		}
	}))
	defer upstream.Close()
	store := openTestStore(t)
	resource, err := store.SaveResource(context.Background(), session.Resource{Kind: session.ResourceMCPServer, Name: "http", Enabled: true, Config: []byte(`{"kind":"http","url":"` + upstream.URL + `","timeout":3}`)})
	if err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, store, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp/servers/"+resource.ID+"/tools/http_echo/call", bytes.NewBufferString(`{"max_output_bytes":5}`))
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
	if got.Tool != "http_echo" || first["text"] != "hello" || got.ResultBytes <= 5 || !got.ResultTruncated {
		t.Fatalf("HTTP MCP tools/call 결과 제한이 이상해요: %+v", got)
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
	if !findTool(listed.Tools, "file_write").RequiresWorkspace || findTool(listed.Tools, "web_fetch").RequiresWorkspace {
		t.Fatalf("tool별 workspace 요구 여부를 discovery해야 해요: %+v", listed.Tools)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/tools/web_fetch", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tool 상세 status = %d body = %s", rec.Code, rec.Body.String())
	}
	var detail ToolDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Name != "web_fetch" || detail.RequiresWorkspace {
		t.Fatalf("tool 상세 discovery가 이상해요: %+v", detail)
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

	body = `{"project_root":"` + root + `","tool":"file_read","arguments":{"path":"notes/todo.md"},"max_output_bytes":2}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &called); err != nil {
		t.Fatal(err)
	}
	if called.Output != "he" || called.OutputBytes != len("hello") || !called.OutputTruncated {
		t.Fatalf("tool output 제한 응답이 이상해요: %+v", called)
	}
}

func TestGatewayCallsWebFetchTool(t *testing.T) {
	store := openTestStore(t)
	srv := newTestServer(t, store, "")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("pong"))
	}))
	defer upstream.Close()
	body := `{"tool":"web_fetch","arguments":{"url":"` + upstream.URL + `","max_bytes":4}}`
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
	return findTool(tools, name).Name != ""
}

func findTool(tools []ToolDTO, name string) ToolDTO {
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	return ToolDTO{}
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
	resource, err := store.SaveResource(context.Background(), session.Resource{Kind: session.ResourceSubagent, Name: "planner", Description: "계획 agent예요", Enabled: true, Config: []byte(`{"display_name":"Planner","prompt":"계획을 세워요","tools":["file_read"],"skills":["review"],"mcp_server_ids":["mcp_context7"],"mcp_servers":{"fs":"mcp-fs","context7":{"kind":"http","url":"https://mcp.context7.com/mcp"}},"infer":true}`)})
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
	context7, _ := preview.MCPServers["context7"].(map[string]any)
	if preview.Subagent.ID != resource.ID || preview.DisplayName != "Planner" || preview.Prompt == "" || len(preview.Tools) != 1 || preview.MCPServers["fs"] != "mcp-fs" || len(preview.MCPServerIDs) != 1 || context7["url"] != "https://mcp.context7.com/mcp" || preview.Infer == nil || !*preview.Infer {
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
	if listed.Limit == 0 || listed.ResultTruncated {
		t.Fatalf("checkpoint list metadata가 이상해요: %+v", listed)
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

func TestGatewayExportsSessionBundle(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	sess := session.NewSession("/repo", "openai", "gpt-5-mini", "agent", session.AgentModeBuild)
	turn := session.NewTurn("go", llm.Request{Model: "gpt-5-mini", Messages: []llm.Message{llm.UserText("go")}})
	turn.Response = llm.TextResponse("openai", "gpt-5-mini", "ok")
	sess.AppendTurn(turn)
	sess.AppendEvent(session.Event{ID: "ev_export", SessionID: sess.ID, TurnID: turn.ID, Type: "turn.completed", At: time.Now().UTC()})
	sess.Todos = []session.Todo{{ID: "todo_export", Content: "내보내기", Status: session.TodoCompleted, UpdatedAt: time.Now().UTC()}}
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCheckpoint(ctx, session.Checkpoint{ID: "cp_export", SessionID: sess.ID, TurnID: turn.ID, CreatedAt: time.Now().UTC(), Payload: json.RawMessage(`{"summary":"복구해요"}`)}); err != nil {
		t.Fatal(err)
	}
	mcpResource, err := store.SaveResource(ctx, session.Resource{Kind: session.ResourceMCPServer, Name: "context7", Enabled: true, Config: []byte(`{"kind":"http","url":"https://mcp.context7.com/mcp","headers":{"Authorization":"Bearer secret-token"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	skillResource, err := store.SaveResource(ctx, session.Resource{Kind: session.ResourceSkill, Name: "review", Enabled: true, Config: []byte(`{"path":"/tmp/skills/review"}`)})
	if err != nil {
		t.Fatal(err)
	}
	subagentResource, err := store.SaveResource(ctx, session.Resource{Kind: session.ResourceSubagent, Name: "planner", Enabled: true, Config: []byte(`{"prompt":"계획해요","mcp_server_ids":["` + mcpResource.ID + `"]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveRun(ctx, session.Run{ID: "run_export", SessionID: sess.ID, Status: "completed", Prompt: "go", Model: "gpt-5-mini", MCPServers: []string{mcpResource.ID}, Skills: []string{skillResource.ID}, Subagents: []string{subagentResource.ID}}); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{
		Store: store,
		RunLister: func(ctx context.Context, q RunQuery) ([]RunDTO, error) {
			runs, err := store.ListRuns(ctx, session.RunQuery{SessionID: q.SessionID, Limit: q.Limit})
			if err != nil {
				return nil, err
			}
			out := make([]RunDTO, 0, len(runs))
			for _, run := range runs {
				out = append(out, *runDTOFromSession(run))
			}
			return out, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/export", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var exported SessionExportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &exported); err != nil {
		t.Fatal(err)
	}
	if exported.FormatVersion != sessionExportFormatVersion || exported.Session.ID != sess.ID || exported.RawSession == nil || exported.RawSession.ID != sess.ID || len(exported.Turns) != 1 || len(exported.Events) != 1 || len(exported.Todos) != 1 || len(exported.Checkpoints) != 1 || len(exported.Runs) != 1 || len(exported.Resources) != 3 {
		t.Fatalf("session export가 이상해요: %+v", exported)
	}
	if exported.Counts.Turns != 1 || exported.Counts.Events != 1 || exported.Counts.Todos != 1 || exported.Counts.Checkpoints != 1 || exported.Counts.Runs != 1 || exported.Counts.Resources != 3 {
		t.Fatalf("session export counts가 이상해요: %+v", exported.Counts)
	}
	if exported.Turns[0].ResponseText != "ok" || exported.RawSession.Turns[0].Response.Output[0].Content != "ok" || exported.Checkpoints[0].ID != "cp_export" || exported.Runs[0].ID != "run_export" || !hasResourceDTO(exported.Resources, mcpResource.ID) || !hasResourceDTO(exported.Resources, skillResource.ID) || !hasResourceDTO(exported.Resources, subagentResource.ID) {
		t.Fatalf("session export 상세가 이상해요: %+v", exported)
	}
}

func TestGatewayImportsSessionBundleWithNewID(t *testing.T) {
	ctx := context.Background()
	sourceStore := openTestStore(t)
	sess := session.NewSession("/repo", "openai", "gpt-5-mini", "agent", session.AgentModeBuild)
	turn := session.NewTurn("go", llm.Request{Model: "gpt-5-mini", Messages: []llm.Message{llm.UserText("go")}})
	turn.Response = llm.TextResponse("openai", "gpt-5-mini", "ok")
	sess.AppendTurn(turn)
	sess.AppendEvent(session.Event{ID: "ev_import", SessionID: sess.ID, TurnID: turn.ID, Type: "turn.completed", At: time.Now().UTC()})
	if err := sourceStore.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if err := sourceStore.SaveCheckpoint(ctx, session.Checkpoint{ID: "cp_import", SessionID: sess.ID, TurnID: turn.ID, CreatedAt: time.Now().UTC(), Payload: json.RawMessage(`{"summary":"옮겨요"}`)}); err != nil {
		t.Fatal(err)
	}
	mcpResource, err := sourceStore.SaveResource(ctx, session.Resource{Kind: session.ResourceMCPServer, Name: "context7", Enabled: true, Config: []byte(`{"kind":"http","url":"https://mcp.context7.com/mcp"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sourceStore.SaveRun(ctx, session.Run{ID: "run_import", SessionID: sess.ID, Status: "completed", Prompt: "go", MCPServers: []string{mcpResource.ID}, EventsURL: "/api/v1/sessions/" + sess.ID + "/events"}); err != nil {
		t.Fatal(err)
	}
	exported := exportSessionForTest(t, sourceStore, sess.ID)
	targetStore := openTestStore(t)
	target, err := New(Config{Store: targetStore})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(SessionImportRequest{FormatVersion: exported.FormatVersion, RawSession: exported.RawSession, Checkpoints: exported.Checkpoints, Runs: exported.Runs, Resources: exported.Resources, NewSessionID: "sess_imported"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/import", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	target.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var imported SessionImportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	if imported.Session.ID != "sess_imported" || !imported.RewrittenSessionID || imported.OriginalSessionID != sess.ID || imported.Counts.Turns != 1 || imported.Counts.Events != 1 || imported.Counts.Checkpoints != 1 || imported.Counts.Runs != 1 || imported.Counts.Resources != 1 {
		t.Fatalf("import 응답이 이상해요: %+v", imported)
	}
	loaded, err := targetStore.LoadSession(ctx, "sess_imported")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Turns) != 1 || len(loaded.Events) != 1 || loaded.Events[0].SessionID != "sess_imported" {
		t.Fatalf("import된 session이 이상해요: %+v", loaded)
	}
	checkpoint, err := targetStore.LoadCheckpoint(ctx, "sess_imported", "cp_import")
	if err != nil || checkpoint.SessionID != "sess_imported" {
		t.Fatalf("import된 checkpoint가 이상해요: %+v err=%v", checkpoint, err)
	}
	run, err := targetStore.LoadRun(ctx, "run_import")
	if err != nil || run.SessionID != "sess_imported" || run.EventsURL != runEventsURL("run_import") {
		t.Fatalf("import된 run이 이상해요: %+v err=%v", run, err)
	}
	loadedResource, err := targetStore.LoadResource(ctx, session.ResourceMCPServer, mcpResource.ID)
	if err != nil || loadedResource.Name != "context7" {
		t.Fatalf("import된 resource가 이상해요: %+v err=%v", loadedResource, err)
	}
}

func hasResourceDTO(resources []ResourceDTO, id string) bool {
	for _, resource := range resources {
		if resource.ID == id {
			return true
		}
	}
	return false
}

func TestGatewayExportsRedactedSessionBundle(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	secret := "token=abc1234567890secretvalue"
	sess := session.NewSession("/repo/"+secret, "openai", "gpt-5-mini", "agent", session.AgentModeBuild)
	sess.Metadata = map[string]string{"note": secret}
	turn := session.NewTurn("prompt "+secret, llm.Request{Model: "gpt-5-mini", Messages: []llm.Message{llm.UserText("message " + secret)}})
	turn.Response = llm.TextResponse("openai", "gpt-5-mini", "response "+secret)
	sess.AppendTurn(turn)
	sess.AppendEvent(session.Event{ID: "ev_redact_export", SessionID: sess.ID, TurnID: turn.ID, Type: "tool", At: time.Now().UTC(), Payload: json.RawMessage(`{"value":"token=abc1234567890secretvalue"}`), Error: "error " + secret})
	sess.Todos = []session.Todo{{ID: "todo_redact_export", Content: "todo " + secret, Status: session.TodoPending, UpdatedAt: time.Now().UTC()}}
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCheckpoint(ctx, session.Checkpoint{ID: "cp_redact_export", SessionID: sess.ID, TurnID: turn.ID, CreatedAt: time.Now().UTC(), Payload: json.RawMessage(`{"summary":"token=abc1234567890secretvalue"}`)}); err != nil {
		t.Fatal(err)
	}
	resource, err := store.SaveResource(ctx, session.Resource{Kind: session.ResourceMCPServer, Name: "secret-mcp", Enabled: true, Config: []byte(`{"headers":{"Authorization":"Bearer abc1234567890secretvalue"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveRun(ctx, session.Run{ID: "run_redact_export", SessionID: sess.ID, Status: "completed", Prompt: "run " + secret, MCPServers: []string{resource.ID}, Metadata: map[string]string{"token": secret}}); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{
		Store: store,
		RunLister: func(ctx context.Context, q RunQuery) ([]RunDTO, error) {
			runs, err := store.ListRuns(ctx, session.RunQuery{SessionID: q.SessionID, Limit: q.Limit})
			if err != nil {
				return nil, err
			}
			out := make([]RunDTO, 0, len(runs))
			for _, run := range runs {
				out = append(out, *runDTOFromSession(run))
			}
			return out, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/export?redact=true", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	var exported SessionExportResponse
	if err := json.Unmarshal([]byte(body), &exported); err != nil {
		t.Fatal(err)
	}
	if !exported.Redacted || exported.RawSession != nil || !strings.Contains(body, "[REDACTED]") || strings.Contains(body, "abc1234567890secretvalue") {
		t.Fatalf("redacted export가 이상해요: %s", body)
	}
	if exported.Counts.Turns != 1 || exported.Counts.Events != 1 || exported.Counts.Checkpoints != 1 || exported.Counts.Runs != 1 || exported.Counts.Resources != 1 {
		t.Fatalf("redacted export counts가 이상해요: %+v", exported.Counts)
	}
}

func exportSessionForTest(t *testing.T, store *session.SQLiteStore, sessionID string) SessionExportResponse {
	t.Helper()
	srv, err := New(Config{
		Store: store,
		RunLister: func(ctx context.Context, q RunQuery) ([]RunDTO, error) {
			runs, err := store.ListRuns(ctx, session.RunQuery{SessionID: q.SessionID, Limit: q.Limit})
			if err != nil {
				return nil, err
			}
			out := make([]RunDTO, 0, len(runs))
			for _, run := range runs {
				out = append(out, *runDTOFromSession(run))
			}
			return out, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sessionID+"/export", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var exported SessionExportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &exported); err != nil {
		t.Fatal(err)
	}
	return exported
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
	if content.Content != "two" || content.ContentBytes != len("two") || content.FileBytes != int64(len("one\ntwo\nthree")) || !content.ContentTruncated {
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

	req = httptest.NewRequest(http.MethodGet, "/api/v1/files/content?project_root="+root+"&path=docs/b.md&max_bytes=2", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	content = FileContentResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &content); err != nil {
		t.Fatal(err)
	}
	if content.Content != "ne" || content.ContentBytes != 2 || content.FileBytes != 3 || !content.ContentTruncated {
		t.Fatalf("file content byte 제한 metadata가 이상해요: %+v", content)
	}
}

func TestGatewayFilesAPIGrepsWorkspace(t *testing.T) {
	store := openTestStore(t)
	srv := newTestServer(t, store, "")
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "src", "a.go"), "package main\n// TODO: wire api\n")
	writeTestFile(t, filepath.Join(root, "notes.txt"), "TODO outside\n")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/grep?project_root="+url.QueryEscape(root)+"&pattern=todo&path_glob=src/**&max_matches=10", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var grep FileGrepResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &grep); err != nil {
		t.Fatal(err)
	}
	if grep.ProjectRoot != root || grep.Pattern != "todo" || grep.PathGlob != "src/**" || len(grep.Matches) != 1 || grep.Matches[0].Path != "src/a.go" || grep.Matches[0].Line != 2 {
		t.Fatalf("grep 결과가 이상해요: %+v", grep)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/files/grep?project_root="+url.QueryEscape(root), nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("pattern 없는 grep은 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGatewayFilesAPIGlobsWorkspace(t *testing.T) {
	store := openTestStore(t)
	srv := newTestServer(t, store, "")
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "src", "a.go"), "package main\n")
	writeTestFile(t, filepath.Join(root, "src", "b.txt"), "notes\n")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/glob?project_root="+url.QueryEscape(root)+"&pattern=src/*.go&limit=10", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var glob FileGlobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &glob); err != nil {
		t.Fatal(err)
	}
	if glob.ProjectRoot != root || glob.Pattern != "src/*.go" || len(glob.Paths) != 1 || glob.Paths[0] != "src/a.go" || glob.TotalPaths != 1 || glob.Limit != 10 || glob.PathsTruncated {
		t.Fatalf("glob 결과가 이상해요: %+v", glob)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/files/glob?project_root="+url.QueryEscape(root)+"&pattern=src/*&limit=1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	glob = FileGlobResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &glob); err != nil {
		t.Fatal(err)
	}
	if len(glob.Paths) != 1 || glob.TotalPaths != 2 || glob.Limit != 1 || !glob.PathsTruncated {
		t.Fatalf("glob limit metadata가 이상해요: %+v", glob)
	}
}

func TestGatewayFilesAPIAppliesPatch(t *testing.T) {
	store := openTestStore(t)
	srv := newTestServer(t, store, "")
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "src", "a.txt"), "one\ntwo\nthree\n")
	patch := `*** Begin Patch
*** Update File: src/a.txt
@@
 one
-two
+patched
 three
*** End Patch
`
	body, err := json.Marshal(FilePatchRequest{ProjectRoot: root, PatchText: patch})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/patch", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp FilePatchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(filepath.Join(root, "src", "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Applied || string(updated) != "one\npatched\nthree\n" {
		t.Fatalf("patch 적용이 이상해요: resp=%+v content=%q", resp, updated)
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
