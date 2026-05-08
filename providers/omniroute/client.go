package omniroute

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/sleepysoong/kkode/llm"
	"github.com/sleepysoong/kkode/providers/internal/httptransport"
	"github.com/sleepysoong/kkode/providers/openai"
)

const (
	DefaultBaseURL  = "http://localhost:20128/v1"
	MaxA2ATextBytes = 8 << 20
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
	hc := httptransport.DefaultClient(cfg.HTTPClient)
	headers := httptransport.CloneHeaders(cfg.Headers)
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
		openai:       openai.New(openai.Config{BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, HTTPClient: hc, Headers: headers, Retry: cfg.Retry, ProviderName: "omniroute"}),
		httpClient:   hc,
		headers:      headers,
		baseURL:      cfg.BaseURL,
		adminBaseURL: cfg.AdminBaseURL,
	}
}

func (c *Client) Name() string { return "omniroute" }

func (c *Client) Capabilities() llm.Capabilities { return DefaultCapabilities() }

// DefaultCapabilities는 OmniRoute의 OpenAI-compatible + routing 확장 기능 계약이에요.
func DefaultCapabilities() llm.Capabilities {
	caps := openai.DefaultCapabilities()
	caps.MCP = true
	caps.A2A = true
	caps.Routing = true
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
			Artifacts []a2aArtifact  `json:"artifacts"`
			Metadata  map[string]any `json:"metadata"`
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
	return &A2AResponse{TaskID: env.Result.Task.ID, State: env.Result.Task.State, Text: joinA2AArtifactText(env.Result.Artifacts, MaxA2ATextBytes), Metadata: env.Result.Metadata, Raw: raw}, nil
}

type a2aArtifact struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

func joinA2AArtifactText(artifacts []a2aArtifact, maxBytes int) string {
	text := newLimitedA2ATextBuffer(maxBytes)
	for _, artifact := range artifacts {
		if artifact.Content == "" {
			continue
		}
		if text.Len() > 0 {
			text.WriteString("\n")
		}
		text.WriteString(artifact.Content)
	}
	return text.String()
}

type limitedA2ATextBuffer struct {
	buf       strings.Builder
	max       int
	truncated bool
}

func newLimitedA2ATextBuffer(max int) *limitedA2ATextBuffer {
	return &limitedA2ATextBuffer{max: max}
}

func (b *limitedA2ATextBuffer) Len() int {
	return b.buf.Len()
}

func (b *limitedA2ATextBuffer) WriteString(text string) {
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

func (b *limitedA2ATextBuffer) String() string {
	text := b.buf.String()
	if b.truncated {
		return strings.TrimRight(text, "\n") + "\n[output truncated]"
	}
	return text
}

func truncateUTF8Bytes(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	if maxBytes <= 0 {
		return ""
	}
	text = text[:maxBytes]
	for len(text) > 0 && !utf8.ValidString(text) {
		_, size := utf8.DecodeLastRuneInString(text)
		if size == 0 {
			return ""
		}
		text = text[:len(text)-size]
	}
	return text
}

func (c *Client) doJSON(ctx context.Context, method, url string, body any, out any) error {
	raw, err := c.doRaw(ctx, method, url, body)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func (c *Client) doRaw(ctx context.Context, method, endpoint string, body any) ([]byte, error) {
	return httptransport.DoJSONRaw(ctx, c.httpClient, method, endpoint, c.cfg.APIKey, c.headers, body, "omniroute "+endpoint)
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
