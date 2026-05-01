package omniroute

import (
	"context"
	"strings"
)

// NewFromOpenAPIServer는 docs/openapi.yaml에서 쓰는 server root로 client를 만들어요.
// 예: http://localhost:20128 -> proxy base http://localhost:20128/api/v1.
func NewFromOpenAPIServer(serverRoot string, cfg Config) *Client {
	root := strings.TrimRight(serverRoot, "/")
	cfg.BaseURL = root + "/api/v1"
	cfg.AdminBaseURL = root
	return New(cfg)
}

// NewFromGatewayBase는 OmniRoute user guide의 사용자용 gateway root로 client를 만들어요.
// 예: http://localhost:20128 -> http://localhost:20128/v1.
func NewFromGatewayBase(serverRoot string, cfg Config) *Client {
	root := strings.TrimRight(serverRoot, "/")
	cfg.BaseURL = root + "/v1"
	cfg.AdminBaseURL = root
	return New(cfg)
}

type TranslateRequest struct {
	Step         string         `json:"step,omitempty"`
	SourceFormat string         `json:"sourceFormat"`
	TargetFormat string         `json:"targetFormat"`
	Provider     string         `json:"provider,omitempty"`
	Body         map[string]any `json:"body"`
}

func (c *Client) Translate(ctx context.Context, req TranslateRequest) (map[string]any, error) {
	var out map[string]any
	if err := c.doJSON(ctx, "POST", c.adminBaseURL+"/api/translator/translate", req, &out); err != nil {
		return nil, err
	}
	return out, nil
}

type ThinkingBudget struct {
	Mode         string         `json:"mode,omitempty"`
	CustomBudget int            `json:"customBudget,omitempty"`
	EffortLevel  string         `json:"effortLevel,omitempty"`
	Raw          map[string]any `json:"-"`
}

func (c *Client) GetThinkingBudget(ctx context.Context) (*ThinkingBudget, error) {
	var raw map[string]any
	if err := c.doJSON(ctx, "GET", c.adminBaseURL+"/api/settings/thinking-budget", nil, &raw); err != nil {
		return nil, err
	}
	return thinkingBudgetFrom(raw), nil
}

func (c *Client) UpdateThinkingBudget(ctx context.Context, budget ThinkingBudget) (*ThinkingBudget, error) {
	var raw map[string]any
	if err := c.doJSON(ctx, "PUT", c.adminBaseURL+"/api/settings/thinking-budget", budget, &raw); err != nil {
		return nil, err
	}
	return thinkingBudgetFrom(raw), nil
}

func thinkingBudgetFrom(raw map[string]any) *ThinkingBudget {
	b := &ThinkingBudget{Raw: raw}
	if v, _ := raw["mode"].(string); v != "" {
		b.Mode = v
	}
	if v, _ := raw["effortLevel"].(string); v != "" {
		b.EffortLevel = v
	}
	b.CustomBudget = intFrom(raw["customBudget"])
	return b
}

type FallbackChain struct {
	Provider string `json:"provider,omitempty"`
	Priority int    `json:"priority,omitempty"`
	Enabled  bool   `json:"enabled,omitempty"`
}

type CreateFallbackChainRequest struct {
	Model string          `json:"model"`
	Chain []FallbackChain `json:"chain"`
}

func (c *Client) ListFallbackChains(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	if err := c.doJSON(ctx, "GET", c.adminBaseURL+"/api/fallback/chains", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) CreateFallbackChain(ctx context.Context, req CreateFallbackChainRequest) (map[string]any, error) {
	var out map[string]any
	if err := c.doJSON(ctx, "POST", c.adminBaseURL+"/api/fallback/chains", req, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) DeleteFallbackChain(ctx context.Context, model string) (map[string]any, error) {
	var out map[string]any
	if err := c.doJSON(ctx, "DELETE", c.adminBaseURL+"/api/fallback/chains", map[string]any{"model": model}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) CacheStats(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	if err := c.doJSON(ctx, "GET", c.adminBaseURL+"/api/cache/stats", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) RateLimits(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	if err := c.doJSON(ctx, "GET", c.adminBaseURL+"/api/rate-limits", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) Sessions(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	if err := c.doJSON(ctx, "GET", c.adminBaseURL+"/api/sessions", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}
