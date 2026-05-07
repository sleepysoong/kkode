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
	maxConcurrentRuns := fs.Int("max-concurrent-runs", app.EnvInt("KKODE_MAX_CONCURRENT_RUNS", 4), "동시에 running 상태로 실행할 background run 최대 개수예요. 0 이하면 제한하지 않아요")
	runTimeout := fs.Duration("run-timeout", envDuration("KKODE_RUN_TIMEOUT", 0), "background run 실행 timeout이에요. 0이면 제한하지 않아요")
	readHeaderTimeout := fs.Duration("read-header-timeout", envDuration("KKODE_READ_HEADER_TIMEOUT", 10*time.Second), "HTTP read header timeout이에요")
	readTimeout := fs.Duration("read-timeout", envDuration("KKODE_READ_TIMEOUT", 0), "HTTP read timeout이에요. 0이면 비활성화해요")
	writeTimeout := fs.Duration("write-timeout", envDuration("KKODE_WRITE_TIMEOUT", 0), "HTTP write timeout이에요. SSE를 오래 유지하려면 0을 권장해요")
	idleTimeout := fs.Duration("idle-timeout", envDuration("KKODE_IDLE_TIMEOUT", 120*time.Second), "HTTP idle timeout이에요")
	shutdownTimeout := fs.Duration("shutdown-timeout", envDuration("KKODE_SHUTDOWN_TIMEOUT", 10*time.Second), "graceful shutdown timeout이에요")
	version := fs.String("version", app.EnvDefault("KKODE_VERSION", "dev"), "version endpoint에 표시할 버전이에요")
	maxIterations := fs.Int("max-iterations", app.EnvInt("KKODE_MAX_ITERATIONS", 8), "gateway run tool loop 최대 반복 횟수예요")
	noWeb := fs.Bool("no-web", app.EnvBool("KKODE_NO_WEB"), "gateway run에서 web_fetch tool을 비활성화해요")
	webMaxBytes := fs.Int64("web-max-bytes", app.EnvInt64("KKODE_WEB_MAX_BYTES", 1<<20), "gateway run web_fetch 최대 byte 수예요")
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
	runManager := gateway.NewAsyncRunManagerWithStore(syncRunStarter(store, runOptions{MaxIterations: *maxIterations, NoWeb: *noWeb, WebMaxBytes: *webMaxBytes}), store).SetMaxConcurrentRuns(*maxConcurrentRuns).SetRunTimeout(*runTimeout)
	if err := runManager.RecoverStaleRuns(context.Background()); err != nil {
		return err
	}
	srv, err := gateway.New(gateway.Config{
		Store:                store,
		Version:              *version,
		APIKey:               *apiKey,
		AllowLocalhostNoAuth: *allowLocalhostNoAuth,
		CORSOrigins:          splitCSV(*corsOrigins),
		MaxRequestBytes:      *maxBodyBytes,
		MaxConcurrentRuns:    runManager.MaxConcurrentRuns(),
		RunTimeout:           runManager.RunTimeout(),
		AccessLogger:         accessLoggerForFlag(*accessLog, os.Stderr),
		RunStarter:           runManager.Start,
		RunPreviewer:         syncRunPreviewer(store, runOptions{MaxIterations: *maxIterations, NoWeb: *noWeb, WebMaxBytes: *webMaxBytes}),
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
		providerHandle, err := app.BuildProviderWithOptions(providerName, absRoot, providerOptions)
		if err != nil {
			return nil, err
		}
		if model == "" {
			model = app.DefaultModel(providerHandle.Provider.Name())
		}
		if providerHandle.Close != nil {
			defer providerHandle.Close()
		}
		ag, err := app.NewAgent(providerHandle.Provider, ws, app.AgentOptions{Model: model, BaseRequest: providerHandle.BaseRequest, MaxIterations: opts.MaxIterations, NoWeb: opts.NoWeb, WebMaxBytes: opts.WebMaxBytes, Observer: runEventTraceObserver()})
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
		run := &gateway.RunDTO{ID: runID, SessionID: req.SessionID, Prompt: req.Prompt, Provider: providerName, Model: model, MCPServers: cloneStringSlice(req.MCPServers), Skills: cloneStringSlice(req.Skills), Subagents: cloneStringSlice(req.Subagents), Status: "completed", StartedAt: started, EndedAt: time.Now().UTC(), Metadata: req.Metadata}
		if result != nil {
			run.TurnID = result.Turn.ID
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
			handle, err := app.BuildProviderWithOptions(providerName, absRoot, providerOptions)
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
		handle, err := app.BuildProviderWithOptions(providerName, sess.ProjectRoot, providerOptions)
		if err != nil {
			return nil, err
		}
		if handle.Close != nil {
			defer handle.Close()
		}
		if model == "" && handle.Provider != nil {
			model = app.DefaultModel(handle.Provider.Name())
		}
		ws, _, err := app.NewWorkspace(app.WorkspaceOptions{Root: sess.ProjectRoot})
		if err != nil {
			return nil, err
		}
		ag, err := app.NewAgent(handle.Provider, ws, app.AgentOptions{Model: model, BaseRequest: handle.BaseRequest, MaxIterations: opts.MaxIterations, NoWeb: opts.NoWeb, WebMaxBytes: opts.WebMaxBytes})
		if err != nil {
			return nil, err
		}
		providerReq, _ := ag.Prepare(req.Prompt)
		providerPreview, err := app.PreviewProviderRequest(ctx, providerName, providerReq, req.PreviewStream, 64<<10)
		if err != nil {
			return nil, err
		}
		return &gateway.RunPreviewResponse{
			SessionID:         req.SessionID,
			ProjectRoot:       sess.ProjectRoot,
			Provider:          providerName,
			Model:             model,
			MCPServers:        resourceDTOsForIDs(ctx, store, session.ResourceMCPServer, req.MCPServers),
			Skills:            resourceDTOsForIDs(ctx, store, session.ResourceSkill, req.Skills),
			Subagents:         resourceDTOsForIDs(ctx, store, session.ResourceSubagent, req.Subagents),
			DefaultMCPServers: defaultMCPDTOs(),
			BaseRequestTools:  toolNames(handle.BaseRequest.Tools),
			ProviderRequest:   toProviderRequestPreviewDTO(providerPreview),
		}, nil
	}
}

func syncProviderTester() gateway.ProviderTester {
	return func(ctx context.Context, providerName string, req gateway.ProviderTestRequest) (*gateway.ProviderTestResponse, error) {
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
			out.Message = err.Error()
			return out, nil
		}
		out.Message = "provider 변환 preflight가 성공했어요"
		if !req.Live {
			return out, nil
		}

		liveCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		handle, err := app.BuildProviderWithOptions(spec.Name, ".", app.ProviderOptions{})
		if err != nil {
			out.OK = false
			out.Message = err.Error()
			return out, nil
		}
		if handle.Close != nil {
			defer handle.Close()
		}
		if handle.Provider == nil {
			out.OK = false
			out.Message = "provider factory가 nil provider를 반환했어요"
			return out, nil
		}
		if req.Stream {
			result, err := smokeStreamProvider(liveCtx, handle.Provider, providerReq)
			if err != nil {
				out.OK = false
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
			out.Message = err.Error()
			return out, nil
		}
		out.Result = providerTestResult(resp)
		out.Message = "provider live test가 성공했어요"
		return out, nil
	}
}

func smokeStreamProvider(ctx context.Context, provider llm.Provider, req llm.Request) (*gateway.ProviderTestResultDTO, error) {
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
	var text strings.Builder
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
			result = providerTestResult(event.Response)
			if text.Len() > 0 && result.Text == "" {
				result.Text = text.String()
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
				result.Text = text.String()
			}
			return result, nil
		}
	}
	if result.Text == "" {
		result.Text = text.String()
	}
	return result, fmt.Errorf("provider stream이 완료 event를 보내지 않았어요")
}

func providerTestResult(resp *llm.Response) *gateway.ProviderTestResultDTO {
	if resp == nil {
		return nil
	}
	result := &gateway.ProviderTestResultDTO{
		ID:     resp.ID,
		Status: resp.Status,
		Text:   llm.RedactSecrets(resp.Text),
	}
	if resp.Usage != (llm.Usage{}) {
		result.Usage = &gateway.UsageDTO{InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens, TotalTokens: resp.Usage.TotalTokens, ReasoningTokens: resp.Usage.ReasoningTokens}
	}
	return result
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

func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
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
		out = append(out, gateway.RouteDTO{Operation: route.Operation, Method: route.Method, Path: route.Path, Accept: route.Accept})
	}
	return out
}

func defaultMCPDTOs() []gateway.ResourceDTO {
	servers := app.DefaultMCPServers("")
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
		out = append(out, gateway.DiagnosticCheckDTO{Name: "default_mcp." + item.Name, Status: item.Status, Message: message})
	}
	return out
}

func cloneStringMap(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func loadProviderOptions(ctx context.Context, store session.Store, req gateway.RunStartRequest) (app.ProviderOptions, error) {
	resourceStore, _ := store.(session.ResourceStore)
	if resourceStore == nil {
		return app.ProviderOptions{}, nil
	}
	opts := app.ProviderOptions{MCPServers: map[string]llm.MCPServer{}}
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
	}
	if len(opts.MCPServers) == 0 {
		opts.MCPServers = nil
	}
	return opts, nil
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
	return mcpServerFromConfig(firstNonEmpty(cfg.Name, resource.Name), cfg), nil
}

func mcpServerFromConfig(defaultName string, cfg mcpResourceConfig) llm.MCPServer {
	kind := llm.MCPServerKind(cfg.Kind)
	if kind == "" {
		if cfg.URL != "" {
			kind = llm.MCPHTTP
		} else {
			kind = llm.MCPStdio
		}
	}
	return llm.MCPServer{Kind: kind, Name: firstNonEmpty(cfg.Name, defaultName), Tools: cfg.Tools, Timeout: cfg.Timeout, Command: cfg.Command, Args: cfg.Args, Env: cfg.Env, Cwd: cfg.Cwd, URL: cfg.URL, Headers: cfg.Headers}
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

func inlineMCPServerFromRaw(name string, raw json.RawMessage) (llm.MCPServer, error) {
	var command string
	if err := json.Unmarshal(raw, &command); err == nil {
		return llm.MCPServer{Name: name, Kind: llm.MCPStdio, Command: command}, nil
	}
	var cfg mcpResourceConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return llm.MCPServer{}, err
	}
	return mcpServerFromConfig(name, cfg), nil
}
