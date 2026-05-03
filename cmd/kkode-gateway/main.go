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
	"strings"
	"sync"
	"syscall"
	"time"

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
	store, err := session.OpenSQLite(*statePath)
	if err != nil {
		return err
	}
	defer store.Close()
	runManager := gateway.NewAsyncRunManagerWithStore(syncRunStarter(store, runOptions{MaxIterations: *maxIterations, NoWeb: *noWeb, WebMaxBytes: *webMaxBytes}), store)
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
		AccessLogger:         accessLoggerForFlag(*accessLog, os.Stderr),
		RunStarter:           runManager.Start,
		RunGetter:            runManager.Get,
		RunLister:            runManager.List,
		RunCanceler:          runManager.Cancel,
		RunEventLister:       runManager.Events,
		RunSubscriber:        runManager.Subscribe,
		Providers:            providerDTOs(),
		DefaultMCPServers:    defaultMCPDTOs(),
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
		ag, err := app.NewAgent(providerHandle.Provider, ws, app.AgentOptions{Model: model, MaxIterations: opts.MaxIterations, NoWeb: opts.NoWeb, WebMaxBytes: opts.WebMaxBytes})
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
			run.EventsURL = "/api/v1/sessions/" + result.Session.ID + "/events"
		}
		if run.EventsURL == "" {
			run.EventsURL = "/api/v1/sessions/" + req.SessionID + "/events"
		}
		if runErr != nil {
			run.Status = "failed"
			run.Error = runErr.Error()
		}
		return run, nil
	}
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
		out = append(out, gateway.ProviderDTO{Name: spec.Name, Models: models, DefaultModel: spec.DefaultModel, Capabilities: spec.Capabilities, AuthStatus: app.ProviderAuthStatus(spec)})
	}
	return out
}

func defaultMCPDTOs() []gateway.ResourceDTO {
	servers := app.DefaultMCPServers("")
	out := make([]gateway.ResourceDTO, 0, len(servers))
	enabled := true
	for name, server := range servers {
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
		out = append(out, gateway.ResourceDTO{Kind: string(session.ResourceMCPServer), Name: firstNonEmpty(server.Name, name), Description: "kkode 기본 MCP server예요", Enabled: &enabled, Config: config})
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
		if dir := skillDirectoryFromResource(resource); dir != "" {
			opts.SkillDirectories = append(opts.SkillDirectories, dir)
		}
	}
	for _, id := range req.Subagents {
		resource, err := resourceStore.LoadResource(ctx, session.ResourceSubagent, id)
		if err != nil {
			return opts, err
		}
		agent, err := agentFromResource(resource)
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
	kind := llm.MCPServerKind(cfg.Kind)
	if kind == "" {
		if cfg.URL != "" {
			kind = llm.MCPHTTP
		} else {
			kind = llm.MCPStdio
		}
	}
	return llm.MCPServer{Kind: kind, Name: firstNonEmpty(cfg.Name, resource.Name), Tools: cfg.Tools, Timeout: cfg.Timeout, Command: cfg.Command, Args: cfg.Args, Env: cfg.Env, Cwd: cfg.Cwd, URL: cfg.URL, Headers: cfg.Headers}, nil
}

type skillResourceConfig struct {
	Path      string `json:"path"`
	Directory string `json:"directory"`
}

func skillDirectoryFromResource(resource session.Resource) string {
	var cfg skillResourceConfig
	_ = json.Unmarshal(resource.Config, &cfg)
	return firstNonEmpty(cfg.Path, cfg.Directory)
}

type agentResourceConfig struct {
	DisplayName string            `json:"display_name"`
	Description string            `json:"description"`
	Prompt      string            `json:"prompt"`
	Tools       []string          `json:"tools"`
	MCPServers  map[string]string `json:"mcp_servers"`
	Skills      []string          `json:"skills"`
	Infer       *bool             `json:"infer"`
}

func agentFromResource(resource session.Resource) (llm.Agent, error) {
	var cfg agentResourceConfig
	if len(resource.Config) > 0 {
		if err := json.Unmarshal(resource.Config, &cfg); err != nil {
			return llm.Agent{}, err
		}
	}
	servers := map[string]llm.MCPServer{}
	for name, value := range cfg.MCPServers {
		servers[name] = llm.MCPServer{Name: name, Kind: llm.MCPStdio, Command: value}
	}
	if len(servers) == 0 {
		servers = nil
	}
	return llm.Agent{Name: resource.ID, DisplayName: firstNonEmpty(cfg.DisplayName, resource.Name), Description: firstNonEmpty(cfg.Description, resource.Description), Prompt: cfg.Prompt, Tools: cfg.Tools, MCPServers: servers, Infer: cfg.Infer, Skills: cfg.Skills}, nil
}
