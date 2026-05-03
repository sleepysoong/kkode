package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
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
	CORSOrigins          []string
	RequestIDGenerator   func() string
	AccessLogger         AccessLogger
	Providers            []ProviderDTO
	Features             []FeatureDTO
	ResourceStore        session.ResourceStore
	RunStarter           RunStarter
	RunGetter            RunGetter
	RunLister            RunLister
	RunCanceler          RunCanceler
	RunEventLister       RunEventLister
	RunSubscriber        RunEventSubscriber
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
	if cfg.RequestIDGenerator == nil {
		cfg.RequestIDGenerator = newRequestID
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
	apiHandler := s.withAPIAuth(http.HandlerFunc(s.handleAPI))
	mux.Handle("/api/v1", apiHandler)
	mux.Handle("/api/v1/", apiHandler)
	return s.requestIDMiddleware(s.accessLogMiddleware(s.recoverMiddleware(s.corsMiddleware(mux))))
}

func (s *Server) requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := requestIDFromRequest(r)
		if id == "" {
			id = s.cfg.RequestIDGenerator()
		}
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, withRequestID(r, id))
	})
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
		s.handleAPIIndex(w, r, parts)
		return
	}
	switch parts[0] {
	case "openapi.yaml":
		s.handleOpenAPI(w, r, parts)
	case "version":
		s.handleVersion(w, r, parts)
	case "providers":
		s.handleProviders(w, r, parts)
	case "models":
		s.handleModels(w, r, parts)
	case "prompts":
		s.handlePrompts(w, r, parts)
	case "capabilities":
		s.handleCapabilities(w, r, parts)
	case "stats":
		s.handleStats(w, r, parts)
	case "sessions":
		s.handleSessions(w, r, parts)
	case "runs":
		s.handleRuns(w, r, parts)
	case "requests":
		s.handleRequests(w, r, parts)
	case "mcp":
		s.handleMCP(w, r, parts)
	case "skills":
		s.handleSkills(w, r, parts)
	case "subagents":
		s.handleSubagents(w, r, parts)
	case "lsp":
		s.handleLSP(w, r, parts)
	case "tools":
		s.handleTools(w, r, parts)
	case "files":
		s.handleFiles(w, r, parts)
	case "git":
		s.handleGit(w, r, parts)
	default:
		writeError(w, r, http.StatusNotFound, "not_found", "API endpoint를 찾을 수 없어요")
	}
}

func (s *Server) handleRequests(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 3 && parts[2] == "runs" {
		s.listRunsByRequestID(w, r, parts[1])
		return
	}
	if len(parts) == 3 && parts[2] == "events" {
		s.listRunEventsByRequestID(w, r, parts[1])
		return
	}
	writeError(w, r, http.StatusNotFound, "not_found", "request correlation endpoint를 찾을 수 없어요")
}

func (s *Server) listRunsByRequestID(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 request correlation method예요")
		return
	}
	if s.cfg.RunLister == nil {
		writeError(w, r, http.StatusNotImplemented, "run_lister_missing", "이 gateway에는 RunLister가 연결되지 않았어요")
		return
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request_id", "request_id가 필요해요")
		return
	}
	limit := queryLimit(r, "limit", 50, 200)
	runs, err := s.cfg.RunLister(r.Context(), RunQuery{RequestID: requestID, Limit: limit})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "list_runs_failed", err.Error())
		return
	}
	writeJSON(w, RequestCorrelationResponse{RequestID: requestID, Runs: runs})
}

func (s *Server) listRunEventsByRequestID(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 request correlation method예요")
		return
	}
	if s.cfg.RunLister == nil {
		writeError(w, r, http.StatusNotImplemented, "run_lister_missing", "이 gateway에는 RunLister가 연결되지 않았어요")
		return
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request_id", "request_id가 필요해요")
		return
	}
	limit := queryLimit(r, "limit", 200, 1000)
	runs, err := s.cfg.RunLister(r.Context(), RunQuery{RequestID: requestID, Limit: 200})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "list_runs_failed", err.Error())
		return
	}
	if wantsSSE(r) {
		s.writeRequestEventsSSE(w, r, runs, limit)
		return
	}
	events, err := s.collectRequestRunEvents(r, runs, limit)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "list_run_events_failed", err.Error())
		return
	}
	writeJSON(w, RequestCorrelationEventsResponse{RequestID: requestID, Events: events})
}

func (s *Server) collectRequestRunEvents(r *http.Request, runs []RunDTO, limit int) ([]RunEventDTO, error) {
	events := make([]RunEventDTO, 0, len(runs))
	for _, run := range runs {
		if len(events) >= limit {
			break
		}
		remaining := limit - len(events)
		runEvents, err := s.eventsForCorrelationRun(r, run, remaining)
		if err != nil {
			return nil, err
		}
		events = append(events, runEvents...)
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].At.Equal(events[j].At) {
			if events[i].Run.ID == events[j].Run.ID {
				return events[i].Seq < events[j].Seq
			}
			return events[i].Run.ID < events[j].Run.ID
		}
		if events[i].At.IsZero() {
			return false
		}
		if events[j].At.IsZero() {
			return true
		}
		return events[i].At.Before(events[j].At)
	})
	if len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}

func (s *Server) eventsForCorrelationRun(r *http.Request, run RunDTO, limit int) ([]RunEventDTO, error) {
	if limit <= 0 {
		return []RunEventDTO{}, nil
	}
	if s.cfg.RunEventLister != nil {
		events, err := s.cfg.RunEventLister(r.Context(), run.ID, 0, limit)
		if err != nil {
			return nil, err
		}
		if len(events) > 0 {
			if len(events) > limit {
				return events[:limit], nil
			}
			return events, nil
		}
	}
	return []RunEventDTO{{Seq: 1, At: s.cfg.Now(), Type: runEventType(run.Status), Run: run}}, nil
}

func (s *Server) writeRequestEventsSSE(w http.ResponseWriter, r *http.Request, runs []RunDTO, limit int) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	events, err := s.collectRequestRunEvents(r, runs, limit)
	if err != nil {
		writeSSEFrame(w, flusher, 1, "error", map[string]string{"error": err.Error()})
		return
	}
	streamSeq := 0
	for _, event := range events {
		streamSeq++
		writeSSEFrame(w, flusher, streamSeq, event.Type, event)
	}
	if s.cfg.RunSubscriber == nil {
		return
	}
	updates := make(chan RunDTO, len(runs)*2)
	active := 0
	for _, run := range runs {
		if isTerminalRunStatus(run.Status) {
			continue
		}
		ch, unsubscribe := s.cfg.RunSubscriber(r.Context(), run.ID)
		defer unsubscribe()
		active++
		go func(ch <-chan RunDTO) {
			for {
				select {
				case <-r.Context().Done():
					return
				case run, ok := <-ch:
					if !ok {
						return
					}
					select {
					case <-r.Context().Done():
						return
					case updates <- run:
					}
				}
			}
		}(ch)
	}
	if active == 0 {
		return
	}
	terminal := map[string]bool{}
	for len(terminal) < active {
		select {
		case <-r.Context().Done():
			return
		case run := <-updates:
			streamSeq++
			event := RunEventDTO{Seq: streamSeq, At: s.cfg.Now(), Type: runEventType(run.Status), Run: run}
			writeSSEFrame(w, flusher, streamSeq, event.Type, event)
			if isTerminalRunStatus(run.Status) {
				terminal[run.ID] = true
			}
		}
	}
}

func (s *Server) handleAPIIndex(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) != 0 || r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 API index 요청이에요")
		return
	}
	writeJSON(w, APIIndexResponse{Version: s.cfg.Version, Commit: s.cfg.Commit, Links: APIIndexLinks()})
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

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) != 1 || r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 models 요청이에요")
		return
	}
	providerFilter := strings.TrimSpace(r.URL.Query().Get("provider"))
	out := []ModelDTO{}
	for _, provider := range s.cfg.Providers {
		if providerFilter != "" && !strings.EqualFold(provider.Name, providerFilter) {
			continue
		}
		models := provider.Models
		if len(models) == 0 && provider.DefaultModel != "" {
			models = []string{provider.DefaultModel}
		}
		for _, model := range models {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			out = append(out, ModelDTO{ID: model, Provider: provider.Name, DisplayName: model, Default: model == provider.DefaultModel, Capabilities: cloneAnyMap(provider.Capabilities), AuthStatus: provider.AuthStatus})
		}
	}
	writeJSON(w, ModelListResponse{Models: out})
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
	if len(parts) == 3 && parts[2] == "transcript" {
		s.getSessionTranscript(w, r, sessionID)
		return
	}
	if len(parts) >= 3 && parts[2] == "turns" {
		s.handleSessionTurns(w, r, sessionID, parts[3:])
		return
	}
	if len(parts) >= 3 && parts[2] == "todos" {
		s.handleSessionTodos(w, r, sessionID, parts[3:])
		return
	}
	if len(parts) >= 3 && parts[2] == "checkpoints" {
		s.handleSessionCheckpoints(w, r, sessionID, parts[3:])
		return
	}
	if len(parts) == 3 && parts[2] == "compact" {
		s.compactSession(w, r, sessionID)
		return
	}
	if len(parts) == 3 && parts[2] == "fork" && r.Method == http.MethodPost {
		s.forkSession(w, r, sessionID)
		return
	}
	writeError(w, r, http.StatusNotFound, "not_found", "session endpoint를 찾을 수 없어요")
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	limit := queryLimit(r, "limit", 50, 200)
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
	afterSeq := queryInt(r, "after_seq", 0)
	limit := queryLimit(r, "limit", 500, 5000)
	var events []EventDTO
	if timeline, ok := s.cfg.Store.(session.TimelineStore); ok {
		records, err := timeline.ListEvents(r.Context(), session.EventQuery{SessionID: sessionID, AfterSeq: afterSeq, Limit: limit})
		if err != nil {
			writeError(w, r, http.StatusNotFound, "session_not_found", err.Error())
			return
		}
		events = make([]EventDTO, 0, len(records))
		for _, record := range records {
			events = append(events, toEventDTO(record.Seq, record.Event))
		}
	} else {
		sess, err := s.cfg.Store.LoadSession(r.Context(), sessionID)
		if err != nil {
			writeError(w, r, http.StatusNotFound, "session_not_found", err.Error())
			return
		}
		events = make([]EventDTO, 0, len(sess.Events))
		for i, ev := range sess.Events {
			seq := i + 1
			if seq <= afterSeq {
				continue
			}
			events = append(events, toEventDTO(seq, ev))
			if len(events) >= limit {
				break
			}
		}
	}
	if wantsSSE(r) {
		s.writeSSEEvents(w, r, events)
		return
	}
	writeJSON(w, EventListResponse{Events: events})
}

func (s *Server) handleSessionTurns(w http.ResponseWriter, r *http.Request, sessionID string, rest []string) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 turns method예요")
		return
	}
	if timeline, ok := s.cfg.Store.(session.TimelineStore); ok {
		if len(rest) == 0 {
			s.listSessionTurnsFromTimeline(w, r, timeline, sessionID)
			return
		}
		if len(rest) == 1 {
			s.getSessionTurnFromTimeline(w, r, timeline, sessionID, rest[0])
			return
		}
		writeError(w, r, http.StatusNotFound, "not_found", "turn endpoint를 찾을 수 없어요")
		return
	}
	sess, err := s.cfg.Store.LoadSession(r.Context(), sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "session_not_found", err.Error())
		return
	}
	if len(rest) == 0 {
		s.listSessionTurns(w, r, sess)
		return
	}
	if len(rest) == 1 {
		s.getSessionTurn(w, r, sess, rest[0])
		return
	}
	writeError(w, r, http.StatusNotFound, "not_found", "turn endpoint를 찾을 수 없어요")
}

func (s *Server) listSessionTurnsFromTimeline(w http.ResponseWriter, r *http.Request, timeline session.TimelineStore, sessionID string) {
	afterSeq := queryInt(r, "after_seq", 0)
	limit := queryLimit(r, "limit", 100, 500)
	records, err := timeline.ListTurns(r.Context(), session.TurnQuery{SessionID: sessionID, AfterSeq: afterSeq, Limit: limit})
	if err != nil {
		writeError(w, r, http.StatusNotFound, "session_not_found", err.Error())
		return
	}
	out := make([]TurnDTO, 0, len(records))
	for _, record := range records {
		out = append(out, toTurnDTO(sessionID, record.Seq, record.Turn))
	}
	writeJSON(w, TurnListResponse{Turns: out})
}

func (s *Server) getSessionTurnFromTimeline(w http.ResponseWriter, r *http.Request, timeline session.TimelineStore, sessionID string, turnID string) {
	record, err := timeline.LoadTurn(r.Context(), sessionID, turnID)
	if err != nil {
		code := "session_not_found"
		if strings.Contains(err.Error(), "turn not found") {
			code = "turn_not_found"
		}
		writeError(w, r, http.StatusNotFound, code, err.Error())
		return
	}
	writeJSON(w, toTurnDTO(sessionID, record.Seq, record.Turn))
}

func (s *Server) listSessionTurns(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	afterSeq := queryInt(r, "after_seq", 0)
	limit := queryLimit(r, "limit", 100, 500)
	out := make([]TurnDTO, 0, min(len(sess.Turns), limit))
	for i, turn := range sess.Turns {
		seq := i + 1
		if seq <= afterSeq {
			continue
		}
		out = append(out, toTurnDTO(sess.ID, seq, turn))
		if len(out) >= limit {
			break
		}
	}
	writeJSON(w, TurnListResponse{Turns: out})
}

func (s *Server) getSessionTurn(w http.ResponseWriter, r *http.Request, sess *session.Session, turnID string) {
	for i, turn := range sess.Turns {
		if turn.ID == turnID {
			writeJSON(w, toTurnDTO(sess.ID, i+1, turn))
			return
		}
	}
	writeError(w, r, http.StatusNotFound, "turn_not_found", "turn을 찾을 수 없어요")
}

func (s *Server) writeSSEEvents(w http.ResponseWriter, r *http.Request, events []EventDTO) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	for _, ev := range events {
		writeSSEFrame(w, flusher, ev.Seq, ev.Type, ev)
	}
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
	if len(parts) == 3 && parts[2] == "events" && r.Method == http.MethodGet {
		s.getRunEvents(w, r, runID)
		return
	}
	if len(parts) == 3 && parts[2] == "retry" && r.Method == http.MethodPost {
		s.retryRun(w, r, runID)
		return
	}
	writeError(w, r, http.StatusNotFound, "not_found", "run endpoint를 찾을 수 없어요")
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	if s.cfg.RunLister == nil {
		writeError(w, r, http.StatusNotImplemented, "run_lister_missing", "이 gateway에는 RunLister가 연결되지 않았어요")
		return
	}
	limit := queryLimit(r, "limit", 50, 200)
	runs, err := s.cfg.RunLister(r.Context(), RunQuery{SessionID: r.URL.Query().Get("session_id"), Status: r.URL.Query().Get("status"), RequestID: r.URL.Query().Get(RequestIDMetadataKey), Limit: limit})
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
	req.Metadata = withRequestIDMetadata(req.Metadata, requestIDFromRequest(r))
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

func (s *Server) retryRun(w http.ResponseWriter, r *http.Request, runID string) {
	if s.cfg.RunGetter == nil || s.cfg.RunStarter == nil {
		writeError(w, r, http.StatusNotImplemented, "run_retry_missing", "이 gateway에는 run retry 경계가 연결되지 않았어요")
		return
	}
	original, err := s.cfg.RunGetter(r.Context(), runID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "run_not_found", err.Error())
		return
	}
	metadata := cloneMap(original.Metadata)
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadata["retried_from"] = original.ID
	metadata = withRequestIDMetadata(metadata, requestIDFromRequest(r))
	retry, err := s.cfg.RunStarter(r.Context(), RunStartRequest{SessionID: original.SessionID, Prompt: original.Prompt, Provider: original.Provider, Model: original.Model, Metadata: metadata, MCPServers: cloneStringSlice(original.MCPServers), Skills: cloneStringSlice(original.Skills), Subagents: cloneStringSlice(original.Subagents)})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "retry_run_failed", err.Error())
		return
	}
	writeJSONStatus(w, http.StatusAccepted, retry)
}

func (s *Server) getRunEvents(w http.ResponseWriter, r *http.Request, runID string) {
	if s.cfg.RunGetter == nil {
		writeError(w, r, http.StatusNotImplemented, "run_getter_missing", "이 gateway에는 RunGetter가 연결되지 않았어요")
		return
	}
	run, err := s.cfg.RunGetter(r.Context(), runID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "run_not_found", err.Error())
		return
	}
	if !wantsSSE(r) {
		writeJSON(w, RunEventListResponse{Events: s.runEventSnapshot(r, runID, *run)})
		return
	}
	s.writeRunSSE(w, r, runID, *run)
}

func (s *Server) runEventSnapshot(r *http.Request, runID string, fallback RunDTO) []RunEventDTO {
	afterSeq := queryInt(r, "after_seq", 0)
	limit := queryLimit(r, "limit", 200, 1000)
	if s.cfg.RunEventLister != nil {
		events, err := s.cfg.RunEventLister(r.Context(), runID, afterSeq, limit)
		if err == nil && len(events) > 0 {
			return events
		}
		if err == nil && afterSeq > 0 {
			return []RunEventDTO{}
		}
	}
	if afterSeq >= 1 {
		return []RunEventDTO{}
	}
	return []RunEventDTO{{Seq: 1, At: s.cfg.Now(), Type: runEventType(fallback.Status), Run: fallback}}
}

func (s *Server) writeRunSSE(w http.ResponseWriter, r *http.Request, runID string, initial RunDTO) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	events := s.runEventSnapshot(r, runID, initial)
	seq := 0
	for _, event := range events {
		seq = event.Seq
		writeRunSSEEvent(w, flusher, event)
	}
	if seq == 0 {
		seq = 1
		writeRunSSEEvent(w, flusher, RunEventDTO{Seq: seq, At: s.cfg.Now(), Type: runEventType(initial.Status), Run: initial})
	}
	if isTerminalRunStatus(initial.Status) || s.cfg.RunSubscriber == nil {
		return
	}
	ch, unsubscribe := s.cfg.RunSubscriber(r.Context(), initial.ID)
	defer unsubscribe()
	for {
		select {
		case <-r.Context().Done():
			return
		case run, ok := <-ch:
			if !ok {
				return
			}
			seq++
			writeRunSSEEvent(w, flusher, RunEventDTO{Seq: seq, At: s.cfg.Now(), Type: runEventType(run.Status), Run: run})
			if isTerminalRunStatus(run.Status) {
				return
			}
		}
	}
}

func writeRunSSEEvent(w http.ResponseWriter, flusher http.Flusher, event RunEventDTO) {
	writeSSEFrame(w, flusher, event.Seq, event.Type, event)
}

func writeSSEFrame(w http.ResponseWriter, flusher http.Flusher, seq int, eventType string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "id: %d\n", seq)
	fmt.Fprintf(w, "event: %s\n", eventType)
	fmt.Fprintf(w, "data: %s\n\n", data)
	if flusher != nil {
		flusher.Flush()
	}
}

func runEventType(status string) string {
	if status == "" {
		return "run.updated"
	}
	return "run." + status
}

func isTerminalRunStatus(status string) bool {
	switch status {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
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

func queryLimit(r *http.Request, key string, fallback int, maxValue int) int {
	limit := queryInt(r, key, fallback)
	if maxValue > 0 && limit > maxValue {
		return maxValue
	}
	return limit
}

func wantsSSE(r *http.Request) bool {
	if strings.EqualFold(r.URL.Query().Get("stream"), "true") || r.URL.Query().Get("stream") == "1" {
		return true
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream")
}
