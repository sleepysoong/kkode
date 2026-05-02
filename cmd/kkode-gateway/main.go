package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sleepysoong/kkode/app"
	"github.com/sleepysoong/kkode/gateway"
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
	runManager := gateway.NewAsyncRunManager(syncRunStarter(store, runOptions{MaxIterations: *maxIterations, NoWeb: *noWeb, WebMaxBytes: *webMaxBytes}))
	srv, err := gateway.New(gateway.Config{
		Store:                store,
		Version:              *version,
		APIKey:               *apiKey,
		AllowLocalhostNoAuth: *allowLocalhostNoAuth,
		RunStarter:           runManager.Start,
		RunGetter:            runManager.Get,
		RunLister:            runManager.List,
		RunCanceler:          runManager.Cancel,
		Providers:            providerDTOs(),
		ResourceStore:        store,
	})
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Fprintf(os.Stderr, "kkode gateway가 http://%s 에서 실행돼요\n", *addr)
	return httpServer.ListenAndServe()
}

type runOptions struct {
	MaxIterations int
	NoWeb         bool
	WebMaxBytes   int64
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
		providerHandle, err := app.BuildProvider(providerName, absRoot)
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
		run := &gateway.RunDTO{ID: runID, SessionID: req.SessionID, Prompt: req.Prompt, Status: "completed", StartedAt: started, EndedAt: time.Now().UTC(), Metadata: req.Metadata}
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
		out = append(out, gateway.ProviderDTO{Name: spec.Name, Models: []string{spec.DefaultModel}, Capabilities: spec.Capabilities, AuthStatus: app.ProviderAuthStatus(spec)})
	}
	return out
}
