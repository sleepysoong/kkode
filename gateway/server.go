package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sleepysoong/kkode/session"
	"github.com/sleepysoong/kkode/workspace"
)

// RunStarter는 gateway가 실제 agent 실행을 시작할 때 주입하는 경계예요.
type RunStarter func(ctx context.Context, req RunStartRequest) (*RunDTO, error)

// RunPreviewer는 실제 실행 전에 provider/model/resource 조립 결과를 계산해요.
type RunPreviewer func(ctx context.Context, req RunStartRequest) (*RunPreviewResponse, error)

// RunValidator는 background queue에 넣기 전에 빠른 실행 전 검증을 수행해요.
type RunValidator func(ctx context.Context, req RunStartRequest) error

// ProviderTester는 provider 단독 preflight/preview/선택적 live smoke를 실행해요.
type ProviderTester func(ctx context.Context, provider string, req ProviderTestRequest) (*ProviderTestResponse, error)

// RunRuntimeStatsGetter는 diagnostics가 process-local run queue 상태를 읽는 경계예요.
type RunRuntimeStatsGetter func() RunRuntimeStats

// Config는 gateway HTTP server 구성값이에요.
type Config struct {
	Store                session.Store
	Version              string
	Commit               string
	APIKey               string
	AllowLocalhostNoAuth bool
	CORSOrigins          []string
	RequestIDGenerator   func() string
	MaxRequestBytes      int64
	MaxConcurrentRuns    int
	RunTimeout           time.Duration
	RunMaxIterations     int
	RunWebMaxBytes       int64
	AccessLogger         AccessLogger
	Providers            []ProviderDTO
	DefaultMCPServers    []ResourceDTO
	DiagnosticChecks     []DiagnosticCheckDTO
	Features             []FeatureDTO
	ResourceStore        session.ResourceStore
	RunStarter           RunStarter
	RunPreviewer         RunPreviewer
	RunValidator         RunValidator
	ProviderTester       ProviderTester
	RunRuntimeStats      RunRuntimeStatsGetter
	RunGetter            RunGetter
	RunLister            RunLister
	RunCanceler          RunCanceler
	RunEventLister       RunEventLister
	RunSubscriber        RunEventSubscriber
	RunEventSubscriber   RunEventStreamSubscriber
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
	if cfg.MaxRequestBytes == 0 {
		cfg.MaxRequestBytes = 32 << 20
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
	apiHandler := s.bodyLimitMiddleware(s.withAPIAuth(http.HandlerFunc(s.handleAPI)))
	mux.Handle("/api/v1", apiHandler)
	mux.Handle("/api/v1/", apiHandler)
	return s.requestIDMiddleware(s.accessLogMiddleware(s.recoverMiddleware(s.securityHeadersMiddleware(s.corsMiddleware(mux)))))
}

func (s *Server) bodyLimitMiddleware(next http.Handler) http.Handler {
	limit := s.cfg.MaxRequestBytes
	if limit < 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			if r.ContentLength > limit {
				writeError(w, r, http.StatusRequestEntityTooLarge, "request_too_large", "요청 body가 gateway 제한보다 커요")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := requestIDFromRequest(r)
		if err := validateRequestIDValue(id); err != nil {
			id = s.cfg.RequestIDGenerator()
			w.Header().Set(RequestIDHeader, id)
			writeError(w, withRequestID(r, id), http.StatusBadRequest, "invalid_request_id", err.Error())
			return
		}
		if id == "" {
			id = s.cfg.RequestIDGenerator()
		}
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, withRequestID(r, id))
	})
}

func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stats := &responseStatsWriter{ResponseWriter: w}
		defer func() {
			if recovered := recover(); recovered != nil {
				if stats.status != 0 {
					return
				}
				writeError(stats, r, http.StatusInternalServerError, "panic", fmt.Sprint(recovered))
			}
		}()
		next.ServeHTTP(stats, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 method예요")
		return
	}
	writeJSON(w, HealthResponse{OK: true, Time: s.cfg.Now()})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 method예요")
		return
	}
	if checker, ok := s.cfg.Store.(session.HealthChecker); ok {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := checker.Ping(ctx); err != nil {
			writeError(w, r, http.StatusServiceUnavailable, "not_ready", err.Error())
			return
		}
	}
	if missing := s.missingRuntimeWiring(); len(missing) > 0 {
		writeErrorDetails(w, r, http.StatusServiceUnavailable, "not_ready", "runtime wiring이 준비되지 않았어요", map[string]any{"missing_runtime_wiring": missing})
		return
	}
	writeJSON(w, ReadyResponse{Ready: true, Time: s.cfg.Now()})
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
	case "diagnostics":
		s.handleDiagnostics(w, r, parts)
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
	if len(parts) == 3 && parts[2] == "transcript" {
		s.getRequestTranscript(w, r, parts[1])
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
	if err := validateRequestIDValue(requestID); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request_id", err.Error())
		return
	}
	limit, ok := queryLimitParam(w, r, "limit", 50, 200, "invalid_request_runs")
	if !ok {
		return
	}
	offset, ok := queryOffsetParam(w, r, "offset", "invalid_request_runs")
	if !ok {
		return
	}
	runs, err := s.cfg.RunLister(r.Context(), RunQuery{RequestID: requestID, Limit: limit + 1, Offset: offset})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "list_runs_failed", err.Error())
		return
	}
	runs, returned, truncated := trimRuns(runs, limit)
	writeJSON(w, RequestCorrelationResponse{RequestID: requestID, Runs: runs, Limit: limit, Offset: offset, NextOffset: nextOffset(offset, returned, truncated), ResultTruncated: truncated})
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
	if err := validateRequestIDValue(requestID); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request_id", err.Error())
		return
	}
	afterSeq, ok := queryAfterSeq(w, r)
	if !ok {
		return
	}
	limit, ok := queryLimitParam(w, r, "limit", 200, 1000, "invalid_request_events")
	if !ok {
		return
	}
	stream, ok := queryWantsSSE(w, r, "invalid_request_events")
	if !ok {
		return
	}
	heartbeatInterval := 15 * time.Second
	if stream {
		var ok bool
		heartbeatInterval, ok = querySSEHeartbeatInterval(w, r, "invalid_request_events")
		if !ok {
			return
		}
	}
	runs, err := s.cfg.RunLister(r.Context(), RunQuery{RequestID: requestID, Limit: 200})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "list_runs_failed", err.Error())
		return
	}
	if stream {
		s.writeRequestEventsSSE(w, r, runs, limit, afterSeq, heartbeatInterval)
		return
	}
	events, err := s.collectRequestRunEvents(r, runs, limit+1, afterSeq)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "list_run_events_failed", err.Error())
		return
	}
	events, truncated, nextAfterSeq := trimRunEvents(events, limit)
	writeJSON(w, RequestCorrelationEventsResponse{RequestID: requestID, Events: events, AfterSeq: afterSeq, Limit: limit, ResultTruncated: truncated, NextAfterSeq: nextAfterSeq})
}

func (s *Server) collectRequestRunEvents(r *http.Request, runs []RunDTO, limit int, afterSeq int) ([]RunEventDTO, error) {
	events := make([]RunEventDTO, 0, len(runs))
	for _, run := range runs {
		if len(events) >= limit {
			break
		}
		remaining := limit - len(events)
		runEvents, err := s.eventsForCorrelationRun(r, run, remaining, afterSeq)
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

func (s *Server) eventsForCorrelationRun(r *http.Request, run RunDTO, limit int, afterSeq int) ([]RunEventDTO, error) {
	if limit <= 0 {
		return []RunEventDTO{}, nil
	}
	if s.cfg.RunEventLister != nil {
		events, err := s.cfg.RunEventLister(r.Context(), run.ID, afterSeq, limit)
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
	if afterSeq >= 1 {
		return []RunEventDTO{}, nil
	}
	return []RunEventDTO{{Seq: 1, At: s.cfg.Now(), Type: runEventType(run.Status), Run: run}}, nil
}

func (s *Server) writeRequestEventsSSE(w http.ResponseWriter, r *http.Request, runs []RunDTO, limit int, afterSeq int, heartbeatInterval time.Duration) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	updates := make(chan RunEventDTO, len(runs)*2)
	active := 0
	activeIDs := map[string]bool{}
	if s.cfg.RunEventSubscriber != nil {
		for _, run := range runs {
			if isTerminalRunStatus(run.Status) {
				continue
			}
			ch, unsubscribe := s.cfg.RunEventSubscriber(r.Context(), run.ID)
			defer unsubscribe()
			active++
			activeIDs[run.ID] = true
			go func(ch <-chan RunEventDTO) {
				for {
					select {
					case <-r.Context().Done():
						return
					case event, ok := <-ch:
						if !ok {
							return
						}
						select {
						case <-r.Context().Done():
							return
						case updates <- event:
						}
					}
				}
			}(ch)
		}
	} else if s.cfg.RunSubscriber != nil {
		for _, run := range runs {
			if isTerminalRunStatus(run.Status) {
				continue
			}
			ch, unsubscribe := s.cfg.RunSubscriber(r.Context(), run.ID)
			defer unsubscribe()
			active++
			activeIDs[run.ID] = true
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
						case updates <- RunEventDTO{Type: runEventType(run.Status), Run: run}:
						}
					}
				}
			}(ch)
		}
	}
	events, err := s.collectRequestRunEvents(r, runs, limit, afterSeq)
	if err != nil {
		writeSSEFrame(w, flusher, 1, "error", map[string]string{"error": err.Error()})
		return
	}
	streamSeq := 0
	terminal := map[string]bool{}
	for _, event := range events {
		streamSeq++
		writeSSEFrame(w, flusher, streamSeq, event.Type, event)
		if activeIDs[event.Run.ID] && isTerminalRunStatus(event.Run.Status) {
			terminal[event.Run.ID] = true
		}
	}
	if active == 0 {
		return
	}
	for len(terminal) < active {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			writeSSEHeartbeat(w, flusher)
		case event := <-updates:
			streamSeq++
			if event.Seq <= 0 {
				event.Seq = streamSeq
			}
			if event.At.IsZero() {
				event.At = s.cfg.Now()
			}
			writeSSEFrame(w, flusher, streamSeq, event.Type, event)
			if isTerminalRunStatus(event.Run.Status) {
				terminal[event.Run.ID] = true
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
	sort.Strings(names)
	writeJSON(w, VersionResponse{Version: s.cfg.Version, Commit: s.cfg.Commit, Providers: names})
}

func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 3 && parts[2] == "test" {
		if r.Method != http.MethodPost {
			writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "provider test는 POST만 지원해요")
			return
		}
		s.testProvider(w, r, parts[1])
		return
	}
	if len(parts) == 2 {
		if r.Method != http.MethodGet {
			writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "provider 상세는 GET만 지원해요")
			return
		}
		provider, ok := findProvider(s.cfg.Providers, parts[1])
		if !ok {
			writeError(w, r, http.StatusNotFound, "provider_not_found", "provider를 찾을 수 없어요")
			return
		}
		writeJSON(w, provider)
		return
	}
	if len(parts) != 1 || r.Method != http.MethodGet {
		if len(parts) == 1 {
			writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "provider 목록은 GET만 지원해요")
			return
		}
		writeError(w, r, http.StatusNotFound, "not_found", "provider endpoint를 찾을 수 없어요")
		return
	}
	providers := providersForDiscovery(s.cfg.Providers)
	limit, ok := queryLimitParam(w, r, "limit", len(providers), 5000, "invalid_provider_list")
	if !ok {
		return
	}
	offset, ok := queryOffsetParam(w, r, "offset", "invalid_provider_list")
	if !ok {
		return
	}
	page, returned, truncated := pageSlice(providers, limit, offset)
	writeJSON(w, ProviderListResponse{Providers: page, TotalProviders: len(providers), Limit: limit, Offset: offset, NextOffset: nextOffset(offset, returned, truncated), ResultTruncated: truncated})
}

func (s *Server) testProvider(w http.ResponseWriter, r *http.Request, providerName string) {
	if _, ok := findProvider(s.cfg.Providers, providerName); !ok {
		writeError(w, r, http.StatusNotFound, "provider_not_found", "provider를 찾을 수 없어요")
		return
	}
	if s.cfg.ProviderTester == nil {
		writeError(w, r, http.StatusNotImplemented, "provider_tester_missing", "이 gateway에는 ProviderTester가 연결되지 않았어요")
		return
	}
	var req ProviderTestRequest
	if _, err := decodeOptionalJSON(r, &req); err != nil {
		writeJSONDecodeError(w, r, err)
		return
	}
	req.Model = strings.TrimSpace(req.Model)
	req.Metadata = sanitizeRunMetadata(req.Metadata)
	if err := validateProviderTestRequest(req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_provider_test", err.Error())
		return
	}
	resp, err := s.cfg.ProviderTester(r.Context(), providerName, req)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "provider_test_failed", err.Error())
		return
	}
	writeJSON(w, resp)
}

func validateProviderTestRequest(req ProviderTestRequest) error {
	switch {
	case req.MaxPreviewBytes < 0:
		return errors.New("max_preview_bytes는 0 이상이어야 해요")
	case req.MaxPreviewBytes > MaxProviderTestPreviewBytes:
		return fmt.Errorf("max_preview_bytes는 %d 이하여야 해요", MaxProviderTestPreviewBytes)
	case req.MaxOutputTokens < 0:
		return errors.New("max_output_tokens는 0 이상이어야 해요")
	case req.MaxOutputTokens > MaxProviderTestOutputTokens:
		return fmt.Errorf("max_output_tokens는 %d 이하여야 해요", MaxProviderTestOutputTokens)
	case req.MaxResultBytes < 0:
		return errors.New("max_result_bytes는 0 이상이어야 해요")
	case req.MaxResultBytes > MaxProviderTestResultBytes:
		return fmt.Errorf("max_result_bytes는 %d 이하여야 해요", MaxProviderTestResultBytes)
	case req.TimeoutMS < 0:
		return errors.New("timeout_ms는 0 이상이어야 해요")
	case req.TimeoutMS > MaxProviderTestTimeoutMS:
		return fmt.Errorf("timeout_ms는 %d 이하여야 해요", MaxProviderTestTimeoutMS)
	default:
		return validateRunMetadata(req.Metadata)
	}
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) != 1 || r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 models 요청이에요")
		return
	}
	providerFilter := strings.TrimSpace(r.URL.Query().Get("provider"))
	out := []ModelDTO{}
	for _, provider := range providersForDiscovery(s.cfg.Providers) {
		if providerFilter != "" && !providerMatches(provider, providerFilter) {
			continue
		}
		for _, model := range modelNamesForDiscovery(provider) {
			out = append(out, ModelDTO{ID: model, Provider: provider.Name, DisplayName: model, Default: model == provider.DefaultModel, Capabilities: cloneAnyMap(provider.Capabilities), AuthStatus: provider.AuthStatus})
		}
	}
	limit, ok := queryLimitParam(w, r, "limit", len(out), 5000, "invalid_model_list")
	if !ok {
		return
	}
	offset, ok := queryOffsetParam(w, r, "offset", "invalid_model_list")
	if !ok {
		return
	}
	page, returned, truncated := pageSlice(out, limit, offset)
	writeJSON(w, ModelListResponse{Models: page, TotalModels: len(out), Limit: limit, Offset: offset, NextOffset: nextOffset(offset, returned, truncated), ResultTruncated: truncated})
}

func providersForDiscovery(providers []ProviderDTO) []ProviderDTO {
	out := append([]ProviderDTO(nil), providers...)
	sort.SliceStable(out, func(i, j int) bool {
		left := strings.ToLower(strings.TrimSpace(out[i].Name))
		right := strings.ToLower(strings.TrimSpace(out[j].Name))
		if left == right {
			return out[i].Name < out[j].Name
		}
		return left < right
	})
	return out
}

func modelNamesForDiscovery(provider ProviderDTO) []string {
	models := append([]string(nil), provider.Models...)
	if len(models) == 0 && provider.DefaultModel != "" {
		models = []string{provider.DefaultModel}
	}
	seen := map[string]bool{}
	rest := []string{}
	defaultModel := strings.TrimSpace(provider.DefaultModel)
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] {
			continue
		}
		seen[model] = true
		if defaultModel != "" && model == defaultModel {
			continue
		}
		rest = append(rest, model)
	}
	sort.Strings(rest)
	if defaultModel != "" && seen[defaultModel] {
		return append([]string{defaultModel}, rest...)
	}
	return rest
}

func findProvider(providers []ProviderDTO, name string) (ProviderDTO, bool) {
	for _, provider := range providers {
		if providerMatches(provider, name) {
			return provider, true
		}
	}
	return ProviderDTO{}, false
}

func providerMatches(provider ProviderDTO, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if strings.EqualFold(provider.Name, name) {
		return true
	}
	for _, alias := range provider.Aliases {
		if strings.EqualFold(alias, name) {
			return true
		}
	}
	return false
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
	writeJSON(w, CapabilityResponse{
		Version:              s.cfg.Version,
		Commit:               s.cfg.Commit,
		Features:             features,
		Providers:            providersForDiscovery(s.cfg.Providers),
		ProviderCapabilities: DefaultProviderCapabilityCatalog(),
		ProviderPipeline:     DefaultProviderPipelineCatalog(),
		DefaultMCPServers:    RedactResourceDTOs(s.cfg.DefaultMCPServers),
		Limits:               gatewayLimits(s.cfg),
	})
}

func gatewayLimits(cfg Config) LimitDTO {
	return LimitDTO{
		MaxRequestBytes:             cfg.MaxRequestBytes,
		MaxConcurrentRuns:           cfg.MaxConcurrentRuns,
		RunTimeoutSeconds:           durationSeconds(cfg.RunTimeout),
		RunMaxIterations:            cfg.RunMaxIterations,
		RunWebMaxBytes:              cfg.RunWebMaxBytes,
		MaxMCPHTTPResponseBytes:     maxMCPHTTPResponseBytes,
		MaxMCPProbeNameBytes:        maxMCPProbeNameBytes,
		MaxMCPProbeURIBytes:         maxMCPProbeURIBytes,
		MaxMCPProbeArgumentBytes:    maxMCPProbeArgumentsBytes,
		MaxMCPProbeOutputBytes:      maxMCPProbeOutputBytes,
		MaxFileContentBytes:         maxFileContentBytes,
		MaxSkillPreviewBytes:        maxSkillPreviewBytes,
		MaxSubagentPreviewBytes:     maxSubagentPreviewPromptBytes,
		MaxPromptTextBytes:          maxPromptTextBytes,
		MaxTranscriptMarkdownBytes:  maxTranscriptMarkdownBytes,
		MaxGitDiffBytes:             maxGitDiffBytes,
		MaxRunPreviewBytes:          MaxRunPreviewBytes,
		MaxProviderTestPreviewBytes: MaxProviderTestPreviewBytes,
		MaxProviderTestResultBytes:  MaxProviderTestResultBytes,
		MaxProviderTestOutputTokens: MaxProviderTestOutputTokens,
		MaxProviderTestTimeoutMS:    MaxProviderTestTimeoutMS,
		MaxLSPFormatInputBytes:      maxLSPFormatInputBytes,
		MaxLSPFormatPreviewBytes:    maxLSPFormatPreviewBytes,
		MaxRunPromptBytes:           maxRunPromptBytes,
		MaxRunSelectorItems:         maxRunSelectorItems,
		MaxRunSelectorItemBytes:     maxRunSelectorItemBytes,
		MaxRunContextBlocks:         maxRunContextBlocks,
		MaxRunContextBlockBytes:     maxRunContextBlockBytes,
		MaxRunMetadataEntries:       maxRunMetadataEntries,
		MaxRunMetadataKeyBytes:      maxRunMetadataKeyBytes,
		MaxRunMetadataValueBytes:    maxRunMetadataValueBytes,
		MaxRequestIDBytes:           maxRequestIDBytes,
		MaxIdempotencyKeyBytes:      maxIdempotencyKeyBytes,
		MaxToolCallNameBytes:        maxToolCallNameBytes,
		MaxToolCallIDBytes:          maxToolCallIDBytes,
		MaxToolCallArgumentBytes:    maxToolCallArgumentsBytes,
		MaxToolCallOutputBytes:      maxToolCallOutputBytes,
		MaxToolCallWebBytes:         maxToolCallWebBytes,
		MaxShellTimeoutMS:           workspace.MaxCommandTimeout.Milliseconds(),
		MaxRunProviderModelBytes:    maxRunProviderModelBytes,
	}
}

func durationSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int((d + time.Second - 1) / time.Second)
}

func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) != 1 || r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "지원하지 않는 diagnostics 요청이에요")
		return
	}
	features := s.cfg.Features
	if len(features) == 0 {
		features = DefaultFeatureCatalog()
	}
	checks := []DiagnosticCheckDTO{{Name: "http", Status: "ok", Message: "gateway handler가 응답하고 있어요"}}
	ok := true
	if checker, supportsPing := s.cfg.Store.(session.HealthChecker); supportsPing {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		err := checker.Ping(ctx)
		cancel()
		if err != nil {
			ok = false
			checks = append(checks, DiagnosticCheckDTO{Name: "store", Status: "error", Message: err.Error()})
		} else {
			checks = append(checks, DiagnosticCheckDTO{Name: "store", Status: "ok"})
		}
	} else {
		checks = append(checks, DiagnosticCheckDTO{Name: "store", Status: "unknown", Message: "store ping을 지원하지 않아요"})
	}
	missingRuntimeWiring := s.missingRuntimeWiring()
	wiringChecks, wiringOK := runtimeWiringChecks(missingRuntimeWiring)
	if !wiringOK {
		ok = false
	}
	checks = append(checks, wiringChecks...)
	providerAuthChecks, providerAuthOK := providerAuthDiagnosticChecks(s.cfg.Providers)
	if !providerAuthOK {
		ok = false
	}
	checks = append(checks, providerAuthChecks...)
	checks = append(checks, s.cfg.DiagnosticChecks...)
	failingChecks := failingDiagnosticCheckNames(checks)
	if len(failingChecks) > 0 {
		ok = false
	}
	resp := DiagnosticsResponse{OK: ok, Version: s.cfg.Version, Commit: s.cfg.Commit, Time: s.cfg.Now(), Checks: checks, Providers: len(s.cfg.Providers), Features: len(features), DefaultMCPServers: len(s.cfg.DefaultMCPServers), MaxRequestBytes: s.cfg.MaxRequestBytes, MaxConcurrentRuns: s.cfg.MaxConcurrentRuns, RunTimeoutSeconds: durationSeconds(s.cfg.RunTimeout), MissingRuntimeWiring: missingRuntimeWiring, FailingChecks: failingChecks}
	if s.cfg.RunRuntimeStats != nil {
		stats := runRuntimeStatsDTO(s.cfg.RunRuntimeStats())
		resp.RunRuntime = &stats
	}
	writeJSON(w, resp)
}

func (s *Server) missingRuntimeWiring() []string {
	var missing []string
	if s.cfg.RunStarter == nil {
		missing = append(missing, "run_starter")
	}
	if s.cfg.RunPreviewer == nil {
		missing = append(missing, "run_previewer")
	}
	if s.cfg.RunValidator == nil {
		missing = append(missing, "run_validator")
	}
	if s.cfg.ProviderTester == nil {
		missing = append(missing, "provider_tester")
	}
	if s.cfg.RunGetter == nil {
		missing = append(missing, "run_getter")
	}
	if s.cfg.RunLister == nil {
		missing = append(missing, "run_lister")
	}
	if s.cfg.RunCanceler == nil {
		missing = append(missing, "run_canceler")
	}
	if s.cfg.RunEventLister == nil {
		missing = append(missing, "run_event_lister")
	}
	if s.cfg.RunSubscriber == nil {
		missing = append(missing, "run_subscriber")
	}
	if s.cfg.RunEventSubscriber == nil {
		missing = append(missing, "run_event_subscriber")
	}
	return missing
}

func providerAuthDiagnosticChecks(providers []ProviderDTO) ([]DiagnosticCheckDTO, bool) {
	checks := make([]DiagnosticCheckDTO, 0, len(providers))
	ok := true
	for _, provider := range providers {
		if provider.Name == "" || provider.AuthStatus == "" {
			continue
		}
		check := DiagnosticCheckDTO{Name: "provider_auth." + provider.Name, Status: provider.AuthStatus}
		switch provider.AuthStatus {
		case "local":
			check.Message = "local provider라서 auth env가 필요하지 않아요"
		case "configured":
			check.Message = "provider auth env가 설정되어 있어요"
		case "missing":
			ok = false
			if len(provider.AuthEnv) > 0 {
				check.Message = "필요한 auth env가 비어 있어요: " + strings.Join(provider.AuthEnv, ", ")
			} else {
				check.Message = "provider auth env가 비어 있어요"
			}
		default:
			check.Message = "provider auth status=" + provider.AuthStatus
		}
		checks = append(checks, check)
	}
	return checks, ok
}

func failingDiagnosticCheckNames(checks []DiagnosticCheckDTO) []string {
	var failing []string
	for _, check := range checks {
		switch strings.ToLower(strings.TrimSpace(check.Status)) {
		case "missing", "error", "failed", "unhealthy":
			failing = append(failing, check.Name)
		}
	}
	return failing
}

func runtimeWiringChecks(missingRuntimeWiring []string) ([]DiagnosticCheckDTO, bool) {
	missing := map[string]struct{}{}
	for _, name := range missingRuntimeWiring {
		missing[name] = struct{}{}
	}
	names := []string{"run_starter", "run_previewer", "run_validator", "provider_tester", "run_getter", "run_lister", "run_canceler", "run_event_lister", "run_subscriber", "run_event_subscriber"}
	checks := make([]DiagnosticCheckDTO, 0, len(names))
	ok := true
	for _, name := range names {
		if _, isMissing := missing[name]; isMissing {
			ok = false
			checks = append(checks, DiagnosticCheckDTO{Name: name, Status: "missing"})
			continue
		}
		checks = append(checks, DiagnosticCheckDTO{Name: name, Status: "ok"})
	}
	return checks, ok
}

func runRuntimeStatsDTO(stats RunRuntimeStats) RunRuntimeStatsDTO {
	return RunRuntimeStatsDTO{
		TrackedRuns:       stats.TrackedRuns,
		ActiveRuns:        stats.ActiveRuns,
		QueuedRuns:        stats.QueuedRuns,
		RunningRuns:       stats.RunningRuns,
		CancellingRuns:    stats.CancellingRuns,
		TerminalRuns:      stats.TerminalRuns,
		MaxConcurrentRuns: stats.MaxConcurrentRuns,
		OccupiedRunSlots:  stats.OccupiedRunSlots,
		AvailableRunSlots: stats.AvailableRunSlots,
		RunTimeoutSeconds: durationSeconds(stats.RunTimeout),
	}
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
	if len(parts) == 2 && parts[1] == "import" {
		s.importSession(w, r)
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
	if len(parts) == 3 && parts[2] == "export" {
		s.exportSession(w, r, sessionID)
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
	limit, ok := queryLimitParam(w, r, "limit", 50, 200, "invalid_session_list")
	if !ok {
		return
	}
	offset, ok := queryOffsetParam(w, r, "offset", "invalid_session_list")
	if !ok {
		return
	}
	projectRoot := strings.TrimSpace(r.URL.Query().Get("project_root"))
	sessions, err := s.cfg.Store.ListSessions(r.Context(), session.SessionQuery{ProjectRoot: projectRoot, Limit: limit + 1, Offset: offset})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "list_sessions_failed", err.Error())
		return
	}
	sessions, returned, truncated := trimSessionSummaries(sessions, limit)
	out := make([]SessionDTO, 0, len(sessions))
	for _, summary := range sessions {
		out = append(out, toSessionSummaryDTO(summary))
	}
	writeJSON(w, SessionListResponse{Sessions: out, Limit: limit, Offset: offset, NextOffset: nextOffset(offset, returned, truncated), ResultTruncated: truncated})
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var req SessionCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONDecodeError(w, r, err)
		return
	}
	req.ProjectRoot = strings.TrimSpace(req.ProjectRoot)
	req.Provider = strings.TrimSpace(req.Provider)
	req.Model = strings.TrimSpace(req.Model)
	req.Agent = strings.TrimSpace(req.Agent)
	req.Metadata = sanitizeRunMetadata(req.Metadata)
	if req.ProjectRoot == "" || req.Provider == "" || req.Model == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_session", "project_root, provider, model이 필요해요")
		return
	}
	if err := validateRunMetadata(req.Metadata); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_session", err.Error())
		return
	}
	mode := session.AgentMode(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = session.AgentModeBuild
	} else if !validAgentMode(mode) {
		writeError(w, r, http.StatusBadRequest, "invalid_session", "mode는 build, plan, ask 중 하나여야 해요")
		return
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

func validAgentMode(mode session.AgentMode) bool {
	switch mode {
	case session.AgentModeBuild, session.AgentModePlan, session.AgentModeAsk:
		return true
	default:
		return false
	}
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
			writeJSONDecodeError(w, r, err)
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
	afterSeq, ok := queryAfterSeq(w, r)
	if !ok {
		return
	}
	limit, ok := queryLimitParam(w, r, "limit", 500, 5000, "invalid_session_events")
	if !ok {
		return
	}
	stream, ok := queryWantsSSE(w, r, "invalid_session_events")
	if !ok {
		return
	}
	var events []EventDTO
	if timeline, ok := s.cfg.Store.(session.TimelineStore); ok {
		records, err := timeline.ListEvents(r.Context(), session.EventQuery{SessionID: sessionID, AfterSeq: afterSeq, Limit: limit + 1})
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
			if len(events) >= limit+1 {
				break
			}
		}
	}
	if stream {
		events, _, _ = trimSessionEvents(events, limit)
		s.writeSSEEvents(w, r, events)
		return
	}
	events, truncated, nextAfterSeq := trimSessionEvents(events, limit)
	writeJSON(w, EventListResponse{Events: events, AfterSeq: afterSeq, Limit: limit, ResultTruncated: truncated, NextAfterSeq: nextAfterSeq})
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
	afterSeq, ok := queryAfterSeq(w, r)
	if !ok {
		return
	}
	limit, ok := queryLimitParam(w, r, "limit", 100, 500, "invalid_session_turns")
	if !ok {
		return
	}
	records, err := timeline.ListTurns(r.Context(), session.TurnQuery{SessionID: sessionID, AfterSeq: afterSeq, Limit: limit + 1})
	if err != nil {
		writeError(w, r, http.StatusNotFound, "session_not_found", err.Error())
		return
	}
	out := make([]TurnDTO, 0, len(records))
	for _, record := range records {
		out = append(out, toTurnDTO(sessionID, record.Seq, record.Turn))
	}
	out, truncated, nextAfterSeq := trimTurns(out, limit)
	writeJSON(w, TurnListResponse{Turns: out, Limit: limit, ResultTruncated: truncated, NextAfterSeq: nextAfterSeq})
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
	afterSeq, ok := queryAfterSeq(w, r)
	if !ok {
		return
	}
	limit, ok := queryLimitParam(w, r, "limit", 100, 500, "invalid_session_turns")
	if !ok {
		return
	}
	out := make([]TurnDTO, 0, min(len(sess.Turns), limit))
	for i, turn := range sess.Turns {
		seq := i + 1
		if seq <= afterSeq {
			continue
		}
		out = append(out, toTurnDTO(sess.ID, seq, turn))
		if len(out) >= limit+1 {
			break
		}
	}
	out, truncated, nextAfterSeq := trimTurns(out, limit)
	writeJSON(w, TurnListResponse{Turns: out, Limit: limit, ResultTruncated: truncated, NextAfterSeq: nextAfterSeq})
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
	if len(parts) == 2 && parts[1] == "preview" && r.Method == http.MethodPost {
		s.previewRun(w, r)
		return
	}
	if len(parts) == 2 && parts[1] == "validate" && r.Method == http.MethodPost {
		s.validateRun(w, r)
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
	if len(parts) == 3 && parts[2] == "transcript" && r.Method == http.MethodGet {
		s.getRunTranscript(w, r, runID)
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
	requestID := strings.TrimSpace(r.URL.Query().Get(RequestIDMetadataKey))
	if err := validateRequestIDValue(requestID); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_run_list", err.Error())
		return
	}
	idempotencyKey := strings.TrimSpace(r.URL.Query().Get(IdempotencyMetadataKey))
	if err := validateIdempotencyKeyValue(idempotencyKey); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_run_list", err.Error())
		return
	}
	limit, ok := queryLimitParam(w, r, "limit", 50, 200, "invalid_run_list")
	if !ok {
		return
	}
	offset, ok := queryOffsetParam(w, r, "offset", "invalid_run_list")
	if !ok {
		return
	}
	runs, err := s.cfg.RunLister(r.Context(), RunQuery{SessionID: r.URL.Query().Get("session_id"), Status: r.URL.Query().Get("status"), RequestID: requestID, IdempotencyKey: idempotencyKey, Limit: limit + 1, Offset: offset})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "list_runs_failed", err.Error())
		return
	}
	runs, returned, truncated := trimRuns(runs, limit)
	writeJSON(w, RunListResponse{Runs: runs, Limit: limit, Offset: offset, NextOffset: nextOffset(offset, returned, truncated), ResultTruncated: truncated})
}

func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	if s.cfg.RunStarter == nil {
		writeError(w, r, http.StatusNotImplemented, "run_starter_missing", "이 gateway에는 RunStarter가 연결되지 않았어요")
		return
	}
	var req RunStartRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONDecodeError(w, r, err)
		return
	}
	req = sanitizeRunStartRequest(req)
	req.Metadata = withRequestIDMetadata(req.Metadata, requestIDFromRequest(r))
	req.Metadata = withIdempotencyMetadata(req.Metadata, idempotencyKeyFromRequest(r, req.Metadata))
	req.Metadata = withDefaultMCPMetadata(req.Metadata, s.cfg.DefaultMCPServers)
	if err := validateRunStartRequest(req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_run", err.Error())
		return
	}
	if existing := s.findIdempotentRun(r.Context(), req); existing != nil {
		w.Header().Set(IdempotencyReplayHeader, "true")
		writeJSON(w, existing)
		return
	}
	if key := strings.TrimSpace(req.Metadata[IdempotencyMetadataKey]); key != "" {
		req.RunID = idempotentRunID(req.SessionID, key)
	}
	if s.cfg.RunValidator != nil {
		if err := s.cfg.RunValidator(r.Context(), req); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_run_preflight", err.Error())
			return
		}
	}
	run, err := s.cfg.RunStarter(r.Context(), req)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "start_run_failed", err.Error())
		return
	}
	if isIdempotencyReplay(run) {
		w.Header().Set(IdempotencyReplayHeader, "true")
		writeJSON(w, run)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, run)
}

func idempotencyKeyFromRequest(r *http.Request, metadata map[string]string) string {
	if r != nil {
		if key := strings.TrimSpace(r.Header.Get(IdempotencyKeyHeader)); key != "" {
			return key
		}
	}
	return strings.TrimSpace(metadata[IdempotencyMetadataKey])
}

func idempotentRunID(sessionID, idempotencyKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(idempotencyKey)))
	return "run_idem_" + fmt.Sprintf("%x", sum[:16])
}

func (s *Server) findIdempotentRun(ctx context.Context, req RunStartRequest) *RunDTO {
	key := strings.TrimSpace(req.Metadata[IdempotencyMetadataKey])
	if key == "" || s.cfg.RunLister == nil {
		return nil
	}
	runs, err := s.cfg.RunLister(ctx, RunQuery{SessionID: req.SessionID, IdempotencyKey: key, Limit: 1})
	if err != nil || len(runs) == 0 {
		return nil
	}
	return markIdempotencyReused(cloneRun(&runs[0]))
}

func isIdempotencyReplay(run *RunDTO) bool {
	return run != nil && strings.EqualFold(strings.TrimSpace(run.Metadata[IdempotencyReusedMetadataKey]), "true")
}

func (s *Server) validateRun(w http.ResponseWriter, r *http.Request) {
	var req RunStartRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONDecodeError(w, r, err)
		return
	}
	req = sanitizeRunStartRequest(req)
	requestID := requestIDFromRequest(r)
	idempotencyKey := idempotencyKeyFromRequest(r, req.Metadata)
	req.Metadata = withRequestIDMetadata(req.Metadata, requestID)
	req.Metadata = withIdempotencyMetadata(req.Metadata, idempotencyKey)
	req.Metadata = withDefaultMCPMetadata(req.Metadata, s.cfg.DefaultMCPServers)
	resp := RunValidateResponse{OK: true, RequestID: requestID, IdempotencyKey: strings.TrimSpace(req.Metadata[IdempotencyMetadataKey]), Metadata: cloneMap(req.Metadata)}
	if err := validateRunStartRequest(req); err != nil {
		resp.OK = false
		resp.Code = "invalid_run"
		resp.Message = err.Error()
		writeJSON(w, resp)
		return
	}
	if key := strings.TrimSpace(req.Metadata[IdempotencyMetadataKey]); key != "" {
		resp.RunID = idempotentRunID(req.SessionID, key)
	}
	if existing := s.findIdempotentRun(r.Context(), req); existing != nil {
		resp.ExistingRun = existing
	}
	if s.cfg.RunValidator == nil {
		resp.OK = false
		resp.Code = "run_validator_missing"
		resp.Message = "이 gateway에는 RunValidator가 연결되지 않았어요"
		writeJSON(w, resp)
		return
	}
	if err := s.cfg.RunValidator(r.Context(), req); err != nil {
		resp.OK = false
		resp.Code = "invalid_run_preflight"
		resp.Message = err.Error()
		writeJSON(w, resp)
		return
	}
	writeJSON(w, resp)
}

func (s *Server) previewRun(w http.ResponseWriter, r *http.Request) {
	if s.cfg.RunPreviewer == nil {
		writeError(w, r, http.StatusNotImplemented, "run_previewer_missing", "이 gateway에는 RunPreviewer가 연결되지 않았어요")
		return
	}
	var req RunStartRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSONDecodeError(w, r, err)
		return
	}
	req = sanitizeRunStartRequest(req)
	req.Metadata = withRequestIDMetadata(req.Metadata, requestIDFromRequest(r))
	req.Metadata = withDefaultMCPMetadata(req.Metadata, s.cfg.DefaultMCPServers)
	if err := validateRunStartRequest(req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_run", err.Error())
		return
	}
	preview, err := s.cfg.RunPreviewer(r.Context(), req)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "preview_run_failed", err.Error())
		return
	}
	writeJSON(w, preview)
}

func validateRunStartRequest(req RunStartRequest) error {
	if strings.TrimSpace(req.SessionID) == "" || strings.TrimSpace(req.Prompt) == "" {
		return errors.New("session_id와 prompt가 필요해요")
	}
	if req.MaxPreviewBytes < 0 {
		return errors.New("max_preview_bytes는 0 이상이어야 해요")
	}
	if req.MaxPreviewBytes > MaxRunPreviewBytes {
		return fmt.Errorf("max_preview_bytes는 %d 이하여야 해요", MaxRunPreviewBytes)
	}
	if err := validateRunRequestShape(req); err != nil {
		return err
	}
	if err := validateRunMetadata(req.Metadata); err != nil {
		return err
	}
	return nil
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
	metadata := sanitizeRunMetadata(original.Metadata)
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadata["retried_from"] = original.ID
	metadata = withRequestIDMetadata(metadata, requestIDFromRequest(r))
	metadata = withDefaultMCPMetadata(metadata, s.cfg.DefaultMCPServers)
	req := RunStartRequest{SessionID: original.SessionID, Prompt: original.Prompt, Provider: original.Provider, Model: original.Model, Metadata: metadata, MCPServers: cloneStringSlice(original.MCPServers), Skills: cloneStringSlice(original.Skills), Subagents: cloneStringSlice(original.Subagents), EnabledTools: cloneStringSlice(original.EnabledTools), DisabledTools: cloneStringSlice(original.DisabledTools), ContextBlocks: cloneStringSlice(original.ContextBlocks)}
	req = sanitizeRunStartRequest(req)
	if err := validateRunStartRequest(req); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_run", err.Error())
		return
	}
	if s.cfg.RunValidator != nil {
		if err := s.cfg.RunValidator(r.Context(), req); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_run_preflight", err.Error())
			return
		}
	}
	retry, err := s.cfg.RunStarter(r.Context(), req)
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
	afterSeq, ok := queryAfterSeq(w, r)
	if !ok {
		return
	}
	limit, ok := queryLimitParam(w, r, "limit", 200, 1000, "invalid_run_events")
	if !ok {
		return
	}
	stream, ok := queryWantsSSE(w, r, "invalid_run_events")
	if !ok {
		return
	}
	heartbeatInterval := 15 * time.Second
	if stream {
		var ok bool
		heartbeatInterval, ok = querySSEHeartbeatInterval(w, r, "invalid_run_events")
		if !ok {
			return
		}
	}
	var live <-chan RunDTO
	var liveEvents <-chan RunEventDTO
	var unsubscribe func()
	if stream && s.cfg.RunEventSubscriber != nil {
		liveEvents, unsubscribe = s.cfg.RunEventSubscriber(r.Context(), runID)
	} else if stream && s.cfg.RunSubscriber != nil {
		live, unsubscribe = s.cfg.RunSubscriber(r.Context(), runID)
	}
	run, err := s.cfg.RunGetter(r.Context(), runID)
	if err != nil {
		if unsubscribe != nil {
			unsubscribe()
		}
		writeError(w, r, http.StatusNotFound, "run_not_found", err.Error())
		return
	}
	if !stream {
		writeJSON(w, s.runEventSnapshotResponse(r, runID, *run, afterSeq, limit))
		return
	}
	s.writeRunSSE(w, r, runID, *run, live, liveEvents, unsubscribe, afterSeq, limit, heartbeatInterval)
}

func (s *Server) runEventSnapshot(r *http.Request, runID string, fallback RunDTO, afterSeq int, limit int) []RunEventDTO {
	return s.runEventSnapshotWithLimit(r, runID, fallback, afterSeq, limit)
}

func (s *Server) runEventSnapshotResponse(r *http.Request, runID string, fallback RunDTO, afterSeq int, limit int) RunEventListResponse {
	events := s.runEventSnapshotWithLimit(r, runID, fallback, afterSeq, limit+1)
	events, truncated, nextAfterSeq := trimRunEvents(events, limit)
	return RunEventListResponse{Events: events, AfterSeq: afterSeq, Limit: limit, ResultTruncated: truncated, NextAfterSeq: nextAfterSeq}
}

func (s *Server) runEventSnapshotWithLimit(r *http.Request, runID string, fallback RunDTO, afterSeq int, limit int) []RunEventDTO {
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

func trimRuns(runs []RunDTO, limit int) ([]RunDTO, int, bool) {
	if limit < 0 {
		limit = 0
	}
	truncated := len(runs) > limit
	if truncated {
		runs = runs[:limit]
	}
	return runs, len(runs), truncated
}

func trimSessionSummaries(sessions []session.SessionSummary, limit int) ([]session.SessionSummary, int, bool) {
	truncated := len(sessions) > limit
	if truncated {
		sessions = sessions[:limit]
	}
	return sessions, len(sessions), truncated
}

func trimResources(resources []session.Resource, limit int) ([]session.Resource, int, bool) {
	truncated := len(resources) > limit
	if truncated {
		resources = resources[:limit]
	}
	return resources, len(resources), truncated
}

func trimCheckpoints(checkpoints []session.Checkpoint, limit int) ([]session.Checkpoint, int, bool) {
	truncated := len(checkpoints) > limit
	if truncated {
		checkpoints = checkpoints[:limit]
	}
	return checkpoints, len(checkpoints), truncated
}

func trimTurns(turns []TurnDTO, limit int) ([]TurnDTO, bool, int) {
	truncated := len(turns) > limit
	if truncated {
		turns = turns[:limit]
	}
	next := 0
	if truncated && len(turns) > 0 {
		next = turns[len(turns)-1].Seq
	}
	return turns, truncated, next
}

func trimSessionEvents(events []EventDTO, limit int) ([]EventDTO, bool, int) {
	truncated := len(events) > limit
	if truncated {
		events = events[:limit]
	}
	next := 0
	if truncated && len(events) > 0 {
		next = events[len(events)-1].Seq
	}
	return events, truncated, next
}

func trimRunEvents(events []RunEventDTO, limit int) ([]RunEventDTO, bool, int) {
	truncated := len(events) > limit
	if truncated {
		events = events[:limit]
	}
	next := 0
	if truncated && len(events) > 0 {
		next = events[len(events)-1].Seq
	}
	return events, truncated, next
}

func (s *Server) writeRunSSE(w http.ResponseWriter, r *http.Request, runID string, initial RunDTO, live <-chan RunDTO, liveEvents <-chan RunEventDTO, unsubscribe func(), afterSeq int, limit int, heartbeatInterval time.Duration) {
	if unsubscribe != nil {
		defer unsubscribe()
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	events := s.runEventSnapshot(r, runID, initial, afterSeq, limit)
	seq := 0
	lastStatus := initial.Status
	for _, event := range events {
		seq = event.Seq
		lastStatus = event.Run.Status
		writeRunSSEEvent(w, flusher, event)
	}
	if seq == 0 {
		seq = 1
		writeRunSSEEvent(w, flusher, RunEventDTO{Seq: seq, At: s.cfg.Now(), Type: runEventType(initial.Status), Run: initial})
	}
	if isTerminalRunStatus(lastStatus) || (s.cfg.RunSubscriber == nil && s.cfg.RunEventSubscriber == nil) {
		return
	}
	if s.cfg.RunEventSubscriber != nil {
		ch := liveEvents
		if ch == nil {
			var localUnsubscribe func()
			ch, localUnsubscribe = s.cfg.RunEventSubscriber(r.Context(), initial.ID)
			defer localUnsubscribe()
		}
		for {
			select {
			case <-r.Context().Done():
				return
			case <-heartbeat.C:
				writeSSEHeartbeat(w, flusher)
			case event, ok := <-ch:
				if !ok {
					return
				}
				if event.Seq <= seq {
					seq++
					event.Seq = seq
				} else {
					seq = event.Seq
				}
				if event.At.IsZero() {
					event.At = s.cfg.Now()
				}
				writeRunSSEEvent(w, flusher, event)
				if isTerminalRunStatus(event.Run.Status) {
					return
				}
			}
		}
	}
	ch := live
	if ch == nil {
		var localUnsubscribe func()
		ch, localUnsubscribe = s.cfg.RunSubscriber(r.Context(), initial.ID)
		defer localUnsubscribe()
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			writeSSEHeartbeat(w, flusher)
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

func querySSEHeartbeatInterval(w http.ResponseWriter, r *http.Request, code string) (time.Duration, bool) {
	ms, ok := queryNonNegativeLimitParam(w, r, "heartbeat_ms", 15000, 300000, code)
	if !ok {
		return 0, false
	}
	if ms == 0 {
		ms = 15000
	}
	if ms < 10 {
		ms = 10
	}
	return time.Duration(ms) * time.Millisecond, true
}

func writeSSEHeartbeat(w http.ResponseWriter, flusher http.Flusher) {
	fmt.Fprint(w, ": heartbeat\n\n")
	if flusher != nil {
		flusher.Flush()
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
	if err := dec.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("JSON body에는 하나의 값만 있어야 해요")
		}
		return err
	}
	return nil
}

func decodeOptionalJSON(r *http.Request, out any) (bool, error) {
	if r.Body == nil {
		return false, nil
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		if err == io.EOF {
			return false, nil
		}
		return false, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return true, errors.New("JSON body에는 하나의 값만 있어야 해요")
		}
		return true, err
	}
	return true, nil
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

func queryLimitParam(w http.ResponseWriter, r *http.Request, key string, fallback int, maxValue int, code string) (int, bool) {
	return queryNonNegativeLimitParam(w, r, key, fallback, maxValue, code)
}

func queryNonNegativeIntParam(w http.ResponseWriter, r *http.Request, key string, fallback int, code string) (int, bool) {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return fallback, true
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		writeError(w, r, http.StatusBadRequest, code, key+"는 0 이상이어야 해요")
		return 0, false
	}
	return parsed, true
}

func queryNonNegativeLimitParam(w http.ResponseWriter, r *http.Request, key string, fallback int, maxValue int, code string) (int, bool) {
	value, ok := queryNonNegativeIntParam(w, r, key, fallback, code)
	if !ok {
		return 0, false
	}
	if maxValue > 0 && value > maxValue {
		return maxValue, true
	}
	return value, true
}

func queryAfterSeq(w http.ResponseWriter, r *http.Request) (int, bool) {
	return queryNonNegativeIntParam(w, r, "after_seq", 0, "invalid_after_seq")
}

func queryOffsetParam(w http.ResponseWriter, r *http.Request, key string, code string) (int, bool) {
	return queryNonNegativeIntParam(w, r, key, 0, code)
}

func nextOffset(offset int, returned int, truncated bool) int {
	if !truncated {
		return 0
	}
	return offset + returned
}

func queryBoolParam(w http.ResponseWriter, r *http.Request, key string, fallback bool, code string) (bool, bool) {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return fallback, true
	}
	switch strings.ToLower(value) {
	case "true", "1", "yes":
		return true, true
	case "false", "0", "no":
		return false, true
	default:
		writeError(w, r, http.StatusBadRequest, code, key+"는 boolean이어야 해요")
		return false, false
	}
}

func queryWantsSSE(w http.ResponseWriter, r *http.Request, code string) (bool, bool) {
	stream, ok := queryBoolParam(w, r, "stream", false, code)
	if !ok {
		return false, false
	}
	if stream {
		return true, true
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream"), true
}
