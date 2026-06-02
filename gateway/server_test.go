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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/session"
	ktools "github.com/sleepysoong/kkode/tools"
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

func TestGatewayHealthUsesTypedDTO(t *testing.T) {
	store := openTestStore(t)
	srv := newTestServer(t, store, "")
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d body = %s", rec.Code, rec.Body.String())
	}
	var health HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if !health.OK || health.Time.IsZero() {
		t.Fatalf("health DTO가 이상해요: %+v", health)
	}
}

func TestGatewayReadyChecksStoreHealth(t *testing.T) {
	store := openTestStore(t)
	srv := newReadyTestServer(t, store)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ready status = %d body = %s", rec.Code, rec.Body.String())
	}
	var ready ReadyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &ready); err != nil {
		t.Fatal(err)
	}
	if !ready.Ready || ready.Time.IsZero() {
		t.Fatalf("ready DTO가 이상해요: %+v", ready)
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

func TestGatewayReadyRejectsMissingRuntimeWiring(t *testing.T) {
	store := openTestStore(t)
	srv, err := New(Config{Store: store, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("runtime wiring 누락은 ready가 아니어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var envelope ErrorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	rawMissing, ok := envelope.Error.Details["missing_runtime_wiring"].([]any)
	if !ok {
		t.Fatalf("ready 오류 details에 missing_runtime_wiring이 필요해요: %+v", envelope.Error)
	}
	missing := map[string]bool{}
	for _, item := range rawMissing {
		name, _ := item.(string)
		missing[name] = true
	}
	if !missing["run_starter"] || !missing["run_previewer"] || !missing["run_validator"] || !missing["provider_tester"] || !missing["run_getter"] || !missing["run_lister"] || !missing["run_canceler"] || !missing["run_event_lister"] || !missing["run_subscriber"] || !missing["run_event_subscriber"] {
		t.Fatalf("ready 오류 details가 이상해요: %+v", envelope.Error.Details)
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

func TestGatewayCreatesAndListsSessions(t *testing.T) {
	store := openTestStore(t)
	srv := newTestServer(t, store, "")

	body := bytes.NewBufferString(`{"project_root":" /tmp/repo ","provider":" openai ","model":" gpt-5-mini ","agent":" web ","mode":" plan ","metadata":{"source":"test"," trace-id ":" abc ","empty":" "}}`)
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
	if created.ID == "" || created.ProjectRoot != "/tmp/repo" || created.ProviderName != "openai" || created.Model != "gpt-5-mini" || created.AgentName != "web" || created.Mode != "plan" || created.Metadata["source"] != "test" || created.Metadata["trace-id"] != "abc" || created.Metadata[" trace-id "] != "" || created.Metadata["empty"] != "" {
		t.Fatalf("unexpected session: %+v", created)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions", bytes.NewBufferString(`{"project_root":"/tmp/repo","provider":"openai","model":"gpt-5-mini","metadata":{"bad key":"value"}}`))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "metadata") {
		t.Fatalf("invalid session metadata는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions", bytes.NewBufferString(`{"project_root":"/tmp/repo","provider":"openai","model":"gpt-5-mini","mode":"debug"}`))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "mode") {
		t.Fatalf("invalid session mode는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions", bytes.NewBufferString(`{"project_root":"`+strings.Repeat("x", maxProjectRootBytes+1)+`","provider":"openai","model":"gpt-5-mini"}`))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("긴 session project_root는 더 이상 막지 않아야 해요: status=%d body=%s", rec.Code, rec.Body.String())
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
	foundCreated := false
	for _, sess := range listed.Sessions {
		if sess.ID == created.ID {
			foundCreated = true
			break
		}
	}
	if len(listed.Sessions) < 2 || !foundCreated {
		t.Fatalf("unexpected list: %+v", listed)
	}
	if listed.TotalSessions < 2 || listed.Limit != 10 || listed.Offset != 0 || listed.NextOffset != 0 || listed.ResultTruncated {
		t.Fatalf("session list metadata가 이상해요: %+v", listed)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions?project_root="+url.QueryEscape(" /tmp/repo ")+"&limit=10", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	listed = SessionListResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) != 1 || listed.Sessions[0].ID != created.ID {
		t.Fatalf("project_root filter는 canonical 값으로 동작해야 해요: %+v", listed)
	}
	extra := session.NewSession("/tmp/repo", "openai", "gpt-5-mini", "agent", session.AgentModeBuild)
	if err := store.CreateSession(context.Background(), extra); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions?provider=openai&model=gpt-5-mini&mode=build&limit=10", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	listed = SessionListResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	foundExtra := false
	for _, sess := range listed.Sessions {
		if sess.ID == extra.ID {
			foundExtra = true
			break
		}
	}
	if !foundExtra {
		t.Fatalf("session provider/model/mode filter가 이상해요: %+v", listed)
	}
	if listed.TotalSessions < 1 {
		t.Fatalf("session provider/model/mode total이 이상해요: %+v", listed)
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
	if len(listed.Sessions) != 1 || listed.TotalSessions < 2 || !listed.ResultTruncated || listed.Limit != 1 {
		t.Fatalf("session list truncation metadata가 이상해요: %+v", listed)
	}
	if listed.NextOffset != 1 {
		t.Fatalf("session next offset이 이상해요: %+v", listed)
	}
	firstPageID := listed.Sessions[0].ID
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions?limit=1&offset=1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	listed = SessionListResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) != 1 || listed.Sessions[0].ID == firstPageID {
		t.Fatalf("session offset page가 이상해요: %+v", listed)
	}
	if listed.TotalSessions < 2 || listed.Limit != 1 || listed.Offset != 1 || listed.NextOffset == 0 || !listed.ResultTruncated {
		t.Fatalf("session offset metadata가 이상해요: %+v", listed)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+strings.Repeat("x", maxSessionIDBytes+1), nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "session id") {
		t.Fatalf("긴 session path id는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	longProvider := strings.Repeat("p", maxRunProviderModelBytes+1)
	longModel := strings.Repeat("m", maxRunProviderModelBytes+1)
	for _, query := range []string{"limit=-1", "limit=abc", "offset=-1", "offset=abc", "provider=" + longProvider, "model=" + longModel, "mode=invalid"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions?"+query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("잘못된 session list query는 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
		if strings.Contains(query, "limit") && !strings.Contains(rec.Body.String(), "limit") {
			t.Fatalf("session list limit 오류는 limit을 설명해야 해요: query=%s body=%s", query, rec.Body.String())
		}
		if strings.Contains(query, "offset") && !strings.Contains(rec.Body.String(), "offset") {
			t.Fatalf("session list offset 오류는 offset을 설명해야 해요: query=%s body=%s", query, rec.Body.String())
		}
	}
}

func TestGatewayRequiresAPIKeyWhenConfigured(t *testing.T) {
	store := openTestStore(t)
	srv := newTestServer(t, store, "secret")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/providers", nil)
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
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/providers", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	req.Header.Set("Origin", "https://panel.example")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	allowedHeaders := rec.Header().Get("Access-Control-Allow-Headers")
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "https://panel.example" || !strings.Contains(allowedHeaders, RequestIDHeader) || !strings.Contains(allowedHeaders, IdempotencyKeyHeader) {
		t.Fatalf("CORS preflight가 이상해요: status=%d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/providers", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	req.Header.Set("Origin", "https://panel.example")
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	exposed := rec.Header().Get("Access-Control-Expose-Headers")
	if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "https://panel.example" || !strings.Contains(exposed, RequestIDHeader) || !strings.Contains(exposed, IdempotencyReplayHeader) {
		t.Fatalf("CORS response header가 필요해요: status=%d headers=%v", rec.Code, rec.Header())
	}
}

func TestGatewaySecurityHeaders(t *testing.T) {
	store := openTestStore(t)
	srv := newTestServer(t, store, "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("보안 header가 필요해요: status=%d headers=%v", rec.Code, rec.Header())
	}
}

func TestOpenAPIYAML_whenRequestedThroughGateway(t *testing.T) {
	store := openTestStore(t)
	srv := newTestServer(t, store, "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.yaml", nil)
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); contentType != "application/yaml; charset=utf-8" {
		t.Fatalf("content type = %q", contentType)
	}
	if !strings.Contains(rec.Body.String(), "openapi: 3.0.3") {
		t.Fatalf("openapi yaml response does not contain spec header: %s", rec.Body.String())
	}
}

func TestGatewayRequestIDHeaderAndErrorEnvelope(t *testing.T) {
	store := openTestStore(t)
	srv, err := New(Config{Store: store, Version: "test", RequestIDGenerator: func() string { return "req_test" }})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers", nil)
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

	req = httptest.NewRequest(http.MethodGet, "/api/v1/providers", nil)
	req.Header.Set(RequestIDHeader, strings.Repeat("x", maxRequestIDBytes+1))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "request_id") || len(rec.Header().Get(RequestIDHeader)) > maxRequestIDBytes {
		t.Fatalf("긴 request id는 짧은 generated id로 400이어야 해요: status=%d header=%q body=%s", rec.Code, rec.Header().Get(RequestIDHeader), rec.Body.String())
	}
}

func TestGatewayRecoverPanicBeforeWriteReturnsErrorEnvelope(t *testing.T) {
	srv, err := New(Config{Store: openTestStore(t), Version: "test", RequestIDGenerator: func() string { return "req_panic" }})
	if err != nil {
		t.Fatal(err)
	}
	handler := srv.requestIDMiddleware(srv.recoverMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("handler failed")
	})))
	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic before write should return 500: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body ErrorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "panic" || body.Error.Message != "handler failed" || body.Error.RequestID != "req_panic" {
		t.Fatalf("panic envelope is wrong: %+v", body)
	}
}

func TestGatewayRecoverPanicAfterWriteDoesNotAppendErrorEnvelope(t *testing.T) {
	srv, err := New(Config{Store: openTestStore(t), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	handler := srv.recoverMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("partial response"))
		panic("handler failed after write")
	}))
	req := httptest.NewRequest(http.MethodGet, "/panic-after-write", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("panic after write should preserve started status: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "partial response" {
		t.Fatalf("panic after write should not append error envelope: %q", rec.Body.String())
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
	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers?detail=true", nil)
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
	if entry.RequestID != "req_log" || entry.Method != http.MethodGet || entry.Path != "/api/v1/providers?detail=true" || entry.Status != http.StatusOK || entry.Bytes <= 0 || entry.Remote != "198.51.100.7:4567" || entry.UserAgent != "panel-test" {
		t.Fatalf("access log entry가 이상해요: %+v", entry)
	}
}

func TestGatewayAccessLoggerPanicDoesNotBreakRequest(t *testing.T) {
	store := openTestStore(t)
	called := false
	srv, err := New(Config{
		Store:   store,
		Version: "test",
		AccessLogger: func(entry AccessLogEntry) {
			called = true
			panic("access logger failed")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("access logger panic should not change response: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !called {
		t.Fatal("access logger should still be called")
	}
}

func TestGatewayRunStarterBoundary(t *testing.T) {
	store := openTestStore(t)
	var started RunStartRequest
	var validated RunStartRequest
	srv, err := New(Config{
		Store:             store,
		DefaultMCPServers: []ResourceDTO{{Name: "serena"}, {Name: "context7"}},
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
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewBufferString(`{"session_id":" sess_1 ","prompt":"go test","provider":" openai ","model":" gpt-5-mini ","max_output_tokens":512,"metadata":{"source":"panel"," trace-id ":" abc ","empty":" "},"mcp_servers":[" mcp_1 ","","mcp_1"],"skills":[" skill_1 ","skill_1"],"subagents":[" agent_1 ","agent_1"]}`))
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
	if run.ID != "run_test" || run.Status != "queued" || run.Metadata[RequestIDMetadataKey] != "req_run" || run.Metadata[DefaultMCPMetadataKey] != "context7,serena" || started.Metadata[RequestIDMetadataKey] != "req_run" || started.Metadata[DefaultMCPMetadataKey] != "context7,serena" || started.Metadata["source"] != "panel" || started.Metadata["trace-id"] != "abc" || started.Metadata[" trace-id "] != "" || started.Metadata["empty"] != "" {
		t.Fatalf("unexpected run: %+v", run)
	}
	if started.SessionID != "sess_1" || started.Provider != "openai" || started.Model != "gpt-5-mini" || started.MaxOutputTokens != 512 || len(started.MCPServers) != 1 || started.MCPServers[0] != "mcp_1" || len(started.Skills) != 1 || started.Skills[0] != "skill_1" || len(started.Subagents) != 1 || started.Subagents[0] != "agent_1" {
		t.Fatalf("run starter resource ids must be normalized: %+v", started)
	}
	if validated.SessionID != "sess_1" || validated.MaxOutputTokens != 512 || validated.Metadata[RequestIDMetadataKey] != "req_run" || validated.Metadata[DefaultMCPMetadataKey] != "context7,serena" || len(validated.MCPServers) != 1 || validated.MCPServers[0] != "mcp_1" {
		t.Fatalf("run validator는 enqueue 전에 같은 request metadata를 받아야 해요: %+v", validated)
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
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs?session_id=+sess_filter+&request_id=req_filter&idempotency_key=idem_filter&provider=copilot&model=gpt-5-mini&turn_id=turn_filter&limit=5&offset=10", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if query.SessionID != "sess_filter" || query.RequestID != "req_filter" || query.IdempotencyKey != "idem_filter" || query.Provider != "copilot" || query.Model != "gpt-5-mini" || query.TurnID != "turn_filter" || query.Limit != 6 || query.Offset != 10 {
		t.Fatalf("run query가 이상해요: %+v", query)
	}
	var body RunListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Runs) != 1 || body.Runs[0].Metadata[RequestIDMetadataKey] != "req_filter" {
		t.Fatalf("run list 응답이 이상해요: %+v", body)
	}
	if body.Limit != 5 || body.Offset != 10 || body.NextOffset != 0 || body.ResultTruncated {
		t.Fatalf("run list metadata가 이상해요: %+v", body)
	}
	for _, queryString := range []string{"limit=-1", "limit=abc", "offset=-1", "offset=abc", "status=paused"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/runs?"+queryString, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("잘못된 run list query는 400이어야 해요: query=%s status=%d body=%s", queryString, rec.Code, rec.Body.String())
		}
		if strings.Contains(queryString, "limit") && !strings.Contains(rec.Body.String(), "limit") {
			t.Fatalf("run list limit 오류는 limit을 설명해야 해요: query=%s body=%s", queryString, rec.Body.String())
		}
		if strings.Contains(queryString, "offset") && !strings.Contains(rec.Body.String(), "offset") {
			t.Fatalf("run list offset 오류는 offset을 설명해야 해요: query=%s body=%s", queryString, rec.Body.String())
		}
		if strings.Contains(queryString, "status") && !strings.Contains(rec.Body.String(), "status") {
			t.Fatalf("run list status 오류는 status를 설명해야 해요: query=%s body=%s", queryString, rec.Body.String())
		}
	}
	for _, tc := range []struct {
		queryString string
		want        string
	}{
		{queryString: "request_id=" + strings.Repeat("x", maxRequestIDBytes+1), want: "request_id"},
		{queryString: "idempotency_key=" + strings.Repeat("x", maxIdempotencyKeyBytes+1), want: "idempotency_key"},
		{queryString: "provider=" + strings.Repeat("x", maxRunProviderModelBytes+1), want: "provider"},
		{queryString: "model=" + strings.Repeat("x", maxRunProviderModelBytes+1), want: "model"},
		{queryString: "session_id=" + strings.Repeat("x", maxSessionIDBytes+1), want: "session_id"},
		{queryString: "session_id=bad/id", want: "session_id"},
		{queryString: "turn_id=" + strings.Repeat("x", maxRunIDBytes+1), want: "turn_id"},
		{queryString: "turn_id=bad/id", want: "turn_id"},
	} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/runs?"+tc.queryString, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("긴 run list correlation query는 400이어야 해요: query=%s status=%d body=%s", tc.queryString, rec.Code, rec.Body.String())
		}
	}
}

func TestGatewayListsRunsWithNextOffset(t *testing.T) {
	store := openTestStore(t)
	var query RunQuery
	srv, err := New(Config{
		Store: store,
		RunLister: func(ctx context.Context, q RunQuery) ([]RunDTO, error) {
			query = q
			return []RunDTO{
				{ID: "run_1", SessionID: "sess_1", Status: "completed"},
				{ID: "run_2", SessionID: "sess_1", Status: "completed"},
				{ID: "run_3", SessionID: "sess_1", Status: "completed"},
			}, nil
		},
		RunCounter: func(ctx context.Context, q RunQuery) (int, error) {
			if q.SessionID != "sess_1" {
				t.Fatalf("run count query가 이상해요: %+v", q)
			}
			return 9, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs?session_id=sess_1&limit=2&offset=4", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if query.SessionID != "sess_1" || query.Limit != 3 || query.Offset != 4 {
		t.Fatalf("run page query가 이상해요: %+v", query)
	}
	var body RunListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Runs) != 2 || body.Runs[0].ID != "run_1" || body.TotalRuns != 9 || !body.ResultTruncated || body.NextOffset != 6 {
		t.Fatalf("run list page metadata가 이상해요: %+v", body)
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

func newReadyTestServer(t *testing.T, store session.Store) *Server {
	t.Helper()
	srv, err := New(Config{
		Store:     store,
		Version:   "test",
		Providers: []ProviderDTO{{Name: "openai"}},
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
		RunGetter: func(ctx context.Context, runID string) (*RunDTO, error) {
			return &RunDTO{ID: runID}, nil
		},
		RunLister: func(ctx context.Context, q RunQuery) ([]RunDTO, error) {
			return nil, nil
		},
		RunCanceler: func(ctx context.Context, runID string) (*RunDTO, error) {
			return &RunDTO{ID: runID, Status: "cancelled"}, nil
		},
		RunEventLister: func(ctx context.Context, runID string, afterSeq int, eventType string, limit int) ([]RunEventDTO, error) {
			return nil, nil
		},
		RunSubscriber: func(ctx context.Context, runID string) (<-chan RunDTO, func()) {
			ch := make(chan RunDTO)
			close(ch)
			return ch, func() {}
		},
		RunEventSubscriber: func(ctx context.Context, runID string) (<-chan RunEventDTO, func()) {
			ch := make(chan RunEventDTO)
			close(ch)
			return ch, func() {}
		},
	})
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
	req = httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+strings.Repeat("x", maxRunIDBytes+1), nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "run id") {
		t.Fatalf("긴 run detail id는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
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

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
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
	if listed.TotalModels != 2 || listed.Limit != 2 || listed.Offset != 0 || listed.NextOffset != 0 || listed.ResultTruncated {
		t.Fatalf("openai model 목록 metadata가 이상해요: %+v", listed)
	}
	if listed.Models[0].Provider != "openai" || listed.Models[0].ID != "gpt-5-mini" || !listed.Models[0].Default || listed.Models[0].AuthStatus != "configured" {
		t.Fatalf("기본 model discovery가 이상해요: %+v", listed.Models[0])
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/models?provider=openai&limit=1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("model page status = %d body = %s", rec.Code, rec.Body.String())
	}
	var page ModelListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Models) != 1 || page.Models[0].ID != "gpt-5-mini" || page.TotalModels != 2 || page.Limit != 1 || page.Offset != 0 || page.NextOffset != 1 || !page.ResultTruncated {
		t.Fatalf("model page가 이상해요: %+v", page)
	}
	for _, query := range []string{"limit=-1", "limit=abc", "offset=-1", "offset=abc", "provider=" + strings.Repeat("x", maxRunProviderModelBytes+1)} {
		target := "/api/v1/models?provider=openai&" + query
		if strings.HasPrefix(query, "provider=") {
			target = "/api/v1/models?" + query
		}
		req = httptest.NewRequest(http.MethodGet, target, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("잘못된 model list query는 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
		if strings.Contains(query, "limit") && !strings.Contains(rec.Body.String(), "limit") {
			t.Fatalf("model list limit 오류는 limit을 설명해야 해요: query=%s body=%s", query, rec.Body.String())
		}
		if strings.Contains(query, "offset") && !strings.Contains(rec.Body.String(), "offset") {
			t.Fatalf("model list offset 오류는 offset을 설명해야 해요: query=%s body=%s", query, rec.Body.String())
		}
		if strings.Contains(query, "provider") && !strings.Contains(rec.Body.String(), "provider") {
			t.Fatalf("model list provider 오류는 provider를 설명해야 해요: query=%s body=%s", query, rec.Body.String())
		}
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
	openaiProvider, ok := findProvider(providers.Providers, "openai")
	if !ok {
		t.Fatalf("openai provider discovery가 필요해요: %+v", providers.Providers)
	}
	if openaiProvider.Conversion == nil || openaiProvider.Conversion.Source != "http-json+sse" || openaiProvider.Conversion.Operations[0] != "responses.create" {
		t.Fatalf("provider 변환 profile discovery가 필요해요: %+v", openaiProvider)
	}
	if providers.TotalProviders != 2 || providers.Limit != 2 || providers.Offset != 0 || providers.NextOffset != 0 || providers.ResultTruncated {
		t.Fatalf("provider 목록 metadata가 이상해요: %+v", providers)
	}
	if len(openaiProvider.Aliases) != 1 || openaiProvider.Aliases[0] != "openai-compatible" {
		t.Fatalf("provider alias discovery가 필요해요: %+v", openaiProvider)
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
	req = httptest.NewRequest(http.MethodGet, "/api/v1/providers/"+strings.Repeat("x", maxRunProviderModelBytes+1), nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "provider") {
		t.Fatalf("긴 provider detail name은 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	var testedProvider string
	var testedProviderReq ProviderTestRequest
	srv, err = New(Config{
		Store:     store,
		Providers: []ProviderDTO{{Name: "openai", Aliases: []string{"openai-compatible"}}},
		ProviderTester: func(ctx context.Context, provider string, req ProviderTestRequest) (*ProviderTestResponse, error) {
			testedProvider = provider
			testedProviderReq = req
			return &ProviderTestResponse{OK: true, Provider: "openai", Model: req.Model, Message: "ok", ProviderRequest: &ProviderRequestPreviewDTO{Provider: "openai", Operation: "responses.create"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/providers/openai-compatible/test", strings.NewReader(`{"model":" gpt-5-mini ","prompt":"ping","metadata":{" trace-id ":" abc ","empty":" "}}`))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("provider test status = %d body = %s", rec.Code, rec.Body.String())
	}
	var testResp ProviderTestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &testResp); err != nil {
		t.Fatal(err)
	}
	if testedProvider != "openai-compatible" || !testResp.OK || testResp.Model != "gpt-5-mini" || testResp.ProviderRequest == nil || testResp.ProviderRequest.Operation != "responses.create" {
		t.Fatalf("provider test 응답이 이상해요: provider=%s resp=%+v", testedProvider, testResp)
	}
	if testedProviderReq.Metadata["trace-id"] != "abc" || testedProviderReq.Metadata[" trace-id "] != "" || testedProviderReq.Metadata["empty"] != "" {
		t.Fatalf("provider test metadata 정규화가 필요해요: %+v", testedProviderReq.Metadata)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/providers/"+strings.Repeat("x", maxRunProviderModelBytes+1)+"/test", strings.NewReader(`{}`))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "provider") {
		t.Fatalf("긴 provider test name은 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/providers/openai-compatible/test", strings.NewReader(`{"model":"gpt-5-mini","unknown":true}`))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("provider test unknown field는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var errBody ErrorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
		t.Fatal(err)
	}
	if errBody.Error.Code != "invalid_json" {
		t.Fatalf("provider test JSON 오류는 표준 invalid_json이어야 해요: %+v", errBody)
	}

	invalidProviderTests := []struct {
		name  string
		body  string
		field string
	}{
		{name: "max preview bytes", body: `{"max_preview_bytes":-1}`, field: "max_preview_bytes"},
		{name: "large max preview bytes", body: `{"max_preview_bytes":` + strconv.Itoa(MaxProviderTestPreviewBytes+1) + `}`, field: "max_preview_bytes"},
		{name: "max output tokens", body: `{"max_output_tokens":-1}`, field: "max_output_tokens"},
		{name: "large max output tokens", body: `{"max_output_tokens":` + strconv.Itoa(MaxProviderTestOutputTokens+1) + `}`, field: "max_output_tokens"},
		{name: "max result bytes", body: `{"max_result_bytes":-1}`, field: "max_result_bytes"},
		{name: "large max result bytes", body: `{"max_result_bytes":` + strconv.Itoa(MaxProviderTestResultBytes+1) + `}`, field: "max_result_bytes"},
		{name: "timeout", body: `{"timeout_ms":-1}`, field: "timeout_ms"},
		{name: "large timeout", body: `{"timeout_ms":` + strconv.Itoa(MaxProviderTestTimeoutMS+1) + `}`, field: "timeout_ms"},
		{name: "metadata", body: `{"metadata":{"bad key":"value"}}`, field: "metadata"},
	}
	for _, tc := range invalidProviderTests {
		req = httptest.NewRequest(http.MethodPost, "/api/v1/providers/openai-compatible/test", strings.NewReader(tc.body))
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("provider test %s invalid request는 400이어야 해요: status=%d body=%s", tc.name, rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
			t.Fatal(err)
		}
		if errBody.Error.Code != "invalid_provider_test" || !strings.Contains(errBody.Error.Message, tc.field) {
			t.Fatalf("provider test %s 오류가 이상해요: %+v", tc.name, errBody)
		}
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/providers/openai-compatible/test", strings.NewReader(``))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("provider test 빈 body는 기본 요청으로 처리해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/providers/missing", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("없는 provider는 404여야 해요: status=%d body=%s", rec.Code, rec.Body.String())
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
	secret := "token=abc1234567890secretvalue"
	run := RunDTO{ID: "run_stream", SessionID: "sess_1", Status: "running", Prompt: "run " + secret, EventsURL: runEventsURL("run_stream"), Metadata: map[string]string{"token": secret}, ContextBlocks: []string{"context " + secret}}
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
	bus.Publish(RunDTO{ID: "run_stream", SessionID: "sess_1", Status: "completed", Prompt: "done " + secret, Metadata: map[string]string{"token": secret}})
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
	if strings.Contains(body, "abc1234567890secretvalue") || !strings.Contains(body, "[REDACTED]") {
		t.Fatalf("run SSE는 run snapshot secret을 숨겨야 해요: %s", body)
	}
}

func TestGatewayRunEventsRejectInvalidAfterSeq(t *testing.T) {
	store := openTestStore(t)
	run := RunDTO{ID: "run_after_seq", SessionID: "sess_1", Status: "completed", EventsURL: runEventsURL("run_after_seq")}
	var gotEventType string
	srv, err := New(Config{
		Store: store,
		RunGetter: func(ctx context.Context, runID string) (*RunDTO, error) {
			copy := run
			return &copy, nil
		},
		RunEventLister: func(ctx context.Context, runID string, afterSeq int, eventType string, limit int) ([]RunEventDTO, error) {
			gotEventType = eventType
			return []RunEventDTO{{Seq: 1, At: time.Now().UTC(), Type: "run.completed", Run: run}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run_after_seq/events?type=run.completed&limit=10", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("type filter status = %d body = %s", rec.Code, rec.Body.String())
	}
	if gotEventType != "run.completed" {
		t.Fatalf("run event type이 run event lister에 전달되지 않았어요: %q", gotEventType)
	}
	for _, tc := range []struct {
		name  string
		query string
	}{
		{name: "negative", query: "after_seq=-1"},
		{name: "malformed", query: "after_seq=abc"},
		{name: "negative limit", query: "limit=-1"},
		{name: "malformed limit", query: "limit=abc"},
		{name: "malformed stream", query: "stream=maybe"},
		{name: "long type", query: "type=" + strings.Repeat("x", maxSessionEventTypeBytes+1)},
	} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/runs/run_after_seq/events?"+tc.query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		want := strings.Split(tc.query, "=")[0]
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("%s run event query는 400이어야 해요: status=%d body=%s", tc.name, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Header().Get("Content-Type"), "text/event-stream") {
			t.Fatalf("%s run event query는 SSE stream을 열기 전에 반환해야 해요: header=%s", tc.name, rec.Header().Get("Content-Type"))
		}
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

func TestGatewayRunSSERejectsInvalidHeartbeat(t *testing.T) {
	store := openTestStore(t)
	run := RunDTO{ID: "run_bad_heartbeat", SessionID: "sess_1", Status: "running", EventsURL: runEventsURL("run_bad_heartbeat")}
	srv, err := New(Config{
		Store: store,
		RunGetter: func(ctx context.Context, runID string) (*RunDTO, error) {
			copy := run
			return &copy, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"heartbeat_ms=-1", "heartbeat_ms=abc"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run_bad_heartbeat/events?stream=true&"+query, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "heartbeat_ms") {
			t.Fatalf("잘못된 run SSE heartbeat_ms는 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Header().Get("Content-Type"), "text/event-stream") {
			t.Fatalf("heartbeat_ms 오류는 SSE stream을 열기 전에 반환해야 해요: query=%s header=%s", query, rec.Header().Get("Content-Type"))
		}
	}
}

func TestGatewayRunSSEStreamsProgressEvents(t *testing.T) {
	store := openTestStore(t)
	bus := NewRunEventBus()
	secret := "token=abc1234567890secretvalue"
	run := RunDTO{ID: "run_progress", SessionID: "sess_1", Status: "running", Prompt: "run " + secret, EventsURL: runEventsURL("run_progress")}
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
	bus.PublishEvent(RunEventDTO{Seq: 2, At: time.Now().UTC(), Type: "tool.completed", Tool: "file_read", Message: "ok " + secret, Error: "err " + secret, Payload: json.RawMessage(`{"value":"token=abc1234567890secretvalue"}`), Run: run})
	bus.PublishEvent(RunEventDTO{Seq: 3, At: time.Now().UTC(), Type: "run.completed", Run: RunDTO{ID: "run_progress", SessionID: "sess_1", Status: "completed", Prompt: "done " + secret}})
	select {
	case <-done:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("progress SSE가 종료되지 않았어요")
	}
	body := rec.BodyString()
	if !strings.Contains(body, "event: tool.completed") || !strings.Contains(body, `"tool":"file_read"`) || !strings.Contains(body, `"message":"ok [REDACTED]"`) {
		t.Fatalf("run progress SSE body가 이상해요: %s", body)
	}
	if strings.Contains(body, "abc1234567890secretvalue") {
		t.Fatalf("run progress SSE는 secret을 숨겨야 해요: %s", body)
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
	run := session.Run{ID: "run_transcript", SessionID: sess.ID, TurnID: turn.ID, Status: "completed", Prompt: "run " + secret, Provider: "openai", Model: "gpt-5-mini", ContextBlocks: []string{"context " + secret}}
	if _, _, err := store.SaveRunWithEvent(ctx, run, session.RunEvent{RunID: run.ID, Type: "tool.completed", Tool: "file_read", Message: "message " + secret, At: time.Now().UTC(), Run: run}); err != nil {
		t.Fatal(err)
	}
	manager := NewAsyncRunManagerWithStore(nil, store)
	srv, err := New(Config{Store: store, RunGetter: manager.Get, RunEventLister: manager.Events})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run_transcript/transcript?max_markdown_bytes=48", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got RunTranscriptResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Run.ID != "run_transcript" || got.Turn == nil || got.Turn.ID != turn.ID || len(got.Events) != 1 || len(got.RunEvents) != 1 || got.MarkdownBytes <= len(got.Markdown) || !got.MarkdownTruncated {
		t.Fatalf("run transcript 응답이 이상해요: %+v", got)
	}
	body := rec.Body.String()
	if !got.Redacted || !strings.Contains(body, "[REDACTED]") || strings.Contains(body, secret) {
		t.Fatalf("run transcript는 기본 redaction을 적용해야 해요: %s", body)
	}
	if len(got.Run.ContextBlocks) != 1 || !strings.Contains(got.Run.ContextBlocks[0], "[REDACTED]") {
		t.Fatalf("run transcript context_blocks redaction이 이상해요: %+v", got.Run.ContextBlocks)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/runs/run_transcript/transcript?max_markdown_bytes=-1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_markdown_bytes") {
		t.Fatalf("음수 run transcript max_markdown_bytes는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, query := range []string{"event_limit=-1", "event_limit=abc", "redact=maybe"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/runs/run_transcript/transcript?"+query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		want := strings.Split(query, "=")[0]
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("잘못된 run transcript query는 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
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
		RunEventLister: func(ctx context.Context, runID string, afterSeq int, eventType string, limit int) ([]RunEventDTO, error) {
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
	original := RunDTO{ID: "run_old", SessionID: "sess_1", Status: "failed", Prompt: "go test", Provider: "copilot", Model: "gpt-5-mini", MaxOutputTokens: 333, MCPServers: []string{"mcp_1"}, Skills: []string{"skill_1"}, Subagents: []string{"agent_1"}, ContextBlocks: []string{"adapter context"}, Metadata: map[string]string{"source": "discord"}}
	var retryReq RunStartRequest
	srv, err := New(Config{
		Store:             store,
		DefaultMCPServers: []ResourceDTO{{Name: "context7"}},
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
	if retried.ID != "run_new" || retryReq.Metadata["retried_from"] != "run_old" || retryReq.Metadata["source"] != "discord" || retryReq.Metadata[RequestIDMetadataKey] != "req_retry" || retryReq.Metadata[DefaultMCPMetadataKey] != "context7" {
		t.Fatalf("retry run이 이상해요: run=%+v req=%+v", retried, retryReq)
	}
	if retryReq.Provider != "copilot" || retryReq.Model != "gpt-5-mini" || retryReq.MaxOutputTokens != 333 || len(retryReq.MCPServers) != 1 || retryReq.MCPServers[0] != "mcp_1" || len(retryReq.Skills) != 1 || retryReq.Skills[0] != "skill_1" || len(retryReq.Subagents) != 1 || retryReq.Subagents[0] != "agent_1" || len(retryReq.ContextBlocks) != 1 || retryReq.ContextBlocks[0] != "adapter context" {
		t.Fatalf("retry가 실행 옵션을 보존해야 해요: %+v", retryReq)
	}
}

func TestReadLimitedBodyRejectsOversizedHTTPMCPResponse(t *testing.T) {
	data, err := ktools.ReadLimitedMCPBody(strings.NewReader("12345"), 5)
	if err != nil || string(data) != "12345" {
		t.Fatalf("제한 안의 HTTP MCP body는 읽어야 해요: data=%q err=%v", data, err)
	}
	if _, err := ktools.ReadLimitedMCPBody(strings.NewReader("123456"), 5); err == nil || !strings.Contains(err.Error(), "너무 커요") {
		t.Fatalf("제한을 넘는 HTTP MCP body는 거부해야 해요: %v", err)
	}
}

func TestBoundedBufferPreservesUTF8(t *testing.T) {
	var buf boundedBuffer
	buf.max = 4
	written, err := buf.Write([]byte("가나다"))
	if err != nil {
		t.Fatal(err)
	}
	if written != len([]byte("가나다")) || !buf.truncated {
		t.Fatalf("bounded buffer write 결과가 이상해요: written=%d truncated=%v", written, buf.truncated)
	}
	if got := buf.String(); got != "가" || !utf8.ValidString(got) {
		t.Fatalf("bounded buffer는 UTF-8 문자를 중간에서 반환하면 안 돼요: %q", got)
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

func checkpointIDFromToolOutput(t *testing.T, output string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "checkpoint_id:") {
			id := strings.TrimSpace(strings.TrimPrefix(line, "checkpoint_id:"))
			if id != "" {
				return id
			}
		}
	}
	t.Fatalf("tool output did not include checkpoint_id: %q", output)
	return ""
}

func hasResourceDTO(resources []ResourceDTO, id string) bool {
	for _, resource := range resources {
		if resource.ID == id {
			return true
		}
	}
	return false
}

func quotedStringList(prefix string, count int) string {
	items := make([]string, 0, count)
	for i := 0; i < count; i++ {
		items = append(items, fmt.Sprintf("%q", fmt.Sprintf("%s_%d", prefix, i)))
	}
	return strings.Join(items, ",")
}

func quotedStringMap(keyPrefix string, value string, count int) string {
	items := make([]string, 0, count)
	for i := 0; i < count; i++ {
		items = append(items, fmt.Sprintf("%q:%q", fmt.Sprintf("%s%d", keyPrefix, i), value))
	}
	return strings.Join(items, ",")
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
