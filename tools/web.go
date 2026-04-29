package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sleepysoong/kkode/llm"
)

type WebConfig struct {
	HTTPClient *http.Client
	UserAgent  string
	MaxBytes   int64
	Timeout    time.Duration
}

type WebFetchResult struct {
	URL         string      `json:"url"`
	Status      string      `json:"status"`
	StatusCode  int         `json:"status_code"`
	ContentType string      `json:"content_type"`
	Body        string      `json:"body"`
	Truncated   bool        `json:"truncated"`
	FetchedAt   time.Time   `json:"fetched_at"`
	Header      http.Header `json:"header,omitempty"`
}

func WebTools(cfg WebConfig) ([]llm.Tool, llm.ToolRegistry) {
	strict := true
	defs := []llm.Tool{
		{Kind: llm.ToolFunction, Name: "web_fetch", Description: "HTTP/HTTPS URL을 가져와 text body와 status를 JSON으로 반환해요", Strict: &strict, Parameters: objectSchemaRequired(map[string]any{"url": stringSchema(), "max_bytes": integerSchema(), "timeout_ms": integerSchema()}, []string{"url"})},
	}
	handlers := llm.ToolRegistry{
		"web_fetch": llm.JSONToolHandler(func(ctx context.Context, in struct {
			URL       string `json:"url"`
			MaxBytes  int64  `json:"max_bytes"`
			TimeoutMS int    `json:"timeout_ms"`
		}) (string, error) {
			res, err := Fetch(ctx, cfg, in.URL, in.MaxBytes, time.Duration(in.TimeoutMS)*time.Millisecond)
			if err != nil {
				return "", err
			}
			b, _ := json.MarshalIndent(res, "", "  ")
			return string(b), nil
		}),
	}
	return defs, handlers
}

func Fetch(ctx context.Context, cfg WebConfig, rawURL string, maxBytes int64, timeout time.Duration) (*WebFetchResult, error) {
	if rawURL == "" {
		return nil, fmt.Errorf("url is required")
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return nil, fmt.Errorf("only http/https URLs are supported: %s", rawURL)
	}
	if maxBytes <= 0 {
		maxBytes = cfg.MaxBytes
	}
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	if timeout <= 0 {
		timeout = cfg.Timeout
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	ua := cfg.UserAgent
	if ua == "" {
		ua = "kkode-agent/0.1"
	}
	req.Header.Set("User-Agent", ua)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	truncated := int64(len(data)) > maxBytes
	if truncated {
		data = data[:maxBytes]
	}
	return &WebFetchResult{URL: rawURL, Status: resp.Status, StatusCode: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: string(data), Truncated: truncated, FetchedAt: time.Now().UTC(), Header: resp.Header}, nil
}
