package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/sleepysoong/kkode/agent"
	"github.com/sleepysoong/kkode/app"
	"github.com/sleepysoong/kkode/gateway"
	"github.com/sleepysoong/kkode/llm"
	kruntime "github.com/sleepysoong/kkode/runtime"
	"github.com/sleepysoong/kkode/session"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "오류가 났어요:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("kkode-gateway", flag.ContinueOnError)
	addr := fs.String("addr", app.EnvDefault("KKODE_GATEWAY_ADDR", "127.0.0.1:41234"), "gateway가 listen할 주소예요")
	statePath := fs.String("state", app.EnvDefault("KKODE_STATE_DB", ".kkode/state.db"), "SQLite session state DB 경로예요")
	apiKey := fs.String("api-key", os.Getenv("KKODE_API_KEY"), "API bearer token이에요")
	apiKeyEnv := fs.String("api-key-env", "", "API bearer token을 읽을 환경변수 이름이에요")
	allowLocalhostNoAuth := fs.Bool("no-auth-localhost", app.EnvBoolDefault("KKODE_NO_AUTH_LOCALHOST", true), "localhost 요청은 API key 없이 허용해요")
	corsOrigins := fs.String("cors-origins", app.EnvDefault("KKODE_CORS_ORIGINS", ""), "쉼표로 구분한 허용 CORS origin 목록이에요")
	accessLog := fs.Bool("access-log", app.EnvBool("KKODE_ACCESS_LOG"), "JSONL access log를 stderr로 출력해요")
	maxBodyBytes := fs.Int64("max-body-bytes", app.EnvInt64("KKODE_MAX_BODY_BYTES", 32<<20), "gateway API 요청 body 최대 byte 수예요. 음수면 비활성화해요")
	minStateFreeBytes := fs.Int64("min-state-free-bytes", app.EnvInt64("KKODE_MIN_STATE_FREE_BYTES", 100<<20), "diagnostics에서 warning으로 표시할 state DB filesystem 최소 여유 byte예요. 0이면 비활성화해요")
	maxConcurrentRuns := fs.Int("max-concurrent-runs", app.EnvInt("KKODE_MAX_CONCURRENT_RUNS", 4), "동시에 running 상태로 실행할 background run 최대 개수예요. 0 이하면 제한하지 않아요")
	runTimeout := fs.Duration("run-timeout", envDuration("KKODE_RUN_TIMEOUT", 0), "background run 실행 timeout이에요. 0이면 제한하지 않아요")
	readHeaderTimeout := fs.Duration("read-header-timeout", envDuration("KKODE_READ_HEADER_TIMEOUT", 10*time.Second), "HTTP read header timeout이에요")
	readTimeout := fs.Duration("read-timeout", envDuration("KKODE_READ_TIMEOUT", 0), "HTTP read timeout이에요. 0이면 비활성화해요")
	writeTimeout := fs.Duration("write-timeout", envDuration("KKODE_WRITE_TIMEOUT", 0), "HTTP write timeout이에요. SSE를 오래 유지하려면 0을 권장해요")
	idleTimeout := fs.Duration("idle-timeout", envDuration("KKODE_IDLE_TIMEOUT", 120*time.Second), "HTTP idle timeout이에요")
	shutdownTimeout := fs.Duration("shutdown-timeout", envDuration("KKODE_SHUTDOWN_TIMEOUT", 10*time.Second), "graceful shutdown timeout이에요")
	version := fs.String("version", app.EnvDefault("KKODE_VERSION", "dev"), "version endpoint에 표시할 버전이에요")
	maxIterations := fs.Int("max-iterations", app.EnvInt("KKODE_MAX_ITERATIONS", app.DefaultAgentMaxIterations), "gateway run tool loop 최대 반복 횟수예요")
	noWeb := fs.Bool("no-web", app.EnvBool("KKODE_NO_WEB"), "gateway run에서 web_fetch tool을 비활성화해요")
	webMaxBytes := fs.Int64("web-max-bytes", app.EnvInt64("KKODE_WEB_MAX_BYTES", app.DefaultAgentWebMaxBytes), "gateway run web_fetch 최대 byte 수예요")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *apiKeyEnv != "" {
		*apiKey = os.Getenv(*apiKeyEnv)
	}
	if !isLoopbackListenAddr(*addr) && *apiKey == "" {
		return fmt.Errorf("remote bind(%s)는 --api-key 또는 --api-key-env가 필요해요", *addr)
	}
	unregisterHTTPJSONProviders, err := app.RegisterHTTPJSONProvidersFromEnv("KKODE_HTTPJSON_PROVIDERS")
	if err != nil {
		return err
	}
	defer unregisterHTTPJSONProviders()
	store, err := session.OpenSQLite(*statePath)
	if err != nil {
		return err
	}
	defer store.Close()
	runOpts, err := normalizeRunOptions(runOptions{MaxIterations: *maxIterations, NoWeb: *noWeb, WebMaxBytes: *webMaxBytes})
	if err != nil {
		return err
	}
	runManager := gateway.NewAsyncRunManagerWithStore(syncRunStarter(store, runOpts), store).SetMaxConcurrentRuns(*maxConcurrentRuns).SetRunTimeout(*runTimeout)
	if err := runManager.RecoverStaleRuns(context.Background()); err != nil {
		return err
	}
	srv, err := gateway.New(gateway.Config{
		Store:                store,
		StatePath:            *statePath,
		MinStateFreeBytes:    *minStateFreeBytes,
		Version:              *version,
		APIKey:               *apiKey,
		AllowLocalhostNoAuth: *allowLocalhostNoAuth,
		CORSOrigins:          splitCSV(*corsOrigins),
		MaxRequestBytes:      *maxBodyBytes,
		MaxConcurrentRuns:    runManager.MaxConcurrentRuns(),
		RunTimeout:           runManager.RunTimeout(),
		RunMaxIterations:     runOpts.MaxIterations,
		RunWebMaxBytes:       runOpts.WebMaxBytes,
		AccessLogger:         accessLoggerForFlag(*accessLog, os.Stderr),
		RunStarter:           runManager.Start,
		RunPreviewer:         syncRunPreviewer(store, runOpts),
		RunValidator:         syncRunValidator(store),
		ProviderTester:       syncProviderTester(),
		RunRuntimeStats:      runManager.RuntimeStats,
		RunGetter:            runManager.Get,
		RunLister:            runManager.List,
		RunCanceler:          runManager.Cancel,
		RunEventLister:       runManager.Events,
		RunSubscriber:        runManager.Subscribe,
		RunEventSubscriber:   runManager.SubscribeEvents,
		Providers:            providerDTOs(),
		DefaultMCPServers:    defaultMCPDTOs(),
		DiagnosticChecks:     defaultMCPDiagnosticChecks(),
		ResourceStore:        store,
	})
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: *readHeaderTimeout,
		ReadTimeout:       *readTimeout,
		WriteTimeout:      *writeTimeout,
		IdleTimeout:       *idleTimeout,
	}
	fmt.Fprintf(os.Stderr, "kkode gateway가 http://%s 에서 실행돼요\n", *addr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serveHTTP(ctx, httpServer, os.Stderr, *shutdownTimeout, runManager.Shutdown)
}

func serveHTTP(ctx context.Context, server *http.Server, log io.Writer, shutdownTimeout time.Duration, shutdownHooks ...func(context.Context) error) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		if log != nil {
			fmt.Fprintln(log, "kkode gateway를 정상 종료해요")
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		for _, hook := range shutdownHooks {
			if hook == nil {
				continue
			}
			if err := hook(shutdownCtx); err != nil {
				return err
			}
		}
		err := <-errCh
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

type accessLogWriter struct {
	mu      sync.Mutex
	encoder *json.Encoder
}

func accessLoggerForFlag(enabled bool, out io.Writer) gateway.AccessLogger {
	if !enabled || out == nil {
		return nil
	}
	writer := &accessLogWriter{encoder: json.NewEncoder(out)}
	return writer.log
}

func (w *accessLogWriter) log(entry gateway.AccessLogEntry) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.encoder.Encode(map[string]any{
		"type":        "access",
		"request_id":  entry.RequestID,
		"method":      entry.Method,
		"path":        entry.Path,
		"status":      entry.Status,
		"bytes":       entry.Bytes,
		"duration_ms": float64(entry.Duration.Microseconds()) / 1000.0,
		"remote":      entry.Remote,
		"user_agent":  entry.UserAgent,
	})
}

type runOptions struct {
	MaxIterations int
	NoWeb         bool
	WebMaxBytes   int64
}

func normalizeRunOptions(opts runOptions) (runOptions, error) {
	if opts.MaxIterations < 0 {
		return opts, fmt.Errorf("max-iterations는 0 이상이어야 해요")
	}
	if opts.MaxIterations == 0 {
		opts.MaxIterations = app.DefaultAgentMaxIterations
	}
	if opts.MaxIterations > app.MaxAgentMaxIterations {
		return opts, fmt.Errorf("max-iterations는 %d 이하여야 해요", app.MaxAgentMaxIterations)
	}
	if opts.WebMaxBytes < 0 {
		return opts, fmt.Errorf("web-max-bytes는 0 이상이어야 해요")
	}
	if opts.WebMaxBytes == 0 {
		opts.WebMaxBytes = app.DefaultAgentWebMaxBytes
	}
	if opts.WebMaxBytes > app.MaxAgentWebMaxBytes {
		return opts, fmt.Errorf("web-max-bytes는 %d 이하여야 해요", app.MaxAgentWebMaxBytes)
	}
	return opts, nil
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func syncRunStarter(store session.Store, opts runOptions) gateway.RunStarter {
	return func(ctx context.Context, req gateway.RunStartRequest) (*gateway.RunDTO, error) {
		req.ContextBlocks = gateway.SanitizeContextBlocks(req.ContextBlocks)
		sess, err := store.LoadSession(ctx, req.SessionID)
		if err != nil {
			return nil, err
		}
		providerName := firstNonEmpty(req.Provider, sess.ProviderName)
		model := firstNonEmpty(req.Model, sess.Model)
		ws, absRoot, err := app.NewWorkspace(app.WorkspaceOptions{Root: sess.ProjectRoot})
		if err != nil {
			return nil, err
		}
		providerOptions, err := loadProviderOptions(ctx, store, req)
		if err != nil {
			return nil, err
		}
		effectiveProviderOptions := app.MergeProviderOptions(app.DefaultProviderOptions(absRoot, req.WorkingDirectory), providerOptions)
		providerHandle, err := app.BuildProviderWithResolvedOptions(providerName, absRoot, effectiveProviderOptions)
		if err != nil {
			return nil, err
		}
		if model == "" {
			model = app.DefaultModel(providerHandle.Provider.Name())
		}
		if providerHandle.Close != nil {
			defer providerHandle.Close()
		}
		ag, err := app.NewAgent(providerHandle.Provider, ws, app.AgentOptions{Model: model, ContextBlocks: effectiveProviderOptions.ContextBlocks, BaseRequest: app.MergeBaseRequest(providerHandle.BaseRequest, llm.Request{Metadata: req.Metadata, MaxOutputTokens: req.MaxOutputTokens}), MaxIterations: opts.MaxIterations, NoWeb: opts.NoWeb, WebMaxBytes: opts.WebMaxBytes, EnabledTools: req.EnabledTools, DisabledTools: req.DisabledTools, MCPServers: effectiveProviderOptions.MCPServers, Observer: runEventTraceObserver()})
		if err != nil {
			return nil, err
		}
		rt := app.NewRuntime(store, ag, app.RuntimeOptions{ProjectRoot: absRoot, ProviderName: providerHandle.Provider.Name(), Model: model, AgentName: firstNonEmpty(sess.AgentName, "kkode-gateway"), Mode: sess.Mode})
		started := time.Now().UTC()
		result, runErr := rt.Run(ctx, kruntime.RunOptions{SessionID: req.SessionID, Prompt: req.Prompt})
		runID := strings.TrimSpace(req.RunID)
		if runID == "" {
			runID = session.NewID("run")
		}
		run := &gateway.RunDTO{ID: runID, SessionID: req.SessionID, Prompt: req.Prompt, Provider: providerName, Model: model, WorkingDirectory: req.WorkingDirectory, MaxOutputTokens: req.MaxOutputTokens, MCPServers: cloneStringSlice(req.MCPServers), Skills: cloneStringSlice(req.Skills), Subagents: cloneStringSlice(req.Subagents), EnabledTools: cloneStringSlice(req.EnabledTools), DisabledTools: cloneStringSlice(req.DisabledTools), ContextBlocks: cloneStringSlice(req.ContextBlocks), Status: "completed", StartedAt: started, EndedAt: time.Now().UTC(), Metadata: req.Metadata}
		if result != nil {
			run.TurnID = result.Turn.ID
			if result.Turn.Response != nil {
				run.Usage = usageDTO(result.Turn.Response.Usage)
			}
		}
		run.EventsURL = "/api/v1/runs/" + runID + "/events"
		if runErr != nil {
			run.Status = "failed"
			run.Error = runErr.Error()
		}
		return run, nil
	}
}

func runEventTraceObserver() agent.Observer {
	return agent.ObserverFunc(func(ctx context.Context, event agent.TraceEvent) {
		gateway.ReportRunEvent(ctx, gateway.RunEventDTO{
			At:      event.At,
			Type:    event.Type,
			Tool:    event.Tool,
			Message: llm.RedactSecrets(event.Message),
			Error:   llm.RedactSecrets(event.Error),
		})
	})
}

func syncRunValidator(store session.Store) gateway.RunValidator {
	return func(ctx context.Context, req gateway.RunStartRequest) error {
		sess, err := store.LoadSession(ctx, req.SessionID)
		if err != nil {
			return err
		}
		providerName := firstNonEmpty(req.Provider, sess.ProviderName)
		if providerName == "" {
			providerName = "openai"
		}
		if _, ok := app.ResolveProviderSpec(providerName); !ok {
			return fmt.Errorf("unknown provider: %s", providerName)
		}
		providerOptions, err := loadProviderOptions(ctx, store, req)
		if err != nil {
			return err
		}
		if _, absRoot, err := app.NewWorkspace(app.WorkspaceOptions{Root: sess.ProjectRoot}); err != nil {
			return fmt.Errorf("workspace preflight failed: %w", err)
		} else {
			effectiveProviderOptions := app.MergeProviderOptions(app.DefaultProviderOptions(absRoot, req.WorkingDirectory), providerOptions)
			handle, err := app.BuildProviderWithResolvedOptions(providerName, absRoot, effectiveProviderOptions)
			if err != nil {
				return fmt.Errorf("provider preflight failed: %w", err)
			}
			if handle.Close != nil {
				if err := handle.Close(); err != nil {
					return fmt.Errorf("provider preflight close failed: %w", err)
				}
			}
		}
		return nil
	}
}

func syncRunPreviewer(store session.Store, opts runOptions) gateway.RunPreviewer {
	return func(ctx context.Context, req gateway.RunStartRequest) (*gateway.RunPreviewResponse, error) {
		sess, err := store.LoadSession(ctx, req.SessionID)
		if err != nil {
			return nil, err
		}
		providerName := firstNonEmpty(req.Provider, sess.ProviderName)
		model := firstNonEmpty(req.Model, sess.Model)
		if model == "" {
			model = app.DefaultModel(providerName)
		}
		providerOptions, err := loadProviderOptions(ctx, store, req)
		if err != nil {
			return nil, err
		}
		ws, absRoot, err := app.NewWorkspace(app.WorkspaceOptions{Root: sess.ProjectRoot})
		if err != nil {
			return nil, err
		}
		effectiveProviderOptions := app.MergeProviderOptions(app.DefaultProviderOptions(absRoot, req.WorkingDirectory), providerOptions)
		handle, err := app.BuildProviderWithResolvedOptions(providerName, absRoot, effectiveProviderOptions)
		if err != nil {
			return nil, err
		}
		if handle.Close != nil {
			defer handle.Close()
		}
		if model == "" && handle.Provider != nil {
			model = app.DefaultModel(handle.Provider.Name())
		}
		ag, err := app.NewAgent(handle.Provider, ws, app.AgentOptions{Model: model, ContextBlocks: effectiveProviderOptions.ContextBlocks, BaseRequest: app.MergeBaseRequest(handle.BaseRequest, llm.Request{Metadata: req.Metadata, MaxOutputTokens: req.MaxOutputTokens}), MaxIterations: opts.MaxIterations, NoWeb: opts.NoWeb, WebMaxBytes: opts.WebMaxBytes, EnabledTools: req.EnabledTools, DisabledTools: req.DisabledTools, MCPServers: effectiveProviderOptions.MCPServers})
		if err != nil {
			return nil, err
		}
		providerReq, handlers := ag.Prepare(req.Prompt)
		localTools := localToolNames(providerReq.Tools, handlers)
		maxPreviewBytes := runPreviewBytes(req.MaxPreviewBytes)
		providerPreview, err := app.PreviewProviderRequest(ctx, providerName, providerReq, req.PreviewStream, maxPreviewBytes)
		if err != nil {
			return nil, err
		}
		contextBlocks, contextTruncated := previewContextBlocks(effectiveProviderOptions.ContextBlocks, maxPreviewBytes)
		return &gateway.RunPreviewResponse{
			SessionID:         req.SessionID,
			ProjectRoot:       absRoot,
			Provider:          providerName,
			Model:             model,
			MCPServers:        resourceDTOsForIDs(ctx, store, session.ResourceMCPServer, req.MCPServers),
			Skills:            resourceDTOsForIDs(ctx, store, session.ResourceSkill, req.Skills),
			Subagents:         resourceDTOsForIDs(ctx, store, session.ResourceSubagent, req.Subagents),
			DefaultMCPServers: defaultMCPDTOs(),
			BaseRequestTools:  toolNames(handle.BaseRequest.Tools),
			LocalTools:        localTools,
			ContextBlocks:     contextBlocks,
			ContextTruncated:  contextTruncated,
			ProviderRequest:   toProviderRequestPreviewDTO(providerPreview),
		}, nil
	}
}

func syncProviderTester() gateway.ProviderTester {
	return func(ctx context.Context, providerName string, req gateway.ProviderTestRequest) (*gateway.ProviderTestResponse, error) {
		if err := validateProviderTestBudgets(req); err != nil {
			return nil, err
		}
		spec, ok := app.ResolveProviderSpec(providerName)
		if !ok {
			return nil, fmt.Errorf("unknown provider: %s", providerName)
		}
		model := firstNonEmpty(req.Model, spec.DefaultModel)
		if model == "" {
			model = app.DefaultModel(spec.Name)
		}
		prompt := strings.TrimSpace(req.Prompt)
		if prompt == "" {
			prompt = "provider test예요. 짧게 ok라고 답해요."
		}
		maxOutputTokens := req.MaxOutputTokens
		if maxOutputTokens <= 0 {
			maxOutputTokens = 64
		}
		providerReq := llm.Request{
			Model:           model,
			Messages:        []llm.Message{llm.UserText(prompt)},
			Metadata:        llm.CloneMetadata(req.Metadata),
			MaxOutputTokens: maxOutputTokens,
		}
		preview, err := app.PreviewProviderRequest(ctx, spec.Name, providerReq, req.Stream, req.MaxPreviewBytes)
		out := &gateway.ProviderTestResponse{
			OK:         err == nil,
			Provider:   spec.Name,
			Model:      model,
			AuthStatus: app.ProviderAuthStatus(spec),
			Live:       req.Live,
			Stream:     req.Stream,
		}
		if preview != nil {
			out.ProviderRequest = toProviderRequestPreviewDTO(preview)
		}
		if err != nil {
			out.Code = "provider_preview_failed"
			out.Message = err.Error()
			return out, nil
		}
		out.Message = "provider 변환 preflight가 성공했어요"
		if !req.Live {
			return out, nil
		}
		if out.AuthStatus == "missing" {
			out.OK = false
			out.Code = "provider_auth_missing"
			out.Message = "provider 인증 환경변수가 설정되지 않았어요: " + strings.Join(spec.AuthEnv, ", ")
			return out, nil
		}

		timeout, err := providerTestTimeout(req.TimeoutMS)
		if err != nil {
			return nil, err
		}
		liveCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		handle, err := app.BuildProviderWithOptions(spec.Name, ".", app.ProviderOptions{})
		if err != nil {
			out.OK = false
			out.Code = "provider_build_failed"
			out.Message = err.Error()
			return out, nil
		}
		if handle.Close != nil {
			defer handle.Close()
		}
		if handle.Provider == nil {
			out.OK = false
			out.Code = "provider_build_failed"
			out.Message = "provider factory가 nil provider를 반환했어요"
			return out, nil
		}
		if req.Stream {
			result, err := smokeStreamProvider(liveCtx, handle.Provider, providerReq, req.MaxResultBytes)
			if err != nil {
				out.OK = false
				out.Code = "provider_live_stream_failed"
				out.Message = err.Error()
				return out, nil
			}
			out.Result = result
			out.Message = "provider live stream test가 성공했어요"
			return out, nil
		}
		resp, err := handle.Provider.Generate(liveCtx, providerReq)
		if err != nil {
			out.OK = false
			out.Code = "provider_live_failed"
			out.Message = err.Error()
			return out, nil
		}
		out.Result = providerTestResult(resp, req.MaxResultBytes)
		out.Message = "provider live test가 성공했어요"
		return out, nil
	}
}

func validateProviderTestBudgets(req gateway.ProviderTestRequest) error {
	switch {
	case req.MaxPreviewBytes < 0:
		return fmt.Errorf("max_preview_bytes는 0 이상이어야 해요")
	case req.MaxPreviewBytes > gateway.MaxProviderTestPreviewBytes:
		return fmt.Errorf("max_preview_bytes는 %d 이하여야 해요", gateway.MaxProviderTestPreviewBytes)
	case req.MaxOutputTokens < 0:
		return fmt.Errorf("max_output_tokens는 0 이상이어야 해요")
	case req.MaxOutputTokens > gateway.MaxProviderTestOutputTokens:
		return fmt.Errorf("max_output_tokens는 %d 이하여야 해요", gateway.MaxProviderTestOutputTokens)
	case req.MaxResultBytes < 0:
		return fmt.Errorf("max_result_bytes는 0 이상이어야 해요")
	case req.MaxResultBytes > gateway.MaxProviderTestResultBytes:
		return fmt.Errorf("max_result_bytes는 %d 이하여야 해요", gateway.MaxProviderTestResultBytes)
	case req.TimeoutMS < 0:
		return fmt.Errorf("timeout_ms는 0 이상이어야 해요")
	case req.TimeoutMS > gateway.MaxProviderTestTimeoutMS:
		return fmt.Errorf("timeout_ms는 %d 이하여야 해요", gateway.MaxProviderTestTimeoutMS)
	default:
		return nil
	}
}

func providerTestTimeout(timeoutMS int) (time.Duration, error) {
	if timeoutMS < 0 {
		return 0, fmt.Errorf("timeout_ms는 0 이상이어야 해요")
	}
	if timeoutMS == 0 {
		return 60 * time.Second, nil
	}
	if timeoutMS > gateway.MaxProviderTestTimeoutMS {
		return 0, fmt.Errorf("timeout_ms는 %d 이하여야 해요", gateway.MaxProviderTestTimeoutMS)
	}
	return time.Duration(timeoutMS) * time.Millisecond, nil
}

func smokeStreamProvider(ctx context.Context, provider llm.Provider, req llm.Request, maxResultBytes int) (*gateway.ProviderTestResultDTO, error) {
	if provider == nil {
		return nil, fmt.Errorf("provider가 필요해요")
	}
	streamer, ok := provider.(llm.StreamProvider)
	if !ok {
		return nil, fmt.Errorf("provider stream을 지원하지 않아요: %s", provider.Name())
	}
	stream, err := streamer.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	text := newLimitedProviderTextBuffer(providerTestResultLimit(maxResultBytes))
	result := &gateway.ProviderTestResultDTO{Status: "streaming"}
	for i := 0; i < 128; i++ {
		event, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		if event.Delta != "" {
			text.WriteString(event.Delta)
		}
		if event.Response != nil {
			result = providerTestResult(event.Response, maxResultBytes)
			if text.Len() > 0 && result.Text == "" {
				setProviderTestResultText(result, text.String(), maxResultBytes)
			}
		}
		if event.Type == llm.StreamEventError {
			if event.Error != nil {
				return nil, event.Error
			}
			return nil, fmt.Errorf("provider stream error event를 받았어요")
		}
		if event.Type == llm.StreamEventCompleted {
			if result.Status == "" || result.Status == "streaming" {
				result.Status = "completed"
			}
			if result.Text == "" {
				setProviderTestResultText(result, text.String(), maxResultBytes)
			}
			return result, nil
		}
	}
	if result.Text == "" {
		setProviderTestResultText(result, text.String(), maxResultBytes)
	}
	return result, fmt.Errorf("provider stream이 완료 event를 보내지 않았어요")
}

func providerTestResult(resp *llm.Response, maxResultBytes int) *gateway.ProviderTestResultDTO {
	if resp == nil {
		return nil
	}
	result := &gateway.ProviderTestResultDTO{
		ID:     resp.ID,
		Status: resp.Status,
	}
	setProviderTestResultText(result, resp.Text, maxResultBytes)
	result.Usage = usageDTO(resp.Usage)
	return result
}

func usageDTO(usage llm.Usage) *gateway.UsageDTO {
	if usage == (llm.Usage{}) {
		return nil
	}
	return &gateway.UsageDTO{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens, ReasoningTokens: usage.ReasoningTokens}
}

func setProviderTestResultText(result *gateway.ProviderTestResultDTO, text string, maxBytes int) {
	if result == nil || text == "" {
		return
	}
	redacted := llm.RedactSecrets(text)
	result.TextBytes = len(redacted)
	maxBytes = providerTestResultLimit(maxBytes)
	if maxBytes <= 0 || len(redacted) <= maxBytes {
		result.Text = redacted
		result.TextTruncated = false
		return
	}
	result.Text = truncateUTF8Bytes(redacted, maxBytes)
	result.TextTruncated = true
}

func providerTestResultLimit(maxBytes int) int {
	if maxBytes <= 0 {
		return gateway.MaxProviderTestResultBytes
	}
	return maxBytes
}

type limitedProviderTextBuffer struct {
	buf       strings.Builder
	max       int
	truncated bool
}

func newLimitedProviderTextBuffer(max int) *limitedProviderTextBuffer {
	return &limitedProviderTextBuffer{max: max}
}

func (b *limitedProviderTextBuffer) Len() int {
	return b.buf.Len()
}

func (b *limitedProviderTextBuffer) WriteString(text string) {
	if b.max <= 0 {
		b.truncated = true
		return
	}
	remaining := b.max - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return
	}
	if len(text) > remaining {
		b.buf.WriteString(truncateUTF8Bytes(text, remaining))
		b.truncated = true
		return
	}
	b.buf.WriteString(text)
}

func (b *limitedProviderTextBuffer) String() string {
	text := b.buf.String()
	if b.truncated {
		return strings.TrimRight(text, "\n") + "\n[output truncated]"
	}
	return text
}

func toProviderRoutePreviewDTO(route *app.ProviderRoutePreview) *gateway.ProviderRoutePreviewDTO {
	if route == nil {
		return nil
	}
	return &gateway.ProviderRoutePreviewDTO{
		Operation:     route.Operation,
		Method:        route.Method,
		Path:          route.Path,
		Accept:        route.Accept,
		Query:         cloneStringMap(route.Query),
		ResolvedPath:  route.ResolvedPath,
		ResolvedQuery: cloneStringMap(route.ResolvedQuery),
	}
}

func toProviderRequestPreviewDTO(preview *app.ProviderRequestPreview) *gateway.ProviderRequestPreviewDTO {
	if preview == nil {
		return nil
	}
	return &gateway.ProviderRequestPreviewDTO{
		Provider:      preview.Provider,
		Operation:     preview.Operation,
		Model:         preview.Model,
		Stream:        preview.Stream,
		Route:         toProviderRoutePreviewDTO(preview.Route),
		BodyJSON:      preview.BodyJSON,
		BodyTruncated: preview.BodyTruncated,
		Headers:       preview.Headers,
		Metadata:      preview.Metadata,
		RawType:       preview.RawType,
		RawJSON:       preview.RawJSON,
		RawTruncated:  preview.RawTruncated,
	}
}

func resourceDTOsForIDs(ctx context.Context, store session.Store, kind session.ResourceKind, ids []string) []gateway.ResourceDTO {
	resourceStore, _ := store.(session.ResourceStore)
	if resourceStore == nil || len(ids) == 0 {
		return nil
	}
	out := make([]gateway.ResourceDTO, 0, len(ids))
	for _, id := range ids {
		resource, err := resourceStore.LoadResource(ctx, kind, id)
		if err != nil {
			continue
		}
		out = append(out, gateway.RedactResourceDTO(resourceToGatewayDTO(resource)))
	}
	return out
}

func resourceToGatewayDTO(resource session.Resource) gateway.ResourceDTO {
	config := map[string]any{}
	if len(resource.Config) > 0 {
		_ = json.Unmarshal(resource.Config, &config)
	}
	enabled := resource.Enabled
	return gateway.ResourceDTO{ID: resource.ID, Kind: string(resource.Kind), Name: resource.Name, Description: resource.Description, Enabled: &enabled, Config: config, CreatedAt: resource.CreatedAt.Format(time.RFC3339Nano), UpdatedAt: resource.UpdatedAt.Format(time.RFC3339Nano)}
}

func toolNames(tools []llm.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool.Name != "" {
			out = append(out, tool.Name)
		}
	}
	return out
}

func localToolNames(tools []llm.Tool, handlers llm.ToolRegistry) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool.Name != "" && handlers[tool.Name] != nil {
			out = append(out, tool.Name)
		}
	}
	return out
}

func isLoopbackListenAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func cloneStringSlice(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func runPreviewBytes(maxBytes int) int {
	if maxBytes <= 0 {
		return 64 << 10
	}
	if maxBytes > gateway.MaxRunPreviewBytes {
		return gateway.MaxRunPreviewBytes
	}
	return maxBytes
}

func previewContextBlocks(blocks []string, maxBytes int) ([]string, bool) {
	if len(blocks) == 0 {
		return nil, false
	}
	if maxBytes <= 0 {
		maxBytes = 64 << 10
	}
	out := make([]string, 0, len(blocks))
	remaining := maxBytes
	truncated := false
	for _, block := range blocks {
		block = strings.TrimSpace(llm.RedactSecrets(block))
		if block == "" {
			continue
		}
		if remaining <= 0 {
			truncated = true
			break
		}
		if len(block) > remaining {
			block = truncateUTF8Bytes(block, remaining)
			truncated = true
		}
		if block != "" {
			out = append(out, block)
			remaining -= len(block)
		}
		if truncated {
			break
		}
	}
	if len(out) == 0 {
		return nil, truncated
	}
	return out, truncated
}

func truncateUTF8Bytes(text string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	used := 0
	end := 0
	for i, r := range text {
		size := utf8.RuneLen(r)
		if size < 0 {
			size = len(string(r))
		}
		if used+size > maxBytes {
			break
		}
		used += size
		end = i + size
	}
	if end == 0 {
		return ""
	}
	return text[:end]
}

func firstNonEmpty(value, fallback string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(fallback)
}

func providerDTOs() []gateway.ProviderDTO {
	specs := app.ProviderSpecs()
	out := make([]gateway.ProviderDTO, 0, len(specs))
	for _, spec := range specs {
		models := append([]string(nil), spec.Models...)
		if len(models) == 0 && spec.DefaultModel != "" {
			models = []string{spec.DefaultModel}
		}
		out = append(out, gateway.ProviderDTO{Name: spec.Name, Aliases: append([]string(nil), spec.Aliases...), Models: models, DefaultModel: spec.DefaultModel, Capabilities: spec.Capabilities, AuthStatus: app.ProviderAuthStatus(spec), AuthEnv: append([]string(nil), spec.AuthEnv...), Conversion: conversionDTO(spec.Conversion)})
	}
	return out
}

func conversionDTO(spec app.ProviderConversionSpec) *gateway.ConversionDTO {
	if spec.RequestConverter == "" && spec.ResponseConverter == "" && spec.Call == "" && spec.Stream == "" && spec.Source == "" && len(spec.Operations) == 0 && len(spec.Routes) == 0 {
		return nil
	}
	return &gateway.ConversionDTO{
		RequestConverter:  spec.RequestConverter,
		ResponseConverter: spec.ResponseConverter,
		Call:              spec.Call,
		Stream:            spec.Stream,
		Source:            spec.Source,
		Operations:        append([]string(nil), spec.Operations...),
		Routes:            routeDTOs(spec.Routes),
	}
}

func routeDTOs(routes []app.ProviderRouteSpec) []gateway.RouteDTO {
	if len(routes) == 0 {
		return nil
	}
	out := make([]gateway.RouteDTO, 0, len(routes))
	for _, route := range routes {
		out = append(out, gateway.RouteDTO{Operation: route.Operation, Method: route.Method, Path: route.Path, Accept: route.Accept, Query: cloneStringMap(route.Query)})
	}
	return out
}

func defaultMCPDTOs() []gateway.ResourceDTO {
	servers := app.DefaultProviderOptions("").MCPServers
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]gateway.ResourceDTO, 0, len(names))
	enabled := true
	for _, name := range names {
		server := servers[name]
		config := map[string]any{
			"kind":    string(server.Kind),
			"name":    firstNonEmpty(server.Name, name),
			"tools":   append([]string{}, server.Tools...),
			"timeout": server.Timeout,
		}
		if server.Command != "" {
			config["command"] = server.Command
		}
		if len(server.Args) > 0 {
			config["args"] = append([]string{}, server.Args...)
		}
		if len(server.Env) > 0 {
			config["env"] = cloneStringMap(server.Env)
		}
		if server.Cwd != "" {
			config["cwd"] = server.Cwd
		}
		if server.URL != "" {
			config["url"] = server.URL
		}
		if len(server.Headers) > 0 {
			config["headers"] = cloneStringMap(server.Headers)
		}
		out = append(out, gateway.RedactResourceDTO(gateway.ResourceDTO{Kind: string(session.ResourceMCPServer), Name: firstNonEmpty(server.Name, name), Description: "kkode 기본 MCP server예요", Enabled: &enabled, Config: config}))
	}
	return out
}

func defaultMCPDiagnosticChecks() []gateway.DiagnosticCheckDTO {
	diagnostics := app.DefaultMCPDiagnostics("")
	out := make([]gateway.DiagnosticCheckDTO, 0, len(diagnostics))
	for _, item := range diagnostics {
		message := item.Message
		if item.Kind != "" {
			message = strings.TrimSpace(message + " kind=" + item.Kind)
		}
		out = append(out, gateway.DiagnosticCheckDTO{Name: "default_mcp." + item.Name, Status: defaultMCPDiagnosticStatus(item.Status), Message: message})
	}
	return out
}

func defaultMCPDiagnosticStatus(status string) string {
	if status == "missing" {
		return "warning"
	}
	return status
}

func cloneStringMap(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func loadProviderOptions(ctx context.Context, store session.Store, req gateway.RunStartRequest) (app.ProviderOptions, error) {
	opts := app.ProviderOptions{MCPServers: map[string]llm.MCPServer{}, ContextBlocks: requestContextBlocks(gateway.SanitizeContextBlocks(req.ContextBlocks))}
	resourceStore, _ := store.(session.ResourceStore)
	if resourceStore == nil {
		if len(opts.MCPServers) == 0 {
			opts.MCPServers = nil
		}
		return opts, nil
	}
	for _, id := range req.MCPServers {
		resource, err := resourceStore.LoadResource(ctx, session.ResourceMCPServer, id)
		if err != nil {
			return opts, err
		}
		if err := ensureResourceEnabled(resource); err != nil {
			return opts, err
		}
		server, err := mcpServerFromResource(resource)
		if err != nil {
			return opts, err
		}
		opts.MCPServers[firstNonEmpty(server.Name, resource.Name)] = server
	}
	for _, id := range req.Skills {
		resource, err := resourceStore.LoadResource(ctx, session.ResourceSkill, id)
		if err != nil {
			return opts, err
		}
		if err := ensureResourceEnabled(resource); err != nil {
			return opts, err
		}
		dir, err := skillDirectoryFromResource(resource)
		if err != nil {
			return opts, err
		}
		opts.SkillDirectories = append(opts.SkillDirectories, dir)
		block, err := skillContextBlockFromResource(resource, dir)
		if err != nil {
			return opts, err
		}
		if block != "" {
			opts.ContextBlocks = append(opts.ContextBlocks, block)
		}
	}
	for _, id := range req.Subagents {
		resource, err := resourceStore.LoadResource(ctx, session.ResourceSubagent, id)
		if err != nil {
			return opts, err
		}
		if err := ensureResourceEnabled(resource); err != nil {
			return opts, err
		}
		agent, err := agentFromResource(ctx, resourceStore, resource)
		if err != nil {
			return opts, err
		}
		opts.CustomAgents = append(opts.CustomAgents, agent)
		if block := subagentContextBlock(agent); block != "" {
			opts.ContextBlocks = append(opts.ContextBlocks, block)
		}
	}
	if len(opts.MCPServers) == 0 {
		opts.MCPServers = nil
	}
	return opts, nil
}

func requestContextBlocks(blocks []string) []string {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]string, 0, len(blocks))
	for _, block := range blocks {
		text := strings.TrimSpace(llm.RedactSecrets(block))
		if text == "" {
			continue
		}
		truncated := false
		const maxRequestContextBytes = 32 << 10
		if len(text) > maxRequestContextBytes {
			text = truncateUTF8Bytes(text, maxRequestContextBytes)
			truncated = true
		}
		parts := []string{"요청 추가 컨텍스트예요:", text}
		if truncated {
			parts = append(parts, "[요청 컨텍스트가 길어서 일부만 포함했어요]")
		}
		out = append(out, strings.Join(parts, "\n"))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func ensureResourceEnabled(resource session.Resource) error {
	if resource.Enabled {
		return nil
	}
	return fmt.Errorf("%s resource %q는 비활성화되어 있어서 run에 연결할 수 없어요", resource.Kind, resource.ID)
}

type mcpResourceConfig struct {
	Kind    string            `json:"kind"`
	Name    string            `json:"name"`
	Tools   []string          `json:"tools"`
	Timeout int               `json:"timeout"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Cwd     string            `json:"cwd"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

func mcpServerFromResource(resource session.Resource) (llm.MCPServer, error) {
	var cfg mcpResourceConfig
	if len(resource.Config) > 0 {
		if err := json.Unmarshal(resource.Config, &cfg); err != nil {
			return llm.MCPServer{}, err
		}
	}
	return mcpServerFromConfig(firstNonEmpty(cfg.Name, resource.Name), cfg)
}

func mcpServerFromConfig(defaultName string, cfg mcpResourceConfig) (llm.MCPServer, error) {
	kind := llm.MCPServerKind(cfg.Kind)
	if kind == "" {
		if cfg.URL != "" {
			kind = llm.MCPHTTP
		} else {
			kind = llm.MCPStdio
		}
	}
	server := llm.MCPServer{Kind: kind, Name: firstNonEmpty(cfg.Name, defaultName), Tools: cfg.Tools, Timeout: cfg.Timeout, Command: cfg.Command, Args: cfg.Args, Env: cfg.Env, Cwd: cfg.Cwd, URL: cfg.URL, Headers: cfg.Headers}
	if err := validateMCPServerConfig(server); err != nil {
		return llm.MCPServer{}, err
	}
	return server, nil
}

func validateMCPServerConfig(server llm.MCPServer) error {
	name := firstNonEmpty(server.Name, "unnamed")
	if server.Timeout < 0 {
		return fmt.Errorf("MCP server %q timeout은 0 이상이어야 해요", name)
	}
	switch server.Kind {
	case llm.MCPStdio:
		if strings.TrimSpace(server.Command) == "" {
			return fmt.Errorf("MCP server %q stdio config에는 command가 필요해요", name)
		}
	case llm.MCPHTTP:
		rawURL := strings.TrimSpace(server.URL)
		if rawURL == "" {
			return fmt.Errorf("MCP server %q http config에는 url이 필요해요", name)
		}
		if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
			return fmt.Errorf("MCP server %q url은 http/https여야 해요", name)
		}
	default:
		return fmt.Errorf("MCP server %q kind는 stdio 또는 http여야 해요", name)
	}
	return nil
}

type skillResourceConfig struct {
	Path      string `json:"path"`
	Directory string `json:"directory"`
}

func skillDirectoryFromResource(resource session.Resource) (string, error) {
	var cfg skillResourceConfig
	if len(resource.Config) > 0 {
		if err := json.Unmarshal(resource.Config, &cfg); err != nil {
			return "", err
		}
	}
	path := strings.TrimSpace(firstNonEmpty(cfg.Path, cfg.Directory))
	if path == "" {
		return "", fmt.Errorf("skill resource %q에는 path 또는 directory가 필요해요", resource.ID)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("skill resource %q 경로를 읽을 수 없어요: %w", resource.ID, err)
	}
	if !info.IsDir() {
		return filepath.Dir(path), nil
	}
	if firstExistingSkillFile(path) == "" {
		return "", fmt.Errorf("skill resource %q directory에는 SKILL.md 또는 README.md가 필요해요: %s", resource.ID, path)
	}
	return path, nil
}

func skillContextBlockFromResource(resource session.Resource, dir string) (string, error) {
	file := skillContextFileFromResource(resource, dir)
	if file == "" {
		return "", nil
	}
	const maxSkillContextBytes = 32 << 10
	text, truncated, err := readSkillContextText(file, maxSkillContextBytes)
	if err != nil {
		return "", err
	}
	text = strings.TrimSpace(llm.RedactSecrets(text))
	if text == "" {
		return "", nil
	}
	parts := []string{"선택된 Skill이에요: " + firstNonEmpty(resource.Name, resource.ID), "경로: " + dir, text}
	if truncated {
		parts = append(parts, "[skill 내용이 길어서 일부만 포함했어요]")
	}
	return strings.Join(parts, "\n\n"), nil
}

func readSkillContextText(file string, maxBytes int) (string, bool, error) {
	info, err := os.Stat(file)
	if err != nil {
		return "", false, err
	}
	f, err := os.Open(file)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)+int64(utf8.UTFMax)))
	if err != nil {
		return "", false, err
	}
	text := truncateUTF8Bytes(string(data), maxBytes)
	return text, info.Size() > int64(len(text)), nil
}

func skillContextFileFromResource(resource session.Resource, dir string) string {
	var cfg skillResourceConfig
	if len(resource.Config) > 0 {
		_ = json.Unmarshal(resource.Config, &cfg)
	}
	path := strings.TrimSpace(firstNonEmpty(cfg.Path, cfg.Directory))
	if path != "" {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return firstExistingSkillFile(dir)
}

func firstExistingSkillFile(dir string) string {
	for _, name := range []string{"SKILL.md", "README.md", "skill.md"} {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

type agentResourceConfig struct {
	DisplayName  string                     `json:"display_name"`
	Description  string                     `json:"description"`
	Prompt       string                     `json:"prompt"`
	Tools        []string                   `json:"tools"`
	MCPServers   map[string]json.RawMessage `json:"mcp_servers"`
	MCPServerIDs []string                   `json:"mcp_server_ids"`
	Skills       []string                   `json:"skills"`
	Infer        *bool                      `json:"infer"`
}

func agentFromResource(ctx context.Context, resourceStore session.ResourceStore, resource session.Resource) (llm.Agent, error) {
	var cfg agentResourceConfig
	if len(resource.Config) > 0 {
		if err := json.Unmarshal(resource.Config, &cfg); err != nil {
			return llm.Agent{}, err
		}
	}
	servers := map[string]llm.MCPServer{}
	for _, id := range cfg.MCPServerIDs {
		linked, err := resourceStore.LoadResource(ctx, session.ResourceMCPServer, id)
		if err != nil {
			return llm.Agent{}, err
		}
		if err := ensureResourceEnabled(linked); err != nil {
			return llm.Agent{}, err
		}
		server, err := mcpServerFromResource(linked)
		if err != nil {
			return llm.Agent{}, err
		}
		servers[firstNonEmpty(server.Name, linked.Name)] = server
	}
	for name, raw := range cfg.MCPServers {
		server, err := inlineMCPServerFromRaw(name, raw)
		if err != nil {
			return llm.Agent{}, err
		}
		servers[firstNonEmpty(server.Name, name)] = server
	}
	if len(servers) == 0 {
		servers = nil
	}
	return llm.Agent{Name: resource.ID, DisplayName: firstNonEmpty(cfg.DisplayName, resource.Name), Description: firstNonEmpty(cfg.Description, resource.Description), Prompt: cfg.Prompt, Tools: cfg.Tools, MCPServers: servers, Infer: cfg.Infer, Skills: cfg.Skills}, nil
}

func subagentContextBlock(agent llm.Agent) string {
	name := firstNonEmpty(agent.DisplayName, agent.Name)
	if strings.TrimSpace(name) == "" && strings.TrimSpace(agent.Prompt) == "" {
		return ""
	}
	parts := []string{"사용 가능한 Subagent예요: " + firstNonEmpty(name, agent.Name)}
	if agent.Description != "" {
		parts = append(parts, "설명: "+agent.Description)
	}
	if agent.Prompt != "" {
		parts = append(parts, "지침:\n"+agent.Prompt)
	}
	if len(agent.Tools) > 0 {
		parts = append(parts, "허용 tool: "+strings.Join(agent.Tools, ", "))
	}
	if len(agent.Skills) > 0 {
		parts = append(parts, "연결 skill: "+strings.Join(agent.Skills, ", "))
	}
	if len(agent.MCPServers) > 0 {
		names := make([]string, 0, len(agent.MCPServers))
		for serverName := range agent.MCPServers {
			names = append(names, serverName)
		}
		sort.Strings(names)
		parts = append(parts, "연결 MCP: "+strings.Join(names, ", "))
	}
	return strings.Join(parts, "\n")
}

func inlineMCPServerFromRaw(name string, raw json.RawMessage) (llm.MCPServer, error) {
	var command string
	if err := json.Unmarshal(raw, &command); err == nil {
		server := llm.MCPServer{Name: name, Kind: llm.MCPStdio, Command: command}
		if err := validateMCPServerConfig(server); err != nil {
			return llm.MCPServer{}, err
		}
		return server, nil
	}
	var cfg mcpResourceConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return llm.MCPServer{}, err
	}
	return mcpServerFromConfig(name, cfg)
}
