package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sleepysoong/kkode/session"
)

// RunStarter는 gateway가 실제 agent 실행을 시작할 때 주입하는 경계예요.
type RunStarter func(ctx context.Context, req RunStartRequest) (*RunDTO, error)

// Config는 gateway HTTP server 구성값이에요.
type Config struct {
	Store                session.Store
	Version              string
	Commit               string
	APIKey               string
	AllowLocalhostNoAuth bool
	Providers            []ProviderDTO
	Features             []FeatureDTO
	ResourceStore        session.ResourceStore
	RunStarter           RunStarter
	RunGetter            RunGetter
	RunLister            RunLister
	RunCanceler          RunCanceler
	Now                  func() time.Time
}

// Server는 kkode session/runtime을 HTTP API로 노출해요.
type Server struct {
	cfg     Config
	handler http.Handler
}

func New(cfg Config) (*Server, error) {
	if cfg.Store == nil {
		return nil, errors.New("session store가 필요해요")
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	srv := &Server{cfg: cfg}
	srv.handler = srv.buildHandler()
	return srv, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func (s *Server) Handler() http.Handler {
	return s.handler
}

func (s *Server) buildHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/readyz", s.handleReady)
	mux.Handle("/api/v1/", s.withAPIAuth(http.HandlerFunc(s.handleAPI)))
	return s.recoverMiddleware(mux)
}

func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				writeError(w, r, http.StatusInternalServerError, "panic", fmt.Sprint(recovered))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 method예요")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "time": s.cfg.Now()})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 method예요")
		return
	}
	writeJSON(w, map[string]any{"ready": true})
}

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	path = strings.Trim(path, "/")
	parts := splitPath(path)
	if len(parts) == 0 {
		writeError(w, r, http.StatusNotFound, "not_found", "API endpoint를 찾을 수 없어요")
		return
	}
	switch parts[0] {
	case "version":
		s.handleVersion(w, r, parts)
	case "providers":
		s.handleProviders(w, r, parts)
	case "capabilities":
		s.handleCapabilities(w, r, parts)
	case "sessions":
		s.handleSessions(w, r, parts)
	case "runs":
		s.handleRuns(w, r, parts)
	case "mcp":
		s.handleMCP(w, r, parts)
	case "skills":
		s.handleSkills(w, r, parts)
	case "subagents":
		s.handleSubagents(w, r, parts)
	default:
		writeError(w, r, http.StatusNotFound, "not_found", "API endpoint를 찾을 수 없어요")
	}
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) != 1 || r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 version 요청이에요")
		return
	}
	names := make([]string, 0, len(s.cfg.Providers))
	for _, p := range s.cfg.Providers {
		names = append(names, p.Name)
	}
	writeJSON(w, VersionResponse{Version: s.cfg.Version, Commit: s.cfg.Commit, Providers: names})
}

func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) != 1 || r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 providers 요청이에요")
		return
	}
	writeJSON(w, ProviderListResponse{Providers: s.cfg.Providers})
}

func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) != 1 || r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 capabilities 요청이에요")
		return
	}
	features := append([]FeatureDTO{}, s.cfg.Features...)
	if len(features) == 0 {
		features = DefaultFeatureCatalog()
	}
	writeJSON(w, CapabilityResponse{Version: s.cfg.Version, Commit: s.cfg.Commit, Features: features, Providers: s.cfg.Providers})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			s.listSessions(w, r)
		case http.MethodPost:
			s.createSession(w, r)
		default:
			writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 sessions method예요")
		}
		return
	}
	if len(parts) < 2 {
		writeError(w, r, http.StatusNotFound, "not_found", "session endpoint를 찾을 수 없어요")
		return
	}
	sessionID := parts[1]
	if len(parts) == 2 && r.Method == http.MethodGet {
		s.getSession(w, r, sessionID)
		return
	}
	if len(parts) == 3 && parts[2] == "events" && r.Method == http.MethodGet {
		s.getSessionEvents(w, r, sessionID)
		return
	}
	if len(parts) == 3 && parts[2] == "todos" && r.Method == http.MethodGet {
		s.getSessionTodos(w, r, sessionID)
		return
	}
	if len(parts) == 3 && parts[2] == "fork" && r.Method == http.MethodPost {
		s.forkSession(w, r, sessionID)
		return
	}
	writeError(w, r, http.StatusNotFound, "not_found", "session endpoint를 찾을 수 없어요")
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)
	if limit > 200 {
		limit = 200
	}
	sessions, err := s.cfg.Store.ListSessions(r.Context(), session.SessionQuery{ProjectRoot: r.URL.Query().Get("project_root"), Limit: limit})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "list_sessions_failed", err.Error())
		return
	}
	out := make([]SessionDTO, 0, len(sessions))
	for _, summary := range sessions {
		out = append(out, toSessionSummaryDTO(summary))
	}
	writeJSON(w, SessionListResponse{Sessions: out})
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var req SessionCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	req.ProjectRoot = strings.TrimSpace(req.ProjectRoot)
	req.Provider = strings.TrimSpace(req.Provider)
	req.Model = strings.TrimSpace(req.Model)
	if req.ProjectRoot == "" || req.Provider == "" || req.Model == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_session", "project_root, provider, model이 필요해요")
		return
	}
	mode := session.AgentMode(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = session.AgentModeBuild
	}
	sess := session.NewSession(req.ProjectRoot, req.Provider, req.Model, req.Agent, mode)
	if req.Metadata != nil {
		sess.Metadata = req.Metadata
	}
	if err := s.cfg.Store.CreateSession(r.Context(), sess); err != nil {
		writeError(w, r, http.StatusInternalServerError, "create_session_failed", err.Error())
		return
	}
	writeJSONStatus(w, http.StatusCreated, toSessionDTO(sess))
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	sess, err := s.cfg.Store.LoadSession(r.Context(), sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "session_not_found", err.Error())
		return
	}
	writeJSON(w, toSessionDTO(sess))
}

func (s *Server) forkSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	var req struct {
		AtTurnID string `json:"at_turn_id,omitempty"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
	}
	forked, err := session.Fork(r.Context(), s.cfg.Store, sessionID, req.AtTurnID)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "fork_session_failed", err.Error())
		return
	}
	writeJSONStatus(w, http.StatusCreated, toSessionDTO(forked))
}

func (s *Server) getSessionEvents(w http.ResponseWriter, r *http.Request, sessionID string) {
	sess, err := s.cfg.Store.LoadSession(r.Context(), sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "session_not_found", err.Error())
		return
	}
	afterSeq := queryInt(r, "after_seq", 0)
	events := make([]EventDTO, 0, len(sess.Events))
	for i, ev := range sess.Events {
		seq := i + 1
		if seq <= afterSeq {
			continue
		}
		events = append(events, toEventDTO(seq, ev))
	}
	if wantsSSE(r) {
		s.writeSSEEvents(w, r, events)
		return
	}
	writeJSON(w, EventListResponse{Events: events})
}

func (s *Server) writeSSEEvents(w http.ResponseWriter, r *http.Request, events []EventDTO) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	for _, ev := range events {
		data, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		fmt.Fprintf(w, "id: %d\n", ev.Seq)
		fmt.Fprintf(w, "event: %s\n", ev.Type)
		fmt.Fprintf(w, "data: %s\n\n", data)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func (s *Server) getSessionTodos(w http.ResponseWriter, r *http.Request, sessionID string) {
	sess, err := s.cfg.Store.LoadSession(r.Context(), sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "session_not_found", err.Error())
		return
	}
	out := make([]TodoDTO, 0, len(sess.Todos))
	for _, todo := range sess.Todos {
		out = append(out, toTodoDTO(todo))
	}
	writeJSON(w, TodoListResponse{Todos: out})
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			s.listRuns(w, r)
		case http.MethodPost:
			s.startRun(w, r)
		default:
			writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 runs method예요")
		}
		return
	}
	if len(parts) < 2 {
		writeError(w, r, http.StatusNotFound, "not_found", "run endpoint를 찾을 수 없어요")
		return
	}
	runID := parts[1]
	if len(parts) == 2 && r.Method == http.MethodGet {
		s.getRun(w, r, runID)
		return
	}
	if len(parts) == 3 && parts[2] == "cancel" && r.Method == http.MethodPost {
		s.cancelRun(w, r, runID)
		return
	}
	writeError(w, r, http.StatusNotFound, "not_found", "run endpoint를 찾을 수 없어요")
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	if s.cfg.RunLister == nil {
		writeError(w, r, http.StatusNotImplemented, "run_lister_missing", "이 gateway에는 RunLister가 연결되지 않았어요")
		return
	}
	limit := queryInt(r, "limit", 50)
	if limit > 200 {
		limit = 200
	}
	runs, err := s.cfg.RunLister(r.Context(), RunQuery{SessionID: r.URL.Query().Get("session_id"), Status: r.URL.Query().Get("status"), Limit: limit})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "list_runs_failed", err.Error())
		return
	}
	writeJSON(w, RunListResponse{Runs: runs})
}

func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	if s.cfg.RunStarter == nil {
		writeError(w, r, http.StatusNotImplemented, "run_starter_missing", "이 gateway에는 RunStarter가 연결되지 않았어요")
		return
	}
	var req RunStartRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.Prompt) == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_run", "session_id와 prompt가 필요해요")
		return
	}
	run, err := s.cfg.RunStarter(r.Context(), req)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "start_run_failed", err.Error())
		return
	}
	writeJSONStatus(w, http.StatusAccepted, run)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request, runID string) {
	if s.cfg.RunGetter == nil {
		writeError(w, r, http.StatusNotImplemented, "run_getter_missing", "이 gateway에는 RunGetter가 연결되지 않았어요")
		return
	}
	run, err := s.cfg.RunGetter(r.Context(), runID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "run_not_found", err.Error())
		return
	}
	writeJSON(w, run)
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request, runID string) {
	if s.cfg.RunCanceler == nil {
		writeError(w, r, http.StatusNotImplemented, "run_canceler_missing", "이 gateway에는 RunCanceler가 연결되지 않았어요")
		return
	}
	run, err := s.cfg.RunCanceler(r.Context(), runID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "run_not_found", err.Error())
		return
	}
	writeJSON(w, run)
}

func decodeJSON(r *http.Request, out any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}

func splitPath(path string) []string {
	if path == "" {
		return nil
	}
	parts := strings.Split(path, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func queryInt(r *http.Request, key string, fallback int) int {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func wantsSSE(r *http.Request) bool {
	if strings.EqualFold(r.URL.Query().Get("stream"), "true") || r.URL.Query().Get("stream") == "1" {
		return true
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream")
}
