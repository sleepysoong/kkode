// Package httpjson은 provider 변환 결과를 일반 HTTP JSON API로 보내는 재사용 caller를 제공해요.
package httpjson

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/providers/internal/httptransport"
)

// RetryConfig는 provider HTTP 호출 retry/backoff 정책이에요.
type RetryConfig = httptransport.RetryConfig

// Route는 provider operation을 실제 HTTP endpoint로 바꾸는 설정이에요.
type Route struct {
	Method  string
	Path    string
	Accept  string
	Headers map[string]string
}

// Config는 HTTP JSON caller가 endpoint, 인증, route를 조립할 때 쓰는 설정이에요.
type Config struct {
	ProviderName     string
	BaseURL          string
	APIKey           string
	Headers          map[string]string
	HTTPClient       *http.Client
	Retry            RetryConfig
	DefaultOperation string
	Routes           map[string]Route
}

// Caller는 llm.ProviderRequest를 route 기반 HTTP JSON 요청으로 보내는 source 경계예요.
// 변환은 RequestConverter가 담당하고, Caller는 API 호출과 HTTP 오류 정규화만 담당해요.
type Caller struct {
	providerName     string
	baseURL          string
	apiKey           string
	headers          map[string]string
	httpClient       *http.Client
	retry            RetryConfig
	defaultOperation string
	routes           map[string]Route
}

// New는 route table 기반 HTTP JSON caller를 만들어요.
func New(cfg Config) *Caller {
	providerName := strings.TrimSpace(cfg.ProviderName)
	if providerName == "" {
		providerName = "http-json"
	}
	return &Caller{
		providerName:     providerName,
		baseURL:          strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:           cfg.APIKey,
		headers:          httptransport.CloneHeaders(cfg.Headers),
		httpClient:       httptransport.DefaultClient(cfg.HTTPClient),
		retry:            httptransport.NormalizeRetry(cfg.Retry),
		defaultOperation: strings.TrimSpace(cfg.DefaultOperation),
		routes:           cloneRoutes(cfg.Routes),
	}
}

// CallProvider는 변환된 ProviderRequest를 route에 맞춰 HTTP JSON API로 보내요.
func (c *Caller) CallProvider(ctx context.Context, req llm.ProviderRequest) (llm.ProviderResult, error) {
	if c == nil {
		return llm.ProviderResult{}, fmt.Errorf("httpjson caller가 필요해요")
	}
	operation := strings.TrimSpace(req.Operation)
	if operation == "" {
		operation = c.defaultOperation
	}
	route, err := c.route(operation)
	if err != nil {
		return llm.ProviderResult{}, err
	}
	endpoint, err := c.endpoint(route)
	if err != nil {
		return llm.ProviderResult{}, err
	}
	headers := httptransport.CloneHeaders(c.headers)
	for k, v := range route.Headers {
		headers[k] = v
	}
	for k, v := range req.Headers {
		headers[k] = v
	}
	method := strings.TrimSpace(route.Method)
	if method == "" {
		method = http.MethodPost
	}
	hreq, payload, err := httptransport.NewJSONRequest(ctx, method, endpoint, c.apiKey, headers, req.Body, route.Accept)
	if err != nil {
		return llm.ProviderResult{}, err
	}
	res, err := httptransport.DoWithRetry(c.httpClient, hreq, payload, c.retry)
	if err != nil {
		return llm.ProviderResult{}, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return llm.ProviderResult{}, err
	}
	if !httptransport.IsSuccessStatus(res.StatusCode) {
		return llm.ProviderResult{}, httptransport.ErrorFromResponse(c.providerName+" "+operation, res, data)
	}
	return llm.ProviderResult{Provider: c.providerName, Model: req.Model, Body: data, Headers: res.Header}, nil
}

func (c *Caller) route(operation string) (Route, error) {
	if operation == "" {
		return Route{}, fmt.Errorf("provider operation이 필요해요")
	}
	if route, ok := c.routes[operation]; ok {
		return route, nil
	}
	return Route{}, fmt.Errorf("지원하지 않는 provider operation이에요: %s", operation)
}

func (c *Caller) endpoint(route Route) (string, error) {
	path := strings.TrimSpace(route.Path)
	if path == "" {
		return "", fmt.Errorf("httpjson route path가 필요해요")
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path, nil
	}
	if c.baseURL == "" {
		return "", fmt.Errorf("httpjson base URL이 필요해요")
	}
	return c.baseURL + "/" + strings.TrimLeft(path, "/"), nil
}

func cloneRoutes(routes map[string]Route) map[string]Route {
	out := make(map[string]Route, len(routes))
	for operation, route := range routes {
		route.Headers = httptransport.CloneHeaders(route.Headers)
		out[operation] = route
	}
	return out
}
