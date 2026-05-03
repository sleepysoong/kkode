package httptransport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RetryConfig는 provider HTTP 호출의 공통 retry/backoff 정책이에요.
type RetryConfig struct {
	MaxRetries int
	MinBackoff time.Duration
	MaxBackoff time.Duration
}

// HTTPError는 OpenAI-compatible provider들이 HTTP 실패를 같은 방식으로 분류하게 해요.
type HTTPError struct {
	Label      string
	Status     string
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	label := strings.TrimSpace(e.Label)
	if label == "" {
		label = "provider request"
	}
	if e.Body == "" {
		return fmt.Sprintf("%s returned %s", label, e.Status)
	}
	return fmt.Sprintf("%s returned %s: %s", label, e.Status, e.Body)
}

// NormalizeRetry는 0값 config를 provider 기본 retry 정책으로 채워요.
func NormalizeRetry(retry RetryConfig) RetryConfig {
	if retry.MaxRetries == 0 {
		retry.MaxRetries = 2
	}
	if retry.MinBackoff == 0 {
		retry.MinBackoff = 250 * time.Millisecond
	}
	if retry.MaxBackoff == 0 {
		retry.MaxBackoff = 2 * time.Second
	}
	return retry
}

// IsSuccessStatus는 HTTP 2xx 응답만 provider 성공으로 취급해요.
func IsSuccessStatus(statusCode int) bool {
	return statusCode >= 200 && statusCode < 300
}

// IsRetryableStatus는 quota/backpressure와 일시적 서버 오류를 retry 대상으로 분류해요.
func IsRetryableStatus(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || statusCode >= 500
}

// ErrorFromResponse는 provider HTTP 실패를 errors.As로 검사할 수 있는 공통 오류로 만들어요.
func ErrorFromResponse(label string, res *http.Response, body []byte) error {
	if res == nil {
		return &HTTPError{Label: label, Status: "no response"}
	}
	return &HTTPError{
		Label:      label,
		Status:     res.Status,
		StatusCode: res.StatusCode,
		Body:       string(body),
	}
}

// DoWithRetry는 retry 가능한 HTTP status와 transport 오류를 같은 backoff 정책으로 재시도해요.
func DoWithRetry(client *http.Client, req *http.Request, payload []byte, retry RetryConfig) (*http.Response, error) {
	retry = NormalizeRetry(retry)
	attempts := retry.MaxRetries + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	var retryAfter string
	for i := 0; i < attempts; i++ {
		if i > 0 {
			backoff := retryBackoff(retry, i, retryAfter)
			retryAfter = ""
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(backoff):
			}
		}
		clone := req.Clone(req.Context())
		clone.Body = io.NopCloser(bytes.NewReader(payload))
		res, err := DefaultClient(client).Do(clone)
		if err != nil {
			lastErr = err
			continue
		}
		if IsRetryableStatus(res.StatusCode) && i < attempts-1 {
			lastErr = fmt.Errorf("retryable status: %s", res.Status)
			retryAfter = res.Header.Get("Retry-After")
			_, _ = io.Copy(io.Discard, res.Body)
			_ = res.Body.Close()
			continue
		}
		return res, nil
	}
	return nil, lastErr
}

func retryBackoff(retry RetryConfig, attempt int, retryAfter string) time.Duration {
	if delay, ok := parseRetryAfter(retryAfter); ok {
		if delay > retry.MaxBackoff {
			return retry.MaxBackoff
		}
		if delay < 0 {
			return 0
		}
		return delay
	}
	backoff := retry.MinBackoff << (attempt - 1)
	if backoff > retry.MaxBackoff {
		return retry.MaxBackoff
	}
	return backoff
}

func parseRetryAfter(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := time.ParseDuration(value + "s"); err == nil {
		return seconds, true
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	return time.Until(when), true
}

// DefaultClient는 provider들이 공유하는 장시간 LLM 호출용 HTTP client 기본값을 돌려줘요.
func DefaultClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: 120 * time.Second}
}

// CloneHeaders는 provider config header를 복사해서 파생 provider가 안전하게 확장하게 해요.
func CloneHeaders(headers map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range headers {
		out[k] = v
	}
	return out
}

// NewJSONRequest는 bearer auth, custom header, JSON body, optional Accept header를 한 번에 적용해요.
func NewJSONRequest(ctx context.Context, method string, endpoint string, apiKey string, headers map[string]string, body any, accept string) (*http.Request, []byte, error) {
	var payload []byte
	var reader io.Reader
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req, payload, nil
}

// DoJSONRaw는 JSON request를 보내고 성공 body를 그대로 돌려줘요.
func DoJSONRaw(ctx context.Context, client *http.Client, method string, endpoint string, apiKey string, headers map[string]string, body any, errorLabel string) ([]byte, error) {
	req, _, err := NewJSONRequest(ctx, method, endpoint, apiKey, headers, body, "")
	if err != nil {
		return nil, err
	}
	res, err := DefaultClient(client).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		if errorLabel == "" {
			errorLabel = endpoint
		}
		return nil, ErrorFromResponse(errorLabel, res, data)
	}
	return data, nil
}

// ReadSSE는 text/event-stream line framing을 읽고 event/data 묶음마다 handle을 호출해요.
// handle이 false를 반환하거나 [DONE]을 만나면 읽기를 멈춰요.
func ReadSSE(ctx context.Context, reader io.Reader, handle func(eventName string, data []byte) bool) error {
	s := bufio.NewScanner(reader)
	// 큰 JSON event도 처리할 수 있게 buffer를 넉넉하게 잡아요.
	s.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var eventName string
	var dataLines []string
	flush := func() bool {
		if len(dataLines) == 0 {
			eventName = ""
			return true
		}
		data := strings.Join(dataLines, "\n")
		dataLines = nil
		if data == "[DONE]" {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		default:
		}
		if handle != nil && !handle(eventName, []byte(data)) {
			return false
		}
		eventName = ""
		return true
	}
	for s.Scan() {
		line := s.Text()
		if line == "" {
			if !flush() {
				return nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			continue
		}
	}
	if err := s.Err(); err != nil {
		return err
	}
	flush()
	return nil
}
