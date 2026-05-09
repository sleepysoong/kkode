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
	"github.com/sleepysoong/kkode/providers/httpjson"
	"github.com/sleepysoong/kkode/session"
	ktools "github.com/sleepysoong/kkode/tools"
	"github.com/sleepysoong/kkode/workspace"
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
	operations := map[string]APIIndexOperationDTO{}
	for _, op := range index.Operations {
		operations[op.Name] = op
	}
	for name, want := range map[string]APIIndexOperationDTO{
		"session_create":          {Name: "session_create", Method: "POST", Path: "/api/v1/sessions"},
		"file_write":              {Name: "file_write", Method: "PUT", Path: "/api/v1/files/content"},
		"file_move":               {Name: "file_move", Method: "POST", Path: "/api/v1/files/move"},
		"file_restore":            {Name: "file_restore", Method: "POST", Path: "/api/v1/files/restore"},
		"file_checkpoints_prune":  {Name: "file_checkpoints_prune", Method: "POST", Path: "/api/v1/files/checkpoints/prune"},
		"file_checkpoint_delete":  {Name: "file_checkpoint_delete", Method: "DELETE", Path: "/api/v1/files/checkpoints/{checkpoint_id}"},
		"session_artifact_create": {Name: "session_artifact_create", Method: "POST", Path: "/api/v1/sessions/{session_id}/artifacts"},
		"session_artifacts_prune": {Name: "session_artifacts_prune", Method: "POST", Path: "/api/v1/sessions/{session_id}/artifacts/prune"},
		"artifact_delete":         {Name: "artifact_delete", Method: "DELETE", Path: "/api/v1/artifacts/{artifact_id}"},
		"run_cancel":              {Name: "run_cancel", Method: "POST", Path: "/api/v1/runs/{run_id}/cancel"},
		"lsp_hover":               {Name: "lsp_hover", Method: "GET", Path: "/api/v1/lsp/hover"},
	} {
		if operations[name] != want {
			t.Fatalf("API index operation %s = %+v, want %+v", name, operations[name], want)
		}
	}
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

func TestGatewayMethodNotAllowedIncludesAllowHeader(t *testing.T) {
	store := openTestStore(t)
	srv := newTestServer(t, store, "")
	cases := []struct {
		name   string
		method string
		path   string
		allow  string
	}{
		{name: "health", method: http.MethodPost, path: "/healthz", allow: "GET"},
		{name: "sessions collection", method: http.MethodPatch, path: "/api/v1/sessions", allow: "GET, POST"},
		{name: "files content", method: http.MethodDelete, path: "/api/v1/files/content", allow: "GET, PUT"},
		{name: "files delete", method: http.MethodGet, path: "/api/v1/files/delete", allow: "POST"},
		{name: "files move", method: http.MethodGet, path: "/api/v1/files/move", allow: "POST"},
		{name: "tools collection", method: http.MethodPost, path: "/api/v1/tools", allow: "GET"},
		{name: "tool call", method: http.MethodGet, path: "/api/v1/tools/call", allow: "POST"},
		{name: "prompt render", method: http.MethodGet, path: "/api/v1/prompts/default/render", allow: "POST"},
		{name: "todo item", method: http.MethodPatch, path: "/api/v1/sessions/sess_1/todos/todo_1", allow: "DELETE"},
		{name: "checkpoint item", method: http.MethodPatch, path: "/api/v1/sessions/sess_1/checkpoints/cp_1", allow: "GET"},
		{name: "artifact item", method: http.MethodPatch, path: "/api/v1/artifacts/artifact_1", allow: "GET, DELETE"},
		{name: "session detail", method: http.MethodPost, path: "/api/v1/sessions/sess_1", allow: "GET"},
		{name: "session fork", method: http.MethodGet, path: "/api/v1/sessions/sess_1/fork", allow: "POST"},
		{name: "run preview", method: http.MethodGet, path: "/api/v1/runs/preview", allow: "POST"},
		{name: "run cancel", method: http.MethodGet, path: "/api/v1/runs/run_1/cancel", allow: "POST"},
		{name: "mcp tool call", method: http.MethodGet, path: "/api/v1/mcp/servers/default/tools/hello/call", allow: "POST"},
		{name: "skill preview", method: http.MethodPost, path: "/api/v1/skills/default/preview", allow: "GET"},
		{name: "subagent preview", method: http.MethodPost, path: "/api/v1/subagents/default/preview", allow: "GET"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Allow"); got != tc.allow {
				t.Fatalf("Allow = %q, want %q", got, tc.allow)
			}
			var body ErrorEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != "method_not_allowed" {
				t.Fatalf("error code = %q", body.Error.Code)
			}
		})
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
	runStartedAt := time.Unix(200, 0).UTC()
	run, err := store.SaveRun(ctx, session.Run{ID: "run_stats", SessionID: sess.ID, Status: "running", Prompt: "go", Provider: "copilot", Model: "gpt-5-mini", StartedAt: runStartedAt, EndedAt: runStartedAt.Add(1200 * time.Millisecond), Usage: llm.Usage{InputTokens: 5, OutputTokens: 4, TotalTokens: 9, ReasoningTokens: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendRunEvent(ctx, session.RunEvent{ID: "run_ev_stats", RunID: run.ID, Type: "run.running", Message: "started", Run: run}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTodos(ctx, sess.ID, []session.Todo{{ID: "todo_stats", Content: "status", Status: session.TodoPending, UpdatedAt: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCheckpoint(ctx, session.Checkpoint{ID: "cp_stats", SessionID: sess.ID, TurnID: turn.ID, Payload: json.RawMessage(`{"summary":"checkpoint"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveArtifact(ctx, session.Artifact{ID: "artifact_stats", SessionID: sess.ID, TurnID: turn.ID, Kind: "tool_output", Content: json.RawMessage(`{"text":"ok"}`)}); err != nil {
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
	if stats.Sessions != 1 || stats.SessionsByProvider["openai"] != 1 || stats.SessionsByModel["gpt-5-mini"] != 1 || stats.SessionsByMode[string(session.AgentModeBuild)] != 1 || stats.Turns != 1 || stats.Events != 1 || stats.EventsByType["turn.completed"] != 1 || stats.RunEvents != 1 || stats.RunEventsByType["run.running"] != 1 || stats.Todos != 1 || stats.TodosByStatus[string(session.TodoPending)] != 1 || stats.Checkpoints != 1 || stats.Artifacts != 1 || stats.ArtifactsByKind["tool_output"] != 1 || stats.ArtifactBytes != 13 || stats.ArtifactBytesByKind["tool_output"] != 13 || stats.TotalRuns != 1 || stats.Runs["running"] != 1 || stats.RunsByProvider["copilot"] != 1 || stats.RunsByModel["gpt-5-mini"] != 1 || stats.RunDuration.Count != 1 || stats.RunDuration.SumMS != 1200 || stats.RunDuration.AvgMS != 1200 || stats.RunDuration.MaxMS != 1200 || stats.RunDuration.P95MS != 1200 || stats.RunDurationByProvider["copilot"].P95MS != 1200 || stats.RunDurationByModel["gpt-5-mini"].P95MS != 1200 || stats.RunUsage.TotalTokens != 9 || stats.RunUsage.ReasoningTokens != 2 || stats.RunUsageByProvider["copilot"].TotalTokens != 9 || stats.RunUsageByModel["gpt-5-mini"].TotalTokens != 9 || stats.TotalResources != 1 || stats.Resources[string(session.ResourceMCPServer)] != 1 || stats.ResourcesByEnabled["disabled"] != 1 {
		t.Fatalf("stats 응답이 이상해요: %+v", stats)
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
	if listed.Limit != 10 || listed.Offset != 0 || listed.NextOffset != 0 || listed.ResultTruncated {
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
	if len(listed.Sessions) != 1 || listed.Sessions[0].ID != extra.ID {
		t.Fatalf("session provider/model/mode filter가 이상해요: %+v", listed)
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
	if listed.Limit != 1 || listed.Offset != 1 || listed.NextOffset != 0 || listed.ResultTruncated {
		t.Fatalf("session offset metadata가 이상해요: %+v", listed)
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

func TestGatewayForkSessionRejectsMissingTurn(t *testing.T) {
	store := openTestStore(t)
	sess := session.NewSession("/tmp/repo", "openai", "gpt-5-mini", "agent", session.AgentModeBuild)
	turn := session.NewTurn("첫 요청", llm.Request{Model: "gpt-5-mini"})
	sess.AppendTurn(turn)
	if err := store.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, store, "")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/fork", bytes.NewBufferString(`{"at_turn_id":"turn_missing"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var errBody ErrorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
		t.Fatal(err)
	}
	if errBody.Error.Code != "fork_session_failed" {
		t.Fatalf("fork error envelope가 이상해요: %+v", errBody)
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

	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/events?type=tool.output&limit=10", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	events = EventListResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}
	if len(events.Events) != 1 || events.Events[0].Seq != 2 || events.Events[0].Type != "tool.output" || events.ResultTruncated {
		t.Fatalf("event type filter가 이상해요: %+v", events)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/events?after_seq=-1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "after_seq") {
		t.Fatalf("음수 event after_seq는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/events?after_seq=abc", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "after_seq") {
		t.Fatalf("잘못된 event after_seq는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	for _, query := range []string{"limit=-1", "limit=abc", "stream=maybe"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/events?"+query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		want := strings.Split(query, "=")[0]
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("잘못된 event query는 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Header().Get("Content-Type"), "text/event-stream") {
			t.Fatalf("잘못된 event query는 SSE stream을 열기 전에 반환해야 해요: query=%s header=%s", query, rec.Header().Get("Content-Type"))
		}
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

func TestGatewayVersionUsesTypedDTOAndStableProviderOrder(t *testing.T) {
	store := openTestStore(t)
	srv, err := New(Config{
		Store:     store,
		Version:   "v-test",
		Commit:    "abc123",
		Providers: []ProviderDTO{{Name: "omniroute"}, {Name: "copilot"}, {Name: "openai"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var body VersionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Version != "v-test" || body.Commit != "abc123" || strings.Join(body.Providers, ",") != "copilot,omniroute,openai" {
		t.Fatalf("version DTO가 안정적으로 노출돼야 해요: %+v", body)
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
	allowedHeaders := rec.Header().Get("Access-Control-Allow-Headers")
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "https://panel.example" || !strings.Contains(allowedHeaders, RequestIDHeader) || !strings.Contains(allowedHeaders, IdempotencyKeyHeader) {
		t.Fatalf("CORS preflight가 이상해요: status=%d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
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
	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("보안 header가 필요해요: status=%d headers=%v", rec.Code, rec.Header())
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

	req = httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
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
	req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
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

func TestGatewayRejectsInvalidRunMetadata(t *testing.T) {
	store := openTestStore(t)
	started := false
	previewed := false
	validated := false
	srv, err := New(Config{
		Store: store,
		RunValidator: func(ctx context.Context, req RunStartRequest) error {
			validated = true
			return nil
		},
		RunStarter: func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
			started = true
			return &RunDTO{ID: "run_bad_metadata", SessionID: req.SessionID, Status: "queued"}, nil
		},
		RunPreviewer: func(ctx context.Context, req RunStartRequest) (*RunPreviewResponse, error) {
			previewed = true
			return &RunPreviewResponse{SessionID: req.SessionID, Provider: "openai", Model: "gpt-5-mini"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"session_id":"sess_1","prompt":"go test","metadata":{"bad key":"value"}}`
	for _, tc := range []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{name: "start", method: http.MethodPost, path: "/api/v1/runs", wantStatus: http.StatusBadRequest},
		{name: "preview", method: http.MethodPost, path: "/api/v1/runs/preview", wantStatus: http.StatusBadRequest},
		{name: "validate", method: http.MethodPost, path: "/api/v1/runs/validate", wantStatus: http.StatusOK},
	} {
		req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), "metadata") {
			t.Fatalf("%s invalid metadata 응답이 이상해요: status=%d body=%s", tc.name, rec.Code, rec.Body.String())
		}
		if tc.name == "validate" {
			var got RunValidateResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.OK || got.Code != "invalid_run" {
				t.Fatalf("validate invalid metadata 응답이 이상해요: %+v", got)
			}
		}
	}
	if started || previewed || validated {
		t.Fatalf("invalid metadata는 runtime hook 전에 거부돼야 해요: started=%v previewed=%v validated=%v", started, previewed, validated)
	}

	body = `{"session_id":"sess_1","prompt":"go test","metadata":{"idempotency_key":"` + strings.Repeat("x", maxIdempotencyKeyBytes+1) + `"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/validate", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "idempotency_key") {
		t.Fatalf("긴 idempotency metadata는 validate에서 invalid_run이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got RunValidateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OK || got.Code != "invalid_run" {
		t.Fatalf("긴 idempotency metadata validate 응답이 이상해요: %+v", got)
	}
}

func TestGatewayRejectsInvalidRunRequestShape(t *testing.T) {
	store := openTestStore(t)
	started := false
	previewed := false
	validated := false
	srv, err := New(Config{
		Store: store,
		RunValidator: func(ctx context.Context, req RunStartRequest) error {
			validated = true
			return nil
		},
		RunStarter: func(ctx context.Context, req RunStartRequest) (*RunDTO, error) {
			started = true
			return &RunDTO{ID: "run_bad_shape", SessionID: req.SessionID, Status: "queued"}, nil
		},
		RunPreviewer: func(ctx context.Context, req RunStartRequest) (*RunPreviewResponse, error) {
			previewed = true
			return &RunPreviewResponse{SessionID: req.SessionID, Provider: "openai", Model: "gpt-5-mini"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"session_id":"sess_1","prompt":"go test","mcp_servers":["` + strings.Repeat("x", maxRunSelectorItemBytes+1) + `"]}`
	for _, tc := range []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{name: "start", method: http.MethodPost, path: "/api/v1/runs", wantStatus: http.StatusBadRequest},
		{name: "preview", method: http.MethodPost, path: "/api/v1/runs/preview", wantStatus: http.StatusBadRequest},
		{name: "validate", method: http.MethodPost, path: "/api/v1/runs/validate", wantStatus: http.StatusOK},
	} {
		req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), "mcp_servers[0]") {
			t.Fatalf("%s invalid run shape 응답이 이상해요: status=%d body=%s", tc.name, rec.Code, rec.Body.String())
		}
		if tc.name == "validate" {
			var got RunValidateResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.OK || got.Code != "invalid_run" {
				t.Fatalf("validate invalid run shape 응답이 이상해요: %+v", got)
			}
		}
	}
	if started || previewed || validated {
		t.Fatalf("invalid run shape은 runtime hook 전에 거부돼야 해요: started=%v previewed=%v validated=%v", started, previewed, validated)
	}
}

func TestGatewayValidatesRunWithoutStarting(t *testing.T) {
	store := openTestStore(t)
	started := false
	var validated RunStartRequest
	srv, err := New(Config{
		Store:             store,
		DefaultMCPServers: []ResourceDTO{{Name: "context7"}},
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
	if !got.OK || got.RequestID != "req_validate" || got.IdempotencyKey != "idem_validate" || got.RunID == "" || got.ExistingRun == nil || got.ExistingRun.ID != "run_existing" || got.Metadata[DefaultMCPMetadataKey] != "context7" || validated.Metadata[RequestIDMetadataKey] != "req_validate" || validated.Metadata[IdempotencyMetadataKey] != "idem_validate" || validated.Metadata[DefaultMCPMetadataKey] != "context7" {
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
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs?request_id=req_filter&idempotency_key=idem_filter&provider=copilot&model=gpt-5-mini&turn_id=turn_filter&limit=5&offset=10", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if query.RequestID != "req_filter" || query.IdempotencyKey != "idem_filter" || query.Provider != "copilot" || query.Model != "gpt-5-mini" || query.TurnID != "turn_filter" || query.Limit != 6 || query.Offset != 10 {
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
	for _, queryString := range []string{"limit=-1", "limit=abc", "offset=-1", "offset=abc"} {
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
	}
	for _, tc := range []struct {
		queryString string
		want        string
	}{
		{queryString: "request_id=" + strings.Repeat("x", maxRequestIDBytes+1), want: "request_id"},
		{queryString: "idempotency_key=" + strings.Repeat("x", maxIdempotencyKeyBytes+1), want: "idempotency_key"},
		{queryString: "provider=" + strings.Repeat("x", maxRunProviderModelBytes+1), want: "provider"},
		{queryString: "model=" + strings.Repeat("x", maxRunProviderModelBytes+1), want: "model"},
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
	if len(body.Runs) != 2 || body.Runs[0].ID != "run_1" || !body.ResultTruncated || body.NextOffset != 6 {
		t.Fatalf("run list page metadata가 이상해요: %+v", body)
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
	req := httptest.NewRequest(http.MethodGet, "/api/v1/requests/req_filter/runs?provider=copilot&model=gpt-5-mini&turn_id=turn_filter&limit=7&offset=3", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if query.RequestID != "req_filter" || query.Provider != "copilot" || query.Model != "gpt-5-mini" || query.TurnID != "turn_filter" || query.Limit != 8 || query.Offset != 3 {
		t.Fatalf("request correlation query가 이상해요: %+v", query)
	}
	var body RequestCorrelationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.RequestID != "req_filter" || body.Offset != 3 || len(body.Runs) != 1 || body.Runs[0].ID != "run_req" {
		t.Fatalf("request correlation 응답이 이상해요: %+v", body)
	}
	if body.Limit != 7 || body.ResultTruncated {
		t.Fatalf("request correlation metadata가 이상해요: %+v", body)
	}
	for _, queryString := range []string{"limit=-1", "limit=abc", "offset=-1", "offset=abc"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/requests/req_filter/runs?"+queryString, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("잘못된 request run query는 400이어야 해요: query=%s status=%d body=%s", queryString, rec.Code, rec.Body.String())
		}
		if strings.Contains(queryString, "limit") && !strings.Contains(rec.Body.String(), "limit") {
			t.Fatalf("request run limit 오류는 limit을 설명해야 해요: query=%s body=%s", queryString, rec.Body.String())
		}
		if strings.Contains(queryString, "offset") && !strings.Contains(rec.Body.String(), "offset") {
			t.Fatalf("request run offset 오류는 offset을 설명해야 해요: query=%s body=%s", queryString, rec.Body.String())
		}
	}
	for _, tc := range []struct {
		queryString string
		want        string
	}{
		{queryString: "provider=" + strings.Repeat("x", maxRunProviderModelBytes+1), want: "provider"},
		{queryString: "model=" + strings.Repeat("x", maxRunProviderModelBytes+1), want: "model"},
	} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/requests/req_filter/runs?"+tc.queryString, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("긴 request run query는 400이어야 해요: query=%s status=%d body=%s", tc.queryString, rec.Code, rec.Body.String())
		}
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/requests/"+strings.Repeat("x", maxRequestIDBytes+1)+"/runs", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "request_id") {
		t.Fatalf("긴 request_id path는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGatewayRequestCorrelationEventsEndpoint(t *testing.T) {
	store := openTestStore(t)
	secret := "token=abc1234567890secretvalue"
	var query RunQuery
	var eventRunID string
	var gotAfterSeq int
	var gotEventType string
	srv, err := New(Config{
		Store: store,
		RunLister: func(ctx context.Context, q RunQuery) ([]RunDTO, error) {
			query = q
			return []RunDTO{{ID: "run_req", SessionID: "sess_1", Status: "completed", Metadata: map[string]string{RequestIDMetadataKey: q.RequestID}}}, nil
		},
		RunEventLister: func(ctx context.Context, runID string, afterSeq int, eventType string, limit int) ([]RunEventDTO, error) {
			eventRunID = runID
			gotAfterSeq = afterSeq
			gotEventType = eventType
			return []RunEventDTO{
				{Seq: 1, At: time.Unix(2, 0).UTC(), Type: "run.completed", Message: "done " + secret, Run: RunDTO{ID: runID, Status: "completed", Prompt: "done " + secret, Metadata: map[string]string{"token": secret}}},
				{Seq: 2, At: time.Unix(1, 0).UTC(), Type: "run.queued", Payload: json.RawMessage(`{"value":"token=abc1234567890secretvalue"}`), Run: RunDTO{ID: runID, Status: "queued", ContextBlocks: []string{"context " + secret}}},
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
	if raw := rec.Body.String(); strings.Contains(raw, "abc1234567890secretvalue") || !strings.Contains(raw, "[REDACTED]") {
		t.Fatalf("request correlation event 응답은 secret을 숨겨야 해요: %s", raw)
	}
	if body.Limit != 5 || body.ResultTruncated {
		t.Fatalf("request correlation event metadata가 이상해요: %+v", body)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/requests/req_filter/events?after_seq=1&limit=5", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("after_seq status = %d body = %s", rec.Code, rec.Body.String())
	}
	if gotAfterSeq != 1 {
		t.Fatalf("request correlation after_seq가 run event lister에 전달되지 않았어요: %d", gotAfterSeq)
	}
	body = RequestCorrelationEventsResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.AfterSeq != 1 {
		t.Fatalf("request correlation after_seq metadata가 이상해요: %+v", body)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/requests/req_filter/events?type=run.completed&limit=5", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("type filter status = %d body = %s", rec.Code, rec.Body.String())
	}
	if gotEventType != "run.completed" {
		t.Fatalf("request correlation event type이 run event lister에 전달되지 않았어요: %q", gotEventType)
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
	} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/requests/req_filter/events?"+tc.query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		want := strings.Split(tc.query, "=")[0]
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("%s request event query는 400이어야 해요: status=%d body=%s", tc.name, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Header().Get("Content-Type"), "text/event-stream") {
			t.Fatalf("%s request event query는 SSE stream을 열기 전에 반환해야 해요: header=%s", tc.name, rec.Header().Get("Content-Type"))
		}
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

func TestGatewayRequestCorrelationSSERejectsInvalidHeartbeat(t *testing.T) {
	store := openTestStore(t)
	srv, err := New(Config{
		Store: store,
		RunLister: func(ctx context.Context, q RunQuery) ([]RunDTO, error) {
			return []RunDTO{{ID: "run_req_bad_heartbeat", SessionID: "sess_1", Status: "running", Metadata: map[string]string{RequestIDMetadataKey: q.RequestID}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"heartbeat_ms=-1", "heartbeat_ms=abc"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/requests/req_bad_heartbeat/events?stream=true&"+query, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "heartbeat_ms") {
			t.Fatalf("잘못된 request SSE heartbeat_ms는 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Header().Get("Content-Type"), "text/event-stream") {
			t.Fatalf("heartbeat_ms 오류는 SSE stream을 열기 전에 반환해야 해요: query=%s header=%s", query, rec.Header().Get("Content-Type"))
		}
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
		RunEventLister: func(ctx context.Context, runID string, afterSeq int, eventType string, limit int) ([]RunEventDTO, error) {
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
	srv, err := New(Config{Store: store, Version: "test", MaxRequestBytes: 1234, MaxConcurrentRuns: 3, RunTimeout: 2 * time.Minute, RunMaxIterations: 8, RunWebMaxBytes: 1 << 20, Providers: []ProviderDTO{{Name: "copilot", Capabilities: map[string]any{"skills": true}}}, DefaultMCPServers: []ResourceDTO{{Name: "context7", Kind: string(session.ResourceMCPServer), Config: map[string]any{"url": "https://mcp.context7.com/mcp", "headers": map[string]any{"CONTEXT7_API_KEY": "secret-token"}}}}})
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
	if caps.Version != "test" || len(caps.Providers) != 1 || len(caps.Features) == 0 || len(caps.DefaultMCPServers) != 1 || caps.Limits.MaxRequestBytes != 1234 || caps.Limits.MaxConcurrentRuns != 3 || caps.Limits.RunTimeoutSeconds != 120 || caps.Limits.RunMaxIterations != 8 || caps.Limits.RunWebMaxBytes != 1<<20 || caps.Limits.MaxMCPHTTPResponseBytes != maxMCPHTTPResponseBytes || caps.Limits.MaxMCPProbeNameBytes != maxMCPProbeNameBytes || caps.Limits.MaxMCPProbeURIBytes != maxMCPProbeURIBytes || caps.Limits.MaxMCPProbeArgumentBytes != maxMCPProbeArgumentsBytes || caps.Limits.MaxMCPProbeOutputBytes != maxMCPProbeOutputBytes || caps.Limits.MaxFileContentBytes != maxFileContentBytes || caps.Limits.MaxSkillPreviewBytes != maxSkillPreviewBytes || caps.Limits.MaxSubagentPreviewBytes != maxSubagentPreviewPromptBytes || caps.Limits.MaxPromptTextBytes != maxPromptTextBytes || caps.Limits.MaxTranscriptMarkdownBytes != maxTranscriptMarkdownBytes || caps.Limits.MaxGitDiffBytes != maxGitDiffBytes || caps.Limits.MaxRunPreviewBytes != MaxRunPreviewBytes || caps.Limits.MaxRunOutputTokens != MaxRunOutputTokens || caps.Limits.MaxProviderTestPreviewBytes != MaxProviderTestPreviewBytes || caps.Limits.MaxProviderTestResultBytes != MaxProviderTestResultBytes || caps.Limits.MaxProviderTestOutputTokens != MaxProviderTestOutputTokens || caps.Limits.MaxProviderTestTimeoutMS != MaxProviderTestTimeoutMS || caps.Limits.MaxHTTPJSONResponseBytes != httpjson.MaxResponseBytes || caps.Limits.MaxWorkspaceFileReadBytes != workspace.MaxFileReadBytes || caps.Limits.MaxWorkspaceFileWriteBytes != workspace.MaxFileWriteBytes || caps.Limits.MaxWorkspaceListEntries != workspace.MaxListEntries || caps.Limits.MaxWorkspaceGlobMatches != workspace.MaxGlobMatches || caps.Limits.MaxWorkspaceGrepMatches != workspace.MaxGrepMatches || caps.Limits.MaxWorkspacePatchBytes != workspace.MaxPatchBytes || caps.Limits.MaxLSPFormatInputBytes != maxLSPFormatInputBytes || caps.Limits.MaxLSPFormatPreviewBytes != maxLSPFormatPreviewBytes || caps.Limits.MaxRunPromptBytes != maxRunPromptBytes || caps.Limits.MaxRunSelectorItems != maxRunSelectorItems || caps.Limits.MaxRunContextBlocks != maxRunContextBlocks || caps.Limits.MaxRequestIDBytes != maxRequestIDBytes || caps.Limits.MaxIdempotencyKeyBytes != maxIdempotencyKeyBytes || caps.Limits.MaxToolCallNameBytes != maxToolCallNameBytes || caps.Limits.MaxToolCallIDBytes != maxToolCallIDBytes || caps.Limits.MaxToolCallArgumentBytes != maxToolCallArgumentsBytes || caps.Limits.MaxToolCallOutputBytes != maxToolCallOutputBytes || caps.Limits.MaxToolCallWebBytes != maxToolCallWebBytes || caps.Limits.MaxShellTimeoutMS != workspace.MaxCommandTimeout.Milliseconds() || caps.Limits.MaxShellOutputBytes != workspace.MaxCommandOutputBytes || caps.Limits.MaxShellStderrBytes != workspace.MaxCommandStderrBytes {
		t.Fatalf("capability discovery가 이상해요: %+v", caps)
	}
	capKeys := map[string]bool{}
	for _, item := range caps.ProviderCapabilities {
		capKeys[item.Name] = item.Description != ""
	}
	for _, name := range []string{"tools", "mcp", "custom_agents", "routing"} {
		if !capKeys[name] {
			t.Fatalf("provider capability catalog에 %s 설명이 필요해요: %+v", name, caps.ProviderCapabilities)
		}
	}
	if len(caps.ProviderPipeline) < 4 || caps.ProviderPipeline[0].Name != "request" || caps.ProviderPipeline[1].Name != "convert_request" || caps.ProviderPipeline[2].Name != "api_call" || caps.ProviderPipeline[3].Name != "convert_response" {
		t.Fatalf("provider pipeline catalog는 요청→변환→API 호출→응답 변환 순서를 노출해야 해요: %+v", caps.ProviderPipeline)
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
	statePath := filepath.Join(t.TempDir(), "state.db")
	srv, err := New(Config{
		Store:             store,
		StatePath:         statePath,
		MinStateFreeBytes: 1,
		Version:           "test",
		MaxRequestBytes:   123,
		MaxConcurrentRuns: 2,
		RunTimeout:        time.Minute,
		Providers:         []ProviderDTO{{Name: "openai", AuthStatus: "configured", AuthEnv: []string{"OPENAI_API_KEY"}}},
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
	var sawStore, sawStateDisk, sawDefaultMCP, sawRunValidator, sawProviderTester, sawProviderAuth bool
	for _, check := range diagnostics.Checks {
		if check.Name == "store" && check.Status == "ok" {
			sawStore = true
		}
		if check.Name == "state_disk" && check.Status == "ok" {
			sawStateDisk = true
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
		if check.Name == "provider_auth.openai" && check.Status == "configured" {
			sawProviderAuth = true
		}
	}
	if !sawStore || !sawStateDisk || !sawDefaultMCP || !sawRunValidator || !sawProviderTester || !sawProviderAuth {
		t.Fatalf("store/default MCP/runtime wiring/provider auth check가 필요해요: %+v", diagnostics.Checks)
	}
}

func TestGatewayDiagnosticsMarksMissingProviderAuthUnhealthy(t *testing.T) {
	store := openTestStore(t)
	srv, err := New(Config{
		Store:     store,
		Version:   "test",
		Providers: []ProviderDTO{{Name: "openai", AuthStatus: "missing", AuthEnv: []string{"OPENAI_API_KEY"}}},
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
		t.Fatalf("missing provider auth는 diagnostics를 unhealthy로 표시해야 해요: %+v", diagnostics)
	}
	if !containsString(diagnostics.FailingChecks, "provider_auth.openai") {
		t.Fatalf("provider auth 실패 원인을 failing_checks에 담아야 해요: %+v", diagnostics)
	}
	for _, check := range diagnostics.Checks {
		if check.Name == "provider_auth.openai" && check.Status == "missing" && strings.Contains(check.Message, "OPENAI_API_KEY") {
			return
		}
	}
	t.Fatalf("missing provider auth check가 필요해요: %+v", diagnostics.Checks)
}

func TestGatewayDiagnosticsMarksInjectedFailingCheckUnhealthy(t *testing.T) {
	store := openTestStore(t)
	srv, err := New(Config{
		Store:            store,
		Version:          "test",
		DiagnosticChecks: []DiagnosticCheckDTO{{Name: "custom.required", Status: "missing", Message: "required dependency is missing"}},
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
		t.Fatalf("injected missing diagnostics check는 unhealthy로 집계해야 해요: %+v", diagnostics)
	}
	if !containsString(diagnostics.FailingChecks, "custom.required") {
		t.Fatalf("injected check 실패 원인을 failing_checks에 담아야 해요: %+v", diagnostics)
	}
	for _, check := range diagnostics.Checks {
		if check.Name == "custom.required" && check.Status == "missing" {
			return
		}
	}
	t.Fatalf("injected diagnostics check가 응답에 남아야 해요: %+v", diagnostics.Checks)
}

func TestGatewayDiagnosticsTreatsWarningsAsHealthy(t *testing.T) {
	store := openTestStore(t)
	srv, err := New(Config{
		Store:             store,
		StatePath:         filepath.Join(t.TempDir(), "state.db"),
		MinStateFreeBytes: 1 << 60,
		Version:           "test",
		DiagnosticChecks:  []DiagnosticCheckDTO{{Name: "default_mcp.serena", Status: "warning", Message: "uvx가 없어요"}},
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
	if !diagnostics.OK || len(diagnostics.FailingChecks) != 0 {
		t.Fatalf("warning diagnostics should not make gateway unhealthy: %+v", diagnostics)
	}
	sawInjectedWarning := false
	sawStateDiskWarning := false
	for _, check := range diagnostics.Checks {
		if check.Name == "default_mcp.serena" && check.Status == "warning" {
			sawInjectedWarning = true
			continue
		}
		if check.Name == "state_disk" && check.Status == "warning" {
			sawStateDiskWarning = true
		}
	}
	if !sawInjectedWarning || !sawStateDiskWarning {
		t.Fatalf("warning diagnostics check should remain visible: %+v", diagnostics.Checks)
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
	if !containsString(diagnostics.FailingChecks, "run_previewer") || !containsString(diagnostics.FailingChecks, "provider_tester") {
		t.Fatalf("runtime wiring 실패 원인을 failing_checks에 담아야 해요: %+v", diagnostics)
	}
	if len(diagnostics.MissingRuntimeWiring) != 9 {
		t.Fatalf("diagnostics는 missing runtime wiring 목록을 직접 제공해야 해요: %+v", diagnostics)
	}
	missing := map[string]bool{}
	for _, name := range diagnostics.MissingRuntimeWiring {
		missing[name] = true
	}
	if !missing["run_previewer"] || !missing["run_validator"] || !missing["provider_tester"] || !missing["run_getter"] || !missing["run_lister"] || !missing["run_canceler"] || !missing["run_event_lister"] || !missing["run_subscriber"] || !missing["run_event_subscriber"] {
		t.Fatalf("빠진 runtime wiring 목록이 필요해요: %+v", diagnostics.MissingRuntimeWiring)
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
		Store:             store,
		DefaultMCPServers: []ResourceDTO{{Name: "context7"}},
		RunPreviewer: func(ctx context.Context, req RunStartRequest) (*RunPreviewResponse, error) {
			gotReq = req
			return &RunPreviewResponse{SessionID: req.SessionID, Provider: "openai", Model: "gpt-5-mini", BaseRequestTools: []string{"mcp"}, ContextBlocks: []string{"선택 context예요"}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/preview", strings.NewReader(`{"session_id":" `+sess.ID+` ","prompt":"미리보기","provider":" openai ","model":" gpt-5-mini ","preview_stream":true,"max_preview_bytes":123,"max_output_tokens":456,"mcp_servers":[" mcp_1 ","","mcp_1"],"skills":[" skill_1 ","skill_1"],"subagents":[" agent_1 ","agent_1"],"enabled_tools":[" file_read ","file_read"],"disabled_tools":[" shell_run "],"context_blocks":["token=ghp_123456789012345678901234567890123456 패널 context"]}`))
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
	if preview.SessionID != sess.ID || preview.BaseRequestTools[0] != "mcp" || len(preview.ContextBlocks) != 1 || preview.ContextBlocks[0] != "선택 context예요" || gotReq.SessionID != sess.ID || gotReq.Provider != "openai" || gotReq.Model != "gpt-5-mini" || gotReq.Metadata[RequestIDMetadataKey] != "req_preview" || gotReq.Metadata[DefaultMCPMetadataKey] != "context7" || !gotReq.PreviewStream || gotReq.MaxPreviewBytes != 123 || gotReq.MaxOutputTokens != 456 || len(gotReq.MCPServers) != 1 || gotReq.MCPServers[0] != "mcp_1" || len(gotReq.Skills) != 1 || gotReq.Skills[0] != "skill_1" || len(gotReq.Subagents) != 1 || gotReq.Subagents[0] != "agent_1" || len(gotReq.EnabledTools) != 1 || gotReq.EnabledTools[0] != "file_read" || len(gotReq.DisabledTools) != 1 || gotReq.DisabledTools[0] != "shell_run" || len(gotReq.ContextBlocks) != 1 || strings.Contains(gotReq.ContextBlocks[0], "ghp_") || !strings.Contains(gotReq.ContextBlocks[0], "[REDACTED]") {
		t.Fatalf("run preview 응답/요청이 이상해요: preview=%+v req=%+v", preview, gotReq)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/runs/preview", strings.NewReader(`{"session_id":"`+sess.ID+`","prompt":"미리보기","max_preview_bytes":-1}`))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_preview_bytes") {
		t.Fatalf("negative max_preview_bytes는 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/runs/preview", strings.NewReader(`{"session_id":"`+sess.ID+`","prompt":"미리보기","max_preview_bytes":`+strconv.Itoa(MaxRunPreviewBytes+1)+`}`))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_preview_bytes") {
		t.Fatalf("large max_preview_bytes는 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/runs/preview", strings.NewReader(`{"session_id":"`+sess.ID+`","prompt":"미리보기","max_output_tokens":`+strconv.Itoa(MaxRunOutputTokens+1)+`}`))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_output_tokens") {
		t.Fatalf("large max_output_tokens는 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
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

	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/turns?after_seq=-1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "after_seq") {
		t.Fatalf("음수 turn after_seq는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/turns?after_seq=abc", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "after_seq") {
		t.Fatalf("잘못된 turn after_seq는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, query := range []string{"limit=-1", "limit=abc"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/turns?"+query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "limit") {
			t.Fatalf("잘못된 turn limit은 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
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
	for _, query := range []string{"limit=-1", "limit=abc", "offset=-1", "offset=abc"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/models?provider=openai&"+query, nil)
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

func TestGatewayDiscoveryUsesStableProviderAndModelOrder(t *testing.T) {
	store := openTestStore(t)
	srv, err := New(Config{Store: store, Providers: []ProviderDTO{
		{Name: "zeta", Models: []string{"z-2", "z-1"}, DefaultModel: "z-1"},
		{Name: "alpha", Models: []string{"alpha-extra", "alpha-default", "alpha-extra"}, DefaultModel: "alpha-default"},
	}})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var providers ProviderListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &providers); err != nil {
		t.Fatal(err)
	}
	if len(providers.Providers) != 2 || providers.Providers[0].Name != "alpha" || providers.Providers[1].Name != "zeta" {
		t.Fatalf("provider discovery는 이름순으로 안정적이어야 해요: %+v", providers.Providers)
	}
	if providers.TotalProviders != 2 || providers.Limit != 2 || providers.Offset != 0 || providers.NextOffset != 0 || providers.ResultTruncated {
		t.Fatalf("provider discovery metadata가 이상해요: %+v", providers)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/providers?limit=1&offset=1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("provider page status = %d body = %s", rec.Code, rec.Body.String())
	}
	var providerPage ProviderListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &providerPage); err != nil {
		t.Fatal(err)
	}
	if len(providerPage.Providers) != 1 || providerPage.Providers[0].Name != "zeta" || providerPage.TotalProviders != 2 || providerPage.Limit != 1 || providerPage.Offset != 1 || providerPage.NextOffset != 0 || providerPage.ResultTruncated {
		t.Fatalf("provider page가 이상해요: %+v", providerPage)
	}
	for _, query := range []string{"limit=-1", "limit=abc", "offset=-1", "offset=abc"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/providers?"+query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("잘못된 provider list query는 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
		if strings.Contains(query, "limit") && !strings.Contains(rec.Body.String(), "limit") {
			t.Fatalf("provider list limit 오류는 limit을 설명해야 해요: query=%s body=%s", query, rec.Body.String())
		}
		if strings.Contains(query, "offset") && !strings.Contains(rec.Body.String(), "offset") {
			t.Fatalf("provider list offset 오류는 offset을 설명해야 해요: query=%s body=%s", query, rec.Body.String())
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/models", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var models ModelListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, model := range models.Models {
		got = append(got, model.Provider+"/"+model.ID)
	}
	if models.TotalModels != 4 || models.Limit != 4 || models.Offset != 0 || models.NextOffset != 0 || models.ResultTruncated {
		t.Fatalf("model discovery metadata가 이상해요: %+v", models)
	}
	if strings.Join(got, ",") != "alpha/alpha-default,alpha/alpha-extra,zeta/z-1,zeta/z-2" {
		t.Fatalf("model discovery는 provider 이름순과 default-first 모델순이어야 해요: %+v", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var caps CapabilityResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &caps); err != nil {
		t.Fatal(err)
	}
	if len(caps.Providers) != 2 || caps.Providers[0].Name != "alpha" || caps.Providers[1].Name != "zeta" {
		t.Fatalf("capabilities provider discovery도 안정적인 순서여야 해요: %+v", caps.Providers)
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
	if listed.TotalPrompts != len(listed.Prompts) || listed.Limit != len(listed.Prompts) || listed.Offset != 0 || listed.NextOffset != 0 || listed.ResultTruncated {
		t.Fatalf("prompt 목록 metadata가 이상해요: %+v", listed)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/prompts?limit=1&offset=1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var page PromptTemplateListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Prompts) != 1 || page.TotalPrompts != listed.TotalPrompts || page.Limit != 1 || page.Offset != 1 {
		t.Fatalf("prompt page가 이상해요: %+v", page)
	}
	if wantTruncated := page.TotalPrompts > 2; page.ResultTruncated != wantTruncated {
		t.Fatalf("prompt page truncation flag가 이상해요: got=%v want=%v page=%+v", page.ResultTruncated, wantTruncated, page)
	}
	if page.ResultTruncated && page.NextOffset != 2 {
		t.Fatalf("prompt page next offset이 이상해요: %+v", page)
	}
	if !page.ResultTruncated && page.NextOffset != 0 {
		t.Fatalf("prompt page next offset이 없어야 해요: %+v", page)
	}
	for _, query := range []string{"limit=-1", "limit=abc", "offset=-1", "offset=abc"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/prompts?"+query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("잘못된 prompt list query는 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
		if strings.Contains(query, "limit") && !strings.Contains(rec.Body.String(), "limit") {
			t.Fatalf("prompt list limit 오류는 limit을 설명해야 해요: query=%s body=%s", query, rec.Body.String())
		}
		if strings.Contains(query, "offset") && !strings.Contains(rec.Body.String(), "offset") {
			t.Fatalf("prompt list offset 오류는 offset을 설명해야 해요: query=%s body=%s", query, rec.Body.String())
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/prompts/agent-system.md?max_text_bytes=32", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got PromptTemplateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "agent-system.md" || got.TextBytes <= len(got.Text) || !got.TextTruncated || !utf8.ValidString(got.Text) || strings.Contains(got.Text, "\uFFFD") {
		t.Fatalf("prompt 원문이 이상해요: %+v", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/prompts/agent-system.md?max_text_bytes=-1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_text_bytes") {
		t.Fatalf("음수 prompt max_text_bytes는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/prompts/agent-system.md?max_text_bytes=abc", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_text_bytes") {
		t.Fatalf("잘못된 prompt max_text_bytes는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	body := bytes.NewBufferString(`{"data":{"AgentName":"kkode","ToolNames":["file_read","shell_run"]},"max_text_bytes":24}`)
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
	if !strings.Contains(rendered.Text, "kkode") || rendered.TextBytes <= len(rendered.Text) || !rendered.TextTruncated || !utf8.ValidString(rendered.Text) || strings.Contains(rendered.Text, "\uFFFD") {
		t.Fatalf("prompt 렌더링이 이상해요: %+v", rendered)
	}

	body = bytes.NewBufferString(`{"data":{"AgentName":"kkode"},"max_text_bytes":-1}`)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/prompts/agent-system.md/render", body)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_text_bytes") {
		t.Fatalf("음수 prompt render max_text_bytes는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGatewayResourceManifestLifecycle(t *testing.T) {
	store := openTestStore(t)
	srv := newTestServer(t, store, "")

	body := bytes.NewBufferString(`{"name":"filesystem","description":"파일 MCP예요","config":{"kind":" stdio ","command":" mcp-fs ","args":["."],"env":{" PATH ":" /tmp/bin "},"headers":{" Authorization ":" Bearer secret-token "}}}`)
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
	if created.Config["kind"] != "stdio" {
		t.Fatalf("생성된 MCP manifest kind는 canonical 값이어야 해요: %+v", created.Config)
	}
	if created.Config["env"].(map[string]any)["PATH"] != "/tmp/bin" {
		t.Fatalf("생성된 MCP manifest env는 canonical 값이어야 해요: %+v", created.Config)
	}
	if created.Config["headers"].(map[string]any)["Authorization"] != "[REDACTED]" {
		t.Fatalf("생성 응답은 secret config를 숨겨야 해요: %+v", created.Config)
	}
	loadedResource, err := store.LoadResource(context.Background(), session.ResourceMCPServer, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(loadedResource.Config), "secret-token") {
		t.Fatalf("저장소에는 실행용 원본 config가 남아 있어야 해요: %s", loadedResource.Config)
	}

	invalidResources := []struct {
		name string
		path string
		body string
		want string
	}{
		{name: "mcp missing transport", path: "/api/v1/mcp/servers", body: `{"name":"broken","config":{"kind":"stdio"}}`, want: "command"},
		{name: "mcp negative timeout", path: "/api/v1/mcp/servers", body: `{"name":"slow","config":{"kind":"http","url":"https://mcp.example.test","timeout":-1}}`, want: "timeout"},
		{name: "mcp fractional timeout", path: "/api/v1/mcp/servers", body: `{"name":"slow","config":{"kind":"http","url":"https://mcp.example.test","timeout":1.5}}`, want: "integer"},
		{name: "mcp bad url", path: "/api/v1/mcp/servers", body: `{"name":"bad-url","config":{"kind":"http","url":"file:///tmp/mcp.sock"}}`, want: "http/https"},
		{name: "mcp bad id", path: "/api/v1/mcp/servers", body: `{"id":"bad id","name":"broken","config":{"kind":"http","url":"https://mcp.example.test"}}`, want: "resource id"},
		{name: "mcp long name", path: "/api/v1/mcp/servers", body: `{"name":"` + strings.Repeat("n", maxResourceNameBytes+1) + `","config":{"kind":"http","url":"https://mcp.example.test"}}`, want: "resource name"},
		{name: "mcp long config", path: "/api/v1/mcp/servers", body: `{"name":"huge","config":{"kind":"http","url":"https://mcp.example.test","extra":"` + strings.Repeat("x", maxResourceConfigBytes+1) + `"}}`, want: "resource config"},
		{name: "mcp long command", path: "/api/v1/mcp/servers", body: `{"name":"long-command","config":{"kind":"stdio","command":"` + strings.Repeat("x", maxResourceConfigStringBytes+1) + `"}}`, want: "command"},
		{name: "mcp too many args", path: "/api/v1/mcp/servers", body: `{"name":"many-args","config":{"kind":"stdio","command":"mcp-fs","args":[` + quotedStringList("arg", maxResourceStringArrayItems+1) + `]}}`, want: "최대"},
		{name: "mcp long arg", path: "/api/v1/mcp/servers", body: `{"name":"long-arg","config":{"kind":"stdio","command":"mcp-fs","args":["` + strings.Repeat("x", maxResourceStringArrayItemBytes+1) + `"]}}`, want: "args[0]"},
		{name: "mcp numeric env", path: "/api/v1/mcp/servers", body: `{"name":"bad-env","config":{"kind":"stdio","command":"mcp-fs","env":{"PORT":3000}}}`, want: "env"},
		{name: "mcp blank header key", path: "/api/v1/mcp/servers", body: `{"name":"blank-header","config":{"kind":"http","url":"https://mcp.example.test","headers":{"  ":"x"}}}`, want: "headers key"},
		{name: "mcp duplicate canonical header key", path: "/api/v1/mcp/servers", body: `{"name":"dup-header","config":{"kind":"http","url":"https://mcp.example.test","headers":{" X-Test ":"a","X-Test":"b"}}}`, want: "중복"},
		{name: "mcp long header key", path: "/api/v1/mcp/servers", body: `{"name":"long-header","config":{"kind":"http","url":"https://mcp.example.test","headers":{"` + strings.Repeat("x", maxResourceStringMapKeyBytes+1) + `":"v"}}}`, want: "key"},
		{name: "mcp long header value", path: "/api/v1/mcp/servers", body: `{"name":"long-header-value","config":{"kind":"http","url":"https://mcp.example.test","headers":{"X-Test":"` + strings.Repeat("x", maxResourceConfigStringBytes+1) + `"}}}`, want: "X-Test"},
		{name: "mcp too many headers", path: "/api/v1/mcp/servers", body: `{"name":"many-headers","config":{"kind":"http","url":"https://mcp.example.test","headers":{` + quotedStringMap("X-Test-", "v", maxResourceStringMapItems+1) + `}}}`, want: "최대"},
		{name: "skill missing path", path: "/api/v1/skills", body: `{"name":"empty","config":{}}`, want: "path"},
		{name: "skill long path", path: "/api/v1/skills", body: `{"name":"long-skill","config":{"path":"` + strings.Repeat("x", maxResourceConfigStringBytes+1) + `"}}`, want: "path"},
		{name: "subagent bad inline mcp", path: "/api/v1/subagents", body: `{"name":"bad-agent","config":{"prompt":"계획해요","mcp_servers":{"context7":{"kind":"http"}}}}`, want: "url"},
		{name: "subagent bad inline mcp env", path: "/api/v1/subagents", body: `{"name":"bad-agent","config":{"prompt":"계획해요","mcp_servers":{"fs":{"kind":"stdio","command":"mcp-fs","env":{"PORT":3000}}}}}`, want: "env"},
		{name: "subagent long prompt", path: "/api/v1/subagents", body: `{"name":"bad-agent","config":{"prompt":"` + strings.Repeat("x", maxResourceConfigStringBytes+1) + `"}}`, want: "prompt"},
		{name: "subagent long tool", path: "/api/v1/subagents", body: `{"name":"bad-agent","config":{"prompt":"계획해요","tools":["` + strings.Repeat("x", maxResourceStringArrayItemBytes+1) + `"]}}`, want: "tools[0]"},
		{name: "subagent blank inline mcp label", path: "/api/v1/subagents", body: `{"name":"bad-agent","config":{"prompt":"계획해요","mcp_servers":{"  ":"mcp-fs"}}}`, want: "label"},
		{name: "subagent long inline mcp label", path: "/api/v1/subagents", body: `{"name":"bad-agent","config":{"prompt":"계획해요","mcp_servers":{"` + strings.Repeat("x", maxResourceInlineMCPLabelBytes+1) + `":"mcp-fs"}}}`, want: "label"},
		{name: "subagent long inline mcp command", path: "/api/v1/subagents", body: `{"name":"bad-agent","config":{"prompt":"계획해요","mcp_servers":{"fs":"` + strings.Repeat("x", maxResourceConfigStringBytes+1) + `"}}}`, want: "command"},
		{name: "subagent duplicate canonical inline mcp label", path: "/api/v1/subagents", body: `{"name":"bad-agent","config":{"prompt":"계획해요","mcp_servers":{" context7 ":"mcp-a","context7":"mcp-b"}}}`, want: "중복"},
	}
	for _, tc := range invalidResources {
		req = httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s invalid resource는 400이어야 해요: status=%d body=%s", tc.name, rec.Code, rec.Body.String())
		}
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
	if got.Config["headers"].(map[string]any)["Authorization"] != "[REDACTED]" {
		t.Fatalf("조회 응답은 secret config를 숨겨야 해요: %+v", got.Config)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/mcp/servers/%20"+created.ID+"%20", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("space-padded resource id 조회 status = %d body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/mcp/servers/%20%20", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "resource id") {
		t.Fatalf("blank resource id 조회는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	body = bytes.NewBufferString(`{"name":"planner","config":{"prompt":"계획을 세워요","tools":[" file_read ","file_read"],"skills":[" review ","review"],"mcp_server_ids":[" mcp_context7 ","mcp_context7"]}}`)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/subagents", body)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var planner ResourceDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &planner); err != nil {
		t.Fatal(err)
	}
	if tools, _ := planner.Config["tools"].([]any); len(tools) != 1 || tools[0] != "file_read" {
		t.Fatalf("subagent tools config는 canonical array여야 해요: %+v", planner.Config)
	}
	if skills, _ := planner.Config["skills"].([]any); len(skills) != 1 || skills[0] != "review" {
		t.Fatalf("subagent skills config는 canonical array여야 해요: %+v", planner.Config)
	}
	if ids, _ := planner.Config["mcp_server_ids"].([]any); len(ids) != 1 || ids[0] != "mcp_context7" {
		t.Fatalf("subagent MCP id config는 canonical array여야 해요: %+v", planner.Config)
	}
	body = bytes.NewBufferString(`{"name":"reviewer","config":{"prompt":"검토해요","tools":["file_read"]}}`)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/subagents", body)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/subagents?name=planner&limit=1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var listed ResourceListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Resources) != 1 {
		t.Fatalf("subagent 목록이 이상해요: %+v", listed)
	}
	if listed.Resources[0].Name != "planner" {
		t.Fatalf("subagent name filter가 이상해요: %+v", listed)
	}
	if listed.TotalResources != 1 {
		t.Fatalf("resource name filter total이 이상해요: %+v", listed)
	}
	if listed.Limit != 1 || listed.Offset != 0 || listed.NextOffset != 0 || listed.ResultTruncated {
		t.Fatalf("resource list metadata가 이상해요: %+v", listed)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/subagents?limit=1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	listed = ResourceListResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Resources) != 1 || listed.TotalResources != 2 {
		t.Fatalf("subagent first page가 이상해요: %+v", listed)
	}
	if listed.Limit != 1 || listed.Offset != 0 || listed.NextOffset != 1 || !listed.ResultTruncated {
		t.Fatalf("resource first page metadata가 이상해요: %+v", listed)
	}
	firstPageID := listed.Resources[0].ID
	req = httptest.NewRequest(http.MethodGet, "/api/v1/subagents?limit=1&offset=1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	listed = ResourceListResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Resources) != 1 || listed.Resources[0].ID == firstPageID {
		t.Fatalf("subagent offset page가 이상해요: %+v", listed)
	}
	if listed.TotalResources != 2 {
		t.Fatalf("resource total이 이상해요: %+v", listed)
	}
	if listed.Limit != 1 || listed.Offset != 1 || listed.NextOffset != 0 || listed.ResultTruncated {
		t.Fatalf("resource offset metadata가 이상해요: %+v", listed)
	}
	for _, query := range []string{"limit=-1", "limit=abc", "offset=-1", "offset=abc"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/subagents?"+query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("잘못된 resource list query는 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
		if strings.Contains(query, "limit") && !strings.Contains(rec.Body.String(), "limit") {
			t.Fatalf("resource list limit 오류는 limit을 설명해야 해요: query=%s body=%s", query, rec.Body.String())
		}
		if strings.Contains(query, "offset") && !strings.Contains(rec.Body.String(), "offset") {
			t.Fatalf("resource list offset 오류는 offset을 설명해야 해요: query=%s body=%s", query, rec.Body.String())
		}
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/subagents?name="+strings.Repeat("x", maxResourceNameBytes+1), nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "resource name") {
		t.Fatalf("긴 resource name filter는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/mcp/servers/%20"+created.ID+"%20", nil)
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
	for _, query := range []string{"limit=-1", "limit=abc"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/lsp/symbols?project_root="+root+"&query=run&"+query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "limit") {
			t.Fatalf("잘못된 LSP symbol limit은 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
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

	req = httptest.NewRequest(http.MethodGet, "/api/v1/lsp/document-symbols?project_root="+root+"&path=main.go&limit=1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var documentSymbols LSPSymbolListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &documentSymbols); err != nil {
		t.Fatal(err)
	}
	if len(documentSymbols.Symbols) != 1 || documentSymbols.Limit != 1 || !documentSymbols.ResultTruncated {
		t.Fatalf("document symbol limit metadata가 이상해요: %+v", documentSymbols)
	}
	for _, query := range []string{"limit=-1", "limit=abc"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/lsp/document-symbols?project_root="+root+"&path=main.go&"+query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "limit") {
			t.Fatalf("잘못된 document symbol limit은 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
	}

	largePath := filepath.Join(root, "large-symbols.go")
	large, err := os.Create(largePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := large.Truncate(int64(maxLSPFormatInputBytes + 1)); err != nil {
		_ = large.Close()
		t.Fatal(err)
	}
	if err := large.Close(); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/lsp/document-symbols?project_root="+root+"&path=large-symbols.go", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_bytes") {
		t.Fatalf("큰 LSP document symbol input은 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/lsp/symbols?project_root="+root+"&query=run", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("large Go files in workspace scan should be skipped: status=%d body=%s", rec.Code, rec.Body.String())
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
	for _, query := range []string{"limit=-1", "limit=abc"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/lsp/definitions?project_root="+root+"&symbol=Runner&"+query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "limit") {
			t.Fatalf("잘못된 definition limit은 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
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
	for _, query := range []string{"limit=-1", "limit=abc"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/lsp/references?project_root="+root+"&symbol=Runner&"+query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "limit") {
			t.Fatalf("잘못된 reference limit은 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
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

	req = httptest.NewRequest(http.MethodGet, "/api/v1/lsp/definitions?project_root="+root+"&path=main.go&line=11&column=4", nil)
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
		t.Fatalf("커서 기반 definition 결과가 이상해요: %+v", defs)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/lsp/references?project_root="+root+"&path=main.go&line=11&column=4&limit=20", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	refs = LSPReferenceListResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &refs); err != nil {
		t.Fatal(err)
	}
	if len(refs.References) < 2 {
		t.Fatalf("커서 기반 reference 결과가 너무 적어요: %+v", refs)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/lsp/rename-preview?project_root="+root+"&path=main.go&line=11&column=4&new_name=Execute&limit=20", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var rename LSPRenamePreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &rename); err != nil {
		t.Fatal(err)
	}
	if rename.Symbol != "Run" || rename.NewName != "Execute" || len(rename.Edits) < 2 || rename.Limit != 20 {
		t.Fatalf("rename preview 결과가 이상해요: %+v", rename)
	}
	for _, query := range []string{"limit=-1", "limit=abc"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/lsp/rename-preview?project_root="+root+"&path=main.go&line=11&column=4&new_name=Execute&"+query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "limit") {
			t.Fatalf("잘못된 rename preview limit은 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/lsp/format-preview?project_root="+root+"&path=main.go&max_bytes=64", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var formatPreview LSPFormatPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &formatPreview); err != nil {
		t.Fatal(err)
	}
	if formatPreview.File != "main.go" || formatPreview.Content == "" || formatPreview.ContentBytes == 0 {
		t.Fatalf("format preview 결과가 이상해요: %+v", formatPreview)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/lsp/format-preview?project_root="+root+"&path=main.go&max_bytes=-1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_bytes") {
		t.Fatalf("음수 LSP format max_bytes는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/lsp/format-preview?project_root="+root+"&path=main.go&max_bytes=abc", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_bytes") {
		t.Fatalf("잘못된 LSP format max_bytes는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	largePath := filepath.Join(root, "large.go")
	large, err := os.Create(largePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := large.Truncate(int64(maxLSPFormatInputBytes + 1)); err != nil {
		_ = large.Close()
		t.Fatal(err)
	}
	if err := large.Close(); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/lsp/format-preview?project_root="+root+"&path=large.go", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "format preview input") {
		t.Fatalf("큰 LSP format input은 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
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
	for _, query := range []string{"limit=-1", "limit=abc"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/lsp/diagnostics?project_root="+root+"&path=broken.go&"+query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "limit") {
			t.Fatalf("잘못된 diagnostics limit은 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
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

	req = httptest.NewRequest(http.MethodGet, "/api/v1/lsp/hover?project_root="+root+"&path=main.go&line=7&column=18", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	hover = LSPHoverResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &hover); err != nil {
		t.Fatal(err)
	}
	if !hover.Found || hover.Symbol != "Run" || hover.Container != "Runner" {
		t.Fatalf("커서 기반 hover 결과가 이상해요: %+v", hover)
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
	run := session.Run{ID: "run_request_transcript", SessionID: sess.ID, TurnID: turn.ID, Status: "completed", Prompt: "run " + secret, Provider: "openai", Model: "gpt-5-mini", Metadata: map[string]string{RequestIDMetadataKey: requestID}, ContextBlocks: []string{"context " + secret}}
	if _, _, err := store.SaveRunWithEvent(ctx, run, session.RunEvent{RunID: run.ID, Type: "tool.completed", Tool: "file_read", Message: "message " + secret, At: time.Now().UTC(), Run: run}); err != nil {
		t.Fatal(err)
	}
	manager := NewAsyncRunManagerWithStore(nil, store)
	srv, err := New(Config{Store: store, RunLister: manager.List, RunEventLister: manager.Events})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/requests/"+requestID+"/transcript?max_markdown_bytes=64", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got RequestCorrelationTranscriptResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.RequestID != requestID || len(got.Transcripts) != 1 || got.Transcripts[0].Run.ID != run.ID || !strings.Contains(got.Markdown, "kkode request transcript") || got.MarkdownBytes <= len(got.Markdown) || !got.MarkdownTruncated || !got.Transcripts[0].MarkdownTruncated {
		t.Fatalf("request transcript 응답이 이상해요: %+v", got)
	}
	body := rec.Body.String()
	if !got.Redacted || !strings.Contains(body, "[REDACTED]") || strings.Contains(body, secret) {
		t.Fatalf("request transcript는 기본 redaction을 적용해야 해요: %s", body)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/requests/"+requestID+"/transcript?max_markdown_bytes=abc", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_markdown_bytes") {
		t.Fatalf("잘못된 request transcript max_markdown_bytes는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, query := range []string{"run_limit=-1", "run_limit=abc", "event_limit=-1", "event_limit=abc", "redact=maybe"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/requests/"+requestID+"/transcript?"+query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		want := strings.Split(query, "=")[0]
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("잘못된 request transcript query는 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
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
        write_frame({"jsonrpc":"2.0","id":msg["id"],"result":{"tools":[{"name":"echo","description":"Echo text","inputSchema":{"type":"object","properties":{"text":{"type":"string"},"repeat":{"type":"integer"}}}},{"name":"second","description":"Second tool"}]}})
        break
`)
	store := openTestStore(t)
	resource, err := store.SaveResource(context.Background(), session.Resource{Kind: session.ResourceMCPServer, Name: "fake", Enabled: true, Config: []byte(`{"kind":"stdio","command":"python3","args":["` + serverPath + `"],"timeout":3}`)})
	if err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, store, "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mcp/servers/"+resource.ID+"/tools?limit=1", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var tools MCPToolListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &tools); err != nil {
		t.Fatal(err)
	}
	if tools.Server.ID != resource.ID || len(tools.Tools) != 1 || tools.Tools[0].Name != "echo" || tools.Tools[0].Category != "mcp" || tools.Tools[0].OutputFormat != "json" || tools.Tools[0].Effects[0] != "mcp" || tools.Tools[0].ExampleArguments["text"] != "value" || tools.Tools[0].ExampleArguments["repeat"] != float64(1) {
		t.Fatalf("MCP tools/list 결과가 이상해요: %+v", tools)
	}
	if tools.Limit != 1 || tools.NextOffset != 1 || !tools.ResultTruncated {
		t.Fatalf("MCP tools/list page metadata가 이상해요: %+v", tools)
	}
	for _, query := range []string{"limit=-1", "limit=abc", "offset=-1", "offset=abc"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/mcp/servers/"+resource.ID+"/tools?"+query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("잘못된 MCP tools/list query는 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
		if strings.Contains(query, "limit") && !strings.Contains(rec.Body.String(), "limit") {
			t.Fatalf("MCP tools/list limit 오류는 limit을 설명해야 해요: query=%s body=%s", query, rec.Body.String())
		}
		if strings.Contains(query, "offset") && !strings.Contains(rec.Body.String(), "offset") {
			t.Fatalf("MCP tools/list offset 오류는 offset을 설명해야 해요: query=%s body=%s", query, rec.Body.String())
		}
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
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"http_echo","description":"HTTP echo","inputSchema":{"type":"object","properties":{"text":{"type":"string"}}}}]}}`)
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
	if tools.Server.ID != resource.ID || len(tools.Tools) != 1 || tools.Tools[0].Name != "http_echo" || tools.Tools[0].Category != "mcp" || tools.Tools[0].ExampleArguments["text"] != "value" {
		t.Fatalf("HTTP MCP tools/list 결과가 이상해요: %+v", tools)
	}
	if tools.Server.Config["headers"].(map[string]any)["X-Test-Token"] != "[REDACTED]" {
		t.Fatalf("MCP probe 응답은 server secret config를 숨겨야 해요: %+v", tools.Server.Config)
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
        write_frame({"jsonrpc":"2.0","id":msg["id"],"result":{"resources":[{"uri":"file:///README.md","name":"README","description":"문서","mimeType":"text/markdown"},{"uri":"file:///ARCHITECTURE.md","name":"ARCH"}]}})
        break
    elif method == "prompts/list":
        write_frame({"jsonrpc":"2.0","id":msg["id"],"result":{"prompts":[{"name":"review","description":"리뷰","arguments":[{"name":"path","description":"대상","required":True}]},{"name":"summarize","description":"요약"}]}})
        break
`)
	store := openTestStore(t)
	resource, err := store.SaveResource(context.Background(), session.Resource{Kind: session.ResourceMCPServer, Name: "fake", Enabled: true, Config: []byte(`{"kind":"stdio","command":"python3","args":["` + serverPath + `"],"timeout":3}`)})
	if err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, store, "")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/mcp/servers/"+resource.ID+"/resources?limit=1", nil)
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
	if resources.Limit != 1 || resources.NextOffset != 1 || !resources.ResultTruncated {
		t.Fatalf("MCP resources/list page metadata가 이상해요: %+v", resources)
	}
	for _, query := range []string{"limit=-1", "limit=abc", "offset=-1", "offset=abc"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/mcp/servers/"+resource.ID+"/resources?"+query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("잘못된 MCP resources/list query는 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
		if strings.Contains(query, "limit") && !strings.Contains(rec.Body.String(), "limit") {
			t.Fatalf("MCP resources/list limit 오류는 limit을 설명해야 해요: query=%s body=%s", query, rec.Body.String())
		}
		if strings.Contains(query, "offset") && !strings.Contains(rec.Body.String(), "offset") {
			t.Fatalf("MCP resources/list offset 오류는 offset을 설명해야 해요: query=%s body=%s", query, rec.Body.String())
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/mcp/servers/"+resource.ID+"/prompts?limit=1&offset=1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("prompts status = %d body = %s", rec.Code, rec.Body.String())
	}
	var prompts MCPPromptListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &prompts); err != nil {
		t.Fatal(err)
	}
	if prompts.Server.ID != resource.ID || len(prompts.Prompts) != 1 || prompts.Prompts[0].Name != "summarize" || prompts.Limit != 1 || prompts.Offset != 1 || prompts.NextOffset != 0 || prompts.ResultTruncated {
		t.Fatalf("MCP prompts/list 결과가 이상해요: %+v", prompts)
	}
	for _, query := range []string{"limit=-1", "limit=abc", "offset=-1", "offset=abc"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/mcp/servers/"+resource.ID+"/prompts?"+query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("잘못된 MCP prompts/list query는 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
		if strings.Contains(query, "limit") && !strings.Contains(rec.Body.String(), "limit") {
			t.Fatalf("MCP prompts/list limit 오류는 limit을 설명해야 해요: query=%s body=%s", query, rec.Body.String())
		}
		if strings.Contains(query, "offset") && !strings.Contains(rec.Body.String(), "offset") {
			t.Fatalf("MCP prompts/list offset 오류는 offset을 설명해야 해요: query=%s body=%s", query, rec.Body.String())
		}
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

	req := httptest.NewRequest(http.MethodGet, "/api/v1/mcp/servers/"+resource.ID+"/resources/read?uri="+url.QueryEscape("file:///README.md")+"&max_content_bytes=7", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("resource read status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resourceRead MCPResourceReadResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resourceRead); err != nil {
		t.Fatal(err)
	}
	if resourceRead.URI != "file:///README.md" || len(resourceRead.Contents) != 1 || resourceRead.Contents[0].Text != "hello r" || resourceRead.ContentBytes <= 7 || !resourceRead.ContentTruncated {
		t.Fatalf("MCP resources/read 결과가 이상해요: %+v", resourceRead)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/mcp/servers/"+resource.ID+"/resources/read?uri="+url.QueryEscape("file:///README.md")+"&max_content_bytes=-1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_content_bytes") {
		t.Fatalf("음수 max_content_bytes는 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/mcp/servers/"+resource.ID+"/resources/read?uri="+url.QueryEscape("file:///"+strings.Repeat("x", maxMCPProbeURIBytes+1)), nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "resource uri") {
		t.Fatalf("긴 MCP resource uri는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/mcp/servers/"+resource.ID+"/prompts/review/get", bytes.NewBufferString(`{"arguments":{"path":"main.go"},"max_message_bytes":12}`))
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
	if promptGet.Prompt != "review" || len(promptGet.Messages) != 1 || promptGet.Messages[0].Content["text"] != "review revie" || promptGet.MessageBytes <= 12 || !promptGet.MessageTruncated {
		t.Fatalf("MCP prompts/get 결과가 이상해요: %+v", promptGet)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/mcp/servers/"+resource.ID+"/prompts/%20review%20/get", bytes.NewBufferString(`{"arguments":{"path":"main.go"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("space-padded prompt get status = %d body = %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &promptGet); err != nil {
		t.Fatal(err)
	}
	if promptGet.Prompt != "review" {
		t.Fatalf("MCP prompt response는 canonical prompt name을 반환해야 해요: %+v", promptGet)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/mcp/servers/"+resource.ID+"/prompts/review/get", bytes.NewBufferString(`{"max_message_bytes":-1}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_mcp_prompt") {
		t.Fatalf("음수 max_message_bytes는 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/mcp/servers/"+resource.ID+"/prompts/review/get", bytes.NewBufferString(`{"max_message_bytes":`+strconv.Itoa(maxMCPProbeOutputBytes+1)+`}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_message_bytes") {
		t.Fatalf("큰 max_message_bytes는 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/mcp/servers/"+resource.ID+"/prompts/"+strings.Repeat("x", maxMCPProbeNameBytes+1)+"/get", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "prompt 이름") {
		t.Fatalf("긴 MCP prompt 이름은 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/mcp/servers/"+resource.ID+"/prompts/review/get", bytes.NewBufferString(`{"arguments":{"fill":"`+strings.Repeat("x", maxMCPProbeArgumentsBytes+1)+`"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "arguments") {
		t.Fatalf("큰 MCP prompt arguments는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/mcp/servers/"+resource.ID+"/prompts/%20%20/get", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_mcp_prompt") {
		t.Fatalf("빈 MCP prompt 이름은 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
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

	req = httptest.NewRequest(http.MethodPost, "/api/v1/mcp/servers/"+resource.ID+"/tools/%20echo%20/call", bytes.NewBufferString(`{"arguments":{"name":"kkode"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("space-padded MCP tool name status = %d body = %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Tool != "echo" {
		t.Fatalf("MCP tool response는 canonical tool name을 반환해야 해요: %+v", got)
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

	req = httptest.NewRequest(http.MethodPost, "/api/v1/mcp/servers/"+resource.ID+"/tools/echo/call", bytes.NewBufferString(`{"max_output_bytes":-1}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_mcp_tool_call") {
		t.Fatalf("음수 MCP max_output_bytes는 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/mcp/servers/"+resource.ID+"/tools/echo/call", bytes.NewBufferString(`{"max_output_bytes":`+strconv.Itoa(maxMCPProbeOutputBytes+1)+`}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_output_bytes") {
		t.Fatalf("큰 MCP max_output_bytes는 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/mcp/servers/"+resource.ID+"/tools/"+strings.Repeat("x", maxMCPProbeNameBytes+1)+"/call", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "tool 이름") {
		t.Fatalf("긴 MCP tool 이름은 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/mcp/servers/"+resource.ID+"/tools/echo/call", bytes.NewBufferString(`{"arguments":{"fill":"`+strings.Repeat("x", maxMCPProbeArgumentsBytes+1)+`"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "arguments") {
		t.Fatalf("큰 MCP tool arguments는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/mcp/servers/"+resource.ID+"/tools/%20%20/call", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_mcp_tool_call") {
		t.Fatalf("빈 MCP tool 이름은 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
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

func TestReadLimitedBodyRejectsOversizedHTTPMCPResponse(t *testing.T) {
	data, err := ktools.ReadLimitedMCPBody(strings.NewReader("12345"), 5)
	if err != nil || string(data) != "12345" {
		t.Fatalf("제한 안의 HTTP MCP body는 읽어야 해요: data=%q err=%v", data, err)
	}
	if _, err := ktools.ReadLimitedMCPBody(strings.NewReader("123456"), 5); err == nil || !strings.Contains(err.Error(), "너무 커요") {
		t.Fatalf("제한을 넘는 HTTP MCP body는 거부해야 해요: %v", err)
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
	writeTestFile(t, filepath.Join(root, "CHANGELOG.md"), "changes\n")
	runTestGit(t, root, "add", "CHANGELOG.md")
	runTestGit(t, root, "commit", "-m", "second")
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

	req = httptest.NewRequest(http.MethodGet, "/api/v1/git/status?project_root="+url.QueryEscape(root)+"&limit=1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("limited status = %d body = %s", rec.Code, rec.Body.String())
	}
	status = GitStatusResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Entries) != 1 || status.TotalEntries < 2 || status.Limit != 1 || !status.EntriesTruncated {
		t.Fatalf("git status 제한 metadata가 이상해요: %+v", status)
	}
	for _, query := range []string{"limit=-1", "limit=abc"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/git/status?project_root="+url.QueryEscape(root)+"&"+query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "limit") {
			t.Fatalf("잘못된 git status limit은 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/git/diff?project_root="+url.QueryEscape(root)+"&path="+url.QueryEscape(" ./README.md "), nil)
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

	req = httptest.NewRequest(http.MethodGet, "/api/v1/git/diff?project_root="+url.QueryEscape(root)+"&max_bytes=-1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_bytes") {
		t.Fatalf("음수 git diff max_bytes는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/git/diff?project_root="+url.QueryEscape(root)+"&max_bytes=abc", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_bytes") {
		t.Fatalf("잘못된 git diff max_bytes는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
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
	if len(log.Commits) != 1 || log.Commits[0].Subject != "second" || log.Limit != 1 || !log.CommitsTruncated {
		t.Fatalf("git log 응답이 이상해요: %+v", log)
	}
	for _, query := range []string{"limit=-1", "limit=abc"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/git/log?project_root="+url.QueryEscape(root)+"&"+query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "limit") {
			t.Fatalf("잘못된 git log limit은 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
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
	if !hasTool(listed.Tools, "file_write") || !hasTool(listed.Tools, "file_delete") || !hasTool(listed.Tools, "file_move") || !hasTool(listed.Tools, "file_restore_checkpoint") || !hasTool(listed.Tools, "file_prune_checkpoints") || !hasTool(listed.Tools, "web_fetch") || !hasTool(listed.Tools, "shell_run") {
		t.Fatalf("표준 tool 목록이 부족해요: %+v", listed.Tools)
	}
	if listed.TotalTools != len(listed.Tools) || listed.Limit != len(listed.Tools) || listed.Offset != 0 || listed.NextOffset != 0 || listed.ResultTruncated {
		t.Fatalf("tool 목록 metadata가 이상해요: %+v", listed)
	}
	seenTools := map[string]bool{}
	for _, tool := range listed.Tools {
		if seenTools[tool.Name] {
			t.Fatalf("tool 목록에 중복 이름이 있으면 안 돼요: %+v", listed.Tools)
		}
		seenTools[tool.Name] = true
	}
	if !findTool(listed.Tools, "file_write").RequiresWorkspace || findTool(listed.Tools, "web_fetch").RequiresWorkspace {
		t.Fatalf("tool별 workspace 요구 여부를 discovery해야 해요: %+v", listed.Tools)
	}
	if findTool(listed.Tools, "file_write").Category != "file" || findTool(listed.Tools, "file_write").Effects[0] != "write" || findTool(listed.Tools, "file_prune_checkpoints").OutputFormat != "json" || findTool(listed.Tools, "shell_run").Effects[0] != "execute" || findTool(listed.Tools, "web_fetch").OutputFormat != "json" {
		t.Fatalf("tool별 category/effects/output_format discovery가 필요해요: %+v", listed.Tools)
	}
	if findTool(listed.Tools, "file_write").ExampleArguments["path"] == "" || findTool(listed.Tools, "file_delete").ExampleArguments["path"] == "" || findTool(listed.Tools, "file_move").ExampleArguments["source"] == "" || findTool(listed.Tools, "file_restore_checkpoint").ExampleArguments["checkpoint_id"] == "" || findTool(listed.Tools, "file_prune_checkpoints").ExampleArguments["keep_latest"] == nil || findTool(listed.Tools, "web_fetch").ExampleArguments["url"] == "" {
		t.Fatalf("adapter form 생성을 위한 tool 예제가 필요해요: %+v", listed.Tools)
	}
	if findTool(listed.Tools, "file_list").ExampleArguments["limit"] == nil || findTool(listed.Tools, "file_glob").ExampleArguments["limit"] == nil || findTool(listed.Tools, "lsp_document_symbols").ExampleArguments["limit"] == nil {
		t.Fatalf("bounded list tool 예제에는 limit이 필요해요: %+v", listed.Tools)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/tools?limit=1&offset=1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tool page status = %d body = %s", rec.Code, rec.Body.String())
	}
	var page ToolListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Tools) != 1 || page.TotalTools != listed.TotalTools || page.Limit != 1 || page.Offset != 1 {
		t.Fatalf("tool page가 이상해요: %+v", page)
	}
	if wantTruncated := page.TotalTools > 2; page.ResultTruncated != wantTruncated {
		t.Fatalf("tool page truncation flag가 이상해요: got=%v want=%v page=%+v", page.ResultTruncated, wantTruncated, page)
	}
	if page.ResultTruncated && page.NextOffset != 2 {
		t.Fatalf("tool page next offset이 이상해요: %+v", page)
	}
	if !page.ResultTruncated && page.NextOffset != 0 {
		t.Fatalf("tool page next offset이 없어야 해요: %+v", page)
	}
	for _, query := range []string{"limit=-1", "limit=abc", "offset=-1", "offset=abc"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/tools?"+query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("잘못된 tool list query는 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
		if strings.Contains(query, "limit") && !strings.Contains(rec.Body.String(), "limit") {
			t.Fatalf("tool list limit 오류는 limit을 설명해야 해요: query=%s body=%s", query, rec.Body.String())
		}
		if strings.Contains(query, "offset") && !strings.Contains(rec.Body.String(), "offset") {
			t.Fatalf("tool list offset 오류는 offset을 설명해야 해요: query=%s body=%s", query, rec.Body.String())
		}
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
	if detail.Name != "web_fetch" || detail.Category != "web" || detail.Effects[0] != "network" || detail.OutputFormat != "json" || detail.RequiresWorkspace || detail.ExampleArguments["url"] == "" {
		t.Fatalf("tool 상세 discovery가 이상해요: %+v", detail)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(`{"tool":"missing_tool"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "tool_not_found") {
		t.Fatalf("없는 tool 직접 호출은 404여야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(`{"tool":"web_fetch","max_output_bytes":-1}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_output_bytes") {
		t.Fatalf("음수 max_output_bytes는 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(`{"tool":"web_fetch","max_output_bytes":`+strconv.Itoa(maxToolCallOutputBytes+1)+`}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_output_bytes") {
		t.Fatalf("큰 max_output_bytes는 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(`{"tool":"web_fetch","web_max_bytes":-1}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "web_max_bytes") {
		t.Fatalf("음수 web_max_bytes는 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(`{"tool":"web_fetch","web_max_bytes":`+strconv.FormatInt(maxToolCallWebBytes+1, 10)+`}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "web_max_bytes") {
		t.Fatalf("큰 web_max_bytes는 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(`{"tool":"`+strings.Repeat("x", maxToolCallNameBytes+1)+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "tool") {
		t.Fatalf("긴 tool 이름은 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(`{"tool":"web_fetch","call_id":"`+strings.Repeat("x", maxToolCallIDBytes+1)+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "call_id") {
		t.Fatalf("긴 call_id는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(`{"tool":"web_fetch","store_artifact":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "artifact_session_id") {
		t.Fatalf("store_artifact without artifact_session_id는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(`{"tool":"web_fetch","arguments":{"url":"https://example.test","fill":"`+strings.Repeat("x", maxToolCallArgumentsBytes+1)+`"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "arguments") {
		t.Fatalf("큰 tool arguments는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	root := t.TempDir()
	body := `{"project_root":"` + root + `","tool":"file_write","arguments":{"path":"notes/todo.md","content":"hello"},"call_id":" call_1 "}`
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

	body = `{"project_root":"` + root + `","tool":"file_move","arguments":{"source":"notes/todo.md","destination":"notes/moved.md"},"call_id":"move_1"}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("file_move status = %d body = %s", rec.Code, rec.Body.String())
	}
	called = ToolCallResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &called); err != nil {
		t.Fatal(err)
	}
	if called.CallID != "move_1" || called.Tool != "file_move" || called.Error != "" {
		t.Fatalf("file_move tool call 응답이 이상해요: %+v", called)
	}
	data, err = os.ReadFile(filepath.Join(root, "notes", "moved.md"))
	if err != nil || string(data) != "hello" {
		t.Fatalf("file_move가 실행되지 않았어요: data=%q err=%v", data, err)
	}

	body = `{"project_root":"` + root + `","tool":"file_delete","arguments":{"path":"notes/moved.md"},"call_id":"delete_1"}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("file_delete status = %d body = %s", rec.Code, rec.Body.String())
	}
	called = ToolCallResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &called); err != nil {
		t.Fatal(err)
	}
	if called.CallID != "delete_1" || called.Tool != "file_delete" || called.Error != "" {
		t.Fatalf("file_delete tool call 응답이 이상해요: %+v", called)
	}
	deleteCheckpoint := checkpointIDFromToolOutput(t, called.Output)
	if _, err := os.Stat(filepath.Join(root, "notes", "moved.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file_delete가 실행되지 않았어요: %v", err)
	}
	body = `{"project_root":"` + root + `","tool":"file_restore_checkpoint","arguments":{"checkpoint_id":"` + deleteCheckpoint + `"},"call_id":"restore_1"}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("file_restore_checkpoint status = %d body = %s", rec.Code, rec.Body.String())
	}
	called = ToolCallResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &called); err != nil {
		t.Fatal(err)
	}
	if called.CallID != "restore_1" || called.Tool != "file_restore_checkpoint" || called.Error != "" {
		t.Fatalf("file_restore_checkpoint tool call 응답이 이상해요: %+v", called)
	}
	data, err = os.ReadFile(filepath.Join(root, "notes", "moved.md"))
	if err != nil || string(data) != "hello" {
		t.Fatalf("file_restore_checkpoint가 실행되지 않았어요: data=%q err=%v", data, err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes", "todo.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	artifactSession := session.NewSession(root, "openai", "gpt-5-mini", "agent", session.AgentModeBuild)
	artifactTurn := session.NewTurn("direct tool", llm.Request{Model: "gpt-5-mini"})
	artifactTurn.ID = "turn_tool"
	artifactSession.AppendTurn(artifactTurn)
	if err := store.CreateSession(context.Background(), artifactSession); err != nil {
		t.Fatal(err)
	}

	body = `{"project_root":"` + root + `","tool":"file_read","arguments":{"path":"notes/todo.md"},"max_output_bytes":2,"artifact_session_id":"` + artifactSession.ID + `","artifact_turn_id":"turn_tool","artifact_name":"todo preview"}`
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
	if called.Artifact == nil || called.Artifact.ID == "" || called.Artifact.SessionID != artifactSession.ID || called.Artifact.TurnID != "turn_tool" || called.Artifact.Content != nil || called.Artifact.ContentBytes == 0 {
		t.Fatalf("truncated tool output artifact 응답이 이상해요: %+v", called.Artifact)
	}
	loadedArtifact, err := store.LoadArtifact(context.Background(), called.Artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	var artifactPayload map[string]any
	if err := json.Unmarshal(loadedArtifact.Content, &artifactPayload); err != nil {
		t.Fatal(err)
	}
	if loadedArtifact.Name != "todo preview" || loadedArtifact.Kind != "tool_output" || artifactPayload["output"] != "hello" || artifactPayload["tool"] != "file_read" {
		t.Fatalf("저장된 tool artifact가 이상해요: artifact=%+v payload=%+v", loadedArtifact, artifactPayload)
	}

	if err := os.WriteFile(filepath.Join(root, "notes", "large.txt"), []byte(strings.Repeat("x", defaultToolCallOutputBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	body = `{"project_root":"` + root + `","tool":"file_read","arguments":{"path":"notes/large.txt"}}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	called = ToolCallResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &called); err != nil {
		t.Fatal(err)
	}
	if called.OutputBytes != defaultToolCallOutputBytes+1 || len(called.Output) != defaultToolCallOutputBytes || !called.OutputTruncated {
		t.Fatalf("tool output 기본 제한 응답이 이상해요: %+v", called)
	}

	body = `{"project_root":"` + root + `","tool":"shell_run","arguments":{"command":"sh","args":["-c","echo out; echo err >&2; exit 7"],"timeout_ms":1000}}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("non-zero shell_run should return structured output: status=%d body=%s", rec.Code, rec.Body.String())
	}
	called = ToolCallResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &called); err != nil {
		t.Fatal(err)
	}
	var command workspace.CommandResult
	if err := json.Unmarshal([]byte(called.Output), &command); err != nil {
		t.Fatal(err)
	}
	if called.Error != "" || command.ExitCode != 7 || command.Stdout != "out\n" || !strings.Contains(command.Stderr, "err") || command.DurationMS < 0 {
		t.Fatalf("non-zero shell_run result가 이상해요: response=%+v command=%+v", called, command)
	}

	body = `{"project_root":"` + root + `","tool":"shell_run","arguments":{"command":"definitely-missing-kkode-command","timeout_ms":1000}}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "definitely-missing-kkode-command") {
		t.Fatalf("missing shell command should remain a tool error: status=%d body=%s", rec.Code, rec.Body.String())
	}

	body = `{"project_root":"` + root + `","tool":"file_read","arguments":{"path":"notes/todo.md","max_bytes":-1}}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_bytes") {
		t.Fatalf("음수 file_read max_bytes는 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	body = `{"project_root":"` + root + `","tool":"shell_run","arguments":{"command":"echo","timeout_ms":-1}}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "timeout_ms") {
		t.Fatalf("음수 shell_run timeout_ms는 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	body = `{"project_root":"` + root + `","tool":"shell_run","arguments":{"command":"echo","timeout_ms":` + strconv.FormatInt(workspace.MaxCommandTimeout.Milliseconds()+1, 10) + `}}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "timeout_ms") {
		t.Fatalf("큰 shell_run timeout_ms는 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
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

	body = `{"tool":"web_fetch","arguments":{"url":"` + upstream.URL + `","max_bytes":-1}}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_bytes") {
		t.Fatalf("음수 web_fetch max_bytes는 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	body = `{"tool":"web_fetch","arguments":{"url":"` + upstream.URL + `","max_bytes":` + strconv.FormatInt(maxToolCallWebBytes+1, 10) + `}}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_bytes") {
		t.Fatalf("큰 web_fetch max_bytes는 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte("slow"))
	}))
	defer slow.Close()
	body = `{"tool":"web_fetch","arguments":{"url":"` + slow.URL + `"},"timeout_ms":5}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "context deadline exceeded") {
		t.Fatalf("tool 직접 호출 timeout이 적용돼야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGatewayListsAndCallsLSPTools(t *testing.T) {
	store := openTestStore(t)
	srv := newTestServer(t, store, "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\n// Runner runs things.\ntype Runner struct{}\n\nfunc (Runner) Run() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
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
	if !hasTool(listed.Tools, "lsp_symbols") || !hasTool(listed.Tools, "lsp_hover") || !findTool(listed.Tools, "lsp_symbols").RequiresWorkspace {
		t.Fatalf("LSP tool discovery가 필요해요: %+v", listed.Tools)
	}
	if findTool(listed.Tools, "lsp_symbols").Category != "codeintel" || findTool(listed.Tools, "lsp_symbols").OutputFormat != "json" || findTool(listed.Tools, "lsp_symbols").ExampleArguments["query"] == "" {
		t.Fatalf("LSP tool metadata가 이상해요: %+v", findTool(listed.Tools, "lsp_symbols"))
	}

	body := `{"project_root":"` + root + `","tool":"lsp_symbols","arguments":{"query":"Runner","limit":10},"call_id":" lsp_1 "}`
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
	if called.CallID != "lsp_1" || called.Tool != "lsp_symbols" || called.Error != "" {
		t.Fatalf("LSP tool call 응답이 이상해요: %+v", called)
	}
	var symbols LSPSymbolListResponse
	if err := json.Unmarshal([]byte(called.Output), &symbols); err != nil {
		t.Fatalf("LSP tool output은 JSON이어야 해요: %v output=%s", err, called.Output)
	}
	if len(symbols.Symbols) == 0 || symbols.Symbols[0].Name != "Runner" {
		t.Fatalf("LSP symbols tool 결과가 이상해요: %+v", symbols)
	}

	body = `{"project_root":"` + root + `","tool":"lsp_symbols","arguments":{"query":"Runner","limit":-1}}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "limit") {
		t.Fatalf("음수 LSP tool limit은 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	body = `{"project_root":"` + root + `","tool":"lsp_symbols","arguments":{"query":"Runner","limit":1.5}}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/tools/call", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "integer") {
		t.Fatalf("fractional LSP tool limit은 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	body = `{"project_root":"` + root + `","tool":"lsp_hover","arguments":{"symbol":"Runner"}}`
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
	var hover LSPHoverResponse
	if err := json.Unmarshal([]byte(called.Output), &hover); err != nil {
		t.Fatalf("LSP hover output은 JSON이어야 해요: %v output=%s", err, called.Output)
	}
	if !hover.Found || !strings.Contains(hover.Documentation, "runs things") {
		t.Fatalf("LSP hover tool 결과가 이상해요: %+v", hover)
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

	req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/todos", bytes.NewBufferString(`{"id":"todo_2","content":"배포해요","status":"pending"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/todos?limit=1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	listed = TodoListResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Todos) != 1 || listed.TotalTodos != 2 || listed.Limit != 1 || listed.NextOffset != 1 || !listed.ResultTruncated {
		t.Fatalf("todo list pagination metadata가 이상해요: %+v", listed)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/todos?status=pending&limit=10", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	listed = TodoListResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Todos) != 1 || listed.Todos[0].ID != "todo_2" || listed.TotalTodos != 1 || listed.NextOffset != 0 || listed.ResultTruncated {
		t.Fatalf("todo status filter가 이상해요: %+v", listed)
	}
	for _, query := range []string{"limit=-1", "limit=abc", "offset=-1", "offset=abc", "status=invalid"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/todos?"+query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("잘못된 todo list query는 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
		if strings.Contains(query, "limit") && !strings.Contains(rec.Body.String(), "limit") {
			t.Fatalf("todo list limit 오류는 limit을 설명해야 해요: query=%s body=%s", query, rec.Body.String())
		}
		if strings.Contains(query, "offset") && !strings.Contains(rec.Body.String(), "offset") {
			t.Fatalf("todo list offset 오류는 offset을 설명해야 해요: query=%s body=%s", query, rec.Body.String())
		}
		if strings.Contains(query, "status") && !strings.Contains(rec.Body.String(), "status") {
			t.Fatalf("todo list status 오류는 status를 설명해야 해요: query=%s body=%s", query, rec.Body.String())
		}
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
	if len(loaded.Todos) != 1 || loaded.Todos[0].ID != "todo_2" {
		t.Fatalf("todo가 삭제되지 않았어요: %+v", loaded.Todos)
	}

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "bad id", body: `{"id":"bad id","content":"검증해요"}`, want: "todo id"},
		{name: "long content", body: `{"content":"` + strings.Repeat("x", maxTodoContentBytes+1) + `"}`, want: "todo content"},
		{name: "long priority", body: `{"content":"검증해요","priority":"` + strings.Repeat("x", maxTodoPriorityBytes+1) + `"}`, want: "todo priority"},
	} {
		req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/todos", bytes.NewBufferString(tc.body))
		req.Header.Set("Content-Type", "application/json")
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s todo는 400이어야 해요: status=%d body=%s", tc.name, rec.Code, rec.Body.String())
		}
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
	req := httptest.NewRequest(http.MethodGet, "/api/v1/skills/"+resource.ID+"/preview?max_bytes=7", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var preview SkillPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Skill.ID != resource.ID || preview.File == "" || preview.MarkdownBytes <= len(preview.Markdown) || !preview.MarkdownTruncated || !preview.Truncated || !utf8.ValidString(preview.Markdown) || strings.Contains(preview.Markdown, "\uFFFD") {
		t.Fatalf("skill preview가 이상해요: %+v", preview)
	}

	largeSkillDir := filepath.Join(root, "large-review")
	largeMarkdown := strings.Repeat("x", maxSkillPreviewBytes+1)
	writeTestFile(t, filepath.Join(largeSkillDir, "SKILL.md"), largeMarkdown)
	largeResource, err := store.SaveResource(context.Background(), session.Resource{Kind: session.ResourceSkill, Name: "large-review", Enabled: true, Config: []byte(`{"path":"` + largeSkillDir + `"}`)})
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/skills/"+largeResource.ID+"/preview?max_bytes=32", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	preview = SkillPreviewResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.MarkdownBytes != len(largeMarkdown) || len(preview.Markdown) != 32 || !preview.MarkdownTruncated {
		t.Fatalf("large skill preview metadata가 이상해요: %+v", preview)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/skills/"+resource.ID+"/preview?max_bytes=-1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_bytes") {
		t.Fatalf("음수 skill preview max_bytes는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/skills/"+resource.ID+"/preview?max_bytes=abc", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_bytes") {
		t.Fatalf("잘못된 skill preview max_bytes는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/skills/"+resource.ID+"/preview?max_bytes="+strconv.Itoa(maxSkillPreviewBytes+1), nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_bytes") {
		t.Fatalf("큰 skill preview max_bytes는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGatewayPreviewsSubagentManifest(t *testing.T) {
	store := openTestStore(t)
	resource, err := store.SaveResource(context.Background(), session.Resource{Kind: session.ResourceSubagent, Name: "planner", Description: "계획 agent예요", Enabled: true, Config: []byte(`{"display_name":"Planner","prompt":"계획을 세워요. 다음 작업을 정리해요.","tools":["file_read"],"skills":["review"],"mcp_server_ids":["mcp_context7"],"mcp_servers":{"fs":"mcp-fs","context7":{"kind":"http","url":"https://mcp.context7.com/mcp"}},"infer":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, store, "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/subagents/"+resource.ID+"/preview?max_prompt_bytes=10", nil)
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
	if preview.Subagent.ID != resource.ID || preview.DisplayName != "Planner" || preview.Prompt == "" || preview.PromptBytes <= len(preview.Prompt) || !preview.PromptTruncated || !utf8.ValidString(preview.Prompt) || strings.Contains(preview.Prompt, "\uFFFD") || len(preview.Tools) != 1 || preview.MCPServers["fs"] != "mcp-fs" || len(preview.MCPServerIDs) != 1 || context7["url"] != "https://mcp.context7.com/mcp" || preview.Infer == nil || !*preview.Infer {
		t.Fatalf("subagent preview가 이상해요: %+v", preview)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/subagents/"+resource.ID+"/preview?max_prompt_bytes=-1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_prompt_bytes") {
		t.Fatalf("음수 subagent preview max_prompt_bytes는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/subagents/"+resource.ID+"/preview?max_prompt_bytes=abc", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_prompt_bytes") {
		t.Fatalf("잘못된 subagent preview max_prompt_bytes는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGatewayCreatesAndReadsCheckpoints(t *testing.T) {
	store := openTestStore(t)
	sess := session.NewSession("/repo", "openai", "gpt", "agent", session.AgentModeBuild)
	turn1 := session.NewTurn("첫 요청", llm.Request{Model: "gpt"})
	turn1.ID = "turn_1"
	sess.AppendTurn(turn1)
	turn2 := session.NewTurn("둘째 요청", llm.Request{Model: "gpt"})
	turn2.ID = "turn_2"
	sess.AppendTurn(turn2)
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
	req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/checkpoints", bytes.NewBufferString(`{"id":"bad id","payload":{"summary":"bad"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "checkpoint id") {
		t.Fatalf("잘못된 checkpoint id는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	largePayload, err := json.Marshal(map[string]string{"summary": strings.Repeat("x", maxCheckpointPayloadBytes+1)})
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/checkpoints", bytes.NewBufferString(`{"payload":`+string(largePayload)+`}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "checkpoint payload") {
		t.Fatalf("너무 큰 checkpoint payload는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/checkpoints", bytes.NewBufferString(`{"turn_id":"turn_missing","payload":{"summary":"bad"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_checkpoint") {
		t.Fatalf("없는 turn checkpoint는 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions/sess_missing/checkpoints", bytes.NewBufferString(`{"payload":{"summary":"bad"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "session_not_found") {
		t.Fatalf("없는 session checkpoint는 404여야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/checkpoints", bytes.NewBufferString(`{"turn_id":"turn_2","payload":{"summary":"다음"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/checkpoints?limit=1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var listed CheckpointListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Checkpoints) != 1 {
		t.Fatalf("checkpoint 목록이 이상해요: %+v", listed)
	}
	if listed.Limit != 1 || listed.Offset != 0 || listed.NextOffset != 1 || !listed.ResultTruncated {
		t.Fatalf("checkpoint list metadata가 이상해요: %+v", listed)
	}
	firstPageID := listed.Checkpoints[0].ID
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/checkpoints?limit=1&offset=1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	listed = CheckpointListResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Checkpoints) != 1 || listed.Checkpoints[0].ID == firstPageID {
		t.Fatalf("checkpoint offset 목록이 이상해요: %+v", listed)
	}
	if listed.Limit != 1 || listed.Offset != 1 || listed.NextOffset != 0 || listed.ResultTruncated {
		t.Fatalf("checkpoint offset metadata가 이상해요: %+v", listed)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/checkpoints?turn_id=turn_1&limit=10", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	listed = CheckpointListResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Checkpoints) != 1 || listed.Checkpoints[0].ID != created.ID || listed.Checkpoints[0].TurnID != "turn_1" {
		t.Fatalf("checkpoint turn_id 목록이 이상해요: %+v", listed)
	}
	if listed.Limit != 10 || listed.Offset != 0 || listed.NextOffset != 0 || listed.ResultTruncated {
		t.Fatalf("checkpoint turn_id metadata가 이상해요: %+v", listed)
	}
	for _, query := range []string{"limit=-1", "limit=abc", "offset=-1", "offset=abc"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/checkpoints?"+query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("잘못된 checkpoint list query는 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
		if strings.Contains(query, "limit") && !strings.Contains(rec.Body.String(), "limit") {
			t.Fatalf("checkpoint list limit 오류는 limit을 설명해야 해요: query=%s body=%s", query, rec.Body.String())
		}
		if strings.Contains(query, "offset") && !strings.Contains(rec.Body.String(), "offset") {
			t.Fatalf("checkpoint list offset 오류는 offset을 설명해야 해요: query=%s body=%s", query, rec.Body.String())
		}
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

func TestGatewayCreatesListsReadsAndDeletesArtifacts(t *testing.T) {
	store := openTestStore(t)
	sess := session.NewSession("/repo", "openai", "gpt", "agent", session.AgentModeBuild)
	turn := session.NewTurn("artifact 요청", llm.Request{Model: "gpt"})
	turn.ID = "turn_artifact"
	sess.AppendTurn(turn)
	if err := store.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, store, "")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/artifacts", bytes.NewBufferString(`{"id":"artifact_1","turn_id":"turn_artifact","run_id":"run_1","kind":"tool_output","name":"grep","mime_type":"application/json","content":{"matches":1},"metadata":{"tool":"file_grep"}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var created ArtifactDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID != "artifact_1" || created.SessionID != sess.ID || created.TurnID != "turn_artifact" || created.ContentBytes == 0 || created.Metadata["tool"] != "file_grep" {
		t.Fatalf("artifact 생성 응답이 이상해요: %+v", created)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/artifacts", bytes.NewBufferString(`{"turn_id":"missing","kind":"tool_output","content":{"bad":true}}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_artifact") {
		t.Fatalf("없는 turn artifact는 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/artifacts?limit=1&kind=tool_output&run_id=run_1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var listed ArtifactListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Artifacts) != 1 || listed.Artifacts[0].ID != "artifact_1" || listed.Limit != 1 {
		t.Fatalf("artifact 목록이 이상해요: %+v", listed)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/artifact_1?max_content_bytes=4", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got ArtifactDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "artifact_1" || !got.ContentTruncated || got.ContentBytes == 0 {
		t.Fatalf("artifact 상세 truncation이 이상해요: %+v", got)
	}

	for _, id := range []string{"artifact_2", "artifact_3"} {
		req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/artifacts", bytes.NewBufferString(`{"id":"`+id+`","turn_id":"turn_artifact","run_id":"run_1","kind":"tool_output","content":{"matches":2}}`))
		req.Header.Set("Content-Type", "application/json")
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
		}
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/artifacts/prune", bytes.NewBufferString(`{"keep_latest":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var pruned ArtifactPruneResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &pruned); err != nil {
		t.Fatal(err)
	}
	if pruned.SessionID != sess.ID || pruned.KeepLatest != 1 || pruned.DeletedArtifacts != 2 {
		t.Fatalf("artifact prune 응답이 이상해요: %+v", pruned)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/artifacts?limit=10", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Artifacts) != 1 {
		t.Fatalf("artifact prune 후 목록이 이상해요: %+v", listed)
	}
	remainingID := listed.Artifacts[0].ID

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/artifacts/"+remainingID, nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/artifacts/"+remainingID, nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("deleted artifact는 404여야 해요: status=%d body=%s", rec.Code, rec.Body.String())
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
	if _, err := store.SaveArtifact(ctx, session.Artifact{ID: "artifact_export", SessionID: sess.ID, TurnID: turn.ID, Kind: "tool_output", Name: "result", Content: json.RawMessage(`{"text":"ok"}`)}); err != nil {
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
	if exported.FormatVersion != sessionExportFormatVersion || exported.Session.ID != sess.ID || exported.RawSession == nil || exported.RawSession.ID != sess.ID || len(exported.Turns) != 1 || len(exported.Events) != 1 || len(exported.Todos) != 1 || len(exported.Checkpoints) != 1 || len(exported.Artifacts) != 1 || len(exported.Runs) != 1 || len(exported.Resources) != 3 {
		t.Fatalf("session export가 이상해요: %+v", exported)
	}
	if exported.Counts.Turns != 1 || exported.Counts.Events != 1 || exported.Counts.Todos != 1 || exported.Counts.Checkpoints != 1 || exported.Counts.Artifacts != 1 || exported.Counts.Runs != 1 || exported.Counts.Resources != 3 {
		t.Fatalf("session export counts가 이상해요: %+v", exported.Counts)
	}
	if exported.Turns[0].ResponseText != "ok" || exported.RawSession.Turns[0].Response.Output[0].Content != "ok" || exported.Checkpoints[0].ID != "cp_export" || exported.Artifacts[0].ID != "artifact_export" || exported.Runs[0].ID != "run_export" || !hasResourceDTO(exported.Resources, mcpResource.ID) || !hasResourceDTO(exported.Resources, skillResource.ID) || !hasResourceDTO(exported.Resources, subagentResource.ID) {
		t.Fatalf("session export 상세가 이상해요: %+v", exported)
	}
}

func TestGatewayExportsBoundedSessionBundlePreview(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	sess := session.NewSession("/repo", "openai", "gpt-5-mini", "agent", session.AgentModeBuild)
	for _, prompt := range []string{"first", "second"} {
		turn := session.NewTurn(prompt, llm.Request{Model: "gpt-5-mini", Messages: []llm.Message{llm.UserText(prompt)}})
		turn.Response = llm.TextResponse("openai", "gpt-5-mini", "ok "+prompt)
		sess.AppendTurn(turn)
		sess.AppendEvent(session.Event{ID: "ev_export_" + prompt, SessionID: sess.ID, TurnID: turn.ID, Type: "turn.completed", At: time.Now().UTC()})
	}
	if err := store.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	for i, turn := range sess.Turns {
		if err := store.SaveCheckpoint(ctx, session.Checkpoint{ID: fmt.Sprintf("cp_bounded_%d", i), SessionID: sess.ID, TurnID: turn.ID, CreatedAt: time.Now().UTC().Add(time.Duration(i) * time.Second), Payload: json.RawMessage(`{"summary":"bounded"}`)}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SaveRun(ctx, session.Run{ID: fmt.Sprintf("run_bounded_%d", i), SessionID: sess.ID, Status: "completed", Prompt: turn.Prompt}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SaveArtifact(ctx, session.Artifact{ID: fmt.Sprintf("artifact_bounded_%d", i), SessionID: sess.ID, TurnID: turn.ID, Kind: "tool_output", Content: json.RawMessage(`{"summary":"bounded"}`)}); err != nil {
			t.Fatal(err)
		}
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
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/export?include_raw=false&turn_limit=1&event_limit=1&checkpoint_limit=1&artifact_limit=1&run_limit=1", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var exported SessionExportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &exported); err != nil {
		t.Fatal(err)
	}
	if exported.RawSession != nil || exported.RawSessionIncluded || len(exported.Turns) != 1 || len(exported.Events) != 1 || len(exported.Checkpoints) != 1 || len(exported.Artifacts) != 1 || len(exported.Runs) != 1 || exported.TurnLimit != 1 || exported.EventLimit != 1 || exported.CheckpointLimit != 1 || exported.ArtifactLimit != 1 || exported.RunLimit != 1 || !exported.CheckpointsTruncated || !exported.ArtifactsTruncated || !exported.RunsTruncated || !exported.ResultTruncated {
		t.Fatalf("bounded session export preview가 이상해요: %+v", exported)
	}

	invalidLimits := []struct {
		name  string
		query string
		want  string
	}{
		{name: "negative turn limit", query: "turn_limit=-1", want: "turn_limit"},
		{name: "bad turn limit", query: "turn_limit=abc", want: "turn_limit"},
		{name: "negative event limit", query: "event_limit=-1", want: "event_limit"},
		{name: "bad event limit", query: "event_limit=abc", want: "event_limit"},
		{name: "negative checkpoint limit", query: "checkpoint_limit=-1", want: "checkpoint_limit"},
		{name: "bad checkpoint limit", query: "checkpoint_limit=abc", want: "checkpoint_limit"},
		{name: "negative artifact limit", query: "artifact_limit=-1", want: "artifact_limit"},
		{name: "bad artifact limit", query: "artifact_limit=abc", want: "artifact_limit"},
		{name: "negative run limit", query: "run_limit=-1", want: "run_limit"},
		{name: "bad run limit", query: "run_limit=abc", want: "run_limit"},
		{name: "bad redact", query: "redact=maybe", want: "redact"},
		{name: "bad include raw", query: "include_raw=maybe", want: "include_raw"},
	}
	for _, tc := range invalidLimits {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/export?"+tc.query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s session export limit은 400이어야 해요: status=%d body=%s", tc.name, rec.Code, rec.Body.String())
		}
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
	if _, err := sourceStore.SaveArtifact(ctx, session.Artifact{ID: "artifact_import", SessionID: sess.ID, TurnID: turn.ID, Kind: "tool_output", Content: json.RawMessage(`{"text":"옮겨요"}`)}); err != nil {
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
	body, err := json.Marshal(SessionImportRequest{FormatVersion: exported.FormatVersion, RawSession: exported.RawSession, Checkpoints: exported.Checkpoints, Artifacts: exported.Artifacts, Runs: exported.Runs, Resources: exported.Resources, NewSessionID: "sess_imported"})
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
	if imported.Session.ID != "sess_imported" || !imported.RewrittenSessionID || imported.OriginalSessionID != sess.ID || imported.Counts.Turns != 1 || imported.Counts.Events != 1 || imported.Counts.Checkpoints != 1 || imported.Counts.Artifacts != 1 || imported.Counts.Runs != 1 || imported.Counts.Resources != 1 {
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
	artifact, err := targetStore.LoadArtifact(ctx, "artifact_import")
	if err != nil || artifact.SessionID != "sess_imported" || artifact.TurnID != turn.ID {
		t.Fatalf("import된 artifact가 이상해요: %+v err=%v", artifact, err)
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

func TestGatewayImportPreflightsArtifactsBeforeSavingSession(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	srv := newTestServer(t, store, "")
	sess := session.NewSession("/repo", "openai", "gpt-5-mini", "agent", session.AgentModeBuild)
	turn := session.NewTurn("go", llm.Request{Model: "gpt-5-mini"})
	sess.AppendTurn(turn)
	for _, tc := range []struct {
		name         string
		mutate       func(*session.Session)
		newSessionID string
		want         string
	}{
		{name: "bad session id", mutate: func(s *session.Session) { s.ID = "bad id" }, want: "session id"},
		{name: "bad new session id", newSessionID: "bad id", want: "session id"},
		{name: "bad session mode", mutate: func(s *session.Session) { s.Mode = "debug" }, want: "mode"},
		{name: "bad session metadata", mutate: func(s *session.Session) { s.Metadata = map[string]string{"bad key": "value"} }, want: "metadata key"},
		{name: "oversized last response id", mutate: func(s *session.Session) {
			s.LastResponseID = strings.Repeat("r", maxSessionLastResponseIDBytes+1)
		}, want: "last_response_id"},
		{name: "oversized last input items", mutate: func(s *session.Session) {
			s.LastInputItems = []llm.Item{{Type: llm.ItemMessage, Role: llm.RoleAssistant, Content: strings.Repeat("x", maxSessionLastInputItemsBytes+1)}}
		}, want: "last_input_items"},
		{name: "bad session todo", mutate: func(s *session.Session) {
			s.Todos = []session.Todo{{ID: "bad id", Content: "todo", Status: session.TodoPending}}
		}, want: "todo id"},
		{name: "missing turn id", mutate: func(s *session.Session) { s.Turns[0].ID = "" }, want: "turn id"},
		{name: "bad turn id", mutate: func(s *session.Session) { s.Turns[0].ID = "bad id" }, want: "turn id"},
		{name: "duplicate turn id", mutate: func(s *session.Session) {
			s.Turns = append(s.Turns, s.Turns[0])
		}, want: "turn id"},
		{name: "missing turn prompt", mutate: func(s *session.Session) { s.Turns[0].Prompt = " " }, want: "turn prompt"},
		{name: "oversized turn prompt", mutate: func(s *session.Session) {
			s.Turns[0].Prompt = strings.Repeat("x", maxSessionTurnPromptBytes+1)
		}, want: "turn prompt"},
		{name: "oversized turn request", mutate: func(s *session.Session) {
			s.Turns[0].Request.Messages = []llm.Message{llm.UserText(strings.Repeat("x", maxSessionTurnSnapshotBytes+1))}
		}, want: "turn request"},
		{name: "oversized turn response", mutate: func(s *session.Session) {
			s.Turns[0].Response = &llm.Response{Text: strings.Repeat("x", maxSessionTurnSnapshotBytes+1)}
		}, want: "turn response"},
		{name: "bad event id", mutate: func(s *session.Session) {
			s.Events = []session.Event{{ID: "bad id", SessionID: s.ID, Type: "turn.completed"}}
		}, want: "event id"},
		{name: "missing event type", mutate: func(s *session.Session) {
			s.Events = []session.Event{{ID: "ev_missing_type", SessionID: s.ID}}
		}, want: "event type"},
		{name: "missing event turn", mutate: func(s *session.Session) {
			s.Events = []session.Event{{ID: "ev_missing_turn", SessionID: s.ID, TurnID: "turn_missing", Type: "turn.completed"}}
		}, want: "event turn_id"},
		{name: "oversized event payload", mutate: func(s *session.Session) {
			payload, err := json.Marshal(map[string]string{"value": strings.Repeat("x", maxSessionEventPayloadBytes+1)})
			if err != nil {
				t.Fatal(err)
			}
			s.Events = []session.Event{{ID: "ev_huge_payload", SessionID: s.ID, Type: "tool.output", Payload: payload}}
		}, want: "event payload"},
	} {
		raw := *sess
		raw.Turns = append([]session.Turn(nil), sess.Turns...)
		raw.Events = append([]session.Event(nil), sess.Events...)
		raw.Todos = append([]session.Todo(nil), sess.Todos...)
		raw.Metadata = cloneMap(sess.Metadata)
		if tc.mutate != nil {
			tc.mutate(&raw)
		}
		body, err := json.Marshal(SessionImportRequest{
			FormatVersion: sessionExportFormatVersion,
			RawSession:    &raw,
			NewSessionID:  tc.newSessionID,
		})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/import", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s import는 400이어야 해요: status=%d body=%s", tc.name, rec.Code, rec.Body.String())
		}
		if _, err := store.LoadSession(ctx, sess.ID); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("%s preflight 실패 후 session이 저장되면 안 돼요: err=%v", tc.name, err)
		}
	}
	body, err := json.Marshal(SessionImportRequest{
		FormatVersion: sessionExportFormatVersion,
		RawSession:    sess,
		Checkpoints:   []CheckpointDTO{{ID: "cp_bad", TurnID: "turn_missing", Payload: json.RawMessage(`{"summary":"bad"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/import", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_import") {
		t.Fatalf("invalid artifact import는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := store.LoadSession(ctx, sess.ID); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("artifact preflight 실패 후 session이 저장되면 안 돼요: err=%v", err)
	}
	body, err = json.Marshal(SessionImportRequest{
		FormatVersion: sessionExportFormatVersion,
		RawSession:    sess,
		Checkpoints:   []CheckpointDTO{{ID: "bad id", TurnID: turn.ID, Payload: json.RawMessage(`{"summary":"bad"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions/import", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "checkpoint id") {
		t.Fatalf("invalid checkpoint id import는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := store.LoadSession(ctx, sess.ID); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("checkpoint id preflight 실패 후 session이 저장되면 안 돼요: err=%v", err)
	}
	body, err = json.Marshal(SessionImportRequest{
		FormatVersion: sessionExportFormatVersion,
		RawSession:    sess,
		Artifacts:     []ArtifactDTO{{ID: "artifact_bad_turn", TurnID: "turn_missing", Kind: "tool_output", Content: json.RawMessage(`{"summary":"bad"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions/import", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "artifact turn_id") {
		t.Fatalf("invalid artifact turn import는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := store.LoadSession(ctx, sess.ID); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("artifact turn preflight 실패 후 session이 저장되면 안 돼요: err=%v", err)
	}
	body, err = json.Marshal(SessionImportRequest{
		FormatVersion: sessionExportFormatVersion,
		RawSession:    sess,
		Artifacts:     []ArtifactDTO{{ID: "bad id", TurnID: turn.ID, Kind: "tool_output", Content: json.RawMessage(`{"summary":"bad"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions/import", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "artifact id") {
		t.Fatalf("invalid artifact id import는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := store.LoadSession(ctx, sess.ID); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("artifact id preflight 실패 후 session이 저장되면 안 돼요: err=%v", err)
	}
	for _, tc := range []struct {
		name string
		run  RunDTO
		want string
	}{
		{name: "bad run id", run: RunDTO{ID: "bad id", Status: "completed", TurnID: turn.ID}, want: "run id"},
		{name: "bad run status", run: RunDTO{ID: "run_bad_status", Status: "paused", TurnID: turn.ID}, want: "run status"},
		{name: "missing run turn", run: RunDTO{ID: "run_missing_turn", Status: "completed", TurnID: "turn_missing"}, want: "run turn_id"},
		{name: "bad run metadata", run: RunDTO{ID: "run_bad_metadata", Status: "completed", Metadata: map[string]string{"bad key": "value"}}, want: "metadata key"},
		{name: "huge run prompt", run: RunDTO{ID: "run_huge_prompt", Status: "completed", Prompt: strings.Repeat("x", maxRunPromptBytes+1)}, want: "prompt"},
		{name: "bad run resource selector", run: RunDTO{ID: "run_bad_selector", Status: "completed", MCPServers: []string{"bad id"}}, want: "mcp_servers[0]"},
	} {
		body, err = json.Marshal(SessionImportRequest{
			FormatVersion: sessionExportFormatVersion,
			RawSession:    sess,
			Runs:          []RunDTO{tc.run},
		})
		if err != nil {
			t.Fatal(err)
		}
		req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions/import", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s import는 400이어야 해요: status=%d body=%s", tc.name, rec.Code, rec.Body.String())
		}
		if _, err := store.LoadSession(ctx, sess.ID); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("%s preflight 실패 후 session이 저장되면 안 돼요: err=%v", tc.name, err)
		}
	}
}

func TestGatewayImportRejectsUnknownResourceKindBeforeSavingSession(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	srv := newTestServer(t, store, "")
	sess := session.NewSession("/repo", "openai", "gpt-5-mini", "agent", session.AgentModeBuild)
	body, err := json.Marshal(SessionImportRequest{
		FormatVersion: sessionExportFormatVersion,
		RawSession:    sess,
		Resources:     []ResourceDTO{{ID: "res_bad", Kind: "unknown", Name: "bad", Config: map[string]any{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/import", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_resource") {
		t.Fatalf("unknown resource kind import는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := store.LoadSession(ctx, sess.ID); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("resource preflight 실패 후 session이 저장되면 안 돼요: err=%v", err)
	}
	for _, tc := range []struct {
		name      string
		resources []ResourceDTO
		want      string
	}{
		{
			name:      "bad resource id",
			resources: []ResourceDTO{{ID: "bad id", Kind: string(session.ResourceMCPServer), Name: "bad", Config: map[string]any{"kind": "http", "url": "https://mcp.example.test"}}},
			want:      "resource id",
		},
		{
			name: "duplicate resource id",
			resources: []ResourceDTO{
				{ID: "mcp_dup", Kind: string(session.ResourceMCPServer), Name: "one", Config: map[string]any{"kind": "http", "url": "https://mcp.example.test/one"}},
				{ID: "mcp_dup", Kind: string(session.ResourceMCPServer), Name: "two", Config: map[string]any{"kind": "http", "url": "https://mcp.example.test/two"}},
			},
			want: "중복",
		},
		{
			name:      "oversized resource config",
			resources: []ResourceDTO{{ID: "mcp_huge", Kind: string(session.ResourceMCPServer), Name: "huge", Config: map[string]any{"kind": "http", "url": "https://mcp.example.test", "extra": strings.Repeat("x", maxResourceConfigBytes+1)}}},
			want:      "resource config",
		},
	} {
		body, err = json.Marshal(SessionImportRequest{
			FormatVersion: sessionExportFormatVersion,
			RawSession:    sess,
			Resources:     tc.resources,
		})
		if err != nil {
			t.Fatal(err)
		}
		req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions/import", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s import는 400이어야 해요: status=%d body=%s", tc.name, rec.Code, rec.Body.String())
		}
		if _, err := store.LoadSession(ctx, sess.ID); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("%s preflight 실패 후 session이 저장되면 안 돼요: err=%v", tc.name, err)
		}
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
	writeTestFile(t, filepath.Join(root, "docs", "b.md"), "second")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/files?project_root="+root+"&path=docs&limit=1", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var listed FileListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Entries) != 1 || listed.Entries[0].Name != "a.md" || listed.Entries[0].Kind != "file" || listed.TotalEntries != 2 || listed.Limit != 1 || !listed.EntriesTruncated {
		t.Fatalf("files list가 이상해요: %+v", listed)
	}
	if listed.Offset != 0 || listed.NextOffset != 1 {
		t.Fatalf("files list paging metadata가 이상해요: %+v", listed)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/files?project_root="+root+"&path=docs&limit=1&offset=1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	listed = FileListResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Entries) != 1 || listed.Entries[0].Name != "b.md" || listed.TotalEntries != 2 || listed.Limit != 1 || listed.Offset != 1 || listed.NextOffset != 0 || listed.EntriesTruncated {
		t.Fatalf("files offset list가 이상해요: %+v", listed)
	}
	for i := 0; i < workspace.MaxListEntries+1; i++ {
		writeTestFile(t, filepath.Join(root, "bulk-list", fmt.Sprintf("entry-%05d.txt", i)), "x")
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/files?project_root="+root+"&path=bulk-list&limit="+strconv.Itoa(workspace.MaxListEntries), nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	listed = FileListResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Entries) != workspace.MaxListEntries || listed.TotalEntries != workspace.MaxListEntries || listed.NextOffset != 0 || !listed.EntriesTruncated {
		t.Fatalf("files list workspace envelope metadata가 이상해요: %+v", listed)
	}
	if listed.Entries[0].Name != "entry-00000.txt" || listed.Entries[len(listed.Entries)-1].Name != fmt.Sprintf("entry-%05d.txt", workspace.MaxListEntries-1) {
		t.Fatalf("files list workspace envelope should be lexically deterministic: first=%q last=%q", listed.Entries[0].Name, listed.Entries[len(listed.Entries)-1].Name)
	}
	invalidListParams := []struct {
		name  string
		query string
		want  string
	}{
		{name: "negative limit", query: "limit=-1", want: "limit"},
		{name: "bad limit", query: "limit=abc", want: "limit"},
		{name: "negative offset", query: "offset=-1", want: "offset"},
		{name: "bad offset", query: "offset=abc", want: "offset"},
	}
	for _, tc := range invalidListParams {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/files?project_root="+root+"&path=docs&"+tc.query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s file list param은 400이어야 해요: status=%d body=%s", tc.name, rec.Code, rec.Body.String())
		}
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

	body := `{"project_root":" ` + root + ` ","path":" docs/b.md ","content":"new"}`
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
	if err := json.Unmarshal(rec.Body.Bytes(), &content); err != nil {
		t.Fatal(err)
	}
	if content.ProjectRoot != root || content.Path != "docs/b.md" || content.CheckpointID == "" {
		t.Fatalf("file write 응답은 canonical project/path를 반환해야 해요: %+v", content)
	}

	body = `{"project_root":" ` + root + ` ","source":" docs/b.md ","destination":" docs/moved.md "}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/files/move", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var moved FileMoveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &moved); err != nil {
		t.Fatal(err)
	}
	if !moved.Moved || moved.ProjectRoot != root || moved.Source != "docs/b.md" || moved.Destination != "docs/moved.md" || moved.CheckpointID == "" {
		t.Fatalf("file move 응답이 이상해요: %+v", moved)
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "b.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file move는 source를 없애야 해요: %v", err)
	}
	data, err = os.ReadFile(filepath.Join(root, "docs", "moved.md"))
	if err != nil || string(data) != "new" {
		t.Fatalf("file move 결과가 이상해요: content=%q err=%v", data, err)
	}

	body = `{"project_root":"` + root + `","path":"docs/moved.md"}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/files/delete", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var deleted FileDeleteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &deleted); err != nil {
		t.Fatal(err)
	}
	if !deleted.Deleted || deleted.ProjectRoot != root || deleted.Path != "docs/moved.md" || deleted.CheckpointID == "" {
		t.Fatalf("file delete 응답이 이상해요: %+v", deleted)
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "moved.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file delete는 파일을 지워야 해요: %v", err)
	}

	body = `{"project_root":"` + root + `","checkpoint_id":"` + deleted.CheckpointID + `"}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/files/restore", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var restored FileRestoreResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &restored); err != nil {
		t.Fatal(err)
	}
	if !restored.Restored || restored.ProjectRoot != root || restored.CheckpointID != deleted.CheckpointID || restored.Entries == 0 {
		t.Fatalf("file restore 응답이 이상해요: %+v", restored)
	}
	data, err = os.ReadFile(filepath.Join(root, "docs", "moved.md"))
	if err != nil || string(data) != "new" {
		t.Fatalf("file restore 결과가 이상해요: content=%q err=%v", data, err)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/files/checkpoints?project_root="+url.QueryEscape(root)+"&limit=1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("checkpoint list status = %d body = %s", rec.Code, rec.Body.String())
	}
	var checkpointList FileCheckpointListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &checkpointList); err != nil {
		t.Fatal(err)
	}
	if len(checkpointList.Checkpoints) != 1 || checkpointList.TotalCheckpoints == 0 || checkpointList.Limit != 1 || checkpointList.Checkpoints[0].ID == "" || checkpointList.Checkpoints[0].Entries == 0 {
		t.Fatalf("file checkpoint list가 이상해요: %+v", checkpointList)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/files/checkpoints?project_root="+url.QueryEscape(root)+"&path="+url.QueryEscape("docs/moved.md")+"&limit=10", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("checkpoint path list status = %d body = %s", rec.Code, rec.Body.String())
	}
	checkpointList = FileCheckpointListResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &checkpointList); err != nil {
		t.Fatal(err)
	}
	if len(checkpointList.Checkpoints) == 0 || checkpointList.TotalCheckpoints != len(checkpointList.Checkpoints) || checkpointList.Limit != 10 || checkpointList.ResultTruncated {
		t.Fatalf("file checkpoint path list가 이상해요: %+v", checkpointList)
	}
	for _, cp := range checkpointList.Checkpoints {
		if !containsString(cp.Paths, "docs/moved.md") {
			t.Fatalf("path filter는 matching checkpoint만 반환해야 해요: %+v", checkpointList)
		}
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/files/checkpoints?project_root="+url.QueryEscape(root)+"&path="+url.QueryEscape("../outside"), nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_file_checkpoint_list") {
		t.Fatalf("workspace 밖 checkpoint path filter는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/files/checkpoints/"+deleted.CheckpointID+"?project_root="+url.QueryEscape(root), nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("checkpoint detail status = %d body = %s", rec.Code, rec.Body.String())
	}
	checkpointList = FileCheckpointListResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &checkpointList); err != nil {
		t.Fatal(err)
	}
	if len(checkpointList.Checkpoints) != 1 || checkpointList.Checkpoints[0].ID != deleted.CheckpointID || len(checkpointList.Checkpoints[0].Paths) == 0 {
		t.Fatalf("file checkpoint detail이 이상해요: %+v", checkpointList)
	}
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/files/checkpoints/"+deleted.CheckpointID+"?project_root="+url.QueryEscape(root), nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("checkpoint delete status = %d body = %s", rec.Code, rec.Body.String())
	}
	var checkpointDelete FileCheckpointDeleteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &checkpointDelete); err != nil {
		t.Fatal(err)
	}
	if !checkpointDelete.Deleted || checkpointDelete.ProjectRoot != root || checkpointDelete.CheckpointID != deleted.CheckpointID {
		t.Fatalf("file checkpoint delete가 이상해요: %+v", checkpointDelete)
	}
	body = `{"project_root":"` + root + `","keep_latest":0}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/files/checkpoints/prune", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("checkpoint prune status = %d body = %s", rec.Code, rec.Body.String())
	}
	var checkpointPrune FileCheckpointPruneResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &checkpointPrune); err != nil {
		t.Fatal(err)
	}
	if checkpointPrune.ProjectRoot != root || checkpointPrune.Kept != 0 || checkpointPrune.DeletedCount != len(checkpointPrune.Deleted) || checkpointPrune.TotalCheckpoints == 0 {
		t.Fatalf("file checkpoint prune가 이상해요: %+v", checkpointPrune)
	}
	body = `{"project_root":"` + root + `","keep_latest":-1}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/files/checkpoints/prune", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "keep_latest") {
		t.Fatalf("negative checkpoint prune는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	body = `{"project_root":"` + root + `","path":"docs"}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/files/delete", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "recursive") {
		t.Fatalf("directory delete without recursive는 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	writeTestFile(t, filepath.Join(root, "docs", "b.md"), "new")

	body = `{"project_root":"` + root + `","path":"docs/too-large.md","content":"` + strings.Repeat("x", workspace.MaxFileWriteBytes+1) + `"}`
	req = httptest.NewRequest(http.MethodPut, "/api/v1/files/content", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "content") {
		t.Fatalf("large file write는 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}

	body = `{"project_root":"` + root + `","patch_text":"` + strings.Repeat("x", workspace.MaxPatchBytes+1) + `"}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/files/patch", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "patch_text") {
		t.Fatalf("large file patch는 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
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

	writeTestFile(t, filepath.Join(root, "docs", "large.md"), strings.Repeat("x", defaultFileContentBytes+1))
	req = httptest.NewRequest(http.MethodGet, "/api/v1/files/content?project_root="+root+"&path=docs/large.md", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	content = FileContentResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &content); err != nil {
		t.Fatal(err)
	}
	if content.ContentBytes != defaultFileContentBytes || content.FileBytes != int64(defaultFileContentBytes+1) || !content.ContentTruncated {
		t.Fatalf("file content 기본 byte 제한 metadata가 이상해요: %+v", content)
	}

	invalidRanges := []struct {
		name  string
		query string
		want  string
	}{
		{name: "negative offset", query: "offset_line=-1", want: "offset_line"},
		{name: "bad offset", query: "offset_line=abc", want: "offset_line"},
		{name: "negative limit", query: "limit_lines=-1", want: "limit_lines"},
		{name: "bad limit", query: "limit_lines=abc", want: "limit_lines"},
		{name: "negative max bytes", query: "max_bytes=-1", want: "max_bytes"},
		{name: "bad max bytes", query: "max_bytes=abc", want: "max_bytes"},
		{name: "large max bytes", query: "max_bytes=" + strconv.Itoa(maxFileContentBytes+1), want: "max_bytes"},
	}
	for _, tc := range invalidRanges {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/files/content?project_root="+root+"&path=docs/b.md&"+tc.query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s file content range는 400이어야 해요: status=%d body=%s", tc.name, rec.Code, rec.Body.String())
		}
	}
}

func TestGatewayFilesAPIGrepsWorkspace(t *testing.T) {
	store := openTestStore(t)
	srv := newTestServer(t, store, "")
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "src", "a.go"), "package main\n// TODO: wire api\n")
	writeTestFile(t, filepath.Join(root, "src", "b.go"), "package main\n// TODO: second\n")
	writeTestFile(t, filepath.Join(root, "notes.txt"), "TODO outside\n")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/grep?project_root="+url.QueryEscape(root)+"&pattern=todo&path_glob=src/**&max_matches=1", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var grep FileGrepResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &grep); err != nil {
		t.Fatal(err)
	}
	if grep.ProjectRoot != root || grep.Pattern != "todo" || grep.PathGlob != "src/**" || len(grep.Matches) != 1 || grep.Matches[0].Path != "src/a.go" || grep.Matches[0].Line != 2 || grep.Limit != 1 || !grep.ResultTruncated {
		t.Fatalf("grep 결과가 이상해요: %+v", grep)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/files/grep?project_root="+url.QueryEscape(root)+"&pattern="+url.QueryEscape("TODO: (wire|second)")+"&regex=true&case_sensitive=true", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("regex grep status = %d body = %s", rec.Code, rec.Body.String())
	}
	grep = FileGrepResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &grep); err != nil {
		t.Fatal(err)
	}
	if !grep.Regex || len(grep.Matches) != 2 {
		t.Fatalf("regex/case_sensitive grep 결과가 이상해요: %+v", grep)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/files/grep?project_root="+url.QueryEscape(root), nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("pattern 없는 grep은 거부해야 해요: status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, query := range []string{"max_matches=-1", "max_matches=abc", "regex=maybe", "case_sensitive=maybe"} {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/files/grep?project_root="+url.QueryEscape(root)+"&pattern=todo&"+query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		want := strings.Split(query, "=")[0]
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("잘못된 grep query는 400이어야 해요: query=%s status=%d body=%s", query, rec.Code, rec.Body.String())
		}
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
	if glob.Offset != 0 || glob.NextOffset != 1 {
		t.Fatalf("glob paging metadata가 이상해요: %+v", glob)
	}
	firstPath := glob.Paths[0]
	req = httptest.NewRequest(http.MethodGet, "/api/v1/files/glob?project_root="+url.QueryEscape(root)+"&pattern=src/*&limit=1&offset=1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	glob = FileGlobResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &glob); err != nil {
		t.Fatal(err)
	}
	if len(glob.Paths) != 1 || glob.Paths[0] == firstPath || glob.TotalPaths != 2 || glob.Limit != 1 || glob.Offset != 1 || glob.NextOffset != 0 || glob.PathsTruncated {
		t.Fatalf("glob offset metadata가 이상해요: %+v", glob)
	}
	for i := 0; i < workspace.MaxGlobMatches+1; i++ {
		writeTestFile(t, filepath.Join(root, "bulk", fmt.Sprintf("entry-%05d.txt", i)), "x")
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/files/glob?project_root="+url.QueryEscape(root)+"&pattern=bulk/*.txt&limit="+strconv.Itoa(workspace.MaxGlobMatches), nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	glob = FileGlobResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &glob); err != nil {
		t.Fatal(err)
	}
	if len(glob.Paths) != workspace.MaxGlobMatches || glob.TotalPaths != workspace.MaxGlobMatches || glob.NextOffset != 0 || !glob.PathsTruncated {
		t.Fatalf("glob workspace envelope metadata가 이상해요: %+v", glob)
	}
	invalidParams := []struct {
		name  string
		query string
		want  string
	}{
		{name: "negative limit", query: "limit=-1", want: "limit"},
		{name: "bad limit", query: "limit=abc", want: "limit"},
		{name: "negative offset", query: "offset=-1", want: "offset"},
		{name: "bad offset", query: "offset=abc", want: "offset"},
	}
	for _, tc := range invalidParams {
		req = httptest.NewRequest(http.MethodGet, "/api/v1/files/glob?project_root="+url.QueryEscape(root)+"&pattern=src/*&"+tc.query, nil)
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s glob param은 400이어야 해요: status=%d body=%s", tc.name, rec.Code, rec.Body.String())
		}
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
	if !resp.Applied || resp.PatchBytes != len(patch) || resp.CheckpointID == "" || string(updated) != "one\npatched\nthree\n" {
		t.Fatalf("patch 적용이 이상해요: resp=%+v content=%q", resp, updated)
	}
	restoreBody, err := json.Marshal(FileRestoreRequest{ProjectRoot: root, CheckpointID: resp.CheckpointID})
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/files/restore", bytes.NewReader(restoreBody))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("restore status = %d body = %s", rec.Code, rec.Body.String())
	}
	updated, err = os.ReadFile(filepath.Join(root, "src", "a.txt"))
	if err != nil || string(updated) != "one\ntwo\nthree\n" {
		t.Fatalf("patch restore 결과가 이상해요: content=%q err=%v", updated, err)
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

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/transcript?max_markdown_bytes=180", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got TranscriptResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Session.ID != sess.ID || len(got.Turns) != 1 || !got.Redacted || !strings.Contains(got.Markdown, "[REDACTED]") || got.MarkdownBytes <= len(got.Markdown) || !got.MarkdownTruncated {
		t.Fatalf("transcript 응답이 이상해요: %+v", got)
	}
	if strings.Contains(got.Markdown, "abc1234567890secretvalue") || strings.Contains(got.Turns[0].Prompt, "abc1234567890secretvalue") {
		t.Fatalf("transcript는 기본적으로 secret을 가려야 해요: %+v", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/transcript?max_markdown_bytes=-1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_markdown_bytes") {
		t.Fatalf("음수 session transcript max_markdown_bytes는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
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

	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sess.ID+"/transcript?redact=maybe", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "redact") {
		t.Fatalf("잘못된 session transcript redact는 400이어야 해요: status=%d body=%s", rec.Code, rec.Body.String())
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
	if got.TotalTurns != 3 || got.CompactedTurns != 1 || got.PreservedTurns != 2 || got.PreserveFirstNTurns != 1 || got.PreserveLastNTurns != 1 || got.SummaryBytes != len(got.Summary) || !got.CheckpointCreated {
		t.Fatalf("compact metadata가 이상해요: %+v", got)
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

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "negative first preserve", body: `{"preserve_first_n_turns":-1}`, want: "preserve_first_n_turns"},
		{name: "negative last preserve", body: `{"preserve_last_n_turns":-1}`, want: "preserve_last_n_turns"},
	} {
		req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sess.ID+"/compact", bytes.NewBufferString(tc.body))
		req.Header.Set("Content-Type", "application/json")
		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s는 400이어야 해요: status=%d body=%s", tc.name, rec.Code, rec.Body.String())
		}
	}
}
