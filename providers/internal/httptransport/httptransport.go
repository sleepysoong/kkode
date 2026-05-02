package httptransport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

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
		return nil, fmt.Errorf("%s returned %s: %s", errorLabel, res.Status, string(data))
	}
	return data, nil
}
