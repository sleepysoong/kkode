package omniroute

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/providers/openai"
)

const (
	DefaultBaseURL = "http://localhost:20128/v1"
)

type Config struct {
	// BaseURL은 보통 /v1로 끝나야해요. 예: http://localhost:20128/v1.
	BaseURL string
	// AdminBaseURL은 dashboard/API root를 가리켜요. 예: http://localhost:20128.
	// 비어 있으면 BaseURL에서 /v1을 제거해서 계산해요.
	AdminBaseURL string
	APIKey       string
	HTTPClient   *http.Client
	Headers      map[string]string

	// OmniRoute routing/cache/session header 설정이에요.
	SessionID      string
	NoCache        bool
	Progress       bool
	IdempotencyKey string
	RequestID      string

	Retry openai.RetryConfig
}

type Client struct {
	cfg          Config
	openai       *openai.Client
	httpClient   *http.Client
	headers      map[string]string
	baseURL      string
	adminBaseURL string
}

func New(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.AdminBaseURL == "" {
		cfg.AdminBaseURL = deriveAdminBaseURL(cfg.BaseURL)
	}
	cfg.AdminBaseURL = strings.TrimRight(cfg.AdminBaseURL, "/")
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 120 * time.Second}
	}
	headers := map[string]string{}
	for k, v := range cfg.Headers {
		headers[k] = v
	}
	if cfg.SessionID != "" {
		headers["X-Session-Id"] = cfg.SessionID
	}
	if cfg.NoCache {
		headers["X-OmniRoute-No-Cache"] = "true"
	}
	if cfg.Progress {
		headers["X-OmniRoute-Progress"] = "true"
	}
	if cfg.IdempotencyKey != "" {
		headers["Idempotency-Key"] = cfg.IdempotencyKey
	}
	if cfg.RequestID != "" {
		headers["X-Request-Id"] = cfg.RequestID
	}
	return &Client{
		cfg:          cfg,
		openai:       openai.New(openai.Config{BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, HTTPClient: hc, Headers: headers, Retry: cfg.Retry}),
		httpClient:   hc,
		headers:      headers,
		baseURL:      cfg.BaseURL,
		adminBaseURL: cfg.AdminBaseURL,
	}
}

func (c *Client) Name() string { return "omniroute" }

func (c *Client) Capabilities() llm.Capabilities {
	caps := c.openai.Capabilities()
	caps.PromptRefs = true
	caps.PreviousResponseID = true
	caps.MCP = true
	return caps
}

func (c *Client) Generate(ctx context.Context, req llm.Request) (*llm.Response, error) {
	resp, err := c.openai.Generate(ctx, req)
	if resp != nil {
		resp.Provider = c.Name()
	}
	return resp, err
}

func (c *Client) Stream(ctx context.Context, req llm.Request) (llm.EventStream, error) {
	return c.openai.Stream(ctx, req)
}

type ModelList struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}
type Model struct {
	ID       string `json:"id"`
	Object   string `json:"object,omitempty"`
	OwnedBy  string `json:"owned_by,omitempty"`
	Provider string `json:"provider,omitempty"`
	Type     string `json:"type,omitempty"`
}

func (c *Client) ListModels(ctx context.Context) (*ModelList, error) {
	var out ModelList
	if err := c.doJSON(ctx, http.MethodGet, c.baseURL+"/models", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type Health struct {
	Status          string         `json:"status"`
	CatalogCount    int            `json:"catalogCount,omitempty"`
	ConfiguredCount int            `json:"configuredCount,omitempty"`
	ActiveCount     int            `json:"activeCount,omitempty"`
	MonitoredCount  int            `json:"monitoredCount,omitempty"`
	Raw             map[string]any `json:"-"`
}

func (c *Client) Health(ctx context.Context) (*Health, error) {
	var raw map[string]any
	if err := c.doJSON(ctx, http.MethodGet, c.adminBaseURL+"/api/monitoring/health", nil, &raw); err != nil {
		return nil, err
	}
	h := &Health{Raw: raw}
	if v, _ := raw["status"].(string); v != "" {
		h.Status = v
	}
	h.CatalogCount = intFrom(raw["catalogCount"])
	h.ConfiguredCount = intFrom(raw["configuredCount"])
	h.ActiveCount = intFrom(raw["activeCount"])
	h.MonitoredCount = intFrom(raw["monitoredCount"])
	return h, nil
}

type A2ARequest struct {
	Skill    string
	Messages []llm.Message
	Metadata map[string]any
}

type A2AResponse struct {
	TaskID   string
	State    string
	Text     string
	Metadata map[string]any
	Raw      json.RawMessage
}

func (c *Client) A2ASend(ctx context.Context, req A2ARequest) (*A2AResponse, error) {
	payload := map[string]any{"jsonrpc": "2.0", "id": "1", "method": "message/send", "params": map[string]any{"skill": firstNonEmpty(req.Skill, "smart-routing"), "messages": req.Messages, "metadata": req.Metadata}}
	var env struct {
		Result struct {
			Task struct {
				ID    string `json:"id"`
				State string `json:"state"`
			} `json:"task"`
			Artifacts []struct {
				Type    string `json:"type"`
				Content string `json:"content"`
			} `json:"artifacts"`
			Metadata map[string]any `json:"metadata"`
		} `json:"result"`
		Error any `json:"error"`
	}
	raw, err := c.doRaw(ctx, http.MethodPost, c.adminBaseURL+"/a2a", payload)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	if env.Error != nil {
		return nil, fmt.Errorf("omniroute a2a error: %v", env.Error)
	}
	var text strings.Builder
	for _, artifact := range env.Result.Artifacts {
		if artifact.Content != "" {
			if text.Len() > 0 {
				text.WriteString("\n")
			}
			text.WriteString(artifact.Content)
		}
	}
	return &A2AResponse{TaskID: env.Result.Task.ID, State: env.Result.Task.State, Text: text.String(), Metadata: env.Result.Metadata, Raw: raw}, nil
}

func (c *Client) doJSON(ctx context.Context, method, url string, body any, out any) error {
	raw, err := c.doRaw(ctx, method, url, body)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func (c *Client) doRaw(ctx context.Context, method, endpoint string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, rdr)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("omniroute %s returned %s: %s", endpoint, res.Status, string(data))
	}
	return data, nil
}

func deriveAdminBaseURL(base string) string {
	if strings.HasSuffix(base, "/v1") {
		return strings.TrimSuffix(base, "/v1")
	}
	if u, err := url.Parse(base); err == nil {
		u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/v1")
		return strings.TrimRight(u.String(), "/")
	}
	return strings.TrimSuffix(base, "/v1")
}

func intFrom(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}
func firstNonEmpty(v, fallback string) string {
	if v != "" {
		return v
	}
	return fallback
}
