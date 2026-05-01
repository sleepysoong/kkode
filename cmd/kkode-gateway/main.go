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

	"github.com/sleepysoong/kkode/gateway"
	"github.com/sleepysoong/kkode/session"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "오류가 났어요:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	_ = ctx
	fs := flag.NewFlagSet("kkode-gateway", flag.ContinueOnError)
	addr := fs.String("addr", envDefault("KKODE_GATEWAY_ADDR", "127.0.0.1:41234"), "gateway가 listen할 주소예요")
	statePath := fs.String("state", envDefault("KKODE_STATE_DB", ".kkode/state.db"), "SQLite session state DB 경로예요")
	apiKey := fs.String("api-key", os.Getenv("KKODE_API_KEY"), "API bearer token이에요")
	apiKeyEnv := fs.String("api-key-env", "", "API bearer token을 읽을 환경변수 이름이에요")
	allowLocalhostNoAuth := fs.Bool("no-auth-localhost", envBool("KKODE_NO_AUTH_LOCALHOST", true), "localhost 요청은 API key 없이 허용해요")
	version := fs.String("version", envDefault("KKODE_VERSION", "dev"), "version endpoint에 표시할 버전이에요")
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
	srv, err := gateway.New(gateway.Config{
		Store:                store,
		Version:              *version,
		APIKey:               *apiKey,
		AllowLocalhostNoAuth: *allowLocalhostNoAuth,
		Providers: []gateway.ProviderDTO{
			{Name: "openai", AuthStatus: envAuthStatus("OPENAI_API_KEY")},
			{Name: "omniroute", AuthStatus: envAuthStatus("OMNIROUTE_API_KEY")},
			{Name: "copilot", AuthStatus: envAuthStatus("COPILOT_GITHUB_TOKEN")},
			{Name: "codex", AuthStatus: "local"},
		},
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

func envAuthStatus(keys ...string) string {
	for _, key := range keys {
		if os.Getenv(key) != "" {
			return "configured"
		}
	}
	return "missing"
}

func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes" || value == "y" || value == "on"
}
