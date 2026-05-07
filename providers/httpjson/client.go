// Package httpjson은 provider 변환 결과를 일반 HTTP JSON API로 보내는 재사용 caller를 제공해요.
package httpjson

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/providers/internal/httptransport"
)

// RetryConfig는 provider HTTP 호출 retry/backoff 정책이에요.
type RetryConfig = httptransport.RetryConfig

// Route는 provider operation을 실제 HTTP endpoint로 바꾸는 설정이에요.
// Path와 Query 값에는 {model}, {operation}, {metadata.key}, {key} template를 쓸 수 있어요.
type Route struct {
	Method  string
	Path    string
	Accept  string
	Headers map[string]string
	Query   map[string]string
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
	hreq, payload, err := c.newRequest(ctx, route, req, req.Body)
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

// StreamProvider는 변환된 ProviderRequest를 SSE HTTP JSON API로 보내고 raw SSE frame을 StreamEvent로 돌려줘요.
// provider별 semantic delta parsing은 전용 ProviderStreamCaller가 맡고, 이 caller는 범용 raw stream 연결에 집중해요.
func (c *Caller) StreamProvider(ctx context.Context, req llm.ProviderRequest) (llm.EventStream, error) {
	if c == nil {
		return nil, fmt.Errorf("httpjson caller가 필요해요")
	}
	operation := strings.TrimSpace(req.Operation)
	if operation == "" {
		operation = c.defaultOperation
	}
	route, err := c.route(operation)
	if err != nil {
		return nil, err
	}
	if route.Accept == "" {
		route.Accept = "text/event-stream"
	}
	body := streamBody(req.Body)
	hreq, payload, err := c.newRequest(ctx, route, req, body)
	if err != nil {
		return nil, err
	}
	res, err := httptransport.DoWithRetry(c.httpClient, hreq, payload, c.retry)
	if err != nil {
		return nil, err
	}
	if !httptransport.IsSuccessStatus(res.StatusCode) {
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		return nil, httptransport.ErrorFromResponse(c.providerName+" "+operation+" stream", res, data)
	}
	events := make(chan llm.StreamEvent, 32)
	go c.readSSE(ctx, operation, res.Body, events)
	return llm.NewChannelStream(ctx, events, res.Body), nil
}

func (c *Caller) newRequest(ctx context.Context, route Route, req llm.ProviderRequest, body any) (*http.Request, []byte, error) {
	endpoint, err := c.endpoint(route, req)
	if err != nil {
		return nil, nil, err
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
	return httptransport.NewJSONRequest(ctx, method, endpoint, c.apiKey, headers, body, route.Accept)
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

func (c *Caller) endpoint(route Route, req llm.ProviderRequest) (string, error) {
	path := strings.TrimSpace(route.Path)
	if path == "" {
		return "", fmt.Errorf("httpjson route path가 필요해요")
	}
	expandedPath, err := expandRouteTemplate(path, req, true)
	if err != nil {
		return "", err
	}
	endpoint := expandedPath
	if !strings.HasPrefix(expandedPath, "http://") && !strings.HasPrefix(expandedPath, "https://") {
		if c.baseURL == "" {
			return "", fmt.Errorf("httpjson base URL이 필요해요")
		}
		endpoint = c.baseURL + "/" + strings.TrimLeft(expandedPath, "/")
	}
	if len(route.Query) == 0 {
		return endpoint, nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("httpjson endpoint URL이 올바르지 않아요: %w", err)
	}
	query := parsed.Query()
	for key, valueTemplate := range route.Query {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		value, err := expandRouteTemplate(valueTemplate, req, false)
		if err != nil {
			return "", err
		}
		query.Set(key, value)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (c *Caller) readSSE(ctx context.Context, operation string, r io.Reader, out chan<- llm.StreamEvent) {
	defer close(out)
	err := httptransport.ReadSSE(ctx, r, func(eventName string, data []byte) bool {
		select {
		case <-ctx.Done():
			return false
		case out <- llm.StreamEvent{Type: llm.StreamEventUnknown, Provider: c.providerName, EventName: eventName, Raw: append([]byte(nil), data...)}:
			return true
		}
	})
	if err != nil {
		out <- llm.StreamEvent{Type: llm.StreamEventError, Provider: c.providerName, EventName: operation, Error: err}
		return
	}
	if ctx.Err() == nil {
		out <- llm.StreamEvent{Type: llm.StreamEventCompleted, Provider: c.providerName, EventName: operation}
	}
}

func streamBody(body any) any {
	m, ok := body.(map[string]any)
	if !ok {
		return body
	}
	out := make(map[string]any, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	if _, exists := out["stream"]; !exists {
		out["stream"] = true
	}
	return out
}

func cloneRoutes(routes map[string]Route) map[string]Route {
	out := make(map[string]Route, len(routes))
	for operation, route := range routes {
		route.Headers = httptransport.CloneHeaders(route.Headers)
		route.Query = cloneQuery(route.Query)
		out[operation] = route
	}
	return out
}

func expandRouteTemplate(template string, req llm.ProviderRequest, escapePath bool) (string, error) {
	var out strings.Builder
	for i := 0; i < len(template); {
		start := strings.IndexByte(template[i:], '{')
		if start < 0 {
			out.WriteString(template[i:])
			break
		}
		start += i
		out.WriteString(template[i:start])
		end := strings.IndexByte(template[start+1:], '}')
		if end < 0 {
			return "", fmt.Errorf("httpjson route template가 닫히지 않았어요: %s", template)
		}
		end += start + 1
		name := strings.TrimSpace(template[start+1 : end])
		value, ok := routeTemplateValue(name, req)
		if !ok {
			return "", fmt.Errorf("httpjson route template 값이 필요해요: %s", name)
		}
		if escapePath {
			value = url.PathEscape(value)
		}
		out.WriteString(value)
		i = end + 1
	}
	return out.String(), nil
}

func routeTemplateValue(name string, req llm.ProviderRequest) (string, bool) {
	switch name {
	case "model":
		return req.Model, req.Model != ""
	case "operation":
		return req.Operation, req.Operation != ""
	}
	name = strings.TrimPrefix(name, "metadata.")
	if req.Metadata == nil {
		return "", false
	}
	value, ok := req.Metadata[name]
	return value, ok
}

func cloneQuery(query map[string]string) map[string]string {
	if len(query) == 0 {
		return nil
	}
	out := make(map[string]string, len(query))
	for key, value := range query {
		out[key] = value
	}
	return out
}
