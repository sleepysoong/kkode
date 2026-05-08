package httptransport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewJSONRequestAppliesSharedHeaders(t *testing.T) {
	req, payload, err := NewJSONRequest(context.Background(), http.MethodPost, "http://example.test/v1/responses", "sk-test", map[string]string{"X-Test": "yes"}, map[string]any{"model": "gpt"}, "text/event-stream")
	if err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "Bearer sk-test" || req.Header.Get("X-Test") != "yes" || req.Header.Get("Accept") != "text/event-stream" || req.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("request header가 이상해요: %+v", req.Header)
	}
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "gpt" {
		t.Fatalf("payload가 이상해요: %s", string(payload))
	}
}

func TestDoJSONRawReturnsErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" || r.Header.Get("X-Test") != "yes" {
			t.Fatalf("headers = %+v", r.Header)
		}
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer server.Close()
	_, err := DoJSONRaw(context.Background(), server.Client(), http.MethodPost, server.URL, "k", map[string]string{"X-Test": "yes"}, map[string]any{"ok": true}, "probe")
	if err == nil || err.Error() != "probe returned 502 Bad Gateway: nope\n" {
		t.Fatalf("error body가 필요해요: %v", err)
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadGateway || httpErr.Body != "nope\n" {
		t.Fatalf("공통 HTTP 오류 분류가 필요해요: %#v", err)
	}
}

func TestDoWithRetryRetriesServerErrors(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			http.Error(w, "try again", http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`ok`))
	}))
	defer server.Close()
	req, payload, err := NewJSONRequest(context.Background(), http.MethodPost, server.URL, "", nil, map[string]any{"model": "gpt"}, "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := DoWithRetry(server.Client(), req, payload, RetryConfig{MaxRetries: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if attempts != 2 || res.StatusCode != http.StatusOK {
		t.Fatalf("retry 결과가 이상해요: attempts=%d status=%d", attempts, res.StatusCode)
	}
}

func TestDrainAndCloseRetryBodyIsBounded(t *testing.T) {
	body := &trackingReadCloser{Reader: strings.NewReader("abcdef")}
	drainAndCloseRetryBody(body, 3)
	if !body.closed {
		t.Fatal("retry body는 닫아야 해요")
	}
	if body.readBytes != 4 {
		t.Fatalf("retry body drain은 max+1까지만 읽어야 해요: %d", body.readBytes)
	}
}

func TestDoWithRetryReturnsFinalRetryableResponse(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, "still down", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	req, payload, err := NewJSONRequest(context.Background(), http.MethodPost, server.URL, "", nil, map[string]any{"model": "gpt"}, "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := DoWithRetry(server.Client(), req, payload, RetryConfig{MaxRetries: 1, MinBackoff: time.Nanosecond, MaxBackoff: time.Nanosecond})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if attempts != 2 || res.StatusCode != http.StatusServiceUnavailable || string(data) != "still down\n" {
		t.Fatalf("마지막 retryable 응답을 호출자가 해석해야 해요: attempts=%d status=%d body=%q", attempts, res.StatusCode, string(data))
	}
}

type trackingReadCloser struct {
	*strings.Reader
	closed    bool
	readBytes int
}

func (r *trackingReadCloser) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.readBytes += n
	return n, err
}

func (r *trackingReadCloser) Close() error {
	r.closed = true
	return nil
}

func TestRetryBackoffHonorsRetryAfter(t *testing.T) {
	retry := NormalizeRetry(RetryConfig{MinBackoff: time.Second, MaxBackoff: 2 * time.Second})
	if got := retryBackoff(retry, 1, "1"); got != time.Second {
		t.Fatalf("Retry-After 초 단위를 따라야 해요: %s", got)
	}
	if got := retryBackoff(retry, 1, "5"); got != 2*time.Second {
		t.Fatalf("Retry-After는 MaxBackoff로 제한돼야 해요: %s", got)
	}
	if got := retryBackoff(retry, 2, "bad"); got != 2*time.Second {
		t.Fatalf("잘못된 Retry-After는 exponential backoff로 돌아가야 해요: %s", got)
	}
}

func TestReadSSEFramesEvents(t *testing.T) {
	input := strings.NewReader(": keepalive\n" +
		"event: response.output_text.delta\n" +
		"data: {\"delta\":\"hel\"}\n" +
		"data: {\"delta\":\"lo\"}\n\n" +
		"data: [DONE]\n\n")
	var events []string
	var payloads []string
	err := ReadSSE(context.Background(), input, func(eventName string, data []byte) bool {
		events = append(events, eventName)
		payloads = append(payloads, string(data))
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0] != "response.output_text.delta" || payloads[0] != "{\"delta\":\"hel\"}\n{\"delta\":\"lo\"}" {
		t.Fatalf("SSE framing이 이상해요: events=%v payloads=%v", events, payloads)
	}
}

func TestReadSSERejectsOversizedMultiLineEvent(t *testing.T) {
	var input strings.Builder
	chunk := strings.Repeat("x", 64*1024)
	for input.Len() <= MaxSSEEventBytes+len(chunk) {
		input.WriteString("data: ")
		input.WriteString(chunk)
		input.WriteString("\n")
	}
	input.WriteString("\n")
	err := ReadSSE(context.Background(), strings.NewReader(input.String()), func(eventName string, data []byte) bool {
		t.Fatal("oversized SSE event should not be delivered")
		return true
	})
	if err == nil || !strings.Contains(err.Error(), "SSE event data") {
		t.Fatalf("oversized SSE event should fail before delivery: %v", err)
	}
}
